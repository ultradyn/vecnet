package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
)

func TestPreferencesMigrationFreshUpgradeReopen(t *testing.T) {
	ctx := context.Background()
	migs, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	var prefsMigration migration
	count := 0
	for _, m := range migs {
		if strings.HasSuffix(m.name, "_hub_preferences.sql") {
			prefsMigration = m
			count++
		}
	}
	if count != 1 {
		t.Fatalf("preferences migration count=%d, want exactly one", count)
	}
	for _, upgrade := range []bool{false, true} {
		t.Run(map[bool]string{false: "fresh", true: "upgrade"}[upgrade], func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "hub.db")
			if upgrade {
				raw, err := sql.Open("sqlite", "file:"+path+"?"+dsnParams)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := raw.Exec(`CREATE TABLE schema_migrations(name TEXT PRIMARY KEY,sha256 TEXT NOT NULL,applied_at TEXT NOT NULL) STRICT`); err != nil {
					t.Fatal(err)
				}
				for _, m := range migs {
					if m.name == prefsMigration.name {
						continue
					}
					if _, err := applyOne(ctx, raw, m); err != nil {
						t.Fatal(err)
					}
				}
				if _, err := raw.Exec(`INSERT INTO meta VALUES ('prefs_upgrade_probe','unchanged')`); err != nil {
					t.Fatal(err)
				}
				raw.Close()
			}
			db, err := Open(path)
			if err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{"hub_preferences", "hub_preferences_state", "hub_preference_activations"} {
				var n int
				if err := db.QueryRow(`SELECT count(*) FROM pragma_table_list WHERE name=? AND strict=1`, name).Scan(&n); err != nil || n != 1 {
					t.Fatalf("STRICT %s: %d %v", name, n, err)
				}
			}
			var revision int
			if err := db.QueryRow(`SELECT revision FROM hub_preferences_state WHERE id=1`).Scan(&revision); err != nil || revision != 0 {
				t.Fatalf("initial revision %d %v", revision, err)
			}
			for _, q := range []string{`INSERT INTO hub_preferences VALUES ('bad','invalid json','now','operator')`, `INSERT INTO hub_preferences_state VALUES (2,0)`, `UPDATE hub_preferences_state SET revision=-1`, `INSERT INTO hub_preferences VALUES ('bad','1',NULL,'operator')`} {
				if _, err := db.Exec(q); err == nil {
					t.Fatalf("constraint accepted: %s", q)
				}
			}
			if _, err := db.Exec(`INSERT INTO hub_preferences VALUES ('agents.thread_budget','600','now','operator'); UPDATE hub_preferences_state SET revision=1; INSERT INTO hub_preference_activations VALUES (1,'retention.history','now',NULL,NULL,NULL,'digest')`); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`INSERT INTO hub_preference_activations VALUES (1,'retention.history','now',NULL,NULL,NULL,'digest')`); err == nil {
				t.Fatal("duplicate activation accepted")
			}
			db.Close()
			db, err = Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			var value string
			if err := db.QueryRow(`SELECT value_json FROM hub_preferences`).Scan(&value); err != nil || value != "600" {
				t.Fatalf("reopen: %s %v", value, err)
			}
			if err := db.QueryRow(`SELECT revision FROM hub_preferences_state`).Scan(&revision); err != nil || revision != 1 {
				t.Fatalf("reopen revision %d %v", revision, err)
			}
			if upgrade {
				if value, err := db.Meta(ctx, "prefs_upgrade_probe"); err != nil || value != "unchanged" {
					t.Fatalf("upgrade changed data: %s %v", value, err)
				}
			}
			if applied, err := Migrate(ctx, db.DB); err != nil || len(applied) != 0 {
				t.Fatalf("reopen migrations: %v %v", applied, err)
			}
		})
	}
}

func TestPreferencesPreservesShippedMigrationChecksums(t *testing.T) {
	want := map[string]string{
		"0001_init.sql":                      "195af58e5296ff3e52bb8591ee29ebb9a111030f237ed6c35daaf3ec77d9473c",
		"0002_reach_via.sql":                 "753f2afebea463d9b1ea74ebf1a29d5c56e91fdbc55cfe18198d708d7f87c847",
		"0003_blob_uploaders.sql":            "29b303a6c105b2303d0da209df4a320423be9bd2294b551eedff6430201d7b7b",
		"0004_delivery_read_at.sql":          "4b207098f1354de932b90ba2f42a261d1beeebd486f14fa212b9e53e926ec80a",
		"0005_event_subscriptions.sql":       "6ef976d9ea52c2a8ebdb93acbb05c0c7c43e89b36dc918e7c1f5de26ef82fd33",
		"0006_oauth.sql":                     "281dc4619aade43fef5065361dc6cb941c62f4fb6aa2e8fead50cd757da37e3d",
		"0007_grokbot_routes.sql":            "a869d3e720a5abc14ce4919fac6667d5d30c2c9ca81da566fcd4d837bd91a539",
		"0008_sources.sql":                   "a4efe7bf67c42e5a14fb38079c677b4cbf534125dac56908876bc507de7face6",
		"0009_connector_hardening.sql":       "d66bfb3af52d56d56245daa975c7441c5636c8f31d42ac5812a56d9362c162a9",
		"0010_audit_action_index.sql":        "0c749aabd643e0efbf8445b9c02fc64da1a4cd2d090f0437359f10f1dd024427",
		"0011_inbox_indexes.sql":             "ca94163a9a7517524c3837669e66924b60a6d7b33b99c4e5ef71b2bdf042eea1",
		"0012_reactions.sql":                 "243071a9366c397ab0915349c3f71de75f847a3666b7668b5fc4b13e58e96b02",
		"0013_delivery_dispatching.sql":      "333d27ff98faf84c60d9bd6a7b9f43a7c6daad1a4a6fb419f8bef398cf843877",
		"0014_principal_peers.sql":           "673afb4824b04e2017b3c0e6df2a030fa29a505e5fbea3319698ba478a95eb77",
		"0015_reserved.sql":                  "973fdf9f6751cfe7350ee34415baf3f5306c66aa0a4cb0520461063414dcdfb5",
		"0016_peer_avatars.sql":              "a485b95dba23bd14bb27b026cc7433260fc23122d077ce104399cc99b38facb7",
		"0017_peer_name_open.sql":            "8a0c36cf945ef42ad9f91b90844c7830359be3917c71fc8e448adceb8cf8854b",
		"0018_sender_info.sql":               "c1ccdbb738504dd411caec9f358d7d900c3f41a24bd351e4f3d19580fe163cdf",
		"0019_event_subscription_status.sql": "4a8a9112b508d0393619b354f4bfad44f4157abc0ef37294624ac6106c3a71a8",
		"0020_event_refresh_before.sql":      "2ac7dd14e99ea10f8864c896a772b71a5d06fd6664343e6cd66b8c2044082bc2",
	}
	migs, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range migs {
		if sum, ok := want[m.name]; ok {
			if m.sum != sum {
				t.Errorf("shipped migration %s checksum changed", m.name)
			}
			delete(want, m.name)
		}
	}
	if len(want) != 0 {
		t.Fatalf("missing shipped migrations: %v", want)
	}
}
