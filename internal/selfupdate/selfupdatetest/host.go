// Package selfupdatetest is an in-memory host for tests of the hub's
// one-click update: a global npm install of vecnet that Detect and Redetect
// accept, a fake npm that "installs" by rewriting package.json, and the new
// binary's --version self-check. Nothing touches the real filesystem, runs
// a command or reaches npm.
package selfupdatetest

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/XertroV/vecnet/internal/selfupdate"
)

// Paths of the fake install.
const (
	Prefix = "/p"
	Root   = Prefix + "/lib/node_modules/vecnet"
	Exe    = Root + "/native/linux-x64/vecnet-hub"
	NPM    = Prefix + "/bin/npm"
)

// Host is a fake Linux x64 host with vecnet installed globally at Prefix.
type Host struct {
	mu       sync.Mutex
	files    map[string]string
	installs [][]string
}

// New returns a host whose installed package is version.
func New(version string) *Host {
	return &Host{files: map[string]string{
		Root + "/package.json": pkg(version),
		Exe:                    elfX8664,
		NPM:                    "#!/usr/bin/env node",
	}}
}

func pkg(version string) string { return `{"name":"vecnet","version":"` + version + `"}` }

// elfX8664 is the start of a little-endian ELF64 x86-64 executable.
var elfX8664 = string([]byte{0x7f, 'E', 'L', 'F', 2, 1, 1, 0, 0, 0, 0, 0, 0, 0, 0, 0, 2, 0, 62, 0})

// Install is the fake npm install: it records argv and rewrites
// package.json to the requested vecnet@<version>.
func (h *Host) Install(_ context.Context, name string, args, _ []string) ([]byte, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.installs = append(h.installs, append([]string{name}, args...))
	last := args[len(args)-1]
	v, ok := strings.CutPrefix(last, "vecnet@")
	if !ok {
		return nil, errors.New("unexpected package")
	}
	h.files[Root+"/package.json"] = pkg(v)
	return nil, nil
}

// Installs returns the recorded install argv lists.
func (h *Host) Installs() [][]string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([][]string(nil), h.installs...)
}

// Install returns the Install Detect finds on this host.
func Install() *selfupdate.Install {
	return &selfupdate.Install{Prefix: Prefix, Root: Root, Exe: Exe, NPM: NPM}
}

func (h *Host) read(p string) ([]byte, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if s, ok := h.files[p]; ok {
		return []byte(s), nil
	}
	return nil, os.ErrNotExist
}

// System is the host as a selfupdate.System. Its Run answers only
// `npm prefix -g` and the installed binary's --version.
func (h *Host) System() selfupdate.System {
	return selfupdate.System{
		GOOS: "linux", GOARCH: "amd64",
		Executable:   func() (string, error) { return Exe, nil },
		EvalSymlinks: func(p string) (string, error) { return path.Clean(p), nil },
		Getenv:       func(string) string { return "" },
		Stat: func(p string) (fs.FileInfo, error) {
			if _, err := h.read(p); err != nil {
				return nil, err
			}
			return info{path.Base(p)}, nil
		},
		ReadFile: h.read,
		ReadHead: func(p string, n int) ([]byte, error) {
			b, err := h.read(p)
			if len(b) > n {
				b = b[:n]
			}
			return b, err
		},
		LookPath: func(string) (string, error) { return "", errors.New("not found") },
		Writable: func(string) bool { return true },
		Run: func(_ context.Context, name string, args, _ []string) ([]byte, error) {
			switch {
			case name == NPM && strings.Join(args, " ") == "prefix -g":
				return []byte(Prefix + "\n"), nil
			case name == Exe && strings.Join(args, " ") == "--version":
				raw, err := h.read(Root + "/package.json")
				if err != nil {
					return nil, err
				}
				var p struct{ Version string }
				_ = json.Unmarshal(raw, &p)
				return []byte("vecnet-hub " + p.Version + "\n"), nil
			}
			return nil, errors.New("selfupdatetest: unexpected command " + name)
		},
	}
}

type info struct{ name string }

func (i info) Name() string       { return i.name }
func (i info) Size() int64        { return 0 }
func (i info) Mode() fs.FileMode  { return 0o755 }
func (i info) ModTime() time.Time { return time.Time{} }
func (i info) IsDir() bool        { return false }
func (i info) Sys() any           { return nil }
