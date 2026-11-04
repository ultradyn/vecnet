package httpapi_test

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/httpapi"
	"github.com/XertroV/vecnet/internal/testhub"
)

// Exercise the real HTTP response terminator: cancelling a writer's deadline
// before net/http writes it turns an otherwise graceful end into unexpected EOF
// (HTTP/1) or an INTERNAL_ERROR reset (HTTP/2).
func TestShutdownDrainsSSECleanly(t *testing.T) {
	for _, http2 := range []bool{false, true} {
		for _, path := range []string{"/v1/events", "/v1/admin/activity"} {
			name := "http1" + path
			if http2 {
				name = "http2" + path
			}
			t.Run(name, func(t *testing.T) {
				env := testhub.Start(t)
				stop := make(chan struct{})
				srv := httptest.NewUnstartedServer(httpapi.New(httpapi.Deps{Hub: env.Hub, Stop: stop}))
				httpapi.ConfigureServer(srv.Config)
				srv.Config.RegisterOnShutdown(func() { close(stop) })
				srv.EnableHTTP2 = http2
				if http2 {
					srv.StartTLS()
				} else {
					srv.Start()
				}
				defer srv.Close()
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				req, err := http.NewRequestWithContext(ctx, "GET", srv.URL+path, nil)
				if err != nil {
					t.Fatal(err)
				}
				req.Header.Set("Authorization", "Bearer "+env.OperatorSecret)
				req.Header.Set("Accept", "text/event-stream")
				resp, err := srv.Client().Do(req)
				if err != nil {
					t.Fatal(err)
				}
				defer resp.Body.Close()
				if resp.StatusCode != http.StatusOK || (http2 && resp.ProtoMajor != 2) {
					t.Fatalf("stream = %s %d", resp.Proto, resp.StatusCode)
				}
				br := bufio.NewReader(resp.Body)
				// The initial retry frame proves the stream is open and flushed.
				for {
					line, err := br.ReadString('\n')
					if err != nil {
						t.Fatal(err)
					}
					if line == "\n" {
						break
					}
				}
				shutdown := make(chan error, 1)
				go func() { shutdown <- srv.Config.Shutdown(ctx) }()
				tail, err := io.ReadAll(br)
				if err != nil {
					t.Errorf("stream end = %v, want clean EOF; tail %q", err, tail)
				}
				const restart = "event: hub_restarting\nretry: 1000\ndata: {\"retry_after_ms\":1000}\n\n"
				if !strings.HasSuffix(string(tail), restart) || strings.Count(string(tail), "event: hub_restarting\n") != 1 {
					t.Errorf("tail = %q, want one final restart frame %q", tail, restart)
				}
				if err := <-shutdown; err != nil {
					t.Errorf("Shutdown = %v, want nil within grace", err)
				}
			})
		}
	}
}

func TestShutdownDrainsLongPoll(t *testing.T) {
	env := testhub.Start(t)
	peer, secret := env.NewPeer("session", "reader")
	stop := make(chan struct{})
	srv := httptest.NewUnstartedServer(httpapi.New(httpapi.Deps{Hub: env.Hub, Stop: stop}))
	httpapi.ConfigureServer(srv.Config)
	srv.Config.RegisterOnShutdown(func() { close(stop) })
	srv.Start()
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", srv.URL+"/v1/events?after=head&wait=30", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+secret)
	type result struct {
		body   []byte
		status int
		err    error
	}
	done := make(chan result, 1)
	go func() {
		resp, err := srv.Client().Do(req)
		if err != nil {
			done <- result{err: err}
			return
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		done <- result{body: body, status: resp.StatusCode, err: err}
	}()
	for env.Hub.Log.Waiters(peer) == 0 {
		select {
		case <-ctx.Done():
			t.Fatal("long poll did not park")
		case <-time.After(time.Millisecond):
		}
	}
	if err := srv.Config.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown = %v", err)
	}
	r := <-done
	if r.err != nil || r.status != 200 {
		t.Fatalf("long poll end = %d %v %s", r.status, r.err, r.body)
	}
	var poll pollResp
	if err := json.Unmarshal(r.body, &poll); err != nil || len(poll.Events) != 0 || poll.NextCursor == "" {
		t.Fatalf("long poll = %s (%v), want empty events with resume cursor", r.body, err)
	}
}

// A long poll may already be writing its JSON when Stop closes. Its normal
// wait+write budget must not let a stalled reader consume the shutdown grace.
type blockedPollWriter struct {
	header           http.Header
	mu               sync.Mutex
	timer            *time.Timer
	entered, expired chan struct{}
	once             sync.Once
}

func (w *blockedPollWriter) Header() http.Header { return w.header }
func (w *blockedPollWriter) WriteHeader(int)     {}
func (w *blockedPollWriter) Write(p []byte) (int, error) {
	close(w.entered)
	<-w.expired
	return 0, errors.New("write deadline exceeded")
}
func (w *blockedPollWriter) SetWriteDeadline(at time.Time) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.timer != nil {
		w.timer.Stop()
	}
	if !at.IsZero() {
		w.timer = time.AfterFunc(time.Until(at), func() { w.once.Do(func() { close(w.expired) }) })
	}
	return nil
}
func TestShutdownInterruptsBlockedLongPollWriter(t *testing.T) {
	env := testhub.Start(t)
	_, secret := env.NewPeer("session", "reader")
	stop := make(chan struct{})
	h := httpapi.New(httpapi.Deps{Hub: env.Hub, Stop: stop})
	w := &blockedPollWriter{header: http.Header{}, entered: make(chan struct{}), expired: make(chan struct{})}
	defer func() { w.once.Do(func() { close(w.expired) }); _ = w.SetWriteDeadline(time.Time{}) }()
	req := httptest.NewRequest("GET", "/v1/events?after=head&wait=0", nil)
	req.Header.Set("Authorization", "Bearer "+secret)
	done := make(chan struct{})
	go func() { h.ServeHTTP(w, req); close(done) }()
	select {
	case <-w.entered:
	case <-time.After(time.Second):
		t.Fatal("long poll never entered Write")
	}
	close(stop)
	select {
	case <-done:
	case <-time.After(2500 * time.Millisecond):
		t.Fatal("blocked long-poll writer outlasted shutdown drain budget")
	}
}
