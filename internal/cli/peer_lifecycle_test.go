package cli_test

import (
	"strings"
	"testing"

	"github.com/XertroV/vecnet/internal/testhub"
)

// peer archive/unarchive/disable/enable are the operator CLI for the same
// admin routes. A peer or principal credential is refused and the row stays.
func TestPeerLifecyclePermissionDenied(t *testing.T) {
	env := testhub.Start(t)
	peer, peerSecret := env.NewPeer("session", "target")
	_, principalSecret := env.NewPrincipal("nosy")
	actions := []string{"archive", "unarchive", "disable", "enable"}
	for _, secret := range []string{peerSecret, principalSecret} {
		caller := profileUser(t, env.URL, secret)
		for _, action := range actions {
			code, _, errs := caller.run("peer", action, peer)
			if code != 2 || !strings.HasPrefix(errs, "forbidden: ") {
				t.Errorf("vecnet peer %s = %d %q, want 2 forbidden", action, code, errs)
			}
		}
	}
	if disabled := peerDisabled(t, env, peer); disabled != 0 {
		t.Fatalf("disabled = %d, want 0", disabled)
	}
	var archived *string
	if err := env.Hub.DB.QueryRow(`SELECT archived_at FROM peers WHERE peer_id=?`, peer).Scan(&archived); err != nil || archived != nil {
		t.Fatalf("archived_at = %v err=%v, want null", archived, err)
	}
}
