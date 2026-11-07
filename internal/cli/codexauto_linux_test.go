package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// An actual detached CLI child, with a private environment and fake read-only
// Node boundary. It never contacts a host manager or a Codex server.
func TestCodexAutoProcess(t *testing.T) {
	for i, arg := range os.Args {
		if arg == "auto-fixture" {
			if os.Getenv("CODEX_THREAD_ID") != "" || os.Getenv("VECNET_CODEX_AUTO_DELIVERY") != "0" {
				os.Exit(98)
			}
			var diagnostics bytes.Buffer
			code := Run(os.Args[i+1:], strings.NewReader(""), io.Discard, &diagnostics, os.Getenv)
			_ = os.WriteFile(filepath.Join(os.Getenv("HOME"), "auto-done"), []byte(diagnostics.String()), 0600)
			os.Exit(code)
		}
	}
}
func autoFixture(t *testing.T) *bindFixture {
	f := newBindFixture(t)
	f.now = time.Now().UTC()

	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/peers/me":
			_ = json.NewEncoder(w).Encode(map[string]string{"kind": "peer", "peer_id": f.peer.Load().(string)})
		case "/v1/peers":
			_, _ = io.WriteString(w, `{"peers":[]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.server.Close)
	f.hub = f.server.URL
	f.config(f.hub)
	for _, sub := range []string{"bin", "runtime", "data"} {
		if err := os.Mkdir(filepath.Join(f.env["HOME"], sub), 0700); err != nil {
			t.Fatal(err)
		}
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	f.env["PATH"] = filepath.Join(f.env["HOME"], "bin")
	f.env["VECNET_BIN"] = filepath.Join(f.env["PATH"], "vecnet")
	f.env["XDG_RUNTIME_DIR"] = filepath.Join(f.env["HOME"], "runtime")
	f.env["XDG_DATA_HOME"] = filepath.Join(f.env["HOME"], "data")
	f.env["CODEX_THREAD_ID"] = bindT1
	f.env["VECNET_CODEX_DELIVERY_ENTRY"] = filepath.Join(f.env["HOME"], "delivery.ts")
	deliveryWrite(t, f.env["VECNET_CODEX_DELIVERY_ENTRY"], "fixture", 0600)
	deliveryWrite(t, f.env["VECNET_BIN"], "#!/bin/sh\n: > \"$HOME/auto-started\"\nexport GORACE=atexit_sleep_ms=0\nexec "+deliveryQuote(exe)+" -test.run=^TestCodexAutoProcess$ -- auto-fixture \"$@\"\n", 0700)
	deliveryWrite(t, filepath.Join(f.env["PATH"], "node"), `#!/bin/sh
if [ "$1" = --version ]; then echo v22.20.0; exit 0; fi
printf probe > "$HOME/probe-started"
while [ -f "$HOME/probe-hold" ] && [ ! -f "$HOME/probe-release" ]; do /bin/sleep 0.01; done
if [ -f "$HOME/probe-result" ]; then /bin/cat "$HOME/probe-result"; else echo '{"loaded":true,"previous":"live"}'; fi
`, 0700)
	// A missing systemctl/runner is intentional: registration must still be
	// durable and preserve the invoking command's result under ensure fallback.
	return f
}
func autoRun(f *bindFixture, input string, args ...string) (int, string, string) {
	var out, errs bytes.Buffer
	code := RunContext(context.Background(), args, strings.NewReader(input), &out, &errs, func(k string) string { return f.env[k] })
	return code, out.String(), errs.String()
}
func autoDone(f *bindFixture) {
	f.t.Helper()
	until := time.Now().Add(15 * time.Second)
	for time.Now().Before(until) {
		if _, e := os.Stat(filepath.Join(f.env["HOME"], "auto-done")); e == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	f.t.Fatal("auto child did not finish")
}
func autoFile(f *bindFixture) codexBindingsFile {
	f.t.Helper()
	var file codexBindingsFile
	if err := json.Unmarshal(f.raw(), &file); err != nil {
		f.t.Fatal(err)
	}
	return file
}
func TestCodexAutoNextAction(t *testing.T) {
	f := autoFixture(t)
	f.env["VECNET_CODEX_AUTO_DELIVERY"] = "0"
	wantCode, wantOut, wantErr := autoRun(f, "", "peers")
	delete(f.env, "VECNET_CODEX_AUTO_DELIVERY")
	code, out, errs := autoRun(f, "", "peers")
	if code != 0 || code != wantCode || out != wantOut || errs != wantErr {
		t.Fatal("automation changed ordinary command result")
	}
	autoDone(f)
	rows := autoFile(f).Bindings
	if len(rows) != 1 || rows[0].ThreadID != bindT1 || rows[0].Hub != f.hub || rows[0].Caller != "peer_A" {
		t.Fatalf("registration=%+v", rows)
	}
}

func autoRegister(f *bindFixture, thread string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 750*time.Millisecond)
	defer cancel()
	a := &app{ctx: ctx, stdout: io.Discard, stderr: io.Discard, getenv: func(k string) string { return f.env[k] }}
	return a.registerCodexAuto(thread, "")
}
func TestCodexAutoRefreshPins(t *testing.T) {
	f := autoFixture(t)
	f.bind("p", bindT1, "--ttl", "2h", "--idle-dormant", "6h", "--attended")
	before := autoFile(f).Bindings[0]
	_ = autoRegister(f, bindT1)
	after := autoFile(f).Bindings[0]
	if !after.ExpiresAt.After(before.ExpiresAt) {
		t.Fatal("same-thread action did not refresh TTL")
	}
	after.ExpiresAt = before.ExpiresAt
	if after != before {
		t.Fatal("refresh changed created_at, identity pins or attendance")
	}
	beforeRaw := f.raw()
	f.peer.Store("peer_B")
	_ = autoRegister(f, bindT1)
	if !bytes.Equal(beforeRaw, f.raw()) {
		t.Fatal("automatic caller replacement")
	}
	f.config("https://changed.invalid")
	_ = autoRegister(f, bindT1)
	if !bytes.Equal(beforeRaw, f.raw()) {
		t.Fatal("automatic hub replacement")
	}
}
func TestCodexAutoDoesNotSteal(t *testing.T) {
	for _, status := range []string{"live", "unknown"} {
		t.Run(status, func(t *testing.T) {
			f := autoFixture(t)
			f.bind("p", bindT1)
			deliveryWrite(t, filepath.Join(f.env["HOME"], "probe-result"), `{"loaded":true,"previous":"`+status+`"}`, 0600)
			before := f.raw()
			_ = autoRegister(f, bindT2)
			if !bytes.Equal(before, f.raw()) {
				t.Fatal("replaced a live or unknown thread")
			}
		})
	}
	t.Run("probe racing explicit takeover", func(t *testing.T) {
		f := autoFixture(t)
		f.bind("p", bindT1)
		deliveryWrite(t, filepath.Join(f.env["HOME"], "probe-result"), `{"loaded":true,"previous":"gone"}`, 0600)
		deliveryWrite(t, filepath.Join(f.env["HOME"], "probe-hold"), "", 0600)
		done := make(chan error, 1)
		go func() { done <- autoRegister(f, bindT2) }()
		deliveryWait(t, func() bool { _, err := os.Stat(filepath.Join(f.env["HOME"], "probe-started")); return err == nil })
		f.bind("p", bindID(3), "--takeover")
		before := f.raw()
		deliveryWrite(t, filepath.Join(f.env["HOME"], "probe-release"), "", 0600)
		<-done
		if !bytes.Equal(before, f.raw()) {
			t.Fatal("stale gone probe overwrote a new binding")
		}
	})
}

func TestCodexAutoIdentityDuringProbe(t *testing.T) {
	f := autoFixture(t)
	f.bind("p", bindT1)
	deliveryWrite(t, filepath.Join(f.env["HOME"], "probe-result"), `{"loaded":true,"previous":"gone"}`, 0600)
	deliveryWrite(t, filepath.Join(f.env["HOME"], "probe-hold"), "", 0600)
	done := make(chan error, 1)
	go func() { done <- autoRegister(f, bindT2) }()
	deliveryWait(t, func() bool { _, err := os.Stat(filepath.Join(f.env["HOME"], "probe-started")); return err == nil })
	before := f.raw()
	f.config(autoHub(t))
	deliveryWrite(t, filepath.Join(f.env["HOME"], "probe-release"), "", 0600)
	<-done
	if !bytes.Equal(before, f.raw()) {
		t.Fatal("profile changed during probe but stale identity was committed")
	}
}

func TestCodexAutoRebindGone(t *testing.T) {
	for _, expired := range []bool{false, true} {
		t.Run(fmt.Sprint(expired), func(t *testing.T) {
			f := autoFixture(t)
			if expired {
				f.now = time.Now().Add(-25 * time.Hour)
			}
			f.bind("p", bindT1)
			if !expired {
				deliveryWrite(t, filepath.Join(f.env["HOME"], "probe-result"), `{"loaded":true,"previous":"gone"}`, 0600)
			}
			_ = autoRegister(f, bindT2)
			if got := autoFile(f).Bindings[0].ThreadID; got != bindT2 {
				t.Fatalf("gone/expired replacement=%s", got)
			}
		})
	}
}
func TestCodexAutoOptOutPending(t *testing.T) {
	f := autoFixture(t)
	deliveryWrite(t, filepath.Join(f.env["HOME"], "probe-hold"), "", 0600)
	done := make(chan error, 1)
	go func() { done <- autoRegister(f, bindT1) }()
	deliveryWait(t, func() bool { _, err := os.Stat(filepath.Join(f.env["HOME"], "probe-started")); return err == nil })
	f.ok("unbind")
	before := f.raw()
	deliveryWrite(t, filepath.Join(f.env["HOME"], "probe-release"), "", 0600)
	<-done
	if !bytes.Equal(before, f.raw()) || !autoFile(f).disabled("p", "") {
		t.Fatal("pending registration undid explicit opt-out")
	}
}
func TestCodexAutoTwoHubs(t *testing.T) {
	f := autoFixture(t)
	f.bind("p", bindT1)
	f.config(autoHub(t))
	f.bind("q", bindT1)
	if len(autoFile(f).Bindings) != 2 {
		t.Fatal("thread cannot bind independently across hubs")
	}
}
func TestCodexAutoLimit(t *testing.T) {
	t.Run("missing authoritative data root", func(t *testing.T) {
		f := autoFixture(t)
		f.env["VECNET_CODEX_MAX_BINDINGS"] = "1"
		f.bind("p", bindT1)
		root := f.env["HOME"]
		f.env["VECNET_CONFIG"] = filepath.Join(root, "other", "v.toml")
		f.path = filepath.Join(root, "other", "codex", "bindings.json")
		f.config(autoHub(t))
		// Both sources exist in the shared registry before the omission.
		f.env["VECNET_CODEX_MAX_BINDINGS"] = "2"
		f.bind("q", bindT2)
		f.ok("--net", "q", "unbind")
		f.env["VECNET_CODEX_MAX_BINDINGS"] = "1"
		before := f.raw()
		delete(f.env, "HOME")
		delete(f.env, "XDG_DATA_HOME")
		code, _, _ := f.run("--net", "q", "bind", "--thread", bindT2)
		if code != 1 || !bytes.Equal(before, f.raw()) {
			t.Fatal("unknown shared capacity admitted a binding or cleared opt-out")
		}
		f.env["VECNET_PROFILE"] = "q"
		_ = autoRegister(f, bindT2)
		if !bytes.Equal(before, f.raw()) {
			t.Fatal("automatic unknown-capacity admission mutated opt-out")
		}
		f.ok("bindings", "--json")
		f.ok("--net", "q", "unbind")
	})
	for _, combined := range []bool{false, true} {
		t.Run(fmt.Sprint(combined), func(t *testing.T) {
			f := autoFixture(t)
			f.env["VECNET_CODEX_MAX_BINDINGS"] = "1"
			f.bind("p", bindT1)
			if combined {
				// Register the first file, then use a second config and hub with the same
				// canonical Codex home. The cap applies before filtering either hub.
				a := &app{ctx: context.Background(), getenv: func(k string) string { return f.env[k] }}
				p, err := a.codexDeliveryPaths(f.hub)
				if err != nil {
					t.Fatal(err)
				}
				config := f.env["VECNET_CONFIG"]
				if err = deliveryRegister(a.ctx, p, config, f.path); err != nil {
					t.Fatal(err)
				}
				f.env["VECNET_CONFIG"] = filepath.Join(f.env["HOME"], "other", "v.toml")
				f.path = filepath.Join(f.env["HOME"], "other", "codex", "bindings.json")
				f.config(autoHub(t))
			}
			code, _, errs := f.run("--net", "q", "bind", "--thread", bindT2)
			if code != 1 || !strings.Contains(errs, "binding limit 1") {
				t.Fatalf("combined=%v: cap exit=%d stderr=%s", combined, code, errs)
			}
			f.env["VECNET_PROFILE"] = "q"
			_ = autoRegister(f, bindT2)
			if file, err := loadCodexBindings(f.path); err != nil || codexBindingFor(file, "q", "") != nil {
				t.Fatal("automatic registration bypassed combined cap")
			}

		})
	}
}

func TestCodexAutoLimitReactivation(t *testing.T) {
	for _, combined := range []bool{false, true} {
		for _, manual := range []bool{false, true} {
			for _, occupied := range []bool{false, true} {
				t.Run(fmt.Sprintf("combined=%v/manual=%v/occupied=%v", combined, manual, occupied), func(t *testing.T) {
					f := autoFixture(t)
					f.env["VECNET_CODEX_MAX_BINDINGS"] = "1"
					f.now = time.Now().Add(-25 * time.Hour).UTC()
					f.bind("p", bindT1, "--attended", "--idle-dormant", "6h")
					beforeRow := autoFile(f).Bindings[0]
					originalConfig, originalPath, originalHub := f.env["VECNET_CONFIG"], f.path, f.hub
					if combined {
						f.env["VECNET_CONFIG"] = filepath.Join(f.env["HOME"], "other", "v.toml")
						f.path = filepath.Join(f.env["HOME"], "other", "codex", "bindings.json")
						f.config(autoHub(t))
					}
					f.now = time.Now().UTC()
					if occupied {
						f.bind("q", bindT2)
					}
					f.env["VECNET_CONFIG"], f.path, f.hub = originalConfig, originalPath, originalHub
					before := f.raw()
					if manual {
						// Equality is expired, too: a renewal at the expiry boundary admits anew.
						f.now = beforeRow.ExpiresAt
						code, _, errs := f.run("--net", "p", "bind", "--thread", bindT1, "--renew")
						if occupied && (code != 1 || !strings.Contains(errs, "binding limit 1")) {
							t.Fatalf("expired manual renewal bypassed cap: exit=%d stderr=%s", code, errs)
						}
						if !occupied && code != 0 {
							t.Fatalf("free-slot renewal refused: %s", errs)
						}
					} else {
						err := autoRegister(f, bindT1)
						if occupied && err == nil {
							t.Fatal("expired auto refresh bypassed cap")
						}
					}
					if occupied {
						if !bytes.Equal(before, f.raw()) {
							t.Fatal("refused reactivation changed bindings")
						}
					} else {
						after := *codexBindingFor(autoFile(f), "p", "")
						if !after.ExpiresAt.After(beforeRow.ExpiresAt) {
							t.Fatal("free-slot reactivation did not extend expiry")
						}
						after.ExpiresAt = beforeRow.ExpiresAt
						if after != beforeRow {
							t.Fatalf("reactivation changed renewal fields: before=%+v after=%+v", beforeRow, after)
						}
					}
				})
			}
		}
	}
}

func autoHub(t *testing.T) string {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/peers/me" {
			_, _ = io.WriteString(w, `{"kind":"peer","peer_id":"peer_A"}`)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(s.Close)
	return s.URL
}

func TestCodexAutoHookProfile(t *testing.T) {
	t.Run("effective profile and hook trust", func(t *testing.T) {
		f := autoFixture(t)
		delete(f.env, "VECNET_PROFILE")
		code, _, _ := autoRun(f, `{"hook_event_name":"PostToolUse"}`, "--net", "p", "hook", "--profile", "q")
		if code != 0 {
			t.Fatal("hook exit changed")
		}
		autoDone(f)
		rows := autoFile(f).Bindings
		if len(rows) != 1 || rows[0].Profile != "q" {
			t.Fatalf("effective profile binding=%+v", rows)
		}
		a := &app{getenv: func(k string) string { return f.env[k] }}
		pinPath, err := pinsPath(a.getenv)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = os.Stat(pinPath); !os.IsNotExist(err) {
			t.Fatal("hook auto-registration touched trust state")
		}
	})
	for _, tc := range []struct {
		name, input string
		args        []string
	}{
		{"profile mismatch", `{}`, []string{"hook", "--profile", "q", "--event", "post-tool"}},
		{"global profile mismatch", `{}`, []string{"--net", "q", "hook", "--event", "post-tool"}},
		{"subagent", `{"agent_id":"foreign","hook_event_name":"PostToolUse"}`, []string{"hook"}},
		{"malformed", "{", []string{"hook", "--event", "post-tool"}},
		{"unrecognized event", `{"hook_event_name":"Unknown"}`, []string{"hook"}},
		{"invalid explicit event", `{}`, []string{"hook", "--event", "unknown"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := autoFixture(t)
			code, out, _ := autoRun(f, tc.input, tc.args...)
			if code != 0 || out != "" {
				t.Fatal("ignored hook changed output or exit")
			}
			if _, err := os.Stat(filepath.Join(f.env["HOME"], "auto-started")); !os.IsNotExist(err) {
				t.Fatal("ignored hook started a helper")
			}
			if _, err := os.Stat(f.path); !os.IsNotExist(err) {
				t.Fatal("ignored hook registered")
			}
		})
	}
}

func TestCodexAutoFallback(t *testing.T) {
	f := autoFixture(t)
	deliveryWrite(t, filepath.Join(f.env["HOME"], "probe-result"), `{"loaded":false,"previous":"unknown"}`, 0600)
	_ = autoRegister(f, bindT1)
	if _, err := os.Stat(f.path); !os.IsNotExist(err) {
		t.Fatal("unavailable socket registered a thread")
	}
	f.env["VECNET_CODEX_AUTO_DELIVERY"] = "0"
	code, out, errs := autoRun(f, "", "peers")
	if code != 0 || out != "" || errs != "" {
		t.Fatal("ordinary action failed under fallback")
	}
}
func TestCodexAutoBoundedCost(t *testing.T) {
	for _, problem := range []string{"hung probe", "missing node", "missing assets"} {
		t.Run(problem, func(t *testing.T) {
			f := autoFixture(t)
			switch problem {
			case "hung probe":
				deliveryWrite(t, filepath.Join(f.env["PATH"], "node"), "#!/bin/sh\nif [ \"$1\" = --version ]; then echo v22.20.0; exit; fi\nexec /bin/sleep 30\n", 0700)
			case "missing node":
				if err := os.Remove(filepath.Join(f.env["PATH"], "node")); err != nil {
					t.Fatal(err)
				}
			case "missing assets":
				if err := os.Remove(f.env["VECNET_CODEX_DELIVERY_ENTRY"]); err != nil {
					t.Fatal(err)
				}
			}
			f.env["VECNET_CODEX_AUTO_DELIVERY"] = "0"
			wantCode, wantOut, wantErr := autoRun(f, "", "peers")
			delete(f.env, "VECNET_CODEX_AUTO_DELIVERY")
			start := time.Now()
			code, out, errs := autoRun(f, "", "peers")
			elapsed := time.Since(start)
			if elapsed > 200*time.Millisecond {
				t.Fatalf("automation took %s", elapsed)
			}
			if code != wantCode || out != wantOut || errs != wantErr {
				t.Fatal("failure path changed ordinary semantics")
			}
			autoDone(f)
			if _, err := os.Stat(f.path); !os.IsNotExist(err) {
				t.Fatal("failure path registered a thread")
			}
		})
	}
}

type codexAutoReader func([]byte) (int, error)

func (r codexAutoReader) Read(p []byte) (int, error) { return r(p) }
func TestCodexAutoHookInput(t *testing.T) {
	for _, problem := range []string{"unreadable", "oversize", "read once"} {
		t.Run(problem, func(t *testing.T) {
			f := autoFixture(t)
			var input io.Reader
			reads := 0
			switch problem {
			case "unreadable":
				input = codexAutoReader(func([]byte) (int, error) { reads++; return 0, errors.New("fixture read failure") })
			case "oversize":
				input = strings.NewReader(strings.Repeat(" ", (1<<20)+1))
			case "read once":
				input = codexAutoReader(func(p []byte) (int, error) {
					reads++
					if reads > 1 {
						return 0, errors.New("stdin reread")
					}
					return copy(p, `{"hook_event_name":"PostToolUse"}`), io.EOF
				})
			}
			var out, errs bytes.Buffer
			code := RunContext(context.Background(), []string{"hook", "--event", "post-tool"}, input, &out, &errs, func(k string) string { return f.env[k] })
			if code != 0 {
				t.Fatal("hook exit changed")
			}
			if problem == "read once" {
				autoDone(f)
				if reads != 1 || len(autoFile(f).Bindings) != 1 {
					t.Fatal("hook did not use the same validated input")
				}
			} else {
				if out.Len() != 0 {
					t.Fatal("ignored hook emitted output")
				}
				if _, err := os.Stat(filepath.Join(f.env["HOME"], "auto-started")); !os.IsNotExist(err) {
					t.Fatal("refused stdin launched registration")
				}
			}
		})
	}
}

// Replace a validated config while the real helper is paused at the external
// probe boundary. Context cancellation cannot interrupt the ensuing FIFO open.
func TestCodexAutoBlockedConfigDeadline(t *testing.T) {
	f := autoFixture(t)
	root := f.env["HOME"]
	config := f.env["VECNET_CONFIG"]
	original, err := os.ReadFile(config)
	if err != nil {
		t.Fatal(err)
	}
	deliveryWrite(t, filepath.Join(root, "probe-hold"), "", 0600)
	a := &app{ctx: context.Background(), getenv: func(k string) string { return f.env[k] }}
	paths, err := a.codexDeliveryPaths(f.hub)
	if err != nil {
		t.Fatal(err)
	}
	leasePath := filepath.Join(filepath.Dir(paths.Lease), "auto-blocked-config.lock")
	lease, err := deliveryPrivateFile(leasePath, true)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	if locked, err := deliveryLock(lease); err != nil || !locked {
		t.Fatalf("debounce lease: %v %v", locked, err)
	}
	cmd := exec.Command(f.env["VECNET_BIN"], "--net", "p", "codex", "auto-register", "--thread", bindT1, "--codex-home", f.home)
	for k, v := range f.env {
		if k != "CODEX_THREAD_ID" {
			cmd.Env = append(cmd.Env, k+"="+v)
		}
	}
	cmd.Env = append(cmd.Env, "VECNET_CODEX_AUTO_DELIVERY=0", "GORACE=atexit_sleep_ms=0")
	cmd.ExtraFiles = []*os.File{lease}
	var diagnostics bytes.Buffer
	cmd.Stdout, cmd.Stderr = &diagnostics, &diagnostics
	started := time.Now()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	lease.Close() // only the actual helper retains the inherited ownership
	done := make(chan error, 1)
	exited := make(chan struct{})
	go func() { done <- cmd.Wait(); close(exited) }()
	defer func() { _ = cmd.Process.Kill(); <-exited }()
	deliveryWait(t, func() bool { _, err := os.Stat(filepath.Join(root, "probe-started")); return err == nil })
	saved := config + ".saved"
	if err := os.Rename(config, saved); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(config, 0600); err != nil {
		t.Fatal(err)
	}
	deliveryWrite(t, filepath.Join(root, "probe-release"), "", 0600)
	lockPaths := []string{filepath.Join(paths.Root, "registry.lock"), f.path + ".lock", leasePath}
	locked := func(path string) bool {
		file, err := os.OpenFile(path, os.O_RDWR, 0)
		if err != nil {
			return false
		}
		defer file.Close()
		ok, err := deliveryLock(file)
		return err == nil && !ok
	}
	deliveryWait(t, func() bool { return locked(lockPaths[0]) && locked(lockPaths[1]) && locked(lockPaths[2]) })
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("blocked helper reported success")
		}
	case <-time.After(17 * time.Second):
		t.Fatal("blocked helper survived its 15-second lifetime with registry/binding/debounce locks")
	}
	if elapsed := time.Since(started); elapsed > 17*time.Second {
		t.Fatalf("deadline exit took %s", elapsed)
	}
	t.Logf("blocked helper exited after %s", time.Since(started))
	for _, path := range lockPaths {
		file, err := os.OpenFile(path, os.O_RDWR, 0)
		if err != nil {
			t.Fatal(err)
		}
		ok, err := deliveryLock(file)
		file.Close()
		if err != nil || !ok {
			t.Fatalf("helper retained lock %s: %v", filepath.Base(path), err)
		}
	}
	// Opening the old FIFO cannot revive a late writer after process death.
	fifo, err := os.OpenFile(config, os.O_RDWR|syscall.O_NONBLOCK, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fifo.Write(original); err != nil {
		t.Fatal(err)
	}
	fifo.Close()
	if err := os.Rename(saved, config); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(f.path); !os.IsNotExist(err) {
		t.Fatal("timed-out helper persisted a binding")
	}
	f.bind("q", bindT2)
	before := f.raw()
	time.Sleep(100 * time.Millisecond)
	if !bytes.Equal(before, f.raw()) {
		t.Fatal("late helper mutation overwrote replacement writer")
	}
}
