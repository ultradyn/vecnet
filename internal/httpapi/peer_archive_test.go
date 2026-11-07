package httpapi_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/XertroV/vecnet/internal/testhub"
)

func TestPeerArchiveLifecycle(t *testing.T) {
	env := testhub.Start(t)
	peer, secret := env.NewPeer("session", "archivable")
	resp, raw := env.Do(env.OperatorSecret, "POST", "/v1/admin/peers/"+peer+"/archive", nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("archive = %d %s", resp.StatusCode, raw)
	}
	resp, _ = env.Do(secret, "GET", "/v1/peers", nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("archived credential = %d, want 401", resp.StatusCode)
	}
	var disabled int
	var archived *string
	if err := env.Hub.DB.QueryRowContext(context.Background(), `SELECT disabled, archived_at FROM peers WHERE peer_id=?`, peer).Scan(&disabled, &archived); err != nil || disabled != 1 || archived == nil {
		t.Fatalf("archive row disabled=%d at=%v err=%v", disabled, archived, err)
	}
	resp, raw = env.Do(env.OperatorSecret, "POST", "/v1/admin/peers/"+peer+"/unarchive", nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("unarchive = %d %s", resp.StatusCode, raw)
	}
	if err := env.Hub.DB.QueryRow(`SELECT archived_at FROM peers WHERE peer_id=?`, peer).Scan(&archived); err != nil || archived != nil {
		t.Fatalf("unarchive at=%v err=%v", archived, err)
	}
	resp, raw = env.Do(env.OperatorSecret, "POST", "/v1/admin/peers/"+env.OperatorPeer+"/archive", nil)
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("operator archive = %d %s", resp.StatusCode, raw)
	}
}
