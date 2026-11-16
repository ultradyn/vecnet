// Package localruntime installs the private standalone Codex delivery runtime.
// It manages software only: delivery authority, ledgers and readiness belong to
// the connector and CLI, and are never changed by installation.
package localruntime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/XertroV/vecnet/connectors"
	"github.com/XertroV/vecnet/internal/platform"
)

// Options permits isolated installation and capability checks. A nil Getenv uses
// os.Getenv; a supplied Getenv is the sole source of environment configuration.
// Run defaults to an exec.CommandContext runner with a small explicit environment.
type Options struct {
	Home, Binary, Node, Version string
	Getenv                      func(string) string
	Run                         func(context.Context, string, ...string) ([]byte, error)
	// Preview performs capability, ownership and conflict checks without writes or activation.
	Preview bool
}

// Result describes successfully activated software, not delivery readiness.
// Release and Unit are absolute filesystem paths (Unit is a task definition on
// Windows). Changed reports activation, or a proposed change during Preview.
type Result struct {
	Version, Release, Unit string
	BinarySHA256           string
	Changed                bool
}

// ErrPending means installation metadata exists but activation has not completed.
// Read returns the previous applied result alongside this error, when available.
var ErrPending = errors.New("managed delivery activation pending; retry repair")

const unitName = "vecnet-codex-delivery.service"
const manifestName = "manifest.json"
const pendingName = "pending.json"

type manifest struct {
	Schema   int    `json:"schema"`
	Version  string `json:"version"`
	Release  string `json:"release"`
	Unit     string `json:"unit"`
	Node     string `json:"node"`
	UnitHash string `json:"unit_sha256"`
}

func (m manifest) result(changed bool) Result {
	return Result{Version: m.Version, Release: m.Release, Unit: m.Unit, Changed: changed}
}
func root(home string) string { return filepath.Join(home, ".local/share/vecnet-codex") }
func unitPath(home string) string {
	if runtime.GOOS == "windows" {
		return filepath.Join(root(home), "delivery-task.json")
	}
	return filepath.Join(home, ".config/systemd/user", unitName)
}
func sum(b []byte) string        { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
func unsafeString(s string) bool { return strings.ContainsAny(s, "\x00\r\n") }

func homePath(home string) (string, error) {
	if runtime.GOOS == "windows" {
		return windowsHome(home)
	}
	if home == "" || !filepath.IsAbs(home) || unsafeString(home) {
		return "", errors.New("runtime requires an absolute HOME")
	}
	resolved, e := filepath.EvalSymlinks(home)
	if e != nil {
		return "", fmt.Errorf("runtime HOME unavailable: %w", e)
	}
	st, e := os.Stat(resolved)
	if e != nil || !st.IsDir() {
		return "", errors.New("runtime HOME is not a directory")
	}
	if st.Mode().Perm()&0022 != 0 {
		return "", errors.New("runtime HOME must not be writable by others")
	}
	if e = owned(st); e != nil {
		return "", e
	}
	return resolved, nil
}

// Read inspects the last successfully activated manifest without installing,
// executing processes, acquiring locks or creating files. Missing is os.ErrNotExist.
func Read(home string) (Result, error) {
	if runtime.GOOS == "windows" {
		return readWindows(home)
	}
	h, e := homePath(home)
	if e != nil {
		return Result{}, e
	}
	m, e := readManifest(h, manifestName)
	if e != nil {
		return Result{}, e
	}
	r := m.result(false)
	if p, e := optionalManifest(h, pendingName); e != nil {
		return r, e
	} else if p != nil {
		return r, ErrPending
	}
	if e = checkPath(h, m.Release); e != nil {
		return r, e
	}
	st, e := os.Lstat(filepath.Join(m.Release, "vecnet"))
	if e != nil {
		return r, e
	}
	if !st.Mode().IsRegular() || st.Mode().Perm()&0077 != 0 || st.Mode().Perm()&0100 == 0 {
		return r, errors.New("unsafe or missing private runtime binary")
	}
	if e = owned(st); e != nil {
		return r, e
	}
	if st.Size() > 128<<20 {
		return r, errors.New("private runtime binary exceeds integrity check limit")
	}
	binary, e := os.ReadFile(filepath.Join(m.Release, "vecnet"))
	if e != nil {
		return r, e
	}
	files, e := distribution(binary)
	if e != nil {
		return r, e
	}
	if filepath.Base(m.Release) != releaseDigest(m.Version, files) {
		return r, errors.New("managed runtime content digest differs; repair required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if e = verifyRelease(ctx, m.Release, files); e != nil {
		return r, e
	}
	r.BinarySHA256 = sum(binary)
	b, e := readUnit(h)
	if e != nil {
		return r, e
	}
	if sum(b) != m.UnitHash {
		return r, errors.New("managed delivery unit missing or modified; repair required")
	}
	return r, nil
}

// Installed reports recognized applied or pending installation metadata. A failed
// first activation counts as installed so ordinary-use maintenance can retry it.
// Invalid metadata also returns true: maintenance must surface corruption rather
// than silently treating an existing installation as absent.
func Installed(home string) bool {
	h, e := homePath(home)
	if e != nil {
		return false
	}
	for _, name := range []string{manifestName, pendingName} {
		if _, e := os.Lstat(filepath.Join(root(h), name)); e == nil {
			return true
		}
	}
	return false
}

// Ensure stages immutable content-addressed software and reconciles its user
// systemd unit or Windows per-user logon task. Capability and conflict checks
// precede every installation write.
// The applied manifest advances only after activation succeeds; pending metadata
// authorizes retrying a unit written before an interrupted or failed activation.
func Ensure(ctx context.Context, o Options) (Result, error) {
	if e := ctx.Err(); e != nil {
		return Result{}, e
	}
	if runtime.GOOS == "windows" {
		return ensureWindows(ctx, o)
	}
	if runtime.GOOS != "linux" {
		return Result{}, errors.New("managed delivery activation requires Linux user systemd")
	}
	if o.Getenv == nil {
		o.Getenv = os.Getenv
	}
	if o.Home == "" {
		o.Home = platform.Home(o.Getenv)
	}
	home, e := homePath(o.Home)
	if e != nil {
		return Result{}, e
	}
	o.Home = home
	if xdg := o.Getenv("XDG_CONFIG_HOME"); xdg != "" && filepath.Clean(xdg) != filepath.Join(home, ".config") {
		return Result{}, errors.New("managed delivery requires XDG_CONFIG_HOME unset or HOME/.config; nondefault user systemd config directories are unsupported")
	}
	if o.Version == "" || len(o.Version) > 256 || unsafeString(o.Version) {
		return Result{}, errors.New("runtime version is required and must be a single line")
	}
	if o.Binary == "" {
		o.Binary, e = os.Executable()
		if e != nil {
			return Result{}, errors.New("cannot locate CLI executable")
		}
	}
	o.Binary, e = filepath.Abs(o.Binary)
	if e != nil || unsafeString(o.Binary) {
		return Result{}, errors.New("invalid CLI executable path")
	}
	binary, e := os.ReadFile(o.Binary)
	if e != nil {
		return Result{}, errors.New("cannot read CLI executable")
	}
	st, e := os.Stat(o.Binary)
	if e != nil || !st.Mode().IsRegular() || st.Mode().Perm()&0111 == 0 {
		return Result{}, errors.New("CLI binary must be an executable regular file")
	}
	if o.Node == "" {
		o.Node, e = findExecutable("node", o.Getenv("PATH"))
		if e != nil {
			return Result{}, errors.New("Node >=22.18 is required on the configured PATH")
		}
	}
	if !filepath.IsAbs(o.Node) || unsafeString(o.Node) {
		return Result{}, errors.New("Node must be an absolute executable path")
	}
	if o.Run == nil {
		o.Run = runner(o)
	}
	// Cap total automatic startup work, including lock waiting and child processes.
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if e = capabilities(ctx, o); e != nil {
		return Result{}, e
	}
	if e = conflicts(ctx, o); e != nil {
		return Result{}, e
	}
	if e = checkUnit(ctx, o); e != nil {
		return Result{}, e
	}
	if e = ctx.Err(); e != nil {
		return Result{}, e
	}
	if o.Preview {
		return previewLinux(ctx, o, binary)
	}
	if e = mkdirSafe(home, root(home), 0700); e != nil {
		return Result{}, e
	}
	unlock, e := lock(ctx, filepath.Join(root(home), "install.lock"))
	if e != nil {
		return Result{}, e
	}
	defer unlock()
	// Recheck after waiting: another installer or operator may have changed units.
	if e = conflicts(ctx, o); e != nil {
		return Result{}, e
	}
	state, e := serviceState(ctx, o)
	if e != nil {
		return Result{}, e
	}
	old, e := optionalManifest(home, manifestName)
	if e != nil {
		return Result{}, e
	}
	pending, e := optionalManifest(home, pendingName)
	if e != nil {
		return Result{}, e
	}
	current, e := readUnit(home)
	if e != nil {
		return Result{}, e
	}
	if e = acceptUnit(home, current, old, pending); e != nil {
		return Result{}, e
	}
	files, e := distribution(binary)
	if e != nil {
		return Result{}, e
	}
	release := filepath.Join(root(home), releaseDigest(o.Version, files))
	desired := manifest{Schema: 1, Version: o.Version, Release: release, Unit: unitPath(home), Node: o.Node}
	unit := makeUnit(home, o.Node, release, true)
	desired.UnitHash = sum(unit)
	repairAllowed := (old != nil && old.Release == release) || (pending != nil && pending.Release == release)
	repaired, e := stage(ctx, home, release, files, repairAllowed, func() error { return writeManifest(home, pendingName, desired) })
	if e != nil {
		return Result{}, e
	}
	if !repaired && old != nil && *old == desired && pending == nil && string(current) == string(unit) && state["ActiveState"] == "active" && state["UnitFileState"] == "enabled" {
		r := desired.result(false)
		r.BinarySHA256 = sum(binary)
		return r, nil
	}
	if e = writeManifest(home, pendingName, desired); e != nil {
		return Result{}, e
	}
	if string(current) != string(unit) {
		if len(current) > 0 {
			backup := filepath.Join(root(home), "unit-backups", sum(current)+".service")
			if e = mkdirSafe(home, filepath.Dir(backup), 0700); e != nil {
				return Result{}, e
			}
			if e = atomicWrite(backup, current, 0600); e != nil {
				return Result{}, e
			}
		}
		if e = mkdirSafe(home, filepath.Dir(desired.Unit), 0755); e != nil {
			return Result{}, e
		}
		if e = atomicWrite(desired.Unit, unit, 0600); e != nil {
			return Result{}, e
		}
	}
	commands := [][]string{}
	if string(current) != string(unit) || pending != nil {
		commands = append(commands, []string{"--user", "daemon-reload"})
	}
	if state["UnitFileState"] != "enabled" {
		commands = append(commands, []string{"--user", "enable", "--", unitName})
	}
	if repaired || pending != nil || old == nil || *old != desired || string(current) != string(unit) || state["ActiveState"] != "active" {
		commands = append(commands, []string{"--user", "restart", "--", unitName})
	}
	for _, args := range commands {
		if e = ctx.Err(); e != nil {
			return Result{}, e
		}
		if _, e = o.Run(ctx, "systemctl", args...); e != nil {
			return Result{}, fmt.Errorf("managed delivery systemctl %s failed; setup pending, retry repair", args[1])
		}
	}
	state, e = serviceState(ctx, o)
	if e != nil {
		return Result{}, e
	}
	if state["ActiveState"] != "active" {
		return Result{}, errors.New("managed delivery is not active after restart; setup pending, inspect user service journal and retry repair")
	}
	if e = ctx.Err(); e != nil {
		return Result{}, e
	}
	if e = writeManifest(home, manifestName, desired); e != nil {
		return Result{}, e
	}
	if e = os.Remove(filepath.Join(root(home), pendingName)); e != nil && !errors.Is(e, os.ErrNotExist) {
		return Result{}, e
	}
	if e = syncDir(root(home)); e != nil {
		return Result{}, e
	}
	r := desired.result(true)
	r.BinarySHA256 = sum(binary)
	return r, nil
}

func releaseDigest(version string, files map[string][]byte) string {
	digest := sha256.New()
	digest.Write([]byte(version + "\x00"))
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		fmt.Fprintf(digest, "%d:%s:%d:", len(n), n, len(files[n]))
		digest.Write(files[n])
	}
	return hex.EncodeToString(digest.Sum(nil))
}

func distribution(binary []byte) (map[string][]byte, error) {
	files := map[string][]byte{platform.Exe("vecnet"): binary}
	addPlatformAssets(files)
	for _, source := range []struct {
		files  fs.FS
		prefix string
	}{{connectors.Launcher, "connectors/launcher/"}, {connectors.HubClient, "connectors/"}} {
		e := fs.WalkDir(source.files, ".", func(p string, d fs.DirEntry, e error) error {
			if e != nil {
				return e
			}
			if d.IsDir() {
				return nil
			}
			b, e := fs.ReadFile(source.files, p)
			if e == nil {
				files[source.prefix+p] = b
			}
			return e
		})
		if e != nil {
			return nil, e
		}
	}
	return files, nil
}
func findExecutable(name, path string) (string, error) {
	for _, dir := range filepath.SplitList(path) {
		if !filepath.IsAbs(dir) {
			continue
		}
		p := filepath.Join(dir, name)
		st, e := os.Stat(p)
		if e == nil && st.Mode().IsRegular() && st.Mode().Perm()&0111 != 0 {
			return p, nil
		}
	}
	return "", errors.New("executable missing")
}
func runner(o Options) func(context.Context, string, ...string) ([]byte, error) {
	env := []string{"HOME=" + o.Home, "PATH=" + o.Getenv("PATH"), "VECNET_LOCAL_MAINTENANCE=0"}
	for _, k := range []string{"XDG_RUNTIME_DIR", "DBUS_SESSION_BUS_ADDRESS", "XDG_CONFIG_HOME", "XDG_DATA_HOME"} {
		if v := o.Getenv(k); v != "" {
			env = append(env, k+"="+v)
		}
	}
	return func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if !filepath.IsAbs(name) {
			var e error
			name, e = findExecutable(name, o.Getenv("PATH"))
			if e != nil {
				return nil, e
			}
		}
		c := exec.CommandContext(ctx, name, args...)
		c.Env = env
		return c.CombinedOutput()
	}
}
func nodeCapabilities(ctx context.Context, o Options) error {
	b, e := o.Run(ctx, o.Node, "--version")
	if e != nil {
		return errors.New("Node version probe failed; Node >=22.18 required")
	}
	parts := strings.Split(strings.TrimPrefix(strings.TrimSpace(string(b)), "v"), ".")
	if len(parts) != 3 {
		return errors.New("unrecognized Node version; Node >=22.18 required")
	}
	major, e1 := strconv.Atoi(parts[0])
	minor, e2 := strconv.Atoi(parts[1])
	_, e3 := strconv.Atoi(parts[2])
	if e1 != nil || e2 != nil || e3 != nil || major < 22 || (major == 22 && minor < 18) {
		return errors.New("Node >=22.18 required")
	}
	script := `import {stripTypeScriptTypes} from 'node:module'; import {DatabaseSync} from 'node:sqlite'; stripTypeScriptTypes('const n: number = 1;'); const db = new DatabaseSync(':memory:'); db.close();`
	if _, e = o.Run(ctx, o.Node, "--disable-warning=ExperimentalWarning", "--experimental-strip-types", "--input-type=module", "--eval", script); e != nil {
		return errors.New("Node requires built-in SQLite and TypeScript support")
	}
	return nil
}
func capabilities(ctx context.Context, o Options) error {
	if e := nodeCapabilities(ctx, o); e != nil {
		return e
	}
	if _, e := o.Run(ctx, "systemctl", "--user", "show-environment"); e != nil {
		return errors.New("Linux user systemd manager unavailable; setup pending")
	}
	return nil
}
func conflicts(ctx context.Context, o Options) error {
	b, e := o.Run(ctx, "systemctl", "--user", "list-units", "--all", "--plain", "--no-legend", "--no-pager", "--", "vecnet-launcher.service", "vecnet-launcher-p-*.service", "vecnet-delivery@*.service")
	if e != nil {
		return errors.New("cannot inspect launcher/legacy delivery services; setup pending")
	}
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) < 3 {
			continue
		}
		if f[0] != "vecnet-launcher.service" && !(strings.HasPrefix(f[0], "vecnet-launcher-p-") && strings.HasSuffix(f[0], ".service")) && !(strings.HasPrefix(f[0], "vecnet-delivery@") && strings.HasSuffix(f[0], ".service")) {
			return errors.New("unexpected launcher/legacy service query response")
		}
		if f[2] != "inactive" && f[2] != "failed" {
			return fmt.Errorf("conflicting running service %s; inspect and stop or migrate it before managed delivery activation", f[0])
		}
	}
	return nil
}
func serviceState(ctx context.Context, o Options) (map[string]string, error) {
	b, e := o.Run(ctx, "systemctl", "--user", "show", "--no-pager", "--property=LoadState,FragmentPath,DropInPaths,ActiveState,UnitFileState", "--", unitName)
	if e != nil {
		return nil, errors.New("cannot inspect managed delivery user unit; setup pending")
	}
	m := map[string]string{}
	for _, line := range strings.Split(string(b), "\n") {
		k, v, ok := strings.Cut(line, "=")
		if ok {
			m[k] = v
		}
	}
	if m["LoadState"] == "" || m["ActiveState"] == "" {
		return nil, errors.New("incomplete managed delivery unit state; setup pending")
	}
	if m["LoadState"] != "loaded" && m["LoadState"] != "not-found" {
		return nil, errors.New("incompatible or masked managed delivery unit")
	}
	if m["DropInPaths"] != "" {
		return nil, errors.New("custom managed delivery systemd drop-ins; refusing replacement")
	}
	if p := m["FragmentPath"]; p != "" && p != unitPath(o.Home) {
		return nil, errors.New("custom managed delivery unit outside managed HOME path; refusing replacement")
	}
	return m, nil
}

// Metadata validation is repeated under the installation lock. Inspecting
// manifest/unit pairs before locking can race another installer's atomic writes.
func checkUnit(ctx context.Context, o Options) error {
	if _, e := serviceState(ctx, o); e != nil {
		return e
	}
	_, e := readUnit(o.Home)
	return e
}

func readUnit(home string) ([]byte, error) {
	p := unitPath(home)
	if e := checkPath(home, filepath.Dir(p)); e != nil {
		return nil, e
	}
	st, e := os.Lstat(p)
	if errors.Is(e, os.ErrNotExist) {
		return nil, nil
	}
	if e != nil {
		return nil, e
	}
	if !st.Mode().IsRegular() || st.Size() == 0 {
		return nil, errors.New("custom, empty or symlinked managed delivery unit")
	}
	if e = owned(st); e != nil {
		return nil, e
	}
	return os.ReadFile(p)
}
func acceptUnit(home string, b []byte, old, pending *manifest) error {
	if len(b) == 0 {
		return nil
	}
	for _, m := range []*manifest{old, pending} {
		if m != nil && sum(b) == m.UnitHash {
			return nil
		}
	}
	if legacyUnit(home, b) {
		return nil
	}
	return errors.New("custom or modified managed delivery unit; refusing replacement")
}
func quote(s string, exec bool) string {
	s = strings.ReplaceAll(s, "\\", "\\\\")
	s = strings.ReplaceAll(s, "\"", "\\\"")
	s = strings.ReplaceAll(s, "%", "%%")
	if exec {
		s = strings.ReplaceAll(s, "$", "$$")
	}
	return "\"" + s + "\""
}
func makeUnit(home, node, release string, managed bool) []byte {
	lines := []string{}
	if managed {
		lines = append(lines, "# vecnet managed standalone delivery v1")
	}
	lines = append(lines, "[Unit]", "Description=vecnet isolated Codex delivery", "After=network-online.target", "[Service]", "Type=simple", "UMask=0077")
	for _, env := range []string{"HOME=" + home, "VECNET_BIN=" + filepath.Join(release, "vecnet"), "PATH=" + filepath.Dir(node) + ":/usr/local/bin:/usr/bin:/bin"} {
		lines = append(lines, "Environment="+quote(env, false))
	}
	if managed {
		lines = append(lines, "Environment="+quote("VECNET_LOCAL_MAINTENANCE=0", false))
	}
	lines = append(lines, "ExecStart="+quote(node, managed)+" --disable-warning=ExperimentalWarning --experimental-strip-types "+quote(filepath.Join(release, "connectors/launcher/src/codex/delivery-main.ts"), managed), "Restart=on-failure", "RestartSec=5", "TimeoutStopSec=45", "[Install]", "WantedBy=default.target", "")
	return []byte(strings.Join(lines, "\n"))
}

// Recognize only the exact standalone installer template, at its private release
// path. Arbitrary edits, extra settings and unrelated units remain custom.
func legacyUnit(home string, b []byte) bool {
	text := string(b)
	var bin, node string
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(line, `Environment="VECNET_BIN=`) {
			v := strings.TrimPrefix(line, "Environment=")
			v = strings.ReplaceAll(v, "%%", "%")
			x, e := strconv.Unquote(v)
			if e == nil {
				bin = strings.TrimPrefix(x, "VECNET_BIN=")
			}
		}
		if strings.HasPrefix(line, "ExecStart=\"") {
			rest := strings.TrimPrefix(line, "ExecStart=")
			for i := 1; i < len(rest); i++ {
				if rest[i] == '"' && rest[i-1] != '\\' {
					x, e := strconv.Unquote(strings.ReplaceAll(rest[:i+1], "%%", "%"))
					if e == nil {
						node = x
					}
					break
				}
			}
		}
	}
	release := filepath.Dir(bin)
	base := filepath.Base(release)
	_, e := hex.DecodeString(base)
	return node != "" && filepath.IsAbs(node) && len(base) == 20 && e == nil && filepath.Dir(release) == root(home) && filepath.Base(bin) == "vecnet" && text == string(makeUnit(home, node, release, false))
}

// Preview checks installation prerequisites and reports the proposed software
// paths without locks, directory creation, service mutation or authority changes.
func Preview(ctx context.Context, o Options) (Result, error) {
	o.Preview = true
	return Ensure(ctx, o)
}

func previewLinux(ctx context.Context, o Options, binary []byte) (Result, error) {
	state, e := serviceState(ctx, o)
	if e != nil {
		return Result{}, e
	}
	old, e := optionalManifest(o.Home, manifestName)
	if e != nil {
		return Result{}, e
	}
	pending, e := optionalManifest(o.Home, pendingName)
	if e != nil {
		return Result{}, e
	}
	current, e := readUnit(o.Home)
	if e != nil {
		return Result{}, e
	}
	if e = acceptUnit(o.Home, current, old, pending); e != nil {
		return Result{}, e
	}
	files, e := distribution(binary)
	if e != nil {
		return Result{}, e
	}
	release := filepath.Join(root(o.Home), releaseDigest(o.Version, files))
	desired := manifest{Schema: 1, Version: o.Version, Release: release, Unit: unitPath(o.Home), Node: o.Node, UnitHash: sum(makeUnit(o.Home, o.Node, release, true))}
	valid := verifyRelease(ctx, release, files) == nil
	r := desired.result(!valid || old == nil || *old != desired || pending != nil || sum(current) != desired.UnitHash || state["ActiveState"] != "active" || state["UnitFileState"] != "enabled")
	r.BinarySHA256 = sum(binary)
	return r, nil
}
