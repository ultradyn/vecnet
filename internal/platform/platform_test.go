package platform

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestMkdirTempCreatesPrivateUniqueDirectories(t *testing.T) {
	parent := t.TempDir()
	seen := map[string]bool{}
	for range 2 {
		dir, err := MkdirTemp(parent, "release-*-suffix")
		if err != nil {
			t.Fatal(err)
		}
		if filepath.Dir(dir) != parent || !strings.HasPrefix(filepath.Base(dir), "release-") || !strings.HasSuffix(dir, "-suffix") || seen[dir] {
			t.Fatalf("unexpected temporary directory %q", dir)
		}
		seen[dir] = true
		if err := CheckPath(dir, true, true); err != nil {
			t.Fatal(err)
		}
		f, err := OpenFile(filepath.Join(dir, "state"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			t.Fatal(err)
		}
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := MkdirTemp(parent, "missing/child"); err == nil {
		t.Fatal("directory separator in pattern accepted")
	}
}

func TestHomePrecedence(t *testing.T) {
	env := map[string]string{"HOME": "explicit", "USERPROFILE": "native"}
	get := func(k string) string { return env[k] }
	if Home(get) != "explicit" {
		t.Fatal("explicit HOME ignored")
	}
	delete(env, "HOME")
	want := ""
	if runtime.GOOS == "windows" {
		want = "native"
	}
	if Home(get) != want {
		t.Fatal("native fallback differs")
	}
	delete(env, "USERPROFILE")
	if Home(get) != "" {
		t.Fatal("missing environment used real user home")
	}
}
