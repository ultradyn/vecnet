// Package store owns the hub's SQLite database: opening it with the right
// pragmas, applying the embedded migrations, and the transaction helper every
// writer goes through.
package store

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"os"
	"strings"
	"time"

	_ "modernc.org/sqlite" // registers the "sqlite" driver (pure Go, CGO_ENABLED=0)
)

// DB is the hub's single database handle.
type DB struct{ *sql.DB }

// dsnParams are applied to every pooled connection. _txlock=immediate makes
// every BeginTx a BEGIN IMMEDIATE: database/sql otherwise issues a deferred
// BEGIN, and two read-then-write transactions would fail with SQLITE_BUSY on
// the lock upgrade instead of queueing on busy_timeout.
const dsnParams = "_txlock=immediate" +
	"&_pragma=journal_mode(WAL)" +
	"&_pragma=busy_timeout(5000)" +
	"&_pragma=wal_autocheckpoint(1000)" +
	"&_pragma=foreign_keys(ON)"

// synchronousEnv selects the WAL fsync policy. FULL (the default) fsyncs every
// commit, so an acknowledged write survives power loss. NORMAL fsyncs only at
// checkpoints: it survives a process crash but can lose the last transactions
// on power loss, in exchange for far less fsync stall under I/O pressure
// (issue 32). Dev hubs may opt in; production keeps FULL unless the operator
// decides otherwise.
const synchronousEnv = "VECNET_SQLITE_SYNCHRONOUS"

func synchronousPragma() (string, error) {
	return parseSynchronous(os.Getenv(synchronousEnv))
}

func parseSynchronous(value string) (string, error) {
	switch v := strings.ToLower(value); v {
	case "", "full":
		return "&_pragma=synchronous(FULL)", nil
	case "normal":
		return "&_pragma=synchronous(NORMAL)", nil
	default:
		return "", fmt.Errorf("store: %s=%q: want full or normal", synchronousEnv, v)
	}
}

// maxOpenConns bounds the pool; writers serialise on the SQLite write lock anyway.
const maxOpenConns = 4

// uriPathEscaper percent-encodes the characters that would otherwise end the
// path part of a "file:" URI (SQLite decodes %XX in URI file names).
var uriPathEscaper = strings.NewReplacer("%", "%25", "?", "%3f", "#", "%23")

// Open opens (creating if needed) the database at path, applies pragmas on
// every connection, and runs all pending migrations.
func Open(path string) (*DB, error) {
	sync, err := synchronousPragma()
	if err != nil {
		return nil, err
	}
	return open(path, sync)
}

// OpenWithSynchronous opens a database with an explicit connection-wide WAL
// policy ("full" or "normal"), independent of the process environment. Prefer
// Open for operator-configured stores. NORMAL is suitable only when loss of
// recent acknowledged transactions on power failure is acceptable.
func OpenWithSynchronous(path, policy string) (*DB, error) {
	if policy == "" {
		return nil, fmt.Errorf("store: explicit synchronous policy required")
	}
	sync, err := parseSynchronous(policy)
	if err != nil {
		return nil, err
	}
	return open(path, sync)
}

func open(path, sync string) (*DB, error) {
	// Initialize only a truly empty SQLite file, before WAL or the first table
	// allocates pages. An existing NONE database must never be converted here.
	if err := initializeEmpty(path); err != nil {
		return nil, err
	}
	dsn := "file:" + uriPathEscaper.Replace(path) + "?" + dsnParams + sync
	sqldb, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("store: open %s: %w", path, err)
	}
	sqldb.SetMaxOpenConns(maxOpenConns)
	if err := sqldb.Ping(); err != nil {
		sqldb.Close()
		return nil, fmt.Errorf("store: open %s: %w", path, err)
	}
	if _, err := Migrate(context.Background(), sqldb); err != nil {
		sqldb.Close()
		return nil, err
	}
	db := &DB{sqldb}
	if err := db.initializeEventRetention(context.Background()); err != nil {
		sqldb.Close()
		return nil, err
	}
	return db, nil
}

func initializeEmpty(path string) error {
	db, err := sql.Open("sqlite", "file:"+uriPathEscaper.Replace(path)+"?_pragma=busy_timeout(5000)")
	if err != nil {
		return fmt.Errorf("store: initialize empty database: %w", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	var pages int
	if err := db.QueryRow("PRAGMA page_count").Scan(&pages); err != nil {
		return fmt.Errorf("store: inspect empty database: %w", err)
	}
	if pages != 0 {
		return nil
	}
	for _, q := range []string{"PRAGMA page_size=4096", "PRAGMA auto_vacuum=INCREMENTAL"} {
		if _, err := db.Exec(q); err != nil {
			return fmt.Errorf("store: initialize empty database: %w", err)
		}
	}
	return nil
}

// Tx runs fn inside one transaction (BEGIN IMMEDIATE via the DSN). It commits
// when fn returns nil and rolls back when fn returns an error or panics; a
// panic is re-raised after the rollback.
func (db *DB) Tx(ctx context.Context, fn func(tx *sql.Tx) error) (err error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: begin: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	if err := fn(tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: commit: %w", err)
	}
	committed = true
	return nil
}

// Meta returns the value stored under key in the meta table. A missing key
// returns an error wrapping sql.ErrNoRows.
func (db *DB) Meta(ctx context.Context, key string) (string, error) {
	var v string
	err := db.QueryRowContext(ctx, `SELECT value FROM meta WHERE key = ?`, key).Scan(&v)
	if err != nil {
		return "", fmt.Errorf("store: meta %q: %w", key, err)
	}
	return v, nil
}

// SetMeta stores value under key, replacing any previous value.
func (db *DB) SetMeta(ctx context.Context, key, value string) error {
	_, err := db.ExecContext(ctx,
		`INSERT INTO meta(key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
		key, value)
	if err != nil {
		return fmt.Errorf("store: set meta %q: %w", key, err)
	}
	return nil
}

// ReadSnapshot runs fn on a pinned connection in a deferred, read-only SQLite
// transaction. Explicit BEGIN DEFERRED avoids inheriting the writer path's
// _txlock=immediate default. The driver also supports a deferred ReadOnly
// BeginTx, but that option does not enforce read-only SQL; query_only does.
// Close event rows before querying bodies through the same connection.
func (db *DB) ReadSnapshot(ctx context.Context, fn func(q *sql.Conn) error) (err error) {
	conn, err := db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("store: read connection: %w", err)
	}
	defer conn.Close()
	if _, err = conn.ExecContext(ctx, "PRAGMA query_only=ON"); err != nil {
		// Cancellation can race a pragma that already took effect. Discard
		// this connection rather than risk leaking query_only into the pool.
		_ = conn.Raw(func(any) error { return driver.ErrBadConn })
		return fmt.Errorf("store: read-only: %w", err)
	}
	defer func() {
		// A canceled request must not return a transaction or query_only connection
		// to the pool. A broken cleanup discards the connection instead.
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, rollbackErr := conn.ExecContext(cleanup, "ROLLBACK")
		_, resetErr := conn.ExecContext(cleanup, "PRAGMA query_only=OFF")
		if rollbackErr != nil || resetErr != nil {
			_ = conn.Raw(func(any) error { return driver.ErrBadConn })
			if err == nil {
				err = fmt.Errorf("store: read snapshot cleanup failed")
			}
		}
	}()
	if _, err = conn.ExecContext(ctx, "BEGIN DEFERRED"); err != nil {
		return fmt.Errorf("store: read begin: %w", err)
	}
	return fn(conn)
}
