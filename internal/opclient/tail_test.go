package opclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync/atomic"
	"testing"
	"time"
)

func eventPage(w http.ResponseWriter, cursor string, events bool) {
	body := `{"events":[],"next_cursor":%q}`
	if events {
		body = `{"events":[{"cursor":%q,"type":"presence","at":"2026-11-02T00:00:00.000Z","data":{"peer_id":"peer_a","state":"idle"}}],"next_cursor":%q}`
		fmt.Fprintf(w, body, cursor, cursor)
		return
	}
	fmt.Fprintf(w, body, cursor)
}
func TestTailFirstCallHasNoAfter(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/ack" {
			w.WriteHeader(204)
			return
		}
		n := calls.Add(1)
		if r.Header.Get("Accept") != "application/json" || r.Header.Get("Authorization") != "Bearer secret" {
			t.Errorf("headers %v", r.Header)
		}
		if n == 1 {
			if r.URL.Query().Has("after") || r.URL.Query().Get("wait") != "0" {
				t.Errorf("initial query %s", r.URL)
			}
			eventPage(w, "e7.45", false)
			return
		}
		if r.URL.Query().Get("after") != "e7.45" || r.URL.Query().Get("wait") != "25" {
			t.Errorf("tail query %s", r.URL)
		}
		w.WriteHeader(401)
	}))
	defer srv.Close()
	err := New(srv.URL, "secret").Tail(context.Background(), func(b Batch) error {
		if b.Cursor != "e7.45" || len(b.Events) != 0 || b.Reset {
			t.Errorf("batch %+v", b)
		}
		return nil
	}, nil)
	if !errors.Is(err, ErrUnauthorized) || calls.Load() != 2 {
		t.Fatalf("err=%v calls=%d", err, calls.Load())
	}
}
func TestTailAcksAfterHandle(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	acked := make(chan string, 1)
	var returned atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/ack" {
			var b struct {
				Cursor string `json:"cursor"`
			}
			if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
				t.Error(err)
			}
			if !returned.Load() {
				t.Error("ack arrived before reload completed")
			}
			acked <- b.Cursor
			w.WriteHeader(204)
			return
		}
		if r.URL.Query().Get("after") == "e7.46" {
			w.WriteHeader(401)
			return
		}
		eventPage(w, "e7.46", true)
	}))
	defer srv.Close()
	done := make(chan error, 1)
	go func() {
		done <- New(srv.URL, "secret").Tail(context.Background(), func(b Batch) error {
			if len(b.Events) != 1 || b.Events[0].Type != "presence" || string(b.Events[0].Data) != `{"peer_id":"peer_a","state":"idle"}` {
				t.Errorf("events %+v", b)
			}
			close(entered)
			<-release
			returned.Store(true)
			return nil
		}, nil)
	}()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("no handle")
	}
	select {
	case <-acked:
		t.Error("premature ack")
	default:
	}
	close(release)
	select {
	case c := <-acked:
		if c != "e7.46" {
			t.Errorf("ack %q", c)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no ack")
	}
	select {
	case err := <-done:
		if !errors.Is(err, ErrUnauthorized) {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("tail did not stop")
	}
}
func TestTailPurgedCursor(t *testing.T) {
	var calls int
	var reset bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/ack" {
			w.WriteHeader(204)
			return
		}
		calls++
		switch calls {
		case 1:
			w.WriteHeader(409)
			fmt.Fprint(w, `{"error":{"code":"cursor_purged","details":{"oldest_cursor":"e9.100"}}}`)
		case 2:
			if !reset {
				t.Error("resumed before reset reload completed")
			}
			if r.URL.Query().Get("after") != "e9.100" {
				t.Errorf("resume %s", r.URL)
			}
			eventPage(w, "e9.101", true)
		default:
			w.WriteHeader(401)
		}
	}))
	defer srv.Close()
	err := New(srv.URL, "secret").Tail(context.Background(), func(b Batch) error {
		if b.Reset {
			if b.Cursor != "e9.100" {
				t.Errorf("reset cursor %s", b.Cursor)
			}
			reset = true
		}
		return nil
	}, nil)
	if !errors.Is(err, ErrUnauthorized) || !reset || calls != 3 {
		t.Fatalf("error=%v reset=%v calls=%d", err, reset, calls)
	}
}
func TestTailTerminals(t *testing.T) {
	for _, tt := range []struct {
		code int
		want error
	}{{401, ErrUnauthorized}, {403, ErrForbidden}, {204, ErrDisabled}} {
		t.Run(fmt.Sprint(tt.code), func(t *testing.T) {
			var calls atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(tt.code) }))
			defer srv.Close()
			err := New(srv.URL, "secret").Tail(context.Background(), func(Batch) error { t.Error("terminal called handler"); return nil }, nil)
			if !errors.Is(err, tt.want) || calls.Load() != 1 {
				t.Fatalf("err=%v calls=%d", err, calls.Load())
			}
		})
	}
}
func TestTailBackoff(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		switch calls {
		case 8:
			eventPage(w, "e4.2", false)
		case 10:
			w.WriteHeader(401)
		default:
			w.WriteHeader(502)
		}
	}))
	defer srv.Close()
	c := New(srv.URL, "secret")
	var delays []time.Duration
	var states []string
	c.sleep = func(ctx context.Context, d time.Duration) error { delays = append(delays, d); return nil }
	err := c.Tail(context.Background(), func(Batch) error { return nil }, func(s string) { states = append(states, s) })
	if !errors.Is(err, ErrUnauthorized) {
		t.Fatal(err)
	}
	want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second, 30 * time.Second, 30 * time.Second, time.Second}
	if reflect.DeepEqual(delays, want) {
		t.Fatalf("retries have no jitter: %v", delays)
	}
	if len(delays) != len(want) {
		t.Fatalf("delays %v", delays)
	}
	for i, d := range delays {
		base := min(want[i], 20*time.Second)
		if d < base*4/5 || d > base*6/5 {
			t.Fatalf("delay %d = %v, base %v", i, d, base)
		}
	}
	if len(states) == 0 || states[0] != "connecting" {
		t.Fatalf("states %v", states)
	}
	var offline, live bool
	for _, s := range states {
		switch s {
		case "offline":
			offline = true
		case "live":
			live = true
		case "connecting":
		default:
			t.Errorf("unknown status %q", s)
		}
	}
	if !offline || !live {
		t.Fatalf("states %v", states)
	}
	t.Run("cancel sleep", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		done := make(chan error, 1)
		go func() {
			done <- New(srv.URL, "secret").Tail(ctx, func(Batch) error { return nil }, func(s string) {
				if s == "offline" {
					cancel()
				}
			})
		}()
		select {
		case err := <-done:
			if !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
		case <-time.After(time.Second):
			t.Fatal("cancellation did not interrupt backoff")
		}
	})
	t.Run("cancel request", func(t *testing.T) {
		entered := make(chan struct{})
		blocked := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(entered); <-r.Context().Done() }))
		defer blocked.Close()
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		done := make(chan error, 1)
		go func() { done <- New(blocked.URL, "secret").Tail(ctx, nil, nil) }()
		<-entered
		cancel()
		select {
		case err := <-done:
			if !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
		case <-time.After(time.Second):
			t.Fatal("cancellation did not interrupt request")
		}
	})
}

func TestTailRetriesHandleAndAck(t *testing.T) {
	for _, fail := range []string{"handle", "ack"} {
		t.Run(fail, func(t *testing.T) {
			var polls, handles, acks int
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/v1/ack" {
					acks++
					if fail == "ack" && acks == 1 {
						w.WriteHeader(502)
					} else {
						w.WriteHeader(204)
					}
					return
				}
				polls++
				if polls <= 2 {
					if r.URL.Query().Has("after") {
						t.Error("cursor advanced before batch committed")
					}
					eventPage(w, "e2.9", true)
				} else {
					w.WriteHeader(401)
				}
			}))
			defer srv.Close()
			c := New(srv.URL, "secret")
			c.sleep = func(context.Context, time.Duration) error { return nil }
			err := c.Tail(context.Background(), func(Batch) error {
				handles++
				if fail == "handle" && handles == 1 {
					return errors.New("reload failed")
				}
				return nil
			}, nil)
			if !errors.Is(err, ErrUnauthorized) || handles != 2 {
				t.Fatalf("err=%v handles=%d", err, handles)
			}
			want := 2
			if fail == "handle" {
				want = 1
			}
			if acks != want {
				t.Fatalf("acks=%d want=%d", acks, want)
			}
		})
	}
}
