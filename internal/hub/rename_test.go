package hub_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/XertroV/vecnet/internal/testhub"
)

// peerFixed is everything a rename must leave alone: the id, principal,
// kind, enrolment, disabled flag, route, rate limits, credential and scopes.
type peerFixed struct {
	id, prn, kind, enrolled, route, cred string
	disabled, perMin, perHour, scopes    int
}

func loadPeerFixed(t *testing.T, env *testhub.Env, id string) peerFixed {
	t.Helper()
	var p peerFixed
	var route sql.NullString
	err := env.Hub.DB.QueryRow(`
		SELECT peer_id, prn_id, kind, enrolled_at, disabled, IFNULL(route_id, ''),
		       sends_per_min, threads_per_hour
		FROM peers WHERE peer_id = ?`, id).Scan(
		&p.id, &p.prn, &p.kind, &p.enrolled, &p.disabled, &route, &p.perMin, &p.perHour)
	if err != nil {
		t.Fatal(err)
	}
	p.route = route.String
	if err := env.Hub.DB.QueryRow(
		`SELECT secret_hash FROM credentials WHERE subject_id = ? AND kind = 'peer' AND revoked_at IS NULL`, id).Scan(&p.cred); err != nil {
		t.Fatal(err)
	}
	p.scopes = count(t, env, `SELECT count(*) FROM scopes WHERE peer_id = ?`, id)
	return p
}

func peerName(t *testing.T, env *testhub.Env, id string) string {
	t.Helper()
	var name string
	if err := env.Hub.DB.QueryRow(`SELECT name FROM peers WHERE peer_id = ?`, id).Scan(&name); err != nil {
		t.Fatal(err)
	}
	return name
}

func directoryNameUpdates(t *testing.T, env *testhub.Env, owner, peer string) int {
	t.Helper()
	return count(t, env, `
		SELECT count(*) FROM events
		WHERE owner = ? AND type = 'directory_updated'
		  AND json_extract(data, '$.peer_id') = ?
		  AND json_extract(data, '$.change') = 'updated'
		  AND json_extract(data, '$.fields') = '["name"]'`, owner, peer)
}

// TestRenamePeerKeepsIdentity: a new display name leaves the peer id, its
// thread, scopes and credential in place, and tells every enabled peer.
func TestRenamePeerKeepsIdentity(t *testing.T) {
	env := testhub.Start(t)
	ctx := context.Background()
	alice, aliceSec := env.NewPeer("session", "alice")
	bob, _ := env.NewPeer("grokbot", "bob")
	if err := env.Hub.SetScopes(ctx, alice, []string{bob}, nil); err != nil {
		t.Fatal(err)
	}
	sent := mustSend(t, env, aliceSec, req("rename-thread", bob))
	before := loadPeerFixed(t, env, alice)

	if err := env.Hub.RenamePeer(ctx, alice, "xpp-cos"); err != nil {
		t.Fatal(err)
	}
	if got := peerName(t, env, alice); got != "xpp-cos" {
		t.Fatalf("name = %q, want xpp-cos", got)
	}
	after := loadPeerFixed(t, env, alice)
	if after != before {
		t.Fatalf("peer identity changed\nbefore %+v\nafter  %+v", before, after)
	}
	if n := count(t, env, `SELECT count(*) FROM thread_participants WHERE thr_id = ? AND peer_id = ?`, sent.ThreadID, alice); n != 1 {
		t.Fatalf("alice still on thread %s = %d, want 1", sent.ThreadID, n)
	}
	for _, owner := range []string{env.OperatorPeer, alice, bob} {
		if n := directoryNameUpdates(t, env, owner, alice); n != 1 {
			t.Fatalf("%s directory_updated name for %s = %d, want 1", owner, alice, n)
		}
	}
	if n := count(t, env, `
		SELECT count(*) FROM audit
		WHERE actor = 'operator' AND action = 'peer_rename'
		  AND json_extract(target_ids, '$[0]') = ?
		  AND json_extract(detail, '$.old_name') = 'alice'
		  AND json_extract(detail, '$.new_name') = 'xpp-cos'`, alice); n != 1 {
		t.Fatalf("peer_rename audit rows = %d, want 1", n)
	}

	// The current name again changes nothing.
	if err := env.Hub.RenamePeer(ctx, alice, "xpp-cos"); err != nil {
		t.Fatal(err)
	}
	if n := count(t, env, `SELECT count(*) FROM audit WHERE action = 'peer_rename'`); n != 1 {
		t.Fatalf("audit rows after no-op = %d, want 1", n)
	}
	if n := directoryNameUpdates(t, env, alice, alice); n != 1 {
		t.Fatalf("directory events after no-op = %d, want 1", n)
	}
}

func TestRenamePeerRefusesBadNames(t *testing.T) {
	env := testhub.Start(t)
	ctx := context.Background()
	alice, _ := env.NewPeer("session", "alice")
	bob, _ := env.NewPeer("session", "bob")

	cases := []struct {
		name       string
		status     int
		code, peer string
	}{
		{name: "", status: 422, code: "invalid_value", peer: alice},
		{name: "x", status: 422, code: "invalid_value", peer: alice},
		{name: " operator", status: 422, code: "invalid_value", peer: alice},
		{name: "max", status: 422, code: "name_reserved", peer: alice},
		{name: "rnax", status: 422, code: "name_reserved", peer: alice}, // skeleton of "max"
		{name: "operator", status: 422, code: "name_reserved", peer: alice},
		{name: "0perat0r", status: 422, code: "name_reserved", peer: alice}, // operator peer's name
		{name: "ALICE", status: 409, code: "name_taken", peer: bob},
		{name: "a.l.i.c.e", status: 409, code: "name_taken", peer: bob},
	}
	for _, tc := range cases {
		err := env.Hub.RenamePeer(ctx, tc.peer, tc.name)
		e := provisionError(t, err, tc.status, tc.code)
		if tc.code == "invalid_value" && e.Details["field"] != "name" {
			t.Errorf("rename %q details = %v, want field name", tc.name, e.Details)
		}
		if got := peerName(t, env, tc.peer); got != map[string]string{alice: "alice", bob: "bob"}[tc.peer] {
			t.Errorf("rename %q changed %s to %q", tc.name, tc.peer, got)
		}
	}
	if n := count(t, env, `SELECT count(*) FROM audit WHERE action = 'peer_rename'`); n != 0 {
		t.Fatalf("refused renames audited %d rows, want 0", n)
	}
}

func TestRenamePeerUnknown(t *testing.T) {
	env := testhub.Start(t)
	err := env.Hub.RenamePeer(context.Background(), "peer_missing", "someone")
	e := provisionError(t, err, 404, "not_found")
	if e.Details["peer_id"] != "peer_missing" {
		t.Fatalf("details = %v, want peer_id", e.Details)
	}
}

// A free-form operator-set name, and the reserved operator name, stay put
// when submitted unchanged. checkAgentName would refuse both as a new name.
func TestRenamePeerSameNameIsNoop(t *testing.T) {
	env := testhub.Start(t)
	ctx := context.Background()
	short, _ := env.NewPeer("session", "x")
	for _, idName := range [][2]string{{short, "x"}, {env.OperatorPeer, "operator"}} {
		if err := env.Hub.RenamePeer(ctx, idName[0], idName[1]); err != nil {
			t.Fatalf("rename %s to %q: %v", idName[0], idName[1], err)
		}
		if got := peerName(t, env, idName[0]); got != idName[1] {
			t.Fatalf("name = %q, want %q", got, idName[1])
		}
	}
	if n := count(t, env, `SELECT count(*) FROM audit WHERE action = 'peer_rename'`); n != 0 {
		t.Fatalf("no-op audited %d rows, want 0", n)
	}
	if n := directoryNameUpdates(t, env, env.OperatorPeer, short); n != 0 {
		t.Fatalf("no-op directory events = %d, want 0", n)
	}
}
