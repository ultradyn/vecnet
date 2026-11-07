package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

type deliveryFixture struct {
	t                            *testing.T
	env                          map[string]string
	app                          *app
	dir, entry, config, bindings string
}

func deliveryWrite(t *testing.T, path, text string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(text), mode); err != nil {
		t.Fatal(err)
	}
}
func deliveryQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }
func newDeliveryFixture(t *testing.T) *deliveryFixture {
	t.Helper()
	d := t.TempDir()
	f := &deliveryFixture{t: t, dir: d, entry: filepath.Join(d, "release", "delivery-main.ts"), config: filepath.Join(d, "v.toml"), bindings: filepath.Join(d, "bindings.json")}
	for _, sub := range []string{"bin", "release", "run", "data", "codex", "config"} {
		if err := os.Mkdir(filepath.Join(d, sub), 0700); err != nil {
			t.Fatal(err)
		}
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for name, role := range map[string]string{"vecnet": "cli", "node": "node", "systemctl": "systemctl"} {
		deliveryWrite(t, filepath.Join(d, "bin", name), "#!/bin/sh\nexport GORACE=atexit_sleep_ms=0\nexec "+deliveryQuote(exe)+" -test.run=^TestCodexDeliveryProcess$ -- delivery-fixture "+role+" \"$@\"\n", 0700)
	}
	deliveryWrite(t, f.entry, "fixture release one", 0600)
	f.env = map[string]string{"HOME": d, "XDG_CONFIG_HOME": filepath.Join(d, "config"), "CODEX_HOME": filepath.Join(d, "codex"), "XDG_DATA_HOME": filepath.Join(d, "data"), "XDG_RUNTIME_DIR": filepath.Join(d, "run"), "PATH": filepath.Join(d, "bin"), "VECNET_BIN": filepath.Join(d, "bin", "vecnet"), "VECNET_CODEX_DELIVERY_ENTRY": f.entry}
	f.app = &app{ctx: context.Background(), getenv: func(k string) string { return f.env[k] }, stdout: io.Discard, stderr: io.Discard}
	t.Cleanup(func() {
		paths, _ := filepath.Glob(filepath.Join(d, "run", "vecnet", "*.json"))
		paths = append(paths, filepath.Join(d, "claim-ready"))
		for _, path := range paths {
			raw, _ := os.ReadFile(path)
			var m struct {
				PID   int    `json:"pid"`
				Birth string `json:"birth"`
			}
			if json.Unmarshal(raw, &m) == nil && m.PID > 1 && m.Birth != "" && procStart(m.PID) == m.Birth {
				_ = syscall.Kill(m.PID, syscall.SIGKILL)
			}
		}
	})
	return f
}
func (f *deliveryFixture) environ() []string {
	var env []string
	for k, v := range f.env {
		env = append(env, k+"="+v)
	}
	return env
}
func (f *deliveryFixture) ensure(hub string) error {
	return f.app.ensureCodexDelivery(context.Background(), hub, f.config, f.bindings)
}
func (f *deliveryFixture) command(args ...string) *exec.Cmd {
	c := exec.Command(f.env["VECNET_BIN"], args...)
	c.Env = f.environ()
	return c
}
func deliveryWait(t *testing.T, check func() bool) {
	t.Helper()
	until := time.Now().Add(5 * time.Second)
	for time.Now().Before(until) {
		if check() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("delivery process did not reach expected state")
}
func (f *deliveryFixture) starts() []string {
	paths, _ := filepath.Glob(filepath.Join(f.dir, "data", "vecnet", "codex-delivery", "*", "starts"))
	var lines []string
	for _, path := range paths {
		raw, _ := os.ReadFile(path)
		lines = append(lines, strings.Fields(string(raw))...)
	}
	return lines
}
func (f *deliveryFixture) owner() (int, string) {
	f.t.Helper()
	paths, _ := filepath.Glob(filepath.Join(f.dir, "run", "vecnet", "delivery-*.json"))
	if len(paths) != 1 {
		f.t.Fatalf("owner metadata files=%v", paths)
	}
	raw, err := os.ReadFile(paths[0])
	if err != nil {
		f.t.Fatal(err)
	}
	var m struct {
		PID int `json:"pid"`
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		f.t.Fatal(err)
	}
	return m.PID, paths[0]
}

// Runs only as an explicit subprocess. The node boundary is a credential-free
// fixture retaining FD3 and responding to SIGTERM, never a Codex server.
func TestCodexDeliveryProcess(t *testing.T) {
	var args []string
	for i, arg := range os.Args {
		if arg == "delivery-fixture" {
			args = os.Args[i+1:]
			break
		}
	}
	if len(args) == 0 {
		return
	}
	switch args[0] {
	case "cli":
		os.Exit(Run(args[1:], strings.NewReader(""), os.Stdout, os.Stderr, os.Getenv))
	case "claim":
		birth := procStart(os.Getpid())
		raw, _ := json.Marshal(map[string]any{"pid": os.Getpid(), "birth": birth})
		_ = os.WriteFile(filepath.Join(os.Getenv("HOME"), "claim-ready"), raw, 0600)
		until := time.Now().Add(5 * time.Second)
		for time.Now().Before(until) {
			if _, err := os.Stat(filepath.Join(os.Getenv("HOME"), "release-claim")); err == nil {
				os.Exit(0)
			}
			time.Sleep(10 * time.Millisecond)
		}
		os.Exit(0)
	case "systemctl":
		// No installed fixture template by default. Tests opt in by creating a
		// private marker, so the host service manager is never contacted.
		if _, err := os.Stat(filepath.Join(os.Getenv("HOME"), "systemd-installed")); err != nil {
			fmt.Println("not-found")
			os.Exit(1)
		}
		if strings.Contains(strings.Join(args, " "), "show") {
			fmt.Println("loaded")
			os.Exit(0)
		}
		file, _ := os.OpenFile(filepath.Join(os.Getenv("HOME"), "systemctl-calls"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
		fmt.Fprintln(file, strings.Join(args[1:], " "))
		file.Close()
		// Service operation invokes the fixture deliver as an actual process.
		hubID := strings.TrimSuffix(strings.TrimPrefix(args[len(args)-1], "vecnet-delivery@"), ".service")
		if strings.Contains(strings.Join(args, " "), "restart") {
			meta := filepath.Join(os.Getenv("XDG_RUNTIME_DIR"), "vecnet", "delivery-"+hubID+".json")
			raw, _ := os.ReadFile(meta)
			var m struct {
				PID     int    `json:"pid"`
				Birth   string `json:"birth"`
				Managed bool   `json:"managed"`
			}
			_ = json.Unmarshal(raw, &m)
			if m.PID <= 1 || procStart(m.PID) != m.Birth {
				os.Exit(4)
			}
			if m.Managed {
				_ = syscall.Kill(m.PID, syscall.SIGTERM)
				lease, err := os.OpenFile(strings.TrimSuffix(meta, ".json")+".lock", os.O_RDWR, 0600)
				if err != nil {
					os.Exit(5)
				}
				until := time.Now().Add(3 * time.Second)
				for syscall.Flock(int(lease.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) != nil {
					if time.Now().After(until) {
						os.Exit(6)
					}
					time.Sleep(10 * time.Millisecond)
				}
				_ = syscall.Flock(int(lease.Fd()), syscall.LOCK_UN)
				lease.Close()
			}
		}

		cmd := exec.Command(os.Getenv("VECNET_BIN"), "codex", "deliver", "--hub-id", hubID)
		cmd.Env = os.Environ()
		cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		if cmd.Start() != nil {
			os.Exit(2)
		}
		go cmd.Wait()
		os.Exit(0)
	case "node":
		if len(args) > 1 && args[1] == "--version" {
			fmt.Println("v22.18.0")
			os.Exit(0)
		}
		command := ""
		var state, hub string
		for i, arg := range args {
			if arg == "run" || arg == "probe" {
				command = arg
			}
			if i+1 < len(args) {
				switch arg {
				case "--state-dir":
					state = args[i+1]
				case "--hub":
					hub = args[i+1]
				}
			}
		}
		if command == "probe" {
			if os.Getenv("VECNET_TOKEN") != "" || os.Getenv("CODEX_THREAD_ID") != "" || os.Getenv("VECNET_CODEX_AUTO_DELIVERY") != "0" {
				os.Exit(13)
			}
			fmt.Println(`{"loaded":true,"previous":"gone"}`)
			os.Exit(0)
		}
		lease := os.NewFile(3, "lease")
		st, err := lease.Stat()
		if err != nil || !st.Mode().IsRegular() {
			os.Exit(7)
		}
		if err := syscall.Flock(3, syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
			os.Exit(8)
		}
		if !strings.HasPrefix(hub, "https://hub") {
			os.Exit(9)
		}
		file, err := os.OpenFile(filepath.Join(state, "starts"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			os.Exit(10)
		}
		fmt.Fprintln(file, os.Getpid())
		file.Close()
		// The stable recovery file stands in for Node's independently tested DB.
		ledger := filepath.Join(state, "ledger.sqlite")
		file, err = os.OpenFile(ledger, os.O_CREATE|os.O_RDWR, 0600)
		if err != nil {
			os.Exit(11)
		}
		file.Close()

		if _, err := os.Stat(filepath.Join(os.Getenv("HOME"), "spawn-claim")); err == nil {
			exe, _ := os.Executable()
			cmd := exec.Command(exe, "-test.run=^TestCodexDeliveryProcess$", "--", "delivery-fixture", "claim")
			cmd.Env = os.Environ()
			cmd.ExtraFiles = []*os.File{lease}
			if cmd.Start() != nil {
				os.Exit(12)
			}
			go cmd.Wait()
		}
		stop := make(chan os.Signal, 1)
		signal.Notify(stop, syscall.SIGTERM, syscall.SIGINT)
		<-stop
		lease.Close()
		os.Exit(0)
	}
	os.Exit(3)
}

func TestCodexDeliveryConcurrentEnsure(t *testing.T) {
	f := newDeliveryFixture(t)
	var commands []*exec.Cmd
	for i := 0; i < 8; i++ {
		cmd := f.command("codex", "delivery-refresh", "--hub", "HTTPS://HUB.INVALID:443/", "--config", f.config, "--bindings", f.bindings)
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		commands = append(commands, cmd)
	}
	for _, cmd := range commands {
		if err := cmd.Wait(); err != nil {
			t.Fatalf("concurrent ensure: %v", err)
		}
	}
	deliveryWait(t, func() bool { return len(f.starts()) == 1 })
	pid, _ := f.owner()
	if got := f.starts()[0]; got != strconv.Itoa(pid) {
		t.Fatalf("Go owner did not exec runner in same PID: metadata=%d runner=%s", pid, got)
	}
	raw, err := os.ReadFile(filepath.Join(f.dir, "data", "vecnet", "codex-delivery", "registry.json"))
	if err != nil {
		t.Fatal(err)
	}
	var reg struct {
		Version int
		Sources []struct{ Config, Bindings string }
	}
	if json.Unmarshal(raw, &reg) != nil || reg.Version != 1 || len(reg.Sources) != 1 || reg.Sources[0].Config != f.config || reg.Sources[0].Bindings != f.bindings {
		t.Fatalf("deduplicated registry: %s", raw)
	}
	// Direct contenders exit successfully without replacing the live runner.
	if out, err := f.command("codex", "deliver", "--hub", "https://hub.invalid").CombinedOutput(); err != nil {
		t.Fatalf("contender: %v %s", err, out)
	}
	if len(f.starts()) != 1 {
		t.Fatal("contender created a second sender")
	}
}

func TestCodexDeliveryTwoHubs(t *testing.T) {
	f := newDeliveryFixture(t)
	for _, hub := range []string{"https://hub.invalid", "https://hub-two.invalid"} {
		if err := f.ensure(hub); err != nil {
			t.Fatalf("independent hub ensure: %v", err)
		}
	}
	deliveryWait(t, func() bool { return len(f.starts()) == 2 })
	if f.starts()[0] == f.starts()[1] {
		t.Fatal("two hubs share an owner")
	}
	paths, _ := filepath.Glob(filepath.Join(f.dir, "data", "vecnet", "codex-delivery", "*", "ledger.sqlite"))
	if len(paths) != 2 {
		t.Fatalf("hub state directories=%v", paths)
	}
	// A second source is admitted atomically without starting a new owner.
	f.config = filepath.Join(f.dir, "other.toml")
	if err := f.ensure("https://hub.invalid"); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(f.dir, "data", "vecnet", "codex-delivery", "registry.json"))
	var r struct{ Sources []any }
	if json.Unmarshal(raw, &r) != nil || len(r.Sources) != 2 {
		t.Fatalf("source registry=%s", raw)
	}
}

func TestCodexDeliveryWorkerCrash(t *testing.T) {
	f := newDeliveryFixture(t)
	if err := f.ensure("https://hub.invalid"); err != nil {
		t.Fatalf("initial worker start: %v", err)
	}
	deliveryWait(t, func() bool { return len(f.starts()) == 1 })
	pid, meta := f.owner()
	leasePath := strings.TrimSuffix(meta, ".json") + ".lock"
	before, err := os.Stat(leasePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Kill(pid, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	if err := f.ensure("https://hub.invalid"); err != nil {
		t.Fatalf("recovery after worker crash: %v", err)
	}
	deliveryWait(t, func() bool { return len(f.starts()) == 2 })
	after, _ := os.Stat(leasePath)
	if !os.SameFile(before, after) {
		t.Fatal("crash recovery replaced the lock inode")
	}
	next, _ := f.owner()
	if next == pid {
		t.Fatal("crash recovery did not replace worker")
	}
}

func TestCodexDeliveryUpgrade(t *testing.T) {
	for _, generation := range []string{"launcher", "binary"} {
		t.Run(generation, func(t *testing.T) {
			f := newDeliveryFixture(t)
			if err := f.ensure("https://hub.invalid"); err != nil {
				t.Fatalf("initial generation start: %v", err)
			}
			deliveryWait(t, func() bool { return len(f.starts()) == 1 })
			pid, meta := f.owner()
			before, _ := os.Stat(strings.TrimSuffix(meta, ".json") + ".lock")
			ledgers, _ := filepath.Glob(filepath.Join(f.dir, "data", "vecnet", "codex-delivery", "*", "ledger.sqlite"))
			deliveryWrite(t, ledgers[0], "accepted ambiguous recovery row", 0600)
			if generation == "launcher" {
				newEntry := filepath.Join(f.dir, "release-two.ts")
				deliveryWrite(t, newEntry, "fixture release two", 0600)
				f.env["VECNET_CODEX_DELIVERY_ENTRY"] = newEntry
			} else {
				raw, _ := os.ReadFile(f.env["VECNET_BIN"])
				deliveryWrite(t, f.env["VECNET_BIN"], string(raw)+"# binary generation two\n", 0700)
			}
			if err := f.ensure("https://hub.invalid"); err != nil {
				t.Fatalf("upgrade replacement: %v", err)
			}
			deliveryWait(t, func() bool { return len(f.starts()) == 2 })
			next, _ := f.owner()
			if pid == next {
				t.Fatal("generation mismatch did not replace worker")
			}
			after, _ := os.Stat(strings.TrimSuffix(meta, ".json") + ".lock")
			if !os.SameFile(before, after) {
				t.Fatal("upgrade replaced lock inode")
			}
			raw, _ := os.ReadFile(ledgers[0])
			if string(raw) != "accepted ambiguous recovery row" {
				t.Fatal("upgrade lost recovery state")
			}
		})
	}
}

func TestCodexDeliveryStalePID(t *testing.T) {
	f := newDeliveryFixture(t)
	if err := f.ensure("https://hub.invalid"); err != nil {
		t.Fatalf("initial owner start: %v", err)
	}
	deliveryWait(t, func() bool { return len(f.starts()) == 1 })
	owner, meta := f.owner()
	originalMeta, _ := os.ReadFile(meta)
	defer deliveryWrite(t, meta, string(originalMeta), 0600)
	innocent := exec.Command("/bin/sleep", "60")
	innocent.Env = f.environ()
	if err := innocent.Start(); err != nil {
		t.Fatal(err)
	}
	innocentDone := make(chan struct{})
	go func() { _ = innocent.Wait(); close(innocentDone) }()
	defer func() { _ = innocent.Process.Kill(); <-innocentDone }()
	raw, _ := os.ReadFile(meta)
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	// A real PID and correct birth are still insufficient: it must hold this lease.
	m["pid"] = innocent.Process.Pid
	m["birth"] = procStart(innocent.Process.Pid)
	raw, _ = json.Marshal(m)
	deliveryWrite(t, meta, string(raw), 0600)
	next := filepath.Join(f.dir, "next.ts")
	deliveryWrite(t, next, "next release", 0600)
	f.env["VECNET_CODEX_DELIVERY_ENTRY"] = next
	if err := f.ensure("https://hub.invalid"); err == nil {
		t.Fatal("unproven stale PID must refuse replacement")
	}
	select {
	case <-innocentDone:
		t.Fatal("signaled unrelated PID")
	default:
	}
	if err := syscall.Kill(owner, 0); err != nil {
		t.Fatal("lost live owner while refusing stale PID")
	}
}

func TestCodexDeliveryUnsafeRuntime(t *testing.T) {
	for _, kind := range []string{"public", "symlink", "lock-symlink", "lock-fifo"} {
		t.Run(kind, func(t *testing.T) {
			f := newDeliveryFixture(t)
			// The valid control ensures refusal isn't a blanket stub.
			if err := f.ensure("https://hub.invalid"); err != nil {
				t.Fatalf("safe runtime control: %v", err)
			}
			deliveryWait(t, func() bool { return len(f.starts()) == 1 })
			switch kind {
			case "public":
				if err := os.Chmod(f.env["XDG_RUNTIME_DIR"], 0777); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				f.env["XDG_RUNTIME_DIR"] = filepath.Join(f.dir, "run-link")
				if err := os.Symlink(filepath.Join(f.dir, "run"), f.env["XDG_RUNTIME_DIR"]); err != nil {
					t.Fatal(err)
				}
			case "lock-symlink", "lock-fifo":
				// A distinct hub has no live lease: don't unlink any lock inode.
				_, slug, _ := codexDeliveryHub("https://hub-unsafe.invalid")
				path := filepath.Join(f.dir, "run", "vecnet", "delivery-"+slug+".lock")
				if kind == "lock-symlink" {
					deliveryWrite(t, filepath.Join(f.dir, "victim"), "untouched", 0600)
					if err := os.Symlink(filepath.Join(f.dir, "victim"), path); err != nil {
						t.Fatal(err)
					}
				} else {
					if err := syscall.Mkfifo(path, 0600); err != nil {
						t.Fatal(err)
					}
				}
			}
			if err := f.ensure("https://hub-unsafe.invalid"); err == nil {
				t.Fatal("unsafe runtime accepted")
			}
		})
	}
}

func TestCodexDeliveryOldNode(t *testing.T) {
	f := newDeliveryFixture(t)
	deliveryWrite(t, filepath.Join(f.dir, "bin", "node"), "#!/bin/sh\nif [ \"$1\" = --version ]; then echo v20.19.0; exit 0; fi\nexec "+deliveryQuote(f.env["VECNET_BIN"])+" \"$@\"\n", 0700)
	if err := f.ensure("https://hub.invalid"); err == nil {
		t.Fatal("unsupported Node must fail closed")
	}
	if paths, _ := filepath.Glob(filepath.Join(f.dir, "run", "vecnet", "*.json")); len(paths) != 0 {
		t.Fatal("unsupported Node started a doomed owner")
	}
}

func TestCodexDeliveryManagedUpgrade(t *testing.T) {
	f := newDeliveryFixture(t)
	deliveryWrite(t, filepath.Join(f.dir, "systemd-installed"), "fixture only", 0600)
	if err := f.ensure("https://hub.invalid"); err != nil {
		t.Fatal(err)
	}
	deliveryWait(t, func() bool { return len(f.starts()) == 1 })
	next := filepath.Join(f.dir, "new-release.ts")
	deliveryWrite(t, next, "next release", 0600)
	f.env["VECNET_CODEX_DELIVERY_ENTRY"] = next
	if err := f.ensure("https://hub.invalid"); err != nil {
		t.Fatalf("managed generation restart: %v", err)
	}
	deliveryWait(t, func() bool { return len(f.starts()) == 2 })
	raw, err := os.ReadFile(filepath.Join(f.dir, "systemctl-calls"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(raw), " start ") != 1 || strings.Count(string(raw), " restart ") != 1 {
		t.Fatalf("service calls: %s", raw)
	}
}

func TestCodexDeliveryOrphanClaim(t *testing.T) {
	f := newDeliveryFixture(t)
	deliveryWrite(t, filepath.Join(f.dir, "spawn-claim"), "fixture only", 0600)
	if err := f.ensure("https://hub.invalid"); err != nil {
		t.Fatal(err)
	}
	deliveryWait(t, func() bool { _, err := os.Stat(filepath.Join(f.dir, "claim-ready")); return err == nil })
	pid, _ := f.owner()
	if err := syscall.Kill(pid, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	deliveryWait(t, func() bool { return !deliveryOwnerRunning(deliveryOwner{PID: pid, Birth: procStart(pid)}) })
	_, slug, _ := codexDeliveryHub("https://hub.invalid")
	if out, err := f.command("codex", "deliver", "--hub-id", slug).CombinedOutput(); err == nil {
		t.Fatalf("managed crash restart must fail retryably while claim child owns lease: %s", out)
	}
	done := make(chan error, 1)
	go func() { done <- f.ensure("https://hub.invalid") }()
	select {
	case err := <-done:
		t.Fatalf("ensure bypassed orphan claim lease: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	if len(f.starts()) != 1 {
		t.Fatal("new sender started during pending claim")
	}
	deliveryWrite(t, filepath.Join(f.dir, "release-claim"), "done", 0600)
	if err := <-done; err != nil {
		t.Fatalf("replacement after bounded child release: %v", err)
	}
	deliveryWait(t, func() bool { return len(f.starts()) == 2 })
}

func TestCodexDeliveryProbe(t *testing.T) {
	f := newDeliveryFixture(t)
	f.env["VECNET_TOKEN"] = "fixture-must-not-inherit"
	f.env["CODEX_THREAD_ID"] = bindT1
	probe, err := f.app.probeCodexDelivery(context.Background(), f.env["CODEX_HOME"], bindT1, bindT2)
	if err != nil || !probe.Loaded || probe.Previous != "gone" {
		t.Fatalf("read-only probe result=%+v err=%v", probe, err)
	}
	if paths, _ := filepath.Glob(filepath.Join(f.dir, "data", "vecnet", "codex-delivery", "*")); len(paths) != 0 {
		t.Fatal("probe created delivery state")
	}
}

func TestCodexDeliveryBounded(t *testing.T) {
	f := newDeliveryFixture(t)
	deliveryWrite(t, filepath.Join(f.dir, "bin", "node"), "#!/bin/sh\nexec /bin/sleep 10\n", 0700)
	for _, action := range []string{"ensure", "probe"} {
		t.Run(action, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
			defer cancel()
			start := time.Now()
			var err error
			if action == "ensure" {
				err = f.app.ensureCodexDelivery(ctx, "https://hub.invalid", f.config, f.bindings)
			} else {
				_, err = f.app.probeCodexDelivery(ctx, f.env["CODEX_HOME"], bindT1, "")
			}
			if err == nil || time.Since(start) > time.Second {
				t.Fatalf("hung Node boundary not bounded: %v elapsed=%s", err, time.Since(start))
			}
		})
	}
}

func TestCodexDeliveryInstalledAssets(t *testing.T) {
	for _, layout := range []string{"current", "npm"} {
		t.Run(layout, func(t *testing.T) {
			f := newDeliveryFixture(t)
			f.env["VECNET_CODEX_DELIVERY_ENTRY"] = ""
			var entry string
			if layout == "current" {
				root := filepath.Join(f.dir, ".local", "share", "vecnet-launcher")
				entry = filepath.Join(root, "release", "launcher", "src", "codex", "delivery-main.ts")
				if err := os.MkdirAll(filepath.Dir(entry), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Join(root, "release"), filepath.Join(root, "current")); err != nil {
					t.Fatal(err)
				}
			} else {
				root := filepath.Join(f.dir, "npm")
				binary := filepath.Join(root, "native", "linux-x64", "vecnet")
				if err := os.MkdirAll(filepath.Dir(binary), 0700); err != nil {
					t.Fatal(err)
				}
				raw, _ := os.ReadFile(f.env["VECNET_BIN"])
				deliveryWrite(t, binary, string(raw), 0700)
				f.env["VECNET_BIN"] = binary
				entry = filepath.Join(root, "connectors", "launcher", "src", "codex", "delivery-main.ts")
				if err := os.MkdirAll(filepath.Dir(entry), 0700); err != nil {
					t.Fatal(err)
				}
			}
			deliveryWrite(t, entry, "installed release", 0600)
			if err := f.ensure("https://hub.invalid"); err != nil {
				t.Fatalf("installed asset discovery: %v", err)
			}
			deliveryWait(t, func() bool { return len(f.starts()) == 1 })
			out, err := f.command("codex", "delivery-status", "--hub", "https://hub.invalid").Output()
			if err != nil || !strings.Contains(string(out), `"running":true`) {
				t.Fatalf("delivery status: %v %s", err, out)
			}
		})
	}
}

func TestCodexDeliveryRefreshRegistry(t *testing.T) {
	f := newDeliveryFixture(t)
	if err := f.ensure("https://hub.invalid"); err != nil {
		t.Fatal(err)
	}
	_, slug, _ := codexDeliveryHub("https://hub.invalid")
	if out, err := f.command("codex", "delivery-refresh", "--hub-id", slug).CombinedOutput(); err != nil {
		t.Fatalf("registered refresh: %v %s", err, out)
	}
	raw, err := os.ReadFile(filepath.Join(f.dir, "data", "vecnet", "codex-delivery", "registry.json"))
	if err != nil {
		t.Fatal(err)
	}
	var r struct {
		Sources []struct{ Config, Bindings string }
	}
	if json.Unmarshal(raw, &r) != nil || len(r.Sources) != 1 || r.Sources[0].Config != f.config || r.Sources[0].Bindings != f.bindings {
		t.Fatalf("refresh fabricated default sources instead of reusing registered paths: %s", raw)
	}
}

func TestCodexDeliveryInstallTransition(t *testing.T) {
	for _, generation := range []string{"same-generation", "new-generation"} {
		t.Run(generation, func(t *testing.T) {
			f := newDeliveryFixture(t)
			if err := f.ensure("https://hub.invalid"); err != nil {
				t.Fatal(err)
			}
			deliveryWait(t, func() bool { return len(f.starts()) == 1 })
			deliveryWrite(t, filepath.Join(f.dir, "systemd-installed"), "fixture only", 0600)
			if generation == "new-generation" {
				next := filepath.Join(f.dir, "installed-release.ts")
				deliveryWrite(t, next, "installed generation", 0600)
				f.env["VECNET_CODEX_DELIVERY_ENTRY"] = next
			}
			if err := f.ensure("https://hub.invalid"); err != nil {
				t.Fatalf("installed service cannot stop a detached owner: %v", err)
			}
			deliveryWait(t, func() bool { return len(f.starts()) == 2 })
			raw, _ := os.ReadFile(filepath.Join(f.dir, "systemctl-calls"))
			if strings.Count(string(raw), " start ") != 1 || strings.Contains(string(raw), " restart ") {
				t.Fatalf("detached owner must be verified/drained, then managed start: %s", raw)
			}
		})
	}
}

func TestCodexDeliveryConcurrentUpgrade(t *testing.T) {
	f := newDeliveryFixture(t)
	if err := f.ensure("https://hub.invalid"); err != nil {
		t.Fatal(err)
	}
	deliveryWait(t, func() bool { return len(f.starts()) == 1 })
	next := filepath.Join(f.dir, "concurrent-release.ts")
	deliveryWrite(t, next, "next generation", 0600)
	f.env["VECNET_CODEX_DELIVERY_ENTRY"] = next
	var commands []*exec.Cmd
	for i := 0; i < 8; i++ {
		cmd := f.command("codex", "delivery-refresh", "--hub", "https://hub.invalid", "--config", f.config, "--bindings", f.bindings)
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		commands = append(commands, cmd)
	}
	for _, cmd := range commands {
		if err := cmd.Wait(); err != nil {
			t.Fatalf("concurrent upgrade: %v", err)
		}
	}
	deliveryWait(t, func() bool { return len(f.starts()) == 2 })
	if len(f.starts()) != 2 {
		t.Fatal("competing upgrades created redundant senders")
	}
}

func TestCodexDeliveryWrongBirth(t *testing.T) {
	f := newDeliveryFixture(t)
	if err := f.ensure("https://hub.invalid"); err != nil {
		t.Fatal(err)
	}
	deliveryWait(t, func() bool { return len(f.starts()) == 1 })
	pid, meta := f.owner()
	raw, _ := os.ReadFile(meta)
	saved := string(raw)
	// Restore the actual fixture identity even if the assertion fails.
	defer deliveryWrite(t, meta, saved, 0600)
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	m["birth"] = "unrelated-birth"
	raw, _ = json.Marshal(m)
	deliveryWrite(t, meta, string(raw), 0600)
	next := filepath.Join(f.dir, "wrong-birth-release.ts")
	deliveryWrite(t, next, "next generation", 0600)
	f.env["VECNET_CODEX_DELIVERY_ENTRY"] = next
	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	if err := f.app.ensureCodexDelivery(ctx, "https://hub.invalid", f.config, f.bindings); err == nil {
		t.Fatal("unverified process birth accepted")
	}
	if err := syscall.Kill(pid, 0); err != nil {
		t.Fatal("wrong-birth metadata caused a signal")
	}
	if len(f.starts()) != 1 {
		t.Fatal("wrong-birth metadata caused overlapping replacement")
	}
}
