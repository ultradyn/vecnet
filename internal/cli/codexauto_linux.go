package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

func init() {
	codexNoticeWriteFD = func(file *os.File, message string) {
		conn, err := file.SyscallConn()
		if err != nil {
			return
		}
		_ = conn.Control(func(original uintptr) {
			var before, reopened unix.Stat_t
			if unix.Fstat(int(original), &before) != nil {
				return
			}
			kind := before.Mode & unix.S_IFMT
			if kind != unix.S_IFIFO {
				if kind != unix.S_IFCHR {
					return
				}
				if _, err := unix.IoctlGetTermios(int(original), unix.TCGETS); err != nil {
					return
				}
			}
			// Reopening creates a separate open-file description. F_SETFL on
			// a dup would alter the invoking parent's shared descriptor flags.
			fd, err := unix.Open(fmt.Sprintf("/proc/self/fd/%d", original), unix.O_WRONLY|unix.O_NONBLOCK|unix.O_CLOEXEC|unix.O_NOCTTY, 0)
			if err != nil {
				return
			}
			defer unix.Close(fd)
			if unix.Fstat(fd, &reopened) != nil || before.Dev != reopened.Dev || before.Ino != reopened.Ino || before.Rdev != reopened.Rdev || before.Mode != reopened.Mode {
				return
			}
			_, _ = unix.Write(fd, []byte(message)) // one best-effort write, including EAGAIN/partial output
		})
	}
}

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
	if a.codexNotice != nil {
		// Prepare the notice last: unrelated preflight must not commit its
		// debounce marker and then exhaust the foreground deadline before
		// the completed worker can return the text for display. Later lease
		// defers run first; cancelled preflight leaves eligibility untouched.
		defer func() {
			if a.ctx.Err() == nil {
				*a.codexNotice = a.codexNoticeMessage(p.Hub, time.Now())
			}
		}()
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
	// The launcher creates a private session/group. Refuse other invocation
	// shapes before starting work: only this live leader's own group is safe
	// to signal, never a PID read from disk or the caller's shared group.
	pid := os.Getpid()
	privateGroup := func() bool {
		sid, err := unix.Getsid(0)
		return err == nil && pid > 1 && sid == pid && syscall.Getpgrp() == pid
	}
	if !privateGroup() {
		return errCodexDelivery
	}
	// Context cancellation reaps temporary exec.CommandContext children on
	// the normal path. At the hard boundary, kill the verified private group
	// together so neither blocked Go writers nor an active probe can survive.
	// Detached delivery owners create their own session; managed owners are
	// started by their manager. Both keep their separate lifetime/lease.
	deadline := time.Now().Add(15 * time.Second)
	watchdog := time.AfterFunc(time.Until(deadline), func() {
		if privateGroup() {
			_ = syscall.Kill(-pid, syscall.SIGKILL)
		}
		os.Exit(1)
	})
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
	// Keep ownership in this helper until its writers stop, but never pass
	// the debounce description to probe/version/manager or delivery execs.
	if _, err := unix.FcntlInt(lease.Fd(), unix.F_SETFD, unix.FD_CLOEXEC); err != nil {
		return errCodexDelivery
	}
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
