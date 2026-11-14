package localruntime_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/XertroV/vecnet/internal/localruntime"
)

type host struct {
	mu                        sync.Mutex
	calls                     []string
	version                   string
	probeErr, restartErr      bool
	legacy, dropins, fragment string
	active, enabled           bool
}

func (h *host) run(ctx context.Context, name string, args ...string) ([]byte, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	c := strings.Join(append([]string{name}, args...), " ")
	h.calls = append(h.calls, c)
	if strings.Contains(c, "--version") {
		return []byte(h.version), nil
	}
	if strings.Contains(c, "--eval") {
		if h.probeErr {
			return nil, errors.New("credential-secret")
		}
		return nil, nil
	}
	if name != "systemctl" {
		return nil, errors.New("unexpected command")
	}
	if strings.Contains(c, "show-environment") {
		return []byte("SECRET=credential-secret"), nil
	}
	if strings.Contains(c, "list-units") {
		return []byte(h.legacy), nil
	}
	if strings.Contains(c, " show ") {
		return []byte("LoadState=loaded\nFragmentPath=" + h.fragment + "\nDropInPaths=" + h.dropins + "\nActiveState=" + map[bool]string{true: "active", false: "inactive"}[h.active] + "\nUnitFileState=" + map[bool]string{true: "enabled", false: "disabled"}[h.enabled] + "\n"), nil
	}
	if strings.Contains(c, " restart ") {
		if h.restartErr {
			return nil, errors.New("credential-secret")
		}
		h.active = true
	}
	if strings.Contains(c, " enable ") {
		h.enabled = true
	}
	return nil, nil
}
func privateHome(t *testing.T) string {
	t.Helper()
	p := t.TempDir()
	if e := os.Chmod(p, 0700); e != nil {
		t.Fatal(e)
	}
	return p
}
func fixture(t *testing.T) (localruntime.Options, *host) {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("Linux activation")
	}
	home := privateHome(t)
	binary := filepath.Join(t.TempDir(), "vecnet")
	if err := os.WriteFile(binary, []byte("private-cli-v1"), 0700); err != nil {
		t.Fatal(err)
	}
	h := &host{version: "v22.18.0"}
	return localruntime.Options{Home: home, Binary: binary, Node: "/opt/node/bin/node", Version: "v1", Getenv: func(string) string { return "" }, Run: h.run}, h
}
func ensure(t *testing.T, o localruntime.Options) localruntime.Result {
	t.Helper()
	r, e := localruntime.Ensure(context.Background(), o)
	if e != nil {
		t.Fatal(e)
	}
	return r
}
func read(t *testing.T, p string) []byte {
	t.Helper()
	b, e := os.ReadFile(p)
	if e != nil {
		t.Fatal(e)
	}
	return b
}
func write(t *testing.T, p, s string) {
	t.Helper()
	if e := os.MkdirAll(filepath.Dir(p), 0700); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(p, []byte(s), 0600); e != nil {
		t.Fatal(e)
	}
}
func TestCapabilityFailuresWriteNothing(t *testing.T) {
	for _, v := range []string{"v22.17.0", "v20.30.0", "garbage", "v22.18.0"} {
		t.Run(v, func(t *testing.T) {
			o, h := fixture(t)
			h.version = v
			h.probeErr = v == "v22.18.0"
			_, e := localruntime.Ensure(context.Background(), o)
			if e == nil {
				t.Fatal("unsupported capabilities accepted")
			}
			if strings.Contains(e.Error(), "credential-secret") {
				t.Fatal("process output leaked")
			}
			entries, _ := os.ReadDir(o.Home)
			if len(entries) != 0 {
				t.Fatalf("capability failure wrote HOME: %v", entries)
			}
		})
	}
}
func TestProfileLauncherServiceConflicts(t *testing.T) {
	for _, active := range []bool{false, true} {
		t.Run(map[bool]string{true: "active", false: "inactive"}[active], func(t *testing.T) {
			o, h := fixture(t)
			state := "inactive dead"
			if active {
				state = "active running"
			}
			h.legacy = "vecnet-launcher-p-0123456789abcdef.service loaded " + state + " launcher\n"
			_, err := localruntime.Ensure(context.Background(), o)
			if active {
				if err == nil || !strings.Contains(err.Error(), "conflicting running service") {
					t.Fatalf("active profile launcher conflict not identified: %v", err)
				}
			} else if err != nil {
				t.Fatalf("inactive profile launcher blocked activation: %v", err)
			}
			if !strings.Contains(strings.Join(h.calls, "\n"), "vecnet-launcher-p-*.service") {
				t.Fatal("profile-owned launcher units omitted from systemd query")
			}
		})
	}
}

func TestStageActivateUpgradeAndNoop(t *testing.T) {
	o, h := fixture(t)
	authority := filepath.Join(o.Home, ".local/state/vecnet/codex/bindings.json")
	ledger := filepath.Join(o.Home, ".local/state/vecnet/codex/delivery.sqlite")
	write(t, authority, "authority sentinel")
	write(t, ledger, "ledger sentinel")
	if localruntime.Installed(o.Home) {
		t.Fatal("fresh installation reported installed")
	}
	r := ensure(t, o)
	if r.BinarySHA256 != "fd81194439ffa9107aff9f5f6e084685df37264c821b8e29a96a37c1ce930517" {
		t.Fatalf("incorrect applied binary hash %q", r.BinarySHA256)
	}
	readResult, readErr := localruntime.Read(o.Home)
	if readErr != nil || readResult.BinarySHA256 != r.BinarySHA256 {
		t.Fatalf("Read binary hash: %+v %v", readResult, readErr)
	}
	if !r.Changed || r.Version != "v1" || !filepath.IsAbs(r.Release) || r.Unit == "" {
		t.Fatalf("bad result: %+v", r)
	}
	if !localruntime.Installed(o.Home) {
		t.Fatal("installation not discoverable")
	}
	if string(read(t, filepath.Join(r.Release, "vecnet"))) != "private-cli-v1" {
		t.Fatal("wrong private binary")
	}
	for _, p := range []string{"connectors/launcher/package.json", "connectors/hub-client/package.json", "connectors/launcher/src/daemon.ts", "connectors/launcher/src/codex/delivery-main.ts", "connectors/hub-client/src/client.ts", "connectors/launcher/schema/launch-question.v1.schema.json"} {
		read(t, filepath.Join(r.Release, p))
	}
	unit := string(read(t, r.Unit))
	for _, want := range []string{"VECNET_LOCAL_MAINTENANCE=0", "UMask=0077", r.Release, "--experimental-strip-types"} {
		if !strings.Contains(unit, want) {
			t.Fatalf("unit missing %q", want)
		}
	}
	first := strings.Count(strings.Join(h.calls, "\n"), " restart ")
	r2 := ensure(t, o)
	if r2.Changed || r2.Release != r.Release {
		t.Fatalf("repeat changed %+v", r2)
	}
	if got := strings.Count(strings.Join(h.calls, "\n"), " restart "); got != first || first != 1 {
		t.Fatalf("restart counts %d -> %d", first, got)
	}
	if e := os.WriteFile(o.Binary, []byte("private-cli-v2"), 0700); e != nil {
		t.Fatal(e)
	}
	o.Version = "v2"
	r3 := ensure(t, o)
	if !r3.Changed || r3.Release == r.Release || r3.Version != "v2" {
		t.Fatalf("upgrade failed %+v", r3)
	}
	if string(read(t, authority)) != "authority sentinel" || string(read(t, ledger)) != "ledger sentinel" {
		t.Fatal("delivery state changed")
	}
	read(t, filepath.Join(r.Release, "vecnet")) // rollback release retained
}
func TestFailedActivationIsRetryable(t *testing.T) {
	o, h := fixture(t)
	old := ensure(t, o)
	o.Version = "v2"
	h.restartErr = true
	_, err := localruntime.Ensure(context.Background(), o)
	if err == nil || !strings.Contains(err.Error(), "restart") {
		t.Fatalf("missing precise restart failure: %v", err)
	}
	if strings.Contains(err.Error(), "credential-secret") {
		t.Fatal("unsanitized error")
	}
	installed, err := localruntime.Read(o.Home)
	if !errors.Is(err, localruntime.ErrPending) || installed.Version != old.Version {
		t.Fatalf("failed upgrade marked applied: %+v %v", installed, err)
	}
	h.restartErr = false
	r := ensure(t, o)
	if !r.Changed || r.Version != "v2" {
		t.Fatalf("retry failed %+v", r)
	}
}
func TestRefuseCustomUnitsAndLegacyEngines(t *testing.T) {
	for _, kind := range []string{"custom", "drift", "dropin", "foreign-fragment", "launcher", "legacy"} {
		t.Run(kind, func(t *testing.T) {
			o, h := fixture(t)
			unit := filepath.Join(o.Home, ".config/systemd/user/vecnet-codex-delivery.service")
			switch kind {
			case "custom":
				write(t, unit, "[Service]\nExecStart=/custom\n")
			case "drift":
				ensure(t, o)
				write(t, unit, "[Service]\nExecStart=/changed\n")
			case "dropin":
				h.dropins = "/etc/systemd/user/override.conf"
			case "foreign-fragment":
				h.fragment = "/etc/systemd/user/vecnet-codex-delivery.service"
			case "launcher":
				h.legacy = "vecnet-launcher.service loaded active running launcher\n"
			case "legacy":
				h.legacy = "vecnet-delivery@scout.service loaded activating start legacy\n"
			}
			before := strings.Count(strings.Join(h.calls, "\n"), " restart ")
			_, e := localruntime.Ensure(context.Background(), o)
			if e == nil {
				t.Fatal("conflict accepted")
			}
			if strings.Count(strings.Join(h.calls, "\n"), " restart ") != before {
				t.Fatal("activated conflicting engine")
			}
			if kind == "custom" && string(read(t, unit)) != "[Service]\nExecStart=/custom\n" {
				t.Fatal("custom unit overwritten")
			}
		})
	}
}
func TestReadIsReadOnlyAndRejectsUnknownManifest(t *testing.T) {
	home := privateHome(t)
	if _, e := localruntime.Read(home); !errors.Is(e, os.ErrNotExist) {
		t.Fatalf("missing read: %v", e)
	}
	entries, _ := os.ReadDir(home)
	if len(entries) != 0 {
		t.Fatal("read installed files")
	}
	o, _ := fixture(t)
	ensure(t, o)
	p := filepath.Join(o.Home, ".local/share/vecnet-codex/manifest.json")
	var m map[string]any
	if e := json.Unmarshal(read(t, p), &m); e != nil {
		t.Fatal(e)
	}
	m["schema"] = 999
	b, _ := json.Marshal(m)
	write(t, p, string(b))
	if _, e := localruntime.Read(o.Home); e == nil {
		t.Fatal("unknown manifest accepted")
	}
}
func TestPrivatePermissionsAndSymlinkRefusal(t *testing.T) {
	o, _ := fixture(t)
	r := ensure(t, o)
	for _, p := range []string{r.Release, filepath.Join(r.Release, "vecnet"), r.Unit, filepath.Join(o.Home, ".local/share/vecnet-codex/manifest.json")} {
		st, e := os.Stat(p)
		if e != nil {
			t.Fatal(e)
		}
		if st.Mode().Perm()&0077 != 0 {
			t.Fatalf("not private %s: %v", p, st.Mode())
		}
	}
	o, _ = fixture(t)
	outside := t.TempDir()
	if e := os.MkdirAll(filepath.Join(o.Home, ".local/share"), 0700); e != nil {
		t.Fatal(e)
	}
	if e := os.Symlink(outside, filepath.Join(o.Home, ".local/share/vecnet-codex")); e != nil {
		t.Fatal(e)
	}
	if _, e := localruntime.Ensure(context.Background(), o); e == nil {
		t.Fatal("symlink accepted")
	}
	entries, _ := os.ReadDir(outside)
	if len(entries) != 0 {
		t.Fatal("wrote through symlink")
	}
}
func TestGetenvControlsHomeAndNodeLookup(t *testing.T) {
	o, _ := fixture(t)
	home := o.Home
	o.Home = ""
	o.Getenv = func(k string) string {
		if k == "HOME" {
			return home
		}
		return ""
	}
	r := ensure(t, o)
	if !strings.HasPrefix(r.Release, home+"/") {
		t.Fatal("ignored injected HOME")
	}
	o, _ = fixture(t)
	o.Node = ""
	if _, e := localruntime.Ensure(context.Background(), o); e == nil {
		t.Fatal("fell back to real PATH")
	}
}
func TestSystemdEscaping(t *testing.T) {
	o, _ := fixture(t)
	o.Home = filepath.Join(o.Home, "space %n $HOME \"quote\"")
	if e := os.Mkdir(o.Home, 0700); e != nil {
		t.Fatal(e)
	}
	o.Node = "/opt/space %n $NODE/node"
	r := ensure(t, o)
	unit := string(read(t, r.Unit))
	if !strings.Contains(unit, "%%n") || !strings.Contains(unit, "$$NODE") || !strings.Contains(unit, `\"quote\"`) {
		t.Fatalf("unsafe systemd expansion: %s", unit)
	}
}
func TestConcurrentEnsureSerializesActivation(t *testing.T) {
	o, h := fixture(t)
	var wg sync.WaitGroup
	errs := make(chan error, 6)
	for range 6 {
		wg.Add(1)
		go func() { defer wg.Done(); _, e := localruntime.Ensure(context.Background(), o); errs <- e }()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	if n := strings.Count(strings.Join(h.calls, "\n"), " restart "); n != 1 {
		t.Fatalf("duplicate restarts: %d", n)
	}
}

// Importing daemon and the schema-reading contract proves the staged distribution
// is self-contained, without starting delivery, touching authority, or using npm.
func TestStagedImportsWithoutNodeModules(t *testing.T) {
	o, _ := fixture(t)
	r := ensure(t, o)
	node, e := exec.LookPath("node")
	if e != nil {
		t.Fatal(e)
	}
	script := `await import('./connectors/launcher/src/daemon.ts'); const m=await import('./connectors/launcher/src/contract/launch-input.ts'); if(!m)process.exit(2);`
	c := exec.Command(node, "--disable-warning=ExperimentalWarning", "--experimental-strip-types", "--input-type=module", "--eval", script)
	c.Dir = r.Release
	c.Env = []string{"HOME=" + o.Home, "PATH=/usr/bin:/bin"}
	out, e := c.CombinedOutput()
	if e != nil {
		t.Fatalf("staged imports: %v\n%s", e, out)
	}
}
func TestStagedEntrypointLoadsWithoutStartingDelivery(t *testing.T) {
	o, _ := fixture(t)
	r := ensure(t, o)
	node, e := exec.LookPath("node")
	if e != nil {
		t.Fatal(e)
	}
	c := exec.Command(node, "--disable-warning=ExperimentalWarning", "--experimental-strip-types", filepath.Join(r.Release, "connectors/launcher/src/codex/delivery-main.ts"))
	c.Dir = t.TempDir()
	c.Env = []string{"HOME=" + o.Home, "VECNET_BIN=/missing-vecnet-test-binary", "VECNET_LOCAL_MAINTENANCE=0", "PATH=/usr/bin:/bin"}
	out, e := c.CombinedOutput()
	if e == nil || !strings.Contains(string(out), "VECNET_BIN must be an absolute executable") {
		t.Fatalf("entry import failure: %v %s", e, out)
	}
	if _, e := os.Lstat(filepath.Join(o.Home, ".local/state")); !errors.Is(e, os.ErrNotExist) {
		t.Fatalf("entrypoint wrote state: %v", e)
	}
}
func TestMissingUnitAndStoppedServiceAreRepaired(t *testing.T) {
	o, h := fixture(t)
	r := ensure(t, o)
	if e := os.Remove(r.Unit); e != nil {
		t.Fatal(e)
	}
	h.active = false
	r2 := ensure(t, o)
	if !r2.Changed || !h.active {
		t.Fatalf("missing unit not repaired %+v", r2)
	}
	h.active = false
	r3 := ensure(t, o)
	if !r3.Changed || !h.active {
		t.Fatal("stopped service not activated")
	}
}
func TestCanceledInstallWritesNothing(t *testing.T) {
	o, _ := fixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, e := localruntime.Ensure(ctx, o)
	if e == nil {
		t.Fatal("canceled install succeeded")
	}
	entries, _ := os.ReadDir(o.Home)
	if len(entries) != 0 {
		t.Fatal("canceled install wrote HOME")
	}
}
func TestPendingFirstInstallIsVisible(t *testing.T) {
	o, h := fixture(t)
	h.restartErr = true
	if _, e := localruntime.Ensure(context.Background(), o); e == nil {
		t.Fatal("restart succeeded")
	}
	if !localruntime.Installed(o.Home) {
		t.Fatal("failed first activation hidden from maintenance")
	}
	if _, e := localruntime.Read(o.Home); !errors.Is(e, os.ErrNotExist) {
		t.Fatalf("failed activation marked applied: %v", e)
	}
	h.restartErr = false
	if r := ensure(t, o); !r.Changed {
		t.Fatal("pending activation did not retry")
	}
}
func TestProbeAndSystemdFailuresPrecedeWrites(t *testing.T) {
	for _, phase := range []string{"sqlite", "manager", "legacy-query", "unit-query"} {
		t.Run(phase, func(t *testing.T) {
			o, h := fixture(t)
			o.Run = func(ctx context.Context, n string, a ...string) ([]byte, error) {
				s := strings.Join(a, " ")
				if (phase == "sqlite" && strings.Contains(s, "--eval")) || (phase == "manager" && strings.Contains(s, "show-environment")) || (phase == "legacy-query" && strings.Contains(s, "list-units")) || (phase == "unit-query" && len(a) > 1 && a[1] == "show") {
					return nil, errors.New("credential-secret")
				}
				return h.run(ctx, n, a...)
			}
			_, e := localruntime.Ensure(context.Background(), o)
			if e == nil {
				t.Fatal("failed prerequisite accepted")
			}
			if strings.Contains(e.Error(), "credential-secret") {
				t.Fatal("process secrets leaked")
			}
			entries, _ := os.ReadDir(o.Home)
			if len(entries) != 0 {
				t.Fatal("failed prerequisite wrote HOME")
			}
		})
	}
}
func TestCompatibleStandaloneInstallerUnitIsAdopted(t *testing.T) {
	o, _ := fixture(t)
	release := filepath.Join(o.Home, ".local/share/vecnet-codex/0123456789abcdefabcd")
	unit := filepath.Join(o.Home, ".config/systemd/user/vecnet-codex-delivery.service")
	write(t, unit, "[Unit]\nDescription=vecnet isolated Codex delivery\nAfter=network-online.target\n[Service]\nType=simple\nUMask=0077\nEnvironment=\"HOME="+o.Home+"\"\nEnvironment=\"VECNET_BIN="+release+"/vecnet\"\nEnvironment=\"PATH=/opt/node/bin:/usr/local/bin:/usr/bin:/bin\"\nExecStart=\"/opt/node/bin/node\" --disable-warning=ExperimentalWarning --experimental-strip-types \""+release+"/connectors/launcher/src/codex/delivery-main.ts\"\nRestart=on-failure\nRestartSec=5\nTimeoutStopSec=45\n[Install]\nWantedBy=default.target\n")
	if r := ensure(t, o); !r.Changed {
		t.Fatal("legacy standalone not adopted")
	}
}
func TestForeignSymlinkAndEmptyUnitsAreRefused(t *testing.T) {
	for _, kind := range []string{"empty", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			o, _ := fixture(t)
			p := filepath.Join(o.Home, ".config/systemd/user/vecnet-codex-delivery.service")
			write(t, p, "")
			if kind == "symlink" {
				if e := os.Remove(p); e != nil {
					t.Fatal(e)
				}
				target := filepath.Join(t.TempDir(), "outside")
				write(t, target, "[Service]\nExecStart=/private\n")
				if e := os.Symlink(target, p); e != nil {
					t.Fatal(e)
				}
			}
			if _, e := localruntime.Ensure(context.Background(), o); e == nil {
				t.Fatal("unsafe unit accepted")
			}
		})
	}
}
func TestPendingSameVersionIsVisibleToVersionPredicate(t *testing.T) {
	o, h := fixture(t)
	old := ensure(t, o)
	h.active = false
	h.restartErr = true
	if _, e := localruntime.Ensure(context.Background(), o); e == nil {
		t.Fatal("failed restart accepted")
	}
	r, e := localruntime.Read(o.Home)
	if e == nil || r.Version != old.Version {
		t.Fatalf("same-version pending hidden: %+v %v", r, e)
	}
}
func TestReadDetectsMissingPrivateBinary(t *testing.T) {
	o, _ := fixture(t)
	r := ensure(t, o)
	if e := os.Remove(filepath.Join(r.Release, "vecnet")); e != nil {
		t.Fatal(e)
	}
	if _, e := localruntime.Read(o.Home); e == nil {
		t.Fatal("missing software hidden by version manifest")
	}
}
func TestWritableHomeRefused(t *testing.T) {
	o, _ := fixture(t)
	if e := os.Chmod(o.Home, 0777); e != nil {
		t.Fatal(e)
	}
	if _, e := localruntime.Ensure(context.Background(), o); e == nil {
		t.Fatal("writable HOME accepted")
	}
}
func TestEnablingAnAlreadyRunningReleaseDoesNotRestart(t *testing.T) {
	o, h := fixture(t)
	ensure(t, o)
	h.enabled = false
	before := strings.Count(strings.Join(h.calls, "\n"), " restart ")
	ensure(t, o)
	if !h.enabled {
		t.Fatal("service not enabled")
	}
	if n := strings.Count(strings.Join(h.calls, "\n"), " restart "); n != before {
		t.Fatal("unnecessary restart to enable running release")
	}
}
func TestLegacyInstallerPublicAssetDirectoryCanBePrivatized(t *testing.T) {
	o, _ := fixture(t)
	legacy := filepath.Join(o.Home, ".local/share/vecnet-codex")
	if e := os.MkdirAll(legacy, 0755); e != nil {
		t.Fatal(e)
	}
	if e := os.Chmod(legacy, 0755); e != nil {
		t.Fatal(e)
	}
	write(t, filepath.Join(legacy, "0123456789abcdefabcd/vecnet"), "old-cli-sentinel")
	ensure(t, o)
	st, e := os.Stat(legacy)
	if e != nil {
		t.Fatal(e)
	}
	if st.Mode().Perm() != 0700 {
		t.Fatal("managed directory not private")
	}
	if string(read(t, filepath.Join(legacy, "0123456789abcdefabcd/vecnet"))) != "old-cli-sentinel" {
		t.Fatal("legacy release changed")
	}
}
func TestCancellationWhileWaitingForUnitStateDoesNotApply(t *testing.T) {
	o, h := fixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	queries := 0
	o.Run = func(ctx context.Context, n string, a ...string) ([]byte, error) {
		if len(a) > 1 && a[1] == "show" {
			queries++
			if queries == 2 {
				cancel()
			}
		}
		return h.run(ctx, n, a...)
	}
	if _, e := localruntime.Ensure(ctx, o); e == nil {
		t.Fatal("canceled activation marked applied")
	}
	if localruntime.Installed(o.Home) {
		t.Fatal("canceled stage marked pending or applied")
	}
}
func TestReadChecksCompleteDistributionIntegrity(t *testing.T) {
	o, _ := fixture(t)
	r := ensure(t, o)
	for _, name := range []string{"vecnet", "connectors/launcher/src/codex/delivery-main.ts", "connectors/launcher/src/daemon.ts", "connectors/launcher/schema/launch-question.v1.schema.json", "connectors/hub-client/src/client.ts", "connectors/launcher/package.json"} {
		t.Run(name, func(t *testing.T) {
			p := filepath.Join(r.Release, name)
			original := read(t, p)
			st, e := os.Stat(p)
			if e != nil {
				t.Fatal(e)
			}
			if name == "vecnet" {
				if e = os.WriteFile(p, []byte("changed-binary"), st.Mode().Perm()); e != nil {
					t.Fatal(e)
				}
			} else {
				if e = os.Remove(p); e != nil {
					t.Fatal(e)
				}
			}
			got, e := localruntime.Read(o.Home)
			if e == nil || got.Version != r.Version {
				t.Fatalf("broken asset reported current: %s %+v %v", name, got, e)
			}
			if e = os.WriteFile(p, original, st.Mode().Perm()); e != nil {
				t.Fatal(e)
			}
		})
	}
	if _, e := localruntime.Read(o.Home); e != nil {
		t.Fatalf("restored release invalid: %v", e)
	}
}
func TestNondefaultXDGConfigRefusedBeforeWrites(t *testing.T) {
	o, _ := fixture(t)
	o.Getenv = func(k string) string {
		if k == "XDG_CONFIG_HOME" {
			return filepath.Join(o.Home, "custom-config")
		}
		return ""
	}
	_, e := localruntime.Ensure(context.Background(), o)
	if e == nil || !strings.Contains(e.Error(), "XDG_CONFIG_HOME") {
		t.Fatalf("missing precise XDG limitation: %v", e)
	}
	entries, _ := os.ReadDir(o.Home)
	if len(entries) != 0 {
		t.Fatal("unsupported XDG setup wrote HOME")
	}
}
func TestManagedAssetDriftIsBackedUpAndRepaired(t *testing.T) {
	for _, name := range []string{"connectors/launcher/src/codex/delivery-main.ts", "vecnet"} {
		t.Run(name, func(t *testing.T) {
			o, h := fixture(t)
			r := ensure(t, o)
			p := filepath.Join(r.Release, name)
			original := read(t, p)
			if name == "vecnet" {
				if e := os.WriteFile(p, []byte("modified-cli-sentinel"), 0700); e != nil {
					t.Fatal(e)
				}
			} else {
				if e := os.Remove(p); e != nil {
					t.Fatal(e)
				}
			}
			write(t, filepath.Join(r.Release, "unknown-recovery-sentinel"), "preserve-whole-directory")
			before := strings.Count(strings.Join(h.calls, "\n"), " restart ")
			r2 := ensure(t, o)
			if !r2.Changed || string(read(t, p)) != string(original) {
				t.Fatal("drift did not converge")
			}
			if _, e := localruntime.Read(o.Home); e != nil {
				t.Fatal(e)
			}
			if strings.Count(strings.Join(h.calls, "\n"), " restart ") != before+1 {
				t.Fatal("repaired software not restarted")
			}
			backups, e := os.ReadDir(filepath.Join(o.Home, ".local/share/vecnet-codex/repair-backups"))
			if e != nil || len(backups) != 1 {
				t.Fatalf("missing repair backup: %v %v", backups, e)
			}
			backup := filepath.Join(o.Home, ".local/share/vecnet-codex/repair-backups", backups[0].Name())
			if string(read(t, filepath.Join(backup, "unknown-recovery-sentinel"))) != "preserve-whole-directory" {
				t.Fatal("backup lost unknown state")
			}
			if name == "vecnet" && string(read(t, filepath.Join(backup, name))) != "modified-cli-sentinel" {
				t.Fatal("changed CLI not preserved")
			}
		})
	}
}
func TestFailedRepairRestartRetriesAtSameVersion(t *testing.T) {
	o, h := fixture(t)
	r := ensure(t, o)
	if e := os.WriteFile(filepath.Join(r.Release, "vecnet"), []byte("broken-cli"), 0700); e != nil {
		t.Fatal(e)
	}
	h.restartErr = true
	if _, e := localruntime.Ensure(context.Background(), o); e == nil {
		t.Fatal("repair restart failure accepted")
	}
	h.restartErr = false
	before := strings.Count(strings.Join(h.calls, "\n"), " restart ")
	ensure(t, o)
	if strings.Count(strings.Join(h.calls, "\n"), " restart ") != before+1 {
		t.Fatal("same-version repaired service was not retried after failed restart")
	}
}
func TestInterruptedRepairRestartsActiveOldProcess(t *testing.T) {
	for _, phase := range []string{"after-backup", "after-publication"} {
		t.Run(phase, func(t *testing.T) {
			o, h := fixture(t)
			r := ensure(t, o)
			original := read(t, filepath.Join(r.Release, "vecnet"))
			root := filepath.Join(o.Home, ".local/share/vecnet-codex")
			write(t, filepath.Join(root, "pending.json"), string(read(t, filepath.Join(root, "manifest.json"))))
			backup := filepath.Join(root, "repair-backups/interrupted")
			if e := os.MkdirAll(filepath.Dir(backup), 0700); e != nil {
				t.Fatal(e)
			}
			if e := os.Rename(r.Release, backup); e != nil {
				t.Fatal(e)
			}
			if phase == "after-publication" {
				if e := os.CopyFS(r.Release, os.DirFS(backup)); e != nil {
					t.Fatal(e)
				}
				if e := filepath.WalkDir(r.Release, func(p string, d os.DirEntry, e error) error {
					if e != nil {
						return e
					}
					mode := os.FileMode(0600)
					if d.IsDir() || p == filepath.Join(r.Release, "vecnet") {
						mode = 0700
					}
					return os.Chmod(p, mode)
				}); e != nil {
					t.Fatal(e)
				}
			}
			h.active = true
			before := strings.Count(strings.Join(h.calls, "\n"), " restart ")
			got := ensure(t, o)
			if !got.Changed || strings.Count(strings.Join(h.calls, "\n"), " restart ") != before+1 {
				t.Fatal("interrupted repair accepted active old process without restart")
			}
			if string(read(t, filepath.Join(r.Release, "vecnet"))) != string(original) {
				t.Fatal("release not restored")
			}
			read(t, filepath.Join(backup, "vecnet"))
			if _, e := os.Lstat(filepath.Join(root, "pending.json")); !errors.Is(e, os.ErrNotExist) {
				t.Fatal("successful activation left pending")
			}
			if _, e := localruntime.Read(o.Home); e != nil {
				t.Fatal(e)
			}
		})
	}
}
