package store_test

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/XertroV/vecnet/internal/store"
)

// v1Tables is every table the plan's Task 3 schema names.
var v1Tables = []string{
	"meta", "principals", "peers", "credentials", "enrol_tokens", "scopes",
	"teams", "team_members", "groups", "group_members", "threads",
	"thread_participants", "messages", "idempotency", "deliveries", "events",
	"acks", "routes", "route_keys", "notify_routes", "blobs", "blob_refs",
	"blob_quota", "jobs", "approvals", "audit", "counters",
}

func openTemp(t *testing.T) *store.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "hub.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestOpenMigratesAndIsIdempotent(t *testing.T) {
	ctx := context.Background()
	db := openTemp(t)

	// Open already migrated; a second run applies nothing.
	applied, err := store.Migrate(ctx, db.DB)
	if err != nil {
		t.Fatalf("second Migrate: %v", err)
	}
	if len(applied) != 0 {
		t.Fatalf("second Migrate applied %v, want none", applied)
	}

	var n int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM schema_migrations WHERE name = '0001_init.sql'`).Scan(&n); err != nil {
		t.Fatalf("schema_migrations: %v", err)
	}
	if n != 1 {
		t.Fatalf("0001_init.sql recorded %d times, want 1", n)
	}
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM schema_migrations WHERE name = '0012_reactions.sql'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("0012_reactions.sql recorded %d times, want 1: %v", n, err)
	}
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM schema_migrations WHERE name = '0014_principal_peers.sql'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("0014_principal_peers.sql recorded %d times, want 1: %v", n, err)
	}
	for _, table := range []string{"principal_limits", "principal_default_scopes", "peer_create_keys"} {
		if err := db.QueryRowContext(ctx, `SELECT count(*) FROM pragma_table_list WHERE name = ? AND strict = 1`, table).Scan(&n); err != nil || n != 1 {
			t.Fatalf("missing STRICT table %s: %d, %v", table, n, err)
		}
	}

	for _, table := range []string{"reactions", "reaction_ops", "reaction_wakes"} {
		if err := db.QueryRowContext(ctx, `SELECT count(*) FROM pragma_table_list WHERE name = ? AND strict = 1`, table).Scan(&n); err != nil || n != 1 {
			t.Fatalf("missing STRICT table %s: %d, %v", table, n, err)
		}
	}

	epoch, err := db.Meta(ctx, "epoch")
	if err != nil {
		t.Fatalf("Meta(epoch): %v", err)
	}
	if epoch != "1" {
		t.Fatalf("meta epoch = %q, want \"1\"", epoch)
	}
	hubKey, err := db.Meta(ctx, "hub_key")
	if err != nil {
		t.Fatalf("Meta(hub_key): %v", err)
	}
	if !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(hubKey) {
		t.Fatalf("meta hub_key = %q, want 32 bytes lower hex", hubKey)
	}

	// Pin one pooled connection so the pragma checks read a real connection's state.
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	var mode string
	if err := conn.QueryRowContext(ctx, `PRAGMA journal_mode`).Scan(&mode); err != nil {
		t.Fatal(err)
	}
	if mode != "wal" {
		t.Fatalf("journal_mode = %q, want wal", mode)
	}
	var fk int
	if err := conn.QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&fk); err != nil {
		t.Fatal(err)
	}
	if fk != 1 {
		t.Fatalf("foreign_keys = %d, want 1", fk)
	}

	rows, err := db.QueryContext(ctx, `SELECT name FROM sqlite_master WHERE type='table'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	have := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		have[name] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	for _, tbl := range append([]string{"schema_migrations"}, v1Tables...) {
		if !have[tbl] {
			t.Errorf("table %q missing", tbl)
		}
	}

	// SetMeta round-trips and overwrites; a missing key is sql.ErrNoRows.
	if err := db.SetMeta(ctx, "probe", "a"); err != nil {
		t.Fatal(err)
	}
	if err := db.SetMeta(ctx, "probe", "b"); err != nil {
		t.Fatal(err)
	}
	if v, err := db.Meta(ctx, "probe"); err != nil || v != "b" {
		t.Fatalf("Meta(probe) = %q, %v; want \"b\"", v, err)
	}
	if _, err := db.Meta(ctx, "absent"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("Meta(absent) err = %v, want sql.ErrNoRows", err)
	}
}

func TestMigration0012KeepsMessages(t *testing.T) {
	ctx := context.Background()
	db := openTemp(t)
	raw, err := os.ReadFile("migrations/0001_init.sql")
	if err != nil {
		t.Fatal(err)
	}
	start := strings.Index(string(raw), "CREATE TABLE messages (")
	if start < 0 {
		t.Fatal("messages definition missing")
	}
	ddl := string(raw)[start:]
	end := strings.Index(ddl, ") STRICT;")
	if end < 0 {
		t.Fatal("messages definition unterminated")
	}
	for _, q := range []string{
		`DROP TABLE IF EXISTS reactions`, `DROP TABLE IF EXISTS reaction_ops`, `DROP TABLE IF EXISTS reaction_wakes`,
		`DROP TABLE messages`, ddl[:end+len(") STRICT;")],
		`DELETE FROM schema_migrations WHERE name = '0012_reactions.sql'`,
		`INSERT INTO messages VALUES ('msg_old','thr_old',1,'peer_old','message','2026-11-01T00:00:00.000Z','{"text":"keep exact bytes"}','job_old','apr_old'), ('msg_null','thr_old',2,'peer_other','job_status','2026-11-02T00:00:00.000Z','{}',NULL,NULL)`,
	} {
		if _, err := db.ExecContext(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	read := func() [][]any {
		t.Helper()
		rows, err := db.QueryContext(ctx, `SELECT * FROM messages ORDER BY msg_id`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var result [][]any
		for rows.Next() {
			values := make([]any, 9)
			args := make([]any, 9)
			for i := range values {
				args[i] = &values[i]
			}
			if err := rows.Scan(args...); err != nil {
				t.Fatal(err)
			}
			result = append(result, values)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		return result
	}
	before := read()
	applied, err := store.Migrate(ctx, db.DB)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(applied, []string{"0012_reactions.sql"}) {
		t.Fatalf("applied %v, want 0012 alone", applied)
	}
	if got := read(); !reflect.DeepEqual(got, before) {
		t.Fatalf("messages changed: %#v, want %#v", got, before)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO messages VALUES ('msg_rx','thr_old',3,'peer_old','reaction','now','{}',NULL,NULL)`); err != nil {
		t.Fatalf("reaction refused: %v", err)
	}
	for _, q := range []string{
		`INSERT INTO messages VALUES ('msg_bad','thr_old',4,'peer_old','bogus','now','{}',NULL,NULL)`,
		`INSERT INTO messages VALUES ('msg_duplicate','thr_old',1,'peer_old','message','now','{}',NULL,NULL)`,
		`INSERT INTO messages VALUES ('msg_seq','thr_old',0,'peer_old','message','now','{}',NULL,NULL)`,
		`INSERT INTO messages VALUES ('msg_json','thr_old',5,'peer_old','message','now','bad',NULL,NULL)`,
	} {
		if _, err := db.ExecContext(ctx, q); err == nil {
			t.Fatalf("constraint lost: %s", q)
		}
	}
	var n int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM pragma_index_info('reaction_ops_peer_at') WHERE (seqno = 0 AND name = 'peer_id') OR (seqno = 1 AND name = 'at')`).Scan(&n); err != nil || n != 2 {
		t.Fatalf("reaction_ops index: %d, %v", n, err)
	}
}

func TestReactionTablesPrimaryKeys(t *testing.T) {
	db := openTemp(t)
	for _, table := range []string{"reactions", "reaction_wakes"} {
		q := `INSERT INTO ` + table + `(msg_id, peer_id, emoji, created_at) VALUES ('msg_x','peer_x','✅','now')`
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%s first insert: %v", table, err)
		}
		if _, err := db.Exec(q); err == nil {
			t.Fatalf("%s duplicate accepted", table)
		}
		for _, args := range [][]any{{"msg_y", "peer_x", "✅"}, {"msg_x", "peer_y", "✅"}, {"msg_x", "peer_x", "❓"}} {
			if _, err := db.Exec(`INSERT INTO `+table+` VALUES (?,?,?,'now')`, args...); err != nil {
				t.Fatalf("%s distinct key: %v", table, err)
			}
		}
	}
}

func TestMigrateChecksumMismatchFails(t *testing.T) {
	ctx := context.Background()
	db := openTemp(t)
	res, err := db.ExecContext(ctx, `UPDATE schema_migrations SET sha256 = 'tampered' WHERE name = '0001_init.sql'`)
	if err != nil {
		t.Fatalf("tamper: %v", err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		t.Fatalf("tamper touched %d rows, want 1", n)
	}
	applied, err := store.Migrate(ctx, db.DB)
	if err == nil {
		t.Fatalf("Migrate after tamper succeeded (applied %v), want checksum error", applied)
	}
	if !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("Migrate error = %q, want it to mention \"checksum\"", err)
	}
}

func TestTxImmediateSerialises(t *testing.T) {
	ctx := context.Background()
	db := openTemp(t)
	if _, err := db.ExecContext(ctx, `INSERT INTO counters(name, value) VALUES ('race', 0)`); err != nil {
		t.Fatal(err)
	}

	// Each Tx reads, does more reading, then writes. Under a deferred BEGIN
	// two transactions both take a read snapshot and the later writer fails
	// with SQLITE_BUSY (the busy handler cannot resolve a stale-snapshot lock
	// upgrade), or a lost update makes the total short. BEGIN IMMEDIATE takes
	// the write lock up front, so all 200 increments land.
	const workers, iters = 2, 100
	start := make(chan struct{})
	errs := make(chan error, workers*iters)
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for i := 0; i < iters; i++ {
				err := db.Tx(ctx, func(tx *sql.Tx) error {
					var v int
					if err := tx.QueryRowContext(ctx, `SELECT value FROM counters WHERE name = 'race'`).Scan(&v); err != nil {
						return err
					}
					var dummy int
					if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master`).Scan(&dummy); err != nil {
						return err
					}
					_, err := tx.ExecContext(ctx, `UPDATE counters SET value = ? WHERE name = 'race'`, v+1)
					return err
				})
				if err != nil {
					errs <- err
				}
			}
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("Tx: %v", err)
	}

	var got int
	if err := db.QueryRowContext(ctx, `SELECT value FROM counters WHERE name = 'race'`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != workers*iters {
		t.Fatalf("counter = %d, want %d", got, workers*iters)
	}
}

func TestTxRollsBackOnErrorAndPanic(t *testing.T) {
	ctx := context.Background()
	db := openTemp(t)
	boom := errors.New("boom")
	err := db.Tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO counters(name, value) VALUES ('err', 1)`); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("Tx err = %v, want boom", err)
	}
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("Tx swallowed the panic")
			}
		}()
		_ = db.Tx(ctx, func(tx *sql.Tx) error {
			if _, err := tx.ExecContext(ctx, `INSERT INTO counters(name, value) VALUES ('panic', 1)`); err != nil {
				return err
			}
			panic("boom")
		})
	}()
	var n int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM counters WHERE name IN ('err', 'panic')`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("%d rows survived rolled-back transactions, want 0", n)
	}
}

// Migration 0007 recreates routes (SQLite cannot alter a CHECK): it must
// keep every existing route id and key, and it must accept kind grokbot.
func TestMigration0007KeepsRoutes(t *testing.T) {
	ctx := context.Background()
	db := openTemp(t)

	// Put the schema back to before 0007, with a live route in it.
	for _, q := range []string{
		`DROP TABLE route_runs`,
		`DROP TABLE routes`,
		`CREATE TABLE routes (
			route_id    TEXT PRIMARY KEY,
			owner_kind  TEXT NOT NULL CHECK (owner_kind IN ('peer', 'principal')),
			owner_id    TEXT NOT NULL,
			kind        TEXT NOT NULL DEFAULT 'http' CHECK (kind IN ('http')),
			url         TEXT NOT NULL,
			verified_at TEXT,
			created_at  TEXT NOT NULL
		) STRICT`,
		`CREATE INDEX routes_owner ON routes(owner_kind, owner_id)`,
		`DELETE FROM schema_migrations WHERE name = '0007_grokbot_routes.sql'`,
		`INSERT INTO routes(route_id, owner_kind, owner_id, url, verified_at, created_at)
		 VALUES ('route_a', 'peer', 'peer_a', 'https://a.example/hook', '2026-11-01T00:00:00.000Z', '2026-10-31T00:00:00.000Z'),
		        ('route_b', 'principal', 'prn_b', 'https://b.example/hook', NULL, '2026-10-31T00:00:01.000Z')`,
		`INSERT INTO route_keys(route_id, whsec, created_at) VALUES ('route_a', 'whsec_a', '2026-10-31T00:00:00.000Z')`,
	} {
		if _, err := db.ExecContext(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}

	applied, err := store.Migrate(ctx, db.DB)
	if err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if len(applied) != 1 || applied[0] != "0007_grokbot_routes.sql" {
		t.Fatalf("Migrate applied %v, want 0007 alone", applied)
	}

	var ownerKind, ownerID, kind, url, verified, created string
	var keyEnc *string
	var perHour int
	if err := db.QueryRowContext(ctx,
		`SELECT owner_kind, owner_id, kind, url, verified_at, created_at, key_enc, runs_per_hour FROM routes WHERE route_id = 'route_a'`).
		Scan(&ownerKind, &ownerID, &kind, &url, &verified, &created, &keyEnc, &perHour); err != nil {
		t.Fatalf("route_a: %v", err)
	}
	if ownerKind != "peer" || ownerID != "peer_a" || kind != "http" || url != "https://a.example/hook" ||
		verified != "2026-11-01T00:00:00.000Z" || created != "2026-10-31T00:00:00.000Z" || keyEnc != nil || perHour != 200 {
		t.Fatalf("route_a changed: %s %s %s %s %s %s %v %d", ownerKind, ownerID, kind, url, verified, created, keyEnc, perHour)
	}
	var n int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM routes WHERE route_id = 'route_b' AND verified_at IS NULL`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("route_b: %d, %v", n, err)
	}
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM route_keys WHERE route_id = 'route_a' AND whsec = 'whsec_a'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("route_keys: %d, %v", n, err)
	}
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type = 'index' AND name = 'routes_owner'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("routes_owner index: %d, %v", n, err)
	}

	if _, err := db.ExecContext(ctx,
		`INSERT INTO routes(route_id, owner_kind, owner_id, kind, url, key_enc, created_at) VALUES ('route_g', 'peer', 'peer_g', 'grokbot', 'u', 'v1:x', 'now')`); err != nil {
		t.Fatalf("kind grokbot refused: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO routes(route_id, owner_kind, owner_id, kind, url, created_at) VALUES ('route_x', 'peer', 'peer_g', 'smtp', 'u', 'now')`); err == nil {
		t.Fatalf("kind smtp accepted")
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO route_runs(route_id, at) VALUES ('route_g', '2026-11-01T00:00:00.000Z')`); err != nil {
		t.Fatalf("route_runs: %v", err)
	}
}

// VECNET_SQLITE_SYNCHRONOUS picks WAL fsync policy: full (default, 2) or normal (1); anything else refuses to open.
func TestOpenSynchronousEnv(t *testing.T) {
	for _, tc := range []struct {
		env  string
		want int
	}{{"", 2}, {"full", 2}, {"FULL", 2}, {"normal", 1}, {"Normal", 1}} {
		t.Run("env="+tc.env, func(t *testing.T) {
			t.Setenv("VECNET_SQLITE_SYNCHRONOUS", tc.env)
			db, err := store.Open(filepath.Join(t.TempDir(), "hub.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			var got int
			if err := db.QueryRow("PRAGMA synchronous").Scan(&got); err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("synchronous=%d, want %d", got, tc.want)
			}
		})
	}
	t.Setenv("VECNET_SQLITE_SYNCHRONOUS", "off")
	if db, err := store.Open(filepath.Join(t.TempDir(), "hub.db")); err == nil {
		db.Close()
		t.Fatal("synchronous=off opened; want an error")
	} else if !strings.Contains(err.Error(), "VECNET_SQLITE_SYNCHRONOUS") {
		t.Fatalf("err=%v, want it to name VECNET_SQLITE_SYNCHRONOUS", err)
	}
}

func TestMigration0014AddsCreatedBy(t *testing.T) {
	ctx := context.Background()
	db := openTemp(t)
	for _, q := range []string{
		`DROP INDEX peers_created_by`,
		`ALTER TABLE peers DROP COLUMN created_by`,
		`DROP TABLE principal_limits`, `DROP TABLE principal_default_scopes`, `DROP TABLE peer_create_keys`,
		`DELETE FROM schema_migrations WHERE name = '0014_principal_peers.sql'`,
		`INSERT INTO principals(prn_id, name, created_at) VALUES ('prn_old','host','2026-11-01T00:00:00.000Z')`,
		`INSERT INTO peers(peer_id, prn_id, name, kind, enrolled_at) VALUES ('peer_old','prn_old','old','pagchat-bot','2026-11-01T00:00:00.000Z')`,
	} {
		if _, err := db.ExecContext(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	applied, err := store.Migrate(ctx, db.DB)
	if err != nil || !reflect.DeepEqual(applied, []string{"0014_principal_peers.sql"}) {
		t.Fatalf("Migrate = %v, %v", applied, err)
	}
	var prn, name, kind, at string
	var creator sql.NullString
	if err := db.QueryRowContext(ctx, `SELECT prn_id,name,kind,enrolled_at,created_by FROM peers WHERE peer_id = 'peer_old'`).Scan(&prn, &name, &kind, &at, &creator); err != nil {
		t.Fatal(err)
	}
	if prn != "prn_old" || name != "old" || kind != "pagchat-bot" || at != "2026-11-01T00:00:00.000Z" || creator.Valid {
		t.Fatalf("migrated row = %q %q %q %q %+v", prn, name, kind, at, creator)
	}
	for _, table := range []string{"principal_limits", "principal_default_scopes", "peer_create_keys"} {
		var n int
		if err := db.QueryRowContext(ctx, `SELECT count(*) FROM pragma_table_list WHERE name = ? AND strict = 1`, table).Scan(&n); err != nil || n != 1 {
			t.Fatalf("table %s = %d, %v", table, n, err)
		}
	}
	q := `INSERT INTO principal_limits(prn_id,updated_at) VALUES ('prn_old','2026-11-01T00:00:00.000Z')`
	if _, err := db.ExecContext(ctx, q); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, q); err == nil {
		t.Fatal("duplicate principal_limits accepted")
	}
	var max, rate int
	var kinds string
	if err := db.QueryRowContext(ctx, `SELECT max_peers,creates_per_hour,kinds FROM principal_limits WHERE prn_id = 'prn_old'`).Scan(&max, &rate, &kinds); err != nil || max != 0 || rate != 60 || kinds != "[]" {
		t.Fatalf("defaults = %d %d %q, %v", max, rate, kinds, err)
	}
}

// TestMigration0018BackfillsSenderInfo: messages and delivery Events stored
// before Message.sender_info get the sender's current directory name and
// kind (a peer's, a source's as kind "source", the hub's as {"hub","hub"});
// the rest of each body keeps its exact bytes, and rows that already carry
// sender_info or whose sender is unknown are untouched.
func TestMigration0018BackfillsSenderInfo(t *testing.T) {
	ctx := context.Background()
	db := openTemp(t)
	const tail = `"metadata":{"lt":"<b>","n":1.50,"e":1e3}}`
	legacy := func(id, sender string) string {
		return `{"message_id":"` + id + `","sender":"` + sender + `",` + tail
	}
	for _, q := range []string{
		`DELETE FROM schema_migrations WHERE name = '0018_sender_info.sql'`,
		`INSERT INTO principals(prn_id, name, created_at) VALUES ('prn_a','host','2026-11-01T00:00:00.000Z')`,
		`INSERT INTO peers(peer_id, prn_id, name, kind, enrolled_at) VALUES ('peer_a','prn_a','Ann "A"','pagchat-bot','2026-11-01T00:00:00.000Z')`,
		`INSERT INTO sources(src_id, kind, name, target_id, secret_hash, secret_enc, created_at) VALUES ('src_g','github','gh','peer_a','h','e','2026-11-01T00:00:00.000Z')`,
	} {
		if _, err := db.ExecContext(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	msgs := map[string]string{
		"msg_peer": legacy("msg_peer", "peer_a"),
		"msg_src":  legacy("msg_src", "src_g"),
		"msg_hub":  legacy("msg_hub", "hub"),
		"msg_gone": legacy("msg_gone", "peer_gone"),
		"msg_has":  `{"message_id":"msg_has","sender":"peer_a","sender_info":{"name":"Then","kind":"session"},` + tail,
	}
	seq := 0
	for id, body := range msgs {
		seq++
		var sender string
		switch id {
		case "msg_peer", "msg_has":
			sender = "peer_a"
		case "msg_src":
			sender = "src_g"
		case "msg_hub":
			sender = "hub"
		default:
			sender = "peer_gone"
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO messages(msg_id, thr_id, seq, sender, kind, created_at, body) VALUES (?, 'thr_x', ?, ?, 'message', 'now', ?)`, id, seq, sender, body); err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO events(owner, type, at, data) VALUES ('peer_r', 'delivery', 'now', ?)`, `{"delivery_id":"dly_`+id+`","message":`+body+`}`); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO events(owner, type, at, data) VALUES ('peer_r', 'receipt', 'now', '{"message_id":"msg_peer"}')`); err != nil {
		t.Fatal(err)
	}

	applied, err := store.Migrate(ctx, db.DB)
	if err != nil || !reflect.DeepEqual(applied, []string{"0018_sender_info.sql"}) {
		t.Fatalf("Migrate = %v, %v", applied, err)
	}
	with := func(id, sender, info string) string {
		return `{"message_id":"` + id + `","sender":"` + sender + `",` + strings.TrimSuffix(tail, "}") + `,"sender_info":` + info + `}`
	}
	want := map[string]string{
		"msg_peer": with("msg_peer", "peer_a", `{"name":"Ann \"A\"","kind":"pagchat-bot"}`),
		"msg_src":  with("msg_src", "src_g", `{"name":"gh","kind":"source"}`),
		"msg_hub":  with("msg_hub", "hub", `{"name":"hub","kind":"hub"}`),
		"msg_gone": msgs["msg_gone"],
		"msg_has":  msgs["msg_has"],
	}
	for id, w := range want {
		var body, data string
		if err := db.QueryRowContext(ctx, `SELECT body FROM messages WHERE msg_id = ?`, id).Scan(&body); err != nil {
			t.Fatal(err)
		}
		if body != w {
			t.Errorf("%s body:\n got %s\nwant %s", id, body, w)
		}
		if err := db.QueryRowContext(ctx, `SELECT data FROM events WHERE json_extract(data, '$.delivery_id') = ?`, "dly_"+id).Scan(&data); err != nil {
			t.Fatal(err)
		}
		if wantData := `{"delivery_id":"dly_` + id + `","message":` + w + `}`; data != wantData {
			t.Errorf("%s event:\n got %s\nwant %s", id, data, wantData)
		}
	}
	var receipt string
	if err := db.QueryRowContext(ctx, `SELECT data FROM events WHERE type = 'receipt'`).Scan(&receipt); err != nil || receipt != `{"message_id":"msg_peer"}` {
		t.Fatalf("receipt event = %q, %v", receipt, err)
	}
}

func TestMigration0019SeedsIdentity(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "hub.db")
	db, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	seed, err := db.Meta(ctx, "hub_identity_seed")
	if err != nil || !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(seed) {
		t.Fatalf("identity seed = %q, %v", seed, err)
	}
	created, err := db.Meta(ctx, "hub_identity_created_at")
	if err != nil || !regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}Z$`).MatchString(created) {
		t.Fatalf("created_at = %q, %v", created, err)
	}
	hubKey, err := db.Meta(ctx, "hub_key")
	if err != nil || seed == hubKey {
		t.Fatalf("identity must be separate from hub_key: %v", err)
	}
	second, err := openTemp(t).Meta(ctx, "hub_identity_seed")
	if err != nil || seed == second {
		t.Fatalf("fresh DB identity reused: %v", err)
	}
	for _, kind := range []string{"moving", "maintenance", "restart", "other"} {
		_, err := db.ExecContext(ctx, `INSERT INTO hub_notices(notice_id,kind,message,hub,issued_at,expires_at,sig,created_by) VALUES (?,?, 'Moving tonight','https://hub.example','2026-11-05T07:00:00.000Z','2026-11-06T07:00:00.000Z','sig','peer_operator')`, "ntc_"+kind, kind)
		if (err == nil) != (kind != "other") {
			t.Fatalf("kind %q insert = %v", kind, err)
		}
	}
	var n int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM pragma_table_list WHERE name='hub_notices' AND strict=1`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("STRICT hub_notices: %d, %v", n, err)
	}
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM pragma_index_info('hub_notices_active') WHERE (seqno=0 AND name='cleared_at') OR (seqno=1 AND name='kind')`).Scan(&n); err != nil || n != 2 {
		t.Fatalf("active index: %d, %v", n, err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	for name, want := range map[string]string{"hub_identity_seed": seed, "hub_identity_created_at": created} {
		got, err := reopened.Meta(ctx, name)
		if err != nil || got != want {
			t.Fatalf("reopened %s = %q, %v; want %q", name, got, err, want)
		}
	}
}
