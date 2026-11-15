package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"reflect"
	"testing"
)

func TestGrokbotRecipientDrainMigrationUpgradeReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "drain-upgrade.db")
	raw, err := sql.Open("sqlite", "file:"+path+"?"+dsnParams)
	if err != nil {
		t.Fatal(err)
	}
	a1Exec(t, raw, `CREATE TABLE schema_migrations(name TEXT PRIMARY KEY,sha256 TEXT NOT NULL,applied_at TEXT NOT NULL) STRICT`)
	migrations, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range migrations {
		if m.name < "0039" {
			if _, err := applyOne(ctx, raw, m); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, q := range []string{
		`INSERT INTO routes(route_id,owner_kind,owner_id,kind,url,verified_at,created_at,key_enc) VALUES ('route_old','peer','peer_old','grokbot','https://example.test/routine','2026-11-15T00:00:00.000Z','2026-11-15T00:00:00.000Z','v1:synthetic')`,
		`INSERT INTO events(seq,owner,type,at,data) VALUES (1,'peer_old','delivery','2026-11-15T00:00:00.000Z','{ "frozen": "\u003c & \/", "number": 1.50 }')`,
		`INSERT INTO messages(msg_id,thr_id,seq,sender,kind,created_at,body) VALUES ('msg_old','thr_old',1,'peer_sender','message','2026-11-15T00:00:00.000Z','{"message_id":"msg_old"}')`,
		`INSERT INTO deliveries(dly_id,msg_id,thr_id,recipient,priority,state,attempt,attempts_left,next_attempt_at,route_id,held,expires_at,created_at,updated_at,event_seq,body_sha256) VALUES ('dly_old','msg_old','thr_old','peer_old','wake','queued',3,5,'2026-11-15T00:01:00.000Z','route_old',0,'2026-12-01T00:00:00.000Z','2026-11-15T00:00:00.000Z','2026-11-15T00:00:00.000Z',1,'literal-old-hash')`,
		`INSERT INTO acks(owner,scope,seq,acked_at) VALUES ('peer_old','*',1,'2026-11-15T00:00:00.000Z')`,
		`INSERT INTO route_runs(route_id,at) VALUES ('route_old','2026-11-15T00:00:00.000Z')`,
	} {
		a1Exec(t, raw, q)
	}
	snapshots := map[string]string{}
	for _, q := range []string{`SELECT * FROM schema_migrations WHERE name<'0039' ORDER BY name`, `SELECT * FROM events`, `SELECT * FROM messages`, `SELECT * FROM deliveries`, `SELECT * FROM acks`, `SELECT route_id,at FROM route_runs`, `SELECT route_id,owner_kind,owner_id,kind,url,verified_at,created_at,key_enc,runs_per_hour FROM routes`} {
		snapshots[q] = a1Snapshot(t, raw, q)
	}
	applied, err := Migrate(ctx, raw)
	if err != nil || !reflect.DeepEqual(applied, []string{"0039_grokbot_recipient_drain.sql"}) {
		t.Fatalf("upgrade=%v err=%v", applied, err)
	}
	a1Unchanged(t, raw, snapshots)
	a1Count(t, raw, `SELECT count(*) FROM routes WHERE grokbot_transport='delivery_v1'`, 1)
	// Original route_id/at values are covered by snapshots; additive columns
	// must independently receive their declared historical-row defaults.
	a1Count(t, raw, `SELECT count(*) FROM route_runs WHERE grok_wake_token IS NULL`, 1)
	a1Count(t, raw, `SELECT count(*) FROM route_runs WHERE grok_wake_settled_200=0`, 1)
	a1Count(t, raw, `SELECT count(*) FROM grokbot_wakes`, 0)
	a1Count(t, raw, `SELECT count(*) FROM grokbot_covers`, 0)
	a1Count(t, raw, `SELECT count(*) FROM grokbot_legacy_claims`, 0)
	if _, err := raw.Exec(`UPDATE routes SET grokbot_transport='unknown'`); err == nil {
		t.Fatal("invalid capability accepted")
	}
	a1Exec(t, raw, `INSERT INTO grokbot_covers VALUES ('historical_route','dly_old','peer_old','2026-11-15T00:00:00.000Z')`)
	a1Exec(t, raw, `INSERT INTO grokbot_wakes(route_id,recipient,representative_id,phase,lease_until) VALUES ('historical_route','peer_old','dly_old','armed','2026-11-15T00:10:00.000Z')`)
	a1Exec(t, raw, `INSERT INTO grokbot_legacy_claims VALUES ('historical_route','peer_old','dly_old','2026-11-15T00:10:00.000Z')`)
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		db, err := Open(path)
		if err != nil {
			t.Fatal(err)
		}
		a1Unchanged(t, db.DB, snapshots)
		a1Count(t, db, `SELECT count(*) FROM route_runs WHERE grok_wake_token IS NULL`, 1)
		a1Count(t, db, `SELECT count(*) FROM route_runs WHERE grok_wake_settled_200=0`, 1)
		a1Count(t, db, `SELECT count(*) FROM grokbot_legacy_claims WHERE route_id='historical_route' AND recipient='peer_old' AND delivery_id='dly_old' AND lease_until='2026-11-15T00:10:00.000Z'`, 1)
		applied, err := Migrate(ctx, db.DB)
		if err != nil || len(applied) != 0 {
			t.Fatalf("reopen=%v %v", applied, err)
		}
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
	}
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	a1Exec(t, db.DB, `DELETE FROM deliveries WHERE dly_id='dly_old'`)
	a1Count(t, db, `SELECT count(*) FROM grokbot_covers`, 0)
	a1Count(t, db, `SELECT count(*) FROM grokbot_wakes WHERE phase='armed'`, 1)
	a1Count(t, db, `SELECT count(*) FROM grokbot_legacy_claims WHERE delivery_id='dly_old' AND lease_until='2026-11-15T00:10:00.000Z'`, 1)
}
