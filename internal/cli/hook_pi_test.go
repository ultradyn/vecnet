package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
)

// piStdin is the installed pi extension's payload (hook_pi.go). pid
// defaults to this process's parent, which never matches a test's own
// runs, so in-process runs carry no process identity.
func piStdin(event, session, mode string, pid int) string {
	return fmt.Sprintf(`{"event":%q,"session_id":%q,"session_file":"/home/u/.pi/agent/sessions/x/%s.jsonl","pid":%d,"mode":%q}`+"\n", event, session, session, pid, mode)
}

// runPiHook runs `vecnet hook --client pi` in-process with a payload.
func runPiHook(t *testing.T, env map[string]string, stdin string, args ...string) (string, string) {
	t.Helper()
	var out, errs bytes.Buffer
	code := RunContext(context.Background(), append([]string{"hook", "--client", "pi"}, args...), strings.NewReader(stdin), &out, &errs, func(k string) string { return env[k] })
	if code != 0 {
		t.Fatalf("pi hook exit %d, stderr=%q", code, errs.String())
	}
	return out.String(), errs.String()
}

// piContextOf parses the hook's output: exactly one JSON object with exactly
// one additionalContext string, on one line.
func piContextOf(t *testing.T, out string) string {
	t.Helper()
	var top map[string]json.RawMessage
	if err := json.Unmarshal([]byte(out), &top); err != nil || len(top) != 1 || top["additionalContext"] == nil || strings.Count(out, "\n") != 1 {
		t.Fatalf("pi output shape=%q err=%v", out, err)
	}
	var s string
	if err := json.Unmarshal(top["additionalContext"], &s); err != nil {
		t.Fatalf("pi additionalContext=%q", out)
	}
	return s
}

const piSess = "019a6f2e-1c2d-7e3f-8a4b-5c6d7e8f9a0b"

// The first prompt from an interactive session binds the profile and
// delivers in a single additionalContext object.
func TestHookPiPromptBindsAndDelivers(t *testing.T) {
	for _, mode := range []string{"tui", "rpc"} {
		t.Run(mode, func(t *testing.T) {
			env, path, polls := agyHub(t)
			seedSession(t, path, func(*hookState) {})
			out, errs := runPiHook(t, env, piStdin("user-prompt", piSess, mode, 0), "--profile", "p")
			ctx := piContextOf(t, out)
			for _, want := range []string{`<vecnet-inbox untrusted="true">`, "turn one", "vecnet: 1 new message(s) for profile p.", "vecnet --net p reply"} {
				if !strings.Contains(ctx, want) {
					t.Fatalf("context lacks %q: %q", want, ctx)
				}
			}
			if !strings.Contains(errs, "bound to this pi session") {
				t.Fatalf("binding stderr=%q", errs)
			}
			s := readHookState(t, path)
			if s.Session != piSess || !reflect.DeepEqual(s.Shown, []string{"dly_1"}) || s.PollCursor != "e1.1" || polls.Load() != 1 {
				t.Fatalf("state=%+v polls=%d", s, polls.Load())
			}
		})
	}
}

// A post-tool run from the bound session delivers under the 5-second rate
// limit; a prompt is never rate limited.
func TestHookPiPostToolRateLimited(t *testing.T) {
	now := time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)
	old := hookNow
	hookNow = func() time.Time { return now }
	t.Cleanup(func() { hookNow = old })
	env, path, polls := agyHub(t)
	seedSession(t, path, func(*hookState) {})
	runPiHook(t, env, piStdin("user-prompt", piSess, "tui", 0), "--profile", "p")
	now = now.Add(6 * time.Second)
	out, _ := runPiHook(t, env, piStdin("post-tool", piSess, "tui", 0), "--profile", "p")
	if ctx := piContextOf(t, out); !strings.Contains(ctx, "turn two") {
		t.Fatalf("post-tool missed delivery: %q", ctx)
	}
	out, errs := runPiHook(t, env, piStdin("post-tool", piSess, "tui", 0), "--profile", "p")
	if out != "" || errs != "" || polls.Load() != 2 {
		t.Fatalf("rate-limited post-tool out=%q errs=%q polls=%d", out, errs, polls.Load())
	}
	out, _ = runPiHook(t, env, piStdin("user-prompt", piSess, "tui", 0), "--profile", "p")
	if out != "" || polls.Load() != 3 {
		t.Fatalf("prompt after rate limit: out=%q polls=%d", out, polls.Load())
	}
}

// Subagents and non-interactive runs never bind, claim, poll or write:
// in-process pi-subagents sessions run in print mode, `pi --mode json -p`
// children (pi's example subagent) in json mode; a payload with no session
// id, an unknown event, a --event that contradicts the payload, and
// unusable stdin are silent too.
func TestHookPiNonInteractiveSilent(t *testing.T) {
	for _, tc := range []struct {
		name, stdin string
		args        []string
	}{
		{"print subagent", piStdin("user-prompt", "c2-sub", "print", 0), nil},
		{"json child", piStdin("user-prompt", "c3-json", "json", 0), nil},
		{"no mode", `{"event":"user-prompt","session_id":"c4","pid":0}`, nil},
		{"no session", piStdin("user-prompt", "", "tui", 0), nil},
		{"unknown event", piStdin("session-start", piSess, "tui", 0), nil},
		{"event mismatch", piStdin("user-prompt", piSess, "tui", 0), []string{"--event", "post-tool"}},
		{"not json", "user-prompt\n", nil},
		{"empty", "", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env, path, polls := agyHub(t)
			out, errs := runPiHook(t, env, tc.stdin, append([]string{"--profile", "p"}, tc.args...)...)
			if out != "" || errs != "" || polls.Load() != 0 {
				t.Fatalf("out=%q errs=%q polls=%d", out, errs, polls.Load())
			}
			for _, file := range []string{path, bindingPath(path)} {
				if _, err := os.Stat(file); !os.IsNotExist(err) {
					t.Fatalf("touched %s: %v", file, err)
				}
			}
		})
	}
}

// A bound profile's post-tool runs from a subagent session in the same pi
// process stay silent, and a print-mode prompt cannot take the binding.
func TestHookPiSubagentNeverTakesBinding(t *testing.T) {
	env, path, polls := agyHub(t)
	seedSession(t, path, boundTo("c1-root"))
	before, _ := os.ReadFile(bindingPath(path))
	for _, stdin := range []string{piStdin("user-prompt", "c2-sub", "print", 0), piStdin("post-tool", "c2-sub", "tui", 0)} {
		out, errs := runPiHook(t, env, stdin, "--profile", "p")
		if out != "" || errs != "" {
			t.Fatalf("subagent out=%q errs=%q", out, errs)
		}
	}
	if after, _ := os.ReadFile(bindingPath(path)); !bytes.Equal(before, after) || polls.Load() != 0 {
		t.Fatalf("subagent changed the binding or polled %d", polls.Load())
	}
}

// A prompt from another session gets a once-only pi-worded notice and no
// content; the binding stays.
func TestHookPiForeignNotice(t *testing.T) {
	env, path, polls := agyHub(t)
	seedSession(t, path, boundTo("c1-root"))
	out, errs := runPiHook(t, noVecnetProfile(env), piStdin("user-prompt", "c9-other", "tui", 0), "--profile", "p")
	ctx := piContextOf(t, out)
	if !strings.Contains(ctx, "belongs to another pi session") || strings.Contains(ctx, "Claude") || strings.Contains(ctx, "turn one") {
		t.Fatalf("notice=%q", ctx)
	}
	if !strings.Contains(errs, "bound to another pi session") {
		t.Fatalf("stderr=%q", errs)
	}
	out, _ = runPiHook(t, noVecnetProfile(env), piStdin("user-prompt", "c9-other", "tui", 0), "--profile", "p")
	if out != "" || readHookState(t, path).Session != "c1-root" || polls.Load() != 0 {
		t.Fatalf("second notice out=%q polls=%d", out, polls.Load())
	}
}

// The hook never registers a Claude root.
func TestHookPiNeverRegistersClaude(t *testing.T) {
	old := hookClaudeRegister
	called := false
	hookClaudeRegister = func(*app, context.Context, string, []byte) localSetupResult { called = true; return localSetupResult{} }
	t.Cleanup(func() { hookClaudeRegister = old })
	env, path, _ := agyHub(t)
	seedSession(t, path, func(*hookState) {})
	runPiHook(t, env, piStdin("user-prompt", piSess, "tui", 0), "--profile", "p")
	if called {
		t.Fatal("pi hook registered a Claude root")
	}
}

// A payload pid that is not this process's parent (or, on Linux, its
// grandparent) gives no process identity.
func TestHookPiProcNeedsMatchingPID(t *testing.T) {
	if p := piHookProc(0); p.PID != 0 {
		t.Fatalf("pid 0 proc=%+v", p)
	}
	if p := piHookProc(1); p.PID != 0 {
		t.Fatalf("pid 1 proc=%+v", p)
	}
	if p := piHookProc(os.Getpid()); p.PID != 0 {
		t.Fatalf("own pid proc=%+v", p)
	}
	if p := piHookProc(os.Getppid()); p.PID != os.Getppid() {
		t.Fatalf("parent pid proc=%+v", p)
	}
}

// TestHookPiProcess is the child of piRun: it runs one hook as a process
// whose parent is this test binary, which stands in for pi.
func TestHookPiProcess(t *testing.T) {
	if os.Getenv("HH_PI_CHILD") == "" {
		return
	}
	os.Exit(RunContext(context.Background(), append([]string{"hook", "--client", "pi"}, strings.Fields(os.Getenv("HH_PI_ARGS"))...), strings.NewReader(os.Getenv("HH_PI_STDIN")), os.Stdout, os.Stderr, os.Getenv))
}

// piRun spawns the hook directly (no shell), as the pi extension does.
func piRun(t *testing.T, env map[string]string, stdin, args string) (string, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestHookPiProcess$")
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(key, "CODEX_") || strings.HasPrefix(key, "CLAUDE_") || strings.HasPrefix(key, "VECNET_") || strings.HasPrefix(key, "PI_") || strings.HasPrefix(key, "HH_") || key == "GORACE" {
			continue
		}
		cmd.Env = append(cmd.Env, entry)
	}
	cmd.Env = append(cmd.Env, "HH_PI_CHILD=1", "HH_PI_ARGS="+args, "HH_PI_STDIN="+stdin, "GORACE="+os.Getenv("GORACE")+" atexit_sleep_ms=0")
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	var out, errs bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errs
	if err := cmd.Run(); err != nil {
		t.Fatalf("pi hook child: %v, stderr=%q", err, errs.String())
	}
	return out.String(), errs.String()
}

// /new, /resume and /fork start a new session id inside the same pi
// process: the binding follows the process (admitCleared). A print-mode
// subagent in that process stays silent and leaves the binding alone.
func TestHookPiNewSessionFollowsProcess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the hook child's start time is not compared on Windows test hosts")
	}
	env, path, _ := agyHub(t)
	seedSession(t, path, func(*hookState) {})
	me := os.Getpid()
	out, errs := piRun(t, env, piStdin("user-prompt", piSess, "tui", me), "--profile p")
	if ctx := piContextOf(t, out); !strings.Contains(ctx, "turn one") {
		t.Fatalf("binding run missed delivery: %q", ctx)
	}
	if s := readHookState(t, path); s.Session != piSess || s.Proc.PID != me {
		t.Fatalf("after binding: %+v (errs=%q)", s, errs)
	}
	out, errs = piRun(t, env, piStdin("user-prompt", "c2-sub", "print", me), "--profile p")
	if out != "" || errs != "" || readHookState(t, path).Session != piSess {
		t.Fatalf("subagent out=%q errs=%q", out, errs)
	}
	next := "019a6f2e-ffff-7e3f-8a4b-5c6d7e8f9a0b"
	out, errs = piRun(t, env, piStdin("user-prompt", next, "tui", me), "--profile p")
	if ctx := piContextOf(t, out); !strings.Contains(ctx, "turn two") {
		t.Fatalf("new-session run missed delivery: %q", ctx)
	}
	if !strings.Contains(errs, "follows this pi process to its new session id") {
		t.Fatalf("new-session stderr=%q", errs)
	}
	if s := readHookState(t, path); s.Session != next || !reflect.DeepEqual(s.Shown, []string{"dly_1", "dly_2"}) {
		t.Fatalf("after /new: %+v", s)
	}
}

// A payload naming the bound pi's pid from a process that is not its child
// or grandchild (an unrelated terminal) gets no process identity, so it
// cannot follow the binding as a /new would. (A command pi's own bash tool
// runs is pi's descendant and can pass: see piHookProc.)
func TestHookPiUnrelatedPIDNoTakeover(t *testing.T) {
	env, path, polls := agyHub(t)
	seedSession(t, path, func(s *hookState) {
		boundTo("c1-root")(s)
		s.Proc = hookProc{PID: os.Getpid(), Start: procStart(os.Getpid()), Host: func() string { h, _ := os.Hostname(); return h }()}
	})
	// In-process: the hook's parent is the test runner, not os.Getpid().
	out, _ := runPiHook(t, noVecnetProfile(env), piStdin("user-prompt", "c9-forged", "tui", os.Getpid()), "--profile", "p")
	if ctx := piContextOf(t, out); !strings.Contains(ctx, "belongs to another pi session") {
		t.Fatalf("forged run=%q", ctx)
	}
	if s := readHookState(t, path); s.Session != "c1-root" || polls.Load() != 0 {
		t.Fatalf("forged run took binding: %+v", s)
	}
}

// pi's output encoder keeps inert's escapes as-is.
func TestHookPiOutputShape(t *testing.T) {
	got := string(piHookOutput(`a &lt;b&gt; "c"`))
	if got != `{"additionalContext":"a &lt;b&gt; \"c\""}`+"\n" {
		t.Fatalf("got %q", got)
	}
}

// `hook rebind` from a pi bash tool (PI_SESSION_ID set) never binds
// directly: it opens a window only that session's interactive hook run may
// take, so a subagent's or print session's rebind cannot strand the
// profile, and the taking run records pi's process.
func TestHookPiRebindRestrictedToSession(t *testing.T) {
	env, path, polls := agyHub(t)
	noVecnetProfile(env)
	seedSession(t, path, func(s *hookState) { boundTo("c1-root")(s); s.PollCursor = "e1.0" })
	env["PI_SESSION_ID"] = "c2-target"
	code, out, errs := runCLI(env, "hook", "rebind", "--profile", "p")
	if code != 0 || !strings.Contains(out, "will bind to this pi session (c2-target) at its next hook run") {
		t.Fatalf("exit=%d out=%q errs=%q", code, out, errs)
	}
	if s := readHookState(t, path); s.Session != "c1-root" || s.RebindFor != "c2-target" || s.RebindUntil.IsZero() {
		t.Fatalf("pending state %+v", s)
	}
	delete(env, "PI_SESSION_ID")
	// Another session — even an interactive one — cannot take the window.
	out, _ = runPiHook(t, env, piStdin("user-prompt", "c3-other", "tui", 0), "--profile", "p")
	if ctx := piContextOf(t, out); !strings.Contains(ctx, "belongs to another pi session") {
		t.Fatalf("other session took window: %q", ctx)
	}
	// The named session, if not interactive, cannot either.
	out, _ = runPiHook(t, env, piStdin("post-tool", "c2-target", "print", 0), "--profile", "p")
	if out != "" || readHookState(t, path).Session != "c1-root" || polls.Load() != 0 {
		t.Fatalf("print run took window: %q", out)
	}
	// Its interactive post-tool run takes it and claims at once.
	out, errs = piRun(t, env, piStdin("post-tool", "c2-target", "tui", os.Getpid()), "--profile p")
	if ctx := piContextOf(t, out); !strings.Contains(ctx, "turn one") || !strings.Contains(errs, "rebound to this pi session") {
		t.Fatalf("target run out=%q errs=%q", out, errs)
	}
	if s := readHookState(t, path); s.Session != "c2-target" || s.RebindFor != "" || !s.RebindUntil.IsZero() || s.Proc.PID != os.Getpid() {
		t.Fatalf("after taking: %+v", s)
	}
}
