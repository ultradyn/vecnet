package cli

import (
	"context"
	"testing"
)

func TestHookAppServerReleaseReportsRemainingAcceptedClaim(t *testing.T) {
	f := newAppHookFixture(t)
	f.add(t, "wake", "wake", "")
	f.run(t, "c1", "wake")
	if err := (stateFile{f.path}).update(context.Background(), func(s *hookState) error {
		for i := range s.Claims {
			s.Claims[i].Accepted = true
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	release := func(claim string) string {
		out, _ := runHook(t, f.env, "--release", "--claim", claim, "--thread", bindT1, "--codex-home", f.env["CODEX_HOME"], "--expect-generation", appHookGeneration, "--expect-hub", f.hub, "--expect-caller", "peer_test")
		return out
	}
	if got := release("c1"); got != "{\"released\":0,\"remaining\":1}\n" {
		t.Fatalf("accepted claim: %q", got)
	}
	if len(f.state(t).Claims) != 1 {
		t.Fatal("accepted claim was removed")
	}
	if got := release("absent"); got != "{\"released\":0,\"remaining\":0}\n" {
		t.Fatalf("already absent claim: %q", got)
	}
}
