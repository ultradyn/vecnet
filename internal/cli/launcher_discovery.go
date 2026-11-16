package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/XertroV/vecnet/internal/platform"
)

// Presence is an inspection trigger, never ownership/adoption evidence. Include
// partial profile publications so deleting managed.json cannot hide a service
// from the repair caller's explicit unregistered-installation refusal.
func localWindowsProfileLauncherUnits(home string) ([]string, error) {
	return localWindowsProfileLauncherUnitsWith(home, os.Getenv)
}
func localWindowsProfileLauncherUnitsWith(home string, getenv func(string) string) ([]string, error) {
	if !filepath.IsAbs(home) {
		return nil, errors.New("launcher discovery requires an absolute HOME")
	}
	roots := []string{filepath.Join(home, ".config", "vecnet", "launcher")}
	if value := getenv("VECNET_CONFIG"); value != "" {
		path, err := filepath.Abs(value)
		if err != nil {
			return nil, err
		}
		roots = append(roots, filepath.Join(filepath.Dir(path), "launcher"))
	}
	if value := getenv("XDG_CONFIG_HOME"); value != "" {
		roots = append(roots, filepath.Join(value, "vecnet", "launcher"))
	}
	seenRoots, units := map[string]bool{}, map[string]bool{}
	for _, dir := range roots {
		if !filepath.IsAbs(dir) {
			return nil, errors.New("launcher discovery config root is not absolute")
		}
		dir = filepath.Clean(dir)
		key := strings.ToLower(dir)
		if seenRoots[key] {
			continue
		}
		seenRoots[key] = true
		entries, err := os.ReadDir(dir)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("cannot inspect Windows launcher profiles: %w", err)
		}
		if err = platform.CheckPath(dir, true, false); err != nil {
			return nil, fmt.Errorf("unsafe Windows launcher profile root: %w", err)
		}
		for _, entry := range entries {
			id := entry.Name()
			if !strings.HasPrefix(id, "p-") || !launcherTaskHex(strings.TrimPrefix(id, "p-")) {
				continue
			}
			profile := filepath.Join(dir, id)
			if entry.Type()&os.ModeSymlink != 0 || !entry.IsDir() {
				return nil, errors.New("unsafe Windows launcher profile directory")
			}
			if err = platform.CheckPath(profile, true, true); err != nil {
				return nil, fmt.Errorf("unsafe Windows launcher profile: %w", err)
			}
			task := filepath.Join(profile, "vecnet-launcher-"+id+".task.json")
			// Setup alone is not an installed service. An install/ownership/task
			// publication, even interrupted or malformed, must prevent false readiness.
			for _, path := range []string{task, filepath.Join(profile, "install.json"), filepath.Join(profile, "managed.json"), filepath.Join(profile, "repair-pending.json")} {
				if _, err = os.Lstat(path); err == nil {
					units[task] = true
					break
				} else if !errors.Is(err, os.ErrNotExist) {
					return nil, err
				}
			}
		}
	}
	out := make([]string, 0, len(units))
	for unit := range units {
		out = append(out, unit)
	}
	sort.Strings(out)
	return out, nil
}

// A background/explicit repair may also enumerate real scheduler objects. The
// CLI foreground must keep using bounded file presence, not start PowerShell.
func localWindowsProfileLauncherTasks(ctx context.Context) ([]string, error) {
	return launcherNativeProfileTasks(ctx)
}

func launcherDecodeTaskNames(raw []byte) ([]string, error) {
	var names []string
	if len(raw) > 1<<20 || json.Unmarshal(raw, &names) != nil {
		return nil, errors.New("uncertain Windows launcher task inventory")
	}
	for _, name := range names {
		if !strings.HasPrefix(name, "vecnet-launcher-p-") || !launcherTaskHex(strings.TrimPrefix(name, "vecnet-launcher-p-")) {
			return nil, errors.New("unsafe Windows launcher task inventory")
		}
	}
	sort.Strings(names)
	return names, nil
}
