package store_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"testing"

	"github.com/XertroV/vecnet/internal/store"
)

// Keep only the bytes of a closed, fully migrated empty database. Building
// with explicit FULL makes the image independent of the caller's environment.
var migratedStoreImage = sync.OnceValues(func() (image []byte, err error) {
	dir, err := os.MkdirTemp("", "vecnet-store-image-")
	if err != nil {
		return nil, err
	}
	defer func() {
		if cleanupErr := os.RemoveAll(dir); err == nil && cleanupErr != nil {
			image, err = nil, cleanupErr
		}
	}()
	path := filepath.Join(dir, "hub.db")
	db, err := store.OpenWithSynchronous(path, "full")
	if err != nil {
		return nil, err
	}
	var busy, logPages, checkpointed int
	checkpointErr := db.QueryRow(`PRAGMA wal_checkpoint(TRUNCATE)`).Scan(&busy, &logPages, &checkpointed)
	closeErr := db.Close()
	if checkpointErr != nil {
		return nil, checkpointErr
	}
	if busy != 0 {
		return nil, fmt.Errorf("store image WAL checkpoint busy: %d of %d pages checkpointed", checkpointed, logPages)
	}
	if closeErr != nil {
		return nil, closeErr
	}
	return os.ReadFile(path)
})

// openMigratedFixture exercises real store.Open (including its per-connection
// pragmas and migration checks) on its own disposable file. It does not test
// first migration; tests of fresh database creation must use store.Open directly.
func openMigratedFixture(t *testing.T) *store.DB {
	t.Helper()
	image, err := migratedStoreImage()
	if err != nil {
		t.Fatalf("build store image: %v", err)
	}
	path := filepath.Join(t.TempDir(), "hub.db")
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_, writeErr := file.Write(image)
	closeErr := file.Close()
	if err := errors.Join(writeErr, closeErr); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	// Copy schema, not shared signing or sealing authority.
	if _, err := db.Exec(`UPDATE meta SET value=lower(hex(randomblob(32))) WHERE key IN ('hub_key','hub_identity_seed')`); err != nil {
		t.Fatal(err)
	}
	return db
}

// Check the fixture contract in the existing default-policy case, without
// adding another test root that would build its own image in native CI.
func checkMigratedFixtureIsolation(t *testing.T, first *store.DB) {
	t.Helper()
	ctx := context.Background()
	if err := first.SetMeta(ctx, "fixture_probe", "first only"); err != nil {
		t.Fatal(err)
	}
	second := openMigratedFixture(t)
	if _, err := second.Meta(ctx, "fixture_probe"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("second fixture saw first fixture's write: %v", err)
	}

	stat := func(db *sql.DB) os.FileInfo {
		t.Helper()
		var path string
		if err := db.QueryRow(`SELECT file FROM pragma_database_list WHERE name = 'main'`).Scan(&path); err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		return info
	}
	if os.SameFile(stat(first.DB), stat(second.DB)) {
		t.Fatal("fixtures share a database file")
	}
	keyFormat := regexp.MustCompile(`^[0-9a-f]{64}$`)
	for _, key := range []string{"hub_key", "hub_identity_seed"} {
		a, errA := first.Meta(ctx, key)
		b, errB := second.Meta(ctx, key)
		if errA != nil || errB != nil || !keyFormat.MatchString(a) || !keyFormat.MatchString(b) || a == b {
			t.Fatalf("fixtures did not get distinct valid %s values: %v, %v", key, errA, errB)
		}
	}

	// Keep each connection pinned so the next Conn call must use another one.
	for i := range 4 {
		conn, err := second.Conn(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		var got int
		if err := conn.QueryRowContext(ctx, `PRAGMA synchronous`).Scan(&got); err != nil || got != 2 {
			t.Fatalf("fixture connection %d: synchronous=%d, want FULL (2): %v", i+1, got, err)
		}
	}
}
