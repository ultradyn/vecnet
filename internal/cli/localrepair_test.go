package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
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
	if err := os.MkdirAll(filepath.Dir(unit), 0700); err != nil {
		t.Fatal(err)
	}
	before := []byte("[Service]\nExecStart=/private/old-launcher\n")
	if err := os.WriteFile(unit, before, 0600); err != nil {
		t.Fatal(err)
	}
	started := 0
	a.localRepairStart = func(*exec.Cmd) error { started++; return nil }
	a.localStartup([]string{"peers"})
	if started != 1 {
		t.Fatalf("installed launcher was omitted from automatic maintenance: %d", started)
	}
	path := filepath.Join(root, "hooks.json")
	if err := os.WriteFile(path, []byte(`{"hooks":{"UserPromptSubmit":[{"hooks":[{"type":"command","command":"vecnet hook --profile p --event user-prompt","timeout":5}]}]}}`), 0600); err != nil {
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
func TestLocalRepairPrefersStandaloneWithLeftoverLauncherUnit(t *testing.T) {
	a, _, home := localRegistryFixture(t)
	for _, name := range []string{"vecnet-launcher.service", "vecnet-codex-delivery.service"} {
		path := filepath.Join(home, ".config/systemd/user", name)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("[Service]\nExecStart=/private/old-runtime\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	calls := 0
	a.localRuntime = func(context.Context) error { calls++; return nil }
	if err := a.cmdLocalRepair(nil); err != nil || calls != 1 {
		t.Fatalf("standalone repair was blocked by leftover launcher: calls=%d error=%v", calls, err)
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
	if err := os.WriteFile(path, raw, 0600); err != nil {
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
			if err := os.MkdirAll(filepath.Dir(legacy), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(legacy, []byte(raw), 0600); err != nil {
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
		if err := os.WriteFile(path, []byte(`{"hooks":{}}`), 0600); err != nil {
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
	completed := map[string]localArtifact{}
	for _, art := range projects[0].Artifacts {
		if filepath.Base(art.Path) == "x.json" {
			completed[art.Path] = art
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
	a, root, _ := localRegistryFixture(t)
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
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	raw := []byte(`{"hooks":{"UserPromptSubmit":[{"hooks":[{"type":"command","command":"vecnet hook --profile old --event user-prompt","timeout":5}]}]}}`)
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	a.localStartup([]string{"peers"})
	if started != 1 {
		t.Fatalf("recognized stale integration did not schedule repair: %d", started)
	}
	projects, err = a.localProjects()
	if err != nil {
		t.Fatal(err)
	}
	if len(projects[0].Artifacts) != 1 || projects[0].Artifacts[0].Version != "legacy" {
		t.Fatalf("%#v", projects)
	}
}

func TestManagedHookRepairRejectsConcurrentUserEdit(t *testing.T) {
	a, root, _ := localRegistryFixture(t)
	path := filepath.Join(root, "hooks.json")
	before := []byte(`{"hooks":{"UserPromptSubmit":[{"hooks":[{"type":"command","command":"vecnet hook --profile old --event user-prompt","timeout":5}]}]}}`)
	changed := []byte(`{"custom":"user removed managed hooks","hooks":{}}`)
	if err := os.WriteFile(path, changed, 0600); err != nil {
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
	if err := os.WriteFile(path, []byte(`{"hooks":{}}`), 0600); err != nil {
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
	why, err := a.finishLocalProjectRepair(root, map[string]localArtifact{path: old}, "ready", "", nil)
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
