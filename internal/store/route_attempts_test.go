package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// A1 is schema-only. The parent owns execution and the later writer/read slices.
const a1FeatureName = "0035_route_attempts.sql"
const a1FeatureSum = "35e036b796f88a867f6461b0d7104d10f9fdb52ed625e3fc943fe23239c4fc42"
const a1Insert = `INSERT INTO route_attempts(route_id,delivery_id,at,outcome,http_status,error_class,body_bytes,latency_ms) VALUES(?,?,?,?,?,?,?,?)`

// Literal filename/byte pins from baseline-source.json, independently of the loader.
var a1HistoricalPins = [][2]string{
	{"0001_init.sql", "195af58e5296ff3e52bb8591ee29ebb9a111030f237ed6c35daaf3ec77d9473c"},
	{"0002_reach_via.sql", "753f2afebea463d9b1ea74ebf1a29d5c56e91fdbc55cfe18198d708d7f87c847"},
	{"0003_blob_uploaders.sql", "29b303a6c105b2303d0da209df4a320423be9bd2294b551eedff6430201d7b7b"},
	{"0004_delivery_read_at.sql", "4b207098f1354de932b90ba2f42a261d1beeebd486f14fa212b9e53e926ec80a"},
	{"0005_event_subscriptions.sql", "6ef976d9ea52c2a8ebdb93acbb05c0c7c43e89b36dc918e7c1f5de26ef82fd33"},
	{"0006_oauth.sql", "281dc4619aade43fef5065361dc6cb941c62f4fb6aa2e8fead50cd757da37e3d"},
	{"0007_grokbot_routes.sql", "a869d3e720a5abc14ce4919fac6667d5d30c2c9ca81da566fcd4d837bd91a539"},
	{"0008_sources.sql", "a4efe7bf67c42e5a14fb38079c677b4cbf534125dac56908876bc507de7face6"},
	{"0009_connector_hardening.sql", "d66bfb3af52d56d56245daa975c7441c5636c8f31d42ac5812a56d9362c162a9"},
	{"0010_audit_action_index.sql", "0c749aabd643e0efbf8445b9c02fc64da1a4cd2d090f0437359f10f1dd024427"},
	{"0011_inbox_indexes.sql", "ca94163a9a7517524c3837669e66924b60a6d7b33b99c4e5ef71b2bdf042eea1"},
	{"0012_reactions.sql", "243071a9366c397ab0915349c3f71de75f847a3666b7668b5fc4b13e58e96b02"},
	{"0013_delivery_dispatching.sql", "333d27ff98faf84c60d9bd6a7b9f43a7c6daad1a4a6fb419f8bef398cf843877"},
	{"0014_principal_peers.sql", "673afb4824b04e2017b3c0e6df2a030fa29a505e5fbea3319698ba478a95eb77"},
	{"0015_reserved.sql", "973fdf9f6751cfe7350ee34415baf3f5306c66aa0a4cb0520461063414dcdfb5"},
	{"0016_peer_avatars.sql", "a485b95dba23bd14bb27b026cc7433260fc23122d077ce104399cc99b38facb7"},
	{"0017_peer_name_open.sql", "8a0c36cf945ef42ad9f91b90844c7830359be3917c71fc8e448adceb8cf8854b"},
	{"0018_sender_info.sql", "c1ccdbb738504dd411caec9f358d7d900c3f41a24bd351e4f3d19580fe163cdf"},
	{"0019_event_subscription_status.sql", "4a8a9112b508d0393619b354f4bfad44f4157abc0ef37294624ac6106c3a71a8"},
	{"0020_event_refresh_before.sql", "2ac7dd14e99ea10f8864c896a772b71a5d06fd6664343e6cd66b8c2044082bc2"},
	{"0021_hub_identity_notices.sql", "04f0dedf59460225d3aeb803b42d16b1c09938495310ee85e19ae151b914962c"},
	{"0022_hub_preferences.sql", "f89d302c95329c53fb4efb9c83ab6b2c62b6004b3e96785229d66fa147c4cd83"},
	{"0023_event_payload_refs.sql", "f6127a6108f2ff93cb93a570bac75f37f8b231ef0d72750ef44fe365d5243fef"},
	{"0024_backfill_exceptions.sql", "f89e8b58ca55d57352dd93221fd9ccef2f664e0387aafac996e6931ed9dca156"},
	{"0025_r2_blobs.sql", "76b29c0609b8a4c339176444c80a22d505e26b6b805bfa088f9f699f5c7f4984"},
	{"0026_peer_archive.sql", "2cac00e8fa88eb723ac6f7119a37e5e4e762a8b094443bd8de06f8f0c7489309"},
	{"0027_event_retention_floors.sql", "c71f8a811b6aec0d575199d45441b248b3cf8581340c2437f23dc492b78629fe"},
	{"0028_event_outbox_retention_seq.sql", "2671b52be2727f9c627a32f36bf4d60eeec9d18ed2e9642f2cca2a508bebf98a"},
	{"0029_callback_retention_lookups.sql", "cdad4b82cebe6ce59113be393079a29de941be7af5e9888a6ff0211184606f4b"},
	{"0030_event_retention_rule_intervals.sql", "9126a82acd3e248da25c1b2b3621c57d6ab54a99f9549772620e9cd29cce0fdb"},
	{"0031_callback_retention_identity.sql", "5a0a8841423c565173928a7f1313e72e29542db8b6394769f1348ee802e0b47b"},
	{"0032_idempotency_created_at_index.sql", "52fa50168ccf96e0d955a6b0c6d3cd89c2b43aa4fd15da1ef9c9fe59d81378f6"},
	{"0033_thread_seq_history_tombstones.sql", "5c04514ec9cc5b74e6e2da69a7c77b064d4ca14ec329814734a98607534f1bae"},
	{"0034_thread_history_loop_parent.sql", "2a3bf7fa01dece6ff0805403a29c2627e04e7e800f37bdd09c00b20af6fa4ff1"},
}

func a1Inventory(t *testing.T) []migration {
	t.Helper()
	all, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) < 34 {
		t.Fatal("historical migrations removed")
	}
	for i, pin := range a1HistoricalPins {
		if all[i].name != pin[0] || all[i].sum != pin[1] {
			t.Fatalf("historical migration %d filename/bytes changed", i+1)
		}
	}
	return all
}

func a1Feature(t *testing.T) migration {
	t.Helper()
	all := a1Inventory(t)
	if len(all) != 35 {
		t.Fatalf("embedded inventory has %d migrations, want historical 34 plus %s", len(all), a1FeatureName)
	}
	if all[34].name != a1FeatureName || all[34].sum != a1FeatureSum {
		t.Fatal("allocated feature filename/bytes differ from reviewed candidate")
	}
	return all[34]
}

func a1Raw(t *testing.T) *sql.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "a1.db")
	db, err := sql.Open("sqlite", "file:"+uriPathEscaper.Replace(path)+"?"+dsnParams)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func a1Exec(t *testing.T, db *sql.DB, q string) {
	t.Helper()
	if _, err := db.Exec(q); err != nil {
		t.Fatal(err)
	}
}

func a1Old(t *testing.T) *sql.DB {
	t.Helper()
	db := a1Raw(t)
	a1Exec(t, db, `CREATE TABLE schema_migrations(name TEXT PRIMARY KEY,sha256 TEXT NOT NULL,applied_at TEXT NOT NULL) STRICT`)
	for _, m := range a1Inventory(t)[:34] {
		if did, err := applyOne(context.Background(), db, m); err != nil || !did {
			t.Fatalf("historical migration %s: applied=%v err=%v", m.name, did, err)
		}
	}
	return db
}

func a1Snapshot(t *testing.T, db *sql.DB, q string) string {
	t.Helper()
	rows, err := db.Query(q)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		t.Fatal(err)
	}
	out := [][]any{}
	for rows.Next() {
		values, ptrs := make([]any, len(cols)), make([]any, len(cols))
		for i := range values {
			ptrs[i] = &values[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			t.Fatal(err)
		}
		out = append(out, values)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func a1Count(t *testing.T, db interface{ QueryRow(string, ...any) *sql.Row }, q string, want int) {
	t.Helper()
	var got int
	if err := db.QueryRow(q).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("%s: got %d, want %d", q, got, want)
	}
}

const a1Ledger = `SELECT * FROM schema_migrations ORDER BY name`
const a1Schema = `SELECT type,name,tbl_name,sql FROM sqlite_schema ORDER BY type,name`

func a1Sentinels(t *testing.T, db *sql.DB) map[string]string {
	t.Helper()
	for _, q := range []string{
		`INSERT INTO deliveries(dly_id,msg_id,thr_id,recipient,priority,state,attempts_left,expires_at,created_at,updated_at,body_sha256,route_id,attempt,last_status,last_error) VALUES('dly_a1','msg_a1','thr_a1','peer_a1','wake','queued',8,'2026-12-01T00:00:00.000Z','2026-11-08T00:00:00.000Z','2026-11-08T00:00:00.000Z','a1-hash','route_a1',3,503,'a1 historical error')`,
		`INSERT INTO event_outbox(sub_id,event_id,seq,body,attempts,created_at,terminal,done_at) VALUES('sub_a1','e1.1',1,' { "frozen": 1.50 } ',7,'2026-11-08T00:00:00.000Z','exhausted','2026-11-08T00:00:01.000Z')`,
		`INSERT INTO route_runs(route_id,at) VALUES('route_a1','2026-11-08T00:00:00.000Z')`,
		`INSERT INTO event_retention(owner,purged_through_seq,updated_at) VALUES('peer_a1',17,'2026-11-08T00:00:00.000Z')`,
		`INSERT INTO hub_preferences VALUES('blobs.expiry','"6h"','2026-11-08T00:00:00.000Z','operator'); UPDATE hub_preferences_state SET revision=7`,
		`INSERT INTO hub_preference_activations VALUES(7,'retention.history','2026-11-08T00:00:00.000Z',NULL,NULL,17,'a1-digest')`,
	} {
		a1Exec(t, db, q)
	}
	a1Count(t, db, `SELECT count(*) FROM meta WHERE key IN ('hub_identity_seed','hub_key') AND length(value)=64 AND value NOT GLOB '*[^0-9a-f]*'`, 2)
	a1Count(t, db, `SELECT count(*) FROM meta WHERE key='epoch' AND value='1'`, 1)
	out := map[string]string{}
	for _, q := range []string{
		`SELECT * FROM deliveries ORDER BY dly_id`, `SELECT * FROM event_outbox ORDER BY sub_id,event_id`,
		`SELECT * FROM route_runs ORDER BY route_id,at`, `SELECT * FROM event_retention ORDER BY owner`,
		`SELECT * FROM meta ORDER BY key`, `SELECT * FROM hub_preferences ORDER BY key`,
		`SELECT * FROM hub_preferences_state ORDER BY id`, `SELECT * FROM hub_preference_activations ORDER BY revision,key`,
	} {
		out[q] = a1Snapshot(t, db, q)
	}
	return out
}

func a1Unchanged(t *testing.T, db *sql.DB, before map[string]string) {
	t.Helper()
	for q, want := range before {
		if a1Snapshot(t, db, q) != want {
			t.Errorf("migration changed sentinel rows: %s", q)
		}
	}
}

func a1EmptySchema(t *testing.T, db *sql.DB) {
	t.Helper()
	a1Count(t, db, `SELECT count(*) FROM pragma_table_list WHERE name='route_attempts' AND type='table' AND strict=1`, 1)
	a1Count(t, db, `SELECT count(*) FROM route_attempts`, 0)
	a1Count(t, db, `SELECT count(*) FROM pragma_foreign_key_list('route_attempts')`, 0)
	a1Count(t, db, `SELECT count(*) FROM sqlite_schema WHERE (type='table' AND name LIKE 'route_usage%') OR (type='trigger' AND tbl_name='route_attempts')`, 0)
	for _, tc := range []struct{ query, want string }{
		{`SELECT name,type,"notnull",pk FROM pragma_table_info('route_attempts') ORDER BY cid`, `[["id","INTEGER",0,1],["route_id","TEXT",1,0],["delivery_id","TEXT",1,0],["at","TEXT",1,0],["outcome","TEXT",1,0],["http_status","INTEGER",0,0],["error_class","TEXT",0,0],["body_bytes","INTEGER",1,0],["latency_ms","INTEGER",1,0]]`},
		{`SELECT name,"unique",origin,partial FROM pragma_index_list('route_attempts') ORDER BY name`, `[["route_attempts_delivery_at",0,"c",0],["route_attempts_route_at",0,"c",0]]`},
		{`SELECT name,"desc",coll FROM pragma_index_xinfo('route_attempts_route_at') WHERE "key"=1 ORDER BY seqno`, `[["route_id",0,"BINARY"],["at",1,"BINARY"],["id",1,"BINARY"]]`},
		{`SELECT name,"desc",coll FROM pragma_index_xinfo('route_attempts_delivery_at') WHERE "key"=1 ORDER BY seqno`, `[["delivery_id",0,"BINARY"],["at",0,"BINARY"]]`},
	} {
		if got := a1Snapshot(t, db, tc.query); got != tc.want {
			t.Errorf("schema output %s: got %s, want %s", tc.query, got, tc.want)
		}
	}
}

func TestRouteAttemptsMasterPortSchema(t *testing.T) {
	t.Setenv(synchronousEnv, "full")
	t.Run("inventory", func(t *testing.T) { a1Feature(t) })
	t.Run("fresh", func(t *testing.T) {
		db, err := Open(filepath.Join(t.TempDir(), "fresh.db"))
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		a1EmptySchema(t, db.DB)
		a1Count(t, db.DB, `SELECT count(*) FROM hub_preferences_state WHERE id=1 AND revision=0`, 1)
		ledger := a1Snapshot(t, db.DB, a1Ledger)
		if applied, err := Migrate(context.Background(), db.DB); err != nil || len(applied) != 0 {
			t.Fatalf("fresh reapply: %v %v", applied, err)
		}
		if a1Snapshot(t, db.DB, a1Ledger) != ledger {
			t.Fatal("fresh reapply changed ledger bytes")
		}
	})
	t.Run("constraints", func(t *testing.T) {
		db, err := Open(filepath.Join(t.TempDir(), "constraints.db"))
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		a1EmptySchema(t, db.DB) // A missing table must never masquerade as constraint rejection.
		base := []any{"route_a1", "dly_a1", "2026-11-08T00:00:00.000Z", "fail", 503, "http_5xx", 0, 0}
		for _, bad := range []struct {
			name string
			col  int
			v    any
		}{
			{"empty_route", 0, ""}, {"long_route", 0, strings.Repeat("x", 81)}, {"route_bytes", 0, strings.Repeat("é", 41)}, {"null_route", 0, nil},
			{"empty_delivery", 1, ""}, {"long_delivery", 1, strings.Repeat("x", 81)}, {"delivery_bytes", 1, strings.Repeat("é", 41)}, {"null_delivery", 1, nil},
			{"bad_time", 2, "tomorrow"}, {"time_offset", 2, "2026-11-08T00:00:00.000+00:00"}, {"time_precision", 2, "2026-11-08T00:00:00Z"},
			{"time_microseconds", 2, "2026-11-08T00:00:00.000001Z"}, {"bad_calendar", 2, "2026-02-30T00:00:00.000Z"}, {"null_time", 2, nil},
			{"bad_outcome", 3, "secretcanary"}, {"null_outcome", 3, nil}, {"ok_with_class", 3, "ok"},
			{"low_status", 4, 99}, {"high_status", 4, 600}, {"real_status", 4, 200.5}, {"text_status", 4, "secretcanary"},
			{"arbitrary_class", 5, "secretcanary"}, {"raw_error", 5, "https://user:key@fixture.invalid/path?token=canary"}, {"fail_without_class", 5, nil},
			{"negative_body", 6, -1}, {"large_body", 6, 1048577}, {"real_body", 6, 0.5}, {"blob_body", 6, []byte("canary")}, {"null_body", 6, nil},
			{"negative_latency", 7, -1}, {"large_latency", 7, 600001}, {"real_latency", 7, 0.5}, {"text_latency", 7, "secretcanary"}, {"null_latency", 7, nil},
		} {
			t.Run(bad.name, func(t *testing.T) {
				tx, err := db.BeginTx(context.Background(), nil)
				if err != nil {
					t.Fatal(err)
				}
				defer tx.Rollback()
				if _, err := tx.Exec(a1Insert, base...); err != nil {
					t.Fatal("valid transaction control rejected", err)
				}
				a := append([]any(nil), base...)
				a[bad.col] = bad.v
				if _, err := tx.Exec(a1Insert, a...); err == nil {
					t.Error("invalid SQL write accepted")
				}
				a1Count(t, tx, `SELECT count(*) FROM route_attempts`, 1)
			})
		}
		// All closed classes, both non-OK outcomes, null/boundary status and IDs,
		// leap-day UTC, measured zero/max and repeated delivery/event IDs are valid.
		for i, class := range strings.Fields("http_3xx http_4xx http_408 http_409 http_410 http_413 http_429 http_5xx http_status routine_auth routine_rate_limited routine_not_started redirect_refused private_address scheme_refused userinfo_refused invalid_url canceled timeout dns_error tls_error connection_refused connection_reset connection_closed network_error request_failed") {
			a := append([]any(nil), base...)
			a[3], a[4], a[5] = []string{"fail", "refused"}[i%2], nil, class
			if _, err := db.Exec(a1Insert, a...); err != nil {
				t.Fatalf("allowlisted class %s rejected: %v", class, err)
			}
		}
		for _, id := range []string{"dly_a1", "dly_a1", "e1.42", "e1.42", strings.Repeat("é", 40), strings.Repeat("x", 80), "x"} {
			for _, status := range []any{nil, 100, 599} {
				if _, err := db.Exec(a1Insert, id, id, "2024-02-29T23:59:59.999Z", "ok", status, nil, 1048576, 600000); err != nil {
					t.Fatal("valid boundary/repeated ID rejected", err)
				}
			}
		}
		for _, outcome := range []string{"fail", "refused"} {
			if _, err := db.Exec(a1Insert, "route_a1", "e1.1", base[2], outcome, nil, nil, 0, 0); err == nil {
				t.Errorf("%s accepted without class", outcome)
			}
		}
		a1Count(t, db.DB, `SELECT count(*) FROM route_attempts`, 47)
		a1Count(t, db.DB, `SELECT count(DISTINCT id) FROM route_attempts`, 47)
	})
}

func TestRouteAttemptsMasterPortUpgradeRollback(t *testing.T) {
	t.Run("upgrade_34_reapply", func(t *testing.T) {
		db := a1Old(t)
		before := a1Sentinels(t, db)
		ledger := a1Snapshot(t, db, a1Ledger)
		applied, err := Migrate(context.Background(), db)
		if err != nil || !reflect.DeepEqual(applied, []string{a1FeatureName}) {
			t.Errorf("34 upgrade applied=%v err=%v, want only %s", applied, err, a1FeatureName)
		}
		a1Unchanged(t, db, before)
		if a1Snapshot(t, db, `SELECT * FROM schema_migrations WHERE name<'0035' ORDER BY name`) != ledger {
			t.Error("upgrade rewrote historical ledger names/checksums/timestamps")
		}
		a1EmptySchema(t, db)
		a1Feature(t)
		a1Count(t, db, `SELECT count(*) FROM schema_migrations WHERE name='0035_route_attempts.sql' AND sha256='`+a1FeatureSum+`' AND length(applied_at)=24`, 1)
		allLedger := a1Snapshot(t, db, a1Ledger)
		if again, err := Migrate(context.Background(), db); err != nil || len(again) != 0 {
			t.Fatalf("upgraded reapply: %v %v", again, err)
		}
		a1Unchanged(t, db, before)
		a1EmptySchema(t, db)
		if a1Snapshot(t, db, a1Ledger) != allLedger {
			t.Error("reapply changed ledger bytes")
		}
		fresh := a1Raw(t)
		if _, err := Migrate(context.Background(), fresh); err != nil {
			t.Fatal(err)
		}
		a1EmptySchema(t, fresh)
		const featureSchema = `SELECT type,name,sql FROM sqlite_schema WHERE tbl_name='route_attempts' ORDER BY type,name`
		if a1Snapshot(t, db, featureSchema) != a1Snapshot(t, fresh, featureSchema) {
			t.Error("fresh and upgraded schemas differ")
		}
	})
	for _, stage := range []string{"ddl", "index", "ledger"} {
		t.Run("rollback_"+stage, func(t *testing.T) {
			db := a1Old(t)
			before := a1Sentinels(t, db)
			ledger, schema := a1Snapshot(t, db, a1Ledger), a1Snapshot(t, db, a1Schema)
			feature := a1Feature(t)
			broken, message := feature, ""
			switch stage {
			case "ddl":
				broken.sql += `; CREATE TABLE route_attempts(id INTEGER PRIMARY KEY) STRICT;`
				message = "already exists"
			case "index":
				broken.sql += `; CREATE INDEX a1_bad_index ON route_attempts(no_such_column);`
				message = "no such column"
			case "ledger":
				a1Exec(t, db, `CREATE TRIGGER a1_fail_ledger BEFORE INSERT ON schema_migrations WHEN NEW.name='0035_route_attempts.sql' BEGIN SELECT RAISE(ABORT,'a1 ledger fault'); END`)
				message = "a1 ledger fault"
			}
			// Only the injected SQL differs; the original migration is the retry control.
			if did, err := applyOne(context.Background(), db, broken); did || err == nil || !strings.Contains(err.Error(), message) {
				t.Fatalf("%s fault: applied=%v err=%v, want %q", stage, did, err, message)
			}
			if stage == "ledger" {
				a1Exec(t, db, `DROP TRIGGER a1_fail_ledger`)
			}
			a1Count(t, db, `SELECT count(*) FROM sqlite_schema WHERE tbl_name='route_attempts' OR name='a1_bad_index'`, 0)
			a1Unchanged(t, db, before)
			if a1Snapshot(t, db, a1Ledger) != ledger || a1Snapshot(t, db, a1Schema) != schema {
				t.Error("failed migration changed historical ledger/schema")
			}
			if did, err := applyOne(context.Background(), db, feature); err != nil || !did {
				t.Fatalf("clean retry: applied=%v err=%v", did, err)
			}
			a1EmptySchema(t, db)
			a1Unchanged(t, db, before)
			after := a1Snapshot(t, db, a1Ledger)
			if did, err := applyOne(context.Background(), db, feature); err != nil || did {
				t.Fatalf("clean reapply: applied=%v err=%v", did, err)
			}
			if a1Snapshot(t, db, a1Ledger) != after {
				t.Error("clean reapply rewrote ledger")
			}
		})
	}
	t.Run("historical_checksum_refusal", func(t *testing.T) {
		db := a1Old(t)
		before := a1Sentinels(t, db)
		a1Exec(t, db, `UPDATE schema_migrations SET sha256='a1-corrupt' WHERE name='0034_thread_history_loop_parent.sql'`)
		ledger, schema := a1Snapshot(t, db, a1Ledger), a1Snapshot(t, db, a1Schema)
		if applied, err := Migrate(context.Background(), db); len(applied) != 0 || err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
			t.Fatalf("corrupt historical ledger: applied=%v err=%v", applied, err)
		}
		a1Unchanged(t, db, before)
		if a1Snapshot(t, db, a1Ledger) != ledger || a1Snapshot(t, db, a1Schema) != schema {
			t.Error("checksum refusal modified schema/ledger")
		}
	})
}

const a1bSince, a1bUntil = "2026-11-02T00:00:00.000Z", "2026-11-09T00:00:00.000Z"

func a1bDB(t *testing.T) *DB {
	t.Helper()
	db := a1Raw(t)
	if _, err := Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	return &DB{db}
}

func a1bSeed(t *testing.T, db *DB, route, at string, n, offset int) {
	t.Helper()
	if _, err := db.Exec(`WITH RECURSIVE n(i) AS (VALUES(1) UNION ALL SELECT i+1 FROM n WHERE i<?)
INSERT INTO route_attempts(id,route_id,delivery_id,at,outcome,body_bytes,latency_ms)
SELECT i+?,?,'e1.1',?,'ok',i,i FROM n`, n, offset, route, at); err != nil {
		t.Fatal(err)
	}
}

func a1bWriteError(t *testing.T, err error, stage, cause string) {
	t.Helper()
	var typed *RouteAttemptWriteError
	if !errors.As(err, &typed) || typed == nil || typed.Stage != stage || typed.Err == nil {
		t.Fatalf("expected typed %s failure with cause", stage)
	}
	if err.Error() != "route attempt write failed: "+stage || !errors.Is(err, typed.Err) || !strings.Contains(typed.Err.Error(), cause) {
		t.Fatal("write error lost its cause or exposed private failure text")
	}
}

func TestRouteAttemptsPruneNewest1000AndSevenDays(t *testing.T) {
	db := a1bDB(t)
	now := time.Date(2026, 11, 9, 11, 0, 0, 0, time.FixedZone("fixture", 11*60*60))
	a1bSeed(t, db, "route_a", a1bUntil, 1005, 0)
	a1bSeed(t, db, "route_idle", "2026-11-01T23:59:59.999Z", 1005, 6000)
	a1Exec(t, db.DB, `INSERT INTO route_attempts(id,route_id,delivery_id,at,outcome,body_bytes,latency_ms) VALUES(0,'route_a','e1.1','2026-11-09T00:00:00.001Z','ok',0,0),(5000,'route_a','e1.1','2026-11-01T23:59:59.999Z','ok',0,0)`)
	idle := a1Snapshot(t, db.DB, `SELECT * FROM route_attempts WHERE route_id='route_idle' ORDER BY id`)
	a := RouteAttemptInsert{RouteID: "route_a", DeliveryID: "e1.1", At: a1bUntil, Outcome: "ok"}
	if err := InsertRouteAttempt(context.Background(), db, a, now); err != nil {
		t.Fatal(err)
	}
	a1Count(t, db.DB, `SELECT count(*) FROM route_attempts WHERE route_id='route_a'`, 1000)
	// Independent exact membership: future ID 0, tied IDs 8..1005, new ID 7006.
	a1Count(t, db.DB, `SELECT count(*) FROM route_attempts WHERE route_id='route_a' AND (id=0 OR id BETWEEN 8 AND 1005 OR id=7006)`, 1000)
	a1bSeed(t, db, "route_cutoff", "2026-11-01T23:59:59.999Z", 1, 8000)
	a1bSeed(t, db, "route_cutoff", a1bSince, 1, 8001)
	a.RouteID = "route_cutoff"
	a.At = a1bSince // Retention follows trusted write time, not this older observation.
	if err := InsertRouteAttempt(context.Background(), db, a, now); err != nil {
		t.Fatal(err)
	}
	if got := a1Snapshot(t, db.DB, `SELECT id,at FROM route_attempts WHERE route_id='route_cutoff' ORDER BY id`); got != `[[8002,"2026-11-02T00:00:00.000Z"],[8003,"2026-11-02T00:00:00.000Z"]]` {
		t.Fatalf("seven-day boundary membership: %s", got)
	}
	if a1Snapshot(t, db.DB, `SELECT * FROM route_attempts WHERE route_id='route_idle' ORDER BY id`) != idle {
		t.Fatal("write pruned another route's idle/over-cap history")
	}
}

func TestRouteAttemptsReadAgeBoundsAndHardCap(t *testing.T) {
	db := a1bDB(t)
	a1bSeed(t, db, "route_read", a1bUntil, 1005, 0)
	a1bSeed(t, db, "route_read", "2026-11-09T00:00:00.001Z", 1001, 2000)
	a1bSeed(t, db, "route_read", "2026-11-01T23:59:59.999Z", 1, 4000)
	a1bSeed(t, db, "route_read", a1bSince, 1, 5000)
	a1bSeed(t, db, "route_other", a1bUntil, 1, 6000)
	before := a1Snapshot(t, db.DB, `SELECT * FROM route_attempts ORDER BY id`)
	if err := db.ReadSnapshot(context.Background(), func(q *sql.Conn) error {
		for _, limit := range []int{0, -1, 1000, 1001, 1000000, 2} {
			rows, err := RouteAttempts(context.Background(), q, "route_read", a1bSince, a1bUntil, limit)
			want := 1000
			if limit == 2 {
				want = 2
			}
			if err != nil || len(rows) != want {
				t.Fatalf("limit %d: count=%d err=%v", limit, len(rows), err)
			}
			for i, row := range rows {
				expected := RouteAttempt{ID: int64(1005 - i), RouteAttemptInsert: RouteAttemptInsert{RouteID: "route_read", DeliveryID: "e1.1", At: a1bUntil, Outcome: "ok", BodyBytes: 1005 - i, LatencyMS: 1005 - i}}
				if !reflect.DeepEqual(row, expected) {
					t.Fatalf("ordered/nullable row %d: got %+v want %+v", i, row, expected)
				}
			}
		}
		cutoff, err := RouteAttempts(context.Background(), q, "route_read", a1bSince, a1bSince, 1000)
		if err != nil || len(cutoff) != 1 || cutoff[0].ID != 5001 || cutoff[0].At != a1bSince {
			t.Fatalf("inclusive lower/equal upper bound: %+v %v", cutoff, err)
		}
		for _, tc := range [][3]string{{"missing", a1bSince, a1bUntil}, {"route_read", "2026-11-10T00:00:00.000Z", "2026-11-17T00:00:00.000Z"}, {"route_read", a1bUntil, a1bSince}} {
			rows, err := RouteAttempts(context.Background(), q, tc[0], tc[1], tc[2], 1000)
			if err != nil || !reflect.DeepEqual(rows, []RouteAttempt{}) {
				t.Fatalf("empty result must be []: %+v %v", rows, err)
			}
		}
		if a1Snapshot(t, db.DB, `SELECT * FROM route_attempts ORDER BY id`) != before {
			t.Fatal("reads physically pruned history")
		}
		// Commit through another connection after q's read snapshot is established.
		if _, err := db.Exec(a1Insert, "route_read", "new", a1bUntil, "fail", 503, "http_5xx", 17, 42); err != nil {
			return err
		}
		rows, err := RouteAttempts(context.Background(), q, "route_read", a1bSince, a1bUntil, 1)
		if err != nil || len(rows) != 1 || rows[0].ID != 1005 {
			t.Fatal("reader abandoned the caller's existing snapshot")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.ReadSnapshot(context.Background(), func(q *sql.Conn) error {
		rows, err := RouteAttempts(context.Background(), q, "route_read", a1bSince, a1bUntil, 1)
		status, class := 503, "http_5xx"
		expected := RouteAttempt{ID: 6002, RouteAttemptInsert: RouteAttemptInsert{RouteID: "route_read", DeliveryID: "new", At: a1bUntil, Outcome: "fail", HTTPStatus: &status, ErrorClass: &class, BodyBytes: 17, LatencyMS: 42}}
		if err != nil || len(rows) != 1 || !reflect.DeepEqual(rows[0], expected) {
			t.Fatal("new snapshot lost committed row/non-null fields")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	a1Count(t, db.DB, `SELECT count(*) FROM route_attempts`, 2010)
}

func TestRouteAttemptWriterNormalizationAndPrivacy(t *testing.T) {
	db := a1bDB(t)
	now := time.Date(2026, 11, 9, 0, 0, 0, 0, time.UTC)
	base := RouteAttemptInsert{RouteID: "route_normalize", DeliveryID: "e1.1", At: a1bUntil, Outcome: "ok", BodyBytes: -1, LatencyMS: 600001}
	if err := InsertRouteAttempt(context.Background(), db, base, now); err != nil {
		t.Fatal(err)
	}
	if a1Snapshot(t, db.DB, `SELECT body_bytes,latency_ms,http_status,error_class FROM route_attempts`) != `[[0,600000,null,null]]` {
		t.Fatal("zero/maximum clamps or nullable success fields changed")
	}
	a := base
	a.BodyBytes, a.LatencyMS = 1048577, -1
	if err := InsertRouteAttempt(context.Background(), db, a, now); err != nil {
		t.Fatal(err)
	}
	if a1Snapshot(t, db.DB, `SELECT body_bytes,latency_ms FROM route_attempts ORDER BY id DESC LIMIT 1`) != `[[1048576,0]]` {
		t.Fatal("opposite measurement clamps changed")
	}
	low, high := 100, 599
	for i, class := range strings.Fields("http_3xx http_4xx http_408 http_409 http_410 http_413 http_429 http_5xx http_status routine_auth routine_rate_limited routine_not_started redirect_refused private_address scheme_refused userinfo_refused invalid_url canceled timeout dns_error tls_error connection_refused connection_reset connection_closed network_error request_failed") {
		a = base
		a.Outcome, a.ErrorClass, a.HTTPStatus = []string{"fail", "refused"}[i%2], &class, []*int{nil, &low, &high}[i%3]
		if err := InsertRouteAttempt(context.Background(), db, a, now); err != nil {
			t.Fatal("valid closed-class/outcome/status control rejected", err)
		}
		want, _ := json.Marshal([][]any{{a.Outcome, a.HTTPStatus, class, 0, 600000}})
		if a1Snapshot(t, db.DB, `SELECT outcome,http_status,error_class,body_bytes,latency_ms FROM route_attempts ORDER BY id DESC LIMIT 1`) != string(want) {
			t.Fatal("writer changed normalized class/outcome/status values")
		}
	}
	status := 204
	for _, id := range []string{"x", strings.Repeat("é", 40)} {
		a = base
		a.RouteID, a.DeliveryID, a.HTTPStatus, a.BodyBytes, a.LatencyMS = id, id, &status, 17, 42
		if err := InsertRouteAttempt(context.Background(), db, a, now); err != nil {
			t.Fatal("valid byte boundary/interior measurement rejected", err)
		}
		want, _ := json.Marshal([][]any{{id, id, a1bUntil, "ok", 204, nil, 17, 42}})
		if a1Snapshot(t, db.DB, `SELECT route_id,delivery_id,at,outcome,http_status,error_class,body_bytes,latency_ms FROM route_attempts ORDER BY id DESC LIMIT 1`) != string(want) {
			t.Fatal("writer changed valid bounded fields")
		}
	}
	a1Count(t, db.DB, `SELECT count(*) FROM route_attempts`, 30)
	private, empty, allowed, badLow, badHigh := "https://user:key@fixture.invalid/?token=canary", "", "http_5xx", 99, 600
	before := a1Snapshot(t, db.DB, `SELECT * FROM route_attempts ORDER BY id`)
	for _, mutate := range []func(*RouteAttemptInsert){
		func(a *RouteAttemptInsert) { a.RouteID = "" },
		func(a *RouteAttemptInsert) { a.RouteID = strings.Repeat("x", 81) },
		func(a *RouteAttemptInsert) { a.RouteID = strings.Repeat("é", 41) },
		func(a *RouteAttemptInsert) { a.DeliveryID = "" },
		func(a *RouteAttemptInsert) { a.DeliveryID = strings.Repeat("é", 41) },
		func(a *RouteAttemptInsert) { a.At = private },
		func(a *RouteAttemptInsert) { a.At = "2026-02-30T00:00:00.000Z" },
		func(a *RouteAttemptInsert) { a.At = "2026-11-09T00:00:00Z" },
		func(a *RouteAttemptInsert) { a.At = "2026-11-09T00:00:00.000+00:00" },
		func(a *RouteAttemptInsert) { a.Outcome = private },
		func(a *RouteAttemptInsert) { a.Outcome = "fail" },
		func(a *RouteAttemptInsert) { a.Outcome = "refused" },
		func(a *RouteAttemptInsert) { a.ErrorClass = &allowed },
		func(a *RouteAttemptInsert) { a.ErrorClass = &private },
		func(a *RouteAttemptInsert) { a.Outcome, a.ErrorClass = "fail", &private },
		func(a *RouteAttemptInsert) { a.Outcome, a.ErrorClass = "refused", &empty },
		func(a *RouteAttemptInsert) { a.HTTPStatus = &badLow },
		func(a *RouteAttemptInsert) { a.HTTPStatus = &badHigh },
	} {
		a = base
		mutate(&a)
		a1bWriteError(t, InsertRouteAttempt(context.Background(), db, a, now), "normalize", "invalid route attempt")
		if a1Snapshot(t, db.DB, `SELECT * FROM route_attempts ORDER BY id`) != before {
			t.Fatal("invalid writer input changed rows or persisted private text")
		}
	}
	a1bSeed(t, db, "route_normalize", "2026-11-01T23:59:59.999Z", 1, 10000)
	for _, fault := range [][2]string{{"INSERT", "insert"}, {"DELETE", "prune"}} {
		before := a1Snapshot(t, db.DB, `SELECT * FROM route_attempts ORDER BY id`)
		a1Exec(t, db.DB, `CREATE TRIGGER a1b_fault BEFORE `+fault[0]+` ON route_attempts BEGIN SELECT RAISE(ABORT,'private-driver-canary'); END`)
		a1bWriteError(t, InsertRouteAttempt(context.Background(), db, base, now), fault[1], "private-driver-canary")
		a1Exec(t, db.DB, `DROP TRIGGER a1b_fault`)
		if a1Snapshot(t, db.DB, `SELECT * FROM route_attempts ORDER BY id`) != before {
			t.Fatal("insert/prune failure violated atomic before-state")
		}
	}
	a = base
	a.DeliveryID = "retry"
	if err := InsertRouteAttempt(context.Background(), db, a, now); err != nil {
		t.Fatal("clean writer retry failed", err)
	}
	a1Count(t, db.DB, `SELECT count(*) FROM route_attempts WHERE id=10001`, 0)
	a1Count(t, db.DB, `SELECT count(*) FROM route_attempts WHERE delivery_id='retry'`, 1)
	a1Count(t, db.DB, `SELECT count(*) FROM route_attempts`, 31)
}
