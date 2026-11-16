package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/XertroV/vecnet/internal/platform"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestLauncherSetupACPUsesExplicitExecutableAndAskPolicy(t *testing.T) {
	f := newLauncherFixture(t, "launcher")
	command := filepath.Join(f.paths.State, platform.Exe("claude-agent-acp-0.101.0"))
	if err := os.MkdirAll(filepath.Dir(command), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(command, []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	args := append(append([]string{}, f.setup[:len(f.setup)-2]...), "--backend", "acp", "--acp-command", command, "--acp-arg=--protocol=v1", "--acp-provider", "claude", "--acp-mode", "default")
	code, out, errs := f.run(t, args...)
	if code != 0 {
		t.Fatal(errs)
	}
	var plan struct {
		Config struct {
			Backends []struct {
				ID     string `json:"id"`
				Module string `json:"module"`
				Config struct {
					Presets map[string]struct {
						Argv     []string `json:"argv"`
						Provider string   `json:"provider"`
						Mode     string   `json:"mode_id"`
					} `json:"presets"`
				} `json:"config"`
			} `json:"backends"`
			Policies map[string]struct {
				Backends []string `json:"backends"`
				Modes    []string `json:"permission_modes"`
			} `json:"policies"`
		} `json:"config"`
	}
	if err := json.Unmarshal([]byte(out), &plan); err != nil {
		t.Fatal(err)
	}
	if len(plan.Config.Backends) != 1 {
		t.Fatal("expected exactly configured ACP backend")
	}
	b := plan.Config.Backends[0]
	p := b.Config.Presets["default"]
	if b.ID != "acp" || b.Module != "./backends/acp/index.ts" || len(p.Argv) != 2 || p.Argv[0] != command || p.Argv[1] != "--protocol=v1" || p.Provider != "claude" || p.Mode != "default" {
		t.Fatalf("incorrect public ACP configuration: %+v", b)
	}
	policy := plan.Config.Policies["default"]
	if strings.Join(policy.Backends, ",") != "acp" || strings.Join(policy.Modes, ",") != "ask" {
		t.Fatal("ACP policy must expose ask only")
	}
}

func TestLauncherSetupACPRejectsUnsafeOrNativeOverrides(t *testing.T) {
	for _, extra := range [][]string{
		{"--acp-command", "relative-command"},
		{"--acp-command", "/absolute/codex"},
		{"--acp-command", "/absolute/claude", "--acp-mode", "bypassPermissions"},
		{"--acp-command", "/absolute/claude", "--model", "native-only-model"},
	} {
		t.Run(strings.Join(extra, " "), func(t *testing.T) {
			f := newLauncherFixture(t, "launcher")
			args := append(append([]string{}, f.setup[:len(f.setup)-2]...), "--backend", "acp")
			args = append(args, extra...)
			code, _, _ := f.run(t, args...)
			if code == 0 {
				t.Fatal("unsafe ACP setup accepted")
			}
		})
	}
}

func newLauncherACPArgvFixture(t *testing.T) (launcherFixture, string, []string) {
	t.Helper()
	f := newLauncherFixture(t, "launcher")
	command := filepath.Join(f.paths.State, platform.Exe("claude-agent-acp-0.101.0"))
	if err := os.MkdirAll(filepath.Dir(command), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(command, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	args := append([]string{"launcher"}, f.setup[:len(f.setup)-2]...)
	args = append(args, "--backend", "acp", "--acp-command", command)
	return f, command, args
}

func assertLauncherACPArgvWrite(t *testing.T, f launcherFixture, args, want []string) {
	t.Helper()
	var out, errs bytes.Buffer
	code := RunContext(context.Background(), args, strings.NewReader(""), &out, &errs, func(k string) string { return f.env[k] })
	if code != 0 {
		t.Fatalf("setup exit=%d stdout=%q stderr=%q", code, out.String(), errs.String())
	}
	var result struct {
		Write      bool   `json:"write"`
		Profile    string `json:"profile"`
		ConfigPath string `json:"config_path"`
	}
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatalf("setup did not report a write: %v stdout=%q", err, out.String())
	}
	if !result.Write || result.Profile != "launcher/test" || result.ConfigPath != f.paths.Config {
		t.Fatalf("wrong write/profile: %+v", result)
	}
	var config struct {
		Backends []struct {
			ID     string `json:"id"`
			Config struct {
				Presets map[string]struct {
					Argv []string `json:"argv"`
				} `json:"presets"`
			} `json:"config"`
		} `json:"backends"`
	}
	if err := launcherReadJSON(f.paths.Config, &config); err != nil {
		t.Fatalf("requested config was not written: %v", err)
	}
	if len(config.Backends) != 1 || config.Backends[0].ID != "acp" {
		t.Fatalf("wrong written backend: %+v", config)
	}
	if got := config.Backends[0].Config.Presets["default"].Argv; !reflect.DeepEqual(got, want) {
		t.Fatalf("written argv=%q, want %q", got, want)
	}
	if got, err := os.ReadFile(f.paths.Credential); err != nil || string(got) != "vn_peer_LAUNCHER_TEST\n" {
		t.Fatalf("selected profile credential was not written: %v", err)
	}
	if got, err := os.ReadFile(f.env["VECNET_CONFIG"]); err != nil || !bytes.Equal(got, f.original) {
		t.Fatalf("setup changed enrolled profiles: %v", err)
	}
}

func TestLauncherSetupACPOpaqueArgumentsWrite(t *testing.T) {
	for _, form := range []string{"separate", "equals"} {
		for _, value := range []string{"-h", "--help", "--json", "--net=other", "--as=other", "--"} {
			t.Run(form+"/"+value, func(t *testing.T) {
				f, command, setup := newLauncherACPArgvFixture(t)
				args := append([]string{"--net", "launcher/test"}, setup...)
				args = append(args, "--acp-arg", "ordinary", "--acp-arg=two words")
				if form == "separate" {
					args = append(args, "--acp-arg", value)
				} else {
					args = append(args, "--acp-arg="+value)
				}
				args = append(args, "--acp-arg", "ordinary", "--write")
				assertLauncherACPArgvWrite(t, f, args, []string{command, "ordinary", "two words", value, "ordinary"})
			})
		}
	}
}

func TestLauncherSetupACPGlobalFlagPlacement(t *testing.T) {
	for _, placement := range []string{"before-command", "before-subcommand", "after-subcommand", "trailing", "trailing-terminator"} {
		t.Run(placement, func(t *testing.T) {
			f, command, setup := newLauncherACPArgvFixture(t)
			setup = append(setup, "--acp-arg", "--json", "--write")
			globals := []string{"--net=launcher/test", "--json"}
			var args []string
			switch placement {
			case "before-command":
				args = append(globals, setup...)
			case "before-subcommand":
				args = append([]string{setup[0]}, globals...)
				args = append(args, setup[1:]...)
			case "after-subcommand":
				args = append(append([]string{}, setup[:2]...), globals...)
				args = append(args, setup[2:]...)
			default:
				args = append(setup, globals...)
				if placement == "trailing-terminator" {
					args = append(args, "--")
				}
			}
			assertLauncherACPArgvWrite(t, f, args, []string{command, "--json"})
		})
	}
}

func TestLauncherSetupACPGlobalControlsAndMissingOperand(t *testing.T) {
	for _, tc := range []struct {
		name  string
		extra []string
		help  bool
		err   string
	}{
		{"help", []string{"--help"}, true, ""},
		{"short-help", []string{"-h"}, true, ""},
		{"profile", []string{"--net=other"}, false, `no profile "other"`},
		{"identity", []string{"--as", "peer_other"}, false, "does not accept --as"},
		{"terminator", []string{"--", "--help", "--acp-arg", "--json"}, false, "unexpected arguments"},
		{"missing-operand", []string{"--acp-arg"}, false, "--acp-arg needs a value"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, _, setup := newLauncherACPArgvFixture(t)
			args := append(setup[1:], "--acp-arg=--help", "--write")
			args = append(args, tc.extra...)
			code, out, errs := f.run(t, args...)
			if tc.help {
				if code != 0 || !strings.HasPrefix(out, "usage: vecnet ") {
					t.Fatalf("genuine help failed: exit=%d stdout=%q stderr=%q", code, out, errs)
				}
			} else if code == 0 || !strings.Contains(errs, tc.err) {
				t.Fatalf("expected refusal containing %q: exit=%d stdout=%q stderr=%q", tc.err, code, out, errs)
			}
			for _, path := range []string{f.paths.Config, f.paths.Credential} {
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Fatalf("help/refused setup wrote %s: %v", path, err)
				}
			}
		})
	}
}

func TestLauncherSetupACPOpaqueArgumentScope(t *testing.T) {
	for _, command := range [][]string{{"launcher", "install"}, {"launcher", "status"}, {"launcher", "check"}, {"peers"}} {
		t.Run(strings.Join(command, "-"), func(t *testing.T) {
			var out, errs bytes.Buffer
			args := append(append([]string{}, command...), "--acp-arg", "--help")
			code := RunContext(context.Background(), args, strings.NewReader(""), &out, &errs, func(string) string { return "" })
			if code != 0 || !strings.HasPrefix(out.String(), "usage: vecnet ") {
				t.Fatalf("other commands lost genuine help: exit=%d stdout=%q stderr=%q", code, out.String(), errs.String())
			}
		})
	}
}
