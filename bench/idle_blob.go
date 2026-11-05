//go:build linux

package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptrace"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// These clients have separate HTTP/1 transports: N means N connections, and
// SSE must outlive the ordinary client's two-minute timeout. The server, not
// this driver, supplies every CPU, RSS and cgroup memory measurement.
func idleBlobHTTP(c *client, connections int, timeout time.Duration) (*http.Client, error) {
	base := c.http.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	tr, ok := base.(*http.Transport)
	if !ok {
		return nil, errors.New("benchmark requires an HTTP transport")
	}
	tr = tr.Clone()
	tr.MaxConnsPerHost = 0
	tr.MaxIdleConns = connections + 16
	tr.MaxIdleConnsPerHost = connections + 16
	tr.DisableCompression = true
	tr.ForceAttemptHTTP2 = false
	tr.TLSNextProto = map[string]func(string, *tls.Conn) http.RoundTripper{}
	// High-count long-poll setup can queue behind FULL-durability admission
	// writes. Let the explicit two-minute readiness deadline decide feasibility.
	tr.ResponseHeaderTimeout = 3 * time.Minute
	tr.ExpectContinueTimeout = time.Second
	hc := *c.http
	hc.Transport, hc.Timeout = tr, timeout
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &hc, nil
}

func idleBlobRequest(ctx context.Context, c *client, secret, method, path string, body io.Reader) (*http.Request, error) {
	r, err := http.NewRequestWithContext(ctx, method, c.base+path, body)
	if err != nil {
		return nil, errors.New("invalid idle/blob benchmark request")
	}
	r.Header.Set("Authorization", "Bearer "+secret)
	return r, nil
}

type idleBlobIdleRow struct {
	UnavailableReason           string  `json:"unavailable_reason,omitempty"`
	Valid                       bool    `json:"valid"`
	Mode                        string  `json:"mode"`
	Connections                 int     `json:"connections"`
	SetupSeconds                float64 `json:"setup_seconds"`
	WindowSeconds               float64 `json:"window_seconds"`
	RSSBeforeSetup              int64   `json:"rss_before_setup_bytes"`
	RSSAtStart                  int64   `json:"rss_at_start_bytes"`
	RSSAtEnd                    int64   `json:"rss_at_end_bytes"`
	RSSObservedMax              int64   `json:"rss_observed_max_bytes"`
	RSSIncrementFromBeforeSetup int64   `json:"rss_increment_from_before_setup_bytes"`
	CgroupMemoryAtStart         int64   `json:"cgroup_memory_at_start_bytes"`
	CgroupMemoryAtEnd           int64   `json:"cgroup_memory_at_end_bytes"`
	CgroupMemoryObservedMax     int64   `json:"cgroup_memory_observed_max_bytes"`
	CPUSeconds                  float64 `json:"hub_cpu_seconds"`
	CPUCores                    float64 `json:"hub_cpu_seconds_per_wall_second"`
	ActiveAtStart               int64   `json:"active_at_start"`
	ActiveAtEnd                 int64   `json:"active_at_end"`
	ActiveObservedMin           int64   `json:"active_observed_min"`
	ActiveObservedMean          float64 `json:"active_observed_mean"`
	ActiveSamples               int64   `json:"active_samples"`
	SamplesBelowTarget          int64   `json:"samples_below_target"`
	SSEHeadersVerified          int64   `json:"sse_headers_verified"`
	SSEHeartbeats               int64   `json:"sse_heartbeats_during_window"`
	LongPollResponses           int64   `json:"longpoll_responses_during_window"`
	LongPollRequestsStarted     int64   `json:"longpoll_requests_started_during_window"`
	LongPollClientsVerified     int64   `json:"longpoll_clients_verified"`
}

// One otherwise unused peer serves all counts of one protocol. The driver
// starts a fresh process for each protocol; counts within it reuse the heap.
func measureIdle(c *client, counts []int, window time.Duration, protocol string) (any, error) {
	if len(counts) == 0 || window <= 0 {
		return nil, errors.New("idle requires counts and a positive window")
	}
	for _, n := range counts {
		if n < 0 {
			return nil, errors.New("idle connection count must be nonnegative")
		}
	}
	p, err := c.peer("bench idle connections")
	if err != nil {
		return nil, err
	}
	if err = c.call(p.Secret, http.MethodPatch, "/v1/peers/me", map[string]string{"subscribe_presence": "none"}, nil); err != nil {
		return nil, err
	}
	rows := []idleBlobIdleRow{}
	result := map[string]any{
		"samples":                                rows,
		"peer_layout":                            "one dedicated peer shared by every connection and count within one protocol; fresh hub per protocol; subscribe_presence=none; after=head",
		"rss_method":                             "fresh process per protocol; incremental RSS across increasing counts within it; retained heap/cache can affect later counts",
		"cpu_method":                             "hub process CPU only, via /bench/snapshot; setup and one-second settle excluded; snapshots sampled about once per second, including zero-connection baseline",
		"readiness":                              "each SSE client verifies flushed 200 event-stream headers; each long-poll client verifies its own first empty 200 response after the production hold; then active handlers and registered waiters >= N, one-second settle and active-handler/client liveness recheck; subsequent renewal work is included",
		"longpoll_method":                        "wait=25; renew immediately after each validated empty 200 response; brief renewal gaps appear in observed active counts",
		"memory_method":                          "hub RSS and cgroup memory.current sampled at start, about once per second and end; observed maxima are not continuous peaks",
		"heartbeat_seconds":                      15,
		"longpoll_wait_seconds":                  25,
		"setup_concurrency":                      64,
		"window_covers_heartbeat_and_poll_cycle": window >= 30*time.Second,
	}
	for _, mode := range []string{protocol} {
		for _, n := range counts {
			fmt.Fprintf(os.Stderr, "benchmark: idle %s connections=%d\n", mode, n)
			row, e := idleBlobMeasureCount(c, p, mode, n, window)
			if e != nil && n >= 5000 {
				row.UnavailableReason = e.Error()
				fmt.Fprintf(os.Stderr, "benchmark: idle %s connections=%d unavailable: %v\n", mode, n, e)
			}
			rows = append(rows, row)
			result["samples"] = rows
			if e != nil {
				if n >= 5000 {
					continue
				} // Assignment makes the 5,000-stream probe optional.
				return result, fmt.Errorf("idle %s count=%d: %w", mode, n, e)
			}
			fmt.Fprintf(os.Stderr, "benchmark: idle %s connections=%d RSS=%.2fMiB CPU=%.3f%% valid=%t\n", mode, n, float64(row.RSSAtEnd)/(1<<20), 100*row.CPUCores, row.Valid)
		}
	}
	return result, nil
}

type idleBlobIdleGroup struct {
	setupContext                               context.Context
	peerID                                     string
	ctx                                        context.Context
	wg                                         sync.WaitGroup
	ready                                      chan struct{}
	errors                                     chan error
	live, headers, heartbeats, polls, requests atomic.Int64
	verifiedPollClients                        atomic.Int64
}

func (g *idleBlobIdleGroup) fail(err error) {
	if g.ctx.Err() == nil {
		select {
		case g.errors <- err:
		default:
		}
	}
}

func (g *idleBlobIdleGroup) check() error {
	select {
	case err := <-g.errors:
		return err
	default:
		return nil
	}
}

func idleBlobActive(s snapshot, mode string) int64 {
	if mode == "sse" {
		return s.ActiveSSE
	}
	return s.ActiveLongPoll
}

func idleBlobWaitActive(c *client, g *idleBlobIdleGroup, mode string, n int, zero bool) (snapshot, error) {
	registeredLast := 0
	ctx := g.setupContext
	if zero {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
	}
	call := func(path string, out any) error { return c.callContext(ctx, c.operator, "GET", path, nil, out, 0) }
	timedOut := func(s snapshot) error {
		return fmt.Errorf("readiness timeout: active=%d/%d registered_event_waiters=%d/%d; no partial-concurrency result", idleBlobActive(s, mode), n, registeredLast, n)
	}
	var last snapshot
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		if err := g.check(); err != nil {
			return snapshot{}, err
		}
		var s snapshot
		err := call("/bench/snapshot", &s)
		if err != nil {
			if ctx.Err() != nil {
				return last, timedOut(last)
			}
			return s, err
		}
		last = s
		if zero {
			if s.ActiveSSE == 0 && s.ActiveLongPoll == 0 {
				return s, nil
			}
		} else if idleBlobActive(s, mode) >= int64(n) {
			var registered struct {
				Waiting int `json:"waiting"`
			}
			if err := call("/bench/waiters?peer="+g.peerID, &registered); err != nil {
				if ctx.Err() != nil {
					return s, timedOut(s)
				}
				return s, err
			}
			registeredLast = registered.Waiting
			if registered.Waiting >= n {
				return s, nil
			}
		}
		select {
		case err := <-g.errors:
			return s, err
		case <-ctx.Done():
			return s, timedOut(s)
		case <-tick.C:
		}
	}
}

func idleBlobMeasureCount(c *client, p peer, mode string, n int, window time.Duration) (row idleBlobIdleRow, err error) {
	row.Mode, row.Connections = mode, n
	before, err := c.snapshot()
	if err != nil {
		return row, err
	}
	if before.ActiveSSE != 0 || before.ActiveLongPoll != 0 {
		return row, errors.New("idle requires no preexisting event requests")
	}
	row.RSSBeforeSetup = before.RSS
	hc, err := idleBlobHTTP(c, n, 0)
	if err != nil {
		return row, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	setup := time.Now()
	setupContext, setupCancel := context.WithDeadline(ctx, setup.Add(2*time.Minute))
	defer setupCancel()
	g := &idleBlobIdleGroup{ctx: ctx, setupContext: setupContext, peerID: p.ID, ready: make(chan struct{}, n), errors: make(chan error, 1)}
	// Close clients, join every worker, then wait for server-side handlers to
	// leave before the next count or a blob-cap mutation is permitted.
	defer func() {
		if err != nil && row.SetupSeconds == 0 {
			row.SetupSeconds = time.Since(setup).Seconds()
		}
		if e := g.check(); err == nil && e != nil {
			err = e
		}
		cancel()
		g.wg.Wait()
		hc.CloseIdleConnections()
		if e := g.check(); err == nil && e != nil {
			err = e
		}
		if _, e := idleBlobWaitActive(c, g, mode, 0, true); err == nil && e != nil {
			err = e
		}
		row.Valid = err == nil
	}()
	gate := make(chan struct{}, 64)
	for i := 0; i < n; i++ {
		g.wg.Add(1)
		go func() {
			defer g.wg.Done()
			g.live.Add(1)
			defer g.live.Add(-1)
			if mode == "sse" {
				idleBlobSSE(c, hc, p, g, gate)
			} else {
				idleBlobPoll(c, hc, p, g, gate)
			}
		}()
	}
	for i := 0; i < n; i++ {
		select {
		case <-g.ready:
		case e := <-g.errors:
			return row, e
		case <-setupContext.Done():
			return row, errors.New("not all idle connections established within two minutes")
		}
	}
	if _, err = idleBlobWaitActive(c, g, mode, n, n == 0); err != nil {
		return row, err
	}
	setupCancel()
	settle := time.NewTimer(time.Second)
	defer settle.Stop()
	select {
	case err = <-g.errors:
		return row, err
	case <-settle.C:
	}
	startAt := time.Now()
	start, err := c.snapshot()
	if err != nil {
		return row, err
	}
	if g.live.Load() != int64(n) || idleBlobActive(start, mode) != int64(n) {
		return row, errors.New("connection target did not remain established through settle")
	}
	row.SetupSeconds = time.Since(setup).Seconds()
	row.SSEHeadersVerified = g.headers.Load()
	row.LongPollClientsVerified = g.verifiedPollClients.Load()
	row.RSSAtStart, row.RSSObservedMax = start.RSS, start.RSS
	row.CgroupMemoryAtStart, row.CgroupMemoryObservedMax = start.MemoryCurrent, start.MemoryCurrent
	row.RSSIncrementFromBeforeSetup = start.RSS - before.RSS
	row.ActiveAtStart, row.ActiveObservedMin = idleBlobActive(start, mode), idleBlobActive(start, mode)
	hb, polls, requests := g.heartbeats.Load(), g.polls.Load(), g.requests.Load()
	var activeSum int64
	observe := func(s snapshot) error {
		active := idleBlobActive(s, mode)
		if g.live.Load() != int64(n) || (mode == "sse" && active != int64(n)) {
			return errors.New("idle connection lost during measurement")
		}
		if active > int64(n) || (mode == "sse" && s.ActiveLongPoll != 0) || (mode == "longpoll" && s.ActiveSSE != 0) {
			return errors.New("unexpected event requests contaminate idle measurement")
		}
		row.ActiveSamples++
		activeSum += active
		row.ActiveObservedMin = min(row.ActiveObservedMin, active)
		if active < int64(n) {
			row.SamplesBelowTarget++
		}
		row.RSSObservedMax = max(row.RSSObservedMax, s.RSS)
		row.CgroupMemoryObservedMax = max(row.CgroupMemoryObservedMax, s.MemoryCurrent)
		return nil
	}
	if err = observe(start); err != nil {
		return row, err
	}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	timer := time.NewTimer(window)
	defer timer.Stop()
	var end snapshot
	var endAt time.Time
measure:
	for {
		select {
		case err = <-g.errors:
			return row, err
		case <-timer.C:
			endAt = time.Now()
			end, err = c.snapshot()
			if err != nil {
				return row, err
			}
			if err = observe(end); err != nil {
				return row, err
			}
			break measure
		case <-ticker.C:
			s, e := c.snapshot()
			if e != nil {
				return row, e
			}
			if err = observe(s); err != nil {
				return row, err
			}
		}
	}
	row.WindowSeconds = endAt.Sub(startAt).Seconds()
	row.RSSAtEnd, row.CgroupMemoryAtEnd = end.RSS, end.MemoryCurrent
	row.CPUSeconds = end.CPUSeconds - start.CPUSeconds
	if row.CPUSeconds < 0 {
		return row, errors.New("hub CPU counter decreased")
	}
	row.CPUCores = row.CPUSeconds / row.WindowSeconds
	row.ActiveAtEnd = idleBlobActive(end, mode)
	row.ActiveObservedMean = float64(activeSum) / float64(row.ActiveSamples)
	row.SSEHeartbeats = g.heartbeats.Load() - hb
	row.LongPollResponses = g.polls.Load() - polls
	row.LongPollRequestsStarted = g.requests.Load() - requests
	return row, g.check()
}

func idleBlobSSE(c *client, hc *http.Client, p peer, g *idleBlobIdleGroup, gate chan struct{}) {
	select {
	case gate <- struct{}{}:
	case <-g.ctx.Done():
		return
	}
	r, err := idleBlobRequest(g.ctx, c, p.Secret, http.MethodGet, "/v1/events?after=head", nil)
	if err != nil {
		<-gate
		g.fail(err)
		return
	}
	r.Header.Set("Accept", "text/event-stream")
	resp, err := hc.Do(r)
	<-gate
	if err != nil {
		g.fail(errors.New("SSE transport failed"))
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		g.fail(fmt.Errorf("SSE expected 200 event-stream, got HTTP %d", resp.StatusCode))
		return
	}
	g.headers.Add(1)
	g.ready <- struct{}{}
	scan := bufio.NewScanner(resp.Body)
	scan.Buffer(make([]byte, 1024), 64*1024)
	for scan.Scan() {
		line := scan.Text()
		if line == ": hb" {
			g.heartbeats.Add(1)
		}
		if strings.HasPrefix(line, "event:") {
			g.fail(errors.New("unexpected event on dedicated idle SSE peer"))
			return
		}
	}
	if g.ctx.Err() == nil {
		if scan.Err() != nil {
			g.fail(errors.New("SSE read failed"))
		} else {
			g.fail(errors.New("SSE closed before cancellation"))
		}
	}
}

func idleBlobPoll(c *client, hc *http.Client, p peer, g *idleBlobIdleGroup, gate chan struct{}) {
	var announced sync.Once
	for g.ctx.Err() == nil {
		select {
		case gate <- struct{}{}:
		case <-g.ctx.Done():
			return
		}
		var released sync.Once
		release := func() { released.Do(func() { <-gate }) }
		// Headers arrive only after the 25s hold. Release the setup permit as
		// soon as the request is written, not when its response returns.
		trace := &httptrace.ClientTrace{WroteRequest: func(info httptrace.WroteRequestInfo) {
			release()
			if info.Err == nil {
				g.requests.Add(1)
			}
		}}
		r, err := idleBlobRequest(httptrace.WithClientTrace(g.ctx, trace), c, p.Secret, http.MethodGet, "/v1/events?after=head&wait=25", nil)
		if err != nil {
			release()
			g.fail(err)
			return
		}
		r.Header.Set("Accept", "application/json")
		started := time.Now()
		resp, err := hc.Do(r)
		release()
		if err != nil {
			g.fail(errors.New("longpoll transport failed"))
			return
		}
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			g.fail(fmt.Errorf("longpoll expected 200, got HTTP %d", resp.StatusCode))
			return
		}
		var body struct {
			Events     []json.RawMessage `json:"events"`
			NextCursor string            `json:"next_cursor"`
		}
		err = json.NewDecoder(io.LimitReader(resp.Body, 64*1024)).Decode(&body)
		closeErr := resp.Body.Close()
		if err != nil || closeErr != nil || body.NextCursor == "" || len(body.Events) != 0 {
			g.fail(errors.New("invalid or nonempty idle longpoll response"))
			return
		}
		if time.Since(started) < 24*time.Second {
			g.fail(errors.New("idle longpoll returned before production 25s hold"))
			return
		}
		g.polls.Add(1)
		// Count each client only after its own validated first response. The
		// event log removes cancelled waiters asynchronously, so a waiter
		// count alone can include both an old poll and its renewal.
		announced.Do(func() {
			g.verifiedPollClients.Add(1)
			g.ready <- struct{}{}
		})
	}
}

type idleBlobTransfer struct {
	Bytes           int64   `json:"bytes"`
	Seconds         float64 `json:"seconds"`
	MBPerSecond     float64 `json:"decimal_MB_per_second"`
	CPUSeconds      float64 `json:"hub_cpu_seconds"`
	CPUSecondsPerGB float64 `json:"hub_cpu_seconds_per_decimal_GB"`
	RSSBefore       int64   `json:"rss_before_bytes"`
	RSSAfter        int64   `json:"rss_after_bytes"`
}

// A 32 KiB random block is repeated through a reader, never a size-byte slice.
// Each repeat uses a new block, so store deduplication cannot hide disk cost.
type idleBlobPattern struct {
	block  []byte
	offset int
	read   atomic.Int64
}

func (p *idleBlobPattern) Read(dst []byte) (int, error) {
	n := 0
	for n < len(dst) {
		k := copy(dst[n:], p.block[p.offset:])
		n += k
		p.offset = (p.offset + k) % len(p.block)
	}
	p.read.Add(int64(n))
	return n, nil
}

func idleBlobTransferResult(size int64, elapsed time.Duration, before, after snapshot) (idleBlobTransfer, error) {
	seconds, cpu := elapsed.Seconds(), after.CPUSeconds-before.CPUSeconds
	if seconds <= 0 || cpu < 0 {
		return idleBlobTransfer{}, errors.New("invalid blob measurement interval")
	}
	return idleBlobTransfer{Bytes: size, Seconds: seconds, MBPerSecond: float64(size) / 1e6 / seconds,
		CPUSeconds: cpu, CPUSecondsPerGB: cpu / (float64(size) / 1e9), RSSBefore: before.RSS, RSSAfter: after.RSS}, nil
}

func measureBlobs(c *client, repeats int) (result any, err error) {
	if repeats < 1 {
		return nil, errors.New("blob repeats must be positive")
	}
	hc, err := idleBlobHTTP(c, 1, 5*time.Minute)
	if err != nil {
		return nil, err
	}
	defer hc.CloseIdleConnections()
	const productionCap int64 = 25 << 20
	const extendedCap int64 = 100 << 20
	rows := []any{}
	report := map[string]any{
		"samples": rows, "production_cap_bytes": productionCap, "repeats": repeats,
		"units":           "sizes labelled MiB are binary; MB/s and CPU seconds/GB use decimal 1e6 and 1e9 bytes respectively",
		"method":          "unique streamed 32KiB random repeating block per upload; operator download; SHA-256 checked against independently hashed input and upload response; hub CPU from snapshots only",
		"storage_method":  "storage difference b-a around each upload and rejection; peer setup excluded; /bench/storage checkpoints WAL outside transfer timing",
		"download_method": "immediate warm-cache download, including client streaming SHA-256 verification in wall time; client CPU excluded from hub CPU",
		"peer_layout":     "one fresh uploader per repeat, shared by that repeat's production and extended uploads; avoids daily quota accumulation across repeats",
	}
	result = report
	// This private endpoint never changes the production API's default cap.
	if err = c.call(c.operator, http.MethodPost, "/bench/blob-cap", map[string]int64{"bytes": productionCap}, nil); err != nil {
		return result, err
	}
	defer func() {
		if e := c.call(c.operator, http.MethodPost, "/bench/blob-cap", map[string]int64{"bytes": productionCap}, nil); e != nil {
			if err == nil {
				err = errors.New("failed restoring production blob cap")
			} else {
				err = fmt.Errorf("%w; failed restoring production blob cap", err)
			}
		}
	}()
	uploaders := make([]peer, repeats)
	for i := range uploaders {
		uploaders[i], err = c.peer(fmt.Sprintf("bench blob uploader %d", i+1))
		if err != nil {
			return result, err
		}
	}
	for i, p := range uploaders {
		var sample any
		sample, err = idleBlobAccepted(c, hc, p, 10<<20, "production_25MiB_cap", i+1)
		if err != nil {
			return result, err
		}
		rows = append(rows, sample)
		report["samples"] = rows
		sample, err = idleBlobRejected(c, hc, p, extendedCap, i+1)
		if err != nil {
			return result, err
		}
		rows = append(rows, sample)
		report["samples"] = rows
	}
	// Extended-cap work is explicitly labelled and runs sequentially, after
	// every production-cap rejection has been measured.
	if err = c.call(c.operator, http.MethodPost, "/bench/blob-cap", map[string]int64{"bytes": extendedCap}, nil); err != nil {
		return result, err
	}
	for i, p := range uploaders {
		var sample any
		sample, err = idleBlobAccepted(c, hc, p, extendedCap, "benchmark_only_extended_100MiB_cap", i+1)
		if err != nil {
			return result, err
		}
		rows = append(rows, sample)
		report["samples"] = rows
	}
	return result, nil
}

func idleBlobAccepted(c *client, hc *http.Client, p peer, size int64, scenario string, repeat int) (any, error) {
	block := make([]byte, 32*1024)
	if _, err := rand.Read(block); err != nil {
		return nil, errors.New("blob payload random generation failed")
	}
	buf := make([]byte, 32*1024)
	want := sha256.New()
	if _, err := io.CopyBuffer(want, io.LimitReader(&idleBlobPattern{block: block}, size), buf); err != nil {
		return nil, errors.New("blob expected hash failed")
	}
	wantSHA := hex.EncodeToString(want.Sum(nil))
	storeBefore, err := c.storage()
	if err != nil {
		return nil, err
	}
	before, err := c.snapshot()
	if err != nil {
		return nil, err
	}
	r, err := idleBlobRequest(context.Background(), c, p.Secret, http.MethodPut, "/v1/blobs", io.LimitReader(&idleBlobPattern{block: block}, size))
	if err != nil {
		return nil, err
	}
	r.ContentLength = size
	r.Header.Set("Content-Type", "application/octet-stream")
	start := time.Now()
	resp, err := hc.Do(r)
	if err != nil {
		return nil, errors.New("blob upload transport failed")
	}
	if resp.StatusCode != http.StatusCreated {
		resp.Body.Close()
		return nil, fmt.Errorf("blob upload expected 201, got HTTP %d", resp.StatusCode)
	}
	var uploaded struct {
		SHA256 string `json:"sha256"`
		Size   int64  `json:"size"`
	}
	decodeErr := json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&uploaded)
	closeErr := resp.Body.Close()
	elapsed := time.Since(start)
	if decodeErr != nil || closeErr != nil {
		return nil, errors.New("invalid blob upload response")
	}
	if uploaded.Size != size || uploaded.SHA256 != wantSHA {
		return nil, errors.New("uploaded blob size or SHA-256 mismatch")
	}
	after, err := c.snapshot()
	if err != nil {
		return nil, err
	}
	upload, err := idleBlobTransferResult(size, elapsed, before, after)
	if err != nil {
		return nil, err
	}
	storeAfter, err := c.storage()
	if err != nil {
		return nil, err
	}
	if storeAfter.BlobBytes-storeBefore.BlobBytes != size {
		return nil, errors.New("unique blob disk growth did not match uploaded bytes")
	}
	before, err = c.snapshot()
	if err != nil {
		return nil, err
	}
	r, err = idleBlobRequest(context.Background(), c, c.operator, http.MethodGet, "/v1/blobs/"+wantSHA, nil)
	if err != nil {
		return nil, err
	}
	got := sha256.New()
	start = time.Now()
	resp, err = hc.Do(r)
	if err != nil {
		return nil, errors.New("blob download transport failed")
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("blob download expected 200, got HTTP %d", resp.StatusCode)
	}
	// Bound even an incorrect response to size+1, then reject any mismatch.
	n, readErr := io.CopyBuffer(got, io.LimitReader(resp.Body, size+1), buf)
	closeErr = resp.Body.Close()
	elapsed = time.Since(start)
	if readErr != nil || closeErr != nil {
		return nil, errors.New("blob download read failed")
	}
	if n != size || hex.EncodeToString(got.Sum(nil)) != wantSHA {
		return nil, errors.New("downloaded blob size or SHA-256 mismatch")
	}
	after, err = c.snapshot()
	if err != nil {
		return nil, err
	}
	download, err := idleBlobTransferResult(size, elapsed, before, after)
	if err != nil {
		return nil, err
	}
	return map[string]any{"scenario": scenario, "repeat": repeat, "size_bytes": size,
		"upload": upload, "download": download, "sha256_verified": true,
		"storage_delta": difference(storeBefore, storeAfter)}, nil
}

func idleBlobRejected(c *client, hc *http.Client, p peer, size int64, repeat int) (any, error) {
	storeBefore, err := c.storage()
	if err != nil {
		return nil, err
	}
	before, err := c.snapshot()
	if err != nil {
		return nil, err
	}
	reader := &idleBlobPattern{block: make([]byte, 32*1024)}
	r, err := idleBlobRequest(context.Background(), c, p.Secret, http.MethodPut, "/v1/blobs", io.LimitReader(reader, size))
	if err != nil {
		return nil, err
	}
	r.ContentLength = size
	r.Header.Set("Content-Type", "application/octet-stream")
	r.Header.Set("Expect", "100-continue")
	start := time.Now()
	resp, err := hc.Do(r)
	if err != nil {
		return nil, errors.New("oversize blob rejection transport failed")
	}
	status := resp.StatusCode
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	decodeErr := json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&body)
	closeErr := resp.Body.Close()
	elapsed := time.Since(start).Seconds()
	if status != http.StatusRequestEntityTooLarge {
		return nil, fmt.Errorf("100 MiB blob under production cap expected 413, got HTTP %d", status)
	}
	if decodeErr != nil || closeErr != nil || body.Error.Code != "blob_too_large" {
		return nil, errors.New("invalid oversize blob rejection response")
	}
	after, err := c.snapshot()
	if err != nil {
		return nil, err
	}
	storeAfter, err := c.storage()
	if err != nil {
		return nil, err
	}
	if storeAfter.BlobBytes != storeBefore.BlobBytes {
		return nil, errors.New("rejected blob changed stored blob bytes")
	}
	if after.CPUSeconds < before.CPUSeconds {
		return nil, errors.New("hub CPU counter decreased")
	}
	return map[string]any{"scenario": "production_25MiB_cap_100MiB_http_rejection", "repeat": repeat,
		"declared_body_bytes": size, "http_status": status, "error_code": "blob_too_large",
		"seconds": elapsed, "hub_cpu_seconds": after.CPUSeconds - before.CPUSeconds,
		"body_bytes_read_by_transport": reader.read.Load(),
		"method":                       "known Content-Length plus Expect: 100-continue; measures HTTP rejection, not transfer of 100 MiB; no CPU/GB normalization",
		"storage_delta":                difference(storeBefore, storeAfter)}, nil
}
