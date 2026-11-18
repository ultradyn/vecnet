package hub

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/hex"
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

// Check the fixture contract from the existing default-preference case rather
// than starting a separate scheduled process and migrating another template.
func checkTransferStoreIsolation(t *testing.T, first *transferTestEnv) {
	t.Helper()
	ctx := context.Background()
	a := first.Hub.DB
	b := newTransferTestEnv(t).Hub.DB
	// The first fixture already persisted an operator before the second opened.
	var exists int
	if err := b.QueryRowContext(ctx, `SELECT 1 FROM peers WHERE peer_id=?`, first.OperatorPeer).Scan(&exists); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("second transfer fixture did not isolate the first operator row: %v", err)
	}
	stat := func(db *store.DB) os.FileInfo {
		t.Helper()
		var path string
		if err := db.QueryRow(`SELECT file FROM pragma_database_list WHERE name='main'`).Scan(&path); err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		return info
	}
	if os.SameFile(stat(a), stat(b)) {
		t.Fatal("transfer fixtures share a database file")
	}
	readAuthority := func(db *store.DB, key string) []byte {
		t.Helper()
		v, err := db.Meta(ctx, key)
		decoded, decodeErr := hex.DecodeString(v)
		if err != nil || decodeErr != nil || len(decoded) != 32 {
			t.Fatalf("transfer fixture lacks a valid %s", key)
		}
		return decoded
	}
	for _, key := range []string{"hub_key", "hub_identity_seed"} {
		if bytes.Equal(readAuthority(a, key), readAuthority(b, key)) {
			t.Fatalf("transfer fixtures share %s", key)
		}
	}

	// Hold all four connections at once so Conn cannot reuse a checked one.
	for i := range 4 {
		conn, err := b.Conn(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		var got int
		if err := conn.QueryRowContext(ctx, `PRAGMA synchronous`).Scan(&got); err != nil || got != 2 {
			t.Fatalf("transfer connection %d: synchronous=%d, want FULL (2): %v", i+1, got, err)
		}
	}
}
