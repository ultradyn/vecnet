package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func migrationRun(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	var out, errs bytes.Buffer
	a := &app{ctx: context.Background(), stdout: &out, stderr: &errs, getenv: func(string) string { return "" }}
	err := a.cmdHookInstall(append([]string{"--client", "codex", "--bin", "vecnet"}, args...))
	return out.String(), errs.String(), err
}

func TestCodexHookMigrationOwnedAndCustom(t *testing.T) {
	src := []byte(`{"number":1e400,"literal":"\u0061","hooks":{"UserPromptSubmit":[{"hooks":[{"type":"command","command":"/old/vecnet hook --profile p --event user-prompt --mcp vecnet-p","timeout":5},{"type":"command","command":"vecnet hook --profile q --event user-prompt --auto-ack","timeout":5},{"type":"command","command":"vecnet hook --profile p --event user-prompt && custom","timeout":5},{"type":"command","command":"vecnet hook --profile p --event user-prompt","timeout":99}]},{"matcher":"custom","hooks":[{"type":"command","command":"vecnet hook --profile p --event user-prompt","timeout":5}]}]}}`)
	path := filepath.Join(t.TempDir(), "hooks.json")
	if err := cliFixtureWriteFile(path, src, 0640); err != nil {
		t.Fatal(err)
	}
	out, _, err := migrationRun(t, "--settings", path, "--dry-run")
	if err != nil || !strings.Contains(out, "--- ") || !strings.Contains(out, "+++ ") {
		t.Fatalf("%s: %v", out, err)
	}
	got, _ := os.ReadFile(path)
	if !bytes.Equal(src, got) {
		t.Fatal("preview mutated hooks")
	}
	_, _, err = migrationRun(t, "--settings", path, "--write")
	if err != nil {
		t.Fatal(err)
	}
	got, _ = os.ReadFile(path)
	for _, s := range []string{"1e400", `\u0061`, "&& custom", `"timeout": 99`, `"matcher": "custom"`} {
		if !strings.Contains(string(got), s) {
			t.Fatalf("lost %s: %s", s, got)
		}
	}
	for _, s := range []string{"--mcp", "--auto-ack"} {
		if bytes.Contains(got, []byte(s)) {
			t.Fatalf("legacy survived: %s", got)
		}
	}
	if strings.Count(string(got), "--client codex") != 3 {
		t.Fatalf("%s", got)
	}
	_, _, err = migrationRun(t, "--settings", path, "--write")
	if err != nil {
		t.Fatal(err)
	}
	again, _ := os.ReadFile(path)
	if !bytes.Equal(got, again) {
		t.Fatal("not idempotent")
	}
	backups, _ := filepath.Glob(path + ".vecnet-backup-*")
	if len(backups) != 1 {
		t.Fatalf("backups=%v", backups)
	}
	// Rollback refuses drift, even though the backup itself remains valid.
	if err := cliFixtureWriteFile(path, append(got, ' '), 0640); err != nil {
		t.Fatal(err)
	}
	if _, _, err := migrationRun(t, "--settings", path, "--rollback", backups[0], "--write"); err == nil || !strings.Contains(err.Error(), "drift") {
		t.Fatalf("rollback drift: %v", err)
	}
	if err := cliFixtureWriteFile(path, got, 0640); err != nil {
		t.Fatal(err)
	}
	if _, _, err := migrationRun(t, "--settings", path, "--rollback", backups[0], "--write"); err != nil {
		t.Fatal(err)
	}
	restored, _ := os.ReadFile(path)
	if !bytes.Equal(src, restored) {
		t.Fatalf("restore=%s", restored)
	}
	cliFixtureRequireMode(t, path, 0640)
}

func TestCodexHookMigrationRollbackNewFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hooks.json")
	if _, _, err := migrationRun(t, "--settings", path, "--write"); err != nil {
		t.Fatal(err)
	}
	backups, _ := filepath.Glob(path + ".vecnet-backup-*")
	if len(backups) != 1 {
		t.Fatal(backups)
	}
	if _, _, err := migrationRun(t, "--settings", path, "--rollback", backups[0]); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal("rollback preview removed file")
	}
	if _, _, err := migrationRun(t, "--settings", path, "--rollback", backups[0], "--write"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal(err)
	}
}

func TestCodexHookMigrationRefusesMalformedJSON(t *testing.T) {
	for _, src := range []string{`{"hooks":{},"hooks":{}}`, `{"hooks":[]}`, `{"hooks":{"PostToolUse":{}}}`} {
		path := filepath.Join(t.TempDir(), "hooks.json")
		_ = cliFixtureWriteFile(path, []byte(src), 0600)
		if _, _, err := migrationRun(t, "--settings", path, "--write"); err == nil {
			t.Fatal("accepted", src)
		}
		got, _ := os.ReadFile(path)
		if string(got) != src {
			t.Fatal("mutated invalid JSON")
		}
	}
}

func TestCodexHookMigrationPreservesCustomizedHandlerFields(t *testing.T) {
	for _, extra := range []string{`,"async":true`, `,"custom":1`, `,"additionalContextLimit":123`} {
		src := []byte(`{"hooks":{"UserPromptSubmit":[{"hooks":[{"type":"command","command":"vecnet hook --profile p --event user-prompt","timeout":5` + extra + `}]}]}}`)
		got, _, err := mergeCodexHooks(src, "vecnet")
		if err != nil {
			t.Fatal(err)
		}
		var parsed any
		if json.Unmarshal(got, &parsed) != nil {
			t.Fatal("invalid JSON")
		}
		if !bytes.Contains(got, []byte("--profile p")) {
			t.Fatalf("overwrote custom: %s", got)
		}
	}
}

func TestCodexHookMigrationExactLegacyForms(t *testing.T) {
	for _, tc := range []struct {
		command string
		owned   bool
	}{
		{"vecnet hook --profile p --event user-prompt --mcp vecnet-p", true},
		{"'/opt/my tools/vecnet' hook --profile p --event user-prompt --auto-ack --mcp vecnet-p", true},
		{"vecnet hook --profile p --event user-prompt --mcp vecnet-other", false},
		{"vecnet hook --profile p --event user-prompt --mcp", false},
		{"vecnet hook --profile p --event post-tool", false},
		{"vecnet hook --profile p --event user-prompt; foreign", false},
		{"X=1 vecnet hook --profile p --event user-prompt", false},
		{"'vecnet' hook --profile p --event user-prompt", false},
		{"/opt/not-vecnet hook --profile p --event user-prompt", false},
	} {
		raw, _ := json.Marshal(hookHandler{Type: "command", Command: tc.command, Timeout: 5})
		handler, err := parseJSONNode(raw)
		if err != nil {
			t.Fatal(err)
		}
		if got := codexOwnedHandler(handler, "vecnet", "user-prompt"); got != tc.owned {
			t.Errorf("owned(%s)=%v want %v", tc.command, got, tc.owned)
		}
	}
}
