//go:build !windows

package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestLocalStartupAdoptionReusesObservedProjectIdentity(t *testing.T) {
	searchPath := os.Getenv("PATH")
	a, root, _ := localRegistryFixture(t)
	// Preserve real Git lookup for this test's counted discovery wrapper.
	t.Setenv("PATH", searchPath)
	if _, err := a.observeLocalProject("v1", localRegistryNow()); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, ".codex", "hooks.json")
	if err := cliFixtureMkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	raw := []byte(`{"hooks":{"UserPromptSubmit":[{"hooks":[{"type":"command","command":"vecnet hook --profile old --event user-prompt","timeout":5}]}]}}`)
	if err := cliFixtureWriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	tools := t.TempDir()
	calls := filepath.Join(tools, "git-calls")
	t.Setenv("VECNET_TEST_REAL_GIT", realGit)
	t.Setenv("VECNET_TEST_GIT_CALLS", calls)
	if err := os.WriteFile(filepath.Join(tools, "git"), []byte("#!/bin/sh\nprintf 'called\\n' >> \"$VECNET_TEST_GIT_CALLS\"\nexec \"$VECNET_TEST_REAL_GIT\" \"$@\"\n"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", tools+string(os.PathListSeparator)+os.Getenv("PATH"))
	a.localRepairStart = func(*exec.Cmd) error { return nil }
	a.localStartup([]string{"peers"})
	observed, err := os.ReadFile(calls)
	if err != nil || strings.Count(string(observed), "called\n") != 1 {
		t.Fatalf("startup rediscovered an already observed project: calls=%q err=%v", observed, err)
	}
	projects, err := a.localProjects()
	if err != nil || len(projects) != 1 || projects[0].Root != root || len(projects[0].Artifacts) != 1 || projects[0].Artifacts[0].Path != path || projects[0].Artifacts[0].Version != "legacy" {
		t.Fatalf("adoption lost observed project authority: projects=%+v err=%v", projects, err)
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != string(raw) {
		t.Fatalf("adoption modified hooks: %q %v", got, err)
	}
}
