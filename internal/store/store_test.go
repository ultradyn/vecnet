package store_test

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
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
