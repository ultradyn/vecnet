//go:build unix

package cli

import (
	"os"
	"path/filepath"
	"testing"
)

func newDefaultPrefixAliasFixture(t *testing.T) *bindFixture {
	t.Helper()
	f := newBindFixture(t)
	actual := filepath.Join(f.env["HOME"], "private-local-target")
	if err := cliFixtureMkdir(actual, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(actual, filepath.Join(f.env["HOME"], ".local")); err != nil {
		t.Fatal(err)
	}
	return f
}

func TestCodexBindDefaultPrefixAliasWithoutOverride(t *testing.T) {
	f := newDefaultPrefixAliasFixture(t)
	delete(f.env, "VECNET_CODEX_BINDINGS")
	f.bind("p", bindT1)
	if _, err := readCodexAuthority(f.path); err != nil {
		t.Fatal(err)
	}
}

func TestCodexBindingsPathDefaultPrefixAlias(t *testing.T) {
	f := newDefaultPrefixAliasFixture(t)
	testCodexBindingsPathDefaultAncestor(t, f)
	// Case alone selects a separate authority only when the filesystem gives
	// these existing directories different identities.
	other := filepath.Join(f.env["HOME"], ".local", "STATE", "vecnet", "codex", "bindings.json")
	if err := cliFixtureMkdirAll(filepath.Dir(other), 0700); err != nil {
		t.Fatal(err)
	}
	lower, err := os.Stat(filepath.Dir(f.path))
	if err != nil {
		t.Fatal(err)
	}
	upper, err := os.Stat(filepath.Dir(other))
	if err != nil {
		t.Fatal(err)
	}
	distinct := !os.SameFile(lower, upper)
	t.Logf("case-only authority directories distinct=%v", distinct)
	if distinct {
		f.env["VECNET_CODEX_BINDINGS"] = other
		if _, err := codexBindingsPath(func(k string) string { return f.env[k] }); err == nil {
			t.Fatal("case-distinct authority accepted")
		}
	}
}
