package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"reflect"
	"testing"
)

// Upgrading an existing hub must neither invent runtime status nor modify
// manual presence/quota; the new table must reject orphans and duplicate peers.
func TestRuntimeStatusMigrationUpgrade(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "upgrade.db")+"?"+dsnParams)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE schema_migrations(name TEXT PRIMARY KEY,sha256 TEXT NOT NULL,applied_at TEXT NOT NULL) STRICT`); err != nil {
		t.Fatal(err)
	}
	migs, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	var pending []string
	for _, m := range migs {
		if m.name >= "0037" {
			pending = append(pending, m.name)
			continue
		}
		if _, err := applyOne(ctx, db, m); err != nil {
			t.Fatal(err)
		}
	}
	for _, q := range []string{
		`INSERT INTO principals(prn_id,name,created_at) VALUES ('prn_runtime','runtime principal','2026-11-15T00:00:00.000Z')`,
		`INSERT INTO peers(peer_id,prn_id,name,kind,enrolled_at,presence_state,presence_at,capabilities,quota,note) VALUES ('peer_runtime','prn_runtime','runtime peer','session','2026-11-15T00:00:00.000Z','working','2026-11-15T00:00:00.000Z','["manual"]','{"weekly_remaining_pct":42}','manual note')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	var prior string
	if err := db.QueryRow(`SELECT max(name) FROM schema_migrations`).Scan(&prior); err != nil || prior != "0036_thread_archive.sql" {
		t.Fatalf("fixture must match deployed migration 0036: %s, %v", prior, err)
	}
	if applied, err := Migrate(ctx, db); err != nil || !reflect.DeepEqual(applied, pending) {
		t.Fatalf("runtime upgrade=%v err=%v", applied, err)
	}
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM peer_runtime_status`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("upgrade should not invent status: rows=%d err=%v", n, err)
	}
	var state, caps, quota, note string
	if err := db.QueryRow(`SELECT presence_state,capabilities,quota,note FROM peers WHERE peer_id='peer_runtime'`).Scan(&state, &caps, &quota, &note); err != nil {
		t.Fatal(err)
	}
	if state != "working" || caps != `["manual"]` || quota != `{"weekly_remaining_pct":42}` || note != "manual note" {
		t.Fatalf("manual fields changed: %q %q %q %q", state, caps, quota, note)
	}
	insert := `INSERT INTO peer_runtime_status(peer_id,runtime,state,delivery,updated_at,expires_at) VALUES (?,'codex','idle','ready','2026-11-15T00:00:00.000Z','2026-11-15T00:01:00.000Z')`
	if _, err := db.Exec(insert, "peer_missing"); err == nil {
		t.Fatal("orphan status accepted")
	}
	if _, err := db.Exec(insert, "peer_runtime"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(insert, "peer_runtime"); err == nil {
		t.Fatal("duplicate status accepted")
	}
	for _, q := range []string{`UPDATE peer_runtime_status SET state='unknown'`, `UPDATE peer_runtime_status SET state='offline'`, `UPDATE peer_runtime_status SET reason='stale'`, `UPDATE peer_runtime_status SET version=''`, `UPDATE peer_runtime_status SET expires_at=updated_at`} {
		if _, err := db.Exec(q); err == nil {
			t.Fatalf("invalid stored status accepted: %s", q)
		}
	}
	if applied, err := Migrate(ctx, db); err != nil || len(applied) != 0 {
		t.Fatalf("reapply=%v err=%v", applied, err)
	}
}
