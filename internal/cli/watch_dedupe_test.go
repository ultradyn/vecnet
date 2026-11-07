package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/envelope"
)

type hookWatchOutput struct {
	mu sync.Mutex
	b  strings.Builder
}

func (b *hookWatchOutput) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}
func (b *hookWatchOutput) String() string { b.mu.Lock(); defer b.mu.Unlock(); return b.b.String() }

func hookDelivery(id string) envelope.Event {
	return envelope.Event{Cursor: "e1.1", Type: "delivery", Data: json.RawMessage(fmt.Sprintf(`{"delivery_id":%q,"message":{"message_id":"msg_X","thread_id":"thr_X","sender":"peer_X","content":{"text":%q}}}`, id, id))}
}

// The stream waits for the test to release frames after the backlog is drained.
func runDedupeWatch(t *testing.T, backlog []envelope.Event) (string, chan<- []envelope.Event, *hookWatchOutput, func()) {
	t.Helper()
	if len(backlog) == 0 {
		return runDedupeWatchPages(t, nil)
	}
	return runDedupeWatchPages(t, [][]envelope.Event{backlog})
}

func runDedupeWatchPages(t *testing.T, pages [][]envelope.Event) (string, chan<- []envelope.Event, *hookWatchOutput, func()) {
	t.Helper()
	env := hookTestEnv(t)
	frames := make(chan []envelope.Event, 1)
	opened := make(chan *http.Request, 1)
	polls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/peers/me" {
			fmt.Fprint(w, `{"kind":"peer","peer_id":"peer_test"}`)
			return
		}
		if r.URL.Path == "/v1/peers" {
			fmt.Fprint(w, `{"peers":[]}`)
			return
		}
		if r.URL.Path != "/v1/events" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Accept") != "text/event-stream" {
			if r.URL.Query().Get("wait") != "0" {
				t.Errorf("replay wait = %q", r.URL.RawQuery)
			}
			events := []envelope.Event{}
			if polls > 0 && r.URL.Query().Get("after") != fmt.Sprintf("page_%d", polls) {
				t.Errorf("next replay query = %q", r.URL.RawQuery)
			}
			if polls < len(pages) {
				events = pages[polls]
			}
			polls++
			json.NewEncoder(w).Encode(pollBody{Events: events, NextCursor: fmt.Sprintf("page_%d", polls)})
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.(http.Flusher).Flush()
		opened <- r.Clone(context.Background())
		select {
		case evs := <-frames:
			for _, ev := range evs {
				raw, _ := json.Marshal(ev)
				fmt.Fprintf(w, "id: %s\nevent: delivery\ndata: %s\n\n", ev.Cursor, raw)
			}
			w.(http.Flusher).Flush()
		case <-r.Context().Done():
			return
		}
		<-r.Context().Done()
	}))
	t.Cleanup(srv.Close)
	conf := env["VECNET_CONFIG"]
	if err := os.MkdirAll(filepath.Dir(conf), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(conf, []byte(fmt.Sprintf("default = \"p\"\n[profiles.p]\nhub = %q\nsecret = \"vn_peer_test\"\n", srv.URL)), 0o600); err != nil {
		t.Fatal(err)
	}
	path, err := statePath(func(k string) string { return env[k] }, "p", "")
	if err != nil {
		t.Fatal(err)
	}
	// Replay must ignore a claim made by the hook before the watch starts.
	if len(pages) > 0 {
		if err := (stateFile{path: path}).update(context.Background(), func(s *hookState) error { s.claim("dly_A"); return nil }); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	out := new(hookWatchOutput)
	errs := new(hookWatchOutput)
	done := make(chan int, 1)
	go func() {
		done <- RunContext(ctx, []string{"watch", "--for-monitor"}, strings.NewReader(""), out, errs, func(k string) string { return env[k] })
	}()
	var once sync.Once
	stop := func() {
		once.Do(func() {
			cancel()
			select {
			case code := <-done:
				if code != 0 {
					t.Errorf("watch exit=%d: %s", code, errs.String())
				}
			case <-time.After(3 * time.Second):
				t.Error("watch did not stop")
			}
		})
	}
	t.Cleanup(stop)
	select {
	case r := <-opened:
		if polls != len(pages)+1 {
			t.Errorf("stream opened before empty replay page: polls=%d, want=%d", polls, len(pages)+1)
		}
		if r.URL.Query().Get("after") != fmt.Sprintf("page_%d", len(pages)+1) || r.Header.Get("Last-Event-ID") != "" {
			t.Errorf("first SSE cursor: query=%q header=%q", r.URL.RawQuery, r.Header.Get("Last-Event-ID"))
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("no stream after replay: %s", errs.String())
	}
	return path, frames, out, stop
}

func waitHookOutput(t *testing.T, out *hookWatchOutput, text string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(out.String(), text) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("watch never printed %q: %q", text, out.String())
}

func TestWatchReplayIgnoresShown(t *testing.T) {
	path, _, out, stop := runDedupeWatch(t, []envelope.Event{hookDelivery("dly_A")})
	waitHookOutput(t, out, "dly_A")
	stop()
	if strings.Count(out.String(), "dly_A") != 1 {
		t.Fatalf("replay = %q", out.String())
	}
	s := readHookState(t, path)
	if len(s.Shown) != 1 || s.Shown[0] != "dly_A" {
		t.Fatalf("shown state = %+v", s)
	}
}

func TestWatchReplayBeyond100Pages(t *testing.T) {
	// A hook-shown delivery must replay after more than 100 pages of other
	// events. Only an empty page ends replay, never a page count or a short
	// page, so a few events per page exercise the loop; full 500-event pages
	// would only make the race run print 50 500 lines against the deadline.
	pages := make([][]envelope.Event, 101)
	for page := range pages {
		pages[page] = make([]envelope.Event, 2)
		for i := range pages[page] {
			pages[page][i] = envelope.Event{
				Cursor: fmt.Sprintf("event_%d_%d", page, i), Type: "presence",
				Data: json.RawMessage(`{"peer_id":"peer_X","state":"idle"}`),
			}
		}
	}
	pages = append(pages, []envelope.Event{hookDelivery("dly_A")})
	_, _, out, stop := runDedupeWatchPages(t, pages)
	stop()
	if strings.Count(out.String(), "dly_A") != 1 {
		t.Fatal("hook-shown tail delivery was not replayed exactly once")
	}
}

func TestWatchReplayCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	stalled := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("after") == "page_1" {
			close(stalled)
			<-r.Context().Done()
			return
		}
		json.NewEncoder(w).Encode(pollBody{Events: []envelope.Event{{Type: "presence", Data: json.RawMessage(`{}`)}}, NextCursor: "page_1"})
	}))
	defer srv.Close()
	defer cancel()
	w := &watcher{a: &app{ctx: ctx, stdout: io.Discard, stderr: io.Discard}, c: newClient(srv.URL, ""), seen: newDedupe(dedupeWindow)}
	done := make(chan error, 1)
	go func() { done <- w.replay() }()
	select {
	case <-stalled:
	case <-time.After(5 * time.Second):
		t.Fatal("replay did not request its next page")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) || w.replayed {
			t.Fatalf("cancelled replay: err=%v, replayed=%v", err, w.replayed)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("replay did not stop on cancellation")
	}
}

func TestWatchLiveSkipsShown(t *testing.T) {
	path, frames, out, stop := runDedupeWatch(t, nil)
	if err := (stateFile{path: path}).update(context.Background(), func(s *hookState) error { s.recordDisplay("dly_B"); return nil }); err != nil {
		t.Fatal(err)
	}
	b, c := hookDelivery("dly_B"), hookDelivery("dly_C")
	b.Cursor = "e1.8"
	c.Cursor = "e1.9"
	frames <- []envelope.Event{b, c}
	waitHookOutput(t, out, "dly_C")
	stop()
	if strings.Contains(out.String(), "dly_B") || strings.Count(out.String(), "dly_C") != 1 {
		t.Fatalf("live output = %q", out.String())
	}
	s := readHookState(t, path)
	if !s.has("dly_C") || !s.has("dly_B") {
		t.Fatalf("shown = %+v", s)
	}
}

func TestMonitorLineStripsBidi(t *testing.T) {
	const hostile = "a\u202eb\u2066c\u200bd\u2028e"
	ev := hookDelivery("dly_X")
	ev.Data = json.RawMessage(fmt.Sprintf(`{"delivery_id":"dly_X","message":{"sender":"peer_X","content":{"text":%q}}}`, hostile))
	for _, got := range []string{monitorLine(ev, map[string]string{"peer_X": hostile}), inboxLine(ev, map[string]string{"peer_X": hostile}), peerName(hostile)} {
		if strings.ContainsAny(got, "\u202e\u2066\u200b\u2028") {
			t.Errorf("unsafe text: %q", got)
		}
	}
	if got := peerName(hostile); got != "a_b_c_d_e" {
		t.Errorf("peerName=%q", got)
	}
}
