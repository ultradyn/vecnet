package cli

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type localLegacyRunner func(context.Context, string, ...string) ([]byte, error)

const localLegacyProbeLimit = 1 << 20

// verifyLegacyRetiredWith only observes state. An empty successful list-units
// response proves no matching units are loaded; command failure proves nothing.
// The caller must hold its migration lock and recheck the source before rename.
func verifyLegacyRetiredWith(parent context.Context, platform, home, procRoot string, uid int, run localLegacyRunner, compatible ...map[string][]byte) error {
	if platform != "linux" {
		return fmt.Errorf("legacy retirement verification unsupported on %s (requires Linux user systemd and /proc)", platform)
	}
	if run == nil || uid < 0 {
		return errors.New("legacy retirement verification unavailable: runner or UID missing")
	}
	ctx, cancel := context.WithTimeout(parent, 2*time.Second)
	defer cancel()
	b, err := run(ctx, "systemctl", "--user", "list-units", "--all", "--plain", "--no-legend", "--no-pager", "--", "vecnet-launcher.service", "vecnet-delivery@*.service")
	if err != nil {
		return fmt.Errorf("cannot verify legacy launcher/delivery services inactive: %w", err)
	}
	if err = ctx.Err(); err != nil {
		return fmt.Errorf("legacy service verification timed out: %w", err)
	}
	if len(b) > localLegacyProbeLimit {
		return errors.New("legacy service response exceeds verification bound")
	}
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) == 0 {
			continue
		}
		if len(f) < 4 || !(f[0] == "vecnet-launcher.service" || (strings.HasPrefix(f[0], "vecnet-delivery@") && strings.HasSuffix(f[0], ".service"))) {
			return errors.New("unexpected legacy service query response")
		}
		if f[1] != "loaded" && f[1] != "not-found" && f[1] != "masked" {
			return fmt.Errorf("unknown legacy service load state %q for %s", f[1], f[0])
		}
		switch f[2] {
		case "inactive", "failed":
			if (f[2] == "inactive" && f[3] != "dead") || (f[2] == "failed" && f[3] != "failed") {
				return fmt.Errorf("unknown legacy service substate %q for %s (%s)", f[3], f[0], f[2])
			}
		case "active", "activating", "deactivating", "reloading", "refreshing":
			return fmt.Errorf("legacy service %s is %s; stop or migrate it before retirement", f[0], f[2])
		default:
			return fmt.Errorf("unknown legacy service state %q for %s", f[2], f[0])
		}
	}
	d, err := os.Open(procRoot)
	if err != nil {
		return fmt.Errorf("cannot inspect legacy processes in %s: %w", procRoot, err)
	}
	defer d.Close()
	entries, err := d.ReadDir(65537)
	if err != nil && err != io.EOF {
		return fmt.Errorf("cannot enumerate legacy processes: %w", err)
	}
	if len(entries) > 65536 {
		return errors.New("legacy process enumeration exceeds verification bound")
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("legacy process verification timed out: %w", err)
		}
		pid := entry.Name()
		if _, err := strconv.ParseUint(pid, 10, 32); err != nil {
			continue
		}
		dir := filepath.Join(procRoot, pid)
		status, err := localLegacyRead(filepath.Join(dir, "status"))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return fmt.Errorf("cannot inspect legacy process %s UID: %w", pid, err)
		}
		same, err := localLegacyUID(status, uid)
		if err != nil {
			return fmt.Errorf("cannot inspect legacy process %s UID: %w", pid, err)
		}
		if !same {
			continue
		}
		cmd, err := localLegacyRead(filepath.Join(dir, "cmdline"))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return fmt.Errorf("cannot inspect legacy process %s command: %w", pid, err)
		}
		if len(cmd) > 0 && cmd[len(cmd)-1] != 0 {
			return fmt.Errorf("cannot inspect legacy process %s: malformed cmdline", pid)
		}
		args := strings.Split(strings.TrimSuffix(string(cmd), "\x00"), "\x00")
		for _, arg := range args {
			clean := filepath.ToSlash(filepath.Clean(arg))
			base := filepath.Base(clean)
			delivery := base == "delivery-main.ts" || base == "delivery-main.js" || strings.HasPrefix(base, "delivery-runner.")
			candidateMain := base == "main.ts" || base == "main.js"
			if !delivery && !candidateMain {
				continue
			}
			if !filepath.IsAbs(arg) {
				cwd, e := os.Readlink(filepath.Join(dir, "cwd"))
				if e != nil {
					return fmt.Errorf("cannot resolve legacy process %s cwd: %w", pid, e)
				}
				clean = filepath.Join(cwd, arg)
			}
			launcher := strings.HasSuffix(clean, "/launcher/src/main.ts") || strings.HasSuffix(clean, "/launcher/src/main.js")
			if !delivery && !launcher {
				continue
			}
			var current map[string][]byte
			if len(compatible) == 1 {
				current = compatible[0]
			}
			if delivery && localLegacyManagedEntry(home, clean, current) && localLegacyManagedProcess(dir, home, clean, args) {
				continue
			}
			return fmt.Errorf("legacy or unverified delivery/launcher process %s remains running; stop or migrate it before retirement", pid)
		}
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("legacy process verification timed out: %w", err)
	}
	return nil
}

func localLegacyRead(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, localLegacyProbeLimit+1))
	if err == nil && len(b) > localLegacyProbeLimit {
		err = errors.New("process data exceeds verification bound")
	}
	return b, err
}
func localLegacyUID(status []byte, uid int) (bool, error) {
	for _, line := range strings.Split(string(status), "\n") {
		if !strings.HasPrefix(line, "Uid:") {
			continue
		}
		f := strings.Fields(line)
		if len(f) != 5 {
			return false, errors.New("malformed UID record")
		}
		same := false
		for _, v := range f[1:] {
			n, e := strconv.ParseUint(v, 10, 32)
			if e != nil {
				return false, errors.New("malformed UID record")
			}
			same = same || n == uint64(uid)
		}
		return same, nil
	}
	return false, errors.New("missing UID record")
}

// A release path alone never establishes authority compatibility. The caller
// supplies the trusted current connector bundle; all its files must match.
func localLegacyManagedEntry(home, path string, compatible map[string][]byte) bool {
	if !filepath.IsAbs(home) || !filepath.IsAbs(path) {
		return false
	}
	rel, e := filepath.Rel(filepath.Join(home, ".local/share/vecnet-codex"), filepath.Clean(path))
	if e != nil {
		return false
	}
	parts := strings.Split(filepath.ToSlash(rel), "/")
	if len(parts) != 6 || (len(parts[0]) != 20 && len(parts[0]) != 64) {
		return false
	}
	if _, e = hex.DecodeString(parts[0]); e != nil {
		return false
	}
	if strings.Join(parts[1:], "/") != "connectors/launcher/src/codex/delivery-main.ts" || len(compatible) == 0 || len(compatible) > 1024 {
		return false
	}
	if _, ok := compatible["connectors/launcher/src/codex/delivery-main.ts"]; !ok {
		return false
	}
	release := filepath.Join(home, ".local/share/vecnet-codex", parts[0])
	total := 0
	for name, want := range compatible {
		if filepath.IsAbs(name) || filepath.Clean(name) != name || strings.HasPrefix(name, "..") {
			return false
		}
		total += len(want)
		if total > 16*localLegacyProbeLimit {
			return false
		}
		file := filepath.Join(release, name)
		resolved, err := filepath.EvalSymlinks(file)
		if err != nil || resolved != file {
			return false
		}
		st, err := os.Lstat(file)
		if err != nil || !st.Mode().IsRegular() || st.Mode().Perm()&0022 != 0 {
			return false
		}
		got, err := localLegacyRead(file)
		if err != nil || !bytes.Equal(got, want) {
			return false
		}
	}
	return true
}

// Verify that the inspected process is executing the standalone entry normally,
// without loaders or a different authority environment. Source identity is
// checked separately; an unreadable process environment never grants exemption.
func localLegacyManagedProcess(dir, home, entry string, args []string) bool {
	if len(args) < 2 || args[len(args)-1] != entry {
		return false
	}
	exe, err := os.Readlink(filepath.Join(dir, "exe"))
	if err != nil || !filepath.IsAbs(exe) {
		return false
	}
	if filepath.Base(exe) != "node" && filepath.Base(exe) != "nodejs" {
		return false
	}
	if filepath.Base(args[0]) != filepath.Base(exe) {
		return false
	}
	if filepath.IsAbs(args[0]) {
		resolved, e := filepath.EvalSymlinks(args[0])
		if e != nil || resolved != exe {
			return false
		}
	}
	for _, arg := range args[1 : len(args)-1] {
		if arg != "--disable-warning=ExperimentalWarning" && arg != "--experimental-strip-types" {
			return false
		}
	}
	raw, err := localLegacyRead(filepath.Join(dir, "environ"))
	if err != nil || len(raw) == 0 || raw[len(raw)-1] != 0 {
		return false
	}
	env := map[string]string{}
	for _, pair := range strings.Split(strings.TrimSuffix(string(raw), "\x00"), "\x00") {
		key, value, ok := strings.Cut(pair, "=")
		if !ok || key == "" {
			return false
		}
		if _, exists := env[key]; exists {
			return false
		}
		env[key] = value
	}
	release := filepath.Dir(filepath.Dir(filepath.Dir(filepath.Dir(filepath.Dir(entry)))))
	if env["HOME"] != home || env["VECNET_BIN"] != filepath.Join(release, "vecnet") {
		return false
	}
	for _, key := range []string{"NODE_OPTIONS", "NODE_PATH"} {
		if env[key] != "" {
			return false
		}
	}
	if override := env["VECNET_CODEX_BINDINGS"]; override != "" && override != filepath.Join(home, ".local/state/vecnet/codex/bindings.json") {
		return false
	}
	return true
}
