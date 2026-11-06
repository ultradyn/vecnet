package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/XertroV/vecnet/internal/testhub"
)

// `vecnet invite status|revoke <peer>` (and the same under `peer invite`)
// read and withdraw an unredeemed invite through the one hub route: the
// token stops enrolling, a peer with nothing live is a clean no-op (exit 0),
// and no output ever carries the token.
func TestInviteRevokeAndStatus(t *testing.T) {
	env := testhub.Start(t)
	opDir := operatorDir(t, env)
	code, out, errs := cliRun(t, opDir, "", "--net", "op", "--json", "peer", "invite", "--name", "Pending", "--ttl", "1h")
	var j map[string]any
	if code != 0 || json.Unmarshal([]byte(out), &j) != nil {
		t.Fatalf("invite: %d %s %s", code, out, errs)
	}
	peer, tok := j["peer_id"].(string), j["enrol_token"].(string)
	clean := func(what, s string) {
		t.Helper()
		if strings.Contains(s, tok) || strings.Contains(s, "vn_enrol_") {
			t.Fatalf("%s carries a token: %s", what, s)
		}
	}

	code, out, errs = cliRun(t, opDir, "", "--net", "op", "invite", "status", peer)
	clean("status", out+errs)
	if code != 0 || !strings.HasPrefix(out, peer+"  pending  expires ") {
		t.Fatalf("invite status: %d %q %q", code, out, errs)
	}
	code, out, errs = cliRun(t, opDir, "", "--net", "op", "--json", "peer", "invite", "status", peer)
	var st map[string]any
	if code != 0 || json.Unmarshal([]byte(out), &st) != nil || st["state"] != "pending" || st["peer_id"] != peer {
		t.Fatalf("peer invite status --json: %d %s %s", code, out, errs)
	}

	code, out, errs = cliRun(t, opDir, "", "--net", "op", "invite", "revoke", peer)
	clean("revoke", out+errs)
	if code != 0 || out != "revoked the invite of "+peer+"; its token no longer enrols\n" {
		t.Fatalf("invite revoke: %d %q %q", code, out, errs)
	}
	if code, _, errs := cliRun(t, t.TempDir(), tok, "enrol", "--hub", env.URL, "--token", "-", "--profile", "pending"); code != 2 || !strings.HasPrefix(errs, "unauthenticated: ") {
		t.Fatalf("enrol with a revoked token: %d %s", code, errs)
	}

	// Nothing live: a no-op that says so, not an error.
	code, out, errs = cliRun(t, opDir, "", "--net", "op", "peer", "invite", "revoke", peer)
	if code != 0 || out != peer+" has no live invite (revoked); nothing to revoke\n" {
		t.Fatalf("second revoke: %d %q %q", code, out, errs)
	}
	code, out, errs = cliRun(t, opDir, "", "--net", "op", "--json", "invite", "revoke", peer)
	var rv map[string]any
	if code != 0 || json.Unmarshal([]byte(out), &rv) != nil || rv["revoked"] != false || rv["state"] != "revoked" {
		t.Fatalf("revoke --json: %d %s %s", code, out, errs)
	}
	code, out, _ = cliRun(t, opDir, "", "--net", "op", "invite", "status", peer)
	if code != 0 || !strings.HasPrefix(out, peer+"  revoked  at ") {
		t.Fatalf("status after revoke: %d %q", code, out)
	}

	// Usage and hub errors keep the CLI's exit codes.
	if code, _, _ := cliRun(t, opDir, "", "--net", "op", "invite", "revoke"); code != 1 {
		t.Fatalf("invite revoke without a peer: exit %d", code)
	}
	if code, _, _ := cliRun(t, opDir, "", "--net", "op", "invite", "frob", peer); code != 1 {
		t.Fatalf("invite frob: exit %d", code)
	}
	for _, flag := range [][]string{{"--kind", "session"}, {"--principal", "prn_x"}, {"--ttl", "1h"}, {"--copy"}} {
		args := append([]string{"--net", "op", "peer", "invite", "revoke", peer}, flag...)
		if code, _, _ := cliRun(t, opDir, "", args...); code != 1 {
			t.Fatalf("peer invite revoke %v: exit %d, want 1", flag, code)
		}
	}
	if code, _, errs := cliRun(t, opDir, "", "--net", "op", "invite", "revoke", "peer_nope"); code != 2 || !strings.HasPrefix(errs, "not_found: ") {
		t.Fatalf("revoke unknown peer: %d %s", code, errs)
	}
}
