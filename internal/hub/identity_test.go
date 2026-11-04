package hub_test

import (
	"context"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/hub"
	"github.com/XertroV/vecnet/internal/hubid"
	"github.com/XertroV/vecnet/internal/testhub"
)

func TestHubIdentityStableAcrossRestart(t *testing.T) {
	e := testhub.Start(t)
	first := e.Hub.IdentityKey().Pubkey()
	if _, err := hubid.DecodePubkey(first); err != nil {
		t.Fatal(err)
	}
	restarted, err := hub.New(e.Hub.DB, e.Hub.Cfg, e.Now.Now)
	if err != nil {
		t.Fatal(err)
	}
	if got := restarted.IdentityKey().Pubkey(); got != first {
		t.Fatalf("restart pubkey = %q, want %q", got, first)
	}
	if fresh := testhub.Start(t).Hub.IdentityKey().Pubkey(); fresh == first {
		t.Fatal("fresh store reused the identity")
	}
}

func TestRotateIdentityEndorses(t *testing.T) {
	e := testhub.Start(t)
	ctx := context.Background()
	old := e.Hub.IdentityKey().Pubkey()
	oldCreated, err := e.Hub.DB.Meta(ctx, "hub_identity_created_at")
	if err != nil {
		t.Fatal(err)
	}
	hubKey, err := e.Hub.DB.Meta(ctx, "hub_key")
	if err != nil {
		t.Fatal(err)
	}
	peer, _ := e.NewPeer("grokbot", "rotation-bot")
	const routeKey = "crsr_rotation-route-secret"
	route, err := e.Hub.AddGrokbotRoute(ctx, "peer", peer,
		"https://api2.cursor.sh/automations/webhook/00000000-0000-0000-0000-000000000000", routeKey)
	if err != nil {
		t.Fatal(err)
	}
	// Source secrets use the same seal; preserve an existing sealed value too.
	sealed, err := e.Hub.Seal([]byte("source-secret"))
	if err != nil {
		t.Fatal(err)
	}
	rotated := e.Now.Now().Add(time.Hour)
	oldFP, newFP, err := hub.RotateIdentity(ctx, e.Hub.DB, rotated)
	if err != nil {
		t.Fatal(err)
	}
	next, err := hub.New(e.Hub.DB, e.Hub.Cfg, e.Now.Now)
	if err != nil {
		t.Fatal(err)
	}
	doc, err := next.IdentityDoc("")
	if err != nil {
		t.Fatal(err)
	}
	if doc.Pubkey == old || doc.Previous == nil || doc.Previous.Pubkey != old {
		t.Fatalf("rotation did not replace and retain the old public key: %+v", doc)
	}
	if err := hubid.VerifyEndorsement(*doc.Previous, doc.Pubkey); err != nil {
		t.Fatal(err)
	}
	wantOldFP, err := hubid.Fingerprint(old)
	if err != nil {
		t.Fatal(err)
	}
	if oldFP != wantOldFP || newFP != doc.Fingerprint || oldFP == newFP {
		t.Fatalf("rotation fingerprints = %q, %q; doc = %+v", oldFP, newFP, doc)
	}
	if doc.KeyCreatedAt != envelope.FormatTime(rotated) || doc.KeyCreatedAt == oldCreated || doc.Previous.RotatedAt != doc.KeyCreatedAt {
		t.Fatalf("rotation timestamps: %+v", doc)
	}
	if got, err := next.DB.Meta(ctx, "hub_key"); err != nil || got != hubKey {
		t.Fatalf("hub_key changed: %v", err)
	}
	if got, err := next.GrokbotKey(ctx, route); err != nil || got != routeKey {
		t.Fatalf("sealed route key = %q, %v", got, err)
	}
	if got, err := next.Unseal(sealed); err != nil || string(got) != "source-secret" {
		t.Fatalf("sealed source value = %q, %v", got, err)
	}
}
