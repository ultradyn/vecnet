package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/envelope"
)

// Every CLI below shares one temporary profile, just as independent processes
// do. The fake hub lets the test order pull, live push, ack and reconnect.
type displayStream struct {
	frames chan []envelope.Event
	after  string
	closed chan struct{}
}

type displayFixture struct {
	env        map[string]string
	path       string
	hub        string
	mu         sync.Mutex
	evs        []envelope.Event
	acks       chan string
	open       chan displayStream
	unverified atomic.Bool
}

func newDisplayFixture(t *testing.T) *displayFixture {
	t.Helper()
	f := &displayFixture{acks: make(chan string, 32), open: make(chan displayStream, 8)}
	f.env, f.path = hookFixtureRaw(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/peers/me":
			if f.unverified.Load() {
				http.Error(w, "identity unavailable", http.StatusServiceUnavailable)
				return
			}
			fmt.Fprint(w, `{"kind":"peer","peer_id":"peer_test"}`)
		case "/v1/peers":
			fmt.Fprint(w, `{"peers":[]}`)
		case "/v1/ack":
			var body struct{ Cursor string }
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			f.acks <- body.Cursor
			w.WriteHeader(http.StatusNoContent)
		case "/v1/threads/thr_X/messages":
			fmt.Fprint(w, `{"messages":[{"message_id":"msg_1","thread_id":"thr_X","sender":"peer_h","content":{"text":"live delivery"}}]}`)
		case "/v1/messages":
			fmt.Fprint(w, `{"message_id":"msg_reply","thread_id":"thr_X","deliveries":[]}`)
		case "/v1/receipts":
			w.WriteHeader(http.StatusNoContent)
		case "/v1/events":
			if r.Header.Get("Accept") == "text/event-stream" {
				stream := displayStream{frames: make(chan []envelope.Event, 8), after: r.Header.Get("Last-Event-ID"), closed: make(chan struct{})}
				defer close(stream.closed)
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, "retry: 1\n\n")
				w.(http.Flusher).Flush()
				f.open <- stream
				for {
					select {
					case evs := <-stream.frames:
						if evs == nil {
							return
						}
						for _, ev := range evs {
							raw, _ := json.Marshal(ev)
							fmt.Fprintf(w, "id: %s\nevent: %s\ndata: %s\n\n", ev.Cursor, ev.Type, raw)
						}
						w.(http.Flusher).Flush()
					case <-r.Context().Done():
						return
					}
				}
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			after := r.URL.Query().Get("after")
			evs := []envelope.Event{}
			next := "e1.0"
			for _, ev := range f.evs {
				next = ev.Cursor
			}
			if after != "head" && after != next {
				evs = append(evs, f.evs...)
			}
			hookPage(w, evs, next)
		default:
			http.NotFound(w, r)
		}
	})
	// Resolve the fake URL from the temporary configuration only.
	a := &app{ctx: t.Context(), getenv: func(k string) string { return f.env[k] }}
	c, _, _, err := a.connect()
	if err != nil {
		t.Fatal(err)
	}
	f.hub = c.base
	seedHook(t, f.path, "e1.0")
	if err := (stateFile{f.path}).update(t.Context(), func(s *hookState) error { s.AppCursor = "e1.0"; return nil }); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *displayFixture) delivery() envelope.Event {
	ev := hookEvent(1, "live delivery")
	var body map[string]any
	_ = json.Unmarshal(ev.Data, &body)
	body["message"].(map[string]any)["priority"] = "wake_idle"
	ev.Data, _ = json.Marshal(body)
	f.mu.Lock()
	f.evs = []envelope.Event{ev}
	f.mu.Unlock()
	return ev
}

func (f *displayFixture) watch(t *testing.T, output io.Writer, autoAck bool) (displayStream, func() int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	errs := new(hookWatchOutput)
	done := make(chan int, 1)
	args := []string{"watch", "--for-monitor", "--since", "e1.0"}
	if autoAck {
		args = append(args, "--auto-ack")
	}
	go func() {
		done <- RunContext(ctx, args, strings.NewReader(""), output, errs, func(k string) string { return f.env[k] })
	}()
	var once sync.Once
	var code int
	stop := func() int {
		once.Do(func() {
			cancel()
			select {
			case code = <-done:
			case <-time.After(3 * time.Second):
				t.Error("watch failed to stop")
			}
		})
		return code
	}
	t.Cleanup(func() { stop() })
	select {
	case stream := <-f.open:
		return stream, stop
	case <-time.After(5 * time.Second):
		t.Fatalf("watch did not open stream: %s", errs.String())
		return displayStream{}, stop
	}
}

func (f *displayFixture) ack(t *testing.T) string {
	t.Helper()
	select {
	case cur := <-f.acks:
		return cur
	case <-time.After(5 * time.Second):
		t.Fatal("watch did not ack")
		return ""
	}
}

func TestWatchLiveDisplaysAppServerClaim(t *testing.T) {
	f := newDisplayFixture(t)
	out := new(hookWatchOutput)
	stream, stop := f.watch(t, out, true)
	ev := f.delivery()
	// A connector's durable claim is not evidence that its eventual session
	// received the context. It can crash before injecting that context.
	claimed, errs := runHook(t, f.env, "--event", "app-server", "--claim", "pending", "--priorities", "wake_idle", "--expect-hub", f.hub, "--expect-caller", "peer_test")
	if !strings.Contains(claimed, "dly_1") {
		t.Fatalf("claim failed: %s %s", claimed, errs)
	}
	stream.frames <- []envelope.Event{ev}
	if cur := f.ack(t); cur != ev.Cursor {
		t.Fatalf("ack = %q", cur)
	}
	stop()
	if !strings.Contains(out.String(), "live delivery") {
		t.Fatalf("RED: cursor %s acked without printing delivery; output=%q; app-server claim remains pending", ev.Cursor, out.String())
	}
	// Releasing the connector's reservation must preserve the monitor's
	// successful display, so a subsequent Claude hook does not duplicate it.
	runHook(t, f.env, "--release", "--claim", "pending")
	if got, _ := runHook(t, f.env, "--event", "user-prompt"); got != "" {
		t.Fatalf("hook duplicated monitor output after release: %s", got)
	}
}

type failedDisplay struct {
	short bool
	tried chan struct{}
	once  sync.Once
}

func (w *failedDisplay) Write(p []byte) (int, error) {
	w.once.Do(func() { close(w.tried) })
	if w.short {
		return len(p) - 1, nil
	}
	return 0, io.ErrClosedPipe
}

func TestWatchFailedDisplayNeverAcknowledges(t *testing.T) {
	for _, short := range []bool{false, true} {
		t.Run(fmt.Sprintf("short=%t", short), func(t *testing.T) {
			f := newDisplayFixture(t)
			out := &failedDisplay{short: short, tried: make(chan struct{})}
			stream, stop := f.watch(t, out, true)
			good := new(hookWatchOutput)
			goodStream, stopGood := f.watch(t, good, false)
			ev := f.delivery()
			// An ack of this later event would cumulatively ack the lost line.
			receipt := envelope.Event{Cursor: "e1.2", Type: "receipt", Data: json.RawMessage(`{"state":"processed"}`)}
			stream.frames <- []envelope.Event{ev, receipt}
			select {
			case <-out.tried:
			case <-time.After(5 * time.Second):
				t.Fatal("watch did not attempt output")
			}
			// Wait for the watch itself to end on the output error, before
			// cancellation; cancellation must not mask an ack attempt.
			select {
			case <-stream.closed:
			case <-time.After(5 * time.Second):
				t.Fatal("failed output did not close stream")
			}
			if code := stop(); code == 0 {
				t.Error("stdout failure must fail the watch")
			}
			select {
			case cur := <-f.acks:
				t.Fatalf("acknowledged %s after failed display", cur)
			default:
			}
			// A competing live consumer must still print it.
			goodStream.frames <- []envelope.Event{ev}
			waitHookOutput(t, good, "live delivery")
			stopGood()
			if strings.Count(good.String(), "live delivery") != 1 {
				t.Fatalf("failed writer suppressed replacement: %q", good.String())
			}
		})
	}
}

func TestWatchLiveIgnoresLegacyReservation(t *testing.T) {
	f := newDisplayFixture(t)
	out := new(hookWatchOutput)
	stream, stop := f.watch(t, out, true)
	ev := f.delivery()
	// Older clients persisted Shown before writing stdout. There is no way
	// to establish that those claims actually reached their consumer.
	if err := (stateFile{f.path}).update(t.Context(), func(s *hookState) error { s.claim("dly_1"); return nil }); err != nil {
		t.Fatal(err)
	}
	stream.frames <- []envelope.Event{ev}
	f.ack(t)
	stop()
	if strings.Count(out.String(), "live delivery") != 1 {
		t.Fatalf("unverified legacy claim suppressed delivery: %q", out.String())
	}
}

func TestWatchDisplayProofBelongsToIdentity(t *testing.T) {
	for _, different := range []string{"hub", "caller", "as"} {
		t.Run(different, func(t *testing.T) {
			f := newDisplayFixture(t)
			out := new(hookWatchOutput)
			stream, stop := f.watch(t, out, true)
			ev := f.delivery()
			if err := (stateFile{f.path}).update(t.Context(), func(s *hookState) error {
				id := hookIdentity{Hub: f.hub, Caller: "peer_test"}
				switch different {
				case "hub":
					id.Hub += "/other"
				case "caller":
					id.Caller = "peer_other"
				case "as":
					id.As = "peer_other"
				}
				s.bind(id)
				s.recordDisplay("dly_1")
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			stream.frames <- []envelope.Event{ev}
			f.ack(t)
			stop()
			if strings.Count(out.String(), "live delivery") != 1 {
				t.Fatalf("another identity suppressed delivery: %q", out.String())
			}
		})
	}
}

func TestWatchDisplayStateBusyFailsOpen(t *testing.T) {
	f := newDisplayFixture(t)
	out := new(hookWatchOutput)
	stream, stop := f.watch(t, out, true)
	ev := f.delivery()
	unlock, err := lockPath(t.Context(), f.path+".lock")
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	stream.frames <- []envelope.Event{ev}
	f.ack(t)
	stop()
	if strings.Count(out.String(), "live delivery") != 1 {
		t.Fatalf("busy state lost delivery or printed twice: %q", out.String())
	}
}

func TestWatchUnverifiedIdentityCannotUseDisplayProof(t *testing.T) {
	f := newDisplayFixture(t)
	if err := (stateFile{f.path}).update(t.Context(), func(s *hookState) error {
		s.bind(hookIdentity{Hub: f.hub, Caller: "peer_other"})
		s.recordDisplay("dly_1")
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	f.unverified.Store(true)
	out := new(hookWatchOutput)
	stream, stop := f.watch(t, out, true)
	ev := f.delivery()
	stream.frames <- []envelope.Event{ev}
	f.ack(t)
	stop()
	if strings.Count(out.String(), "live delivery") != 1 {
		t.Fatalf("unverified identity reused another caller's display proof: %q", out.String())
	}
}

func TestWatchReplayFailedDisplayNeverAcknowledges(t *testing.T) {
	f := newDisplayFixture(t)
	f.delivery()
	out := &failedDisplay{tried: make(chan struct{})}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	var errs bytes.Buffer
	code := RunContext(ctx, []string{"watch", "--for-monitor", "--auto-ack", "--since", "e1.0"}, strings.NewReader(""), out, &errs, func(k string) string { return f.env[k] })
	if code == 0 || !strings.Contains(errs.String(), "watch output failed") {
		t.Fatalf("failed replay output: exit=%d %s", code, errs.String())
	}
	select {
	case cur := <-f.acks:
		t.Fatalf("failed replay acknowledged %s", cur)
	default:
	}
	select {
	case <-f.open:
		t.Fatal("watch advanced to live stream past a failed replay display")
	default:
	}
}

func TestWatchConcurrentCommandsDoNotClaimDisplay(t *testing.T) {
	f := newDisplayFixture(t)
	out := new(hookWatchOutput)
	stream, stop := f.watch(t, out, true)
	ev := f.delivery()
	commands := [][]string{
		{"--json", "inbox", "--since", "e1.0"},
		{"thread", "thr_X"},
		{"reply", "msg_1", "reply", "--thread", "thr_X", "--hold"},
		{"receipt", "dly_1", "read"},
	}
	var wg sync.WaitGroup
	for _, args := range commands {
		wg.Go(func() {
			var stdout, stderr bytes.Buffer
			code := RunContext(t.Context(), args, strings.NewReader(""), &stdout, &stderr, func(k string) string { return f.env[k] })
			if code != 0 {
				t.Errorf("%v exit=%d: %s", args, code, stderr.String())
			}
		})
	}
	wg.Wait()
	if s := readHookState(t, f.path); s.has("dly_1") {
		t.Fatal("ordinary CLI command claimed the delivery")
	}
	stream.frames <- []envelope.Event{ev}
	f.ack(t)
	stop()
	if strings.Count(out.String(), "live delivery") != 1 {
		t.Fatalf("concurrent commands suppressed delivery: %q", out.String())
	}
}

func TestWatchDisplayedByHookMayAcknowledgeWithoutDuplicate(t *testing.T) {
	f := newDisplayFixture(t)
	out := new(hookWatchOutput)
	stream, stop := f.watch(t, out, true)
	ev := f.delivery()
	if got, _ := runHook(t, f.env, "--event", "user-prompt"); !strings.Contains(got, "live delivery") {
		t.Fatalf("hook did not display delivery: %s", got)
	}
	stream.frames <- []envelope.Event{ev}
	if got := f.ack(t); got != ev.Cursor {
		t.Fatalf("ack=%s", got)
	}
	stop()
	if out.String() != "" {
		t.Fatalf("monitor duplicated hook display: %q", out.String())
	}
}

func TestWatchSecondMonitorAndReconnectDoNotDuplicate(t *testing.T) {
	f := newDisplayFixture(t)
	one, two := new(hookWatchOutput), new(hookWatchOutput)
	s1, stop1 := f.watch(t, one, true)
	s2, stop2 := f.watch(t, two, false)
	ev := f.delivery()
	s2.frames <- []envelope.Event{ev}
	waitHookOutput(t, two, "live delivery")
	s1.frames <- []envelope.Event{ev}
	f.ack(t)
	// An ordinary reconnect is a live read with Last-Event-ID. It must not
	// bypass either dedupe ring or turn a suppressed frame into a replay.
	s1.frames <- nil
	select {
	case again := <-f.open:
		if again.after != ev.Cursor {
			t.Fatalf("resume cursor=%s, want %s", again.after, ev.Cursor)
		}
		again.frames <- []envelope.Event{ev}
		f.ack(t)
	case <-time.After(5 * time.Second):
		t.Fatal("watch did not reconnect")
	}
	stop1()
	stop2()
	if one.String() != "" || strings.Count(two.String(), "live delivery") != 1 {
		t.Fatalf("duplicate display: first=%q second=%q", one.String(), two.String())
	}
	// Explicitly starting another watch with --since does request backlog
	// replay and intentionally prints a previously displayed delivery.
	three := new(hookWatchOutput)
	_, stop3 := f.watch(t, three, false)
	stop3()
	if strings.Count(three.String(), "live delivery") != 1 {
		t.Fatalf("new --since watch did not replay: %q", three.String())
	}
}
