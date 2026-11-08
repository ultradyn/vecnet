package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// Source-only T1 draft. These are proposed calls to the future normalized
// writer and snapshot reader, not implementations or an allocated migration:
// InsertRouteAttempt(ctx, *DB, RouteAttemptInsert, trustedWriteTime) error
// RouteAttempts(ctx, *sql.Conn, route, since, until, limit) ([]RouteAttempt, error).
// RouteAttemptInsert uses the provenance value fields, without caller Cutoff.
// No test in this file has been compiled or run.

var t1MasterMigrationSums = []string{
	"195af58e5296ff3e52bb8591ee29ebb9a111030f237ed6c35daaf3ec77d9473c",
	"753f2afebea463d9b1ea74ebf1a29d5c56e91fdbc55cfe18198d708d7f87c847",
	"29b303a6c105b2303d0da209df4a320423be9bd2294b551eedff6430201d7b7b",
	"4b207098f1354de932b90ba2f42a261d1beeebd486f14fa212b9e53e926ec80a",
	"6ef976d9ea52c2a8ebdb93acbb05c0c7c43e89b36dc918e7c1f5de26ef82fd33",
	"281dc4619aade43fef5065361dc6cb941c62f4fb6aa2e8fead50cd757da37e3d",
	"a869d3e720a5abc14ce4919fac6667d5d30c2c9ca81da566fcd4d837bd91a539",
	"a4efe7bf67c42e5a14fb38079c677b4cbf534125dac56908876bc507de7face6",
	"d66bfb3af52d56d56245daa975c7441c5636c8f31d42ac5812a56d9362c162a9",
	"0c749aabd643e0efbf8445b9c02fc64da1a4cd2d090f0437359f10f1dd024427",
	"ca94163a9a7517524c3837669e66924b60a6d7b33b99c4e5ef71b2bdf042eea1",
	"243071a9366c397ab0915349c3f71de75f847a3666b7668b5fc4b13e58e96b02",
	"333d27ff98faf84c60d9bd6a7b9f43a7c6daad1a4a6fb419f8bef398cf843877",
	"673afb4824b04e2017b3c0e6df2a030fa29a505e5fbea3319698ba478a95eb77",
	"973fdf9f6751cfe7350ee34415baf3f5306c66aa0a4cb0520461063414dcdfb5",
	"a485b95dba23bd14bb27b026cc7433260fc23122d077ce104399cc99b38facb7",
	"8a0c36cf945ef42ad9f91b90844c7830359be3917c71fc8e448adceb8cf8854b",
	"c1ccdbb738504dd411caec9f358d7d900c3f41a24bd351e4f3d19580fe163cdf",
	"4a8a9112b508d0393619b354f4bfad44f4157abc0ef37294624ac6106c3a71a8",
	"2ac7dd14e99ea10f8864c896a772b71a5d06fd6664343e6cd66b8c2044082bc2",
	"04f0dedf59460225d3aeb803b42d16b1c09938495310ee85e19ae151b914962c",
	"f89d302c95329c53fb4efb9c83ab6b2c62b6004b3e96785229d66fa147c4cd83",
	"f6127a6108f2ff93cb93a570bac75f37f8b231ef0d72750ef44fe365d5243fef",
	"f89e8b58ca55d57352dd93221fd9ccef2f664e0387aafac996e6931ed9dca156",
	"76b29c0609b8a4c339176444c80a22d505e26b6b805bfa088f9f699f5c7f4984",
	"2cac00e8fa88eb723ac6f7119a37e5e4e762a8b094443bd8de06f8f0c7489309",
	"c71f8a811b6aec0d575199d45441b248b3cf8581340c2437f23dc492b78629fe",
	"2671b52be2727f9c627a32f36bf4d60eeec9d18ed2e9642f2cca2a508bebf98a",
	"cdad4b82cebe6ce59113be393079a29de941be7af5e9888a6ff0211184606f4b",
	"9126a82acd3e248da25c1b2b3621c57d6ab54a99f9549772620e9cd29cce0fdb",
	"5a0a8841423c565173928a7f1313e72e29542db8b6394769f1348ee802e0b47b",
	"52fa50168ccf96e0d955a6b0c6d3cd89c2b43aa4fd15da1ef9c9fe59d81378f6",
	"5c04514ec9cc5b74e6e2da69a7c77b064d4ca14ec329814734a98607534f1bae",
	"2a3bf7fa01dece6ff0805403a29c2627e04e7e800f37bdd09c00b20af6fa4ff1",
}

var t1MasterMigrationNames = []string{
	"0001_init.sql",
	"0002_reach_via.sql",
	"0003_blob_uploaders.sql",
	"0004_delivery_read_at.sql",
	"0005_event_subscriptions.sql",
	"0006_oauth.sql",
	"0007_grokbot_routes.sql",
	"0008_sources.sql",
	"0009_connector_hardening.sql",
	"0010_audit_action_index.sql",
	"0011_inbox_indexes.sql",
	"0012_reactions.sql",
	"0013_delivery_dispatching.sql",
	"0014_principal_peers.sql",
	"0015_reserved.sql",
	"0016_peer_avatars.sql",
	"0017_peer_name_open.sql",
	"0018_sender_info.sql",
	"0019_event_subscription_status.sql",
	"0020_event_refresh_before.sql",
	"0021_hub_identity_notices.sql",
	"0022_hub_preferences.sql",
	"0023_event_payload_refs.sql",
	"0024_backfill_exceptions.sql",
	"0025_r2_blobs.sql",
	"0026_peer_archive.sql",
	"0027_event_retention_floors.sql",
	"0028_event_outbox_retention_seq.sql",
	"0029_callback_retention_lookups.sql",
	"0030_event_retention_rule_intervals.sql",
	"0031_callback_retention_identity.sql",
	"0032_idempotency_created_at_index.sql",
	"0033_thread_seq_history_tombstones.sql",
	"0034_thread_history_loop_parent.sql",
}

func t1Inventory(t *testing.T) ([]migration, migration) {
	t.Helper()
	all, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) < len(t1MasterMigrationSums) {
		t.Fatal("master migration inventory was removed")
	}
	for i, sum := range t1MasterMigrationSums {
		if all[i].sum != sum || all[i].name != t1MasterMigrationNames[i] {
			t.Fatalf("master migration %d changed: %s", i+1, all[i].name)
		}
	}
	var before []migration
	var feature migration
	for _, m := range all {
		if strings.HasSuffix(m.name, "_route_attempts.sql") {
			if feature.name != "" {
				t.Fatal("multiple route_attempts migrations")
			}
			feature = m
		} else {
			before = append(before, m)
		}
	}
	if feature.name == "" {
		t.Fatal("RED: no controller-allocated route_attempts migration")
	}
	return before, feature
}

func t1OldDB(t *testing.T, inventory []migration) (*sql.DB, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "attempts.db")
	db, err := sql.Open("sqlite", "file:"+uriPathEscaper.Replace(path)+"?"+dsnParams)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(`CREATE TABLE schema_migrations(name TEXT PRIMARY KEY, sha256 TEXT NOT NULL, applied_at TEXT NOT NULL) STRICT`); err != nil {
		t.Fatal(err)
	}
	for _, m := range inventory {
		if _, err := applyOne(context.Background(), db, m); err != nil {
			t.Fatal(err)
		}
	}
	return db, path
}

func t1Snapshot(t *testing.T, db *sql.DB, query string) []byte {
	t.Helper()
	rows, err := db.Query(query)
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
	return b
}

var t1SentinelQueries = []string{
	`SELECT * FROM deliveries ORDER BY dly_id`,
	`SELECT * FROM event_outbox ORDER BY sub_id,event_id`,
	`SELECT * FROM route_runs ORDER BY route_id,at`,
	`SELECT * FROM event_retention ORDER BY owner`,
	`SELECT * FROM meta ORDER BY key`,
}

func t1Sentinels(t *testing.T, db *sql.DB) map[string][]byte {
	t.Helper()
	for _, q := range []string{
		`INSERT INTO deliveries(dly_id,msg_id,thr_id,recipient,priority,state,attempts_left,expires_at,created_at,updated_at,body_sha256) VALUES('dly_sentinel','msg_sentinel','thr_sentinel','peer_sentinel','wake','queued',8,'2026-12-01T00:00:00.000Z','2026-11-08T00:00:00.000Z','2026-11-08T00:00:00.000Z','sentinel-hash')`,
		`INSERT INTO event_outbox(sub_id,event_id,seq,body,attempts,created_at,terminal,done_at) VALUES('sub_sentinel','e1.1',1,' { "frozen": 1.50 } ',7,'2026-11-08T00:00:00.000Z','exhausted','2026-11-08T00:00:01.000Z')`,
		`INSERT INTO route_runs(route_id,at) VALUES('route_sentinel','2026-11-08T00:00:00.000Z')`,
		`INSERT INTO event_retention(owner,purged_through_seq,updated_at) VALUES('peer_sentinel',17,'2026-11-08T00:00:00.000Z')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	return t1CaptureSentinels(t, db)
}

func t1CaptureSentinels(t *testing.T, db *sql.DB) map[string][]byte {
	t.Helper()
	out := map[string][]byte{}
	for _, q := range t1SentinelQueries {
		out[q] = t1Snapshot(t, db, q)
	}
	return out
}

func t1Count(t *testing.T, db *sql.DB, query string, args ...any) int {
	t.Helper()
	var n int
	if err := db.QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestRouteAttemptsUpgradeFromCurrentMaster(t *testing.T) {
	before, _ := t1Inventory(t)
	for _, baseline := range []struct {
		name string
		migs []migration
	}{{"exact_master_34", before[:34]}, {"current_prefeature", before}} {
		t.Run(baseline.name, func(t *testing.T) {
			db, _ := t1OldDB(t, baseline.migs)
			want := t1Sentinels(t, db)
			ledger := t1Snapshot(t, db, `SELECT * FROM schema_migrations ORDER BY name`)
			applied, err := Migrate(context.Background(), db)
			if err != nil || len(applied) == 0 {
				t.Fatalf("additive upgrade: %v, %v", applied, err)
			}
			if !reflect.DeepEqual(want, t1CaptureSentinels(t, db)) {
				t.Fatal("upgrade changed unrelated sentinel bytes")
			}
			last := baseline.migs[len(baseline.migs)-1].name
			rows := t1Snapshot(t, db, `SELECT * FROM schema_migrations WHERE name <= '`+last+`' ORDER BY name`)
			if !bytes.Equal(ledger, rows) {
				t.Fatal("upgrade rewrote old ledger/checksums/times")
			}
			if n := t1Count(t, db, `SELECT count(*) FROM route_attempts`); n != 0 {
				t.Fatalf("upgrade backfilled %d attempts", n)
			}
			if n := t1Count(t, db, `SELECT count(*) FROM sqlite_schema WHERE type='table' AND name LIKE 'route_usage%'`); n != 0 {
				t.Fatal("abandoned lifetime schema present")
			}
			if n := t1Count(t, db, `SELECT count(*) FROM pragma_table_list WHERE name='route_attempts' AND strict=1`); n != 1 {
				t.Fatal("route_attempts is not STRICT")
			}
			if again, err := Migrate(context.Background(), db); err != nil || len(again) != 0 {
				t.Fatalf("reapply: %v, %v", again, err)
			}
			fresh, err := Open(filepath.Join(t.TempDir(), "fresh.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer fresh.Close()
			q := `SELECT type,name,sql FROM sqlite_schema WHERE tbl_name='route_attempts' ORDER BY type,name`
			if !bytes.Equal(t1Snapshot(t, db, q), t1Snapshot(t, fresh.DB, q)) {
				t.Fatal("fresh and upgraded attempt schemas differ")
			}
		})
	}
}

func TestRouteAttemptsMigrationFailureRollsBack(t *testing.T) {
	before, feature := t1Inventory(t)
	for _, stage := range []string{"ddl", "index", "ledger"} {
		t.Run(stage, func(t *testing.T) {
			db, _ := t1OldDB(t, before)
			want := t1Sentinels(t, db)
			ledger := t1Snapshot(t, db, `SELECT * FROM schema_migrations ORDER BY name`)
			broken := feature
			switch stage {
			case "ddl":
				broken.sql += `; CREATE TABLE route_attempts(id INTEGER PRIMARY KEY) STRICT;`
			case "index":
				broken.sql += `; CREATE INDEX t1_bad_index ON route_attempts(no_such_column);`
			case "ledger":
				if _, err := db.Exec(`CREATE TRIGGER t1_fail_ledger BEFORE INSERT ON schema_migrations WHEN NEW.name='` + feature.name + `' BEGIN SELECT RAISE(ROLLBACK,'synthetic ledger failure'); END`); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := applyOne(context.Background(), db, broken); err == nil {
				t.Fatal("injected migration failure succeeded")
			}
			if n := t1Count(t, db, `SELECT count(*) FROM sqlite_schema WHERE tbl_name='route_attempts' OR name='t1_bad_index'`); n != 0 {
				t.Fatal("failed migration left table/index artifacts")
			}
			if !bytes.Equal(ledger, t1Snapshot(t, db, `SELECT * FROM schema_migrations ORDER BY name`)) || !reflect.DeepEqual(want, t1CaptureSentinels(t, db)) {
				t.Fatal("failed migration changed prior data/ledger")
			}
			if stage == "ledger" {
				if _, err := db.Exec(`DROP TRIGGER t1_fail_ledger`); err != nil {
					t.Fatal(err)
				}
			}
			if did, err := applyOne(context.Background(), db, feature); err != nil || !did {
				t.Fatalf("clean retry: %v, %v", did, err)
			}
		})
	}
}

func TestRouteAttemptsMasterInventoryReopen(t *testing.T) {
	before, _ := t1Inventory(t)
	db, path := t1OldDB(t, before[:34])
	want := t1Sentinels(t, db)
	if _, err := Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	ledger := t1Snapshot(t, db, `SELECT * FROM schema_migrations ORDER BY name`)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	// The old inventory and applyOne mirror M's loader; no old binary is run.
	old, err := sql.Open("sqlite", "file:"+uriPathEscaper.Replace(path)+"?"+dsnParams)
	if err != nil {
		t.Fatal(err)
	}
	defer old.Close()
	for _, m := range before[:34] {
		if did, err := applyOne(context.Background(), old, m); err != nil || did {
			t.Fatalf("old inventory reopen: %v, %v", did, err)
		}
	}
	if !reflect.DeepEqual(want, t1CaptureSentinels(t, old)) {
		t.Fatal("old inventory reopen changed sentinels")
	}
	if _, err := old.Exec(`INSERT INTO meta(key,value) VALUES('t1_old_write','old-table write'); UPDATE deliveries SET last_status=204 WHERE dly_id='dly_sentinel'`); err != nil {
		t.Fatal("normal old-table write failed", err)
	}
	wantAfterWrites := t1CaptureSentinels(t, old)
	if !bytes.Equal(ledger, t1Snapshot(t, old, `SELECT * FROM schema_migrations ORDER BY name`)) {
		t.Fatal("old inventory altered unknown additive ledger")
	}
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}
	newDB, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer newDB.Close()
	if n := t1Count(t, newDB.DB, `SELECT count(*) FROM meta WHERE key='t1_old_write' AND value='old-table write'`); n != 1 {
		t.Fatal("new reopen lost old write")
	}
	if n := t1Count(t, newDB.DB, `SELECT count(*) FROM route_attempts`); n != 0 {
		t.Fatal("old inventory lost or populated additive table")
	}
	if !bytes.Equal(ledger, t1Snapshot(t, newDB.DB, `SELECT * FROM schema_migrations ORDER BY name`)) {
		t.Fatal("new reopen changed ledger")
	}
	for _, query := range t1SentinelQueries[:4] {
		if !bytes.Equal(wantAfterWrites[query], t1Snapshot(t, newDB.DB, query)) {
			t.Fatal("new reopen changed old-table data/replay bytes")
		}
	}
	var seed string
	if err := newDB.QueryRow(`SELECT value FROM meta WHERE key='hub_identity_seed'`).Scan(&seed); err != nil || seed == "" || !bytes.Contains(wantAfterWrites[`SELECT * FROM meta ORDER BY key`], []byte(seed)) {
		t.Fatal("new reopen changed hub identity", err)
	}
}

const t1AttemptSQL = `INSERT INTO route_attempts(route_id,delivery_id,at,outcome,http_status,error_class,body_bytes,latency_ms) VALUES(?,?,?,?,?,?,?,?)`

func t1AttemptDB(t *testing.T) *DB {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "attempts.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestRouteAttemptsConstraintsAndPrivacy(t *testing.T) {
	db := t1AttemptDB(t)
	base := []any{"route_fixture", "dly_fixture", "2026-11-08T00:00:00.000Z", "fail", 503, "http_5xx", 0, 0}
	for _, bad := range []struct {
		name string
		col  int
		v    any
	}{
		{"empty_route", 0, ""}, {"long_route", 0, strings.Repeat("x", 81)}, {"route_byte_bound", 0, strings.Repeat("é", 41)}, {"null_route", 0, nil},
		{"empty_event", 1, ""}, {"long_event", 1, strings.Repeat("x", 81)}, {"null_event", 1, nil},
		{"bad_time", 2, "tomorrow"}, {"time_offset", 2, "2026-11-08T00:00:00.000+00:00"}, {"time_precision", 2, "2026-11-08T00:00:00Z"},
		{"bad_calendar", 2, "2026-02-30T00:00:00.000Z"}, {"null_time", 2, nil},
		{"bad_outcome", 3, "secretcanary"}, {"null_outcome", 3, nil}, {"ok_with_class", 3, "ok"},
		{"low_status", 4, 99}, {"high_status", 4, 600}, {"real_status", 4, 200.5}, {"text_status", 4, "secretcanary"},
		{"arbitrary_lowercase_class", 5, "secretcanary"}, {"raw_error", 5, "https://user:key@fixture.invalid/path?token=canary"}, {"fail_without_class", 5, nil},
		{"negative_body", 6, -1}, {"large_body", 6, 1048577}, {"real_body", 6, 0.5}, {"blob_body", 6, []byte("canary")}, {"null_body", 6, nil},
		{"negative_latency", 7, -1}, {"large_latency", 7, 600001}, {"real_latency", 7, 0.5}, {"null_latency", 7, nil},
	} {
		t.Run(bad.name, func(t *testing.T) {
			a := append([]any(nil), base...)
			a[bad.col] = bad.v
			if _, err := db.Exec(t1AttemptSQL, a...); err == nil {
				t.Fatal("invalid direct SQL write accepted")
			}
		})
	}
	classes := strings.Fields("http_3xx http_4xx http_408 http_409 http_410 http_413 http_429 http_5xx http_status routine_auth routine_rate_limited routine_not_started redirect_refused private_address scheme_refused userinfo_refused invalid_url canceled timeout dns_error tls_error connection_refused connection_reset connection_closed network_error request_failed")
	for _, class := range classes {
		a := append([]any(nil), base...)
		a[5] = class
		if _, err := db.Exec(t1AttemptSQL, a...); err != nil {
			t.Fatalf("allowlisted class %s rejected: %v", class, err)
		}
	}
	for _, outcome := range []string{"fail", "refused"} {
		if _, err := db.Exec(t1AttemptSQL, "route_fixture", "e1.1", base[2], outcome, nil, "request_failed", 0, 0); err != nil {
			t.Fatal("nullable network status rejected", err)
		}
		if _, err := db.Exec(t1AttemptSQL, "route_fixture", "e1.1", base[2], outcome, nil, nil, 0, 0); err == nil {
			t.Fatal("non-OK outcome accepted without an error class")
		}
	}
	for _, id := range []string{"dly_fixture", "dly_fixture", "e1.42", "e1.42"} {
		if _, err := db.Exec(t1AttemptSQL, "sub_fixture", id, base[2], "ok", 204, nil, 1048576, 600000); err != nil {
			t.Fatal("repeated delivery/event or boundary rejected", err)
		}
	}
	if n := t1Count(t, db.DB, `SELECT count(*) FROM pragma_foreign_key_list('route_attempts')`); n != 0 {
		t.Fatal("telemetry pins unrelated data")
	}
	got := t1Snapshot(t, db.DB, `SELECT name FROM pragma_table_info('route_attempts') ORDER BY cid`)
	want := `[["id"],["route_id"],["delivery_id"],["at"],["outcome"],["http_status"],["error_class"],["body_bytes"],["latency_ms"]]`
	if string(got) != want {
		t.Fatalf("unexpected telemetry columns: %s", got)
	}
	indexSQL := string(t1Snapshot(t, db.DB, `SELECT sql FROM sqlite_schema WHERE type='index' AND tbl_name='route_attempts' ORDER BY name`))
	compact := strings.NewReplacer(" ", "", "\\n", "", "\\t", "").Replace(strings.ToLower(indexSQL))
	for _, columns := range []string{"(route_id,atdesc,iddesc)", "(delivery_id,at)"} {
		if !strings.Contains(compact, columns) {
			t.Errorf("missing index columns %s", columns)
		}
	}
	// Writer input validation/clamping is separate from direct SQL rejection.
	now := time.Date(2026, 11, 8, 0, 0, 0, 0, time.UTC)
	a := RouteAttemptInsert{RouteID: "route_writer", DeliveryID: "e1.9", At: "2026-11-08T00:00:00.000Z", Outcome: "ok", BodyBytes: -1, LatencyMS: 600001}
	if err := InsertRouteAttempt(context.Background(), db, a, now); err != nil {
		t.Fatal(err)
	}
	if n := t1Count(t, db.DB, `SELECT count(*) FROM route_attempts WHERE route_id='route_writer' AND body_bytes=0 AND latency_ms=600000`); n != 1 {
		t.Fatal("writer did not clamp measurements")
	}
	a.BodyBytes, a.LatencyMS = 1048577, -1
	if err := InsertRouteAttempt(context.Background(), db, a, now); err != nil {
		t.Fatal(err)
	}
	if n := t1Count(t, db.DB, `SELECT count(*) FROM route_attempts WHERE route_id='route_writer' AND body_bytes=1048576 AND latency_ms=0`); n != 1 {
		t.Fatal("writer did not clamp opposite measurement bounds")
	}
	for _, mutate := range []func(*RouteAttemptInsert){
		func(a *RouteAttemptInsert) { a.At = "not-a-time" },
		func(a *RouteAttemptInsert) { a.RouteID = "" },
		func(a *RouteAttemptInsert) { a.DeliveryID = strings.Repeat("é", 41) },
	} {
		bad := a
		mutate(&bad)
		if err := InsertRouteAttempt(context.Background(), db, bad, now); err == nil {
			t.Fatal("writer accepted invalid identity/timestamp")
		}
	}
}

func t1SeedAttempts(t *testing.T, db *DB, route, at string, n int) {
	t.Helper()
	if err := db.Tx(context.Background(), func(tx *sql.Tx) error {
		for i := range n {
			if _, err := tx.Exec(t1AttemptSQL, route, fmt.Sprintf("e1.%d", i), at, "ok", 200, nil, i, i); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestRouteAttemptsPruneNewest1000AndSevenDays(t *testing.T) {
	db := t1AttemptDB(t)
	now := time.Date(2026, 11, 8, 0, 0, 0, 0, time.UTC)
	const at = "2026-11-08T00:00:00.000Z"
	for _, route := range []string{"route_a", "route_b"} {
		t1SeedAttempts(t, db, route, at, 1005)
	}
	other := t1Snapshot(t, db.DB, `SELECT * FROM route_attempts WHERE route_id='route_b' ORDER BY id`)
	a := RouteAttemptInsert{RouteID: "route_a", DeliveryID: "e1.9999", At: at, Outcome: "ok"}
	if err := InsertRouteAttempt(context.Background(), db, a, now); err != nil {
		t.Fatal(err)
	}
	if n := t1Count(t, db.DB, `SELECT count(*) FROM route_attempts WHERE route_id='route_a'`); n != 1000 {
		t.Fatalf("route cap = %d", n)
	}
	if n := t1Count(t, db.DB, `SELECT count(*) FROM route_attempts WHERE route_id='route_a' AND delivery_id IN ('e1.0','e1.5')`); n != 0 {
		t.Fatal("tie prune kept oldest inserted rows")
	}
	if n := t1Count(t, db.DB, `SELECT count(*) FROM route_attempts WHERE route_id='route_a' AND delivery_id IN ('e1.6','e1.9999')`); n != 2 {
		t.Fatal("tie prune discarded newest inserted rows")
	}
	if !bytes.Equal(other, t1Snapshot(t, db.DB, `SELECT * FROM route_attempts WHERE route_id='route_b' ORDER BY id`)) {
		t.Fatal("prune touched another route")
	}
	a.RouteID = "route_b"
	if err := InsertRouteAttempt(context.Background(), db, a, now); err != nil {
		t.Fatal(err)
	}
	if n := t1Count(t, db.DB, `SELECT count(*) FROM route_attempts WHERE route_id='route_b'`); n != 1000 {
		t.Fatal("second route not independently capped")
	}
	t1SeedAttempts(t, db, "route_cutoff", "2026-10-31T23:59:59.999Z", 1)
	t1SeedAttempts(t, db, "route_cutoff", "2026-11-01T00:00:00.000Z", 1)
	a.RouteID = "route_cutoff"
	if err := InsertRouteAttempt(context.Background(), db, a, now); err != nil {
		t.Fatal(err)
	}
	if n := t1Count(t, db.DB, `SELECT count(*) FROM route_attempts WHERE route_id='route_cutoff' AND at='2026-11-01T00:00:00.000Z'`); n != 1 {
		t.Fatal("inclusive cutoff was pruned")
	}
	if n := t1Count(t, db.DB, `SELECT count(*) FROM route_attempts WHERE route_id='route_cutoff'`); n != 2 {
		t.Fatal("row one millisecond outside cutoff survived")
	}
	for _, operation := range []string{"INSERT", "DELETE"} {
		t.Run(operation+"_rollback", func(t *testing.T) {
			want := t1Snapshot(t, db.DB, `SELECT * FROM route_attempts ORDER BY id`)
			if _, err := db.Exec(`CREATE TRIGGER t1_fail_attempt BEFORE ` + operation + ` ON route_attempts BEGIN SELECT RAISE(ROLLBACK,'telemetry only'); END`); err != nil {
				t.Fatal(err)
			}
			a.RouteID = "route_a"
			err := InsertRouteAttempt(context.Background(), db, a, now)
			if _, dropErr := db.Exec(`DROP TRIGGER t1_fail_attempt`); dropErr != nil {
				t.Fatal(dropErr)
			}
			if err == nil || !bytes.Equal(want, t1Snapshot(t, db.DB, `SELECT * FROM route_attempts ORDER BY id`)) {
				t.Fatal("failed insert/prune did not roll back all telemetry")
			}
		})
	}
}

func TestRouteAttemptsReadAgeBoundsAndHardCap(t *testing.T) {
	db := t1AttemptDB(t)
	const since, until = "2026-11-01T00:00:00.000Z", "2026-11-08T00:00:00.000Z"
	t1SeedAttempts(t, db, "route_read", until, 1005)
	t1SeedAttempts(t, db, "route_read", "2026-11-08T00:00:00.001Z", 1001)
	t1SeedAttempts(t, db, "route_read", "2026-10-31T23:59:59.999Z", 1)
	t1SeedAttempts(t, db, "route_cutoff", since, 1)
	physical := t1Count(t, db.DB, `SELECT count(*) FROM route_attempts`)
	for _, limit := range []int{0, -1, 1000, 1001, 1000000, 2} {
		if err := db.ReadSnapshot(context.Background(), func(q *sql.Conn) error {
			rows, err := RouteAttempts(context.Background(), q, "route_read", since, until, limit)
			if err != nil {
				return err
			}
			want := 1000
			if limit == 2 {
				want = 2
			}
			if len(rows) != want {
				t.Fatalf("limit %d returned %d, want %d", limit, len(rows), want)
			}
			for i, row := range rows {
				if row.At != until || row.DeliveryID != fmt.Sprintf("e1.%d", 1004-i) {
					t.Fatalf("future filtering/tie ordering at row %d: %+v", i, row)
				}
			}
			cutoff, err := RouteAttempts(context.Background(), q, "route_cutoff", since, until, 1000)
			if err != nil || len(cutoff) != 1 || cutoff[0].At != since {
				t.Fatalf("inclusive read cutoff: %+v, %v", cutoff, err)
			}
			// Idle passage needs no insert or physical deletion.
			idle, err := RouteAttempts(context.Background(), q, "route_read", "2026-11-09T00:00:00.000Z", "2026-11-16T00:00:00.000Z", 1000)
			if err != nil || len(idle) != 0 {
				t.Fatalf("idle age filter: %d, %v", len(idle), err)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	if n := t1Count(t, db.DB, `SELECT count(*) FROM route_attempts`); n != physical {
		t.Fatal("read performed a physical sweep")
	}
}
