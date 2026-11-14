package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/envelope"
)

// sessionStdin is Claude Code's hook input for one session.
func sessionStdin(session string) string {
	if session == "" {
		return `{"hook_event_name":"UserPromptSubmit"}`
	}
	return `{"hook_event_name":"UserPromptSubmit","session_id":"` + session + `"}`
}

// runHookAs runs the user-prompt hook for profile as Claude Code session
// session would, returning stdout and stderr.
func runHookAs(t *testing.T, env map[string]string, session, profile string) (string, string) {
	t.Helper()
	var out, errs bytes.Buffer
	code := RunContext(context.Background(), []string{"hook", "--profile", profile, "--event", "user-prompt"}, strings.NewReader(sessionStdin(session)), &out, &errs, func(k string) string { return env[k] })
	if code != 0 {
		t.Fatalf("hook exit %d, stderr=%q", code, errs.String())
	}
	return out.String(), errs.String()
}

// sessionHub serves one delivery ("secret text") after e1.0 and counts
// event polls.
func sessionHub(t *testing.T) (map[string]string, string, *atomic.Int32) {
	t.Helper()
	var polls atomic.Int32
	env, path := hookFixture(t, func(w http.ResponseWriter, r *http.Request) {
		polls.Add(1)
		switch r.URL.Query().Get("after") {
		case "e1.0":
			hookPage(w, []envelope.Event{hookEvent(1, "secret text")}, "e1.1")
		case "head":
			hookPage(w, nil, "e1.0")
		default:
			hookPage(w, nil, "e1.1")
		}
	})
	return env, path, &polls
}

func seedSession(t *testing.T, path string, fn func(*hookState)) {
	t.Helper()
	if err := (stateFile{path}).update(context.Background(), func(s *hookState) error {
		if s.PollCursor == "" {
			s.PollCursor = "e1.0"
		}
		fn(s)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
func boundTo(session string) func(*hookState) {
	return func(s *hookState) {
		s.Session, s.SessionAt, s.SessionHow = session, time.Date(2026, 11, 4, 9, 30, 0, 0, time.UTC), bindAuto
	}
}
func noVecnetProfile(env map[string]string) map[string]string {
	delete(env, "VECNET_PROFILE")
	return env
}

func TestHookSessionNormalised(t *testing.T) {
	if hookSession("") != "" || hookSession("8d0c1e2a-0b1c-4d5e-9f00-123456789abc") != "8d0c1e2a-0b1c-4d5e-9f00-123456789abc" {
		t.Fatal("plain ids must be kept")
	}
	if got := hookSession("a b\n<x>"); !strings.HasPrefix(got, "x-") || len(got) != 18 {
		t.Fatalf("odd id stored as %q", got)
	}
}

// The first session to run an unbound profile's hook binds it; that run
// claims nothing (its other hooks may still reveal it as ambiguous), and
// the next run shows the delivery.
func TestHookSessionFirstRunBinds(t *testing.T) {
	env, path, _ := sessionHub(t)
	noVecnetProfile(env)
	seedSession(t, path, func(*hookState) {})
	out, errs := runHookAs(t, env, "S1", "p")
	if out != "" || !strings.Contains(errs, "bound to this Claude Code session") {
		t.Fatalf("binding run out=%q errs=%q", out, errs)
	}
	s := readHookState(t, path)
	if s.Session != "S1" || s.SessionHow != bindAuto || len(s.Shown) != 0 || s.PollCursor != "e1.0" {
		t.Fatalf("after binding: %+v", s)
	}
	out, _ = runHookAs(t, env, "S1", "p")
	if !strings.Contains(contextOf(t, out), "secret text") {
		t.Fatalf("bound session missed its delivery: %q", out)
	}
}

// A run from another session claims nothing, marks nothing shown, makes no
// poll and tells that session once, without message content.
func TestHookForeignSessionClaimsNothing(t *testing.T) {
	env, path, polls := sessionHub(t)
	noVecnetProfile(env)
	seedSession(t, path, boundTo("S1"))
	before := readHookState(t, path)
	out, errs := runHookAs(t, env, "S2", "p")
	ctx := contextOf(t, out)
	for _, want := range []string{"profile p's hook belongs to another Claude Code session (bound 2026-11-04 09:30 UTC)", "If this session is profile p, run: vecnet hook rebind --profile p.", "Otherwise ignore this."} {
		if !strings.Contains(ctx, want) {
			t.Fatalf("notice lacks %q: %q", want, ctx)
		}
	}
	if strings.Contains(ctx, "secret") || strings.Contains(ctx, "vecnet-inbox") || !strings.Contains(errs, "another Claude Code session") {
		t.Fatalf("notice=%q errs=%q", ctx, errs)
	}
	after := readHookState(t, path)
	if polls.Load() != 0 {
		t.Fatalf("foreign run polled the hub %d times", polls.Load())
	}
	want := before
	want.Noticed = []string{"S2"}
	if !reflect.DeepEqual(after, want) {
		t.Fatalf("foreign run changed state:\n got %+v\nwant %+v", after, want)
	}
	raw, _ := os.ReadFile(path)
	out, errs = runHookAs(t, env, "S2", "p")
	if again, _ := os.ReadFile(path); out != "" || !bytes.Equal(raw, again) || !strings.Contains(errs, "another Claude Code session") {
		t.Fatalf("second foreign run out=%q errs=%q changed=%t", out, errs, !bytes.Equal(raw, again))
	}
	out, _ = runHookAs(t, env, "S1", "p")
	if !strings.Contains(contextOf(t, out), "secret text") {
		t.Fatalf("owner missed delivery: %q", out)
	}
}

// A notice whose write fails is not recorded, so it is shown next time.
func TestHookForeignNoticeRepeatsAfterFailedWrite(t *testing.T) {
	env, path, _ := sessionHub(t)
	noVecnetProfile(env)
	seedSession(t, path, boundTo("S1"))
	code := RunContext(context.Background(), []string{"hook", "--profile", "p", "--event", "user-prompt"}, strings.NewReader(sessionStdin("S2")), failWriter{}, &bytes.Buffer{}, func(k string) string { return env[k] })
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	if s := readHookState(t, path); len(s.Noticed) != 0 {
		t.Fatalf("unwritten notice recorded: %+v", s)
	}
	if out, _ := runHookAs(t, env, "S2", "p"); !strings.Contains(contextOf(t, out), "belongs to another") {
		t.Fatalf("notice not repeated: %q", out)
	}
}

type failWriter struct{}

func (failWriter) Write([]byte) (int, error) { return 0, errors.New("closed") }

// Of concurrent first runs from different sessions, exactly one binds.
func TestHookSessionFirstClaimRace(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "p.json")
	f := stateFile{path}
	const n = 12
	results := make([]hookAdmit, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if err := f.update(ctx, func(s *hookState) error {
				results[i] = s.admit(hookOwner{session: "S" + strconv.Itoa(i), profiles: []string{"p"}, prompt: true}, time.Now())
				return nil
			}); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	bound := 0
	for i, r := range results {
		switch r {
		case admitBound:
			bound++
			if s := readHookState(t, path); s.Session != "S"+strconv.Itoa(i) {
				t.Fatalf("winner S%d but state bound to %q", i, s.Session)
			}
		case admitForeign:
		default:
			t.Fatalf("run %d: outcome %d", i, r)
		}
	}
	if bound != 1 {
		t.Fatalf("%d runs bound the state: %v", bound, results)
	}
}

// The same race through the real hook: one session binds, no run shows
// the delivery, and every other run either noticed or found the lock busy.
func TestHookSessionFirstClaimRaceEndToEnd(t *testing.T) {
	env, path, _ := sessionHub(t)
	noVecnetProfile(env)
	seedSession(t, path, func(*hookState) {})
	const n = 6
	outs := make([]string, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var out, errs bytes.Buffer
			RunContext(context.Background(), []string{"hook", "--profile", "p", "--event", "user-prompt"}, strings.NewReader(sessionStdin("S"+strconv.Itoa(i))), &out, &errs, func(k string) string { return env[k] })
			outs[i] = out.String()
		}()
	}
	wg.Wait()
	s := readHookState(t, path)
	if s.Session == "" || len(s.Shown) != 0 {
		t.Fatalf("after race: %+v", s)
	}
	for i, out := range outs {
		if strings.Contains(out, "secret text") {
			t.Fatalf("run %d showed content during the race: %q", i, out)
		}
		if out != "" && "S"+strconv.Itoa(i) == s.Session {
			t.Fatalf("the binding run printed %q", out)
		}
	}
}

// A session that runs several profiles' hooks binds none of them
// automatically, releases one it bound on its first run, and shows no
// content until it rebinds explicitly.
func TestHookAmbiguousSessionBindsNothing(t *testing.T) {
	env, path, _ := sessionHub(t)
	noVecnetProfile(env)
	qPath, err := statePath(func(k string) string { return env[k] }, "q", "")
	if err != nil {
		t.Fatal(err)
	}
	seedSession(t, path, func(*hookState) {})
	seedSession(t, qPath, func(*hookState) {})
	if out, _ := runHookAs(t, env, "S1", "p"); out != "" {
		t.Fatalf("p binding run printed %q", out)
	}
	out, errs := runHookAs(t, env, "S1", "q")
	ctx := contextOf(t, out)
	if !strings.Contains(ctx, "several profiles (p, q)") || !strings.Contains(ctx, "run: vecnet hook rebind --profile q.") || strings.Contains(ctx, "secret") || !strings.Contains(errs, "several profiles") {
		t.Fatalf("ambiguous notice=%q errs=%q", ctx, errs)
	}
	out, _ = runHookAs(t, env, "S1", "p")
	if ctx := contextOf(t, out); !strings.Contains(ctx, "profile p's hook is not bound") || strings.Contains(ctx, "secret") {
		t.Fatalf("released notice=%q", ctx)
	}
	if s := readHookState(t, path); s.Session != "" || len(s.Shown) != 0 {
		t.Fatalf("ambiguous binding kept: %+v", s)
	}
	for _, p := range []string{"p", "q"} {
		if out, _ := runHookAs(t, env, "S1", p); out != "" {
			t.Fatalf("%s: repeated notice %q", p, out)
		}
	}
	// An explicit rebind is never released for ambiguity.
	env["CLAUDE_CODE_SESSION_ID"] = "S1"
	if code, out, errs := runCLI(env, "hook", "rebind", "--profile", "p"); code != 0 || !strings.Contains(out, "now bound to this Claude Code session (S1)") {
		t.Fatalf("rebind exit=%d out=%q errs=%q", code, out, errs)
	}
	delete(env, "CLAUDE_CODE_SESSION_ID")
	if out, _ := runHookAs(t, env, "S1", "p"); !strings.Contains(contextOf(t, out), "secret text") {
		t.Fatalf("rebound session missed delivery: %q", out)
	}
	if out, _ := runHookAs(t, env, "S1", "q"); out != "" {
		t.Fatalf("q claimed in the ambiguous session: %q", out)
	}
}

func runCLI(env map[string]string, args ...string) (int, string, string) {
	var out, errs bytes.Buffer
	code := RunContext(context.Background(), args, strings.NewReader(""), &out, &errs, func(k string) string { return env[k] })
	return code, out.String(), errs.String()
}

func TestHookRebind(t *testing.T) {
	t.Run("session id", func(t *testing.T) {
		env, path, _ := sessionHub(t)
		noVecnetProfile(env)
		seedSession(t, path, func(s *hookState) { boundTo("S1")(s); s.Noticed = []string{"S2", "S3"} })
		env["CLAUDE_CODE_SESSION_ID"] = "S2"
		code, out, errs := runCLI(env, "hook", "rebind", "--profile", "p")
		if code != 0 || !strings.Contains(out, "profile p's hook is now bound to this Claude Code session (S2); it was bound to session S1 since 2026-11-04 09:30 UTC.") {
			t.Fatalf("exit=%d out=%q errs=%q", code, out, errs)
		}
		s := readHookState(t, path)
		if s.Session != "S2" || s.SessionHow != bindRebind || !s.RebindUntil.IsZero() || !reflect.DeepEqual(s.Noticed, []string{"S3"}) {
			t.Fatalf("state %+v", s)
		}
		if code, out, _ := runCLI(env, "hook", "rebind", "--profile", "p"); code != 0 || !strings.Contains(out, "already bound to this Claude Code session") {
			t.Fatalf("repeat rebind exit=%d out=%q", code, out)
		}
		delete(env, "CLAUDE_CODE_SESSION_ID")
		if out, _ := runHookAs(t, env, "S2", "p"); !strings.Contains(contextOf(t, out), "secret text") {
			t.Fatalf("rebound session missed delivery: %q", out)
		}
		if out, _ := runHookAs(t, env, "S1", "p"); !strings.Contains(contextOf(t, out), "belongs to another") {
			t.Fatalf("old session not told: %q", out)
		}
	})
	t.Run("pending window", func(t *testing.T) {
		env, path, _ := sessionHub(t)
		noVecnetProfile(env)
		seedSession(t, path, boundTo("S1"))
		start := time.Now()
		code, out, _ := runCLI(env, "hook", "rebind", "--profile", "p")
		if code != 0 || !strings.Contains(out, "CLAUDE_CODE_SESSION_ID is not set here") || !strings.Contains(out, "(10m0s)") {
			t.Fatalf("exit=%d out=%q", code, out)
		}
		s := readHookState(t, path)
		if s.Session != "S1" || s.RebindUntil.Before(start.Add(hookRebindWindow-time.Minute)) || s.RebindUntil.After(time.Now().Add(hookRebindWindow)) {
			t.Fatalf("pending state %+v", s)
		}
		// The next run from any session takes it and claims at once.
		out, errs := runHookAs(t, env, "S3", "p")
		if !strings.Contains(contextOf(t, out), "secret text") || !strings.Contains(errs, "rebound") {
			t.Fatalf("pending rebind run out=%q errs=%q", out, errs)
		}
		if s := readHookState(t, path); s.Session != "S3" || s.SessionHow != bindRebind || !s.RebindUntil.IsZero() {
			t.Fatalf("after taking: %+v", s)
		}
		if out, _ := runHookAs(t, env, "S1", "p"); !strings.Contains(out, "belongs to another") {
			t.Fatalf("old owner not told: %q", out)
		}
	})
	t.Run("expired window", func(t *testing.T) {
		env, path, _ := sessionHub(t)
		noVecnetProfile(env)
		seedSession(t, path, func(s *hookState) { boundTo("S1")(s); s.RebindUntil = time.Now().Add(-time.Second) })
		if out, _ := runHookAs(t, env, "S3", "p"); !strings.Contains(out, "belongs to another") {
			t.Fatalf("expired window taken: %q", out)
		}
		if s := readHookState(t, path); s.Session != "S1" {
			t.Fatalf("state %+v", s)
		}
	})
	t.Run("refusals", func(t *testing.T) {
		env, path, _ := sessionHub(t)
		seedSession(t, path, boundTo("S1"))
		before, _ := os.ReadFile(path)
		env["VECNET_PROFILE"] = "q"
		if code, _, errs := runCLI(env, "hook", "rebind", "--profile", "p"); code == 0 || !strings.Contains(errs, "VECNET_PROFILE=q") {
			t.Fatalf("mismatch exit=%d errs=%q", code, errs)
		}
		delete(env, "VECNET_PROFILE")
		if code, _, errs := runCLI(env, "hook", "rebind"); code == 0 || !strings.Contains(errs, "--profile") {
			t.Fatalf("unnamed exit=%d errs=%q", code, errs)
		}
		if code, _, _ := runCLI(env, "hook", "rebind", "--profile", "nope"); code == 0 {
			t.Fatal("unknown profile accepted")
		}
		if after, _ := os.ReadFile(path); !bytes.Equal(before, after) {
			t.Fatal("refused rebind changed state")
		}
	})
}

// Without a session_id (older Claude Code, a manual run) the hook keeps its
// unbound behaviour and leaves any binding alone; CLAUDE_CODE_SESSION_ID
// stands in when stdin has none.
func TestHookMissingSessionID(t *testing.T) {
	env, path, _ := sessionHub(t)
	noVecnetProfile(env)
	seedSession(t, path, boundTo("S1"))
	out, _ := runHook(t, env, "--profile", "p", "--event", "user-prompt")
	if !strings.Contains(contextOf(t, out), "secret text") {
		t.Fatalf("no-session run missed delivery: %q", out)
	}
	if s := readHookState(t, path); s.Session != "S1" || len(s.Noticed) != 0 {
		t.Fatalf("binding changed: %+v", s)
	}
	env["CLAUDE_CODE_SESSION_ID"] = "S2"
	if out, _ := runHookAs(t, env, "", "p"); !strings.Contains(out, "belongs to another") {
		t.Fatalf("env session ignored: %q", out)
	}
}

func TestHookVecnetProfile(t *testing.T) {
	t.Run("mismatch", func(t *testing.T) {
		env, path, polls := sessionHub(t)
		env["VECNET_PROFILE"] = "q"
		for _, session := range []string{"S1", ""} {
			if out, errs := runHookAs(t, env, session, "p"); out != "" || errs != "" {
				t.Fatalf("session %q: out=%q errs=%q", session, out, errs)
			}
		}
		if _, err := os.Stat(path); !os.IsNotExist(err) || polls.Load() != 0 {
			t.Fatalf("mismatch touched state (%v) or polled (%d)", err, polls.Load())
		}
		// Precedence over binding: even the bound session stays silent.
		seedSession(t, path, boundTo("S1"))
		before, _ := os.ReadFile(path)
		if out, errs := runHookAs(t, env, "S1", "p"); out != "" || errs != "" {
			t.Fatalf("bound mismatch out=%q errs=%q", out, errs)
		}
		if after, _ := os.ReadFile(path); !bytes.Equal(before, after) {
			t.Fatal("mismatch changed state")
		}
	})
	t.Run("match", func(t *testing.T) {
		env, path, _ := sessionHub(t) // VECNET_PROFILE=p
		seedSession(t, path, func(*hookState) {})
		// A declared session binds and claims on the same run.
		out, _ := runHookAs(t, env, "S1", "p")
		if !strings.Contains(contextOf(t, out), "secret text") {
			t.Fatalf("declared session missed delivery: %q", out)
		}
		if s := readHookState(t, path); s.Session != "S1" || s.SessionHow != bindDeclared {
			t.Fatalf("state %+v", s)
		}
	})
	t.Run("unset", func(t *testing.T) {
		env, path, _ := sessionHub(t)
		noVecnetProfile(env)
		seedSession(t, path, func(*hookState) {})
		runHookAs(t, env, "S1", "p")
		if out, _ := runHookAs(t, env, "S1", "p"); !strings.Contains(contextOf(t, out), "secret text") {
			t.Fatalf("unset: %q", out)
		}
	})
}

// After /clear the session id changes but the Claude Code process does
// not: the binding follows it. Another live process is still foreign.
func TestHookClearFollowsProcess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("process identity is unknown outside Unix")
	}
	env, path, _ := sessionHub(t)
	noVecnetProfile(env)
	env["CLAUDE_PID"] = strconv.Itoa(os.Getpid())
	seedSession(t, path, func(*hookState) {})
	runHookAs(t, env, "S1", "p")
	if s := readHookState(t, path); s.Proc.PID != os.Getpid() {
		t.Fatalf("process not recorded: %+v", s)
	}
	out, errs := runHookAs(t, env, "S1-cleared", "p")
	if !strings.Contains(contextOf(t, out), "secret text") || !strings.Contains(errs, "new session id") {
		t.Fatalf("cleared session out=%q errs=%q", out, errs)
	}
	if s := readHookState(t, path); s.Session != "S1-cleared" || s.SessionHow != bindAuto {
		t.Fatalf("state %+v", s)
	}
	env["CLAUDE_PID"] = strconv.Itoa(os.Getppid())
	if out, _ := runHookAs(t, env, "S2", "p"); !strings.Contains(out, "belongs to another") {
		t.Fatalf("other live process: %q", out)
	}
}

// A binding whose Claude Code process has exited is stale: the next
// session binds it (claiming from its following run).
func TestHookDeadSessionReleased(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("process identity is unknown outside Unix")
	}
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	host, _ := os.Hostname()
	env, path, _ := sessionHub(t)
	noVecnetProfile(env)
	seedSession(t, path, func(s *hookState) {
		boundTo("S1")(s)
		s.Proc = hookProc{PID: cmd.ProcessState.Pid(), Host: host}
	})
	if out, errs := runHookAs(t, env, "S2", "p"); out != "" || !strings.Contains(errs, "bound to this") {
		t.Fatalf("stale binding not taken: out=%q errs=%q", out, errs)
	}
	if out, _ := runHookAs(t, env, "S2", "p"); !strings.Contains(contextOf(t, out), "secret text") {
		t.Fatalf("new owner missed delivery: %q", out)
	}
	// Another host's process is never judged dead from here.
	seedSession(t, path, func(s *hookState) {
		boundTo("S1")(s)
		s.Proc = hookProc{PID: cmd.ProcessState.Pid(), Host: host + "-other"}
	})
	if out, _ := runHookAs(t, env, "S3", "p"); !strings.Contains(out, "belongs to another") {
		t.Fatalf("other host's binding taken: %q", out)
	}
}

func TestProcStart(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("/proc start time is Linux-only")
	}
	if a, b := procStart(os.Getpid()), procStart(os.Getpid()); a == "" || a != b {
		t.Fatalf("start=%q/%q", a, b)
	}
	if alive, known := procAlive(os.Getpid()); !alive || !known {
		t.Fatal("self not alive")
	}
}

// The registry stays bounded and keeps the most recently seen sessions.
func TestSessionRegistryBounded(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "+sessions.json")
	base := time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC)
	for i := range hookSessionsMax + 10 {
		if _, err := registerSession(context.Background(), path, "S"+strconv.Itoa(i), "p", base.Add(time.Duration(i)*2*time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	got, err := registerSession(context.Background(), path, "S"+strconv.Itoa(hookSessionsMax+9), "q", base.Add(1000*time.Hour))
	if err != nil || !reflect.DeepEqual(got, []string{"p", "q"}) {
		t.Fatalf("profiles=%v err=%v", got, err)
	}
	raw, _ := os.ReadFile(path)
	if n := strings.Count(string(raw), `"id"`); n != hookSessionsMax {
		t.Fatalf("registry holds %d sessions", n)
	}
	if strings.Contains(string(raw), `"S0"`) {
		t.Fatal("oldest session kept")
	}
}

// A subagent's or teammate's tool hook carries agent_id and its parent's
// session_id. It claims nothing, prints nothing, makes no hub request and
// leaves the binding and registry alone, so the parent's next run shows
// the delivery (issue 39).
func TestHookSubagentClaimsNothing(t *testing.T) {
	for _, tc := range []struct{ name, state string }{{"bound to parent", "S1"}, {"unbound", ""}} {
		t.Run(tc.name, func(t *testing.T) {
			env, path, polls := sessionHub(t)
			noVecnetProfile(env)
			seedSession(t, path, func(s *hookState) {
				if tc.state != "" {
					boundTo(tc.state)(s)
				}
			})
			before, _ := os.ReadFile(path)
			beforeBinding, _ := os.ReadFile(bindingPath(path))
			for _, event := range []string{"PostToolUse", "UserPromptSubmit"} {
				var out, errs bytes.Buffer
				stdin := `{"hook_event_name":"` + event + `","session_id":"S1","agent_id":"agent-7f3a","agent_type":"Explore","tool_name":"Bash"}`
				for _, args := range [][]string{{"hook", "--profile", "p", "--event", "post-tool"}, {"hook", "--profile", "p"}} {
					out.Reset()
					errs.Reset()
					if code := RunContext(context.Background(), args, strings.NewReader(stdin), &out, &errs, func(k string) string { return env[k] }); code != 0 || out.Len() != 0 || errs.Len() != 0 {
						t.Fatalf("%s %v: exit=%d out=%q errs=%q", event, args, code, out.String(), errs.String())
					}
				}
			}
			if after, _ := os.ReadFile(bindingPath(path)); !bytes.Equal(beforeBinding, after) {
				t.Fatal("subagent run changed the binding")
			}
			if after, _ := os.ReadFile(path); !bytes.Equal(before, after) || polls.Load() != 0 {
				t.Fatalf("subagent run changed state (%t) or polled (%d)", !bytes.Equal(before, after), polls.Load())
			}
			if _, err := os.Stat(sessionsPath(path)); !os.IsNotExist(err) {
				t.Fatalf("subagent run wrote the registry: %v", err)
			}
			// The parent itself (no agent_id) still claims; agent_type alone
			// (a session started with --agent) is the main thread.
			if tc.state == "" {
				runHookAs(t, env, "S1", "p") // binding run
			}
			var out, errs bytes.Buffer
			stdin := `{"hook_event_name":"UserPromptSubmit","session_id":"S1","agent_type":"reviewer"}`
			RunContext(context.Background(), []string{"hook", "--profile", "p", "--event", "user-prompt"}, strings.NewReader(stdin), &out, &errs, func(k string) string { return env[k] })
			if !strings.Contains(contextOf(t, out.String()), "secret text") {
				t.Fatalf("parent missed the delivery: out=%q errs=%q", out.String(), errs.String())
			}
		})
	}
}

// failingReader returns its bytes, then a read error.
type failingReader struct{ data []byte }

func (r *failingReader) Read(p []byte) (int, error) {
	if len(r.data) == 0 {
		return 0, errors.New("stdin read failed")
	}
	n := copy(p, r.data)
	r.data = r.data[n:]
	return n, nil
}

// Installed hooks always pass --event. Nonempty stdin that cannot be read
// or decoded (a Codex tool_response over the 1 MiB cap, truncated JSON, a
// read error) is not a manual run: the identity it carried, including a
// foreign session_id or a subagent's agent_id, is unknown, so the run
// claims nothing, prints nothing, polls nothing and leaves state, binding
// and registry alone.
func TestHookUnusableStdinClaimsNothing(t *testing.T) {
	big, err := json.Marshal(map[string]any{"hook_event_name": "PostToolUse", "session_id": "S2", "agent_id": "agent-7f3a", "tool_name": "shell_command", "tool_response": strings.Repeat("x", 1<<20)})
	if err != nil {
		t.Fatal(err)
	}
	inputs := map[string]func() io.Reader{
		"oversized": func() io.Reader { return bytes.NewReader(big) },
		"truncated": func() io.Reader {
			return strings.NewReader(`{"hook_event_name":"PostToolUse","session_id":"S2","agent_id":"agent-7f3a"`)
		},
		"not json": func() io.Reader { return strings.NewReader("session S2\n") },
		"read error": func() io.Reader {
			return &failingReader{data: []byte(`{"hook_event_name":"PostToolUse","session_id":"S2"`)}
		},
	}
	for name, input := range inputs {
		t.Run(name, func(t *testing.T) {
			env, path, polls := sessionHub(t)
			noVecnetProfile(env)
			seedSession(t, path, boundTo("S1"))
			before, _ := os.ReadFile(path)
			beforeBinding, _ := os.ReadFile(bindingPath(path))
			for _, event := range []string{"post-tool", "user-prompt", "session-start"} {
				var out, errs bytes.Buffer
				if code := RunContext(context.Background(), []string{"hook", "--profile", "p", "--event", event}, input(), &out, &errs, func(k string) string { return env[k] }); code != 0 || out.Len() != 0 || errs.Len() != 0 {
					t.Fatalf("%s: exit=%d out=%q errs=%q", event, code, out.String(), errs.String())
				}
			}
			if after, _ := os.ReadFile(bindingPath(path)); !bytes.Equal(beforeBinding, after) {
				t.Fatal("unusable stdin changed the binding")
			}
			if after, _ := os.ReadFile(path); !bytes.Equal(before, after) || polls.Load() != 0 {
				t.Fatalf("unusable stdin changed state (%t) or polled (%d)", !bytes.Equal(before, after), polls.Load())
			}
			if _, err := os.Stat(sessionsPath(path)); !os.IsNotExist(err) {
				t.Fatalf("unusable stdin wrote the registry: %v", err)
			}
		})
	}
	// Empty or whitespace-only stdin is still a manual run without a session.
	for _, stdin := range []string{"", " \n"} {
		env, path, _ := sessionHub(t)
		noVecnetProfile(env)
		seedSession(t, path, boundTo("S1"))
		var out, errs bytes.Buffer
		RunContext(context.Background(), []string{"hook", "--profile", "p", "--event", "user-prompt"}, strings.NewReader(stdin), &out, &errs, func(k string) string { return env[k] })
		if !strings.Contains(contextOf(t, out.String()), "secret text") {
			t.Fatalf("manual run %q missed the delivery: out=%q errs=%q", stdin, out.String(), errs.String())
		}
	}
}

// oldWriter rewrites path as a vecnet from before session binding would
// (a long-running watch --for-monitor, or a stanza naming an older
// binary): it knows only the pre-binding fields.
func oldWriter(t *testing.T, path string) {
	t.Helper()
	var old struct {
		Hub              string    `json:"hub,omitempty"`
		Caller           string    `json:"caller,omitempty"`
		As               string    `json:"as,omitempty"`
		Cred             string    `json:"cred,omitempty"`
		PollCursor       string    `json:"poll_cursor,omitempty"`
		Shown            []string  `json:"shown"`
		LastPostToolPoll time.Time `json:"last_post_tool_poll,omitzero"`
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &old); err != nil {
		t.Fatal(err)
	}
	old.Shown = append(old.Shown, "dly_old_watch")
	raw, _ = json.Marshal(old)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

// The reported regression: a top-level session that loads two profiles'
// stanzas rebinds its profile, an older vecnet then claims a delivery for
// the same profile, and the binding must still hold.
func TestHookRebindSurvivesPreBindingWriter(t *testing.T) {
	env, path, _ := sessionHub(t)
	noVecnetProfile(env)
	env["CLAUDE_PID"] = strconv.Itoa(os.Getpid())
	seedSession(t, path, func(*hookState) {})
	runHookAs(t, env, "S1", "q") // the session runs both profiles' hooks
	env["CLAUDE_CODE_SESSION_ID"] = "S1"
	if code, out, errs := runCLI(env, "hook", "rebind", "--profile", "p"); code != 0 {
		t.Fatalf("rebind exit=%d out=%q errs=%q", code, out, errs)
	}
	delete(env, "CLAUDE_CODE_SESSION_ID")
	oldWriter(t, path)
	out, errs := runHookAs(t, env, "S1", "p")
	if out != "" || readHookState(t, path).Session != "S1" {
		t.Fatal("legacy reservation bypassed proof or lost session binding")
	}
	// The old writer cannot attest output. Explicit verified proof unblocks
	// delivery; it must not change the independently preserved session binding.
	cfg, err := loadConfig(env["VECNET_CONFIG"])
	if err != nil {
		t.Fatal(err)
	}
	if err := (stateFile{path}).update(t.Context(), func(s *hookState) error {
		s.bind(hookIdentity{Hub: normalizeHub(cfg.Profiles["p"].Hub), Caller: "peer_test", Cred: hookCredential("vn_peer_test")})
		s.recordDisplay("dly_old_watch")
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	out, errs = runHookAs(t, env, "S1", "p")
	if !strings.Contains(contextOf(t, out), "secret text") {
		t.Fatalf("rebind binding lost after an older writer: out=%q errs=%q", out, errs)
	}
}

// runHookEvent runs the hook for one Claude Code event (UserPromptSubmit or
// PostToolUse) as session, with extra environment, returning stdout+stderr.
func runHookEvent(t *testing.T, env map[string]string, extra map[string]string, event, session string) (string, string) {
	t.Helper()
	flag := map[string]string{"UserPromptSubmit": "user-prompt", "PostToolUse": "post-tool"}[event]
	stdin := `{"hook_event_name":"` + event + `","session_id":"` + session + `"}`
	var out, errs bytes.Buffer
	code := RunContext(context.Background(), []string{"hook", "--profile", "p", "--event", flag}, strings.NewReader(stdin), &out, &errs, func(k string) string {
		if v, ok := extra[k]; ok {
			return v
		}
		return env[k]
	})
	if code != 0 {
		t.Fatalf("hook exit %d", code)
	}
	return out.String(), errs.String()
}

// A bound profile moves only by an explicit rebind, or when its Claude
// Code process has provably exited and a user-prompt run binds it anew.
// Nothing another live session's hook does unbinds or takes it.
func TestHookBindingMovesOnlyExplicitly(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("process identity is unknown outside Unix")
	}
	host, _ := os.Hostname()
	self := hookProc{PID: os.Getpid(), Start: procStart(os.Getpid()), Host: host}
	other := strconv.Itoa(os.Getppid())
	for _, how := range []string{bindRebind, bindDeclared, bindAuto} {
		t.Run(how, func(t *testing.T) {
			env, path, _ := sessionHub(t)
			noVecnetProfile(env)
			seedSession(t, path, func(s *hookState) {
				s.Session, s.SessionAt, s.SessionHow, s.Proc = "S1", time.Now(), how, self
			})
			// S2 runs every profile's hook (ambiguous), as subagents and
			// sibling sessions in a shared directory do.
			if _, err := registerSession(context.Background(), sessionsPath(path), "S2", "q", time.Now()); err != nil {
				t.Fatal(err)
			}
			hostile := []struct {
				name, event, session string
				extra                map[string]string
			}{
				{"user-prompt", "UserPromptSubmit", "S2", nil},
				{"post-tool", "PostToolUse", "S2", nil},
				{"declared", "UserPromptSubmit", "S2", map[string]string{"VECNET_PROFILE": "p"}},
				{"declared post-tool", "PostToolUse", "S2", map[string]string{"VECNET_PROFILE": "p"}},
				{"other live process", "UserPromptSubmit", "S3", map[string]string{"CLAUDE_PID": other}},
				{"post-tool, owner's process", "PostToolUse", "S4", map[string]string{"CLAUDE_PID": strconv.Itoa(os.Getpid())}},
				{"no session", "UserPromptSubmit", "", nil},
			}
			for _, h := range hostile {
				out, _ := runHookEvent(t, env, h.extra, h.event, h.session)
				if strings.Contains(out, "secret text") && h.session != "" {
					t.Fatalf("%s: another session claimed: %q", h.name, out)
				}
				if s := readHookState(t, path); s.Session != "S1" || s.SessionHow != how {
					t.Fatalf("%s: binding moved to %q (%s)", h.name, s.Session, s.SessionHow)
				}
			}
			// An older vecnet rewriting the state file leaves it bound.
			oldWriter(t, path)
			if s := readHookState(t, path); s.Session != "S1" {
				t.Fatalf("older writer unbound it: %+v", s.hookBinding)
			}
			// Once the owner's process is provably gone, a tool hook still
			// never binds; the next top-level prompt does.
			cmd := exec.Command(os.Args[0], "-test.run=^$")
			if err := cmd.Run(); err != nil {
				t.Fatal(err)
			}
			seedSession(t, path, func(s *hookState) { s.Proc = hookProc{PID: cmd.ProcessState.Pid(), Host: host} })
			if out, errs := runHookEvent(t, env, nil, "PostToolUse", "S5"); out != "" || errs != "" {
				t.Fatalf("post-tool on a dead owner: out=%q errs=%q", out, errs)
			}
			if s := readHookState(t, path); s.Session != "S1" {
				t.Fatalf("post-tool took a dead owner's binding: %+v", s.hookBinding)
			}
			runHookEvent(t, env, nil, "UserPromptSubmit", "S5")
			if s := readHookState(t, path); s.Session != "S5" || s.SessionHow != bindAuto {
				t.Fatalf("prompt after the owner exited: %+v", s.hookBinding)
			}
		})
	}
}

// Issue 39 item 4: after install, a subagent (its own session id, tool
// hooks only) runs first. It binds nothing; the top-level session's first
// prompt binds; the subagent stays silent afterwards.
func TestHookSubagentSessionNeverBinds(t *testing.T) {
	env, path, _ := sessionHub(t)
	noVecnetProfile(env)
	seedSession(t, path, func(*hookState) {})
	for range 3 {
		if out, errs := runHookEvent(t, env, nil, "PostToolUse", "SUB"); out != "" || errs != "" {
			t.Fatalf("subagent run: out=%q errs=%q", out, errs)
		}
	}
	if s := readHookState(t, path); s.Session != "" || len(s.Shown) != 0 {
		t.Fatalf("subagent bound or claimed: %+v", s)
	}
	runHookEvent(t, env, nil, "UserPromptSubmit", "TOP") // binds (claims next run)
	if out, _ := runHookEvent(t, env, nil, "PostToolUse", "TOP"); !strings.Contains(contextOf(t, out), "secret text") {
		t.Fatalf("top-level tool hook missed the delivery: %q", out)
	}
	if out, errs := runHookEvent(t, env, nil, "PostToolUse", "SUB"); out != "" || errs != "" {
		t.Fatalf("subagent after binding: out=%q errs=%q", out, errs)
	}
	if s := readHookState(t, path); s.Session != "TOP" || len(s.Noticed) != 0 {
		t.Fatalf("state %+v", s.hookBinding)
	}
}

// A binding that 8631cc0 kept inside the state file is adopted once, then
// moved to the binding file.
func TestHookBindingMigrates(t *testing.T) {
	env, path, _ := sessionHub(t)
	noVecnetProfile(env)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	legacy := `{"poll_cursor":"e1.0","shown":[],"session":"S1","session_at":"2026-11-04T09:46:00Z","session_how":"rebind"}`
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, _ := runHookAs(t, env, "S1", "p"); !strings.Contains(contextOf(t, out), "secret text") {
		t.Fatalf("legacy binding not adopted: %q", out)
	}
	raw, _ := os.ReadFile(path)
	braw, err := os.ReadFile(bindingPath(path))
	if err != nil || strings.Contains(string(raw), `"session"`) || !strings.Contains(string(braw), `"session":"S1"`) {
		t.Fatalf("state=%s binding=%s err=%v", raw, braw, err)
	}
}

// The hook names a sender from the delivery's send-time sender_info, then
// the cached directory, sanitised either way.
func TestHookNamesSenderFromSenderInfo(t *testing.T) {
	raw, _ := json.Marshal(map[string]any{"delivery_id": "dly_1", "message": map[string]any{"message_id": "msg_1", "thread_id": "thr_X", "sender": "peer_h", "kind": "message", "sender_info": map[string]string{"name": "Scout <b>\nX", "kind": "session"}, "content": map[string]string{"text": "hi"}}})
	withInfo := envelope.Event{Cursor: "e1.1", Type: "delivery", Data: raw}
	item, err := hookItemOf(withInfo, map[string]string{"peer_h": "Cached"})
	if err != nil || item.Name != "Scout <b>\nX" {
		t.Fatalf("name=%q err=%v", item.Name, err)
	}
	if ctx := hookContext(newHookHint("p", "", ""), []hookItem{item}, 0, false); !strings.Contains(ctx, `name="Scout_&lt;b&gt;_X"`) {
		t.Fatalf("sanitised name missing: %q", ctx)
	}
	item, _ = hookItemOf(hookEvent(2, "x"), map[string]string{"peer_h": "Cached"})
	if item.Name != "Cached" {
		t.Fatalf("fallback name=%q", item.Name)
	}
}

func TestHookSessionStartAdmission(t *testing.T) {
	for _, tc := range []struct {
		name                                           string
		declared, existing, foreign, ambiguous, rebind bool
	}{
		{name: "fresh unbound"}, {name: "existing unbound", existing: true},
		{name: "declared prompt binding", declared: true, existing: true},
		{name: "automatic prompt binding", existing: true},
		{name: "foreign", existing: true, foreign: true},
		{name: "ambiguous", existing: true, ambiguous: true},
		{name: "explicit rebind window", existing: true, foreign: true, rebind: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env, path, polls := sessionHub(t)
			env["CODEX_HOME"] = filepath.Join(t.TempDir(), "codex")
			if !tc.declared {
				noVecnetProfile(env)
			}
			if tc.existing {
				seedSession(t, path, func(s *hookState) {
					if tc.foreign {
						boundTo("other")(s)
					}
				})
			}
			if tc.ambiguous {
				if _, err := registerSession(context.Background(), sessionsPath(path), "s1", "q", hookNow()); err != nil {
					t.Fatal(err)
				}
			}
			if tc.rebind {
				if code, out, errs := runCLI(env, "hook", "rebind", "--profile", "p"); code != 0 {
					t.Fatalf("rebind: exit=%d out=%q stderr=%q", code, out, errs)
				}
			}
			before, beforeErr := os.ReadFile(path)
			beforeBinding, _ := os.ReadFile(bindingPath(path))
			out, errs := runHookStdin(t, env, `{"hook_event_name":"SessionStart","session_id":"s1","source":"startup"}`, "hook", "--profile", "p")
			if tc.rebind {
				if !strings.Contains(contextOf(t, out), `text="secret text"`) || polls.Load() != 1 {
					t.Fatalf("rebound start: out=%q stderr=%q polls=%d", out, errs, polls.Load())
				}
				if s := readHookState(t, path); s.Session != "s1" || s.SessionHow != bindRebind || !s.RebindUntil.IsZero() {
					t.Fatalf("binding=%+v", s.hookBinding)
				}
				return
			}
			if out != "" || errs != "" || polls.Load() != 0 {
				t.Fatalf("unadmitted start: out=%q stderr=%q polls=%d", out, errs, polls.Load())
			}
			after, afterErr := os.ReadFile(path)
			afterBinding, _ := os.ReadFile(bindingPath(path))
			if !bytes.Equal(before, after) || !bytes.Equal(beforeBinding, afterBinding) || errors.Is(beforeErr, os.ErrNotExist) != errors.Is(afterErr, os.ErrNotExist) {
				t.Fatal("unadmitted start changed state or binding")
			}
			if tc.foreign || tc.ambiguous {
				return
			}
			// Only a prompt may establish the binding. Declared prompts claim at
			// once; automatic prompts on an existing cursor defer the first claim.
			prompt, _ := runHookAs(t, env, "s1", "p")
			s := readHookState(t, path)
			wantHow := bindAuto
			if tc.declared {
				wantHow = bindDeclared
			}
			if s.Session != "s1" || s.SessionHow != wantHow {
				t.Fatalf("prompt binding=%+v", s.hookBinding)
			}
			if tc.declared {
				if !strings.Contains(contextOf(t, prompt), `text="secret text"`) || polls.Load() != 1 {
					t.Fatalf("declared prompt failed to claim: %q polls=%d", prompt, polls.Load())
				}
				// Leave a new delivery for the already admitted SessionStart.
				seedSession(t, path, func(s *hookState) { s.Shown = nil; s.Displayed = nil; s.PollCursor = "e1.0" })
			} else if tc.existing && (prompt != "" || polls.Load() != 0) {
				t.Fatalf("automatic prompt claimed too early: %q polls=%d", prompt, polls.Load())
			}
			beforePolls := polls.Load()
			out, _ = runHookStdin(t, env, `{"hook_event_name":"SessionStart","session_id":"s1","source":"resume"}`, "hook", "--profile", "p")
			if !strings.Contains(contextOf(t, out), `text="secret text"`) || !strings.Contains(out, `"hookEventName":"SessionStart"`) || polls.Load() != beforePolls+1 {
				t.Fatalf("admitted start: out=%q polls=%d", out, polls.Load())
			}
		})
	}
}

func TestHookSessionStartAgentIDSkip(t *testing.T) {
	for _, bound := range []bool{false, true} {
		t.Run(strconv.FormatBool(bound), func(t *testing.T) {
			var requests atomic.Int32
			env, path := hookFixtureRaw(t, func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				t.Errorf("unexpected request: %s", r.URL)
				w.WriteHeader(500)
			})
			env["CODEX_HOME"] = filepath.Join(t.TempDir(), "codex")
			if bound {
				seedSession(t, path, boundTo("s1"))
			}
			before, _ := os.ReadFile(path)
			binding, _ := os.ReadFile(bindingPath(path))
			for _, args := range [][]string{{"hook", "--profile", "p"}, {"hook", "--profile", "p", "--event", "session-start"}} {
				out, errs := runHookStdin(t, env, `{"hook_event_name":"SessionStart","session_id":"s1","agent_id":"agent-7","source":"resume"}`, args...)
				if out != "" || errs != "" || requests.Load() != 0 {
					t.Fatalf("out=%q stderr=%q requests=%d", out, errs, requests.Load())
				}
			}
			if after, _ := os.ReadFile(path); !bytes.Equal(before, after) {
				t.Fatal("agent changed state")
			}
			if after, _ := os.ReadFile(bindingPath(path)); !bytes.Equal(binding, after) {
				t.Fatal("agent changed binding")
			}
			if _, err := os.Stat(sessionsPath(path)); !os.IsNotExist(err) {
				t.Fatalf("agent touched registry: %v", err)
			}
			if !bound {
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Fatalf("agent created state: %v", err)
				}
			}
		})
	}
}

func TestHookSessionStartMCPHints(t *testing.T) {
	for _, summary := range []bool{false, true} {
		for _, mcp := range []string{"", "vecnet-p", "vecnet-q"} {
			t.Run(strconv.FormatBool(summary)+"/"+mcp, func(t *testing.T) {
				var want string
				for _, event := range []string{"user-prompt", "session-start"} {
					env, path := hookFixture(t, func(w http.ResponseWriter, r *http.Request) {
						if r.URL.Query().Get("after") == "head" {
							hookPage(w, nil, "e1.0")
						} else {
							hookPage(w, []envelope.Event{hookEvent(1, "secret text")}, "e1.1")
						}
					})
					env["CODEX_HOME"] = filepath.Join(t.TempDir(), "codex")
					seedSession(t, path, func(s *hookState) {
						boundTo("s1")(s)
						if summary {
							s.PollCursor = ""
						}
					})
					out, _ := runHookStdin(t, env, `{"session_id":"s1"}`, "hook", "--profile", "p", "--event", event, "--mcp", mcp)
					ctx := contextOf(t, out)
					if (mcp == "vecnet-p") != strings.Contains(ctx, "MCP server vecnet-p") {
						t.Fatalf("mcp=%q context=%q", mcp, ctx)
					}
					if !strings.Contains(ctx, "vecnet --net p ") || !strings.Contains(ctx, "Commands need the same VECNET_CONFIG (v.toml).") {
						t.Fatalf("qualification lost: %q", ctx)
					}
					if want == "" {
						want = ctx
					} else if ctx != want {
						t.Fatalf("start context=%q, prompt context=%q", ctx, want)
					}
				}
			})
		}
	}
}
