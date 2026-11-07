package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
)

func TestEventRetentionFloorsMigrationUpgradeAndReopen(t *testing.T) {
	ctx := context.Background()
	migrations, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "hub.db")
	raw, err := sql.Open("sqlite", "file:"+path+"?"+dsnParams)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`CREATE TABLE schema_migrations(name TEXT PRIMARY KEY,sha256 TEXT NOT NULL,applied_at TEXT NOT NULL) STRICT`); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, m := range migrations {
		if strings.HasSuffix(m.name, "_event_retention_floors.sql") {
			found = true
			break
		}
		if _, err := applyOne(ctx, raw, m); err != nil {
			t.Fatal(err)
		}
	}
	if !found {
		t.Fatal("missing floor migration")
	}
	if _, err := raw.Exec(`INSERT INTO events(owner,type,at,data) VALUES('peer_synthetic','thread_updated','2026-01-01T00:00:00.000Z','{}'),('peer_synthetic','thread_updated','2026-01-01T00:00:00.000Z','{}'); DELETE FROM events WHERE seq=1; INSERT INTO acks(owner,scope,seq,acked_at) VALUES('peer_synthetic','*',2,'2026-01-01T00:00:00.000Z')`); err != nil {
		t.Fatal(err)
	}
	raw.Close()
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	var floor, high, epoch int64
	if err := db.QueryRow(`SELECT purged_through_seq FROM event_retention WHERE owner='peer_synthetic'`).Scan(&floor); err != nil || floor != 1 {
		t.Fatalf("legacy floor=%d err=%v", floor, err)
	}
	if err := db.QueryRow(`SELECT CAST(value AS INTEGER) FROM meta WHERE key='event_retention_initial_high_water'`).Scan(&high); err != nil || high != 2 {
		t.Fatal("upgrade lost approval boundary")
	}
	if err := db.QueryRow(`SELECT epoch FROM acks`).Scan(&epoch); err != nil || epoch != 1 {
		t.Fatal("legacy ack epoch not preserved")
	}
	if _, err := db.Exec(`DELETE FROM events`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.QueryRow(`SELECT purged_through_seq FROM event_retention WHERE owner='peer_synthetic'`).Scan(&floor); err != nil || floor != 2 {
		t.Fatal("empty reopen lost floor")
	}
	if _, err := db.Exec(`INSERT INTO events(owner,type,at,data) VALUES('peer_synthetic','thread_updated','2026-01-01T00:00:00.000Z','{}')`); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT seq FROM events`).Scan(&high); err != nil || high != 3 {
		t.Fatal("empty reopen reused seq")
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`DELETE FROM events`); err != nil {
		t.Fatal(err)
	}
	tx.Rollback()
	if err := db.QueryRow(`SELECT purged_through_seq FROM event_retention WHERE owner='peer_synthetic'`).Scan(&floor); err != nil || floor != 2 {
		t.Fatal("rollback leaked floor")
	}
}
