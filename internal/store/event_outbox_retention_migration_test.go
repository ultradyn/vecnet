package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
)

func TestEventOutboxRetentionIndexUpgrade(t *testing.T) {
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
	defer raw.Close()
	if _, err := raw.Exec(`CREATE TABLE schema_migrations(name TEXT PRIMARY KEY,sha256 TEXT NOT NULL,applied_at TEXT NOT NULL) STRICT`); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, m := range migrations {
		if strings.HasSuffix(m.name, "_event_outbox_retention_seq.sql") {
			found = true
			break
		}
		if _, err := applyOne(ctx, raw, m); err != nil {
			t.Fatal(err)
		}
	}
	if !found {
		t.Fatal("missing retention exact-copy index migration")
	}
	if _, err := raw.Exec(`INSERT INTO event_outbox(sub_id,event_id,seq,body,created_at,done_at,terminal) VALUES
		('sub_synthetic','pending',1,'{}','2026-11-01T00:00:00.000Z',NULL,NULL),
		('sub_synthetic','exhausted',2,'{}','2026-11-01T00:00:00.000Z','2026-11-01T00:00:00.000Z','exhausted'),
		('sub_synthetic','terminal',3,'{}','2026-11-01T00:00:00.000Z','2026-11-01T00:00:00.000Z','http_410')`); err != nil {
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}
	// Open twice: applying the additive migration and then validating its
	// checksum on reopen must preserve pending, exhausted and terminal copies.
	for range 2 {
		db, err := Open(path)
		if err != nil {
			t.Fatal(err)
		}
		func() {
			defer db.Close()
			var n int
			if err := db.QueryRow(`SELECT count(*) FROM event_outbox WHERE sub_id='sub_synthetic' AND body='{}' AND
				(event_id='pending' AND seq=1 AND done_at IS NULL AND terminal IS NULL OR
				event_id='exhausted' AND seq=2 AND done_at IS NOT NULL AND terminal='exhausted' OR
				event_id='terminal' AND seq=3 AND done_at IS NOT NULL AND terminal='http_410')`).Scan(&n); err != nil || n != 3 {
				t.Fatalf("upgrade changed callback copies: count=%d err=%v", n, err)
			}
			var ddl string
			if err := db.QueryRow(`SELECT sql FROM sqlite_schema WHERE name='event_outbox_retention_seq'`).Scan(&ddl); err != nil || strings.Contains(strings.ToUpper(ddl), "WHERE") {
				t.Fatalf("exact-copy index must cover all states: err=%v", err)
			}
			rows, err := db.Query(`EXPLAIN QUERY PLAN SELECT 1 FROM event_outbox WHERE sub_id=? AND seq=?`, "sub_synthetic", 2)
			if err != nil {
				t.Fatal(err)
			}
			defer rows.Close()
			indexed := false
			for rows.Next() {
				var id, parent, unused int
				var detail string
				if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
					t.Fatal(err)
				}
				t.Log(detail)
				indexed = indexed || strings.Contains(detail, "event_outbox_retention_seq (sub_id=? AND seq=?)")
			}
			if err := rows.Err(); err != nil {
				t.Fatal(err)
			}
			if !indexed {
				t.Fatal("exact-copy query did not seek both subscription and seq")
			}
		}()
	}
}
