package cli_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/XertroV/vecnet/internal/cli"
	"github.com/XertroV/vecnet/internal/testhub"
)

// startWatch runs the public CLI until stop or test cleanup, and verifies
// cancellation closes the watch cleanly before its test hub is shut down.
func startWatch(t *testing.T, u *user, args ...string) (*syncBuffer, func()) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	var out, errb syncBuffer
	done := make(chan int, 1)
	go func() {
		done <- cli.RunContext(ctx, args, strings.NewReader(""), &out, &errb, u.getenv)
	}()
	stopped := false
	stop := func() {
		t.Helper()
		if stopped {
			return
		}
		stopped = true
		cancel()
		select {
		case code := <-done:
			if code != 0 {
				t.Errorf("watch exit = %d; stderr: %s", code, errb.String())
			}
		case <-time.After(5 * time.Second):
			t.Error("watch did not stop after cancellation")
		}
	}
	t.Cleanup(stop)
	return &out, stop
}

// monitorOutput serves a wire-format delivery and a directory with a fixed
// peer id so assertions cover the exact monitor columns through the CLI.
func monitorOutput(t *testing.T, name, text string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/peers":
			w.Header().Set("Content-Type", "application/json")
			peers := []map[string]string{}
			if name != "" {
				peers = append(peers, map[string]string{"peer_id": "peer_X", "name": name})
			}
			json.NewEncoder(w).Encode(map[string]any{"peers": peers})
		case "/v1/events":
			w.Header().Set("Content-Type", "text/event-stream")
			ev := map[string]any{
				"cursor": "e1.1", "type": "delivery",
				"data": map[string]any{"delivery_id": "dly_X", "message": map[string]any{
					"message_id": "msg_X", "thread_id": "thr_X", "sender": "peer_X",
					"content": map[string]string{"text": text},
				}},
			}
			if r.Header.Get("Accept") != "text/event-stream" {
				w.Header().Set("Content-Type", "application/json")
				events := []any{}
				if r.URL.Query().Get("after") == "" {
					events = append(events, ev)
				}
				json.NewEncoder(w).Encode(map[string]any{"events": events, "next_cursor": "e1.1"})
				return
			}
			raw, _ := json.Marshal(ev)
			fmt.Fprintf(w, "id: e1.1\nevent: delivery\ndata: %s\n\n", raw)
			w.(http.Flusher).Flush()
			<-r.Context().Done()
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	u := profileUser(t, srv.URL, "vn_peer_test")
	out, stop := startWatch(t, u, "watch", "--for-monitor")
	line := waitLine(t, out, regexp.MustCompile(`^e1\.1 delivery `), 5*time.Second)
	stop()
	if got := nonEmptyLines(out.String()); len(got) != 1 {
		t.Fatalf("monitor lines = %q, want one event and no header", got)
	}
	return line
}

func TestMonitorLineNamePrefix(t *testing.T) {
	for _, tc := range []struct {
		label, name, want string
	}{
		{"known", "Max", "Max: hello"},
		{"unknown", "", "hello"},
		{"hostile", "\x1b\nMax Name " + strings.Repeat("界", 100), "__Max_Name_" + strings.Repeat("界", 21) + ": hello"},
	} {
		t.Run(tc.label, func(t *testing.T) {
			got := monitorOutput(t, tc.name, "hello")
			if want := "e1.1 delivery thr_X peer_X " + tc.want; got != want {
				t.Fatalf("monitor line = %q, want %q", got, want)
			}
		})
	}
}

func TestMonitorLineStaysOneLineAndCapped(t *testing.T) {
	text := "hi\r\n\x1b[2K\t\u2028\u2029\x7f\u009b" + strings.Repeat("é", 150)
	got := monitorOutput(t, "Max", text)
	const columns = "e1.1 delivery thr_X peer_X "
	if !strings.HasPrefix(got, columns) {
		t.Fatalf("monitor columns changed: %q", got)
	}
	excerpt := strings.TrimPrefix(got, columns)
	if n := utf8.RuneCountInString(excerpt); n > 120 {
		t.Fatalf("text including name has %d runes, want <= 120: %q", n, excerpt)
	}
	want := "Max: hi  [2K     " + strings.Repeat("é", 103)
	if excerpt != want {
		t.Fatalf("monitor text = %q, want %q", excerpt, want)
	}
}

func TestInboxHeaderAndFrom(t *testing.T) {
	env := testhub.Start(t)
	aID, aSec := env.NewPeer("session", "Max")
	bID, bSec := env.NewPeer("session", "Watcher")
	a := profileUser(t, env.URL, aSec)
	b := profileUser(t, env.URL, bSec)
	sent := a.send(bID, "hello", "--hold")
	out, stop := startWatch(t, b, "watch")
	line := waitLine(t, out, regexp.MustCompile(` delivery .* hello$`), 5*time.Second)
	stop()
	const header = "CURSOR TYPE THREAD FROM MSG DELIVERY TEXT"
	if lines := nonEmptyLines(out.String()); len(lines) < 2 || lines[0] != header || strings.Count(out.String(), header) != 1 {
		t.Fatalf("watch must start with one header: %q", out.String())
	}
	if want := "Max(" + aID + ")"; !strings.Contains(line, " "+want+" ") {
		t.Fatalf("watch FROM lacks %q: %q", want, line)
	}

	for _, tc := range []struct {
		label string
		u     *user
		from  string
	}{
		{"cached", b, "Max(" + aID + ")"},
		// No cached directory: the delivery's own sender_info names it.
		{"uncached", profileUser(t, env.URL, bSec), "Max(" + aID + ")"},
	} {
		t.Run(tc.label, func(t *testing.T) {
			got := tc.u.mustRun("inbox")
			lines := nonEmptyLines(got)
			if len(lines) != 2 || lines[0] != header || strings.Count(got, header) != 1 {
				t.Fatalf("inbox = %q, want header and one event", got)
			}
			fields := strings.Fields(lines[1])
			if len(fields) != 7 || fields[3] != tc.from || fields[4] != sent.MessageID || fields[6] != "hello" {
				t.Fatalf("inbox fields = %q, want FROM %s and original message", fields, tc.from)
			}
			if evs := tc.u.inboxJSON(); len(evs) != 1 || evs[0].Data.Message.Sender != aID {
				t.Fatalf("inbox JSON changed: %+v", evs)
			}
		})
	}
	b.mustRun("inbox", "--ack")
	if got := b.mustRun("inbox"); got != header+"\n" {
		t.Fatalf("empty inbox = %q, want header only", got)
	}
	env.NewPeer("session", "Directory change")
	lines := nonEmptyLines(b.mustRun("inbox"))
	if len(lines) != 2 || !strings.HasSuffix(lines[1], " directory_updated - - - - -") {
		t.Fatalf("empty human fields must keep dashes: %q", lines)
	}
}

func TestWatchRefreshesNamesOnDirectoryUpdated(t *testing.T) {
	for _, deliveriesOnly := range []bool{false, true} {
		t.Run(fmt.Sprintf("deliveries-only=%t", deliveriesOnly), func(t *testing.T) {
			env := testhub.Start(t)
			aID, aSec := env.NewPeer("session", "Watcher")
			_, bSec := env.NewPeer("session", "Before")
			a := profileUser(t, env.URL, aSec)
			b := profileUser(t, env.URL, bSec)
			args := []string{"watch", "--for-monitor"}
			if deliveriesOnly {
				args = append(args, "--deliveries-only")
			}
			out, stop := startWatch(t, a, args...)
			b.send(aID, "ready", "--hold")
			waitLine(t, out, regexp.MustCompile(` Before: ready$`), 5*time.Second)

			// Enrolment emits directory_updated to the already running watch.
			// The next sender did not exist in its initial directory fetch.
			cID, cSec := env.NewPeer("session", "New Name")
			c := profileUser(t, env.URL, cSec)
			sent := c.send(aID, "after refresh", "--hold")
			line := waitLine(t, out, regexp.MustCompile(` delivery `+regexp.QuoteMeta(sent.ThreadID)+` `), 5*time.Second)
			if want := " " + cID + " New_Name: after refresh"; !strings.HasSuffix(line, want) {
				t.Fatalf("directory update did not refresh name: %q, want suffix %q", line, want)
			}
			stop()
			if got := a.mustRun("inbox"); !strings.Contains(got, "New_Name("+cID+")") {
				t.Fatalf("refreshed name was not persisted for inbox: %q", got)
			}
			if deliveriesOnly && strings.Contains(out.String(), "directory_updated") {
				t.Fatalf("--deliveries-only printed a directory update: %q", out.String())
			}
		})
	}
}

func TestPeerNameFetchFailureKeepsWatching(t *testing.T) {
	for _, mode := range []string{"startup", "refresh-http", "refresh-json"} {
		t.Run(mode, func(t *testing.T) {
			env := testhub.Start(t)
			aID, aSec := env.NewPeer("session", "Watcher")
			_, bSec := env.NewPeer("session", "Max")
			target, err := url.Parse(env.URL)
			if err != nil {
				t.Fatal(err)
			}
			proxy := httputil.NewSingleHostReverseProxy(target)
			var fail, fetches atomic.Int32
			if mode == "startup" {
				fail.Store(1)
			}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/v1/peers" {
					fetches.Add(1)
					switch fail.Load() {
					case 1:
						http.Error(w, "directory unavailable", http.StatusServiceUnavailable)
						return
					case 2:
						fmt.Fprint(w, `{"peers":[`)
						return
					}
				}
				proxy.ServeHTTP(w, r)
			}))
			t.Cleanup(srv.Close)
			a := profileUser(t, srv.URL, aSec)
			b := profileUser(t, env.URL, bSec)
			out, stop := startWatch(t, a, "watch", "--for-monitor", "--deliveries-only")
			b.send(aID, "ready", "--hold")
			waitLine(t, out, regexp.MustCompile(` ready$`), 5*time.Second)
			initialFetches := fetches.Load()
			if initialFetches == 0 {
				t.Fatal("watch did not fetch the initial directory")
			}
			fail.Store(1)
			if mode == "refresh-json" {
				fail.Store(2)
			}
			env.NewPeer("session", "Trigger directory update")
			sent := b.send(aID, "still watching", "--hold")
			line := waitLine(t, out, regexp.MustCompile(` delivery `+regexp.QuoteMeta(sent.ThreadID)+` `), 5*time.Second)
			if got := fetches.Load(); got <= initialFetches {
				t.Fatalf("directory update did not retry fetch: before %d, after %d", initialFetches, got)
			}
			// Even when the startup fetch failed, the delivery's sender_info
			// names the sender: no directory lookup is needed (PROTOCOL §14).
			want := " Max: still watching"
			if !strings.HasSuffix(line, want) {
				t.Fatalf("fetch failure lost previous name: %q, want suffix %q", line, want)
			}
			stop()
			beforeInbox := fetches.Load()
			if got := a.mustRun("inbox"); mode != "startup" && !strings.Contains(got, "Max(") {
				t.Fatalf("fetch failure lost persisted name: %q", got)
			}
			if fetches.Load() != beforeInbox {
				t.Fatal("inbox fetched the directory instead of using cached names")
			}
			if mode != "startup" {
				// A new watch retains persisted names when its startup fetch fails.
				restarted, stopAgain := startWatch(t, a, "watch", "--for-monitor")
				waitLine(t, restarted, regexp.MustCompile(` Max: ready$`), 5*time.Second)
				stopAgain()
			}
		})
	}
}

func TestHelpExplainsAddressingAndWatchAck(t *testing.T) {
	out := newUser(t).mustRun("help")
	for _, want := range []string{"recipients are peer ids", "names are refused", "does not ack", "--auto-ack", "vecnet ack <cursor>", "avoid replaying"} {
		if !strings.Contains(out, want) {
			t.Errorf("help lacks %q", want)
		}
	}
}

func TestWatchJSONUnchanged(t *testing.T) {
	env := testhub.Start(t)
	aID, aSec := env.NewPeer("session", "Max")
	bID, bSec := env.NewPeer("session", "Watcher")
	a := profileUser(t, env.URL, aSec)
	b := profileUser(t, env.URL, bSec)
	const text = "hello\n\x1b[2K界"
	sent := a.send(bID, text, "--hold")
	// Global --json wins over --for-monitor and retains the original event.
	out, stop := startWatch(t, b, "--json", "watch", "--for-monitor")
	line := waitLine(t, out, regexp.MustCompile(`"type":"delivery"`), 5*time.Second)
	stop()
	var ev inboxEvent
	if err := json.Unmarshal([]byte(line), &ev); err != nil {
		t.Fatal(err)
	}
	if ev.Cursor == "" || ev.Data.Message.MessageID != sent.MessageID || ev.Data.Message.Sender != aID || ev.Data.Message.Content.Text != text {
		t.Fatalf("watch JSON changed the event: %s", line)
	}
	for _, line := range nonEmptyLines(out.String()) {
		if !json.Valid([]byte(line)) {
			t.Fatalf("watch JSON contains a header or non-JSON line: %q", line)
		}
	}
}

func TestWatchPlainIgnoresShownState(t *testing.T) {
	for _, args := range [][]string{{"watch"}, {"--json", "watch", "--for-monitor"}} {
		t.Run(strings.Join(args, "-"), func(t *testing.T) {
			env := testhub.Start(t)
			_, senderSecret := env.NewPeer("session", "Sender")
			recipient, secret := env.NewPeer("session", "Watcher")
			sender := profileUser(t, env.URL, senderSecret)
			u := profileUser(t, env.URL, secret)
			u.env["XDG_CONFIG_HOME"] = filepath.Join(filepath.Dir(u.conf), "xdg")
			sent := sender.send(recipient, "already shown", "--hold")
			path := filepath.Join(filepath.Dir(u.conf), "state", "p.json")
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}
			raw, _ := json.Marshal(map[string]any{"shown": []string{sent.Deliveries[0].DeliveryID}})
			if err := os.WriteFile(path, raw, 0o600); err != nil {
				t.Fatal(err)
			}
			out, stop := startWatch(t, u, args...)
			waitLine(t, out, regexp.MustCompile(regexp.QuoteMeta(sent.MessageID)), 5*time.Second)
			stop()
			got, err := os.ReadFile(path)
			if err != nil || string(got) != string(raw) {
				t.Fatalf("plain/JSON watch modified shown state: %s, %v", got, err)
			}
		})
	}
}

// A shown-set bound to another identity (a re-pointed profile) must not hide
// this identity's live deliveries: watch resets it before claiming.
func TestWatchMismatchedStateDoesNotSuppressLive(t *testing.T) {
	ev := map[string]any{
		"cursor": "e1.1", "type": "delivery",
		"data": map[string]any{"delivery_id": "dly_X", "message": map[string]any{
			"message_id": "msg_X", "thread_id": "thr_X", "sender": "peer_X",
			"content": map[string]string{"text": "live for the new identity"},
		}},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/v1/peers/me":
			fmt.Fprint(w, `{"kind":"peer","peer_id":"peer_A"}`)
		case r.URL.Path == "/v1/peers":
			fmt.Fprint(w, `{"peers":[]}`)
		case r.URL.Path == "/v1/events" && r.Header.Get("Accept") != "text/event-stream":
			fmt.Fprint(w, `{"events":[],"next_cursor":"e1.0"}`)
		case r.URL.Path == "/v1/events":
			w.Header().Set("Content-Type", "text/event-stream")
			raw, _ := json.Marshal(ev)
			fmt.Fprintf(w, "id: e1.1\nevent: delivery\ndata: %s\n\n", raw)
			w.(http.Flusher).Flush()
			<-r.Context().Done()
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	u := profileUser(t, srv.URL, "vn_peer_test")
	state := filepath.Join(filepath.Dir(u.conf), "state", "p.json")
	if err := os.MkdirAll(filepath.Dir(state), 0o700); err != nil {
		t.Fatal(err)
	}
	stale := `{"hub":"http://other-hub.example","caller":"peer_OTHER","cred":"0123456789abcdef","poll_cursor":"e9.9","shown":["dly_X"]}`
	if err := os.WriteFile(state, []byte(stale), 0o600); err != nil {
		t.Fatal(err)
	}
	out, stop := startWatch(t, u, "watch", "--for-monitor")
	waitLine(t, out, regexp.MustCompile(`live for the new identity$`), 5*time.Second)
	stop()
	raw, err := os.ReadFile(state)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil || got["caller"] != "peer_A" || got["poll_cursor"] != nil || len(got["shown"].([]any)) != 1 {
		t.Fatalf("watch did not rebind the state: %s", raw)
	}
}
