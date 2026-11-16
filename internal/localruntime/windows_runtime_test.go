//go:build windows

package localruntime

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf16"

	"github.com/XertroV/vecnet/internal/platform"
	"golang.org/x/sys/windows"
)

type windowsTestHost struct {
	mu       sync.Mutex
	task     taskState
	calls    []string
	fail     string
	conflict bool
}

func decodeTaskInput(args []string) (struct {
	Op         string      `json:"op"`
	Definition windowsTask `json:"definition"`
	Expected   string      `json:"expected"`
}, error) {
	var v struct {
		Op         string      `json:"op"`
		Definition windowsTask `json:"definition"`
		Expected   string      `json:"expected"`
	}
	raw, e := base64.StdEncoding.DecodeString(args[len(args)-1])
	if e != nil {
		return v, e
	}
	words := make([]uint16, len(raw)/2)
	for i := range words {
		words[i] = binary.LittleEndian.Uint16(raw[i*2:])
	}
	script := string(utf16.Decode(words))
	first := strings.SplitN(script, "\n", 2)[0]
	data := strings.TrimSuffix(strings.TrimPrefix(first, "$inputData='"), "'")
	b, e := base64.StdEncoding.DecodeString(data)
	if e != nil {
		return v, e
	}
	e = json.Unmarshal(b, &v)
	return v, e
}
func (h *windowsTestHost) run(ctx context.Context, name string, args ...string) ([]byte, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, a := range args {
		if a == "--version" {
			return []byte("v24.22.1"), nil
		}
		if a == "--eval" {
			return nil, nil
		}
	}
	in, e := decodeTaskInput(args)
	if e != nil {
		return nil, e
	}
	h.calls = append(h.calls, in.Op)
	if in.Op == h.fail {
		return []byte("credential-secret"), errors.New("credential-secret")
	}
	if h.conflict {
		h.task.Conflicts = []string{"vecnet-launcher-test"}
	} else {
		h.task.Conflicts = nil
	}
	switch in.Op {
	case "register":
		h.task = taskState{Exists: true, Safe: true, Enabled: true, State: 3, XML: "owned-test-xml", Definition: in.Definition}
	case "start":
		h.task.State = 4
	case "stop":
		h.task.State = 3
	case "enable":
		h.task.Enabled = true
	}
	return json.Marshal(h.task)
}
func windowsFixture(t *testing.T) (Options, *windowsTestHost) {
	t.Helper()
	home, e := windowsHome(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	bin := filepath.Join(t.TempDir(), "vecnet.exe")
	f, e := platform.OpenFile(bin, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0700)
	if e != nil {
		t.Fatal(e)
	}
	_, e = f.Write([]byte("test-cli-v1"))
	f.Close()
	if e != nil {
		t.Fatal(e)
	}
	h := &windowsTestHost{}
	return Options{Home: home, Binary: bin, Node: `C:\Program Files\nodejs\node.exe`, Version: "v1", Getenv: func(string) string { return "" }, Run: h.run}, h
}
func ensureWindowsTest(t *testing.T, o Options) Result {
	t.Helper()
	r, e := Ensure(context.Background(), o)
	if e != nil {
		t.Fatal(e)
	}
	return r
}
func TestWindowsPreviewHasNoWrites(t *testing.T) {
	o, h := windowsFixture(t)
	r, e := Preview(context.Background(), o)
	if e != nil {
		t.Fatal(e)
	}
	if !r.Changed || r.Unit != unitPath(o.Home) {
		t.Fatalf("preview result %+v", r)
	}
	entries, e := os.ReadDir(o.Home)
	if e != nil || len(entries) != 0 {
		t.Fatalf("preview wrote HOME %v %v", entries, e)
	}
	for _, op := range h.calls {
		if op != "inspect" {
			t.Fatalf("preview mutated scheduler %s", op)
		}
	}
}
func TestWindowsEnsureUpgradeNoopAndIntentPreserved(t *testing.T) {
	o, h := windowsFixture(t)
	intent := filepath.Join(o.Home, ".local/state/vecnet/codex/bindings.json")
	ledger := filepath.Join(o.Home, ".local/state/vecnet/codex/delivery.sqlite")
	if e := platform.MkdirAll(filepath.Dir(intent), 0700); e != nil {
		t.Fatal(e)
	}
	for _, p := range []string{intent, ledger} {
		f, e := platform.OpenFile(p, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if e != nil {
			t.Fatal(e)
		}
		f.Write([]byte("intent-sentinel"))
		f.Close()
	}
	r := ensureWindowsTest(t, o)
	if !r.Changed || !strings.HasSuffix(r.Unit, ".json") {
		t.Fatalf("result %+v", r)
	}
	for _, p := range []string{r.Release, r.Unit, filepath.Join(r.Release, "vecnet.exe"), filepath.Join(root(o.Home), manifestName)} {
		st, e := os.Stat(p)
		if e != nil {
			t.Fatal(e)
		}
		if e = platform.CheckPath(p, st.IsDir(), true); e != nil {
			t.Fatal(e)
		}
	}
	got, e := Read(o.Home)
	if e != nil || got.BinarySHA256 != r.BinarySHA256 {
		t.Fatalf("read %+v %v", got, e)
	}
	if ensureWindowsTest(t, o).Changed {
		t.Fatal("no-op changed installation")
	}
	o.Version = "v2"
	r2 := ensureWindowsTest(t, o)
	if r2.Release == r.Release || !r2.Changed {
		t.Fatal("no upgrade")
	}
	for _, p := range []string{intent, ledger} {
		b, e := os.ReadFile(p)
		if e != nil || string(b) != "intent-sentinel" {
			t.Fatal("intent/ledger changed")
		}
	}
	if n := strings.Count(strings.Join(h.calls, ","), "register"); n != 2 {
		t.Fatalf("task registrations %d", n)
	}
}
func TestWindowsFailedActivationPendingAndRetry(t *testing.T) {
	o, h := windowsFixture(t)
	r := ensureWindowsTest(t, o)
	o.Version = "v2"
	h.fail = "start"
	if _, e := Ensure(context.Background(), o); e == nil || strings.Contains(e.Error(), "credential-secret") {
		t.Fatalf("failure not sanitized %v", e)
	}
	got, e := Read(o.Home)
	if !errors.Is(e, ErrPending) || got.Version != r.Version {
		t.Fatalf("failed upgrade applied %+v %v", got, e)
	}
	h.fail = ""
	if !ensureWindowsTest(t, o).Changed {
		t.Fatal("retry did not activate")
	}
}
func TestWindowsConflictsAndCustomTasksWriteNothing(t *testing.T) {
	for _, kind := range []string{"active-launcher", "unsafe-task", "custom-task", "query-failed"} {
		t.Run(kind, func(t *testing.T) {
			o, h := windowsFixture(t)
			switch kind {
			case "active-launcher":
				h.conflict = true
			case "unsafe-task":
				h.task.Exists = true
			case "custom-task":
				h.task.Exists = true
				h.task.Safe = true
				h.task.Definition = windowsTask{Schema: 1, Name: "custom"}
			case "query-failed":
				h.fail = "inspect"
			}
			if _, e := Ensure(context.Background(), o); e == nil {
				t.Fatal("conflict accepted")
			}
			entries, _ := os.ReadDir(o.Home)
			if len(entries) != 0 {
				t.Fatal("conflict wrote HOME")
			}
		})
	}
}
func TestWindowsRepairBacksUpAndRestarts(t *testing.T) {
	o, h := windowsFixture(t)
	r := ensureWindowsTest(t, o)
	p := filepath.Join(r.Release, "connectors/launcher/package.json")
	if e := os.Remove(p); e != nil {
		t.Fatal(e)
	}
	before := strings.Count(strings.Join(h.calls, ","), "start")
	if !ensureWindowsTest(t, o).Changed {
		t.Fatal("repair not changed")
	}
	if strings.Count(strings.Join(h.calls, ","), "start") != before+1 {
		t.Fatal("repair did not restart")
	}
	backups, e := os.ReadDir(filepath.Join(root(o.Home), "repair-backups"))
	if e != nil || len(backups) != 1 {
		t.Fatalf("repair backup %v %v", backups, e)
	}
	if _, e = Read(o.Home); e != nil {
		t.Fatal(e)
	}
}
func TestWindowsReenableDoesNotRestart(t *testing.T) {
	o, h := windowsFixture(t)
	ensureWindowsTest(t, o)
	h.task.Enabled = false
	before := strings.Count(strings.Join(h.calls, ","), "start")
	r := ensureWindowsTest(t, o)
	if !r.Changed || !h.task.Enabled || strings.Count(strings.Join(h.calls, ","), "start") != before {
		t.Fatal("enabling a running task unnecessarily restarted")
	}
}
func TestWindowsConcurrentEnsureSerializesActivation(t *testing.T) {
	o, h := windowsFixture(t)
	var wg sync.WaitGroup
	errs := make(chan error, 4)
	for range 4 {
		wg.Add(1)
		go func() { defer wg.Done(); _, e := Ensure(context.Background(), o); errs <- e }()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	if strings.Count(strings.Join(h.calls, ","), "start") != 1 {
		t.Fatal("concurrent installers started duplicate tasks")
	}
}
func TestWindowsCanceledInstallWritesNothing(t *testing.T) {
	o, _ := windowsFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, e := Ensure(ctx, o); !errors.Is(e, context.Canceled) {
		t.Fatalf("canceled install: %v", e)
	}
	entries, _ := os.ReadDir(o.Home)
	if len(entries) != 0 {
		t.Fatal("canceled installation wrote HOME")
	}
}
func TestWindowsManifestDuplicateKeysRefused(t *testing.T) {
	o, _ := windowsFixture(t)
	ensureWindowsTest(t, o)
	path := filepath.Join(root(o.Home), manifestName)
	b, e := readPrivate(path, 64<<10)
	if e != nil {
		t.Fatal(e)
	}
	b = append([]byte(`{"schema":1,`), b[1:]...)
	if e = atomicWrite(path, b, 0600); e != nil {
		t.Fatal(e)
	}
	if _, e = Read(o.Home); e == nil {
		t.Fatal("duplicate manifest keys accepted")
	}
}
func TestWindowsUnknownReleaseFailsBeforePending(t *testing.T) {
	o, _ := windowsFixture(t)
	binary, e := os.ReadFile(o.Binary)
	if e != nil {
		t.Fatal(e)
	}
	files, e := distribution(binary)
	if e != nil {
		t.Fatal(e)
	}
	release := filepath.Join(root(o.Home), releaseDigest(o.Version, files))
	if e = platform.MkdirAll(release, 0700); e != nil {
		t.Fatal(e)
	}
	if _, e = Preview(context.Background(), o); e == nil {
		t.Fatal("preview accepted unknown damaged release")
	}
	if _, e = Ensure(context.Background(), o); e == nil {
		t.Fatal("unknown damaged release adopted")
	}
	if Installed(o.Home) {
		t.Fatal("unrecognized release authorized pending repair")
	}
}
func TestWindowsNativeSchedulerBoundary(t *testing.T) {
	if os.Getenv("VECNET_TEST_NATIVE_SCHEDULER") != "1" {
		t.Skip("set VECNET_TEST_NATIVE_SCHEDULER=1 for disposable per-user task acceptance")
	}
	node, e := exec.LookPath("node.exe")
	if e != nil {
		t.Fatal(e)
	}
	home := t.TempDir()
	release := filepath.Join(root(home), strings.Repeat("b", 64))
	if e = platform.MkdirAll(release, 0700); e != nil {
		t.Fatal(e)
	}
	host := []byte(windowsHost)
	// This process intentionally has no delivery authority. The native boundary
	// test verifies scheduler identity, startup, job containment and bounded stop.
	script := []byte(`import{writeFileSync}from'node:fs';import{join,dirname}from'node:path';import{fileURLToPath}from'node:url';const release=dirname(fileURLToPath(import.meta.url));writeFileSync(join(dirname(release),'delivery-active.json'),JSON.stringify({schema:1,pid:process.pid,release,node:process.execPath}));setInterval(()=>{},1000);`)
	for p, b := range map[string][]byte{"windows-host.ps1": host, "windows-delivery.mjs": script} {
		if e = atomicWrite(filepath.Join(release, p), b, 0600); e != nil {
			t.Fatal(e)
		}
	}
	raw, e := windowsDefinition(home, node, release)
	if e != nil {
		t.Fatal(e)
	}
	var d windowsTask
	json.Unmarshal(raw, &d)
	o := Options{Home: home, Node: node, Getenv: os.Getenv}
	native := windowsRunner(o)
	o.Run = func(ctx context.Context, n string, a ...string) ([]byte, error) {
		b, e := native(ctx, n, a...)
		if e != nil {
			t.Logf("native process failed: %v\n%s", e, b)
		}
		return b, e
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	state, e := inspectTask(ctx, o, d)
	if e != nil {
		t.Fatal(e)
	}
	if state.Exists {
		t.Fatal("disposable HOME unexpectedly has a task")
	}
	// Cleanup is exactly this randomized-HOME-derived task, never a wildcard.
	t.Cleanup(func() {
		ps, _ := powershellPath()
		script := `$s=New-Object -ComObject Schedule.Service;$s.Connect();$f=$s.GetFolder('\');$t=$f.GetTask('` + d.Name + `');$t.Stop(0);$f.DeleteTask('` + d.Name + `',0)`
		b, e := native(context.Background(), ps, "-NoLogo", "-NoProfile", "-NonInteractive", "-WindowStyle", "Hidden", "-EncodedCommand", encodePowerShell(script))
		if e != nil {
			t.Errorf("disposable task cleanup %v %s", e, b)
		}
	})
	state, e = taskCommand(ctx, o, "register", d, "")
	if e != nil {
		t.Fatalf("register: %v state %+v", e, state)
	}
	state, e = taskCommand(ctx, o, "start", d, state.XML)
	if e != nil {
		t.Fatal(e)
	}
	state, e = waitTask(ctx, o, d, true)
	if e != nil {
		t.Fatal(e)
	}
	if e = verifyWindowsActivation(ctx, o, d); e != nil {
		t.Fatal(e)
	}
	ack, e := readPrivate(filepath.Join(root(home), "delivery-active.json"), 64<<10)
	if e != nil {
		t.Fatal(e)
	}
	var v struct {
		PID int `json:"pid"`
	}
	json.Unmarshal(ack, &v)
	state, e = taskCommand(ctx, o, "stop", d, state.XML)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = waitTask(ctx, o, d, false); e != nil {
		t.Fatal(e)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		handle, e := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(v.PID))
		if e != nil {
			break
		}
		status, e := windows.WaitForSingleObject(handle, 0)
		windows.CloseHandle(handle)
		if e != nil || status == windows.WAIT_OBJECT_0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("scheduler stop left delivery child alive")
		}
		time.Sleep(100 * time.Millisecond)
	}
	// Same-version repair operates while only HOME/root ancestors are pinned,
	// never the release directory that must move atomically into recovery backup.
	unpin, e := pinRuntimePath(home, root(home))
	if e != nil {
		t.Fatal(e)
	}
	defer unpin()
	if e = os.Remove(filepath.Join(release, "windows-delivery.mjs")); e != nil {
		t.Fatal(e)
	}
	intentRecorded := false
	repaired, e := stage(ctx, home, release, map[string][]byte{"windows-host.ps1": host, "windows-delivery.mjs": script}, true, func() error {
		intentRecorded = true
		return atomicWrite(filepath.Join(root(home), pendingName), []byte("native-repair-intent"), 0600)
	})
	if e != nil || !repaired || !intentRecorded {
		t.Fatalf("native same-version repair: repaired=%v pending=%v err=%v", repaired, intentRecorded, e)
	}
	if e = verifyRelease(ctx, release, map[string][]byte{"windows-host.ps1": host, "windows-delivery.mjs": script}); e != nil {
		t.Fatal(e)
	}
	backups, e := os.ReadDir(filepath.Join(root(home), "repair-backups"))
	if e != nil || len(backups) != 1 {
		t.Fatalf("native repair backup %v %v", backups, e)
	}
}

// The first probe must not run before both the executable and all ancestors are
// trusted, and their handles must remain pinned throughout execution.
func TestWindowsRuntimeExecutablePinning(t *testing.T) {
	for _, kind := range []string{"safe", "file", "parent", "ancestor", "hardlink"} {
		t.Run(kind, func(t *testing.T) {
			base := filepath.Join(t.TempDir(), "software")
			dir := filepath.Join(base, "bin")
			if e := platform.MkdirAll(dir, 0700); e != nil {
				t.Fatal(e)
			}
			path := filepath.Join(dir, "node.exe")
			f, e := platform.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0700)
			if e != nil {
				t.Fatal(e)
			}
			_, e = f.WriteString("never execute")
			f.Close()
			if e != nil {
				t.Fatal(e)
			}
			target := ""
			switch kind {
			case "file":
				target = path
			case "parent":
				target = dir
			case "ancestor":
				target = base
			case "hardlink":
				if e = os.Link(path, path+".link"); e != nil {
					t.Fatal(e)
				}
			}
			if target != "" {
				sd, e := windows.SecurityDescriptorFromString("D:P(A;OICI;FA;;;WD)")
				if e != nil {
					t.Fatal(e)
				}
				acl, _, e := sd.DACL()
				if e != nil {
					t.Fatal(e)
				}
				if e = windows.SetNamedSecurityInfo(target, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, acl, nil); e != nil {
					t.Fatal(e)
				}
			}
			release, e := pinWindowsExecutable(path)
			if kind != "safe" {
				if e == nil {
					release()
					t.Fatal("unsafe executable namespace accepted")
				}
				return
			}
			if e != nil {
				t.Fatal(e)
			}
			defer release()
			if writable, e := os.OpenFile(path, os.O_WRONLY, 0); e == nil {
				writable.Close()
				t.Fatal("pinned executable is writable")
			}
			if e := os.Rename(path, path+".moved"); e == nil {
				t.Fatal("pinned executable was replaced")
			}
			if e := os.Rename(base, base+".moved"); e == nil {
				t.Fatal("pinned ancestor was replaced")
			}
		})
	}
}

func TestWindowsRuntimeRefusesUnsafeNodeBeforeAnyProbe(t *testing.T) {
	o, _ := windowsFixture(t)
	dir := filepath.Join(t.TempDir(), "node-parent")
	if e := platform.MkdirAll(dir, 0700); e != nil {
		t.Fatal(e)
	}
	o.Node = filepath.Join(dir, "node.exe")
	f, e := platform.OpenFile(o.Node, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0700)
	if e != nil {
		t.Fatal(e)
	}
	f.WriteString("never execute")
	f.Close()
	sd, e := windows.SecurityDescriptorFromString("D:P(A;OICI;FA;;;WD)")
	if e != nil {
		t.Fatal(e)
	}
	acl, _, e := sd.DACL()
	if e != nil {
		t.Fatal(e)
	}
	if e = windows.SetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, acl, nil); e != nil {
		t.Fatal(e)
	}
	o.Run = nil
	if _, e = Ensure(context.Background(), o); e == nil || !strings.Contains(e.Error(), "unsafe Node executable") {
		t.Fatalf("unsafe namespace reached probe: %v", e)
	}
	entries, e := os.ReadDir(o.Home)
	if e != nil || len(entries) != 0 {
		t.Fatalf("unsafe Node wrote HOME: %v %v", entries, e)
	}
}

func TestWindowsRuntimePinsInstalledNode(t *testing.T) {
	source, e := exec.LookPath("node.exe")
	if e != nil {
		t.Fatal(e)
	}
	// A toolcache is not a trusted service namespace merely because Node runs
	// from it. Preserve its actual bytes in an explicitly owned private fixture.
	dir := filepath.Join(t.TempDir(), "private node")
	if e = platform.MkdirAll(dir, 0700); e != nil {
		t.Fatal(e)
	}
	raw, e := os.ReadFile(source)
	if e != nil {
		t.Fatal(e)
	}
	node := filepath.Join(dir, "node.exe")
	f, e := platform.OpenFile(node, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0700)
	if e != nil {
		t.Fatal(e)
	}
	_, e = f.Write(raw)
	closeErr := f.Close()
	if e != nil || closeErr != nil {
		t.Fatalf("copy Node: write=%v close=%v", e, closeErr)
	}
	copy, e := os.ReadFile(node)
	if e != nil || sum(copy) != sum(raw) {
		t.Fatalf("Node copy differs: %v", e)
	}
	if e = platform.CheckPath(node, false, true); e != nil {
		t.Fatal(e)
	}
	release, e := pinWindowsExecutable(node)
	if e != nil {
		t.Fatal(e)
	}
	defer release()
	o := Options{Home: t.TempDir(), Node: node, Getenv: os.Getenv}
	o.Run = windowsRunner(o)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if e = nodeCapabilities(ctx, o); e != nil {
		t.Fatal(e)
	}
}
