package cli

import (
	"bytes"
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
	return "", usageErr("cannot find launcher sources; pass --source /absolute/vecnet-checkout")
}

// Collect runtime sources, schemas and the unit, excluding tests and state.
func launcherAssets(source string) (map[string][]byte, string, error) {
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
	for _, required := range []string{"connectors/launcher/src/main.ts", "connectors/launcher/src/backends/codex-app-server/index.ts", "connectors/hub-client/src/client.ts"} {
		if _, ok := files[required]; !ok {
			return nil, "", usageErr("launcher source is missing " + required)
		}
	}
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

func launcherSnapshot(root string, files map[string][]byte) error {
	if st, err := os.Lstat(root); err == nil {
		if !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
			return usageErr("unsafe launcher snapshot directory")
		}
		for name, want := range files {
			path := filepath.Join(root, filepath.FromSlash(name))
			st, err := os.Lstat(path)
			if err != nil {
				return err
			}
			if !st.Mode().IsRegular() {
				return usageErr("launcher snapshot contains a non-regular file")
			}
			got, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if !bytes.Equal(got, want) {
				return usageErr("installed launcher snapshot was modified; remove that snapshot before reinstalling")
			}
		}
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(root), 0o700); err != nil {
		return err
	}
	tmp, err := os.MkdirTemp(filepath.Dir(root), ".snapshot-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	for name, data := range files {
		path := filepath.Join(tmp, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return err
		}
		if err := os.WriteFile(path, data, 0o600); err != nil {
			return err
		}
	}
	return os.Rename(tmp, root)
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
	if err = os.MkdirAll(filepath.Dir(p.Manifest), 0o700); err != nil {
		return err
	}
	unlock, err := lockPath(a.ctx, p.Manifest+".lock")
	if err != nil {
		return err
	}
	defer unlock()
	if existing, e := os.ReadFile(p.Unit); e == nil && !bytes.HasPrefix(existing, []byte("# Managed by vecnet launcher install.")) {
		return usageErr("launcher unit already exists and is not managed by vecnet; review it before installing")
	} else if e != nil && !errors.Is(e, os.ErrNotExist) {
		return e
	}
	if err = launcherSnapshot(snapshot, files); err != nil {
		return err
	}
	if err = writeFileAtomic(p.Unit, []byte(unit)); err != nil {
		return err
	}
	if err = launcherJSON(p.Manifest, m); err != nil {
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
	return a.emitValue(plan)
}
