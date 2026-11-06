package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	"modernc.org/sqlite"
)

func retentionPages(t *testing.T, db *sql.DB, reset bool) int {
	t.Helper()
	c, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	total := 0
	if err := c.Raw(func(raw any) error {
		for _, op := range []sqlite.DBStatusOp{sqlite.DBStatusCacheHit, sqlite.DBStatusCacheMiss} {
			n, _, err := raw.(sqlite.DBStatus).Status(op, reset)
			if err != nil {
				return err
			}
			total += n
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return total
}

func TestRetentionFloorMigrationIndependentOfEventCount(t *testing.T) {
	for _, analyze := range []bool{false, true} {
		small := 0
		for _, n := range []int{100, 100000} {
			db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "hub.db")+"?"+dsnParams)
			if err != nil {
				t.Fatal(err)
			}
			db.SetMaxOpenConns(1)
			func() {
				defer db.Close()
				if _, err := db.Exec(`CREATE TABLE schema_migrations(name TEXT PRIMARY KEY,sha256 TEXT NOT NULL,applied_at TEXT NOT NULL) STRICT`); err != nil {
					t.Fatal(err)
				}
				migrations, err := loadMigrations()
				if err != nil {
					t.Fatal(err)
				}
				var floor migration
				for _, m := range migrations {
					if strings.HasSuffix(m.name, "_event_retention_floors.sql") {
						floor = m
						break
					}
					if _, err := applyOne(context.Background(), db, m); err != nil {
						t.Fatal(err)
					}
				}
				if floor.name == "" {
					t.Fatal("floor migration missing")
				}
				if _, err := db.Exec(`INSERT INTO principals(prn_id,name,created_at) VALUES('prn_synthetic','synthetic','2026-01-01T00:00:00.000Z');
     INSERT INTO peers(peer_id,prn_id,name,kind,enrolled_at) VALUES('peer_synthetic','prn_synthetic','synthetic','session','2026-01-01T00:00:00.000Z');
     WITH RECURSIVE n(i) AS (VALUES(1) UNION ALL SELECT i+1 FROM n WHERE i<?)
     INSERT INTO events(owner,type,at,data) SELECT 'peer_synthetic','presence','2026-01-01T00:00:00.000Z','{}' FROM n`, n); err != nil {
					t.Fatal(err)
				}
				if analyze {
					if _, err := db.Exec(`ANALYZE`); err != nil {
						t.Fatal(err)
					}
				}
				retentionPages(t, db, true)
				if _, err := applyOne(context.Background(), db, floor); err != nil {
					t.Fatal(err)
				}
				pages := retentionPages(t, db, false)
				t.Logf("analyze=%v events=%d migration_pages=%d", analyze, n, pages)
				if n == 100 {
					small = pages
				} else if pages > small+64 {
					t.Errorf("floor migration scans retained events: small=%d large=%d", small, pages)
				}
				wrapped := &DB{db}
				retentionPages(t, db, true)
				done, err := wrapped.eventRetentionInitSlice(context.Background())
				if err != nil || done {
					t.Fatalf("first owner slice: done=%v err=%v", done, err)
				}
				initPages := retentionPages(t, db, false)
				t.Logf("analyze=%v events=%d owner_slice_pages=%d", analyze, n, initPages)
				if initPages > 100 {
					t.Errorf("one-owner initialization scans events: pages=%d", initPages)
				}
				done, err = wrapped.eventRetentionInitSlice(context.Background())
				if err != nil || !done {
					t.Fatalf("completion slice: done=%v err=%v", done, err)
				}
				var count, gotFloor int
				if err := db.QueryRow(`SELECT count(*),coalesce(max(purged_through_seq),0) FROM event_retention`).Scan(&count, &gotFloor); err != nil || count != 1 || gotFloor != 0 {
					t.Fatalf("floors count=%d floor=%d err=%v", count, gotFloor, err)
				}

			}()
		}
	}
}

func TestRetentionFloorInitializationCheckpointRollbackAndReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "hub.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { db.Close() }()
	// Simulate an interrupted post-DDL startup, with peer-only and orphan-event
	// owners. A legacy-path deletion during initialization must never lower a floor.
	if _, err := db.Exec(`INSERT INTO principals(prn_id,name,created_at) VALUES('prn_synthetic','synthetic','2026-01-01T00:00:00.000Z');
 INSERT INTO peers(peer_id,prn_id,name,kind,enrolled_at) VALUES('peer_a','prn_synthetic','synthetic','session','2026-01-01T00:00:00.000Z'),('peer_c','prn_synthetic','synthetic','session','2026-01-01T00:00:00.000Z');
 INSERT INTO events(seq,owner,type,at,data) VALUES(10,'peer_b','presence','2026-01-01T00:00:00.000Z','{}'),(11,'peer_d','presence','2026-01-01T00:00:00.000Z','{}');
 UPDATE meta SET value='7' WHERE key='event_retention_legacy_floor';
 UPDATE meta SET value='' WHERE key='event_retention_init_owner';
 UPDATE meta SET value='0' WHERE key='event_retention_init_done';
 CREATE TRIGGER retention_init_fail BEFORE UPDATE ON meta WHEN NEW.key='event_retention_init_owner' BEGIN SELECT RAISE(ABORT,'synthetic_failure'); END;`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.eventRetentionInitSlice(ctx); err == nil {
		t.Fatal("injected checkpoint failure not reached")
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM event_retention`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("floor escaped failed checkpoint: count=%d err=%v", count, err)
	}
	var last string
	if err := db.QueryRow(`SELECT value FROM meta WHERE key='event_retention_init_owner'`).Scan(&last); err != nil || last != "" {
		t.Fatal("failed checkpoint advanced", err)
	}
	if _, err := db.Exec(`DROP TRIGGER retention_init_fail`); err != nil {
		t.Fatal(err)
	}
	done, err := db.eventRetentionInitSlice(ctx)
	if err != nil || done {
		t.Fatal("first slice", done, err)
	}
	if err := db.QueryRow(`SELECT value FROM meta WHERE key='event_retention_init_owner'`).Scan(&last); err != nil || last != "peer_a" {
		t.Fatal("missing committed owner checkpoint", err)
	}
	if _, err := db.Exec(`DELETE FROM events WHERE owner='peer_d'`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	// The retained orphan peer_b needs the event-index seek. peer_d vanished
	// from both discovery inputs, but its higher trigger floor must survive.
	for owner, want := range map[string]int{"peer_a": 7, "peer_b": 7, "peer_c": 7, "peer_d": 11} {
		var got int
		if err := db.QueryRow(`SELECT purged_through_seq FROM event_retention WHERE owner=?`, owner).Scan(&got); err != nil || got != want {
			t.Fatalf("owner=%s floor=%d want=%d err=%v", owner, got, want, err)
		}
	}
	if err := db.QueryRow(`SELECT value FROM meta WHERE key='event_retention_init_done'`).Scan(&last); err != nil || last != "1" {
		t.Fatal("Open exposed incomplete floors", err)
	}
}
