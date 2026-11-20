package main

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/buildinfo"
	"github.com/XertroV/vecnet/internal/selfupdate"
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
	for _, v := range []string{"-1", "-1h", "soon", "1.5"} {
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

// The restart path: a one-click update drains the server and serve returns
// restartError naming the detected binary, after closing the store.
func TestOneClickUpdateDrainsAndRequestsRestart(t *testing.T) {
	withVersion(t, "0.2.6")
	reg, _ := countingRegistry(t, "0.2.7")
	const root = "/p/lib/node_modules/vecnet"
	exe := root + "/native/linux-x64/vecnet-hub"
	var filesMu sync.Mutex
	files := map[string]string{root + "/package.json": `{"name":"vecnet","version":"0.2.6"}`, exe: "\x7fELF"}
	read := func(p string) ([]byte, error) {
		filesMu.Lock()
		defer filesMu.Unlock()
		if s, ok := files[p]; ok {
			return []byte(s), nil
		}
		return nil, os.ErrNotExist
	}
	var installs atomic.Int32
	withServeHook(t, func(c *serveConfig) {
		c.UpdateSetup = func(s *selfupdate.Service) {
			s.Checker.URL, s.Checker.Client = reg.URL, reg.Client()
			s.Checker.InitialDelay = time.Millisecond
			sys := refuseSystem(t)
			sys.ReadFile = read
			sys.ReadHead = func(p string, _ int) ([]byte, error) { return read(p) }
			sys.Stat = func(p string) (fs.FileInfo, error) {
				if _, err := read(p); err != nil {
					return nil, err
				}
				return execInfo{}, nil
			}
			s.Sys = sys
			s.Install = func(context.Context, string, []string, []string) ([]byte, error) {
				installs.Add(1)
				filesMu.Lock()
				files[root+"/package.json"] = `{"name":"vecnet","version":"0.2.7"}`
				filesMu.Unlock()
				return nil, nil
			}
			// Detection on this machine would refuse; record a detected install.
			s.SetDetected(&selfupdate.Install{Prefix: "/p", Root: root, Exe: exe, NPM: "/p/bin/npm"}, "")
		}
	})
	dataDir, op := initHub(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out := &lockedBuffer{}
	done := make(chan error, 1)
	go func() {
		done <- run(ctx, []string{"--listen", "127.0.0.1:0", "--data-dir", dataDir}, out, noEnv)
	}()
	var base string
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
	resp, raw := hubDo(t, "POST", base+"/v1/admin/update", op, nil)
	if resp.StatusCode != 202 {
		t.Fatalf("POST = %d %s", resp.StatusCode, raw)
	}
	select {
	case err := <-done:
		var re *restartError
		if !errors.As(err, &re) || re.exe != exe {
			t.Fatalf("run = %v", err)
		}
	case <-time.After(smokeShutdownBudget):
		t.Fatal("hub did not drain for the restart")
	}
	if installs.Load() != 1 {
		t.Fatalf("installs = %d", installs.Load())
	}
	// The store is closed: a fresh hub opens the same data dir.
	h := bootHubFromDisk(t, []string{"--listen", "127.0.0.1:0", "--data-dir", dataDir, "--update-check-interval", "0"}, noEnv)
	if resp, _ := hubDo(t, "GET", h.base+"/healthz", "", nil); resp.StatusCode != 200 {
		t.Fatalf("reopened hub healthz = %d", resp.StatusCode)
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
