package store_test

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/XertroV/vecnet/internal/store"
)

func pragmaInt(t *testing.T, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, name string) int64 {
	t.Helper()
	var n int64
	if err := q.QueryRowContext(context.Background(), "PRAGMA "+name).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestCompactionNewDatabaseAndEveryReopenedConnection(t *testing.T) {
	t.Setenv("VECNET_SQLITE_SYNCHRONOUS", "full")
	path := filepath.Join(t.TempDir(), "hub.db")
	// An existing empty file is still a new SQLite database.
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	for pass := 0; pass < 2; pass++ {
		db, err := store.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		var connections []*sql.Conn
		for i := 0; i < 4; i++ {
			c, err := db.Conn(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			connections = append(connections, c)
			for name, want := range map[string]int64{"page_size": 4096, "auto_vacuum": 2, "synchronous": 2, "wal_autocheckpoint": 1000, "foreign_keys": 1} {
				if got := pragmaInt(t, c, name); got != want {
					t.Errorf("pass=%d connection=%d %s=%d want %d", pass, i, name, got, want)
				}
			}
			var mode string
			if err := c.QueryRowContext(context.Background(), "PRAGMA journal_mode").Scan(&mode); err != nil || mode != "wal" {
				t.Fatalf("journal_mode=%s: %v", mode, err)
			}
		}
		for _, c := range connections {
			c.Close()
		}
		db.Close()
	}
}

func TestCompactionExistingNoneAndBackupKeepPageSize(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	raw, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{"PRAGMA page_size=8192", "PRAGMA auto_vacuum=NONE", "CREATE TABLE legacy(value TEXT)", "INSERT INTO legacy VALUES ('preserved')"} {
		if _, err := raw.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	raw.Close()
	db, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := pragmaInt(t, db, "auto_vacuum"); got != 0 {
		t.Fatalf("existing auto_vacuum=%d", got)
	}
	if got := pragmaInt(t, db, "page_size"); got != 8192 {
		t.Fatalf("existing page_size=%d", got)
	}
	// Quiesced DB+WAL backup: closing the only handle checkpoints its WAL.
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	backup := filepath.Join(t.TempDir(), "restored.db")
	if err := os.WriteFile(backup, b, 0600); err != nil {
		t.Fatal(err)
	}
	restored, err := store.Open(backup)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	if got := pragmaInt(t, restored, "auto_vacuum"); got != 0 {
		t.Fatalf("restored auto_vacuum=%d", got)
	}
	if got := pragmaInt(t, restored, "page_size"); got != 8192 {
		t.Fatalf("restored page_size=%d", got)
	}
	var value string
	if err := restored.QueryRow("SELECT value FROM legacy").Scan(&value); err != nil || value != "preserved" {
		t.Fatalf("restored fixture: %v", err)
	}
}
