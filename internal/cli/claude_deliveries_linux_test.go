//go:build linux

package cli

// Task 8 (claude delivery chain): the retry resolution against a live
// registered root. Re-arming a claim for one fresh native attempt must still
// pass admission at resolve time: the exact binding is live, policy-enabled
// (build qualification is parked), and its generation still fences the claim. This test
// registers the test process itself as the Claude root, exactly as the
// deliver helper's tests do.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestClaudeResolveRetryReArmsAfterAdmission pins the retry path: an
// explicitly confirmed duplicate risk re-arms the claim as one fresh reserved
// attempt, and the delivery host's own fences (generation, admission, fresh
// correlation ids at attempt time) stay in charge of the write itself.
func TestClaudeResolveRetryReArmsAfterAdmission(t *testing.T) {
	home, env := claudeStatusFixture(t)
	dir := t.TempDir()
	configRoot := filepath.Join(dir, "claude-config")
	if err := os.MkdirAll(filepath.Join(configRoot, "sessions"), 0o700); err != nil {
		t.Fatal(err)
	}
	session := "f11c6ee2-79f5-4f0d-a5c1-d0a3f24ee9c1"
	sessionRow := `{"sessionId":` + jsonString(session) + `}`
	if err := os.WriteFile(filepath.Join(configRoot, "sessions", strconv.Itoa(os.Getpid())+".json"), []byte(sessionRow), 0o600); err != nil {
		t.Fatal(err)
	}
	scope := claudeScope{
		ConfigPath:  env["VECNET_CONFIG"],
		Profile:     "p",
		HubIdentity: claudeTestHubPin,
		HubEndpoint: "https://hub.example.com",
		Caller:      claudeTestPeerID,
		Recipient:   claudeTestPeerID,
	}
	doc := `{"version":2,"bindings":[{` +
		`"config_path":` + jsonString(scope.ConfigPath) + `,"profile":"p",` +
		`"hub_identity":` + jsonString(claudeTestHubPin) + `,"hub_endpoint":"https://hub.example.com",` +
		`"caller":` + jsonString(claudeTestPeerID) + `,"recipient":` + jsonString(claudeTestPeerID) + `,` +
		`"config_root":` + jsonString(configRoot) + `,"session_id":` + jsonString(session) + `,` +
		`"process_start":` + jsonString(procStart(os.Getpid())) + `,"boot_id":` + jsonString(claudeBootID()) + `,` +
		`"pid":` + strconv.Itoa(os.Getpid()) + `,"uid":` + strconv.Itoa(os.Geteuid()) + `,` +
		`"generation":` + jsonString(claudeDeliveryTestGen) + `,"updated_at":"2026-11-19T00:00:00Z"}],"opt_outs":[],"canary":[` + claudeCanaryRecordJSON(t, scope) + `]}`
	stateDir := filepath.Join(home, ".local", "state", "vecnet", "projects")
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, claudeBindingsName), []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	id := "dly_00000000000000000000000001"
	oldAttempt := "07060504030201000f0e0d0c0b0a0908"
	claudeDeliveryLedger(t, home, map[string]string{
		claudeDeliveryClaimKey(id): claudeDeliveryClaimJSON(t, map[string]any{
			"state": "uncertain", "disposition": "uncertain",
			"dispositionReason": "write_failed_or_partial", "attemptId": oldAttempt,
		}),
	})

	code, out, errs := claudeStatusRun(t, env, "--net", "p", "claude", "resolve", id,
		"--expect-generation", claudeDeliveryTestGen, "--retry", "--confirm-duplicate-risk")
	if code != 0 {
		t.Fatalf("retry resolve exit %d: %s %s", code, out, errs)
	}
	var claim map[string]any
	if err := json.Unmarshal([]byte(claudeDeliveryKV(t, home)[claudeDeliveryClaimKey(id)]), &claim); err != nil {
		t.Fatal(err)
	}
	if claim["state"] != "reserved" {
		t.Fatalf("retry did not re-arm the claim as reserved: %v", claim)
	}
	if claim["attemptId"] == oldAttempt {
		t.Fatalf("retry must mint a fresh attempt identity: %v", claim)
	}
	if claim["generation"] != claudeDeliveryTestGen || claim["messageId"] != claudeDeliveryTestMessage {
		t.Fatalf("retry disturbed the claim's identity: %v", claim)
	}
	if _, held := claim["disposition"]; held {
		t.Fatalf("a re-armed reservation carries no disposition: %v", claim)
	}
	if claim["hubAckObserved"] == true {
		t.Fatalf("retry must never acknowledge: %v", claim)
	}

	// A rotated binding generation fences the retry: refusal, state unchanged.
	rotated := strings.Replace(doc, claudeDeliveryTestGen, strings.Repeat("ee", 16), 1)
	if err := os.WriteFile(filepath.Join(stateDir, claudeBindingsName), []byte(rotated), 0o600); err != nil {
		t.Fatal(err)
	}
	code, _, _ = claudeStatusRun(t, env, "--net", "p", "claude", "resolve", id,
		"--expect-generation", claudeDeliveryTestGen, "--retry", "--confirm-duplicate-risk")
	if code == 0 {
		t.Fatalf("a retry past a rotated generation must refuse")
	}
	if after := claudeDeliveryKV(t, home)[claudeDeliveryClaimKey(id)]; !strings.Contains(after, `"state":"reserved"`) {
		t.Fatalf("the fenced retry changed the claim: %s", after)
	}
}

// TestClaudeResolveRetryWitnessesStoredConfig pins the config witness on the
// retry path: a binding whose STORED config no longer names its profile
// refuses the retry with the claim byte-identical, even though the env's
// config (which addresses the profile for the command) still does.
func TestClaudeResolveRetryWitnessesStoredConfig(t *testing.T) {
	home, env := claudeStatusFixture(t)
	dir := t.TempDir()
	configRoot := filepath.Join(dir, "claude-config")
	if err := os.MkdirAll(filepath.Join(configRoot, "sessions"), 0o700); err != nil {
		t.Fatal(err)
	}
	session := "f11c6ee2-79f5-4f0d-a5c1-d0a3f24ee9c1"
	if err := os.WriteFile(filepath.Join(configRoot, "sessions", strconv.Itoa(os.Getpid())+".json"), []byte(`{"sessionId":`+jsonString(session)+`}`), 0o600); err != nil {
		t.Fatal(err)
	}
	stored, err := canonicalLocalSetupPath(filepath.Join(dir, "bind", "vecnet.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if err := saveConfig(stored, Config{Profiles: map[string]Profile{
		"r": {Hub: "https://hub.example.com", HubPubkey: claudeTestHubPin, Secret: "vn_peer_test_r", PeerID: claudeTestPeerB},
	}}); err != nil {
		t.Fatal(err)
	}
	scope := claudeScope{ConfigPath: stored, Profile: "p", HubIdentity: claudeTestHubPin,
		HubEndpoint: "https://hub.example.com", Caller: claudeTestPeerID, Recipient: claudeTestPeerID}
	doc := `{"version":2,"bindings":[{` +
		`"config_path":` + jsonString(stored) + `,"profile":"p",` +
		`"hub_identity":` + jsonString(claudeTestHubPin) + `,"hub_endpoint":"https://hub.example.com",` +
		`"caller":` + jsonString(claudeTestPeerID) + `,"recipient":` + jsonString(claudeTestPeerID) + `,` +
		`"config_root":` + jsonString(configRoot) + `,"session_id":` + jsonString(session) + `,` +
		`"process_start":` + jsonString(procStart(os.Getpid())) + `,"boot_id":` + jsonString(claudeBootID()) + `,` +
		`"pid":` + strconv.Itoa(os.Getpid()) + `,"uid":` + strconv.Itoa(os.Geteuid()) + `,` +
		`"generation":` + jsonString(claudeDeliveryTestGen) + `,"updated_at":"2026-11-19T00:00:00Z"}],"opt_outs":[],"canary":[` + claudeCanaryRecordJSON(t, scope) + `]}`
	stateDir := filepath.Join(home, ".local", "state", "vecnet", "projects")
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, claudeBindingsName), []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	id := "dly_00000000000000000000000001"
	claudeDeliveryLedger(t, home, map[string]string{
		claudeDeliveryClaimKey(id): claudeDeliveryClaimJSON(t, map[string]any{
			"state": "uncertain", "disposition": "uncertain", "dispositionReason": "write_failed_or_partial",
		}),
	})
	// The command is addressed through the env's config, which still has "p";
	// the binding key it derives must match the stored row.
	if claudeDeliveryScopeKey() != claudeBindingKey(scope) {
		t.Fatal("fixture scope key differs from the env-addressed key")
	}
	before := claudeDeliveryKV(t, home)[claudeDeliveryClaimKey(id)]
	code, _, errs := claudeStatusRun(t, env, "--net", "p", "claude", "resolve", id,
		"--expect-generation", claudeDeliveryTestGen, "--retry", "--confirm-duplicate-risk")
	if code == 0 || !strings.Contains(errs, "config witness") {
		t.Fatalf("an unwitnessed retry: exit %d %q, want a witness refusal", code, errs)
	}
	if after := claudeDeliveryKV(t, home)[claudeDeliveryClaimKey(id)]; after != before {
		t.Fatalf("a refused retry changed the claim: %s -> %s", before, after)
	}
}
