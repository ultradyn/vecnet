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
	"strings"
	"testing"
	"time"
)

type watchCancelReader struct {
	r      io.Reader
	cancel context.CancelFunc
}

func (r watchCancelReader) Read(p []byte) (int, error) { r.cancel(); return r.r.Read(p) }

func TestWatchCanceledLineReaderReportsTerminalError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	lines := make(chan string)
	errs := make(chan error, 1)
	readLines(ctx, watchCancelReader{strings.NewReader("data: queued\n\n"), cancel}, lines, errs)
	if _, ok := <-lines; ok {
		t.Fatal("line channel still open")
	}
	select {
	case err := <-errs:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("terminal=%v", err)
		}
	default:
		t.Fatal("canceled reader closed lines without terminal error")
	}
}

func TestWatchCanceledBufferedStreamReturns(t *testing.T) {
	for i := 0; i < 200; i++ {
		ctx, cancel := context.WithCancel(context.Background())
		c := newClient("http://localhost", "test")
		c.hc.Transport = relayRoundTrip(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(watchCancelReader{strings.NewReader(strings.Repeat("data: queued\n\n", 100)), cancel})}, nil
		})
		var out, errs bytes.Buffer
		w := watcher{a: &app{ctx: ctx, stdout: &out, stderr: &errs}, c: c, forMonitor: true, seen: newDedupe(1024)}
		done := make(chan error, 1)
		go func() { done <- w.stream() }()
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(200 * time.Millisecond):
			cancel()
			t.Fatalf("canceled stream %d did not return", i)
		}
		cancel()
	}
}

// A finite response must drain its last event before consuming EOF. The
// restart control carries no durable cursor and must never reach stdout.
func TestWatchFiniteRestartPreservesLastEvent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for i := 1; i <= 20; i++ {
			fmt.Fprintf(w, "id: e1.%d\nevent: presence\ndata: {\"cursor\":\"e1.%d\",\"type\":\"presence\",\"data\":{}}\n\n", i, i)
		}
		fmt.Fprint(w, "retry: 1000\nevent: hub_restarting\ndata: {\"retry_after_ms\":1000}\n\n")
	}))
	defer srv.Close()
	for i := 0; i < 30; i++ {
		var out, errs bytes.Buffer
		w := watcher{a: &app{ctx: context.Background(), stdout: &out, stderr: &errs}, c: newClient(srv.URL, "test"), forMonitor: true, seen: newDedupe(1024)}
		// The restart frame carries a retry_after_ms hint, so the stream ends
		// with errRestarting (the hint is honoured, PROTOCOL §21.3).
		if err := w.stream(); !errors.Is(err, errRestarting) {
			t.Fatalf("stream end = %v, want errRestarting", err)
		}
		if w.cursor != "e1.20" || strings.Count(out.String(), " presence ") != 20 || strings.Contains(out.String(), "hub_restarting") {
			t.Fatalf("finite stream cursor=%q output=%q", w.cursor, out.String())
		}
	}
}

func TestWatchQuietOutageAndRecovery(t *testing.T) {
	for _, restart := range []bool{false, true} {
		for _, sustained := range []bool{false, true} {
			t.Run(fmt.Sprintf("restart=%t/sustained=%t", restart, sustained), func(t *testing.T) {
				var calls int
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path == "/v1/peers" {
						fmt.Fprint(w, `{"peers":[]}`)
						return
					}
					calls++
					if calls == 1 && restart {
						w.Header().Set("Content-Type", "text/event-stream")
						fmt.Fprint(w, "retry: 3000\n\nevent: hub_restarting\nretry: 1000\ndata: {\"retry_after_ms\":1000}\n\n")
						return
					}
					if calls <= 2 {
						w.WriteHeader(503)
						return
					}
					if calls == 3 {
						w.Header().Set("Content-Type", "text/event-stream")
						fmt.Fprint(w, "id: e1.7\nevent: presence\ndata: {\"cursor\":\"e1.7\",\"type\":\"presence\",\"data\":{}}\n\n")
						return
					}
					if r.Header.Get("Last-Event-ID") != "e1.7" {
						t.Errorf("resume cursor = %q", r.Header.Get("Last-Event-ID"))
					}
					w.WriteHeader(204)
				}))
				defer srv.Close()
				var out, errs bytes.Buffer
				w := watcher{identityOff: true, a: &app{ctx: context.Background(), stdout: &out, stderr: &errs}, c: newClient(srv.URL, "test"), forMonitor: true, replayed: true, seen: newDedupe(1024), retry: watchDefaultRetry}
				now := time.Unix(100, 0)
				w.now = func() time.Time { return now }
				var delays []time.Duration
				w.sleep = func(_ context.Context, d time.Duration) bool {
					delays = append(delays, d)
					if sustained {
						now = now.Add(6 * time.Second)
					}
					return true
				}
				if err := w.run(); !errors.Is(err, errPeerDisabled) {
					t.Fatalf("terminal=%v", err)
				}
				wantStatus := ""
				if sustained {
					wantStatus = "vecnet: watch: hub unreachable for 6s; reconnecting…\nvecnet: watch: reconnected after 12s\n"
				}
				if errs.String() != wantStatus {
					t.Fatalf("status=%q", errs.String())
				}
				if len(delays) == 0 || delays[0] < 750*time.Millisecond || delays[0] > 1250*time.Millisecond {
					t.Fatalf("first delays=%v", delays)
				}
				if strings.Contains(out.String(), "hub_restarting") || strings.Count(out.String(), "presence") != 1 {
					t.Fatalf("stdout=%q", out.String())
				}
			})
		}
	}
}

func TestWatchVerboseOnlyAfterSustainedOutage(t *testing.T) {
	var errs bytes.Buffer
	now := time.Unix(100, 0)
	status := reconnectStatus{out: &errs, name: "watch", now: func() time.Time { return now }}
	status.failed("unexpected EOF")
	now = now.Add(59 * time.Second)
	status.failed("connection refused")
	if strings.Contains(errs.String(), "refused") || strings.Contains(errs.String(), "EOF") {
		t.Fatalf("early details: %q", errs.String())
	}
	now = now.Add(time.Second)
	status.failed("connection refused")
	status.live()
	if strings.Count(errs.String(), "hub unreachable for 59s") != 1 || strings.Count(errs.String(), "reconnected after 60s") != 1 || !strings.Contains(errs.String(), "connection refused") {
		t.Fatalf("outage status: %q", errs.String())
	}
	for i := 1; i < 100; i++ {
		if d := reconnectDelay(i); d > 24*time.Second || d < 800*time.Millisecond {
			t.Fatalf("delay %v", d)
		}
	}
}

func TestWatchPurgedCursorThenAuthStops(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/peers" {
			fmt.Fprint(w, `{"peers":[]}`)
			return
		}
		calls++
		if calls == 1 {
			w.WriteHeader(409)
			fmt.Fprint(w, `{"error":{"code":"cursor_purged","details":{"oldest_cursor":"e2.5"}}}`)
			return
		}
		if r.URL.Query().Get("after") != "e2.5" || r.Header.Get("Last-Event-ID") != "" {
			t.Errorf("purged resume=%s header=%q", r.URL.RawQuery, r.Header.Get("Last-Event-ID"))
		}
		w.WriteHeader(401)
	}))
	defer srv.Close()
	var out, errs bytes.Buffer
	w := watcher{identityOff: true, a: &app{ctx: context.Background(), stdout: &out, stderr: &errs}, c: newClient(srv.URL, "test"), forMonitor: true, replayed: true, connected: true, cursor: "e1.9", seen: newDedupe(1024)}
	err := w.run()
	if !terminal(err) || calls != 2 || !strings.Contains(errs.String(), "cursor_purged") || strings.Contains(errs.String(), "reconnect") {
		t.Fatalf("calls=%d err=%v stderr=%q", calls, err, errs.String())
	}
}

func TestWatchSuccessfulPollIsRecovery(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/peers" {
			fmt.Fprint(w, `{"peers":[]}`)
			return
		}
		calls++
		if calls <= 3 {
			w.WriteHeader(503)
			return
		}
		if calls == 4 {
			fmt.Fprint(w, `{"events":[],"next_cursor":"e1.4"}`)
			return
		}
		w.WriteHeader(401)
	}))
	defer srv.Close()
	var out, errs bytes.Buffer
	now := time.Unix(100, 0)
	w := watcher{identityOff: true, a: &app{ctx: context.Background(), stdout: &out, stderr: &errs}, c: newClient(srv.URL, "test"), forMonitor: true, replayed: true, seen: newDedupe(1024), now: func() time.Time { return now }, sleep: func(context.Context, time.Duration) bool { now = now.Add(6 * time.Second); return true }}
	if err := w.run(); !terminal(err) {
		t.Fatal(err)
	}
	if strings.Count(errs.String(), "hub unreachable for 6s") != 1 || strings.Count(errs.String(), "reconnected after 18s") != 1 {
		t.Fatalf("status=%q", errs.String())
	}
}

// TestWatchJSONStopLine: a terminal answer ends `watch --json` with one
// client-made watch_stopped line carrying the HTTP status, so a supervisor
// (the Codex connector) can tell re-auth/disabled from a crash. Recoverable
// failures and other output modes print no such line.
func TestWatchJSONStopLine(t *testing.T) {
	for _, tc := range []struct {
		status int
		json   bool
	}{{401, true}, {403, true}, {204, true}, {401, false}} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/v1/peers" {
				fmt.Fprint(w, `{"peers":[]}`)
				return
			}
			w.WriteHeader(tc.status)
			if tc.status != 204 {
				fmt.Fprint(w, `{"error":{"code":"unauthorized","message":"<b>bad</b>"}}`)
			}
		}))
		var out, errs bytes.Buffer
		w := watcher{identityOff: true, a: &app{ctx: context.Background(), stdout: &out, stderr: &errs, json: tc.json}, c: newClient(srv.URL, "test"), replayed: true, connected: true, cursor: "e1.1", seen: newDedupe(1024)}
		err := w.run()
		srv.Close()
		if !terminal(err) {
			t.Fatalf("status %d: err=%v", tc.status, err)
		}
		if !tc.json {
			if strings.Contains(out.String(), "watch_stopped") {
				t.Fatalf("plain watch printed %q", out.String())
			}
			continue
		}
		lines := strings.Split(strings.TrimSpace(out.String()), "\n")
		var ev struct {
			Type   string `json:"type"`
			Cursor string `json:"cursor"`
			Data   struct {
				Text   string `json:"text"`
				Status int    `json:"status"`
			} `json:"data"`
		}
		if err := json.Unmarshal([]byte(lines[len(lines)-1]), &ev); err != nil || ev.Type != "watch_stopped" || ev.Cursor != "" || ev.Data.Status != tc.status || strings.Contains(ev.Data.Text, "<b>") {
			t.Fatalf("status %d: last line %q (%v)", tc.status, lines[len(lines)-1], err)
		}
	}
}
