package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

// Setup selects an existing profile and an explicitly identified conversation.
// The invoking environment is the only automatic source of a thread ID.
func (a *app) cmdCodexSetup(args []string, nowTime func() time.Time) error {
	o, err := parseFlags("codex setup", args, map[string]flagKind{
		"thread": strFlag, "codex-home": strFlag, "settings": strFlag, "bin": strFlag,
		"dry-run": boolFlag, "ttl": strFlag, "idle-dormant": strFlag, "attended": boolFlag, "renew": boolFlag, "expect-generation": strFlag,
	})
	if err != nil {
		return err
	}
	if len(o.pos) != 0 {
		return usageErr("codex setup: unexpected arguments")
	}
	profile, thread, home, config, err := a.codexSetupTarget(o)
	if err != nil {
		return err
	}
	legacy, err := a.codexLegacyBindings(config)
	if err != nil {
		return err
	}
	if len(legacy) > 0 {
		a.warnCodexLegacy(legacy)
		if !o.has("dry-run") {
			return usageErr("codex setup: unresolved legacy bindings; stop/replace the old connector and preserve/review its bindings before setup")
		}
	}
	for _, name := range []string{"ttl", "idle-dormant"} {
		if o.has(name) {
			if _, err := codexBindDuration(o, name, 0); err != nil {
				return err
			}
		}
	}
	installArgs := []string{"--client", "codex"}
	for _, name := range []string{"settings", "bin"} {
		if o.has(name) {
			installArgs = append(installArgs, "--"+name, o.str(name))
		}
	}
	if o.has("dry-run") {
		fmt.Fprintf(a.stdout, "would bind %s to Codex thread %s (%s); no binding or trust changes\n", profile, thread, home)
		return a.cmdHookInstall(installArgs)
	}
	// Validate the proposed migration before binding. The write performs its own
	// final checks under the settings lock; partial setup is always an error.
	preview := *a
	preview.stdout = io.Discard
	preview.stderr = io.Discard
	if err := preview.cmdHookInstall(installArgs); err != nil {
		return err
	}
	bindArgs := []string{"bind", "--thread", thread, "--codex-home", home}
	for _, name := range []string{"ttl", "idle-dormant", "expect-generation"} {
		if o.has(name) {
			bindArgs = append(bindArgs, "--"+name, o.str(name))
		}
	}
	for _, name := range []string{"attended", "renew"} {
		if o.has(name) {
			bindArgs = append(bindArgs, "--"+name)
		}
	}
	binder := *a
	binder.stdout = io.Discard
	if err := binder.cmdCodex(bindArgs, nowTime); err != nil {
		return err
	}
	if err := a.cmdHookInstall(append(installArgs, "--write")); err != nil {
		return fmt.Errorf("Codex binding saved but hook migration failed: %w", err)
	}
	return a.reportCodexSetup(profile, thread, home, config, nowTime())
}

// Status is read-only, including when no binding or status file exists.
func (a *app) cmdCodexStatus(args []string, nowTime func() time.Time) error {
	o, err := parseFlags("codex status", args, map[string]flagKind{"thread": strFlag, "codex-home": strFlag})
	if err != nil {
		return err
	}
	if len(o.pos) != 0 {
		return usageErr("codex status: unexpected arguments")
	}
	profile, thread, home, config, err := a.codexSetupTarget(o)
	if err != nil {
		return err
	}
	return a.reportCodexSetup(profile, thread, home, config, nowTime())
}

func (a *app) codexSetupTarget(o opts) (profile, thread, home, config string, err error) {
	if a.namedProfile() == "" {
		err = usageErr("codex setup/status: select an existing profile with --net P or VECNET_PROFILE")
		return
	}
	thread = o.str("thread")
	if !o.has("thread") {
		thread = a.getenv("CODEX_THREAD_ID")
	}
	if !codexThreadID.MatchString(thread) {
		err = usageErr("codex setup/status: use --thread UUID or CODEX_THREAD_ID; no recent-thread inference")
		return
	}
	config, err = configPath(a.getenv)
	if err != nil {
		return
	}
	var cfg Config
	cfg, err = loadConfig(config)
	if err != nil {
		return
	}
	profile, _, err = cfg.profile(a.namedProfile())
	if err != nil {
		return
	}
	config, err = canonicalCodexPath(config)
	if err != nil {
		return
	}
	home = o.str("codex-home")
	if !o.has("codex-home") {
		home = a.getenv("CODEX_HOME")
		if home == "" && a.getenv("HOME") != "" {
			home = filepath.Join(a.getenv("HOME"), ".codex")
		}
	}
	if !filepath.IsAbs(home) {
		err = usageErr("codex-home must be absolute; set CODEX_HOME or pass --codex-home")
		return
	}
	home, err = canonicalCodexPath(home)
	if err != nil {
		return
	}
	var info os.FileInfo
	info, err = os.Stat(home)
	if err == nil && !info.IsDir() {
		err = usageErr("codex-home must be a directory")
	}
	return
}

type codexSetupReport struct {
	NextStep       string   `json:"next_step,omitempty"`
	Enrolled       bool     `json:"enrolled"`
	Migration      string   `json:"migration"`
	LegacyBindings []string `json:"legacy_bindings,omitempty"`
	Profile        string   `json:"profile"`
	ThreadID       string   `json:"thread_id"`
	CodexHome      string   `json:"codex_home"`
	ConfigPath     string   `json:"config_path"`
	Generation     string   `json:"generation,omitempty"`
	Delivery       string   `json:"delivery"`
	Reason         string   `json:"reason"`
}

type codexConnectorStatus struct {
	Version   int       `json:"version"`
	UpdatedAt time.Time `json:"updated_at"`
	Bindings  []struct {
		ConfigPath string `json:"config_path"`
		Profile    string `json:"profile"`
		As         string `json:"as"`
		CodexHome  string `json:"codex_home"`
		ThreadID   string `json:"thread_id"`
		Generation string `json:"generation"`
		State      string `json:"state"`
		Liveness   struct {
			Live   bool   `json:"live"`
			Reason string `json:"reason"`
		} `json:"liveness"`
	} `json:"bindings"`
}

func (a *app) reportCodexSetup(profile, thread, home, config string, now time.Time) error {
	r := codexSetupReport{Profile: profile, ThreadID: thread, CodexHome: home, ConfigPath: config, Delivery: "unavailable", Migration: "legacy_check_clear"}
	r.Reason = "no matching active binding; run codex setup"
	path, err := codexBindingsPath(a.getenv)
	if err != nil {
		return err
	}
	raw, err := readPrivateCodexStatus(path)
	var file codexBindingsFile
	if err == nil {
		if _, err = parseJSONNode(raw); err == nil {
			err = json.Unmarshal(raw, &file)
		}
		if err != nil || file.Version != 1 {
			r.Reason = "invalid bindings file"
		} else {
			var found *codexBinding
			for i := range file.Bindings {
				b := &file.Bindings[i]
				if b.ConfigPath == config && b.Profile == profile && b.As == a.as {
					if found != nil {
						found = nil
						r.Reason = "conflicting bindings"
						break
					}
					found = b
				}
			}
			if found != nil && found.ConfigPath == config && found.ThreadID == thread && found.CodexHome == home && codexGeneration.MatchString(found.Generation) && now.Before(found.ExpiresAt) {
				r.Generation = found.Generation
				ownerOpts := opts{vals: map[string][]string{
					"thread": {thread}, "codex-home": {home}, "expect-generation": {found.Generation},
					"expect-hub": {found.Hub}, "expect-caller": {found.Caller},
				}}
				if _, err := a.codexOwner(ownerOpts, now); err != nil {
					r.Reason = "binding authority is invalid, conflicting or changed"
				} else if !a.codexSetupIdentityMatches(*found) {
					r.Reason = "profile identity changed or cannot be verified"
				} else {
					r.Enrolled = true
					r.Delivery, r.Reason = codexSetupReadiness(path+".status.json", *found, now)
					current, err := readPrivateCodexStatus(path)
					if err != nil || !bytes.Equal(raw, current) {
						r.Enrolled = false
						r.Delivery, r.Reason = "unavailable", "binding changed while reading status"
					}
				}
			}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		r.Reason = "bindings unavailable or unsafe"
	}
	legacy, err := a.codexLegacyBindings(config)
	if err != nil {
		return err
	}
	if len(legacy) > 0 {
		a.warnCodexLegacy(legacy)
		r.LegacyBindings = legacy
		r.Migration = "legacy_unresolved"
		r.Delivery = "unavailable"
		r.Reason = "legacy connector activity is unknown; enrollment does not establish completed migration"
	}
	if r.Delivery != "ready" {
		r.NextStep = "Enrollment automatically configures supported standalone delivery. Run vecnet repair to retry saved setup and upgrade managed software (Node >=22.18, Linux user systemd, same HOME). A supported app-server host must expose the exact live root; inspect the reason above and docs/connectors/codex-appserver.md, then rerun codex status. Review new or changed hooks in Codex /hooks when required by the host."
		if len(legacy) > 0 {
			r.NextStep = "Stop/replace all old connectors and preserve/review every legacy source before starting the standalone delivery service or current launcher; see the legacy_bindings paths and docs/connectors/codex-appserver.md."
		}
	}
	if a.json {
		return a.emitValue(r)
	}
	if r.Enrolled {
		fmt.Fprintln(a.stdout, "Codex enrollment: exact owner registered (enrollment alone is not delivery readiness)")
	}
	fmt.Fprintf(a.stdout, "Codex automatic delivery %s: %s\n", r.Delivery, r.Reason)
	if r.Delivery != "ready" {
		fmt.Fprintln(a.stdout, "A supported app-server host and running delivery connector are required; embedded/unsupported hosts have no automatic delivery. Manual inbox remains available.")
		fmt.Fprintln(a.stdout, r.NextStep)
	}
	return nil
}

func codexSetupReadiness(path string, b codexBinding, now time.Time) (string, string) {
	raw, err := readPrivateCodexStatus(path)
	if err != nil {
		return "unavailable", "connector status unavailable"
	}
	if _, err := parseJSONNode(raw); err != nil {
		return "unavailable", "invalid connector status"
	}
	var status codexConnectorStatus
	if json.Unmarshal(raw, &status) != nil || status.Version != 1 || status.UpdatedAt.IsZero() {
		return "unavailable", "invalid connector status"
	}
	age := now.Sub(status.UpdatedAt)
	if age < 0 || age > 30*time.Second {
		return "unavailable", "connector status is stale"
	}
	matches := 0
	delivery, reason := "unavailable", "connector has not reported this exact owner generation"
	for _, row := range status.Bindings {
		if row.ConfigPath != b.ConfigPath || row.Profile != b.Profile || row.As != b.As || row.CodexHome != b.CodexHome || row.ThreadID != b.ThreadID || row.Generation != b.Generation {
			continue
		}
		matches++
		switch {
		case row.State == "unsupported" || row.State == "refused_version":
			delivery, reason = "unsupported", "connector reports an unsupported host/version"
		case row.State == "ready" && row.Liveness.Live:
			delivery, reason = "ready", "fresh connector status confirms the exact owner is live"
		case !row.Liveness.Live:
			delivery, reason = "unavailable", "connector is not ready or the exact owner is not live"
			switch row.Liveness.Reason {
			case "expired", "archived", "not_loaded", "idle_stale", "approval_policy", "waiting_approval", "waiting_input", "over_limit", "identity_changed", "ownership_changed", "unverified_thread", "identity_unverified", "watch_stopped", "delivery_unresolved":
				reason = "exact owner is not live: " + row.Liveness.Reason
			}
		default:
			delivery, reason = "unavailable", "connector is not ready or the exact owner is not live"
		}
	}
	if matches > 1 {
		return "unavailable", "conflicting connector status rows"
	}
	return delivery, reason
}

// Enforce private regular files as well as the binding reader's inode-owner
// check. Replacing the path during the read invalidates this status sample.
func readPrivateCodexStatus(path string) ([]byte, error) {
	before, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !before.Mode().IsRegular() || before.Mode().Perm()&0077 != 0 || before.Size() > 1<<20 {
		return nil, errors.New("unsafe or oversized Codex status/authority file")
	}
	raw, err := readCodexBindings(path)
	if err != nil {
		return nil, err
	}
	after, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !os.SameFile(before, after) || after.Mode().Perm()&0077 != 0 {
		return nil, errors.New("Codex status/authority changed while reading")
	}
	return raw, nil
}

// A status sample cannot authorize a replacement profile's credentials. This
// read-only identity request deliberately avoids first-use pin/config writes.
func (a *app) codexSetupIdentityMatches(b codexBinding) bool {
	cfg, err := loadConfig(b.ConfigPath)
	if err != nil {
		return false
	}
	_, p, err := cfg.profile(b.Profile)
	if err != nil || normalizeHub(p.Hub) != b.Hub {
		return false
	}
	c := newClient(p.Hub, p.Secret)
	if label := a.operatorLabel(p); label != nil {
		c.clientHeader = label.Header()
	}
	caller, err := a.codexBindCaller(c)
	return err == nil && caller == b.Caller
}

// Previous releases derived their authority from the config directory. Probe
// both the configured spelling and canonical config (which may be a symlink),
// plus the conventional config homes. Do not read, import, or alter rows: even
// an empty file cannot prove that an old process has discarded its cached rows.
// This is deliberately a bounded check, not a search for arbitrary past configs.
func (a *app) codexLegacyBindings(config string) ([]string, error) {
	configured, err := configPath(a.getenv)
	if err != nil {
		return nil, err
	}
	authority, err := codexBindingsPath(a.getenv)
	if err != nil {
		return nil, err
	}
	candidates := []string{filepath.Join(filepath.Dir(configured), "codex", "bindings.json"), filepath.Join(filepath.Dir(config), "codex", "bindings.json")}
	if xdg := a.getenv("XDG_CONFIG_HOME"); xdg != "" {
		candidates = append(candidates, filepath.Join(xdg, "vecnet", "codex", "bindings.json"))
	}
	if home := a.getenv("HOME"); home != "" {
		candidates = append(candidates, filepath.Join(home, ".config", "vecnet", "codex", "bindings.json"))
	}
	seen := map[string]bool{}
	var legacy []string
	for _, candidate := range candidates {
		path, err := filepath.Abs(candidate)
		if err != nil {
			return nil, err
		}
		if path == authority || seen[path] {
			continue
		}
		seen[path] = true
		_, err = os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		// Even an alias to the new authority can still be used by an old
		// connector without its ownership guard; presence remains unresolved.
		legacy = append(legacy, path)
	}
	return legacy, nil
}

func (a *app) warnCodexLegacy(paths []string) {
	for _, path := range paths {
		fmt.Fprintf(a.stderr, "codex: unresolved legacy bindings at %s; old connector activity is unknown. Stop/replace every old connector using this source, preserve a backup and review/migrate all rows (including unrelated profiles), then archive the retired source before rerunning setup/status. No legacy rows were changed.\n", path)
	}
}
