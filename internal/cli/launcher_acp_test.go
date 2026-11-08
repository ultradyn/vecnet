package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLauncherSetupACPUsesExplicitExecutableAndAskPolicy(t *testing.T) {
	f := newLauncherFixture(t, "launcher")
	command := filepath.Join(f.paths.State, "claude-agent-acp-0.101.0")
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
