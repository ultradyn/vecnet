package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/envelope"
)

func TestWatchPendingClaimResumesLocally(t *testing.T) {
	for _, receipt := range []bool{false, true} {
		t.Run(map[bool]string{true: "receipt", false: "unsent-release"}[receipt], func(t *testing.T) {
			f := newDisplayFixture(t)
			out := new(hookWatchOutput)
			stream, stop := f.watch(t, out, true)
			ev := f.delivery()
			if err := codexUpdateState(t.Context(), f.path, false, func(s *hookState) error {
				s.Claims = []claimRef{{ID: "dly_1", Claim: "pending", Generation: "generation", Accepted: receipt}}
				// Even older proof must not bypass a current unresolved claim.
				if receipt {
					s.recordDisplay("dly_1")
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			stream.frames <- []envelope.Event{ev, hookEvent(2, "later delivery")}
			waitDisplayBlocked(t, f, stream, out)
			if err := codexUpdateState(t.Context(), f.path, false, func(s *hookState) error {
				s.Claims = nil
				if receipt {
					s.recordDisplay("dly_1")
				} else {
					s.Shown = nil
					s.Replay = append(s.Replay, replayRef{ID: "dly_1", After: "e1.0"})
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			// No new stream notification follows the shared-state transition.
			if got := f.ack(t); got != ev.Cursor {
				t.Fatalf("ack passed retained cursor: %s", got)
			}
			select {
			case again := <-f.open:
				if again.after != ev.Cursor {
					t.Fatalf("wrong opaque resume: %s", again.after)
				}
				again.frames <- []envelope.Event{ev}
				if got := f.ack(t); got != ev.Cursor {
					t.Fatalf("duplicate ack: %s", got)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("did not resume after local proof/release")
			}
			stop()
			want := 1
			if receipt {
				want = 0
			}
			if strings.Count(out.String(), "live delivery") != want || strings.Contains(out.String(), "later delivery") {
				t.Fatalf("duplicate or crossed barrier: %q", out.String())
			}
		})
	}
}

type incompleteWatchOutput struct{ short bool }

func (o incompleteWatchOutput) Write(p []byte) (int, error) {
	if o.short {
		return len(p) - 1, nil
	}
	return 0, errors.New("closed output")
}

// A transport acknowledgment must never pass output which was not delivered.
// Test all output modes: the JSON path must honor complete writes too.
func TestWatchIncompleteOutputNeverAcknowledges(t *testing.T) {
	for _, short := range []bool{false, true} {
		for _, mode := range []string{"text", "json", "monitor"} {
			t.Run(mode+map[bool]string{true: "-short", false: "-error"}[short], func(t *testing.T) {
				w := &watcher{a: &app{ctx: context.Background(), stdout: incompleteWatchOutput{short}, stderr: io.Discard, json: mode == "json"},
					forMonitor: mode == "monitor", autoAck: true, seen: newDedupe(dedupeWindow)}
				ev := hookDelivery("dly_unprinted")
				raw, _ := json.Marshal(ev)
				w.handle(ev.Type, string(raw), false)
				if w.pendingAck != "" {
					t.Fatalf("queued acknowledgment %q for incomplete output", w.pendingAck)
				}
				if _, seen := w.seen.set["dly_unprinted"]; seen {
					t.Fatal("failed output was recorded as seen")
				}
			})
		}
	}
}

// Every CLI below shares one temporary profile, just as independent processes
// do. The fake hub lets the test order pull, live push, ack and reconnect.
type displayStream struct {
	frames chan []envelope.Event
	after  string
	closed chan struct{}
}

type displayFixture struct {
	env            map[string]string
	path           string
	hub            string
	mu             sync.Mutex
	evs            []envelope.Event
	polls          []string
	acks           chan string
	open           chan displayStream
	unverified     atomic.Bool
	identityJSON   atomic.Pointer[string] // nil keeps the fixture's verified peer
	expectedBearer atomic.Pointer[string]
}

func newDisplayFixture(t *testing.T) *displayFixture {
	t.Helper()
	f := &displayFixture{acks: make(chan string, 32), open: make(chan displayStream, 8)}
	f.env, f.path = hookFixtureRaw(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/peers/me":
			if bearer := f.expectedBearer.Load(); bearer != nil && r.Header.Get("Authorization") != "Bearer "+*bearer {
				http.Error(w, "rotated credential", http.StatusUnauthorized)
				return
			}
			if f.unverified.Load() {
				http.Error(w, "identity unavailable", http.StatusServiceUnavailable)
				return
			}
			if raw := f.identityJSON.Load(); raw != nil {
				fmt.Fprint(w, *raw)
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
			f.polls = append(f.polls, after)
			evs := []envelope.Event{}
			next := "e1.0"
			if len(f.evs) > 0 {
				next = f.evs[len(f.evs)-1].Cursor
			}
			if after != "head" {
				start := slices.IndexFunc(f.evs, func(ev envelope.Event) bool { return ev.Cursor == after }) + 1
				end := min(start+hookPageLimit, len(f.evs))
				evs = append(evs, f.evs[start:end]...)
				if len(evs) > 0 {
					next = evs[len(evs)-1].Cursor
				}
			}
			hookPage(w, evs, next)
		default:
			http.NotFound(w, r)
		}
	})
	f.env["CODEX_HOME"] = filepath.Join(f.env["HOME"], ".codex")
	f.env["XDG_DATA_HOME"] = filepath.Join(f.env["HOME"], ".local", "share")
	f.env["XDG_RUNTIME_DIR"] = filepath.Join(f.env["HOME"], "runtime")
	f.env["VECNET_CODEX_AUTO_DELIVERY"] = "0"
	// Resolve the fake URL from the temporary configuration only.
	a := &app{ctx: t.Context(), getenv: func(k string) string { return f.env[k] }}
	c, _, _, err := a.connect()
	if err != nil {
		t.Fatal(err)
	}
	f.hub = c.base
	seedHook(t, f.path, "e1.0")
	if err := (stateFile{f.path}).update(t.Context(), func(s *hookState) error {
		s.bind(hookIdentity{Hub: f.hub, Caller: "peer_test", Cred: hookCredential("vn_peer_test")})
		s.AppCursor = "e1.0"
		return nil
	}); err != nil {
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

func (f *displayFixture) watch(t *testing.T, output io.Writer, autoAck bool, selectedAs ...string) (displayStream, func() int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	errs := new(hookWatchOutput)
	done := make(chan int, 1)
	args := []string{"watch", "--for-monitor", "--since", "e1.0"}
	if len(selectedAs) != 0 && selectedAs[0] != "" {
		args = append([]string{"--as", selectedAs[0]}, args...)
	}
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
			before := readHookState(t, f.path)
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
			waitDisplayBlocked(t, f, stream, out)
			// Foreign state cannot be rebound as permission to print. A
			// repaired matching state can resume the same retained event.
			if err := (stateFile{f.path}).update(t.Context(), func(s *hookState) error { *s = before; return nil }); err != nil {
				t.Fatal(err)
			}
			f.ack(t)
			stop()
			if strings.Count(out.String(), "live delivery") != 1 {
				t.Fatalf("another identity suppressed delivery: %q", out.String())
			}
		})
	}
}

func TestWatchDisplayStateBusyDefers(t *testing.T) {
	f := newDisplayFixture(t)
	out := new(hookWatchOutput)
	stream, stop := f.watch(t, out, true)
	ev := f.delivery()
	unlock, err := lockPath(t.Context(), f.path+".lock")
	if err != nil {
		t.Fatal(err)
	}
	var once sync.Once
	release := func() { once.Do(unlock) }
	defer release()
	stream.frames <- []envelope.Event{ev}
	waitDisplayBlocked(t, f, stream, out)
	release()
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
	waitDisplayBlocked(t, f, stream, out)
	stop()
	if out.String() != "" {
		t.Fatalf("unverified identity authorized monitor output: %q", out.String())
	}
	if s := readHookState(t, f.path); s.Caller != "peer_other" || !slices.Contains(s.Displayed, "dly_1") {
		t.Fatal("unverified monitor changed foreign display proof")
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

// An old reservation must not age a fresh successful monitor display out of
// the hook's dedupe window after only one subsequent delivery.
func TestWatchFreshDisplaySurvivesReservationEviction(t *testing.T) {
	f := newDisplayFixture(t)
	out := new(hookWatchOutput)
	stream, stop := f.watch(t, out, true)
	ev := f.delivery()
	if err := (stateFile{f.path}).update(t.Context(), func(s *hookState) error {
		s.bind(hookIdentity{Hub: f.hub, Caller: "peer_test"})
		s.claim("dly_1")
		for i := 0; i < shownWindow-1; i++ {
			s.claim(fmt.Sprintf("dly_old_%d", i))
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	stream.frames <- []envelope.Event{ev, hookEvent(2, "next delivery")}
	if cur := f.ack(t); cur != "e1.2" {
		t.Fatalf("ack=%s", cur)
	}
	stop()
	if !strings.Contains(out.String(), "live delivery") || !strings.Contains(out.String(), "next delivery") {
		t.Fatalf("monitor failed to display both deliveries: %q", out.String())
	}
	s := readHookState(t, f.path)
	if !slices.Contains(s.Displayed, "dly_1") {
		t.Fatalf("expected fresh display proof: displayed=%v", s.Displayed)
	}
	if got, errs := runHook(t, f.env, "--event", "user-prompt", "--auto-ack"); got != "" {
		t.Fatalf("hook duplicated fresh monitor display after reservation eviction: %q; stderr=%q", got, errs)
	}
	select {
	case cur := <-f.acks:
		t.Fatalf("hook acknowledged %s across other unproved legacy reservations", cur)
	default:
	}
}

func waitDisplayBlocked(t *testing.T, f *displayFixture, stream displayStream, out *hookWatchOutput) {
	t.Helper()
	select {
	case <-stream.closed:
	case cur := <-f.acks:
		t.Fatalf("missing deferral: acknowledged %q before reservation/state resolved", cur)
	case <-time.After(5 * time.Second):
		t.Fatal("blocked monitor did not close its transport")
	}
	if out.String() != "" {
		t.Fatal("unresolved reservation/state authorized monitor output")
	}
	select {
	case cur := <-f.acks:
		t.Fatalf("blocked monitor acknowledged %q", cur)
	default:
	}
}
