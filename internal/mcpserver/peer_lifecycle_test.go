package mcpserver_test

import (
	"testing"

	"github.com/XertroV/vecnet/internal/testhub"
)

// vecnet_peer_lifecycle is operator-only. RequirePeerLifecycle refuses a peer
// or principal before disable, enable, archive, or unarchive changes a row.
func TestPeerLifecyclePermissionDenied(t *testing.T) {
	env := testhub.Start(t)
	peer, peerSecret := env.NewPeer("session", "target")
	_, principalSecret := env.NewPrincipal("nosy")
	for _, secret := range []string{peerSecret, principalSecret} {
		for _, action := range []string{"disable", "enable", "archive", "unarchive"} {
			toolFails(t, env, secret, "vecnet_peer_lifecycle", map[string]any{"peer_id": peer, "action": action}, "forbidden")
		}
	}
	var disabled int
	var archived *string
	if err := env.Hub.DB.QueryRow(`SELECT disabled, archived_at FROM peers WHERE peer_id=?`, peer).Scan(&disabled, &archived); err != nil || disabled != 0 || archived != nil {
		t.Fatalf("peer changed after refusal: disabled=%d archived=%v err=%v", disabled, archived, err)
	}
}

func TestMergedPeerToolsAuthorityAndValueOrder(t *testing.T) {
	env := testhub.Start(t)
	_, peerSecret := env.NewPeer("session", "authority caller")
	_, principalSecret := env.NewPrincipal("authority principal")
	for _, name := range []string{"vecnet_peer_lifecycle", "vecnet_peer_invite_get", "vecnet_peer_invite_revoke"} {
		args := map[string]any{"peer_id": "malformed"}
		if name == "vecnet_peer_lifecycle" {
			args["action"] = "unknown"
		}
		for _, secret := range []string{peerSecret, principalSecret} {
			toolFails(t, env, secret, name, args, "forbidden")
		}
		// Shape errors still precede handler authority checks.
		args["peer_id"] = 123
		if path := invalidPath(t, env, peerSecret, name, args); path != "/peer_id" {
			t.Errorf("%s: shape refusal path %q", name, path)
		}
		args["peer_id"] = "malformed"
		if name == "vecnet_peer_lifecycle" {
			// Operator checks the action before looking up the target.
			toolFails(t, env, env.OperatorSecret, name, args, "invalid_value")
			args["action"] = "disable"
		}
		toolFails(t, env, env.OperatorSecret, name, args, "not_found")
	}
}
