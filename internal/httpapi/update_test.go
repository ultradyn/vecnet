package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/httpapi"
	"github.com/XertroV/vecnet/internal/selfupdate"
	"github.com/XertroV/vecnet/internal/testhub"
)

// updateFixture wires a selfupdate.Service with a fake registry, a fake
// host filesystem and a fake npm into a test hub. Nothing reaches npm,
// installs anything or restarts.
type updateFixture struct {
	env      *testhub.Env
	svc      *selfupdate.Service
	registry atomic.Int32
	mu       sync.Mutex
	installs [][]string
	restarts atomic.Int32
	latest   atomic.Value
}

type memInfo struct{ mode fs.FileMode }

func (m memInfo) Name() string       { return "f" }
func (m memInfo) Size() int64        { return 0 }
func (m memInfo) Mode() fs.FileMode  { return m.mode }
func (m memInfo) ModTime() time.Time { return time.Time{} }
func (m memInfo) IsDir() bool        { return false }
func (m memInfo) Sys() any           { return nil }

func newUpdateFixture(t *testing.T, oneClick bool) *updateFixture {
	t.Helper()
	f := &updateFixture{env: testhub.Start(t)}
	f.latest.Store("0.2.7")
	reg := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.registry.Add(1)
		_, _ = w.Write([]byte(`{"latest":"` + f.latest.Load().(string) + `"}`))
	}))
	t.Cleanup(reg.Close)
	const root = "/p/lib/node_modules/vecnet"
	var filesMu sync.Mutex
	files := map[string]string{root + "/package.json": `{"name":"vecnet","version":"0.2.6"}`, root + "/native/linux-x64/vecnet-hub": "\x7fELF"}
	read := func(p string) ([]byte, error) {
		filesMu.Lock()
		defer filesMu.Unlock()
		if s, ok := files[p]; ok {
			return []byte(s), nil
		}
		return nil, os.ErrNotExist
	}
	sys := selfupdate.System{
		Getenv:   func(string) string { return "" },
		ReadFile: read,
		ReadHead: func(p string, n int) ([]byte, error) { return read(p) },
		Stat: func(p string) (fs.FileInfo, error) {
			if _, err := read(p); err != nil {
				return nil, err
			}
			return memInfo{0o755}, nil
		},
		Run: func(context.Context, string, []string, []string) ([]byte, error) {
			t.Error("System.Run called; tests must use the injected installer")
			return nil, errors.New("no")
		},
	}
	c := &selfupdate.Checker{URL: reg.URL, Client: reg.Client(), Current: "0.2.6", Interval: time.Hour}
	f.svc = selfupdate.NewService(c, sys)
	f.svc.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
	f.svc.Install = func(_ context.Context, name string, args, _ []string) ([]byte, error) {
		f.mu.Lock()
		f.installs = append(f.installs, append([]string{name}, args...))
		f.mu.Unlock()
		filesMu.Lock()
		files[root+"/package.json"] = `{"name":"vecnet","version":"` + args[len(args)-1][len("vecnet@"):] + `"}`
		filesMu.Unlock()
		return nil, nil
	}
	f.svc.Restart = func() { f.restarts.Add(1) }
	f.svc.Audit = httpapi.AuditUpdates(f.env.Hub)
	if oneClick {
		f.svc.SetDetected(&selfupdate.Install{Prefix: "/p", Root: root, Exe: root + "/native/linux-x64/vecnet-hub", NPM: "/p/bin/npm"}, "")
	} else {
		f.svc.SetDetected(nil, selfupdate.ReasonContainer)
	}
	if _, err := c.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	f.env.Configure(func(d *httpapi.Deps) { d.Update = f.svc })
	return f
}

func (f *updateFixture) installCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.installs)
}

func decodeUpdateStatus(t *testing.T, raw []byte) selfupdate.Status {
	t.Helper()
	var st selfupdate.Status
	if err := json.Unmarshal(raw, &st); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}
	return st
}

func waitUpdateState(t *testing.T, e *testhub.Env, want string) selfupdate.Status {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		resp, raw := e.Do(e.OperatorSecret, "GET", "/v1/admin/update", nil)
		if resp.StatusCode != 200 {
			t.Fatalf("GET = %d %s", resp.StatusCode, raw)
		}
		st := decodeUpdateStatus(t, raw)
		if st.Job != nil && st.Job.State == want {
			return st
		}
		if time.Now().After(deadline) {
			t.Fatalf("job never reached %s: %s", want, raw)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestUpdateRoutesAreOperatorOnly(t *testing.T) {
	f := newUpdateFixture(t, true)
	e := f.env
	_, peer := e.NewPeer("session", "update peer")
	_, principal := e.NewPrincipal("update principal")
	for _, method := range []string{"GET", "POST"} {
		resp, raw := e.Do("", method, "/v1/admin/update", nil)
		mustErr(t, resp, raw, 401, "unauthenticated")
		for _, secret := range []string{peer, principal} {
			resp, raw := e.Do(secret, method, "/v1/admin/update", nil)
			mustErr(t, resp, raw, 403, "forbidden")
		}
	}
	if n := f.installCount(); n != 0 {
		t.Fatalf("installs = %d", n)
	}
}

func TestGetUpdateReportsStatus(t *testing.T) {
	f := newUpdateFixture(t, true)
	before := f.registry.Load()
	resp, raw := f.env.Do(f.env.OperatorSecret, "GET", "/v1/admin/update", nil)
	if resp.StatusCode != 200 || resp.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("GET = %d %v %s", resp.StatusCode, resp.Header, raw)
	}
	st := decodeUpdateStatus(t, raw)
	if !st.Enabled || st.Current != "0.2.6" || st.Latest != "0.2.7" || !st.UpdateAvailable || !st.OneClick || !st.Restart {
		t.Fatalf("status = %s", raw)
	}
	if f.registry.Load() != before {
		t.Fatal("GET contacted the registry")
	}
}

func TestGetUpdateWithoutServiceIsDisabled(t *testing.T) {
	e := testhub.Start(t)
	resp, raw := e.Do(e.OperatorSecret, "GET", "/v1/admin/update", nil)
	if resp.StatusCode != 200 {
		t.Fatalf("GET = %d %s", resp.StatusCode, raw)
	}
	if st := decodeUpdateStatus(t, raw); st.Enabled || st.UpdateAvailable || st.OneClick {
		t.Fatalf("status = %s", raw)
	}
	resp, raw = e.Do(e.OperatorSecret, "POST", "/v1/admin/update", nil)
	mustErr(t, resp, raw, 404, "update_unavailable")
}

func TestPostUpdateInstallsRegistryVersionAndRestarts(t *testing.T) {
	f := newUpdateFixture(t, true)
	resp, raw := f.env.Do(f.env.OperatorSecret, "POST", "/v1/admin/update", nil)
	if resp.StatusCode != 202 {
		t.Fatalf("POST = %d %s", resp.StatusCode, raw)
	}
	var out struct{ Job selfupdate.Job }
	if err := json.Unmarshal(raw, &out); err != nil || out.Job.Version != "0.2.7" {
		t.Fatalf("POST body %s", raw)
	}
	waitUpdateState(t, f.env, selfupdate.StateRestarting)
	deadline := time.Now().Add(5 * time.Second)
	for f.restarts.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if f.restarts.Load() != 1 || f.installCount() != 1 || !strings.HasSuffix(strings.Join(f.installs[0], " "), " vecnet@0.2.7") {
		t.Fatalf("restarts %d installs %v", f.restarts.Load(), f.installs)
	}
	resp, raw = f.env.Do(f.env.OperatorSecret, "GET", "/v1/admin/audit?limit=5", nil)
	if resp.StatusCode != 200 || !bytes.Contains(raw, []byte(`"hub_update"`)) || !bytes.Contains(raw, []byte(`"to":"0.2.7"`)) {
		t.Fatalf("audit = %d %s", resp.StatusCode, raw)
	}
}

// The body may not name a version (or anything else): the hub installs only
// what it read from the registry.
func TestPostUpdateRefusesBody(t *testing.T) {
	f := newUpdateFixture(t, true)
	for _, body := range []string{`{"version":"0.0.1"}`, `{"package":"evil"}`, `[]`, `"0.2.7"`, `garbage`} {
		resp, raw := f.env.Do(f.env.OperatorSecret, "POST", "/v1/admin/update", body)
		mustErr(t, resp, raw, 400, "invalid_body")
	}
	if n := f.installCount(); n != 0 {
		t.Fatalf("installs = %d", n)
	}
	// An empty object is the same as no body.
	resp, raw := f.env.Do(f.env.OperatorSecret, "POST", "/v1/admin/update", `{}`)
	if resp.StatusCode != 202 {
		t.Fatalf("POST {} = %d %s", resp.StatusCode, raw)
	}
}

func TestPostUpdateRefusesCrossOrigin(t *testing.T) {
	f := newUpdateFixture(t, true)
	for name, hdr := range map[string][2]string{
		"sec-fetch cross-site": {"Sec-Fetch-Site", "cross-site"},
		"sec-fetch same-site":  {"Sec-Fetch-Site", "same-site"},
		"foreign origin":       {"Origin", "https://evil.example"},
		"null origin":          {"Origin", "null"},
	} {
		t.Run(name, func(t *testing.T) {
			req, _ := http.NewRequest("POST", f.env.URL+"/v1/admin/update", nil)
			req.Header.Set("Authorization", "Bearer "+f.env.OperatorSecret)
			req.Header.Set(hdr[0], hdr[1])
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			raw, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			mustErr(t, resp, raw, 403, "forbidden")
		})
	}
	if n := f.installCount(); n != 0 {
		t.Fatalf("installs = %d", n)
	}
	// The UI's own same-origin request passes.
	req, _ := http.NewRequest("POST", f.env.URL+"/v1/admin/update", nil)
	req.Header.Set("Authorization", "Bearer "+f.env.OperatorSecret)
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	req.Header.Set("Origin", f.env.URL)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 202 {
		t.Fatalf("same-origin POST = %d", resp.StatusCode)
	}
}

func TestPostUpdateBusyAndCooldown(t *testing.T) {
	f := newUpdateFixture(t, true)
	f.svc.Restart = nil // end the job "installed" so cooldown applies next
	resp, raw := f.env.Do(f.env.OperatorSecret, "POST", "/v1/admin/update", nil)
	if resp.StatusCode != 202 {
		t.Fatalf("POST = %d %s", resp.StatusCode, raw)
	}
	waitUpdateState(t, f.env, selfupdate.StateInstalled)
	resp, raw = f.env.Do(f.env.OperatorSecret, "POST", "/v1/admin/update", nil)
	// The installed package is now 0.2.7 but this process still runs 0.2.6
	// and npm still says 0.2.7, so a second click is a cooldown, not a loop.
	e := mustErr(t, resp, raw, 429, "update_cooldown")
	if resp.Header.Get("Retry-After") == "" || e.Error.Details["retry_after_seconds"] == nil {
		t.Fatalf("429 without retry hint: %v %s", resp.Header, raw)
	}
	if n := f.installCount(); n != 1 {
		t.Fatalf("installs = %d", n)
	}
}

func TestPostUpdateUnavailableWithoutOneClick(t *testing.T) {
	f := newUpdateFixture(t, false)
	resp, raw := f.env.Do(f.env.OperatorSecret, "GET", "/v1/admin/update", nil)
	st := decodeUpdateStatus(t, raw)
	if resp.StatusCode != 200 || !st.UpdateAvailable || st.OneClick || st.ManualReason != selfupdate.ReasonContainer || st.ManualCommand != "npm install -g vecnet@0.2.7" {
		t.Fatalf("status = %s", raw)
	}
	resp, raw = f.env.Do(f.env.OperatorSecret, "POST", "/v1/admin/update", nil)
	mustErr(t, resp, raw, 404, "update_unavailable")
	if n := f.installCount(); n != 0 {
		t.Fatalf("installs = %d", n)
	}
}

func TestPostUpdateNoNewerVersion(t *testing.T) {
	f := newUpdateFixture(t, true)
	f.latest.Store("0.2.6")
	if _, err := f.svc.Checker.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	resp, raw := f.env.Do(f.env.OperatorSecret, "POST", "/v1/admin/update", nil)
	mustErr(t, resp, raw, 409, "no_update")
}

// The cached answer may be stale: the job installs, and the audit records,
// what the registry says when the job runs.
func TestPostUpdateAuditsConfirmedVersion(t *testing.T) {
	f := newUpdateFixture(t, true)
	f.svc.Restart = nil
	f.latest.Store("0.2.8")
	resp, raw := f.env.Do(f.env.OperatorSecret, "POST", "/v1/admin/update", nil)
	if resp.StatusCode != 202 {
		t.Fatalf("POST = %d %s", resp.StatusCode, raw)
	}
	st := waitUpdateState(t, f.env, selfupdate.StateInstalled)
	if st.Job.Version != "0.2.8" || !strings.HasSuffix(strings.Join(f.installs[0], " "), " vecnet@0.2.8") {
		t.Fatalf("job %+v installs %v", st.Job, f.installs)
	}
	resp, raw = f.env.Do(f.env.OperatorSecret, "GET", "/v1/admin/audit?limit=5", nil)
	if resp.StatusCode != 200 || !bytes.Contains(raw, []byte(`"to":"0.2.8"`)) || bytes.Contains(raw, []byte(`"to":"0.2.7"`)) {
		t.Fatalf("audit = %s", raw)
	}
}
