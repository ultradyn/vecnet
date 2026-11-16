package cli

import (
	"context"
	"errors"
	"fmt"
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
				if e := cliFixtureMkdir(d, 0700); e != nil {
					t.Fatal(e)
				}
				status := tc.status
				if status == "" {
					status = "Uid:\t1000\t1000\t1000\t1000\n"
				}
				for n, b := range map[string]string{"status": status, "cmdline": tc.cmd} {
					if e := cliFixtureWriteFile(filepath.Join(d, n), []byte(b), 0600); e != nil {
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
				if e := cliFixtureMkdir(d, 0700); e != nil {
					t.Fatal(e)
				}
				if e := cliFixtureWriteFile(filepath.Join(d, "status"), []byte(tc.status), 0600); e != nil {
					t.Fatal(e)
				}
				if !tc.missingCmd {
					if e := cliFixtureWriteFile(filepath.Join(d, "cmdline"), []byte(tc.cmd), 0600); e != nil {
						t.Fatal(e)
					}
				}
				if tc.relative {
					cwd := filepath.Join(t.TempDir(), "old", "launcher", "src")
					if e := cliFixtureMkdirAll(cwd, 0700); e != nil {
						t.Fatal(e)
					}
					if e := cliFixtureSymlink(t, cwd, filepath.Join(d, "cwd")); e != nil {
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
