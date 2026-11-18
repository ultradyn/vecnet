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
	if err := platform.CheckPath(exe, false, false); err != nil {
		t.Fatalf("original executable before fixture: %v", err)
	}
	var fixture string
	t.Run("fixture alive", func(t *testing.T) {
		dir := t.TempDir()
		fixture = filepath.Join(dir, "fixture.exe")
		log := filepath.Join(dir, "argv.json")
		cliFixtureProgram(t, dir, "fixture", "", cliFixtureProgramSpec{Kind: "argv", Log: log})

		// A hardlinked fixture makes the shared running executable fail this
		// check even in another packet process. Keep the fixture alive here.
		if _, err := launcherCurrentBinary(); err != nil {
			t.Errorf("fixture invalidated current executable: %v", err)
		}
		original, err := os.Stat(exe)
		if err != nil {
			t.Fatal(err)
		}
		copyInfo, err := os.Stat(fixture)
		if err != nil {
			t.Fatal(err)
		}
		if os.SameFile(original, copyInfo) {
			t.Error("fixture aliases the running executable")
		}
		if err := platform.CheckPath(fixture, false, false); err != nil {
			t.Errorf("fixture must remain a trusted single-link file: %v", err)
		}

		// Exercise the real sidecar-driven subprocess, not just copy metadata.
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if out, err := exec.CommandContext(ctx, fixture, "two words").CombinedOutput(); err != nil {
			t.Fatalf("synthetic fixture: %v: %s", err, out)
		}
		raw, err := os.ReadFile(log)
		if err != nil {
			t.Fatal(err)
		}
		var args []string
		if err := json.Unmarshal(raw, &args); err != nil {
			t.Fatal(err)
		}
		if len(args) != 1 || args[0] != "two words" {
			t.Fatalf("fixture argv = %q", args)
		}
	})
	for _, path := range []string{fixture, fixture + ".cli-fixture.json"} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("fixture cleanup %s: want absence, got %v", path, err)
		}
	}
	if err := platform.CheckPath(exe, false, false); err != nil {
		t.Errorf("original executable after cleanup: %v", err)
	}
}
