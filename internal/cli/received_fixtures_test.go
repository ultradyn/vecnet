package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/hubid"
)

const receivedCanary = "RECEIVED_PRIVATE_CANARY"
const receivedDestination = "https://destination.example.invalid"

type receivedRequest struct {
	origin, path string
	method, auth string
	read, closed int
}

// Every request crosses a real loopback HTTP server. Only the public transport
// routing seam is replaced; production constructors, decoding and consumers run.
// The package has no t.Parallel calls. Restore the global transport at cleanup.
type receivedFixture struct {
	t         *testing.T
	env       map[string]string
	hub       string
	key       hubid.Key
	mu        sync.Mutex
	status    int
	body      []byte
	requests  []*receivedRequest
	blocked   int
	allow     map[string]bool
	serverURL *url.URL
	transport *http.Transport
	repo      string
}

func receivedJSON(t *testing.T, v any) []byte {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func newReceivedFixture(t *testing.T, hub string) *receivedFixture {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	f := &receivedFixture{t: t, hub: hub, key: testIdentityKey(t, "1"), allow: map[string]bool{hub: true, receivedDestination: true}}
	f.env = map[string]string{
		"HOME": dir, "VECNET_CONFIG": filepath.Join(dir, "config.toml"),
		"XDG_CONFIG_HOME": filepath.Join(dir, "config"), "XDG_CACHE_HOME": filepath.Join(dir, "cache"),
		"XDG_DATA_HOME": filepath.Join(dir, "data"), "XDG_STATE_HOME": filepath.Join(dir, "state"),
		"CODEX_HOME": filepath.Join(dir, "codex-home"), "VECNET_PROFILE": "p",
		"VECNET_CODEX_AUTO_DELIVERY": "0",
	}
	if err := os.Mkdir(f.env["CODEX_HOME"], 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.env["CODEX_HOME"], "config.toml"), []byte("# synthetic Codex home\n"), 0600); err != nil {
		t.Fatal(err)
	}
	f.profile(false)
	srv := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(srv.Close)
	var err error
	f.serverURL, err = url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	f.transport = &http.Transport{Proxy: nil, ResponseHeaderTimeout: 2 * time.Second}
	t.Cleanup(f.transport.CloseIdleConnections)
	previous := http.DefaultTransport
	http.DefaultTransport = receivedRoundTrip(f.roundTrip)
	t.Cleanup(func() { http.DefaultTransport = previous })
	f.reply(410, "hub_moved", "untrusted "+receivedCanary, nil)
	t.Cleanup(func() {
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.blocked != 0 {
			t.Errorf("attempted nonfixture HTTP destinations: %d", f.blocked)
		}
		for _, r := range f.requests {
			if r.closed != 1 {
				t.Errorf("body close count=%d for %s %s", r.closed, r.origin, r.path)
			}
		}
	})
	return f
}

func (f *receivedFixture) profile(allow bool) {
	f.t.Helper()
	p := Profile{Hub: f.hub, HubPubkey: f.key.Pubkey(), Secret: "vn_operator_fixture", AllowInsecureHTTP: allow}
	if err := saveConfig(f.env["VECNET_CONFIG"], Config{Profiles: map[string]Profile{"p": p}}); err != nil {
		f.t.Fatal(err)
	}
}

func (f *receivedFixture) reply(status int, code, message string, details map[string]any) {
	f.t.Helper()
	raw := receivedJSON(f.t, map[string]any{"error": map[string]any{"code": code, "message": message, "details": details}})
	f.mu.Lock()
	f.status, f.body = status, raw
	f.mu.Unlock()
}

type receivedRoundTrip func(*http.Request) (*http.Response, error)

func (fn receivedRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return fn(r) }

type receivedBody struct {
	io.ReadCloser
	f *receivedFixture
	r *receivedRequest
}

func (b *receivedBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	b.f.mu.Lock()
	b.r.read += n
	b.f.mu.Unlock()
	return n, err
}

func (b *receivedBody) Close() error {
	b.f.mu.Lock()
	b.r.closed++
	b.f.mu.Unlock()
	return b.ReadCloser.Close()
}

func (f *receivedFixture) roundTrip(r *http.Request) (*http.Response, error) {
	origin := r.URL.Scheme + "://" + r.URL.Host
	f.mu.Lock()
	allowed := f.allow[origin]
	if !allowed {
		f.blocked++
		f.mu.Unlock()
		return nil, errors.New("nonfixture destination refused")
	}
	entry := &receivedRequest{origin: origin, path: r.URL.Path, method: r.Method, auth: r.Header.Get("Authorization")}
	f.requests = append(f.requests, entry)
	f.mu.Unlock()
	forward := r.Clone(r.Context())
	u := *r.URL
	u.Scheme, u.Host = f.serverURL.Scheme, f.serverURL.Host
	forward.URL = &u
	forward.Host = r.URL.Host
	forward.Header.Set("X-Received-Fixture-Origin", origin)
	resp, err := f.transport.RoundTrip(forward)
	if err == nil {
		resp.Body = &receivedBody{ReadCloser: resp.Body, f: f, r: entry}
	}
	return resp, err
}

func (f *receivedFixture) serve(w http.ResponseWriter, r *http.Request) {
	origin := r.Header.Get("X-Received-Fixture-Origin")
	w.Header().Set("Content-Type", "application/json")
	if r.URL.Path == "/v1/hub/identity" {
		if r.Header.Get("Authorization") != "" || r.Header.Get(envelope.ClientHeader) != "" {
			f.t.Error("identity proof received a credential or client label")
		}
		sig, err := f.key.SignChallenge(origin, r.URL.Query().Get("challenge"))
		if err != nil {
			f.t.Error(err)
			w.WriteHeader(500)
			return
		}
		if err := json.NewEncoder(w).Encode(envelope.HubIdentity{Pubkey: f.key.Pubkey(), PublicURL: origin, ChallengeSig: sig}); err != nil {
			f.t.Error(err)
		}
		return
	}
	if origin != f.hub {
		fmt.Fprint(w, `{"peers":[{"peer_id":"peer_fixture","name":"fixture","kind":"session"}]}`)
		return
	}
	f.mu.Lock()
	status, raw := f.status, append([]byte(nil), f.body...)
	f.mu.Unlock()
	w.Header().Set("Location", receivedDestination+"/must-not-redirect")
	w.WriteHeader(status)
	// Oversized-body tests deliberately close after the existing 64KiB cap.
	if _, err := w.Write(raw); err != nil && len(raw) <= 64<<10 {
		f.t.Error(err)
	}
}

func (f *receivedFixture) app() *app {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	f.t.Cleanup(cancel)
	return &app{ctx: ctx, net: "p", getenv: func(k string) string { return f.env[k] }, stdin: strings.NewReader(""), stdout: new(bytes.Buffer), stderr: new(bytes.Buffer)}
}

func (f *receivedFixture) run(args ...string) (int, string, string) {
	f.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var out, errs bytes.Buffer
	code := RunContext(ctx, args, strings.NewReader(""), &out, &errs, func(k string) string { return f.env[k] })
	if ctx.Err() != nil {
		f.t.Fatalf("fixture did not complete: %v", ctx.Err())
	}
	return code, out.String(), errs.String()
}

// Compare every regular file in the private fixture HOME, including config,
// pins, bindings, backups, cache and Codex/launcher files. Only the three known
// config/pins/bindings locks are allowed bookkeeping. Never follow a link out.
func (f *receivedFixture) snapshot() map[string]string {
	f.t.Helper()
	out := map[string]string{}
	config := f.env["VECNET_CONFIG"]
	locks := map[string]bool{config + ".lock": true, filepath.Join(filepath.Dir(config), "state", "pins.json.lock"): true, filepath.Join(filepath.Dir(config), "codex", "bindings.json.lock"): true}
	err := filepath.WalkDir(f.env["HOME"], func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || locks[path] {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("nonregular fixture path: %s", path)
		}
		raw, err := os.ReadFile(path)
		if err == nil {
			out[path] = fmt.Sprintf("%o:%s", info.Mode().Perm(), raw)
		}
		return err
	})
	if err != nil {
		f.t.Fatal(err)
	}
	return out
}

func (f *receivedFixture) unchanged(before map[string]string) {
	f.t.Helper()
	if after := f.snapshot(); !reflect.DeepEqual(before, after) {
		f.t.Errorf("response changed fixture files: before=%v after=%v", before, after)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.blocked != 0 {
		f.t.Errorf("attempted nonfixture destination: %d", f.blocked)
	}
	for _, r := range f.requests {
		if r.origin != f.hub {
			f.t.Errorf("refused response contacted destination: %s %s", r.origin, r.path)
		}
	}
}

func (f *receivedFixture) counts() (source, destination, closed, read int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, r := range f.requests {
		if r.origin != f.hub {
			destination++
		} else if r.path != "/v1/hub/identity" {
			source++
			closed += r.closed
			read += r.read
		}
	}
	return
}

func receivedError(t *testing.T, err error, status int, code, message string, details any) *hubError {
	t.Helper()
	var he *hubError
	if !errors.As(err, &he) {
		t.Fatalf("expected HTTP hubError, got %T: %v", err, err)
	}
	actualRaw, expectedRaw := receivedJSON(t, he.Details), receivedJSON(t, details)
	var actualDetails, expectedDetails any
	if err := json.Unmarshal(actualRaw, &actualDetails); err != nil {
		t.Fatalf("decode actual recovery JSON: %v", err)
	}
	if err := json.Unmarshal(expectedRaw, &expectedDetails); err != nil {
		t.Fatalf("decode expected recovery JSON: %v", err)
	}
	// Ignore object key order, retaining null/empty, all keys and exact strings.
	if he.Status != status || he.Code != code || he.Message != message || !reflect.DeepEqual(actualDetails, expectedDetails) {
		t.Errorf("HTTP error = %+v details=%s; want %d %s %q %s", he, actualRaw, status, code, message, expectedRaw)
	}
	return he
}

func (f *receivedFixture) notice() envelope.HubNotice {
	f.t.Helper()
	n := envelope.HubNotice{NoticeID: "ntc_01ARZ3NDEKTSV4RRFFQ69G5FAV", Kind: "moving", Hub: f.hub, NewURL: receivedDestination,
		Message: "Operator prose https://user:pass@example.invalid/?prose#is-prose", IssuedAt: envelope.FormatTime(time.Now().Add(-time.Minute)), ExpiresAt: envelope.FormatTime(time.Now().Add(time.Hour))}
	if err := f.key.SignNotice(&n); err != nil {
		f.t.Fatal(err)
	}
	return n
}

func (f *receivedFixture) launcherArgs() []string {
	f.t.Helper()
	// launcherSetup's git child also gets a synthetic HOME and no global config.
	f.t.Setenv("HOME", f.env["HOME"])
	f.t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	f.t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	f.t.Setenv("GIT_CONFIG_COUNT", "0")
	f.repo = filepath.Join(f.env["HOME"], "repo")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, args := range [][]string{{"init", "-q", f.repo}, {"-C", f.repo, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "--allow-empty", "-qm", "fixture"}} {
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + f.env["HOME"], "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=" + os.DevNull}
		if out, err := cmd.CombinedOutput(); err != nil {
			f.t.Fatalf("synthetic git fixture: %v %s", err, out)
		}
	}
	return []string{"setup", "--project", "fixture", "--repo", f.repo, "--base-ref", "HEAD", "--codex-home", f.env["CODEX_HOME"], "--write"}
}

func (f *receivedFixture) constructor(kind string, flag bool, launcherArgs []string) error {
	// Pin the actual first body-consuming endpoint, not just an error code from
	// a catch-all server route. Enrol's POST is its authorized preceding write.
	endpoints := map[string]string{"connect": "/v1/probe", "profile-follow": "/v1/hub/notices", "codex-auto": "/v1/peers/me", "codex-bind": "/v1/peers/me", "launcher": "/v1/peers/me", "enrol": "/v1/enrol"}
	defer func() {
		f.mu.Lock()
		defer f.mu.Unlock()
		count := 0
		for _, r := range f.requests {
			if r.path == "/v1/hub/identity" {
				continue
			}
			count++
			method, auth := "GET", "Bearer vn_operator_fixture"
			if kind == "enrol" {
				method, auth = "POST", ""
			}
			if r.origin != f.hub || r.path != endpoints[kind] || r.method != method || r.auth != auth {
				f.t.Errorf("wrong %s constructor request: %+v", kind, r)
			}
		}
		if count != 1 {
			f.t.Errorf("%s constructor body requests=%d, want one", kind, count)
		}
	}()
	a := f.app()
	switch kind {
	case "connect":
		c, _, _, err := a.connect()
		if err != nil {
			return err
		}
		return c.do(a.ctx, "GET", "/v1/probe", nil, nil, nil)
	case "profile-follow":
		return a.cmdProfile([]string{"follow"})
	case "codex-auto":
		return a.registerCodexAuto(bindT1, f.env["CODEX_HOME"])
	case "codex-bind":
		return a.cmdCodex([]string{"bind", "--thread", bindT1}, time.Now)
	case "launcher":
		return a.cmdLauncher(launcherArgs)
	case "enrol":
		args := []string{"--hub", f.hub, "--token", "fixture-token", "--profile", "new-profile"}
		if flag {
			args = append(args, "--allow-insecure-http")
		}
		return a.cmdEnrol(args)
	default:
		f.t.Fatalf("unknown constructor %s", kind)
		return nil
	}
}
