// Package testhub boots a real hub (store, hub service, httpapi handler) on a
// random loopback port for tests. Nothing it does touches the network beyond
// 127.0.0.1.
package testhub

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/httpapi"
	"github.com/XertroV/vecnet/internal/hub"
	"github.com/XertroV/vecnet/internal/store"
)

// Env is one running test hub. The push worker and tickers are not started
// (Task 17 adds StartWorkers).
type Env struct {
	T              *testing.T
	URL            string
	Hub            *hub.Hub
	OperatorSecret string
	OperatorPeer   string
	Now            *FakeClock
}

// FakeClock is the hub's clock in tests; it only moves when Advance is called.
type FakeClock struct {
	mu sync.Mutex
	t  time.Time
}

// Now returns the current fake time.
func (c *FakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

// Advance moves the clock forward by d.
func (c *FakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// Start opens a store in t.TempDir(), builds the hub on a FakeClock (starting
// at the real current time, millisecond-truncated), serves httpapi on a random
// loopback port and creates the operator. Everything is closed at test end.
func Start(t *testing.T) *Env {
	t.Helper()
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "hub.db"))
	if err != nil {
		t.Fatalf("testhub: open store: %v", err)
	}
	clock := &FakeClock{t: time.Now().UTC().Truncate(time.Millisecond)}

	// The handler is installed after the server exists so Config.PublicURL
	// can carry the server's real URL; no request arrives before then.
	var handler http.Handler
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handler.ServeHTTP(w, r)
	}))

	cfg := hub.DefaultConfig()
	cfg.PublicURL = srv.URL
	cfg.DataDir = dir
	h, err := hub.New(db, cfg, clock.Now)
	if err != nil {
		srv.Close()
		db.Close()
		t.Fatalf("testhub: hub.New: %v", err)
	}
	handler = httpapi.New(httpapi.Deps{Hub: h})
	t.Cleanup(func() {
		srv.Close()
		db.Close()
	})

	peer, secret, _, err := h.EnsureOperator(context.Background(), "operator")
	if err != nil {
		t.Fatalf("testhub: EnsureOperator: %v", err)
	}
	return &Env{T: t, URL: srv.URL, Hub: h, OperatorSecret: secret, OperatorPeer: peer, Now: clock}
}

// NewPeer creates a peer of kind under an implicit principal and enrols it,
// returning its id and long-lived secret. It calls Hub.AddPeer and
// Auth.Enrol directly; the HTTP admin and enrol routes arrive in Task 17.
func (e *Env) NewPeer(kind, name string) (peerID, secret string) {
	e.T.Helper()
	ctx := context.Background()
	id, tok, err := e.Hub.AddPeer(ctx, kind, name, "")
	if err != nil {
		e.T.Fatalf("testhub: AddPeer(%s, %s): %v", kind, name, err)
	}
	got, secret, err := e.Hub.Auth.Enrol(ctx, tok)
	if err != nil {
		e.T.Fatalf("testhub: Enrol(%s): %v", id, err)
	}
	if got != id {
		e.T.Fatalf("testhub: Enrol returned peer %s, want %s", got, id)
	}
	return id, secret
}

// NewPrincipal creates a principal and returns its id and credential.
func (e *Env) NewPrincipal(name string) (prnID, secret string) {
	e.T.Helper()
	prnID, secret, err := e.Hub.AddPrincipal(context.Background(), name)
	if err != nil {
		e.T.Fatalf("testhub: AddPrincipal(%s): %v", name, err)
	}
	return prnID, secret
}

// Do sends one request to the hub and returns the response (body already
// read and closed) with its body. secret "" sends no Authorization header.
// body nil sends none; []byte, string and io.Reader are sent raw; anything
// else is JSON-encoded. A body is sent as application/json.
func (e *Env) Do(secret, method, path string, body any) (*http.Response, []byte) {
	e.T.Helper()
	var r io.Reader
	switch b := body.(type) {
	case nil:
	case []byte:
		r = bytes.NewReader(b)
	case string:
		r = bytes.NewReader([]byte(b))
	case io.Reader:
		r = b
	default:
		j, err := json.Marshal(b)
		if err != nil {
			e.T.Fatalf("testhub: marshal body: %v", err)
		}
		r = bytes.NewReader(j)
	}
	req, err := http.NewRequest(method, e.URL+path, r)
	if err != nil {
		e.T.Fatalf("testhub: new request: %v", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if secret != "" {
		req.Header.Set("Authorization", "Bearer "+secret)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.T.Fatalf("testhub: %s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	out, err := io.ReadAll(resp.Body)
	if err != nil {
		e.T.Fatalf("testhub: read %s %s: %v", method, path, err)
	}
	return resp, out
}
