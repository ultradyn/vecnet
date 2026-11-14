package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

func TestThreadArchiveMigration(t *testing.T) {
	for _, upgrade := range []bool{false, true} {
		t.Run(map[bool]string{false: "fresh", true: "upgrade"}[upgrade], func(t *testing.T) {
			var db *DB
			if !upgrade {
				var err error
				db, err = Open(filepath.Join(t.TempDir(), "archive.db"))
				if err != nil {
					t.Fatal(err)
				}
				defer db.Close()
			} else {
				raw := a1Raw(t)
				a1Exec(t, raw, `CREATE TABLE schema_migrations(name TEXT PRIMARY KEY,sha256 TEXT NOT NULL,applied_at TEXT NOT NULL) STRICT`)
				all, err := loadMigrations()
				if err != nil {
					t.Fatal(err)
				}
				for _, m := range all {
					if m.name >= "0036_" {
						continue
					}
					if _, err := applyOne(context.Background(), raw, m); err != nil {
						t.Fatal(err)
					}
				}
				a1Exec(t, raw, `INSERT INTO threads(thr_id,created_at,created_by,last_message_at) VALUES('thr_old','2026-01-01','peer_old','2026-01-01')`)
				for _, m := range all {
					if m.name == "0036_thread_archive.sql" {
						if _, err := applyOne(context.Background(), raw, m); err != nil {
							t.Fatal(err)
						}
					}
				}
				var at, by, un sql.NullString
				if err := raw.QueryRow(`SELECT archived_at,archived_by,unarchived_at FROM threads WHERE thr_id='thr_old'`).Scan(&at, &by, &un); err != nil {
					t.Fatal(err)
				}
				if at.Valid || by.Valid || un.Valid {
					t.Fatal("upgrade archived an existing thread")
				}
				if _, err := raw.Exec(`UPDATE threads SET archived_by='invalid'`); err == nil {
					t.Fatal("archived_by lacks check")
				}
				if _, err := raw.Exec(`SELECT unarchived_at FROM peers`); err != nil {
					t.Fatal(err)
				}
				return
			}
			var n int
			if err := db.QueryRow(`SELECT count(*) FROM pragma_table_info('threads') WHERE name IN ('archived_at','archived_by','unarchived_at')`).Scan(&n); err != nil || n != 3 {
				t.Fatalf("thread columns=%d %v", n, err)
			}
			if err := db.QueryRow(`SELECT count(*) FROM pragma_table_info('peers') WHERE name='unarchived_at'`).Scan(&n); err != nil || n != 1 {
				t.Fatalf("peer column=%d %v", n, err)
			}
			if err := db.QueryRow(`SELECT count(*) FROM sqlite_schema WHERE type='index' AND name='threads_archived'`).Scan(&n); err != nil || n != 1 {
				t.Fatalf("index=%d %v", n, err)
			}
		})
	}
}
