package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestLocalLegacyVerification(t *testing.T) {
	for _, tc := range []struct {
		name, units, cmd, status, want string
		runErr                         bool
	}{
		{name: "inactive", units: "vecnet-launcher.service loaded inactive dead launcher\nvecnet-delivery@old.service loaded failed failed old\n"},
		{name: "absent"},
		{name: "active launcher", units: "vecnet-launcher.service loaded active running launcher", want: "vecnet-launcher.service"},
		{name: "active legacy", units: "vecnet-delivery@old.service loaded activating start old", want: "vecnet-delivery@old.service"},
		{name: "unknown", units: "vecnet-launcher.service loaded mysterious unknown launcher", want: "unknown"},
		{name: "unknown substate", units: "vecnet-launcher.service loaded inactive running launcher", want: "substate"},
		{name: "malformed", units: "garbage", want: "response"},
		{name: "query failure", runErr: true, want: "services"},
		{name: "old runner", cmd: "node\x00/old/connectors/launcher/src/codex/delivery-main.ts\x00", want: "process 123"},
		{name: "launcher runner", cmd: "node\x00/home/me/.local/share/vecnet-launcher/current/launcher/src/main.ts\x00run\x00", want: "process 123"},
		{name: "unverified managed rejected", cmd: "node\x00/home/me/.local/share/vecnet-codex/0123456789abcdef0123/connectors/launcher/src/codex/delivery-main.ts\x00", want: "process 123"},
		{name: "foreign user", cmd: "node\x00/old/codex/delivery-main.ts\x00", status: "Uid:\t2000\t2000\t2000\t2000\n"},
		{name: "unknown uid", cmd: "node\x00", status: "Name: node\n", want: "UID"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			if tc.cmd != "" {
				d := filepath.Join(root, "123")
				if e := os.Mkdir(d, 0700); e != nil {
					t.Fatal(e)
				}
				status := tc.status
				if status == "" {
					status = "Uid:\t1000\t1000\t1000\t1000\n"
				}
				for n, b := range map[string]string{"status": status, "cmdline": tc.cmd} {
					if e := os.WriteFile(filepath.Join(d, n), []byte(b), 0600); e != nil {
						t.Fatal(e)
					}
				}
			}
			run := func(ctx context.Context, name string, args ...string) ([]byte, error) {
				if name != "systemctl" || !reflect.DeepEqual(args, []string{"--user", "list-units", "--all", "--plain", "--no-legend", "--no-pager", "--", "vecnet-launcher.service", "vecnet-delivery@*.service"}) {
					t.Fatalf("unexpected command %s %v", name, args)
				}
				if _, ok := ctx.Deadline(); !ok {
					t.Fatal("unbounded query")
				}
				if tc.runErr {
					return nil, errors.New("unavailable")
				}
				return []byte(tc.units), nil
			}
			err := verifyLegacyRetiredWith(context.Background(), "linux", "/home/me", root, 1000, run)
			if tc.want == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want %q", err, tc.want)
			}
		})
	}
}

func TestLocalLegacyUnsupported(t *testing.T) {
	err := verifyLegacyRetiredWith(context.Background(), "darwin", "", t.TempDir(), 1000, nil)
	if err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatal(fmt.Sprint(err))
	}
}

func TestLocalLegacyProcessFailures(t *testing.T) {
	for _, tc := range []struct {
		name, status, cmd, want           string
		missingRoot, missingCmd, relative bool
	}{
		{name: "proc unavailable", missingRoot: true, want: "cannot inspect legacy processes"},
		{name: "missing command is exited process", missingCmd: true, status: "Uid: 1000 1000 1000 1000\n"},
		{name: "truncated command", status: "Uid: 1000 1000 1000 1000\n", cmd: "node", want: "malformed cmdline"},
		{name: "malformed uid", status: "Uid: nope 1000 1000 1000\n", want: "malformed UID"},
		{name: "relative old runner", status: "Uid: 1000 1000 1000 1000\n", cmd: "node\x00codex/delivery-main.ts\x00", relative: true, want: "process 123"},
		{name: "fake managed release", status: "Uid: 1000 1000 1000 1000\n", cmd: "node\x00/home/me/.local/share/vecnet-codex/custom/connectors/launcher/src/codex/delivery-main.ts\x00", want: "process 123"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			if tc.missingRoot {
				root = filepath.Join(root, "absent")
			} else {
				d := filepath.Join(root, "123")
				if e := os.Mkdir(d, 0700); e != nil {
					t.Fatal(e)
				}
				if e := os.WriteFile(filepath.Join(d, "status"), []byte(tc.status), 0600); e != nil {
					t.Fatal(e)
				}
				if !tc.missingCmd {
					if e := os.WriteFile(filepath.Join(d, "cmdline"), []byte(tc.cmd), 0600); e != nil {
						t.Fatal(e)
					}
				}
				if tc.relative {
					if e := os.Symlink("/old/launcher/src", filepath.Join(d, "cwd")); e != nil {
						t.Fatal(e)
					}
				}
			}
			err := verifyLegacyRetiredWith(context.Background(), "linux", "/home/me", root, 1000, func(context.Context, string, ...string) ([]byte, error) { return nil, nil })
			if tc.want == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want %q", err, tc.want)
			}
		})
	}
}

func TestLocalLegacyCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := verifyLegacyRetiredWith(ctx, "linux", "/home/me", t.TempDir(), 1000, func(ctx context.Context, _ string, _ ...string) ([]byte, error) { return nil, ctx.Err() })
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
}

func TestLocalLegacyServiceBound(t *testing.T) {
	err := verifyLegacyRetiredWith(context.Background(), "linux", "/home/me", t.TempDir(), 1000, func(context.Context, string, ...string) ([]byte, error) {
		return make([]byte, localLegacyProbeLimit+1), nil
	})
	if err == nil || !strings.Contains(err.Error(), "bound") {
		t.Fatal(err)
	}
}

func TestLocalLegacyManagedCompatibility(t *testing.T) {
	for _, size := range []int{20, 64} {
		for _, kind := range []string{"current", "obsolete", "unverified", "symlink", "missing dependency", "unexpected authority", "unexpected node options", "missing environment", "wrong executable"} {
			t.Run(fmt.Sprintf("%d/%s", size, kind), func(t *testing.T) {
				home := t.TempDir()
				release := filepath.Join(home, ".local/share/vecnet-codex", strings.Repeat("a", size))
				entry := filepath.Join(release, "connectors/launcher/src/codex/delivery-main.ts")
				if e := os.MkdirAll(filepath.Dir(entry), 0700); e != nil {
					t.Fatal(e)
				}
				content := []byte("current authority connector")
				actual := content
				if kind == "obsolete" {
					actual = []byte("old authority connector")
				}
				if e := os.WriteFile(entry, actual, 0600); e != nil {
					t.Fatal(e)
				}
				if kind == "symlink" {
					if e := os.Rename(entry, entry+".real"); e != nil {
						t.Fatal(e)
					}
					if e := os.Symlink(entry+".real", entry); e != nil {
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
				if err := os.Mkdir(pid, 0700); err != nil {
					t.Fatal(err)
				}
				for name, data := range map[string]string{"status": "Uid: 1000 1000 1000 1000\n", "cmdline": "node\x00" + entry + "\x00"} {
					if err := os.WriteFile(filepath.Join(pid, name), []byte(data), 0600); err != nil {
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
					if e := os.WriteFile(filepath.Join(pid, "environ"), []byte(env), 0600); e != nil {
						t.Fatal(e)
					}
				}
				exe := "/usr/bin/node"
				if kind == "wrong executable" {
					exe = "/usr/bin/bash"
				}
				if e := os.Symlink(exe, filepath.Join(pid, "exe")); e != nil {
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
			if e := os.Mkdir(pid, 0700); e != nil {
				t.Fatal(e)
			}
			for name, data := range map[string]string{"status": "Uid: 1000 1000 1000 1000\n", "cmdline": "node\x00" + tc.arg + "\x00run\x00"} {
				if e := os.WriteFile(filepath.Join(pid, name), []byte(data), 0600); e != nil {
					t.Fatal(e)
				}
			}
			if tc.cwd != "" {
				if e := os.Symlink(tc.cwd, filepath.Join(pid, "cwd")); e != nil {
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
