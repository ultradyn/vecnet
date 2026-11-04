package cli

import (
	"bytes"
	"context"
	"errors"
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
				results[i] = s.admit(hookOwner{session: "S" + strconv.Itoa(i), profiles: []string{"p"}}, time.Now())
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
