//go:build windows

package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/platform"
	"golang.org/x/sys/windows"
)

type launcherFakeTasks struct {
	state                     launcherTaskState
	calls                     []string
	failRegister, failRestart bool
}

func (m *launcherFakeTasks) Inspect(ctx context.Context, name string) (launcherTaskState, error) {
	if err := ctx.Err(); err != nil {
		return launcherTaskState{}, err
	}
	m.calls = append(m.calls, "inspect "+name)
	return m.state, nil
}
func (m *launcherFakeTasks) Register(ctx context.Context, def launcherTaskDefinition, enabled bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.calls = append(m.calls, fmt.Sprintf("register %s enabled=%t", def.Name, enabled))
	if m.failRegister {
		return errors.New("injected registration failure")
	}
	m.state.Exists = true
	m.state.Definition = def
	m.state.Enabled = enabled
	return nil
}
func (m *launcherFakeTasks) Restart(ctx context.Context, name string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.calls = append(m.calls, "restart "+name)
	m.state.Active = false
	if m.failRestart {
		return errors.New("injected restart failure")
	}
	m.state.Active = true
	return nil
}
func launcherUseFakeTasks(t *testing.T) *launcherFakeTasks {
	t.Helper()
	manager := &launcherFakeTasks{}
	original := newLauncherTaskController
	newLauncherTaskController = func(*app) (launcherTaskController, error) { return manager, nil }
	t.Cleanup(func() { newLauncherTaskController = original })
	return manager
}
func launcherWindowsFixture(t *testing.T, nodeMode launcherFixtureNodeMode) (launcherFixture, *app, *launcherFakeTasks, []string) {
	t.Helper()
	manager := launcherUseFakeTasks(t)
	f := newLauncherFixture(t, "launcher")
	if code, _, errs := f.run(t, append(f.setup, "--write")...); code != 0 {
		t.Fatal(errs)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(f.env["HOME"], "public cli", "vecnet.exe")
	if err = platform.MkdirAll(filepath.Dir(bin), 0o700); err != nil {
		t.Fatal(err)
	}
	file, err := platform.OpenFile(bin, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o700)
	if err != nil {
		t.Fatal(err)
	}
	source, err := os.Open(executable)
	if err != nil {
		t.Fatal(err)
	}
	_, err = copyFixtureImage(file, source)
	source.Close()
	closeErr := file.Close()
	if err != nil {
		t.Fatal(err)
	}
	if closeErr != nil {
		t.Fatal(closeErr)
	}
	nodeCopy, err := launcherFixtureNode(f.env["HOME"], nodeMode)
	if err != nil {
		t.Fatal("stage genuine Windows Node for launcher install tests: ", err)
	}
	args := []string{"install", "--source", launcherTestSource(t), "--node", nodeCopy, "--vecnet-bin", bin}
	start := time.Now()
	code, _, errs := f.run(t, append(args, "--write")...)
	t.Logf("launcher fixture initial install: prepared=%t elapsed=%s exit=%d", nodeMode != coldLauncherNode, time.Since(start), code)
	if code != 0 {
		t.Fatal(errs)
	}
	a := &app{ctx: context.Background(), getenv: func(k string) string { return f.env[k] }, stdout: io.Discard, stderr: io.Discard}
	return f, a, manager, args
}
func launcherWindowsRepairFixture(t *testing.T, active, enabled bool) (launcherFixture, *app, *launcherFakeTasks, string, localArtifact) {
	t.Helper()
	f, a, manager, _ := launcherWindowsFixture(t, preparedLauncherNode)
	path := filepath.Join(filepath.Dir(f.paths.Manifest), "managed.json")
	var meta launcherManaged
	if err := json.Unmarshal(launcherTestRead(t, path), &meta); err != nil {
		t.Fatal(err)
	}
	meta.SourceDefault = true
	root, err := launcherOpenDir(filepath.Dir(path), filepath.Dir(path), false)
	if err != nil {
		t.Fatal(err)
	}
	if err = launcherDurableWrite(root, filepath.Base(path), launcherManagedJSON(meta), 0o600); err != nil {
		t.Fatal(err)
	}
	root.Close()
	project := meta.ProjectRoots[0]
	if err = a.registerLocalArtifactForProject(project, path, "launcher-profile", meta.Version, time.Now()); err != nil {
		t.Fatal(err)
	}
	projects, err := a.localProjects()
	if err != nil {
		t.Fatal(err)
	}
	var artifact localArtifact
	for _, p := range projects {
		if p.Root == project {
			for _, art := range p.Artifacts {
				if art.Path == path {
					artifact = art
				}
			}
		}
	}
	if artifact.Path == "" {
		t.Fatal("launcher registration missing")
	}
	manager.state.Active = active
	manager.state.Enabled = enabled
	manager.calls = nil
	return f, a, manager, project, artifact
}
func TestLauncherWindowsNativeExecutableLookup(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"node.exe", "vecnet.EXE", "node.cmd", "vecnet.ps1"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("not invoked"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, raw := range []string{"node", "node.exe", filepath.Join(dir, "node"), "vecnet.EXE"} {
		path, err := launcherExecutable(raw, dir)
		if err != nil || !strings.EqualFold(filepath.Ext(path), ".exe") {
			t.Fatalf("native lookup %q: %s %v", raw, path, err)
		}
	}
	for _, raw := range []string{"node.cmd", "vecnet.ps1", filepath.Join(dir, "node.cmd")} {
		if _, err := launcherExecutable(raw, dir); err == nil {
			t.Fatal("accepted shell wrapper: ", raw)
		}
	}
	if _, err := launcherExecutable("node", ""); err == nil {
		t.Fatal("searched ambient PATH/current directory")
	}
}
func TestLauncherWindowsProfileHomeAndTaskIdentity(t *testing.T) {
	f := newLauncherFixture(t, "launcher")
	delete(f.env, "HOME")
	a := &app{net: "launcher/test", getenv: func(k string) string { return f.env[k] }}
	p, err := a.launcherPaths()
	if err != nil {
		t.Fatal(err)
	}
	if p != f.paths || !strings.HasSuffix(p.Unit, ".task.json") || filepath.Dir(p.Unit) != filepath.Dir(p.Config) {
		t.Fatalf("native home/profile paths changed: %+v", p)
	}
	if _, err = launcherOpenDir(`\\server\share\private`, "", true); err == nil {
		t.Fatal("UNC managed state accepted")
	}
}
func TestLauncherWindowsTaskDefinitionIsLiteralAndComplete(t *testing.T) {
	f := newLauncherFixture(t, "launcher")
	m := launcherInstall{Node: `C:\Program Files\nodejs\node.exe`, Entry: filepath.Join(f.paths.Data, "%$ & ' literal", "main.ts"), VecnetBin: filepath.Join(f.env["HOME"], "a & %$ cli", "vecnet.exe")}
	raw, err := launcherWindowsUnit(m, f.paths.Config, f.env["VECNET_CONFIG"], f.env["HOME"], `C:\Program Files\nodejs;C:\Windows\System32`)
	if err != nil {
		t.Fatal(err)
	}
	def, err := launcherDecodeTask([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if def.Executable != m.Node || !reflect.DeepEqual(def.Args, []string{"--disable-warning=ExperimentalWarning", m.Entry, "run", "--config", f.paths.Config}) || def.Environment["VECNET_BIN"] != m.VecnetBin {
		t.Fatalf("lost literal paths: %+v", def)
	}
	for _, field := range []string{"executable", "directory", "environment"} {
		var changed map[string]any
		json.Unmarshal([]byte(raw), &changed)
		delete(changed, field)
		if launcherWindowsManagedUnit(launcherManagedJSON(changed)) {
			t.Fatal("accepted incomplete task: ", field)
		}
	}
	if _, err = launcherWindowsUnit(m, f.paths.Config, f.env["VECNET_CONFIG"], f.env["HOME"], "bad\nPATH"); err == nil {
		t.Fatal("control character environment accepted")
	}
}
func TestLauncherWindowsInstallPreviewAndExplicitIntent(t *testing.T) {
	// This existing exclusive root covers cold admission through the real
	// install's five-second probe. Do not prepare this image in the fixture.
	f, _, manager, args := launcherWindowsFixture(t, coldLauncherNode)
	if manager.state.Active || manager.state.Enabled {
		t.Fatal("implicit install enabled/started task")
	}
	before := len(manager.calls)
	if code, out, errs := f.run(t, args...); code != 0 || strings.Contains(out, "vn_peer_LAUNCHER_TEST") {
		t.Fatalf("preview %d: %s", code, errs)
	}
	if len(manager.calls) != before {
		t.Fatal("preview contacted Task Scheduler")
	}
	if code, _, _ := f.run(t, append(args, "--start")...); code == 0 {
		t.Fatal("start accepted without write")
	}
	if code, _, errs := f.run(t, append(args, "--write", "--start")...); code != 0 {
		t.Fatal(errs)
	}
	if !manager.state.Active || manager.state.Enabled {
		t.Fatal("manual start did not preserve disabled intent")
	}
	if code, _, errs := f.run(t, append(args, "--write", "--enable")...); code != 0 {
		t.Fatal(errs)
	}
	if !manager.state.Active || !manager.state.Enabled {
		t.Fatal("enable disturbed active intent")
	}
	code, out, errs := f.run(t, "status")
	if code != 0 {
		t.Fatal(errs)
	}
	var status struct{ Active, Enabled, Manager string }
	if err := json.Unmarshal([]byte(out), &status); err != nil {
		t.Fatal(err)
	}
	if status.Active != "active" || status.Enabled != "enabled" || status.Manager != "windows-task-scheduler" {
		t.Fatal(out)
	}
	if code, _, errs := f.run(t, "check"); code != 0 {
		t.Fatalf("native installed check: %s", errs)
	}
	for _, path := range []string{f.paths.Config, f.paths.Credential, f.paths.Manifest, f.paths.Unit, filepath.Join(filepath.Dir(f.paths.Manifest), "managed.json")} {
		if err := platform.CheckPath(path, false, true); err != nil {
			t.Fatalf("non-private managed file %s: %v", path, err)
		}
	}
}
func TestLauncherWindowsManagedRepairPreservesServiceIntent(t *testing.T) {
	for _, active := range []bool{false, true} {
		for _, enabled := range []bool{false, true} {
			t.Run(fmt.Sprintf("active=%t/enabled=%t", active, enabled), func(t *testing.T) {
				f, a, manager, project, art := launcherWindowsRepairFixture(t, active, enabled)
				config, credential := launcherTestRead(t, f.paths.Config), launcherTestRead(t, f.paths.Credential)
				projects, err := a.localProjects()
				if err != nil {
					t.Fatal(err)
				}
				done, err := a.repairLauncherArtifact(project, art)
				if err != nil {
					t.Fatal(err)
				}
				if done.Hash == art.Hash || manager.state.Active != active || manager.state.Enabled != enabled {
					t.Fatal("repair lost task intent")
				}
				restarted := strings.Contains(strings.Join(manager.calls, "\n"), "restart ")
				if restarted != active {
					t.Fatalf("restart intent: %v", manager.calls)
				}
				var runtime launcherInstall
				if err = json.Unmarshal(launcherTestRead(t, f.paths.Manifest), &runtime); err != nil {
					t.Fatal(err)
				}
				if filepath.Base(runtime.VecnetBin) != "vecnet.exe" {
					t.Fatal("private CLI missing exe suffix")
				}
				if err = platform.CheckPath(runtime.VecnetBin, false, true); err != nil {
					t.Fatal(err)
				}
				current, err := launcherCurrentBinary()
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(current, launcherTestRead(t, runtime.VecnetBin)) {
					t.Fatal("private CLI not repairing image")
				}
				if !bytes.Equal(config, launcherTestRead(t, f.paths.Config)) || !bytes.Equal(credential, launcherTestRead(t, f.paths.Credential)) {
					t.Fatal("repair changed configuration/credential")
				}
				after, err := a.localProjects()
				if err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(projects, after) {
					t.Fatal("repair raced registry CAS")
				}
				manager.calls = nil
				if _, err = a.repairLauncherArtifact(project, art); err != nil {
					t.Fatal("stale generation failed: ", err)
				}
				if _, err = a.repairLauncherArtifact(project, done); err != nil {
					t.Fatal(err)
				}
				if strings.Contains(strings.Join(manager.calls, "\n"), "register ") || strings.Contains(strings.Join(manager.calls, "\n"), "restart ") {
					t.Fatal("no-op repair mutated task")
				}
			})
		}
	}
}
func TestLauncherWindowsRepairResumesInterruptedTaskPublication(t *testing.T) {
	for _, failure := range []string{"register", "restart"} {
		t.Run(failure, func(t *testing.T) {
			f, a, manager, project, art := launcherWindowsRepairFixture(t, true, false)
			before := launcherTestRead(t, art.Path)
			manager.failRegister = failure == "register"
			manager.failRestart = failure == "restart"
			if _, err := a.repairLauncherArtifact(project, art); err == nil {
				t.Fatal("manager failure advanced repair")
			}
			if !bytes.Equal(before, launcherTestRead(t, art.Path)) {
				t.Fatal("advanced applied ownership")
			}
			pending := filepath.Join(filepath.Dir(art.Path), "repair-pending.json")
			var intent launcherRepairIntent
			if err := json.Unmarshal(launcherTestRead(t, pending), &intent); err != nil {
				t.Fatal(err)
			}
			if !intent.WasActive || intent.Enabled != "disabled" || intent.Checksum != intent.checksum() {
				t.Fatal("lost durable intent")
			}
			manager.failRegister = false
			manager.failRestart = false
			if _, err := a.repairLauncherArtifact(project, art); err != nil {
				t.Fatal(err)
			}
			if !manager.state.Active || manager.state.Enabled {
				t.Fatal("failed restart recovery lost original intent")
			}
			if _, err := os.Stat(pending); !os.IsNotExist(err) {
				t.Fatal("did not commit repair")
			}
			_ = f
		})
	}
}
func TestLauncherWindowsRepairRejectsTaskAndPublicationDrift(t *testing.T) {
	for _, kind := range []string{"task-executable", "task-environment", "task-arguments", "task-missing", "config", "unit", "manifest", "snapshot", "metadata"} {
		t.Run(kind, func(t *testing.T) {
			f, a, manager, project, art := launcherWindowsRepairFixture(t, false, false)
			switch kind {
			case "task-executable":
				manager.state.Definition.Executable = `C:\custom\node.exe`
			case "task-environment":
				manager.state.Definition.Environment = map[string]string{}
				for k, v := range map[string]string{"HOME": f.env["HOME"], "USERPROFILE": f.env["HOME"], "PATH": "custom", "VECNET_BIN": "custom", "VECNET_CONFIG": f.env["VECNET_CONFIG"]} {
					manager.state.Definition.Environment[k] = v
				}
			case "task-arguments":
				manager.state.Definition.Args = append([]string{}, manager.state.Definition.Args...)
				manager.state.Definition.Args[1] = `C:\custom\main.ts`
			case "task-missing":
				manager.state.Exists = false
			default:
				path := map[string]string{"config": f.paths.Config, "unit": f.paths.Unit, "manifest": f.paths.Manifest, "metadata": art.Path}[kind]
				if kind == "snapshot" {
					var m launcherInstall
					json.Unmarshal(launcherTestRead(t, f.paths.Manifest), &m)
					path = m.Entry
				}
				if err := os.WriteFile(path, []byte("custom\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			unit, manifest, meta := launcherTestRead(t, f.paths.Unit), launcherTestRead(t, f.paths.Manifest), launcherTestRead(t, art.Path)
			if _, err := a.repairLauncherArtifact(project, art); err == nil {
				t.Fatal("accepted drift")
			}
			if !bytes.Equal(unit, launcherTestRead(t, f.paths.Unit)) || !bytes.Equal(manifest, launcherTestRead(t, f.paths.Manifest)) || !bytes.Equal(meta, launcherTestRead(t, art.Path)) {
				t.Fatal("overwrote customization")
			}
			if strings.Contains(strings.Join(manager.calls, "\n"), "register ") || strings.Contains(strings.Join(manager.calls, "\n"), "restart ") {
				t.Fatal("drift mutated task")
			}
		})
	}
}
func TestLauncherWindowsInstallRejectsUnsafeDACLAndHardlinks(t *testing.T) {
	for _, kind := range []string{"credential", "snapshot", "unit", "lock", "hardlink", "data-ancestor", "manifest-ancestor"} {
		t.Run(kind, func(t *testing.T) {
			f, _, manager, args := launcherWindowsFixture(t, preparedLauncherNode)
			var runtime launcherInstall
			json.Unmarshal(launcherTestRead(t, f.paths.Manifest), &runtime)
			path := map[string]string{"credential": f.paths.Credential, "snapshot": runtime.Entry, "unit": f.paths.Unit, "lock": f.paths.Manifest + ".lock", "hardlink": runtime.Entry, "data-ancestor": f.env["XDG_DATA_HOME"], "manifest-ancestor": f.env["XDG_CONFIG_HOME"]}[kind]
			if kind == "hardlink" {
				if err := os.Link(path, path+".link"); err != nil {
					t.Fatal(err)
				}
			} else {
				var file *os.File
				var err error
				if strings.HasSuffix(kind, "ancestor") {
					var h windows.Handle
					var ptr *uint16
					ptr, err = windows.UTF16PtrFromString(path)
					if err == nil {
						h, err = windows.CreateFile(ptr, windows.WRITE_DAC|windows.READ_CONTROL, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
						if err == nil {
							file = os.NewFile(uintptr(h), path)
						}
					}
				} else {
					file, err = platform.OpenFile(path, os.O_RDWR, 0)
				}
				if err != nil {
					t.Fatal(err)
				}
				sd, err := windows.SecurityDescriptorFromString("D:P(A;;FA;;;WD)")
				if err != nil {
					t.Fatal(err)
				}
				acl, _, err := sd.DACL()
				if err != nil {
					t.Fatal(err)
				}
				err = windows.SetSecurityInfo(windows.Handle(file.Fd()), windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, acl, nil)
				file.Close()
				if err != nil {
					t.Fatal(err)
				}
			}
			unit, manifest := launcherTestRead(t, f.paths.Unit), launcherTestRead(t, f.paths.Manifest)
			manager.calls = nil
			if code, _, errs := f.run(t, append(args, "--write", "--start")...); code == 0 {
				t.Fatalf("unsafe %s accepted: %s", kind, errs)
			}
			if !bytes.Equal(unit, launcherTestRead(t, f.paths.Unit)) || !bytes.Equal(manifest, launcherTestRead(t, f.paths.Manifest)) {
				t.Fatal("unsafe install published")
			}
			for _, call := range manager.calls {
				if strings.HasPrefix(call, "register ") || strings.HasPrefix(call, "restart ") {
					t.Fatal("unsafe install changed task")
				}
			}
		})
	}
}
func TestLauncherWindowsRepairLockAndCancellation(t *testing.T) {
	f, a, _, project, art := launcherWindowsRepairFixture(t, false, false)
	lock, err := platform.OpenFile(f.paths.Manifest+".lock", os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	held, err := launcherTryLock(lock)
	if err != nil || !held {
		t.Fatalf("lock %t %v", held, err)
	}
	defer platform.Unlock(lock)
	before := launcherTestRead(t, art.Path)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	a.ctx = ctx
	if _, err = a.repairLauncherArtifact(project, art); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("lock did not honor context: ", err)
	}
	if !bytes.Equal(before, launcherTestRead(t, art.Path)) {
		t.Fatal("contended repair wrote ownership")
	}
}

// Opt-in because it creates exactly one isolated per-user scheduled task. The
// finally cleanup deletes only this test's exact SID/profile-hashed task name.
func TestLauncherWindowsLiveTaskScheduler(t *testing.T) {
	if os.Getenv("VECNET_TEST_WINDOWS_LAUNCHER_TASK") != "1" {
		t.Skip("set VECNET_TEST_WINDOWS_LAUNCHER_TASK=1 for the live Task Scheduler boundary")
	}
	controller, err := launcherNativeTaskController(nil)
	if err != nil {
		t.Fatal(err)
	}
	scheduler := controller.(*launcherScheduler)
	dir := filepath.Join(t.TempDir(), "private live task")
	if err = platform.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	node, err := exec.LookPath("node.exe")
	if err != nil {
		t.Fatal(err)
	}
	hash := launcherHash([]byte(dir + "\x00" + scheduler.sid))
	profile := filepath.Join(dir, "p-"+hash[:16])
	if err = platform.MkdirAll(profile, 0o700); err != nil {
		t.Fatal(err)
	}
	entry := filepath.Join(profile, "host-probe.ts")
	marker := filepath.Join(profile, "ready")
	script := fmt.Sprintf("import fs from 'node:fs'; fs.writeFileSync(%q, String(process.pid)+'\\n'+process.env.VECNET_CONFIG);setInterval(()=>{},1000);\n", marker)
	if err = os.WriteFile(entry, []byte(script), 0o600); err != nil {
		t.Fatal(err)
	}
	bin, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(profile, "config.json")
	raw, err := launcherWindowsUnit(launcherInstall{Node: node, Entry: entry, VecnetBin: bin}, config, filepath.Join(profile, "vecnet.toml"), dir, filepath.Dir(node)+`;C:\Windows\System32`)
	if err != nil {
		t.Fatal(err)
	}
	def, err := launcherDecodeTask([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	state, err := scheduler.Inspect(context.Background(), def.Name)
	if err != nil {
		t.Fatal(err)
	}
	if state.Exists {
		t.Fatal("isolated live test task unexpectedly exists")
	}
	t.Cleanup(func() {
		script := fmt.Sprintf("$ErrorActionPreference='Stop';$s=New-Object -ComObject Schedule.Service;$s.Connect();$f=$s.GetFolder('\\');try{$t=$f.GetTask('%s');$t.Stop(0);$f.DeleteTask('%s',0)}catch{if($_.Exception.HResult -ne -2147024894){throw}}", def.Name, def.Name)
		cmd := exec.Command(scheduler.powershell, "-NoLogo", "-NoProfile", "-NonInteractive", "-WindowStyle", "Hidden", "-EncodedCommand", launcherEncodePowerShell(script))
		platform.Background(cmd)
		if out, e := cmd.CombinedOutput(); e != nil {
			t.Errorf("live task cleanup: %v %s", e, out)
		}
	})
	if err = scheduler.Register(context.Background(), def, false); err != nil {
		t.Fatal(err)
	}
	names, err := localWindowsProfileLauncherTasks(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, name := range names {
		if name == def.Name {
			found = true
		}
	}
	if !found {
		t.Fatal("orphan scheduler inventory omitted live task")
	}
	state, err = scheduler.Inspect(context.Background(), def.Name)
	if err != nil || !state.Exists || state.Active || state.Enabled || !reflect.DeepEqual(state.Definition, def) {
		t.Fatalf("disabled/stopped registration: %+v %v", state, err)
	}
	if err = scheduler.Restart(context.Background(), def.Name); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	var child int
	for {
		raw, e := os.ReadFile(marker)
		if e == nil {
			fmt.Sscanf(string(raw), "%d", &child)
			if !strings.Contains(string(raw), def.Environment["VECNET_CONFIG"]) {
				t.Fatal("host environment changed")
			}
			break
		}
		if !sleepCtx(ctx, 100*time.Millisecond) {
			t.Fatal("real Node child never became ready: ", ctx.Err())
		}
	}
	state, err = scheduler.Inspect(context.Background(), def.Name)
	if err != nil || !state.Active || state.Enabled {
		t.Fatalf("manual disabled start lost intent: %+v %v", state, err)
	}
	reply, err := scheduler.inspect(context.Background(), def.Name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = scheduler.command(context.Background(), "stop", reply.Task, reply.XML, false); err != nil {
		t.Fatal(err)
	}
	for {
		process, e := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(child))
		if e != nil {
			break
		}
		wait, e := windows.WaitForSingleObject(process, 0)
		windows.CloseHandle(process)
		if e == nil && wait == windows.WAIT_OBJECT_0 {
			break
		}
		if !sleepCtx(ctx, 100*time.Millisecond) {
			t.Fatal("scheduler stop orphaned Node child")
		}
	}
	state, err = scheduler.Inspect(context.Background(), def.Name)
	if err != nil || state.Active || state.Enabled {
		t.Fatalf("stopped task state: %+v %v", state, err)
	}
}

func TestLauncherWindowsTaskControllerRejectsConcurrentChanges(t *testing.T) {
	f, _, manager, args := launcherWindowsFixture(t, preparedLauncherNode)
	raw := launcherTestRead(t, f.paths.Unit)
	prepare, err := (&app{ctx: context.Background(), getenv: func(k string) string { return f.env[k] }}).launcherWindowsPrepareInstall(f.paths, raw, false, false)
	if err != nil {
		t.Fatal(err)
	}
	manager.state.Enabled = true
	manager.calls = nil
	if err = prepare(); err == nil {
		t.Fatal("installation ignored concurrent enablement change")
	}
	if strings.Contains(strings.Join(manager.calls, "\n"), "register ") {
		t.Fatal("concurrent task overwritten")
	}
	manager.state.Definition.Args = append([]string{}, manager.state.Definition.Args...)
	manager.state.Definition.Args[1] = `C:\custom\runtime.ts`
	manager.calls = nil
	unit, manifest := launcherTestRead(t, f.paths.Unit), launcherTestRead(t, f.paths.Manifest)
	if code, _, errs := f.run(t, append(args, "--write")...); code == 0 {
		t.Fatalf("custom task adopted: %s", errs)
	}
	if !bytes.Equal(unit, launcherTestRead(t, f.paths.Unit)) || !bytes.Equal(manifest, launcherTestRead(t, f.paths.Manifest)) {
		t.Fatal("custom task refusal republished profile")
	}
}

// Each drift case is an independent root so native whole-root scheduling can
// place it without a seven-fixture indivisible straggler. Assertions stay shared.
func TestLauncherWindowsRepairPendingDriftEnabled(t *testing.T) {
	launcherWindowsRepairPendingDrift(t, "enabled")
}
func TestLauncherWindowsRepairPendingDriftActive(t *testing.T) {
	launcherWindowsRepairPendingDrift(t, "active")
}
func TestLauncherWindowsRepairPendingDriftTask(t *testing.T) {
	launcherWindowsRepairPendingDrift(t, "task")
}
func TestLauncherWindowsRepairPendingDriftIntent(t *testing.T) {
	launcherWindowsRepairPendingDrift(t, "intent")
}
func TestLauncherWindowsRepairPendingDriftMetadata(t *testing.T) {
	launcherWindowsRepairPendingDrift(t, "metadata")
}
func TestLauncherWindowsRepairPendingDriftUnit(t *testing.T) {
	launcherWindowsRepairPendingDrift(t, "unit")
}
func TestLauncherWindowsRepairPendingDriftManifest(t *testing.T) {
	launcherWindowsRepairPendingDrift(t, "manifest")
}
func launcherWindowsRepairPendingDrift(t *testing.T, kind string) {
	t.Helper()
	f, a, manager, project, art := launcherWindowsRepairFixture(t, false, false)
	manager.failRegister = true
	if _, err := a.repairLauncherArtifact(project, art); err == nil {
		t.Fatal("did not leave pending repair")
	}
	manager.failRegister = false
	manager.calls = nil
	switch kind {
	case "enabled":
		manager.state.Enabled = true
	case "active":
		manager.state.Active = true
	case "task":
		manager.state.Definition.Executable = `C:\custom\node.exe`
	default:
		path := map[string]string{"intent": filepath.Join(filepath.Dir(art.Path), "repair-pending.json"), "metadata": art.Path, "unit": f.paths.Unit, "manifest": f.paths.Manifest}[kind]
		if err := os.WriteFile(path, []byte("custom pending\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	unit, manifest, meta := launcherTestRead(t, f.paths.Unit), launcherTestRead(t, f.paths.Manifest), launcherTestRead(t, art.Path)
	if _, err := a.repairLauncherArtifact(project, art); err == nil {
		t.Fatal("pending drift accepted")
	}
	if !bytes.Equal(unit, launcherTestRead(t, f.paths.Unit)) || !bytes.Equal(manifest, launcherTestRead(t, f.paths.Manifest)) || !bytes.Equal(meta, launcherTestRead(t, art.Path)) {
		t.Fatal("pending drift overwritten")
	}
	if strings.Contains(strings.Join(manager.calls, "\n"), "register ") || strings.Contains(strings.Join(manager.calls, "\n"), "restart ") {
		t.Fatal("drift activated task")
	}
}
func TestLauncherWindowsTaskHostRoundTripAndTamperRefusal(t *testing.T) {
	f := newLauncherFixture(t, "launcher")
	controller, err := launcherNativeTaskController(nil)
	if err != nil {
		t.Fatal(err)
	}
	scheduler := controller.(*launcherScheduler)
	raw, err := launcherWindowsUnit(launcherInstall{Node: `C:\Program Files\nodejs\node.exe`, Entry: filepath.Join(f.paths.Data, "runtime % & $ ' spaces", "main.ts"), VecnetBin: filepath.Join(f.env["HOME"], "vecnet.exe")}, f.paths.Config, f.env["VECNET_CONFIG"], f.env["HOME"], f.env["PATH"])
	if err != nil {
		t.Fatal(err)
	}
	def, err := launcherDecodeTask([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	task := scheduler.definition(def)
	got, err := scheduler.project(task)
	if err != nil || !reflect.DeepEqual(got, def) {
		t.Fatalf("host roundtrip: %+v %v", got, err)
	}
	for _, kind := range []string{"sid", "command", "description", "directory", "arguments", "host"} {
		t.Run(kind, func(t *testing.T) {
			changed := task
			switch kind {
			case "sid":
				changed.SID = "S-1-5-18"
			case "command":
				changed.Command = `C:\custom\powershell.exe`
			case "description":
				changed.Description = "unmanaged"
			case "directory":
				changed.Directory = filepath.Dir(task.Directory)
			case "arguments":
				changed.Arguments += " x"
			case "host":
				changed.Arguments = strings.TrimSuffix(task.Arguments, launcherEncodePowerShell(launcherTaskHost(def))) + launcherEncodePowerShell(launcherTaskHost(def)+"\nexit 0\n")
			}
			if _, err := scheduler.project(changed); err == nil {
				t.Fatal("accepted custom scheduler host")
			}
		})
	}
}

func TestLauncherWindowsUnregisteredProfileDiscovery(t *testing.T) {
	dir := t.TempDir()
	home := filepath.Join(dir, "home")
	if err := platform.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	custom := filepath.Join(dir, "custom config", "vecnet.toml")
	env := map[string]string{"VECNET_CONFIG": custom}
	id := "p-0123456789abcdef"
	profile := filepath.Join(filepath.Dir(custom), "launcher", id)
	if err := platform.MkdirAll(profile, 0o700); err != nil {
		t.Fatal(err)
	}
	units, err := localWindowsProfileLauncherUnitsWith(home, func(k string) string { return env[k] })
	if err != nil || len(units) != 0 {
		t.Fatalf("setup-only profile treated as installed: %v %v", units, err)
	}
	target := filepath.Join(profile, "vecnet-launcher-"+id+".task.json")
	for _, publication := range []string{"install.json", "managed.json", "repair-pending.json", filepath.Base(target)} {
		t.Run(publication, func(t *testing.T) {
			file, err := platform.OpenFile(filepath.Join(profile, publication), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
			if err != nil {
				t.Fatal(err)
			}
			file.WriteString("partial profile")
			file.Close()
			defer os.Remove(filepath.Join(profile, publication))
			got, err := localWindowsProfileLauncherUnitsWith(home, func(k string) string { return env[k] })
			if err != nil || !reflect.DeepEqual(got, []string{target}) {
				t.Fatalf("hidden partial installation: %v %v", got, err)
			}
		})
	}
}

// Regression for executable lookup authorizing Node --version before checking
// its DACL. This deliberately uses the real install entry point and asserts no
// command, publication or task mutation is reached on unsafe software paths.
func TestLauncherWindowsInstallRejectsUnsafeExecutableBeforeExecution(t *testing.T) {
	for _, kind := range []string{"node-file", "node-parent", "cli-file", "cli-parent", "node-hardlink"} {
		t.Run(kind, func(t *testing.T) {
			// Initial valid installation is setup, not cold-admission coverage.
			// Prepare this case's own mutable image; never mutate the job image.
			f, _, manager, args := launcherWindowsFixture(t, preparedMutableLauncherNode)
			var installed launcherInstall
			if err := launcherReadJSON(f.paths.Manifest, &installed); err != nil {
				t.Fatal(err)
			}
			path := installed.Node
			switch kind {
			case "node-parent":
				path = filepath.Dir(path)
			case "cli-file":
				path = installed.VecnetBin
			case "cli-parent":
				path = filepath.Dir(installed.VecnetBin)
			}
			if kind == "node-hardlink" {
				if err := os.Link(path, path+".link"); err != nil {
					t.Fatal(err)
				}
			} else {
				ptr, err := windows.UTF16PtrFromString(path)
				if err != nil {
					t.Fatal(err)
				}
				flags := uint32(windows.FILE_FLAG_OPEN_REPARSE_POINT)
				if strings.HasSuffix(kind, "parent") {
					flags |= windows.FILE_FLAG_BACKUP_SEMANTICS
				}
				handle, err := windows.CreateFile(ptr, windows.WRITE_DAC|windows.READ_CONTROL, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, flags, 0)
				if err != nil {
					t.Fatal(err)
				}
				sd, err := windows.SecurityDescriptorFromString("D:P(A;OICI;FA;;;WD)")
				if err != nil {
					t.Fatal(err)
				}
				acl, _, err := sd.DACL()
				if err != nil {
					t.Fatal(err)
				}
				err = windows.SetSecurityInfo(handle, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, acl, nil)
				windows.CloseHandle(handle)
				if err != nil {
					t.Fatal(err)
				}
			}
			beforeUnit, beforeManifest := launcherTestRead(t, f.paths.Unit), launcherTestRead(t, f.paths.Manifest)
			manager.calls = nil
			if code, _, errs := f.run(t, append(args, "--write", "--start")...); code == 0 || !strings.Contains(errs, "unsafe launcher executable") {
				t.Fatalf("want executable trust rejection before Node version admission for %s, exit=%d: %s", kind, code, errs)
			}
			if len(manager.calls) != 0 {
				t.Fatalf("unsafe executable reached Task Scheduler: %v", manager.calls)
			}
			if !bytes.Equal(beforeUnit, launcherTestRead(t, f.paths.Unit)) || !bytes.Equal(beforeManifest, launcherTestRead(t, f.paths.Manifest)) {
				t.Fatal("unsafe executable published installation")
			}
		})
	}
}
func TestLauncherWindowsPinnedExecutableCannotBeReplacedOrWritten(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "pinned executable")
	if err := platform.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "node.exe")
	file, err := platform.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o700)
	if err != nil {
		t.Fatal(err)
	}
	file.WriteString("never execute")
	file.Close()
	release, err := launcherPinExecutable(path)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if file, err = os.OpenFile(path, os.O_WRONLY, 0); err == nil {
		file.Close()
		t.Fatal("validated executable was writable during execution window")
	}
	if err = os.Rename(path, path+".moved"); err == nil {
		t.Fatal("validated executable replaced during execution window")
	}
}
func TestLauncherWindowsStartupAcknowledgementRejectsStaleOrWrongGraph(t *testing.T) {
	f := newLauncherFixture(t, "launcher")
	raw, err := launcherWindowsUnit(launcherInstall{Node: `C:\Program Files\nodejs\node.exe`, Entry: filepath.Join(f.paths.Data, "main.ts"), VecnetBin: filepath.Join(f.env["HOME"], "vecnet.exe")}, f.paths.Config, f.env["VECNET_CONFIG"], f.env["HOME"], f.env["PATH"])
	if err != nil {
		t.Fatal(err)
	}
	def, err := launcherDecodeTask([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	request := launcherTaskStartRequest(def)
	ack := launcherStartAck{launcherStartRequest: request, PID: 123, HostPID: 456, HostCreated: "132400000000000000", Node: def.Executable, Entry: def.Args[1], Config: def.Args[4]}
	if !launcherAckMatches(ack, request, def) {
		t.Fatal("valid ack rejected")
	}
	for _, kind := range []string{"token", "definition", "task", "node", "entry", "config", "pid", "host", "created"} {
		t.Run(kind, func(t *testing.T) {
			changed := ack
			switch kind {
			case "token":
				changed.Token = launcherTempName("")
			case "definition":
				changed.DefinitionSHA256 = strings.Repeat("0", 64)
			case "task":
				changed.Task += "other"
			case "node":
				changed.Node = `C:\custom\node.exe`
			case "entry":
				changed.Entry += ".old"
			case "config":
				changed.Config += ".old"
			case "pid":
				changed.PID = 0
			case "host":
				changed.HostPID = 0
			case "created":
				changed.HostCreated = ""
			}
			if launcherAckMatches(changed, request, def) {
				t.Fatal("stale/wrong graph acknowledgement accepted")
			}
		})
	}
}

// Exercises the real scheduler/PowerShell/Node boundary, not the state fake.
// A loading module keeps scheduler state4 but is not a started runtime graph;
// a broken entry exits before ack. Neither may report successful activation.
func TestLauncherWindowsLiveStartupAcknowledgementFailures(t *testing.T) {
	if os.Getenv("VECNET_TEST_WINDOWS_LAUNCHER_TASK") != "1" {
		t.Skip("set VECNET_TEST_WINDOWS_LAUNCHER_TASK=1 for live negative startup tests")
	}
	for _, kind := range []string{"loading-module", "missing-entry", "early-exit", "stale-ack"} {
		t.Run(kind, func(t *testing.T) {
			ctl, err := launcherNativeTaskController(nil)
			if err != nil {
				t.Fatal(err)
			}
			scheduler := ctl.(*launcherScheduler)
			home := filepath.Join(t.TempDir(), "private host")
			if err = platform.MkdirAll(home, 0o700); err != nil {
				t.Fatal(err)
			}
			node, err := exec.LookPath("node.exe")
			if err != nil {
				t.Fatal(err)
			}
			bin, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			profile := filepath.Join(home, "p-"+launcherHash([]byte(home))[:16])
			if err = platform.MkdirAll(profile, 0o700); err != nil {
				t.Fatal(err)
			}
			entry := filepath.Join(profile, "entry.mjs")
			body := "setInterval(()=>{},1000);"
			switch kind {
			case "loading-module":
				body = "await new Promise(()=>setInterval(()=>{},1000));"
			case "early-exit":
				body = "setTimeout(()=>process.exit(0),100);"
			}
			if kind != "missing-entry" {
				if err = os.WriteFile(entry, []byte(body), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			raw, err := launcherWindowsUnit(launcherInstall{Node: node, Entry: entry, VecnetBin: bin}, filepath.Join(profile, "config.json"), filepath.Join(profile, "vecnet.toml"), home, os.Getenv("PATH"))
			if err != nil {
				t.Fatal(err)
			}
			def, err := launcherDecodeTask([]byte(raw))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				script := fmt.Sprintf("$s=New-Object -ComObject Schedule.Service;$s.Connect();$f=$s.GetFolder('\\');try{$t=$f.GetTask('%s');$t.Stop(0);$f.DeleteTask('%s',0)}catch{}", def.Name, def.Name)
				cmd := exec.Command(scheduler.powershell, "-NoLogo", "-NoProfile", "-NonInteractive", "-WindowStyle", "Hidden", "-EncodedCommand", launcherEncodePowerShell(script))
				platform.Background(cmd)
				if out, e := cmd.CombinedOutput(); e != nil {
					t.Errorf("cleanup: %v %s", e, out)
				}
			})
			if err = scheduler.Register(context.Background(), def, false); err != nil {
				t.Fatal(err)
			}
			if kind == "stale-ack" {
				// A state4 runtime from a previous start must not satisfy a new token.
				if err = scheduler.Restart(context.Background(), def.Name); err != nil {
					t.Fatal(err)
				}
				reply, err := scheduler.inspect(context.Background(), def.Name)
				if err != nil {
					t.Fatal(err)
				}
				root, err := launcherOpenDir(profile, profile, false)
				if err != nil {
					t.Fatal(err)
				}
				defer root.Close()
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				defer cancel()
				if err = scheduler.waitForStart(ctx, reply, launcherTaskStartRequest(def), def, root); err == nil {
					t.Fatal("stale ack satisfied fresh token")
				}
			} else {
				ctx, cancel := context.WithTimeout(context.Background(), 7*time.Second)
				defer cancel()
				if err = scheduler.Restart(ctx, def.Name); err == nil {
					t.Fatal("state4/broken runtime falsely reported started")
				}
			}
		})
	}
}
