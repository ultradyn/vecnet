package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"html"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/cli"
	"github.com/XertroV/vecnet/internal/events"
	"github.com/XertroV/vecnet/internal/mcpserver"
	"github.com/XertroV/vecnet/internal/testhub"
	"github.com/XertroV/vecnet/internal/webhook"
)

// lockedBuffer is a bytes.Buffer safe for one writer goroutine and a reader.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// noEnv is a getenv that sees an empty environment.
func noEnv(string) string { return "" }

// cliUser runs the vecnet CLI against one config file, never the real home.
type cliUser struct {
	t   *testing.T
	env map[string]string
}

func newCLIUser(t *testing.T) *cliUser {
	dir := t.TempDir()
	return &cliUser{t: t, env: map[string]string{
		"VECNET_CONFIG":  filepath.Join(dir, "config.toml"),
		"XDG_CACHE_HOME": filepath.Join(dir, "cache"),
		"HOME":           filepath.Join(dir, "home"),
	}}
}

func (u *cliUser) mustRun(args ...string) string {
	u.t.Helper()
	var out, errb bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	code := cli.RunContext(ctx, args, strings.NewReader(""), &out, &errb, func(k string) string { return u.env[k] })
	if code != 0 {
		u.t.Fatalf("vecnet %s: exit %d\nstdout: %s\nstderr: %s", strings.Join(args, " "), code, out.String(), errb.String())
	}
	return out.String()
}

var listenLine = regexp.MustCompile(`listening on (http://127\.0\.0\.[0-9]+:([0-9]+))`)

// bootedHub is a hub run in a goroutine by bootHub.
type bootedHub struct {
	base, port string
	out        *lockedBuffer
	stop       func() error // cancel and wait (≤ 2 s); later calls return the first result
}

// bootHub runs run(ctx, args, getenv) in a goroutine and waits for its
// listening line. A t.Cleanup, registered at once (so it runs before the
// caller's earlier t.TempDir removal), stops the hub and waits for it, so no
// failure path leaks a serving hub that holds the data dir.
func bootHub(t *testing.T, args []string, getenv func(string) string) *bootedHub {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	out := &lockedBuffer{}
	done := make(chan error, 1)
	go func() { done <- run(ctx, args, out, getenv) }()
	var once sync.Once
	var stopErr error
	stop := func() error {
		once.Do(func() {
			cancel()
			select {
			case stopErr = <-done:
			case <-time.After(2 * time.Second):
				stopErr = errors.New("run did not return within 2 s of ctx cancel")
			}
		})
		return stopErr
	}
	t.Cleanup(func() {
		if err := stop(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			t.Errorf("hub stop: %v", err)
		}
	})
	deadline := time.Now().Add(5 * time.Second)
	for {
		if m := listenLine.FindStringSubmatch(out.String()); m != nil {
			return &bootedHub{base: m[1], port: m[2], out: out, stop: stop}
		}
		select {
		case err := <-done:
			done <- err // let stop see it
			t.Fatalf("run returned before listening: %v\n%s", err, out.String())
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("no listening line within 5 s; output:\n%s", out.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestSmokeBootSendReceive(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "data")

	// Init the operator through run, the way `fly-init` does.
	var initOut bytes.Buffer
	if err := run(context.Background(), []string{"init", "--data-dir", dataDir, "--operator-name", "Max"}, &initOut, noEnv); err != nil {
		t.Fatalf("run init: %v\n%s", err, initOut.String())
	}
	opSecret := regexp.MustCompile(`vn_operator_\S+`).FindString(initOut.String())
	if opSecret == "" {
		t.Fatalf("init output lacks the operator secret:\n%s", initOut.String())
	}

	// Boot the real server on a random loopback port.
	hb := bootHub(t, []string{"--listen", "127.0.0.1:0", "--data-dir", dataDir}, noEnv)
	base := hb.base

	// /healthz answers 200 without credentials.
	resp, err := http.Get(base + "/healthz")
	if err != nil {
		t.Fatalf("GET /healthz: %v", err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /healthz = %d, want 200", resp.StatusCode)
	}

	// The operator adds two peers via the CLI package; each enrols.
	op := newCLIUser(t)
	conf := "default = \"op\"\n\n[profiles.op]\nhub = \"" + base + "\"\nsecret = \"" + opSecret + "\"\n"
	if err := os.WriteFile(op.env["VECNET_CONFIG"], []byte(conf), 0o600); err != nil {
		t.Fatal(err)
	}
	addAndEnrol := func(name string) (string, *cliUser) {
		var added struct {
			PeerID     string `json:"peer_id"`
			EnrolToken string `json:"enrol_token"`
		}
		out := op.mustRun("--json", "peer", "add", "--kind", "session", "--name", name)
		if err := json.Unmarshal([]byte(out), &added); err != nil {
			t.Fatalf("peer add --json output %q: %v", out, err)
		}
		if !strings.HasPrefix(added.PeerID, "peer_") || !strings.HasPrefix(added.EnrolToken, "vn_enrol_") {
			t.Fatalf("peer add --json = %q; want peer_id and enrol_token", out)
		}
		u := newCLIUser(t)
		u.mustRun("enrol", "--hub", base, "--token", added.EnrolToken, "--profile", "smoke")
		return added.PeerID, u
	}
	aID, a := addAndEnrol("A")
	bID, b := addAndEnrol("B")

	// A sends to B with --hold; B's inbox shows it.
	out := a.mustRun("--json", "send", bID, "hello from the smoke test", "--hold")
	var sent struct {
		MessageID string `json:"message_id"`
		ThreadID  string `json:"thread_id"`
	}
	if err := json.Unmarshal([]byte(out), &sent); err != nil || !strings.HasPrefix(sent.MessageID, "msg_") {
		t.Fatalf("send --json output %q: %v", out, err)
	}
	out = b.mustRun("--json", "inbox")
	var inbox struct {
		Events []struct {
			Type string `json:"type"`
			Data struct {
				Recipient string `json:"recipient"`
				Message   struct {
					MessageID string                `json:"message_id"`
					Sender    string                `json:"sender"`
					Content   struct{ Text string } `json:"content"`
				} `json:"message"`
			} `json:"data"`
		} `json:"events"`
	}
	if err := json.Unmarshal([]byte(out), &inbox); err != nil {
		t.Fatalf("inbox --json output %q: %v", out, err)
	}
	found := false
	for _, ev := range inbox.Events {
		m := ev.Data.Message
		if m.MessageID == sent.MessageID && m.Sender == aID && ev.Data.Recipient == bID && m.Content.Text == "hello from the smoke test" {
			found = true
		}
	}
	if !found {
		t.Fatalf("B's inbox lacks message %s from %s:\n%s", sent.MessageID, aID, out)
	}

	// An open SSE stream must not hold shutdown: the hub ends it at once.
	streamCtx, streamCancel := context.WithCancel(context.Background())
	defer streamCancel()
	bSecret := regexp.MustCompile(`vn_peer_\S+?"`).FindString(readFile(t, b.env["VECNET_CONFIG"]))
	req, err := http.NewRequestWithContext(streamCtx, "GET", base+"/v1/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Authorization", "Bearer "+strings.TrimSuffix(bSecret, `"`))
	stream, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("open /v1/events: %v", err)
	}
	defer stream.Body.Close()
	if stream.StatusCode != http.StatusOK {
		t.Fatalf("GET /v1/events = %d, want 200", stream.StatusCode)
	}
	if _, err := bufio.NewReader(stream.Body).ReadString('\n'); err != nil {
		t.Fatalf("read the stream preamble: %v", err)
	}

	// Cancelling ctx shuts the hub down within 2 s, cleanly, stream and all.
	if err := hb.stop(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		t.Fatalf("run after cancel = %v, want nil", err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestFlagDefaultsFromEnv(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "envdata")
	listen, wantHost := "127.0.0.1:0", ""
	if ln, err := net.Listen("tcp", "127.0.0.2:0"); err == nil {
		ln.Close()
		listen, wantHost = "127.0.0.2:0", "127.0.0.2"
	}
	env := map[string]string{
		"VECNET_LISTEN":     listen,
		"VECNET_DATA_DIR":   dataDir,
		"VECNET_PUBLIC_URL": "https://vecnet.example",
	}
	getenv := func(k string) string { return env[k] }

	// init with no --data-dir uses VECNET_DATA_DIR.
	var initOut bytes.Buffer
	if err := run(context.Background(), []string{"init", "--operator-name", "Max"}, &initOut, getenv); err != nil {
		t.Fatalf("run init: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dataDir, "hub.db")); err != nil {
		t.Fatalf("init did not create hub.db under VECNET_DATA_DIR: %v", err)
	}

	// VECNET_LISTEN is honoured: on a distinctive loopback address when the
	// host has one, else at least not the default dev port.
	hb := bootHub(t, nil, getenv)
	if wantHost != "" && !strings.Contains(hb.base, "//"+wantHost+":") {
		t.Fatalf("hub listens on %s, want VECNET_LISTEN's host %s", hb.base, wantHost)
	}
	if hb.port == "45752" {
		t.Fatalf("hub listens on the default port %s, want VECNET_LISTEN's :0", hb.port)
	}
	if !strings.Contains(hb.out.String(), "public url https://vecnet.example") {
		t.Fatalf("output %q does not report VECNET_PUBLIC_URL", hb.out.String())
	}
	if err := hb.stop(); err != nil {
		t.Fatalf("run after cancel = %v", err)
	}

	// A flag beats its environment default.
	hb = bootHub(t, []string{"--listen", "127.0.0.1:0", "--public-url", "https://flag.example"}, getenv)
	if !strings.Contains(hb.out.String(), "public url https://flag.example") {
		t.Fatalf("output %q: --public-url did not override VECNET_PUBLIC_URL", hb.out.String())
	}
	if err := hb.stop(); err != nil {
		t.Fatalf("run after cancel = %v", err)
	}
}

// initHub runs `vecnet-hub init` in a new data dir and returns it and the
// operator secret.
func initHub(t *testing.T) (dataDir, opSecret string) {
	t.Helper()
	dataDir = filepath.Join(t.TempDir(), "data")
	var out bytes.Buffer
	if err := run(context.Background(), []string{"init", "--data-dir", dataDir, "--operator-name", "Max"}, &out, noEnv); err != nil {
		t.Fatalf("run init: %v\n%s", err, out.String())
	}
	opSecret = regexp.MustCompile(`vn_operator_\S+`).FindString(out.String())
	if opSecret == "" {
		t.Fatalf("init output lacks the operator secret:\n%s", out.String())
	}
	return dataDir, opSecret
}

// hubDo sends one JSON request and returns the response and its body.
func hubDo(t *testing.T, method, url, secret string, body any) (*http.Response, []byte) {
	t.Helper()
	var r io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		r = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, url, r)
	if err != nil {
		t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if secret != "" {
		req.Header.Set("Authorization", "Bearer "+secret)
	}
	resp, err := noRedirects.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer resp.Body.Close()
	out, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp, out
}

// noRedirects answers a 302 instead of following it: the consent step hands
// its code to chatgpt.com, which a test must not visit.
var noRedirects = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

// TestEgressGuardDefault pins how the binary arms the guard: on unless the
// flag or its environment variable says otherwise (PROTOCOL §14.7).
func TestEgressGuardDefault(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		env  map[string]string
		want int
	}{
		{"default", nil, nil, http.StatusUnprocessableEntity},
		{"env other than 1", nil, map[string]string{"VECNET_ALLOW_PRIVATE_EGRESS": "true"}, http.StatusUnprocessableEntity},
		{"env", nil, map[string]string{"VECNET_ALLOW_PRIVATE_EGRESS": "1"}, http.StatusCreated},
		{"flag", []string{"--allow-private-egress"}, nil, http.StatusCreated},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dataDir, opSecret := initHub(t)
			args := append([]string{"--listen", "127.0.0.1:0", "--data-dir", dataDir}, tc.args...)
			hb := bootHub(t, args, func(k string) string { return tc.env[k] })
			resp, body := hubDo(t, http.MethodPost, hb.base+"/v1/admin/peers", opSecret, map[string]string{"kind": "session", "name": "p"})
			var added struct {
				PeerID string `json:"peer_id"`
			}
			if resp.StatusCode != http.StatusCreated || json.Unmarshal(body, &added) != nil {
				t.Fatalf("peer add: %d %s", resp.StatusCode, body)
			}
			resp, body = hubDo(t, http.MethodPost, hb.base+"/v1/admin/routes", opSecret,
				map[string]string{"owner": added.PeerID, "url": "http://127.0.0.1:9/hook"})
			if resp.StatusCode != tc.want {
				t.Fatalf("loopback route = %d %s, want %d", resp.StatusCode, body, tc.want)
			}
			if tc.want != http.StatusCreated && !strings.Contains(string(body), `"invalid_url"`) {
				t.Fatalf("refusal = %s, want invalid_url", body)
			}
		})
	}
}

// TestSmokeDotSubscription drives the binary's own wiring from the front door:
// OAuth is mounted, the Events worker runs and reads the binding, so a live
// subscription pushes and stops when the operator moves its peer away.
func TestSmokeDotSubscription(t *testing.T) {
	dataDir, opSecret := initHub(t)
	hb := bootHub(t, []string{"--listen", "127.0.0.1:0", "--data-dir", dataDir, "--allow-private-egress"}, noEnv)
	base := hb.base
	admin := func(method, path string, body any) []byte {
		t.Helper()
		resp, out := hubDo(t, method, base+path, opSecret, body)
		if resp.StatusCode/100 != 2 {
			t.Fatalf("%s %s: %d %s", method, path, resp.StatusCode, out)
		}
		return out
	}
	if resp, body := hubDo(t, http.MethodGet, base+"/.well-known/oauth-authorization-server", "", nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("metadata: %d %s", resp.StatusCode, body)
	}

	var dot, sender struct {
		PeerID     string `json:"peer_id"`
		EnrolToken string `json:"enrol_token"`
	}
	if err := json.Unmarshal(admin(http.MethodPost, "/v1/admin/peers", map[string]string{"kind": "dot", "name": "mira"}), &dot); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(admin(http.MethodPost, "/v1/admin/peers", map[string]string{"kind": "session", "name": "s"}), &sender); err != nil {
		t.Fatal(err)
	}
	resp, body := hubDo(t, http.MethodPost, base+"/v1/enrol", "", map[string]string{"token": sender.EnrolToken})
	var enrolled struct {
		Secret string `json:"secret"`
	}
	if resp.StatusCode != http.StatusOK || json.Unmarshal(body, &enrolled) != nil {
		t.Fatalf("enrol: %d %s", resp.StatusCode, body)
	}
	var link struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(admin(http.MethodPost, "/v1/admin/dot-links", map[string]string{"peer_id": dot.PeerID}), &link); err != nil {
		t.Fatal(err)
	}

	access := dotLogin(t, base, link.Code)
	cb := testhub.NewCallback(t)
	subscribeSmoke(t, base, access, cb.URL)

	send := func(key string) {
		t.Helper()
		resp, body := hubDo(t, http.MethodPost, base+"/v1/messages", enrolled.Secret, map[string]any{
			"idempotency_key": key, "priority": "wake", "recipients": []string{dot.PeerID},
			"content": map[string]any{"text": key},
		})
		if resp.StatusCode != http.StatusAccepted {
			t.Fatalf("send: %d %s", resp.StatusCode, body)
		}
	}
	send("before")
	waitUntil(t, 10*time.Second, "the first push", func() bool { return len(cb.Posts()) == 1 })

	// The operator moves the peer: the subscription's binding is gone.
	var prn struct {
		PrincipalID string `json:"principal_id"`
	}
	if err := json.Unmarshal(admin(http.MethodPost, "/v1/admin/principals", map[string]string{"name": "elsewhere"}), &prn); err != nil {
		t.Fatal(err)
	}
	admin(http.MethodPost, "/v1/admin/peers/"+dot.PeerID+"/move", map[string]string{"principal_id": prn.PrincipalID})
	send("after")
	waitUntil(t, 10*time.Second, "the worker to pause the subscription", func() bool {
		return strings.Contains(string(admin(http.MethodGet, "/v1/admin/subscriptions", nil)), `"paused_reason":"unauthorized"`)
	})
	if n := len(cb.Posts()); n != 1 {
		t.Fatalf("POSTs = %d, want none after the move", n)
	}
}

func waitUntil(t *testing.T, within time.Duration, what string, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(within); !cond(); time.Sleep(20 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
	}
}

// dotLogin runs the OAuth code flow a ChatGPT dot runs, spending link, and
// returns an access token.
func dotLogin(t *testing.T, base, link string) string {
	t.Helper()
	const redirect = "https://chatgpt.com/connector_platform_oauth_redirect"
	resp, body := hubDo(t, http.MethodPost, base+"/oauth/register", "", map[string]any{
		"client_name": "ChatGPT", "redirect_uris": []string{redirect},
		"grant_types": []string{"authorization_code", "refresh_token"}, "response_types": []string{"code"},
		"token_endpoint_auth_method": "none",
	})
	var client struct {
		ClientID string `json:"client_id"`
	}
	if resp.StatusCode != http.StatusCreated || json.Unmarshal(body, &client) != nil {
		t.Fatalf("register: %d %s", resp.StatusCode, body)
	}
	verifier := strings.Repeat("v", 64)
	sum := sha256.Sum256([]byte(verifier))
	q := url.Values{
		"response_type": {"code"}, "client_id": {client.ClientID}, "redirect_uri": {redirect},
		"code_challenge": {base64.RawURLEncoding.EncodeToString(sum[:])}, "code_challenge_method": {"S256"},
		"state": {"s"}, "resource": {base + "/mcp"}, "scope": {"vecnet"},
	}
	resp, body = hubDo(t, http.MethodGet, base+"/oauth/authorize?"+q.Encode(), "", nil)
	m := regexp.MustCompile(`name="request"\s+value="([^"]*)"`).FindStringSubmatch(string(body))
	if resp.StatusCode != http.StatusOK || m == nil {
		t.Fatalf("authorize: %d %s", resp.StatusCode, body)
	}
	form := func(path string, v url.Values) (*http.Response, []byte) {
		t.Helper()
		resp, err := noRedirects.PostForm(base+path, v)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		out, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		return resp, out
	}
	resp, body = form("/oauth/authorize", url.Values{"request": {html.UnescapeString(m[1])}, "approve": {"1"}, "link_code": {link}})
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("consent: %d %s", resp.StatusCode, body)
	}
	to, err := url.Parse(resp.Header.Get("Location"))
	if err != nil || to.Query().Get("code") == "" {
		t.Fatalf("consent redirect = %q, %v", resp.Header.Get("Location"), err)
	}
	resp, body = form("/oauth/token", url.Values{
		"grant_type": {"authorization_code"}, "code": {to.Query().Get("code")}, "client_id": {client.ClientID},
		"redirect_uri": {redirect}, "code_verifier": {verifier},
	})
	var tok struct {
		AccessToken string `json:"access_token"`
	}
	if resp.StatusCode != http.StatusOK || json.Unmarshal(body, &tok) != nil || tok.AccessToken == "" {
		t.Fatalf("token: %d %s", resp.StatusCode, body)
	}
	return tok.AccessToken
}

// subscribeSmoke subscribes the token's dot to callback through /mcp.
func subscribeSmoke(t *testing.T, base, access, callback string) {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "events/subscribe", "params": map[string]any{
		"name": events.Name, "arguments": map[string]any{},
		"delivery": map[string]any{"mode": "webhook", "url": callback, "secret": webhook.NewSecret()},
	}})
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, base+"/mcp", bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+access)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("MCP-Protocol-Version", mcpserver.ProtoCurrent)
	resp, err := noRedirects.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK || bytes.Contains(body, []byte(`"error"`)) {
		t.Fatalf("events/subscribe: %d %s", resp.StatusCode, body)
	}
}
