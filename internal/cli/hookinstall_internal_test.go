package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseEncodeRoundTrip(t *testing.T) {
	files, err := filepath.Glob("testdata/settings/*.json")
	if err != nil || len(files) == 0 {
		t.Fatalf("fixtures=%v: %v", files, err)
	}
	for _, file := range files {
		t.Run(filepath.Base(file), func(t *testing.T) {
			src, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			node, err := parseJSONNode(src)
			if err != nil {
				t.Fatal(err)
			}
			if got := node.encode(detectIndent(src)); !bytes.Equal(got, src) {
				t.Fatalf("round trip:\n%s\nwant:\n%s", got, src)
			}
		})
	}
	for _, src := range []string{"{} {}", "{} junk", `{"a":1,}`, `{"a" 1}`, "[1,]"} {
		if _, err := parseJSONNode([]byte(src)); err == nil {
			t.Errorf("accepted %q", src)
		}
	}
	for _, src := range []string{"1e400", "1e400\n", "{}", "[]\n", "{\n  \"a\": 1.0,\n  \"b\": [\n    true,\n    null,\n    \"\\u00e9\"\n  ]\n}"} {
		node, err := parseJSONNode([]byte(src))
		if err != nil {
			t.Fatalf("parse %q: %v", src, err)
		}
		if got := string(node.encode(detectIndent([]byte(src)))); got != src {
			t.Errorf("got=%q want=%q", got, src)
		}
	}
}

func TestHookInstallRefusesTempBinary(t *testing.T) {
	for _, tc := range []struct{ exe, want, warning string }{
		{filepath.Join(os.TempDir(), "go-build123", "exe", "vecnet"), "vecnet", "temporary"},
		{"/opt/project/.worktrees/x/bin/vecnet", "/opt/project/.worktrees/x/bin/vecnet", "worktree"},
		{"/opt/bin/vecnet", "/opt/bin/vecnet", ""},
		{"/opt/bin/other", "vecnet", ""},
		{filepath.Clean(os.TempDir()) + "-other/vecnet", filepath.Clean(os.TempDir()) + "-other/vecnet", ""},
	} {
		got, warning := defaultBin(tc.exe)
		if got != tc.want || (tc.warning == "" && warning != "") || (tc.warning != "" && !strings.Contains(warning, tc.warning)) {
			t.Errorf("defaultBin(%q)=(%q,%q), want (%q, %q)", tc.exe, got, warning, tc.want, tc.warning)
		}
	}
}

func TestHookInstallCommandQuotesBinary(t *testing.T) {
	for _, tc := range []struct{ bin, want string }{
		{"/opt/bin/vecnet", "/opt/bin/vecnet hook --profile p --event user-prompt"},
		{"/opt/my tools/vecnet", "'/opt/my tools/vecnet' hook --profile p --event user-prompt"},
		{"/opt/it's/vecnet", "'/opt/it'\"'\"'s/vecnet' hook --profile p --event user-prompt"},
		{"$(unsafe)/vecnet", "'$(unsafe)/vecnet' hook --profile p --event user-prompt"},
	} {
		if got := hookCommand(tc.bin, "p", "user-prompt", false); got != tc.want {
			t.Errorf("command=%q, want %q", got, tc.want)
		}
	}
	if got := hookCommand("vecnet", "q", "post-tool", true); got != "vecnet hook --profile q --event post-tool --auto-ack" {
		t.Fatal(got)
	}
}
