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
