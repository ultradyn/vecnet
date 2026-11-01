package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"sort"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

type migration struct {
	name string
	sql  string
	sum  string // lower-hex SHA-256 of the file bytes
}

func loadMigrations() ([]migration, error) {
	names, err := fs.Glob(migrationsFS, "migrations/*.sql")
	if err != nil {
		return nil, err
	}
	sort.Strings(names)
	out := make([]migration, 0, len(names))
	for _, p := range names {
		b, err := migrationsFS.ReadFile(p)
		if err != nil {
			return nil, err
		}
		sum := sha256.Sum256(b)
		out = append(out, migration{name: p[len("migrations/"):], sql: string(b), sum: hex.EncodeToString(sum[:])})
	}
	return out, nil
}

// Migrate applies every embedded migration not yet recorded in
// schema_migrations, in name order, each in its own transaction, and returns
// the names it applied. A recorded migration whose stored SHA-256 differs from
// the embedded file is a checksum error and nothing further is applied.
func Migrate(ctx context.Context, db *sql.DB) (applied []string, err error) {
	if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
	name       TEXT PRIMARY KEY,
	sha256     TEXT NOT NULL,
	applied_at TEXT NOT NULL
) STRICT`); err != nil {
		return nil, fmt.Errorf("store: create schema_migrations: %w", err)
	}
	migs, err := loadMigrations()
	if err != nil {
		return nil, fmt.Errorf("store: load migrations: %w", err)
	}
	applied = []string{}
	for _, m := range migs {
		did, err := applyOne(ctx, db, m)
		if err != nil {
			return applied, err
		}
		if did {
			applied = append(applied, m.name)
		}
	}
	return applied, nil
}

func applyOne(ctx context.Context, db *sql.DB, m migration) (bool, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("store: migrate %s: begin: %w", m.name, err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit

	var have string
	err = tx.QueryRowContext(ctx, `SELECT sha256 FROM schema_migrations WHERE name = ?`, m.name).Scan(&have)
	switch {
	case err == nil:
		if have != m.sum {
			return false, fmt.Errorf("store: migration %s checksum mismatch: recorded %s, embedded %s", m.name, have, m.sum)
		}
		return false, nil
	case !errors.Is(err, sql.ErrNoRows):
		return false, fmt.Errorf("store: migrate %s: %w", m.name, err)
	}

	if _, err := tx.ExecContext(ctx, m.sql); err != nil {
		return false, fmt.Errorf("store: migrate %s: %w", m.name, err)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO schema_migrations(name, sha256, applied_at) VALUES (?, ?, strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))`,
		m.name, m.sum); err != nil {
		return false, fmt.Errorf("store: migrate %s: record: %w", m.name, err)
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("store: migrate %s: commit: %w", m.name, err)
	}
	return true, nil
}
