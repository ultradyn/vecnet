//go:build linux

package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLocalLegacyArchive(t *testing.T) {
	for _, kind := range []string{"normal", "verify fails", "initial drift", "source drift", "source symlink", "parent swap"} {
		t.Run(kind, func(t *testing.T) {
			home := localLegacyLockHome(t)
			parent := filepath.Join(home, "config")
			if e := os.Mkdir(parent, 0700); e != nil {
				t.Fatal(e)
			}
			path := filepath.Join(parent, "bindings.json")
			original := []byte("{\"version\":1,\"bindings\":[]}\n")
			if e := os.WriteFile(path, original, 0600); e != nil {
				t.Fatal(e)
			}
			victimDir := localLegacyLockHome(t)
			victim := filepath.Join(victimDir, "bindings.json")
			if e := os.WriteFile(victim, []byte("external"), 0600); e != nil {
				t.Fatal(e)
			}
			expected := original
			if kind == "initial drift" {
				expected = []byte("other")
			}
			called := false
			verify := func(context.Context) error {
				called = true
				switch kind {
				case "verify fails":
					return errors.New("active legacy")
				case "source drift":
					return os.WriteFile(path, []byte("changed"), 0600)
				case "source symlink":
					if e := os.Remove(path); e != nil {
						return e
					}
					return os.Symlink(victim, path)
				case "parent swap":
					if e := os.Rename(parent, parent+".old"); e != nil {
						return e
					}
					return os.Symlink(victimDir, parent)
				}
				return nil
			}
			err := archiveLegacySourceWith(context.Background(), home, path, expected, verify)
			actualParent := parent
			if kind == "parent swap" && called {
				actualParent = parent + ".old"
			}
			backups, e := filepath.Glob(filepath.Join(actualParent, "bindings.json.retired-*"))
			if e != nil {
				t.Fatal(e)
			}
			if kind == "normal" {
				if err != nil {
					t.Fatal(err)
				}
				if !called {
					t.Fatal("verification omitted")
				}
				if _, e := os.Lstat(path); !errors.Is(e, os.ErrNotExist) {
					t.Fatalf("source remains: %v", e)
				}
				if len(backups) != 1 {
					t.Fatalf("backups = %v", backups)
				}
				raw, e := os.ReadFile(backups[0])
				if e != nil || string(raw) != string(original) {
					t.Fatalf("backup lost original: %q %v", raw, e)
				}
			} else {
				if err == nil {
					t.Fatal("unsafe archive accepted")
				}
				if len(backups) != 0 {
					t.Fatal("archived after failed proof")
				}
				if kind == "initial drift" && called {
					t.Fatal("verified changed source")
				}
				if kind == "parent swap" && !strings.Contains(err.Error(), "parent") {
					t.Fatal(err)
				}
			}
			raw, e := os.ReadFile(victim)
			if e != nil || string(raw) != "external" {
				t.Fatalf("external source touched: %q %v", raw, e)
			}
			externalBackups, _ := filepath.Glob(filepath.Join(victimDir, "bindings.json.retired-*"))
			if len(externalBackups) > 0 {
				t.Fatal("archived through swapped parent")
			}
		})
	}
}
