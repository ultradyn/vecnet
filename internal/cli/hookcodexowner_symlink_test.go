package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCodexLegacyHookSymlinkPreservesReservedState(t *testing.T) {
	env, state, _ := sessionHub(t)
	other, _, _ := sessionHub(t)
	cfg, err := loadConfig(env["VECNET_CONFIG"])
	if err != nil {
		t.Fatal(err)
	}
	_, profile, err := cfg.profile("p")
	if err != nil {
		t.Fatal(err)
	}
	authority, err := codexBindingsPath(func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	binding := codexBinding{ConfigPath: env["VECNET_CONFIG"], Profile: "p", Hub: profile.Hub, Caller: "peer_test", Generation: appHookGeneration, CodexHome: env["HOME"], ThreadID: bindT1, CreatedAt: now.Add(-time.Hour), ExpiresAt: now.Add(time.Hour)}
	raw, _ := json.Marshal(codexBindingsFile{Version: 1, Bindings: []codexBinding{binding}})
	if err = cliFixtureMkdirAll(filepath.Dir(authority), 0700); err != nil {
		t.Fatal(err)
	}
	if err = cliFixtureWriteFile(authority, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = readCodexAuthority(authority); err != nil {
		t.Fatal(err)
	}
	err = (stateFile{state}).update(context.Background(), func(s *hookState) error {
		s.Hub = profile.Hub
		s.Caller = "peer_test"
		s.Cred = hookCredential(profile.Secret)
		s.PollCursor = "e1.0"
		s.AppCursor = "e1.42"
		s.Shown = []string{"dly_protected"}
		s.Claims = []claimRef{{ID: "dly_protected", Generation: appHookGeneration, Accepted: true}}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(state)
	if err != nil {
		t.Fatal(err)
	}
	aliasDir := filepath.Join(filepath.Dir(env["VECNET_CONFIG"]), "alias-config")
	if err = cliFixtureSymlink(t, filepath.Dir(other["VECNET_CONFIG"]), aliasDir); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(aliasDir, filepath.Base(other["VECNET_CONFIG"]))
	env["VECNET_CONFIG"] = alias
	out, errs := runHookAs(t, env, "legacy", "p")
	after, err := os.ReadFile(state)
	if err != nil {
		t.Fatal(err)
	}
	if out != "" || !bytes.Equal(before, after) {
		t.Fatalf("legacy alias overwrote reserved state: stdout=%q stderr=%q before=%s after=%s", out, errs, before, after)
	}
}
