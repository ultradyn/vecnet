//go:build windows

package cli

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/platform"
)

func TestCLIWindowsFixturePreservesCurrentBinaryTrust(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(exe)
	if err != nil {
		t.Fatal(err)
	}
	// On Windows os.Stat can defer reading the file ID until SameFile.
	// Capture the original's ID from an open handle before creating aliases.
	original, err := f.Stat()
	closeErr := f.Close()
	if err != nil {
		t.Fatal(err)
	}
	if closeErr != nil {
		t.Fatal(closeErr)
	}
	checkOriginal := func(t *testing.T) {
		t.Helper()
		if err := platform.CheckPath(exe, false, false); err != nil {
			t.Fatalf("original executable lost single-link trust: %v", err)
		}
		info, err := os.Stat(exe)
		if err != nil {
			t.Fatal(err)
		}
		if !os.SameFile(original, info) {
			t.Fatal("original executable identity changed")
		}
	}
	checkOriginal(t)

	var fixtures []string
	var snapshotPath string
	var snapshot os.FileInfo
	t.Run("fixtures alive", func(t *testing.T) {
		for _, name := range []string{"first", "two words", "third", "fourth"} {
			dir := t.TempDir()
			fixture := filepath.Join(dir, name+".exe")
			log := filepath.Join(dir, "argv.json")
			fixtures = append(fixtures, fixture)
			cliFixtureProgram(t, dir, name, "", cliFixtureProgramSpec{Kind: "argv", Log: log})
			checkOriginal(t)

			info, err := os.Stat(fixture)
			if err != nil {
				t.Fatal(err)
			}
			if os.SameFile(original, info) {
				t.Error("fixture aliases the running executable")
			}
			if snapshot == nil {
				snapshotPath, err = cliFixtureSnapshot()
				if err != nil {
					t.Fatal(err)
				}
				snapshot, err = os.Stat(snapshotPath)
				if err != nil {
					t.Fatal(err)
				}
			}
			// Distinct executable copies pay the cold image cost for every name.
			if !os.SameFile(snapshot, info) {
				t.Error("fixture did not reuse the independent helper image")
			}
			if err := platform.PrivateFile(fixture + ".cli-fixture.json"); err != nil {
				t.Errorf("fixture sidecar is not private: %v", err)
			}
		}
		if _, err := launcherCurrentBinary(); err != nil {
			t.Errorf("fixtures invalidated current executable: %v", err)
		}

		// Run two names after all sidecars exist. Each must use its own
		// sidecar, preserving the quoted argument and separate output path.
		for _, fixture := range fixtures[:2] {
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			arg := "literal " + filepath.Base(fixture)
			out, err := exec.CommandContext(ctx, fixture, arg).CombinedOutput()
			cancel()
			if err != nil {
				t.Fatalf("synthetic fixture: %v: %s", err, out)
			}
			raw, err := os.ReadFile(filepath.Join(filepath.Dir(fixture), "argv.json"))
			if err != nil {
				t.Fatal(err)
			}
			var args []string
			if err := json.Unmarshal(raw, &args); err != nil {
				t.Fatal(err)
			}
			if len(args) != 1 || args[0] != arg {
				t.Fatalf("fixture argv = %q, want [%q]", args, arg)
			}
		}
	})
	for _, fixture := range fixtures {
		for _, path := range []string{fixture, fixture + ".cli-fixture.json"} {
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Errorf("fixture cleanup %s: want absence, got %v", path, err)
			}
		}
	}
	if snapshot != nil {
		// The process-owned image survives t.TempDir cleanup, but none of
		// the aliases should. PrivateFile checks both ACL and single link.
		if err := platform.PrivateFile(snapshotPath); err != nil {
			t.Errorf("snapshot after alias cleanup: %v", err)
		}
	}
	checkOriginal(t)
}
