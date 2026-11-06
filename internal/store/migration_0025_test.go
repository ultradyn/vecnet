package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"reflect"
	"testing"
)

// TestMigration0025UpgradesFrom0024: a database at master's 0024 (DB
// compaction backfill exceptions) upgrades by applying only 0025 (R2 blob
// objects, numbered 0021 on the r2-build branch and renumbered at its merge),
// which adds the R2 tables without touching legacy blob rows, event bytes,
// the hub identity or the preferences state. Migrate also applies 0026
// (route usage), which only adds its own tables.
func TestMigration0025UpgradesFrom0024(t *testing.T) {
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
		if m.name > "0025" {
			break
		}
		if _, err := applyOne(ctx, db, m); err != nil {
			t.Fatal(err)
		}
	}
	var last string
	if err := db.QueryRow(`SELECT max(name) FROM schema_migrations`).Scan(&last); err != nil || last != "0024_backfill_exceptions.sql" {
		t.Fatalf("precondition: last applied = %q, %v; want 0024_backfill_exceptions.sql", last, err)
	}
	var seed string
	if err := db.QueryRow(`SELECT value FROM meta WHERE key='hub_identity_seed'`).Scan(&seed); err != nil || seed == "" {
		t.Fatalf("precondition: identity seed %q, %v", seed, err)
	}
	const legacy = ` { "message": {"message_id":"msg_old","metadata":{"n":1.50}}, "thread":{"title":"old"} } `
	const hash = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	for _, q := range []string{
		`INSERT INTO events(owner,type,at,data) VALUES ('peer_old','delivery','now','` + legacy + `')`,
		`INSERT INTO blobs(sha256,size,media_type,uploader,created_at) VALUES ('` + hash + `',7,'text/plain','peer_old','2026-11-01T00:00:00.000Z')`,
		`INSERT INTO blob_refs(sha256,msg_id) VALUES ('` + hash + `','msg_old')`,
		`INSERT INTO blob_quota(peer_id,day,bytes) VALUES ('peer_old','2026-11-01',7)`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	before := map[string][]byte{}
	for _, table := range []string{"events", "blobs", "blob_refs", "blob_quota", "hub_preferences", "hub_preferences_state"} {
		before[table] = r2Snapshot(t, db, table)
	}

	applied, err := Migrate(ctx, db)
	if err != nil || !reflect.DeepEqual(applied, []string{"0025_r2_blobs.sql", "0026_route_usage.sql"}) {
		t.Fatalf("upgrade applied %v, %v; want 0025 and 0026", applied, err)
	}
	for table, want := range before {
		if !reflect.DeepEqual(want, r2Snapshot(t, db, table)) {
			t.Errorf("0025 changed existing %s rows", table)
		}
	}
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM schema_migrations WHERE name IN ('0022_hub_preferences.sql', '0023_event_payload_refs.sql', '0024_backfill_exceptions.sql', '0025_r2_blobs.sql')`).Scan(&n); err != nil || n != 4 {
		t.Fatalf("recorded 0022-0025 = %d, %v", n, err)
	}
	for _, name := range []string{"blob_objects", "blob_upload_requests", "blob_object_refs", "blob_r2_quota", "blob_r2_storage", "durable_blob_payloads", "durable_blob_refs", "blob_purge_tombstones", "blob_delete_jobs", "blob_usage_events"} {
		if err := db.QueryRow(`SELECT count(*) FROM pragma_table_list WHERE name=? AND strict=1`, name).Scan(&n); err != nil || n != 1 {
			t.Fatalf("STRICT %s: %d, %v", name, n, err)
		}
	}
	var reserved, retained int64
	if err := db.QueryRow(`SELECT storage_reserved_bytes, retained_bytes FROM blob_r2_storage WHERE singleton=1`).Scan(&reserved, &retained); err != nil || reserved != 0 || retained != 0 {
		t.Fatalf("storage singleton = %d/%d, %v", reserved, retained, err)
	}
	// The R2 migration seeds no preference: blobs.expiry has no installed
	// consumer and stays unset.
	if err := db.QueryRow(`SELECT count(*) FROM hub_preferences WHERE key LIKE 'blobs.%'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("blob preferences stored: %d, %v", n, err)
	}
	var got string
	if err := db.QueryRow(`SELECT value FROM meta WHERE key='hub_identity_seed'`).Scan(&got); err != nil || got != seed {
		t.Fatalf("identity seed changed across 0025: %v", err)
	}
	// Reapply verifies every recorded checksum and applies nothing.
	if applied, err := Migrate(ctx, db); err != nil || len(applied) != 0 {
		t.Fatalf("reapply = %v, %v", applied, err)
	}
}
