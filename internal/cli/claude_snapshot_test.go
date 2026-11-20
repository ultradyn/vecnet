package cli

// Delivery Phase 2, task 5: the runtime snapshot hands the delivery host the
// binding's own canonical config path and pinned hub identity, so the host's
// scoped children (inbox, watch) run against exactly the config the binding
// was registered from rather than the daemon's default. admission.witness
// re-proves that stored scope against the config at that stored path — the
// profile, hub, pin and caller witness — independent of the snapshot child's
// own VECNET_CONFIG. Neither field reaches the status sidecar (the TS side
// whitelists it). Portable: nothing here needs live process evidence.

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"
)

// claudeTestHubPinOther is a second well-formed pinned identity.
const claudeTestHubPinOther = "ed25519:AQIDBAUGBwgJCgsMDQ4PEBESExQVFhcYGRobHB0eHyA"

// claudeSnapshotFixture stores one binding for profile "p" of the canary
// fixture's config, then repoints the snapshot child's own VECNET_CONFIG at
// an unrelated file, so a field that tracked the env would be visible.
func claudeSnapshotFixture(t *testing.T) (f *claudeCanaryFixture, scope claudeScope) {
	t.Helper()
	f = newClaudeCanaryFixture(t)
	a := &app{ctx: t.Context(), stdout: nil, stderr: nil, getenv: func(k string) string { return f.env[k] }}
	scope, err := a.claudeScopeForProfile("p")
	if err != nil {
		t.Fatal(err)
	}
	stored := claudeBinding{claudeScope: scope, claudeRoot: claudeRoot{
		ConfigRoot: filepath.Join(f.home, "root"), SessionID: "S1", ProcessStart: "12",
		BootID: "01234567-89ab-cdef-0123-456789abcdef", PID: 4242, UID: 1000,
	}, Generation: "0123456789abcdef0123456789abcdef", UpdatedAt: time.Now().UTC()}
	rows, err := json.Marshal(claudeBindingsFile{Version: 2, Bindings: []claudeBinding{stored}, OptOuts: []claudeScope{}, Canary: []claudeScope{}})
	if err != nil {
		t.Fatal(err)
	}
	claudeSeedLocalState(t, f.env, claudeBindingsName, rows)
	other := filepath.Join(f.home, "daemon-default.toml")
	if err := saveConfig(other, Config{Profiles: map[string]Profile{}}); err != nil {
		t.Fatal(err)
	}
	f.env["VECNET_CONFIG"] = other
	return f, scope
}

type claudeSnapshotRowView struct {
	Admission map[string]json.RawMessage `json:"admission"`
	Scope     map[string]json.RawMessage `json:"scope"`
}

func claudeSnapshotRow(t *testing.T, f *claudeCanaryFixture) claudeSnapshotRowView {
	t.Helper()
	var snap struct {
		Bindings []claudeSnapshotRowView `json:"bindings"`
	}
	out := f.ok(t, "claude", "runtime-snapshot")
	if err := json.Unmarshal([]byte(out), &snap); err != nil || len(snap.Bindings) != 1 {
		t.Fatalf("runtime-snapshot output: %q (%v)", out, err)
	}
	return snap.Bindings[0]
}

func claudeSnapshotWitness(t *testing.T, f *claudeCanaryFixture) bool {
	t.Helper()
	row := claudeSnapshotRow(t, f)
	raw, ok := row.Admission["witness"]
	if !ok {
		t.Fatalf("the snapshot admission carries no witness flag: %v", row.Admission)
	}
	return string(raw) == "true"
}

// TestClaudeSnapshotScopeCarriesBindingConfig pins the child-spawn
// coordinates: scope.config is the binding's stored canonical config path
// (never the snapshot child's VECNET_CONFIG) and scope.hub_identity is its
// pinned identity; the witness holds against that stored config.
func TestClaudeSnapshotScopeCarriesBindingConfig(t *testing.T) {
	f, scope := claudeSnapshotFixture(t)
	row := claudeSnapshotRow(t, f)
	if got := string(row.Scope["config"]); got != jsonString(scope.ConfigPath) {
		t.Fatalf("scope.config = %s, want the binding's %s", got, jsonString(scope.ConfigPath))
	}
	if got := string(row.Scope["config"]); got == jsonString(f.env["VECNET_CONFIG"]) {
		t.Fatalf("scope.config tracked the snapshot child's env: %s", got)
	}
	if got := string(row.Scope["hub_identity"]); got != jsonString(claudeTestHubPin) {
		t.Fatalf("scope.hub_identity = %s, want %s", got, jsonString(claudeTestHubPin))
	}
	if !filepath.IsAbs(scope.ConfigPath) {
		t.Fatalf("fixture config path %q is not absolute", scope.ConfigPath)
	}
	if !claudeSnapshotWitness(t, f) {
		t.Fatal("the witness failed against the binding's own unchanged config")
	}
	f.assertNoHub(t)
}

// TestClaudeSnapshotWitnessFailsAfterConfigReplaced rewrites the config at
// the binding's stored path: a removed profile, a changed caller witness, a
// moved hub or a changed pin each make witness false; restoring it holds.
func TestClaudeSnapshotWitnessFailsAfterConfigReplaced(t *testing.T) {
	f, scope := claudeSnapshotFixture(t)
	original := Config{Profiles: map[string]Profile{
		"p": {Hub: f.hub, HubPubkey: claudeTestHubPin, Secret: "vn_peer_test_p", PeerID: claudeTestPeerID},
	}}
	cases := map[string]Config{
		"profile removed": {Profiles: map[string]Profile{
			"r": {Hub: f.hub, HubPubkey: claudeTestHubPin, Secret: "vn_peer_test_r", PeerID: claudeTestPeerB},
		}},
		"caller changed": {Profiles: map[string]Profile{
			"p": {Hub: f.hub, HubPubkey: claudeTestHubPin, Secret: "vn_peer_test_p", PeerID: claudeTestPeerB},
		}},
		"hub moved": {Profiles: map[string]Profile{
			"p": {Hub: "https://elsewhere.example.com", HubPubkey: claudeTestHubPin, Secret: "vn_peer_test_p", PeerID: claudeTestPeerID},
		}},
		"pin changed": {Profiles: map[string]Profile{
			"p": {Hub: f.hub, HubPubkey: claudeTestHubPinOther, Secret: "vn_peer_test_p", PeerID: claudeTestPeerID},
		}},
		"no caller witness": {Profiles: map[string]Profile{
			"p": {Hub: f.hub, HubPubkey: claudeTestHubPin, Secret: "vn_peer_test_p"},
		}},
	}
	for name, cfg := range cases {
		if err := saveConfig(scope.ConfigPath, cfg); err != nil {
			t.Fatal(err)
		}
		if claudeSnapshotWitness(t, f) {
			t.Fatalf("%s: the witness held against a replaced config", name)
		}
		if err := saveConfig(scope.ConfigPath, original); err != nil {
			t.Fatal(err)
		}
		if !claudeSnapshotWitness(t, f) {
			t.Fatalf("%s: the restored config did not witness", name)
		}
	}
	f.assertNoHub(t)
}
