package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/envelope"
)

// agyHub serves "turn one" (dly_1) after e1.0 and "turn two" (dly_2) after
// e1.1, counting event polls.
func agyHub(t *testing.T) (map[string]string, string, *atomic.Int32) {
	t.Helper()
	var polls atomic.Int32
	env, path := hookFixture(t, func(w http.ResponseWriter, r *http.Request) {
		polls.Add(1)
		switch r.URL.Query().Get("after") {
		case "e1.0":
			hookPage(w, []envelope.Event{hookEvent(1, "turn one")}, "e1.1")
		case "e1.1":
			hookPage(w, []envelope.Event{hookEvent(2, "turn two")}, "e1.2")
		case "head":
			hookPage(w, nil, "e1.0")
		default:
			hookPage(w, nil, "e1.2")
		}
	})
	return env, path, &polls
}

// agyStdin is agy's PreInvocation hook payload for one conversation
// (docs/research/evidence/agy-1.5.13.json, hooks.payload_fields).
func agyStdin(event, conv string, inv int, input string) string {
	return fmt.Sprintf(`{"hookEventName":%q,"conversationId":%q,"invocationNum":%d,"initialNumSteps":1,"lastUserInput":%q,"cwd":"/w/.agents","modelName":"gemini-4-neon","transcriptPath":"/w/t.jsonl","artifactDirectoryPath":"/w/a","workspacePaths":["/w"]}`, event, conv, inv, input)
}

// agyStartStdin is agy's SessionStart payload: no invocation fields.
func agyStartStdin(conv string) string {
	return fmt.Sprintf(`{"hookEventName":"SessionStart","conversationId":%q,"lastUserInput":"","cwd":"/w/.agents","modelName":"gemini-4-neon","transcriptPath":"/w/t.jsonl","artifactDirectoryPath":"/w/a","workspacePaths":["/w"]}`, conv)
}

// runAgyHook runs `vecnet hook --client agy` with an agy payload on stdin.
func runAgyHook(t *testing.T, env map[string]string, stdin io.Reader, args ...string) (string, string) {
	t.Helper()
	var out, errs bytes.Buffer
	code := RunContext(context.Background(), append([]string{"hook", "--client", "agy"}, args...), stdin, &out, &errs, func(k string) string { return env[k] })
	if code != 0 {
		t.Fatalf("agy hook exit %d, stderr=%q", code, errs.String())
	}
	return out.String(), errs.String()
}

// agyContextOf parses agy's hook output and returns the injected
// systemMessage. The shape is exactly one inject step with exactly one
// systemMessage key holding exactly one systemMessage string: never
// userMessage, ephemeralMessage or any other step kind.
func agyContextOf(t *testing.T, out string) string {
	t.Helper()
	var top map[string]json.RawMessage
	if err := json.Unmarshal([]byte(out), &top); err != nil {
		t.Fatalf("agy hook JSON: %v, output=%q", err, out)
	}
	if len(top) != 1 || top["injectSteps"] == nil || strings.Count(out, "\n") != 1 || !strings.HasSuffix(out, "\n") {
		t.Fatalf("agy output shape=%q", out)
	}
	for _, banned := range []string{"userMessage", "ephemeralMessage", "user_markdown", "terminationBehavior"} {
		if strings.Contains(out, `"`+banned+`"`) {
			t.Fatalf("agy output uses banned key %s: %q", banned, out)
		}
	}
	var steps []map[string]json.RawMessage
	if err := json.Unmarshal(top["injectSteps"], &steps); err != nil || len(steps) != 1 {
		t.Fatalf("agy inject steps=%q", out)
	}
	if len(steps[0]) != 1 || steps[0]["systemMessage"] == nil {
		t.Fatalf("agy step shape=%q", out)
	}
	var inner map[string]json.RawMessage
	if err := json.Unmarshal(steps[0]["systemMessage"], &inner); err != nil || len(inner) != 1 || inner["systemMessage"] == nil {
		t.Fatalf("agy systemMessage shape=%q", out)
	}
	var s string
	if err := json.Unmarshal(inner["systemMessage"], &s); err != nil {
		t.Fatalf("agy systemMessage text=%q", out)
	}
	return s
}

// TestHookAgyProcess is the child of agyChild: it runs one hook command
// exactly as agy does (agy → sh -c → vecnet), so this test binary's own
// process stands in for the agy process: an ancestor found by name.
func TestHookAgyProcess(t *testing.T) {
	switch os.Getenv("HH_AGY_CHILD") {
	case "":
		return
	case "proc":
		agyNameSelf(t)
		p := agyHookProc()
		fmt.Printf("%d %s %s\n", p.PID, p.Start, p.Host)
		return
	}
	agyNameSelf(t)
	os.Exit(RunContext(context.Background(), append([]string{"hook", "--client", "agy"}, strings.Fields(os.Getenv("HH_AGY_ARGS"))...), strings.NewReader(os.Getenv("HH_AGY_STDIN")), os.Stdout, os.Stderr, os.Getenv))
}

// agyChild builds the hook child process under sh -c, mirroring agy's
// hook → sh -c → vecnet launch chain. mode selects TestHookAgyProcess's
// child behaviour.
func agyChild(t *testing.T, mode string, env map[string]string, stdin, args string) *exec.Cmd {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	// Quote the test binary for sh: a Windows path (C:\...) has backslashes
	// that sh would otherwise eat.
	cmd := exec.CommandContext(ctx, "sh", "-c", hookShellWord(filepath.ToSlash(os.Args[0]))+" -test.run=^TestHookAgyProcess$")
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(key, "CODEX_") || strings.HasPrefix(key, "CLAUDE_") || strings.HasPrefix(key, "VECNET_") || strings.HasPrefix(key, "ANTIGRAVITY_") || strings.HasPrefix(key, "HH_") || key == "GORACE" {
			continue
		}
		cmd.Env = append(cmd.Env, entry)
	}
	cmd.Env = append(cmd.Env, "HH_AGY_CHILD="+mode, "HH_AGY_ARGS="+args, "HH_AGY_STDIN="+stdin, "GORACE="+os.Getenv("GORACE")+" atexit_sleep_ms=0")
	for key, value := range env {
		cmd.Env = append(cmd.Env, key+"="+value)
	}
	return cmd
}

// agyRun runs one hook under sh -c and returns its stdout and stderr.
func agyRun(t *testing.T, env map[string]string, stdin, args string) (string, string) {
	t.Helper()
	var out, errs bytes.Buffer
	cmd := agyChild(t, "hook", env, stdin, args)
	cmd.Stdout, cmd.Stderr = &out, &errs
	if err := cmd.Run(); err != nil {
		t.Fatalf("agy hook child: %v, stderr=%q", err, errs.String())
	}
	return out.String(), errs.String()
}

// A turn-start PreInvocation (invocationNum 0, non-empty lastUserInput)
// binds the profile and delivers the inbox in a single systemMessage step.
func TestHookAgyTurnStartBindsAndDelivers(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"event flag", []string{"--profile", "p", "--event", "pre-invocation"}},
		{"stdin only", []string{"--profile", "p"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env, path, polls := agyHub(t)
			seedSession(t, path, func(*hookState) {})
			out, errs := runAgyHook(t, env, strings.NewReader(agyStdin("PreInvocation", "0b1c2d3e-4f5a-6b7c-8d9e-0f1a2b3c4d5e", 0, "first prompt")), tc.args...)
			ctx := agyContextOf(t, out)
			for _, want := range []string{`<vecnet-inbox untrusted="true">`, "turn one", "vecnet: 1 new message(s) for profile p."} {
				if !strings.Contains(ctx, want) {
					t.Fatalf("context lacks %q: %q", want, ctx)
				}
			}
			if !strings.Contains(errs, "bound to this agy conversation") {
				t.Fatalf("binding stderr=%q", errs)
			}
			s := readHookState(t, path)
			if s.Session != "0b1c2d3e-4f5a-6b7c-8d9e-0f1a2b3c4d5e" || !reflect.DeepEqual(s.Shown, []string{"dly_1"}) || s.PollCursor != "e1.1" {
				t.Fatalf("state=%+v", s)
			}
			if polls.Load() != 1 {
				t.Fatalf("polls=%d", polls.Load())
			}
		})
	}
}

// A mid-turn PreInvocation (invocationNum > 0) from the bound conversation
// delivers like a Claude post-tool hook, with its 5-second rate limit; a
// turn-start still polls every time.
func TestHookAgyMidTurnRateLimited(t *testing.T) {
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	old := hookNow
	hookNow = func() time.Time { return now }
	t.Cleanup(func() { hookNow = old })
	env, path, polls := agyHub(t)
	seedSession(t, path, func(*hookState) {})
	conv := "0b1c2d3e-4f5a-6b7c-8d9e-0f1a2b3c4d5e"
	runAgyHook(t, env, strings.NewReader(agyStdin("PreInvocation", conv, 0, "first prompt")), "--profile", "p", "--event", "pre-invocation")
	if polls.Load() != 1 {
		t.Fatalf("binding polls=%d", polls.Load())
	}
	now = now.Add(6 * time.Second)
	out, _ := runAgyHook(t, env, strings.NewReader(agyStdin("PreInvocation", conv, 1, "first prompt")), "--profile", "p", "--event", "pre-invocation")
	if ctx := agyContextOf(t, out); !strings.Contains(ctx, "turn two") {
		t.Fatalf("mid-turn missed delivery: %q", ctx)
	}
	// Within five seconds the mid-turn run is silent: {} on stdout.
	out, errs := runAgyHook(t, env, strings.NewReader(agyStdin("PreInvocation", conv, 2, "first prompt")), "--profile", "p", "--event", "pre-invocation")
	if out != "{}\n" || errs != "" {
		t.Fatalf("rate-limited mid-turn out=%q errs=%q", out, errs)
	}
	if s := readHookState(t, path); !reflect.DeepEqual(s.Shown, []string{"dly_1", "dly_2"}) || s.PollCursor != "e1.2" || polls.Load() != 2 {
		t.Fatalf("after mid-turn: state=%+v polls=%d", s, polls.Load())
	}
	// A turn-start is never rate limited (Claude user-prompt semantics).
	out, _ = runAgyHook(t, env, strings.NewReader(agyStdin("PreInvocation", conv, 0, "next turn")), "--profile", "p", "--event", "pre-invocation")
	if out != "{}\n" || polls.Load() != 3 {
		t.Fatalf("turn-start after rate limit: out=%q polls=%d", out, polls.Load())
	}
}

// A subagent's first PreInvocation (invocationNum 0, empty lastUserInput,
// its own conversationId) never binds, claims or says anything, and leaves
// the state file untouched.
func TestHookAgySubagentNeverBinds(t *testing.T) {
	env, path, polls := agyHub(t)
	seedSession(t, path, boundTo("c1-root"))
	before, _ := os.ReadFile(path)
	beforeBinding, _ := os.ReadFile(bindingPath(path))
	out, errs := runAgyHook(t, env, strings.NewReader(agyStdin("PreInvocation", "c2-sub", 0, "")), "--profile", "p", "--event", "pre-invocation")
	if out != "{}\n" || errs != "" {
		t.Fatalf("subagent out=%q errs=%q", out, errs)
	}
	if after, _ := os.ReadFile(path); !bytes.Equal(before, after) {
		t.Fatal("subagent run changed the state file")
	}
	if after, _ := os.ReadFile(bindingPath(path)); !bytes.Equal(beforeBinding, after) {
		t.Fatal("subagent run changed the binding")
	}
	if polls.Load() != 0 {
		t.Fatalf("subagent run polled %d times", polls.Load())
	}
}

// /clear starts a new conversationId in the same agy process: the binding
// follows the process (admitCleared) and delivers. A subagent conversation
// in that same process stays silent and binds nothing.
func TestHookAgyClearRebinds(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("/clear rebinding needs the agy process identity, which only Linux /proc provides")
	}
	env, path, _ := agyHub(t)
	seedSession(t, path, func(*hookState) {})
	conv := "0b1c2d3e-4f5a-6b7c-8d9e-0f1a2b3c4d5e"
	out, errs := agyRun(t, env, agyStdin("PreInvocation", conv, 0, "first prompt"), "--profile p --event pre-invocation")
	if ctx := agyContextOf(t, out); !strings.Contains(ctx, "turn one") {
		t.Fatalf("binding run missed delivery: %q", ctx)
	}
	if !strings.Contains(errs, "bound to this agy conversation") {
		t.Fatalf("binding stderr=%q", errs)
	}
	s := readHookState(t, path)
	if s.Session != conv || !reflect.DeepEqual(s.Shown, []string{"dly_1"}) {
		t.Fatalf("after binding: %+v", s)
	}
	// A subagent conversation inside the same agy process (the child's
	// grandparent is unchanged): silent, no rebind.
	out, errs = agyRun(t, env, agyStdin("PreInvocation", "c2-sub", 0, ""), "--profile p --event pre-invocation")
	if out != "{}\n" || errs != "" {
		t.Fatalf("subagent out=%q errs=%q", out, errs)
	}
	if s := readHookState(t, path); s.Session != conv {
		t.Fatalf("subagent moved the binding: %+v", s)
	}
	// /clear: a new conversationId, same process.
	cleared := "1a2b3c4d-5e6f-7a8b-9c0d-1e2f3a4b5c6d"
	out, errs = agyRun(t, env, agyStdin("PreInvocation", cleared, 0, "after clear"), "--profile p --event pre-invocation")
	if ctx := agyContextOf(t, out); !strings.Contains(ctx, "turn two") {
		t.Fatalf("cleared run missed delivery: %q", ctx)
	}
	if !strings.Contains(errs, "new conversation id") {
		t.Fatalf("cleared stderr=%q", errs)
	}
	s = readHookState(t, path)
	if s.Session != cleared || !reflect.DeepEqual(s.Shown, []string{"dly_1", "dly_2"}) {
		t.Fatalf("after clear: %+v", s)
	}
}

// An unbound profile plus a mid-turn run stays silent and binds nothing.
func TestHookAgyUnboundMidTurnSilent(t *testing.T) {
	env, path, polls := agyHub(t)
	out, errs := runAgyHook(t, env, strings.NewReader(agyStdin("PreInvocation", "c9", 1, "input")), "--profile", "p", "--event", "pre-invocation")
	if out != "{}\n" || errs != "" {
		t.Fatalf("unbound mid-turn out=%q errs=%q", out, errs)
	}
	for _, file := range []string{path, bindingPath(path)} {
		if _, err := os.Stat(file); !os.IsNotExist(err) {
			t.Fatalf("unbound mid-turn touched %s: %v", file, err)
		}
	}
	if polls.Load() != 0 {
		t.Fatalf("unbound mid-turn polled %d times", polls.Load())
	}
}

// SessionStart does no hub work and answers {}.
func TestHookAgySessionStartSilent(t *testing.T) {
	var requests atomic.Int32
	env, path := hookFixtureRaw(t, func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		t.Errorf("unexpected request: %s", r.URL)
		w.WriteHeader(500)
	})
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"event flag", []string{"--profile", "p", "--event", "session-start"}},
		{"stdin only", []string{"--profile", "p"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, errs := runAgyHook(t, env, strings.NewReader(agyStartStdin("c1")), tc.args...)
			if out != "{}\n" || errs != "" {
				t.Fatalf("start out=%q errs=%q", out, errs)
			}
		})
	}
	if requests.Load() != 0 {
		t.Fatalf("SessionStart made %d requests", requests.Load())
	}
	for _, file := range []string{path, bindingPath(path), sessionsPath(path), path + ".lock"} {
		if _, err := os.Stat(file); !os.IsNotExist(err) {
			t.Fatalf("SessionStart touched %s: %v", file, err)
		}
	}
}

// Events agy sends but Vecnet does not handle (PostInvocation, Stop) and
// unknown --event values stay silent: {} on stdout, nothing touched.
func TestHookAgyUnhandledEventsSilent(t *testing.T) {
	for _, tc := range []struct {
		name  string
		stdin string
		args  string
	}{
		{"PostInvocation stdin", agyStdin("PostInvocation", "c1", 1, "input"), "--profile p --event pre-invocation"},
		{"Stop stdin", `{"hookEventName":"Stop","conversationId":"c1","lastUserInput":"input","executionNum":1,"fullyIdle":true,"continuationActive":false,"terminationReason":"stop"}`, "--profile p --event pre-invocation"},
		{"unknown stdin event", `{"hookEventName":"Whatever","conversationId":"c1","lastUserInput":"input"}`, "--profile p"},
		{"claude event flag", agyStdin("PreInvocation", "c1", 0, "input"), "--profile p --event user-prompt"},
		{"unknown event flag", agyStdin("PreInvocation", "c1", 0, "input"), "--profile p --event bogus"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var requests atomic.Int32
			env, path := hookFixtureRaw(t, func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				t.Errorf("unexpected request: %s", r.URL)
				w.WriteHeader(500)
			})
			out, errs := runAgyHook(t, env, strings.NewReader(tc.stdin), strings.Fields(tc.args)...)
			if out != "{}\n" || errs != "" {
				t.Fatalf("out=%q errs=%q", out, errs)
			}
			if requests.Load() != 0 {
				t.Fatalf("%d requests", requests.Load())
			}
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Fatalf("touched state: %v", err)
			}
		})
	}
}

// The foreign-session notice arrives as a systemMessage step, never a
// userMessage, and carries no message content. agy's sandbox blocks
// ~/.config, so the notice tells the model to ask its user to run the
// rebind outside agy rather than running it itself.
func TestHookAgyForeignNoticeIsSystemMessage(t *testing.T) {
	env, path, polls := agyHub(t)
	seedSession(t, path, boundTo("c1-root"))
	out, errs := runAgyHook(t, env, strings.NewReader(agyStdin("PreInvocation", "c2-other", 0, "whose profile is this?")), "--profile", "p", "--event", "pre-invocation")
	ctx := agyContextOf(t, out) // asserts the single-systemMessage shape
	for _, want := range []string{
		"belongs to another agy conversation",
		"If this conversation is profile p, ask your user to run `vecnet hook rebind --profile p` in a terminal outside agy, then send the next prompt in this conversation.",
		"Otherwise ignore this.",
	} {
		if !strings.Contains(ctx, want) {
			t.Fatalf("notice lacks %q: %q", want, ctx)
		}
	}
	for _, banned := range []string{"Claude Code", "run: vecnet"} {
		if strings.Contains(ctx, banned) || strings.Contains(errs, banned) {
			t.Fatalf("agy notice or stderr names %q: ctx=%q errs=%q", banned, ctx, errs)
		}
	}
	if strings.Contains(ctx, "turn one") || strings.Contains(ctx, "vecnet-inbox") {
		t.Fatalf("notice carries content: %q", ctx)
	}
	if !strings.Contains(errs, "another agy conversation") {
		t.Fatalf("notice stderr=%q", errs)
	}
	if polls.Load() != 0 {
		t.Fatalf("foreign run polled %d times", polls.Load())
	}
	s := readHookState(t, path)
	if s.Session != "c1-root" || !reflect.DeepEqual(s.Noticed, []string{"c2-other"}) || len(s.Shown) != 0 {
		t.Fatalf("after notice: %+v", s)
	}
	// The second foreign run stays silent ({}), already told.
	out, _ = runAgyHook(t, env, strings.NewReader(agyStdin("PreInvocation", "c2-other", 0, "whose profile is this?")), "--profile", "p", "--event", "pre-invocation")
	if out != "{}\n" {
		t.Fatalf("repeat foreign out=%q", out)
	}
}

// An agy run never takes a rebind window without a prompt: during the
// window a subagent-shaped run stays silent and changes nothing, and the
// next agy root prompt takes the window.
func TestHookAgyRebindWindowNeedsPrompt(t *testing.T) {
	now := time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)
	old := hookNow
	hookNow = func() time.Time { return now }
	t.Cleanup(func() { hookNow = old })
	env, path, polls := agyHub(t)
	seedSession(t, path, func(s *hookState) { s.RebindUntil = now.Add(5 * time.Minute) })
	before, _ := os.ReadFile(bindingPath(path))
	out, errs := runAgyHook(t, env, strings.NewReader(agyStdin("PreInvocation", "c2-sub", 0, "")), "--profile", "p", "--event", "pre-invocation")
	if out != "{}\n" || errs != "" {
		t.Fatalf("window subagent out=%q errs=%q", out, errs)
	}
	if after, _ := os.ReadFile(bindingPath(path)); !bytes.Equal(before, after) {
		t.Fatal("window subagent run changed the binding")
	}
	if polls.Load() != 0 {
		t.Fatalf("window subagent run polled %d times", polls.Load())
	}
	// The next root prompt inside the window takes it.
	conv := "0b1c2d3e-4f5a-6b7c-8d9e-0f1a2b3c4d5e"
	out, errs = runAgyHook(t, env, strings.NewReader(agyStdin("PreInvocation", conv, 0, "mine now")), "--profile", "p", "--event", "pre-invocation")
	if ctx := agyContextOf(t, out); !strings.Contains(ctx, "turn one") {
		t.Fatalf("window root prompt missed delivery: %q", ctx)
	}
	if !strings.Contains(errs, "rebound to this agy conversation") {
		t.Fatalf("window root stderr=%q", errs)
	}
	s := readHookState(t, path)
	if s.Session != conv || s.SessionHow != bindRebind || !s.RebindUntil.IsZero() {
		t.Fatalf("after window: %+v", s.hookBinding)
	}
}

// agy notices name the agy conversation and delegate the rebind to the
// user; Claude's wording stays byte-identical (hook_test.go pins it).
func TestHookAgyNoticeWording(t *testing.T) {
	h := newHookHint("p", "", "")
	bound := time.Date(2026, 11, 4, 9, 30, 0, 0, time.UTC)
	agy := hookOwner{session: "c2", agy: true, profiles: []string{"p", "q"}}
	foreign := hookForeignNotice(h, bound, agy)
	ambiguous := hookAmbiguousNotice(h, agy)
	for _, notice := range []struct{ name, text string }{
		{"foreign", foreign},
		{"ambiguous", ambiguous},
	} {
		if strings.Contains(notice.text, "Claude Code") || strings.Contains(notice.text, "session") {
			t.Fatalf("%s notice names Claude Code or a session: %q", notice.name, notice.text)
		}
		for _, want := range []string{
			"ask your user to run `vecnet hook rebind --profile p` in a terminal outside agy, then send the next prompt in this conversation",
		} {
			if !strings.Contains(notice.text, want) {
				t.Fatalf("%s notice lacks %q: %q", notice.name, want, notice.text)
			}
		}
	}
	if !strings.Contains(foreign, "another agy conversation (bound 2026-11-04 09:30 UTC)") {
		t.Fatalf("foreign notice: %q", foreign)
	}
	if !strings.Contains(foreign, "This conversation also runs vecnet hooks for several profiles (p, q)") {
		t.Fatalf("foreign ambiguous clause: %q", foreign)
	}
	if !strings.Contains(ambiguous, "not bound to this agy conversation") || !strings.Contains(ambiguous, "the conversation runs vecnet hooks for several profiles (p, q)") {
		t.Fatalf("ambiguous notice: %q", ambiguous)
	}
}

// Nonempty stdin that cannot be decoded, or stdin over the 1 MiB cap,
// carried an identity this run cannot see: silent, state untouched.
func TestHookAgyUnusableStdinSilent(t *testing.T) {
	big, err := json.Marshal(map[string]any{"hookEventName": "PreInvocation", "conversationId": "c2", "lastUserInput": strings.Repeat("x", 1<<20)})
	if err != nil {
		t.Fatal(err)
	}
	inputs := map[string]func() io.Reader{
		"truncated": func() io.Reader { return strings.NewReader(`{"hookEventName":"PreInvocation","conversationId":"c2"`) },
		"not json":  func() io.Reader { return strings.NewReader("conversation c2\n") },
		"oversized": func() io.Reader { return bytes.NewReader(big) },
		"read error": func() io.Reader {
			return &failingReader{data: []byte(`{"hookEventName":"PreInvocation","conversationId":"c2"`)}
		},
	}
	for name, input := range inputs {
		t.Run(name, func(t *testing.T) {
			var requests atomic.Int32
			env, path := hookFixtureRaw(t, func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				t.Errorf("unexpected request: %s", r.URL)
				w.WriteHeader(500)
			})
			seedSession(t, path, boundTo("c1-root"))
			before, _ := os.ReadFile(path)
			beforeBinding, _ := os.ReadFile(bindingPath(path))
			out, errs := runAgyHook(t, env, input(), "--profile", "p", "--event", "pre-invocation")
			if out != "{}\n" || errs != "" {
				t.Fatalf("out=%q errs=%q", out, errs)
			}
			if requests.Load() != 0 {
				t.Fatalf("%d requests", requests.Load())
			}
			if after, _ := os.ReadFile(path); !bytes.Equal(before, after) {
				t.Fatal("unusable stdin changed state")
			}
			if after, _ := os.ReadFile(bindingPath(path)); !bytes.Equal(beforeBinding, after) {
				t.Fatal("unusable stdin changed the binding")
			}
			if _, err := os.Stat(sessionsPath(path)); !os.IsNotExist(err) {
				t.Fatalf("unusable stdin wrote the registry: %v", err)
			}
		})
	}
}

// The session identity falls back to ANTIGRAVITY_CONVERSATION_ID when the
// payload carries no conversationId.
func TestHookAgyEnvConversationID(t *testing.T) {
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	old := hookNow
	hookNow = func() time.Time { return now }
	t.Cleanup(func() { hookNow = old })
	env, path, _ := agyHub(t)
	seedSession(t, path, func(*hookState) {})
	conv := "0b1c2d3e-4f5a-6b7c-8d9e-0f1a2b3c4d5e"
	runAgyHook(t, env, strings.NewReader(agyStdin("PreInvocation", conv, 0, "first prompt")), "--profile", "p", "--event", "pre-invocation")
	now = now.Add(6 * time.Second)
	env["ANTIGRAVITY_CONVERSATION_ID"] = conv
	defer delete(env, "ANTIGRAVITY_CONVERSATION_ID")
	out, _ := runAgyHook(t, env, strings.NewReader(`{"hookEventName":"PreInvocation","invocationNum":2,"initialNumSteps":5,"lastUserInput":"first prompt"}`), "--profile", "p", "--event", "pre-invocation")
	if ctx := agyContextOf(t, out); !strings.Contains(ctx, "turn two") {
		t.Fatalf("env identity missed delivery: %q", ctx)
	}
	if s := readHookState(t, path); !reflect.DeepEqual(s.Shown, []string{"dly_1", "dly_2"}) {
		t.Fatalf("state=%+v", s)
	}
}

// Through a native pipe (agy's capture), the encoded output never exceeds
// the atomic write size and still parses as one systemMessage step.
func TestHookAgyPipeOutputCapped(t *testing.T) {
	long := strings.Repeat(`"界<>&`, 1200)
	env, path := hookFixture(t, func(w http.ResponseWriter, r *http.Request) {
		hookPage(w, []envelope.Event{hookEvent(1, long), hookEvent(2, long)}, "e1.2")
	})
	seedHook(t, path, "e1.0")
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	cmd := agyChild(t, "hook", env, agyStdin("PreInvocation", "c1", 0, "big turn"), "--profile p --event pre-invocation")
	var errs bytes.Buffer
	cmd.Stdout, cmd.Stderr = writer, &errs
	if err := cmd.Run(); err != nil {
		t.Fatalf("agy hook child: %v, stderr=%q", err, errs.String())
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) > hookPipeAtomicBytes {
		t.Fatalf("pipe output is %d bytes, atomic cap %d", len(raw), hookPipeAtomicBytes)
	}
	ctx := agyContextOf(t, string(raw))
	if !strings.Contains(ctx, "delivery=dly_1 ") {
		t.Fatalf("capped output lost the delivery: %q", ctx)
	}
	// Both items fit the caps (each item's text is rune-capped), and the
	// whole encoded output stayed within the atomic pipe write.
	if s := readHookState(t, path); !reflect.DeepEqual(s.Shown, []string{"dly_1", "dly_2"}) {
		t.Fatalf("capped run state=%+v", s)
	}
}

// A run whose payload carries no conversationId, with no
// ANTIGRAVITY_CONVERSATION_ID either, has no identity to bind or claim
// with: it is not admitted, whatever its invocationNum says.
func TestHookAgyNoConversationNotAdmitted(t *testing.T) {
	var requests atomic.Int32
	env, path := hookFixtureRaw(t, func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		t.Errorf("unexpected request: %s", r.URL)
		w.WriteHeader(500)
	})
	seedSession(t, path, boundTo("c1-root"))
	before, _ := os.ReadFile(path)
	beforeBinding, _ := os.ReadFile(bindingPath(path))
	for _, tc := range []struct{ name, stdin, event string }{
		{"turn-start", `{"hookEventName":"PreInvocation","invocationNum":0,"lastUserInput":"hello"}`, "pre-invocation"},
		{"mid-turn", `{"hookEventName":"PreInvocation","invocationNum":2,"lastUserInput":"hello"}`, "pre-invocation"},
		{"start", `{"hookEventName":"SessionStart"}`, "session-start"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, errs := runAgyHook(t, env, strings.NewReader(tc.stdin), "--profile", "p", "--event", tc.event)
			if out != "{}\n" || errs != "" {
				t.Fatalf("out=%q errs=%q", out, errs)
			}
		})
	}
	if requests.Load() != 0 {
		t.Fatalf("no-identity runs made %d requests", requests.Load())
	}
	if after, _ := os.ReadFile(path); !bytes.Equal(before, after) {
		t.Fatal("no-identity run changed state")
	}
	if after, _ := os.ReadFile(bindingPath(path)); !bytes.Equal(beforeBinding, after) {
		t.Fatal("no-identity run changed the binding")
	}
}

// The agy installer is not qualified on Windows: agy runs hook commands
// under cmd /c, where the installed POSIX quoting breaks. The OS gate is
// an overridable var so the refusal is testable on Linux.
func TestHookInstallAgyRefusesWindows(t *testing.T) {
	old := agyInstallOS
	agyInstallOS = "windows"
	t.Cleanup(func() { agyInstallOS = old })
	var out, errs bytes.Buffer
	code := RunContext(context.Background(), []string{"hook", "install", "--client", "agy", "--profile", "p", "--write"}, strings.NewReader(""), &out, &errs, func(string) string { return "" })
	if code != 2 || out.String() != "" || !strings.Contains(errs.String(), "hook install: agy hooks are not qualified on Windows yet") {
		t.Fatalf("exit=%d out=%q errs=%q", code, out.String(), errs.String())
	}
}

// Claude and Codex silence is byte-identical to before: only agy answers {}
// on a silent run.
func TestHookAgySilenceKeepsClaudeCodexBytes(t *testing.T) {
	env, _ := hookFixture(t, func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected request: %s", r.URL)
		w.WriteHeader(500)
	})
	for _, tc := range []struct {
		name  string
		args  []string
		stdin string
		want  string
	}{
		{"claude session-start", []string{"hook", "--profile", "p", "--event", "session-start"}, `{"hook_event_name":"SessionStart","session_id":"s1"}`, ""},
		{"claude unknown event", []string{"hook", "--profile", "p", "--event", "bogus"}, sessionStdin("s1"), ""},
		{"claude silent client", []string{"hook", "--client", "claude", "--profile", "p", "--event", "bogus"}, sessionStdin("s1"), ""},
		{"codex client", []string{"hook", "--client", "codex", "--profile", "p"}, sessionStdin("s1"), ""},
		{"agy silent", []string{"hook", "--client", "agy", "--profile", "p", "--event", "session-start"}, agyStartStdin("c1"), "{}\n"},
		{"agy bad flag", []string{"hook", "--client", "agy", "--nope"}, agyStartStdin("c1"), "{}\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, errs := runHookStdin(t, env, tc.stdin, tc.args...)
			if out != tc.want {
				t.Fatalf("out=%q, want %q (errs=%q)", out, tc.want, errs)
			}
		})
	}
}

// agyHookOutput answers {} for no context and exactly one systemMessage
// inject step otherwise; the worst hints and a maximal hostile item stay
// within the atomic pipe cap.
func TestHookAgyOutputShape(t *testing.T) {
	if got := string(agyHookOutput("")); got != "{}\n" {
		t.Fatalf("silent agy output=%q", got)
	}
	context := `a&amp;b <x> "q" 界 😀`
	want := `{"injectSteps":[{"systemMessage":{"systemMessage":"a&amp;b <x> \"q\" 界 😀"}}]}` + "\n"
	if got := string(agyHookOutput(context)); got != want {
		t.Fatalf("agy output=%q, want %q", got, want)
	}
	if got := agyContextOf(t, string(agyHookOutput(context))); got != context {
		t.Fatalf("agy output does not round-trip: %q", got)
	}
	long := func(s string) string { return strings.Repeat(s, 4*hookTextRunes) }
	p24 := strings.Repeat("p", 24)
	for _, text := range []string{`"`, "&", "<", "＜", "界", "😀", "‮"} {
		id := strings.Repeat("x", 80)
		item := hookItem{Cursor: id, Thread: id, From: id, Name: long(text), Message: id, Delivery: id, Kind: strings.Repeat("x", 32), Text: long(text), Attachments: 1 << 30}
		for _, hint := range []hookHint{
			newHookHint(long(text), long(text), "/x/"+long(text)),
			newHookHintMCP(p24, "", "/x/"+long(text), "vecnet-"+p24),
		} {
			for _, context := range []string{hookContext(hint, []hookItem{item}, hookPageLimit, true), hookSummary(hint, 1<<30, true)} {
				if n := utf16Len(context); n > hookMaxUnits {
					t.Fatalf("text %q: agy context is %d UTF-16 units, cap %d", text, n, hookMaxUnits)
				}
				out := agyHookOutput(context)
				if len(out) > hookPipeAtomicBytes {
					t.Fatalf("text %q: agy output is %d bytes, cap %d", text, len(out), hookPipeAtomicBytes)
				}
				if got := agyContextOf(t, string(out)); got != context {
					t.Fatalf("text %q: agy output does not round-trip", text)
				}
			}
		}
	}
}

// agy runs never touch Claude's passive hook registration; the Claude path
// still does.
func TestHookAgyNeverRegistersClaude(t *testing.T) {
	var calls atomic.Int32
	old := hookClaudeRegister
	hookClaudeRegister = func(*app, context.Context, string, []byte) localSetupResult { calls.Add(1); return localSetupResult{} }
	t.Cleanup(func() { hookClaudeRegister = old })
	env, path, _ := agyHub(t)
	seedSession(t, path, boundTo("c1-root"))
	conv := "0b1c2d3e-4f5a-6b7c-8d9e-0f1a2b3c4d5e"
	runAgyHook(t, env, strings.NewReader(agyStartStdin(conv)), "--profile", "p", "--event", "session-start")
	runAgyHook(t, env, strings.NewReader(agyStdin("PreInvocation", conv, 0, "hi")), "--profile", "p", "--event", "pre-invocation")
	runAgyHook(t, env, strings.NewReader(agyStdin("PreInvocation", conv, 1, "hi")), "--profile", "p", "--event", "pre-invocation")
	if calls.Load() != 0 {
		t.Fatalf("agy runs called claude registration %d times", calls.Load())
	}
	out, _ := runHookStdin(t, env, sessionStdin("c1-root"), "hook", "--profile", "p", "--event", "user-prompt")
	if calls.Load() == 0 {
		t.Fatal("claude run skipped claude registration")
	}
	if got := contextOf(t, out); !strings.Contains(got, "turn one") {
		t.Fatalf("claude run output=%q", out)
	}
}
