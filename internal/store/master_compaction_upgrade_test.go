package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"reflect"
	"testing"
)

func TestCompactionMigrationsFreshAndUpgradeFromMaster0026(t *testing.T) {
	pending := []string{
		"0027_event_retention_floors.sql",
		"0028_event_outbox_retention_seq.sql",
		"0029_callback_retention_lookups.sql",
		"0030_event_retention_rule_intervals.sql",
		"0031_callback_retention_identity.sql",
		"0032_idempotency_created_at_index.sql",
		"0033_thread_seq_history_tombstones.sql",
		"0034_thread_history_loop_parent.sql",
	}
	for _, fromMaster := range []bool{false, true} {
		name := "fresh"
		if fromMaster {
			name = "master-0026"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			path := filepath.Join(t.TempDir(), "hub.db")
			db, err := sql.Open("sqlite", "file:"+path+"?"+dsnParams)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			migs, err := loadMigrations()
			if err != nil {
				t.Fatal(err)
			}
			if len(migs) < 34 {
				t.Fatalf("migration count=%d, want at least 34", len(migs))
			}
			want := make([]string, len(migs))
			for i, m := range migs {
				want[i] = m.name
			}
			before := map[string][]byte{}
			var identity string
			const body = ` { "message": {"message_id":"msg_old", "attachments":[{"blob_id":"blo_00000000000000000000000001","retention_class":"keep","expires_at":null}]}, "n":1.50 } `
			if fromMaster {
				if _, err := db.Exec(`CREATE TABLE schema_migrations(name TEXT PRIMARY KEY,sha256 TEXT NOT NULL,applied_at TEXT NOT NULL) STRICT`); err != nil {
					t.Fatal(err)
				}
				for _, m := range migs[:26] {
					if _, err := applyOne(ctx, db, m); err != nil {
						t.Fatal(err)
					}
				}
				var last string
				if err := db.QueryRow(`SELECT max(name) FROM schema_migrations`).Scan(&last); err != nil || last != "0026_peer_archive.sql" {
					t.Fatal("fixture is not at master's deployed 0026")
				}
				for _, q := range []string{
					`INSERT INTO principals(prn_id,name,created_at) VALUES('prn_old','old','2026-11-01T00:00:00.000Z')`,
					`INSERT INTO peers(peer_id,prn_id,name,kind,enrolled_at,archived_at) VALUES('peer_old','prn_old','archived','session','2026-11-01T00:00:00.000Z','2026-11-02T00:00:00.000Z')`,
					`UPDATE blob_r2_storage SET retained_bytes=19`,
					`INSERT INTO hub_preferences(key,value_json,updated_at,updated_by) VALUES('blobs.expiry','"6h"','2026-11-01T00:00:00.000Z','operator')`,
				} {
					if _, err := db.Exec(q); err != nil {
						t.Fatal(err)
					}
				}
				if _, err := db.Exec(`INSERT INTO events(owner,type,at,data) VALUES('peer_old','delivery','2026-11-01T00:00:00.000Z',?)`, body); err != nil {
					t.Fatal(err)
				}
				if err := db.QueryRow(`SELECT value FROM meta WHERE key='hub_identity_seed'`).Scan(&identity); err != nil {
					t.Fatal(err)
				}
				for _, table := range []string{"peers", "events", "blob_r2_storage", "hub_preferences", "hub_preferences_state"} {
					before[table] = r2Snapshot(t, db, table)
				}
				want = append(append([]string(nil), pending...), want[34:]...)
			}
			applied, err := Migrate(ctx, db)
			if err != nil || !reflect.DeepEqual(applied, want) {
				t.Fatalf("applied=%v err=%v want=%v", applied, err, want)
			}
			if fromMaster {
				for table, rows := range before {
					if !reflect.DeepEqual(rows, r2Snapshot(t, db, table)) {
						t.Errorf("compaction changed master %s rows", table)
					}
				}
				var got string
				if err := db.QueryRow(`SELECT value FROM meta WHERE key='hub_identity_seed'`).Scan(&got); err != nil || got != identity {
					t.Fatal("identity changed")
				}
			}
			// Every master checksum and every new migration is verified on reapply.
			if applied, err := Migrate(ctx, db); err != nil || len(applied) != 0 {
				t.Fatalf("reapply=%v err=%v", applied, err)
			}
			var integrity string
			if err := db.QueryRow(`PRAGMA integrity_check`).Scan(&integrity); err != nil || integrity != "ok" {
				t.Fatal("migration integrity failed")
			}
			rows, err := db.Query(`PRAGMA foreign_key_check`)
			if err != nil {
				t.Fatal(err)
			}
			violations := rows.Next()
			err = rows.Err()
			rows.Close()
			if err != nil || violations {
				t.Fatal("migration foreign key check failed")
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			reopened, err := Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			if fromMaster {
				var got string
				if err := reopened.QueryRow(`SELECT data FROM events WHERE owner='peer_old'`).Scan(&got); err != nil || got != body {
					t.Fatal("reopen changed master delivery bytes")
				}
			}
		})
	}
}
