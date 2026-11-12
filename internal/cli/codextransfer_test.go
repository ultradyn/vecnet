package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

const guardAttemptA = "11111111111111111111111111111111"
const guardAttemptB = "22222222222222222222222222222222"

func (f *bindFixture) guardFlags(claim string) []string {
	f.t.Helper()
	return []string{"--thread", bindT1, "--codex-home", f.home, "--expect-generation", bindValue[string](f.t, f.entries()[0], "generation"), "--expect-hub", f.hub, "--expect-caller", "peer_A", "--claim", claim, "--attempt", guardAttemptA}
}

func TestCodexAcceptancePreservesInvalidState(t *testing.T) {
	for _, damaged := range []string{"{corrupt", "null", `{}`, `{"hub":"duplicate","hub":"other"}`, "missing", "no-claim", "case-accepted", "null-accepted"} {
		t.Run(damaged, func(t *testing.T) {
			f := newBindFixture(t)
			f.bind("p", bindT1)
			path := f.seedGuardClaim("send1")
			flags := f.guardFlags("send1")
			f.ok(append([]string{"guard"}, flags...)...)
			generation := bindValue[string](t, f.entries()[0], "generation")
			pendingPath := f.path + "." + generation + ".pending"
			pending, err := os.ReadFile(pendingPath)
			if err != nil {
				t.Fatal(err)
			}
			if damaged == "missing" {
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			} else {
				raw := []byte(damaged)
				if damaged == "no-claim" || damaged == "case-accepted" || damaged == "null-accepted" {
					s := readHookState(t, path)
					if damaged == "no-claim" {
						s.Claims = nil
					} else {
						s.Claims[0].Accepted = true
					}
					raw, err = json.Marshal(s)
					if err != nil {
						t.Fatal(err)
					}
				}
				if damaged == "case-accepted" {
					raw = bytes.Replace(raw, []byte(`"Accepted":true`), []byte(`"Accepted":true,"accepted":false`), 1)
				}
				if damaged == "null-accepted" {
					raw = bytes.Replace(raw, []byte(`"Accepted":true`), []byte(`"Accepted":null`), 1)
				}
				if err := os.WriteFile(path, raw, 0600); err != nil {
					t.Fatal(err)
				}
			}
			before, beforeErr := os.ReadFile(path)
			code, _, _ := f.run(append([]string{"settle", "--outcome", "accepted"}, flags...)...)
			after, afterErr := os.ReadFile(path)
			marker, markerErr := os.ReadFile(pendingPath)
			if code == 0 || !bytes.Equal(before, after) || os.IsNotExist(beforeErr) != os.IsNotExist(afterErr) || markerErr != nil || !bytes.Equal(pending, marker) {
				t.Fatalf("acceptance erased recovery evidence: exit=%d state=%s pending=%s", code, after, marker)
			}
		})
	}
}

func TestCodexTransferPreservesCorruptState(t *testing.T) {
	f := newBindFixture(t)
	f.bind("p", bindT1)
	path := f.seedGuardClaim("send1")
	if err := os.WriteFile(path, []byte("{corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	before := f.raw()
	code, _, _ := f.run("bind", "--thread", bindT2, "--expect-generation", bindValue[string](t, f.entries()[0], "generation"))
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if code == 0 || !bytes.Equal(before, f.raw()) || string(after) != "{corrupt" {
		t.Fatalf("handoff erased corrupt state: exit=%d state=%s", code, after)
	}
}

func TestCodexSettlementFencesSendAttempts(t *testing.T) {
	f := newBindFixture(t)
	f.bind("p", bindT1)
	path := f.seedGuardClaim("send1")
	flags := f.guardFlags("send1")
	a := append([]string{}, flags...)
	b := append([]string{}, flags...)
	b[len(b)-1] = guardAttemptB
	f.ok(append([]string{"guard"}, a...)...)
	f.ok(append([]string{"settle", "--outcome", "rejected"}, a...)...)
	f.ok(append([]string{"guard"}, b...)...)
	pendingPath := f.path + "." + bindValue[string](t, f.entries()[0], "generation") + ".pending"
	before, err := os.ReadFile(pendingPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, outcome := range []string{"accepted", "rejected"} {
		code, _, _ := f.run(append([]string{"settle", "--outcome", outcome}, a...)...)
		after, err := os.ReadFile(pendingPath)
		if err != nil || code == 0 || !bytes.Equal(before, after) || readHookState(t, path).Claims[0].Accepted {
			t.Fatalf("stale %s changed current attempt: exit=%d err=%v", outcome, code, err)
		}
	}
	f.ok(append([]string{"settle", "--outcome", "accepted"}, b...)...)
	if !readHookState(t, path).Claims[0].Accepted {
		t.Fatal("current acceptance not recorded")
	}
}

func TestCodexMissingPendingSettlementDoesNotAcceptClaim(t *testing.T) {
	f := newBindFixture(t)
	f.bind("p", bindT1)
	path := f.seedGuardClaim("send1")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	f.ok(append([]string{"settle", "--outcome", "accepted"}, f.guardFlags("send1")...)...)
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("stale settlement accepted unsent claim without pending evidence")
	}
}

func (f *bindFixture) seedGuardClaim(nonce string) string {
	f.t.Helper()
	path, err := statePath(func(k string) string { return f.env[k] }, "p", "")
	if err != nil {
		f.t.Fatal(err)
	}
	gen := bindValue[string](f.t, f.entries()[0], "generation")
	if err := (stateFile{path}).update(f.t.Context(), func(s *hookState) error {
		s.Hub = f.hub
		s.Caller = "peer_A"
		s.Shown = []string{"dly_claim"}
		s.Claims = []claimRef{{ID: "dly_claim", After: "e1.0", Priority: "wake", Claim: nonce, Generation: gen}}
		return nil
	}); err != nil {
		f.t.Fatal(err)
	}
	return path
}

func TestCodexRecoveredReceiptSurvivesHandoff(t *testing.T) {
	f := newBindFixture(t)
	f.bind("p", bindT1)
	path := f.seedGuardClaim("send1")
	generation := bindValue[string](t, f.entries()[0], "generation")
	flags := []string{"--thread", bindT1, "--codex-home", f.home, "--expect-generation", generation, "--expect-hub", f.hub, "--expect-caller", "peer_A", "--claim", "send1", "--attempt", guardAttemptA}
	f.ok(append([]string{"guard"}, flags...)...) // EOF simulates lost acceptance.
	f.ok(append([]string{"settle", "--outcome", "accepted"}, flags...)...)
	f.bind("p", bindT2, "--expect-generation", generation)
	s := readHookState(t, path)
	if !s.has("dly_claim") || len(s.Replay) != 0 || len(s.Claims) != 1 || !s.Claims[0].Accepted {
		t.Fatalf("accepted send replayed: %+v", s)
	}
}

func TestCodexOwnershipAcrossConfigDirectories(t *testing.T) {
	f := newBindFixture(t)
	f.bind("p", bindT1)
	original := f.env["VECNET_CONFIG"]
	f.env["VECNET_CONFIG"] = filepath.Join(f.env["HOME"], "second", "config.toml")
	f.config(f.hub)
	f.rejected("recipient", "bind", "--thread", bindT2)
	// Another recipient is allowed, and unbind cannot remove the first config.
	f.peer.Store("peer_B")
	f.bind("p", bindT2)
	f.ok("unbind")
	if len(f.entries()) != 1 || bindValue[string](t, f.entries()[0], "config_path") != original {
		t.Fatal("unbind removed another namespace")
	}
}

func TestCodexSeparateAuthorityOverrideRejected(t *testing.T) {
	f := newBindFixture(t)
	alternate := filepath.Join(f.env["HOME"], "alternate.json")
	f.env["VECNET_CODEX_BINDINGS"] = alternate
	code, _, _ := f.run("bind", "--thread", bindT1)
	if code == 0 {
		t.Fatal("separate authority admitted")
	}
	if _, err := os.Stat(alternate); !os.IsNotExist(err) {
		t.Fatal("wrote alternate authority")
	}
}

func TestCodexHandoffReleasesOnlyUnsentClaims(t *testing.T) {
	f := newBindFixture(t)
	f.bind("p", bindT1)
	generation := bindValue[string](t, f.entries()[0], "generation")
	path, err := statePath(func(k string) string { return f.env[k] }, "p", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := (stateFile{path}).update(t.Context(), func(s *hookState) error {
		s.Hub = f.hub
		s.Caller = "peer_A"
		s.Shown = []string{"dly_unsent"}
		s.Claims = []claimRef{{ID: "dly_unsent", After: "e1.0", Priority: "wake", Claim: "claim1", Generation: generation}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	f.bind("p", bindT2, "--expect-generation", generation)
	state := readHookState(t, path)
	if state.has("dly_unsent") || len(state.Replay) != 1 || len(state.Claims) != 0 {
		t.Fatalf("unsent claim suppressed after handoff: %+v", state)
	}
}

type guardReadyWriter struct {
	once  sync.Once
	ready chan struct{}
}

func (w *guardReadyWriter) Write(p []byte) (int, error) {
	w.once.Do(func() { close(w.ready) })
	return len(p), nil
}

func TestCodexHandoffWaitsForInFlightGuard(t *testing.T) {
	f := newBindFixture(t)
	f.bind("p", bindT1)
	f.seedGuardClaim("send1")
	generation := bindValue[string](t, f.entries()[0], "generation")
	input, write := io.Pipe()
	defer input.Close()
	defer write.Close()
	ready := &guardReadyWriter{ready: make(chan struct{})}
	var errs bytes.Buffer
	a := &app{ctx: t.Context(), stdin: input, stdout: ready, stderr: &errs, getenv: func(k string) string { return f.env[k] }}
	guardDone := make(chan error, 1)
	go func() {
		guardDone <- a.cmdCodex([]string{"guard", "--thread", bindT1, "--codex-home", f.home, "--expect-generation", generation, "--expect-hub", f.hub, "--expect-caller", "peer_A", "--claim", "send1", "--attempt", guardAttemptA}, func() time.Time { return f.now })
	}()
	select {
	case <-ready.ready:
	case err := <-guardDone:
		t.Fatalf("guard failed: %v", err)
	case <-time.After(3 * time.Second):
		t.Fatal("guard timeout")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	blocked := &app{ctx: ctx, stdin: strings.NewReader(""), stdout: &bytes.Buffer{}, stderr: &bytes.Buffer{}, getenv: a.getenv}
	err := blocked.cmdCodex([]string{"bind", "--thread", bindT2, "--expect-generation", generation}, func() time.Time { return f.now })
	if err == nil {
		t.Fatal("handoff passed an in-flight send")
	}
	if _, err := io.WriteString(write, "accepted\n"); err != nil {
		t.Fatal(err)
	}
	if err := <-guardDone; err != nil {
		t.Fatal(err)
	}
	f.bind("p", bindT2, "--expect-generation", generation)
}
