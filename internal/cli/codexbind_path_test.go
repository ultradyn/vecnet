package cli

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCodexBindingsPathUsesCanonicalHomeIdentity(t *testing.T) {
	home := t.TempDir()
	alias := filepath.Join(t.TempDir(), "home alias")
	if err := cliFixtureSymlink(t, home, alias); err != nil {
		t.Fatal(err)
	}
	canonical, err := canonicalCodexPath(home)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(canonical, ".local", "state", "vecnet", "codex", "bindings.json")
	env := map[string]string{"HOME": alias, "USERPROFILE": t.TempDir()}
	get := func(k string) string { return env[k] }
	// The alias must identify the same authority both before and after creation.
	for _, existing := range []bool{false, true} {
		if existing {
			if err := cliFixtureMkdirAll(filepath.Dir(want), 0700); err != nil {
				t.Fatal(err)
			}
			if err := cliFixtureWriteFile(want, []byte("{}"), 0600); err != nil {
				t.Fatal(err)
			}
		}
		env["VECNET_CODEX_BINDINGS"] = filepath.Join(alias, ".local", "state", "vecnet", "codex", "bindings.json")
		if got, err := codexBindingsPath(get); err != nil || got != want {
			t.Fatalf("existing=%v: path=%q err=%v, want %q", existing, got, err, want)
		}
		for _, other := range []string{filepath.Join(env["USERPROFILE"], "bindings.json"), "bindings.json"} {
			env["VECNET_CODEX_BINDINGS"] = other
			if _, err := codexBindingsPath(get); err == nil {
				t.Fatalf("separate authority %q accepted", other)
			}
		}
	}
	if _, err := os.Stat(want); err != nil {
		t.Fatal(err)
	}
}

func testCodexBindingsPathDefaultAncestor(t *testing.T, f *bindFixture) {
	t.Helper()
	home := f.env["HOME"]
	alias := filepath.Join(t.TempDir(), "home alias")
	if err := cliFixtureSymlink(t, home, alias); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(t.TempDir(), "bindings.json")
	for _, existing := range []bool{false, true} {
		for _, h := range []string{home, alias} {
			f.env["HOME"] = h
			for _, override := range []string{"", f.path, filepath.Join(alias, ".local", "state", "vecnet", "codex", "bindings.json")} {
				f.env["VECNET_CODEX_BINDINGS"] = override
				got, err := codexBindingsPath(func(k string) string { return f.env[k] })
				if err != nil || got != f.path {
					t.Fatalf("existing=%v HOME=%q override=%q: path=%q err=%v, want default %q", existing, h, override, got, err, f.path)
				}
			}
			for _, override := range []string{other, "bindings.json"} {
				f.env["VECNET_CODEX_BINDINGS"] = override
				if _, err := codexBindingsPath(func(k string) string { return f.env[k] }); err == nil {
					t.Fatalf("separate authority %q accepted", override)
				}
			}
		}
		f.env["HOME"], f.env["VECNET_CODEX_BINDINGS"] = home, f.path
		f.bind("p", bindT1)
		if _, err := readCodexAuthority(f.path); err != nil {
			t.Fatal(err)
		}
	}
}
