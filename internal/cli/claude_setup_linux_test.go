//go:build linux

package cli

// Task 4 (claude delivery foundation): enrollment and hook wiring into
// passive registration. Hook fixtures run inside real children of the
// claimed Claude process (the task 3 helper tree), and every fixture
// asserts the registration paths touch neither the hub nor the messaging
// socket.

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// claudeHookEnv is the full host-tree evidence of tree tr for session.
func (tr *claudeTree) claudeHookEnv(session string) map[string]string {
	return map[string]string{
		"CLAUDECODE":                   "1",
		"CLAUDE_CONFIG_DIR":            tr.configRoot,
		"CLAUDE_PID":                   strconv.Itoa(tr.pid),
		"CLAUDE_CODE_SESSION_ID":       session,
		"CLAUDE_CODE_MESSAGING_SOCKET": tr.socket,
	}
}

// sessionStartPayload is Claude Code's SessionStart hook input.
func sessionStartPayload(session string) string {
	return `{"hook_event_name":"SessionStart","session_id":"` + session + `","source":"startup","cwd":"/w"}`
}

// storedBindingCount returns the number of stored binding rows.
func (f *claudeRegFixture) storedBindingCount(t *testing.T) int {
	t.Helper()
	rows, err := f.app().readClaudeBindings()
	if err != nil {
		t.Fatal(err)
	}
	return len(rows)
}

func TestClaudeSetupOwnedSessionStartMergeIsIdempotent(t *testing.T) {
	f := newClaudeRegFixture(t)
	a := f.startTree("a")
	a.sessionRecord(a.pid, "S1", a.socket)
	env := a.claudeHookEnv("S1")

	first := a.hook(env, []string{"--profile", "p", "--event", "session-start"}, sessionStartPayload("S1"))
	if first.Stdout != "" || first.Stderr != "" {
		t.Fatalf("session-start emitted output: stdout=%q stderr=%q", first.Stdout, first.Stderr)
	}
	rows := f.storedRows(t)
	if len(rows) != 1 {
		t.Fatalf("expected one binding row, got %d", len(rows))
	}
	base := f.registryBytes()

	// The same owned SessionStart merges into the existing row: idempotent,
	// same generation, no rewrite.
	second := a.hook(env, []string{"--profile", "p", "--event", "session-start"}, sessionStartPayload("S1"))
	if second.Stdout != "" || second.Stderr != "" {
		t.Fatalf("second session-start emitted output: stdout=%q stderr=%q", second.Stdout, second.Stderr)
	}
	if after := f.registryBytes(); !bytes.Equal(base, after) {
		t.Fatal("idempotent session-start rewrote the registry")
	}
	rows = f.storedRows(t)
	if len(rows) != 1 || rows[0].Generation == "" {
		t.Fatalf("merge disturbed the binding row: %+v", rows)
	}
	f.assertPassive()
}

func TestClaudeSetupSessionStartEmitsNoContextOrNetwork(t *testing.T) {
	f := newClaudeRegFixture(t)
	a := f.startTree("a")
	a.sessionRecord(a.pid, "S1", a.socket)
	args := []string{"--profile", "p", "--event", "session-start"}

	// An owned SessionStart registers passively: no context, no
	// diagnostics, no hub request, no socket connect.
	out := a.hook(a.claudeHookEnv("S1"), args, sessionStartPayload("S1"))
	if out.Stdout != "" || out.Stderr != "" {
		t.Fatalf("session-start emitted output: stdout=%q stderr=%q", out.Stdout, out.Stderr)
	}
	if n := f.storedBindingCount(t); n != 1 {
		t.Fatalf("owned session-start did not register: %d rows", n)
	}
	f.assertPassive()
	base := f.registryBytes()

	// A child agent's SessionStart (hook payload agent_id) never registers
	// its parent session and stays silent.
	agent := a.hook(a.claudeHookEnv("S1"), args, `{"hook_event_name":"SessionStart","session_id":"S1","agent_id":"agent-7","source":"resume"}`)
	if agent.Stdout != "" || agent.Stderr != "" {
		t.Fatalf("child agent session-start emitted output: %q / %q", agent.Stdout, agent.Stderr)
	}

	// A bare terminal run (no hook payload at all) claims nothing.
	terminal := a.hook(a.claudeHookEnv("S1"), args, "")
	if terminal.Stdout != "" || terminal.Stderr != "" {
		t.Fatalf("terminal session-start emitted output: %q / %q", terminal.Stdout, terminal.Stderr)
	}

	// A mismatched profile (the session declared another profile) stays
	// silent and registers nothing.
	mismatchEnv := a.claudeHookEnv("S1")
	mismatchEnv["VECNET_PROFILE"] = "q"
	mismatch := a.hook(mismatchEnv, args, sessionStartPayload("S1"))
	if mismatch.Stdout != "" || mismatch.Stderr != "" {
		t.Fatalf("mismatched profile session-start emitted output: %q / %q", mismatch.Stdout, mismatch.Stderr)
	}

	// Missing root evidence (no CLAUDE_PID) registers nothing.
	noPID := a.claudeHookEnv("S1")
	delete(noPID, "CLAUDE_PID")
	noPIDOut := a.hook(noPID, args, sessionStartPayload("S1"))
	if noPIDOut.Stdout != "" || noPIDOut.Stderr != "" {
		t.Fatalf("rootless session-start emitted output: %q / %q", noPIDOut.Stdout, noPIDOut.Stderr)
	}

	// Missing config evidence (profile absent from the present config)
	// registers nothing and consumes no unrelated authority.
	absent := a.hook(a.claudeHookEnv("S1"), []string{"--profile", "nope", "--event", "session-start"}, sessionStartPayload("S1"))
	if absent.Stdout != "" || absent.Stderr != "" {
		t.Fatalf("absent profile session-start emitted output: %q / %q", absent.Stdout, absent.Stderr)
	}
	if after := f.registryBytes(); !bytes.Equal(base, after) {
		t.Fatal("refused session-start runs changed the registry")
	}
	if n := f.storedBindingCount(t); n != 1 {
		t.Fatalf("refused runs changed the registry: %d rows", n)
	}
	f.assertPassive()
}

// claudeLegacyHub serves the identity and event pages the legacy
// user-prompt path needs, and counts every request.
func claudeLegacyHub(t *testing.T) (string, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		switch r.URL.Path {
		case "/v1/peers/me":
			fmt.Fprint(w, `{"kind":"peer","peer_id":"peer_A"}`)
		case "/v1/events":
			switch r.URL.Query().Get("after") {
			case "head":
				fmt.Fprint(w, `{"events":[],"next_cursor":"e1.0"}`)
			case "":
				fmt.Fprint(w, `{"events":[{"cursor":"e1.1","type":"delivery","data":{"delivery_id":"dly_1","message":{"message_id":"msg_1","thread_id":"thr_1","sender":"peer_h","kind":"message","content":{"text":"first root prompt marker"}}}}],"next_cursor":"e1.1"}`)
			default:
				fmt.Fprint(w, `{"events":[],"next_cursor":"e1.1"}`)
			}
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv.URL, &hits
}

// claudeLegacyEnv is a fresh fixture environment whose profile p points at
// hubURL and carries the stored caller witness registration requires.
func claudeLegacyEnv(t *testing.T, hubURL string) *claudeRegFixture {
	t.Helper()
	f := newClaudeRegFixture(t)
	if err := saveConfig(f.config, Config{Profiles: map[string]Profile{
		"p": {Hub: hubURL, HubPubkey: claudeTestHubPin, Secret: "vn_peer_test", PeerID: "peer_A"},
	}}); err != nil {
		t.Fatal(err)
	}
	return f
}

func TestClaudeSetupFirstRootPromptKeepsLegacyOutput(t *testing.T) {
	// Baseline: the ordinary legacy user-prompt path with no Claude host
	// evidence in the environment.
	baseHub, baseHits := claudeLegacyHub(t)
	baseFixture := claudeLegacyEnv(t, baseHub)
	baseApp := baseFixture.app()
	baseApp.inHook = true
	baseApp.stdin = strings.NewReader(`{"hook_event_name":"UserPromptSubmit","session_id":"S1"}`)
	var baseOut, baseErr bytes.Buffer
	baseApp.stdout, baseApp.stderr = &baseOut, &baseErr
	baseApp.hookRun([]string{"--profile", "p", "--event", "user-prompt"}, hookNow, func() {}, func(data []byte) error {
		_, err := baseOut.Write(data)
		return err
	}, 0)

	// Evidence run: the same prompt inside the Claude tree, so the verified
	// first prompt updates registration before the ordinary legacy path.
	hub, hits := claudeLegacyHub(t)
	f := claudeLegacyEnv(t, hub)
	a := f.startTree("a")
	a.sessionRecord(a.pid, "S1", a.socket)
	env := a.claudeHookEnv("S1")
	for k, v := range f.env {
		env[k] = v
	}
	out := a.hook(env, []string{"--profile", "p", "--event", "user-prompt"}, `{"hook_event_name":"UserPromptSubmit","session_id":"S1"}`)

	if out.Stdout != baseOut.String() {
		t.Fatalf("legacy prompt output changed:\nwith registration: %q\nbaseline: %q", out.Stdout, baseOut.String())
	}
	if out.Stderr != baseErr.String() {
		t.Fatalf("legacy prompt stderr changed:\nwith registration: %q\nbaseline: %q", out.Stderr, baseErr.String())
	}
	if !strings.Contains(out.Stdout, "1 unacked deliveries waiting") {
		t.Fatalf("legacy delivery missing from prompt output: %q", out.Stdout)
	}
	// Registration was updated by the same prompt, passively.
	rows := f.storedRows(t)
	if len(rows) != 1 || rows[0].SessionID != "S1" || rows[0].PID != a.pid {
		t.Fatalf("first prompt did not register the root: %+v", rows)
	}
	if extra := hits.Load() - baseHits.Load(); extra != 0 {
		t.Fatalf("registration changed the hub requests: %d extra", extra)
	}
	f.assertPassive()
}

// TestClaudeSetupReconcileGlobalDisableIsStableDisabled covers the task 5
// review finding: reconciliation that hits the durable global disable must
// settle the enrollment record in the stable disabled phase (matching the
// hook path's claudeSetupResultFor mapping), not leave it retryable pending.
func TestClaudeSetupReconcileGlobalDisableIsStableDisabled(t *testing.T) {
	f := newClaudeRegFixture(t)
	a := f.startTree("a")
	a.sessionRecord(a.pid, "S1", a.socket)

	// One committed enrollment record for profile p at this config, exactly
	// as the post-redemption commit leaves it.
	intent := "0123456789abcdef0123456789abcdef"
	secret := "vn_peer_test"
	if err := saveConfig(f.config, Config{Profiles: map[string]Profile{
		"p": {Hub: f.hub, HubPubkey: claudeTestHubPin, Secret: secret, PeerID: "peer_A",
			EnrollmentIntent: intent, EnrollmentCredential: hookDigest([]byte(secret)),
			EnrollmentHub: f.hub, EnrollmentCaller: "peer_A"},
	}}); err != nil {
		t.Fatal(err)
	}
	record := claudeEnrollment{
		IntentID: intent, Hub: f.hub, Caller: "peer_A",
		Config: f.config, Profile: "p", ConfigRoot: a.configRoot,
		State: claudeEnrollmentPending, UpdatedAt: time.Now().UTC(),
	}

	// The durable global disable refuses the registration inside
	// reconciliation; the record must settle disabled, not stay pending.
	if err := f.app().setClaudeGlobalDisable(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	result := a.reconcile(record, a.claudeHookEnv("S1"))
	if result.State != claudeEnrollmentDisabled {
		t.Fatalf("reconcile under global disable settled %q (%s), want %q", result.State, result.Reason, claudeEnrollmentDisabled)
	}
	if !strings.Contains(result.Reason, "disabled globally") {
		t.Fatalf("reconcile lost the disable reason: %q", result.Reason)
	}
	records, err := f.app().claudeEnrollments()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].State != claudeEnrollmentDisabled || claudeEnrollmentRetryable(records[0].State) {
		t.Fatalf("saved enrollment record is not stably disabled: %+v", records)
	}
	if n := f.storedBindingCount(t); n != 0 {
		t.Fatalf("globally disabled reconcile registered %d rows", n)
	}
	f.assertPassive()
}

// TestClaudeSetupNoVersionEnvRegisters pins in-session enrollment without
// CLAUDE_CODE_VERSION: the intent is prepared and reconciliation registers
// the exact root (build gating is parked, so no build is observed).
func TestClaudeSetupNoVersionEnvRegisters(t *testing.T) {
	f := newClaudeRegFixture(t)
	a := f.startTree("a")
	a.sessionRecord(a.pid, "S1", a.socket)
	env := a.claudeHookEnv("S1")
	if _, set := env["CLAUDE_CODE_VERSION"]; set {
		t.Fatal("the hook fixture still exports CLAUDE_CODE_VERSION")
	}
	for k, v := range f.env {
		env[k] = v
	}
	prep := &app{ctx: context.Background(), stdout: io.Discard, stderr: io.Discard, getenv: func(k string) string { return env[k] }}
	intent, err := prep.prepareClaudeEnrollment("p")
	if err != nil || intent == nil {
		t.Fatalf("in-session enrollment without the version export was refused: %v", err)
	}

	secret := "vn_peer_test"
	if err := saveConfig(f.config, Config{Profiles: map[string]Profile{
		"p": {Hub: f.hub, HubPubkey: claudeTestHubPin, Secret: secret, PeerID: "peer_A",
			EnrollmentIntent: intent.IntentID, EnrollmentCredential: hookDigest([]byte(secret)),
			EnrollmentHub: normalizeHub(f.hub), EnrollmentCaller: "peer_A"},
	}}); err != nil {
		t.Fatal(err)
	}
	record := *intent
	record.Hub, record.Caller, record.State = normalizeHub(f.hub), "peer_A", claudeEnrollmentPending
	f.runtimeOK = true
	result := a.reconcile(record, a.claudeHookEnv("S1"))
	if result.State != claudeEnrollmentRegistered {
		t.Fatalf("reconcile without the version export settled %q: %s", result.State, result.Reason)
	}
	rows := f.storedRows(t)
	if len(rows) != 1 || rows[0].PID != a.pid || rows[0].HubIdentity != claudeTestHubPin || rows[0].ExeSHA256 != "" {
		t.Fatalf("reconcile did not register the exact root (without build evidence): %+v", rows)
	}
	f.assertPassive()
}

// TestClaudeSetupSlowDigestLeavesHookOutput pins Review Focus 2 under the
// parked build gating: even with a hash that would block forever, the
// user-prompt hook output is byte-identical to the no-evidence baseline,
// because no hook ever reaches the hash, and the root still registers.
func TestClaudeSetupSlowDigestLeavesHookOutput(t *testing.T) {
	prompt := `{"hook_event_name":"UserPromptSubmit","session_id":"S1"}`
	baseHub, _ := claudeLegacyHub(t)
	baseFixture := claudeLegacyEnv(t, baseHub)
	baseApp := baseFixture.app()
	baseApp.inHook = true
	baseApp.stdin = strings.NewReader(prompt)
	var baseOut, baseErr bytes.Buffer
	baseApp.stdout, baseApp.stderr = &baseOut, &baseErr
	baseApp.hookRun([]string{"--profile", "p", "--event", "user-prompt"}, hookNow, func() {}, func(data []byte) error {
		_, err := baseOut.Write(data)
		return err
	}, 0)

	hub, _ := claudeLegacyHub(t)
	f := claudeLegacyEnv(t, hub)
	a := f.startTree("a")
	a.sessionRecord(a.pid, "S1", a.socket)
	env := a.claudeHookEnv("S1")
	for k, v := range f.env {
		env[k] = v
	}
	// The counting seam passes through to the real hash; any call at all is
	// the failure, so a slow digest cannot matter.
	out := a.hook(env, []string{"--profile", "p", "--event", "user-prompt"}, prompt)
	if out.Stdout != baseOut.String() || out.Stderr != baseErr.String() {
		t.Fatalf("registration changed the hook output:\nwith: %q / %q\nbaseline: %q / %q", out.Stdout, out.Stderr, baseOut.String(), baseErr.String())
	}
	if !strings.Contains(out.Stdout, "1 unacked deliveries waiting") {
		t.Fatalf("legacy delivery missing from prompt output: %q", out.Stdout)
	}
	if n := f.hashCalls(t); n != 0 {
		t.Fatalf("the prompt hook hashed %d time(s)", n)
	}
	if rows := f.storedRows(t); len(rows) != 1 || rows[0].ExeSHA256 != "" {
		t.Fatalf("the prompt hook did not register the root without build evidence: %+v", rows)
	}
	f.assertPassive()
}
