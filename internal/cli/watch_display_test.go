package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
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

func TestWatchLiveDisplaysAppServerClaim(t *testing.T) {
	f := newDisplayFixture(t)
	out := new(hookWatchOutput)
	stream, stop := f.watch(t, out, true)
	second := new(hookWatchOutput)
	secondStream, stopSecond := f.watch(t, second, true)
	ev := f.delivery()
	// A connector's durable claim is not evidence that its eventual session
	// received the context. It can crash before injecting that context.
	claimed, errs := runHook(t, f.env, "--event", "app-server", "--claim", "pending", "--priorities", "wake_idle", "--expect-hub", f.hub, "--expect-caller", "peer_test")
	if !strings.Contains(claimed, "dly_1") {
		t.Fatalf("claim failed: %s %s", claimed, errs)
	}
	stream.frames <- []envelope.Event{ev}
	waitDisplayBlocked(t, f, stream, out)
	// A known-unsent ordinary release permits exactly one eventual display.
	// Releasing again after display must preserve that positive proof.
	released, errs := runHook(t, f.env, "--release", "--claim", "pending")
	var response struct {
		Released int `json:"released"`
	}
	if err := json.Unmarshal([]byte(released), &response); err != nil || response.Released != 1 {
		t.Fatalf("release failed: response=%q stderr=%q error=%v", released, errs, err)
	}
	if cur := f.ack(t); cur != ev.Cursor {
		t.Fatalf("ack = %q", cur)
	}
	stop()
	if !strings.Contains(out.String(), "live delivery") {
		t.Fatalf("released cursor %s acked without printing delivery; output=%q", ev.Cursor, out.String())
	}
	deferredRelease(t, f, "pending", 0)
	s := readHookState(t, f.path)
	if slices.ContainsFunc(s.Claims, func(r claimRef) bool { return r.ID == "dly_1" || r.Claim == "pending" }) {
		t.Fatalf("release retained matching claim: %+v", s.Claims)
	}
	if !slices.Contains(s.Displayed, "dly_1") || slices.ContainsFunc(s.Replay, func(r replayRef) bool { return r.ID == "dly_1" }) {
		t.Fatalf("release lost display proof or queued replay: displayed=%v replay=%v", s.Displayed, s.Replay)
	}
	secondStream.frames <- []envelope.Event{ev}
	if cur := f.ack(t); cur != ev.Cursor {
		t.Fatalf("second live monitor ack=%q", cur)
	}
	stopSecond()
	if second.String() != "" {
		t.Fatalf("already-connected live monitor duplicated released display: %q", second.String())
	}
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

func TestWatchConcurrentNonDisplayCommandsDoNotClaimDisplay(t *testing.T) {
	f := newDisplayFixture(t)
	out := new(hookWatchOutput)
	stream, stop := f.watch(t, out, true)
	ev := f.delivery()
	commands := [][]string{
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

// Complete inbox output now supplies the same positive display proof as a
// successful hook/monitor. An explicit inbox replay must still print, while
// an already-connected live monitor must not call it a new delivery again.
func TestWatchInboxOutputSuppressesLiveDuplicate(t *testing.T) {
	f := newDisplayFixture(t)
	out := new(hookWatchOutput)
	stream, stop := f.watch(t, out, true)
	ev := f.delivery()
	for i := 0; i < 2; i++ {
		var manual bytes.Buffer
		if code, stderr := runDisplayInbox(t, f.env, &manual, "--json", "inbox", "--since", "e1.0"); code != 0 || !strings.Contains(manual.String(), "live delivery") {
			t.Fatalf("explicit inbox did not print: %d %s", code, stderr)
		}
	}
	stream.frames <- []envelope.Event{ev}
	if cursor := f.ack(t); cursor != ev.Cursor {
		t.Fatalf("ack=%q", cursor)
	}
	stop()
	if out.String() != "" {
		t.Fatalf("live monitor duplicated successful inbox output: %q", out.String())
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
	if cur := f.ack(t); cur != ev.Cursor {
		t.Fatalf("hook did not acknowledge verified prior display: %s", cur)
	}
}

// The hook must retain its safe poll/ack position before an unsent claim.
// A later receipt, delivery, or full page must not cumulatively acknowledge it,
// including on a subsequent hook invocation. Keep the reservation intact.
func TestHookPendingClaimBlocksCumulativeAck(t *testing.T) {
	for _, scenario := range []string{"alone", "later-events", "next-page", "safe-prefix", "claim-outlives-shown"} {
		t.Run(scenario, func(t *testing.T) {
			f := newDisplayFixture(t)
			failed := &failedDisplay{tried: make(chan struct{})}
			stream, stop := f.watch(t, failed, true)
			ev := f.delivery()
			stream.frames <- []envelope.Event{ev}
			select {
			case <-stream.closed:
			case <-time.After(5 * time.Second):
				t.Fatal("failed monitor did not end")
			}
			if code := stop(); code == 0 {
				t.Fatal("failed monitor unexpectedly succeeded")
			}
			select {
			case cur := <-f.acks:
				t.Fatalf("failed monitor acknowledged %s", cur)
			default:
			}
			claimed, errs := runHook(t, f.env, "--event", "app-server", "--claim", "unsent", "--priorities", "wake_idle", "--expect-hub", f.hub, "--expect-caller", "peer_test")
			if !strings.Contains(claimed, "dly_1") {
				t.Fatalf("claim failed: %s %s", claimed, errs)
			}
			if scenario == "claim-outlives-shown" {
				if err := (stateFile{f.path}).update(t.Context(), func(s *hookState) error {
					for i := 0; i < shownWindow; i++ {
						s.claim(fmt.Sprintf("dly_other_%d", i))
					}
					return nil
				}); err != nil {
					t.Fatal(err)
				}
			}
			f.mu.Lock()
			if scenario != "alone" {
				f.evs = append(f.evs, envelope.Event{Cursor: "e1.2", Type: "receipt", Data: json.RawMessage(`{"state":"processed"}`)}, hookEvent(3, "later delivery"))
			}
			if scenario == "next-page" {
				for i := 4; i <= hookPageLimit+1; i++ {
					f.evs = append(f.evs, envelope.Event{Cursor: fmt.Sprintf("e1.%d", i), Type: "receipt", Data: json.RawMessage(`{"state":"processed"}`)})
				}
			}
			wantPoll := "e1.0"
			if scenario == "safe-prefix" {
				wantPoll = "opaque-safe-prefix"
				f.evs = append([]envelope.Event{{Cursor: wantPoll, Type: "receipt", Data: json.RawMessage(`{"state":"processed"}`)}}, f.evs...)
			}
			last := f.evs[len(f.evs)-1].Cursor
			f.polls = nil
			f.mu.Unlock()
			for attempt := 0; attempt < 2; attempt++ {
				got, errs := runHook(t, f.env, "--event", "user-prompt", "--auto-ack")
				if strings.Contains(got, "live delivery") {
					t.Errorf("hook duplicated unsent reservation: %q; stderr=%q", got, errs)
				}
				if scenario != "alone" && attempt == 0 && !strings.Contains(got, "later delivery") {
					t.Errorf("hook suppressed unrelated later delivery: %q; stderr=%q", got, errs)
				}
				if attempt == 1 && got != "" {
					t.Errorf("hook duplicated later display while blocked: %q", got)
				}
				if scenario == "safe-prefix" && attempt == 0 {
					if cur := f.ack(t); cur != wantPoll {
						t.Errorf("safe prefix ack=%s", cur)
					}
				}
				select {
				case cur := <-f.acks:
					t.Errorf("hook acknowledged %s without display of unsent reservation", cur)
				default:
				}
				s := readHookState(t, f.path)
				if s.AckAfter != wantPoll || slices.Contains(s.Displayed, "dly_1") || len(s.Claims) != 1 || s.Claims[0].Claim != "unsent" {
					t.Fatalf("hook crossed pending reservation or changed its ownership: safe-after=%s poll=%s displayed=%v claims=%v", s.AckAfter, s.PollCursor, s.Displayed, s.Claims)
				}
			}
			f.mu.Lock()
			polls := slices.Clone(f.polls)
			f.mu.Unlock()
			if len(polls) != 2 || polls[0] != "e1.0" || polls[1] != wantPoll {
				t.Fatalf("subsequent hook advanced to a later page: polls=%v", polls)
			}
			// Verified monitor output unblocks the watermark, but does not
			// silently cancel the engine's pending claim (integration-owned).
			good := new(hookWatchOutput)
			if err := seedPriorWatchDisplay(t.Context(), stateFile{f.path}, "dly_1", func() error {
				_, err := good.Write([]byte("live delivery"))
				return err
			}); err != nil {
				t.Fatal(err)
			}
			if good.String() != "live delivery" {
				t.Fatal("no verified replacement display")
			}
			for page := 0; page < 2; page++ {
				got, errs := runHook(t, f.env, "--event", "user-prompt", "--auto-ack")
				if strings.Contains(got, "live delivery") {
					t.Fatalf("hook duplicated verified display: %s %s", got, errs)
				}
				cur := f.ack(t)
				if cur == last {
					if s := readHookState(t, f.path); s.AckAfter != "" || len(s.Claims) != 1 {
						t.Fatalf("resolved barrier was retained or pending claim cancelled: %+v", s)
					}
					break
				}
				if page == 1 || cur != fmt.Sprintf("e1.%d", hookPageLimit) {
					t.Fatalf("unexpected resumed page ack=%s, want final cursor %s within two pages", cur, last)
				}
			}
		})
	}
}

// A non-auto-ack invocation must not let a later auto-ack run cross two
// reservations, even as monitor proof resolves them one at a time.
func TestHookPendingClaimsKeepEarliestAckBarrier(t *testing.T) {
	for _, firstRun := range []bool{false, true} {
		t.Run(fmt.Sprintf("first-run=%t", firstRun), func(t *testing.T) {
			f := newDisplayFixture(t)
			one := f.delivery()
			two := hookEvent(2, "second pending delivery")
			var body map[string]any
			if err := json.Unmarshal(two.Data, &body); err != nil {
				t.Fatal(err)
			}
			body["message"].(map[string]any)["priority"] = "wake_idle"
			two.Data, _ = json.Marshal(body)
			f.mu.Lock()
			f.evs = []envelope.Event{one, two, {Cursor: "e1.3", Type: "receipt", Data: json.RawMessage(`{"state":"processed"}`)}}
			f.mu.Unlock()
			claimed, errs := runHook(t, f.env, "--event", "app-server", "--claim", "two_pending", "--priorities", "wake_idle", "--expect-hub", f.hub, "--expect-caller", "peer_test")
			if !strings.Contains(claimed, "dly_1") || !strings.Contains(claimed, "dly_2") {
				t.Fatalf("claims failed: %s %s", claimed, errs)
			}
			if firstRun {
				if err := (stateFile{f.path}).update(t.Context(), func(s *hookState) error { s.PollCursor = ""; return nil }); err != nil {
					t.Fatal(err)
				}
			}
			if got, _ := runHook(t, f.env, "--event", "user-prompt"); got != "" {
				t.Fatalf("hook duplicated reservations: %s", got)
			}
			for _, delivery := range []string{"dly_1", "dly_2"} {
				if got, _ := runHook(t, f.env, "--event", "user-prompt", "--auto-ack"); got != "" {
					t.Fatalf("hook duplicated pending delivery: %s", got)
				}
				select {
				case cur := <-f.acks:
					t.Fatalf("hook crossed pending %s with ack=%s", delivery, cur)
				default:
				}
				if err := seedPriorWatchDisplay(t.Context(), stateFile{f.path}, delivery, func() error { _, err := io.WriteString(new(bytes.Buffer), delivery); return err }); err != nil {
					t.Fatal(err)
				}
				if got, _ := runHook(t, f.env, "--event", "user-prompt", "--auto-ack"); got != "" {
					t.Fatalf("hook duplicated monitor proof: %s", got)
				}
				want := "e1.1"
				if delivery == "dly_2" {
					want = "e1.3"
				}
				if cur := f.ack(t); cur != want {
					t.Fatalf("ack=%s want=%s", cur, want)
				}
				s := readHookState(t, f.path)
				if len(s.Claims) != 2 {
					t.Fatalf("hook changed engine claim ownership: %+v", s.Claims)
				}
				if delivery == "dly_1" && s.AckAfter != "e1.1" {
					t.Fatalf("lost second barrier: %+v", s)
				}
				if delivery == "dly_2" && s.AckAfter != "" {
					t.Fatalf("retained resolved barrier: %+v", s)
				}
			}
		})
	}
}

// An ordinary first-run summary can jump past A before an app-server with
// an older independent cursor claims it. Neither the summary nor that claim
// proves individual display, including when the claim comes from replay.
func TestHookClaimCreatedAfterFirstRunKeepsAckBarrier(t *testing.T) {
	for _, scenario := range []string{"later-events", "next-page", "empty-page", "replay"} {
		t.Run(scenario, func(t *testing.T) {
			f := newDisplayFixture(t)
			failed := &failedDisplay{tried: make(chan struct{})}
			stream, stop := f.watch(t, failed, true)
			if err := (stateFile{f.path}).update(t.Context(), func(s *hookState) error { s.PollCursor = ""; return nil }); err != nil {
				t.Fatal(err)
			}
			ev := f.delivery()
			if scenario == "replay" {
				got, errs := runHook(t, f.env, "--event", "app-server", "--claim", "defer_idle", "--priorities", "wake", "--expect-hub", f.hub, "--expect-caller", "peer_test")
				s := readHookState(t, f.path)
				if len(s.Replay) != 1 || s.Replay[0].ID != "dly_1" || len(s.Claims) != 0 {
					t.Fatalf("did not defer A to replay: state=%+v output=%s stderr=%s", s, got, errs)
				}
			}
			got, errs := runHook(t, f.env, "--event", "user-prompt", "--auto-ack")
			s := readHookState(t, f.path)
			if !strings.Contains(got, "1 unacked deliveries waiting") || strings.Contains(got, "live delivery") || s.PollCursor != ev.Cursor || slices.Contains(s.Displayed, "dly_1") {
				t.Fatalf("first hook did not summarize without individual display: output=%q stderr=%s state=%+v", got, errs, s)
			}
			select {
			case cur := <-f.acks:
				t.Fatalf("first-run summary acknowledged %s", cur)
			default:
			}
			stream.frames <- []envelope.Event{ev}
			select {
			case <-stream.closed:
			case <-time.After(5 * time.Second):
				t.Fatal("failed monitor did not end")
			}
			if code := stop(); code == 0 {
				t.Fatal("failed monitor unexpectedly succeeded")
			}
			select {
			case cur := <-f.acks:
				t.Fatalf("failed monitor acknowledged %s", cur)
			default:
			}
			claimed, errs := runHook(t, f.env, "--event", "app-server", "--claim", "unsent", "--priorities", "wake_idle", "--expect-hub", f.hub, "--expect-caller", "peer_test")
			s = readHookState(t, f.path)
			if !strings.Contains(claimed, "dly_1") || len(s.Claims) != 1 || s.Claims[0].After != "e1.0" || s.Claims[0].Claim != "unsent" || s.PollCursor != ev.Cursor {
				t.Fatalf("late claim failed: output=%s stderr=%s state=%+v", claimed, errs, s)
			}
			f.mu.Lock()
			if scenario != "empty-page" {
				f.evs = append(f.evs, envelope.Event{Cursor: "e1.2", Type: "receipt", Data: json.RawMessage(`{"state":"processed"}`)}, hookEvent(3, "later delivery"))
			}
			if scenario == "next-page" {
				for i := 4; i <= hookPageLimit+1; i++ {
					f.evs = append(f.evs, envelope.Event{Cursor: fmt.Sprintf("e1.%d", i), Type: "receipt", Data: json.RawMessage(`{"state":"processed"}`)})
				}
			}
			last := f.evs[len(f.evs)-1].Cursor
			f.mu.Unlock()
			for attempt := 0; attempt < 2; attempt++ {
				got, errs := runHook(t, f.env, "--event", "user-prompt", "--auto-ack")
				if strings.Contains(got, "live delivery") || (attempt == 1 && got != "") {
					t.Errorf("hook duplicated a reservation or later output: %q stderr=%s", got, errs)
				}
				if scenario != "empty-page" && attempt == 0 && !strings.Contains(got, "later delivery") {
					t.Errorf("hook suppressed unrelated later output: %q stderr=%s", got, errs)
				}
				select {
				case cur := <-f.acks:
					t.Errorf("hook acknowledged %s across late unsent claim A", cur)
				default:
				}
				s = readHookState(t, f.path)
				if s.AckAfter != "e1.0" || slices.Contains(s.Displayed, "dly_1") || len(s.Claims) != 1 || s.Claims[0].Claim != "unsent" {
					t.Errorf("lost durable predecessor, claimed display, or changed ownership: %+v", s)
				}
			}
			good := new(bytes.Buffer)
			if err := seedPriorWatchDisplay(t.Context(), stateFile{f.path}, "dly_1", func() error { _, err := io.WriteString(good, "live delivery"); return err }); err != nil {
				t.Fatal(err)
			}
			if good.String() != "live delivery" {
				t.Fatal("no verified replacement display")
			}
			for page := 0; page < 2; page++ {
				if got, errs := runHook(t, f.env, "--event", "user-prompt", "--auto-ack"); got != "" {
					t.Fatalf("hook repeated verified output: %s stderr=%s", got, errs)
				}
				cur := f.ack(t)
				if cur == last {
					s = readHookState(t, f.path)
					if s.AckAfter != "" || len(s.Claims) != 1 || s.Claims[0].Claim != "unsent" {
						t.Fatalf("resolved barrier retained or ownership changed: %+v", s)
					}
					break
				}
				if page == 1 || cur != fmt.Sprintf("e1.%d", hookPageLimit) {
					t.Fatalf("resumed ack=%s want=%s within two pages", cur, last)
				}
			}
		})
	}
}

// Replay can introduce a claim on either side of an existing barrier. Merge
// by stream order, keeping the older safe position even with opaque cursors.
func TestHookLateReplayClaimReconcilesAckBarrier(t *testing.T) {
	for _, lateEarlier := range []bool{false, true} {
		t.Run(fmt.Sprintf("late-claim-earlier=%t", lateEarlier), func(t *testing.T) {
			f := newDisplayFixture(t)
			one := f.delivery()
			one.Cursor = "opaque-z-first"
			two := hookEvent(3, "second pending delivery")
			two.Cursor = "opaque-b-second"
			for i, ev := range []*envelope.Event{&one, &two} {
				priority := "wake"
				if (i == 0) == lateEarlier {
					priority = "wake_idle"
				}
				var body map[string]any
				if err := json.Unmarshal(ev.Data, &body); err != nil {
					t.Fatal(err)
				}
				body["message"].(map[string]any)["priority"] = priority
				ev.Data, _ = json.Marshal(body)
			}
			f.mu.Lock()
			f.evs = []envelope.Event{one, {Cursor: "opaque-a-between", Type: "receipt", Data: json.RawMessage(`{"state":"processed"}`)}, two, hookEvent(4, "unrelated later delivery")}
			f.mu.Unlock()
			if err := (stateFile{f.path}).update(t.Context(), func(s *hookState) error { s.PollCursor = ""; return nil }); err != nil {
				t.Fatal(err)
			}
			// The first app-server claims just the wake item and retains the
			// wake_idle item in replay, preserving its original log order.
			runHook(t, f.env, "--event", "app-server", "--claim", "first", "--priorities", "wake", "--expect-hub", f.hub, "--expect-caller", "peer_test")
			runHook(t, f.env, "--event", "user-prompt", "--auto-ack")
			runHook(t, f.env, "--event", "app-server", "--claim", "late", "--priorities", "wake_idle", "--expect-hub", f.hub, "--expect-caller", "peer_test")
			for attempt := 0; attempt < 2; attempt++ {
				got, errs := runHook(t, f.env, "--event", "user-prompt", "--auto-ack")
				if strings.Contains(got, "live delivery") || strings.Contains(got, "second pending delivery") || (attempt == 0 && !strings.Contains(got, "unrelated later delivery")) || (attempt == 1 && got != "") {
					t.Errorf("incorrect hook output: %s stderr=%s", got, errs)
				}
				select {
				case cur := <-f.acks:
					t.Errorf("hook acknowledged %s across pending claim", cur)
				default:
				}
				s := readHookState(t, f.path)
				if s.AckAfter != "e1.0" || len(s.Claims) != 2 || len(s.Displayed) != 1 || s.Displayed[0] != "dly_4" {
					t.Errorf("lost earlier barrier, claim ownership, or display identity: %+v", s)
				}
			}
			for i, delivery := range []string{"dly_1", "dly_3"} {
				if err := seedPriorWatchDisplay(t.Context(), stateFile{f.path}, delivery, func() error { _, err := io.WriteString(new(bytes.Buffer), delivery); return err }); err != nil {
					t.Fatal(err)
				}
				runHook(t, f.env, "--event", "user-prompt", "--auto-ack")
				want := "opaque-a-between"
				if i == 1 {
					want = "e1.4"
				}
				if cur := f.ack(t); cur != want {
					t.Fatalf("resumed ack=%s want=%s", cur, want)
				}
				if s := readHookState(t, f.path); len(s.Claims) != 2 || (i == 1 && s.AckAfter != "") {
					t.Fatalf("resumption changed ownership or retained resolved barrier: %+v", s)
				}
			}
		})
	}
}

func TestHookLateClaimBeyondPageRetainsEarlierAckBarrier(t *testing.T) {
	f := newDisplayFixture(t)
	f.delivery()
	// An earlier legacy reservation is a barrier even without a Claims row.
	if err := (stateFile{f.path}).update(t.Context(), func(s *hookState) error { s.claim("dly_1"); return nil }); err != nil {
		t.Fatal(err)
	}
	runHook(t, f.env, "--event", "user-prompt", "--auto-ack")
	f.mu.Lock()
	for i := 2; i <= hookPageLimit+1; i++ {
		f.evs = append(f.evs, envelope.Event{Cursor: fmt.Sprintf("e1.%d", i), Type: "receipt", Data: json.RawMessage(`{"state":"processed"}`)})
	}
	late := hookEvent(hookPageLimit+2, "later pending delivery")
	var body map[string]any
	if err := json.Unmarshal(late.Data, &body); err != nil {
		t.Fatal(err)
	}
	body["message"].(map[string]any)["priority"] = "wake_idle"
	late.Data, _ = json.Marshal(body)
	f.evs = append(f.evs, late, hookEvent(hookPageLimit+3, "unrelated delivery"))
	f.mu.Unlock()
	for page := 0; page < 2; page++ {
		runHook(t, f.env, "--event", "app-server", "--claim", "late", "--priorities", "wake_idle", "--expect-hub", f.hub, "--expect-caller", "peer_test")
	}
	for attempt := 0; attempt < 2; attempt++ {
		if got, errs := runHook(t, f.env, "--event", "user-prompt", "--auto-ack"); got != "" {
			t.Errorf("hook duplicated reservation or jumped a page: %s stderr=%s", got, errs)
		}
		select {
		case cur := <-f.acks:
			t.Errorf("later claim discarded earlier barrier: ack=%s", cur)
		default:
		}
		if s := readHookState(t, f.path); s.AckAfter != "e1.0" || len(s.Claims) != 1 || s.Claims[0].ID != fmt.Sprintf("dly_%d", hookPageLimit+2) {
			t.Fatalf("earlier barrier or later claim lost: %+v", s)
		}
	}
	for _, delivery := range []string{"dly_1", fmt.Sprintf("dly_%d", hookPageLimit+2)} {
		if err := seedPriorWatchDisplay(t.Context(), stateFile{f.path}, delivery, func() error { _, err := io.WriteString(new(bytes.Buffer), delivery); return err }); err != nil {
			t.Fatal(err)
		}
		for _, want := range map[string][]string{
			"dly_1":                                {fmt.Sprintf("e1.%d", hookPageLimit), fmt.Sprintf("e1.%d", hookPageLimit+1)},
			fmt.Sprintf("dly_%d", hookPageLimit+2): {fmt.Sprintf("e1.%d", hookPageLimit+3)},
		}[delivery] {
			runHook(t, f.env, "--event", "user-prompt", "--auto-ack")
			if cur := f.ack(t); cur != want {
				t.Fatalf("resumed ack=%s want=%s", cur, want)
			}
		}
	}
	if s := readHookState(t, f.path); s.AckAfter != "" || len(s.Claims) != 1 {
		t.Fatalf("resolved barrier retained or ownership lost: %+v", s)
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

// seedPriorWatchDisplay supplies the retained positive-proof/active-claim
// combination that the hook barrier regressions have always exercised. Strict
// monitors cannot create it from a pending reservation; older persisted proof
// still has to reconcile the hook's original opaque predecessor correctly.
func seedPriorWatchDisplay(ctx context.Context, f stateFile, delivery string, display func() error) error {
	return f.update(ctx, func(s *hookState) error {
		if err := display(); err != nil {
			return err
		}
		s.recordDisplay(delivery)
		return nil
	})
}

// Task5a3 primary tests: written before product changes or executable checks.
// The only fake boundaries are hub HTTP/SSE and time. Claims, releases and
// receipts use the real CLI and the same on-disk Go transaction as the monitor.
const deferredBefore = "opaque-z-before"

type deferredRequest struct {
	after string
	sse   bool
}

type deferredTick struct {
	delay    time.Duration
	resume   chan struct{}
	cursor   string
	ack      string
	seen     int
	replayed bool
	requests []deferredRequest
}

type deferredWatch struct {
	f             *displayFixture
	w             *watcher
	out           *hookWatchOutput
	errs          *hookWatchOutput
	ticks         chan deferredTick
	acked         chan string
	done          chan error
	cancel        context.CancelFunc
	requests      []deferredRequest // accessed by the watcher, or while it is paused
	closed        atomic.Int32
	mode          string
	outputFailure bool
}

// The body remains open after its initial frames. Closing a deferred stream
// must cancel that transport, rather than leave its reader or a suffix alive.
type deferredSSEBody struct {
	ctx    context.Context
	cancel context.CancelFunc
	head   *strings.Reader
	closed *atomic.Int32
}

func (b *deferredSSEBody) Read(p []byte) (int, error) {
	if b.head.Len() != 0 {
		return b.head.Read(p)
	}
	<-b.ctx.Done()
	return 0, b.ctx.Err()
}

func (b *deferredSSEBody) Close() error {
	b.closed.Add(1)
	b.cancel()
	return nil
}

func deferredEvent(n int, text, cursor string) envelope.Event {
	ev := hookEvent(n, text)
	ev.Cursor = cursor
	var body map[string]any
	_ = json.Unmarshal(ev.Data, &body)
	body["message"].(map[string]any)["priority"] = "wake_idle"
	ev.Data, _ = json.Marshal(body)
	return ev
}

func deferredClaim(t *testing.T, f *displayFixture, nonce string) {
	t.Helper()
	raw, errs := runHook(t, f.env, "--event", "app-server", "--claim", nonce,
		"--priorities", "wake_idle", "--expect-hub", f.hub, "--expect-caller", "peer_test")
	if !strings.Contains(raw, `"dly_1"`) || !strings.Contains(raw, `"lifecycle":"receipt-display-v1"`) {
		t.Fatalf("real Go reservation failed: output=%q stderr=%q", raw, errs)
	}
}

func deferredReceipt(t *testing.T, f *displayFixture, nonce string) {
	t.Helper()
	raw, _ := runHook(t, f.env, "--release", "--claim", nonce,
		"--receipt-key", "vn.original", "--delivery-ids", "dly_1",
		"--expect-hub", f.hub, "--expect-caller", "peer_test")
	receiptConfirmation(t, raw, "vn.original", "dly_1")
}

func deferredRelease(t *testing.T, f *displayFixture, nonce string, want int) {
	t.Helper()
	raw, errs := runHook(t, f.env, "--release", "--claim", nonce)
	var result struct {
		Released *int `json:"released"`
	}
	if json.Unmarshal([]byte(raw), &result) != nil || result.Released == nil || *result.Released != want {
		t.Fatalf("ordinary release: output=%q stderr=%q, want released=%d", raw, errs, want)
	}
}

func newDeferredWatch(t *testing.T, mode string, claimed bool, suffix int) *deferredWatch {
	t.Helper()
	f := newDisplayFixture(t)
	one := deferredEvent(1, "retained A", "opaque-y-A")
	f.evs = []envelope.Event{one}
	if err := (stateFile{f.path}).update(t.Context(), func(s *hookState) error {
		s.PollCursor, s.AppCursor = deferredBefore, deferredBefore
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if claimed {
		// The real Go child has durably reserved A; the engine is paused
		// before any RPC. Nothing in this fixture accepts or displays it.
		deferredClaim(t, f, "pending")
	}
	for n := 2; n < suffix+2; n++ {
		f.evs = append(f.evs, deferredEvent(n, fmt.Sprintf("suffix %d", n), fmt.Sprintf("opaque-a-suffix-%d", n)))
	}
	return deferredWatchFor(t, f, mode)
}

func deferredWatchFor(t *testing.T, f *displayFixture, mode string) *deferredWatch {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	h := &deferredWatch{f: f, out: new(hookWatchOutput), errs: new(hookWatchOutput),
		ticks: make(chan deferredTick), acked: make(chan string, 32), done: make(chan error, 1), cancel: cancel, mode: mode}
	a := &app{ctx: ctx, stdin: strings.NewReader(""), stdout: h.out, stderr: h.errs,
		getenv: func(k string) string { return f.env[k] }}
	c, name, ch, err := a.connect()
	if err != nil {
		t.Fatal(err)
	}
	id := a.resolveIdentity(c, nil)
	if id.Caller != "peer_test" {
		t.Fatal("fixture caller is not verified")
	}
	// Bind fresh fixture state through the same writer, before introducing
	// any corrupt/foreign/busy state in the individual tests.
	if err := (stateFile{f.path}).update(ctx, func(s *hookState) error { s.bind(id); return nil }); err != nil {
		t.Fatal(err)
	}
	h.w = &watcher{a: a, c: c, ch: ch, name: name, names: map[string]string{},
		forMonitor: true, autoAck: true, identityOff: true, initialized: true,
		identity: &id, shown: &stateFile{f.path}, cursor: deferredBefore,
		replayed: mode != "replay", seen: newDedupe(dedupeWindow), retry: time.Second}
	h.w.sleep = func(ctx context.Context, d time.Duration) bool {
		step := deferredTick{delay: d, resume: make(chan struct{}), cursor: h.w.cursor,
			ack: h.w.pendingAck, seen: len(h.w.seen.set), replayed: h.w.replayed,
			requests: slices.Clone(h.requests)}
		select {
		case h.ticks <- step:
		case <-ctx.Done():
			return false
		}
		select {
		case <-step.resume:
			return true
		case <-ctx.Done():
			return false
		}
	}
	transport := c.hc.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	sseAttempts := 0
	c.hc.Transport = relayRoundTrip(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/v1/ack" {
			raw, err := io.ReadAll(r.Body)
			if err != nil {
				return nil, err
			}
			r.Body.Close()
			r.Body = io.NopCloser(bytes.NewReader(raw))
			var ack struct{ Cursor string }
			if err := json.Unmarshal(raw, &ack); err != nil {
				return nil, err
			}
			resp, err := transport.RoundTrip(r)
			if err == nil && resp.StatusCode == http.StatusNoContent {
				h.acked <- ack.Cursor
			}
			return resp, err
		}
		if r.URL.Path != "/v1/events" {
			return transport.RoundTrip(r)
		}
		sse := r.Header.Get("Accept") == "text/event-stream"
		after := r.URL.Query().Get("after")
		if sse && r.Header.Get("Last-Event-ID") != "" {
			after = r.Header.Get("Last-Event-ID")
		}
		h.requests = append(h.requests, deferredRequest{after, sse})
		if mode == "poll" && sse && sseAttempts < watchSSEFailures {
			sseAttempts++
			return &http.Response{StatusCode: 503, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("unavailable"))}, nil
		}
		f.mu.Lock()
		events := slices.Clone(f.evs)
		f.mu.Unlock()
		if after == "opaque-page-tail" {
			events = nil
		} else if at := slices.IndexFunc(events, func(ev envelope.Event) bool { return ev.Cursor == after }); at >= 0 {
			events = events[at+1:]
		} else if after != deferredBefore {
			return nil, fmt.Errorf("unexpected opaque resume position %q", after)
		}
		if !sse {
			raw, _ := json.Marshal(pollBody{Events: events, NextCursor: "opaque-page-tail"})
			return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(bytes.NewReader(raw))}, nil
		}
		var frames strings.Builder
		if len(events) == 0 {
			fmt.Fprintf(&frames, "id: %s\n\n", after)
		}
		for _, ev := range events {
			raw, _ := json.Marshal(ev)
			fmt.Fprintf(&frames, "id: %s\nevent: %s\ndata: %s\n\n", ev.Cursor, ev.Type, raw)
		}
		bodyCtx, bodyCancel := context.WithCancel(r.Context())
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}},
			Body: &deferredSSEBody{bodyCtx, bodyCancel, strings.NewReader(frames.String()), &h.closed}}, nil
	})
	t.Cleanup(func() { cancel() })
	return h
}

func (h *deferredWatch) start(t *testing.T) deferredTick {
	t.Helper()
	go func() { h.done <- h.w.run() }()
	t.Cleanup(func() { h.stop(t) })
	if h.mode == "poll" {
		// Reach the real fallback, rather than calling a mocked poll helper.
		for i := 0; i < watchSSEFailures; i++ {
			step := h.next(t)
			close(step.resume)
		}
	}
	return h.next(t)
}

func (h *deferredWatch) next(t *testing.T) deferredTick {
	t.Helper()
	select {
	case step := <-h.ticks:
		return step
	case cur := <-h.f.acks:
		t.Fatalf("missing deferral: monitor cumulatively acknowledged %q while A remained unresolved (output bytes=%d)", cur, len(h.out.String()))
	case err := <-h.done:
		h.done <- err
		t.Fatalf("monitor ended instead of retaining unresolved A: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatalf("missing local deferred retry (output bytes=%d)", len(h.out.String()))
	}
	return deferredTick{}
}

func (h *deferredWatch) blocked(t *testing.T, step deferredTick, delay time.Duration) {
	t.Helper()
	if step.delay != delay || step.cursor != deferredBefore || step.ack != "" || step.seen != 0 || (h.mode == "replay" && step.replayed) {
		t.Fatalf("crossed blocked opaque position: delay=%s want=%s cursor=%q ack=%q seen=%d replayed=%t",
			step.delay, delay, step.cursor, step.ack, step.seen, step.replayed)
	}
	if h.out.String() != "" {
		t.Fatalf("pending/uncertain state authorized monitor output (%d bytes)", len(h.out.String()))
	}
	select {
	case cur := <-h.f.acks:
		t.Fatalf("crossed unresolved A with cumulative ack %q", cur)
	default:
	}
}

func (h *deferredWatch) finish(t *testing.T, step deferredTick, wantCursor string, printsA bool) {
	t.Helper()
	close(step.resume)
	h.complete(t, wantCursor)
	if got := strings.Count(h.out.String(), "retained A"); got != boolCount(printsA) {
		t.Fatalf("retained A prints=%d want=%d", got, boolCount(printsA))
	}
}

func (h *deferredWatch) complete(t *testing.T, wantCursor string) {
	t.Helper()
	for {
		select {
		case cur := <-h.acked:
			if cur == wantCursor {
				h.stop(t)
				return
			}
			if cur != "opaque-y-A" {
				t.Fatalf("unexpected resume acknowledgement %q", cur)
			}
		case next := <-h.ticks:
			// A transport reconnect may use its ordinary timing after A is
			// handled; no later retry may retain the blocked predecessor.
			if next.cursor == deferredBefore {
				t.Fatalf("settled retained event did not progress: delay=%s stderr=%s", next.delay, h.errs.String())
			}
			close(next.resume)
		case err := <-h.done:
			h.done <- err
			t.Fatalf("monitor stopped before completion: %v", err)
		case <-time.After(5 * time.Second):
			t.Fatal("retained event did not complete locally after state settlement")
		}
	}
}

func boolCount(b bool) int {
	if b {
		return 1
	}
	return 0
}

func (h *deferredWatch) stop(t *testing.T) {
	t.Helper()
	h.cancel()
	select {
	case err := <-h.done:
		h.done <- err // permit explicit stop and Cleanup to share the result
		if err != nil && !(h.outputFailure && errors.Is(err, errWatchOutput)) {
			t.Errorf("monitor stopped with error: %v (stderr=%s)", err, h.errs.String())
		}
	case <-time.After(time.Second):
		t.Error("cancellation did not drain deferred timer/transport promptly")
	}
}

func TestWatchDeferredReceipt(t *testing.T) {
	for _, mode := range []string{"sse", "poll", "replay"} {
		t.Run(mode, func(t *testing.T) {
			h := newDeferredWatch(t, mode, true, 1)
			step := h.start(t)
			h.blocked(t, step, time.Second)
			if s := readHookState(t, h.f.path); len(s.Claims) != 1 || slices.Contains(s.Displayed, "dly_1") || s.Claims[0].After != deferredBefore {
				t.Fatalf("reservation before RPC was mistaken for receipt proof: %+v", s)
			}
			if mode == "sse" && h.closed.Load() == 0 {
				t.Fatal("blocked SSE transport was left open")
			}
			deferredReceipt(t, h.f, "pending")
			// Even an initially replayed A waited for live ownership and must
			// now honor its exact receipt, rather than deliberately replay it.
			h.finish(t, step, "opaque-a-suffix-2", false)
			if strings.Count(h.out.String(), "suffix 2") != 1 {
				t.Fatal("suffix was skipped or duplicated after receipt")
			}
			for _, req := range h.requests[len(step.requests):] {
				if req.after == deferredBefore {
					t.Fatal("receipt recovery refetched A instead of resuming its exact cursor")
				}
			}
		})
	}
}

func TestWatchDeferredReleaseWithoutNotification(t *testing.T) {
	for _, mode := range []string{"sse", "poll", "replay"} {
		t.Run(mode, func(t *testing.T) {
			h := newDeferredWatch(t, mode, true, 0)
			step := h.start(t)
			h.blocked(t, step, time.Second)
			deferredRelease(t, h.f, "pending", 1)
			// No hub frame, new event or notification is produced here.
			h.finish(t, step, "opaque-y-A", true)
			if s := readHookState(t, h.f.path); !slices.Contains(s.Displayed, "dly_1") || len(s.Claims) != 0 {
				t.Fatal("released retained output did not publish positive proof")
			}
			if got, _ := runHook(t, h.f.env, "--event", "user-prompt"); got != "" {
				t.Fatal("ordinary hook duplicated monitor output after release")
			}
		})
	}
}

func TestWatchDeferredReleaseRefusalAndReclaim(t *testing.T) {
	h := newDeferredWatch(t, "sse", true, 0)
	step := h.start(t)
	h.blocked(t, step, time.Second)
	deferredRelease(t, h.f, "wrong_nonce", 0)
	close(step.resume)
	step = h.next(t)
	h.blocked(t, step, 2*time.Second)
	if err := (stateFile{h.f.path}).update(t.Context(), func(s *hookState) error {
		for n := 0; n < appServerReplayLimit; n++ {
			s.Replay = append(s.Replay, replayRef{ID: fmt.Sprintf("capacity_%d", n), After: deferredBefore, Priority: "wake_idle", Order: uint64(n + 10)})
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(h.f.path)
	if err != nil {
		t.Fatal(err)
	}
	if raw, _ := runHook(t, h.f.env, "--release", "--claim", "pending"); raw != "" {
		t.Fatal("capacity-refused release incorrectly confirmed success")
	}
	after, _ := os.ReadFile(h.f.path)
	if !bytes.Equal(before, after) {
		t.Fatal("failed release changed durable ownership")
	}
	close(step.resume)
	step = h.next(t)
	h.blocked(t, step, 4*time.Second)
	if err := (stateFile{h.f.path}).update(t.Context(), func(s *hookState) error { s.Replay = nil; return nil }); err != nil {
		t.Fatal(err)
	}
	deferredRelease(t, h.f, "pending", 1)
	deferredClaim(t, h.f, "reclaimed")
	close(step.resume)
	step = h.next(t)
	h.blocked(t, step, 8*time.Second)
	if len(step.requests) != 1 {
		t.Fatal("unresolved release/reclaim resumed the transport")
	}
	deferredReceipt(t, h.f, "reclaimed")
	h.finish(t, step, "opaque-y-A", false)
}

func TestWatchDeferredUncertainState(t *testing.T) {
	for _, problem := range []string{"lock", "corrupt", "read-error", "missing", "hub", "caller", "as", "credential", "unverified", "no-identity", "no-state"} {
		t.Run(problem, func(t *testing.T) {
			h := newDeferredWatch(t, "sse", false, 0)
			raw, err := os.ReadFile(h.f.path)
			if err != nil {
				t.Fatal(err)
			}
			restore := func() {
				if err := os.WriteFile(h.f.path, raw, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			switch problem {
			case "lock":
				unlock, err := lockPath(t.Context(), h.f.path+".lock")
				if err != nil {
					t.Fatal(err)
				}
				var once sync.Once
				restore = func() { once.Do(unlock) }
				t.Cleanup(restore)
			case "corrupt":
				if err := os.WriteFile(h.f.path, []byte("{broken"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "read-error":
				if err := os.Remove(h.f.path); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(h.f.path, 0o700); err != nil {
					t.Fatal(err)
				}
				restore = func() {
					if err := os.Remove(h.f.path); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(h.f.path, raw, 0o600); err != nil {
						t.Fatal(err)
					}
				}
			case "missing":
				if err := os.Remove(h.f.path); err != nil {
					t.Fatal(err)
				}
			case "unverified", "no-identity":
				id := h.w.identity
				restore = func() { h.w.identity = id }
				if problem == "unverified" {
					h.w.identity = &hookIdentity{Hub: id.Hub, Cred: id.Cred}
				} else {
					h.w.identity = nil
				}
			case "no-state":
				shown := h.w.shown
				restore = func() { h.w.shown = shown }
				h.w.shown = nil
			default:
				if err := (stateFile{h.f.path}).update(t.Context(), func(s *hookState) error {
					switch problem {
					case "hub":
						s.Hub += "/foreign"
					case "caller":
						s.Caller = "peer_foreign"
					case "as":
						s.As = "peer_foreign"
					case "credential":
						s.Cred = "foreign"
					}
					s.recordDisplay("dly_1")
					return nil
				}); err != nil {
					t.Fatal(err)
				}
			}
			uncertain, _ := os.ReadFile(h.f.path)
			step := h.start(t)
			h.blocked(t, step, time.Second)
			got, _ := os.ReadFile(h.f.path)
			if !bytes.Equal(uncertain, got) {
				t.Fatal("uncertain state was reset/rebound to authorize output")
			}
			restore()
			h.finish(t, step, "opaque-y-A", true)
		})
	}
}

func TestWatchDeferredRetryCapAndCancellation(t *testing.T) {
	h := newDeferredWatch(t, "sse", true, hookPageLimit)
	step := h.start(t)
	for n, seconds := range []int{1, 2, 4, 8, 15, 15, 15} {
		if n > 0 {
			step = h.next(t)
		}
		h.blocked(t, step, time.Duration(seconds)*time.Second)
		if len(step.requests) != 1 {
			t.Fatal("deferred retry reopened transport or accumulated more envelopes")
		}
		if n < 6 {
			close(step.resume)
		}
	}
	h.stop(t) // cancellation interrupts the capped wait without a clock tick
	s := readHookState(t, h.f.path)
	if len(s.Claims) != 1 || s.Claims[0].Claim != "pending" || len(s.Displayed) != 0 {
		t.Fatal("cancel released another engine's ownership or created display proof")
	}
	if raw, _ := os.ReadFile(h.f.path); bytes.Contains(raw, []byte("retained A")) || bytes.Contains(raw, []byte("suffix 2")) {
		t.Fatal("deferred peer bodies were persisted")
	}
}

func TestWatchDeferredConcurrentReleasePrintsOnce(t *testing.T) {
	one := newDeferredWatch(t, "sse", true, 0)
	two := deferredWatchFor(t, one.f, "sse")
	first, second := one.start(t), two.start(t)
	one.blocked(t, first, time.Second)
	two.blocked(t, second, time.Second)
	deferredRelease(t, one.f, "pending", 1)
	close(first.resume)
	close(second.resume)
	// Both connected monitors observed the reservation before release and
	// race the same shared lock when their local timers fire.
	one.complete(t, "opaque-y-A")
	two.complete(t, "opaque-y-A")
	if got := strings.Count(one.out.String()+two.out.String(), "retained A"); got != 1 {
		t.Fatalf("competing retained monitors printed A %d times", got)
	}
}

func TestWatchDeferredReleaseOutputFailure(t *testing.T) {
	for _, mode := range []string{"sse", "poll", "replay"} {
		for _, short := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/short=%t", mode, short), func(t *testing.T) {
				h := newDeferredWatch(t, mode, true, 1)
				h.outputFailure = true
				failed := &failedDisplay{short: short, tried: make(chan struct{})}
				h.w.a.stdout = failed
				step := h.start(t)
				h.blocked(t, step, time.Second)
				deferredRelease(t, h.f, "pending", 1)
				close(step.resume)
				select {
				case <-failed.tried:
				case <-time.After(5 * time.Second):
					t.Fatal("release never retried retained output")
				}
				select {
				case err := <-h.done:
					if !errors.Is(err, errWatchOutput) {
						t.Fatalf("failed retained output: %v", err)
					}
					h.done <- err
				case <-time.After(time.Second):
					t.Fatal("failed output did not stop transport")
				}
				select {
				case cur := <-h.f.acks:
					t.Fatalf("ack %q crossed failed retained output", cur)
				default:
				}
				if s := readHookState(t, h.f.path); len(s.Displayed) != 0 {
					t.Fatal("failed/short retained output published proof")
				}
			})
		}
	}
}

// Task5a3 R34 tests: prepared after the original primary GREEN, before the
// operator correction. These are a separate RED/GREEN tranche, not original
// primary coverage. HTTP identity, CLI output/ack and real shared state are the
// observation seams; no identity/display helper return is mocked.
func setDisplayIdentity(f *displayFixture, raw string) {
	f.identityJSON.Store(&raw)
}

func TestWatchMonitorCredentialRotation(t *testing.T) {
	for _, as := range []string{"", "peer_selected"} {
		t.Run("as="+as, func(t *testing.T) { testWatchMonitorCredentialRotation(t, as) })
	}
}

func testWatchMonitorCredentialRotation(t *testing.T, as string) {
	f := newDisplayFixture(t)
	if as != "" {
		setDisplayIdentity(f, `{"kind":"principal","principal_id":"principal_fixture"}`)
		var err error
		f.path, err = statePath(func(k string) string { return f.env[k] }, "p", as)
		if err != nil {
			t.Fatal(err)
		}
	}
	oldSecret := "vn_peer_test"
	f.expectedBearer.Store(&oldSecret)
	_, stop := f.watch(t, new(hookWatchOutput), false, as)
	stop() // positively authenticate the original token before actual rotation
	file := stateFile{f.path}
	if err := file.update(t.Context(), func(s *hookState) error {
		s.Claims = []claimRef{{ID: "dly_pending", After: "opaque-predecessor", Priority: "wake_idle", Claim: "held", Order: 3}}
		s.Replay = []replayRef{{ID: "dly_replay", After: "opaque-replay", Priority: "wake_idle", Order: 2}}
		s.Displayed, s.Shown = []string{"dly_1"}, []string{"dly_1", "dly_pending"}
		s.AckAfter, s.PollCursor, s.AppCursor = "opaque-fence", "opaque-poll", "e1.0"
		s.LastPostToolPoll = time.Now().UTC()
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	before := readHookState(t, f.path)
	session, _ := json.Marshal(hookBinding{Session: "fixture-retained", SessionHow: bindDeclared, SessionAt: before.LastPostToolPoll, RebindUntil: before.LastPostToolPoll.Add(time.Minute), Noticed: []string{"fixture-foreign"}})
	if err := os.WriteFile(bindingPath(f.path), session, 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadConfig(f.env["VECNET_CONFIG"])
	if err != nil {
		t.Fatal(err)
	}
	p := cfg.Profiles["p"]
	p.Secret = "vn_peer_rotated"
	cfg.Profiles["p"] = p
	if err := saveConfig(f.env["VECNET_CONFIG"], cfg); err != nil {
		t.Fatal(err)
	}
	f.expectedBearer.Store(&p.Secret) // A now fails actual /peers/me; B identifies the same caller.
	out := new(hookWatchOutput)
	stream, stopRotated := f.watch(t, out, false, as)
	refreshed := readHookState(t, f.path)
	want := before
	want.Cred = hookCredential(p.Secret)
	wantRaw, _ := json.Marshal(want)
	gotRaw, _ := json.Marshal(refreshed)
	if !bytes.Equal(wantRaw, gotRaw) {
		t.Errorf("authenticated rotation did not change only credential provenance: before=%s after=%s", wantRaw, gotRaw)
	}
	// One already-proved and one fresh event, supplied once on this stream.
	// No other consumer, state repair or later event can release deferral.
	ev := f.delivery()
	stream.frames <- []envelope.Event{ev, deferredEvent(2, "credential payload", "opaque-rotation")}
	deadline := time.Now().Add(2500 * time.Millisecond)
	for !strings.Contains(out.String(), "credential payload") && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	stopRotated()
	if strings.Count(out.String(), "credential payload") != 1 || strings.Contains(out.String(), "live delivery") {
		t.Errorf("rotation failed receipt suppression/resume: %q", out.String())
	}
	after := readHookState(t, f.path)
	if len(after.Claims) != 1 || after.Claims[0] != before.Claims[0] || after.AckAfter != before.AckAfter || !slices.Contains(after.Displayed, "dly_1") {
		t.Fatal("rotation discarded unresolved ownership or retained display proof")
	}
	if raw, err := os.ReadFile(bindingPath(f.path)); err != nil || !bytes.Equal(raw, session) {
		t.Fatal("rotation changed session sidecar")
	}
}

func TestWatchR34KnownOperatorDisplaysAndDedupes(t *testing.T) {
	f := newDisplayFixture(t)
	setDisplayIdentity(f, `{"kind":"operator"}`)
	one := new(hookWatchOutput)
	stream, stop := f.watch(t, one, true)
	two := new(hookWatchOutput)
	second, stopSecond := f.watch(t, two, true)
	ev := f.delivery()
	stream.frames <- []envelope.Event{ev}
	if cur := f.ack(t); cur != ev.Cursor {
		t.Fatalf("operator ack=%q", cur)
	}
	stop()
	if strings.Count(one.String(), "live delivery") != 1 {
		t.Fatal("authenticated operator did not display exactly once")
	}
	s := readHookState(t, f.path)
	if s.Caller == "" || hookID.MatchString(s.Caller) || s.Hub != normalizeHub(f.hub) || s.Cred != hookCredential("vn_peer_test") || !slices.Contains(s.Displayed, "dly_1") {
		t.Fatal("operator proof lacks a distinct private subject and existing hub/credential scope")
	}
	second.frames <- []envelope.Event{ev}
	if cur := f.ack(t); cur != ev.Cursor {
		t.Fatalf("second operator ack=%q", cur)
	}
	stopSecond()
	if two.String() != "" {
		t.Fatal("already-connected operator monitor duplicated successful display")
	}

	// A private monitor role must not become a cached peer/principal pin
	// that authorizes the app-server claim/receipt consumer.
	ev2 := deferredEvent(2, "operator unrelated", "opaque-operator-next")
	f.mu.Lock()
	f.evs = append(f.evs, ev2)
	f.mu.Unlock()
	before, err := os.ReadFile(f.path)
	if err != nil {
		t.Fatal(err)
	}
	claimed, _ := runHook(t, f.env, "--event", "app-server", "--claim", "operator_nonce", "--priorities", "wake_idle", "--expect-hub", f.hub, "--expect-caller", s.Caller)
	if !strings.Contains(claimed, `"refused":"identity_changed"`) {
		t.Fatal("private operator monitor role authorized an app-server claim")
	}
	receipt, _ := runHook(t, f.env, "--release", "--claim", "operator_nonce", "--receipt-key", "vn.operator", "--delivery-ids", "dly_2", "--expect-hub", f.hub, "--expect-caller", s.Caller)
	if !strings.Contains(receipt, `"refused":"identity_changed"`) {
		t.Fatal("private operator monitor role authorized receipt settlement")
	}
	after, err := os.ReadFile(f.path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("refused operator lifecycle request changed shared state")
	}
}

func TestWatchR34UnknownRoleDefersUntilKnownOperator(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		outage     bool
	}{
		{"missing-role", `{}`, false},
		{"empty-body", ``, false},
		{"malformed", `{"kind":`, false},
		{"unknown-role", `{"kind":"unrecognized"}`, false},
		{"malformed-role", `{"kind":7}`, false},
		{"missing-peer", `{"kind":"peer"}`, false},
		{"missing-principal", `{"kind":"principal"}`, false},
		{"operator-role-as-peer-id", `{"kind":"peer","peer_id":"role:operator"}`, false},
		{"operator-outage", `{"kind":"operator"}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newDisplayFixture(t)
			setDisplayIdentity(f, tc.body)
			f.unverified.Store(tc.outage)
			out := new(hookWatchOutput)
			stream, stop := f.watch(t, out, true)
			ev := f.delivery()
			before, err := os.ReadFile(f.path)
			if err != nil {
				t.Fatal(err)
			}
			stream.frames <- []envelope.Event{ev}
			waitDisplayBlocked(t, f, stream, out)
			after, err := os.ReadFile(f.path)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatal("unknown identity created or rebound display proof")
			}
			// No new event/notification: only a positive authenticated role
			// response can let the retained event complete on its local retry.
			setDisplayIdentity(f, `{"kind":"operator"}`)
			f.unverified.Store(false)
			if cur := f.ack(t); cur != ev.Cursor {
				t.Fatalf("recovered operator ack=%q", cur)
			}
			stop()
			if strings.Count(out.String(), "live delivery") != 1 {
				t.Fatal("known-role recovery skipped or duplicated the retained event")
			}
		})
	}
}

func TestWatchR34OperatorStillHonorsClaimAndLock(t *testing.T) {
	for _, block := range []string{"claim", "lock"} {
		t.Run(block, func(t *testing.T) {
			f := newDisplayFixture(t)
			setDisplayIdentity(f, `{"kind":"operator"}`)
			out := new(hookWatchOutput)
			stream, stop := f.watch(t, out, true)
			ev := f.delivery()
			var release func()
			if block == "claim" {
				if err := (stateFile{f.path}).update(t.Context(), func(s *hookState) error {
					s.claim("dly_1")
					s.Claims = append(s.Claims, claimRef{ID: "dly_1", Claim: "operator_pending", After: "e1.0", Priority: "wake_idle", Order: 1})
					return nil
				}); err != nil {
					t.Fatal(err)
				}
				release = func() { deferredRelease(t, f, "operator_pending", 1) }
			} else {
				unlock, err := lockPath(t.Context(), f.path+".lock")
				if err != nil {
					t.Fatal(err)
				}
				var once sync.Once
				release = func() { once.Do(unlock) }
				t.Cleanup(release)
			}
			stream.frames <- []envelope.Event{ev}
			waitDisplayBlocked(t, f, stream, out)
			release()
			if cur := f.ack(t); cur != ev.Cursor {
				t.Fatalf("unblocked operator ack=%q", cur)
			}
			stop()
			if strings.Count(out.String(), "live delivery") != 1 {
				t.Fatal("operator bypassed ownership/lock or lost local retry")
			}
		})
	}
}
