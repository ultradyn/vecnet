package cli

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/XertroV/vecnet/connectors"
)

// verifyLegacyRetired is a read-only precondition for retiring an empty v1
// bindings file. It never stops services or signals processes.
func (a *app) verifyLegacyRetired(path string) error {
	ctx := a.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	run := func(ctx context.Context, name string, args ...string) ([]byte, error) {
		// Resolve with the app's environment, not the host process's PATH.
		executable := ""
		for _, dir := range filepath.SplitList(a.getenv("PATH")) {
			if !filepath.IsAbs(dir) {
				continue
			}
			p := filepath.Join(dir, name)
			st, e := os.Stat(p)
			if e == nil && st.Mode().IsRegular() && st.Mode().Perm()&0111 != 0 {
				executable = p
				break
			}
		}
		if executable == "" {
			return nil, fmt.Errorf("%s unavailable in configured PATH", name)
		}
		cmd := exec.CommandContext(ctx, executable, args...)
		cmd.WaitDelay = 100 * time.Millisecond
		for _, key := range []string{"HOME", "PATH", "XDG_RUNTIME_DIR", "DBUS_SESSION_BUS_ADDRESS", "XDG_CONFIG_HOME"} {
			if v := a.getenv(key); v != "" {
				cmd.Env = append(cmd.Env, key+"="+v)
			}
		}
		cmd.Env = append(cmd.Env, "LC_ALL=C", "SYSTEMD_COLORS=0")
		output := &localLegacyOutput{}
		cmd.Stdout = output
		cmd.Stderr = output
		err := cmd.Run()
		if output.overflow {
			return nil, fmt.Errorf("service query output exceeds verification bound")
		}
		return []byte(output.text.String()), err
	}
	compatible := make(map[string][]byte)

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
			if err == nil {
				compatible[source.prefix+name] = data
			}
			return err
		}); err != nil {
			return fmt.Errorf("cannot inspect current standalone connector bundle: %w", err)
		}
	}

	if err := verifyLegacyRetiredWith(ctx, runtime.GOOS, a.getenv("HOME"), "/proc", os.Getuid(), run, compatible); err != nil {
		return fmt.Errorf("cannot retire legacy bindings %s: %w", path, err)
	}
	return nil
}

type localLegacyOutput struct {
	text     strings.Builder
	overflow bool
}

func (b *localLegacyOutput) Write(p []byte) (int, error) {
	n := len(p)
	remaining := localLegacyProbeLimit - b.text.Len()
	if n > remaining {
		b.overflow = true
		p = p[:remaining]
	}
	_, _ = b.text.Write(p)
	return n, nil
}
