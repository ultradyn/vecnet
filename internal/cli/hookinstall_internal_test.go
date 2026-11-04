package cli

import (
	"bytes"
	"encoding/json"
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
		if got := hookCommand(tc.bin, "p", "user-prompt", false, ""); got != tc.want {
			t.Errorf("command=%q, want %q", got, tc.want)
		}
	}
	if got := hookCommand("vecnet", "q", "post-tool", true, ""); got != "vecnet hook --profile q --event post-tool --auto-ack" {
		t.Fatal(got)
	}
}

func TestOursHookArgumentBoundaries(t *testing.T) {
	for _, command := range []string{
		"vecnet hook --profile p-other --event user-prompt --as peer_other",
		"vecnet hook --profile p --event user-prompt-extra --auto-ack --auto-ack",
		"vecnet hook --profile p --event post-tool --as peer_other",
		"vecnet hook --profile p --event user-prompt --profile q",
		"vecnet hook --profile p --event user-prompt --event post-tool",
		"vecnet hook --profile p --event user-prompt --unsupported",
		"vecnet hook --profile p --event user-prompt --as",
		"vecnet hook --profile p --event user-prompt --auto-ack=false",
		"vecnet hook --profile p --event user-prompt --help",
		"vecnet hook --profile p --event user-prompt && foreign",
		"vecnet hook install --profile p --event user-prompt",
		"vecnet echo hook --profile p --event user-prompt",
		"vecnet hook --profile p --event 'user-prompt",
		"C2C_CONFIG=/foreign/vecnet hook --profile p --event user-prompt",
		"C2C_CONFIG='/foreign/vecnet' hook --profile p --event user-prompt",
		"X=1 vecnet hook --profile p --event user-prompt",
		"vecnet hook --profile=p --event user-prompt",
		"vecnet hook --profile p --event user-prompt --as peer_1",
		"vecnet --net p hook --profile p --event user-prompt",
		" vecnet hook --profile p --event user-prompt",
		"'vecnet' hook --profile p --event user-prompt",
		"'/opt/it's/vecnet' hook --profile p --event user-prompt",
		"/opt/bin/vn hook --profile p --event user-prompt",
		"vecnet hook --profile p --event post-tool",
		"vecnet hook --profile q --event user-prompt",
	} {
		raw, err := json.Marshal(hookHandler{Type: "command", Command: command})
		if err != nil {
			t.Fatal(err)
		}
		handler, err := parseJSONNode(raw)
		if err != nil {
			t.Fatal(err)
		}
		if oursHook(handler, "vecnet", "p", "user-prompt") {
			t.Errorf("claimed foreign or invalid command: %q", command)
		}
	}
}

func TestOursHookExactForms(t *testing.T) {
	for _, tc := range []struct{ bin, command string }{
		{"vecnet", "vecnet hook --profile p --event user-prompt"},
		{"vecnet", "vecnet hook --profile p --event user-prompt --auto-ack"},
		{"vecnet", "/usr/local/bin/vecnet hook --profile p --event user-prompt"},
		{"vecnet", "/opt/vecnet.exe hook --profile p --event user-prompt"},
		{"vecnet", "'/opt/my tools/vecnet' hook --profile p --event user-prompt"},
		{"/opt/it's tools/vn", "'/opt/it'\"'\"'s tools/vn' hook --profile p --event user-prompt --auto-ack"},
		{"/opt/bin/vn", "/opt/bin/vn hook --profile p --event user-prompt"},
		{"vecnet", hookCommand("/opt/$(x);'\"/vecnet", "p", "user-prompt", true, "")},
	} {
		raw, err := json.Marshal(hookHandler{Type: "command", Command: tc.command})
		if err != nil {
			t.Fatal(err)
		}
		handler, err := parseJSONNode(raw)
		if err != nil {
			t.Fatal(err)
		}
		if !oursHook(handler, tc.bin, "p", "user-prompt") {
			t.Errorf("did not recognise exact form %q (bin %q)", tc.command, tc.bin)
		}
	}
}
