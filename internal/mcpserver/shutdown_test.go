package mcpserver_test

import (
	"bufio"
	"bytes"
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
	"github.com/XertroV/vecnet/internal/mcpserver"
	"github.com/XertroV/vecnet/internal/testhub"
)

// deadlineWriter models a socket blocked in Write until its deadline fires.
// blockAt=1 covers the initial retry frame, before Stream returns to a handler.
type deadlineWriter struct {
	header           http.Header
	mu               sync.Mutex
	timer            *time.Timer
	writes, blockAt  int
	entered, expired chan struct{}
	once             sync.Once
}

func (w *deadlineWriter) Header() http.Header { return w.header }
func (w *deadlineWriter) WriteHeader(int)     {}
func (w *deadlineWriter) Flush()              {}
func (w *deadlineWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	w.writes++
	blocked := w.writes >= w.blockAt
	w.mu.Unlock()
	if !blocked {
		return len(p), nil
	}
	close(w.entered)
	<-w.expired
	return 0, errors.New("write deadline exceeded")
}
func (w *deadlineWriter) SetWriteDeadline(at time.Time) error {
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

func TestShutdownInterruptsBlockedMCPWriter(t *testing.T) {
	for _, initial := range []bool{false, true} {
		name, blockAt := "notification", 2
		if initial {
			name, blockAt = "initial frame", 1
		}
		t.Run(name, func(t *testing.T) {
			s := mcpserver.New(nil, nil, "http://hub.example")
			stop := make(chan struct{})
			s.Stop = stop
			w := &deadlineWriter{header: http.Header{}, blockAt: blockAt, entered: make(chan struct{}), expired: make(chan struct{})}
			defer func() {
				w.once.Do(func() { close(w.expired) })
				_ = w.SetWriteDeadline(time.Time{})
			}()
			done := make(chan error, 1)
			go func() {
				st, err := s.Stream(w, httptest.NewRequest("GET", "/mcp", nil))
				if err == nil {
					err = st.Send("", "message", map[string]any{"jsonrpc": "2.0", "method": "notifications/resources/updated"})
					st.Close()
				}
				done <- err
			}()
			select {
			case <-w.entered:
			case <-time.After(time.Second):
				t.Fatal("stream never entered blocked write")
			}
			close(stop)
			select {
			case err := <-done:
				if err == nil {
					t.Error("blocked write succeeded after shutdown")
				}
			case <-time.After(2500 * time.Millisecond):
				t.Fatal("blocked MCP writer outlasted shutdown drain budget")
			}
		})
	}
}

// Keep request cancellation subject to the same bounded close as hub shutdown.
func TestCancelledMCPStreamCloses(t *testing.T) {
	s := mcpserver.New(nil, nil, "http://hub.example")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	st, err := s.Stream(httptest.NewRecorder(), httptest.NewRequest("GET", "/mcp", nil).WithContext(ctx))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	cancel()
	select {
	case <-st.Done():
	case <-time.After(time.Second):
		t.Fatal("cancelled request left stream open")
	}
}

// Both MCP eras terminate normally on the wire, without a final JSON-RPC
// result that would tell a subscriptions/listen client to stop reconnecting.
func TestShutdownDrainsMCPStreamsCleanly(t *testing.T) {
	for _, http2 := range []bool{false, true} {
		for _, legacy := range []bool{false, true} {
			name := "http1/listen"
			if http2 {
				name = "http2/listen"
			}
			if legacy {
				name = strings.TrimSuffix(name, "listen") + "GET"
			}
			t.Run(name, func(t *testing.T) {
				env := testhub.Start(t)
				peer, secret := env.NewPeer("session", "reader")
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
				var req *http.Request
				if legacy {
					front := &testhub.Env{T: t, URL: srv.URL}
					resp, body := do(t, front, call{secret: secret, client: srv.Client(), body: initialize(1, mcpserver.ProtoLegacy), headers: map[string]string{"Accept": "application/json, text/event-stream"}})
					if resp.StatusCode != 200 || decodeRPC(t, body).Error != nil {
						t.Fatalf("initialize = %d %s", resp.StatusCode, body)
					}
					req, _ = http.NewRequestWithContext(ctx, "GET", srv.URL+"/mcp", nil)
					req.Header.Set("Mcp-Session-Id", resp.Header.Get("Mcp-Session-Id"))
				} else {
					body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 9, "method": "subscriptions/listen", "params": map[string]any{"resourceSubscriptions": []string{peerURI(peer)}}})
					req, _ = http.NewRequestWithContext(ctx, "POST", srv.URL+"/mcp", bytes.NewReader(body))
					req.Header.Set("Content-Type", "application/json")
					req.Header.Set("MCP-Protocol-Version", mcpserver.ProtoCurrent)
				}
				req.Header.Set("Accept", "application/json, text/event-stream")
				req.Header.Set("Authorization", "Bearer "+secret)
				resp, err := srv.Client().Do(req)
				if err != nil {
					t.Fatal(err)
				}
				defer resp.Body.Close()
				if resp.StatusCode != 200 || (http2 && resp.ProtoMajor != 2) {
					t.Fatalf("stream = %s %d", resp.Proto, resp.StatusCode)
				}
				br := bufio.NewReader(resp.Body)
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
				if strings.Contains(string(tail), "hub_restarting") || strings.Contains(string(tail), `"result":`) {
					t.Errorf("MCP restart has an unpermitted event or final result: %q", tail)
				}
				if err := <-shutdown; err != nil {
					t.Errorf("Shutdown = %v, want nil within grace", err)
				}
			})
		}
	}
}
