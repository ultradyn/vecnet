package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

// autoCodexAction does no network or runtime work. Hook dispatch calls this
// only after hookRun has consumed and validated its input exactly once.
func (a *app) autoCodexAction(cmd string, args []string) {
	switch cmd {
	case "hook":
		if !a.inHook {
			return
		}
	case "send", "reply", "react", "inbox", "ack", "watch", "threads", "thread", "peers", "groups", "presence", "avatar", "receipt",
		"principal", "peer", "invite", "route", "source", "subscriptions", "team", "dead-letters", "notify", "diag", "audit", "notice", "prefs", "tui":
	default:
		return
	}
	// Supervise local preflight too: a missing runtime or blocked local file
	// must not spend the ordinary invocation's budget on registration.
	worker := *a
	var notice string
	if !a.inHook && isTerminal(a.stdin) && isTerminal(a.stdout) {
		worker.codexNotice = &notice
	}
	worker.stdout, worker.stderr, worker.stdin = io.Discard, io.Discard, nil
	ctx, cancel := context.WithTimeout(a.ctx, 150*time.Millisecond)
	defer cancel()
	worker.ctx = ctx
	done := make(chan struct{})
	go func() {
		defer close(done)
		if worker.getenv("VECNET_CODEX_AUTO_DELIVERY") == "0" {
			return
		}
		thread := worker.getenv("CODEX_THREAD_ID")
		if !codexThreadID.MatchString(thread) || worker.namedProfile() == "" {
			return
		}
		worker.startCodexAuto(thread)
	}()
	select {
	case <-done:
		if notice != "" {
			fmt.Fprint(a.stderr, notice)
		}
	case <-ctx.Done():
	}
}

func (a *app) codexAutoInput(thread, home string) (string, Profile, string, string, error) {
	if !codexThreadID.MatchString(thread) || a.namedProfile() == "" || (a.as != "" && !hookID.MatchString(a.as)) {
		return "", Profile{}, "", "", errCodexDelivery
	}
	if home == "" {
		home = a.getenv("CODEX_HOME")
		if home == "" && a.getenv("HOME") != "" {
			home = filepath.Join(a.getenv("HOME"), ".codex")
		}
	}
	if !filepath.IsAbs(home) {
		return "", Profile{}, "", "", errCodexDelivery
	}
	home, err := filepath.EvalSymlinks(home)
	if err != nil {
		return "", Profile{}, "", "", err
	}
	st, err := os.Stat(home)
	if err != nil || !st.IsDir() {
		return "", Profile{}, "", "", errCodexDelivery
	}
	config, err := configPath(a.getenv)
	if err != nil {
		return "", Profile{}, "", "", err
	}
	config, err = filepath.Abs(config)
	if err != nil {
		return "", Profile{}, "", "", err
	}
	// Reject non-regular inputs at preflight. A later pathname replacement
	// or blocked regular-file read is still bounded by the helper's lifetime.
	if st, err := os.Stat(config); err != nil || !st.Mode().IsRegular() {
		return "", Profile{}, "", "", errCodexDelivery
	}
	cfg, err := loadConfig(config)
	if err != nil {
		return "", Profile{}, "", "", err
	}
	if err = cfg.checkDefault(); err != nil {
		return "", Profile{}, "", "", err
	}
	name, p, err := cfg.profile(a.namedProfile())
	if err != nil {
		return "", Profile{}, "", "", err
	}
	if err = a.useStoredHub(name, p); err != nil {
		return "", Profile{}, "", "", err
	}
	if _, _, err = codexDeliveryHub(p.Hub); err != nil {
		return "", Profile{}, "", "", err
	}
	return name, p, filepath.Clean(home), config, nil
}

func loadCodexBindings(path string) (codexBindingsFile, error) {
	f := codexBindingsFile{Version: 1, Bindings: []codexBinding{}}
	raw, err := readCodexBindings(path)
	if errors.Is(err, os.ErrNotExist) {
		return f, nil
	}
	if err != nil {
		return f, err
	}
	if err = json.Unmarshal(raw, &f); err != nil {
		return f, err
	}
	if f.Version != 1 {
		return f, errCodexDelivery
	}
	return f, nil
}
func codexBindingFor(f codexBindingsFile, profile, as string) *codexBinding {
	for _, b := range f.Bindings {
		if b.Profile == profile && b.As == as {
			return &b
		}
	}
	return nil
}

// Probe work happens without a binding lock. Its answer authorizes only the
// exact old row it observed; explicit unbind/takeover always wins a race.
func (a *app) registerCodexAuto(thread, home string) error {
	name, p, home, config, err := a.codexAutoInput(thread, home)
	if err != nil {
		return err
	}
	path, err := codexBindingsPath(a.getenv)
	if err != nil {
		return err
	}
	path, err = filepath.Abs(path)
	if err != nil {
		return err
	}
	before, err := loadCodexBindings(path)
	if err != nil {
		return err
	}
	if before.disabled(name, a.as) {
		return nil
	}
	old := codexBindingFor(before, name, a.as)
	hub := normalizeHub(p.Hub)
	if old != nil && (normalizeHub(old.Hub) != hub || old.Caller == "" || old.CodexHome != home) {
		return nil
	}
	// Use the same trust and authentication rules as explicit bind, with
	// the validated profile snapshot. Hooks never TOFU; credentials never
	// enter child arguments or environment.
	a.ensurePin(name, p)
	c := newClient(p.Hub, p.Secret)
	if label := a.operatorLabel(p); label != nil {
		c.clientHeader = label.Header()
	}
	caller, err := a.codexBindCaller(c)
	if err != nil {
		return err
	}
	if old != nil && old.Caller != caller {
		return nil
	}
	previous := ""
	if old != nil && old.ThreadID != thread && time.Now().Before(old.ExpiresAt) {
		previous = old.ThreadID
	}
	probe, err := a.probeCodexDelivery(a.ctx, home, thread, previous)
	if err != nil || !probe.Loaded {
		if err != nil {
			a.persistCodexReason(hub, err)
		}
		return errCodexDelivery
	}
	if previous != "" && probe.Previous != "gone" {
		reason := codexInactiveReason("ownership_ambiguous")
		if probe.Previous == "live" {
			reason = "other_thread_live"
		}
		a.persistCodexReason(hub, reason)
		return nil
	}
	binding := codexBinding{Profile: name, As: a.as, Hub: hub, Caller: caller, CodexHome: home, ThreadID: thread, CreatedAt: time.Now().UTC()}
	binding.ExpiresAt = binding.CreatedAt.Add(24 * time.Hour)
	if err = a.updateCodexAuto(config, path, binding, old, p); err != nil {
		return err
	}
	return a.ensureCodexDelivery(a.ctx, hub, config, path)
}
