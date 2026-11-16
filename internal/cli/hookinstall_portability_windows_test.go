//go:build windows

package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/platform"
)

// Execute the actual installed Codex stanza in native PowerShell. The private
// fake executable records argv, so spaces, apostrophes and metacharacters must
// remain one literal executable path rather than a second shell command.
func TestCodexHookWindowsInstalledCommandPreservesArgv(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "agent tools & literal $value")
	if err := cliFixtureMkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(dir, "argv.json")
	name := "it's vecnet.exe"
	cliFixtureProgram(t, dir, name, "", cliFixtureProgramSpec{Kind: "argv", Log: log})
	bin := filepath.Join(dir, name)
	raw, changed, err := mergeCodexHooks(nil, bin)
	if err != nil || !changed {
		t.Fatalf("install: changed=%t err=%v", changed, err)
	}
	again, changed, err := mergeCodexHooks(raw, bin)
	if err != nil || changed || !bytes.Equal(raw, again) {
		t.Fatalf("not idempotent: changed=%t err=%v", changed, err)
	}
	var file struct{ Hooks map[string][]hookGroup }
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatal(err)
	}
	for _, event := range codexHookEvents {
		groups := file.Hooks[event.key]
		if len(groups) != 1 || len(groups[0].Hooks) != 1 {
			t.Fatalf("%s: %+v", event.key, groups)
		}
		command := groups[0].Hooks[0].Command
		want := "& '" + strings.ReplaceAll(bin, "'", "''") + "' hook --client codex --event " + event.name
		if command != want {
			t.Fatalf("command=%q want=%q", command, want)
		}
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		cmd := exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-Command", command)
		platform.Background(cmd)
		out, err := cmd.CombinedOutput()
		cancel()
		if err != nil {
			t.Fatalf("PowerShell: %v: %s", err, out)
		}
		raw, err := os.ReadFile(log)
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatal(err)
		}
		args := []string{"hook", "--client", "codex", "--event", event.name}
		if !reflect.DeepEqual(got, args) {
			t.Fatalf("argv=%q want=%q", got, args)
		}
	}
	// Claude Code still uses Bash; it must not acquire PowerShell's call operator.
	claude := hookCommand(bin, "p", "user-prompt", false, "")
	if strings.HasPrefix(claude, "& ") || !strings.Contains(claude, "'\"'\"'") {
		t.Fatalf("Claude command lost POSIX quoting: %q", claude)
	}
}

// The Windows clipboard boundary must carry all Unicode text in UTF-16LE, not
// console-code-page bytes. The fake tool validates the BOM and decodes stdin.
func TestClipboardWindowsUTF16PreservesUnicode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "clipboard.txt")
	cliFixtureProgram(t, dir, "clip.exe", "", cliFixtureProgramSpec{Kind: "clipboard", Log: path})
	text := "R\u00e9sum\u00e9 \u4e16\u754c \U0001F680\r\nexact invitation\n"
	tool, err := copyClipboard(t.Context(), func(k string) string {
		if k == "PATH" {
			return dir
		}
		return ""
	}, text)
	if err != nil || tool != "clip.exe" {
		t.Fatalf("clipboard: %s %v", tool, err)
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != text {
		t.Fatalf("clipboard Unicode changed: %q %v", got, err)
	}
}
