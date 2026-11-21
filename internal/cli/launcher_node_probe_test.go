//go:build linux

package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// These real commands catch the install's misleading obsolete-Node diagnosis.
// They are negative controls, not stand-ins for successful cold Node admission.
func TestLauncherInstallNodeProbeFailures(t *testing.T) {
	for _, tc := range []struct {
		name, image, want string
		caller            string
		preview           bool
	}{
		{"cancel", "#!/bin/sh\necho v24.0.0\n", "probe canceled", "cancel", false},
		{"deadline", "#!/bin/sh\nexec /bin/sleep 20\n", "probe caller deadline exceeded", "deadline", false},
		{"timeout", "#!/bin/sh\nexec /bin/sleep 20\n", "probe timed out", "", false},
		{"execution", "invalid executable image", "probe could not execute", "", false},
		{"exit", "#!/bin/sh\necho PRIVATE_NODE_OUTPUT\necho PRIVATE_NODE_ERROR >&2\nexit 7\n", "probe exited with code 7", "", false},
		{"preview exit", "#!/bin/sh\necho PRIVATE_NODE_ERROR >&2\nexit 7\n", "probe exited with code 7", "", true},
		{"malformed", "#!/bin/sh\necho PRIVATE_NODE_OUTPUT\n", "invalid version output", "", false},
		{"unsupported", "#!/bin/sh\necho v20.0.0\n", "requires Node 22.18", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, args, managerLog := launcherInstalledFixture(t)
			node := filepath.Join(t.TempDir(), "node")
			if err := os.WriteFile(node, []byte(tc.image), 0o700); err != nil {
				t.Fatal(err)
			}
			// Snapshot the old runtime and publication files as well as task intent.
			before := launcherProbeState(t, f, managerLog)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tc.caller == "cancel" {
				cancel()
			} else if tc.caller == "deadline" {
				var deadlineCancel context.CancelFunc
				ctx, deadlineCancel = context.WithTimeout(ctx, 100*time.Millisecond)
				defer deadlineCancel()
			}
			var out, errs bytes.Buffer
			command := append([]string{"--net", "launcher/test", "launcher"}, args...)
			command = append(command, "--node", node)
			if !tc.preview {
				command = append(command, "--write", "--enable", "--start")
			}
			start := time.Now()
			code := RunContext(ctx, command, strings.NewReader(""), &out, &errs, func(k string) string { return f.env[k] })
			elapsed := time.Since(start)
			wantCLI := tc.want
			if tc.caller == "cancel" {
				wantCLI = "interrupted" // RunContext's established cancellation mapping.
			}
			if code == 0 || !strings.Contains(errs.String(), wantCLI) {
				t.Errorf("exit=%d elapsed=%s want %q; got %s", code, elapsed, wantCLI, errs.String())
			}
			if tc.caller != "" {
				a := &app{ctx: ctx, stdout: io.Discard, getenv: func(k string) string { return f.env[k] }}
				err := a.launcherInstall(f.paths, opts{vals: map[string][]string{
					"source": {launcherTestSource(t)}, "node": {node},
					"vecnet-bin": {filepath.Join(f.env["PATH"], "vecnet")}, "write": {},
				}})
				if err == nil || !errors.Is(err, ctx.Err()) || !strings.Contains(err.Error(), tc.want) {
					t.Errorf("install must preserve caller context and classify %q: %v", tc.want, err)
				}
			}
			if tc.caller != "" && elapsed > 2*time.Second {
				t.Errorf("caller context did not bound probe: %s", elapsed)
			}
			if elapsed > 7*time.Second {
				t.Errorf("local probe did not bound command: %s", elapsed)
			}
			for _, secret := range []string{"PRIVATE_NODE_OUTPUT", "PRIVATE_NODE_ERROR", "vn_peer_LAUNCHER_TEST"} {
				if strings.Contains(out.String()+errs.String(), secret) {
					t.Error("probe leaked command output or credential")
				}
			}
			if after := launcherProbeState(t, f, managerLog); !equalLauncherProbeState(before, after) {
				t.Error("failed admission changed installed files or service intent")
			}
		})
	}
}

func launcherProbeState(t *testing.T, f launcherFixture, managerLog string) map[string][sha256.Size]byte {
	t.Helper()
	state := map[string][sha256.Size]byte{}
	for _, root := range []string{filepath.Dir(f.paths.Config), f.paths.Data, filepath.Dir(f.paths.Unit)} {
		if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if !entry.IsDir() {
				data, err := os.ReadFile(path)
				if err != nil {
					return err
				}
				state[path] = sha256.Sum256(data)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	data, err := os.ReadFile(managerLog)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	state[managerLog] = sha256.Sum256(data)
	return state
}

func equalLauncherProbeState(a, b map[string][sha256.Size]byte) bool {
	if len(a) != len(b) {
		return false
	}
	for path, digest := range a {
		if other, exists := b[path]; !exists || digest != other {
			return false
		}
	}
	return true
}

func TestLauncherInstallGenuineColdNode(t *testing.T) {
	f, args, managerLog := launcherInstalledFixture(t)
	// The source is selected by the check's private PATH; it is never executed.
	source, err := exec.LookPath("node")
	if err != nil {
		t.Fatal(err)
	}
	node, err := launcherFixtureNode(f.env["HOME"], coldLauncherNode)
	if err != nil {
		t.Fatal(err)
	}
	identity := func(path string) (os.FileInfo, []byte) {
		file, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		defer file.Close()
		info, err := file.Stat()
		if err != nil {
			t.Fatal(err)
		}
		hash := sha256.New()
		if _, err := io.Copy(hash, file); err != nil {
			t.Fatal(err)
		}
		return info, hash.Sum(nil)
	}
	sourceInfo, sourceHash := identity(source)
	nodeInfo, nodeHash := identity(node)
	if os.SameFile(sourceInfo, nodeInfo) || !bytes.Equal(sourceHash, nodeHash) {
		t.Fatal("cold image must be an independent byte-for-byte genuine Node copy")
	}
	before := launcherProbeState(t, f, managerLog)
	start := time.Now()
	code, _, errs := f.run(t, append(args, "--node", node)...)
	t.Logf("genuine cold Node install preview elapsed=%s exit=%d", time.Since(start), code)
	if code != 0 {
		t.Fatal(errs)
	}
	if !equalLauncherProbeState(before, launcherProbeState(t, f, managerLog)) {
		t.Fatal("preview changed installed files or service intent")
	}
}
