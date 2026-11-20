package cli_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/XertroV/vecnet/internal/platform"
)

// agyInstallRepo returns an install user chdir'ed into a disposable git
// repository, the default target root for --client agy.
func agyInstallRepo(t *testing.T) (*user, string) {
	t.Helper()
	isolateGitEnv(t)
	u := installUser(t)
	name := "agy repo "
	if runtime.GOOS == "windows" {
		name = "agy repo with spaces"
	}
	root := filepath.Join(t.TempDir(), name)
	gitT(t, "init", "--quiet", root)
	root, err := platform.Canonical(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	return u, root
}

func agyGroup(t *testing.T, body []byte) map[string]any {
	t.Helper()
	group, ok := decodeSettings(t, body)["vecnet-p"].(map[string]any)
	if !ok {
		t.Fatalf("missing vecnet-p group: %s", body)
	}
	return group
}

// The default target is the git top level's .agents/hooks.json, never the
// cwd's, agy's own settings, or the shared Claude/Codex files.
func TestHookInstallAgyDefaultTarget(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	u, root := agyInstallRepo(t)
	sub := filepath.Join(root, "sub")
	if err := cliFixtureMkdir(sub, 0755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(sub)
	out, errs := installRun(t, u, "--client", "agy", "--profile", "p", "--write")
	if out != "" {
		t.Fatalf("write stdout=%q", out)
	}
	path := filepath.Join(root, ".agents", "hooks.json")
	group := agyGroup(t, readSettings(t, path))
	if group["enabled"] != true {
		t.Fatalf("enabled=%v", group["enabled"])
	}
	for event, name := range map[string]string{"SessionStart": "session-start", "PreInvocation": "pre-invocation"} {
		handlers, ok := group[event].([]any)
		if !ok || len(handlers) != 1 {
			t.Fatalf("%s handlers=%v", event, group[event])
		}
		want := map[string]any{"type": "command", "command": "vecnet hook --client agy --profile p --event " + name, "timeout": float64(5)}
		if !reflect.DeepEqual(handlers[0], want) {
			t.Fatalf("%s handler=%v want %v", event, handlers[0], want)
		}
	}
	for event := range group {
		if event != "enabled" && event != "SessionStart" && event != "PreInvocation" {
			t.Fatalf("unexpected event %s in %v", event, group)
		}
	}
	for _, want := range []string{
		"settings target " + path + " (git top-level; agy workspace hooks)",
		"wrote " + path + " (SessionStart, PreInvocation)",
		"each git worktree needs its own install",
	} {
		if !strings.Contains(errs, want) {
			t.Fatalf("missing %q: %q", want, errs)
		}
	}
	if _, err := os.Stat(filepath.Join(sub, ".agents")); !os.IsNotExist(err) {
		t.Fatalf("sub/.agents written: %v", err)
	}
	for _, path := range []string{filepath.Join(u.env["HOME"], ".gemini"), filepath.Join(root, ".claude"), filepath.Join(root, ".codex")} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("unexpected %s: %v", path, err)
		}
	}
	cliFixtureRequireMode(t, path, 0644)
}

// agy discovers .agents/hooks.json from the workspace root, so a linked
// worktree needs (and gets) its own install; the main checkout stays alone.
func TestHookInstallAgyWorktreeOwnInstall(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	isolateGitEnv(t)
	u := installUser(t)
	base := t.TempDir()
	main := filepath.Join(base, "main")
	gitT(t, "init", "--quiet", main)
	gitT(t, "-C", main, "-c", "user.email=t@example.invalid", "-c", "user.name=t", "commit", "--quiet", "--allow-empty", "-m", "init")
	wt := filepath.Join(main, ".worktrees", "x")
	gitT(t, "-C", main, "worktree", "add", "--quiet", wt)
	t.Chdir(wt)
	_, errs := installRun(t, u, "--client", "agy", "--profile", "p", "--write")
	agyGroup(t, readSettings(t, filepath.Join(wt, ".agents", "hooks.json")))
	if _, err := os.Stat(filepath.Join(main, ".agents")); !os.IsNotExist(err) {
		t.Fatalf("main checkout .agents written: %v", err)
	}
	if !strings.Contains(errs, "each git worktree needs its own install") {
		t.Fatalf("missing worktree advice: %q", errs)
	}
}

func TestHookInstallAgyFallbackToCwd(t *testing.T) {
	isolateGitEnv(t)
	u := installUser(t)
	root := t.TempDir()
	t.Chdir(root)
	t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(root))
	if out, err := exec.Command("git", "rev-parse", "--show-toplevel").CombinedOutput(); err == nil {
		t.Fatalf("private cwd is inside git: %s", out)
	}
	_, errs := installRun(t, u, "--client", "agy", "--profile", "p", "--write")
	path := filepath.Join(root, ".agents", "hooks.json")
	agyGroup(t, readSettings(t, path))
	for _, want := range []string{"not in a git repository; using " + root, "settings target " + path + " (cwd fallback; agy workspace hooks)"} {
		if !strings.Contains(errs, want) {
			t.Fatalf("missing %q: %q", want, errs)
		}
	}
}

// A dry run (no --write, or an explicit --dry-run) prints a diff and writes
// nothing, including no .agents directory at all.
func TestHookInstallAgyDryRun(t *testing.T) {
	u, root := agyInstallRepo(t)
	path := filepath.Join(root, ".agents", "hooks.json")
	for _, flag := range []string{"", "--dry-run"} {
		args := []string{"--client", "agy", "--profile", "p"}
		if flag != "" {
			args = append(args, flag)
		}
		out, errs := installRun(t, u, args...)
		for _, want := range []string{"--- " + path, "+++ " + path, `"command": "vecnet hook --client agy --profile p --event pre-invocation"`} {
			if !strings.Contains(out, want) {
				t.Fatalf("%s: diff lacks %q:\n%s", flag, want, out)
			}
		}
		if !strings.Contains(errs, "would update "+path+"; rerun with --write") {
			t.Fatalf("%s: stderr=%q", flag, errs)
		}
		if _, err := os.Lstat(filepath.Join(root, ".agents")); !os.IsNotExist(err) {
			t.Fatalf("%s: dry run wrote .agents: %v", flag, err)
		}
	}
	code, out, errs := u.run("hook", "install", "--bin", "vecnet", "--client", "agy", "--profile", "p", "--write", "--dry-run")
	if code != 1 || out != "" || !strings.Contains(errs, "mutually exclusive") {
		t.Fatalf("exit=%d out=%q errs=%q", code, out, errs)
	}
}

func TestHookInstallAgyWriteLayout(t *testing.T) {
	u, root := agyInstallRepo(t)
	installRun(t, u, "--client", "agy", "--profile", "p", "--write")
	const want = `{
  "vecnet-p": {
    "enabled": true,
    "SessionStart": [
      {
        "type": "command",
        "command": "vecnet hook --client agy --profile p --event session-start",
        "timeout": 5
      }
    ],
    "PreInvocation": [
      {
        "type": "command",
        "command": "vecnet hook --client agy --profile p --event pre-invocation",
        "timeout": 5
      }
    ]
  }
}`
	if got := string(readSettings(t, filepath.Join(root, ".agents", "hooks.json"))); got != want {
		t.Fatalf("layout:\n%s\nwant:\n%s", got, want)
	}
}

func TestHookInstallAgyIdempotent(t *testing.T) {
	u, root := agyInstallRepo(t)
	path := filepath.Join(root, ".agents", "hooks.json")
	args := []string{"--client", "agy", "--profile", "p", "--write"}
	installRun(t, u, args...)
	first := string(readSettings(t, path))
	_, errs := installRun(t, u, args...)
	if !strings.Contains(errs, "unchanged "+path) || string(readSettings(t, path)) != first {
		t.Fatalf("not idempotent: %q", errs)
	}
	// Adding --mcp replaces the group's commands in place, once.
	installRun(t, u, "--client", "agy", "--profile", "p", "--mcp", "--write")
	updated := string(readSettings(t, path))
	if strings.Count(updated, `"vecnet-p": {`) != 1 || strings.Count(updated, " --mcp vecnet-p") != 2 {
		t.Fatalf("--mcp install:\n%s", updated)
	}
	installRun(t, u, "--client", "agy", "--profile", "p", "--mcp", "--write")
	if string(readSettings(t, path)) != updated {
		t.Fatalf("--mcp install not idempotent:\n%s", readSettings(t, path))
	}
	// A second profile gets its own named group beside the first.
	installRun(t, u, "--client", "agy", "--profile", "q", "--write")
	both := decodeSettings(t, readSettings(t, path))
	if _, ok := both["vecnet-p"].(map[string]any); !ok {
		t.Fatal("vecnet-p group lost")
	}
	if _, ok := both["vecnet-q"].(map[string]any); !ok {
		t.Fatal("vecnet-q group missing")
	}
}

// An unrelated group and the key order of the file are preserved.
func TestHookInstallAgyPreservesUnrelatedGroups(t *testing.T) {
	u, root := agyInstallRepo(t)
	const before = `{
  "formatter": {
    "enabled": true,
    "PostToolUse": [
      {
        "matcher": "Editor",
        "hooks": [
          {
            "type": "command",
            "command": "fmt"
          }
        ]
      }
    ]
  },
  "stop-note": {
    "Stop": [
      {
        "type": "command",
        "command": "vecnet hook --client agy --profile p --event stop",
        "timeout": 5
      }
    ]
  }
}
`
	path := filepath.Join(root, ".agents", "hooks.json")
	if err := cliFixtureMkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := cliFixtureWriteFile(path, []byte(before), 0640); err != nil {
		t.Fatal(err)
	}
	installRun(t, u, "--client", "agy", "--profile", "p", "--write")
	after := string(readSettings(t, path))
	b, a := decodeSettings(t, []byte(before)), decodeSettings(t, []byte(after))
	if !reflect.DeepEqual(b["formatter"], a["formatter"]) || !reflect.DeepEqual(b["stop-note"], a["stop-note"]) {
		t.Fatalf("foreign group changed:\n%s", after)
	}
	if !strings.Contains(after, `"stop-note": {`) || strings.Index(after, `"formatter"`) > strings.Index(after, `"stop-note"`) {
		t.Fatalf("foreign key order changed:\n%s", after)
	}
	if strings.Index(after, `"stop-note"`) > strings.Index(after, `"vecnet-p"`) {
		t.Fatalf("vecnet-p not appended last:\n%s", after)
	}
	if !strings.HasSuffix(after, "}\n") {
		t.Fatalf("trailing newline policy changed:\n%s", after)
	}
	cliFixtureRequireMode(t, path, 0640)
}

// An existing vecnet-P group with a stale command is replaced in place:
// same position, exact installer content, whatever it held before.
func TestHookInstallAgyReplacesStaleGroupInPlace(t *testing.T) {
	u, root := agyInstallRepo(t)
	const before = `{
  "alpha": {
    "enabled": true
  },
  "vecnet-p": {
    "enabled": false,
    "SessionStart": [
      {
        "type": "command",
        "command": "/stale/vecnet hook --client agy --profile p --event session-start",
        "timeout": 9
      }
    ],
    "Stop": [
      {
        "type": "command",
        "command": "old stop hook"
      }
    ]
  },
  "zulu": {
    "PreInvocation": [
      {
        "type": "command",
        "command": "keep"
      }
    ]
  }
}
`
	path := filepath.Join(root, ".agents", "hooks.json")
	if err := cliFixtureMkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := cliFixtureWriteFile(path, []byte(before), 0644); err != nil {
		t.Fatal(err)
	}
	installRun(t, u, "--client", "agy", "--profile", "p", "--write")
	after := string(readSettings(t, path))
	if strings.Index(after, `"alpha"`) > strings.Index(after, `"vecnet-p"`) || strings.Index(after, `"vecnet-p"`) > strings.Index(after, `"zulu"`) {
		t.Fatalf("group position changed:\n%s", after)
	}
	if strings.Contains(after, "stale") || strings.Contains(after, "Stop") || strings.Contains(after, "false") {
		t.Fatalf("stale content survived:\n%s", after)
	}
	a := decodeSettings(t, []byte(after))
	if _, ok := a["alpha"].(map[string]any); !ok {
		t.Fatal("alpha lost")
	}
	zulu, ok := a["zulu"].(map[string]any)
	if !ok || !reflect.DeepEqual(zulu, decodeSettings(t, []byte(before))["zulu"]) {
		t.Fatalf("zulu changed: %v", a["zulu"])
	}
	// The replacement is exactly a fresh install of the group.
	freshUser, freshRoot := agyInstallRepo(t)
	installRun(t, freshUser, "--client", "agy", "--profile", "p", "--write")
	want := agyGroup(t, readSettings(t, filepath.Join(freshRoot, ".agents", "hooks.json")))
	if !reflect.DeepEqual(a["vecnet-p"], want) {
		t.Fatalf("replacement=%v want %v", a["vecnet-p"], want)
	}
}

// Invalid or duplicate JSON leaves the file byte-identical.
func TestHookInstallAgyRefusesBadShapes(t *testing.T) {
	for _, body := range []string{`{"vecnet-p":`, `[]`, `{"a":1,"a":2}`, `{"vecnet-p":{"SessionStart":1,"SessionStart":2}}`} {
		t.Run(body, func(t *testing.T) {
			u, root := agyInstallRepo(t)
			path := filepath.Join(root, ".agents", "hooks.json")
			if err := cliFixtureMkdirAll(filepath.Dir(path), 0755); err != nil {
				t.Fatal(err)
			}
			if err := cliFixtureWriteFile(path, []byte(body), 0640); err != nil {
				t.Fatal(err)
			}
			code, out, errs := u.run("hook", "install", "--bin", "vecnet", "--client", "agy", "--profile", "p", "--write")
			if code != 1 || out != "" || errs == "" {
				t.Fatalf("exit=%d out=%q errs=%q", code, out, errs)
			}
			if got := string(readSettings(t, path)); got != body {
				t.Fatalf("bad file modified: %q", got)
			}
		})
	}
}

// A symlinked .agents directory, hooks.json or mcp_config.json is refused
// without --settings: the link and its target stay unchanged.
func TestHookInstallAgyRefusesSymlinkDefault(t *testing.T) {
	for _, mode := range []string{"dir", "file", "mcp"} {
		t.Run(mode, func(t *testing.T) {
			u, root := agyInstallRepo(t)
			target := filepath.Join(t.TempDir(), "hooks.json")
			if mode == "mcp" {
				target = filepath.Join(t.TempDir(), "mcp_config.json")
			}
			if err := cliFixtureWriteFile(target, []byte("{}\n"), 0640); err != nil {
				t.Fatal(err)
			}
			agents := filepath.Join(root, ".agents")
			link, to := agents, filepath.Dir(target)
			if mode != "dir" {
				if err := cliFixtureMkdir(agents, 0755); err != nil {
					t.Fatal(err)
				}
				link = filepath.Join(agents, filepath.Base(target))
				to = target
			}
			if err := cliFixtureSymlink(t, to, link); err != nil {
				t.Fatal(err)
			}
			code, out, errs := u.run("hook", "install", "--bin", "vecnet", "--client", "agy", "--profile", "p", "--mcp", "--write")
			what := "the workspace .agents/hooks.json"
			if mode == "mcp" {
				what = "the workspace .agents/mcp_config.json"
			}
			refused := strings.Contains(errs, what) && strings.Contains(errs, "--settings")
			if runtime.GOOS == "windows" {
				refused = refused || strings.Contains(errs, "reparse point refused")
			}
			if code == 0 || out != "" || !refused {
				t.Fatalf("exit=%d out=%q errs=%q", code, out, errs)
			}
			if after, err := os.Readlink(link); err != nil || after != to {
				t.Fatalf("link=%q err=%v", after, err)
			}
			if got := readSettings(t, target); string(got) != "{}\n" {
				t.Fatalf("symlink target changed: %q", got)
			}
		})
	}
}

// A git-tracked .agents/hooks.json is refused without --settings, with
// advice naming .git/info/exclude; a dry run may describe it.
func TestHookInstallAgyRefusesTrackedDefault(t *testing.T) {
	u, root := agyInstallRepo(t)
	path := filepath.Join(root, ".agents", "hooks.json")
	if err := cliFixtureMkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := cliFixtureWriteFile(path, []byte("{}\n"), 0640); err != nil {
		t.Fatal(err)
	}
	gitT(t, "add", "--", ".agents/hooks.json")
	gitT(t, "-c", "commit.gpgsign=false", "-c", "user.name=test", "-c", "user.email=test@example.invalid", "commit", "--quiet", "-m", "tracked hooks")
	installRun(t, u, "--client", "agy", "--profile", "p") // a dry run may describe it
	code, out, errs := u.run("hook", "install", "--bin", "vecnet", "--client", "agy", "--profile", "p", "--write")
	for _, want := range []string{"tracked by git", ".git/info/exclude", "pass --settings " + path} {
		if code != 1 || out != "" || !strings.Contains(errs, want) {
			t.Fatalf("exit=%d out=%q errs=%q (want %q)", code, out, errs, want)
		}
	}
	if got := string(readSettings(t, path)); got != "{}\n" {
		t.Fatalf("tracked file changed: %q", got)
	}
	installRun(t, u, "--client", "agy", "--profile", "p", "--write", "--settings", path)
	agyGroup(t, readSettings(t, path))
	cliFixtureRequireMode(t, path, 0640)
}

// A tracking status that git cannot answer (or a repository it cannot even
// discover) refuses the default write target.
func TestHookInstallAgyRefusesUnknownTracking(t *testing.T) {
	for _, tc := range []struct{ name, stub, want string }{
		{"status unknown", "#!/bin/sh\ncase \"$3\" in\nrev-parse) printf '%s\\n' \"$2\";;\nls-files) exit 2;;\n*) exit 99;;\nesac\n", "tracked by git (or its status is unknown)"},
		{"discovery failed", "#!/bin/sh\necho \"fatal: detected dubious ownership in repository at '$2'\" >&2\nexit 128\n", "cannot tell whether"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			u, root := agyInstallRepo(t)
			binDir := t.TempDir()
			cliFixtureProgram(t, binDir, "git", tc.stub, cliFixtureProgramSpec{Kind: "git-agy-" + tc.name, Exit: 2})
			t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
			path := filepath.Join(root, ".agents", "hooks.json")
			code, out, errs := u.run("hook", "install", "--bin", "vecnet", "--client", "agy", "--profile", "p", "--write")
			if code != 1 || out != "" || !strings.Contains(errs, tc.want) || !strings.Contains(errs, "pass --settings "+path) {
				t.Fatalf("exit=%d out=%q errs=%q", code, out, errs)
			}
			if _, err := os.Lstat(filepath.Dir(path)); !os.IsNotExist(err) {
				t.Fatalf("unknown-status target written: %v", err)
			}
		})
	}
}

func agyWantServer(u *user) map[string]any {
	// The installer always opts the server's relay into the agy extensions;
	// VECNET_CONFIG is baked in only when it is set at install time.
	env := map[string]any{"VECNET_MCP_CLIENT": "agy"}
	if conf := u.env["VECNET_CONFIG"]; conf != "" {
		env["VECNET_CONFIG"] = conf
	}
	return map[string]any{
		"command":        "vecnet",
		"args":           []any{"--net", "p", "mcp"},
		"env":            env,
		"timeoutSeconds": float64(1860),
		"tools": map[string]any{
			"vecnet_inbox": map[string]any{"eager": true},
			"vecnet_reply": map[string]any{"eager": true},
			"vecnet_ack":   map[string]any{"eager": true},
			"vecnet_send":  map[string]any{"eager": true},
			"vecnet_wait":  map[string]any{"eager": true, "background": "always"},
		},
	}
}

// --mcp merges the vecnet-P server into .agents/mcp_config.json, names it in
// the hook commands, prints the allow-rule advice and never runs claude.
func TestHookInstallAgyMCP(t *testing.T) {
	log := fakeClaude(t, 0)
	u, root := agyInstallRepo(t)
	hooks := filepath.Join(root, ".agents", "hooks.json")
	mcpPath := filepath.Join(root, ".agents", "mcp_config.json")
	_, errs := installRun(t, u, "--client", "agy", "--profile", "p", "--mcp", "--write")
	for _, want := range []string{
		"vecnet hook --client agy --profile p --event session-start --mcp vecnet-p",
		"vecnet hook --client agy --profile p --event pre-invocation --mcp vecnet-p",
	} {
		if !strings.Contains(string(readSettings(t, hooks)), want) {
			t.Fatalf("hooks lack %q:\n%s", want, readSettings(t, hooks))
		}
	}
	servers := decodeSettings(t, readSettings(t, mcpPath))["mcpServers"].(map[string]any)
	if !reflect.DeepEqual(servers["vecnet-p"], agyWantServer(u)) {
		t.Fatalf("server=%v want %v", servers["vecnet-p"], agyWantServer(u))
	}
	for _, want := range []string{"wrote " + mcpPath + " (MCP server vecnet-p)", "asks before each vecnet_wait call", "mcp(vecnet-p/vecnet_wait)"} {
		if !strings.Contains(errs, want) {
			t.Fatalf("missing %q: %q", want, errs)
		}
	}
	if runs := claudeRuns(t, log); runs != nil {
		t.Fatalf("claude ran: %q", runs)
	}
	// A re-run is idempotent for both files.
	hooksBefore, mcpBefore := string(readSettings(t, hooks)), string(readSettings(t, mcpPath))
	_, errs = installRun(t, u, "--client", "agy", "--profile", "p", "--mcp", "--write")
	if !strings.Contains(errs, "unchanged "+hooks) || !strings.Contains(errs, "unchanged "+mcpPath) {
		t.Fatalf("not idempotent: %q", errs)
	}
	if string(readSettings(t, hooks)) != hooksBefore || string(readSettings(t, mcpPath)) != mcpBefore {
		t.Fatal("idempotent re-run changed a file")
	}
	// Without VECNET_CONFIG the env carries only the client opt-in.
	u2, root2 := agyInstallRepo(t)
	delete(u2.env, "VECNET_CONFIG")
	installRun(t, u2, "--client", "agy", "--profile", "p", "--mcp", "--write")
	servers = decodeSettings(t, readSettings(t, filepath.Join(root2, ".agents", "mcp_config.json")))["mcpServers"].(map[string]any)
	if !reflect.DeepEqual(servers["vecnet-p"], agyWantServer(u2)) {
		t.Fatalf("server without config=%v", servers["vecnet-p"])
	}
}

// Other MCP servers are never touched, and an existing vecnet-p entry is
// replaced in place, keeping its position.
func TestHookInstallAgyMCPPreservesServers(t *testing.T) {
	u, root := agyInstallRepo(t)
	const before = `{
  "mcpServers": {
    "docs": {
      "command": "docs-server"
    },
    "vecnet-p": {
      "command": "/stale/vecnet",
      "args": [
        "wrong"
      ],
      "timeoutSeconds": 1
    },
    "lsp": {
      "command": "lsp-server"
    }
  }
}
`
	mcpPath := filepath.Join(root, ".agents", "mcp_config.json")
	if err := cliFixtureMkdirAll(filepath.Dir(mcpPath), 0755); err != nil {
		t.Fatal(err)
	}
	if err := cliFixtureWriteFile(mcpPath, []byte(before), 0640); err != nil {
		t.Fatal(err)
	}
	installRun(t, u, "--client", "agy", "--profile", "p", "--mcp", "--write")
	after := string(readSettings(t, mcpPath))
	if strings.Contains(after, "stale") || strings.Contains(after, "wrong") {
		t.Fatalf("stale server survived:\n%s", after)
	}
	if !(strings.Index(after, `"docs"`) < strings.Index(after, `"vecnet-p"`) && strings.Index(after, `"vecnet-p"`) < strings.Index(after, `"lsp"`)) {
		t.Fatalf("server position changed:\n%s", after)
	}
	b, a := decodeSettings(t, []byte(before))["mcpServers"].(map[string]any), decodeSettings(t, []byte(after))["mcpServers"].(map[string]any)
	if !reflect.DeepEqual(b["docs"], a["docs"]) || !reflect.DeepEqual(b["lsp"], a["lsp"]) {
		t.Fatalf("foreign server changed:\n%s", after)
	}
	if !reflect.DeepEqual(a["vecnet-p"], agyWantServer(u)) {
		t.Fatalf("server=%v want %v", a["vecnet-p"], agyWantServer(u))
	}
	cliFixtureRequireMode(t, mcpPath, 0640)
}

// A dry run with --mcp previews both files and writes neither; --settings
// carries the MCP target next to the explicit hooks target.
func TestHookInstallAgyMCPDryRunAndSettings(t *testing.T) {
	u, root := agyInstallRepo(t)
	hooks := filepath.Join(root, ".agents", "hooks.json")
	mcpPath := filepath.Join(root, ".agents", "mcp_config.json")
	out, errs := installRun(t, u, "--client", "agy", "--profile", "p", "--mcp")
	for _, want := range []string{"+++ " + hooks, "+++ " + mcpPath, `"timeoutSeconds": 1860`} {
		if !strings.Contains(out, want) {
			t.Fatalf("diff lacks %q:\n%s", want, out)
		}
	}
	if !strings.Contains(errs, "would update "+hooks) || !strings.Contains(errs, "would update "+mcpPath) {
		t.Fatalf("stderr=%q", errs)
	}
	if _, err := os.Lstat(filepath.Join(root, ".agents")); !os.IsNotExist(err) {
		t.Fatalf("dry run wrote .agents: %v", err)
	}
	dir := t.TempDir()
	explicit := filepath.Join(dir, "hooks.json")
	installRun(t, u, "--client", "agy", "--profile", "p", "--mcp", "--write", "--settings", explicit)
	agyGroup(t, readSettings(t, explicit))
	servers := decodeSettings(t, readSettings(t, filepath.Join(dir, "mcp_config.json")))["mcpServers"].(map[string]any)
	if !reflect.DeepEqual(servers["vecnet-p"], agyWantServer(u)) {
		t.Fatalf("server=%v", servers["vecnet-p"])
	}
}

// Claude's --dry-run/--rollback usage error keeps its exact pre-agy text;
// agy alone may use --dry-run, and its --rollback refusal stays its own.
func TestHookInstallDryRunRollbackUsageText(t *testing.T) {
	u, _ := agyInstallRepo(t)
	for _, tc := range []struct{ name, client, flag string }{
		{"claude dry-run", "", "--dry-run"},
		{"claude rollback", "", "--rollback,backup.json"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args := []string{"hook", "install", "--bin", "vecnet", "--profile", "p"}
			if tc.client != "" {
				args = append(args, "--client", tc.client)
			}
			code, out, errs := u.run(append(args, strings.Split(tc.flag, ",")...)...)
			if code != 1 || out != "" || !strings.Contains(errs, "hook install: --rollback and --dry-run require --client codex") {
				t.Fatalf("exit=%d out=%q errs=%q", code, out, errs)
			}
		})
	}
}

// A git-tracked .agents/mcp_config.json is refused like a tracked
// hooks.json: --mcp --write without --settings writes nothing.
func TestHookInstallAgyRefusesTrackedMCPConfig(t *testing.T) {
	u, root := agyInstallRepo(t)
	agents := filepath.Join(root, ".agents")
	hooksPath := filepath.Join(agents, "hooks.json")
	mcpPath := filepath.Join(agents, "mcp_config.json")
	if err := cliFixtureMkdirAll(agents, 0755); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{hooksPath, mcpPath} {
		if err := cliFixtureWriteFile(path, []byte("{}\n"), 0640); err != nil {
			t.Fatal(err)
		}
	}
	gitT(t, "add", "--", ".agents/mcp_config.json")
	gitT(t, "-c", "commit.gpgsign=false", "-c", "user.name=test", "-c", "user.email=test@example.invalid", "commit", "--quiet", "-m", "tracked mcp config")
	code, out, errs := u.run("hook", "install", "--bin", "vecnet", "--client", "agy", "--profile", "p", "--mcp", "--write")
	for _, want := range []string{"tracked by git", ".git/info/exclude", "pass --settings " + mcpPath} {
		if code != 1 || out != "" || !strings.Contains(errs, want) {
			t.Fatalf("exit=%d out=%q errs=%q (want %q)", code, out, errs, want)
		}
	}
	for _, path := range []string{hooksPath, mcpPath} {
		if got := string(readSettings(t, path)); got != "{}\n" {
			t.Fatalf("tracked %s changed: %q", path, got)
		}
	}
}

// When the mcp_config.json step fails after hooks.json was already
// updated, the error says so and names both paths.
func TestHookInstallAgyMCPWriteFailureNamesBothPaths(t *testing.T) {
	u, root := agyInstallRepo(t)
	hooksPath := filepath.Join(root, ".agents", "hooks.json")
	mcpPath := filepath.Join(root, ".agents", "mcp_config.json")
	if err := cliFixtureMkdirAll(filepath.Dir(mcpPath), 0755); err != nil {
		t.Fatal(err)
	}
	if err := cliFixtureWriteFile(mcpPath, []byte(`{"mcpServers":`), 0644); err != nil {
		t.Fatal(err)
	}
	code, out, errs := u.run("hook", "install", "--bin", "vecnet", "--client", "agy", "--profile", "p", "--mcp", "--write")
	if code == 0 || out != "" {
		t.Fatalf("exit=%d out=%q errs=%q", code, out, errs)
	}
	for _, want := range []string{"already updated", hooksPath, mcpPath} {
		if !strings.Contains(errs, want) {
			t.Fatalf("error lacks %q: %q", want, errs)
		}
	}
	agyGroup(t, readSettings(t, hooksPath)) // hooks.json itself was written
}

func TestHookInstallAgyRejectsAutoAck(t *testing.T) {
	u, root := agyInstallRepo(t)
	code, out, errs := u.run("hook", "install", "--bin", "vecnet", "--client", "agy", "--profile", "p", "--auto-ack", "--write")
	if code != 1 || out != "" || !strings.Contains(errs, "omit --auto-ack") {
		t.Fatalf("exit=%d out=%q errs=%q", code, out, errs)
	}
	if _, err := os.Lstat(filepath.Join(root, ".agents")); !os.IsNotExist(err) {
		t.Fatalf("file written: %v", err)
	}
}

func TestHookInstallAgyRejectsRollback(t *testing.T) {
	u, root := agyInstallRepo(t)
	code, out, errs := u.run("hook", "install", "--bin", "vecnet", "--client", "agy", "--profile", "p", "--rollback", "backup.json", "--write")
	if code != 1 || out != "" || !strings.Contains(errs, "--rollback requires --client codex") {
		t.Fatalf("exit=%d out=%q errs=%q", code, out, errs)
	}
	if _, err := os.Lstat(filepath.Join(root, ".agents")); !os.IsNotExist(err) {
		t.Fatalf("file written: %v", err)
	}
}
