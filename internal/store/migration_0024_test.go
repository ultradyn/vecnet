package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"reflect"
	"testing"
)

// TestMigration0024UpgradesFrom0022: a database at master's 0022 (hub
// preferences) upgrades through the DB-compaction migrations 0023 (event
// payload references) and 0024 (backfill exceptions) without touching
// existing event bytes, the hub identity or the preferences state. Migrate
// applies every later migration too (0025 R2 blobs since the r2 merge, 0026
// route usage); its own upgrade guard is TestMigration0025UpgradesFrom0024.
func TestMigration0024UpgradesFrom0022(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "upgrade.db")+"?"+dsnParams)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	migs, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE schema_migrations(name TEXT PRIMARY KEY,sha256 TEXT NOT NULL,applied_at TEXT NOT NULL) STRICT`); err != nil {
		t.Fatal(err)
	}
	for _, m := range migs {
		if m.name > "0023" {
			break
		}
		if _, err := applyOne(ctx, db, m); err != nil {
			t.Fatal(err)
		}
	}
	var last string
	if err := db.QueryRow(`SELECT max(name) FROM schema_migrations`).Scan(&last); err != nil || last != "0022_hub_preferences.sql" {
		t.Fatalf("precondition: last applied = %q, %v; want 0022_hub_preferences.sql", last, err)
	}
	var seed string
	if err := db.QueryRow(`SELECT value FROM meta WHERE key='hub_identity_seed'`).Scan(&seed); err != nil || seed == "" {
		t.Fatalf("precondition: identity seed %q, %v", seed, err)
	}
	const legacy = ` { "message": {"message_id":"msg_old","metadata":{"n":1.50}}, "thread":{"title":"old"} } `
	if _, err := db.Exec(`INSERT INTO events(owner,type,at,data) VALUES ('peer_old','delivery','now',?)`, legacy); err != nil {
		t.Fatal(err)
	}

	applied, err := Migrate(ctx, db)
	if err != nil || !reflect.DeepEqual(applied, []string{"0023_event_payload_refs.sql", "0024_backfill_exceptions.sql", "0025_r2_blobs.sql", "0026_route_usage.sql", "0027_route_attempts.sql"}) {
		t.Fatalf("upgrade applied %v, %v; want 0023 through 0027", applied, err)
	}
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM schema_migrations WHERE name IN ('0021_hub_identity_notices.sql', '0022_hub_preferences.sql', '0023_event_payload_refs.sql', '0024_backfill_exceptions.sql')`).Scan(&n); err != nil || n != 4 {
		t.Fatalf("recorded 0021-0024 = %d, %v", n, err)
	}
	var data, format string
	var msg, snapshot sql.NullString
	if err := db.QueryRow(`SELECT data,payload_format,msg_id,snapshot_id FROM events`).Scan(&data, &format, &msg, &snapshot); err != nil {
		t.Fatal(err)
	}
	if data != legacy || format != "inline" || msg.Valid || snapshot.Valid {
		t.Fatal("upgrade changed legacy event bytes or reference defaults")
	}
	for _, name := range []string{"event_message_snapshots", "delivery_backfill_exceptions", "hub_preferences", "hub_preferences_state"} {
		if err := db.QueryRow(`SELECT count(*) FROM pragma_table_list WHERE name=? AND strict=1`, name).Scan(&n); err != nil || n != 1 {
			t.Fatalf("STRICT %s: %d, %v", name, n, err)
		}
	}
	for _, index := range []string{"events_msg_ref", "events_snapshot_ref", "deliveries_event_seq_hash", "events_backfill_inline_delivery_seq"} {
		if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='index' AND name=?`, index).Scan(&n); err != nil || n != 1 {
			t.Fatalf("index %s: %d, %v", index, n, err)
		}
	}
	var revision int
	if err := db.QueryRow(`SELECT revision FROM hub_preferences_state WHERE id=1`).Scan(&revision); err != nil || revision != 0 {
		t.Fatalf("preferences revision = %d, %v", revision, err)
	}
	var got string
	if err := db.QueryRow(`SELECT value FROM meta WHERE key='hub_identity_seed'`).Scan(&got); err != nil || got != seed {
		t.Fatalf("identity seed changed across 0023-0024: %v", err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM meta WHERE key='event_payload_refs_enabled'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("migration enabled reference writes: %d, %v", n, err)
	}
	// Reapply verifies every recorded checksum and applies nothing.
	if applied, err := Migrate(ctx, db); err != nil || len(applied) != 0 {
		t.Fatalf("reapply = %v, %v", applied, err)
	}
}
