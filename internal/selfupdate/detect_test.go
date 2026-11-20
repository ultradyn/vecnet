package selfupdate

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path"
	"strings"
	"testing"
	"time"
)

// fakeHost is an in-memory host: files, env, PATH lookups and commands.
// Detection and installs run against it, never the machine running tests.
type fakeHost struct {
	files    map[string]string
	modes    map[string]fs.FileMode
	env      map[string]string
	links    map[string]string
	exe      string
	path     map[string]string
	readonly map[string]bool
	prefix   string // `npm prefix -g` output
	runs     [][]string
	runErr   error
}

type fakeInfo struct {
	name string
	mode fs.FileMode
}

func (f fakeInfo) Name() string       { return f.name }
func (f fakeInfo) Size() int64        { return 0 }
func (f fakeInfo) Mode() fs.FileMode  { return f.mode }
func (f fakeInfo) ModTime() time.Time { return time.Time{} }
func (f fakeInfo) IsDir() bool        { return f.mode.IsDir() }
func (f fakeInfo) Sys() any           { return nil }

const prefix = "/home/u/.npm-global"
const pkgRoot = prefix + "/lib/node_modules/vecnet"
const nativeExe = pkgRoot + "/native/linux-x64/vecnet-hub"

func newFakeHost(version string) *fakeHost {
	return &fakeHost{
		files: map[string]string{
			pkgRoot + "/package.json": `{"name":"vecnet","version":"` + version + `"}`,
			nativeExe:                 "\x7fELF....",
			prefix + "/bin/npm":       "#!/usr/bin/env node",
			"/proc/1/cgroup":          "0::/init.scope\n",
		},
		modes:    map[string]fs.FileMode{nativeExe: 0o755, prefix + "/bin/npm": 0o755},
		env:      map[string]string{"HOME": "/home/u", "PATH": "/usr/bin:/bin", "VECNET_SECRET_THING": "x"},
		links:    map[string]string{},
		exe:      nativeExe,
		path:     map[string]string{},
		readonly: map[string]bool{},
		prefix:   prefix,
	}
}

func (h *fakeHost) system() System {
	return System{
		GOOS: "linux", GOARCH: "amd64",
		Executable: func() (string, error) { return h.exe, nil },
		EvalSymlinks: func(p string) (string, error) {
			if t, ok := h.links[p]; ok {
				return t, nil
			}
			return path.Clean(p), nil
		},
		Getenv: func(k string) string { return h.env[k] },
		Stat: func(p string) (fs.FileInfo, error) {
			if _, ok := h.files[p]; !ok {
				return nil, os.ErrNotExist
			}
			m := h.modes[p]
			if m == 0 {
				m = 0o644
			}
			return fakeInfo{path.Base(p), m}, nil
		},
		ReadFile: func(p string) ([]byte, error) {
			if s, ok := h.files[p]; ok {
				return []byte(s), nil
			}
			return nil, os.ErrNotExist
		},
		ReadHead: func(p string, n int) ([]byte, error) {
			s, ok := h.files[p]
			if !ok {
				return nil, os.ErrNotExist
			}
			if len(s) > n {
				s = s[:n]
			}
			return []byte(s), nil
		},
		LookPath: func(name string) (string, error) {
			if p, ok := h.path[name]; ok {
				return p, nil
			}
			return "", errors.New("not found")
		},
		Writable: func(dir string) bool { return !h.readonly[dir] },
		Run: func(_ context.Context, name string, args, env []string) ([]byte, error) {
			h.runs = append(h.runs, append([]string{name}, args...))
			if h.runErr != nil {
				return nil, h.runErr
			}
			return []byte(h.prefix + "\n"), nil
		},
	}
}

func TestDetectAcceptsGlobalNPMInstall(t *testing.T) {
	h := newFakeHost("0.2.6")
	inst, reason := Detect(context.Background(), h.system(), "0.2.6")
	if inst == nil {
		t.Fatalf("Detect refused: %s", reason)
	}
	if inst.Prefix != prefix || inst.Root != pkgRoot || inst.Exe != nativeExe || inst.NPM != prefix+"/bin/npm" {
		t.Fatalf("install = %+v", inst)
	}
	if len(h.runs) != 1 || strings.Join(h.runs[0], " ") != prefix+"/bin/npm prefix -g" {
		t.Fatalf("runs = %v", h.runs)
	}
}

func TestDetectFollowsSymlinkedExecutable(t *testing.T) {
	h := newFakeHost("0.2.6")
	h.exe = "/usr/local/bin/vecnet-hub-link"
	h.links[h.exe] = nativeExe
	if inst, reason := Detect(context.Background(), h.system(), "0.2.6"); inst == nil {
		t.Fatalf("Detect refused: %s", reason)
	}
}

func TestDetectUsesPATHNPMWhenPrefixHasNone(t *testing.T) {
	h := newFakeHost("0.2.6")
	delete(h.files, prefix+"/bin/npm")
	h.path["npm"] = "/usr/bin/npm"
	inst, reason := Detect(context.Background(), h.system(), "0.2.6")
	if inst == nil || inst.NPM != "/usr/bin/npm" {
		t.Fatalf("Detect = %+v, %s", inst, reason)
	}
}

func TestDetectRefusals(t *testing.T) {
	for _, tc := range []struct {
		name, want string
		mutate     func(*fakeHost, *System)
		current    string
	}{
		{"windows", ReasonUnsupportedOS, func(_ *fakeHost, s *System) { s.GOOS = "windows" }, ""},
		{"darwin", ReasonUnsupportedOS, func(_ *fakeHost, s *System) { s.GOOS = "darwin" }, ""},
		{"riscv", ReasonUnsupportedOS, func(_ *fakeHost, s *System) { s.GOARCH = "riscv64" }, ""},
		{"fly", ReasonContainer, func(h *fakeHost, _ *System) { h.env["FLY_APP_NAME"] = "vecnet" }, ""},
		{"fly machine", ReasonContainer, func(h *fakeHost, _ *System) { h.env["FLY_MACHINE_ID"] = "m" }, ""},
		{"kubernetes", ReasonContainer, func(h *fakeHost, _ *System) { h.env["KUBERNETES_SERVICE_HOST"] = "10.0.0.1" }, ""},
		{"podman env", ReasonContainer, func(h *fakeHost, _ *System) { h.env["container"] = "podman" }, ""},
		{"dockerenv", ReasonContainer, func(h *fakeHost, _ *System) { h.files["/.dockerenv"] = "" }, ""},
		{"containerenv", ReasonContainer, func(h *fakeHost, _ *System) { h.files["/run/.containerenv"] = "" }, ""},
		{"cgroup docker", ReasonContainer, func(h *fakeHost, _ *System) { h.files["/proc/1/cgroup"] = "0::/docker/abc\n" }, ""},
		{"cgroup kubepods", ReasonContainer, func(h *fakeHost, _ *System) { h.files["/proc/1/cgroup"] = "0::/kubepods/x\n" }, ""},
		{"source build", ReasonNotNPMGlobal, func(h *fakeHost, _ *System) { h.exe = "/home/u/src/vecnet/bin/vecnet-hub" }, ""},
		{"scratch image", ReasonNotNPMGlobal, func(h *fakeHost, _ *System) { h.exe = "/vecnet-hub" }, ""},
		{"local node_modules", ReasonNotNPMGlobal, func(h *fakeHost, _ *System) {
			h.exe = "/home/u/proj/node_modules/vecnet/native/linux-x64/vecnet-hub"
		}, ""},
		{"npx cache", ReasonNotNPMGlobal, func(h *fakeHost, _ *System) {
			h.exe = "/home/u/.npm/_npx/abc/node_modules/vecnet/native/linux-x64/vecnet-hub"
		}, ""},
		{"wrong arch dir", ReasonNotNPMGlobal, func(h *fakeHost, _ *System) {
			h.exe = pkgRoot + "/native/linux-arm64/vecnet-hub"
		}, ""},
		{"other package", ReasonNotNPMGlobal, func(h *fakeHost, _ *System) {
			h.files[pkgRoot+"/package.json"] = `{"name":"evil","version":"0.2.6"}`
		}, ""},
		{"no package.json", ReasonNotNPMGlobal, func(h *fakeHost, _ *System) { delete(h.files, pkgRoot+"/package.json") }, ""},
		{"version mismatch", ReasonVersion, func(*fakeHost, *System) {}, "0.2.5"},
		{"no npm", ReasonNoNPM, func(h *fakeHost, _ *System) { delete(h.files, prefix+"/bin/npm") }, ""},
		{"npm fails", ReasonNoNPM, func(h *fakeHost, _ *System) { h.runErr = errors.New("boom") }, ""},
		{"prefix elsewhere", ReasonPrefix, func(h *fakeHost, _ *System) { h.prefix = "/usr/local" }, ""},
		{"prefix empty", ReasonPrefix, func(h *fakeHost, _ *System) { h.prefix = "" }, ""},
		{"root owned modules", ReasonNotWritable, func(h *fakeHost, _ *System) { h.readonly[prefix+"/lib/node_modules"] = true }, ""},
		{"root owned bin", ReasonNotWritable, func(h *fakeHost, _ *System) { h.readonly[prefix+"/bin"] = true }, ""},
		{"executable error", ReasonDetectFailed, func(_ *fakeHost, s *System) {
			s.Executable = func() (string, error) { return "", errors.New("x") }
		}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newFakeHost("0.2.6")
			sys := h.system()
			tc.mutate(h, &sys)
			current := tc.current
			if current == "" {
				current = "0.2.6"
			}
			inst, reason := Detect(context.Background(), sys, current)
			if inst != nil || reason != tc.want {
				t.Fatalf("Detect = %+v, %q; want %q", inst, reason, tc.want)
			}
		})
	}
}

func TestNPMEnvDropsHubSecrets(t *testing.T) {
	h := newFakeHost("0.2.6")
	env := NPMEnv(h.system().Getenv, &Install{Prefix: prefix})
	joined := strings.Join(env, "\n")
	if strings.Contains(joined, "VECNET_") {
		t.Fatalf("hub environment leaked to npm: %v", env)
	}
	if !strings.Contains(joined, "HOME=/home/u") || !strings.Contains(joined, "PATH="+prefix+"/bin:/usr/bin:/bin") {
		t.Fatalf("env = %v", env)
	}
}

func TestVerifyInstalled(t *testing.T) {
	inst := &Install{Prefix: prefix, Root: pkgRoot, Exe: nativeExe}
	h := newFakeHost("0.2.7")
	if err := VerifyInstalled(h.system(), inst, "0.2.7"); err != nil {
		t.Fatal(err)
	}
	if err := VerifyInstalled(h.system(), inst, "0.2.8"); err == nil {
		t.Fatal("accepted the wrong version")
	}
	h.modes[nativeExe] = 0o644
	if err := VerifyInstalled(h.system(), inst, "0.2.7"); err == nil {
		t.Fatal("accepted a non-executable binary")
	}
	h.modes[nativeExe] = 0o755
	h.files[nativeExe] = "#!/bin/sh"
	if err := VerifyInstalled(h.system(), inst, "0.2.7"); err == nil {
		t.Fatal("accepted a non-ELF binary")
	}
	delete(h.files, nativeExe)
	if err := VerifyInstalled(h.system(), inst, "0.2.7"); err == nil {
		t.Fatal("accepted a missing binary")
	}
}

// DefaultSystem on the test machine must never claim one-click: the test
// binary is not the npm-installed vecnet-hub. This also keeps any real npm
// from running: Detect stops before `npm prefix -g`.
func TestDefaultSystemRefusesTestBinary(t *testing.T) {
	sys := DefaultSystem()
	ran := false
	sys.Run = func(context.Context, string, []string, []string) ([]byte, error) {
		ran = true
		return nil, errors.New("no")
	}
	if inst, _ := Detect(context.Background(), sys, "0.2.6"); inst != nil {
		t.Fatalf("Detect accepted the test binary: %+v", inst)
	}
	if ran {
		t.Fatal("Detect ran a command for a non-npm binary")
	}
}
