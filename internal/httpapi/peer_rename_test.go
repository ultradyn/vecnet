package httpapi_test

import (
	"net/http"
	"testing"

	"github.com/XertroV/vecnet/internal/testhub"
)

func TestAdminRenamePeer(t *testing.T) {
	env := testhub.Start(t)
	alice := adminAddPeer(t, env, map[string]any{"kind": "session", "name": "alice"})
	bob := adminAddPeer(t, env, map[string]any{"kind": "grokbot", "name": "bob"})
	enrolled := enrol(t, env, alice.EnrolToken)

	adminDo(t, env, http.MethodPatch, "/v1/admin/peers/"+alice.PeerID, map[string]any{"name": "xpp-cos"}, http.StatusNoContent)
	var name, id string
	if err := env.Hub.DB.QueryRow(`SELECT peer_id, name FROM peers WHERE peer_id = ?`, alice.PeerID).Scan(&id, &name); err != nil {
		t.Fatal(err)
	}
	if id != alice.PeerID || name != "xpp-cos" {
		t.Fatalf("peer = %s %q, want %s xpp-cos", id, name, alice.PeerID)
	}

	// Same name is a no-op success.
	adminDo(t, env, http.MethodPatch, "/v1/admin/peers/"+alice.PeerID, map[string]any{"name": "xpp-cos"}, http.StatusNoContent)

	resp, raw := env.Do(env.OperatorSecret, http.MethodPatch, "/v1/admin/peers/"+bob.PeerID, map[string]any{"name": ""})
	e := mustErr(t, resp, raw, http.StatusUnprocessableEntity, "invalid_value")
	if e.Error.Details["field"] != "name" {
		t.Fatalf("empty name details = %v", e.Error.Details)
	}
	resp, raw = env.Do(env.OperatorSecret, http.MethodPatch, "/v1/admin/peers/"+bob.PeerID, map[string]any{"name": "max"})
	mustErr(t, resp, raw, http.StatusUnprocessableEntity, "name_reserved")
	resp, raw = env.Do(env.OperatorSecret, http.MethodPatch, "/v1/admin/peers/"+bob.PeerID, map[string]any{"name": "xpp-cos"})
	mustErr(t, resp, raw, http.StatusConflict, "name_taken")
	resp, raw = env.Do(env.OperatorSecret, http.MethodPatch, "/v1/admin/peers/"+bob.PeerID, map[string]any{"name": "XPP-COS"})
	mustErr(t, resp, raw, http.StatusConflict, "name_taken")

	resp, raw = env.Do(env.OperatorSecret, http.MethodPatch, "/v1/admin/peers/peer_missing", map[string]any{"name": "someone"})
	e = mustErr(t, resp, raw, http.StatusNotFound, "not_found")
	if e.Error.Details["peer_id"] != "peer_missing" {
		t.Fatalf("404 details = %v", e.Error.Details)
	}

	resp, raw = env.Do(enrolled.Secret, http.MethodPatch, "/v1/admin/peers/"+alice.PeerID, map[string]any{"name": "nope"})
	mustErr(t, resp, raw, http.StatusForbidden, "forbidden")

	resp, raw = env.Do(env.OperatorSecret, http.MethodPatch, "/v1/admin/peers/"+bob.PeerID, map[string]any{})
	mustErr(t, resp, raw, http.StatusBadRequest, "invalid_body")
	resp, raw = env.Do(env.OperatorSecret, http.MethodPatch, "/v1/admin/peers/"+bob.PeerID, map[string]any{"name": "ok", "kind": "dot"})
	mustErr(t, resp, raw, http.StatusBadRequest, "invalid_body")

	var still string
	if err := env.Hub.DB.QueryRow(`SELECT name FROM peers WHERE peer_id = ?`, bob.PeerID).Scan(&still); err != nil {
		t.Fatal(err)
	}
	if still != "bob" {
		t.Fatalf("bob name = %q after refused patches", still)
	}
}
