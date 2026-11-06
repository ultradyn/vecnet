package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
)

func TestBoundedRetentionMigrationsPreserveCopiesOnUpgrade(t *testing.T) {
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
		if strings.HasSuffix(m.name, "_callback_retention_lookups.sql") {
			found = true
			break
		}
		if _, err := applyOne(ctx, raw, m); err != nil {
			t.Fatal(err)
		}
	}
	if !found {
		t.Fatal("missing bounded pin index migration")
	}
	if _, err := raw.Exec(`INSERT INTO event_outbox(sub_id,event_id,seq,body,created_at,done_at,terminal,parked) VALUES
  ('sub_synthetic','pending',1,'{"synthetic":"pending bytes"}','2026-11-01T00:00:00.000Z',NULL,NULL,0),
  ('sub_synthetic','parked',2,'{"synthetic":"parked bytes"}','2026-11-01T00:00:00.000Z',NULL,NULL,1),
  ('sub_synthetic','exhausted',3,'{"synthetic":"exhausted bytes"}','2026-11-01T00:00:00.000Z','2026-11-01T00:00:00.000Z','exhausted',0),
  ('sub_synthetic','terminal',4,'{"synthetic":"terminal bytes"}','2026-11-01T00:00:00.000Z','2026-11-01T00:00:00.000Z','http_410',0)`); err != nil {
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		db, err := Open(path)
		if err != nil {
			t.Fatal(err)
		}
		func() {
			defer db.Close()
			var count int
			if err := db.QueryRow(`SELECT count(*) FROM event_outbox WHERE body='{"synthetic":"'||event_id||' bytes"}' AND
    (event_id='pending' AND done_at IS NULL AND terminal IS NULL AND parked=0 OR
     event_id='parked' AND done_at IS NULL AND terminal IS NULL AND parked=1 OR
     event_id='exhausted' AND done_at IS NOT NULL AND terminal='exhausted' OR
     event_id='terminal' AND done_at IS NOT NULL AND terminal='http_410')`).Scan(&count); err != nil || count != 4 {
				t.Fatalf("upgrade changed callbacks: count=%d err=%v", count, err)
			}
			if err := db.QueryRow(`SELECT count(*) FROM event_outbox INDEXED BY event_outbox_retention_candidate WHERE seq IN (1,2,3,4) AND (done_at IS NULL OR terminal='exhausted')`).Scan(&count); err != nil || count != 3 {
				t.Fatalf("candidate index omitted retry pins: count=%d err=%v", count, err)
			}
			if err := db.QueryRow(`SELECT count(*) FROM event_retention_rule_nodes`).Scan(&count); err != nil || count != 0 {
				t.Fatalf("migration added policy authority: count=%d err=%v", count, err)
			}
		}()
	}
}
