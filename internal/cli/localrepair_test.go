package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

func TestLocalMaintenanceConcurrentSchedulingAndChildEnvironment(t *testing.T) {
	a, _, home := localRegistryFixture(t)
	var started atomic.Int32
	a.localRepairStart = func(cmd *exec.Cmd) error {
		if len(cmd.Args) != 3 || cmd.Args[1] != "repair" || cmd.Args[2] != "--background" {
			t.Errorf("bad repair child: %#v", cmd.Args)
		}
		for _, entry := range cmd.Env {
			if strings.HasPrefix(entry, "CODEX_THREAD_ID=") || strings.HasPrefix(entry, "VECNET_ENROL_TOKEN=") || strings.HasPrefix(entry, "VECNET_PROFILE=") {
				t.Errorf("child inherited session/credential: %s", entry)
			}
		}
		if !strings.Contains(strings.Join(cmd.Env, "\n"), "VECNET_LOCAL_MAINTENANCE=0") {
			t.Error("child can recurse")
		}
		started.Add(1)
		return nil
	}
	a.getenv = func(k string) string {
		switch k {
		case "HOME":
			return home
		case "CODEX_THREAD_ID":
			return bindT1
		case "VECNET_ENROL_TOKEN":
			return "not-for-child"
		case "VECNET_PROFILE":
			return "p"
		case "PATH":
			return "/usr/bin"
		}
		return ""
	}
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); a.scheduleLocalRepair() }()
	}
	wg.Wait()
	if n := started.Load(); n != 1 {
		t.Fatalf("started %d duplicate workers", n)
	}
}

func TestLocalInspectionDoesNotObserveOrSchedulePendingSetup(t *testing.T) {
	for _, args := range [][]string{{"guide"}, {"profile", "show"}, {"profile", "list"}, {"diag"}, {"audit"}, {"prefs", "list"}, {"prefs", "get", "key"}, {"prefs", "set", "key", "value", "--preview"}, {"prefs", "set", "--preview", "--reset", "key"}, {"prefs", "set", "key", "value", "--preview=false"}, {"typo"}} {
		t.Run(strings.Join(args, "-"), func(t *testing.T) {
			a, root, home := localRegistryFixture(t)
			if err := a.saveLocalEnrollment(localEnrollment{IntentID: "pending", Config: filepath.Join(home, "config.toml"), Profile: "p", Directory: root, ThreadID: bindT1, CodexHome: home, State: "pending", UpdatedAt: time.Now()}); err != nil {
				t.Fatal(err)
			}
			started := 0
			a.localRepairStart = func(*exec.Cmd) error { started++; return nil }
			a.localStartup(args)
			projects, err := a.localProjects()
			if err != nil || len(projects) != 0 || started != 0 {
				t.Fatalf("inspection changed installation state: projects=%v started=%d error=%v", projects, started, err)
			}
		})
	}
}

func TestLocalRepairReportsLauncherUpgradePending(t *testing.T) {
	a, root, home := localRegistryFixture(t)
	unit := filepath.Join(home, ".config/systemd/user/vecnet-launcher.service")
	if err := cliFixtureMkdirAll(filepath.Dir(unit), 0700); err != nil {
		t.Fatal(err)
	}
	before := []byte("[Service]\nExecStart=/private/old-launcher\n")
	if runtime.GOOS == "windows" && strings.HasSuffix(unit, ".task.json") {
		before = []byte(`{"schema":1,"managed_by":"unregistered fixture"}`)
	}
	if err := cliFixtureWriteFile(unit, before, 0600); err != nil {
		t.Fatal(err)
	}
	started := 0
	a.localRepairStart = func(*exec.Cmd) error { started++; return nil }
	a.localStartup([]string{"peers"})
	if started != 1 {
		t.Fatalf("installed launcher was omitted from automatic maintenance: %d", started)
	}
	path := filepath.Join(root, "hooks.json")
	if err := cliFixtureWriteFile(path, []byte(`{"hooks":{"UserPromptSubmit":[{"hooks":[{"type":"command","command":"vecnet hook --profile p --event user-prompt","timeout":5}]}]}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := a.registerLocalArtifact(path, "codex-hooks", "old", time.Now()); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	a.stdout = &out
	a.json = true
	if err := a.cmdLocalRepair(nil); err == nil || !strings.Contains(err.Error(), "launcher") {
		t.Fatalf("launcher upgrade falsely completed: error=%v output=%s", err, out.String())
	}
	if !strings.Contains(out.String(), `"state":"pending"`) || !strings.Contains(out.String(), "installer") {
		t.Fatalf("missing actionable launcher upgrade result: %s", out.String())
	}
	if got, err := os.ReadFile(unit); err != nil || !bytes.Equal(before, got) {
		t.Fatalf("replaced launcher without verified migration: %s %v", got, err)
	}
	projects, err := a.localProjects()
	if err != nil || len(projects) != 1 || projects[0].Status != "pending" || projects[0].AppliedVersion != "" {
		t.Fatalf("launcher software incorrectly marked upgraded: %#v %v", projects, err)
	}
}

// Windows pathname Stat may defer file identity lookup until SameFile. Stat an
// open handle so the identity is captured before repair can replace the path.
func localRepairFileIdentity(path string) (os.FileInfo, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	info, statErr := file.Stat()
	closeErr := file.Close()
	if statErr != nil {
		return nil, statErr
	}
	if closeErr != nil {
		return nil, closeErr
	}
	return info, nil
}

func TestLocalRepairFileIdentityRejectsSameContentReplacement(t *testing.T) {
	for _, name := range []string{"vecnet-launcher.service", "vecnet-codex-delivery.service"} {
		t.Run(name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "leftovers")
			if err := cliFixtureMkdir(dir, 0700); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, name)
			raw := []byte("[Service]\nExecStart=/private/old-runtime\n")
			if err := cliFixtureWriteFile(path, raw, 0600); err != nil {
				t.Fatal(err)
			}
			before, err := localRepairFileIdentity(path)
			if err != nil {
				t.Fatal(err)
			}
			permissions, err := cliFixturePermissions(path)
			if err != nil {
				t.Fatal(err)
			}
			// Keep the old object alive so identity cannot be reused, while
			// replacing its pathname with identical content and security.
			if err := os.Rename(path, path+".original"); err != nil {
				t.Fatal(err)
			}
			if err := cliFixtureWriteFile(path, raw, 0600); err != nil {
				t.Fatal(err)
			}
			after, err := localRepairFileIdentity(path)
			if err != nil {
				t.Fatal(err)
			}
			if os.SameFile(before, after) {
				t.Error("saved leftover identity followed a same-content replacement")
			}
			if got, err := os.ReadFile(path); err != nil || !bytes.Equal(got, raw) {
				t.Fatalf("replacement content differs: %q %v", got, err)
			}
			if got, err := cliFixturePermissions(path); err != nil || got != permissions {
				t.Fatalf("replacement security differs: %q want=%q err=%v", got, permissions, err)
			}
		})
	}
}

func TestLocalRepairPrefersStandaloneWithLeftoverLauncherUnit(t *testing.T) {
	uncertain := errors.New("fixture task inventory is uncertain")
	for _, tc := range []struct {
		name       string
		tasks      []string
		inspectErr error
		wantReason string
	}{
		{name: "empty foreground"},
		{name: "uncertain", inspectErr: uncertain, wantReason: uncertain.Error()},
		{name: "unregistered", tasks: []string{"vecnet-launcher-p-0123456789abcdef"}, wantReason: "Windows profile launcher task is not registered"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, _, home := localRegistryFixture(t)
			before := []byte("[Service]\nExecStart=/private/old-runtime\n")
			type unitSnapshot struct {
				info        os.FileInfo
				permissions string
			}
			units := map[string]unitSnapshot{}
			for _, name := range []string{"vecnet-launcher.service", "vecnet-codex-delivery.service"} {
				path := filepath.Join(home, ".config/systemd/user", name)
				if err := cliFixtureMkdirAll(filepath.Dir(path), 0700); err != nil {
					t.Fatal(err)
				}
				if err := cliFixtureWriteFile(path, before, 0600); err != nil {
					t.Fatal(err)
				}
				info, err := localRepairFileIdentity(path)
				if err != nil {
					t.Fatal(err)
				}
				permissions, err := cliFixturePermissions(path)
				if err != nil {
					t.Fatal(err)
				}
				units[path] = unitSnapshot{info, permissions}
			}
			type contextKey struct{}
			parent := context.WithValue(t.Context(), contextKey{}, "private repair")
			a.ctx = parent
			var runtimeCtx context.Context
			runtimeCalls, inventoryCalls := 0, 0
			a.localRuntime = func(ctx context.Context) error {
				runtimeCalls++
				runtimeCtx = ctx
				return nil
			}
			a.localLauncherTasks = func(ctx context.Context) ([]string, error) {
				inventoryCalls++
				if runtimeCalls != 1 || ctx != runtimeCtx || ctx.Value(contextKey{}) != "private repair" || ctx.Err() != nil {
					t.Errorf("inventory lost runtime selection/context: runtime=%d same=%t error=%v", runtimeCalls, ctx == runtimeCtx, ctx.Err())
				}
				if ctx != parent {
					t.Error("foreground inventory context changed")
				}
				return tc.tasks, tc.inspectErr
			}
			err := a.cmdLocalRepair(nil)
			if runtimeCalls != 1 || inventoryCalls != 1 || a.ctx != parent || parent.Err() != nil {
				t.Errorf("repair selection/worker copy: runtime=%d inventory=%d error=%v", runtimeCalls, inventoryCalls, err)
			}
			wantState := "ready"
			if tc.wantReason == "" {
				if err != nil {
					t.Errorf("standalone repair was blocked by leftover launcher: %v", err)
				}
			} else {
				wantState = "pending"
				if err == nil || !strings.Contains(err.Error(), tc.wantReason) {
					t.Errorf("inventory refusal lost: error=%v want=%q", err, tc.wantReason)
				}
			}
			if tc.inspectErr != nil && !errors.Is(err, tc.inspectErr) {
				t.Errorf("inventory error identity lost: %v", err)
			}
			raw, err := a.readLocalState("repair-status.json")
			if err != nil {
				t.Fatal(err)
			}
			var report localRepairReport
			if err := json.Unmarshal(raw, &report); err != nil {
				t.Fatal(err)
			}
			if report.State != wantState || tc.wantReason == "" && report.Reason != "" || tc.wantReason != "" && !strings.Contains(report.Reason, tc.wantReason) {
				t.Errorf("durable repair state=%q reason=%q want=%q/%q", report.State, report.Reason, wantState, tc.wantReason)
			}
			for path, snapshot := range units {
				got, err := os.ReadFile(path)
				if err != nil || !bytes.Equal(got, before) {
					t.Errorf("repair changed leftover unit %s: %q %v", path, got, err)
				}
				info, err := os.Stat(path)
				if err != nil || !os.SameFile(info, snapshot.info) {
					t.Errorf("repair replaced leftover unit %s: %v", path, err)
				}
				permissions, err := cliFixturePermissions(path)
				if err != nil || permissions != snapshot.permissions {
					t.Errorf("repair changed leftover unit permissions %s: %v", path, err)
				}
			}
		})
	}
}

func TestLocalRepairReportsUnregisteredProfileLauncher(t *testing.T) {
	a, _, home := localRegistryFixture(t)
	// This is a private filesystem/inventory fixture, not a native Task
	// Scheduler contract test. It must not read or mutate host tasks.
	// The project is a plain directory; host Git startup is not part of the
	// bounded launcher-discovery check.
	t.Setenv("PATH", t.TempDir())
	home = filepath.Join(filepath.Dir(home), "operator[1]")
	a.getenv = func(k string) string {
		if k == "HOME" {
			return home
		}
		return ""
	}
	unit := filepath.Join(home, ".config/systemd/user/vecnet-launcher-p-0123456789abcdef.service")
	if runtime.GOOS == "windows" {
		unit = filepath.Join(home, ".config", "vecnet", "launcher", "p-0123456789abcdef", "vecnet-launcher-p-0123456789abcdef.task.json")
	}
	if err := cliFixtureMkdirAll(filepath.Dir(unit), 0700); err != nil {
		t.Fatal(err)
	}
	before := []byte("[Service]\nExecStart=/private/old-launcher\n")
	if runtime.GOOS == "windows" {
		before = []byte(`{"schema":1,"managed_by":"unregistered fixture"}`)
	}
	if err := cliFixtureWriteFile(unit, before, 0600); err != nil {
		t.Fatal(err)
	}
	// Capture identity from an opened file: on Windows, path-based Stat
	// may defer reading the file ID until SameFile is called.
	unitInfo, err := localRepairFileIdentity(unit)
	if err != nil {
		t.Fatal(err)
	}
	// Prepare the cached executable identity outside the launcher-discovery check.
	localSoftwareVersion()
	started := 0
	a.localRepairStart = func(*exec.Cmd) error { started++; return nil }
	a.localStartup([]string{"peers"})
	if started != 1 {
		t.Fatalf("profile launcher omitted from maintenance: %d", started)
	}
	if err := a.cmdLocalRepair(nil); err == nil || !strings.Contains(err.Error(), "not registered") {
		t.Fatalf("unregistered profile launcher falsely upgraded: %v", err)
	}
	if got, err := os.ReadFile(unit); err != nil || !bytes.Equal(got, before) {
		t.Fatalf("unregistered unit changed: %s %v", got, err)
	}
	if info, err := os.Stat(unit); err != nil || !os.SameFile(unitInfo, info) {
		t.Fatalf("unregistered unit replaced: %v", err)
	}
}

func TestLocalLauncherInspectionFailureRemainsPending(t *testing.T) {
	if runtime.GOOS != "windows" && os.Geteuid() == 0 {
		t.Skip("root can read directories despite permission denial")
	}
	a, _, home := localRegistryFixture(t)
	dir := filepath.Join(home, ".config/systemd/user")
	if runtime.GOOS == "windows" {
		dir = filepath.Join(home, ".config", "vecnet", "launcher")
	}
	if err := cliFixtureMkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	cliFixtureDenyDirectoryRead(t, dir)
	if !localLauncherPresent(home) {
		t.Fatal("uncertain launcher discovery did not schedule inspection")
	}
	if err := a.unregisteredLocalLaunchers(); err == nil {
		t.Fatal("unreadable launcher directory was reported absent")
	}
	if err := a.cmdLocalRepair(nil); err == nil {
		t.Fatal("unreadable launcher directory falsely repaired")
	}
}

func TestLocalMaintenanceFailedStartRemainsRetryable(t *testing.T) {
	a, _, _ := localRegistryFixture(t)
	a.localRepairStart = func(*exec.Cmd) error { return errors.New("failed") }
	a.scheduleLocalRepair()
	if _, err := a.readLocalState("repair-requested.json"); !os.IsNotExist(err) {
		t.Fatalf("failed start throttled retry: %v", err)
	}
	var count int
	a.localRepairStart = func(*exec.Cmd) error { count++; return nil }
	a.scheduleLocalRepair()
	if count != 1 {
		t.Fatal("did not retry")
	}
}
func TestLocalRepairBackgroundDoesNotQueue(t *testing.T) {
	a, _, _ := localRegistryFixture(t)
	unlock, err := a.lockLocalState(context.Background(), "repair")
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	start := time.Now()
	if err := a.cmdLocalRepair([]string{"--background"}); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > time.Second {
		t.Fatal("background worker queued behind in-flight repair")
	}
}
func TestLocalRepairDoesNotAdvanceAppliedVersionOnRuntimeFailure(t *testing.T) {
	a, root, _ := localRegistryFixture(t)
	path := filepath.Join(root, "hooks.json")
	raw := []byte(`{"hooks":{"UserPromptSubmit":[{"hooks":[{"type":"command","command":"vecnet hook --profile p --event user-prompt","timeout":5}]}]}}`)
	if err := cliFixtureWriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err := a.registerLocalArtifact(path, "codex-hooks", "old", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := a.repairLocalProjectsAfterRuntime(errors.New("upgrade failed")); err == nil {
		t.Fatal("runtime failure hidden")
	}
	projects, err := a.localProjects()
	if err != nil {
		t.Fatal(err)
	}
	if p := projects[0]; p.Status != "pending" || p.AppliedVersion != "" || p.LastMigration != "" {
		t.Fatalf("false complete migration: %#v", p)
	}
	after, _ := os.ReadFile(path)
	if bytes.Contains(after, []byte("--profile p")) {
		t.Fatal("successful file stage was not retained")
	}
}
func TestLocalLegacyRetirementRejectsUnrecognizedEmptySchema(t *testing.T) {
	for _, raw := range []string{`{"version":1}`, `{"version":1,"bindings":null}`, `{"version":1,"bindings":[],"owners":[1]}`, `{"version":2,"bindings":[]}`} {
		t.Run(raw, func(t *testing.T) {
			f := newBindFixture(t)
			legacy := filepath.Join(filepath.Dir(f.env["VECNET_CONFIG"]), "codex/bindings.json")
			if err := cliFixtureMkdirAll(filepath.Dir(legacy), 0700); err != nil {
				t.Fatal(err)
			}
			if err := cliFixtureWriteFile(legacy, []byte(raw), 0600); err != nil {
				t.Fatal(err)
			}
			a := &app{ctx: context.Background(), stdout: &bytes.Buffer{}, stderr: &bytes.Buffer{}, getenv: func(k string) string { return f.env[k] }}
			if err := a.retireEmptyLegacySources(); err == nil {
				t.Fatal("retired unrecognized schema")
			}
			if after, err := os.ReadFile(legacy); err != nil || string(after) != raw {
				t.Fatalf("source changed: %s %v", after, err)
			}
		})
	}
}

func TestLocalRepairStatusReadOnlyAndPendingExit(t *testing.T) {
	a, _, home := localRegistryFixture(t)
	a.json = true
	var out bytes.Buffer
	a.stdout = &out
	if err := a.cmdLocalRepair([]string{"--status"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"state": "not_run"`) && !strings.Contains(out.String(), `"state":"not_run"`) {
		t.Fatalf("%s", out.String())
	}
	if _, err := os.Stat(filepath.Join(home, ".local")); !os.IsNotExist(err) {
		t.Fatal("status created local state")
	}
	if _, err := a.observeLocalProject("old", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := a.updateLocalProject(mustLocalProjectRoot(t, a), func(p *localProject) error {
		p.Artifacts = append(p.Artifacts, localArtifact{Path: filepath.Join(p.Root, "missing"), Kind: "future-kind", Version: "old", Schema: 1, Hash: strings.Repeat("a", 64), InstalledAt: time.Now()})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := a.cmdLocalRepair(nil); err == nil {
		t.Fatal("JSON mode concealed pending repair failure")
	}
	if !strings.Contains(out.String(), "pending") {
		t.Fatal(out.String())
	}
}
func mustLocalProjectRoot(t *testing.T, a *app) string {
	t.Helper()
	p, err := a.localProjects()
	if err != nil || len(p) == 0 {
		t.Fatalf("%#v %v", p, err)
	}
	return p[0].Root
}

func TestLocalRepairConcurrentAdoptionRemainsPending(t *testing.T) {
	a, root, _ := localRegistryFixture(t)
	for _, name := range []string{"x.json", "y.json"} {
		path := filepath.Join(root, name)
		if err := cliFixtureWriteFile(path, []byte(`{"hooks":{}}`), 0600); err != nil {
			t.Fatal(err)
		}
		version := localSoftwareVersion()
		if name == "y.json" {
			version = "legacy"
		}
		if err := a.registerLocalArtifact(path, "codex-hooks", version, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	projects, err := a.localProjects()
	if err != nil {
		t.Fatal(err)
	}
	completed := map[string]localArtifactRepair{}
	for _, art := range projects[0].Artifacts {
		if filepath.Base(art.Path) == "x.json" {
			completed[art.Path] = localArtifactRepair{Before: art, After: art}
		}
	}
	why, err := a.finishLocalProjectRepair(root, completed, "ready", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(why, "changed during repair") {
		t.Fatalf("concurrent adoption falsely completed: %q", why)
	}
	projects, err = a.localProjects()
	if err != nil {
		t.Fatal(err)
	}
	if p := projects[0]; p.Status != "pending" || p.AppliedVersion != "" || p.LastMigration != "" {
		t.Fatalf("%#v", p)
	}
}

func TestLocalStartupObservesWithoutInstallingAndAdoptsLegacyHooks(t *testing.T) {
	// Use logical time for observation/adoption assertions so cold private
	// filesystem I/O cannot consume the production 200ms startup budget.
	// synctest advances time only when the bubble is durably blocked.
	synctest.Test(t, func(t *testing.T) {
		a, root, _ := localRegistryFixture(t)
		// This is a plain-directory project. Do not spend its bounded startup
		// budget launching the host Git just to report that it is not a repo.
		t.Setenv("PATH", t.TempDir())
		var started int
		a.localRepairStart = func(*exec.Cmd) error { started++; return nil }
		a.localStartup([]string{"peers"})
		projects, err := a.localProjects()
		if err != nil || len(projects) != 1 {
			t.Fatalf("%#v %v", projects, err)
		}
		if p := projects[0]; p.ObservedVersion != localSoftwareVersion() || p.AppliedVersion != "" || len(p.Artifacts) != 0 {
			t.Fatalf("observation changed installation state: %#v", p)
		}
		if started != 0 {
			t.Fatal("inventory-only observation installed integration")
		}
		path := filepath.Join(root, ".codex/hooks.json")
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatal("observation installed hooks")
		}
		if err := cliFixtureMkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		raw := []byte(`{"hooks":{"UserPromptSubmit":[{"hooks":[{"type":"command","command":"vecnet hook --profile old --event user-prompt","timeout":5}]}]}}`)
		if err := cliFixtureWriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
		a.localStartup([]string{"peers"})
		if started != 1 {
			t.Fatalf("recognized stale integration did not schedule repair: %d", started)
		}
		projects, err = a.localProjects()
		if err != nil || len(projects) != 1 {
			t.Fatalf("%#v %v", projects, err)
		}
		if len(projects[0].Artifacts) != 1 || projects[0].Artifacts[0].Version != "legacy" || projects[0].Artifacts[0].Path != path || projects[0].Artifacts[0].Kind != "codex-hooks" {
			t.Fatalf("%#v", projects)
		}
		if got, err := os.ReadFile(path); err != nil || !bytes.Equal(got, raw) {
			t.Fatalf("adoption changed hooks before repair: %q %v", got, err)
		}
		if err := a.repairLocalProjects(); err != nil {
			t.Fatal(err)
		}
		if got, err := os.ReadFile(path); err != nil || bytes.Contains(got, []byte("--profile old")) || !hasOwnedCodexHooks(got, localBinary()) {
			t.Fatalf("legacy hooks were not repaired: %q %v", got, err)
		}
		projects, err = a.localProjects()
		if err != nil || len(projects) != 1 {
			t.Fatalf("%#v %v", projects, err)
		}
		if p := projects[0]; p.Status != "ready" || p.AppliedVersion != localSoftwareVersion() || len(p.Artifacts) != 1 || p.Artifacts[0].Version != localSoftwareVersion() {
			t.Fatalf("repair did not publish applied integration: %#v", p)
		}
	})
}

func TestManagedHookRepairRejectsConcurrentUserEdit(t *testing.T) {
	a, root, _ := localRegistryFixture(t)
	path := filepath.Join(root, "hooks.json")
	before := []byte(`{"hooks":{"UserPromptSubmit":[{"hooks":[{"type":"command","command":"vecnet hook --profile old --event user-prompt","timeout":5}]}]}}`)
	changed := []byte(`{"custom":"user removed managed hooks","hooks":{}}`)
	if err := cliFixtureWriteFile(path, changed, 0600); err != nil {
		t.Fatal(err)
	}
	err := a.installCodexHooksExpected(path, "vecnet", opts{vals: map[string][]string{"write": {}}}, hookDigest(before))
	if err == nil || !strings.Contains(err.Error(), "changed before migration") {
		t.Fatalf("unattended repair lost inspected preimage: %v", err)
	}
	if got, _ := os.ReadFile(path); !bytes.Equal(got, changed) {
		t.Fatalf("overwrote user edit: %s", got)
	}
	backups, _ := filepath.Glob(path + ".vecnet-backup-*")
	if len(backups) != 0 {
		t.Fatal("created backup/migration for unapproved preimage")
	}
}

func TestLocalRepairPreservesConcurrentSamePathInstallation(t *testing.T) {
	a, root, _ := localRegistryFixture(t)
	path := filepath.Join(root, "hooks.json")
	if err := cliFixtureWriteFile(path, []byte(`{"hooks":{}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := a.registerLocalArtifact(path, "codex-hooks", "newer", time.Now()); err != nil {
		t.Fatal(err)
	}
	projects, err := a.localProjects()
	if err != nil {
		t.Fatal(err)
	}
	current := projects[0].Artifacts[0]
	old := current
	old.Hash = strings.Repeat("b", 64)
	old.Version = "older"
	why, err := a.finishLocalProjectRepair(root, map[string]localArtifactRepair{path: {Before: old, After: old}}, "ready", "", nil)
	if err != nil || why == "" {
		t.Fatalf("%q %v", why, err)
	}
	projects, err = a.localProjects()
	if err != nil {
		t.Fatal(err)
	}
	got := projects[0].Artifacts[0]
	if got.Hash != current.Hash || got.Version != current.Version {
		t.Fatalf("lost newer installation metadata: %#v", got)
	}
}
func TestLocalStartupDefersFileIntegrityToPeriodicRepair(t *testing.T) {
	a, root, _ := localRegistryFixture(t)
	if _, err := a.observeLocalProject(localSoftwareVersion(), time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := a.updateLocalProject(root, func(p *localProject) error {
		// Deliberately inaccessible/missing managed targets: foreground must not
		// inspect every registered file before dispatching an ordinary command.
		for i := 0; i < 100; i++ {
			p.Artifacts = append(p.Artifacts, localArtifact{Path: filepath.Join(root, "missing", time.Unix(int64(i), 0).Format("150405")), Kind: "codex-hooks", Version: localSoftwareVersion(), Hash: strings.Repeat("a", 64), Schema: 1, InstalledAt: time.Now()})
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	last := localRepairReport{localSetupResult: localSetupResult{State: "ready"}, Version: localSoftwareVersion(), BinarySHA256: localBinarySHA256, AttemptedAt: time.Now()}
	if err := a.writeLocalState("repair-status.json", mustLocalJSON(last)); err != nil {
		t.Fatal(err)
	}
	var starts int
	a.localRepairStart = func(*exec.Cmd) error { starts++; return nil }
	a.localStartup([]string{"peers"})
	if starts != 0 {
		t.Fatal("foreground scanned registered files despite a fresh repair checkpoint")
	}
	last.AttemptedAt = time.Now().Add(-11 * time.Minute)
	if err := a.writeLocalState("repair-status.json", mustLocalJSON(last)); err != nil {
		t.Fatal(err)
	}
	a.localStartup([]string{"peers"})
	if starts != 1 {
		t.Fatal("periodic integrity work was not scheduled")
	}
}

// Exercise the complete caller, including publication of the helper's new
// artifact generation into the project registry.
func TestLocalRepairPublishesLauncherReplacement(t *testing.T) {
	_, a, root, before, _ := launcherRepairFixture(t, false)
	if err := a.repairLocalProjectsAfterRuntime(nil); err != nil {
		t.Fatal(err)
	}
	projects, err := a.localProjects()
	if err != nil {
		t.Fatal(err)
	}
	for _, project := range projects {
		if project.Root != root {
			continue
		}
		if project.Status != "ready" || project.AppliedVersion != localSoftwareVersion() || project.AppliedDigest == "" {
			t.Fatalf("project did not converge: %#v", project)
		}
		for _, artifact := range project.Artifacts {
			if artifact.Path != before.Path {
				continue
			}
			if artifact.Hash == before.Hash || artifact.Hash != launcherHash(launcherTestRead(t, artifact.Path)) || artifact.Version != localSoftwareVersion() {
				t.Fatalf("replacement not published: %#v", artifact)
			}
			if err := a.repairLocalProjectsAfterRuntime(nil); err != nil {
				t.Fatalf("repeat repair: %v", err)
			}
			return
		}
	}
	t.Fatal("launcher registration disappeared")
}

func TestLocalRepairCompletionRequiresInspectedGeneration(t *testing.T) {
	for _, field := range []string{"hash", "version", "kind", "installed-at", "removed", "error"} {
		t.Run(field, func(t *testing.T) {
			a, root, _ := localRegistryFixture(t)
			path := filepath.Join(root, "managed.json")
			if err := cliFixtureWriteFile(path, []byte("old"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := a.registerLocalArtifact(path, "launcher-profile", "old", time.Now()); err != nil {
				t.Fatal(err)
			}
			projects, err := a.localProjects()
			if err != nil {
				t.Fatal(err)
			}
			before := projects[0].Artifacts[0]
			after := before
			after.Hash, after.Version = strings.Repeat("a", 64), "new"
			current := before
			switch field {
			case "hash":
				current.Hash = strings.Repeat("b", 64)
			case "version":
				current.Version = "concurrent"
			case "kind":
				current.Kind = "codex-hooks"
			case "installed-at":
				current.InstalledAt = current.InstalledAt.Add(time.Second)
			}
			if err := a.updateLocalProject(root, func(p *localProject) error {
				if field == "removed" {
					p.Artifacts = []localArtifact{}
				} else {
					p.Artifacts[0] = current
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			status, why := "ready", ""
			if field == "error" {
				status, why = "pending", "helper failure"
			}
			why, err = a.finishLocalProjectRepair(root, map[string]localArtifactRepair{path: {Before: before, After: after}}, status, why, nil)
			if err != nil || why == "" {
				t.Fatalf("concurrent generation/error accepted: %q %v", why, err)
			}
			projects, err = a.localProjects()
			if err != nil {
				t.Fatal(err)
			}
			project := projects[0]
			if project.Status != "pending" || project.AppliedDigest != "" {
				t.Fatalf("incorrect completion: %#v", project)
			}
			if field == "removed" {
				if len(project.Artifacts) != 0 {
					t.Fatal("resurrected removed artifact")
				}
				return
			}
			if len(project.Artifacts) != 1 || !sameLocalArtifact(project.Artifacts[0], current) {
				t.Fatalf("overwrote concurrent evidence: %#v", project.Artifacts)
			}
		})
	}
}
