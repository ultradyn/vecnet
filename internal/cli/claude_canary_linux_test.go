//go:build linux

package cli

// Delivery Phase 2, task 3, against live roots: the canary status mode needs
// the durable opt-in AND the live host's v2 sidecar admission of the exact
// key at the current generation; and the deliver helper and resolve --retry
// both refuse a scope without the canary opt-in before any connect or claim
// mutation.

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestClaudeStatusCanaryModeNeedsHostAdmission pins the truthful mode: an
// opted-in, live, registered recipient stays legacy (with the reason naming
// the missing host admission) until the fresh sidecar row at the current
// generation carries native "canary"; a stale generation stays legacy, and
// so does a host marker without the opt-in. The mode is never "native".
func TestClaudeStatusCanaryModeNeedsHostAdmission(t *testing.T) {
	f := newClaudeRegFixture(t)
	a := f.startTree("a")
	a.sessionRecord(a.pid, "S1", a.socket)
	scope := f.scope("c")
	binding := mustReg(t, a.reg(scope, a.root("S1"), nil))
	key := claudeBindingKey(scope)
	host := holdClaudeStatusHost(t, f.dir)
	host.version = 2
	status := func() claudeStatusResult {
		t.Helper()
		return claudeStatusJSON(t, f.env, "--net", "c", "claude", "status", "--json")
	}

	// Live and registered, with the host marker, but not opted in: legacy.
	host.writeLive([]claudeStatusBinding{{BindingKey: key, Generation: binding.Generation, State: "registered", Native: "canary"}})
	if got := status(); got.Owner != "registered" || got.Mode != "legacy" || got.CanaryOptIn ||
		got.Reason != "the delivery host reports canary admission without an opt-in; it stays legacy" {
		t.Fatalf("a host marker without an opt-in = %+v", got)
	}

	mustCanary(t, f, "c")
	// Opted in, live and registered (build check parked), but the host has
	// not admitted the binding: legacy with the reason.
	host.writeLive([]claudeStatusBinding{{BindingKey: key, Generation: binding.Generation, State: "registered"}})
	got := status()
	if got.Owner != "registered" || got.Mode != "legacy" || !got.CanaryOptIn || got.BuildCheck != "disabled" ||
		got.Reason != "canary opted in; the delivery host has not admitted this binding" {
		t.Fatalf("opted in without host admission = %+v", got)
	}
	// With the host's admission at the current generation: canary.
	host.writeLive([]claudeStatusBinding{{BindingKey: key, Generation: binding.Generation, State: "registered", Native: "canary"}})
	got = status()
	if got.Owner != "registered" || got.Mode != "canary" || got.Reason != "" || got.Generation != binding.Generation || !got.CanaryOptIn {
		t.Fatalf("opted in with host admission = %+v, want canary", got)
	}
	code, out, errs := claudeStatusRun(t, f.env, "--net", "c", "claude", "status")
	if code != 0 || !strings.Contains(out, "mode: canary") || !strings.Contains(out, "canary_opt_in: true") {
		t.Fatalf("human canary status: %d %q %s", code, out, errs)
	}
	// A stale generation (the host admitted an older binding): legacy.
	host.writeLive([]claudeStatusBinding{{BindingKey: key, Generation: strings.Repeat("ee", 16), State: "registered", Native: "canary"}})
	if got := status(); got.Mode != "legacy" || got.Owner != "none" {
		t.Fatalf("a stale-generation host admission = %+v, want legacy", got)
	}
	// A stale heartbeat carrying the marker: legacy.
	host.write([]claudeStatusBinding{{BindingKey: key, Generation: binding.Generation, State: "registered", Native: "canary"}},
		claudeTestStale(), host.pid, host.start, claudeBootID())
	if got := status(); got.Mode != "legacy" || got.Owner != "none" {
		t.Fatalf("a stale heartbeat = %+v, want legacy", got)
	}
	// Withdrawing the opt-in drops the mode at once, whatever the host says.
	host.writeLive([]claudeStatusBinding{{BindingKey: key, Generation: binding.Generation, State: "registered", Native: "canary"}})
	if code, _, errs := claudeStatusRun(t, f.env, "--net", "c", "claude", "canary", "disable"); code != 0 {
		t.Fatalf("canary disable: %s", errs)
	}
	if got := status(); got.Mode != "legacy" || got.CanaryOptIn {
		t.Fatalf("status after canary disable = %+v, want legacy", got)
	}
	f.assertPassive()
}

// TestClaudeDeliverAndRetryRequireCanary pins the sole production gate: a
// live, policy-enabled, registered binding without the canary record is
// refused by the deliver helper before any connect (zero sink connections,
// the fixed reason), and resolve --retry refuses with the stored claim left
// byte-identical. With the record both proceed.
func TestClaudeDeliverAndRetryRequireCanary(t *testing.T) {
	env, dir, socket, key, generation, sink := deliverBindingFixtureSink(t)
	stateDir := filepath.Join(dir, ".local", "state", "vecnet", "projects")
	path := filepath.Join(stateDir, claudeBindingsName)
	withRecord, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc claudeBindingsFile
	if err := json.Unmarshal(withRecord, &doc); err != nil || len(doc.Canary) != 1 {
		t.Fatalf("fixture lacks its canary record: %v %+v", err, doc.Canary)
	}
	doc.Canary = []claudeScope{}
	without, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, without, 0o600); err != nil {
		t.Fatal(err)
	}

	code, _, errs := claudeDeliverRun(t, env, deliverBindingArgs(key, generation)...)
	if code != 5 || !strings.Contains(errs, "the recipient has not opted into the native canary") {
		t.Fatalf("deliver without the canary: exit %d %q, want 5 with the canary reason", code, errs)
	}
	// A delegated or principal record shape can never be stored to unlock it.
	for name, mutate := range map[string]func(*claudeScope){
		"delegated": func(s *claudeScope) { s.As, s.Recipient = claudeTestPeerB, claudeTestPeerB },
		"principal": func(s *claudeScope) { s.Caller, s.Recipient = claudeTestPrn, claudeTestPrn },
	} {
		bad := doc
		s := doc.Bindings[0].claudeScope
		mutate(&s)
		bad.Canary = []claudeScope{s}
		if err := claudeDocumentValidate(bad); err == nil {
			t.Fatalf("%s canary record validated", name)
		}
	}
	// Synchronous proof of "refused before connect": any connection the
	// helper made is already queued ahead of the barrier probe.
	if early := deliverSinkBarrier(t, sink, socket); len(early) != 0 {
		t.Fatalf("a refused deliver connected %d time(s): %q", len(early), early)
	}

	// resolve --retry: the same refusal, the claim unchanged. The deliver
	// fixture's HOME needs a witnessed config for the addressed profile.
	if err := saveConfig(env["VECNET_CONFIG"], Config{Profiles: map[string]Profile{
		"p": {Hub: "https://hub.example.com", HubPubkey: claudeTestHubPin, Secret: "vn_peer_test", PeerID: claudeTestPeerID},
	}}); err != nil {
		t.Fatal(err)
	}
	canonical, err := canonicalLocalSetupPath(env["VECNET_CONFIG"])
	if err != nil {
		t.Fatal(err)
	}
	// The fixture row addresses a placeholder config path; point it (and its
	// canary record) at the real one so the scope matches the profile.
	retarget := func(raw []byte) []byte {
		var d claudeBindingsFile
		if err := json.Unmarshal(raw, &d); err != nil {
			t.Fatal(err)
		}
		d.Bindings[0].ConfigPath = canonical
		for i := range d.Canary {
			d.Canary[i].ConfigPath = canonical
		}
		out, err := json.Marshal(d)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	without, withRecord = retarget(without), retarget(withRecord)
	if err := os.WriteFile(path, without, 0o600); err != nil {
		t.Fatal(err)
	}
	id := "dly_00000000000000000000000001"
	claudeDeliveryLedger(t, dir, map[string]string{
		claudeDeliveryClaimKey(id): claudeDeliveryClaimJSON(t, map[string]any{
			"scope": key, "generation": generation, "state": "uncertain", "disposition": "uncertain",
			"dispositionReason": "write_failed_or_partial",
		}),
	})
	before := claudeDeliveryKV(t, dir)[claudeDeliveryClaimKey(id)]
	retry := []string{"--net", "p", "claude", "resolve", id, "--expect-generation", generation, "--retry", "--confirm-duplicate-risk"}
	code, _, errs = claudeStatusRun(t, env, retry...)
	if code == 0 || !strings.Contains(errs, "the recipient has not opted into the native canary") {
		t.Fatalf("retry without the canary: exit %d %q", code, errs)
	}
	if after := claudeDeliveryKV(t, dir)[claudeDeliveryClaimKey(id)]; after != before {
		t.Fatalf("a refused retry changed the claim: %s -> %s", before, after)
	}

	// With the record restored both proceed.
	if err := os.WriteFile(path, withRecord, 0o600); err != nil {
		t.Fatal(err)
	}
	if code, out, errs := claudeDeliverRun(t, env, deliverBindingArgs(key, generation)...); code != 0 {
		t.Fatalf("deliver with the canary: exit %d %s %s", code, out, errs)
	}
	select {
	case frame := <-sink.frames:
		if string(frame) != deliverWantFrame {
			t.Fatalf("the admitted deliver wrote %q, want the pointer frame", frame)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the admitted deliver never reached the sink")
	}
	if extra := deliverSinkBarrier(t, sink, socket); len(extra) != 0 {
		t.Fatalf("the admitted deliver made %d extra connection(s): %q", len(extra), extra)
	}
	if code, out, errs := claudeStatusRun(t, env, retry...); code != 0 {
		t.Fatalf("retry with the canary: exit %d %s %s", code, out, errs)
	}
	if after := claudeDeliveryKV(t, dir)[claudeDeliveryClaimKey(id)]; !strings.Contains(after, `"state":"reserved"`) {
		t.Fatalf("the admitted retry did not re-arm the claim: %s", after)
	}
}

// claudeTestStale is a heartbeat time past the freshness window.
func claudeTestStale() time.Time { return time.Now().Add(-2 * claudeStatusFreshFor) }

// deliverSinkProbe is the barrier's marker frame; no helper ever writes it.
const deliverSinkProbe = "vecnet-test-sink-barrier\n"

// deliverSinkBarrier is a synchronous accept-count barrier. The sink accepts
// connections one at a time in backlog (FIFO) order, so a probe connection
// dialled now is accepted only after every connection already queued — a
// connect completes into the backlog before the dialler returns. It returns
// every frame (an empty one for a connect-then-close) that arrived before
// the probe: zero frames proves nothing connected earlier.
func deliverSinkBarrier(t *testing.T, sink *deliverSink, socket string) [][]byte {
	t.Helper()
	probe, err := net.DialTimeout("unix", socket, 2*time.Second)
	if err != nil {
		t.Fatalf("barrier probe dial: %v", err)
	}
	if _, err := probe.Write([]byte(deliverSinkProbe)); err != nil {
		t.Fatalf("barrier probe write: %v", err)
	}
	probe.Close()
	var early [][]byte
	for {
		select {
		case frame := <-sink.frames:
			if string(frame) == deliverSinkProbe {
				return early
			}
			early = append(early, frame)
		case <-time.After(5 * time.Second):
			t.Fatal("barrier probe never reached the sink")
		}
	}
}
