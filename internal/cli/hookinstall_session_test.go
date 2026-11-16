package cli_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Installing a second profile into one settings file warns that every
// session will run both, and how to keep them apart (issue 38).
func TestHookInstallWarnsOtherProfile(t *testing.T) {
	u := installUser(t)
	settings := filepath.Join(t.TempDir(), "settings.local.json")
	if _, errs := installRun(t, u, "--profile", "p", "--write", "--settings", settings); strings.Contains(errs, "already has vecnet hooks") {
		t.Fatalf("first profile warned: %q", errs)
	}
	if _, errs := installRun(t, u, "--profile", "p", "--write", "--settings", settings); strings.Contains(errs, "already has vecnet hooks") {
		t.Fatalf("same profile warned: %q", errs)
	}
	for _, args := range [][]string{{"--profile", "q", "--settings", settings}, {"--profile", "q", "--write", "--settings", settings}} {
		_, errs := installRun(t, u, args...)
		for _, want := range []string{"already has vecnet hooks for profile(s) p;", "Session binding", "vecnet hook rebind --profile P", "VECNET_PROFILE=q claude"} {
			if !strings.Contains(errs, want) {
				t.Fatalf("%v: warning lacks %q: %q", args, want, errs)
			}
		}
	}
	if got := settingsCommands(t, readSettings(t, settings), "UserPromptSubmit"); len(got) != 2 {
		t.Fatalf("second profile not written: %v", got)
	}
	if _, errs := installRun(t, u, "--profile", "p", "--settings", settings); !strings.Contains(errs, "profile(s) q;") {
		t.Fatalf("reinstalling p: %q", errs)
	}
}

func gitT(t *testing.T, args ...string) {
	t.Helper()
	if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v %s", args, err, out)
	}
}

// In a linked worktree the installer warns that Claude Code reads the
// main checkout's settings.local.json, but still writes where it said.
func TestHookInstallWarnsLinkedWorktree(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	for _, layout := range []string{"sibling", "nested"} {
		t.Run(layout, func(t *testing.T) { testLinkedWorktree(t, layout == "nested") })
	}
}
func testLinkedWorktree(t *testing.T, nested bool) {
	isolateGitEnv(t)
	u := installUser(t)
	base := t.TempDir()
	main, wt := filepath.Join(base, "main"), filepath.Join(base, "wt")
	if nested { // this repo's own layout: <main>/.worktrees/<name>
		wt = filepath.Join(main, ".worktrees", "x")
	}
	gitT(t, "init", "--quiet", main)
	gitT(t, "-C", main, "-c", "user.email=t@example.invalid", "-c", "user.name=t", "commit", "--quiet", "--allow-empty", "-m", "init")
	gitT(t, "-C", main, "worktree", "add", "--quiet", wt)
	realMain, err := filepath.EvalSymlinks(main)
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(wt)
	_, errs := installRun(t, u, "--profile", "p", "--write")
	want := filepath.Join(realMain, ".claude", "settings.local.json")
	if !strings.Contains(errs, "is a linked git worktree") || !strings.Contains(errs, "--settings "+want) {
		t.Fatalf("worktree warning: %q", errs)
	}
	if _, err := os.Stat(filepath.Join(wt, ".claude", "settings.local.json")); err != nil {
		t.Fatalf("target not written: %v", err)
	}
	if _, err := os.Stat(filepath.Join(main, ".claude")); !os.IsNotExist(err) {
		t.Fatalf("installer switched target silently: %v", err)
	}
	// The main checkout itself is not a linked worktree.
	t.Chdir(main)
	if _, errs := installRun(t, u, "--profile", "p"); strings.Contains(errs, "linked git worktree") {
		t.Fatalf("main checkout warned: %q", errs)
	}
}

// CLAUDE_PROJECT_DIR, where the Claude Code session started, picks the
// target instead of the current directory; Claude Code's git-root rule
// still applies within it.
func TestHookInstallPrefersClaudeProjectDir(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	isolateGitEnv(t)
	u := installUser(t)
	base := t.TempDir()
	repo, elsewhere := filepath.Join(base, "repo"), filepath.Join(base, "elsewhere")
	gitT(t, "init", "--quiet", repo)
	sub := filepath.Join(repo, "sub")
	for _, d := range []string{sub, elsewhere} {
		if err := cliFixtureMkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Chdir(elsewhere)
	u.env["CLAUDE_PROJECT_DIR"] = sub
	_, errs := installRun(t, u, "--profile", "p", "--write")
	realRepo, err := filepath.EvalSymlinks(repo)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(realRepo, ".claude", "settings.local.json")
	if !strings.Contains(errs, "settings target "+path+" (CLAUDE_PROJECT_DIR git top-level") || !strings.Contains(errs, "using CLAUDE_PROJECT_DIR "+sub) {
		t.Fatalf("target: %q", errs)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("not written at CLAUDE_PROJECT_DIR's root: %v", err)
	}
	if _, err := os.Stat(filepath.Join(elsewhere, ".claude")); !os.IsNotExist(err) {
		t.Fatalf("cwd written: %v", err)
	}
	// Outside a git repository CLAUDE_PROJECT_DIR itself is the root.
	plain := filepath.Join(base, "plain")
	if err := cliFixtureMkdir(plain, 0o755); err != nil {
		t.Fatal(err)
	}
	u.env["CLAUDE_PROJECT_DIR"] = plain
	if _, errs := installRun(t, u, "--profile", "p", "--write"); !strings.Contains(errs, "CLAUDE_PROJECT_DIR fallback") {
		t.Fatalf("non-git CLAUDE_PROJECT_DIR: %q", errs)
	}
	if got := settingsCommands(t, readSettings(t, filepath.Join(plain, ".claude", "settings.local.json")), "UserPromptSubmit"); len(got) != 1 {
		t.Fatalf("non-git CLAUDE_PROJECT_DIR settings = %v", got)
	}
	u.env["CLAUDE_PROJECT_DIR"] = filepath.Join(base, "missing")
	if _, errs := installRun(t, u, "--profile", "p"); !strings.Contains(errs, "is not a directory; using the current directory") {
		t.Fatalf("missing CLAUDE_PROJECT_DIR: %q", errs)
	}
}

// A worktree nested inside a checkout whose settings already hold vecnet
// hooks: installing in either warns that one session can load both files.
func TestHookInstallWarnsNestedCheckouts(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	isolateGitEnv(t)
	u := installUser(t)
	main := filepath.Join(t.TempDir(), "main")
	wt := filepath.Join(main, ".worktrees", "x")
	gitT(t, "init", "--quiet", main)
	gitT(t, "-C", main, "-c", "user.email=t@example.invalid", "-c", "user.name=t", "commit", "--quiet", "--allow-empty", "-m", "init")
	gitT(t, "-C", main, "worktree", "add", "--quiet", wt)
	t.Chdir(main)
	if _, errs := installRun(t, u, "--profile", "p", "--write"); strings.Contains(errs, "can load both files") {
		t.Fatalf("no nested hooks yet, but warned: %q", errs)
	}
	t.Chdir(wt)
	_, errs := installRun(t, u, "--profile", "q", "--write")
	realMain, _ := filepath.EvalSymlinks(main)
	if !strings.Contains(errs, "is inside "+realMain+", whose .claude/settings.local.json has vecnet hooks for profile(s) p;") || !strings.Contains(errs, "VECNET_PROFILE=<its profile>") {
		t.Fatalf("inner install: %q", errs)
	}
	t.Chdir(main)
	_, errs = installRun(t, u, "--profile", "p")
	realWT, _ := filepath.EvalSymlinks(wt)
	if !strings.Contains(errs, "is the parent of "+realWT+", whose .claude/settings.local.json has vecnet hooks for profile(s) q;") {
		t.Fatalf("outer install: %q", errs)
	}
}
