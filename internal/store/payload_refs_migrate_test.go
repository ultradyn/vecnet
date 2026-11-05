package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPayloadRefsAdditiveFreshAndUpgrade(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "upgrade.db")+"?"+dsnParams)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	migs, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE schema_migrations(name TEXT PRIMARY KEY,sha256 TEXT NOT NULL,applied_at TEXT NOT NULL) STRICT`)
	if err != nil {
		t.Fatal(err)
	}
	var addition migration
	for _, m := range migs {
		if strings.HasSuffix(m.name, "_event_payload_refs.sql") {
			addition = m
			break
		}
		if _, err := applyOne(ctx, db, m); err != nil {
			t.Fatal(err)
		}
	}
	if addition.name == "" {
		t.Fatal("missing _event_payload_refs.sql migration")
	}
	const legacy = ` { "message": {"message_id":"msg_old","metadata":{"n":1.50}}, "thread":{"title":"old"} } `
	if _, err := db.Exec(`INSERT INTO events(owner,type,at,data) VALUES ('peer_old','delivery','now',?)`, legacy); err != nil {
		t.Fatal(err)
	}
	if _, err := Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	var data, format string
	var msg, snapshot sql.NullString
	if err := db.QueryRow(`SELECT data,payload_format,msg_id,snapshot_id FROM events`).Scan(&data, &format, &msg, &snapshot); err != nil {
		t.Fatal(err)
	}
	if data != legacy || format != "inline" || msg.Valid || snapshot.Valid {
		t.Fatal("upgrade changed legacy bytes or defaults")
	}
	// Reapply verifies every historic checksum, including migration 0018.
	if applied, err := Migrate(ctx, db); err != nil || len(applied) != 0 {
		t.Fatal("checksum or idempotence failure", err)
	}
	fresh, err := Open(filepath.Join(t.TempDir(), "fresh.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer fresh.Close()
	var strict int
	if err := fresh.QueryRow(`SELECT strict FROM pragma_table_list WHERE name='event_message_snapshots'`).Scan(&strict); err != nil || strict != 1 {
		t.Fatal("snapshots not STRICT", err)
	}
	var n int
	if err := fresh.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='index' AND tbl_name='events' AND sql LIKE '%WHERE%IS NOT NULL%'`).Scan(&n); err != nil || n != 2 {
		t.Fatal("missing dependency indexes", n, err)
	}
}
func TestPayloadReferenceIdentityAndImmutability(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "refs.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`INSERT INTO messages(msg_id,thr_id,seq,sender,kind,created_at,body) VALUES ('msg_a','thr_a',1,'hub','message','now','{}')`); err != nil {
		t.Fatal(err)
	}
	// Legacy behavior stays available until the Task 3 cutover enables the guard.
	if _, err := db.Exec(`UPDATE messages SET body=' { } ' WHERE msg_id='msg_a'`); err != nil {
		t.Fatal("guard enabled before cutover", err)
	}
	if err := db.SetMeta(context.Background(), "event_payload_refs_enabled", "1"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE messages SET body='{}' WHERE msg_id='msg_a'`); err == nil {
		t.Fatal("mutable message after cutover")
	}
	if _, err := db.Exec(`UPDATE messages SET body=body WHERE msg_id='msg_a'`); err != nil {
		t.Fatal("no-op body update rejected", err)
	}
	if _, err := db.Exec(`INSERT INTO event_message_snapshots(snapshot_id,msg_id,body_sha256,body) VALUES ('snap_a','msg_a','hash','{}')`); err != nil {
		t.Fatal(err)
	}
	for i, query := range []string{
		`UPDATE event_message_snapshots SET body='[]' WHERE snapshot_id='snap_a'`,
		`UPDATE event_message_snapshots SET msg_id='msg_b' WHERE snapshot_id='snap_a'`,
		`INSERT INTO event_message_snapshots VALUES ('snap_b','msg_a','hash','{}')`,
		`INSERT INTO event_message_snapshots VALUES ('snap_c','msg_b','hash','bad')`,
		`INSERT INTO events(owner,type,at,data,payload_format) VALUES ('p','delivery','now','{}','bad')`,
		`INSERT INTO events(owner,type,at,data,msg_id) VALUES ('p','delivery','now','{}','msg_a')`,
		`INSERT INTO events(owner,type,at,data,payload_format) VALUES ('p','delivery','now','{}','delivery_ref_v1')`,
		`INSERT INTO events(owner,type,at,data,payload_format,msg_id) VALUES ('p','delivery','now','{"msg_id":"other"}','delivery_ref_v1','msg_a')`,
		`INSERT INTO events(owner,type,at,data,payload_format,msg_id) VALUES ('p','receipt','now','{"msg_id":"msg_a"}','delivery_ref_v1','msg_a')`,
		`INSERT INTO events(owner,type,at,data,payload_format,msg_id,snapshot_id) VALUES ('p','delivery','now','{"msg_id":"msg_a","snapshot_id":"other"}','delivery_ref_v1','msg_a','snap_a')`,
	} {
		if _, err := db.Exec(query); err == nil {
			t.Fatal("constraint missing", i)
		}
	}
	if _, err := db.Exec(`INSERT INTO events(owner,type,at,data,payload_format,msg_id) VALUES ('p','delivery','now','{"msg_id":"msg_a"}','delivery_ref_v1','msg_a')`); err != nil {
		t.Fatal("valid canonical reference", err)
	}
	if _, err := db.Exec(`INSERT INTO events(owner,type,at,data,payload_format,msg_id,snapshot_id) VALUES ('p','delivery','now','{"msg_id":"msg_a","snapshot_id":"snap_a"}','delivery_ref_v1','msg_a','snap_a')`); err != nil {
		t.Fatal("valid snapshot reference", err)
	}
}
func TestReadSnapshotDeferredConsistentDuringDelete(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "snapshot.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`INSERT INTO events(owner,type,at,data) VALUES ('p','receipt','now','{}')`); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err = db.ReadSnapshot(ctx, func(q *sql.Conn) error {
		var before int
		if err := q.QueryRowContext(ctx, `SELECT count(*) FROM events`).Scan(&before); err != nil {
			return err
		}
		done := make(chan error, 1)
		go func() {
			done <- db.Tx(ctx, func(tx *sql.Tx) error { _, err := tx.ExecContext(ctx, `DELETE FROM events`); return err })
		}()
		select {
		case err := <-done:
			if err != nil {
				return err
			}
		case <-ctx.Done():
			return ctx.Err()
		}
		var after int
		if err := q.QueryRowContext(ctx, `SELECT count(*) FROM events`).Scan(&after); err != nil {
			return err
		}
		if before != 1 || after != 1 {
			t.Fatal("read snapshot changed during delete")
		}
		if _, err := q.ExecContext(ctx, `INSERT INTO counters(name,value) VALUES ('readonly',1)`); err == nil {
			t.Fatal("read snapshot allowed writes")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM events`).Scan(&n); err != nil || n != 0 {
		t.Fatal("writer did not commit", n, err)
	}
	boom := errors.New("rollback")
	if err := db.ReadSnapshot(ctx, func(*sql.Conn) error { return boom }); !errors.Is(err, boom) {
		t.Fatal("callback error lost")
	}
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("panic lost")
			}
		}()
		_ = db.ReadSnapshot(ctx, func(*sql.Conn) error { panic("test") })
	}()
	if err := db.Tx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO counters(name,value) VALUES ('after_snapshot',1)`)
		return err
	}); err != nil {
		t.Fatal("query_only leaked to pool", err)
	}
}

func TestReadSnapshotCancellationReturnsUsableConnection(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "cancel.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	ctx, cancel := context.WithCancel(context.Background())
	err = db.ReadSnapshot(ctx, func(q *sql.Conn) error {
		var n int
		if err := q.QueryRowContext(ctx, `SELECT count(*) FROM events`).Scan(&n); err != nil {
			return err
		}
		cancel()
		return ctx.Err()
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation lost", err)
	}
	next, stop := context.WithTimeout(context.Background(), time.Second)
	defer stop()
	if err := db.Tx(next, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(next, `INSERT INTO counters(name,value) VALUES ('after_cancel',1)`)
		return err
	}); err != nil {
		t.Fatal("canceled snapshot poisoned pool", err)
	}
}
