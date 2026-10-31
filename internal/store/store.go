// Package store owns the hub's SQLite database: opening it with the right
// pragmas, applying the embedded migrations, and the transaction helper every
// writer goes through.
package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

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
	"&_pragma=synchronous(FULL)" +
	"&_pragma=busy_timeout(5000)" +
	"&_pragma=foreign_keys(ON)"

// maxOpenConns bounds the pool; writers serialise on the SQLite write lock anyway.
const maxOpenConns = 4

// uriPathEscaper percent-encodes the characters that would otherwise end the
// path part of a "file:" URI (SQLite decodes %XX in URI file names).
var uriPathEscaper = strings.NewReplacer("%", "%25", "?", "%3f", "#", "%23")

// Open opens (creating if needed) the database at path, applies pragmas on
// every connection, and runs all pending migrations.
func Open(path string) (*DB, error) {
	dsn := "file:" + uriPathEscaper.Replace(path) + "?" + dsnParams
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
	return &DB{sqldb}, nil
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
