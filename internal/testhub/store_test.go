package testhub_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/XertroV/vecnet/internal/testhub"
)

// Catches shared fixture state/identity, missing migrations and a non-NORMAL
// connection returned to the pool after copying the closed database image.
func TestFreshStoreIsIndependentAndNormal(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	var keys, seeds []string
	for range 2 {
		db := testhub.FreshStore(t, filepath.Join(t.TempDir(), "hub.db"))
		for _, key := range []string{"hub_key", "hub_identity_seed"} {
			value, err := db.Meta(ctx, key)
			if err != nil {
				t.Fatal(err)
			}
			if key == "hub_key" {
				keys = append(keys, value)
			} else {
				seeds = append(seeds, value)
			}
		}
		var count int
		if err := db.QueryRow(`SELECT count(*) FROM hub_preferences_state WHERE revision=0`).Scan(&count); err != nil || count != 1 {
			t.Fatalf("fresh preference state: %d %v", count, err)
		}
		var conns []*sql.Conn
		for range 4 {
			conn, err := db.Conn(ctx)
			if err != nil {
				t.Fatal(err)
			}
			conns = append(conns, conn)
			var sync, fk int
			var mode string
			if err := conn.QueryRowContext(ctx, `PRAGMA synchronous`).Scan(&sync); err != nil || sync != 1 {
				t.Fatalf("disposable NORMAL: %d %v", sync, err)
			}
			if err := conn.QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&fk); err != nil || fk != 1 {
				t.Fatalf("foreign_keys: %d %v", fk, err)
			}
			if err := conn.QueryRowContext(ctx, `PRAGMA journal_mode`).Scan(&mode); err != nil || mode != "wal" {
				t.Fatalf("WAL: %s %v", mode, err)
			}
		}
		for _, conn := range conns {
			conn.Close()
		}
		if _, err := db.Exec(`UPDATE hub_preferences_state SET revision=42`); err != nil {
			t.Fatal(err)
		}
	}
	if keys[0] == keys[1] || seeds[0] == seeds[1] {
		t.Fatal("independent stores shared identity material")
	}
}

func TestFreshStoreParallelIsolation(t *testing.T) {
	for i := range 8 {
		t.Run(string(rune('a'+i)), func(t *testing.T) {
			t.Parallel()
			db := testhub.FreshStore(t, filepath.Join(t.TempDir(), "parallel.db"))
			var n int
			if err := db.QueryRow(`SELECT count(*) FROM meta WHERE key='parallel_marker'`).Scan(&n); err != nil || n != 0 {
				t.Fatalf("shared state: %d %v", n, err)
			}
			if err := db.SetMeta(context.Background(), "parallel_marker", t.Name()); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// Reopening a fixture must not silently restore FULL and its per-commit cost.
// Production Open's default is asserted independently by internal/store tests.
func TestFixtureReopenAndCloneKeepExplicitPolicy(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "hub.db")
	db := testhub.FreshStore(t, path)
	if err := db.SetMeta(context.Background(), "reopen_marker", "kept"); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	reopened := testhub.OpenFixtureStore(t, path)
	value, err := reopened.Meta(context.Background(), "reopen_marker")
	if err != nil || value != "kept" {
		t.Fatalf("reopen lost state: %s %v", value, err)
	}
	env := testhub.Start(t)
	clone := env.CloneDB(t)
	for _, db := range []*sql.DB{reopened.DB, env.Hub.DB.DB, clone.Hub.DB.DB} {
		var got int
		if err := db.QueryRow(`PRAGMA synchronous`).Scan(&got); err != nil || got != 1 {
			t.Fatalf("fixture policy reverted: %d %v", got, err)
		}
	}
}
