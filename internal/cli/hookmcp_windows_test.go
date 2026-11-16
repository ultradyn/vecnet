//go:build windows

package cli

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
)

func TestWindowsClaudeNpmRegistrationPreservesArgv(t *testing.T) {
	dir := t.TempDir()
	entry := filepath.Join(dir, "node_modules", "@anthropic-ai", "claude-code", "cli.js")
	if e := os.MkdirAll(filepath.Dir(entry), 0700); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(entry, []byte(`process.stdout.write(JSON.stringify(process.argv.slice(2)))`), 0600); e != nil {
		t.Fatal(e)
	}
	args := []string{"mcp", "add", "--env", `VECNET_CONFIG=C:\space & quote's\config.toml`, "--", "vecnet", "--net", "%PATH% & | $(never-run)", "mcp"}
	binary, argv, e := hookMCPInvocation(filepath.Join(dir, "claude.cmd"), args)
	if e != nil {
		t.Fatal(e)
	}
	out, e := exec.CommandContext(context.Background(), binary, argv...).Output()
	if e != nil {
		t.Fatal(e)
	}
	var got []string
	if e = json.Unmarshal(out, &got); e != nil || !reflect.DeepEqual(got, args) {
		t.Fatalf("argv %q %v", out, e)
	}
	if _, _, e = hookMCPInvocation(filepath.Join(t.TempDir(), "claude.cmd"), args); e == nil {
		t.Fatal("unknown shell shim accepted")
	}
}
