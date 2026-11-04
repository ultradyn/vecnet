package httpapi_test

// Public-deploy hardening (PROTOCOL §20): the per-client limiter, HSTS and
// server timeouts that long-lived responses must survive.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/httpapi"
	"github.com/XertroV/vecnet/internal/hub"
	"github.com/XertroV/vecnet/internal/testhub"
)

// limited configures env with a limiter of burst tokens that never refill
// and returns it.
func limited(env *testhub.Env, burst int, trustFly bool) *httpapi.Limiter {
	l := httpapi.NewLimiter(burst, time.Hour)
	l.Now = env.Now.Now
	env.Configure(func(d *httpapi.Deps) { d.Limiter, d.TrustFlyClientIP = l, trustFly })
	return l
}

func TestIdentityChallengeCostsToken(t *testing.T) {
	env := testhub.Start(t)
	lim := httpapi.DefaultLimiter()
	lim.Now = env.Now.Now
	env.Configure(func(d *httpapi.Deps) { d.Limiter = lim })
	for range 100 {
		identityDoc(t, env, "")
	}
	for range 30 {
		identityDoc(t, env, "?challenge="+identityChallenge())
	}
	resp, body := env.Do("", http.MethodGet, "/v1/hub/identity?challenge="+identityChallenge(), nil)
	want429(t, resp, "31st challenge")
	var got struct {
		Error struct {
			Code    string         `json:"code"`
			Details map[string]any `json:"details"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if got.Error.Code != "client_rate_limited" || got.Error.Details["retry_after_seconds"] != float64(2) {
		t.Fatalf("limited challenge: %s", body)
	}
	for range 100 {
		identityDoc(t, env, "")
	}
}

func rawPost(t *testing.T, env *testhub.Env, path, body string, hdr map[string]string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, env.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp
}

func want429(t *testing.T, resp *http.Response, what string) {
	t.Helper()
	if resp.StatusCode != http.StatusTooManyRequests || resp.Header.Get("Retry-After") == "" {
		t.Fatalf("%s: %d Retry-After=%q, want 429 with Retry-After", what, resp.StatusCode, resp.Header.Get("Retry-After"))
	}
}

// Anonymous enrolment costs a token per request; with none left it is 429
// with Retry-After, while a good credential from the same address is never
// limited.
func TestLimiterEnrolAndGoodCredential(t *testing.T) {
	env := testhub.Start(t)
	limited(env, 3, false)
	for i := range 3 {
		if resp := rawPost(t, env, "/v1/enrol", `{"token":"vn_enrol_nope"}`, nil); resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("enrol %d: %d", i, resp.StatusCode)
		}
	}
	want429(t, rawPost(t, env, "/v1/enrol", `{"token":"vn_enrol_nope"}`, nil), "4th enrol")
	resp, body := env.Do(env.OperatorSecret, http.MethodGet, "/v1/peers/me", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("good bearer from a limited address: %d %s", resp.StatusCode, body)
	}
}

// A junk bearer costs a token per failure, then gets 429 instead of 401; a
// good bearer still works.
func TestLimiterFailedBearer(t *testing.T) {
	env := testhub.Start(t)
	limited(env, 2, false)
	for i := range 2 {
		if resp, _ := env.Do("vn_peer_junk", http.MethodGet, "/v1/peers", nil); resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("junk %d: %d", i, resp.StatusCode)
		}
	}
	resp, _ := env.Do("vn_peer_junk", http.MethodGet, "/v1/peers", nil)
	want429(t, resp, "3rd junk bearer")
	if resp, _ := env.Do(env.OperatorSecret, http.MethodGet, "/v1/peers", nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("good bearer: %d", resp.StatusCode)
	}
}

// Ingress costs a token per failure only, and is refused once they are gone.
func TestLimiterIngressFailures(t *testing.T) {
	env := testhub.Start(t)
	limited(env, 2, false)
	for i := range 2 {
		if resp := rawPost(t, env, "/v1/ingress/src_nope", `{}`, nil); resp.StatusCode != http.StatusNotFound {
			t.Fatalf("unknown source %d: %d", i, resp.StatusCode)
		}
	}
	want429(t, rawPost(t, env, "/v1/ingress/src_nope", `{}`, nil), "3rd unknown source")
}

// /mcp without a credential costs a token per 401 and is refused once they
// are gone; a good bearer still gets through.
func TestLimiterMCP(t *testing.T) {
	env := testhub.Start(t)
	limited(env, 1, false)
	init := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"t","version":"1"}}}`
	hdr := map[string]string{"Accept": "application/json, text/event-stream"}
	if resp := rawPost(t, env, "/mcp", init, hdr); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("anonymous /mcp: %d", resp.StatusCode)
	}
	want429(t, rawPost(t, env, "/mcp", init, hdr), "2nd anonymous /mcp")
	hdr["Authorization"] = "Bearer " + env.OperatorSecret
	if resp := rawPost(t, env, "/mcp", init, hdr); resp.StatusCode != http.StatusOK {
		t.Fatalf("operator /mcp from a limited address: %d", resp.StatusCode)
	}
}

// Fly-Client-IP names the client only when the hub is told it is behind Fly;
// otherwise a direct client could dodge its limit by inventing addresses.
func TestLimiterFlyClientIP(t *testing.T) {
	for _, trust := range []bool{false, true} {
		t.Run(fmt.Sprint("trust=", trust), func(t *testing.T) {
			env := testhub.Start(t)
			limited(env, 1, trust)
			junk := `{"token":"vn_enrol_nope"}`
			if resp := rawPost(t, env, "/v1/enrol", junk, map[string]string{"Fly-Client-IP": "203.0.113.7"}); resp.StatusCode != http.StatusUnauthorized {
				t.Fatalf("first: %d", resp.StatusCode)
			}
			resp := rawPost(t, env, "/v1/enrol", junk, map[string]string{"Fly-Client-IP": "203.0.113.8"})
			if trust && resp.StatusCode != http.StatusUnauthorized {
				t.Fatalf("trusted header, new address: %d, want its own bucket (401)", resp.StatusCode)
			}
			if !trust {
				want429(t, resp, "untrusted header, new address")
			}
			if trust {
				// Two addresses in one IPv6 /64 are one client.
				rawPost(t, env, "/v1/enrol", junk, map[string]string{"Fly-Client-IP": "2001:db8:1:2::1"})
				want429(t, rawPost(t, env, "/v1/enrol", junk, map[string]string{"Fly-Client-IP": "2001:db8:1:2::ffff"}), "same /64")
			}
		})
	}
}

// The limiter refills, is safe for concurrent use (run with -race), and never
// grants more than its burst at once.
func TestLimiterConcurrent(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	var mu sync.Mutex
	l := httpapi.NewLimiter(50, time.Second)
	l.Now = func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	var wg sync.WaitGroup
	var count sync.Map
	for g := range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			key := fmt.Sprint("client-", g%4)
			for range 100 {
				if ok, _ := l.Take(key); ok {
					v, _ := count.LoadOrStore(key, new(int64))
					mu.Lock()
					*v.(*int64)++
					mu.Unlock()
				}
				l.Spend("other")
				l.Check(key)
			}
		}()
	}
	wg.Wait()
	for i := range 4 {
		v, _ := count.Load(fmt.Sprint("client-", i))
		if n := *v.(*int64); n != 50 {
			t.Fatalf("client-%d granted %d, want exactly the burst (50)", i, n)
		}
	}
	if ok, wait := l.Take("client-0"); ok || wait != time.Second {
		t.Fatalf("empty bucket: ok=%v wait=%s, want false 1s", ok, wait)
	}
	mu.Lock()
	now = now.Add(3 * time.Second)
	mu.Unlock()
	for i := range 3 {
		if ok, _ := l.Take("client-0"); !ok {
			t.Fatalf("refilled token %d refused", i)
		}
	}
	if ok, _ := l.Take("client-0"); ok {
		t.Fatal("refill granted more than elapsed time allows")
	}
}

// Over https the hub sends HSTS on every response; over plain http it does
// not (a browser ignores it there, and dev hubs run on http).
func TestHSTS(t *testing.T) {
	env := testhub.StartWith(t, func(c *hub.Config) { c.PublicURL = "https://hub.example" })
	for _, p := range []string{"/healthz", "/ui/", "/v1/peers", "/.well-known/oauth-authorization-server"} {
		resp, err := http.Get(env.URL + p)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if got := resp.Header.Get("Strict-Transport-Security"); got != "max-age=31536000" {
			t.Fatalf("%s: Strict-Transport-Security = %q", p, got)
		}
	}
	plain := testhub.Start(t)
	resp, err := http.Get(plain.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if got := resp.Header.Get("Strict-Transport-Security"); got != "" {
		t.Fatalf("http hub sent HSTS %q", got)
	}
}

// shortTimeouts is the production ConfigureServer with every timeout cut to
// 300 ms, so a test sees a stream outlive them.
func shortTimeouts(srv *http.Server) {
	httpapi.ConfigureServer(srv)
	srv.ReadHeaderTimeout, srv.ReadTimeout, srv.WriteTimeout = 300*time.Millisecond, 300*time.Millisecond, 300*time.Millisecond
}

// readFrames reads an SSE body for d and returns the frames' first lines.
func readFrames(t *testing.T, resp *http.Response, d time.Duration) []string {
	t.Helper()
	lines := make(chan string, 256)
	go func() {
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			if l := sc.Text(); l != "" {
				lines <- l
			}
		}
		close(lines)
	}()
	var got []string
	deadline := time.After(d)
	for {
		select {
		case l, ok := <-lines:
			if !ok {
				return got
			}
			got = append(got, l)
		case <-deadline:
			return got
		}
	}
}

func openStream(t *testing.T, env *testhub.Env, secret, path string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, env.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+secret)
	req.Header.Set("Accept", "text/event-stream")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("%s: %d", path, resp.StatusCode)
	}
	return resp
}

// SSE streams (/v1/events, /v1/admin/activity) keep flowing after the
// server's ReadTimeout and WriteTimeout have passed many times over.
func TestStreamsOutliveServerTimeouts(t *testing.T) {
	env := testhub.StartWithServer(t, nil, shortTimeouts)
	env.Configure(func(d *httpapi.Deps) { d.Heartbeat = 50 * time.Millisecond })
	_, secret := env.NewPeer("session", "streamer")

	for _, tc := range []struct{ secret, path string }{
		{secret, "/v1/events"},
		{env.OperatorSecret, "/v1/admin/activity"},
	} {
		resp := openStream(t, env, tc.secret, tc.path)
		start := time.Now()
		frames := readFrames(t, resp, 1500*time.Millisecond)
		if el := time.Since(start); el < 1400*time.Millisecond || len(frames) < 10 {
			t.Fatalf("%s: %d frames in %s with 300 ms server timeouts; the stream was cut", tc.path, len(frames), el)
		}
	}
}

// A long poll holds past the server's ReadTimeout and WriteTimeout and still
// answers 200.
func TestLongPollOutlivesServerTimeouts(t *testing.T) {
	env := testhub.StartWithServer(t, nil, shortTimeouts)
	env.Configure(func(d *httpapi.Deps) { d.WaitUnit = time.Second })
	_, secret := env.NewPeer("session", "poller")
	// Drain what the new peer already has, then hold on an empty log.
	resp, body := env.Do(secret, http.MethodGet, "/v1/events?wait=0", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("drain: %d %s", resp.StatusCode, body)
	}
	var first struct {
		NextCursor string `json:"next_cursor"`
	}
	_ = json.Unmarshal(body, &first)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, env.URL+"/v1/events?wait=1&after="+first.NextCursor, nil)
	req.Header.Set("Authorization", "Bearer "+secret)
	start := time.Now()
	r, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("long poll cut after %s: %v", time.Since(start), err)
	}
	var buf bytes.Buffer
	_, err = buf.ReadFrom(r.Body)
	r.Body.Close()
	if err != nil || r.StatusCode != http.StatusOK {
		t.Fatalf("long poll: %d %v %s", r.StatusCode, err, buf.String())
	}
	if el := time.Since(start); el < 900*time.Millisecond {
		t.Fatalf("long poll returned after %s, want the full 1 s hold", el)
	}
}

// An ordinary request body that stalls past ReadTimeout is cut.
func TestSlowBodyIsCut(t *testing.T) {
	env := testhub.StartWithServer(t, nil, shortTimeouts)
	pr, pw := io.Pipe()
	go func() {
		pw.Write([]byte(`{"token":`))
		time.Sleep(time.Second)
		pw.Write([]byte(`"vn_enrol_x"}`))
		pw.Close()
	}()
	req, _ := http.NewRequest(http.MethodPost, env.URL+"/v1/enrol", pr)
	req.Header.Set("Content-Type", "application/json")
	start := time.Now()
	resp, err := http.DefaultClient.Do(req)
	if err == nil {
		resp.Body.Close()
		if resp.StatusCode == http.StatusUnauthorized && time.Since(start) >= 900*time.Millisecond {
			t.Fatalf("a body stalled for 1 s was read to the end despite a 300 ms ReadTimeout")
		}
	}
}
