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
	actual := filepath.Join(f.env["HOME"], ".LOCAL")
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
	// Both existing names are distinct on a case-sensitive filesystem. A
	// blanket case fold would wrongly admit this separate authority.
	other := filepath.Join(f.env["HOME"], ".local", "STATE", "vecnet", "codex", "bindings.json")
	if err := cliFixtureMkdirAll(filepath.Dir(other), 0700); err != nil {
		t.Fatal(err)
	}
	f.env["VECNET_CODEX_BINDINGS"] = other
	if _, err := codexBindingsPath(func(k string) string { return f.env[k] }); err == nil {
		t.Fatal("case-distinct authority accepted")
	}
}
