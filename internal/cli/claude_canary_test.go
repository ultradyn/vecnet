package cli

// Delivery Phase 2, task 3: the per-profile native canary opt-in. With build
// gating parked this durable record is the only production gate on a native
// write, so every test here pins a fail-closed edge: one exact peer scope per
// user, no delegated or principal holder, no alias bypass, never clearing an
// opt-out, and no hub contact from either verb. The live-root status mode and
// the deliver/retry refusals live in claude_canary_linux_test.go.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const (
	claudeTestPeerB = "peer_00000000000000000000000043"
	claudeTestPrn   = "prn_00000000000000000000000042"
)

// claudeCanaryFixture is a private HOME whose config reaches one loopback
// hub (which counts every request, so the verbs can prove they never call
// it) through two URL aliases, plus a second recipient, a principal and a
// legacy-shaped caller id.
type claudeCanaryFixture struct {
	home string
	env  map[string]string
	hub  string
	hits *atomic.Int32
}

func newClaudeCanaryFixture(t *testing.T) *claudeCanaryFixture {
	t.Helper()
	f := &claudeCanaryFixture{home: t.TempDir(), hits: &atomic.Int32{}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.hits.Add(1)
		http.NotFound(w, r)
	}))
	t.Cleanup(server.Close)
	f.hub = server.URL
	alias := strings.Replace(f.hub, "127.0.0.1", "localhost", 1)
	if alias == f.hub {
		t.Fatalf("fixture hub %s is not a 127.0.0.1 URL", f.hub)
	}
	config := filepath.Join(f.home, "v.toml")
	if err := saveConfig(config, Config{Profiles: map[string]Profile{
		"p":        {Hub: f.hub, HubPubkey: claudeTestHubPin, Secret: "vn_peer_test_p", PeerID: claudeTestPeerID},
		"alias":    {Hub: alias, HubPubkey: claudeTestHubPin, Secret: "vn_peer_test_alias", PeerID: claudeTestPeerID},
		"r":        {Hub: f.hub, HubPubkey: claudeTestHubPin, Secret: "vn_peer_test_r", PeerID: claudeTestPeerB},
		"operator": {Hub: f.hub, HubPubkey: claudeTestHubPin, Secret: "vn_prn_test", EnrollmentCaller: claudeTestPrn},
		"loose":    {Hub: f.hub, HubPubkey: claudeTestHubPin, Secret: "vn_peer_test_loose", PeerID: "peer_A"},
		"unpinned": {Hub: "https://unpinned.example.com", Secret: "vn_peer_test_u", PeerID: claudeTestPeerID},
	}}); err != nil {
		t.Fatal(err)
	}
	f.env = map[string]string{
		"HOME":            f.home,
		"VECNET_CONFIG":   config,
		"XDG_CONFIG_HOME": filepath.Join(f.home, "xdg"),
		"XDG_CACHE_HOME":  filepath.Join(f.home, "cache"),
	}
	return f
}

func (f *claudeCanaryFixture) run(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	return claudeStatusRun(t, f.env, args...)
}

// document reads the stored binding document (an absent one is empty).
func (f *claudeCanaryFixture) document(t *testing.T) claudeBindingsFile {
	t.Helper()
	raw, err := os.ReadFile(claudeStatePath(f.home, claudeBindingsName))
	if os.IsNotExist(err) {
		return claudeDocumentEmpty()
	}
	if err != nil {
		t.Fatal(err)
	}
	doc, err := claudeDocumentDecode(raw)
	if err != nil {
		t.Fatalf("stored binding document does not decode: %v", err)
	}
	return doc
}

func (f *claudeCanaryFixture) documentBytes(t *testing.T) []byte {
	t.Helper()
	raw, err := os.ReadFile(claudeStatePath(f.home, claudeBindingsName))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return raw
}

func (f *claudeCanaryFixture) assertNoHub(t *testing.T) {
	t.Helper()
	if n := f.hits.Load(); n != 0 {
		t.Fatalf("the canary verbs made %d hub request(s)", n)
	}
}

func (f *claudeCanaryFixture) refuse(t *testing.T, want string, args ...string) {
	t.Helper()
	before := f.documentBytes(t)
	code, _, errs := f.run(t, args...)
	if code == 0 || !strings.Contains(errs, want) {
		t.Fatalf("%v: exit %d, stderr %q; want a refusal containing %q", args, code, errs, want)
	}
	if after := f.documentBytes(t); string(after) != string(before) {
		t.Fatalf("%v: a refused canary verb rewrote the binding document:\n%s\n->\n%s", args, before, after)
	}
}

func (f *claudeCanaryFixture) ok(t *testing.T, args ...string) string {
	t.Helper()
	code, out, errs := f.run(t, args...)
	if code != 0 {
		t.Fatalf("%v: exit %d: %s", args, code, errs)
	}
	return out
}

// TestClaudeCanarySingleScope pins Review Focus 5 for the canary: one record
// per user. A second recipient is refused, and so is the first recipient
// reached through another profile on a URL alias of the same pinned hub;
// the record count stays 1 and neither verb ever contacts the hub.
func TestClaudeCanarySingleScope(t *testing.T) {
	f := newClaudeCanaryFixture(t)
	f.ok(t, "--net", "p", "claude", "canary", "enable")
	doc := f.document(t)
	if len(doc.Canary) != 1 || doc.Canary[0].Recipient != claudeTestPeerID || doc.Canary[0].HubIdentity != claudeTestHubPin ||
		doc.Canary[0].Profile != "p" || doc.Canary[0].As != "" {
		t.Fatalf("enable did not store exactly the addressed scope: %+v", doc.Canary)
	}
	// Repeating the exact enable is idempotent and rewrites nothing.
	before := f.documentBytes(t)
	f.ok(t, "--net", "p", "claude", "canary", "enable")
	if string(f.documentBytes(t)) != string(before) {
		t.Fatal("a repeated enable rewrote the document")
	}

	f.refuse(t, "another recipient holds the canary; disable it there first", "--net", "r", "claude", "canary", "enable")
	f.refuse(t, "held through another profile or hub alias", "--net", "alias", "claude", "canary", "enable")
	if n := len(f.document(t).Canary); n != 1 {
		t.Fatalf("canary record count = %d, want 1", n)
	}

	// Disabling another recipient is an idempotent no-op that keeps the
	// holder's record; disabling through the alias withdraws it (the same
	// recipient), and a second disable is idempotent.
	f.ok(t, "--net", "r", "claude", "canary", "disable")
	if n := len(f.document(t).Canary); n != 1 {
		t.Fatalf("disable for another recipient removed the holder's record (count %d)", n)
	}
	f.ok(t, "--net", "alias", "claude", "canary", "disable")
	if n := len(f.document(t).Canary); n != 0 {
		t.Fatalf("disable through the alias left %d record(s)", n)
	}
	f.ok(t, "--net", "p", "claude", "canary", "disable")
	// Only now may the other recipient take it.
	f.ok(t, "--net", "r", "claude", "canary", "enable")
	if doc := f.document(t); len(doc.Canary) != 1 || doc.Canary[0].Recipient != claudeTestPeerB {
		t.Fatalf("the freed canary did not move to the second recipient: %+v", doc.Canary)
	}
	// A hand-written two-record document is refused by every reader.
	two := f.document(t)
	first := two.Canary[0]
	first.Recipient, first.Caller, first.Profile = claudeTestPeerID, claudeTestPeerID, "p"
	two.Canary = append(two.Canary, first)
	if err := claudeDocumentValidate(two); err == nil {
		t.Fatal("a document with two canary records validated")
	}
	f.assertNoHub(t)
}

// TestClaudeCanaryRefusesDelegatedAndPrincipal pins the scope contract: no
// --as delegation (even an empty one), no principal credential and no
// caller that is not a well-formed peer id may hold the canary, and a
// pinned hub identity is required. A stored record of any such shape is
// refused by the document validator, so no reader can ever admit one.
func TestClaudeCanaryRefusesDelegatedAndPrincipal(t *testing.T) {
	f := newClaudeCanaryFixture(t)
	scopeCopy := errClaudeCanaryScope.Error()
	if scopeCopy != "the first canary accepts peer credentials and a single exact recipient scope only" {
		t.Fatalf("the scope refusal copy drifted: %q", scopeCopy)
	}
	f.refuse(t, scopeCopy, "--net", "p", "--as", claudeTestPeerB, "claude", "canary", "enable")
	f.refuse(t, scopeCopy, "--net", "p", "--as=", "claude", "canary", "enable")
	f.refuse(t, scopeCopy, "--net", "operator", "claude", "canary", "enable")
	f.refuse(t, scopeCopy, "--net", "loose", "claude", "canary", "enable")
	f.refuse(t, errClaudeHubUnpinned.Error(), "--net", "unpinned", "claude", "canary", "enable")
	f.refuse(t, "requires the addressed profile", "claude", "canary", "enable")
	f.refuse(t, "expected enable or disable", "--net", "p", "claude", "canary")
	f.refuse(t, "expected enable or disable", "--net", "p", "claude", "canary", "on")
	f.refuse(t, "takes no positional arguments", "--net", "p", "claude", "canary", "enable", "extra")
	if n := len(f.document(t).Canary); n != 0 {
		t.Fatalf("a refused enable stored %d record(s)", n)
	}

	// The programmatic entry point refuses the same shapes on its own.
	a := &app{ctx: t.Context(), stdout: os.Stdout, stderr: os.Stderr, getenv: func(k string) string { return f.env[k] }}
	base, err := a.claudeScopeForProfile("p")
	if err != nil {
		t.Fatal(err)
	}
	delegated := base
	delegated.As, delegated.Recipient = claudeTestPeerB, claudeTestPeerB
	principal := base
	principal.Caller, principal.Recipient = claudeTestPrn, claudeTestPrn
	for name, s := range map[string]claudeScope{"delegated": delegated, "principal": principal} {
		if err := a.setClaudeCanary(t.Context(), s, true); err == nil || err.Error() != scopeCopy {
			t.Fatalf("%s scope: setClaudeCanary = %v, want the fixed scope refusal", name, err)
		}
		doc := claudeDocumentEmpty()
		doc.Canary = []claudeScope{s}
		if err := claudeDocumentValidate(doc); err == nil {
			t.Fatalf("%s scope: a stored canary record of this shape validated", name)
		}
	}

	// Disable stays available for every shape and before a pin exists.
	f.ok(t, "--net", "unpinned", "claude", "canary", "disable")
	f.ok(t, "--net", "operator", "claude", "canary", "disable")
	f.assertNoHub(t)
}

// TestClaudeCanaryNeverClearsOptOut pins the opt-out contract: a canary
// enable after an addressed disable (or under the global disable) leaves the
// opt-out stored, the admission's policy false and the status mode
// opted_out. Only explicit claude enable clears it.
func TestClaudeCanaryNeverClearsOptOut(t *testing.T) {
	f := newClaudeCanaryFixture(t)
	a := &app{ctx: t.Context(), stdout: os.Stdout, stderr: os.Stderr, getenv: func(k string) string { return f.env[k] }}
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

	f.ok(t, "--net", "p", "claude", "disable")
	out := f.ok(t, "--net", "p", "claude", "canary", "enable")
	if !strings.Contains(out, "a durable opt-out still applies") {
		t.Fatalf("enable under an opt-out did not say the opt-out still applies: %q", out)
	}
	doc := f.document(t)
	if len(doc.OptOuts) != 1 || !claudeSameRecipient(doc.OptOuts[0], scope) || len(doc.Canary) != 1 {
		t.Fatalf("canary enable disturbed the opt-out or stored no record: %+v", doc)
	}
	adm := claudeAdmissionEvidence(doc.Bindings[0], doc.OptOuts, doc.Canary, false)
	if adm.Policy || !adm.Canary {
		t.Fatalf("an opted-out recipient's admission = %+v, want policy:false canary:true", adm)
	}
	status := claudeStatusJSON(t, f.env, "--net", "p", "claude", "status", "--json")
	if status.Mode != "opted_out" || status.Owner != "none" || !status.CanaryOptIn {
		t.Fatalf("status under opt-out + canary = %+v, want opted_out with the opt-in reported", status)
	}
	// Disable, then enable again, still never clears it.
	f.ok(t, "--net", "p", "claude", "canary", "disable")
	f.ok(t, "--net", "p", "claude", "canary", "enable")
	if doc := f.document(t); len(doc.OptOuts) != 1 {
		t.Fatalf("a canary re-enable cleared the opt-out: %+v", doc.OptOuts)
	}
	if status := claudeStatusJSON(t, f.env, "--net", "p", "claude", "status", "--json"); status.Mode != "opted_out" {
		t.Fatalf("status after re-enable = %+v, want opted_out", status)
	}

	// The global disable wins the same way.
	f.ok(t, "--net", "p", "claude", "enable")
	f.ok(t, "claude", "disable")
	f.ok(t, "--net", "p", "claude", "canary", "enable")
	if status := claudeStatusJSON(t, f.env, "--net", "p", "claude", "status", "--json"); status.Mode != "opted_out" || !status.CanaryOptIn {
		t.Fatalf("status under the global disable = %+v, want opted_out with the opt-in reported", status)
	}
	if settings, err := a.claudeSettings(); err != nil || !settings.Disabled {
		t.Fatalf("canary enable cleared the global disable: %+v %v", settings, err)
	}
	f.assertNoHub(t)
}

// TestClaudeSnapshotAdmissionCanary pins the admission flag: true only for a
// binding of a peer credential's own inbox matched by the single stored
// record through claudeSameRecipient (a URL alias matches); false with no
// record, another recipient's record, a delegated or principal binding, or
// more than one record. The runtime snapshot carries it.
func TestClaudeSnapshotAdmissionCanary(t *testing.T) {
	f := newClaudeCanaryFixture(t)
	a := &app{ctx: t.Context(), stdout: os.Stdout, stderr: os.Stderr, getenv: func(k string) string { return f.env[k] }}
	scope, err := a.claudeScopeForProfile("p")
	if err != nil {
		t.Fatal(err)
	}
	alias, err := a.claudeScopeForProfile("alias")
	if err != nil {
		t.Fatal(err)
	}
	other, err := a.claudeScopeForProfile("r")
	if err != nil {
		t.Fatal(err)
	}
	binding := claudeBinding{claudeScope: scope}
	delegated := binding
	delegated.As, delegated.Recipient = claudeTestPeerB, claudeTestPeerB
	principal := binding
	principal.Caller, principal.Recipient = claudeTestPrn, claudeTestPrn
	cases := []struct {
		name   string
		b      claudeBinding
		canary []claudeScope
		want   bool
	}{
		{"no record", binding, nil, false},
		{"empty record list", binding, []claudeScope{}, false},
		{"exact record", binding, []claudeScope{scope}, true},
		{"alias record", binding, []claudeScope{alias}, true},
		{"another recipient's record", binding, []claudeScope{other}, false},
		{"two records", binding, []claudeScope{scope, scope}, false},
		{"delegated binding", delegated, []claudeScope{scope}, false},
		{"delegated binding with its own record", delegated, []claudeScope{delegated.claudeScope}, false},
		{"principal binding", principal, []claudeScope{principal.claudeScope}, false},
	}
	for _, c := range cases {
		if got := claudeAdmissionEvidence(c.b, nil, c.canary, false).Canary; got != c.want {
			t.Fatalf("%s: canary admission = %t, want %t", c.name, got, c.want)
		}
	}

	// The runtime snapshot carries the flag from the stored record.
	stored := claudeBinding{claudeScope: scope, claudeRoot: claudeRoot{
		ConfigRoot: filepath.Join(f.home, "root"), SessionID: "S1", ProcessStart: "12",
		BootID: "01234567-89ab-cdef-0123-456789abcdef", PID: 4242, UID: 1000,
	}, Generation: "0123456789abcdef0123456789abcdef", UpdatedAt: time.Now().UTC()}
	rows, err := json.Marshal(claudeBindingsFile{Version: 2, Bindings: []claudeBinding{stored}, OptOuts: []claudeScope{}, Canary: []claudeScope{}})
	if err != nil {
		t.Fatal(err)
	}
	claudeSeedLocalState(t, f.env, claudeBindingsName, rows)
	snapshotCanary := func() bool {
		t.Helper()
		var snap struct {
			Bindings []struct {
				Admission map[string]json.RawMessage `json:"admission"`
			} `json:"bindings"`
		}
		out := f.ok(t, "claude", "runtime-snapshot")
		if err := json.Unmarshal([]byte(out), &snap); err != nil || len(snap.Bindings) != 1 {
			t.Fatalf("runtime-snapshot output: %q (%v)", out, err)
		}
		raw, ok := snap.Bindings[0].Admission["canary"]
		if !ok {
			t.Fatalf("the snapshot admission carries no canary flag: %q", out)
		}
		return string(raw) == "true"
	}
	if snapshotCanary() {
		t.Fatal("the snapshot admitted the canary with no record")
	}
	f.ok(t, "--net", "p", "claude", "canary", "enable")
	if !snapshotCanary() {
		t.Fatal("the snapshot did not carry the stored canary opt-in")
	}
	f.ok(t, "--net", "p", "claude", "canary", "disable")
	if snapshotCanary() {
		t.Fatal("the snapshot kept the canary after disable")
	}
	f.assertNoHub(t)
}

// TestClaudeStatusReportsCanaryOptInAndBuildCheck pins the passive status
// fields: canary_opt_in reflects the record alone, build_check states the
// parked build gate honestly, and without a live host the mode stays
// legacy whatever the record says.
func TestClaudeStatusReportsCanaryOptInAndBuildCheck(t *testing.T) {
	f := newClaudeCanaryFixture(t)
	status := claudeStatusJSON(t, f.env, "--net", "p", "claude", "status", "--json")
	if status.CanaryOptIn || status.BuildCheck != "disabled" || status.Mode != "legacy" {
		t.Fatalf("baseline status = %+v", status)
	}
	f.ok(t, "--net", "p", "claude", "canary", "enable")
	status = claudeStatusJSON(t, f.env, "--net", "p", "claude", "status", "--json")
	if !status.CanaryOptIn || status.Mode != "legacy" || status.Owner != "none" {
		t.Fatalf("opted in with no live host = %+v, want legacy with canary_opt_in", status)
	}
	if alias := claudeStatusJSON(t, f.env, "--net", "alias", "claude", "status", "--json"); !alias.CanaryOptIn {
		t.Fatalf("the alias profile does not see the same recipient's opt-in: %+v", alias)
	}
	if other := claudeStatusJSON(t, f.env, "--net", "r", "claude", "status", "--json"); other.CanaryOptIn {
		t.Fatalf("another recipient reports the canary opt-in: %+v", other)
	}
	out := f.ok(t, "--net", "p", "claude", "status")
	for _, want := range []string{"mode: legacy", "canary_opt_in: true", "build_check: disabled"} {
		if !strings.Contains(out, want) {
			t.Fatalf("human status missing %q: %q", want, out)
		}
	}
	f.assertNoHub(t)
}
