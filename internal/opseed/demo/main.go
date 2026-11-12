// The demo command is intentionally outside cmd/: it never ships in the hub image.
package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/httpapi"
	"github.com/XertroV/vecnet/internal/hub"
	"github.com/XertroV/vecnet/internal/opseed"
	"github.com/XertroV/vecnet/internal/prefs"
	"github.com/XertroV/vecnet/internal/store"
)

type peer struct {
	ID     string `json:"id"`
	Secret string `json:"secret,omitempty"`
}
type manifest struct {
	URL            string            `json:"url"`
	OperatorSecret string            `json:"operator_secret"`
	OperatorPeer   string            `json:"operator_peer"`
	Peers          map[string]peer   `json:"peers"`
	Threads        map[string]string `json:"threads"`
	Approvals      []string          `json:"approvals"`
	DeadLetters    []string          `json:"dead_letters"`
}

type controls struct {
	drain     *httpapi.Drain
	restartMu sync.Mutex
	streams   atomic.Int64
	// wallNow and wait are injectable so control tests never race wall time.
	wallNow      func() time.Time
	wait         func(context.Context, time.Duration, <-chan struct{})
	offset       atomic.Int64
	mu           sync.Mutex
	stallUntil   time.Time
	stallRelease chan struct{}
	stalled      int
	failUntil    time.Time
	failPrefix   string
	failStatus   int
}

func (c *controls) realNow() time.Time {
	if c.wallNow != nil {
		return c.wallNow()
	}
	return time.Now()
}
func (c *controls) now() time.Time { return c.realNow().Add(time.Duration(c.offset.Load())) }

func (c *controls) waitForRelease(ctx context.Context, d time.Duration, release <-chan struct{}) {
	if c.wait != nil {
		c.wait(ctx, d, release)
		return
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-release:
	case <-ctx.Done():
	}
}

// clearStall is called with mu held; it releases requests already waiting.
func (c *controls) clearStall() {
	c.stallUntil = time.Time{}
	if c.stallRelease != nil {
		close(c.stallRelease)
		c.stallRelease = nil
	}
}
func (c *controls) handler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/__demo/sink/") && r.Method == http.MethodPost {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/__demo/") {
			w.Header().Set("Content-Type", "application/json")
			if r.Method != http.MethodPost {
				w.Header().Set("Allow", http.MethodPost)
				writeDemoError(w, http.StatusMethodNotAllowed, "method_not_allowed", "use POST for demo controls")
				return
			}
			switch r.URL.Path {
			case "/__demo/restart":
				var in struct {
					RetryAfterMS *int64 `json:"retry_after_ms"`
				}
				if !readControl(w, r, &in) {
					return
				}
				if in.RetryAfterMS == nil || *in.RetryAfterMS < 0 || *in.RetryAfterMS > 60000 {
					writeDemoError(w, http.StatusBadRequest, "invalid_body", "retry_after_ms must be in 0..60000")
					return
				}
				// Serialize generations; stream handlers remain free to finish.
				c.restartMu.Lock()
				defer c.restartMu.Unlock()
				streams := c.streams.Load()
				c.drain.Announce(time.Duration(*in.RetryAfterMS) * time.Millisecond)
				c.drain.Wait(2 * time.Second)
				c.drain.Reset()
				_ = json.NewEncoder(w).Encode(map[string]int64{"streams": streams})
				return
			case "/__demo/status":
				if !readControl(w, r, &struct{}{}) {
					return
				}
				c.mu.Lock()
				stalled := c.stalled
				c.mu.Unlock()
				_ = json.NewEncoder(w).Encode(map[string]int{"stalled_requests": stalled})
				return
			case "/__demo/clear":
				var in struct {
					Control string `json:"control"`
				}
				if !readControl(w, r, &in) {
					return
				}
				c.mu.Lock()
				switch in.Control {
				case "stall":
					c.clearStall()
				case "fail":
					c.failUntil = time.Time{}
				default:
					c.mu.Unlock()
					writeDemoError(w, http.StatusBadRequest, "invalid_body", "invalid control")
					return
				}
				c.mu.Unlock()
				_ = json.NewEncoder(w).Encode(map[string]bool{"cleared": true})
				return
			case "/__demo/advance":
				var in struct {
					By string `json:"by"`
				}
				if !readControl(w, r, &in) {
					return
				}
				d, ok := controlDuration(w, in.By)
				if !ok {
					return
				}
				c.offset.Add(int64(d))
			case "/__demo/stall":
				var in struct {
					For string `json:"for"`
				}
				if !readControl(w, r, &in) {
					return
				}
				d, ok := controlDuration(w, in.For)
				if !ok {
					return
				}
				c.mu.Lock()
				c.clearStall()
				c.stallRelease = make(chan struct{})
				c.stallUntil = c.realNow().Add(d)
				c.mu.Unlock()
			case "/__demo/fail":
				var in struct {
					For    string `json:"for"`
					Prefix string `json:"prefix"`
					Status int    `json:"status"`
				}
				if !readControl(w, r, &in) {
					return
				}
				d, ok := controlDuration(w, in.For)
				if !ok {
					return
				}
				if !strings.HasPrefix(in.Prefix, "/v1/") || in.Status < 400 || in.Status > 599 {
					writeDemoError(w, http.StatusBadRequest, "invalid_body", "invalid failure")
					return
				}
				c.mu.Lock()
				c.failPrefix = in.Prefix
				c.failStatus = in.Status
				c.failUntil = c.realNow().Add(d)
				c.mu.Unlock()
			default:
				writeDemoError(w, http.StatusNotFound, "not_found", "no such demo control")
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"now": envelope.FormatTime(c.now())})
			return
		}
		if strings.HasPrefix(r.URL.Path, "/v1/") {
			c.mu.Lock()
			d := c.stallUntil.Sub(c.realNow())
			release := c.stallRelease
			if d > 0 {
				c.stalled++
			}
			c.mu.Unlock()
			if d > 0 {
				c.waitForRelease(r.Context(), d, release)
				c.mu.Lock()
				c.stalled--
				c.mu.Unlock()
				if r.Context().Err() != nil {
					return
				}
			}
			c.mu.Lock()
			failUntil, prefix, status := c.failUntil, c.failPrefix, c.failStatus
			c.mu.Unlock()
			if c.realNow().Before(failUntil) && strings.HasPrefix(r.URL.Path, prefix) {
				writeDemoError(w, status, "demo_failure", "Demo failure")
				return
			}
		}
		// Count only the two transports participating in this demo restart.
		// This wrapper keeps counting out of the production Drain interface.
		if r.Method == http.MethodGet && (r.URL.Path == "/v1/events" || r.URL.Path == "/v1/admin/activity") {
			c.streams.Add(1)
			defer c.streams.Add(-1)
		}
		next.ServeHTTP(w, r)
	})
}

// writeDemoError uses the same error/code/message/details envelope as httpapi.
// The production writer is package-private; these controls only exist here.
func writeDemoError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{
		"code": code, "message": message, "details": map[string]any{},
	}})
}

// readControl accepts exactly one JSON object, with only this endpoint's fields.
// Finish validation before callers can change any control state.
func readControl(w http.ResponseWriter, r *http.Request, dst any) bool {
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 4096))
	raw = bytes.TrimSpace(raw)
	if err != nil || len(raw) == 0 || raw[0] != '{' {
		writeDemoError(w, http.StatusBadRequest, "invalid_body", "invalid control body")
		return false
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		writeDemoError(w, http.StatusBadRequest, "invalid_body", "invalid control body")
		return false
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		writeDemoError(w, http.StatusBadRequest, "invalid_body", "invalid control body")
		return false
	}
	return true
}

func controlDuration(w http.ResponseWriter, text string) (time.Duration, bool) {
	d, err := time.ParseDuration(text)
	if err != nil || d <= 0 || d > 7*24*time.Hour {
		writeDemoError(w, http.StatusBadRequest, "invalid_body", "invalid duration")
		return 0, false
	}
	return d, true
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "demo:", err)
		os.Exit(1)
	}
}

// prefFlags collects --pref-flag KEY=VALUE@ORIGIN: an int or duration
// (seconds) preference with flag provenance, as cmd/vecnet-hub records an
// explicit flag or environment value. Screens use it to show a real
// read-only flag row; the hub validates it at start-up like any other.
type prefFlags map[string]prefs.Effective

func (p prefFlags) String() string { return "" }
func (p prefFlags) Set(s string) error {
	kv, origin, ok := strings.Cut(s, "@")
	key, raw, ok2 := strings.Cut(kv, "=")
	if !ok || !ok2 || origin == "" {
		return errors.New("want KEY=VALUE@ORIGIN")
	}
	d, err := prefs.Lookup(key)
	if err != nil {
		return err
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return err
	}
	switch d.Type {
	case prefs.TypeInt:
		p[key] = prefs.Effective{Value: prefs.Int(n), Source: prefs.SourceFlag, Origin: origin}
	case prefs.TypeDuration:
		p[key] = prefs.Effective{Value: prefs.Duration(n), Source: prefs.SourceFlag, Origin: origin}
	default:
		return errors.New("only int and duration preferences")
	}
	return nil
}

func run(ctx context.Context, args []string, stdin io.Reader, out io.Writer) error {
	fs := flag.NewFlagSet("demo", flag.ContinueOnError)
	listen := fs.String("listen", "127.0.0.1:0", "loopback address")
	var publicURL string
	fs.Func("public-url", "canonical HTTP 127.0.0.1 origin advertised by this demo", func(value string) error {
		portText, ok := strings.CutPrefix(value, "http://127.0.0.1:")
		publicPort, err := strconv.Atoi(portText)
		if !ok || err != nil || publicPort < 1 || publicPort > 65535 || strconv.Itoa(publicPort) != portText {
			return errors.New("demo public URL must be a canonical HTTP 127.0.0.1 origin with a nonzero port")
		}
		publicURL = value
		return nil
	})
	dir := fs.String("data-dir", "", "demo data directory (default: temporary)")
	ui := fs.String("dev-ui-dir", "", "UI asset directory")
	scale := fs.Bool("scale", false, "include large threads")
	reset := fs.Bool("reset", false, "reset this demo's saved data")
	exitOnEOF := fs.Bool("exit-on-stdin-eof", false, "shut down when stdin closes")
	prefFlag := prefFlags{}
	fs.Var(prefFlag, "pref-flag", "start-up preference with flag provenance, KEY=VALUE@ORIGIN (repeatable)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	ctx, cancelRun := context.WithCancel(ctx)
	defer cancelRun()
	if *exitOnEOF {
		go func() {
			_, _ = io.Copy(io.Discard, stdin)
			cancelRun()
		}()
	}
	host, port, err := net.SplitHostPort(*listen)
	if err != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() {
		return errors.New("demo listen must be a loopback IP address")
	}
	if port == "45752" {
		return errors.New("demo must not use the dev hub port")
	}
	if *dir == "" {
		tmp, err := os.MkdirTemp("", "vecnet-demo-")
		if err != nil {
			return err
		}
		*dir = tmp
		defer os.RemoveAll(tmp)
	}
	if err := os.MkdirAll(*dir, 0700); err != nil {
		return err
	}
	saved := filepath.Join(*dir, "demo.json")
	raw, readErr := os.ReadFile(saved)
	if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
		return readErr
	}
	var m manifest
	if readErr == nil {
		if err := json.Unmarshal(raw, &m); err != nil {
			return fmt.Errorf("invalid demo manifest: %w", err)
		}
	}
	if *reset && readErr == nil {
		for _, name := range []string{"hub.db", "hub.db-wal", "hub.db-shm", "blobs", "demo.json"} {
			if err := os.RemoveAll(filepath.Join(*dir, name)); err != nil {
				return err
			}
		}
		m = manifest{}
	}
	if m.OperatorSecret == "" {
		if _, err := os.Stat(filepath.Join(*dir, "hub.db")); err == nil {
			return errors.New("refusing to seed an existing database without a demo manifest")
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		return err
	}
	defer ln.Close()
	url := "http://" + ln.Addr().String()
	db, err := store.Open(filepath.Join(*dir, "hub.db"))
	if err != nil {
		return err
	}
	defer db.Close()
	c := &controls{drain: httpapi.NewDrain()}
	c.offset.Store(int64(7 * time.Minute))
	cfg := hub.DefaultConfig()
	cfg.DataDir = *dir
	cfg.PublicURL = url
	if publicURL != "" {
		cfg.PublicURL = publicURL
	}
	cfg.AllowPrivateEgress = true
	if len(prefFlag) > 0 {
		cfg.PreferenceBootstrap = map[string]prefs.Effective(prefFlag)
	}
	h, err := hub.New(db, cfg, c.now)
	if err != nil {
		return err
	}
	previousURL := m.URL
	if m.OperatorSecret == "" {
		op, sec, _, err := h.EnsureOperator(ctx, "max")
		if err != nil {
			return err
		}
		caller, err := h.Auth.Resolve(ctx, sec)
		if err != nil {
			return err
		}
		s, err := opseed.Seed(ctx, h, caller, opseed.Options{Scale: *scale})
		if err != nil {
			return err
		}
		m = manifest{URL: url, OperatorSecret: sec, OperatorPeer: op, Peers: map[string]peer{}, Threads: s.Threads, Approvals: s.Approvals, DeadLetters: s.DeadLetters}
		for name, id := range s.Peers {
			m.Peers[name] = peer{ID: id, Secret: s.Secrets[name]}
		}
		m.Peers["operator"] = peer{ID: op, Secret: sec}
	} else {
		m.URL = url
	}
	// Keep only the known fixture receivers on the listener, never the public proxy.
	triage, dot := m.Peers["triage"].ID, m.Peers["dot"].ID
	callbackOrigins := []string{url, cfg.PublicURL, previousURL}
	// Old manifests record U, not P. Seeded OAuth resources retain that original P.
	rows, err := db.QueryContext(ctx, `SELECT DISTINCT t.resource FROM oauth_tokens t
        JOIN event_subscriptions s ON s.token_hash=t.token_hash
        WHERE t.client_id='demo' AND s.principal_kind='oauth'
        AND s.peer_id=? AND s.name='vecnet.message'`, dot)
	if err != nil {
		return err
	}
	for rows.Next() {
		var resource string
		if err := rows.Scan(&resource); err != nil {
			rows.Close()
			return err
		}
		origin, hasMCP := strings.CutSuffix(resource, "/mcp")
		portText, local := strings.CutPrefix(origin, "http://127.0.0.1:")
		publicPort, parseErr := strconv.Atoi(portText)
		if hasMCP && local && parseErr == nil && publicPort > 0 && publicPort <= 65535 && strconv.Itoa(publicPort) == portText {
			callbackOrigins = append(callbackOrigins, origin)
		}
	}
	readErr = rows.Err()
	closeErr := rows.Close()
	if readErr != nil {
		return readErr
	}
	if closeErr != nil {
		return closeErr
	}
	if err := db.Tx(ctx, func(tx *sql.Tx) error {
		for _, origin := range callbackOrigins {
			if origin == "" {
				continue
			}
			if _, err := tx.ExecContext(ctx, `UPDATE routes SET url=?
                WHERE owner_kind='peer' AND owner_id=? AND kind='http' AND verified_at IS NOT NULL AND url=?`,
				url+"/__demo/sink/push", triage, origin+"/__demo/sink/push"); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `UPDATE event_subscriptions SET url=? || sub_id
                WHERE principal_kind='oauth' AND peer_id=? AND name='vecnet.message'
                AND token_hash IN (SELECT token_hash FROM oauth_tokens WHERE client_id='demo')
                AND url=? || sub_id`, url+"/__demo/sink/", dot, origin+"/__demo/sink/"); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return err
	}
	raw, err = json.Marshal(m)
	if err != nil {
		return err
	}
	if err = os.WriteFile(saved+".tmp", append(raw, '\n'), 0600); err != nil {
		return err
	}
	if err = os.Rename(saved+".tmp", saved); err != nil {
		return err
	}
	stack := httpapi.NewStack(h)
	stop := make(chan struct{})
	var once sync.Once
	closeStop := func() { once.Do(func() { close(stop) }) }
	defer closeStop()
	deps := stack.Deps()
	deps.UIDir = *ui
	deps.Stop = stop
	deps.Drain = c.drain
	srv := &http.Server{Handler: c.handler(httpapi.New(deps)), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 120 * time.Second}
	srv.RegisterOnShutdown(closeStop)
	bgCtx, cancel := context.WithCancel(ctx)
	var wg sync.WaitGroup
	defer func() { cancel(); wg.Wait() }()
	wg.Go(func() { stack.Run(bgCtx) })
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ln) }()
	if _, err = fmt.Fprintln(out, string(raw)); err != nil {
		_ = srv.Close()
		return err
	}
	select {
	case err := <-done:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	case <-ctx.Done():
	}
	closeStop()
	shutdown, cancelShutdown := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelShutdown()
	if err := srv.Shutdown(shutdown); err != nil {
		_ = srv.Close()
		return err
	}
	return nil
}
