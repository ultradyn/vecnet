package selfupdate

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// Install is a global npm installation of vecnet that this process runs
// from and that the current user can update with npm.
type Install struct {
	// Prefix is npm's global prefix (`npm prefix -g`): packages live under
	// Prefix/lib/node_modules, commands under Prefix/bin.
	Prefix string
	// Root is Prefix/lib/node_modules/vecnet.
	Root string
	// Exe is the running native binary, Root/native/<platform>/vecnet-hub,
	// symlinks resolved. A restart re-executes this path.
	Exe string
	// NPM is the absolute npm command to run.
	NPM string
}

// Reasons Detect gives for not offering one-click update. They are fixed
// codes, safe to log and to return to the operator.
const (
	ReasonUnsupportedOS = "unsupported_os"      // one-click update is Linux only
	ReasonContainer     = "container"           // running in a container or on Fly
	ReasonNotNPMGlobal  = "not_npm_global"      // not the npm-installed vecnet-hub
	ReasonVersion       = "version_mismatch"    // package.json disagrees with this build
	ReasonNoNPM         = "npm_not_found"       // no npm command
	ReasonPrefix        = "npm_prefix_mismatch" // npm would install somewhere else
	ReasonNotWritable   = "not_writable"        // the npm prefix is not ours to write
	ReasonDetectFailed  = "detect_failed"       // something else went wrong
)

// System is everything Detect reads from the host. DefaultSystem is the
// real host; tests supply fakes, so detection never depends on the machine
// running the tests.
type System struct {
	GOOS, GOARCH string
	Executable   func() (string, error)
	EvalSymlinks func(string) (string, error)
	Getenv       func(string) string
	Stat         func(string) (fs.FileInfo, error)
	ReadFile     func(string) ([]byte, error)
	LookPath     func(string) (string, error)
	// Writable reports whether the current user may create and remove
	// entries in a directory.
	Writable func(string) bool
	// ReadHead returns at most n leading bytes of a file.
	ReadHead func(path string, n int) ([]byte, error)
	// Run runs a command (npm prefix -g) and returns its standard output.
	Run Runner
}

// Runner runs name with args and the given environment and returns its
// combined output. ExecRunner is the real one; tests inject fakes so no test
// ever runs npm.
type Runner func(ctx context.Context, name string, args, env []string) ([]byte, error)

// ExecRunner runs a real command, without a shell.
func ExecRunner(ctx context.Context, name string, args, env []string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = env
	// Outside any project, so no project .npmrc can redirect npm.
	cmd.Dir = string(filepath.Separator)
	cmd.WaitDelay = 5 * time.Second
	return cmd.CombinedOutput()
}

// DefaultSystem is the running host.
func DefaultSystem() System {
	return System{
		GOOS: runtime.GOOS, GOARCH: runtime.GOARCH,
		Executable: os.Executable, EvalSymlinks: evalSymlinks, Getenv: os.Getenv,
		Stat: os.Stat, ReadFile: os.ReadFile, LookPath: exec.LookPath, Writable: writable,
		ReadHead: readHead, Run: ExecRunner,
	}
}

// containerEnv are variables only container platforms set.
var containerEnv = []string{"FLY_APP_NAME", "FLY_MACHINE_ID", "KUBERNETES_SERVICE_HOST", "container"}

// containerFiles are markers Docker and Podman leave in a container.
var containerFiles = []string{"/.dockerenv", "/run/.containerenv"}

// npmPlatform maps Go's GOARCH to the npm package's native/ directory names.
var npmPlatform = map[string]string{"amd64": "x64", "arm64": "arm64"}

// Detect decides whether this process is a global npm install of vecnet
// that it can update and restart itself. It is conservative: every check
// must pass, and any doubt is a reason not to offer one-click update.
//
//  1. Linux (re-executing in place needs exec; Windows keeps a running
//     binary locked).
//  2. Not a container: no container platform variables, no Docker/Podman
//     marker file, and no container runtime in PID 1's cgroup.
//  3. The running binary, symlinks resolved, is
//     <prefix>/lib/node_modules/vecnet/native/linux-<arch>/vecnet-hub.
//  4. That package.json names vecnet at exactly this build's version.
//  5. An npm command exists (<prefix>/bin/npm preferred, else PATH) and
//     `npm prefix -g` prints exactly <prefix>, so npm install -g replaces
//     this very package and not another copy.
//  6. The current user can write <prefix>/lib/node_modules and <prefix>/bin.
func Detect(ctx context.Context, sys System, current string) (*Install, string) {
	if sys.GOOS != "linux" {
		return nil, ReasonUnsupportedOS
	}
	for _, k := range containerEnv {
		if sys.Getenv(k) != "" {
			return nil, ReasonContainer
		}
	}
	for _, f := range containerFiles {
		if _, err := sys.Stat(f); err == nil {
			return nil, ReasonContainer
		}
	}
	if cg, err := sys.ReadFile("/proc/1/cgroup"); err == nil {
		for _, marker := range []string{"docker", "kubepods", "containerd", "libpod", "lxc"} {
			if bytes.Contains(cg, []byte(marker)) {
				return nil, ReasonContainer
			}
		}
	}
	arch, ok := npmPlatform[sys.GOARCH]
	if !ok {
		return nil, ReasonUnsupportedOS
	}
	exe, err := sys.Executable()
	if err != nil {
		return nil, ReasonDetectFailed
	}
	if exe, err = sys.EvalSymlinks(exe); err != nil || !path.IsAbs(exe) {
		return nil, ReasonDetectFailed
	}
	exe = path.Clean(exe)
	// exe = <prefix>/lib/node_modules/vecnet/native/linux-<arch>/vecnet-hub
	platformDir := path.Dir(exe)
	native := path.Dir(platformDir)
	root := path.Dir(native)
	modules := path.Dir(root)
	lib := path.Dir(modules)
	prefix := path.Dir(lib)
	if path.Base(exe) != "vecnet-hub" || path.Base(platformDir) != "linux-"+arch ||
		path.Base(native) != "native" || path.Base(root) != PackageName ||
		path.Base(modules) != "node_modules" || path.Base(lib) != "lib" || prefix == lib {
		return nil, ReasonNotNPMGlobal
	}
	if v, err := packageVersion(sys.ReadFile, root); err != nil {
		return nil, ReasonNotNPMGlobal
	} else if v != current {
		return nil, ReasonVersion
	}
	npm := path.Join(prefix, "bin", "npm")
	if fi, err := sys.Stat(npm); err != nil || fi.IsDir() {
		if npm, err = sys.LookPath("npm"); err != nil || !path.IsAbs(npm) {
			return nil, ReasonNoNPM
		}
	}
	inst := &Install{Prefix: prefix, Root: root, Exe: exe, NPM: npm}
	pctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	out, err := sys.Run(pctx, npm, []string{"prefix", "-g"}, NPMEnv(sys.Getenv, inst))
	if err != nil {
		return nil, ReasonNoNPM
	}
	got := strings.TrimSpace(string(out))
	if resolved, err := sys.EvalSymlinks(got); err == nil {
		got = resolved
	}
	wantPrefix := prefix
	if resolved, err := sys.EvalSymlinks(prefix); err == nil {
		wantPrefix = resolved
	}
	if got == "" || path.Clean(got) != path.Clean(wantPrefix) {
		return nil, ReasonPrefix
	}
	if !sys.Writable(modules) || !sys.Writable(path.Join(prefix, "bin")) {
		return nil, ReasonNotWritable
	}
	return inst, ""
}

// packageVersion reads <root>/package.json and returns its version when it
// names the vecnet package.
func packageVersion(read func(string) ([]byte, error), root string) (string, error) {
	raw, err := read(path.Join(root, "package.json"))
	if err != nil {
		return "", err
	}
	var pkg struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	}
	if err := json.Unmarshal(raw, &pkg); err != nil {
		return "", err
	}
	if pkg.Name != PackageName {
		return "", errors.New("not the vecnet package")
	}
	return pkg.Version, nil
}

// npmEnvKeys are passed to npm unchanged. Everything else in the hub's
// environment, including every VECNET_* value and provider credentials,
// stays out of npm's process.
var npmEnvKeys = []string{
	"HOME", "USER", "LOGNAME", "LANG", "LC_ALL", "TMPDIR", "XDG_CONFIG_HOME", "XDG_CACHE_HOME",
	"NODE_EXTRA_CA_CERTS", "SSL_CERT_FILE", "SSL_CERT_DIR",
	"HTTPS_PROXY", "HTTP_PROXY", "NO_PROXY", "https_proxy", "http_proxy", "no_proxy",
	"NPM_CONFIG_USERCONFIG", "NPM_CONFIG_GLOBALCONFIG", "NPM_CONFIG_CACHE",
	"npm_config_userconfig", "npm_config_globalconfig", "npm_config_cache",
}

// NPMEnv is the environment npm runs with: npmEnvKeys from getenv, and PATH
// with <prefix>/bin first, so npm finds the node it was installed with.
func NPMEnv(getenv func(string) string, inst *Install) []string {
	var env []string
	for _, k := range npmEnvKeys {
		if v := getenv(k); v != "" {
			env = append(env, k+"="+v)
		}
	}
	search := getenv("PATH")
	if search == "" {
		search = "/usr/local/bin:/usr/bin:/bin"
	}
	return append(env, "PATH="+path.Join(inst.Prefix, "bin")+":"+search)
}

// VerifyInstalled checks, after npm install, that inst now holds exactly
// version: package.json names vecnet at that version, and the native binary
// is an executable ELF file. Only then is a restart safe.
func VerifyInstalled(sys System, inst *Install, version string) error {
	v, err := packageVersion(sys.ReadFile, inst.Root)
	if err != nil {
		return errors.New("installed package.json is unreadable")
	}
	if v != version {
		return errors.New("installed package is not the expected version")
	}
	fi, err := sys.Stat(inst.Exe)
	if err != nil || !fi.Mode().IsRegular() || fi.Mode().Perm()&0o111 == 0 {
		return errors.New("installed vecnet-hub binary is missing or not executable")
	}
	head, err := sys.ReadHead(inst.Exe, 4)
	if err != nil || !bytes.HasPrefix(head, []byte("\x7fELF")) {
		return errors.New("installed vecnet-hub binary is not a Linux executable")
	}
	return nil
}

// evalSymlinks resolves symlinks and returns a slash-separated path: the
// layouts Detect matches are Linux paths (one-click update is Linux only).
func evalSymlinks(p string) (string, error) {
	r, err := filepath.EvalSymlinks(p)
	return filepath.ToSlash(r), err
}

// readHead reads at most n leading bytes of path.
func readHead(path string, n int) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	buf := make([]byte, n)
	k, err := io.ReadFull(f, buf)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) {
		return nil, err
	}
	return buf[:k], nil
}
