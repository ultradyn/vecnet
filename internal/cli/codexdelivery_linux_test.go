package cli

import (
	"context"
	"encoding/json"
	"errors"
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
	return newDeliveryFixtureIn(t, t.TempDir())
}
func newDeliveryFixtureIn(t *testing.T, d string) *deliveryFixture {
	t.Helper()
	f := &deliveryFixture{t: t, dir: d, entry: filepath.Join(d, "release", "delivery-main.ts"), config: filepath.Join(d, "v.toml"), bindings: filepath.Join(d, "bindings.json")}
	for _, sub := range []string{"bin", "release", "run", ".local", ".local/share", "codex", "config"} {
		if err := os.Mkdir(filepath.Join(d, sub), 0700); err != nil {
			t.Fatal(err)
		}
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for name, role := range map[string]string{"vecnet": "cli", "node": "node", "systemctl": "systemctl"} {
		deliveryWrite(t, filepath.Join(d, "bin", name), "#!/bin/sh\nexport GORACE=atexit_sleep_ms=0\nexport VECNET_TEST_MANAGER_ROOT="+deliveryQuote(d)+"\nexec "+deliveryQuote(exe)+" -test.run=^TestCodexDeliveryProcess$ -- delivery-fixture "+role+" \"$@\"\n", 0700)
	}
	deliveryWrite(t, f.entry, "fixture release one", 0600)
	f.env = map[string]string{"HOME": d, "XDG_CONFIG_HOME": filepath.Join(d, "config"), "CODEX_HOME": filepath.Join(d, "codex"), "XDG_DATA_HOME": filepath.Join(d, ".local/share"), "XDG_RUNTIME_DIR": filepath.Join(d, "run"), "PATH": filepath.Join(d, "bin"), "VECNET_BIN": filepath.Join(d, "bin", "vecnet"), "VECNET_CODEX_DELIVERY_ENTRY": f.entry}
	prior := deliveryAccountScope
	deliveryAccountScope = func() (string, string, error) { return d, filepath.Join(d, "run"), nil }
	t.Cleanup(func() { deliveryAccountScope = prior })
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
func (f *deliveryFixture) installManaged() {
	f.t.Helper()
	manager := map[string]string{
		"HOME": f.dir, "XDG_DATA_HOME": filepath.Join(f.dir, ".local/share"),
		"XDG_RUNTIME_DIR": filepath.Join(f.dir, "run"), "PATH": filepath.Join(f.dir, "bin"),
		"XDG_CONFIG_HOME": filepath.Join(f.dir, "manager-config"), "CODEX_HOME": filepath.Join(f.dir, "manager-codex"),
		"VECNET_BIN": f.env["VECNET_BIN"], "VECNET_CODEX_DELIVERY_ENTRY": f.env["VECNET_CODEX_DELIVERY_ENTRY"],
	}
	raw, _ := json.Marshal(manager)
	deliveryWrite(f.t, filepath.Join(f.dir, "manager-env.json"), string(raw), 0600)
	root := filepath.Join(f.dir, ".local/share/vecnet-launcher")
	if err := os.MkdirAll(root, 0700); err != nil {
		f.t.Fatal(err)
	}
	raw, _ = json.Marshal(map[string]any{"version": 1, "binary": manager["VECNET_BIN"], "entry": manager["VECNET_CODEX_DELIVERY_ENTRY"], "node": filepath.Join(f.dir, "bin/node"), "path": manager["PATH"]})
	deliveryWrite(f.t, filepath.Join(root, "delivery.json"), string(raw), 0600)
	deliveryWrite(f.t, filepath.Join(f.dir, "systemd-installed"), "fixture only", 0600)
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
	paths, _ := filepath.Glob(filepath.Join(f.dir, ".local/share", "vecnet", "codex-delivery", "*", "starts"))
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
	if root := os.Getenv("VECNET_TEST_MANAGER_ROOT"); root != "" {
		deliveryAccountScope = func() (string, string, error) { return root, filepath.Join(root, "run"), nil }
	}
	switch args[0] {
	case "cli":
		os.Exit(Run(args[1:], strings.NewReader(""), os.Stdout, os.Stderr, os.Getenv))
	case "delayed-ensure":
		// Pause release discovery after the binary has been resolved and
		// fingerprinted, before start serialization. The launcher stays fixed.
		paused := false
		getenv := func(key string) string {
			if key == "VECNET_CODEX_DELIVERY_ENTRY" && !paused {
				paused = true
				ready := filepath.Join(os.Getenv("HOME"), "old-ensure-paused")
				if os.WriteFile(ready, []byte("binary selected"), 0600) != nil {
					os.Exit(14)
				}
				until := time.Now().Add(5 * time.Second)
				for {
					if _, err := os.Stat(filepath.Join(os.Getenv("HOME"), "resume-old-ensure")); err == nil {
						break
					}
					if time.Now().After(until) {
						os.Exit(15)
					}
					time.Sleep(10 * time.Millisecond)
				}
			}
			return os.Getenv(key)
		}
		a := &app{ctx: context.Background(), getenv: getenv, stdout: os.Stdout, stderr: os.Stderr}
		if err := a.ensureCodexDelivery(a.ctx, args[1], args[2], args[3]); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(16)
		}
		os.Exit(0)
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
		_, installedErr := os.Stat(filepath.Join(os.Getenv("VECNET_TEST_MANAGER_ROOT"), "systemd-installed"))
		if strings.Contains(strings.Join(args, " "), "show") {
			failure, _ := os.ReadFile(filepath.Join(os.Getenv("VECNET_TEST_MANAGER_ROOT"), "systemd-show-failure"))
			switch string(failure) {
			case "error":
				os.Exit(1)
			case "unexpected":
				fmt.Println("unknown-state")
				os.Exit(0)
			case "timeout":
				time.Sleep(10 * time.Second)
				os.Exit(1)
			}
			root := os.Getenv("VECNET_TEST_MANAGER_ROOT")
			var manager map[string]string
			raw, _ := os.ReadFile(filepath.Join(root, "manager-env.json"))
			_ = json.Unmarshal(raw, &manager)
			joined := strings.Join(args, " ")
			if strings.Contains(joined, "--property=ExecStart") {
				if override, err := os.ReadFile(filepath.Join(root, "manager-effective")); err == nil {
					fmt.Print(string(override))
					os.Exit(0)
				}
				id := strings.TrimSuffix(strings.TrimPrefix(args[len(args)-1], "vecnet-delivery@"), ".service")
				fmt.Printf("ExecStart={ path=%s ; argv[]=%s codex deliver --hub-id %s ; ignore_errors=no ; start_time=[n/a] ; stop_time=[n/a] ; pid=0 ; code=(null) ; status=0/0 }\n", manager["VECNET_BIN"], manager["VECNET_BIN"], id)
				fmt.Printf("Environment=HOME=%s XDG_DATA_HOME=%s XDG_RUNTIME_DIR=%s VECNET_BIN=%s VECNET_CODEX_DELIVERY_ENTRY=%s PATH=%s\nUMask=0077\n", manager["HOME"], manager["XDG_DATA_HOME"], manager["XDG_RUNTIME_DIR"], manager["VECNET_BIN"], manager["VECNET_CODEX_DELIVERY_ENTRY"], manager["PATH"])
				os.Exit(0)
			}
			if strings.Contains(joined, "--property=ActiveState") {
				state, err := os.ReadFile(filepath.Join(root, "manager-state"))
				if errors.Is(err, os.ErrNotExist) {
					state = []byte("ActiveState=inactive\nSubState=dead\nJob=\n")
				}
				fmt.Print(string(state))
				os.Exit(0)
			}
			if installedErr == nil {
				fmt.Println("loaded")
			} else {
				fmt.Println("not-found")
			}
			os.Exit(0)
		}
		file, _ := os.OpenFile(filepath.Join(os.Getenv("VECNET_TEST_MANAGER_ROOT"), "systemctl-calls"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
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

		var manager map[string]string
		raw, _ := os.ReadFile(filepath.Join(os.Getenv("VECNET_TEST_MANAGER_ROOT"), "manager-env.json"))
		if json.Unmarshal(raw, &manager) != nil {
			os.Exit(17)
		}
		cmd := exec.Command(manager["VECNET_BIN"], "codex", "deliver", "--hub-id", hubID)
		for key, value := range manager {
			cmd.Env = append(cmd.Env, key+"="+value)
		}
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
		scope, _ := json.Marshal(map[string]string{
			"home": os.Getenv("HOME"), "data": os.Getenv("XDG_DATA_HOME"), "runtime": os.Getenv("XDG_RUNTIME_DIR"),
			"path": os.Getenv("PATH"), "config": os.Getenv("XDG_CONFIG_HOME"), "codex": os.Getenv("CODEX_HOME"),
		})
		_ = os.WriteFile(filepath.Join(state, "runner-scope.json"), scope, 0600)
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
		var finite <-chan time.Time
		if _, err := os.Stat(filepath.Join(os.Getenv("HOME"), "finite-runner")); err == nil {
			finite = time.After(10 * time.Second)
		}
		select {
		case <-stop:
			file, _ = os.OpenFile(filepath.Join(os.Getenv("HOME"), "runner-signals"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
			fmt.Fprintln(file, os.Getpid())
			file.Close()
		case <-finite:
		}
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
	raw, err := os.ReadFile(filepath.Join(f.dir, ".local/share", "vecnet", "codex-delivery", "registry.json"))
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
	paths, _ := filepath.Glob(filepath.Join(f.dir, ".local/share", "vecnet", "codex-delivery", "*", "ledger.sqlite"))
	if len(paths) != 2 {
		t.Fatalf("hub state directories=%v", paths)
	}
	// A second source is admitted atomically without starting a new owner.
	f.config = filepath.Join(f.dir, "other.toml")
	if err := f.ensure("https://hub.invalid"); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(f.dir, ".local/share", "vecnet", "codex-delivery", "registry.json"))
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
			ledgers, _ := filepath.Glob(filepath.Join(f.dir, ".local/share", "vecnet", "codex-delivery", "*", "ledger.sqlite"))
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

func TestCodexDeliveryNodeCompatibility(t *testing.T) {
	for _, tc := range []struct {
		version string
		want    codexInactiveReason
	}{
		{"v20.19.0", "node_too_old"},
		{"v22.17.9", "node_too_old"},
		{"v22.18.0", ""},
		{"v22.20.0", ""},
		{"v23.0.0", "node_too_old"},
		{"v23.5.0", "node_too_old"},
		{"v23.5.9", "node_too_old"},
		{"v23.6.0", ""},
		{"v24.0.0", ""},
		{"v25.0.0", ""},
		{"v23.x.0", "node_unavailable"},
	} {
		t.Run(tc.version, func(t *testing.T) {
			f := newDeliveryFixture(t)
			deliveryWrite(t, filepath.Join(f.dir, "bin", "node"), "#!/bin/sh\nprintf '%s\\n' "+deliveryQuote(tc.version)+"\n", 0700)
			release, err := f.app.codexDeliveryRelease()
			if err != nil {
				t.Fatal(err)
			}
			err = f.app.checkCodexDeliveryNode(context.Background(), release)
			if tc.want == "" {
				if err != nil {
					t.Fatalf("supported Node %s refused: %v", tc.version, err)
				}
				return
			}
			var reason codexInactiveReason
			if !errors.Is(err, errCodexDelivery) || !errors.As(err, &reason) || reason != tc.want {
				t.Fatalf("Node %s: reason=%q error=%v, want typed %q hooks fallback", tc.version, reason, err, tc.want)
			}
		})
	}
}

func TestCodexDeliveryUnsupportedNodePreflight(t *testing.T) {
	for _, version := range []string{"v22.17.9", "v23.0.0", "v23.5.0", "v23.5.9"} {
		t.Run(version, func(t *testing.T) {
			for _, action := range []string{"ensure", "probe"} {
				t.Run(action, func(t *testing.T) {
					f := newDeliveryFixture(t)
					nodeCalls := filepath.Join(f.dir, "unexpected-node-call")
					managerCalls := filepath.Join(f.dir, "unexpected-manager-call")
					// Every fake is synchronous and finite, including on the RED
					// path. The manager refuses before any detached owner can start.
					deliveryWrite(t, filepath.Join(f.dir, "bin", "node"), "#!/bin/sh\nif [ \"$1\" = --version ]; then printf '%s\\n' "+deliveryQuote(version)+"; exit 0; fi\nprintf '%s\\n' \"$@\" > "+deliveryQuote(nodeCalls)+"\nprintf '%s\\n' '{\"loaded\":true,\"previous\":\"gone\"}'\n", 0700)
					deliveryWrite(t, filepath.Join(f.dir, "bin", "systemctl"), "#!/bin/sh\nprintf '%s\\n' \"$@\" > "+deliveryQuote(managerCalls)+"\nexit 1\n", 0700)
					ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
					defer cancel()
					var err error
					if action == "ensure" {
						err = f.app.ensureCodexDelivery(ctx, "https://hub.invalid", f.config, f.bindings)
					} else {
						var probe codexDeliveryProbe
						probe, err = f.app.probeCodexDelivery(ctx, f.env["CODEX_HOME"], bindT1, bindT2)
						if probe.Loaded || probe.Previous != "unknown" {
							t.Errorf("unsupported Node produced an authoritative probe: %+v", probe)
						}
					}
					var reason codexInactiveReason
					if !errors.Is(err, errCodexDelivery) || !errors.As(err, &reason) || reason != "node_too_old" {
						t.Errorf("unsupported Node %s: reason=%q error=%v, want typed node_too_old hooks fallback", version, reason, err)
					}
					for _, path := range []string{nodeCalls, managerCalls} {
						if _, err := os.Stat(path); !os.IsNotExist(err) {
							t.Errorf("unsupported Node reached runtime/manager boundary %s: %v", filepath.Base(path), err)
						}
					}
					if starts := f.starts(); len(starts) != 0 {
						t.Errorf("unsupported Node started a worker: %v", starts)
					}
					if paths, err := filepath.Glob(filepath.Join(f.dir, "run", "vecnet", "delivery-*.json")); err != nil || len(paths) != 0 {
						t.Errorf("unsupported Node published owner metadata: %v %v", paths, err)
					}
				})
			}
		})
	}
}

func TestCodexDeliverySupportedNodeProbe(t *testing.T) {
	for _, version := range []string{"v22.18.0", "v23.6.0", "v24.0.0"} {
		t.Run(version, func(t *testing.T) {
			f := newDeliveryFixture(t)
			// Keep the existing probe fixture's environment checks, replacing
			// only its version response with this supported boundary value.
			node := filepath.Join(f.dir, "bin", "node")
			probeNode := filepath.Join(f.dir, "bin", "probe-node")
			if err := os.Rename(node, probeNode); err != nil {
				t.Fatal(err)
			}
			deliveryWrite(t, node, "#!/bin/sh\nif [ \"$1\" = --version ]; then printf '%s\\n' "+deliveryQuote(version)+"; exit 0; fi\nexec "+deliveryQuote(probeNode)+" \"$@\"\n", 0700)
			f.env["VECNET_TOKEN"] = "fixture-must-not-inherit"
			f.env["CODEX_THREAD_ID"] = bindT1
			probe, err := f.app.probeCodexDelivery(context.Background(), f.env["CODEX_HOME"], bindT1, bindT2)
			if err != nil || !probe.Loaded || probe.Previous != "gone" {
				t.Fatalf("supported Node %s probe=%+v error=%v", version, probe, err)
			}
		})
	}
}

func TestCodexDeliveryManagedUpgrade(t *testing.T) {
	f := newDeliveryFixture(t)
	f.installManaged()
	if err := f.ensure("https://hub.invalid"); err != nil {
		t.Fatal(err)
	}
	deliveryWait(t, func() bool { return len(f.starts()) == 1 })
	next := filepath.Join(f.dir, "new-release.ts")
	deliveryWrite(t, next, "next release", 0600)
	f.env["VECNET_CODEX_DELIVERY_ENTRY"] = next
	f.installManaged()
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

func TestCodexDeliveryNoManager(t *testing.T) {
	for _, lifecycle := range []string{"initial", "upgrade"} {
		t.Run(lifecycle, func(t *testing.T) {
			f := newDeliveryFixture(t)
			var before deliveryOwner
			if lifecycle == "upgrade" {
				if err := f.ensure("https://hub.invalid"); err != nil {
					t.Fatalf("detached control start: %v", err)
				}
				deliveryWait(t, func() bool { return len(f.starts()) == 1 })
				_, meta := f.owner()
				if err := deliveryRead(meta, &before); err != nil || before.Managed || !deliveryOwnerRunning(before) {
					t.Fatalf("expected live detached owner: %+v %v", before, err)
				}
				next := filepath.Join(f.dir, "new-release.ts")
				deliveryWrite(t, next, "next generation", 0600)
				f.env["VECNET_CODEX_DELIVERY_ENTRY"] = next
			}
			// PATH contains only this fixture's bin directory. Moving its fake
			// manager out leaves no executable and cannot reach a host manager.
			if err := os.Rename(filepath.Join(f.dir, "bin", "systemctl"), filepath.Join(f.dir, "systemctl-unavailable")); err != nil {
				t.Fatal(err)
			}
			if err := f.ensure("https://hub.invalid"); err != nil {
				t.Fatalf("detached %s without manager executable: %v", lifecycle, err)
			}
			wantStarts := 1
			if lifecycle == "upgrade" {
				wantStarts = 2
			}
			deliveryWait(t, func() bool { return len(f.starts()) == wantStarts })
			_, meta := f.owner()
			var after deliveryOwner
			if err := deliveryRead(meta, &after); err != nil || after.Managed || after.Launcher != f.env["VECNET_CODEX_DELIVERY_ENTRY"] || !deliveryOwnerRunning(after) {
				t.Fatalf("expected current live detached owner: %+v %v", after, err)
			}
			if lifecycle == "upgrade" && (after.PID == before.PID || deliveryOwnerRunning(before)) {
				t.Fatal("missing-manager upgrade did not drain and replace the detached owner")
			}
		})
	}
}

func TestCodexDeliveryManagedLookupFailure(t *testing.T) {
	for _, failure := range []string{"error", "unexpected", "timeout", "missing"} {
		t.Run(failure, func(t *testing.T) {
			f := newDeliveryFixture(t)
			f.installManaged()
			if err := f.ensure("https://hub.invalid"); err != nil {
				t.Fatal(err)
			}
			deliveryWait(t, func() bool { return len(f.starts()) == 1 })
			_, meta := f.owner()
			var owner deliveryOwner
			if err := deliveryRead(meta, &owner); err != nil || !owner.Managed {
				t.Fatalf("expected managed owner: %+v %v", owner, err)
			}
			next := filepath.Join(f.dir, "new-release.ts")
			deliveryWrite(t, next, "next generation", 0600)
			f.env["VECNET_CODEX_DELIVERY_ENTRY"] = next
			if failure == "missing" {
				if err := os.Rename(filepath.Join(f.dir, "bin", "systemctl"), filepath.Join(f.dir, "systemctl-unavailable")); err != nil {
					t.Fatal(err)
				}
			} else {
				deliveryWrite(t, filepath.Join(f.dir, "systemd-show-failure"), failure, 0600)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
			defer cancel()
			started := time.Now()
			err := f.app.ensureCodexDelivery(ctx, "https://hub.invalid", f.config, f.bindings)
			if err == nil {
				t.Error("unknown service manager must return hooks fallback")
			}
			if elapsed := time.Since(started); elapsed > time.Second {
				t.Errorf("manager failure exceeded caller bound: %s", elapsed)
			}
			// Observe signals processed asynchronously after ensure returns.
			time.Sleep(100 * time.Millisecond)
			if raw, _ := os.ReadFile(filepath.Join(f.dir, "runner-signals")); len(raw) != 0 {
				t.Errorf("manager lookup failure directly signaled the managed runner: %s", raw)
			}
			if !deliveryOwnerRunning(owner) {
				t.Error("manager lookup failure stopped the managed owner")
			}
			var after deliveryOwner
			if deliveryRead(meta, &after) != nil || after != owner || len(f.starts()) != 1 {
				t.Errorf("manager lookup failure started a detached replacement: owner=%+v starts=%v", after, f.starts())
			}
			raw, err := os.ReadFile(filepath.Join(f.dir, "systemctl-calls"))
			if err != nil || strings.Count(string(raw), " start ") != 1 || strings.Contains(string(raw), " restart ") {
				t.Errorf("failed lookup changed the service: %s %v", raw, err)
			}
		})
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
	if paths, _ := filepath.Glob(filepath.Join(f.dir, ".local/share", "vecnet", "codex-delivery", "*")); len(paths) != 0 {
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
	raw, err := os.ReadFile(filepath.Join(f.dir, ".local/share", "vecnet", "codex-delivery", "registry.json"))
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
			f.installManaged()
			if generation == "new-generation" {
				next := filepath.Join(f.dir, "installed-release.ts")
				deliveryWrite(t, next, "installed generation", 0600)
				f.env["VECNET_CODEX_DELIVERY_ENTRY"] = next
				f.installManaged()
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

func TestCodexDeliveryPendingUpgrade(t *testing.T) {
	f := newDeliveryFixture(t)
	stable := f.env["VECNET_BIN"]
	oldBinary := filepath.Join(f.dir, "bin", "vecnet-old")
	if err := os.Rename(stable, oldBinary); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(oldBinary, stable); err != nil {
		t.Fatal(err)
	}
	if err := f.ensure("https://hub.invalid"); err != nil {
		t.Fatal(err)
	}
	deliveryWait(t, func() bool { return len(f.starts()) == 1 })
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	pending := exec.Command(exe, "-test.run=^TestCodexDeliveryProcess$", "--", "delivery-fixture", "delayed-ensure", "https://hub.invalid", f.config, f.bindings)
	pending.Env = append(f.environ(), "GORACE=atexit_sleep_ms=0")
	if err := pending.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- pending.Wait() }()
	t.Cleanup(func() { _ = pending.Process.Kill() })
	deliveryWait(t, func() bool { _, err := os.Stat(filepath.Join(f.dir, "old-ensure-paused")); return err == nil })
	newBinary := filepath.Join(f.dir, "bin", "vecnet-new")
	raw, err := os.ReadFile(oldBinary)
	if err != nil {
		t.Fatal(err)
	}
	deliveryWrite(t, newBinary, string(raw)+"# retained binary generation two\n", 0700)
	nextLink := stable + ".next"
	if err := os.Symlink(newBinary, nextLink); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(nextLink, stable); err != nil {
		t.Fatal(err)
	}
	if out, err := f.command("codex", "delivery-refresh", "--hub", "https://hub.invalid", "--config", f.config, "--bindings", f.bindings).CombinedOutput(); err != nil {
		t.Fatalf("new-generation ensure: %v %s", err, out)
	}
	deliveryWait(t, func() bool { return len(f.starts()) == 2 })
	current, meta := f.owner()
	before, err := os.ReadFile(meta)
	if err != nil {
		t.Fatal(err)
	}
	deliveryWrite(t, filepath.Join(f.dir, "resume-old-ensure"), "resume", 0600)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("pending ensure: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("pending ensure did not finish")
	}
	after, err := os.ReadFile(meta)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) || len(f.starts()) != 2 {
		t.Fatalf("pending old-generation ensure replaced the completed upgrade: starts=%v", f.starts())
	}
	var owner deliveryOwner
	if err := json.Unmarshal(after, &owner); err != nil {
		t.Fatal(err)
	}
	if owner.PID != current || !deliveryOwnerRunning(owner) {
		t.Fatal("completed upgrade lost its live owner")
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

func TestCodexDeliveryUnknownOwnerProvenance(t *testing.T) {
	for _, field := range []string{"state_root", "registry"} {
		for _, value := range []string{"", "/foreign/private-state"} {
			t.Run(field+value, func(t *testing.T) {
				f := newDeliveryFixture(t)
				if err := f.ensure("https://hub.invalid"); err != nil {
					t.Fatal(err)
				}
				pid, meta := f.owner()
				raw, _ := os.ReadFile(meta)
				var owner map[string]any
				if json.Unmarshal(raw, &owner) != nil {
					t.Fatal("invalid fixture metadata")
				}
				owner[field] = value
				raw, _ = json.Marshal(owner)
				deliveryWrite(t, meta, string(raw), 0600)
				if err := f.ensure("https://hub.invalid"); err == nil {
					t.Error("unknown owner provenance must refuse replacement or reuse")
				}
				if procStart(pid) != owner["birth"] {
					t.Error("unknown owner was stopped")
				}
				if raw, _ := os.ReadFile(filepath.Join(f.dir, "runner-signals")); len(raw) != 0 {
					t.Error("unknown owner was signaled")
				}
			})
		}
	}
}

func TestCodexDeliveryCustomScopeNeverReusesForeignLedger(t *testing.T) {
	f := newDeliveryFixture(t)
	if err := f.ensure("https://hub.invalid"); err != nil {
		t.Fatal(err)
	}
	pid, meta := f.owner()
	before, _ := os.ReadFile(meta)
	foreign := filepath.Join(f.dir, "other-data")
	if err := os.Mkdir(foreign, 0700); err != nil {
		t.Fatal(err)
	}
	f.env["XDG_DATA_HOME"] = foreign
	if err := f.ensure("https://hub.invalid"); err == nil {
		t.Error("custom scope reused a foreign ledger owner")
	}
	after, _ := os.ReadFile(meta)
	if string(after) != string(before) || procStart(pid) == "" {
		t.Error("foreign owner changed")
	}
}

func TestCodexDeliveryManagedDefaultScopeReachesRegisteredStateAndLease(t *testing.T) {
	f := newDeliveryFixture(t)
	f.installManaged()
	if err := f.ensure("https://hub.invalid"); err != nil {
		t.Fatal(err)
	}
	p, err := f.app.codexDeliveryPaths("https://hub.invalid")
	if err != nil {
		t.Fatal(err)
	}
	var owner map[string]any
	raw, _ := os.ReadFile(p.Owner)
	if json.Unmarshal(raw, &owner) != nil || owner["managed"] != true || owner["state_root"] != p.Root || owner["registry"] != p.Registry {
		t.Fatalf("managed owner missing registered provenance: %s", raw)
	}
	var registry deliveryRegistry
	if deliveryRead(p.Registry, &registry) != nil || len(registry.Sources) != 1 || registry.Sources[0] != (deliverySource{f.config, f.bindings}) {
		t.Fatal("manager lost private registered sources")
	}
	var m deliveryOwner
	if deliveryRead(p.Owner, &m) != nil {
		t.Fatal("missing owner")
	}
	lease, err := deliveryPrivateFile(p.Lease, false)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	if !deliveryOwnerHoldsLease(p, lease, m) {
		t.Fatal("manager uses a different lease")
	}
	deliveryWait(t, func() bool { _, err := os.Stat(filepath.Join(p.State, "runner-scope.json")); return err == nil })
	var scope map[string]string
	raw, _ = os.ReadFile(filepath.Join(p.State, "runner-scope.json"))
	_ = json.Unmarshal(raw, &scope)
	if scope["home"] != f.dir || scope["data"] != f.env["XDG_DATA_HOME"] || scope["runtime"] != f.env["XDG_RUNTIME_DIR"] || scope["config"] != filepath.Join(f.dir, "manager-config") || scope["codex"] != filepath.Join(f.dir, "manager-codex") || scope["path"] != filepath.Join(f.dir, "bin") {
		t.Fatalf("manager inherited client paths: %v", scope)
	}

}

func TestCodexDeliveryIncompatibleManagedScope(t *testing.T) {
	for _, mismatch := range []string{"home", "data", "runtime", "entry", "binary"} {
		for _, state := range []string{"inactive", "active", "activating", "queued", "unknown"} {
			t.Run(mismatch+"/"+state, func(t *testing.T) {
				f := newDeliveryFixture(t)
				f.installManaged()
				switch mismatch {
				case "home", "data", "runtime":
					path := filepath.Join(f.dir, "custom-"+mismatch)
					if err := os.Mkdir(path, 0700); err != nil {
						t.Fatal(err)
					}
					key := map[string]string{"home": "HOME", "data": "XDG_DATA_HOME", "runtime": "XDG_RUNTIME_DIR"}[mismatch]
					f.env[key] = path
				case "entry":
					f.env["VECNET_CODEX_DELIVERY_ENTRY"] = filepath.Join(f.dir, "custom-entry.ts")
					deliveryWrite(t, f.env["VECNET_CODEX_DELIVERY_ENTRY"], "custom release", 0600)
				case "binary":
					raw, _ := os.ReadFile(f.env["VECNET_BIN"])
					f.env["VECNET_BIN"] = filepath.Join(f.dir, "bin/custom-vecnet")
					deliveryWrite(t, f.env["VECNET_BIN"], string(raw)+"\n# new binary\n", 0700)
				}
				states := map[string]string{"inactive": "ActiveState=inactive\nSubState=dead\nJob=\n", "active": "ActiveState=active\nSubState=running\nJob=\n", "activating": "ActiveState=activating\nSubState=auto-restart\nJob=\n", "queued": "ActiveState=inactive\nSubState=dead\nJob=7\n", "unknown": "ActiveState=unknown\n"}
				deliveryWrite(t, filepath.Join(f.dir, "manager-state"), states[state], 0600)
				ctx, cancel := context.WithTimeout(context.Background(), 600*time.Millisecond)
				defer cancel()
				err := f.app.ensureCodexDelivery(ctx, "https://hub.invalid", f.config, f.bindings)
				if state == "inactive" {
					if err != nil {
						t.Fatalf("known inactive incompatible unit should go detached: %v", err)
					}
					p, _ := f.app.codexDeliveryPaths("https://hub.invalid")
					var owner deliveryOwner
					if deliveryRead(p.Owner, &owner) != nil || owner.Managed {
						t.Error("incompatible scope incorrectly started managed")
					}
					if owner.PID > 1 {
						t.Cleanup(func() {
							if deliveryOwnerRunning(owner) {
								_ = syscall.Kill(owner.PID, syscall.SIGKILL)
							}
						})
					}
				} else if err == nil {
					t.Error("ambiguous or active incompatible unit must refuse")
				}
				if calls, _ := os.ReadFile(filepath.Join(f.dir, "systemctl-calls")); len(calls) != 0 {
					t.Errorf("incompatible scope operated manager: %s", calls)
				}
			})
		}
	}
}

// Unlike the older lifecycle fixtures, these cases retain their roots on any
// failure and observe retirement before removing a successful fixture's root.
func newFiniteDeliveryFixture(t *testing.T) *deliveryFixture {
	t.Helper()
	d, err := os.MkdirTemp("", "vecnet-delivery-not-found-")
	if err != nil {
		t.Fatal(err)
	}
	retired := false
	t.Cleanup(func() {
		if !retired || t.Failed() {
			t.Logf("retained delivery fixture root: %s", d)
			return
		}
		if err := os.RemoveAll(d); err != nil {
			t.Error(err)
		}
	})
	f := newDeliveryFixtureIn(t, d)
	deliveryWrite(t, filepath.Join(d, "finite-runner"), "ten-second maximum lifetime", 0600)
	t.Cleanup(func() {
		var owners []deliveryOwner
		for _, runtime := range []string{"run", "custom-run"} {
			paths, err := filepath.Glob(filepath.Join(d, runtime, "vecnet", "delivery-*.json"))
			if err != nil {
				t.Error(err)
				return
			}
			for _, path := range paths {
				var owner deliveryOwner
				if err := deliveryRead(path, &owner); err != nil || owner.PID <= 1 || owner.Birth == "" {
					t.Errorf("cannot prove fixture ownership at %s: %+v %v", path, owner, err)
					return
				}
				owners = append(owners, owner)
				if deliveryOwnerRunning(owner) {
					if err := syscall.Kill(owner.PID, syscall.SIGKILL); err != nil && err != syscall.ESRCH {
						t.Errorf("retire owned fixture PID %d: %v", owner.PID, err)
						return
					}
				}
			}
		}
		until := time.Now().Add(3 * time.Second)
		for {
			retired = true
			for _, owner := range owners {
				if deliveryOwnerRunning(owner) {
					retired = false
				}
			}
			if retired {
				return
			}
			if time.Now().After(until) {
				t.Error("fixture PID/birth retirement unproven; retaining root")
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	})
	return f
}

func TestCodexDeliveryNotFoundManagedScope(t *testing.T) {
	for _, tc := range []struct {
		name, properties string
		absent           bool
	}{
		{"inactive", "ActiveState=inactive\nSubState=dead\nJob=\n", true},
		{"active", "ActiveState=active\nSubState=running\nJob=\n", false},
		{"activating", "ActiveState=activating\nSubState=auto-restart\nJob=\n", false},
		{"queued", "ActiveState=inactive\nSubState=dead\nJob=7\n", false},
		{"unknown", "ActiveState=unknown\nSubState=unknown\nJob=\n", false},
		{"missing", "", false},
		{"missing-active", "SubState=dead\nJob=\n", false},
		{"missing-substate", "ActiveState=inactive\nJob=\n", false},
		{"missing-job", "ActiveState=inactive\nSubState=dead\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFiniteDeliveryFixture(t)
			f.installManaged()
			var before deliveryOwner
			var managedMeta string
			if !tc.absent {
				if err := f.ensure("https://hub.invalid"); err != nil {
					t.Fatalf("managed control start: %v", err)
				}
				deliveryWait(t, func() bool { return len(f.starts()) == 1 })
				_, managedMeta = f.owner()
				if err := deliveryRead(managedMeta, &before); err != nil || !before.Managed || !deliveryOwnerRunning(before) {
					t.Fatalf("missing live managed control: %+v %v", before, err)
				}
			}
			// Removing the unit definition does not stop its managed consumer.
			if err := os.Remove(filepath.Join(f.dir, "systemd-installed")); err != nil {
				t.Fatal(err)
			}
			deliveryWrite(t, filepath.Join(f.dir, "manager-state"), tc.properties, 0600)
			for key, sub := range map[string]string{"XDG_DATA_HOME": "custom-data", "XDG_RUNTIME_DIR": "custom-run"} {
				path := filepath.Join(f.dir, sub)
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
				f.env[key] = path
			}
			callsBefore, _ := os.ReadFile(filepath.Join(f.dir, "systemctl-calls"))
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			err := f.app.ensureCodexDelivery(ctx, "https://hub.invalid", f.config, f.bindings)
			_, slug, _ := codexDeliveryHub("https://hub.invalid")
			customMeta := filepath.Join(f.env["XDG_RUNTIME_DIR"], "vecnet", "delivery-"+slug+".json")
			customStarts := filepath.Join(f.env["XDG_DATA_HOME"], "vecnet", "codex-delivery", slug, "starts")
			if tc.absent {
				if err != nil {
					t.Fatalf("proven absent unit should permit detached start: %v", err)
				}
				var owner deliveryOwner
				if err := deliveryRead(customMeta, &owner); err != nil || owner.Managed || !deliveryOwnerRunning(owner) {
					t.Fatalf("missing live detached positive control: %+v %v", owner, err)
				}
				deliveryWait(t, func() bool {
					raw, _ := os.ReadFile(customStarts)
					return len(strings.Fields(string(raw))) == 1
				})
			} else {
				var reason codexInactiveReason
				if !errors.Is(err, errCodexDelivery) || !errors.As(err, &reason) || reason != "ownership_ambiguous" {
					t.Errorf("not-found/%s must refuse with ownership_ambiguous: reason=%q error=%v", tc.name, reason, err)
				}
				for _, path := range []string{customMeta, customStarts} {
					if _, err := os.Stat(path); !os.IsNotExist(err) {
						t.Errorf("not-found/%s spawned a detached consumer: %s (%v)", tc.name, path, err)
					}
				}
				var after deliveryOwner
				if err := deliveryRead(managedMeta, &after); err != nil || after != before || !deliveryOwnerRunning(before) {
					t.Errorf("not-found/%s changed the live managed owner: before=%+v after=%+v error=%v", tc.name, before, after, err)
				}
				if signals, err := os.ReadFile(filepath.Join(f.dir, "runner-signals")); !os.IsNotExist(err) {
					t.Errorf("not-found/%s signaled an existing owner: %s (%v)", tc.name, signals, err)
				}
			}
			callsAfter, _ := os.ReadFile(filepath.Join(f.dir, "systemctl-calls"))
			if string(callsAfter) != string(callsBefore) {
				t.Errorf("not-found/%s mutated the manager: before=%q after=%q", tc.name, callsBefore, callsAfter)
			}
		})
	}
}

func TestCodexDeliveryIncompatibleAssetsPreserveLiveManagedOwner(t *testing.T) {
	f := newDeliveryFixture(t)
	f.installManaged()
	if err := f.ensure("https://hub.invalid"); err != nil {
		t.Fatal(err)
	}
	_, meta := f.owner()
	var before deliveryOwner
	if deliveryRead(meta, &before) != nil {
		t.Fatal("missing managed owner")
	}
	f.env["VECNET_CODEX_DELIVERY_ENTRY"] = filepath.Join(f.dir, "foreign-release.ts")
	deliveryWrite(t, f.env["VECNET_CODEX_DELIVERY_ENTRY"], "foreign release", 0600)
	ctx, cancel := context.WithTimeout(context.Background(), 600*time.Millisecond)
	defer cancel()
	if err := f.app.ensureCodexDelivery(ctx, "https://hub.invalid", f.config, f.bindings); err == nil {
		t.Error("incompatible assets must refuse live managed replacement")
	}
	var after deliveryOwner
	if deliveryRead(meta, &after) != nil || after != before || !deliveryOwnerRunning(before) {
		t.Error("incompatible request demoted managed owner")
	}
	if raw, _ := os.ReadFile(filepath.Join(f.dir, "runner-signals")); len(raw) != 0 {
		t.Error("incompatible request signaled managed owner")
	}
}

func TestCodexDeliveryNPMPinsSiblingAssets(t *testing.T) {
	f := newDeliveryFixture(t)
	binary := filepath.Join(f.dir, "package/native/linux-x64/vecnet")
	entry := filepath.Join(f.dir, "package/connectors/launcher/src/codex/delivery-main.ts")
	other := filepath.Join(f.dir, ".local/share/vecnet-launcher/current/launcher/src/codex/delivery-main.ts")
	for _, file := range []string{binary, entry, other} {
		if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
			t.Fatal(err)
		}
	}
	raw, _ := os.ReadFile(f.env["VECNET_BIN"])
	deliveryWrite(t, binary, string(raw), 0700)
	deliveryWrite(t, entry, "npm-pinned entry", 0600)
	deliveryWrite(t, other, "unrelated installed entry", 0600)
	f.env["VECNET_BIN"] = binary
	f.env["VECNET_CODEX_DELIVERY_ENTRY"] = ""
	release, err := f.app.codexDeliveryRelease()
	if err != nil {
		t.Fatal(err)
	}
	if release.Entry != entry {
		t.Fatalf("npm binary selected unrelated HOME assets: %s", release.Entry)
	}
}

func TestCodexDeliveryManagedNodeAlias(t *testing.T) {
	f := newDeliveryFixture(t)
	node := filepath.Join(f.dir, "bin/node")
	realNode := filepath.Join(f.dir, "bin/node-real")
	if err := os.Rename(node, realNode); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realNode, node); err != nil {
		t.Fatal(err)
	}
	f.installManaged()
	manifest := filepath.Join(f.dir, ".local/share/vecnet-launcher/delivery.json")
	var metadata map[string]any
	raw, _ := os.ReadFile(manifest)
	_ = json.Unmarshal(raw, &metadata)
	metadata["node"] = realNode
	raw, _ = json.Marshal(metadata)
	deliveryWrite(t, manifest, string(raw), 0600)
	if err := f.ensure("https://hub.invalid"); err != nil {
		t.Fatal(err)
	}
	_, meta := f.owner()
	var owner deliveryOwner
	if deliveryRead(meta, &owner) != nil || !owner.Managed {
		t.Fatal("same installed Node reached through a symlink should remain managed")
	}
}

// Supplemental checks of effective cached unit properties, written after the
// primary incompatible-scope RED/GREEN; the report records that chronology.
func TestCodexDeliveryEffectiveManagedContract(t *testing.T) {
	for _, mismatch := range []string{"entry", "exec", "umask", "unknown"} {
		t.Run(mismatch, func(t *testing.T) {
			f := newDeliveryFixture(t)
			f.installManaged()
			if err := f.ensure("https://hub.invalid"); err != nil {
				t.Fatal(err)
			}
			_, meta := f.owner()
			var before deliveryOwner
			if deliveryRead(meta, &before) != nil {
				t.Fatal("missing owner")
			}
			_, slug, _ := codexDeliveryHub("https://hub.invalid")
			properties := fmt.Sprintf("ExecStart={ path=%s ; argv[]=%s codex deliver --hub-id %s ; ignore_errors=no ; pid=0 }\nEnvironment=HOME=%s XDG_DATA_HOME=%s XDG_RUNTIME_DIR=%s VECNET_BIN=%s VECNET_CODEX_DELIVERY_ENTRY=%s PATH=%s\nUMask=0077\n", f.env["VECNET_BIN"], f.env["VECNET_BIN"], slug, f.dir, f.env["XDG_DATA_HOME"], f.env["XDG_RUNTIME_DIR"], f.env["VECNET_BIN"], f.entry, f.env["PATH"])
			switch mismatch {
			case "entry":
				properties = strings.Replace(properties, "VECNET_CODEX_DELIVERY_ENTRY="+f.entry, "VECNET_CODEX_DELIVERY_ENTRY=/foreign/entry.ts", 1)
			case "exec":
				properties = strings.Replace(properties, "argv[]="+f.env["VECNET_BIN"], "argv[]=/foreign/vecnet", 1)
			case "umask":
				properties = strings.Replace(properties, "UMask=0077", "UMask=0022", 1)
			case "unknown":
				properties = "unrecognized output\n"
			}
			deliveryWrite(t, filepath.Join(f.dir, "manager-effective"), properties, 0600)
			deliveryWrite(t, filepath.Join(f.dir, "manager-state"), "ActiveState=active\nSubState=running\nJob=\n", 0600)
			if err := f.ensure("https://hub.invalid"); err == nil {
				t.Error("cached mismatched unit must refuse even a same-generation owner")
			}
			var after deliveryOwner
			if deliveryRead(meta, &after) != nil || before != after || !deliveryOwnerRunning(before) {
				t.Error("mismatched cached unit changed the owner")
			}
		})
	}
}
