//go:build unix

package main

import (
	"os"
	"path/filepath"
	"testing"
)

// The data directory protects the plaintext signing keys in hub.db
// (PROTOCOL §12 "Secrets at rest"). MkdirAll keeps an existing directory's
// mode, so startup must tighten one that grants group or other access.
func TestOpenStoreMakesDataDirPrivate(t *testing.T) {
	for _, mode := range []os.FileMode{0o755, 0o777, 0o750, 0o700} {
		t.Run(mode.String(), func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "data")
			if err := os.Mkdir(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(dir, mode); err != nil {
				t.Fatal(err)
			}
			db, err := openStore(dir)
			if err != nil {
				t.Fatal(err)
			}
			db.Close()
			info, err := os.Stat(dir)
			if err != nil {
				t.Fatal(err)
			}
			if got := info.Mode().Perm(); got != 0o700 {
				t.Fatalf("data dir mode %v, want 0700", got)
			}
		})
	}
	// A new directory is created private.
	dir := filepath.Join(t.TempDir(), "new", "data")
	db, err := openStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	if info, err := os.Stat(dir); err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("new data dir: %v %v", info, err)
	}
	// A path that is not a directory is refused before SQLite opens it.
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if db, err := openStore(file); err == nil {
		db.Close()
		t.Fatal("a file was accepted as the data dir")
	}
}
