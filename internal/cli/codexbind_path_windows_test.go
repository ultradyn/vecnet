//go:build windows

package cli

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestCodexBindingsPathMixedCaseDefaultAncestor(t *testing.T) {
	f := newBindFixture(t)
	actual := filepath.Join(f.env["HOME"], ".LOCAL")
	if err := cliFixtureMkdir(actual, 0700); err != nil {
		t.Fatal(err)
	}
	upper, err := os.Stat(actual)
	if err != nil {
		t.Fatal(err)
	}
	lowerPath := filepath.Join(f.env["HOME"], ".local")
	lower, err := os.Stat(lowerPath)
	distinct := errors.Is(err, os.ErrNotExist)
	if distinct {
		// Case-sensitive Windows directories must retain separate authorities.
		if err := cliFixtureMkdir(lowerPath, 0700); err != nil {
			t.Fatal(err)
		}
	} else if err != nil || !os.SameFile(upper, lower) {
		t.Fatalf("cannot identify mixed-case directory: %v", err)
	}
	testCodexBindingsPathDefaultAncestor(t, f)
	if distinct {
		f.env["VECNET_CODEX_BINDINGS"] = filepath.Join(actual, "state", "vecnet", "codex", "bindings.json")
		if _, err := codexBindingsPath(func(k string) string { return f.env[k] }); err == nil {
			t.Fatal("case-distinct Windows authority accepted")
		}
	}
}
