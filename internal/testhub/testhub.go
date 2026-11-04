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
	"sync/atomic"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/events"
	"github.com/XertroV/vecnet/internal/httpapi"
	"github.com/XertroV/vecnet/internal/hub"
	"github.com/XertroV/vecnet/internal/oauth"
	"github.com/XertroV/vecnet/internal/push"
	"github.com/XertroV/vecnet/internal/store"
)

// Env is one running test hub. Worker is the push worker on the fake clock;
// it is wired to the handler (Deps.Kick, Deps.VerifyRoute) but runs only
// when a test calls Worker.Step or StartWorkers. Events, the MCP Events
// worker, is the same: it runs on Events.Step or StartWorkers. OAuth is the
// handler's OAuth server, also on the fake clock; a test may change its fields
// between requests.
type Env struct {
	T              *testing.T
	URL            string
	Hub            *hub.Hub
	Worker         *push.Worker
	Events         *events.Worker
	OAuth          *oauth.Server
	Drain          *httpapi.Drain
	OperatorSecret string
	OperatorPeer   string
	Now            *FakeClock

	stack   *httpapi.Stack
	stop    chan struct{}
	handler atomic.Pointer[http.Handler]
	started bool
}

// WorkerInterval is the push worker poll and ticker period StartWorkers uses.
const WorkerInterval = 50 * time.Millisecond

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
// The hub runs with Config.AllowPrivateEgress = true, since every test
// receiver is an httptest server on 127.0.0.1.
func Start(t *testing.T) *Env {
	t.Helper()
	return StartWith(t, nil)
}

// StartWith is Start with cfgFn (when non-nil) adjusting the hub Config
// after the testhub defaults are set and before the hub is built.
func StartWith(t *testing.T, cfgFn func(*hub.Config)) *Env {
	t.Helper()
	return StartWithServer(t, cfgFn, nil)
}

// StartWithServer is StartWith with srvFn (when non-nil) adjusting the test
// server's http.Server, e.g. its timeouts, before it starts.
func StartWithServer(t *testing.T, cfgFn func(*hub.Config), srvFn func(*http.Server)) *Env {
	t.Helper()
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "hub.db"))
	if err != nil {
		t.Fatalf("testhub: open store: %v", err)
	}
	clock := &FakeClock{t: time.Now().UTC().Truncate(time.Millisecond)}
	return startDB(t, db, dir, clock, cfgFn, srvFn)
}

// CloneDB starts another hub over a VACUUM INTO snapshot. Database-backed
// identity, peers and credentials survive, while the URL and clock are its own.
func (e *Env) CloneDB(t *testing.T) *Env {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "hub.db")
	if _, err := e.Hub.DB.ExecContext(context.Background(), `VACUUM INTO ?`, path); err != nil {
		t.Fatalf("testhub: clone database: %v", err)
	}
	db, err := store.Open(path)
	if err != nil {
		t.Fatalf("testhub: open cloned store: %v", err)
	}
	env := startDB(t, db, dir, &FakeClock{t: e.Now.Now()}, func(c *hub.Config) {
		url, dataDir := c.PublicURL, c.DataDir
		*c = e.Hub.Cfg
		c.PublicURL, c.DataDir = url, dataDir
	}, nil)
	env.OperatorSecret, env.OperatorPeer = e.OperatorSecret, e.OperatorPeer
	return env
}

func startDB(t *testing.T, db *store.DB, dir string, clock *FakeClock, cfgFn func(*hub.Config), srvFn func(*http.Server)) *Env {
	t.Helper()

	// The handler is installed after the server exists so Config.PublicURL
	// can carry the server's real URL; no request arrives before then.
	env := &Env{T: t, Now: clock, Drain: httpapi.NewDrain()}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		(*env.handler.Load()).ServeHTTP(w, r)
	}))
	if srvFn != nil {
		srvFn(srv.Config)
	}
	srv.Start()

	cfg := hub.DefaultConfig()
	cfg.PublicURL = srv.URL
	cfg.DataDir = dir
	cfg.AllowPrivateEgress = true
	if cfgFn != nil {
		cfgFn(&cfg)
	}
	h, err := hub.New(db, cfg, clock.Now)
	if err != nil {
		srv.Close()
		db.Close()
		t.Fatalf("testhub: hub.New: %v", err)
	}
	env.URL, env.Hub = srv.URL, h
	// The production wiring, with the fake clock and short intervals on top.
	env.stack = httpapi.NewStack(h)
	env.stack.TickEvery = WorkerInterval
	env.Worker, env.Events, env.OAuth = env.stack.Push, env.stack.Events, env.stack.OAuth
	env.Worker.Now, env.Worker.Interval = clock.Now, WorkerInterval
	env.Events.Store, env.Events.Log = events.NewStore(h), h.Log
	env.Events.Now, env.Events.Interval = clock.Now, WorkerInterval
	env.OAuth.AllowedRedirects = append(env.OAuth.AllowedRedirects, "http://127.0.0.1:*")
	env.stop = make(chan struct{})
	env.Configure(nil)
	t.Cleanup(func() {
		close(env.stop) // ends session watchers and open streams
		srv.Close()
		db.Close()
	})

	peer, secret, _, err := h.EnsureOperator(context.Background(), "operator")
	if err != nil {
		t.Fatalf("testhub: EnsureOperator: %v", err)
	}
	env.OperatorSecret, env.OperatorPeer = secret, peer
	return env
}

// Configure rebuilds the HTTP handler from Deps{Hub: e.Hub} after f (when
// non-nil) adjusts them, e.g. to shorten Deps.Heartbeat. Requests already in
// flight keep the old handler; later ones use the new one.
func (e *Env) Configure(f func(*httpapi.Deps)) {
	d := e.stack.Deps()
	// Tests send many anonymous requests from 127.0.0.1; a test of the
	// per-client limiter sets Deps.Limiter itself.
	d.Limiter = nil
	d.Stop = e.stop
	d.Drain = e.Drain
	if f != nil {
		f(&d)
	}
	h := httpapi.New(d)
	e.handler.Store(&h)
}

// StartWorkers runs the push worker, the Events worker and the hub's tickers
// (Hub.RunTickers) on the fake clock, each every WorkerInterval, until the
// test ends. Call it
// at most once.
func (e *Env) StartWorkers() {
	e.T.Helper()
	if e.started {
		e.T.Fatal("testhub: StartWorkers called twice")
	}
	e.started = true
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Go(func() { e.stack.Run(ctx) })
	// Registered after Start's cleanup, so it runs first: the workers stop
	// before the store closes.
	e.T.Cleanup(func() {
		cancel()
		wg.Wait()
	})
}

// NewPeer creates a peer of kind under an implicit principal and enrols it,
// returning its id and long-lived secret. It calls Hub.AddPeer and
// Auth.Enrol directly, not the HTTP admin and enrol routes, so tests of
// other features do not depend on them. A dot has no secret, so its is "": a
// test that needs a dot's credential logs in through OAuth, and one that only
// needs a bearer on /mcp uses another kind.
func (e *Env) NewPeer(kind, name string) (peerID, secret string) {
	e.T.Helper()
	ctx := context.Background()
	id, tok, err := e.Hub.AddPeer(ctx, kind, name, "")
	if err != nil {
		e.T.Fatalf("testhub: AddPeer(%s, %s): %v", kind, name, err)
	}
	if kind == "dot" {
		return id, ""
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

// SetLimits sets peerID's per-peer rate limits (PROTOCOL §16.2) by updating
// its peers row; tests that send in bulk raise them with it.
func (e *Env) SetLimits(peerID string, sendsPerMin, threadsPerHour int) {
	e.T.Helper()
	res, err := e.Hub.DB.Exec(`UPDATE peers SET sends_per_min = ?, threads_per_hour = ? WHERE peer_id = ?`,
		sendsPerMin, threadsPerHour, peerID)
	if err != nil {
		e.T.Fatalf("testhub: SetLimits(%s): %v", peerID, err)
	}
	if n, err := res.RowsAffected(); err != nil || n != 1 {
		e.T.Fatalf("testhub: SetLimits(%s): %d rows, %v; want 1", peerID, n, err)
	}
}
