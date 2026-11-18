package hub

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/XertroV/vecnet/internal/store"
)

// Cache only a closed, checkpointed empty store, never a hub or its data.
// Build with explicit FULL so the template does not depend on the test env.
var transferStoreImage = sync.OnceValues(func() (image []byte, err error) {
	dir, err := os.MkdirTemp("", "vecnet-transfer-image-")
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
	if err := errors.Join(checkpointErr, closeErr); err != nil {
		return nil, err
	}
	if busy != 0 {
		return nil, fmt.Errorf("transfer store image WAL checkpoint busy: %d of %d pages checkpointed", checkpointed, logPages)
	}
	return os.ReadFile(path)
})

// Open an independent logical transfer fixture, not a first-migration oracle.
// store.Open still selects the caller's env policy (FULL by default), configures
// the real pool and checks the migrations. The caller owns the private path.
func openTransferFixtureStore(t *testing.T, path string) *store.DB {
	t.Helper()
	image, err := transferStoreImage()
	if err != nil {
		t.Fatalf("build transfer store image: %v", err)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_, writeErr := file.Write(image)
	if err := errors.Join(writeErr, file.Close()); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	// Restore independent signing/sealing authority before constructing the hub.
	if _, err := db.Exec(`UPDATE meta SET value=lower(hex(randomblob(32))) WHERE key IN ('hub_key','hub_identity_seed')`); err != nil {
		t.Fatal(err)
	}
	return db
}
