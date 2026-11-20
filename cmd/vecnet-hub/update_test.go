package main

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/buildinfo"
	"github.com/XertroV/vecnet/internal/selfupdate"
	"github.com/XertroV/vecnet/internal/selfupdate/selfupdatetest"
)

// countingRegistry is a fake npm dist-tags endpoint that counts hits.
func countingRegistry(t *testing.T, latest string) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte(`{"latest":"` + latest + `"}`))
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

// withServeHook installs testServeHook for one test.
func withServeHook(t *testing.T, hook func(*serveConfig)) {
	t.Helper()
	testServeHook = func(c *serveConfig) error { hook(c); return nil }
	t.Cleanup(func() { testServeHook = nil })
}

// withVersion pretends the build is version v.
func withVersion(t *testing.T, v string) {
	t.Helper()
	old := buildinfo.Version
	buildinfo.Version = v
	t.Cleanup(func() { buildinfo.Version = old })
}

func TestUpdateCheckIntervalFlag(t *testing.T) {
	for _, v := range []string{"-1", "-1h", "soon", "1.5", "721h", "9999"} {
		err := run(context.Background(), []string{"--update-check-interval", v}, io.Discard, noEnv)
		if err == nil || !strings.Contains(err.Error(), "update-check-interval") || strings.Contains(err.Error(), "soon") {
			t.Fatalf("flag %q = %v", v, err)
		}
		err = run(context.Background(), nil, io.Discard, func(k string) string {
			if k == "VECNET_UPDATE_CHECK_INTERVAL" {
				return v
			}
			return ""
		})
		if err == nil || !strings.Contains(err.Error(), "update-check-interval") {
			t.Fatalf("env %q = %v", v, err)
		}
	}
	for _, tc := range []struct {
		args []string
		env  string
		want time.Duration
	}{
		{nil, "", selfupdate.DefaultInterval},
		{nil, "0", 0},
		{nil, "2", 2 * time.Hour},
		{nil, "1m", selfupdate.MinInterval},
		{[]string{"--update-check-interval", "0"}, "6h", 0},
		{[]string{"--update-check-interval", "45m"}, "0", 45 * time.Minute},
	} {
		var got time.Duration
		seen := false
		stop := errors.New("stop before serving")
		testServeHook = func(c *serveConfig) error { got, seen = c.UpdateInterval, true; return stop }
		t.Cleanup(func() { testServeHook = nil })
		args := append([]string{"--listen", "127.0.0.1:0", "--data-dir", t.TempDir()}, tc.args...)
		_ = run(context.Background(), args, io.Discard, func(k string) string {
			if k == "VECNET_UPDATE_CHECK_INTERVAL" {
				return tc.env
			}
			return ""
		})
		if !seen || got != tc.want {
			t.Fatalf("args %v env %q: interval %v (seen %v), want %v", tc.args, tc.env, got, seen, tc.want)
		}
	}
}

// A release build with interval 0 serves the hub but never contacts the
// registry, and the UI endpoint reports the check disabled.
func TestUpdateCheckDisabledMakesNoRequests(t *testing.T) {
	withVersion(t, "0.2.6")
	reg, hits := countingRegistry(t, "0.2.7")
	withServeHook(t, func(c *serveConfig) {
		c.UpdateSetup = func(s *selfupdate.Service) {
			s.Checker.URL, s.Checker.Client = reg.URL, reg.Client()
			s.Checker.InitialDelay = time.Millisecond
			s.Sys = refuseSystem(t)
		}
	})
	dataDir, op := initHub(t)
	h := bootHubFromDisk(t, []string{"--listen", "127.0.0.1:0", "--data-dir", dataDir, "--update-check-interval", "0"}, noEnv)
	time.Sleep(200 * time.Millisecond)
	resp, raw := hubDo(t, "GET", h.base+"/v1/admin/update", op, nil)
	if resp.StatusCode != 200 || !strings.Contains(string(raw), `"enabled":false`) {
		t.Fatalf("GET = %d %s", resp.StatusCode, raw)
	}
	if n := hits.Load(); n != 0 {
		t.Fatalf("registry hits = %d, want 0", n)
	}
}

// A release build with a non-zero interval checks the fake registry, after
// its (shortened) start delay, and reports the result.
func TestUpdateCheckEnabledReportsLatest(t *testing.T) {
	withVersion(t, "0.2.6")
	reg, hits := countingRegistry(t, "0.2.7")
	withServeHook(t, func(c *serveConfig) {
		c.UpdateSetup = func(s *selfupdate.Service) {
			s.Checker.URL, s.Checker.Client = reg.URL, reg.Client()
			s.Checker.InitialDelay = time.Millisecond
			s.Sys = refuseSystem(t)
		}
	})
	dataDir, op := initHub(t)
	h := bootHubFromDisk(t, []string{"--listen", "127.0.0.1:0", "--data-dir", dataDir}, noEnv)
	var raw []byte
	waitUntil(t, 5*time.Second, "update check", func() bool {
		_, raw = hubDo(t, "GET", h.base+"/v1/admin/update", op, nil)
		return strings.Contains(string(raw), `"update_available":true`)
	})
	if !strings.Contains(string(raw), `"one_click":false`) || !strings.Contains(string(raw), `"manual_command":"npm install -g vecnet@0.2.7"`) {
		t.Fatalf("status = %s", raw)
	}
	if hits.Load() != 1 {
		t.Fatalf("registry hits = %d", hits.Load())
	}
}

// freeListen returns a loopback address with a fixed, currently free port:
// one-click update refuses an ephemeral (:0) listen address.
func freeListen(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	return addr
}

// oneClickHub boots a release hub (0.2.6) on a fixed port whose update
// service sees a fake global npm install and a fake registry naming 0.2.7.
// wrapRestart, when non-nil, wraps the restart hook.
func oneClickHub(t *testing.T, ctx context.Context, wrapRestart func(func()) func()) (base, op string, host *selfupdatetest.Host, done chan error) {
	t.Helper()
	withVersion(t, "0.2.6")
	reg, _ := countingRegistry(t, "0.2.7")
	host = selfupdatetest.New("0.2.6")
	withServeHook(t, func(c *serveConfig) {
		c.UpdateSetup = func(s *selfupdate.Service) {
			s.Checker.URL, s.Checker.Client = reg.URL, reg.Client()
			s.Checker.InitialDelay = time.Millisecond
			s.Sys = host.System()
			s.Install = host.Install
			if wrapRestart != nil {
				s.Restart = wrapRestart(s.Restart)
			}
		}
	})
	dataDir, op := initHub(t)
	out := &lockedBuffer{}
	done = make(chan error, 1)
	go func() {
		done <- run(ctx, []string{"--listen", freeListen(t), "--data-dir", dataDir}, out, noEnv)
	}()
	waitUntil(t, 5*time.Second, "listening", func() bool {
		if m := listenLine.FindStringSubmatch(out.String()); m != nil {
			base = m[1]
			return true
		}
		return false
	})
	waitUntil(t, 5*time.Second, "update check", func() bool {
		_, raw := hubDo(t, "GET", base+"/v1/admin/update", op, nil)
		return strings.Contains(string(raw), `"one_click":true`)
	})
	t.Cleanup(func() {
		// Re-open the data dir: the store must have been closed.
		h := bootHubFromDisk(t, []string{"--listen", "127.0.0.1:0", "--data-dir", dataDir, "--update-check-interval", "0"}, noEnv)
		if resp, _ := hubDo(t, "GET", h.base+"/healthz", "", nil); resp.StatusCode != 200 {
			t.Errorf("reopened hub healthz = %d", resp.StatusCode)
		}
	})
	return base, op, host, done
}

// The restart path: a one-click update installs, self-checks, drains the
// server, and serve returns restartError naming the binary, store closed.
func TestOneClickUpdateDrainsAndRequestsRestart(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	base, op, host, done := oneClickHub(t, ctx, nil)
	resp, raw := hubDo(t, "POST", base+"/v1/admin/update", op, nil)
	if resp.StatusCode != 202 {
		t.Fatalf("POST = %d %s", resp.StatusCode, raw)
	}
	select {
	case err := <-done:
		var re *restartError
		if !errors.As(err, &re) || re.exe != selfupdatetest.Exe {
			t.Fatalf("run = %v", err)
		}
	case <-time.After(smokeShutdownBudget):
		t.Fatal("hub did not drain for the restart")
	}
	if n := len(host.Installs()); n != 1 {
		t.Fatalf("installs = %d", n)
	}
}

// M1: a stop signal during the restart drain wins: serve returns nil and
// main never re-executes.
func TestStopSignalDuringRestartDrainExits(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	base, op, _, done := oneClickHub(t, ctx, func(restart func()) func() {
		return func() {
			restart()
			cancel() // SIGTERM arrives while the hub drains for the restart
		}
	})
	resp, raw := hubDo(t, "POST", base+"/v1/admin/update", op, nil)
	if resp.StatusCode != 202 {
		t.Fatalf("POST = %d %s", resp.StatusCode, raw)
	}
	select {
	case err := <-done:
		var re *restartError
		if errors.As(err, &re) || err != nil {
			t.Fatalf("run = %v, want a plain stop", err)
		}
	case <-time.After(smokeShutdownBudget):
		t.Fatal("hub did not stop")
	}
}

// L8: a hub on :0 is not offered one-click update.
func TestEphemeralListenNotOneClick(t *testing.T) {
	withVersion(t, "0.2.6")
	reg, _ := countingRegistry(t, "0.2.7")
	host := selfupdatetest.New("0.2.6")
	withServeHook(t, func(c *serveConfig) {
		c.UpdateSetup = func(s *selfupdate.Service) {
			s.Checker.URL, s.Checker.Client = reg.URL, reg.Client()
			s.Checker.InitialDelay = time.Millisecond
			s.Sys = host.System()
			s.Install = host.Install
		}
	})
	dataDir, op := initHub(t)
	h := bootHubFromDisk(t, []string{"--listen", "127.0.0.1:0", "--data-dir", dataDir}, noEnv)
	var raw []byte
	waitUntil(t, 5*time.Second, "update check", func() bool {
		_, raw = hubDo(t, "GET", h.base+"/v1/admin/update", op, nil)
		return strings.Contains(string(raw), `"update_available":true`)
	})
	if !strings.Contains(string(raw), `"one_click":false`) || !strings.Contains(string(raw), `"manual_reason":"ephemeral_port"`) {
		t.Fatalf("status = %s", raw)
	}
}

func TestEphemeralPort(t *testing.T) {
	for addr, want := range map[string]bool{
		"127.0.0.1:0": true, ":0": true, "[::1]:0": true, "nonsense": true,
		"127.0.0.1:45752": false, ":8080": false, "[::1]:45752": false,
	} {
		if got := ephemeralPort(addr); got != want {
			t.Errorf("ephemeralPort(%q) = %v", addr, got)
		}
	}
}

// The update self-check runs the new binary with --version.
func TestVersionFlag(t *testing.T) {
	withVersion(t, "0.2.7")
	var out strings.Builder
	if err := run(context.Background(), []string{"--version"}, &out, noEnv); err != nil {
		t.Fatal(err)
	}
	if out.String() != "vecnet-hub 0.2.7\n" {
		t.Fatalf("--version = %q", out.String())
	}
}

// refuseSystem is a host on which detection refuses and no command runs.
func refuseSystem(t *testing.T) selfupdate.System {
	sys := selfupdate.DefaultSystem()
	sys.Getenv = func(k string) string {
		if k == "FLY_APP_NAME" {
			return "test"
		}
		return ""
	}
	sys.Run = func(context.Context, string, []string, []string) ([]byte, error) {
		t.Error("a command ran during a hub test")
		return nil, errors.New("no")
	}
	return sys
}

// execInfo is an executable regular file, on any OS.
type execInfo struct{}

func (execInfo) Name() string       { return "vecnet-hub" }
func (execInfo) Size() int64        { return 4 }
func (execInfo) Mode() fs.FileMode  { return 0o755 }
func (execInfo) ModTime() time.Time { return time.Time{} }
func (execInfo) IsDir() bool        { return false }
func (execInfo) Sys() any           { return nil }
