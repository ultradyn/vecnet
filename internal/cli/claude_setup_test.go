package cli

// Task 4 (claude delivery foundation): durable Claude enrollment intent,
// repair reconciliation, and the redacted runtime snapshot. The enrollment
// fixtures run the real enrol command against a counting loopback hub with
// this test process posing as the (non-ancestor) Claude process, so the
// in-session registration stays refused and the intent's survival is what
// is under test.

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
)

const (
	claudeTestPeerID = "peer_00000000000000000000000042"
	claudeTestSecret = "vn_peer_SYNTHETICSECRETDONOTPRINT0123456789"
	claudeTestToken  = "vn_enrol_Zm9vYmFyYmF6cXV4MDEyMzQ1Njc4OWFiY2RlZmdoaWo"

	// claudeTestHubPin is the fixture hub's pinned ed25519 identity (the
	// public key of a fixed test seed; no real hub ever carries it).
	claudeTestHubPin = "ed25519:IVL40Zt5HSRFMkLhXy6rbLfP-ntqXtMAl5YOBpiB2xI"
)

// claudeSetupFixture is one enrollable Claude-session user.
type claudeSetupFixture struct {
	t        *testing.T
	dir      string
	env      map[string]string
	config   string
	root     string
	settings string
	hits     *atomic.Int32
}

func newClaudeSetupFixture(t *testing.T) *claudeSetupFixture {
	t.Helper()
	d, err := canonicalCodexPath(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f := &claudeSetupFixture{t: t, dir: d}
	f.config = filepath.Join(d, "config", "vecnet.toml")
	f.root = filepath.Join(d, "claude-root")
	if err := os.MkdirAll(f.root, 0o700); err != nil {
		t.Fatal(err)
	}
	// Claude Code's own session registry row for this (posing) process.
	sessions := filepath.Join(f.root, "sessions")
	if err := os.MkdirAll(sessions, 0o700); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(map[string]string{"sessionId": "S1"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sessions, strconv.Itoa(os.Getpid())+".json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	// Unmanaged settings with a foreign hook and an installed Vecnet command
	// that keeps its --auto-ack and MCP options.
	f.settings = filepath.Join(d, "project", ".claude", "settings.local.json")
	if err := os.MkdirAll(filepath.Dir(f.settings), 0o755); err != nil {
		t.Fatal(err)
	}
	settings := "{\n  \"hooks\": {\n    \"UserPromptSubmit\": [\n      {\n        \"hooks\": [\n          {\n            \"type\": \"command\",\n            \"command\": \"vecnet hook --profile p --event user-prompt --auto-ack --mcp vecnet-p\",\n            \"timeout\": 5\n          }\n        ]\n      }\n    ],\n    \"PostToolUse\": [\n      {\n        \"matcher\": \"*\",\n        \"hooks\": [\n          {\n            \"type\": \"command\",\n            \"command\": \"notify-send foreign-marker\",\n            \"timeout\": 5\n          }\n        ]\n      }\n    ]\n  }\n}\n"
	if err := os.WriteFile(f.settings, []byte(settings), 0o644); err != nil {
		t.Fatal(err)
	}
	f.env = map[string]string{
		"HOME":                     filepath.Join(d, "home"),
		"VECNET_CONFIG":            f.config,
		"XDG_CONFIG_HOME":          filepath.Join(d, "xdg"),
		"XDG_CACHE_HOME":           filepath.Join(d, "cache"),
		"CLAUDECODE":               "1",
		"CLAUDE_CONFIG_DIR":        f.root,
		"CLAUDE_PID":               strconv.Itoa(os.Getpid()),
		"CLAUDE_CODE_SESSION_ID":   "S1",
		"VECNET_LOCAL_MAINTENANCE": "0",
	}
	return f
}

func (f *claudeSetupFixture) getenv(k string) string { return f.env[k] }

// run runs one CLI invocation and returns exit code, stdout and stderr.
func (f *claudeSetupFixture) run(args ...string) (int, string, string) {
	f.t.Helper()
	var out, errs bytes.Buffer
	code := Run(args, strings.NewReader(""), &out, &errs, f.getenv)
	return code, out.String(), errs.String()
}

func (f *claudeSetupFixture) app() *app {
	return &app{ctx: f.t.Context(), stdout: io.Discard, stderr: io.Discard, getenv: f.getenv}
}

// enrollments reads the durable Claude intent records.
func (f *claudeSetupFixture) enrollments() []claudeEnrollment {
	f.t.Helper()
	records, err := f.app().claudeEnrollments()
	if err != nil {
		f.t.Fatal(err)
	}
	return records
}

func (f *claudeSetupFixture) settingsBytes() []byte {
	f.t.Helper()
	raw, err := os.ReadFile(f.settings)
	if err != nil {
		f.t.Fatal(err)
	}
	return raw
}

func (f *claudeSetupFixture) configBytes() []byte {
	f.t.Helper()
	raw, err := os.ReadFile(f.config)
	if err != nil {
		f.t.Fatal(err)
	}
	return raw
}

// enroll redeems exactly one invitation on a counting loopback hub.
func (f *claudeSetupFixture) enroll() {
	f.t.Helper()
	f.hits = &atomic.Int32{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/enrol" {
			http.NotFound(w, r)
			return
		}
		f.hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"peer_id": claudeTestPeerID, "secret": claudeTestSecret, "name": "scout", "hub_pubkey": claudeTestHubPin})
	}))
	f.t.Cleanup(srv.Close)
	code, out, errs := f.run("enrol", "--hub", srv.URL, "--token", claudeTestToken, "--profile", "scout", "--allow-insecure-http")
	if code != 0 {
		f.t.Fatalf("enrol exit %d\nstdout: %s\nstderr: %s", code, out, errs)
	}
	for _, s := range []string{out, errs} {
		if strings.Contains(s, claudeTestSecret) || strings.Contains(s, "SYNTHETICSECRET") {
			f.t.Fatalf("the issued secret reached the enrol output: %q %q", out, errs)
		}
	}
	if f.hits.Load() != 1 {
		f.t.Fatalf("enrol made %d enrol request(s), want 1", f.hits.Load())
	}
}

// assertNoSecrets fails when any output or state document carries the
// issued secret.
func (f *claudeSetupFixture) assertNoSecrets(outputs ...string) {
	f.t.Helper()
	for _, s := range outputs {
		if strings.Contains(s, claudeTestSecret) || strings.Contains(s, "SYNTHETICSECRET") {
			f.t.Fatalf("the issued secret reached an output: %q", s)
		}
	}
	stateDir := filepath.Join(f.env["HOME"], ".local", "state", "vecnet", "projects")
	entries, err := os.ReadDir(stateDir)
	if err == nil {
		for _, e := range entries {
			raw, err := os.ReadFile(filepath.Join(stateDir, e.Name()))
			if err == nil && (bytes.Contains(raw, []byte(claudeTestSecret)) || bytes.Contains(raw, []byte("SYNTHETICSECRET"))) {
				f.t.Fatalf("the issued secret reached %s", e.Name())
			}
		}
	}
}

func TestClaudeSetupIntentSurvivesPostRedemptionFailure(t *testing.T) {
	f := newClaudeSetupFixture(t)
	settingsBefore := f.settingsBytes()
	f.enroll()

	// The intent survived redemption, and the post-redemption failure (this
	// process is not a child of the posed Claude process, and the delivery
	// runtime is unavailable here) leaves the phase pending and retryable.
	records := f.enrollments()
	if len(records) != 1 {
		t.Fatalf("expected one claude enrollment record, got %d", len(records))
	}
	r := records[0]
	if r.IntentID == "" || r.State != claudeEnrollmentPending || r.Caller != claudeTestPeerID || r.ConfigRoot != f.root || r.Hub == "" {
		t.Fatalf("saved intent is wrong: %+v", r)
	}
	if rows, err := f.app().readClaudeBindings(); err != nil || len(rows) != 0 {
		t.Fatalf("refused registration left bindings: %v %+v", err, rows)
	}

	// Repair retries the saved intent and consumes no further invitation.
	code, out, errs := f.run("repair")
	f.assertNoSecrets(out, errs)
	if f.hits.Load() != 1 {
		t.Fatalf("repair made %d enrol request(s), want still 1", f.hits.Load())
	}
	records = f.enrollments()
	if len(records) != 1 || records[0].IntentID != r.IntentID || records[0].State != claudeEnrollmentPending {
		t.Fatalf("repair disturbed the saved intent: %+v", records)
	}
	// Off Linux, root evidence stops earlier and by design: the platform is
	// not qualified for session ownership, so the intent stays pending on the
	// legacy path with that reason instead.
	pendingReason := strings.Contains(out+errs, "not a child of the claimed claude process") || strings.Contains(out+errs, "delivery runtime") ||
		runtime.GOOS != "linux" && strings.Contains(out+errs, errClaudeUnsupported.Error())
	if !pendingReason {
		t.Fatalf("repair lost the pending reason: stdout=%q stderr=%q", out, errs)
	}
	if code == 0 {
		t.Fatalf("repair reported success while setup is pending: %q", out)
	}

	// Unmanaged settings keep every byte, including the installed Vecnet
	// command's --auto-ack and MCP options and the foreign command.
	if after := f.settingsBytes(); !bytes.Equal(settingsBefore, after) {
		t.Fatalf("settings changed:\nbefore: %s\nafter:  %s", settingsBefore, after)
	}
	if !bytes.Contains(f.settingsBytes(), []byte("--auto-ack --mcp vecnet-p")) {
		t.Fatal("installed vecnet command lost its --auto-ack/MCP options")
	}
	if !bytes.Contains(f.settingsBytes(), []byte("notify-send foreign-marker")) {
		t.Fatal("foreign hook command was rewritten")
	}
}

func TestClaudeSetupReplacementProfileStaysUntouched(t *testing.T) {
	f := newClaudeSetupFixture(t)
	f.enroll()
	intent := f.enrollments()[0].IntentID
	hub := f.enrollments()[0].Hub
	settingsBefore := f.settingsBytes()

	// Replace the profile: same identity, rotated credential, no committed
	// enrollment; add another profile with unrelated authority.
	if err := saveConfig(f.config, Config{Profiles: map[string]Profile{
		"scout": {Hub: hub, Secret: "vn_peer_ROTATEDCREDENTIAL0123456789abcdef", PeerID: claudeTestPeerID},
		"q":     {Hub: hub, Secret: "vn_peer_other_credential_0123456789", PeerID: "peer_00000000000000000000000043"},
	}}); err != nil {
		t.Fatal(err)
	}
	configBefore := f.configBytes()

	// Repair must refuse the saved intent (superseded) without consuming
	// another invitation, adopting the replacement identity, or touching
	// the other profile, the config bytes or any settings.
	_, out, errs := f.run("repair")
	f.assertNoSecrets(out, errs)
	if f.hits.Load() != 1 {
		t.Fatalf("repair made %d enrol request(s), want still 1", f.hits.Load())
	}
	records := f.enrollments()
	if len(records) != 1 || records[0].IntentID != intent || records[0].State != claudeEnrollmentSuperseded {
		t.Fatalf("replaced profile was not superseded: %+v", records)
	}
	if after := f.configBytes(); !bytes.Equal(configBefore, after) {
		t.Fatalf("repair rewrote the config:\nbefore: %s\nafter:  %s", configBefore, after)
	}
	if after := f.settingsBytes(); !bytes.Equal(settingsBefore, after) {
		t.Fatal("repair rewrote unmanaged settings")
	}
	if rows, err := f.app().readClaudeBindings(); err != nil || len(rows) != 0 {
		t.Fatalf("superseded repair registered something: %v %+v", err, rows)
	}
}

// TestClaudeRuntimeSnapshotRedaction covers the private runtime-snapshot
// child's redaction (parked from task 3, extended in task 7): opaque keys,
// generations, passive states, the admission booleans and the hub/profile
// coordinates the host's own scoped children need. The root's private
// coordinates — config root, session id, socket path, process identity and
// build evidence — never leave the registry.
func TestClaudeRuntimeSnapshotRedaction(t *testing.T) {
	d, err := canonicalCodexPath(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	env := map[string]string{
		"HOME":            d,
		"VECNET_CONFIG":   filepath.Join(d, "v.toml"),
		"XDG_CONFIG_HOME": filepath.Join(d, "xdg"),
		"XDG_CACHE_HOME":  filepath.Join(d, "cache"),
	}
	// Every stored path is absolute on this platform (the registry refuses
	// anything else) and carries a marker that must never reach the output.
	configPath := filepath.Join(d, "SNAPSHOT-REDACT-CONFIG", "vecnet.toml")
	configRoot := filepath.Join(d, "SNAPSHOT-REDACT-CONFIG-ROOT")
	socketPath := filepath.Join(d, "vecnet-SNAPSHOT-REDACT-SOCKET.sock")
	scope := claudeScope{
		ConfigPath:  configPath,
		Profile:     "p",
		HubIdentity: claudeTestHubPin,
		HubEndpoint: "https://hub.example.com",
		Caller:      "peer_A",
		Recipient:   "peer_A",
	}
	// Parked build evidence on the stored row must not leak either.
	digest := strings.Repeat("5e", 32)
	doc := `{"version":2,"bindings":[{"config_path":` + jsonString(scope.ConfigPath) +
		`,"profile":"p","hub_identity":` + jsonString(claudeTestHubPin) + `,"hub_endpoint":"https://hub.example.com","caller":"peer_A","recipient":"peer_A",` +
		`"config_root":` + jsonString(configRoot) + `,"session_id":"S-SNAPSHOT-REDACT-SESSION","socket_path":` + jsonString(socketPath) + `,` +
		`"process_start":"12","boot_id":"01234567-89ab-cdef-0123-456789abcdef","build":"SNAPSHOT-REDACT-BUILD","exe_sha256":"` + digest + `","exe_identity":"77:88:99:11:22","pid":4242,"uid":1000,` +
		`"generation":"0123456789abcdef0123456789abcdef","updated_at":"2026-11-19T00:00:00Z"}],"opt_outs":[],"canary":[]}`
	claudeSeedLocalState(t, env, claudeBindingsName, []byte(doc))
	var out, errs bytes.Buffer
	code := Run([]string{"claude", "runtime-snapshot"}, strings.NewReader(""), &out, &errs, func(k string) string { return env[k] })
	if code != 0 {
		t.Fatalf("runtime-snapshot exit %d: %s", code, errs.String())
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal([]byte(out.String()), &top); err != nil || len(top) != 2 || top["version"] == nil || top["bindings"] == nil {
		t.Fatalf("snapshot has unexpected top-level fields: %q", out.String())
	}
	var rows []map[string]json.RawMessage
	if err := json.Unmarshal(top["bindings"], &rows); err != nil || len(rows) != 1 {
		t.Fatalf("snapshot rows have unexpected fields: %q", out.String())
	}
	// A row is exactly the opaque key, the generation, the passive state
	// (plus its fixed reason when the root is not live), the admission
	// booleans and the scope coordinates.
	if len(rows[0]) != 6 && len(rows[0]) != 7 {
		t.Fatalf("snapshot rows have unexpected fields: %q", out.String())
	}
	if string(rows[0]["binding_key"]) != jsonString(claudeBindingKey(scope)) {
		t.Fatalf("binding key %s is not the opaque scope key", rows[0]["binding_key"])
	}
	if string(rows[0]["generation"]) != jsonString("0123456789abcdef0123456789abcdef") {
		t.Fatalf("generation missing: %q", out.String())
	}
	// Linux validates the root and finds no live evidence (blocked); every
	// other platform stays on the legacy path and says so (unsupported).
	wantState := "blocked"
	if runtime.GOOS != "linux" {
		wantState = "unsupported"
	}
	if string(rows[0]["state"]) != jsonString(wantState) {
		t.Fatalf("a root with no live evidence must report %s: %q", wantState, out.String())
	}
	// The task-7 admission gate and the host's child-spawn coordinates.
	var admission struct {
		Live             bool   `json:"live"`
		Policy           bool   `json:"policy"`
		VersionQualified bool   `json:"version_qualified"`
		BuildCheck       string `json:"build_check"`
	}
	// Build gating is parked: version_qualified holds and build_check says
	// no build was verified; the dead root is what keeps it unadmitted.
	if err := json.Unmarshal(rows[0]["admission"], &admission); err != nil || admission.Policy != true ||
		admission.Live != false || admission.VersionQualified != true || admission.BuildCheck != "disabled" {
		t.Fatalf("admission evidence missing or wrong: %q", rows[0]["admission"])
	}
	var scopeRow struct {
		Profile     string `json:"profile"`
		HubEndpoint string `json:"hub_endpoint"`
		Caller      string `json:"caller"`
		Recipient   string `json:"recipient"`
	}
	if err := json.Unmarshal(rows[0]["scope"], &scopeRow); err != nil || scopeRow.Profile != "p" ||
		scopeRow.HubEndpoint != "https://hub.example.com" || scopeRow.Caller != "peer_A" || scopeRow.Recipient != "peer_A" {
		t.Fatalf("scope coordinates missing or wrong: %q", rows[0]["scope"])
	}
	// The root's private coordinates never leave the registry.
	for _, marker := range []string{"SNAPSHOT-REDACT", jsonString(d)[1 : len(jsonString(d))-1], "4242", "vecnet.toml", digest, "77:88:99"} {
		if strings.Contains(out.String(), marker) {
			t.Fatalf("snapshot leaked %q: %s", marker, out.String())
		}
	}
}

func jsonString(s string) string {
	raw, _ := json.Marshal(s)
	return string(raw)
}
