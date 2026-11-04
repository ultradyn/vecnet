package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

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
		if err := w.stream(); err != nil {
			t.Fatal(err)
		}
		if w.cursor != "e1.20" || strings.Count(out.String(), " presence ") != 20 || strings.Contains(out.String(), "hub_restarting") {
			t.Fatalf("finite stream cursor=%q output=%q", w.cursor, out.String())
		}
	}
}

func TestWatchQuietOutageAndRecovery(t *testing.T) {
	for _, restart := range []bool{false, true} {
		t.Run(fmt.Sprint(restart), func(t *testing.T) {
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
			w := watcher{a: &app{ctx: context.Background(), stdout: &out, stderr: &errs}, c: newClient(srv.URL, "test"), forMonitor: true, replayed: true, seen: newDedupe(1024), retry: watchDefaultRetry}
			var delays []time.Duration
			w.sleep = func(_ context.Context, d time.Duration) bool { delays = append(delays, d); return true }
			if err := w.run(); !errors.Is(err, errPeerDisabled) {
				t.Fatalf("terminal=%v", err)
			}
			if strings.Count(errs.String(), "hub restarting; reconnecting") != 2 || strings.Count(errs.String(), "reconnected") != 1 || strings.Contains(errs.String(), "http_503") {
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
	if strings.Count(errs.String(), "hub restarting") != 1 || strings.Count(errs.String(), "reconnected") != 1 || !strings.Contains(errs.String(), "connection refused") {
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
	w := watcher{a: &app{ctx: context.Background(), stdout: &out, stderr: &errs}, c: newClient(srv.URL, "test"), forMonitor: true, replayed: true, connected: true, cursor: "e1.9", seen: newDedupe(1024)}
	err := w.run()
	if !terminal(err) || calls != 2 || !strings.Contains(errs.String(), "cursor_purged") || strings.Contains(errs.String(), "hub restarting") {
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
	w := watcher{a: &app{ctx: context.Background(), stdout: &out, stderr: &errs}, c: newClient(srv.URL, "test"), forMonitor: true, replayed: true, seen: newDedupe(1024), sleep: func(context.Context, time.Duration) bool { return true }}
	if err := w.run(); !terminal(err) {
		t.Fatal(err)
	}
	if strings.Count(errs.String(), "hub restarting") != 1 || strings.Count(errs.String(), "reconnected") != 1 {
		t.Fatalf("status=%q", errs.String())
	}
}
