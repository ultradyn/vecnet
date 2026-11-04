package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEditProfilePreservesCommentsAndUnknownKeys(t *testing.T) {
	original := "# preserve\nunknown = \"future\"\n[profiles.first]\nhub = \"https://first.example\"\nsecret = \"one\"\n[profiles.\"a.b\"] # dotted name\n  hub = \"https://old.example\" # hub comment\nsecret = \"two\"\nfuture = 9\n[profiles.last]\nhub = \"https://last.example\"\nsecret = \"three\"\n"
	path, _ := pinTestEnv(t, original)
	if e := editProfiles(path, map[string]map[string]string{"a.b": {"hub": "https://new.example", "hub_pubkey": "key"}}); e != nil {
		t.Fatal(e)
	}
	want := strings.Replace(original, "  hub = \"https://old.example\" # hub comment", "  hub = \"https://new.example\" # hub comment", 1)
	want = strings.Replace(want, "[profiles.last]", "hub_pubkey = \"key\"\n[profiles.last]", 1)
	if got := string(pinRead(t, path)); got != want {
		t.Fatalf("line diff:\ngot %q\nwant %q", got, want)
	}
	if string(pinRead(t, path+".bak")) != original {
		t.Fatal("backup differs")
	}
	for _, p := range []string{path, path + ".bak"} {
		info, e := os.Stat(p)
		if e != nil || info.Mode().Perm() != 0600 {
			t.Fatalf("mode: %v %v", info, e)
		}
	}
}
func TestEditProfileRefusesUnsafe(t *testing.T) {
	for _, body := range []string{
		`profiles = { p = { hub = "https://old.example", secret = "s" } }`,
		"[profiles.other]\nhub = \"https://old.example\"\nsecret = \"s\"\n",
		"[profiles.p]\nhub = \"\"\"https://old.example\n/path\"\"\"\nsecret = \"s\"\n",
	} {
		t.Run(body, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.toml")
			if e := os.WriteFile(path, []byte(body), 0600); e != nil {
				t.Fatal(e)
			}
			if e := editProfiles(path, map[string]map[string]string{"p": {"hub": "https://new.example"}}); e == nil {
				t.Fatal("unsafe edit accepted")
			}
			if string(pinRead(t, path)) != body {
				t.Fatal("unsafe edit changed bytes")
			}
			if _, e := os.Stat(path + ".bak"); !os.IsNotExist(e) {
				t.Fatalf("backup created: %v", e)
			}
		})
	}
}
