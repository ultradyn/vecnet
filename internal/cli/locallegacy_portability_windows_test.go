//go:build windows

package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLocalLegacyWindowsRetirementRefusesProcfsEvidence(t *testing.T) {
	for _, kind := range []string{"matching-managed-release", "obsolete-release", "relative-launcher", "missing-procfs"} {
		t.Run(kind, func(t *testing.T) {
			home := t.TempDir()
			path := filepath.Join(home, "legacy-bindings.json")
			authority := []byte("{\"version\":1,\"bindings\":[]}\n")
			if err := cliFixtureWriteFile(path, authority, 0600); err != nil {
				t.Fatal(err)
			}
			source := []byte("matching connector")
			release := filepath.Join(home, ".local", "share", "vecnet-codex", strings.Repeat("a", 20))
			entry := filepath.Join(release, "connectors", "launcher", "src", "codex", "delivery-main.ts")
			if err := cliFixtureMkdirAll(filepath.Dir(entry), 0700); err != nil {
				t.Fatal(err)
			}
			actual := source
			if kind == "obsolete-release" {
				actual = []byte("old connector")
			}
			if err := cliFixtureWriteFile(entry, actual, 0600); err != nil {
				t.Fatal(err)
			}
			proc := filepath.Join(home, "proc")
			if kind != "missing-procfs" {
				pid := filepath.Join(proc, "123")
				if err := cliFixtureMkdirAll(pid, 0700); err != nil {
					t.Fatal(err)
				}
				command := "node\x00" + entry + "\x00"
				if kind == "relative-launcher" {
					command = "node\x00src/main.ts\x00run\x00"
				}
				for name, raw := range map[string]string{"status": "Uid: 1000 1000 1000 1000\n", "cmdline": command, "environ": "HOME=" + home + "\x00"} {
					if err := cliFixtureWriteFile(filepath.Join(pid, name), []byte(raw), 0600); err != nil {
						t.Fatal(err)
					}
				}
			}
			calls := 0
			run := func(context.Context, string, ...string) ([]byte, error) { calls++; return nil, nil }
			err := verifyLegacyRetiredWith(t.Context(), "windows", home, proc, 1000, run, map[string][]byte{"connectors/launcher/src/codex/delivery-main.ts": source})
			if err == nil || !strings.Contains(err.Error(), "unsupported on windows") || calls != 0 {
				t.Fatalf("retirement inferred from procfs: calls=%d err=%v", calls, err)
			}
			after, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(authority, after) {
				t.Fatalf("retirement changed authority: %q %v", after, err)
			}
			after, err = os.ReadFile(entry)
			if err != nil || !bytes.Equal(actual, after) {
				t.Fatalf("retirement changed release: %q %v", after, err)
			}
		})
	}
}
