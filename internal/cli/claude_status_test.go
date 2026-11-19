package cli

// Task 5 (claude delivery foundation): passive status surface. The tests here
// cover the addressed disable/enable semantics, which are platform-independent:
// the global disable flag lives with the host's versioned Claude settings,
// addressed opt-outs live in the binding document, global disable wins over
// profile enable, and neither removes the legacy path or a stored binding row.

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// claudeStatusFixture is a private HOME with one witnessed profile.
func claudeStatusFixture(t *testing.T) (home string, env map[string]string) {
	t.Helper()
	d := t.TempDir()
	config := filepath.Join(d, "v.toml")
	if err := saveConfig(config, Config{Profiles: map[string]Profile{
		"p": {Hub: "https://hub.example.com", Secret: "vn_peer_test", PeerID: "peer_A"},
	}}); err != nil {
		t.Fatal(err)
	}
	env = map[string]string{
		"HOME":            d,
		"VECNET_CONFIG":   config,
		"XDG_CONFIG_HOME": filepath.Join(d, "xdg"),
		"XDG_CACHE_HOME":  filepath.Join(d, "cache"),
	}
	return d, env
}

func claudeStatusRun(t *testing.T, env map[string]string, args ...string) (int, string, string) {
	t.Helper()
	var out, errs bytes.Buffer
	code := Run(args, strings.NewReader(""), &out, &errs, func(k string) string { return env[k] })
	return code, out.String(), errs.String()
}

func claudeStatusJSON(t *testing.T, env map[string]string, args ...string) claudeStatusResult {
	t.Helper()
	code, out, errs := claudeStatusRun(t, env, args...)
	if code != 0 {
		t.Fatalf("claude %s exit %d: %s", strings.Join(args, " "), code, errs)
	}
	var result claudeStatusResult
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("claude status output is not JSON: %q (%v)", out, err)
	}
	return result
}

func claudeStatePath(home, name string) string {
	return filepath.Join(home, ".local", "state", "vecnet", "projects", name)
}

func TestClaudeStatusDisableEnableLeavesLegacy(t *testing.T) {
	home, env := claudeStatusFixture(t)
	// One stored binding row: an opt-out must block registration without
	// removing it, exactly as the addressed opt-out contract requires.
	scope := claudeScope{
		ConfigPath:  env["VECNET_CONFIG"],
		Profile:     "p",
		HubIdentity: "https://hub.example.com",
		HubEndpoint: "https://hub.example.com",
		Caller:      "peer_A",
		Recipient:   "peer_A",
	}
	stored := claudeBinding{claudeScope: scope, claudeRoot: claudeRoot{
		ConfigRoot: filepath.Join(home, "root"), SessionID: "S1", ProcessStart: "12",
		BootID: "01234567-89ab-cdef-0123-456789abcdef", Version: "2.1.322", PID: 4242, UID: 1000,
	}, Generation: "0123456789abcdef0123456789abcdef", UpdatedAt: time.Now().UTC()}
	if err := os.MkdirAll(filepath.Dir(claudeStatePath(home, claudeBindingsName)), 0o700); err != nil {
		t.Fatal(err)
	}
	rows, err := json.Marshal(claudeBindingsFile{Version: 1, Bindings: []claudeBinding{stored}, OptOuts: []claudeScope{}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(claudeStatePath(home, claudeBindingsName), rows, 0o600); err != nil {
		t.Fatal(err)
	}

	// Baseline: no opt-out, so mode stays legacy with no live owner.
	if result := claudeStatusJSON(t, env, "--net", "p", "claude", "status", "--json"); result.Mode != "legacy" || result.Owner != "none" || result.PendingCount != 0 {
		t.Fatalf("baseline status is not passive legacy: %+v", result)
	}

	// The global disable persists with the host's versioned Claude settings.
	if code, _, _ := claudeStatusRun(t, env, "claude", "disable"); code != 0 {
		t.Fatal("global claude disable failed")
	}
	raw, err := os.ReadFile(claudeStatePath(home, claudeSettingsName))
	if err != nil {
		t.Fatalf("global disable wrote no settings document: %v", err)
	}
	var settings struct {
		Version  int  `json:"version"`
		Disabled bool `json:"disabled"`
	}
	if json.Unmarshal(raw, &settings) != nil || settings.Version != 1 || !settings.Disabled {
		t.Fatalf("settings document is not a versioned disable: %s", raw)
	}
	// A duplicate global disable stays idempotent.
	if code, _, _ := claudeStatusRun(t, env, "claude", "disable"); code != 0 {
		t.Fatal("duplicate global claude disable failed")
	}

	// Profile enable clears only the addressed opt-out; global disable wins.
	if code, _, _ := claudeStatusRun(t, env, "--net", "p", "claude", "enable"); code != 0 {
		t.Fatal("profile claude enable failed")
	}
	global := claudeStatusJSON(t, env, "--net", "p", "claude", "status", "--json")
	if global.Mode != "opted_out" || global.Owner != "none" {
		t.Fatalf("profile enable defeated the global disable: %+v", global)
	}

	// The global enable restores legacy mode.
	if code, _, _ := claudeStatusRun(t, env, "claude", "enable"); code != 0 {
		t.Fatal("global claude enable failed")
	}
	if after := claudeStatusJSON(t, env, "--net", "p", "claude", "status", "--json"); after.Mode != "legacy" {
		t.Fatalf("global enable did not restore legacy mode: %+v", after)
	}

	// The addressed opt-out lands in the binding document and keeps rows.
	if code, _, _ := claudeStatusRun(t, env, "--net", "p", "claude", "disable"); code != 0 {
		t.Fatal("addressed claude disable failed")
	}
	addressed := claudeStatusJSON(t, env, "--net", "p", "claude", "status", "--json")
	if addressed.Mode != "opted_out" || addressed.Owner != "none" {
		t.Fatalf("addressed opt-out did not surface: %+v", addressed)
	}
	var doc claudeBindingsFile
	raw, err = os.ReadFile(claudeStatePath(home, claudeBindingsName))
	if err != nil || json.Unmarshal(raw, &doc) != nil {
		t.Fatalf("binding document unreadable: %v", err)
	}
	if len(doc.Bindings) != 1 || doc.Bindings[0].Generation != stored.Generation {
		t.Fatalf("addressed opt-out disturbed the stored binding: %+v", doc.Bindings)
	}
	if len(doc.OptOuts) != 1 || !claudeSameRecipient(doc.OptOuts[0], scope) {
		t.Fatalf("addressed opt-out was not recorded: %+v", doc.OptOuts)
	}

	// Neither surface ever claims pending work or a native path, and no
	// forward-looking deliveries/resolve subcommand exists.
	if result := claudeStatusJSON(t, env, "--net", "p", "claude", "status", "--json"); result.PendingCount != 0 {
		t.Fatalf("passive status reported pending work: %+v", result)
	}
	if code, _, _ := claudeStatusRun(t, env, "--net", "p", "claude", "deliveries"); code == 0 {
		t.Fatal("claude deliveries exists")
	}
	if code, _, _ := claudeStatusRun(t, env, "--net", "p", "claude", "resolve"); code == 0 {
		t.Fatal("claude resolve exists")
	}

	// The addressed enable clears only the addressed opt-out.
	if code, _, _ := claudeStatusRun(t, env, "--net", "p", "claude", "enable"); code != 0 {
		t.Fatal("addressed claude enable failed")
	}
	raw, err = os.ReadFile(claudeStatePath(home, claudeBindingsName))
	if err != nil || json.Unmarshal(raw, &doc) != nil {
		t.Fatal(err)
	}
	if len(doc.OptOuts) != 0 || len(doc.Bindings) != 1 {
		t.Fatalf("addressed enable disturbed the document: %+v", doc)
	}
	if result := claudeStatusJSON(t, env, "--net", "p", "claude", "status", "--json"); result.Mode != "legacy" {
		t.Fatalf("addressed enable did not restore legacy mode: %+v", result)
	}
}
