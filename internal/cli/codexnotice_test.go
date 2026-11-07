//go:build linux

package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

const noticeHub = "https://hub.invalid"

func TestCodexStatusEscapedHub(t *testing.T) {
	for _, path := range []string{"/base%0Aextra", "/base%0Dextra", "/%1B%5B31mextra", "/ordinary%20path", "/" + strings.Repeat("long", 200)} {
		t.Run(path[:min(len(path), 32)], func(t *testing.T) {
			f := newDeliveryFixture(t)
			noticeConfig(t, f)
			hub := noticeHub + path
			if err := saveConfig(f.config, Config{Profiles: map[string]Profile{"p": {Hub: hub, Secret: "vn_peer_private-error-text"}}}); err != nil {
				t.Fatal(err)
			}
			code, out, errs := noticeRun(f, "codex", "status")
			if code != 0 || errs != "" {
				t.Fatalf("public status exit=%d stderr=%q", code, errs)
			}
			check := func(label, output string, lines int) {
				t.Helper()
				if strings.Count(output, "\n") != lines || strings.ContainsAny(output, "\r\x1b\t") || strings.Contains(output, "vn_peer") || strings.Contains(output, "private-error-text") || len(output) > 512 {
					t.Errorf("%s unsafe/unbounded output=%q", label, output)
				}
				if path == "/ordinary%20path" && !strings.Contains(output, "ordinary path") {
					t.Errorf("%s lost ordinary decoded path: %q", label, output)
				}
			}
			check("status", out, 2)
			noticeSeed(t, f, hub, "node_missing")
			message := f.app.codexNoticeMessage(hub, time.Now())
			if message == "" {
				t.Fatal("notice fixture did not emit")
			}
			check("notice", message, 1)
		})
	}
}

func TestCodexNoticeReasonWriterContention(t *testing.T) {
	for _, activation := range []bool{false, true} {
		name := "refusal"
		if activation {
			name = "verified-activation"
		}
		t.Run(name, func(t *testing.T) {
			f := newDeliveryFixture(t)
			if activation {
				if err := f.ensure(noticeHub); err != nil {
					t.Fatal(err)
				}
			} else if err := os.Remove(filepath.Join(f.dir, "bin/node")); err != nil {
				t.Fatal(err)
			}
			noticeSeed(t, f, noticeHub, "manager_refused")
			if f.app.codexNoticeMessage(noticeHub, time.Now()) == "" {
				t.Fatal("missing initial reason notice")
			}
			lock, err := deliveryPrivateFile(filepath.Join(filepath.Dir(noticePath(f, noticeHub)), "state.lock"), true)
			if err != nil {
				t.Fatal(err)
			}
			defer lock.Close()
			if locked, err := deliveryLock(lock); err != nil || !locked {
				t.Fatal("cannot hold marker lock")
			}
			done := make(chan error, 1)
			go func() {
				done <- f.app.ensureCodexDelivery(context.Background(), noticeHub, f.config, f.bindings+"-other-registration")
			}()
			var result error
			completed := false
			select {
			case result = <-done:
				completed = true
			case <-time.After(300 * time.Millisecond):
			}
			if err := unix.Flock(int(lock.Fd()), unix.LOCK_UN); err != nil {
				t.Fatal(err)
			}
			if !completed {
				select {
				case result = <-done:
				case <-time.After(2 * time.Second):
					t.Fatal("reason writer exceeded bounded lifetime")
				}
			}
			if (result == nil) != activation {
				t.Fatalf("ensure activation=%v error=%v", activation, result)
			}
			want := "node_missing"
			if activation {
				want = ""
			}
			noticeReason(t, f, want)
			if !activation && f.app.codexNoticeMessage(noticeHub, time.Now()) == "" {
				t.Fatal("changed reason did not emit")
			}
			if message := f.app.codexNoticeMessage(noticeHub, time.Now()); message != "" {
				t.Fatalf("repeated or stale notice=%q", message)
			}
			if err := f.ensure(noticeHub); (err == nil) != activation {
				t.Fatalf("repeat ensure=%v", err)
			}
			if message := f.app.codexNoticeMessage(noticeHub, time.Now()); message != "" {
				t.Fatalf("reason history was lost: %q", message)
			}
		})
	}
}

func TestCodexNoticeOwnershipRefusalSites(t *testing.T) {
	t.Run("live-older-owner-without-lease-proof", func(t *testing.T) {
		f := newDeliveryFixture(t)
		if err := f.ensure(noticeHub); err != nil {
			t.Fatal(err)
		}
		pid, meta := f.owner()
		raw, err := os.ReadFile(meta)
		if err != nil {
			t.Fatal(err)
		}
		defer deliveryWrite(t, meta, string(raw), 0600)
		// This live process has valid birth evidence but no delivery lease FD.
		child := exec.Command("/bin/sleep", "10")
		child.Env = f.environ()
		if err := child.Start(); err != nil {
			t.Fatal(err)
		}
		defer func() { _ = child.Process.Kill(); _ = child.Wait() }()
		var owner deliveryOwner
		if err := json.Unmarshal(raw, &owner); err != nil {
			t.Fatal(err)
		}
		owner.PID, owner.Birth = child.Process.Pid, procStart(child.Process.Pid)
		changed, _ := json.Marshal(owner)
		deliveryWrite(t, meta, string(changed), 0600)
		next := filepath.Join(f.dir, "next-release.ts")
		deliveryWrite(t, next, "new generation", 0600)
		f.env["VECNET_CODEX_DELIVERY_ENTRY"] = next
		noticeSeed(t, f, noticeHub, "assets_missing")
		if err := f.ensure(noticeHub); err == nil {
			t.Fatal("unprovable live owner accepted")
		}
		if !deliveryOwnerRunning(owner) || procStart(pid) == "" {
			t.Fatal("owner or unproven process was signaled")
		}
		if signals, _ := os.ReadFile(filepath.Join(f.dir, "runner-signals")); len(signals) != 0 {
			t.Fatal("existing delivery owner was signaled")
		}
		noticeReason(t, f, "ownership_ambiguous")
	})
	t.Run("child-never-proves-ownership", func(t *testing.T) {
		f := newDeliveryFixture(t)
		deliveryWrite(t, f.env["VECNET_BIN"], "#!/bin/sh\nexit 0\n", 0700)
		noticeSeed(t, f, noticeHub, "assets_missing")
		ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
		defer cancel()
		if err := f.app.ensureCodexDelivery(ctx, noticeHub, f.config, f.bindings); err == nil {
			t.Fatal("unverified child accepted")
		}
		noticeReason(t, f, "ownership_ambiguous")
		if len(f.starts()) != 0 {
			t.Fatal("unexpected delivery activation")
		}
	})
}

func TestCodexNoticeReasonWriterDeadline(t *testing.T) {
	f := newDeliveryFixture(t)
	if err := os.Remove(filepath.Join(f.dir, "bin/node")); err != nil {
		t.Fatal(err)
	}
	noticeSeed(t, f, noticeHub, "manager_refused")
	lock, err := deliveryPrivateFile(filepath.Join(filepath.Dir(noticePath(f, noticeHub)), "state.lock"), true)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if locked, err := deliveryLock(lock); err != nil || !locked {
		t.Fatal("cannot hold marker lock")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()
	start := time.Now()
	if err := f.app.ensureCodexDelivery(ctx, noticeHub, f.config, f.bindings); err == nil {
		t.Fatal("missing node must refuse")
	}
	if time.Since(start) > 250*time.Millisecond {
		t.Fatal("reason serialization ignored caller deadline")
	}
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_UN); err != nil {
		t.Fatal(err)
	}
	noticeReason(t, f, "manager_refused")
}

func TestCodexNoticeNodeProbeFailureClassification(t *testing.T) {
	for _, shape := range []string{"supported", "old", "sleep", "execution-failure", "malformed", "malformed-numeric", "cancelled"} {
		t.Run(shape, func(t *testing.T) {
			f := newDeliveryFixture(t)
			noticeConfig(t, f)
			noticeSeed(t, f, noticeHub, "assets_missing")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			script := ""
			switch shape {
			case "old":
				script = "#!/bin/sh\necho v20.1.0\n"
			case "sleep":
				script = "#!/bin/sh\nexec /bin/sleep 5\n"
			case "execution-failure":
				script = "#!/missing-test-interpreter\n"
			case "malformed":
				script = "#!/bin/sh\necho private-error-text\n"
			case "malformed-numeric":
				script = "#!/bin/sh\necho v22.x.0\n"
			case "cancelled":
				cancel()
			}
			if script != "" {
				deliveryWrite(t, filepath.Join(f.dir, "bin/node"), script, 0700)
			}
			start := time.Now()
			err := f.app.ensureCodexDelivery(ctx, noticeHub, f.config, f.bindings)
			if time.Since(start) > 2*time.Second || (err == nil) != (shape == "supported") {
				t.Fatalf("probe classification shape=%s error=%v elapsed=%s", shape, err, time.Since(start))
			}
			want := "node_unavailable"
			if shape == "supported" {
				want = ""
			} else if shape == "old" {
				want = "node_too_old"
			}
			noticeReason(t, f, want)
			if want == "node_unavailable" {
				code, out, errs := noticeRun(f, "codex", "status")
				if code != 0 || errs != "" || !strings.Contains(out, want) || !strings.Contains(out, "Action:") || strings.Contains(out, "install Node") || strings.Contains(out, "private-error-text") {
					t.Fatalf("dishonest probe status: exit=%d out=%q err=%q", code, out, errs)
				}
			}
		})
	}
}

func noticePath(f *deliveryFixture, hub string) string {
	_, slug, _ := codexDeliveryHub(hub)
	return filepath.Join(f.env["XDG_DATA_HOME"], "vecnet", "codex-notices", slug, "state.json")
}

func noticeReason(t *testing.T, f *deliveryFixture, want string) {
	t.Helper()
	path := noticePath(f, noticeHub)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("persisted reason: %v", err)
	}
	var state struct{ Hub, Reason string }
	if json.Unmarshal(raw, &state) != nil || state.Hub != noticeHub || state.Reason != want {
		t.Fatalf("persisted reason = %s, want %q", raw, want)
	}
	st, err := os.Stat(path)
	if err != nil || st.Mode().Perm() != 0600 {
		t.Fatalf("metadata must be private: %v %v", st, err)
	}
	if strings.Contains(string(raw), f.dir) || strings.Contains(string(raw), "private-error-text") {
		t.Fatal("metadata leaked a private path or upstream error")
	}
}

func TestCodexNoticeReasonsPersist(t *testing.T) {
	for _, reason := range []string{"node_missing", "node_too_old", "assets_missing", "manager_refused", "ownership_ambiguous"} {
		t.Run(reason, func(t *testing.T) {
			f := newDeliveryFixture(t)
			switch reason {
			case "node_missing":
				if err := os.Remove(filepath.Join(f.dir, "bin/node")); err != nil {
					t.Fatal(err)
				}
			case "node_too_old":
				deliveryWrite(t, filepath.Join(f.dir, "bin/node"), "#!/bin/sh\necho v20.1.0\necho private-error-text >&2\n", 0700)
			case "assets_missing":
				f.env["VECNET_CODEX_DELIVERY_ENTRY"] = filepath.Join(f.dir, "missing.ts")
			case "manager_refused":
				f.installManaged()
				deliveryWrite(t, filepath.Join(f.dir, "systemd-show-failure"), "error", 0600)
			case "ownership_ambiguous":
				if err := f.ensure(noticeHub); err != nil {
					t.Fatal(err)
				}
				_, meta := f.owner()
				raw, _ := os.ReadFile(meta)
				t.Cleanup(func() { deliveryWrite(t, meta, string(raw), 0600) })
				deliveryWrite(t, meta, "{}", 0600)
			}
			if err := f.ensure(noticeHub + "/"); err == nil {
				t.Fatal("fixture must refuse activation")
			}
			noticeReason(t, f, reason)
		})
	}
}

func noticeSeed(t *testing.T, f *deliveryFixture, hub, reason string) {
	t.Helper()
	path := noticePath(f, hub)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	normalized, _, _ := codexDeliveryHub(hub)
	raw, _ := json.Marshal(map[string]any{"version": 1, "hub": normalized, "reason": reason})
	deliveryWrite(t, path, string(raw), 0600)
}

func noticeConfig(t *testing.T, f *deliveryFixture) {
	t.Helper()
	f.env["VECNET_CONFIG"] = f.config
	f.env["VECNET_PROFILE"] = "p"
	if err := saveConfig(f.config, Config{Profiles: map[string]Profile{"p": {Hub: noticeHub, Secret: "vn_peer_test"}, "q": {Hub: "https://hub.other.invalid", Secret: "vn_peer_other"}}}); err != nil {
		t.Fatal(err)
	}
}

func noticeRun(f *deliveryFixture, args ...string) (int, string, string) {
	var out, errs bytes.Buffer
	code := RunContext(context.Background(), args, strings.NewReader(""), &out, &errs, f.app.getenv)
	return code, out.String(), errs.String()
}

func TestCodexStatusKnownReasons(t *testing.T) {
	for _, reason := range []string{"node_missing", "node_too_old", "assets_missing", "manager_refused", "ownership_ambiguous", "other_thread_live"} {
		t.Run(reason, func(t *testing.T) {
			f := newDeliveryFixture(t)
			noticeConfig(t, f)
			noticeSeed(t, f, noticeHub, reason)
			code, out, errs := noticeRun(f, "codex", "status")
			if code != 0 || errs != "" || !strings.Contains(out, reason) || !strings.Contains(out, "Action:") {
				t.Fatalf("status exit=%d stdout=%q stderr=%q", code, out, errs)
			}
			code, out, errs = noticeRun(f, "--net", "q", "codex", "status")
			if code != 0 || errs != "" || strings.Contains(out, reason) || !strings.Contains(out, "unknown") {
				t.Fatalf("profile isolation exit=%d out=%q err=%q", code, out, errs)
			}
			if strings.Contains(out, f.dir) || strings.Contains(out, "vn_peer") {
				t.Fatal("status leaked private data")
			}
		})
	}
	for _, shape := range []string{"absent", "corrupt", "public", "foreign-hub", "missing-identity", "missing-version", "oversize", "fifo", "symlink", "optout-env", "optout-profile"} {
		t.Run(shape, func(t *testing.T) {
			f := newDeliveryFixture(t)
			noticeConfig(t, f)
			path := noticePath(f, noticeHub)
			if shape != "absent" {
				noticeSeed(t, f, noticeHub, "node_missing")
			}
			switch shape {
			case "corrupt":
				deliveryWrite(t, path, "{private-error-text", 0600)
			case "public":
				if err := os.Chmod(path, 0644); err != nil {
					t.Fatal(err)
				}
			case "missing-identity":
				deliveryWrite(t, path, `{"version":1,"reason":"node_missing"}`, 0600)
			case "missing-version":
				deliveryWrite(t, path, `{"hub":"https://hub.invalid","reason":"node_missing"}`, 0600)
			case "foreign-hub":
				deliveryWrite(t, path, `{"version":1,"hub":"https://foreign.invalid","reason":"node_missing"}`, 0600)
			case "oversize":
				deliveryWrite(t, path, strings.Repeat(" ", 8192), 0600)
			case "fifo":
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err := unix.Mkfifo(path, 0600); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Rename(path, path+".safe"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(path+".safe", path); err != nil {
					t.Fatal(err)
				}
			case "optout-env":
				f.env["VECNET_CODEX_AUTO_DELIVERY"] = "0"
			case "optout-profile":
				path, _ := codexBindingsPath(f.app.getenv)
				if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
					t.Fatal(err)
				}
				deliveryWrite(t, path, `{"version":1,"bindings":[],"disabled":[{"profile":"p","as":""}]}`, 0600)
			}
			start := time.Now()
			code, out, errs := noticeRun(f, "codex", "status")
			want := "unknown"
			if strings.HasPrefix(shape, "optout") {
				want = "disabled"
			}
			if code != 0 || errs != "" || !strings.Contains(out, want) || strings.Contains(out, "node_missing") || strings.Contains(out, "private-error-text") {
				t.Fatalf("safe status exit=%d out=%q err=%q", code, out, errs)
			}
			if time.Since(start) > 200*time.Millisecond {
				t.Fatal("status blocked on metadata")
			}
		})
	}
}

func noticeTTY(t *testing.T) *os.File {
	_, tty := noticePTY(t)
	return tty
}

func noticePTY(t *testing.T) (*os.File, *os.File) {
	t.Helper()
	master, err := os.OpenFile("/dev/ptmx", os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { master.Close() })
	if err := unix.IoctlSetPointerInt(int(master.Fd()), unix.TIOCSPTLCK, 0); err != nil {
		t.Fatal(err)
	}
	n, err := unix.IoctlGetInt(int(master.Fd()), unix.TIOCGPTN)
	if err != nil {
		t.Fatal(err)
	}
	slave, err := os.OpenFile("/dev/pts/"+strconv.Itoa(n), os.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { slave.Close() })
	return master, slave
}

func noticeAction(f *deliveryFixture, tty *os.File) string {
	var errs bytes.Buffer
	a := &app{ctx: context.Background(), stdin: tty, stdout: tty, stderr: &errs, getenv: f.app.getenv}
	a.autoCodexAction("peers", nil)
	return errs.String()
}

func noticeForeground(t *testing.T) (*deliveryFixture, *os.File) {
	t.Helper()
	f := newDeliveryFixture(t)
	noticeConfig(t, f)
	f.env["CODEX_THREAD_ID"] = bindT1
	// A relative binary prevents helper execution after normal preflight.
	f.env["VECNET_BIN"] = "relative"
	noticeSeed(t, f, noticeHub, "node_missing")
	return f, noticeTTY(t)
}

func TestCodexNoticeDebounced(t *testing.T) {
	f, tty := noticeForeground(t)
	first := noticeAction(f, tty)
	if first != "vecnet: Codex auto-delivery inactive for https://hub.invalid: node_missing; run `vecnet codex status`\n" {
		t.Fatalf("foreground notice=%q", first)
	}
	if again := noticeAction(f, tty); again != "" {
		t.Fatalf("repeated notice=%q", again)
	}
	var state map[string]any
	raw, _ := os.ReadFile(noticePath(f, noticeHub))
	if err := json.Unmarshal(raw, &state); err != nil {
		t.Fatal(err)
	}
	state["reason"] = "assets_missing"
	raw, _ = json.Marshal(state)
	deliveryWrite(t, noticePath(f, noticeHub), string(raw), 0600)
	results := make(chan string, 16)
	for i := 0; i < 16; i++ {
		go func() { results <- noticeAction(f, tty) }()
	}
	count := 0
	for i := 0; i < 16; i++ {
		if result := <-results; result != "" {
			count++
			if !strings.Contains(result, "assets_missing") {
				t.Errorf("changed reason=%q", result)
			}
		}
	}
	if count != 1 {
		t.Fatalf("concurrent notices=%d, want 1", count)
	}
	// Returning to an already announced reason retains its own 24h debounce.
	raw, _ = os.ReadFile(noticePath(f, noticeHub))
	_ = json.Unmarshal(raw, &state)
	state["reason"] = "node_missing"
	raw, _ = json.Marshal(state)
	deliveryWrite(t, noticePath(f, noticeHub), string(raw), 0600)
	if result := noticeAction(f, tty); result != "" {
		t.Fatalf("reason-cycle notice=%q", result)
	}
	state["noticed"] = map[string]time.Time{"node_missing": time.Now().Add(-24*time.Hour - time.Second)}
	raw, _ = json.Marshal(state)
	deliveryWrite(t, noticePath(f, noticeHub), string(raw), 0600)
	if result := noticeAction(f, tty); !strings.Contains(result, "node_missing") {
		t.Fatalf("expired notice=%q", result)
	}
}

func TestCodexNoticeHooksSilent(t *testing.T) {
	f, tty := noticeForeground(t)
	if noticeAction(f, tty) == "" {
		t.Error("interactive control must emit a notice")
	}
	for _, inherited := range []bool{false, true} {
		t.Run(strconv.FormatBool(inherited), func(t *testing.T) {
			bf := autoFixture(t)
			df := &deliveryFixture{env: bf.env}
			noticeSeed(t, df, bf.hub, "node_missing")
			if inherited {
				// A helper with a future stderr write must not keep command pipes
				// open or inject output into the admitted hook.
				deliveryWrite(t, bf.env["VECNET_BIN"], "#!/bin/sh\n/bin/sleep 1\necho private-error-text >&2\n", 0700)
			}
			start := time.Now()
			code, out, errs := autoRun(bf, `{"hook_event_name":"PostToolUse"}`, "hook")
			if code != 0 || out != "" || strings.Contains(errs, "auto-delivery") || strings.Contains(errs, "private-error-text") {
				t.Fatalf("hook exit=%d out=%q err=%q", code, out, errs)
			}
			if time.Since(start) > 1500*time.Millisecond {
				t.Fatal("helper extended hook deadline")
			}
			if !inherited {
				autoDone(bf)
			}
			// Even a terminal-backed hook never prints the notice.
			var hookErr bytes.Buffer
			a := &app{ctx: context.Background(), inHook: true, stdin: tty, stdout: tty, stderr: &hookErr, getenv: f.app.getenv}
			a.autoCodexAction("hook", nil)
			if hookErr.Len() != 0 {
				t.Fatalf("terminal hook stderr=%q", hookErr.String())
			}
		})
	}
}

func noticeTerminalRun(t *testing.T, f *bindFixture, args ...string) (int, string, string) {
	t.Helper()
	master, tty := noticePTY(t)
	var errs bytes.Buffer
	code := RunContext(context.Background(), args, tty, tty, &errs, func(k string) string { return f.env[k] })
	var out strings.Builder
	for {
		poll := []unix.PollFd{{Fd: int32(master.Fd()), Events: unix.POLLIN}}
		if n, err := unix.Poll(poll, 10); err != nil || n == 0 {
			break
		}
		buf := make([]byte, 8192)
		n, err := unix.Read(int(master.Fd()), buf)
		if err != nil {
			t.Fatal(err)
		}
		out.Write(buf[:n])
	}
	return code, out.String(), errs.String()
}

func TestCodexNoticePreservesCommandOutput(t *testing.T) {
	for _, args := range [][]string{{"peers"}, {"peers", "--unknown-flag"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			f := autoFixture(t)
			// Supplemental strengthening after the primary RED/GREEN: preserve
			// nonempty successful stdout, rather than an empty directory listing.
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.WriteString(w, `{"peers":[{"peer_id":"peer_fixture","kind":"agent","name":"Fixture","presence":"online","reachability":"yes"}]}`)
			}))
			t.Cleanup(server.Close)
			f.hub = server.URL
			f.config(f.hub)
			f.env["VECNET_BIN"] = "relative"
			df := &deliveryFixture{env: f.env}
			noticeSeed(t, df, f.hub, "node_missing")
			f.env["VECNET_CODEX_AUTO_DELIVERY"] = "0"
			wantCode, wantOut, wantErr := noticeTerminalRun(t, f, args...)
			if len(args) == 1 && !strings.Contains(wantOut, "peer_fixture") {
				t.Fatalf("missing successful output: %q", wantOut)
			}
			delete(f.env, "VECNET_CODEX_AUTO_DELIVERY")
			code, out, errs := noticeTerminalRun(t, f, args...)
			line := "vecnet: Codex auto-delivery inactive for " + f.hub + ": node_missing; run `vecnet codex status`\n"
			if code != wantCode || out != wantOut || errs != line+wantErr {
				t.Fatalf("command result exit=%d/%d out=%q/%q err=%q want=%q", code, wantCode, out, wantOut, errs, line+wantErr)
			}
			// Noninteractive automation preserves stderr too.
			noticeSeed(t, df, f.hub, "assets_missing")
			code, out, errs = autoRun(f, "", args...)
			f.env["VECNET_CODEX_AUTO_DELIVERY"] = "0"
			wantCode, wantOut, wantErr = autoRun(f, "", args...)
			if code != wantCode || out != wantOut || errs != wantErr {
				t.Fatal("noninteractive result changed")
			}
		})
	}
}

func TestCodexNoticeRegistrationReasons(t *testing.T) {
	for _, reason := range []string{"node_missing", "node_too_old", "assets_missing", "other_thread_live", "ownership_ambiguous"} {
		t.Run(reason, func(t *testing.T) {
			f := autoFixture(t)
			thread := bindT1
			switch reason {
			case "node_missing":
				if err := os.Remove(filepath.Join(f.env["PATH"], "node")); err != nil {
					t.Fatal(err)
				}
			case "node_too_old":
				deliveryWrite(t, filepath.Join(f.env["PATH"], "node"), "#!/bin/sh\necho v20.1.0\n", 0700)
			case "assets_missing":
				f.env["VECNET_CODEX_DELIVERY_ENTRY"] += ".missing"
			default:
				f.bind("p", bindT1)
				thread = bindT2
				previous := "live"
				if reason == "ownership_ambiguous" {
					previous = "unknown"
				}
				deliveryWrite(t, filepath.Join(f.env["HOME"], "probe-result"), `{"loaded":true,"previous":"`+previous+`"}`, 0600)
			}
			_ = autoRegister(f, thread)
			raw, err := os.ReadFile(noticePath(&deliveryFixture{env: f.env}, f.hub))
			if err != nil {
				t.Fatalf("automatic registration reason: %v", err)
			}
			var state struct{ Reason string }
			if json.Unmarshal(raw, &state) != nil || state.Reason != reason {
				t.Fatalf("automatic reason=%s, want %s", raw, reason)
			}
		})
	}
}

func TestCodexNoticeActivationClears(t *testing.T) {
	f := newDeliveryFixture(t)
	noticeSeed(t, f, noticeHub, "manager_refused")
	if err := f.ensure(noticeHub); err != nil {
		t.Fatal(err)
	}
	noticeReason(t, f, "")
	// Verified reuse clears a subsequent known refusal too.
	noticeSeed(t, f, noticeHub, "node_missing")
	if err := f.ensure(noticeHub); err != nil {
		t.Fatal(err)
	}
	noticeReason(t, f, "")
}

// Supplemental exact-clock coverage after the primary foreground RED/GREEN.
func TestCodexNoticeExactExpiry(t *testing.T) {
	f := newDeliveryFixture(t)
	noticeSeed(t, f, noticeHub, "node_missing")
	now := time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)
	if f.app.codexNoticeMessage(noticeHub, now) == "" {
		t.Fatal("first notice absent")
	}
	if f.app.codexNoticeMessage(noticeHub, now.Add(24*time.Hour-time.Nanosecond)) != "" {
		t.Fatal("notice before expiry")
	}
	if f.app.codexNoticeMessage(noticeHub, now.Add(24*time.Hour)) == "" {
		t.Fatal("notice absent at exact expiry")
	}
}

// Supplemental hostile-metadata and opt-out coverage after primary RED/GREEN.
func TestCodexNoticeForegroundSafety(t *testing.T) {
	for _, shape := range []string{"absent", "corrupt", "oversize", "fifo", "symlink", "directory-symlink", "public-directory", "hardlink", "locked", "optout-env", "optout-profile"} {
		t.Run(shape, func(t *testing.T) {
			f, tty := noticeForeground(t)
			path := noticePath(f, noticeHub)
			switch shape {
			case "absent":
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			case "corrupt":
				deliveryWrite(t, path, "{private-error-text", 0600)
			case "oversize":
				deliveryWrite(t, path, strings.Repeat(" ", 8192), 0600)
			case "fifo":
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err := unix.Mkfifo(path, 0600); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Rename(path, path+".safe"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(path+".safe", path); err != nil {
					t.Fatal(err)
				}
			case "directory-symlink":
				dir := filepath.Dir(path)
				if err := os.Rename(dir, dir+".safe"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(dir+".safe", dir); err != nil {
					t.Fatal(err)
				}
			case "public-directory":
				if err := os.Chmod(filepath.Dir(path), 0755); err != nil {
					t.Fatal(err)
				}
			case "hardlink":
				if err := os.Link(path, path+".safe"); err != nil {
					t.Fatal(err)
				}
			case "locked":
				lock, err := deliveryPrivateFile(filepath.Join(filepath.Dir(path), "state.lock"), true)
				if err != nil {
					t.Fatal(err)
				}
				defer lock.Close()
				if locked, err := deliveryLock(lock); err != nil || !locked {
					t.Fatal("cannot lock fixture marker")
				}
			case "optout-env":
				f.env["VECNET_CODEX_AUTO_DELIVERY"] = "0"
			case "optout-profile":
				bindingPath, _ := codexBindingsPath(f.app.getenv)
				if err := os.MkdirAll(filepath.Dir(bindingPath), 0700); err != nil {
					t.Fatal(err)
				}
				deliveryWrite(t, bindingPath, `{"version":1,"bindings":[],"disabled":[{"profile":"p","as":""}]}`, 0600)
			}
			start := time.Now()
			if result := noticeAction(f, tty); result != "" {
				t.Fatalf("unsafe/disabled state emitted %q", result)
			}
			if time.Since(start) > 200*time.Millisecond {
				t.Fatal("foreground metadata read blocked")
			}
		})
	}
}

func TestCodexStatusNoRPC(t *testing.T) {
	f := newDeliveryFixture(t)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); http.Error(w, "unexpected RPC", 500) }))
	t.Cleanup(server.Close)
	f.env["VECNET_CONFIG"] = f.config
	f.env["VECNET_PROFILE"] = "p"
	if err := saveConfig(f.config, Config{Profiles: map[string]Profile{"p": {Hub: server.URL, Secret: "vn_peer_test"}}}); err != nil {
		t.Fatal(err)
	}
	noticeSeed(t, f, server.URL, "node_missing")
	code, out, errs := noticeRun(f, "codex", "status")
	if code != 0 || errs != "" || !strings.Contains(out, "node_missing") || calls.Load() != 0 {
		t.Fatalf("status exit=%d out=%q err=%q calls=%d", code, out, errs, calls.Load())
	}
	if _, err := os.Stat(filepath.Join(f.dir, "systemctl-calls")); !os.IsNotExist(err) {
		t.Fatal("status called manager")
	}
}

// Supplemental real inherited-pipe check: capture a subprocess CLI's OS
// stdout/stderr, so accidental inheritance by its detached helper is visible.
func TestCodexNoticeHookPipes(t *testing.T) {
	f := newDeliveryFixture(t)
	bf := autoFixture(t)
	f.env["VECNET_CONFIG"] = bf.env["VECNET_CONFIG"]
	f.env["VECNET_PROFILE"] = "p"
	f.env["CODEX_THREAD_ID"] = bindT1
	noticeSeed(t, f, bf.hub, "node_missing")
	binary := f.env["VECNET_BIN"]
	raw, err := os.ReadFile(binary)
	if err != nil {
		t.Fatal(err)
	}
	// Preserve the fixture CLI entrypoint, intercept only its helper role.
	script := "#!/bin/sh\ncase \"$*\" in *'codex auto-register'*) printf started > \"$HOME/notice-helper-started\"; /bin/sleep 2; echo private-error-text >&2; exit 0;; esac\n" + strings.TrimPrefix(string(raw), "#!/bin/sh\n")
	deliveryWrite(t, binary, script, 0700)
	cmd := f.command("hook", "--event", "post-tool")
	cmd.Stdin = strings.NewReader(`{"hook_event_name":"PostToolUse"}`)
	start := time.Now()
	out, err := cmd.CombinedOutput()
	elapsed := time.Since(start)
	if err != nil || len(out) != 0 {
		t.Fatalf("hook OS pipes: err=%v out=%q", err, out)
	}
	if elapsed > 1500*time.Millisecond {
		t.Fatalf("helper retained hook pipes for %s", elapsed)
	}
	deliveryWait(t, func() bool { _, err := os.Stat(filepath.Join(f.dir, "notice-helper-started")); return err == nil })
}
