package selfupdate

import (
	"bytes"
	"context"
	"debug/elf"
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
	ReasonEphemeralPort = "ephemeral_port"      // listening on port 0: a restart would move it
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

// MaxCommandOutput caps what a Runner keeps of a command's output.
const MaxCommandOutput = 64 << 10

// ExecRunner runs a real command, without a shell, from the filesystem
// root (outside any project, so no project .npmrc applies). On Unix the
// command gets its own process group, and ctx ending kills the whole group,
// so npm's children die with it. Output past MaxCommandOutput is dropped.
func ExecRunner(ctx context.Context, name string, args, env []string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = env
	cmd.Dir = string(filepath.Separator)
	cmd.WaitDelay = 5 * time.Second
	ownProcessGroup(cmd)
	out := &cappedBuffer{max: MaxCommandOutput}
	cmd.Stdout, cmd.Stderr = out, out
	err := cmd.Run()
	return out.Bytes(), err
}

// cappedBuffer keeps the first max bytes written and discards the rest.
type cappedBuffer struct {
	bytes.Buffer
	max int
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	if room := b.max - b.Len(); room > 0 {
		b.Buffer.Write(p[:min(len(p), room)])
	}
	return len(p), nil
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

// containerFiles are markers Docker, Podman and systemd-nspawn leave in a
// container.
var containerFiles = []string{"/.dockerenv", "/run/.containerenv", "/run/systemd/container"}

// inContainer reports any container sign: platform variables, marker files,
// a container runtime in PID 1's cgroup, container= in PID 1's environment
// (when readable), or an overlay filesystem mounted at /.
func inContainer(sys System) bool {
	for _, k := range containerEnv {
		if sys.Getenv(k) != "" {
			return true
		}
	}
	for _, f := range containerFiles {
		if _, err := sys.Stat(f); err == nil {
			return true
		}
	}
	if cg, err := sys.ReadFile("/proc/1/cgroup"); err == nil {
		for _, marker := range []string{"docker", "kubepods", "containerd", "libpod", "lxc"} {
			if bytes.Contains(cg, []byte(marker)) {
				return true
			}
		}
	}
	if env, err := sys.ReadFile("/proc/1/environ"); err == nil {
		for _, kv := range bytes.Split(env, []byte{0}) {
			if bytes.HasPrefix(kv, []byte("container=")) {
				return true
			}
		}
	}
	if mi, err := sys.ReadFile("/proc/self/mountinfo"); err == nil && overlayRoot(mi) {
		return true
	}
	return false
}

// overlayRoot reports whether mountinfo mounts an overlay filesystem at /.
// Fields: id parent major:minor root mountpoint options [optional...] - fstype ...
func overlayRoot(mountinfo []byte) bool {
	for _, line := range strings.Split(string(mountinfo), "\n") {
		pre, post, ok := strings.Cut(line, " - ")
		if !ok {
			continue
		}
		f, g := strings.Fields(pre), strings.Fields(post)
		if len(f) >= 5 && f[4] == "/" && len(g) >= 1 && (g[0] == "overlay" || g[0] == "fuse-overlayfs") {
			return true
		}
	}
	return false
}

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
	return detect(ctx, sys, current, false)
}

// Redetect is Detect at the start of an update job: the same checks, read
// fresh from disk, except that a missing package.json is allowed (an earlier
// install may have stopped half way). A present one must still name exactly
// the running version, so a version installed by hand since the hub started
// is never replaced, and never downgraded.
func Redetect(ctx context.Context, sys System, current string) (*Install, string) {
	return detect(ctx, sys, current, true)
}

func detect(ctx context.Context, sys System, current string, allowMissing bool) (*Install, string) {
	if sys.GOOS != "linux" {
		return nil, ReasonUnsupportedOS
	}
	if inContainer(sys) {
		return nil, ReasonContainer
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
	if v, err := packageVersion(sys.ReadFile, root); errors.Is(err, fs.ErrNotExist) && allowMissing {
		// A half-finished install; the job reinstalls the package.
	} else if err != nil {
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
	head, err := sys.ReadHead(inst.Exe, 64)
	if err != nil || !bytes.HasPrefix(head, []byte("\x7fELF")) {
		return errors.New("installed vecnet-hub binary is not a Linux executable")
	}
	if m, ok := elfMachine(head); !ok || m != wantMachine[sys.GOARCH] {
		return errors.New("installed vecnet-hub binary is for another CPU architecture")
	}
	return nil
}

// wantMachine is the ELF e_machine for each GOARCH one-click supports.
var wantMachine = map[string]elf.Machine{"amd64": elf.EM_X86_64, "arm64": elf.EM_AARCH64}

// elfMachine reads e_machine from an ELF header (at least 20 bytes).
func elfMachine(head []byte) (elf.Machine, bool) {
	if len(head) < 20 {
		return 0, false
	}
	switch elf.Data(head[elf.EI_DATA]) {
	case elf.ELFDATA2LSB:
		return elf.Machine(uint16(head[18]) | uint16(head[19])<<8), true
	case elf.ELFDATA2MSB:
		return elf.Machine(uint16(head[18])<<8 | uint16(head[19])), true
	}
	return 0, false
}

// SelfCheckTimeout bounds the new binary's --version run.
const SelfCheckTimeout = 15 * time.Second

// SelfCheck runs the installed binary's `vecnet-hub --version` with an empty
// environment and requires it to print exactly "vecnet-hub <version>". It
// proves the new binary loads and runs on this host before the hub drains.
func SelfCheck(ctx context.Context, sys System, inst *Install, version string) error {
	ctx, cancel := context.WithTimeout(ctx, SelfCheckTimeout)
	defer cancel()
	out, err := sys.Run(ctx, inst.Exe, []string{"--version"}, []string{"PATH=/usr/bin:/bin"})
	if err != nil {
		return errors.New("installed vecnet-hub binary did not run")
	}
	if strings.TrimSpace(string(out)) != "vecnet-hub "+version {
		return errors.New("installed vecnet-hub binary reported an unexpected version")
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
