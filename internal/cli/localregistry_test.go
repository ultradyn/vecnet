package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

func localRegistryFixture(t *testing.T) (*app, string, string) {
	t.Helper()
	base, err := localRegistryCanonical(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := cliFixtureChmod(base, 0700); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(base, "project")
	home := filepath.Join(base, "home")
	if err := cliFixtureMkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	cliFixtureChdir(t, root)
	return &app{ctx: context.Background(), getenv: func(k string) string {
		if k == "HOME" {
			return home
		}
		return ""
	}, stdout: io.Discard, stderr: io.Discard}, root, home
}
func localRegistryInventory(home string) string {
	return filepath.Join(home, ".local", "state", "vecnet", "projects", "inventory.json")
}
func localRegistryNow() time.Time { return time.Date(2026, 11, 15, 2, 0, 0, 0, time.UTC) }

func TestLocalRegistryInspectionDoesNotCreateFiles(t *testing.T) {
	a, _, home := localRegistryFixture(t)
	var out bytes.Buffer
	a.stdout = &out
	if err := a.cmdProjects([]string{"list", "--json"}); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out.String()) != "[]" {
		t.Fatalf("empty list: %q", out.String())
	}
	if _, err := os.Stat(home); !os.IsNotExist(err) {
		t.Fatalf("inspection created HOME: %v", err)
	}
	if err := a.cmdProjects([]string{"forget", filepath.Join(t.TempDir(), "missing")}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(home); !os.IsNotExist(err) {
		t.Fatalf("forget created HOME: %v", err)
	}
}
func TestLocalRegistryObservationSeparatesAppliedState(t *testing.T) {
	a, root, home := localRegistryFixture(t)
	now := localRegistryNow()
	p, err := a.observeLocalProject("v1", now)
	if err != nil {
		t.Fatal(err)
	}
	if p.Root != root || p.Directory != root || p.Repository != "" || p.Worktree != "" || p.ObservedVersion != "v1" || !p.FirstSeen.Equal(now) || !p.LastSeen.Equal(now) {
		t.Fatalf("first observation: %+v", p)
	}
	if err := a.updateLocalProject(root, func(p *localProject) error {
		p.AppliedVersion = "v1"
		p.AppliedDigest = strings.Repeat("a", 64)
		p.Status = "pending"
		p.Error = "directory unavailable"
		p.LastMigration = "hooks-v1"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(t.TempDir(), "alias")
	if err := cliFixtureSymlink(t, root, alias); err != nil {
		t.Fatal(err)
	}
	cliFixtureChdir(t, alias)
	p, err = a.observeLocalProject("v2", now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if p.AppliedVersion != "v1" || p.Status != "pending" || p.Error != "directory unavailable" || p.LastMigration != "hooks-v1" || !p.FirstSeen.Equal(now) || !p.LastSeen.Equal(now.Add(time.Hour)) {
		t.Fatalf("observation changed applied state: %+v", p)
	}
	rows, err := a.localProjects()
	if err != nil || len(rows) != 1 {
		t.Fatalf("alias duplicates: %+v %v", rows, err)
	}
	for _, rel := range []string{".codex", ".claude", ".git"} {
		if _, err := os.Stat(filepath.Join(root, rel)); !os.IsNotExist(err) {
			t.Fatalf("observation installed %s", rel)
		}
	}
	for path, want := range map[string]os.FileMode{filepath.Dir(localRegistryInventory(home)): 0700, localRegistryInventory(home): 0600, filepath.Join(filepath.Dir(localRegistryInventory(home)), "inventory.lock"): 0600} {
		cliFixtureRequireMode(t, path, want)
	}
}
func TestLocalRegistryGitWorktreesAndSubdirectories(t *testing.T) {
	a, root, _ := localRegistryFixture(t)
	git := func(args ...string) string {
		t.Helper()
		c := exec.Command("git", args...)
		c.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull)
		out, err := c.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %s %v", args, out, err)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", root)
	git("-C", root, "-c", "user.name=fixture", "-c", "user.email=fixture@example.invalid", "commit", "--allow-empty", "-m", "fixture")
	sub := filepath.Join(root, "sub")
	if err := cliFixtureMkdir(sub, 0700); err != nil {
		t.Fatal(err)
	}
	cliFixtureChdir(t, sub)
	first, err := a.observeLocalProject("v1", localRegistryNow())
	if err != nil {
		t.Fatal(err)
	}
	if first.Root != root || first.Directory != sub || first.Repository != filepath.Join(root, ".git") || first.Worktree != filepath.Join(root, ".git") {
		t.Fatalf("git identity: %+v", first)
	}
	other := filepath.Join(t.TempDir(), "worktree")
	git("-C", root, "worktree", "add", "-b", "fixture", other)
	other, err = localRegistryCanonical(other)
	if err != nil {
		t.Fatal(err)
	}
	cliFixtureChdir(t, other)
	second, err := a.observeLocalProject("v1", localRegistryNow())
	if err != nil {
		t.Fatal(err)
	}
	if second.Root != other || second.Repository != first.Repository || second.Worktree == first.Worktree || second.Worktree == "" {
		t.Fatalf("lost separate worktree: %+v vs %+v", first, second)
	}
	rows, err := a.localProjects()
	if err != nil || len(rows) != 2 {
		t.Fatalf("worktrees: %+v %v", rows, err)
	}
}
func TestLocalRegistryArtifactsAndForgetPreserveFiles(t *testing.T) {
	a, root, home := localRegistryFixture(t)
	now := localRegistryNow()
	if _, err := a.observeLocalProject("v1", now); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "explicit-hooks.json")
	body := []byte("{}\n")
	if err := cliFixtureWriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	path, err := localRegistryCanonical(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.registerLocalArtifact(path, "codex-hooks", "v1", now); err != nil {
		t.Fatal(err)
	}
	if err := a.registerLocalArtifact(path, "codex-hooks", "v1", now); err != nil {
		t.Fatal(err)
	}
	rows, err := a.localProjects()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || len(rows[0].Artifacts) != 1 {
		t.Fatalf("artifact duplicate: %+v", rows)
	}
	art := rows[0].Artifacts[0]
	if art.Path != path || art.Kind != "codex-hooks" || art.Version != "v1" || art.Schema != 1 || art.Hash != "ca3d163bab055381827226140568f3bef7eaac187cebd76878e0b63e9e442356" || !art.InstalledAt.Equal(now) {
		t.Fatalf("artifact metadata: %+v", art)
	}
	authority := filepath.Join(home, ".local", "state", "vecnet", "codex", "bindings.json")
	if err := cliFixtureMkdirAll(filepath.Dir(authority), 0700); err != nil {
		t.Fatal(err)
	}
	if err := cliFixtureWriteFile(authority, []byte("authority sentinel"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(home); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}
	if err := a.updateLocalProject(root, func(p *localProject) error { p.Status = "pending"; p.Error = "missing project"; return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatal("update recreated removed project")
	}
	var out bytes.Buffer
	a.stdout = &out
	if err := a.cmdProjects([]string{"list", "--json"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "missing project") {
		t.Fatal(out.String())
	}
	if err := a.cmdProjects([]string{"forget", root}); err != nil {
		t.Fatal(err)
	}
	if raw, err := os.ReadFile(path); err != nil || !bytes.Equal(raw, body) {
		t.Fatalf("forget touched artifact: %q %v", raw, err)
	}
	if raw, err := os.ReadFile(authority); err != nil || string(raw) != "authority sentinel" {
		t.Fatalf("forget touched ownership: %q %v", raw, err)
	}
	rows, err = a.localProjects()
	if err != nil || len(rows) != 0 {
		t.Fatalf("forget: %+v %v", rows, err)
	}
}
func TestLocalRegistryRejectsMalformedInventoryWithoutOverwrite(t *testing.T) {
	cases := []string{`null`, `{}`, `{"version":2,"projects":[]}`, `{"version":1,"projects":null}`, `{"version":1,"version":1,"projects":[]}`, `{"Version":1,"projects":[]}`, `{"version":1,"projects":[],"secret":"no"}`, `{"version":1,"projects":[]} {}`}
	for i, raw := range cases {
		t.Run(strconv.Itoa(i), func(t *testing.T) {
			a, _, home := localRegistryFixture(t)
			path := localRegistryInventory(home)
			if err := cliFixtureMkdirAll(filepath.Dir(path), 0700); err != nil {
				t.Fatal(err)
			}
			if err := cliFixtureWriteFile(path, []byte(raw), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := a.localProjects(); err == nil {
				t.Fatal("accepted invalid schema")
			}
			if _, err := a.observeLocalProject("v1", localRegistryNow()); err == nil {
				t.Fatal("overwrote invalid schema")
			}
			if after, _ := os.ReadFile(path); string(after) != raw {
				t.Fatal("modified invalid inventory")
			}
		})
	}
}
func TestLocalRegistryRejectsUnsafeRowsAndUpdates(t *testing.T) {
	a, root, home := localRegistryFixture(t)
	if _, err := a.observeLocalProject("v1", localRegistryNow()); err != nil {
		t.Fatal(err)
	}
	path := localRegistryInventory(home)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*localProject){func(p *localProject) { p.Root = "relative" }, func(p *localProject) { p.Directory = "/a/../bad" }, func(p *localProject) { p.Root = "/bad\npath" }, func(p *localProject) {
		p.Artifacts = []localArtifact{{Path: "../hooks.json", Kind: "codex-hooks", Schema: 1, Hash: strings.Repeat("a", 64), InstalledAt: localRegistryNow()}}
	}, func(p *localProject) { p.FirstSeen = time.Time{} }, func(p *localProject) { p.LastSeen = p.FirstSeen.Add(-time.Hour) }} {
		if err := a.updateLocalProject(root, func(p *localProject) error { mutate(p); return nil }); err == nil {
			t.Fatal("accepted unsafe mutation")
		}
		if after, _ := os.ReadFile(path); !bytes.Equal(before, after) {
			t.Fatal("unsafe update changed file")
		}
	}
	if err := a.updateLocalProject(root, func(p *localProject) error { p.Status = "changed"; return fmt.Errorf("abort") }); err == nil {
		t.Fatal("callback error ignored")
	}
	if after, _ := os.ReadFile(path); !bytes.Equal(before, after) {
		t.Fatal("aborted update persisted")
	}
	var file map[string]any
	if err := json.Unmarshal(before, &file); err != nil {
		t.Fatal(err)
	}
	rows := file["projects"].([]any)
	rows[0].(map[string]any)["root"] = "../hostile"
	raw, _ := json.Marshal(file)
	if err := cliFixtureWriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := a.localProjects(); err == nil {
		t.Fatal("accepted unsafe stored path")
	}
}
func TestLocalRegistryInterprocessUpdates(t *testing.T) {
	a, root, home := localRegistryFixture(t)
	if _, err := a.observeLocalProject("v1", localRegistryNow()); err != nil {
		t.Fatal(err)
	}
	const workers = 6
	const concurrent = 2
	cmds := make([]*exec.Cmd, workers)
	outputs := make([]bytes.Buffer, workers)
	for i := range cmds {
		cmds[i] = exec.Command(os.Args[0], "-test.run=^TestLocalRegistryProcessWorker$")
		cmds[i].Env = append(os.Environ(), "VECNET_REGISTRY_TEST_HOME="+home, "VECNET_REGISTRY_TEST_ROOT="+root)
		cmds[i].Stdout = &outputs[i]
		cmds[i].Stderr = &outputs[i]
		if err := cmds[i].Start(); err != nil {
			t.Fatal(err)
		}
		// Keep all six independent processes and 60 updates, with two writers
		// contending at once. Six cold Windows writers exceeded the production
		// two-second lock budget; the test must not change that user-facing cap.
		if (i+1)%concurrent == 0 {
			for j := i + 1 - concurrent; j <= i; j++ {
				if err := cmds[j].Wait(); err != nil {
					t.Errorf("worker %d: %v: %s", j, err, outputs[j].String())
				}
			}
		}
	}
	rows, err := a.localProjects()
	if err != nil || len(rows) != 1 || rows[0].LastMigration != "60" {
		t.Fatalf("lost concurrent writes: %+v %v", rows, err)
	}
}
func TestLocalRegistryProcessWorker(t *testing.T) {
	home := os.Getenv("VECNET_REGISTRY_TEST_HOME")
	if home == "" {
		t.Skip("subprocess only")
	}
	a := &app{ctx: context.Background(), getenv: func(k string) string {
		if k == "HOME" {
			return home
		}
		return ""
	}}
	for range 10 {
		if err := a.updateLocalProject(os.Getenv("VECNET_REGISTRY_TEST_ROOT"), func(p *localProject) error {
			n, _ := strconv.Atoi(p.LastMigration)
			p.LastMigration = strconv.Itoa(n + 1)
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestLocalRegistryRejectsInvalidInputWithoutWrites(t *testing.T) {
	for _, home := range []string{"", "relative", "/bad\npath"} {
		t.Run(strconv.Quote(home), func(t *testing.T) {
			a, _, _ := localRegistryFixture(t)
			a.getenv = func(k string) string {
				if k == "HOME" {
					return home
				}
				return ""
			}
			if _, err := a.localProjects(); err == nil {
				t.Fatal("accepted invalid HOME")
			}
			if _, err := a.observeLocalProject("v1", localRegistryNow()); err == nil {
				t.Fatal("observed invalid HOME")
			}
		})
	}
	a, root, home := localRegistryFixture(t)
	if _, err := a.observeLocalProject("v1", time.Time{}); err == nil {
		t.Fatal("accepted zero timestamp")
	}
	if _, err := os.Stat(home); !os.IsNotExist(err) {
		t.Fatal("invalid observation created files")
	}
	if _, err := a.observeLocalProject("v1", localRegistryNow()); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(localRegistryInventory(home))
	if err := a.updateLocalProject(filepath.Join(root, "missing"), func(p *localProject) error { return nil }); err == nil {
		t.Fatal("updated unknown project")
	}
	if err := a.registerLocalArtifact("relative.json", "codex-hooks", "v1", localRegistryNow()); err == nil {
		t.Fatal("accepted relative artifact")
	}
	if err := a.cmdProjects([]string{"forget"}); err == nil {
		t.Fatal("accepted missing forget argument")
	}
	if err := a.cmdProjects([]string{"unknown"}); err == nil {
		t.Fatal("accepted unknown command")
	}
	if after, _ := os.ReadFile(localRegistryInventory(home)); !bytes.Equal(before, after) {
		t.Fatal("invalid operation changed inventory")
	}
}

func TestLocalRegistryArtifactAdoptionPreservesAppliedVersion(t *testing.T) {
	a, _, _ := localRegistryFixture(t)
	now := localRegistryNow()
	if _, err := a.observeLocalProject("v1", now); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "hooks.json")
	if err := cliFixtureWriteFile(path, []byte("{}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := a.registerLocalArtifact(path, "codex-hooks", "v1", now); err != nil {
		t.Fatal(err)
	}
	if err := a.registerLocalArtifact(path, "codex-hooks", "legacy", now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	rows, err := a.localProjects()
	if err != nil || len(rows) != 1 || len(rows[0].Artifacts) != 1 {
		t.Fatalf("artifact missing: %+v %v", rows, err)
	}
	if art := rows[0].Artifacts[0]; art.Version != "v1" || !art.InstalledAt.Equal(now) {
		t.Fatalf("adoption reset installation: %+v", art)
	}
	if err := cliFixtureWriteFile(path, []byte("{\"changed\":true}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := a.registerLocalArtifact(path, "codex-hooks", "v2", now.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	rows, err = a.localProjects()
	if err != nil {
		t.Fatal(err)
	}
	if art := rows[0].Artifacts[0]; art.Version != "v2" || !art.InstalledAt.Equal(now.Add(2*time.Hour)) {
		t.Fatalf("changed installation was not recorded: %+v", art)
	}
}

func TestLocalRegistrySavedProjectRootOverridesCWD(t *testing.T) {
	a, root, _ := localRegistryFixture(t)
	other := t.TempDir()
	cliFixtureChdir(t, other)
	a.localProjectRoot = root
	p, err := a.observeLocalProject("v1", localRegistryNow())
	if err != nil {
		t.Fatal(err)
	}
	if p.Root != root || p.Directory != root {
		t.Fatalf("used repair caller cwd: %+v", p)
	}
	path := filepath.Join(t.TempDir(), "hooks.json")
	if err := cliFixtureWriteFile(path, []byte("{}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := a.registerLocalArtifactForProject(root, path, "codex-hooks", "v1", localRegistryNow()); err != nil {
		t.Fatal(err)
	}
	rows, err := a.localProjects()
	if err != nil || len(rows) != 1 || len(rows[0].Artifacts) != 1 {
		t.Fatalf("wrong saved project: %+v %v", rows, err)
	}
	a.localProjectRoot = filepath.Join(root, "missing")
	if _, err := a.observeLocalProject("v1", localRegistryNow()); err == nil {
		t.Fatal("accepted missing saved root")
	}
	if _, err := os.Stat(a.localProjectRoot); !os.IsNotExist(err) {
		t.Fatal("recreated missing saved root")
	}
}

func TestLocalRegistryStoredRootWinsOverRetargetedSymlink(t *testing.T) {
	for _, registeredTarget := range []bool{false, true} {
		t.Run(strconv.FormatBool(registeredTarget), func(t *testing.T) {
			a, root, _ := localRegistryFixture(t)
			now := localRegistryNow()
			if _, err := a.observeLocalProject("v1", now); err != nil {
				t.Fatal(err)
			}
			target, err := localRegistryCanonical(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			cliFixtureChdir(t, target)
			if registeredTarget {
				if _, err := a.observeLocalProject("v1", now); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.Rename(root, root+"-moved"); err != nil {
				t.Fatal(err)
			}
			if err := cliFixtureSymlink(t, target, root); err != nil {
				t.Fatal(err)
			}
			if err := a.updateLocalProject(root, func(p *localProject) error { p.Status = "deferred"; p.Error = "original root retargeted"; return nil }); err != nil {
				t.Fatal(err)
			}
			rows, err := a.localProjects()
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, p := range rows {
				if p.Root == root {
					found = true
					if p.Status != "deferred" {
						t.Fatalf("stored root was not updated: %+v", p)
					}
				} else if p.Root == target && p.Status != "" {
					t.Fatalf("updated symlink target instead: %+v", p)
				}
			}
			if !found {
				t.Fatal("lost original root")
			}
			if err := a.cmdProjects([]string{"forget", root}); err != nil {
				t.Fatal(err)
			}
			rows, err = a.localProjects()
			if err != nil {
				t.Fatal(err)
			}
			want := 0
			if registeredTarget {
				want = 1
			}
			if len(rows) != want || registeredTarget && rows[0].Root != target {
				t.Fatalf("forgot wrong project: %+v", rows)
			}
			if st, err := os.Lstat(root); err != nil || st.Mode()&(os.ModeSymlink|os.ModeIrregular) == 0 {
				t.Fatalf("forget touched root symlink: %v %v", st, err)
			}
		})
	}
}

func TestLocalRegistryStoredRootSurvivesUnresolvableFilesystem(t *testing.T) {
	a, root, _ := localRegistryFixture(t)
	if _, err := a.observeLocalProject("v1", localRegistryNow()); err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(root, root+"-moved"); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS == "windows" {
		target := filepath.Join(t.TempDir(), "junction-target")
		if err := cliFixtureMkdir(target, 0700); err != nil {
			t.Fatal(err)
		}
		if err := cliFixtureSymlink(t, target, root); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(target); err != nil {
			t.Fatal(err)
		}
	} else {
		if err := cliFixtureSymlink(t, root, root); err != nil {
			t.Fatal(err)
		}
	} // EvalSymlinks fails for the loop/dangling reparse point.
	if err := a.updateLocalProject(root, func(p *localProject) error { p.Status = "deferred"; p.Error = "unresolvable root"; return nil }); err != nil {
		t.Fatal(err)
	}
	if err := a.cmdProjects([]string{"forget", root}); err != nil {
		t.Fatal(err)
	}
	rows, err := a.localProjects()
	if err != nil || len(rows) != 0 {
		t.Fatalf("unresolvable root retained: %+v %v", rows, err)
	}
}

func TestLocalRegistryExplicitInstallAdvancesUnchangedArtifactVersion(t *testing.T) {
	a, _, _ := localRegistryFixture(t)
	now := localRegistryNow()
	if _, err := a.observeLocalProject("v1", now); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "hooks.json")
	if err := cliFixtureWriteFile(path, []byte("{}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := a.registerLocalArtifact(path, "codex-hooks", "v1", now); err != nil {
		t.Fatal(err)
	}
	if err := a.registerLocalArtifact(path, "codex-hooks", "v2", now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	rows, err := a.localProjects()
	if err != nil {
		t.Fatal(err)
	}
	art := rows[0].Artifacts[0]
	if art.Version != "v2" || !art.InstalledAt.Equal(now.Add(time.Hour)) {
		t.Fatalf("explicit unchanged install retained stale version: %+v", art)
	}
	if err := cliFixtureWriteFile(path, []byte("{\"changed\":true}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := a.registerLocalArtifact(path, "codex-hooks", "legacy", now.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	rows, err = a.localProjects()
	if err != nil {
		t.Fatal(err)
	}
	if got := rows[0].Artifacts[0]; got != art {
		t.Fatalf("legacy adoption blessed drift/reset installation: %+v vs %+v", got, art)
	}
}
