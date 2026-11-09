package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/hubnotice"
)

// All frames and pages cross HTTP. The existing fixture owns transport routing,
// body-close accounting and synthetic HOME; no validator or watcher is mocked.
type receivedSSEFixture struct {
	*receivedPageFixture
	w                         *watcher
	path                      string
	frames                    string
	next                      string
	ackFailure                bool
	pollStatus                int
	pages                     []pollBody
	notices                   []envelope.HubNotice
	streamAfter, streamResume []string
	waits                     []string
	lists, completed          int
	served                    chan struct{}
}

func newReceivedSSE(t *testing.T, mode, policy string) *receivedSSEFixture {
	t.Helper()
	p := newReceivedPageFixture(t)
	receivedPagePolicy(p, policy)
	f := &receivedSSEFixture{receivedPageFixture: p, served: make(chan struct{}, 4)}
	f.path = receivedPageState(t, p, "e1.0", "e1.0", nil)
	srv := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(srv.Close)
	var err error
	p.serverURL, err = url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	a := p.app()
	a.json = strings.HasPrefix(mode, "json")
	c, name, ch, err := a.connect()
	if err != nil {
		t.Fatal(err)
	}
	f.w = &watcher{a: a, c: c, ch: ch, name: name, cursor: "e1.0",
		forMonitor: strings.HasPrefix(mode, "monitor"), deliveriesOnly: strings.HasSuffix(mode, "only"),
		autoAck: true, seen: newDedupe(dedupeWindow), retry: watchStartRetry}
	if err := f.w.verifyConnection(); err != nil {
		t.Fatal(err)
	}
	if f.w.forMonitor {
		id := f.w.resolveDisplayIdentity()
		if id.Caller != "peer_fixture" {
			t.Fatal("fixture identity not verified")
		}
		f.w.identity, f.w.shown = &id, &stateFile{f.path}
	}
	t.Cleanup(f.w.stopMoveTimer)
	return f
}

func (f *receivedSSEFixture) serve(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/v1/events":
		f.mu.Lock()
		if r.Header.Get("Accept") == "text/event-stream" {
			i := len(f.streamAfter)
			f.streamAfter = append(f.streamAfter, r.URL.Query().Get("after"))
			f.streamResume = append(f.streamResume, r.Header.Get("Last-Event-ID"))
			f.mu.Unlock()
			frames := f.frames
			if i == 1 {
				frames = f.next
			}
			if i > 1 || (i == 1 && frames == "") { // Finite reconnect trap.
				w.WriteHeader(http.StatusNoContent)
				return
			}
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, frames)
			w.(http.Flusher).Flush()
			f.served <- struct{}{}
			return
		}
		i := len(f.polls)
		f.polls = append(f.polls, r.URL.Query().Get("after"))
		f.waits = append(f.waits, r.URL.Query().Get("wait"))
		page := pollBody{Events: []envelope.Event{}, NextCursor: "e1.9"}
		if i < len(f.pages) {
			page = f.pages[i]
		}
		f.completed++
		f.mu.Unlock()
		if f.pollStatus != 0 {
			w.WriteHeader(f.pollStatus)
			fmt.Fprint(w, `{"error":{"code":"unavailable","message":"ordinary poll outage"}}`)
			return
		}
		json.NewEncoder(w).Encode(page)
	case "/v1/hub/notices":
		f.mu.Lock()
		f.lists++
		f.mu.Unlock()
		json.NewEncoder(w).Encode(map[string]any{"notices": f.notices})
	case "/v1/peers":
		fmt.Fprint(w, `{"peers":[]}`)
	case "/v1/ack":
		var body struct{ Cursor string }
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			f.t.Error(err)
		}
		f.mu.Lock()
		f.acks = append(f.acks, body.Cursor)
		f.mu.Unlock()
		if f.ackFailure {
			w.WriteHeader(503)
			fmt.Fprint(w, `{"error":{"code":"unavailable","message":"ordinary ack outage"}}`)
			return
		}
		fmt.Fprint(w, `{}`)
	default:
		f.servePage(w, r)
	}
}

func receivedSSEFrame(kind, raw, id string) string {
	return "id: " + id + "\nevent: " + kind + "\ndata: " + raw + "\n\n"
}

func (f *receivedSSEFixture) frame(t *testing.T, ev envelope.Event) string {
	t.Helper()
	return receivedSSEFrame(ev.Type, string(receivedJSON(t, ev)), ev.Cursor)
}

func (f *receivedSSEFixture) traffic(t *testing.T, streams, lists int, polls, waits, acks []string) {
	t.Helper()
	if err := f.w.a.ctx.Err(); err != nil {
		t.Errorf("fixture relied on context expiry: %v", err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.streamAfter) != streams || f.lists != lists || f.completed != len(polls) ||
		!reflect.DeepEqual(f.polls, polls) || !reflect.DeepEqual(f.waits, waits) || !reflect.DeepEqual(f.acks, acks) || f.writes != 0 {
		t.Errorf("HTTP streams/lists/pages/after/wait/ack/other=%v/%d/%d/%v/%v/%v/%d; want %d/%d/%d/%v/%v/%v/0",
			f.streamAfter, f.lists, f.completed, f.polls, f.waits, f.acks, f.writes, streams, lists, len(polls), polls, waits, acks)
	}
	if streams > 0 && (f.streamAfter[0] != "e1.0" || f.streamResume[0] != "") {
		t.Errorf("initial SSE resume changed: after=%v header=%v", f.streamAfter, f.streamResume)
	}
	f.receivedFixture.mu.Lock()
	defer f.receivedFixture.mu.Unlock()
	for _, req := range f.requests {
		if req.path != "/v1/hub/identity" && (req.auth != "Bearer "+receivedPageSecret || (req.path == "/v1/ack" && req.method != "POST") || (req.path != "/v1/ack" && req.method != "GET")) {
			t.Errorf("request method/credential changed: %+v", req)
		}
		if req.origin != f.hub || req.closed != 1 || (req.path == "/v1/events" && req.read == 0 && streams == 1 && f.frames != "") {
			t.Errorf("HTTP destination/body effect: %+v", req)
		}
	}
}

func receivedSSERefusal(t *testing.T, f *receivedSSEFixture, err error, before map[string]string) {
	t.Helper()
	w := f.w
	if w.a.ctx.Err() != nil || !errors.Is(err, hubnotice.ErrInvalidNotice) {
		t.Errorf("known notice did not refuse synchronously: err=%v context=%v", err, w.a.ctx.Err())
	}
	if w.cursor != "e1.0" || w.pendingAck != "" || len(w.seen.set) != 0 || w.deferred != nil || w.move != nil || w.moveTimer != nil {
		t.Errorf("refused frame earned effects: cursor=%q ack=%q seen=%v deferred=%+v move=%+v", w.cursor, w.pendingAck, w.seen.set, w.deferred, w.move)
	}
	if w.a.stdout.(*bytes.Buffer).Len() != 0 || w.a.stderr.(*bytes.Buffer).Len() != 0 {
		t.Errorf("refused frame emitted output: %q/%q", w.a.stdout, w.a.stderr)
	}
	f.unchanged(before)
	w.flushAck() // Exercise the actual HTTP ack path, including suppressed notices.
	if code := w.a.exit(err); code != exitHub || w.a.stderr.(*bytes.Buffer).String() != "bad_response: invalid hub notice\n" {
		t.Errorf("unsafe refusal diagnostic: code=%d stderr=%q", code, w.a.stderr)
	}
}

func TestWatchNoticeReceiveBoundaries(t *testing.T) {
	for _, bad := range receivedBadPageEvents(t) {
		for _, mode := range []string{"json", "plain", "monitor", "json-only", "plain-only", "monitor-only"} {
			t.Run("refusal/"+bad.name+"/"+mode, func(t *testing.T) {
				f := newReceivedSSE(t, mode, bad.policy)
				f.frames = receivedSSEFrame("hub_notice", string(receivedJSON(t, bad.event)), "e1.90") + f.frame(t, receivedPageDelivery(t, 3))
				before := f.snapshot()
				err := f.w.stream()
				receivedSSERefusal(t, f, err, before)
				f.traffic(t, 1, 0, nil, nil, nil)
			})
		}
	}
	malformed := []struct{ name, header, raw string }{
		{"empty-data", "hub_notice", ""},
		{"no-data", "hub_notice", ""},
		{"broken-envelope", "hub_notice", `{"private":"` + receivedCanary + `","type":!}`},
		{"null-envelope", "hub_notice", `null`},
		{"array-envelope", "hub_notice", `[]`},
		{"string-envelope", "hub_notice", `"` + receivedCanary + `"`},
		{"bad-cursor", "hub_notice", `{"cursor":17,"type":"hub_notice","data":{}}`},
		{"bad-type", "hub_notice", `{"type":17,"data":{}}`},
		{"missing-data", "hub_notice", `{"type":"hub_notice"}`},
		{"malformed-data", "hub_notice", `{"type":"hub_notice","data":"` + receivedCanary + `"}`},
		{"header-notice-body-delivery", "hub_notice", string(receivedJSON(t, receivedPageDelivery(t, 2)))},
		{"header-delivery-body-notice", "delivery", string(receivedJSON(t, receivedPageNotice(t, receivedListNotice(), "posted")))},
		{"header-restart-body-notice", "hub_restarting", string(receivedJSON(t, receivedPageNotice(t, receivedListNotice(), "posted")))},
		{"header-notice-body-restart", "hub_notice", `{"type":"hub_restarting","data":{}}`},
		{"body-notice-bad-cursor", "", `{"cursor":17,"type":"hub_notice","data":{}}`},
	}
	for _, bad := range malformed {
		for _, mode := range []string{"json", "plain", "monitor", "json-only", "plain-only", "monitor-only"} {
			t.Run("malformed-NC1/"+bad.name+"/"+mode, func(t *testing.T) {
				f := newReceivedSSE(t, mode, "")
				f.frames = receivedSSEFrame(bad.header, bad.raw, "e1.90") + f.frame(t, receivedPageDelivery(t, 3))
				if bad.name == "no-data" {
					f.frames = strings.Replace(f.frames, "data: \n", "", 1)
				}
				before := f.snapshot()
				receivedSSERefusal(t, f, f.w.stream(), before)
				f.traffic(t, 1, 0, nil, nil, nil)
			})
		}
	}
	t.Run("admitted", receivedSSEAdmitted)
	t.Run("transport-controls", receivedSSETransport)
	t.Run("prior-frame", receivedSSEPrior)
	t.Run("within-stream-prefix", receivedSSEPrefix)
	t.Run("run-stops", receivedSSERun)
	t.Run("poll-consumers", receivedSSEPages)
	t.Run("expired-move", receivedSSEExpired)
	t.Run("deferred-barrier", receivedSSEDeferred)
}

func receivedSSEAdmitted(t *testing.T) {
	for _, good := range receivedGoodPageEvents(t) {
		for _, kind := range []string{"matching", "empty-header", "empty-body"} {
			t.Run(good.name+"/"+kind, func(t *testing.T) {
				f := newReceivedSSE(t, "json", good.policy)
				ev := good.event
				ev.Data = []byte("{ \"opaque\" : 1e+02, " + strings.TrimPrefix(string(ev.Data), "{"))
				wire, header := ev, ev.Type
				if kind == "empty-header" {
					header = ""
				}
				if kind == "empty-body" {
					wire.Type = ""
				}
				// Preserve raw Data in the HTTP frame; JSON output has always compacted
				// whitespace, but must retain all fields, numeric spelling, Sig and URLs.
				raw := fmt.Sprintf(`{"cursor":%s,"type":%s,"at":%s,"data":%s}`, receivedJSON(t, wire.Cursor), receivedJSON(t, wire.Type), receivedJSON(t, wire.At), wire.Data)
				f.frames = receivedSSEFrame(header, raw, "e1.2")
				before := f.snapshot()
				if err := f.w.stream(); err != nil || f.w.cursor != "e1.2" {
					t.Errorf("admitted stream/cursor=%v/%q", err, f.w.cursor)
				}
				f.w.flushAck()
				if got, want := f.w.a.stdout.(*bytes.Buffer).String(), string(receivedJSON(t, ev))+"\n"; got != want || f.w.a.stderr.(*bytes.Buffer).Len() != 0 {
					t.Errorf("admitted data changed: output=%q want=%q errors=%q", got, want, f.w.a.stderr)
				}
				lists := 0
				var n envelope.HubNoticeEvent
				if ev.Type == "hub_notice" && json.Unmarshal(ev.Data, &n) == nil && n.Notice.Kind == "moving" {
					lists = 1
				}
				f.unchanged(before)
				f.traffic(t, 1, lists, nil, nil, []string{"e1.2"})
			})
		}
	}
	for _, mode := range []string{"plain", "monitor", "json-only", "plain-only", "monitor-only"} {
		t.Run(mode, func(t *testing.T) {
			f := newReceivedSSE(t, mode, "")
			n := receivedListNotice()
			n.Kind, n.NewURL = "maintenance", ""
			f.frames = f.frame(t, receivedPageNotice(t, n, "posted"))
			before := f.snapshot()
			if err := f.w.stream(); err != nil {
				t.Fatal(err)
			}
			f.w.flushAck()
			want := ""
			if mode == "plain" {
				want = "e1.2 hub_notice - hub - - [maintenance] prose https://u:p@x/?q#f <>& 界\n"
			}
			if mode == "monitor" {
				want = "e1.2 hub_notice - hub hub: [maintenance] prose https://u:p@x/?q#f <>& 界\n"
			}
			if f.w.a.stdout.(*bytes.Buffer).String() != want || f.w.a.stderr.(*bytes.Buffer).Len() != 0 || f.w.cursor != "e1.2" {
				t.Errorf("admitted mode output/cursor=%q/%q want=%q", f.w.a.stdout, f.w.cursor, want)
			}
			f.unchanged(before)
			f.traffic(t, 1, 0, nil, nil, []string{"e1.2"})
		})
	}
}

func receivedSSETransport(t *testing.T) {
	for _, tc := range []struct {
		name, frames, cursor, diagnostic string
		err                              error
	}{
		{"EOF", "", "e1.0", "", nil},
		{"priming", "id: e1.7\ndata:\n\n", "e1.7", "", nil},
		{"restart", "id: e1.7\nevent: hub_restarting\ndata: {\"retry_after_ms\":10}\n\n", "e1.0", "", errRestarting},
		{"restart-no-hint", "event: hub_restarting\ndata: {}\n\n", "e1.0", "", nil},
		{"unrelated-malformed", receivedSSEFrame("presence", "!", "e1.7"), "e1.7", "vecnet: watch: undecodable presence event: invalid character '!' looking for beginning of value\n", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newReceivedSSE(t, "json", "")
			f.frames = tc.frames
			before := f.snapshot()
			if err := f.w.stream(); !errors.Is(err, tc.err) || f.w.cursor != tc.cursor || f.w.pendingAck != "" || f.w.a.stdout.(*bytes.Buffer).Len() != 0 || f.w.a.stderr.(*bytes.Buffer).String() != tc.diagnostic {
				t.Errorf("transport behavior changed: cursor=%q err=%v output=%q/%q", f.w.cursor, err, f.w.a.stdout, f.w.a.stderr)
			}
			f.unchanged(before)
			// Empty EOF is legal; body accounting still requires exactly one close.
			f.traffic(t, 1, 0, nil, nil, nil)
		})
	}
	// Conflicting types without a known notice retain existing body precedence.
	t.Run("unrelated-type-mismatch", func(t *testing.T) {
		f := newReceivedSSE(t, "json", "")
		ev := envelope.Event{Cursor: "e1.2", Type: "presence", Data: []byte(`{}`)}
		f.frames = receivedSSEFrame("receipt", string(receivedJSON(t, ev)), "e1.2")
		if err := f.w.stream(); err != nil || f.w.a.stdout.(*bytes.Buffer).String() != string(receivedJSON(t, ev))+"\n" {
			t.Errorf("unrelated mismatch changed: %v/%q", err, f.w.a.stdout)
		}
		f.w.flushAck()
		f.traffic(t, 1, 0, nil, nil, []string{"e1.2"})
	})
}

func receivedSSEPrior(t *testing.T) {
	f := newReceivedSSE(t, "monitor", "")
	one := receivedPageDelivery(t, 1)
	f.ackFailure = true
	f.frames = f.frame(t, one) + f.frame(t, one) // Actual per-watch dedupe.
	if err := f.w.stream(); err != nil {
		t.Fatal(err)
	}
	if got := f.w.a.stdout.(*bytes.Buffer).String(); got != "e1.1 delivery thr_X peer_h "+receivedCanary+" delivery\n" || !reflect.DeepEqual(readHookState(t, f.path).Displayed, []string{"dly_kept", "dly_1"}) {
		t.Fatalf("accepted prefix display/proof=%q", got)
	}
	// An actual failed HTTP ack leaves the accepted prefix owed on reconnect.
	f.w.flushAck()
	if f.w.pendingAck != "e1.1" || f.w.cursor != "e1.1" {
		t.Fatal("actual ack outage did not retain earlier accepted debt")
	}
	f.mu.Lock()
	priorAcks := len(f.acks)
	for _, ack := range f.acks {
		if ack != "e1.1" {
			t.Errorf("accepted-prefix ack=%q", ack)
		}
	}
	f.mu.Unlock()
	f.next = f.frame(t, receivedBadPageEvents(t)[0].event) + f.frame(t, receivedPageDelivery(t, 3))
	before, out := f.snapshot(), f.w.a.stdout.(*bytes.Buffer).String()
	err := f.w.stream()
	if !errors.Is(err, hubnotice.ErrInvalidNotice) || f.w.cursor != "e1.1" || f.w.pendingAck != "e1.1" || len(f.w.seen.set) != 1 || f.w.a.stdout.(*bytes.Buffer).String() != out || f.w.a.stderr.(*bytes.Buffer).Len() != 0 {
		t.Errorf("refusal crossed accepted prefix: err=%v cursor=%q ack=%q seen=%v", err, f.w.cursor, f.w.pendingAck, f.w.seen.set)
	}
	f.unchanged(before)
	f.ackFailure = false
	f.w.flushAck()
	wantAcks := make([]string, priorAcks+1)
	for i := range wantAcks {
		wantAcks[i] = "e1.1"
	}
	if len(f.streamAfter) != 2 || f.streamAfter[1] != "" || f.streamResume[1] != "e1.1" || f.w.pendingAck != "" {
		t.Error("accepted cursor/debt did not survive real reconnect")
	}
	f.traffic(t, 2, 0, nil, nil, wantAcks)
}

func receivedSSERun(t *testing.T) {
	for _, mode := range []string{"json", "monitor"} {
		t.Run(mode, func(t *testing.T) {
			f := newReceivedSSE(t, mode, "")
			f.pages = []pollBody{{Events: []envelope.Event{}, NextCursor: "e1.0"}}
			f.frames = f.frame(t, receivedBadPageEvents(t)[0].event)
			f.w.refreshNames() // Establish permitted initialization cache before snapshot.
			before := f.snapshot()
			sleeps := 0
			f.w.sleep = func(context.Context, time.Duration) bool { sleeps++; return true }
			err := f.w.run()
			select {
			case <-f.served:
			case <-f.w.a.ctx.Done():
				t.Fatal("run never received the actual bad frame")
			}
			if f.w.a.ctx.Err() != nil || !errors.Is(err, hubnotice.ErrInvalidNotice) || sleeps != 0 || f.w.cursor != "e1.0" || f.w.a.stdout.(*bytes.Buffer).Len() != 0 || f.w.a.stderr.(*bytes.Buffer).Len() != 0 {
				t.Errorf("run did not stop safely: err=%v sleeps=%d cursor=%q output=%q/%q", err, sleeps, f.w.cursor, f.w.a.stdout, f.w.a.stderr)
			}
			var polls, waits []string
			if mode == "monitor" {
				polls, waits = []string{"e1.0"}, []string{"0"}
				// Initial empty replay's next cursor is a transport position.
				// Keep it equal to the seeded position for a literal refusal oracle.
			}
			f.unchanged(before)
			f.traffic(t, 1, 1, polls, waits, nil)
		})
	}
}

func receivedSSEPrefix(t *testing.T) {
	f := newReceivedSSE(t, "monitor", "")
	wantState, before := readHookState(t, f.path), f.snapshot()
	f.frames = f.frame(t, receivedPageDelivery(t, 1)) + f.frame(t, receivedBadPageEvents(t)[0].event) + f.frame(t, receivedPageDelivery(t, 3))
	err := f.w.stream()
	if !errors.Is(err, hubnotice.ErrInvalidNotice) || f.w.cursor != "e1.1" || len(f.w.seen.set) != 1 || f.w.a.stdout.(*bytes.Buffer).String() != "e1.1 delivery thr_X peer_h "+receivedCanary+" delivery\n" || f.w.a.stderr.(*bytes.Buffer).Len() != 0 {
		t.Errorf("refused suffix changed accepted prefix: err=%v cursor=%q seen=%v output=%q/%q", err, f.w.cursor, f.w.seen.set, f.w.a.stdout, f.w.a.stderr)
	}
	wantState.Shown, wantState.Displayed = []string{"dly_kept", "dly_1"}, []string{"dly_kept", "dly_1"}
	if !reflect.DeepEqual(readHookState(t, f.path), wantState) || !reflect.DeepEqual(f.w.ch.readJSON("messages.json"), map[string]string{"msg_1": "thr_X"}) {
		t.Error("refused suffix changed accepted proof/claim/cursor/cache")
	}
	after := f.snapshot()
	delete(before, f.path)
	delete(after, f.path)
	delete(after, filepath.Join(f.w.ch.dir, "messages.json"))
	if !reflect.DeepEqual(before, after) {
		t.Error("accepted prefix/refused suffix changed other fixture files")
	}
	f.w.flushAck()
	f.traffic(t, 1, 0, nil, nil, []string{"e1.1"})
}

func receivedSSEPages(t *testing.T) {
	for _, method := range []string{"poll", "replay", "longPoll"} {
		for _, bad := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/bad=%t", method, bad), func(t *testing.T) {
				f := newReceivedSSE(t, "json", "")
				evs := []envelope.Event{receivedPageDelivery(t, 1)}
				if bad {
					evs = append(evs, receivedBadPageEvents(t)[0].event)
				}
				f.pages = []pollBody{{Events: evs, NextCursor: "e1.9"}, {NextCursor: "e1.9"}}
				before := f.snapshot()
				var err error
				switch method {
				case "poll":
					var body pollBody
					body, err = f.w.poll(0)
					if bad && !reflect.DeepEqual(body, pollBody{}) || !bad && !reflect.DeepEqual(body, f.pages[0]) {
						t.Errorf("poll page changed: %+v", body)
					}
				case "replay":
					err = f.w.replay()
				case "longPoll":
					err = f.w.longPoll()
				}
				polls, waits := []string{"e1.0"}, []string{"0"}
				if method == "longPoll" {
					waits[0] = "25"
				}
				if bad {
					receivedSSERefusal(t, f, err, before)
				} else {
					wantOut, wantCursor := "", "e1.0"
					if method != "poll" {
						wantOut, wantCursor = string(receivedJSON(t, evs[0]))+"\n", "e1.9"
					}
					if err != nil || f.w.a.stdout.(*bytes.Buffer).String() != wantOut || f.w.cursor != wantCursor {
						t.Errorf("admitted poll consumer changed: %v/%q/%q", err, f.w.a.stdout, f.w.cursor)
					}
					if method == "replay" {
						polls, waits = append(polls, "e1.9"), append(waits, "0")
					}
				}
				var acks []string
				if !bad && method != "poll" {
					if f.w.pendingAck != "e1.1" || (method == "replay" && !f.w.replayed) {
						t.Error("admitted consumer lost owed ack or replay completion")
					}
					f.w.flushAck()
					acks = []string{"e1.1"}
				}
				f.traffic(t, 0, 0, polls, waits, acks)
			})
		}
	}
}

// Return already-received bytes only after the real scheduled timer expires.
// The HTTP request/decoder/validator still run; no error return is substituted.
type receivedSSEExpiryBody struct {
	io.ReadCloser
	due        time.Time
	hit, ready bool
	raw        *bytes.Reader
}

func (b *receivedSSEExpiryBody) Read(p []byte) (int, error) {
	if !b.hit {
		raw, err := io.ReadAll(b.ReadCloser)
		if err != nil {
			return 0, err
		}
		b.raw, b.ready = bytes.NewReader(raw), time.Now().Before(b.due)
		b.hit = true
		timer := time.NewTimer(max(0, time.Until(b.due)))
		defer timer.Stop()
		<-timer.C
	}
	return b.raw.Read(p)
}

func receivedSSEExpired(t *testing.T) {
	t.Run("notice-refusal", func(t *testing.T) { receivedSSEExpiredCase(t, true) })
	t.Run("ordinary-outage", func(t *testing.T) { receivedSSEExpiredCase(t, false) })
}

func receivedSSEExpiredCase(t *testing.T, bad bool) {
	f := newReceivedSSE(t, "json", "")
	n := receivedScheduled(t, f.receivedFixture, "ntc_01ARZ3NDEKTSV4RRFFQ69G5FAV")
	n.At = envelope.FormatTime(time.Now().Add(time.Second))
	if err := f.key.SignNotice(&n); err != nil {
		t.Fatal(err)
	}
	previous := moveJitter
	moveJitter = func() time.Duration { return 0 }
	t.Cleanup(func() { moveJitter = previous })
	f.notices = []envelope.HubNotice{n}
	f.w.refreshMoving()
	if f.w.move == nil || *f.w.move != n || f.w.moveTimer == nil || !f.w.moveDue.After(time.Now()) {
		t.Fatal("actual admitted moving timer was not installed")
	}
	old := f.w.moveTimer
	t.Cleanup(func() { old.Stop() })
	f.pages = []pollBody{{Events: []envelope.Event{receivedBadPageEvents(t)[0].event}, NextCursor: "e1.9"}}
	want := error(hubnotice.ErrInvalidNotice)
	if !bad {
		f.pollStatus, want = 503, errMoveDue
	}
	var held *receivedSSEExpiryBody
	f.w.c.hc.Transport = receivedRoundTrip(func(r *http.Request) (*http.Response, error) {
		resp, err := f.receivedFixture.roundTrip(r)
		if err == nil {
			held = &receivedSSEExpiryBody{ReadCloser: resp.Body, due: f.w.moveDue}
			resp.Body = held
		}
		return resp, err
	})
	before := f.snapshot()
	body, err := f.w.poll(0)
	if held == nil || !held.hit || !held.ready || time.Now().Before(f.w.moveDue) || f.w.a.ctx.Err() != nil || !errors.Is(err, want) || !reflect.DeepEqual(body, pollBody{}) {
		t.Errorf("expired move replaced actual notice refusal: body=%+v err=%v held=%+v", body, err, held)
	}
	if f.w.cursor != "e1.0" || f.w.pendingAck != "" || f.w.moveTimer != old || f.w.moveC != old.C || f.w.move == nil || *f.w.move != n || len(f.w.seen.set) != 0 || f.w.deferred != nil || f.w.a.stdout.(*bytes.Buffer).Len() != 0 || f.w.a.stderr.(*bytes.Buffer).Len() != 0 {
		t.Error("poll race changed prior timer/cursor/ack")
	}
	f.unchanged(before)
	f.traffic(t, 0, 1, []string{"e1.0"}, []string{"0"}, nil)
}

func receivedSSEDeferred(t *testing.T) {
	f := newReceivedSSE(t, "monitor", "")
	if err := (stateFile{f.path}).update(f.w.a.ctx, func(s *hookState) error {
		s.Claims = []claimRef{{ID: "dly_1", After: "e1.0", Priority: "wake_idle", Claim: "held", Order: 1}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	one := receivedPageDelivery(t, 1)
	f.frames = f.frame(t, one) + f.frame(t, receivedBadPageEvents(t)[0].event)
	before := f.snapshot()
	if err := f.w.stream(); !errors.Is(err, errDisplayDeferred) || f.w.deferred == nil || f.w.cursor != "e1.0" || f.w.pendingAck != "" || len(f.w.seen.set) != 0 {
		t.Fatalf("prior claim lost cumulative ack barrier: err=%v deferred=%+v", err, f.w.deferred)
	}
	f.unchanged(before)
	f.w.flushAck()
	if f.w.a.stdout.(*bytes.Buffer).Len() != 0 || f.w.a.stderr.(*bytes.Buffer).Len() != 0 {
		t.Error("blocked prior display or unread notice produced output")
	}
	f.traffic(t, 1, 0, nil, nil, nil)
	// An independent consumer releases its reservation through the real state
	// transaction. Retry must finish the earlier envelope before another transport.
	if err := (stateFile{f.path}).update(f.w.a.ctx, func(s *hookState) error { s.Claims = nil; return nil }); err != nil {
		t.Fatal(err)
	}
	f.w.sleep = func(context.Context, time.Duration) bool { return true }
	if err := f.w.retryDeferred(); err != nil || f.w.cursor != "e1.1" || f.w.deferred != nil || f.w.pendingAck != "" || f.w.a.stdout.(*bytes.Buffer).String() != "e1.1 delivery thr_X peer_h "+receivedCanary+" delivery\n" {
		t.Fatalf("released earlier display did not settle before reconnect: %v", err)
	}
	before, out := f.snapshot(), f.w.a.stdout.(*bytes.Buffer).String()
	f.next = f.frame(t, receivedBadPageEvents(t)[0].event) + f.frame(t, receivedPageDelivery(t, 3))
	if err := f.w.stream(); !errors.Is(err, hubnotice.ErrInvalidNotice) || f.w.cursor != "e1.1" || f.w.pendingAck != "" || len(f.w.seen.set) != 1 || f.w.a.stdout.(*bytes.Buffer).String() != out {
		t.Errorf("refusal crossed settled deferred delivery: err=%v cursor=%q ack=%q", err, f.w.cursor, f.w.pendingAck)
	}
	f.unchanged(before)
	f.w.flushAck()
	f.traffic(t, 2, 0, nil, nil, []string{"e1.1"})
}
