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
	"path/filepath"
	"strings"
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
