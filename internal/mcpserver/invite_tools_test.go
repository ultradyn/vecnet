package mcpserver_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/XertroV/vecnet/internal/testhub"
)

// vecnet_peer_invite_get and vecnet_peer_invite_revoke are the operator's
// invite status and revoke on MCP: the same hub calls as GET and DELETE
// /v1/admin/peers/{id}/enrol-token, behind the same operator gate, and never
// a token in or out (PROTOCOL §2.12, §4.3).
func TestPeerInviteTools(t *testing.T) {
	env := testhub.Start(t)
	resp, raw := env.Do(env.OperatorSecret, "POST", "/v1/admin/peers", map[string]any{"kind": "session", "name": "mcp-pending"})
	var p struct {
		PeerID     string `json:"peer_id"`
		EnrolToken string `json:"enrol_token"`
	}
	if resp.StatusCode != http.StatusCreated || json.Unmarshal(raw, &p) != nil || p.EnrolToken == "" {
		t.Fatalf("add peer: %d %s", resp.StatusCode, raw)
	}
	clean := func(what string, v any) {
		t.Helper()
		b, _ := json.Marshal(v)
		if strings.Contains(string(b), p.EnrolToken) || strings.Contains(string(b), "vn_enrol_") || strings.Contains(string(b), "token") {
			t.Fatalf("%s carries a token: %s", what, b)
		}
	}

	// Not the operator: forbidden, and the invite is untouched.
	_, peerSecret := env.NewPeer("session", "nosy")
	prn, prnSecret := env.NewPrincipal("delegate")
	_ = prn
	for _, secret := range []string{peerSecret, prnSecret} {
		toolFails(t, env, secret, "vecnet_peer_invite_get", map[string]any{"peer_id": p.PeerID}, "forbidden")
		toolFails(t, env, secret, "vecnet_peer_invite_revoke", map[string]any{"peer_id": p.PeerID}, "forbidden")
	}

	var st map[string]any
	toolOK(t, env, env.OperatorSecret, "vecnet_peer_invite_get", map[string]any{"peer_id": p.PeerID}, &st)
	clean("get", st)
	if st["state"] != "pending" || st["peer_id"] != p.PeerID || st["expires_at"] == "" {
		t.Fatalf("get = %v", st)
	}

	var rv map[string]any
	toolOK(t, env, env.OperatorSecret, "vecnet_peer_invite_revoke", map[string]any{"peer_id": p.PeerID}, &rv)
	clean("revoke", rv)
	if rv["revoked"] != true || rv["state"] != "revoked" {
		t.Fatalf("revoke = %v", rv)
	}
	resp, raw = env.Do("", "POST", "/v1/enrol", map[string]string{"token": p.EnrolToken})
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("enrol after MCP revoke = %d %s", resp.StatusCode, raw)
	}
	toolOK(t, env, env.OperatorSecret, "vecnet_peer_invite_revoke", map[string]any{"peer_id": p.PeerID}, &rv)
	if rv["revoked"] != false || rv["state"] != "revoked" {
		t.Fatalf("second revoke = %v", rv)
	}
	var n int
	if err := env.Hub.DB.QueryRow(`SELECT count(*) FROM audit WHERE action = 'enrol_revoke'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("enrol_revoke audit rows = %d, %v", n, err)
	}

	toolFails(t, env, env.OperatorSecret, "vecnet_peer_invite_revoke", map[string]any{"peer_id": "peer_nope"}, "not_found")
	if path := invalidPath(t, env, env.OperatorSecret, "vecnet_peer_invite_revoke", map[string]any{"peer_id": p.PeerID, "token": "x"}); path != "/token" {
		t.Fatalf("revoke accepts extra input: path = %q", path)
	}
}
