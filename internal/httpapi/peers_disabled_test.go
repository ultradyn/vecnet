package httpapi_test

import (
	"github.com/XertroV/vecnet/internal/testhub"
	"strings"
	"testing"
)

func TestDisabledRecipientIdentityUnavailable(t *testing.T) {
	e := testhub.Start(t)
	principal, secret := e.NewPrincipal("gate-owner")
	peer, _, err := e.Hub.AddPeer(t.Context(), "session", "gate-owned", principal)
	if err != nil {
		t.Fatal(err)
	}
	if err = e.Hub.SetPeerDisabled(t.Context(), peer, true); err != nil {
		t.Fatal(err)
	}
	resp, body := e.Do(secret, "GET", "/v1/peers/me?peer="+peer, nil)
	t.Logf("disabled owned recipient identity: HTTP %d %s", resp.StatusCode, body)
	if !strings.Contains(string(body), "peer_disabled") {
		t.Fatalf("missing lifecycle error: %s", body)
	}
	if resp.StatusCode != 503 {
		t.Fatalf("expected unavailable recipient: HTTP %d", resp.StatusCode)
	}
}
