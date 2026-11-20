package testhub

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/XertroV/vecnet/internal/store"
)

// The template is a closed, checkpointed, fully migrated empty database, not a
// live shared store. Keep only its bytes, so no temporary directory outlives
// setup. Each fixture gets its own file, pool, transactions and identity.
var emptyStoreImage = sync.OnceValues(func() ([]byte, error) {
	dir, err := os.MkdirTemp("", "vecnet-test-schema-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "hub.db")
	db, err := store.OpenWithSynchronous(path, "normal")
	if err != nil {
		return nil, err
	}
	if err := db.Close(); err != nil {
		return nil, err
	}
	return os.ReadFile(path)
})

// FreshStore opens an independent disposable store without reapplying every
// migration for every fixture. Disposable logical fixtures explicitly use
// NORMAL; production defaults, migrations and power-loss/durability contracts
// use store.Open directly (FULL by default).
// The caller owns the path; the handle is closed at test cleanup.
func FreshStore(t *testing.T, path string) *store.DB {
	t.Helper()
	image, err := emptyStoreImage()
	if err != nil {
		t.Fatal(err)
	}
	// Never overwrite an existing database or silently discard its WAL.
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_, writeErr := file.Write(image)
	closeErr := file.Close()
	if writeErr != nil {
		t.Fatal(writeErr)
	}
	if closeErr != nil {
		t.Fatal(closeErr)
	}
	db := OpenFixtureStore(t, path)
	// These normally come from migrations. Never share signing/sealing material
	// between independent hubs just because they share a schema image.
	if err := db.Tx(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE meta SET value=lower(hex(randomblob(32))) WHERE key IN ('hub_key','hub_identity_seed')`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return db
}

// OpenFixtureStore opens or reopens an existing disposable logical fixture with
// per-database NORMAL policy. It must not be used for fsync/power-loss contract
// tests; those use store.Open. Every pooled connection gets the same explicit
// policy without modifying the process environment, including parallel tests.
func OpenFixtureStore(t *testing.T, path string) *store.DB {
	t.Helper()
	db, err := store.OpenWithSynchronous(path, "normal")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}
