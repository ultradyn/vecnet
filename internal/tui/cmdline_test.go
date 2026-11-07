package tui

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/opapi"
	"github.com/XertroV/vecnet/internal/opclient"
)

// cmdAPI records what fakeAPI drops: which dead letters were retried, in what
// order and how many at once, and the presence reports.
type cmdAPI struct {
	*fakeAPI
	mu      sync.Mutex
	retried []string
	active  int
	peak    int
	failing map[string]error
	states  []string
	ats     []time.Time
}

func (a *cmdAPI) RetryDeadLetter(ctx context.Context, dly string) error {
	a.mu.Lock()
	a.retried = append(a.retried, dly)
	a.active++
	a.peak = max(a.peak, a.active)
	a.mu.Unlock()
	err := a.fakeAPI.RetryDeadLetter(ctx, dly)
	a.mu.Lock()
	defer a.mu.Unlock()
	a.active--
	if failed := a.failing[dly]; failed != nil {
		return failed
	}
	return err
}

func (a *cmdAPI) SetPresence(ctx context.Context, state string, at time.Time) error {
	a.mu.Lock()
	a.states = append(a.states, state)
	a.ats = append(a.ats, at)
	a.mu.Unlock()
	return a.fakeAPI.SetPresence(ctx, state, at)
}

func (a *cmdAPI) attempts() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return slices.Clone(a.retried)
}

func (a *cmdAPI) reports() ([]string, []time.Time) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return slices.Clone(a.states), slices.Clone(a.ats)
}

func cmdModel(t *testing.T, clock *testClock) (*Model, *pump, *cmdAPI) {
	t.Helper()
	f := seeded()
	m, p := loaded(t, f, clock)
	api := &cmdAPI{fakeAPI: f, failing: map[string]error{}}
	m.API = api
	return m, p, api
}

// typeText sends text one key at a time, as a terminal does.
func typeText(p *pump, text string) {
	p.t.Helper()
	for _, r := range text {
		p.send(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
}

func keyTab(p *pump)   { p.t.Helper(); p.send(tea.KeyPressMsg{Code: tea.KeyTab}) }
func keyUp(p *pump)    { p.t.Helper(); p.send(tea.KeyPressMsg{Code: tea.KeyUp}) }
func keyDown(p *pump)  { p.t.Helper(); p.send(tea.KeyPressMsg{Code: tea.KeyDown}) }
func keyEnter(p *pump) { p.t.Helper(); p.send(tea.KeyPressMsg{Code: tea.KeyEnter}) }

// submit opens the command line, types line and runs it.
func submit(p *pump, line string) {
	p.t.Helper()
	p.press(":")
	typeText(p, line)
	keyEnter(p)
}

// bottomLine is the frame's command line, without its padding.
func bottomLine(t *testing.T, m *Model) string {
	t.Helper()
	lines := strings.Split(plainAt(t, m, 140, 42), "\n")
	return strings.TrimRight(lines[len(lines)-1], " ")
}

func wantInput(t *testing.T, m *Model, want string) {
	t.Helper()
	if m.Input != want {
		t.Fatalf("Input = %q, want %q", m.Input, want)
	}
}

func TestParse(t *testing.T) {
	for _, tc := range []struct {
		line, name string
		args       []string
	}{
		{line: ""},
		{line: "    "},
		{line: "\t "},
		{line: "q", name: "q"},
		{line: "  refresh", name: "refresh"},
		{line: "refresh   ", name: "refresh"},
		{line: "   retry   all  ", name: "retry", args: []string{"all"}},
		{line: "presence\tworking", name: "presence", args: []string{"working"}},
		// No quoting: quotes and backslashes are ordinary characters.
		{line: `msg "two words" 'x y' a\ b`, name: "msg", args: []string{`"two`, `words"`, `'x`, `y'`, `a\`, "b"}},
	} {
		name, args := parseLine(tc.line)
		if name != tc.name || !slices.Equal(args, tc.args) {
			t.Errorf("parseLine(%q) = %q %q, want %q %q", tc.line, name, args, tc.name, tc.args)
		}
	}
}

func TestRetryAll(t *testing.T) {
	summary := func(p *pump, m *Model) {
		p.t.Helper()
		p.until("the summary", func() bool { return strings.Contains(m.toast.text, " failed") })
	}
	t.Run("one failure", func(t *testing.T) {
		m, p, api := cmdModel(t, newClock())
		api.failing["dly_2"] = errors.New("hub said no")
		submit(p, "retry all")
		summary(p, m)
		if got, want := api.attempts(), []string{"dly_1", "dly_2", "dly_3"}; !slices.Equal(got, want) {
			t.Fatalf("attempts = %v, want %v", got, want)
		}
		if api.peak != 1 {
			t.Fatalf("%d retries ran at once, want one at a time", api.peak)
		}
		if line := bottomLine(t, m); line != "2 ok, 1 failed" {
			t.Fatalf("command line = %q", line)
		}
		if !m.toast.bad {
			t.Fatal("a summary with a failure must read as bad")
		}
	})
	t.Run("all ok reloads what changed", func(t *testing.T) {
		m, p, api := cmdModel(t, newClock())
		api.reset()
		submit(p, "retry all")
		summary(p, m)
		if line := bottomLine(t, m); line != "3 ok, 0 failed" || m.toast.bad {
			t.Fatalf("command line = %q, bad %v", line, m.toast.bad)
		}
		p.loaded(api.fakeAPI, "Inbox", "DeliveryHealth")
		if line := bottomLine(t, m); line != "3 ok, 0 failed" {
			t.Fatalf("the reload replaced the summary: %q", line)
		}
	})
	t.Run("nothing landed keeps the summary on screen", func(t *testing.T) {
		m, p, api := cmdModel(t, newClock())
		for _, id := range []string{"dly_1", "dly_2", "dly_3"} {
			api.failing[id] = &opclient.Error{Status: 503, Code: "unavailable", Message: "down"}
		}
		api.reset()
		api.fail("Inbox", errors.New("down")) // a reload now would toast over the summary
		submit(p, "retry all")
		summary(p, m)
		p.idle()
		if line := bottomLine(t, m); line != "0 ok, 3 failed" {
			t.Fatalf("command line = %q", line)
		}
		if n := api.count("Inbox"); n != 0 {
			t.Fatalf("%d inbox loads after nothing changed", n)
		}
	})
	t.Run("running shows progress and refuses a second run", func(t *testing.T) {
		clock := newClock()
		m, p, api := cmdModel(t, clock)
		release := api.hold("RetryDeadLetter")
		submit(p, "retry all")
		p.until("the first attempt", func() bool { return len(api.attempts()) == 1 })
		if line := bottomLine(t, m); line != "retrying dead letters: 3" {
			t.Fatalf("command line = %q", line)
		}
		// A hub that takes its time must not make the run look finished.
		clock.advance(10 * time.Second)
		p.send(tickMsg{})
		if line := bottomLine(t, m); line != "retrying dead letters: 3" {
			t.Fatalf("after 10 s: command line = %q", line)
		}
		submit(p, "retry all")
		if line := bottomLine(t, m); line != "retry all is already running" {
			t.Fatalf("second run: command line = %q", line)
		}
		// The refusal passes and the progress line comes back.
		clock.advance(toastBad + time.Second)
		p.send(tickMsg{})
		if line := bottomLine(t, m); line != "retrying dead letters: 3" {
			t.Fatalf("after the refusal: command line = %q", line)
		}
		release()
		summary(p, m)
		if got := len(api.attempts()); got != 3 {
			t.Fatalf("%d attempts, want 3: the second run must not have started", got)
		}
		p.idle()
		submit(p, "retry all") // the guard ends with the run
		p.until("the next run", func() bool { return len(api.attempts()) == 6 })
		p.until("the summary", func() bool { return strings.Contains(m.toast.text, " failed") })
	})
	t.Run("summary stays as long as an error", func(t *testing.T) {
		clock := newClock()
		m, p, _ := cmdModel(t, clock)
		submit(p, "retry all")
		summary(p, m)
		clock.advance(toastGood + time.Second)
		p.send(tickMsg{})
		if line := bottomLine(t, m); line != "3 ok, 0 failed" {
			t.Fatalf("summary gone after %v: %q", toastGood+time.Second, line)
		}
		clock.advance(toastBad)
		p.send(tickMsg{})
		if line := bottomLine(t, m); line == "3 ok, 0 failed" {
			t.Fatal("summary never expired")
		}
	})
	t.Run("a rejected credential stops the run", func(t *testing.T) {
		m, p, api := cmdModel(t, newClock())
		api.failing["dly_1"] = &opclient.Error{Status: 401, Code: "unauthenticated", Message: "no"}
		submit(p, "retry all")
		summary(p, m)
		if got := api.attempts(); len(got) != 1 {
			t.Fatalf("attempts after 401 = %v, want one", got)
		}
		if m.Conn != ConnRejected {
			t.Fatalf("Conn = %q", m.Conn)
		}
		if line := bottomLine(t, m); line != "0 ok, 3 failed" {
			t.Fatalf("command line = %q", line)
		}
		submit(p, "retry all")
		if got := api.attempts(); len(got) != 1 {
			t.Fatalf("a stopped client retried: %v", got)
		}
		if line := bottomLine(t, m); !strings.Contains(line, ConnRejected) {
			t.Fatalf("command line = %q", line)
		}
	})
	t.Run("a forbidden credential stops the run without rejecting it", func(t *testing.T) {
		m, p, api := cmdModel(t, newClock())
		api.failing["dly_1"] = &opclient.Error{Status: 403, Code: "forbidden", Message: "not the operator"}
		submit(p, "retry all")
		summary(p, m)
		if got := api.attempts(); len(got) != 1 {
			t.Fatalf("attempts after 403 = %v, want one", got)
		}
		if m.Conn != ConnLive {
			t.Fatalf("Conn = %q: a 403 does not reject the credential", m.Conn)
		}
	})
	t.Run("nothing listed", func(t *testing.T) {
		m, p, api := cmdModel(t, newClock())
		m.Data.Inbox.DeadLetters = nil
		submit(p, "retry all")
		if got := api.attempts(); len(got) != 0 {
			t.Fatalf("attempts = %v", got)
		}
		if line := bottomLine(t, m); line != "no dead letters to retry" {
			t.Fatalf("command line = %q", line)
		}
	})
	t.Run("only all", func(t *testing.T) {
		for _, line := range []string{"retry", "retry dly_1", "retry all now"} {
			m, p, api := cmdModel(t, newClock())
			submit(p, line)
			if got := api.attempts(); len(got) != 0 {
				t.Fatalf("%s: attempts = %v", line, got)
			}
			if got := bottomLine(t, m); got != "usage: :retry all" {
				t.Fatalf("%s: command line = %q", line, got)
			}
		}
	})
}

// A non-operator hub refusal on :peer archive must surface as an error and
// must not toast success. The fake stands in for the hub's RequirePeerLifecycle.
func TestPeerLifecycleForbidden(t *testing.T) {
	m, p, api := cmdModel(t, newClock())
	m.API = &lifecycleFailAPI{fakeAPI: api.fakeAPI, err: &opclient.Error{Status: 403, Code: "forbidden", Message: "operator credential required"}}
	submit(p, "peer archive peer_target")
	p.until("the refusal", func() bool { return strings.Contains(m.toast.text, "forbidden") })
	if !m.toast.bad || strings.Contains(m.toast.text, "complete") {
		t.Fatalf("toast = %q bad=%v, want a forbidden error and no success", m.toast.text, m.toast.bad)
	}
	if m.Conn != ConnLive {
		t.Fatalf("Conn = %q, want live: a lifecycle 403 is not a rejected operator credential", m.Conn)
	}
}

type lifecycleFailAPI struct {
	*fakeAPI
	err error
}

func (a *lifecycleFailAPI) PeerLifecycle(context.Context, string, string) error { return a.err }

func TestCompletionAndHistory(t *testing.T) {
	t.Run("tab completes a unique name", func(t *testing.T) {
		m, p, _ := cmdModel(t, newClock())
		p.press(":", "t", "h")
		keyTab(p)
		wantInput(t, m, "thread ")
		keyTab(p) // past the name there is nothing to complete
		wantInput(t, m, "thread ")
	})
	t.Run("tab cycles several names in order", func(t *testing.T) {
		m, p, _ := cmdModel(t, newClock())
		p.press(":", "r", "e")
		for _, want := range []string{"refresh", "retry", "refresh"} {
			keyTab(p)
			wantInput(t, m, want)
		}
	})
	t.Run("typing ends the cycle", func(t *testing.T) {
		m, p, _ := cmdModel(t, newClock())
		p.press(":", "r", "e")
		keyTab(p)
		wantInput(t, m, "refresh")
		for range "refresh" {
			p.send(tea.KeyPressMsg{Code: tea.KeyBackspace})
		}
		p.press("t")
		keyTab(p)
		wantInput(t, m, "tab")
		keyTab(p)
		wantInput(t, m, "thread")
	})
	t.Run("tab finds every registered command", func(t *testing.T) {
		m, p, _ := cmdModel(t, newClock())
		m.Commands["msg"] = Command{Help: "<peer> <text>  new thread"}
		p.press(":", "m", "s")
		keyTab(p)
		wantInput(t, m, "msg ")
		if _, ok := m.Commands["msg"]; !ok {
			t.Fatal("another task's command was dropped")
		}
	})
	t.Run("no match changes nothing", func(t *testing.T) {
		m, p, _ := cmdModel(t, newClock())
		p.press(":", "z", "z")
		keyTab(p)
		wantInput(t, m, "zz")
	})
	t.Run("up and down walk history", func(t *testing.T) {
		m, p, _ := cmdModel(t, newClock())
		submit(p, "tab chat")
		submit(p, "tab subs")
		p.press(":", "d")
		keyUp(p)
		wantInput(t, m, "tab subs")
		keyUp(p)
		wantInput(t, m, "tab chat")
		keyUp(p) // the oldest stays
		wantInput(t, m, "tab chat")
		keyDown(p)
		wantInput(t, m, "tab subs")
		keyDown(p) // past the newest is what was being typed
		wantInput(t, m, "d")
		keyDown(p)
		wantInput(t, m, "d")
		p.press("esc")
		p.press(":")
		wantInput(t, m, "")
		keyUp(p) // history outlives leaving the line
		wantInput(t, m, "tab subs")
		keyEnter(p)
		if m.Tab != tabSubs {
			t.Fatalf("recalled line did not run: Tab = %d", m.Tab)
		}
	})
	t.Run("history skips empty lines and repeats", func(t *testing.T) {
		m, p, _ := cmdModel(t, newClock())
		submit(p, "tab chat")
		submit(p, "  tab chat  ")
		submit(p, "   ")
		p.press(":")
		keyUp(p)
		wantInput(t, m, "tab chat")
		keyUp(p)
		wantInput(t, m, "tab chat")
		keyDown(p)
		wantInput(t, m, "")
	})
	t.Run("history is bounded", func(t *testing.T) {
		m, p, _ := cmdModel(t, newClock())
		for i := 0; i < maxHistory+10; i++ {
			submit(p, "nope"+strings.Repeat("x", i))
		}
		p.press(":")
		for i := 0; i < maxHistory+10; i++ {
			keyUp(p)
		}
		wantInput(t, m, "nope"+strings.Repeat("x", 10))
	})
	t.Run("two models keep their own history", func(t *testing.T) {
		_, p, _ := cmdModel(t, newClock())
		submit(p, "tab chat")
		other, q, _ := cmdModel(t, newClock())
		q.press(":")
		keyUp(q)
		wantInput(t, other, "")
	})
}

func TestUnknownCommand(t *testing.T) {
	m, p, api := cmdModel(t, newClock())
	before := slices.Clone(m.Data.Inbox.DeadLetters)
	api.reset()
	submit(p, "frobnicate now")
	if line := bottomLine(t, m); line != "unknown command: frobnicate" {
		t.Fatalf("command line = %q", line)
	}
	if !m.toast.bad {
		t.Fatal("an unknown command must read as an error")
	}
	if m.Mode != ModeNormal || m.Tab != tabInbox || m.Help || m.Input != "" || m.CurrentThread != "" {
		t.Fatalf("state changed: mode %v tab %d help %v input %q thread %q", m.Mode, m.Tab, m.Help, m.Input, m.CurrentThread)
	}
	p.idle()
	api.mu.Lock()
	calls := len(api.calls)
	api.mu.Unlock()
	if calls != 0 || len(api.attempts()) != 0 {
		t.Fatalf("API touched: %v", api.calls)
	}
	if !slices.EqualFunc(before, m.Data.Inbox.DeadLetters, func(a, b opapi.DeadLetter) bool { return a.DeliveryID == b.DeliveryID }) {
		t.Fatal("data changed")
	}

	t.Run("hostile input stays inert", func(t *testing.T) {
		m, p, _ := cmdModel(t, newClock())
		p.press(":")
		p.send(tea.PasteMsg{Content: "\x1b]52;c;aGk=\x07zz"})
		p.send(tea.KeyPressMsg{Code: 'y', Text: "\x1b[2Jy\u202e"})
		if strings.ContainsAny(m.Input, "\x1b\x07\u202e") {
			t.Fatalf("Input kept controls: %q", m.Input)
		}
		keyEnter(p)
		if strings.ContainsAny(m.toast.text, "\x1b\x07\u202e") || !strings.HasPrefix(m.toast.text, "unknown command: ") {
			t.Fatalf("toast = %q", m.toast.text)
		}
	})
}

func TestHelpGolden(t *testing.T) {
	m, p, _ := cmdModel(t, newClock())
	p.press("?")
	frame := plainAt(t, m, 140, 42)
	assertFrame(t, frame, 140, 42)
	path := filepath.Join("testdata", "help.golden")
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.WriteFile(path, []byte(frame), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden (run with UPDATE_GOLDEN=1 once the frame is right): %v", err)
	}
	if !bytes.Equal(want, []byte(frame)) {
		t.Errorf("frame differs from %s\n--- got ---\n%s\n--- want ---\n%s", path, frame, want)
	}
	for _, text := range []string{
		"1-4", "switch tab", "q", "quit", "command line", "ctrl+l", "reload everything", // global keys
		"j/k move  a answer", // the current screen
		":prefs", "show read-only hub preferences",
		":presence <idle|working|blocked|offline>", ":q", ":refresh", ":retry all", ":tab <name>", ":thread <n>",
	} {
		if !strings.Contains(frame, text) {
			t.Errorf("help lacks %q:\n%s", text, frame)
		}
	}

	t.Run("follows the current screen", func(t *testing.T) {
		m, p, _ := cmdModel(t, newClock())
		p.press("2", "?")
		frame := plainAt(t, m, 140, 42)
		if !strings.Contains(frame, "i write") || strings.Contains(frame, "j/k move") {
			t.Fatalf("hints are not the chat screen's:\n%s", frame)
		}
	})
	t.Run("lists every registered command", func(t *testing.T) {
		m, p, _ := cmdModel(t, newClock())
		p.press("?")
		m.Commands["msg"] = Command{Help: "<peer-prefix> <text>  start a thread"}
		m.Commands["bare"] = Command{Help: "help with no usage part"}
		frame := plainAt(t, m, 140, 42)
		for _, text := range []string{":msg <peer-prefix> <text>", "start a thread", ":bare", "help with no usage part"} {
			if !strings.Contains(frame, text) {
				t.Errorf("help lacks %q:\n%s", text, frame)
			}
		}
	})
	t.Run("fits the smallest terminal", func(t *testing.T) {
		m, p, _ := cmdModel(t, newClock())
		m.screens[0] = &staticScreen{title: "inbox", hints: "j/k move  enter open  a answer  r retry  d discard  x dismiss  u unread only  f filter by peer  s sort"}
		m.Commands["msg"] = Command{Help: "<peer-prefix> <text>  start a thread"}
		p.press("?")
		frame := plainAt(t, m, 80, 24)
		assertFrame(t, frame, 80, 24)
		for _, text := range []string{
			"ctrl+l", "j/k move", "s sort", ":q", ":refresh", ":tab", ":thread",
			":msg <peer-prefix> <text>", "start a thread", // whole lines, not cut at the border
			":presence <idle|working|blocked|offline>  set your presence",
			"retry every listed dead letter", "nth thread in the chat list",
		} {
			if !strings.Contains(frame, text) {
				t.Errorf("help lacks %q at 80x24:\n%s", text, frame)
			}
		}
	})
}

func TestCommands(t *testing.T) {
	t.Run("q", func(t *testing.T) {
		m, p, _ := cmdModel(t, newClock())
		submit(p, "q")
		if m.ctx.Err() == nil {
			t.Fatal(":q did not quit")
		}
	})
	t.Run("refresh", func(t *testing.T) {
		m, p, api := cmdModel(t, newClock())
		api.reset()
		submit(p, "refresh")
		p.loaded(api.fakeAPI, "Inbox", "Threads", "Peers", "Subscriptions", "Audit")
		m.Conn = ConnRejected
		api.reset()
		submit(p, "refresh")
		if line := bottomLine(t, m); !strings.Contains(line, ConnRejected) || api.count("Inbox") != 0 {
			t.Fatalf("stopped client: %q", line)
		}
	})
	t.Run("presence", func(t *testing.T) {
		clock := newClock()
		m, p, api := cmdModel(t, clock)
		hubAhead := 5 * time.Minute
		m.noteHubNow(envelope.FormatTime(fixedNow.Add(hubAhead)), fixedNow)
		api.reset()
		submit(p, "presence blocked")
		p.until("the report", func() bool { return m.toast.text != "" })
		states, ats := api.reports()
		if !slices.Equal(states, []string{"blocked"}) {
			t.Fatalf("states = %v", states)
		}
		if !ats[0].Equal(fixedNow.Add(hubAhead)) {
			t.Fatalf("at = %v, want the hub's clock", ats[0])
		}
		if line := bottomLine(t, m); line != "presence blocked" || m.toast.bad {
			t.Fatalf("command line = %q", line)
		}
		// The status line reads Inbox.Me, and no event refreshes that.
		p.loaded(api.fakeAPI, "Inbox", "Peers")
	})
	t.Run("presence refuses other states", func(t *testing.T) {
		for _, line := range []string{"presence", "presence asleep", "presence idle now", "presence unknown"} {
			m, p, api := cmdModel(t, newClock())
			submit(p, line)
			if states, _ := api.reports(); len(states) != 0 {
				t.Fatalf("%s: sent %v", line, states)
			}
			if got := bottomLine(t, m); got != "usage: :presence <idle|working|blocked|offline>" {
				t.Fatalf("%s: command line = %q", line, got)
			}
		}
	})
	t.Run("presence failure", func(t *testing.T) {
		m, p, api := cmdModel(t, newClock())
		api.reset()
		api.fail("SetPresence", errors.New("hub down"))
		submit(p, "presence idle")
		p.until("the error", func() bool { return m.toast.text != "" })
		if line := bottomLine(t, m); line != "presence: hub down" || !m.toast.bad {
			t.Fatalf("command line = %q", line)
		}
		if api.count("Inbox") != 0 {
			t.Fatal("reloaded after a failed report")
		}
	})
	t.Run("presence with a rejected credential", func(t *testing.T) {
		m, p, api := cmdModel(t, newClock())
		api.fail("SetPresence", &opclient.Error{Status: 401, Code: "unauthenticated", Message: "no"})
		submit(p, "presence idle")
		p.until("the rejection", func() bool { return m.Conn == ConnRejected })
		submit(p, "presence working")
		// The refusal is synchronous; a send would show up only later, so wait
		// for the refusal and the loads before counting.
		if line := bottomLine(t, m); !strings.Contains(line, ConnRejected) {
			t.Fatalf("command line = %q", line)
		}
		p.idle()
		if api.count("SetPresence") != 1 {
			t.Fatal("a stopped client reported presence")
		}
	})
	t.Run("thread", func(t *testing.T) {
		m, p, api := cmdModel(t, newClock())
		m.Data.Threads.Threads = []opapi.ThreadRow{
			{Thread: envelope.ThreadSummary{ThreadID: "thr_1"}},
			{Thread: envelope.ThreadSummary{ThreadID: "thr_2"}},
		}
		api.reset()
		submit(p, "thread 2")
		if m.Tab != tabChat || m.CurrentThread != "thr_2" {
			t.Fatalf("tab %d thread %q", m.Tab, m.CurrentThread)
		}
		p.loaded(api.fakeAPI, "Thread")
		for line, want := range map[string]string{
			"thread 3":  "no thread 3: the chat list has 2",
			"thread 0":  "no thread 0: the chat list has 2",
			"thread -1": "no thread -1: the chat list has 2",
			"thread":    "usage: :thread <n>",
			"thread x":  "usage: :thread <n>",
		} {
			m.CurrentThread = "thr_2"
			submit(p, line)
			if got := bottomLine(t, m); got != want || m.CurrentThread != "thr_2" {
				t.Errorf("%s: command line = %q, thread %q", line, got, m.CurrentThread)
			}
		}
	})
	t.Run("tab", func(t *testing.T) {
		m, p, _ := cmdModel(t, newClock())
		for i, name := range []string{"inbox", "chat", "dash", "subs"} {
			submit(p, "tab "+name)
			if m.Tab != i {
				t.Fatalf("tab %s: Tab = %d", name, m.Tab)
			}
		}
		submit(p, "tab nope")
		if line := bottomLine(t, m); line != "unknown tab: nope" || m.Tab != tabSubs {
			t.Fatalf("command line = %q, tab %d", line, m.Tab)
		}
		submit(p, "tab")
		if line := bottomLine(t, m); line != "usage: :tab <name>" {
			t.Fatalf("command line = %q", line)
		}
	})
}
