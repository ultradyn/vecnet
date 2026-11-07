package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"
)

func (a *app) startCodexAuto(thread string) {
	name, p, home, config, err := a.codexAutoInput(thread, "")
	if err != nil || a.ctx.Err() != nil {
		return
	}
	path, err := codexBindingsPath(a.getenv)
	if err != nil {
		return
	}
	path, err = filepath.Abs(path)
	if err != nil {
		return
	}
	f, err := loadCodexBindings(path)
	if err != nil || f.disabled(name, a.as) {
		return
	}
	paths, err := a.codexDeliveryPaths(p.Hub)
	if err != nil || a.ctx.Err() != nil {
		return
	}
	key := sha256.Sum256([]byte(config + "\x00" + name + "\x00" + a.as + "\x00" + thread))
	lease, err := deliveryPrivateFile(filepath.Join(filepath.Dir(paths.Lease), "auto-"+hex.EncodeToString(key[:])+".lock"), true)
	if err != nil {
		return
	}
	defer lease.Close()
	locked, err := deliveryLock(lease)
	if err != nil || !locked || a.ctx.Err() != nil {
		return
	}
	binary := a.getenv("VECNET_BIN")
	if binary == "" {
		binary, err = os.Executable()
	}
	if err != nil || !filepath.IsAbs(binary) {
		return
	}
	args := []string{"--net", name}
	if a.as != "" {
		args = append(args, "--as", a.as)
	}
	args = append(args, "codex", "auto-register", "--thread", thread, "--codex-home", home)
	if a.inHook {
		args = append(args, "--hook")
	}
	cmd := exec.Command(binary, args...)
	// Explicitly retain the effective config path and no profile credential,
	// CODEX_THREAD_ID or unrelated parent variables.
	for _, key := range []string{"HOME", "PATH", "XDG_CONFIG_HOME", "XDG_RUNTIME_DIR", "XDG_DATA_HOME", "XDG_CACHE_HOME", "VECNET_CODEX_MAX_BINDINGS", "VECNET_CODEX_DELIVERY_ENTRY"} {
		if v := a.getenv(key); v != "" {
			cmd.Env = append(cmd.Env, key+"="+v)
		}
	}
	cmd.Env = append(cmd.Env, "VECNET_CONFIG="+config, "VECNET_BIN="+binary, "VECNET_CODEX_AUTO_DELIVERY=0")
	cmd.ExtraFiles = []*os.File{lease}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if a.ctx.Err() != nil {
		return
	}
	if cmd.Start() == nil {
		go cmd.Wait()
	}
}

func (a *app) cmdCodexAuto(args []string) error {
	// This internal command runs only in the detached helper process. A
	// context cannot interrupt blocked filesystem I/O, so enforce its lifetime
	// at the process boundary. Exit kills every potential writer together;
	// registry, binding and inherited debounce locks stay owned until then.
	deadline := time.Now().Add(15 * time.Second)
	watchdog := time.AfterFunc(time.Until(deadline), func() { os.Exit(1) })
	defer watchdog.Stop()
	o, err := parseFlags("codex auto-register", args, map[string]flagKind{"thread": strFlag, "codex-home": strFlag, "hook": boolFlag})
	if err != nil || len(o.pos) != 0 {
		return errCodexDelivery
	}
	// The parent owns this debounce flock; retaining its descriptor until all
	// bounded registration/ensure work finishes suppresses concurrent helpers.
	lease := os.NewFile(3, "auto-registration")
	if lease == nil {
		return errCodexDelivery
	}
	defer lease.Close()
	if st, err := lease.Stat(); err != nil || !st.Mode().IsRegular() {
		return errCodexDelivery
	}
	ctx, cancel := context.WithDeadline(a.ctx, deadline)
	defer cancel()
	worker := *a
	worker.ctx = ctx
	worker.inHook = o.has("hook")
	return worker.registerCodexAuto(o.str("thread"), o.str("codex-home"))
}

// Registry serialization precedes binding serialization for every participating
// writer. Admission includes all registered sources before any hub filtering.
func (a *app) codexAdmission(ctx context.Context, hub, config, path string, register bool) (func(), []codexBinding, error) {

	// Binding admission uses stable data state even when this manual invocation
	// has no runtime directory. Omitting runtime must not bypass a registered
	// source's global cap. Process ownership still requires codexDeliveryPaths.
	data := a.getenv("XDG_DATA_HOME")
	if data == "" && a.getenv("HOME") != "" {
		data = filepath.Join(a.getenv("HOME"), ".local", "share")
	}
	if data == "" {
		return nil, nil, errCodexDelivery
	}
	if !filepath.IsAbs(data) {
		return nil, nil, errCodexDelivery
	}
	normalized, slug, err := codexDeliveryHub(hub)
	if err != nil {
		return nil, nil, err
	}
	root := filepath.Join(data, "vecnet", "codex-delivery")
	state := filepath.Join(root, slug)
	for _, dir := range []string{filepath.Join(data, "vecnet"), root, state} {
		if err = deliveryPrivateDir(dir); err != nil {
			return nil, nil, err
		}
	}
	p := deliveryPaths{Root: root, State: state, Registry: filepath.Join(root, "registry.json"), Descriptor: filepath.Join(state, "descriptor.json"), Hub: normalized}
	if register {
		if err = deliveryRegister(ctx, p, config, path); err != nil {
			return nil, nil, err
		}
	}
	lock, err := deliveryLockWait(ctx, filepath.Join(p.Root, "registry.lock"))
	if err != nil {
		return nil, nil, err
	}
	release := func() { lock.Close() }
	reg := deliveryRegistry{Version: 1}
	if err = deliveryRead(p.Registry, &reg); errors.Is(err, os.ErrNotExist) {
		return release, nil, nil
	} else if err != nil || reg.Version != 1 {
		release()
		return nil, nil, errCodexDelivery
	}
	var other []codexBinding
	seen := map[string]bool{filepath.Clean(path): true}
	for _, source := range reg.Sources {
		if !filepath.IsAbs(source.Bindings) {
			release()
			return nil, nil, errCodexDelivery
		}
		sourcePath := filepath.Clean(source.Bindings)
		if seen[sourcePath] {
			continue
		}
		seen[sourcePath] = true
		f, err := loadCodexBindings(sourcePath)
		if err != nil {
			release()
			return nil, nil, err
		}
		other = append(other, f.Bindings...)
	}
	return release, other, nil
}
func (a *app) updateCodexAuto(config, path string, binding codexBinding, old *codexBinding, expected Profile) error {
	release, other, err := a.codexAdmission(a.ctx, binding.Hub, config, path, true)
	if err != nil {
		return err
	}
	defer release()
	if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	if err = os.Chmod(filepath.Dir(path), 0700); err != nil {
		return err
	}
	unlock, err := lockPath(a.ctx, path+".lock")
	if err != nil {
		return err
	}
	defer unlock()
	cfg, err := loadConfig(config)
	if err != nil {
		return err
	}
	_, profile, err := cfg.profile(binding.Profile)
	if err != nil || profile != expected {
		return errCodexDelivery
	}
	f, err := loadCodexBindings(path)
	if err != nil {
		return err
	}
	if f.disabled(binding.Profile, binding.As) {
		return errCodexDelivery
	}
	current := codexBindingFor(f, binding.Profile, binding.As)
	if (old == nil) != (current == nil) || (old != nil && *old != *current) {
		return errCodexDelivery
	}
	// Evaluate expiry under serialization, after any registry/binding wait.
	now := time.Now().UTC()

	for _, b := range other {
		if normalizeHub(b.Hub) == binding.Hub && (b.ThreadID == binding.ThreadID || (b.Profile == binding.Profile && b.As == binding.As)) {
			return errCodexDelivery
		}
	}
	for _, b := range f.Bindings {
		if b.Profile == binding.Profile && b.As == binding.As {
			continue
		}
		if b.ThreadID == binding.ThreadID && normalizeHub(b.Hub) == binding.Hub {
			return errCodexDelivery
		}
	}
	// Expiry releases a slot. Reviving the same thread is admission too.
	if current == nil || current.ThreadID != binding.ThreadID || !now.Before(current.ExpiresAt) {
		rows := append([]codexBinding{}, other...)
		for _, b := range f.Bindings {
			if b.Profile != binding.Profile || b.As != binding.As {
				rows = append(rows, b)
			}
		}
		if err = codexBindingLimit(a.getenv, rows, binding.CodexHome, now); err != nil {
			return err
		}
	}
	if current != nil && current.ThreadID == binding.ThreadID {
		if current.ExpiresAt.Before(binding.ExpiresAt) {
			current.ExpiresAt = binding.ExpiresAt
		}
		for i, b := range f.Bindings {
			if b.Profile == binding.Profile && b.As == binding.As {
				f.Bindings[i] = *current
			}
		}
	} else {
		if current == nil {
			f.Bindings = append(f.Bindings, binding)
		} else {
			for i, b := range f.Bindings {
				if b.Profile == binding.Profile && b.As == binding.As {
					f.Bindings[i] = binding
				}
			}
		}
	}
	raw, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(path, raw)
}
