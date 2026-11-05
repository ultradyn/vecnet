//go:build linux

package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/hub"
)

func percentiles(samples []float64) map[string]float64 {
	a := append([]float64(nil), samples...)
	sort.Float64s(a)
	p := map[string]float64{}
	if len(a) == 0 {
		return p
	}
	for _, v := range []int{50, 95, 99} {
		p[fmt.Sprintf("p%d_ms", v)] = a[max(0, int(math.Ceil(float64(len(a))*float64(v)/100))-1)]
	}
	return p
}
func (c *client) send(p peer, key, text string, to []string) (hub.SendResponse, error) {
	var out hub.SendResponse
	// Count only fresh 202s; message-row deltas independently verify acceptance.
	e := c.callStatus(p.Secret, "POST", "/v1/messages", map[string]any{"idempotency_key": key, "priority": "hold", "recipients": to, "content": map[string]string{"text": text}}, &out, http.StatusAccepted)
	return out, e
}

type sendRecord struct {
	response hub.SendResponse
	latency  float64
}
type pollResult struct {
	Events []envelope.Event `json:"events"`
	Next   string           `json:"next_cursor"`
}

func measureSends(c *client, size, fanout, workers int, duration time.Duration, minimum int) (any, error) {
	sender, e := c.peer("bench-sender")
	if e != nil {
		return nil, e
	}
	var peers []peer
	var to []string
	for i := 0; i < fanout; i++ {
		p, e := c.peer(fmt.Sprintf("bench-recipient-%d", i))
		if e != nil {
			return nil, e
		}
		peers = append(peers, p)
		to = append(to, p.ID)
	}
	before, e := c.storage()
	if e != nil {
		return nil, e
	}
	s0, e := c.snapshot()
	if e != nil {
		return nil, e
	}
	start := time.Now()
	var sequence atomic.Int64
	var accepted atomic.Int64
	var wg sync.WaitGroup
	var mu sync.Mutex
	var records []sendRecord
	var failedLatency []float64
	failedStatuses := map[int]int{}
	errs := make(chan error, workers)
	for i := 0; i < workers; i++ {
		wg.Go(func() {
			for {
				if accepted.Load() >= int64(minimum) && time.Since(start) >= duration {
					return
				}
				if time.Since(start) > max(2*time.Minute, 10*duration) {
					errs <- fmt.Errorf("send scenario did not reach its accepted-sample target within run budget")
					return
				}
				n := sequence.Add(1)
				prefix := fmt.Sprintf("%020d:", n)
				text := prefix + strings.Repeat("x", max(0, size-len(prefix)))
				t := time.Now()
				r, e := c.send(sender, fmt.Sprintf("bench-%d", n), text, to)
				lat := float64(time.Since(t)) / float64(time.Millisecond)
				if e != nil {
					var status *httpFailure
					if errors.As(e, &status) && (status.Status >= 500 || status.Status == 429) {
						mu.Lock()
						failedStatuses[status.Status]++
						failedLatency = append(failedLatency, lat)
						mu.Unlock()
						continue
					}
					errs <- e
					return
				}
				if r.MessageID == "" || len(r.Deliveries) != fanout {
					errs <- fmt.Errorf("unexpected send result")
					return
				}
				mu.Lock()
				records = append(records, sendRecord{r, lat})
				mu.Unlock()
				accepted.Add(1)
			}
		})
	}
	wg.Wait()
	elapsed := time.Since(start).Seconds()
	close(errs)
	for e := range errs {
		if e != nil {
			return nil, e
		}
	}
	s1, e := c.snapshot()
	if e != nil {
		return nil, e
	}
	after, e := c.storage()
	if e != nil {
		return nil, e
	}
	delta := difference(before, after)
	sendCheckpoint, e := c.snapshot()
	if e != nil {
		return nil, e
	}
	if delta.Rows["messages"] != int64(len(records)) {
		return nil, fmt.Errorf("accepted sends do not match message rows")
	}
	var latency []float64
	spilled := 0
	for _, r := range records {
		latency = append(latency, r.latency)
		if r.response.Spilled {
			spilled++
		}
	}
	result := map[string]any{"text_bytes": size, "fanout": fanout, "accepted": len(records), "elapsed_seconds": elapsed, "accepted_per_second": float64(len(records)) / elapsed, "latency": percentiles(latency), "hub_cpu_seconds": s1.CPUSeconds - s0.CPUSeconds, "hub_cpu_ms_per_send": (s1.CPUSeconds - s0.CPUSeconds) * 1000 / float64(len(records)), "hub_rss_bytes": s1.RSS, "limits": s0, "storage": delta, "spilled": spilled}
	result["attempted"] = sequence.Load()
	result["failed_requests"] = len(failedLatency)
	result["failed_statuses"] = failedStatuses
	result["failed_latency"] = percentiles(failedLatency)
	// Drain actual recipient HTTP logs then ack once per recipient. Ack emits
	// one accepted receipt per delivered message, besides updating its cursor.
	ackStart := after
	for _, p := range peers {
		cursor := ""
		expected := map[string]bool{}
		for _, r := range records {
			for _, d := range r.response.Deliveries {
				if d.Recipient == p.ID {
					expected[d.DeliveryID] = true
				}
			}
		}
		seen := map[string]bool{}
		deadline := time.Now().Add(2 * time.Minute)
		for {
			if time.Now().After(deadline) {
				return result, fmt.Errorf("event drain timeout")
			}
			var poll pollResult
			path := "/v1/events?wait=0"
			if cursor != "" {
				path += "&after=" + cursor
			}
			if e = c.call(p.Secret, "GET", path, nil, &poll); e != nil {
				return result, e
			}
			for _, ev := range poll.Events {
				if ev.Type == "delivery" {
					var delivery envelope.Delivery
					if json.Unmarshal(ev.Data, &delivery) != nil || !expected[delivery.DeliveryID] || seen[delivery.DeliveryID] {
						return result, fmt.Errorf("unexpected or duplicate delivery")
					}
					seen[delivery.DeliveryID] = true
				}
			}
			if len(poll.Events) > 0 && cursor == poll.Next {
				return result, fmt.Errorf("event cursor failed to advance")
			}
			cursor = poll.Next
			if len(poll.Events) == 0 {
				break
			}
		}
		if len(seen) != len(expected) {
			return result, fmt.Errorf("recipient got %d deliveries, expected %d", len(seen), len(expected))
		}
		if e = c.call(p.Secret, "POST", "/v1/ack", map[string]string{"cursor": cursor}, nil); e != nil {
			return result, e
		}
	}
	ackEnd, e := c.storage()
	if e != nil {
		return result, e
	}
	ackCPU, e := c.snapshot()
	if e != nil {
		return result, e
	}
	result["ack"] = map[string]any{"requests": fanout, "accepted_deliveries": len(records) * fanout, "storage": difference(ackStart, ackEnd), "hub_cpu_seconds_including_poll_and_checkpoint": ackCPU.CPUSeconds - sendCheckpoint.CPUSeconds}
	credentials := map[string]string{}
	for _, p := range peers {
		credentials[p.ID] = p.Secret
	}
	for _, r := range records {
		for _, d := range r.response.Deliveries {
			if e = c.call(credentials[d.Recipient], "POST", "/v1/receipts", map[string]string{"delivery_id": d.DeliveryID, "state": "processed"}, nil); e != nil {
				return result, e
			}
		}
	}
	receiptEnd, e := c.storage()
	if e != nil {
		return result, e
	}
	receiptCPU, e := c.snapshot()
	if e != nil {
		return result, e
	}
	result["receipt"] = map[string]any{"requests": len(records) * fanout, "storage": difference(ackEnd, receiptEnd), "hub_cpu_seconds_including_checkpoint": receiptCPU.CPUSeconds - ackCPU.CPUSeconds}
	result["lifecycle_hub_cpu_ms_per_send"] = (receiptCPU.CPUSeconds - s0.CPUSeconds) * 1000 / float64(len(records))
	result["lifecycle_storage"] = difference(before, receiptEnd)
	// Explicit all-subscriber presence fan-out, alternating states (no dedupe).
	for _, p := range peers {
		if e = c.call(p.Secret, "PATCH", "/v1/peers/me", map[string]string{"subscribe_presence": "all"}, nil); e != nil {
			return result, e
		}
	}
	presenceStart, e := c.storage()
	if e != nil {
		return result, e
	}
	const presenceN = 100
	for i := 0; i < presenceN; i++ {
		state := "working"
		if i%2 == 1 {
			state = "blocked"
		}
		if e = c.call(sender.Secret, "POST", "/v1/presence", map[string]string{"state": state, "at": envelope.FormatTime(time.Now())}, nil); e != nil {
			return result, e
		}
	}
	presenceEnd, e := c.storage()
	if e != nil {
		return result, e
	}
	pd := difference(presenceStart, presenceEnd)
	result["presence"] = map[string]any{"requests": presenceN, "events": pd.Rows["events"], "storage": pd}
	return result, nil
}
func measureDelivery(c *client) (any, error) {
	sender, e := c.peer("delivery-sender")
	if e != nil {
		return nil, e
	}
	recipient, e := c.peer("delivery-recipient")
	if e != nil {
		return nil, e
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, e := http.NewRequestWithContext(ctx, "GET", c.base+"/v1/events?after=head", nil)
	if e != nil {
		return nil, e
	}
	req.Header.Set("Authorization", "Bearer "+recipient.Secret)
	req.Header.Set("Accept", "text/event-stream")
	resp, e := c.http.Do(req)
	if e != nil {
		return nil, fmt.Errorf("delivery stream connection failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("delivery stream HTTP %d", resp.StatusCode)
	}
	type arrival struct {
		ID, Cursor string
		At         time.Time
	}
	arrivals := make(chan arrival, 200)
	readErr := make(chan error, 1)
	go func() {
		scanner := bufio.NewScanner(resp.Body)
		scanner.Buffer(make([]byte, 4096), 1<<20)
		for scanner.Scan() {
			line := scanner.Text()
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			raw := strings.TrimPrefix(line, "data: ")
			if raw == "" {
				continue
			}
			var ev envelope.Event
			if json.Unmarshal([]byte(raw), &ev) != nil || ev.Type != "delivery" {
				continue
			}
			var d envelope.Delivery
			if json.Unmarshal(ev.Data, &d) != nil {
				continue
			}
			select {
			case arrivals <- arrival{d.Message.MessageID, ev.Cursor, time.Now()}:
			case <-ctx.Done():
				return
			}
		}
		err := scanner.Err()
		if err == nil {
			err = io.EOF
		}
		readErr <- err
	}()
	const n = 100
	var latencies []float64
	cursor := ""
	for i := 0; i < n; i++ {
		t := time.Now()
		sr, e := c.send(sender, fmt.Sprintf("delivery-%d", i), strings.Repeat("x", 200), []string{recipient.ID})
		if e != nil {
			return nil, e
		}
		select {
		case a := <-arrivals:
			if a.ID != sr.MessageID {
				return nil, fmt.Errorf("delivery correlation mismatch")
			}
			latencies = append(latencies, float64(a.At.Sub(t))/float64(time.Millisecond))
			cursor = a.Cursor
		case <-readErr:
			return nil, fmt.Errorf("delivery stream closed")
		case <-time.After(10 * time.Second):
			return nil, fmt.Errorf("delivery timeout")
		}
		if pause := 100*time.Millisecond - time.Since(t); pause > 0 {
			time.Sleep(pause)
		}
	}
	if e = c.call(recipient.Secret, "POST", "/v1/ack", map[string]string{"cursor": cursor}, nil); e != nil {
		return nil, e
	}
	return map[string]any{"samples": n, "target_sends_per_second": 10, "text_bytes": 200, "fanout": 1, "latency": percentiles(latencies), "definition": "client before send JSON encoding to recipient SSE frame parsed, including HTTP and commit; loopback, real-time 10/s"}, nil
}
