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
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/XertroV/vecnet/connectors"
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
	candidates := []string{raw}
	if !strings.ContainsRune(raw, filepath.Separator) {
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
		if err == nil && st.Mode().IsRegular() && st.Mode().Perm()&0o111 != 0 {
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
func launcherOpenDir(path, managed string, create bool) (*os.Root, error) {
	if !filepath.IsAbs(path) {
		return nil, usageErr("launcher path must be absolute")
	}
	root, err := os.OpenRoot(string(filepath.Separator))
	if err != nil {
		return nil, err
	}
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

func launcherReadOwned(root *os.Root, name string, managed bool) ([]byte, error) {
	info, err := root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if err = launcherSafeInfo(info, false, managed); err != nil {
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
	return io.ReadAll(file)
}

func launcherValidateSnapshot(parent *os.Root, name string, files map[string][]byte) error {
	info, err := parent.Lstat(name)
	if err != nil {
		return err
	}
	if err = launcherSafeInfo(info, true, true); err != nil {
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
		if err = launcherSafeInfo(info, entry.IsDir(), true); err != nil {
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

func launcherSnapshot(parent *os.Root, name string, files map[string][]byte) error {
	if _, err := parent.Lstat(name); err == nil {
		return launcherValidateSnapshot(parent, name, files)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
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
		if err = root.WriteFile(filepath.FromSlash(path), data, 0o600); err != nil {
			return err
		}
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

func launcherAtomicWrite(root *os.Root, name string, data []byte) error {
	if _, err := launcherReadOwned(root, name, true); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	tmp := launcherTempName("." + name + "-")
	file, err := root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer root.Remove(tmp)
	_, err = file.Write(data)
	closeErr := file.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return root.Rename(tmp, name)
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
	if runtime.GOOS != "linux" {
		return usageErr("launcher install requires Linux and systemd --user")
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
	st, err := os.Stat(config.Hub.Credential)
	if err != nil || !st.Mode().IsRegular() || st.Mode()&0o7777 != 0o600 {
		return usageErr("launcher credential must be a regular file with mode 0600")
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
		searchPath = "/usr/local/bin:/usr/bin:/bin"
	}
	node := o.str("node")
	if node == "" {
		node = "node"
	}
	node, err = launcherExecutable(node, searchPath)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(a.ctx, 5*time.Second)
	defer cancel()
	version, err := exec.CommandContext(ctx, node, "--version").Output()
	if err != nil || !launcherNodeVersion(string(version)) {
		return usageErr("launcher requires Node 22.18 or newer; pass --node /absolute/path/to/node")
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
	unit, err := launcherUnit(files["connectors/launcher/systemd/vecnet-launcher-profile.service.in"], m, p.Config, vecnetConfig, a.getenv("HOME"), searchPath)
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
	if _, e := launcherReadOwned(manifestRoot, lockName, true); e != nil && !errors.Is(e, os.ErrNotExist) {
		return e
	}
	lock, err := manifestRoot.OpenFile(lockName, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return err
	}
	defer lock.Close()
	info, err := lock.Stat()
	if err != nil {
		return err
	}
	if err = launcherSafeInfo(info, false, true); err != nil {
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
	if existing, e := launcherReadOwned(unitRoot, filepath.Base(p.Unit), true); e == nil && !bytes.HasPrefix(existing, []byte("# Managed by vecnet launcher install.")) {
		return usageErr("launcher unit already exists and is not managed by vecnet; review it before installing")
	} else if e != nil && !errors.Is(e, os.ErrNotExist) {
		return e
	}
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
	if err = a.launcherRun("systemctl", "--user", "daemon-reload"); err != nil {
		return err
	}
	if o.has("enable") {
		if err = a.launcherRun("systemctl", "--user", "enable", filepath.Base(p.Unit)); err != nil {
			return err
		}
	}
	// Restart also starts a stopped unit and adopts the new snapshot on upgrade.
	if o.has("start") {
		if err = a.launcherRun("systemctl", "--user", "restart", filepath.Base(p.Unit)); err != nil {
			return err
		}
	}
	if err = a.registerLauncherInstallation(p, m, []byte(unit), vecnetConfig, searchPath, !o.has("vecnet-bin") && a.getenv("VECNET_BIN") == "", !o.has("source")); err != nil {
		return err
	}
	return a.emitValue(plan)
}
