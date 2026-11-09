package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

var launcherAlias = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

type launcherPaths struct{ Config, Credential, State, Data, Manifest, Unit, Name string }

func (a *app) launcherPaths() (launcherPaths, error) {
	name := a.namedProfile()
	if name == "" || a.as != "" {
		return launcherPaths{}, usageErr("launcher requires --net PROFILE and does not accept --as")
	}
	config, err := configPath(a.getenv)
	if err != nil {
		return launcherPaths{}, err
	}
	config, err = filepath.Abs(config)
	if err != nil {
		return launcherPaths{}, err
	}
	if a.getenv("HOME") == "" {
		return launcherPaths{}, usageErr("launcher requires HOME")
	}
	h := sha256.Sum256([]byte(config + "\x00" + name))
	id := "p-" + hex.EncodeToString(h[:8])
	dir, err := filepath.Abs(filepath.Join(filepath.Dir(config), "launcher", id))
	if err != nil {
		return launcherPaths{}, err
	}
	base := func(key, fallback string) string {
		if s := a.getenv(key); s != "" {
			return s
		}
		return filepath.Join(a.getenv("HOME"), fallback)
	}
	data, err := filepath.Abs(filepath.Join(base("XDG_DATA_HOME", ".local/share"), "vecnet-launcher"))
	if err != nil {
		return launcherPaths{}, err
	}
	state, err := filepath.Abs(filepath.Join(base("XDG_STATE_HOME", ".local/state"), "vecnet-launcher", id))
	if err != nil {
		return launcherPaths{}, err
	}
	unit := filepath.Join(base("XDG_CONFIG_HOME", ".config"), "systemd", "user", "vecnet-launcher-"+id+".service")
	unit, err = filepath.Abs(unit)
	return launcherPaths{filepath.Join(dir, "config.json"), filepath.Join(dir, "credential"), state, data, filepath.Join(dir, "install.json"), unit, name}, err
}

func (a *app) cmdLauncher(args []string) error {
	if len(args) == 0 {
		return usageErr("launcher: expected setup, install, status or check")
	}
	spec := map[string]flagKind{}
	switch args[0] {
	case "setup":
		spec = map[string]flagKind{"backend": strFlag, "acp-command": strFlag, "acp-arg": multiFlag, "acp-provider": strFlag, "acp-mode": strFlag, "project": strFlag, "repo": strFlag, "base-ref": strFlag, "codex-home": strFlag, "model": strFlag, "effort": strFlag, "write": boolFlag, "force": boolFlag}
	case "install":
		spec = map[string]flagKind{"source": strFlag, "node": strFlag, "vecnet-bin": strFlag, "write": boolFlag, "enable": boolFlag, "start": boolFlag}
	case "status", "check":
	default:
		return usageErr("launcher: expected setup, install, status or check")
	}
	o, err := parseFlags("launcher "+args[0], args[1:], spec)
	if err != nil {
		return err
	}
	if len(o.pos) > 0 {
		return usageErr("launcher: unexpected arguments")
	}
	p, err := a.launcherPaths()
	if err != nil {
		return err
	}
	switch args[0] {
	case "setup":
		return a.launcherSetup(p, o)
	case "install":
		return a.launcherInstall(p, o)
	case "status":
		return a.launcherStatus(p)
	default:
		var m launcherInstall
		if err := launcherReadJSON(p.Manifest, &m); err != nil {
			return usageErr("launcher is not installed; run launcher install --write")
		}
		return a.launcherRun(m.Node, "--disable-warning=ExperimentalWarning", m.Entry, "check", "--config", p.Config)
	}
}

func launcherReadJSON(path string, out any) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if json.Unmarshal(raw, out) != nil {
		return usageErr("cannot parse launcher file " + filepath.Base(path))
	}
	return nil
}

func launcherJSON(path string, value any) error {
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(path, append(raw, '\n'))
}

func launcherDir(path, label string) (string, error) {
	if !filepath.IsAbs(path) {
		return "", usageErr(label + " must be an absolute path")
	}
	p, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", fmt.Errorf("%s: %w", label, err)
	}
	s, err := os.Stat(p)
	if err != nil {
		return "", err
	}
	if !s.IsDir() {
		return "", usageErr(label + " must be a directory")
	}
	return p, nil
}

func (a *app) launcherSetup(p launcherPaths, o opts) error {
	if !launcherAlias.MatchString(o.str("project")) {
		return usageErr("--project must be an alias containing letters, numbers, dots, underscores or hyphens")
	}
	backendID := o.str("backend")
	if backendID == "" {
		backendID = "codex-app-server"
	}
	if backendID != "codex-app-server" && backendID != "acp" {
		return usageErr("--backend must be codex-app-server or acp")
	}
	var acpPreset map[string]any
	if backendID == "acp" {
		for _, flag := range []string{"codex-home", "model", "effort"} {
			if o.has(flag) {
				return usageErr("--" + flag + " applies only to codex-app-server")
			}
		}
		command := o.str("acp-command")
		provider, mode := o.str("acp-provider"), o.str("acp-mode")
		if provider == "" {
			provider = "claude"
		}
		if mode == "" {
			mode = "default"
		}
		if !filepath.IsAbs(command) || strings.ContainsAny(command, "\x00\r\n") || strings.Contains(strings.ToLower(provider+" "+filepath.Base(command)), "codex") || regexp.MustCompile(`(?i)bypass|full.access|accept.?edits|auto`).MatchString(mode) {
			return usageErr("ACP requires an absolute non-Codex executable and an interactive ask mode")
		}
		command, err := launcherExecutable(command, a.getenv("PATH"))
		if err != nil {
			return usageErr("--acp-command must name an installed executable")
		}
		if strings.Contains(strings.ToLower(filepath.Base(command)), "codex") {
			return usageErr("ACP must not use a Codex executable")
		}
		argv := append([]string{command}, o.all("acp-arg")...)
		for _, arg := range argv {
			if arg == "" || strings.ContainsRune(arg, 0) {
				return usageErr("ACP arguments must be nonempty and contain no NUL")
			}
		}
		acpPreset = map[string]any{"argv": argv, "provider": provider, "mode_id": mode}
	} else {
		for _, flag := range []string{"acp-command", "acp-arg", "acp-provider", "acp-mode"} {
			if o.has(flag) {
				return usageErr("--" + flag + " requires --backend acp")
			}
		}
	}
	repo, err := launcherDir(o.str("repo"), "--repo")
	if err != nil {
		return err
	}
	ref := o.str("base-ref")
	if ref == "" || strings.HasPrefix(ref, "-") {
		return usageErr("--base-ref is required")
	}
	cmd := exec.CommandContext(a.ctx, "git", "-C", repo, "rev-parse", "--verify", ref+"^{commit}")
	if err := cmd.Run(); err != nil {
		return usageErr("--base-ref must resolve to a commit in --repo")
	}
	home := o.str("codex-home")
	if home == "" {
		home = a.getenv("CODEX_HOME")
	}
	if home == "" {
		home = filepath.Join(a.getenv("HOME"), ".codex")
	}
	if backendID == "codex-app-server" {
		home, err = launcherDir(home, "--codex-home")
		if err != nil {
			return err
		}
	}
	path, err := configPath(a.getenv)
	if err != nil {
		return err
	}
	cfg, err := loadConfig(path)
	if err != nil {
		return err
	}
	_, profile, err := cfg.profile(p.Name)
	if err != nil {
		return err
	}
	if err = a.useStoredHub(p.Name, profile); err != nil {
		return err
	}
	// Resolve both authenticated identity and directory kind. No watch, ack,
	// enrolment, hook registration or profile rewrite occurs during setup.
	c := newClient(profile.Hub, profile.Secret)
	c.allowInsecureHTTP = profile.AllowInsecureHTTP || a.getenv(insecureEnv) == "1"
	ctx, cancel := context.WithTimeout(a.ctx, 10*time.Second)
	defer cancel()
	var me struct {
		Kind   string `json:"kind"`
		PeerID string `json:"peer_id"`
	}
	if err = c.do(ctx, http.MethodGet, "/v1/peers/me", nil, nil, &me); err != nil {
		return err
	}
	if me.Kind != "peer" || me.PeerID == "" || (profile.PeerID != "" && profile.PeerID != me.PeerID) {
		return usageErr("launcher setup requires an enrolled peer credential matching the profile")
	}
	var directory struct {
		Peers []struct {
			PeerID string `json:"peer_id"`
			Kind   string `json:"kind"`
		} `json:"peers"`
	}
	if err = c.do(ctx, http.MethodGet, "/v1/peers", nil, nil, &directory); err != nil {
		return err
	}
	isLauncher := false
	for _, peer := range directory.Peers {
		if peer.PeerID == me.PeerID && peer.Kind == "launcher" {
			isLauncher = true
		}
	}
	if !isLauncher {
		return usageErr("launcher setup requires a peer of kind launcher; enrol a dedicated launcher profile")
	}
	preset := map[string]string{}
	if effort := o.str("effort"); o.has("effort") {
		valid := false
		for _, allowed := range []string{"none", "minimal", "low", "medium", "high", "xhigh", "max", "ultra"} {
			if effort == allowed {
				valid = true
			}
		}
		if !valid {
			return usageErr("--effort must be none, minimal, low, medium, high, xhigh, max or ultra")
		}
	}
	for _, k := range []string{"model", "effort"} {
		if o.has(k) {
			if strings.TrimSpace(o.str(k)) == "" {
				return usageErr("--" + k + " must not be empty")
			}
			preset[k] = o.str(k)
		}
	}
	host, err := os.Hostname()
	if err != nil {
		return err
	}
	scratchAlias := "temporary"
	if o.str("project") == scratchAlias {
		scratchAlias = "vecnet-scratch"
	}
	backendConfig := map[string]any{"codex_home": home, "workspace_root": filepath.Join(p.Data, "workspaces"), "presets": map[string]any{"default": preset}}
	permissionModes := []string{"read-only", "ask", "auto", "full-access"}
	if backendID == "acp" {
		backendConfig = map[string]any{"workspace_root": filepath.Join(p.Data, "workspaces"), "presets": map[string]any{"default": acpPreset}}
		permissionModes = []string{"ask"}
	}
	config := map[string]any{
		"host": host, "hub": map[string]string{"url": profile.Hub, "credential_file": p.Credential}, "state_dir": p.State,
		"backends": []any{map[string]any{"id": backendID, "module": "./backends/" + backendID + "/index.ts", "config": backendConfig}},
		"projects": map[string]any{o.str("project"): map[string]string{"repo_root": repo, "base_ref": ref}, scratchAlias: map[string]any{"repo_root": p.Data, "base_ref": "HEAD", "scratch": true}},
		"policies": map[string]any{"default": map[string]any{"backends": []string{backendID}, "projects": []string{o.str("project"), scratchAlias}, "permission_modes": permissionModes}},
	}
	if !o.has("write") {
		return a.emitValue(map[string]any{"write": false, "profile": p.Name, "peer_id": me.PeerID, "config_path": p.Config, "credential_file": p.Credential, "config": config})
	}
	// Refuse implicit replacement, including after an incomplete earlier setup.
	for _, path := range []string{p.Config, p.Credential} {
		if _, e := os.Lstat(path); e == nil && !o.has("force") {
			return usageErr("launcher setup already exists; review it or pass --force --write to replace it")
		} else if e != nil && !errors.Is(e, os.ErrNotExist) {
			return e
		}
	}
	if err = os.MkdirAll(filepath.Dir(p.Config), 0o700); err != nil {
		return err
	}
	if err = os.MkdirAll(p.Data, 0o700); err != nil {
		return err
	}
	unlock, err := lockPath(a.ctx, p.Config+".lock")
	if err != nil {
		return err
	}
	defer unlock()
	// Recheck under the setup lock for concurrent setup commands.
	if !o.has("force") {
		for _, path := range []string{p.Config, p.Credential} {
			if _, e := os.Lstat(path); !errors.Is(e, os.ErrNotExist) {
				return usageErr("launcher setup already exists; use --force --write to replace it")
			}
		}
	}
	if err = writeFileAtomic(p.Credential, []byte(profile.Secret+"\n")); err != nil {
		return err
	}
	if err = launcherJSON(p.Config, config); err != nil {
		return err
	}
	return a.emitValue(map[string]any{"write": true, "profile": p.Name, "peer_id": me.PeerID, "config_path": p.Config, "credential_file": p.Credential, "next": "vecnet --net " + p.Name + " launcher install --source /absolute/vecnet-checkout --write"})
}

func (a *app) launcherRun(bin string, args ...string) error {
	if !filepath.IsAbs(bin) && a.getenv("PATH") != "" {
		resolved, err := launcherExecutable(bin, a.getenv("PATH"))
		if err != nil {
			return err
		}
		bin = resolved
	}
	cmd := exec.CommandContext(a.ctx, bin, args...)
	cmd.Stdin = a.stdin
	cmd.Stdout = a.stdout
	cmd.Stderr = a.stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("launcher command failed: %w", err)
	}
	return nil
}

func (a *app) launcherStatus(p launcherPaths) error {
	var config map[string]any
	configured := false
	if err := launcherReadJSON(p.Config, &config); err == nil {
		configured = true
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	var m launcherInstall
	installed := false
	if err := launcherReadJSON(p.Manifest, &m); err == nil {
		installed = true
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	active, enabled := "unknown", "unknown"
	if installed {
		for _, item := range []struct {
			arg   string
			value *string
		}{{"is-active", &active}, {"is-enabled", &enabled}} {
			ctx, cancel := context.WithTimeout(a.ctx, 3*time.Second)
			cmd := exec.CommandContext(ctx, "systemctl", "--user", item.arg, filepath.Base(p.Unit))
			out, err := cmd.Output()
			cancel()
			if s := strings.TrimSpace(string(out)); s != "" {
				*item.value = s
			} else if err != nil {
				*item.value = "unavailable"
			}
		}
	}
	return a.emitValue(map[string]any{"profile": p.Name, "configured": configured, "installed": installed, "config_path": p.Config, "unit": p.Unit, "active": active, "enabled": enabled, "runtime": m})
}
