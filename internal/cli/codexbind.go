package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"time"
)

func unsafeCodexBindings(path, why string) error {
	return usageErr("unsafe codex bindings file " + path + ": " + why + "; make it yours with mode 0600 (and check its rows), or remove it")
}

// codexBindingsPath is shared with the launcher connector's default path.
func codexBindingsPath(getenv func(string) string) (string, error) {
	config, err := configPath(getenv)
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(config), "codex", "bindings.json"), nil
}

type codexBinding struct {
	Profile          string    `json:"profile"`
	As               string    `json:"as"`
	Hub              string    `json:"hub"`
	Caller           string    `json:"caller"`
	CodexHome        string    `json:"codex_home"`
	ThreadID         string    `json:"thread_id"`
	CreatedAt        time.Time `json:"created_at"`
	ExpiresAt        time.Time `json:"expires_at"`
	IdleDormantHours float64   `json:"idle_dormant_hours,omitempty"`
	Attended         bool      `json:"attended,omitempty"`
}
type codexBindingsFile struct {
	Version  int            `json:"version"`
	Bindings []codexBinding `json:"bindings"`
}

var codexThreadID = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

func codexBindDuration(o opts, name string, def time.Duration) (time.Duration, error) {
	if !o.has(name) {
		return def, nil
	}
	d, err := time.ParseDuration(o.str(name))
	if err != nil || d < time.Hour || d > 168*time.Hour {
		return 0, usageErr("--" + name + " must be a duration from 1h to 168h")
	}
	return d, nil
}

// codexBindCaller preserves hub errors so dispatch can verify and follow a
// relocation. Transport failures and unusable identities still exit 1 for bind.
func (a *app) codexBindCaller(c *client) (string, error) {
	ctx, cancel := context.WithTimeout(a.ctx, 2*time.Second)
	defer cancel()
	var me struct {
		Kind        string `json:"kind"`
		PeerID      string `json:"peer_id"`
		PrincipalID string `json:"principal_id"`
	}
	if err := c.do(ctx, http.MethodGet, "/v1/peers/me", nil, nil, &me); err != nil {
		var he *hubError
		if errors.As(err, &he) {
			return "", err
		}
		return "", usageErr("cannot resolve binding identity from hub")
	}
	var caller string
	switch me.Kind {
	case "peer":
		caller = me.PeerID
	case "principal":
		caller = me.PrincipalID
	}
	if !hookID.MatchString(caller) {
		return "", usageErr("cannot resolve binding identity from hub")
	}
	return caller, nil
}

// cmdCodex takes its clock at the command boundary so expiry and renewal are
// deterministic without changing the clock used by other CLI commands.
func (a *app) cmdCodex(args []string, nowTime func() time.Time) error {
	if len(args) == 0 {
		return usageErr("codex: expected bind, unbind or bindings")
	}
	var spec map[string]flagKind
	switch args[0] {
	case "deliver", "delivery-status", "delivery-refresh":
		return a.cmdCodexDelivery(args)
	case "bind":
		spec = map[string]flagKind{"thread": strFlag, "codex-home": strFlag, "ttl": strFlag, "idle-dormant": strFlag, "attended": boolFlag, "renew": boolFlag}
	case "unbind":
		spec = map[string]flagKind{"thread": strFlag}
	case "bindings":
		spec = map[string]flagKind{}
	default:
		return usageErr("codex: expected bind, unbind or bindings")
	}
	o, err := parseFlags("codex "+args[0], args[1:], spec)
	if err != nil {
		return err
	}
	if len(o.pos) != 0 {
		return usageErr("codex " + args[0] + ": unexpected arguments")
	}
	thread := o.str("thread")
	if (args[0] == "bind" || o.has("thread")) && !codexThreadID.MatchString(thread) {
		return usageErr("--thread must be a lowercase UUID")
	}
	now := nowTime().UTC()
	var binding codexBinding
	if args[0] == "bind" {
		ttl, err := codexBindDuration(o, "ttl", 24*time.Hour)
		if err != nil {
			return err
		}
		idle, err := codexBindDuration(o, "idle-dormant", 0)
		if err != nil {
			return err
		}
		home := o.str("codex-home")
		if !o.has("codex-home") {
			home = a.getenv("CODEX_HOME")
			if home == "" {
				if a.getenv("HOME") == "" {
					return usageErr("cannot locate codex-home: set CODEX_HOME or HOME")
				}
				home = filepath.Join(a.getenv("HOME"), ".codex")
			}
		}
		if !filepath.IsAbs(home) {
			return usageErr("--codex-home must be absolute")
		}
		home, err = filepath.EvalSymlinks(home)
		if err != nil {
			return fmt.Errorf("codex-home: %w", err)
		}
		home = filepath.Clean(home)
		info, err := os.Stat(home)
		if err != nil {
			return fmt.Errorf("codex-home: %w", err)
		}
		if !info.IsDir() {
			return usageErr("codex-home must be a directory")
		}
		config, err := configPath(a.getenv)
		if err != nil {
			return err
		}
		cfg, err := loadConfig(config)
		if err != nil {
			return err
		}
		if err := cfg.checkDefault(); err != nil {
			return err
		}
		name, p, err := cfg.profile(a.namedProfile())
		if err != nil {
			return err
		}
		if err := a.useStoredHub(name, p); err != nil {
			return err
		}
		// Bind is a non-hook command: record best-effort first-use trust before
		// looking up the authenticated caller, as ordinary hub commands do.
		a.ensurePin(name, p)
		c := newClient(p.Hub, p.Secret)
		if label := a.operatorLabel(p); label != nil {
			c.clientHeader = label.Header()
		}
		caller, err := a.codexBindCaller(c)
		if err != nil {
			return err
		}
		binding = codexBinding{Profile: name, As: a.as, Hub: normalizeHub(p.Hub), Caller: caller, CodexHome: home, ThreadID: thread, CreatedAt: now, ExpiresAt: now.Add(ttl), IdleDormantHours: idle.Hours(), Attended: o.has("attended")}
	}
	if args[0] == "unbind" && thread == "" && a.namedProfile() == "" {
		return usageErr("codex unbind: pass --net <profile> or --thread <uuid>")
	}
	path, err := codexBindingsPath(a.getenv)
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	if err := os.Chmod(dir, 0700); err != nil {
		return err
	}
	unlock, err := lockPath(a.ctx, path+".lock")
	if err != nil {
		return err
	}
	defer unlock()
	file := codexBindingsFile{Version: 1, Bindings: []codexBinding{}}
	raw, err := readCodexBindings(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err == nil {
		if err := json.Unmarshal(raw, &file); err != nil {
			return fmt.Errorf("read codex bindings: %w", err)
		}
		if file.Version != 1 {
			return usageErr("unsupported codex bindings version")
		}
		if file.Bindings == nil {
			file.Bindings = []codexBinding{}
		}
	}
	switch args[0] {
	case "bindings":
		type listedBinding struct {
			codexBinding
			State string `json:"state"`
		}
		list := make([]listedBinding, 0, len(file.Bindings))
		for _, b := range file.Bindings {
			state := "active"
			if !now.Before(b.ExpiresAt) {
				state = "expired"
			}
			list = append(list, listedBinding{b, state})
		}
		if a.json {
			return a.emitValue(list)
		}
		for _, b := range list {
			profile := b.Profile
			if b.As != "" {
				profile += "@" + b.As
			}
			fmt.Fprintf(a.stdout, "%s %s %s %s (expires %s)\n", profile, b.ThreadID, b.CodexHome, b.State, b.ExpiresAt.Format(time.RFC3339))
		}
		return nil
	case "unbind":
		kept := make([]codexBinding, 0, len(file.Bindings))
		for _, b := range file.Bindings {
			remove := b.Profile == a.namedProfile() && b.As == a.as
			if thread != "" {
				remove = b.ThreadID == thread
			}
			if !remove {
				kept = append(kept, b)
			}
		}
		file.Bindings = kept
	case "bind":
		existing := -1
		for i, b := range file.Bindings {
			sameProfile := b.Profile == binding.Profile && b.As == binding.As
			if sameProfile && b.ThreadID == thread {
				existing = i
				continue
			}
			if sameProfile {
				return usageErr("already bound: " + binding.Profile + " to Codex thread " + b.ThreadID)
			}
			if b.ThreadID == thread {
				return usageErr("already bound: Codex thread " + thread + " to " + b.Profile)
			}
		}
		if existing >= 0 {
			b := file.Bindings[existing]
			if o.has("renew") {
				if b.Hub != binding.Hub || b.Caller != binding.Caller {
					return usageErr("identity changed since bind; unbind and bind again")
				}
				b.ExpiresAt = binding.ExpiresAt
				if o.has("idle-dormant") {
					b.IdleDormantHours = binding.IdleDormantHours
				}
				if o.has("attended") {
					b.Attended = true
				}
				file.Bindings[existing] = b
			}
			binding = b
		} else {
			if o.has("renew") {
				return usageErr("no existing binding to renew")
			}
			limit, err := strconv.Atoi(a.getenv("VECNET_CODEX_MAX_BINDINGS"))
			if err != nil || limit < 1 || limit > 32 {
				limit = 8
			}
			// Stored homes were canonical at bind time but may since have become
			// symlinks (a moved home), so resolve both sides. A stored home that
			// no longer resolves keeps its slot: it might be this home.
			count, unresolved := 0, 0
			for _, b := range file.Bindings {
				if !now.Before(b.ExpiresAt) {
					continue
				}
				home, err := filepath.EvalSymlinks(b.CodexHome)
				switch {
				case err != nil:
					count++
					unresolved++
				case filepath.Clean(home) == binding.CodexHome:
					count++
				}
			}
			if count >= limit {
				msg := fmt.Sprintf("binding limit %d reached for %s; unbind one or raise VECNET_CODEX_MAX_BINDINGS", limit, binding.CodexHome)
				if unresolved > 0 {
					msg += fmt.Sprintf(" (%d counted binding(s) have a codex_home that no longer resolves; see `vecnet codex bindings`)", unresolved)
				}
				return usageErr(msg)
			}
			file.Bindings = append(file.Bindings, binding)
		}
	}
	encoded, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return err
	}
	if err := writeFileAtomic(path, encoded); err != nil {
		return err
	}
	if args[0] == "unbind" {
		fmt.Fprintln(a.stdout, "unbound Codex binding")
		return nil
	}
	hint := ""
	if !binding.Attended {
		hint = " (needs -a never or the auto reviewer; pass --attended if a human answers approvals)"
	}
	fmt.Fprintf(a.stdout, "bound %s to Codex thread %s; the launcher connector picks it up within 10 s%s\n", binding.Profile, binding.ThreadID, hint)
	return nil
}
