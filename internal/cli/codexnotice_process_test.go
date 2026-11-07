//go:build linux

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
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// These are isolated process proofs, not installed-service acceptance. The
// executable is this package's test binary, dispatching the real Run entrypoint
// with OS descriptors, as in the existing auto/delivery process fixtures.
// No production fault switch or notice/storage implementation is substituted.
func TestCodexNoticeProcess(t *testing.T) {
	var args []string
	for i, arg := range os.Args {
		if arg == "notice-process-fixture" {
			args = os.Args[i+1:]
			break
		}
	}
	if len(args) == 0 {
		return
	}
	if args[0] == "traced" {
		// Keep the real child's stderr separate from supervisor diagnostics.
		// This is an explicit private-fixture argv role, not a product flag.
		os.Stderr = os.NewFile(3, "private-child-stderr")
		unix.CloseOnExec(3)
		args = args[1:]
	}
	root := os.Getenv("HOME")
	deliveryAccountScope = func() (string, string, error) {
		return root, os.Getenv("XDG_RUNTIME_DIR"), nil
	}
	getenv := os.Getenv
	if args[0] == "limited-cli" || args[0] == "limited-ensure" {
		// An actual kernel write failure, confined to this private child and
		// its descendants. Reads, pipes and terminal output remain available.
		signal.Ignore(syscall.SIGXFSZ)
		if unix.Setrlimit(unix.RLIMIT_FSIZE, &unix.Rlimit{Cur: 0, Max: 0}) != nil {
			os.Exit(90)
		}
		args[0] = strings.TrimPrefix(args[0], "limited-")
	}
	switch args[0] {
	case "gated-cli":
		// Exercise the original review's post-publication/preflight deadline
		// through the existing getenv boundary. This is a scheduling gate,
		// NOT a claim that a regular filesystem read/fsync has been stalled.
		getenv = func(key string) string {
			if key == "VECNET_BIN" {
				if os.WriteFile(filepath.Join(root, "notice-preflight-gated"), []byte("entered"), 0600) != nil {
					os.Exit(91)
				}
				time.Sleep(2 * time.Second)
			}
			return os.Getenv(key)
		}
	case "ensure":
		// Keep publication's two fsync calls on one traced task so strace's
		// per-task when=2 counter identifies the directory sync exactly.
		runtime.LockOSThread()
		a := &app{ctx: context.Background(), getenv: getenv, stdout: os.Stdout, stderr: os.Stderr}
		if err := a.ensureCodexDelivery(a.ctx, args[1], args[2], args[3]); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	case "notice-at":
		runtime.LockOSThread()
		now, err := time.Parse(time.RFC3339Nano, args[2])
		if err != nil {
			os.Exit(92)
		}
		a := &app{ctx: context.Background(), getenv: getenv}
		fmt.Fprint(os.Stdout, a.codexNoticeMessage(args[1], now))
		os.Exit(0)
	case "timed-cli":
		runtime.LockOSThread()
		started := time.Now()
		code := Run(args[1:], os.Stdin, os.Stdout, os.Stderr, getenv)
		noticeProcessRecordPhase(started, code)
		os.Exit(code)
	case "timed-notice":
		runtime.LockOSThread()
		now, err := time.Parse(time.RFC3339Nano, args[2])
		if err != nil {
			os.Exit(92)
		}
		started := time.Now()
		a := &app{ctx: context.Background(), getenv: getenv}
		fmt.Fprint(os.Stdout, a.codexNoticeMessage(args[1], now))
		noticeProcessRecordPhase(started, 0)
		os.Exit(0)
	case "cli":
	default:
		os.Exit(93)
	}
	os.Exit(Run(args[1:], os.Stdin, os.Stdout, os.Stderr, getenv))
}

type noticeProcessPhase struct {
	Started, Returned time.Time
	Elapsed           time.Duration
	PID, TID, Code    int
}

// Private child metadata, never a product input or clock substitution.
func noticeProcessRecordPhase(started time.Time, code int) {
	returned := time.Now()
	phase := noticeProcessPhase{started, returned, returned.Sub(started), os.Getpid(), unix.Gettid(), code}
	raw, _ := json.Marshal(phase)
	duration, _ := json.Marshal(phase.Elapsed)
	root := os.Getenv("HOME")
	if os.WriteFile(filepath.Join(root, "notice-command-phase.json"), raw, 0600) != nil || os.WriteFile(filepath.Join(root, "notice-command-duration.json"), duration, 0600) != nil {
		os.Exit(94)
	}
}

type noticeProcessResult struct {
	code     int
	out, err string
	elapsed  time.Duration
}

func noticeProcessCommand(t *testing.T, f *deliveryFixture, role string, args ...string) *exec.Cmd {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	childRole := role
	var traceArgs []string
	if strings.HasPrefix(role, "trace-") {
		// R33 authorizes supervision of our own child only. The absolute
		// executable path is controller-discovered, never host PATH lookup.
		traceArgs = []string{"-f", "-q", "-ttt", "-T", "-yy", "-o", filepath.Join(f.dir, "notice-syscalls.log"), "-e", "trace=fsync,renameat,exit_group"}
		switch role {
		case "trace-baseline-cli":
			childRole = "timed-cli"
		case "trace-delay-cli":
			childRole = "timed-cli"
			traceArgs = append(traceArgs, "-e", "inject=fsync:delay_enter=800ms:when=1")
		case "trace-delay-control":
			childRole = "timed-notice"
			traceArgs = append(traceArgs, "-e", "inject=fsync:delay_enter=800ms:when=1")
		case "trace-fail-notice":
			childRole = "notice-at"
			traceArgs = append(traceArgs, "-e", "inject=fsync:error=EIO:when=2")
		case "trace-fail-ensure":
			childRole = "ensure"
			traceArgs = append(traceArgs, "-e", "inject=fsync:error=EIO:when=2")
		default:
			t.Fatalf("unknown private trace role %q", role)
		}
	}
	fixtureArgs := []string{"notice-process-fixture", childRole}
	if traceArgs != nil {
		fixtureArgs = []string{"notice-process-fixture", "traced", childRole}
	}
	childArgs := append(append([]string{"-test.run=^TestCodexNoticeProcess$", "--"}, fixtureArgs...), args...)
	c := exec.Command(exe, childArgs...)
	if traceArgs != nil {
		c = exec.Command("/usr/bin/strace", append(append(traceArgs, "--", exe), childArgs...)...)
	}
	c.Env = append(f.environ(), "GORACE=atexit_sleep_ms=0")
	return c
}

// Explicit pipes are drained until kernel EOF, independently of cmd.Wait.
// A descendant retaining either writer therefore fails the EOF deadline even
// if the foreground CLI itself has already exited. No WaitDelay masks a leak.
func noticeProcessRun(t *testing.T, f *deliveryFixture, tty bool, input, role string, args ...string) noticeProcessResult {
	t.Helper()
	c := noticeProcessCommand(t, f, role, args...)
	if strings.HasPrefix(role, "trace-") {
		for _, file := range []string{"notice-command-phase.json", "notice-command-duration.json"} {
			if err := os.Remove(filepath.Join(f.dir, file)); err != nil && !errors.Is(err, os.ErrNotExist) {
				t.Fatal("cannot clear stale private child phase evidence")
			}
		}
		defer func() {
			trace, err := os.ReadFile(filepath.Join(f.dir, "notice-syscalls.log"))
			if err != nil {
				t.Error("private trace unavailable for preservation")
				return
			}
			// Keep full private evidence beyond t.TempDir cleanup, including
			// fixture failures. This is a verification artifact, not source.
			dir := filepath.Join("..", "..", ".superpowers", "sdd", "2026-11-07-codex-auto-delivery", "task-5c3-fix1-syscall-traces")
			name := strings.ReplaceAll(t.Name(), "/", "_") + "-" + role + "-"
			if err := os.MkdirAll(dir, 0700); err != nil {
				t.Error("cannot create private syscall evidence directory")
				return
			}
			archive, err := os.MkdirTemp(dir, name)
			if err != nil {
				t.Error("cannot reserve fresh private syscall evidence archive")
				return
			}
			if err := os.WriteFile(filepath.Join(archive, "trace.log"), trace, 0600); err != nil {
				t.Error("cannot preserve private syscall evidence")
			}
			for _, file := range []string{"notice-command-phase.json", "notice-command-duration.json", "notice-supervisor-stderr.log"} {
				data, err := os.ReadFile(filepath.Join(f.dir, file))
				if err == nil {
					if err := os.WriteFile(filepath.Join(archive, file), data, 0600); err != nil {
						t.Error("cannot preserve private child phase evidence")
					}
				}
			}
		}()
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer outR.Close()
	defer outW.Close()
	errR, errW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer errR.Close()
	defer errW.Close()
	c.Stdin, c.Stdout, c.Stderr = strings.NewReader(input), outW, errW
	if strings.HasPrefix(role, "trace-") {
		supervisor, err := os.OpenFile(filepath.Join(f.dir, "notice-supervisor-stderr.log"), os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
		if err != nil {
			t.Fatal("cannot capture private supervisor diagnostics")
		}
		defer supervisor.Close()
		c.Stderr = supervisor
		c.ExtraFiles = []*os.File{errW}
	}
	var master, slave *os.File
	if tty {
		master, slave = noticePTY(t)
		c.Stdin, c.Stdout = slave, slave
		// Disable line conversion so stdout can be compared byte for byte.
		term, err := unix.IoctlGetTermios(int(slave.Fd()), unix.TCGETS)
		if err != nil {
			t.Fatal(err)
		}
		term.Oflag &^= unix.OPOST
		if err := unix.IoctlSetTermios(int(slave.Fd()), unix.TCSETS, term); err != nil {
			t.Fatal(err)
		}
	}
	started := time.Now()
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Process.Kill() }()
	_ = outW.Close()
	_ = errW.Close()
	if slave != nil {
		_ = slave.Close()
	}
	type readResult struct {
		data []byte
		err  error
	}
	read := func(r *os.File, pty bool) <-chan readResult {
		ch := make(chan readResult, 1)
		go func() {
			data, err := io.ReadAll(r)
			if pty && errors.Is(err, syscall.EIO) {
				err = nil // Linux PTY EOF once every slave descriptor closes.
			}
			ch <- readResult{data, err}
		}()
		return ch
	}
	stdout := outR
	if tty {
		stdout = master
	}
	outDone, errDone := read(stdout, tty), read(errR, false)
	waitDone := make(chan error, 1)
	go func() { waitDone <- c.Wait() }()
	budget := 1500 * time.Millisecond
	if role == "ensure" || role == "limited-ensure" {
		budget = 8 * time.Second // ensure's existing six-second context.
	}
	if strings.HasPrefix(role, "trace-") {
		// Tracer shutdown may include an injected delay after the actual CLI
		// has returned. The supervised CLI's own recorded duration is checked
		// separately; do not mistake tracer wall time for foreground cost.
		budget = 8 * time.Second
	}
	deadline := time.NewTimer(budget)
	defer deadline.Stop()
	collect := func(ch <-chan readResult, label string) string {
		t.Helper()
		select {
		case result := <-ch:
			if result.err != nil {
				t.Fatalf("%s read: %v", label, result.err)
			}
			return string(result.data)
		case <-deadline.C:
			t.Fatalf("%s %s did not reach OS EOF within %s", role, label, budget)
			return ""
		}
	}
	r := noticeProcessResult{out: collect(outDone, "stdout"), err: collect(errDone, "stderr")}
	select {
	case err := <-waitDone:
		if err != nil {
			if exit, ok := err.(*exec.ExitError); ok {
				r.code = exit.ExitCode()
			} else {
				t.Fatal(err)
			}
		}
	case <-deadline.C:
		t.Fatalf("%s process did not exit within %s", role, budget)
	}
	r.elapsed = time.Since(started)
	for _, private := range []string{"private-error-text", "vn_peer_", "private-body-text", f.dir} {
		if strings.Contains(r.out+r.err, private) {
			t.Fatalf("private fixture data reached CLI output: %q", private)
		}
	}
	return r
}

func noticeProcessState(t *testing.T, f *deliveryFixture, hub string) codexNoticeState {
	t.Helper()
	raw, err := os.ReadFile(noticePath(f, hub))
	if err != nil {
		t.Fatal(err)
	}
	var state codexNoticeState
	if err := json.Unmarshal(raw, &state); err != nil {
		t.Fatal(err)
	}
	if state.Version != 1 || state.Hub != normalizeHub(hub) {
		t.Fatal("notice lost its version or full hub identity")
	}
	return state
}

func noticeProcessDuration(t *testing.T, f *deliveryFixture) time.Duration {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(f.dir, "notice-command-duration.json"))
	if err != nil {
		t.Fatal("supervised CLI did not record its own duration")
	}
	var elapsed time.Duration
	if json.Unmarshal(raw, &elapsed) != nil || elapsed <= 0 {
		t.Fatal("invalid supervised CLI duration")
	}
	return elapsed
}

type noticeProcessTraceEvent struct {
	tid    int
	at     time.Time
	detail string
}

func noticeProcessReadTrace(t *testing.T, f *deliveryFixture) []noticeProcessTraceEvent {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(f.dir, "notice-syscalls.log"))
	if err != nil {
		t.Fatal("private syscall trace was not retained")
	}
	var events []noticeProcessTraceEvent
	for _, line := range strings.Split(string(raw), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		tid, tidErr := strconv.Atoi(fields[0])
		seconds, timeErr := strconv.ParseFloat(fields[1], 64)
		if tidErr != nil || timeErr != nil || tid <= 1 || seconds <= 0 {
			t.Fatal("private syscall trace does not provide task IDs and absolute timestamps")
		}
		events = append(events, noticeProcessTraceEvent{tid, time.Unix(0, int64(seconds*1e9)), strings.Join(fields[2:], " ")})
	}
	return events
}

func noticeProcessTraceExit(events []noticeProcessTraceEvent, tid, code int) (entering, exited time.Time, ok bool) {
	var resumed time.Time
	unfinished := false
	for _, event := range events {
		if event.tid != tid {
			continue
		}
		line := event.detail
		// strace can split exit_group before printing its closing parenthesis.
		// Match the whole code token, then require this caller's resumed
		// unknown result before accepting its actual exit status.
		if strings.HasPrefix(line, fmt.Sprintf("exit_group(%d)", code)) || line == fmt.Sprintf("exit_group(%d <unfinished ...>", code) {
			if !entering.IsZero() || !exited.IsZero() {
				return entering, exited, false
			}
			entering = event.at
			unfinished = strings.Contains(line, "<unfinished ...>")
		}
		if strings.HasPrefix(line, "<... exit_group resumed>") {
			if !unfinished || entering.IsZero() || !resumed.IsZero() || !exited.IsZero() || event.at.Before(entering) || line != "<... exit_group resumed>) = ?" {
				return entering, exited, false
			}
			resumed = event.at
		}
		if line == fmt.Sprintf("+++ exited with %d +++", code) {
			if entering.IsZero() || (unfinished && (resumed.IsZero() || event.at.Before(resumed))) {
				return entering, exited, false
			}
			exited = event.at
		}
	}
	return entering, exited, !entering.IsZero() && !exited.Before(entering)
}

func TestCodexNoticeTraceExit(t *testing.T) {
	// Literal exit events from the first failed full run (caller 315303,
	// returned CLI code 1). No private paths or generated expectations.
	entry := noticeProcessTraceEvent{315303, time.Unix(1794074323, 119698000), "exit_group(1 <unfinished ...>"}
	resume := noticeProcessTraceEvent{315303, time.Unix(1794074323, 119740000), "<... exit_group resumed>) = ?"}
	exit := noticeProcessTraceEvent{315303, time.Unix(1794074323, 771562000), "+++ exited with 1 +++"}
	changed := func(event noticeProcessTraceEvent, tid int, detail string) noticeProcessTraceEvent {
		event.tid, event.detail = tid, detail
		return event
	}
	cases := []struct {
		name   string
		events []noticeProcessTraceEvent
		want   bool
	}{
		{"retained-full-unfinished", []noticeProcessTraceEvent{entry, resume, exit}, true},
		{"completed-entry", []noticeProcessTraceEvent{changed(entry, entry.tid, "exit_group(1) = ?"), exit}, true},
		{"closed-unfinished-entry", []noticeProcessTraceEvent{changed(entry, entry.tid, "exit_group(1) <unfinished ...>"), resume, exit}, true},
		{"wrong-entry-caller", []noticeProcessTraceEvent{changed(entry, 315304, entry.detail), resume, exit}, false},
		{"wrong-entry-code", []noticeProcessTraceEvent{changed(entry, entry.tid, "exit_group(11 <unfinished ...>"), resume, exit}, false},
		{"missing-resume", []noticeProcessTraceEvent{entry, exit}, false},
		{"wrong-resume-caller", []noticeProcessTraceEvent{entry, changed(resume, 315304, resume.detail), exit}, false},
		{"wrong-resume-result", []noticeProcessTraceEvent{entry, changed(resume, resume.tid, "<... exit_group resumed>) = 0"), exit}, false},
		{"resume-before-entry", []noticeProcessTraceEvent{resume, entry, exit}, false},
		{"resume-after-exit", []noticeProcessTraceEvent{entry, exit, resume}, false},
		{"resume-timestamp-before-entry", []noticeProcessTraceEvent{entry, {resume.tid, entry.at.Add(-time.Microsecond), resume.detail}, exit}, false},
		{"exit-timestamp-before-resume", []noticeProcessTraceEvent{entry, resume, {exit.tid, resume.at.Add(-time.Microsecond), exit.detail}}, false},
		{"duplicate-entry", []noticeProcessTraceEvent{entry, entry, resume, exit}, false},
		{"wrong-exit-caller", []noticeProcessTraceEvent{entry, resume, changed(exit, 315304, exit.detail)}, false},
		{"wrong-exit-code", []noticeProcessTraceEvent{entry, resume, changed(exit, exit.tid, "+++ exited with 11 +++")}, false},
		{"missing-exit", []noticeProcessTraceEvent{entry, resume}, false},
	}
	for _, tc := range cases {
		entered, exited, ok := noticeProcessTraceExit(tc.events, 315303, 1)
		if ok != tc.want || (ok && (!entered.Equal(entry.at) || !exited.Equal(exit.at))) {
			t.Errorf("%s: exact caller/code/entry/resume/exit correlation = %t, want %t", tc.name, ok, tc.want)
		}
	}
}

// A completed positive control must retain DELAYED evidence and spend the
// actual injected interval. The interrupted case requires independent child
// phase/exit evidence and a same-task unfinished/resumed pair, not just an
// unannotated fsync entry. Both must be executed before claiming a stall proof.
func noticeProcessDelayTrace(t *testing.T, f *deliveryFixture, code int, completed bool) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(f.dir, "notice-command-phase.json"))
	var phase noticeProcessPhase
	if err != nil || json.Unmarshal(raw, &phase) != nil || phase.PID <= 1 || phase.TID <= 1 || phase.Code != code || !phase.Returned.After(phase.Started) || phase.Elapsed <= 0 {
		t.Fatal("supervised child phase/result was not established")
	}
	events := noticeProcessReadTrace(t, f)
	privateDir := filepath.Dir(noticePath(f, noticeHub))
	var entered, resumed time.Time
	fsyncTID := 0
	completedDelay := false
	for _, event := range events {
		line := event.detail
		if strings.Contains(line, "fsync(") && strings.Contains(line, privateDir+"/.state-") {
			if fsyncTID != 0 {
				t.Fatal("multiple candidate delayed marker syscalls; exact window is ambiguous")
			}
			fsyncTID = event.tid
			if strings.Contains(line, "<unfinished ...>") {
				entered = event.at
			}
			completedDelay = strings.Contains(line, "DELAYED") && strings.Contains(line, "= 0")
		}
		if fsyncTID != 0 && event.tid == fsyncTID && strings.Contains(line, "<... fsync resumed>") {
			if strings.Contains(line, "= ?") {
				resumed = event.at
			}
			completedDelay = completedDelay || (strings.Contains(line, "DELAYED") && strings.Contains(line, "= 0"))
		}
		if !completed && strings.Contains(line, "renameat(") && strings.Contains(line, privateDir) {
			t.Fatal("interrupted marker unexpectedly reached rename")
		}
	}
	exiting, exited, exitOK := noticeProcessTraceExit(events, phase.TID, code)
	if !exitOK || exited.Before(exiting) || exiting.Before(phase.Returned.Add(-20*time.Millisecond)) || exiting.After(phase.Returned.Add(250*time.Millisecond)) {
		t.Fatal("actual supervised child exit does not match its returned CLI phase/result")
	}
	if completed {
		if !completedDelay || fsyncTID != phase.TID || phase.Elapsed < 800*time.Millisecond || phase.Elapsed >= 3*time.Second {
			t.Fatal("completed control did not prove an actual bounded 800ms delayed private-marker fsync")
		}
		return
	}
	if entered.IsZero() || resumed.IsZero() || resumed.Before(exiting) || entered.Before(phase.Started.Add(-20*time.Millisecond)) || entered.After(phase.Returned) || !exiting.After(entered) || exiting.Sub(entered) >= 800*time.Millisecond || phase.Elapsed >= 800*time.Millisecond || completedDelay {
		t.Fatal("interrupted marker lacks correlated entry, unfinished/resumed, sub-delay child exit and timing evidence; no stall proof")
	}
}

// Validate the reached private operation, rather than treating an injection
// option or successful strace exit as evidence. Raw traces remain private
// workspace fixtures; errors intentionally do not dump their contents.
func noticeProcessTrace(t *testing.T, f *deliveryFixture) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(f.dir, "notice-syscalls.log"))
	if err != nil {
		t.Fatal("private syscall trace was not retained")
	}
	renamed, syncedTemp := false, false
	for _, line := range strings.Split(string(raw), "\n") {
		if !strings.Contains(line, filepath.Dir(noticePath(f, noticeHub))) {
			continue
		}
		if strings.Contains(line, "fsync(") && strings.Contains(line, ".state-") {
			if strings.Contains(line, "= 0") {
				syncedTemp = true
			}
		}
		if syncedTemp && strings.Contains(line, "renameat(") && strings.Contains(line, ".state-") && strings.Contains(line, `"state.json"`) && strings.Contains(line, "= 0") {
			renamed = true
		}
		if renamed && strings.Contains(line, "fsync(") && !strings.Contains(line, ".state-") && strings.Contains(line, "EIO") && strings.Contains(line, "INJECTED") {
			return
		}
	}
	t.Fatal("private supervisor did not establish the exact requested syscall window; retain trace and report this fixture limitation, not product proof")
}

// An invalid ordinary command still goes through actual CLI automation and
// exits before any hub RPC. This lets notice/ensure fixtures use hub.invalid.
var noticeProcessInvalidPeers = []string{"peers", "--task5c3-invalid"}

func noticeProcessQuietBinary(t *testing.T, f *deliveryFixture) {
	t.Helper()
	f.env["CODEX_THREAD_ID"] = bindT1
	f.env["VECNET_BIN"] = "relative" // existing preflight rejection; no child.
}

func TestCodexNoticeMarkerDeadline(t *testing.T) {
	for _, shape := range []string{"held-marker-lock", "unopened-fifo"} {
		t.Run(shape, func(t *testing.T) {
			f := newDeliveryFixture(t)
			noticeConfig(t, f)
			noticeProcessQuietBinary(t, f)
			noticeSeed(t, f, noticeHub, "node_missing")
			f.env["VECNET_CODEX_AUTO_DELIVERY"] = "0"
			baseline := noticeProcessRun(t, f, true, "", "cli", noticeProcessInvalidPeers...)
			delete(f.env, "VECNET_CODEX_AUTO_DELIVERY")
			path := noticePath(f, noticeHub)
			if shape == "held-marker-lock" {
				lock, err := deliveryPrivateFile(filepath.Join(filepath.Dir(path), "state.lock"), true)
				if err != nil {
					t.Fatal(err)
				}
				defer lock.Close()
				if ok, err := deliveryLock(lock); err != nil || !ok {
					t.Fatal("fixture could not hold marker lock")
				}
			} else {
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err := unix.Mkfifo(path, 0600); err != nil {
					t.Fatal(err)
				}
			}
			got := noticeProcessRun(t, f, true, "", "cli", noticeProcessInvalidPeers...)
			if got.code != baseline.code || got.out != baseline.out || got.err != baseline.err || got.elapsed-baseline.elapsed >= 200*time.Millisecond {
				t.Fatalf("blocked marker altered command/budget: baseline=%+v got=%+v", baseline, got)
			}
		})
	}
	t.Run("deadline-after-marker-publication", func(t *testing.T) {
		f := newDeliveryFixture(t)
		noticeConfig(t, f)
		noticeProcessQuietBinary(t, f)
		noticeSeed(t, f, noticeHub, "node_missing")
		f.env["VECNET_CODEX_AUTO_DELIVERY"] = "0"
		baseline := noticeProcessRun(t, f, true, "", "cli", noticeProcessInvalidPeers...)
		delete(f.env, "VECNET_CODEX_AUTO_DELIVERY")
		got := noticeProcessRun(t, f, true, "", "gated-cli", noticeProcessInvalidPeers...)
		if _, err := os.Stat(filepath.Join(f.dir, "notice-preflight-gated")); err != nil {
			t.Fatal("post-marker preflight gate was not reached")
		}
		if got.code != baseline.code || got.out != baseline.out || got.elapsed-baseline.elapsed >= 200*time.Millisecond {
			t.Fatalf("post-marker deadline changed command/budget: baseline=%+v got=%+v", baseline, got)
		}
		state := noticeProcessState(t, f, noticeHub)
		if got.err == baseline.err && !state.Noticed["node_missing"].IsZero() {
			t.Fatal("unprinted notice consumed its 24-hour marker at foreground deadline")
		}
		// Recovery must still announce the refusal exactly once.
		retry := noticeProcessRun(t, f, true, "", "cli", noticeProcessInvalidPeers...)
		if strings.Count(retry.err, "vecnet: Codex auto-delivery inactive") != 1 {
			t.Fatalf("deadline lost the next eligible notice: %q", retry.err)
		}
	})
	// Supplemental R36 proof; retain the executed primary subtest above
	// byte for byte while checking the complete deadline/retry/repeat cycle.
	t.Run("deadline-recovery-displays-once", func(t *testing.T) {
		f := newDeliveryFixture(t)
		noticeConfig(t, f)
		noticeProcessQuietBinary(t, f)
		noticeSeed(t, f, noticeHub, "node_missing")
		f.env["VECNET_CODEX_AUTO_DELIVERY"] = "0"
		baseline := noticeProcessRun(t, f, true, "", "cli", noticeProcessInvalidPeers...)
		delete(f.env, "VECNET_CODEX_AUTO_DELIVERY")
		first := noticeProcessRun(t, f, true, "", "gated-cli", noticeProcessInvalidPeers...)
		if _, err := os.Stat(filepath.Join(f.dir, "notice-preflight-gated")); err != nil {
			t.Fatal("deadline/recovery gate was not reached")
		}
		if first.code != baseline.code || first.out != baseline.out || first.err != baseline.err || first.elapsed-baseline.elapsed >= 200*time.Millisecond {
			t.Fatal("unrelated stalled preflight changed command result, notice output or foreground budget")
		}
		retry := noticeProcessRun(t, f, true, "", "cli", noticeProcessInvalidPeers...)
		repeat := noticeProcessRun(t, f, true, "", "cli", noticeProcessInvalidPeers...)
		line := "vecnet: Codex auto-delivery inactive for " + noticeHub + ": node_missing; run `vecnet codex status`\n"
		if retry.code != baseline.code || retry.out != baseline.out || retry.err != line+baseline.err || repeat.code != baseline.code || repeat.out != baseline.out || repeat.err != baseline.err {
			t.Fatal("deadline/retry/repeat did not preserve stdout/exit and display exactly once")
		}
		state := noticeProcessState(t, f, noticeHub)
		if state.Reason != "node_missing" || state.Noticed["node_missing"].IsZero() {
			t.Fatal("successful recovery lost truthful reason or committed debounce")
		}
	})
	t.Run("supervised-regular-marker-sync", func(t *testing.T) {
		f := newDeliveryFixture(t)
		noticeConfig(t, f)
		noticeProcessQuietBinary(t, f)
		noticeSeed(t, f, noticeHub, "node_missing")
		control := noticeProcessRun(t, f, false, "", "trace-delay-control", noticeHub, time.Now().UTC().Format(time.RFC3339Nano))
		if control.code != 0 || control.err != "" || strings.Count(control.out, "vecnet: Codex auto-delivery inactive") != 1 {
			t.Fatal("completed private marker-delay control did not publish one notice")
		}
		noticeProcessDelayTrace(t, f, control.code, true)
		// A fresh unannounced record makes the CLI case independent of the
		// completed control; its trace and phase were already archived.
		noticeSeed(t, f, noticeHub, "node_missing")
		f.env["VECNET_CODEX_AUTO_DELIVERY"] = "0"
		baseline := noticeProcessRun(t, f, true, "", "trace-baseline-cli", noticeProcessInvalidPeers...)
		baselineDuration := noticeProcessDuration(t, f)
		delete(f.env, "VECNET_CODEX_AUTO_DELIVERY")
		got := noticeProcessRun(t, f, true, "", "trace-delay-cli", noticeProcessInvalidPeers...)
		elapsed := noticeProcessDuration(t, f)
		noticeProcessDelayTrace(t, f, got.code, false)
		if got.code != baseline.code || got.out != baseline.out || got.err != baseline.err || elapsed-baselineDuration >= 200*time.Millisecond {
			t.Fatalf("stalled marker sync changed foreground result/budget: duration=%s baseline=%s result=%+v baseline-result=%+v", elapsed, baselineDuration, got, baseline)
		}
		// The process must exit before the stalled worker can publish late.
		if !noticeProcessState(t, f, noticeHub).Noticed["node_missing"].IsZero() {
			t.Fatal("blocked marker sync published after foreground exit")
		}
		retry := noticeProcessRun(t, f, true, "", "cli", noticeProcessInvalidPeers...)
		if strings.Count(retry.err, "vecnet: Codex auto-delivery inactive") != 1 {
			t.Fatal("stalled publication consumed the next eligible notice")
		}
	})
}

func TestCodexNoticeWriteFailures(t *testing.T) {
	t.Run("kernel-write-failure-before-publication", func(t *testing.T) {
		f := newDeliveryFixture(t)
		noticeConfig(t, f)
		noticeProcessQuietBinary(t, f)
		noticeSeed(t, f, noticeHub, "assets_missing")
		path := noticePath(f, noticeHub)
		before, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		f.env["VECNET_CODEX_AUTO_DELIVERY"] = "0"
		baseline := noticeProcessRun(t, f, true, "", "cli", noticeProcessInvalidPeers...)
		delete(f.env, "VECNET_CODEX_AUTO_DELIVERY")
		got := noticeProcessRun(t, f, true, "", "limited-cli", noticeProcessInvalidPeers...)
		if got.code != baseline.code || got.out != baseline.out || got.err != baseline.err || got.elapsed-baseline.elapsed >= 200*time.Millisecond {
			t.Fatalf("write failure changed command/budget: baseline=%+v got=%+v", baseline, got)
		}
		after, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(before, after) {
			t.Fatal("failed publication changed the previously committed reason/history")
		}
		pending, err := filepath.Glob(filepath.Join(filepath.Dir(path), ".state-*"))
		if err != nil || len(pending) != 0 {
			t.Fatal("failed publication left temporary metadata")
		}
		status := noticeProcessRun(t, f, false, "", "cli", "codex", "status")
		if status.code != 0 || status.err != "" || !strings.Contains(status.out, "assets_missing") {
			t.Fatalf("status lost last committed reason: %+v", status)
		}
		retry := noticeProcessRun(t, f, true, "", "cli", noticeProcessInvalidPeers...)
		if strings.Count(retry.err, "vecnet: Codex auto-delivery inactive") != 1 {
			t.Fatalf("failed write consumed an unpublished notice: %q", retry.err)
		}
		repeat := noticeProcessRun(t, f, true, "", "cli", noticeProcessInvalidPeers...)
		if repeat.err != baseline.err {
			t.Fatalf("successful retry failed to debounce: %q", repeat.err)
		}
	})
	t.Run("refusal-write-keeps-last-committed-reason", func(t *testing.T) {
		f := newDeliveryFixture(t)
		noticeConfig(t, f)
		noticeSeed(t, f, noticeHub, "assets_missing")
		if err := os.Remove(filepath.Join(f.dir, "bin/node")); err != nil {
			t.Fatal(err)
		}
		before, err := os.ReadFile(noticePath(f, noticeHub))
		if err != nil {
			t.Fatal(err)
		}
		got := noticeProcessRun(t, f, false, "", "limited-ensure", noticeHub, f.config, f.bindings)
		if got.code != 1 || got.out != "" || got.err != errCodexDelivery.Error()+"\n" {
			t.Fatalf("failed refusal publication changed fallback output: %+v", got)
		}
		after, err := os.ReadFile(noticePath(f, noticeHub))
		if err != nil || !bytes.Equal(before, after) {
			t.Fatal("failed reason write damaged the previous committed record")
		}
		// Storage failure cannot make the new reason durable. Status must
		// expose the last committed value; it must not assert activation.
		status := noticeProcessRun(t, f, false, "", "cli", "codex", "status")
		if status.code != 0 || status.err != "" || !strings.Contains(status.out, "assets_missing") || strings.Contains(status.out, "node_missing") {
			t.Fatalf("status invented an uncommitted reason: %+v", status)
		}
		retry := noticeProcessRun(t, f, false, "", "ensure", noticeHub, f.config, f.bindings)
		if retry.code != 1 || noticeProcessState(t, f, noticeHub).Reason != "node_missing" {
			t.Fatal("reason did not recover when the write fault was removed")
		}
	})
	t.Run("after-rename-directory-sync", func(t *testing.T) {
		f := newDeliveryFixture(t)
		noticeConfig(t, f)
		noticeSeed(t, f, noticeHub, "assets_missing")
		if err := os.Remove(filepath.Join(f.dir, "bin/node")); err != nil {
			t.Fatal(err)
		}
		got := noticeProcessRun(t, f, false, "", "trace-fail-ensure", noticeHub, f.config, f.bindings)
		noticeProcessTrace(t, f)
		if got.code != 1 || got.out != "" || got.err != errCodexDelivery.Error()+"\n" || noticeProcessState(t, f, noticeHub).Reason != "node_missing" {
			t.Fatal("after-rename refusal did not expose the actually published reason safely")
		}
		status := noticeProcessRun(t, f, false, "", "cli", "codex", "status")
		if status.code != 0 || status.err != "" || !strings.Contains(status.out, "node_missing") || strings.Contains(status.out, "assets_missing") {
			t.Fatal("public status did not reflect the record visible after rename")
		}
		now := time.Now().UTC()
		marker := noticeProcessRun(t, f, false, "", "trace-fail-notice", noticeHub, now.Format(time.RFC3339Nano))
		noticeProcessTrace(t, f)
		if marker.code != 0 || marker.out != "" || marker.err != "" || !noticeProcessState(t, f, noticeHub).Noticed["node_missing"].Equal(now) {
			t.Fatal("failed directory sync did not preserve the actual visible marker/output contract")
		}
		// A successful rename can expose a marker even when directory fsync
		// fails. Do not invent rollback: inspect it and check actual debounce.
		retry := noticeProcessRun(t, f, false, "", "notice-at", noticeHub, now.Add(time.Second).Format(time.RFC3339Nano))
		if retry.code != 0 || retry.out != "" || retry.err != "" {
			t.Fatal("retry ignored the marker already visible after rename")
		}
	})
}

func TestCodexNoticeForegroundPipes(t *testing.T) {
	for _, mode := range []string{"pipes", "interactive", "hook"} {
		t.Run(mode, func(t *testing.T) {
			bf := autoFixture(t)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/v1/peers":
					_, _ = io.WriteString(w, `{"peers":[{"peer_id":"peer_fixture","kind":"agent","name":"Fixture","presence":"online","reachability":"yes"}]}`)
				case "/v1/peers/me":
					_, _ = io.WriteString(w, `{"kind":"peer","peer_id":"peer_A"}`)
				default:
					http.NotFound(w, r)
				}
			}))
			t.Cleanup(server.Close)
			bf.hub = server.URL
			bf.config(bf.hub)
			f := &deliveryFixture{t: t, dir: bf.env["HOME"], env: bf.env}
			noticeSeed(t, f, bf.hub, "node_missing")
			// The fake helper remains alive and writes late to both descriptors.
			// Its own file marker proves it actually started and completed.
			deliveryWrite(t, f.env["VECNET_BIN"], "#!/bin/sh\nprintf started > \"$HOME/notice-linger-started\"\n/bin/sleep 2\nprintf private-body-text\nprintf private-error-text >&2\nprintf done > \"$HOME/notice-linger-done\"\n", 0700)
			t.Cleanup(func() {
				if _, err := os.Stat(filepath.Join(f.dir, "notice-linger-started")); err == nil {
					deliveryWait(t, func() bool {
						_, err := os.Stat(filepath.Join(f.dir, "notice-linger-done"))
						return err == nil
					})
				}
			})
			args, input := []string{"peers"}, ""
			if mode == "hook" {
				args, input = []string{"hook", "--event", "post-tool"}, `{"hook_event_name":"PostToolUse"}`
			}
			f.env["VECNET_CODEX_AUTO_DELIVERY"] = "0"
			baseline := noticeProcessRun(t, f, mode == "interactive", input, "cli", args...)
			delete(f.env, "VECNET_CODEX_AUTO_DELIVERY")
			got := noticeProcessRun(t, f, mode == "interactive", input, "cli", args...)
			deliveryWait(t, func() bool {
				_, err := os.Stat(filepath.Join(f.dir, "notice-linger-started"))
				return err == nil
			})
			if got.code != baseline.code || got.out != baseline.out || (mode != "hook" && got.out == "") {
				t.Fatalf("helper changed stdout/exit: baseline=%+v got=%+v", baseline, got)
			}
			wantErr := baseline.err
			if mode == "interactive" {
				wantErr = "vecnet: Codex auto-delivery inactive for " + normalizeHub(bf.hub) + ": node_missing; run `vecnet codex status`\n" + wantErr
			}
			if got.err != wantErr || (mode == "hook" && (got.out != "" || got.err != "")) {
				t.Fatalf("unexpected notice/hook output: %+v", got)
			}
			if mode == "interactive" {
				// Suppress a second helper without changing notice eligibility.
				f.env["VECNET_BIN"] = "relative"
				repeat := noticeProcessRun(t, f, true, "", "cli", args...)
				if repeat.code != baseline.code || repeat.out != baseline.out || repeat.err != baseline.err {
					t.Fatalf("second interactive invocation failed debounce: %+v", repeat)
				}
			}
		})
	}
}

func TestCodexNoticeClearReasonCycle(t *testing.T) {
	f := newDeliveryFixture(t)
	noticeConfig(t, f)
	f.env["CODEX_THREAD_ID"] = bindT1
	binary := f.env["VECNET_BIN"]
	node := filepath.Join(f.dir, "bin/node")
	nodeBytes, err := os.ReadFile(node)
	if err != nil {
		t.Fatal(err)
	}
	ensure := func(wantCode int, wantReason codexInactiveReason) {
		t.Helper()
		f.env["VECNET_BIN"] = binary
		got := noticeProcessRun(t, f, false, "", "ensure", noticeHub, f.config, f.bindings)
		if got.code != wantCode || noticeProcessState(t, f, noticeHub).Reason != wantReason {
			t.Fatalf("ensure result/reason mismatch: %+v, want %q", got, wantReason)
		}
		f.env["VECNET_BIN"] = "relative"
	}
	status := func(want string) {
		t.Helper()
		got := noticeProcessRun(t, f, false, "", "cli", "codex", "status")
		if got.code != 0 || got.err != "" || !strings.Contains(got.out, want) {
			t.Fatalf("cycle status want %q: %+v", want, got)
		}
	}
	if err := os.Remove(node); err != nil {
		t.Fatal(err)
	}
	ensure(1, "node_missing") // actual refusal, not a seeded reason.
	status("node_missing")
	first := noticeProcessRun(t, f, true, "", "cli", noticeProcessInvalidPeers...)
	if strings.Count(first.err, "vecnet: Codex auto-delivery inactive") != 1 {
		t.Fatalf("initial refusal did not announce: %q", first.err)
	}
	announced := noticeProcessState(t, f, noticeHub).Noticed["node_missing"]
	if announced.IsZero() {
		t.Fatal("initial notice was not committed")
	}
	deliveryWrite(t, node, string(nodeBytes), 0700)
	ensure(0, "") // real detached owner must verify before clearing.
	pid, meta := f.owner()
	var owner deliveryOwner
	if deliveryRead(meta, &owner) != nil || owner.PID != pid || !deliveryOwnerRunning(owner) {
		t.Fatal("clear was not backed by a verified live fixture owner")
	}
	status("unknown (no trusted inactive reason recorded)")
	cleared := noticeProcessState(t, f, noticeHub)
	if !cleared.Noticed["node_missing"].Equal(announced) {
		t.Fatal("verified activation cleared the debounce history")
	}
	if err := os.Remove(node); err != nil {
		t.Fatal(err)
	}
	ensure(1, "node_missing")
	status("node_missing")
	again := noticeProcessRun(t, f, true, "", "cli", noticeProcessInvalidPeers...)
	if strings.Contains(again.err, "Codex auto-delivery inactive") || !noticeProcessState(t, f, noticeHub).Noticed["node_missing"].Equal(announced) {
		t.Fatal("same refusal after verified clear bypassed its 24-hour history")
	}
	deliveryWrite(t, node, "#!/bin/sh\necho v20.1.0\nprintf private-error-text >&2\n", 0700)
	ensure(1, "node_too_old")
	status("node_too_old")
	changed := noticeProcessRun(t, f, true, "", "cli", noticeProcessInvalidPeers...)
	if strings.Count(changed.err, "vecnet: Codex auto-delivery inactive") != 1 || !strings.Contains(changed.err, "node_too_old") {
		t.Fatal("changed refusal did not get its own notice")
	}
	if err := os.Remove(node); err != nil {
		t.Fatal(err)
	}
	ensure(1, "node_missing")
	// Exact-clock controls call the real notice boundary in separate processes;
	// the public CLI uses time.Now, so these do not claim a fake CLI wall clock.
	for _, step := range []struct {
		delta time.Duration
		want  bool
	}{{24*time.Hour - time.Nanosecond, false}, {24 * time.Hour, true}, {24 * time.Hour, false}} {
		got := noticeProcessRun(t, f, false, "", "notice-at", noticeHub, announced.Add(step.delta).Format(time.RFC3339Nano))
		if got.code != 0 || got.err != "" || (strings.Contains(got.out, "node_missing") != step.want) {
			t.Fatalf("expiry delta=%s want notice=%v: %+v", step.delta, step.want, got)
		}
	}
}
