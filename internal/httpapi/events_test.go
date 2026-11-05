package httpapi_test

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/eventlog"
	"github.com/XertroV/vecnet/internal/httpapi"
	"github.com/XertroV/vecnet/internal/hub"
	"github.com/XertroV/vecnet/internal/testhub"
)

// pollResp is the long-poll body of GET /v1/events.
type pollResp struct {
	Events     []envelope.Event `json:"events"`
	NextCursor string           `json:"next_cursor"`
}

// getEvents runs one GET /v1/events with Accept application/json and the
// given extra headers, returning the response, body and elapsed time.
func getEvents(t *testing.T, env *testhub.Env, secret, query string, hdr map[string]string) (*http.Response, []byte, time.Duration) {
	t.Helper()
	req, err := http.NewRequest("GET", env.URL+"/v1/events"+query, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+secret)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	start := time.Now()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Errorf("GET /v1/events%s: %v", query, err)
		return nil, nil, 0
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Errorf("read /v1/events%s: %v", query, err)
	}
	return resp, body, time.Since(start)
}

func mustPoll(t *testing.T, env *testhub.Env, secret, query string) pollResp {
	t.Helper()
	resp, body, _ := getEvents(t, env, secret, query, nil)
	if resp == nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /v1/events%s = %v; %s", query, resp, body)
	}
	var pr pollResp
	if err := json.Unmarshal(body, &pr); err != nil {
		t.Fatalf("decode poll: %v; %s", err, body)
	}
	return pr
}

// zeroCursor is e<epoch>.0: reading after it yields the whole log.
func zeroCursor(env *testhub.Env) string {
	return eventlog.Cursor{Epoch: env.Hub.Log.Epoch()}.String()
}

func recipientOf(t *testing.T, ev envelope.Event) string {
	t.Helper()
	var d struct {
		Recipient string `json:"recipient"`
	}
	if err := json.Unmarshal(ev.Data, &d); err != nil {
		t.Fatalf("decode delivery: %v", err)
	}
	return d.Recipient
}

// sseFrame is one parsed SSE frame (fields up to a blank line).
type sseFrame struct {
	ID, Event, Data, Retry, Comment string
	HasID, HasData                  bool
}

// sseStream reads frames from an SSE response in the background.
type sseStream struct {
	resp   *http.Response
	frames chan sseFrame
	cancel context.CancelFunc
}

// openSSE connects to /v1/events with Accept text/event-stream and fails
// unless the answer is 200.
func openSSE(t *testing.T, env *testhub.Env, secret, query string, hdr map[string]string) *sseStream {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, "GET", env.URL+"/v1/events"+query, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Authorization", "Bearer "+secret)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		cancel()
		t.Fatalf("open SSE: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		cancel()
		t.Fatalf("open SSE = %d; %s", resp.StatusCode, b)
	}
	s := &sseStream{resp: resp, frames: make(chan sseFrame, 1024), cancel: cancel}
	t.Cleanup(func() { cancel(); resp.Body.Close() })
	return s
}

// start begins parsing frames in the background.
func (s *sseStream) start() *sseStream {
	go func() {
		defer close(s.frames)
		br := bufio.NewReader(s.resp.Body)
		var f sseFrame
		empty := true
		for {
			line, err := br.ReadString('\n')
			if err != nil {
				return
			}
			line = strings.TrimRight(line, "\r\n")
			if line == "" {
				if !empty {
					s.frames <- f
				}
				f, empty = sseFrame{}, true
				continue
			}
			empty = false
			name, val, _ := strings.Cut(line, ":")
			val = strings.TrimPrefix(val, " ")
			switch name {
			case "":
				f.Comment = val
			case "id":
				f.ID, f.HasID = val, true
			case "event":
				f.Event = val
			case "data":
				f.Data, f.HasData = val, true
			case "retry":
				f.Retry = val
			}
		}
	}()
	return s
}

// next returns the next frame, failing after d.
func (s *sseStream) next(t *testing.T, d time.Duration) sseFrame {
	t.Helper()
	select {
	case f, ok := <-s.frames:
		if !ok {
			t.Fatal("SSE stream ended")
		}
		return f
	case <-time.After(d):
		t.Fatalf("no SSE frame within %v", d)
	}
	return sseFrame{}
}

// nextEvent returns the next non-comment frame, failing after d.
func (s *sseStream) nextEvent(t *testing.T, d time.Duration) sseFrame {
	t.Helper()
	deadline := time.Now().Add(d)
	for {
		f := s.next(t, time.Until(deadline))
		if f.Comment == "" {
			return f
		}
	}
}

// expectPreamble checks the two opening frames: retry and the priming id.
func (s *sseStream) expectPreamble(t *testing.T, wantID string) {
	t.Helper()
	f := s.next(t, 10*time.Second)
	if f.Retry != "3000" || f.HasID || f.HasData {
		t.Fatalf("first frame = %+v, want only retry: 3000", f)
	}
	f = s.next(t, 10*time.Second)
	if !f.HasID || !f.HasData || f.Data != "" || f.Event != "" {
		t.Fatalf("second frame = %+v, want priming id with empty data", f)
	}
	if wantID != "" && f.ID != wantID {
		t.Fatalf("priming id = %q, want %q", f.ID, wantID)
	}
}

func TestLongPollReturnsImmediatelyAndThenWaits(t *testing.T) {
	env := testhub.Start(t)
	_, aSec := env.NewPeer("grokbot", "a")
	b, bSec := env.NewPeer("session", "b")
	sr := postSend(t, env, aSec, sendBody("lp-1", b), http.StatusAccepted)

	resp, body, took := getEvents(t, env, bSec, "?after="+zeroCursor(env)+"&wait=20", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET = %d; %s", resp.StatusCode, body)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("Content-Type = %q", ct)
	}
	var pr pollResp
	if err := json.Unmarshal(body, &pr); err != nil {
		t.Fatal(err)
	}
	if len(pr.Events) != 1 || pr.Events[0].Type != "delivery" {
		t.Fatalf("events = %+v, want one delivery", pr.Events)
	}
	if pr.NextCursor == "" || pr.NextCursor != pr.Events[0].Cursor {
		t.Fatalf("next_cursor = %q, want the event's cursor %q", pr.NextCursor, pr.Events[0].Cursor)
	}
	if took >= 10*time.Second {
		t.Fatalf("long-poll with an event pending took %v; it must return at once", took)
	}
	var d struct {
		DeliveryID string `json:"delivery_id"`
	}
	if err := json.Unmarshal(pr.Events[0].Data, &d); err != nil || d.DeliveryID != sr.Deliveries[0].DeliveryID {
		t.Fatalf("delivery_id = %q, want %q (%v)", d.DeliveryID, sr.Deliveries[0].DeliveryID, err)
	}

	resp, body, took = getEvents(t, env, bSec, "?after="+pr.NextCursor+"&wait=1", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("second GET = %d; %s", resp.StatusCode, body)
	}
	var pr2 pollResp
	if err := json.Unmarshal(body, &pr2); err != nil {
		t.Fatal(err)
	}
	if pr2.Events == nil || len(pr2.Events) != 0 {
		t.Fatalf("events = %s, want []", body)
	}
	if pr2.NextCursor != pr.NextCursor {
		t.Fatalf("empty poll next_cursor = %q, want the requested %q", pr2.NextCursor, pr.NextCursor)
	}
	if took < time.Second {
		t.Fatalf("empty long-poll with wait=1 returned after %v, want >= 1s", took)
	}
}

// TestNeverAckedStartsAtOldest: with no Last-Event-ID, no ?after= and nothing
// acked, a stream starts at the oldest retained event, so a first read sees
// what arrived before it; once acked, the shared acked cursor is used, and
// after=head still means "from now" (PROTOCOL §2.3).
func TestNeverAckedStartsAtOldest(t *testing.T) {
	env := testhub.Start(t)
	_, aSec := env.NewPeer("grokbot", "a")
	b, bSec := env.NewPeer("session", "b")
	sr := postSend(t, env, aSec, sendBody("first-read", b), http.StatusAccepted)

	pr := mustPoll(t, env, bSec, "?wait=0")
	if len(pr.Events) != 1 || pr.Events[0].Type != "delivery" || recipientOf(t, pr.Events[0]) != b {
		t.Fatalf("never-acked poll = %+v, want the delivery sent before the first read", pr.Events)
	}
	if head := mustPoll(t, env, bSec, "?after=head&wait=0"); len(head.Events) != 0 {
		t.Fatalf("after=head = %+v, want none", head.Events)
	}
	resp, body := env.Do(bSec, "POST", "/v1/ack", map[string]string{"cursor": pr.Events[0].Cursor})
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("ack = %d; %s", resp.StatusCode, body)
	}
	if again := mustPoll(t, env, bSec, "?wait=0"); len(again.Events) != 0 || again.NextCursor != pr.Events[0].Cursor {
		t.Fatalf("poll after ack = %+v next %q, want none from the acked cursor %q (delivery %s)",
			again.Events, again.NextCursor, pr.Events[0].Cursor, sr.Deliveries[0].DeliveryID)
	}
}

func TestLongPollWakesOnSend(t *testing.T) {
	env := testhub.Start(t)
	_, aSec := env.NewPeer("grokbot", "a")
	b, bSec := env.NewPeer("session", "b")
	cur, err := env.Hub.Log.Current(context.Background(), []string{b})
	if err != nil {
		t.Fatal(err)
	}

	type result struct {
		resp *http.Response
		body []byte
		err  error
		at   time.Time
	}
	// The poll goroutine reports through done and never touches t, so it may
	// outlive a failed test; cleanup cancels its request.
	pctx, pcancel := context.WithCancel(context.Background())
	t.Cleanup(pcancel)
	done := make(chan result, 1)
	go func() {
		req, err := http.NewRequestWithContext(pctx, "GET", env.URL+"/v1/events?after="+cur.String()+"&wait=10", nil)
		if err != nil {
			done <- result{err: err}
			return
		}
		req.Header.Set("Accept", "application/json")
		req.Header.Set("Authorization", "Bearer "+bSec)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			done <- result{err: err}
			return
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		done <- result{resp, body, err, time.Now()}
	}()
	// Send only once the long-poll is parked on B's log, so the wake path
	// (not an immediate read) is what returns the Event.
	for deadline := time.Now().Add(10 * time.Second); env.Hub.Log.Waiters(b) == 0; {
		if time.Now().After(deadline) {
			t.Fatal("long-poll never parked on B's log")
		}
		time.Sleep(time.Millisecond)
	}
	postSend(t, env, aSec, sendBody("wake-1", b), http.StatusAccepted)
	sent := time.Now()

	select {
	case r := <-done:
		if r.err != nil {
			t.Fatalf("poll: %v", r.err)
		}
		if r.resp.StatusCode != http.StatusOK {
			t.Fatalf("poll = %d; %s", r.resp.StatusCode, r.body)
		}
		var pr pollResp
		if err := json.Unmarshal(r.body, &pr); err != nil {
			t.Fatal(err)
		}
		if len(pr.Events) != 1 || recipientOf(t, pr.Events[0]) != b {
			t.Fatalf("events = %s, want one delivery to b", r.body)
		}
		if lag := r.at.Sub(sent); lag > 2*time.Second {
			t.Fatalf("poll returned %v after the send, want within 2s", lag)
		}
	case <-time.After(8 * time.Second):
		t.Fatal("long-poll did not wake on send")
	}
}

func TestSSEFramesAndHeartbeat(t *testing.T) {
	env := testhub.Start(t)
	env.Configure(func(d *httpapi.Deps) { d.Heartbeat = 100 * time.Millisecond })
	_, aSec := env.NewPeer("grokbot", "a")
	b, bSec := env.NewPeer("session", "b")
	cur, err := env.Hub.Log.Oldest(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	s := openSSE(t, env, bSec, "", nil)
	h := s.resp.Header
	if got := h.Get("Content-Type"); !strings.HasPrefix(got, "text/event-stream") {
		t.Fatalf("Content-Type = %q", got)
	}
	if h.Get("Cache-Control") != "no-store" || h.Get("X-Accel-Buffering") != "no" || h.Get("Connection") != "keep-alive" {
		t.Fatalf("headers = %v", h)
	}
	s.start()
	s.expectPreamble(t, cur.String()) // no cursor, nothing acked → oldest retained

	postSend(t, env, aSec, sendBody("sse-1", b), http.StatusAccepted)
	f := s.nextEvent(t, 10*time.Second)
	if f.Event != "delivery" {
		t.Fatalf("frame = %+v, want event: delivery", f)
	}
	var ev envelope.Event
	if err := json.Unmarshal([]byte(f.Data), &ev); err != nil {
		t.Fatalf("decode data: %v; %q", err, f.Data)
	}
	if err := envelope.Validate("event", []byte(f.Data)); err != nil {
		t.Fatalf("event data is not a schema-valid Event: %v", err)
	}
	if f.ID == "" || f.ID != ev.Cursor || ev.Type != "delivery" || recipientOf(t, ev) != b {
		t.Fatalf("id %q, data.cursor %q, type %q", f.ID, ev.Cursor, ev.Type)
	}

	deadline := time.Now().Add(time.Second)
	for {
		f := s.next(t, time.Until(deadline))
		if f.Comment == "hb" {
			break
		}
	}
}

func TestSSEResumeFromLastEventID(t *testing.T) {
	env := testhub.Start(t)
	_, aSec := env.NewPeer("grokbot", "a")
	b, bSec := env.NewPeer("session", "b")
	postSend(t, env, aSec, sendBody("r-1", b), http.StatusAccepted)
	postSend(t, env, aSec, sendBody("r-2", b), http.StatusAccepted)
	evs, err := env.Hub.Log.Read(context.Background(), []string{b}, eventlog.Cursor{Epoch: env.Hub.Log.Epoch()}, 0)
	if err != nil || len(evs) != 2 {
		t.Fatalf("Read = %d, %v", len(evs), err)
	}

	s := openSSE(t, env, bSec, "", map[string]string{"Last-Event-ID": evs[0].Cursor}).start()
	s.expectPreamble(t, evs[0].Cursor)
	f := s.nextEvent(t, 10*time.Second)
	if f.ID != evs[1].Cursor || f.Event != "delivery" {
		t.Fatalf("first frame after priming = %+v, want id %s", f, evs[1].Cursor)
	}

	// ?after= alone resumes the same way, and an equal Last-Event-ID is fine.
	s2 := openSSE(t, env, bSec, "?after="+evs[0].Cursor, map[string]string{"Last-Event-ID": evs[0].Cursor}).start()
	s2.expectPreamble(t, evs[0].Cursor)
	if f := s2.nextEvent(t, 10*time.Second); f.ID != evs[1].Cursor {
		t.Fatalf("?after resume first frame id = %q, want %s", f.ID, evs[1].Cursor)
	}
}

func TestCursorMismatch400AndPurged409(t *testing.T) {
	env := testhub.Start(t)
	_, bSec := env.NewPeer("session", "b")

	resp, body, _ := getEvents(t, env, bSec, "?after=e1.2", map[string]string{"Last-Event-ID": "e1.1"})
	mustErr(t, resp, body, http.StatusBadRequest, "cursor_mismatch")

	for _, accept := range []string{"application/json", "text/event-stream"} {
		resp, body, _ = getEvents(t, env, bSec, "?after=e9.1", map[string]string{"Accept": accept})
		eb := mustErr(t, resp, body, http.StatusConflict, "cursor_purged")
		oldest, _ := eb.Error.Details["oldest_cursor"].(string)
		c, err := eventlog.ParseCursor(oldest)
		if err != nil || c.Epoch != env.Hub.Log.Epoch() {
			t.Fatalf("%s: details.oldest_cursor = %q (%v), want a cursor of epoch %d", accept, oldest, err, env.Hub.Log.Epoch())
		}
	}

	resp, body, _ = getEvents(t, env, bSec, "?after=nonsense", nil)
	mustErr(t, resp, body, http.StatusUnprocessableEntity, "invalid_value")
	resp, body, _ = getEvents(t, env, bSec, "?wait=soon", nil)
	mustErr(t, resp, body, http.StatusUnprocessableEntity, "invalid_value")
}

func TestPrincipalStreamAndNarrow(t *testing.T) {
	env := testhub.Start(t)
	ctx := context.Background()
	a, aSec := env.NewPeer("grokbot", "a")
	prn, pSec := env.NewPrincipal("p")
	var bc []string
	for _, n := range []string{"b", "c"} {
		id, _, err := env.Hub.AddPeer(ctx, "session", n, prn)
		if err != nil {
			t.Fatal(err)
		}
		bc = append(bc, id)
	}
	b, c := bc[0], bc[1]
	// Start after the setup: c's enrolment put a directory_updated Event in b's log.
	start, err := env.Hub.Log.Current(ctx, bc)
	if err != nil {
		t.Fatal(err)
	}
	postSend(t, env, aSec, sendBody("p-1", b, c), http.StatusAccepted)

	all := mustPoll(t, env, pSec, "?after="+start.String()+"&wait=0")
	if len(all.Events) != 2 {
		t.Fatalf("principal stream = %d events, want 2", len(all.Events))
	}
	got := map[string]bool{recipientOf(t, all.Events[0]): true, recipientOf(t, all.Events[1]): true}
	if !got[b] || !got[c] {
		t.Fatalf("principal stream recipients = %v, want %s and %s", got, b, c)
	}

	narrow := mustPoll(t, env, pSec, "?peer="+b+"&after="+start.String()+"&wait=0")
	if len(narrow.Events) != 1 || recipientOf(t, narrow.Events[0]) != b {
		t.Fatalf("?peer=b = %+v, want one delivery to b", narrow.Events)
	}

	resp, body, _ := getEvents(t, env, pSec, "?peer="+a+"&wait=0", nil)
	mustErr(t, resp, body, http.StatusForbidden, "not_your_peer")
	resp, body = env.Do(pSec, "POST", "/v1/ack?peer="+a, map[string]string{"cursor": zeroCursor(env)})
	mustErr(t, resp, body, http.StatusForbidden, "not_your_peer")
}

// stalledDeliveryWriter lets the real SSE handler prime the stream, then
// blocks its first Delivery write until the test releases it. Channels prove
// the writer is stalled without depending on TCP buffer sizes or send rate.
type stalledDeliveryWriter struct {
	*httptest.ResponseRecorder
	primed  chan struct{}
	blocked chan struct{}
	release chan struct{}
}

func (w *stalledDeliveryWriter) Write(p []byte) (int, error) {
	if strings.Contains(string(p), "\nevent: delivery\n") {
		close(w.blocked)
		<-w.release
		return 0, io.ErrClosedPipe
	}
	n, err := w.ResponseRecorder.Write(p)
	if strings.HasPrefix(string(p), "retry:") {
		close(w.primed)
	}
	return n, err
}

func TestSlowSSEConsumerDoesNotBlockSend(t *testing.T) {
	for _, mode := range []struct {
		name string
		refs bool
	}{{"inline", false}, {"reference", true}} {
		t.Run(mode.name, func(t *testing.T) {
			env := testhub.StartWith(t, func(cfg *hub.Config) {
				cfg.DeliveryReferences = mode.refs
			})
			a, aSec := env.NewPeer("grokbot", "a")
			b, bSec := env.NewPeer("session", "b")
			c, _ := env.NewPeer("dot", "c")
			env.SetLimits(a, 10000, 10000)

			// Serve the production handler against a controlled writer. Keep
			// stall/heartbeat timers out of the test: only cancellation or
			// releasing the writer should end this stream.
			handler := httpapi.New(httpapi.Deps{
				Hub: env.Hub, SSEStall: time.Hour, Heartbeat: time.Hour,
			})
			ctx, cancel := context.WithCancel(context.Background())
			req := httptest.NewRequest("GET", "/v1/events?after=head", nil).WithContext(ctx)
			req.Header.Set("Accept", "text/event-stream")
			req.Header.Set("Authorization", "Bearer "+bSec)
			w := &stalledDeliveryWriter{
				ResponseRecorder: httptest.NewRecorder(),
				primed:           make(chan struct{}), blocked: make(chan struct{}), release: make(chan struct{}),
			}
			done := make(chan struct{})
			go func() {
				defer close(done)
				handler.ServeHTTP(w, req)
			}()
			t.Cleanup(func() {
				cancel()
				close(w.release)
				select {
				case <-done:
				case <-time.After(10 * time.Second):
					t.Error("SSE handler did not exit after releasing its writer")
				}
			})
			await := func(ch <-chan struct{}, what string) {
				t.Helper()
				select {
				case <-ch:
				case <-done:
					t.Fatalf("SSE handler exited before %s", what)
				case <-time.After(10 * time.Second):
					t.Fatalf("SSE handler did not reach %s", what)
				}
			}
			await(w.primed, "priming frame")

			send := func(key, to string) {
				t.Helper()
				body := sendBody(key, to)
				body["content"] = map[string]any{"text": strings.Repeat("x", 4000)}
				j, err := json.Marshal(body)
				if err != nil {
					t.Fatal(err)
				}
				// This is a deadlock watchdog per request, not a shared
				// throughput budget that race instrumentation can exhaust.
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				req, err := http.NewRequestWithContext(ctx, "POST", env.URL+"/v1/messages", strings.NewReader(string(j)))
				if err != nil {
					t.Fatal(err)
				}
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("Authorization", "Bearer "+aSec)
				resp, err := http.DefaultClient.Do(req)
				if err != nil {
					t.Fatalf("send %s blocked while SSE writer was stalled: %v", key, err)
				}
				_, err = io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
				if err != nil {
					t.Fatalf("send %s: read response: %v", key, err)
				}
				if resp.StatusCode != http.StatusAccepted {
					t.Fatalf("send %s = %d, want %d", key, resp.StatusCode, http.StatusAccepted)
				}
			}
			send("seed", b)
			await(w.blocked, "stalled Delivery write")

			// Exceed the 64-frame SSE queue while its writer is known to be
			// blocked. Same-owner and unrelated sends must both commit even
			// when the log reader is backpressured by the full queue.
			for i := 0; i < 200; i++ {
				send(fmt.Sprintf("slow-%d", i), b)
			}
			send("free", c)
			select {
			case <-done:
				t.Fatal("SSE stream ended instead of remaining stalled during sends")
			default:
			}
		})
	}
}

// TestSSEReplaysLargeBacklogOnOneConnection: an actively reading client that
// resumes over a backlog far larger than the per-connection frame queue gets
// every event, in order, on one connection (PROTOCOL §2.3 replay; the queue
// applies backpressure to a reading client instead of dropping it).
func TestSSEReplaysLargeBacklogOnOneConnection(t *testing.T) {
	env := testhub.Start(t)
	a, aSec := env.NewPeer("grokbot", "a")
	b, bSec := env.NewPeer("session", "b")
	env.SetLimits(a, 10000, 10000)
	const n = 300
	for i := 0; i < n; i++ {
		body := sendBody(fmt.Sprintf("backlog-%d", i), b)
		body["content"] = map[string]any{"text": "0123456789"}
		resp, raw := env.Do(aSec, "POST", "/v1/messages", body)
		if resp.StatusCode != http.StatusAccepted {
			t.Fatalf("send %d = %d; %s", i, resp.StatusCode, raw)
		}
	}
	want, err := env.Hub.Log.Read(context.Background(), []string{b}, eventlog.Cursor{Epoch: env.Hub.Log.Epoch()}, 0)
	if err != nil || len(want) != n {
		t.Fatalf("Read = %d, %v; want %d", len(want), err, n)
	}

	s := openSSE(t, env, bSec, "?after="+zeroCursor(env), nil).start()
	s.expectPreamble(t, zeroCursor(env))
	for i := 0; i < n; i++ {
		f := s.nextEvent(t, 10*time.Second)
		if f.ID != want[i].Cursor || f.Event != "delivery" {
			t.Fatalf("frame %d = id %q event %q, want id %q delivery", i, f.ID, f.Event, want[i].Cursor)
		}
	}
	if got := httpapi.ActiveStreams(); got != 1 {
		t.Fatalf("active SSE streams after replay = %d, want 1 (connection kept)", got)
	}
}

// waitStreams waits until n SSE handlers are running.
func waitStreams(t *testing.T, n int64) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for httpapi.ActiveStreams() != n {
		if time.Now().After(deadline) {
			t.Fatalf("active SSE streams = %d, want %d", httpapi.ActiveStreams(), n)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestDisabledPeer204(t *testing.T) {
	env := testhub.Start(t)
	ctx := context.Background()
	b, bSec := env.NewPeer("session", "b")
	prn, pSec := env.NewPrincipal("p")
	c, _, err := env.Hub.AddPeer(ctx, "session", "c", prn)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := env.Hub.DB.Exec(`UPDATE peers SET disabled = 1 WHERE peer_id IN (?, ?)`, b, c); err != nil {
		t.Fatal(err)
	}
	for _, accept := range []string{"application/json", "text/event-stream"} {
		resp, body, _ := getEvents(t, env, bSec, "", map[string]string{"Accept": accept})
		if resp.StatusCode != http.StatusNoContent || len(body) != 0 {
			t.Fatalf("%s: disabled peer GET /v1/events = %d %q, want 204", accept, resp.StatusCode, body)
		}
	}
	resp, body, _ := getEvents(t, env, pSec, "?peer="+c, nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("principal ?peer=<disabled> = %d %s, want 204", resp.StatusCode, body)
	}
	// Other endpoints still treat the disabled peer's credential as 401.
	resp, body = env.Do(bSec, "POST", "/v1/ack", map[string]string{"cursor": zeroCursor(env)})
	mustErr(t, resp, body, http.StatusUnauthorized, "unauthenticated")
}

// TestStopEndsLongPollAndSSE: closing Deps.Stop (server shutdown) answers a
// parked long-poll at once with no events and ends an open SSE stream, so
// neither holds http.Server.Shutdown for its grace period.
func TestStopEndsLongPollAndSSE(t *testing.T) {
	env := testhub.Start(t)
	stop := make(chan struct{})
	env.Configure(func(d *httpapi.Deps) { d.Stop = stop })
	b, bSec := env.NewPeer("session", "b")

	s := openSSE(t, env, bSec, "", nil)
	defer s.cancel()
	waitStreams(t, 1)

	type result struct {
		resp *http.Response
		body []byte
	}
	done := make(chan result, 1)
	go func() {
		resp, body, _ := getEvents(t, env, bSec, "?after=head&wait=30", nil)
		done <- result{resp, body}
	}()
	for deadline := time.Now().Add(10 * time.Second); env.Hub.Log.Waiters(b) < 2; {
		if time.Now().After(deadline) {
			t.Fatal("long-poll never parked")
		}
		time.Sleep(time.Millisecond)
	}
	close(stop)
	select {
	case r := <-done:
		if r.resp == nil || r.resp.StatusCode != http.StatusOK {
			t.Fatalf("long-poll after stop = %v %s, want 200", r.resp, r.body)
		}
		var pr pollResp
		if err := json.Unmarshal(r.body, &pr); err != nil || len(pr.Events) != 0 || pr.NextCursor == "" {
			t.Fatalf("long-poll after stop body %s, want no events and a next_cursor", r.body)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("long-poll still parked 5 s after stop")
	}
	waitStreams(t, 0)
}

// wantStreamEnds reads s until it closes, failing on any event first: an open
// stream outlives the check that admitted it, and ends with what changed.
func wantStreamEnds(t *testing.T, s *sseStream) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case f, open := <-s.frames:
			if !open {
				return
			}
			if f.Event != "" {
				t.Fatalf("event on a stream whose credential or owners changed: %+v", f)
			}
		case <-deadline:
			t.Fatal("stream still open")
		}
	}
}

func TestSSEEndsOnRotation(t *testing.T) {
	ctx := context.Background()
	t.Run("peer", func(t *testing.T) {
		env := testhub.Start(t)
		_, aSec := env.NewPeer("grokbot", "a")
		b, bSec := env.NewPeer("session", "b")
		s := openSSE(t, env, bSec, "", nil).start()
		cur, _ := env.Hub.Log.Oldest(ctx)
		s.expectPreamble(t, cur.String())
		if _, err := env.Hub.RotatePeer(ctx, b); err != nil {
			t.Fatal(err)
		}
		postSend(t, env, aSec, sendBody("after-rotate", b), http.StatusAccepted)
		wantStreamEnds(t, s)
	})
	t.Run("an idle stream wakes for it", func(t *testing.T) {
		env := testhub.Start(t)
		b, bSec := env.NewPeer("session", "b")
		s := openSSE(t, env, bSec, "", nil).start()
		cur, _ := env.Hub.Log.Oldest(ctx)
		s.expectPreamble(t, cur.String())
		if _, err := env.Hub.RotatePeer(ctx, b); err != nil {
			t.Fatal(err)
		}
		wantStreamEnds(t, s)
	})
	t.Run("principal", func(t *testing.T) {
		env := testhub.Start(t)
		prn, secret := env.NewPrincipal("pc")
		adminAddPeer(t, env, map[string]any{"kind": "session", "name": "b", "principal_id": prn})
		s := openSSE(t, env, secret, "", nil).start()
		cur, _ := env.Hub.Log.Oldest(ctx)
		s.expectPreamble(t, cur.String())
		if _, err := env.Hub.RotatePrincipal(ctx, prn); err != nil {
			t.Fatal(err)
		}
		wantStreamEnds(t, s)
	})
}

// With no wake at all, the heartbeat finds the rotation.
func TestSSECredentialCheckedOnHeartbeat(t *testing.T) {
	env := testhub.Start(t)
	env.Configure(func(d *httpapi.Deps) { d.Heartbeat = 20 * time.Millisecond })
	b, bSec := env.NewPeer("session", "b")
	s := openSSE(t, env, bSec, "", nil).start()
	if _, err := env.Hub.Auth.Rotate(context.Background(), "peer", b); err != nil { // no Notify
		t.Fatal(err)
	}
	wantStreamEnds(t, s)
}

// The old principal's stream ends when its peer moves away: it no longer reads
// that log. The peer's own stream carries on, since its owners are the same.
func TestSSEEndsForOldPrincipalOnMove(t *testing.T) {
	env := testhub.Start(t)
	ctx := context.Background()
	_, aSec := env.NewPeer("grokbot", "a")
	prn, secret := env.NewPrincipal("old")
	newPrn, _ := env.NewPrincipal("new")
	added := adminAddPeer(t, env, map[string]any{"kind": "session", "name": "b", "principal_id": prn})
	b := added.PeerID
	got := enrol(t, env, added.EnrolToken)

	mine := openSSE(t, env, got.Secret, "", nil).start()
	cur, _ := env.Hub.Log.Oldest(ctx)
	mine.expectPreamble(t, cur.String())
	old := openSSE(t, env, secret, "", nil).start()
	old.expectPreamble(t, cur.String())
	if err := env.Hub.MovePeer(ctx, b, newPrn); err != nil {
		t.Fatal(err)
	}
	postSend(t, env, aSec, sendBody("after-move", b), http.StatusAccepted)
	wantStreamEnds(t, old)
	if f := mine.nextEvent(t, 5*time.Second); f.Event != "delivery" {
		t.Fatalf("the peer's own stream = %+v, want its delivery", f)
	}
}

func TestSSEEndsOnDisable(t *testing.T) {
	env := testhub.Start(t)
	ctx := context.Background()
	_, aSec := env.NewPeer("grokbot", "a")
	b, bSec := env.NewPeer("session", "b")
	s := openSSE(t, env, bSec, "", nil).start()
	cur, _ := env.Hub.Log.Oldest(ctx)
	s.expectPreamble(t, cur.String())
	if err := env.Hub.SetPeerDisabled(ctx, b, true); err != nil {
		t.Fatal(err)
	}
	postSend(t, env, aSec, sendBody("after-disable", b), http.StatusAccepted)
	wantStreamEnds(t, s)
}

// A parked long-poll answers empty once its credential is gone, and the next
// poll meets the 401.
func TestLongPollEndsOnRotation(t *testing.T) {
	env := testhub.Start(t)
	b, bSec := env.NewPeer("session", "b")
	done := make(chan []byte, 1)
	go func() {
		_, body, _ := getEvents(t, env, bSec, "?after=head&wait=25", nil)
		done <- body
	}()
	time.Sleep(100 * time.Millisecond) // parked in its hold
	if _, err := env.Hub.RotatePeer(context.Background(), b); err != nil {
		t.Fatal(err)
	}
	select {
	case body := <-done:
		var p pollResp
		if err := json.Unmarshal(body, &p); err != nil || len(p.Events) != 0 {
			t.Fatalf("poll = %s, %v", body, err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("long-poll still parked")
	}
}
