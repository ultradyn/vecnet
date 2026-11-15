package store

import (
	"context"
	"fmt"
	"reflect"
	"testing"
)

// Recover exactly the known addressed groups, including non-primary groups,
// without inventing creation history from a later retained message/reply.
func TestGroupThreadCreationsMigrationBackfill(t *testing.T) {
	db := a1Raw(t)
	a1Exec(t, db, `CREATE TABLE schema_migrations(name TEXT PRIMARY KEY,sha256 TEXT NOT NULL,applied_at TEXT NOT NULL) STRICT`)
	all, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range all {
		if m.name >= "0038_" {
			break
		}
		if _, err := applyOne(context.Background(), db, m); err != nil {
			t.Fatal(err)
		}
	}
	for _, g := range []string{"grp_a", "grp_b", "grp_reply"} {
		a1Exec(t, db, fmt.Sprintf(`INSERT INTO groups(grp_id,created_by,created_at,last_active_at) VALUES('%s','peer_old','2026-11-15T00:00:00.000Z','2026-11-15T00:00:00.000Z')`, g))
	}
	for _, team := range []string{"team_a", "team_b"} {
		a1Exec(t, db, fmt.Sprintf(`INSERT INTO teams(team_id,name,created_at) VALUES('%s','legacy team','2026-11-15T00:00:00.000Z')`, team))
	}
	for _, tc := range []struct{ id, group string }{
		{"thr_primary", "'grp_a'"}, {"thr_team_primary", "'team_a'"}, {"thr_secondary", "NULL"}, {"thr_reply_only", "NULL"},
		{"thr_missing_first", "NULL"}, {"thr_primary_no_message", "'grp_a'"}, {"thr_deleted_group", "'grp_deleted'"},
	} {
		a1Exec(t, db, fmt.Sprintf(`INSERT INTO threads(thr_id,group_id,created_by,created_at,last_message_at) VALUES('%s',%s,'peer_old','2026-11-15T00:00:00.000Z','2026-11-15T00:00:00.000Z')`, tc.id, tc.group))
	}
	legacy := ` { "recipients": ["peer_old", "grp_a", "grp_b", "grp_b", "team_b", "team_b"], "content": {"text":"private legacy"} } `
	if _, err := db.Exec(`INSERT INTO messages(msg_id,thr_id,seq,sender,kind,created_at,body) VALUES('msg_primary','thr_primary',1,'peer_old','message','2026-11-15T00:00:01.000Z',?)`, legacy); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO messages(msg_id,thr_id,seq,sender,kind,created_at,body) VALUES('msg_secondary','thr_secondary',1,'peer_old','message','2026-11-15T00:00:00.000Z','{"recipients":["peer_old","grp_b","team_a"]}')`,
		`INSERT INTO messages(msg_id,thr_id,seq,sender,kind,created_at,body) VALUES('msg_direct','thr_reply_only',1,'peer_old','message','2026-11-15T00:00:00.000Z','{"recipients":["peer_old"]}')`,
		`INSERT INTO messages(msg_id,thr_id,seq,sender,kind,created_at,body) VALUES('msg_reply','thr_reply_only',2,'peer_old','message','2026-11-15T00:00:01.000Z','{"recipients":["grp_reply"]}')`,
		`INSERT INTO messages(msg_id,thr_id,seq,sender,kind,created_at,body) VALUES('msg_retained','thr_missing_first',2,'peer_old','message','2026-11-15T00:00:01.000Z','{"recipients":["grp_reply"]}')`,
		`INSERT INTO idempotency(sender,key,body_sha256,response,created_at) VALUES('peer_old','legacy','abc',' {"message_id":"msg_primary"} ','2026-11-15T00:00:00.000Z')`,
	} {
		a1Exec(t, db, q)
	}
	applied, err := Migrate(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(applied, []string{"0038_group_thread_creations.sql"}) {
		t.Fatalf("upgrade applied %v", applied)
	}
	rows, err := db.Query(`SELECT group_id,thread_id,created_at FROM group_thread_creations ORDER BY group_id,thread_id`)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for rows.Next() {
		var group, thread, at string
		if err := rows.Scan(&group, &thread, &at); err != nil {
			t.Fatal(err)
		}
		got = append(got, group+"/"+thread+"/"+at)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	rows.Close()
	want := []string{"grp_a/thr_primary/2026-11-15T00:00:00.000Z", "grp_a/thr_primary_no_message/2026-11-15T00:00:00.000Z", "grp_b/thr_primary/2026-11-15T00:00:00.000Z", "grp_b/thr_secondary/2026-11-15T00:00:00.000Z", "team_a/thr_secondary/2026-11-15T00:00:00.000Z", "team_a/thr_team_primary/2026-11-15T00:00:00.000Z", "team_b/thr_primary/2026-11-15T00:00:00.000Z"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("backfilled %v, want %v", got, want)
	}
	var body, response string
	if err := db.QueryRow(`SELECT body FROM messages WHERE msg_id='msg_primary'`).Scan(&body); err != nil || body != legacy {
		t.Fatalf("legacy message changed: %q %v", body, err)
	}
	if err := db.QueryRow(`SELECT response FROM idempotency WHERE key='legacy'`).Scan(&response); err != nil || response != ` {"message_id":"msg_primary"} ` {
		t.Fatalf("legacy response changed: %q %v", response, err)
	}
	if applied, err := Migrate(context.Background(), db); err != nil || len(applied) != 0 {
		t.Fatalf("reapply=%v %v", applied, err)
	}
	// Both parent deletions clean up only their own accounting rows.
	a1Exec(t, db, `DELETE FROM threads WHERE thr_id='thr_primary'`)
	a1Exec(t, db, `DELETE FROM groups WHERE grp_id='grp_a'`)
	a1Exec(t, db, `DELETE FROM teams WHERE team_id='team_a'`)
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM group_thread_creations`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("cascade rows=%d %v", n, err)
	}
	if _, err := db.Exec(`INSERT INTO group_thread_creations(group_id,thread_id,created_at) VALUES('grp_missing','thr_secondary','2026-11-15T00:00:00.000Z')`); err == nil {
		t.Fatal("orphan group history accepted")
	}
	if _, err := db.Exec(`INSERT INTO group_thread_creations(group_id,thread_id,created_at) VALUES('grp_b','thr_missing','2026-11-15T00:00:00.000Z')`); err == nil {
		t.Fatal("orphan thread history accepted")
	}
	if _, err := db.Exec(`INSERT INTO group_thread_creations(group_id,thread_id,created_at) VALUES('grp_b','thr_secondary','2026-11-15T00:00:00.000Z')`); err == nil {
		t.Fatal("duplicate group/thread counted")
	}
}
