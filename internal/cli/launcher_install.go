package cli

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/XertroV/vecnet/connectors"
	"github.com/XertroV/vecnet/internal/platform"
)

// launcherInstall records only public paths, never the launcher credential.
type launcherInstall struct {
	Node      string `json:"node"`
	Entry     string `json:"entry"`
	VecnetBin string `json:"vecnet_bin"`
	Source    string `json:"source"`
	Snapshot  string `json:"snapshot"`
}

func launcherExecutable(raw, searchPath string) (string, error) {
	if raw == "" {
		return "", usageErr("executable path is empty")
	}
	if runtime.GOOS == "windows" {
		if ext := filepath.Ext(raw); ext != "" && !strings.EqualFold(ext, ".exe") {
			return "", usageErr("launcher requires a native .exe executable, not a command wrapper")
		}
		if filepath.Ext(raw) == "" {
			raw = platform.Exe(raw)
		}
	}
	candidates := []string{raw}
	if !strings.ContainsAny(raw, `/\`) && filepath.VolumeName(raw) == "" {
		candidates = nil
		for _, dir := range filepath.SplitList(searchPath) {
			if dir != "" {
				candidates = append(candidates, filepath.Join(dir, raw))
			}
		}
	}
	for _, candidate := range candidates {
		p, err := filepath.Abs(candidate)
		if err != nil {
			continue
		}
		p, err = filepath.EvalSymlinks(p)
		if err != nil {
			continue
		}
		st, err := os.Stat(p)
		if err == nil && launcherExecutableInfo(p, st) {
			release, e := launcherPinExecutable(p)
			if e != nil {
				return "", fmt.Errorf("unsafe launcher executable %s: %w", p, e)
			}
			release()
			return p, nil
		}
	}
	return "", usageErr("cannot find executable " + filepath.Base(raw) + "; pass an absolute --node or --vecnet-bin path")
}

func launcherNodeVersion(raw string) bool {
	v := strings.Split(strings.TrimPrefix(strings.TrimSpace(raw), "v"), ".")
	if len(v) < 3 {
		return false
	}
	major, e1 := strconv.Atoi(v[0])
	minor, e2 := strconv.Atoi(v[1])
	_, e3 := strconv.Atoi(v[2])
	return e1 == nil && e2 == nil && e3 == nil && (major >= 24 || major == 23 && minor >= 6 || major == 22 && minor >= 18)
}

func launcherSource(raw string) (string, error) {
	if raw != "" {
		return launcherDir(raw, "--source")
	}
	// npm wrappers exec the native binary inside their package. Start from that
	// actual executable, so installed assets work outside any source checkout.
	if executable, err := os.Executable(); err == nil {
		if executable, err = filepath.EvalSymlinks(executable); err == nil {
			for dir := filepath.Dir(executable); ; dir = filepath.Dir(dir) {
				var metadata struct {
					Launcher struct {
						Entry string `json:"entry"`
					} `json:"launcher"`
				}
				if launcherReadJSON(filepath.Join(dir, "BUILD_INFO.json"), &metadata) == nil && metadata.Launcher.Entry == "connectors/launcher/src/main.ts" {
					return dir, nil
				}
				if filepath.Dir(dir) == dir {
					break
				}
			}
		}
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for dir := cwd; ; dir = filepath.Dir(dir) {
		if st, e := os.Stat(filepath.Join(dir, "connectors", "launcher", "src", "main.ts")); e == nil && st.Mode().IsRegular() {
			return dir, nil
		}
		if filepath.Dir(dir) == dir {
			break
		}
	}
	return "embedded", nil
}

// Collect runtime sources, schemas and the unit, excluding tests and state.
func launcherAssets(source string) (map[string][]byte, string, error) {
	// Source is explicitly selected (or belongs to the installed executable).
	// Copy it into a separately validated private namespace; never repair source
	// ownership or mode. Package digests below bind its complete runtime graph.
	if source == "" || source == "embedded" {
		files := map[string][]byte{}
		for _, source := range []struct {
			files  fs.FS
			prefix string
		}{{connectors.Launcher, "connectors/launcher/"}, {connectors.HubClient, "connectors/"}} {
			if err := fs.WalkDir(source.files, ".", func(name string, entry fs.DirEntry, err error) error {
				if err != nil {
					return err
				}
				if entry.IsDir() {
					return nil
				}
				data, err := fs.ReadFile(source.files, name)
				if err != nil {
					return err
				}
				files[source.prefix+name] = data
				return nil
			}); err != nil {
				return nil, "", err
			}
		}
		return launcherAssetDigest(files)
	}
	trusted, err := os.OpenRoot(source)
	if err != nil {
		return nil, "", err
	}
	defer trusted.Close()
	files := map[string][]byte{}
	for _, root := range []string{"connectors/launcher/src", "connectors/launcher/schema", "connectors/launcher/package.json", "connectors/hub-client/src", "connectors/hub-client/package.json", "connectors/launcher/systemd/vecnet-launcher-profile.service.in"} {
		err := filepath.WalkDir(filepath.Join(source, filepath.FromSlash(root)), func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				return nil
			}
			if !d.Type().IsRegular() {
				return usageErr("launcher runtime assets must be regular files: " + path)
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			rel, err := filepath.Rel(source, path)
			if err != nil {
				return err
			}
			files[filepath.ToSlash(rel)] = data
			return nil
		})
		if err != nil {
			return nil, "", err
		}
	}
	// Packaged assets carry a complete digest inventory. A checkout has no
	// BUILD_INFO; an installed package must match its recorded runtime exactly.
	if raw, err := trusted.ReadFile("BUILD_INFO.json"); err == nil {
		var metadata struct {
			Launcher struct {
				Entry  string            `json:"entry"`
				Assets map[string]string `json:"assets"`
			} `json:"launcher"`
		}
		if json.Unmarshal(raw, &metadata) != nil || metadata.Launcher.Entry != "connectors/launcher/src/main.ts" || len(metadata.Launcher.Assets) != len(files) {
			return nil, "", usageErr("incomplete packaged launcher asset inventory")
		}
		for name, data := range files {
			digest := sha256.Sum256(data)
			if metadata.Launcher.Assets[name] != hex.EncodeToString(digest[:]) {
				return nil, "", usageErr("packaged launcher asset digest mismatch: " + name)
			}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, "", err
	}
	for _, required := range []string{"connectors/launcher/src/main.ts", "connectors/launcher/src/backends/codex-app-server/index.ts", "connectors/hub-client/src/client.ts"} {
		if _, ok := files[required]; !ok {
			return nil, "", usageErr("launcher source is missing " + required)
		}
	}
	return launcherAssetDigest(files)
}

func launcherAssetDigest(files map[string][]byte) (map[string][]byte, string, error) {
	keys := make([]string, 0, len(files))
	for key := range files {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	h := sha256.New()
	for _, key := range keys {
		fmt.Fprintf(h, "%d:%s:%d:", len(key), key, len(files[key]))
		h.Write(files[key])
	}
	return files, hex.EncodeToString(h.Sum(nil)), nil
}

func launcherSafeInfo(info os.FileInfo, directory, managed bool) error {
	uid, links, ok := launcherFileIdentity(info)
	if !ok || (directory && !info.IsDir()) || (!directory && !info.Mode().IsRegular()) || info.Mode()&os.ModeSymlink != 0 {
		return usageErr("unsafe launcher path type")
	}
	if uid != os.Geteuid() && (managed || uid != 0) {
		return usageErr("unsafe launcher path owner")
	}
	// A root-owned sticky system temp directory can contain our private fixture
	// or HOME; writable directories anywhere inside a managed tree cannot.
	stickyAncestor := directory && !managed && uid == 0 && info.Mode()&os.ModeSticky != 0
	if info.Mode().Perm()&0o022 != 0 && !stickyAncestor {
		return usageErr("unsafe writable launcher path")
	}
	if !directory && links != 1 {
		return usageErr("unsafe linked launcher file")
	}
	return nil
}

// Pin each directory while traversing from /. No component is followed through
// a symlink or traversed below an untrusted writable parent. Existing unsafe
// paths are refused, never repaired with chmod/chown.
func launcherOpenDir(path, managed string, create bool) (*launcherRoot, error) {
	if !filepath.IsAbs(path) {
		return nil, usageErr("launcher path must be absolute")
	}
	if runtime.GOOS == "windows" {
		return launcherWindowsOpenDir(path, managed, create)
	}
	rawRoot, err := os.OpenRoot(string(filepath.Separator))
	if err != nil {
		return nil, err
	}
	root := &launcherRoot{Root: rawRoot}
	prefix := string(filepath.Separator)
	for _, part := range strings.Split(strings.TrimPrefix(filepath.Clean(path), prefix), string(filepath.Separator)) {
		if part == "" {
			continue
		}
		prefix = filepath.Join(prefix, part)
		info, e := root.Lstat(part)
		if errors.Is(e, os.ErrNotExist) && create {
			e = root.Mkdir(part, 0o700)
			if e == nil || errors.Is(e, os.ErrExist) {
				info, e = root.Lstat(part)
			}
		}
		strict := managed != "" && (prefix == managed || strings.HasPrefix(prefix, managed+string(filepath.Separator)))
		if e == nil {
			e = launcherSafeInfo(info, true, strict)
		}
		if e != nil {
			root.Close()
			return nil, usageErr(fmt.Sprintf("launcher directory %s: %v", prefix, e))
		}
		next, e := root.OpenRoot(part)
		if e != nil {
			root.Close()
			return nil, e
		}
		pinned, e := next.Stat(".")
		if e == nil && !os.SameFile(info, pinned) {
			e = usageErr("launcher directory changed during validation")
		}
		if e == nil {
			e = launcherSafeInfo(pinned, true, strict)
		}
		root.Close()
		root = next
		if e != nil {
			root.Close()
			return nil, usageErr(fmt.Sprintf("launcher pinned directory %s: %v", prefix, e))
		}
	}
	return root, nil
}

func launcherReadOwned(root *launcherRoot, name string, managed bool) ([]byte, error) {
	info, err := root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if err = launcherCheckInfo(root, name, info, false, managed); err != nil {
		return nil, err
	}
	file, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	pinned, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !os.SameFile(info, pinned) {
		return nil, usageErr("launcher file changed during validation")
	}
	if err = launcherCheckFile(file, false, managed); err != nil {
		return nil, err
	}
	return io.ReadAll(file)
}

func launcherValidateSnapshot(parent *launcherRoot, name string, files map[string][]byte) error {
	info, err := parent.Lstat(name)
	if err != nil {
		return err
	}
	if err = launcherCheckInfo(parent, name, info, true, true); err != nil {
		return err
	}
	root, err := parent.OpenRoot(name)
	if err != nil {
		return err
	}
	defer root.Close()
	seen := map[string]bool{}
	err = fs.WalkDir(root.FS(), ".", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := root.Lstat(path)
		if err != nil {
			return err
		}
		if err = launcherCheckInfo(root, path, info, entry.IsDir(), true); err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		want, ok := files[path]
		if !ok {
			return usageErr("unexpected file in launcher snapshot")
		}
		got, err := launcherReadOwned(root, path, true)
		if err != nil {
			return err
		}
		if !bytes.Equal(got, want) {
			return usageErr("installed launcher snapshot was modified; remove that snapshot before reinstalling")
		}
		seen[path] = true
		return nil
	})
	if err != nil {
		return err
	}
	if len(seen) != len(files) {
		return usageErr("incomplete launcher snapshot")
	}
	return nil
}

func launcherTempName(prefix string) string {
	var value [12]byte
	if _, err := rand.Read(value[:]); err != nil {
		panic(err)
	}
	return prefix + hex.EncodeToString(value[:])
}

func launcherSnapshot(parent *launcherRoot, name string, files map[string][]byte) error {
	if _, err := parent.Lstat(name); err == nil {
		return launcherValidateSnapshot(parent, name, files)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	for path := range files {
		if !fs.ValidPath(path) || !filepath.IsLocal(filepath.FromSlash(path)) || (runtime.GOOS == "windows" && strings.ContainsAny(path, `\:`)) {
			return usageErr("invalid launcher asset path")
		}
	}
	tmp := launcherTempName(".snapshot-")
	if err := parent.Mkdir(tmp, 0o700); err != nil {
		return err
	}
	defer parent.RemoveAll(tmp)
	root, err := parent.OpenRoot(tmp)
	if err != nil {
		return err
	}
	defer root.Close()
	for path, data := range files {
		if !filepath.IsLocal(filepath.FromSlash(path)) {
			return usageErr("invalid launcher asset path")
		}
		if err = root.MkdirAll(filepath.Dir(filepath.FromSlash(path)), 0o700); err != nil {
			return err
		}
		if err = launcherDurableWrite(root, filepath.FromSlash(path), data, 0o600); err != nil {
			return err
		}
	}
	// Windows pins directories against rename while validation/writes run.
	if err = root.Close(); err != nil {
		return err
	}
	if err = parent.Rename(tmp, name); err != nil {
		// Another profile may concurrently install the identical content hash.
		if _, exists := parent.Lstat(name); exists == nil {
			return launcherValidateSnapshot(parent, name, files)
		}
		return err
	}
	return launcherValidateSnapshot(parent, name, files)
}

func launcherAtomicWrite(root *launcherRoot, name string, data []byte) error {
	return launcherDurableWrite(root, name, data, 0o600)
}

// systemd expands percent specifiers everywhere and dollars in ExecStart.
func launcherSystemdQuote(s string, command bool) (string, error) {
	if strings.ContainsAny(s, "\x00\r\n") {
		return "", usageErr("launcher service paths and environment must not contain control characters")
	}
	s = strings.ReplaceAll(s, "%", "%%")
	if command {
		s = strings.ReplaceAll(s, "$", "$$")
	}
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return `"` + s + `"`, nil
}

func launcherUnit(template []byte, m launcherInstall, config, vecnetConfig, home, path string) (string, error) {
	if runtime.GOOS == "windows" {
		return launcherWindowsUnit(m, config, vecnetConfig, home, path)
	}
	unit := string(template)
	replacements := []string{}
	for key, value := range map[string]string{"NODE": m.Node, "ENTRY": m.Entry, "CONFIG": config, "ENV_VECNET_BIN": "VECNET_BIN=" + m.VecnetBin, "ENV_VECNET_CONFIG": "VECNET_CONFIG=" + vecnetConfig, "ENV_HOME": "HOME=" + home, "ENV_PATH": "PATH=" + path} {
		q, err := launcherSystemdQuote(value, key == "NODE" || key == "ENTRY" || key == "CONFIG")
		if err != nil {
			return "", err
		}
		token := "@" + key + "@"
		if !strings.Contains(unit, token) {
			return "", usageErr("launcher source has an incompatible service template; update the checkout")
		}
		replacements = append(replacements, token, q)
	}
	return strings.NewReplacer(replacements...).Replace(unit), nil
}

func (a *app) launcherInstall(p launcherPaths, o opts) error {
	traceStarted := time.Now()
	if runtime.GOOS != "linux" && runtime.GOOS != "windows" {
		return usageErr("launcher install requires Linux/systemd or Windows Task Scheduler")
	}
	if (o.has("enable") || o.has("start")) && !o.has("write") {
		return usageErr("--enable and --start require --write; omit them to preview installation")
	}
	var config struct {
		Hub struct {
			Credential string `json:"credential_file"`
		} `json:"hub"`
	}
	if err := launcherReadJSON(p.Config, &config); err != nil {
		return usageErr("launcher setup is missing or invalid; run launcher setup --write first")
	}
	credentialRoot, err := launcherOpenDir(filepath.Dir(config.Hub.Credential), filepath.Dir(config.Hub.Credential), false)
	if err != nil {
		return err
	}
	_, err = launcherManagedRead(credentialRoot, filepath.Base(config.Hub.Credential))
	credentialRoot.Close()
	if err != nil {
		return fmt.Errorf("launcher credential must be an owner-private regular file (0600 on Unix): %w", err)
	}
	source, err := launcherSource(o.str("source"))
	if err != nil {
		return err
	}
	files, hash, err := launcherAssets(source)
	if err != nil {
		return err
	}
	searchPath := a.getenv("PATH")
	if searchPath == "" {
		searchPath = launcherDefaultPath(a.getenv)
	}
	if searchPath == "" {
		return usageErr("launcher requires PATH or an absolute SystemRoot")
	}
	node := o.str("node")
	if node == "" {
		node = "node"
	}
	node, err = launcherExecutable(node, searchPath)
	if err != nil {
		return err
	}
	bin := o.str("vecnet-bin")
	if bin == "" {
		bin = a.getenv("VECNET_BIN")
	}
	if bin == "" {
		bin, err = os.Executable()
		if err != nil {
			return err
		}
	}
	bin, err = launcherExecutable(bin, searchPath)
	if err != nil {
		return err
	}
	releaseNode, err := launcherPinExecutable(node)
	if err != nil {
		return err
	}
	defer releaseNode()
	releaseBin, err := launcherPinExecutable(bin)
	if err != nil {
		return err
	}
	defer releaseBin()
	ctx, cancel := context.WithTimeout(a.ctx, 5*time.Second)
	defer cancel()
	version, err := launcherNodeOutput(ctx, node)
	if err != nil || !launcherNodeVersion(string(version)) {
		return usageErr("launcher requires Node 22.18 or newer; pass --node /absolute/path/to/node")
	}
	vecnetConfig, err := configPath(a.getenv)
	if err != nil {
		return err
	}
	vecnetConfig, err = filepath.Abs(vecnetConfig)
	if err != nil {
		return err
	}
	snapshot := filepath.Join(p.Data, "snapshots", hash)
	m := launcherInstall{node, filepath.Join(snapshot, "connectors", "launcher", "src", "main.ts"), bin, source, snapshot}
	unit, err := launcherUnit(files["connectors/launcher/systemd/vecnet-launcher-profile.service.in"], m, p.Config, vecnetConfig, platform.Home(a.getenv), searchPath)
	if err != nil {
		return err
	}
	plan := map[string]any{"write": o.has("write"), "profile": p.Name, "runtime": m, "config_path": p.Config, "unit_path": p.Unit, "unit": unit, "enable": o.has("enable"), "start": o.has("start")}
	if !o.has("write") {
		return a.emitValue(plan)
	}
	manifestRoot, err := launcherOpenDir(filepath.Dir(p.Manifest), filepath.Dir(p.Manifest), false)
	if err != nil {
		return err
	}
	defer manifestRoot.Close()
	unitRoot, err := launcherOpenDir(filepath.Dir(p.Unit), filepath.Dir(p.Unit), true)
	if err != nil {
		return err
	}
	defer unitRoot.Close()
	snapshots, err := launcherOpenDir(filepath.Dir(snapshot), p.Data, true)
	if err != nil {
		return err
	}
	defer snapshots.Close()
	lockName := filepath.Base(p.Manifest) + ".lock"
	if e := launcherValidateLock(manifestRoot, lockName, true); e != nil {
		return e
	}
	lock, err := manifestRoot.OpenFile(lockName, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err = launcherCheckFile(lock, false, true); err != nil {
		return err
	}
	for {
		locked, e := launcherTryLock(lock)
		if e != nil {
			return e
		}
		if locked {
			break
		}
		if !sleepCtx(a.ctx, 10*time.Millisecond) {
			return errStateBusy
		}
	}
	// Validate every existing publication target before replacing either one.
	// Equal unit bytes still constitute a publication when its inode changes.
	if _, e := launcherReadOwned(manifestRoot, filepath.Base(p.Manifest), true); e != nil && !errors.Is(e, os.ErrNotExist) {
		return e
	}
	if existing, e := launcherReadOwned(unitRoot, filepath.Base(p.Unit), true); e == nil && !launcherManagedUnit(existing) {
		return usageErr("launcher unit already exists and is not managed by vecnet; review it before installing")
	} else if e != nil && !errors.Is(e, os.ErrNotExist) {
		return e
	}
	fmt.Fprintf(os.Stderr, "R27_TRACE install prepare-manager elapsed=%s\n", time.Since(traceStarted))
	applyManager, err := a.launcherPrepareManagerInstall(p, []byte(unit), o.has("enable"), o.has("start"))
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "R27_TRACE install snapshot elapsed=%s\n", time.Since(traceStarted))
	if err = launcherSnapshot(snapshots, filepath.Base(snapshot), files); err != nil {
		return err
	}
	if err = launcherAtomicWrite(unitRoot, filepath.Base(p.Unit), []byte(unit)); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	if err = launcherAtomicWrite(manifestRoot, filepath.Base(p.Manifest), append(raw, '\n')); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "R27_TRACE install apply-manager elapsed=%s\n", time.Since(traceStarted))
	if err = applyManager(); err != nil {
		return err
	}

	fmt.Fprintf(os.Stderr, "R27_TRACE install inventory elapsed=%s\n", time.Since(traceStarted))
	if err = a.registerLauncherInstallation(p, m, []byte(unit), vecnetConfig, searchPath, !o.has("vecnet-bin") && a.getenv("VECNET_BIN") == "", !o.has("source")); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "R27_TRACE install done elapsed=%s\n", time.Since(traceStarted))
	return a.emitValue(plan)
}
