//go:build unix

package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLocalLegacyManagedCompatibility(t *testing.T) {
	for _, size := range []int{20, 64} {
		for _, kind := range []string{"current", "obsolete", "unverified", "symlink", "missing dependency", "unexpected authority", "unexpected node options", "missing environment", "wrong executable"} {
			t.Run(fmt.Sprintf("%d/%s", size, kind), func(t *testing.T) {
				home := t.TempDir()
				release := filepath.Join(home, ".local/share/vecnet-codex", strings.Repeat("a", size))
				entry := filepath.Join(release, "connectors/launcher/src/codex/delivery-main.ts")
				if e := cliFixtureMkdirAll(filepath.Dir(entry), 0700); e != nil {
					t.Fatal(e)
				}
				content := []byte("current authority connector")
				actual := content
				if kind == "obsolete" {
					actual = []byte("old authority connector")
				}
				if e := cliFixtureWriteFile(entry, actual, 0600); e != nil {
					t.Fatal(e)
				}
				if kind == "symlink" {
					if e := os.Rename(entry, entry+".real"); e != nil {
						t.Fatal(e)
					}
					if e := cliFixtureSymlink(t, entry+".real", entry); e != nil {
						t.Fatal(e)
					}
				}
				compatible := map[string][]byte{"connectors/launcher/src/codex/delivery-main.ts": content}
				if kind == "unverified" {
					compatible = nil
				}
				if kind == "missing dependency" {
					compatible["connectors/launcher/src/codex/connector.ts"] = []byte("canonical authority")
				}
				if got := localLegacyManagedEntry(home, entry, compatible); got != (kind == "current" || strings.HasPrefix(kind, "unexpected") || kind == "missing environment" || kind == "wrong executable") {
					t.Fatalf("compatible = %v", got)
				}
				proc := t.TempDir()
				pid := filepath.Join(proc, "123")
				if err := cliFixtureMkdir(pid, 0700); err != nil {
					t.Fatal(err)
				}
				for name, data := range map[string]string{"status": "Uid: 1000 1000 1000 1000\n", "cmdline": "node\x00" + entry + "\x00"} {
					if err := cliFixtureWriteFile(filepath.Join(pid, name), []byte(data), 0600); err != nil {
						t.Fatal(err)
					}
				}
				env := "HOME=" + home + "\x00VECNET_BIN=" + filepath.Join(release, "vecnet") + "\x00"
				if kind == "unexpected authority" {
					env += "VECNET_CODEX_BINDINGS=/old/bindings.json\x00"
				}
				if kind == "unexpected node options" {
					env += "NODE_OPTIONS=--import=/old/loader.mjs\x00"
				}
				if kind != "missing environment" {
					if e := cliFixtureWriteFile(filepath.Join(pid, "environ"), []byte(env), 0600); e != nil {
						t.Fatal(e)
					}
				}
				exe := "/usr/bin/node"
				if kind == "wrong executable" {
					exe = "/usr/bin/bash"
				}
				if e := cliFixtureSymlink(t, exe, filepath.Join(pid, "exe")); e != nil {
					t.Fatal(e)
				}
				err := verifyLegacyRetiredWith(context.Background(), "linux", home, proc, 1000, func(context.Context, string, ...string) ([]byte, error) { return nil, nil }, compatible)
				if (err == nil) != (kind == "current") {
					t.Fatalf("running process verification: %v", err)
				}

			})
		}
	}
}

func TestLocalLegacyRelativeLauncher(t *testing.T) {
	for _, tc := range []struct{ arg, cwd string }{
		{"main.ts", "/old/connectors/launcher/src"},
		{"src/main.ts", "/old/connectors/launcher"},
		{"launcher/src/main.ts", "/old/connectors"},
		{"connectors/launcher/src/main.ts", "/old"},
		{"main.js", "/old/connectors/launcher/src"},
		{"src/main.ts", ""},
	} {
		t.Run(tc.arg+tc.cwd, func(t *testing.T) {
			proc := t.TempDir()
			pid := filepath.Join(proc, "123")
			if e := cliFixtureMkdir(pid, 0700); e != nil {
				t.Fatal(e)
			}
			for name, data := range map[string]string{"status": "Uid: 1000 1000 1000 1000\n", "cmdline": "node\x00" + tc.arg + "\x00run\x00"} {
				if e := cliFixtureWriteFile(filepath.Join(pid, name), []byte(data), 0600); e != nil {
					t.Fatal(e)
				}
			}
			if tc.cwd != "" {
				cwd := filepath.Join(t.TempDir(), filepath.FromSlash(strings.TrimPrefix(tc.cwd, "/")))
				if e := cliFixtureMkdirAll(cwd, 0700); e != nil {
					t.Fatal(e)
				}
				if e := cliFixtureSymlink(t, cwd, filepath.Join(pid, "cwd")); e != nil {
					t.Fatal(e)
				}
			}
			err := verifyLegacyRetiredWith(context.Background(), "linux", "/home/me", proc, 1000, func(context.Context, string, ...string) ([]byte, error) { return nil, nil })
			if err == nil || !strings.Contains(err.Error(), "process 123") {
				t.Fatalf("accepted relative launcher %q cwd %q: %v", tc.arg, tc.cwd, err)
			}
		})
	}
}
