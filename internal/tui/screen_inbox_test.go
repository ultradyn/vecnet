package tui

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
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

// inboxAPI is the shared fake plus a record of what each write was called with.
// fake_test.go only counts calls, and these tests care which row a key hit.
type inboxAPI struct {
	*fakeAPI
	writes sync.Mutex
	// answered, read, retried, discarded, resumed and presence hold arguments
	// in call order; an answer is "<approval id>/<option>".
	answered, read, retried, discarded, resumed, presence []string
	presenceAt                                            []time.Time
	// onAnswer and onPresence change the hub's state as the real write would.
	onAnswer   func(id string)
	onPresence func(state string)
	// readEarly makes a held load return what the hub held when it started, as
	// a request the network holds does, rather than what it holds on release.
	readEarly bool
}

func (f *inboxAPI) note(list *[]string, value string) {
	f.writes.Lock()
	defer f.writes.Unlock()
	*list = append(*list, value)
}

func (f *inboxAPI) calls(list *[]string) []string {
	f.writes.Lock()
	defer f.writes.Unlock()
	return slices.Clone(*list)
}

// Inbox waits on its gate before it reads, so a held load returns what the hub
// holds on release, unless readEarly is set.
func (f *inboxAPI) Inbox(ctx context.Context, _ opapi.InboxQuery) (opapi.Inbox, error) {
	read := func() opapi.Inbox {
		f.mu.Lock()
		defer f.mu.Unlock()
		return f.inbox
	}
	if f.readEarly {
		snapshot := read()
		return snapshot, f.hit(ctx, "Inbox")
	}
	err := f.hit(ctx, "Inbox")
	return read(), err
}

func (f *inboxAPI) setInbox(change func(*opapi.Inbox)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	change(&f.inbox)
}

func (f *inboxAPI) dropApproval(id string) {
	f.setInbox(func(in *opapi.Inbox) {
		in.Approvals = slices.DeleteFunc(slices.Clone(in.Approvals), func(a opapi.OpenApproval) bool { return a.ApprovalID == id })
	})
}

func (f *inboxAPI) dropDeadLetter(id string) {
	f.setInbox(func(in *opapi.Inbox) {
		in.DeadLetters = slices.DeleteFunc(slices.Clone(in.DeadLetters), func(d opapi.DeadLetter) bool { return d.DeliveryID == id })
	})
}

func (f *inboxAPI) Answer(ctx context.Context, a opapi.OpenApproval, option int) error {
	f.note(&f.answered, fmt.Sprintf("%s/%d", a.ApprovalID, option))
	err := f.hit(ctx, "Answer")
	if err == nil && f.onAnswer != nil {
		f.onAnswer(a.ApprovalID)
	}
	return err
}

func (f *inboxAPI) ReadThread(ctx context.Context, id string) (opapi.ReadResult, error) {
	f.note(&f.read, id)
	return opapi.ReadResult{Marked: 1}, f.hit(ctx, "ReadThread")
}

func (f *inboxAPI) RetryDeadLetter(ctx context.Context, id string) error {
	f.note(&f.retried, id)
	return f.hit(ctx, "RetryDeadLetter")
}

func (f *inboxAPI) DiscardDeadLetter(ctx context.Context, id string) error {
	f.note(&f.discarded, id)
	return f.hit(ctx, "DiscardDeadLetter")
}

func (f *inboxAPI) ResumeSubscription(ctx context.Context, id string) error {
	f.note(&f.resumed, id)
	return f.hit(ctx, "ResumeSubscription")
}

func (f *inboxAPI) SetPresence(ctx context.Context, state string, at time.Time) error {
	f.writes.Lock()
	f.presence = append(f.presence, state)
	f.presenceAt = append(f.presenceAt, at)
	f.writes.Unlock()
	err := f.hit(ctx, "SetPresence")
	if err == nil && f.onPresence != nil {
		f.onPresence(state)
	}
	return err
}

// Row order in inboxSeed, as counts of j from the top.
const (
	rowApr1 = iota
	rowApr2
	rowDlq1
	rowDlq2
	rowDlq3
	rowSubAuth
	rowSubToken
	rowSubFailed
	rowSubOperator
	rowWake
	rowHold1
	rowHold2
)

func inboxSeed() *inboxAPI {
	f := &inboxAPI{fakeAPI: newFake()}
	at := func(d time.Duration) string { return envelope.FormatTime(fixedNow.Add(d)) }
	code := func(n int) *int { return &n }
	note := func(s string) *string { return &s }
	f.peers = []opapi.PeerRow{
		{Peer: opapi.Peer{PeerID: "peer_dot", Name: "dot-research", Kind: "dot"}},
		{Peer: opapi.Peer{PeerID: "peer_claw", Name: "claw-ops", Kind: "openclaw"}},
		{Peer: opapi.Peer{PeerID: "peer_triage", Name: "grok-triage", Kind: "grokbot"}},
		{Peer: opapi.Peer{PeerID: "peer_codex", Name: "codex-hark", Kind: "session"}},
		{Peer: opapi.Peer{PeerID: "peer_sleuth", Name: "pag-hark-sleuth", Kind: "pagchat-bot"}},
		{Peer: opapi.Peer{PeerID: "peer_op", Name: "Max", Kind: "operator"}},
	}
	f.inbox = opapi.Inbox{
		Now: at(0),
		Me:  opapi.Me{PeerID: "peer_op", Name: "Max", Presence: "working"},
		Approvals: []opapi.OpenApproval{
			{ApprovalID: "apr_1", ThreadID: "thr_quota", ThreadTitle: "Hosting cost deep dive", MessageID: "msg_01JD3S6G8J0M2P4R6T8V0X2Z4B", Requester: "peer_dot", Question: "Spend about 20% of my weekly quota on a deep-research run?", Text: "A thorough comparison needs a deep-research run. That costs about 20% of my weekly quota (35% left).", ExpiresAt: at(11*time.Minute + 39*time.Second), Options: []string{"yes", "no"}},
			{ApprovalID: "apr_2", ThreadID: "thr_hark", ThreadTitle: "hark B196: radio end-phrase bug", MessageID: "msg_01JD3S6G8J0M2P4R6T8V0X2Z4C", Requester: "peer_claw", Question: "Push classifier fix to hark master and release?", Text: "Push classifier fix to hark master and release?", ExpiresAt: at(18*time.Minute + 59*time.Second), Options: []string{"yes", "no", "yes but no release"}},
		},
		DeadLetters: []opapi.DeadLetter{
			{DeliveryID: "dly_1", MessageID: "msg_d1", ThreadID: "thr_d1", Recipient: "peer_triage", Sender: "peer_op", Excerpt: "Scan the overnight tickets.", Class: "retries_exhausted", UpdatedAt: at(-3*time.Hour - 30*time.Minute), Attempt: 8, LastStatus: code(503), LastError: note("HTTP 503 after 8/8 attempts")},
			{DeliveryID: "dly_2", MessageID: "msg_d2", ThreadID: "thr_d2", Recipient: "peer_triage", Sender: "peer_op", Excerpt: "Summarise the release notes.", Class: "terminal", UpdatedAt: at(-3*time.Hour - 11*time.Minute), Attempt: 1, LastStatus: code(410), LastError: note("HTTP 410 Gone")},
			{DeliveryID: "dly_3", MessageID: "msg_d3", ThreadID: "thr_d3", Recipient: "peer_dot", Sender: "peer_op", Excerpt: "Check the callback.", Class: "network", UpdatedAt: at(-23 * time.Minute), Attempt: 8, LastError: note("connection refused")},
		},
		Attention: []opapi.Attention{
			{Kind: "subscription", ID: "sub_auth", PeerID: "peer_dot", Health: opapi.HealthPausedAuth, Summary: "dot-research · The peer was disabled or moved.", At: at(-time.Minute)},
			{Kind: "subscription", ID: "sub_token", PeerID: "peer_dot", Health: opapi.HealthPausedToken, Summary: "dot-research · Waiting for ChatGPT to re-subscribe.", At: at(-time.Minute)},
			{Kind: "subscription", ID: "sub_failed", PeerID: "peer_dot", Health: opapi.HealthPausedFailed, Summary: "dot-research · Fix the callback, then resume.", Action: "resume", At: at(-time.Minute)},
			{Kind: "subscription", ID: "sub_operator", PeerID: "peer_dot", Health: opapi.HealthPausedOperator, Summary: "dot-research · You paused this subscription.", Action: "resume", At: at(-time.Minute)},
		},
		Deliveries: []opapi.InboxDelivery{
			{DeliveryID: "dly_w1", MessageID: "msg_w1", ThreadID: "thr_hark", ThreadTitle: "hark B196: radio end-phrase bug", Sender: "peer_codex", Kind: "text", Priority: "wake", Section: "woke", State: "accepted", CreatedAt: at(-89 * time.Minute), Excerpt: "The radio listen did not finalize on \"over\" in the replay.", ReadAt: note(at(-time.Hour))},
			{DeliveryID: "dly_h1", MessageID: "msg_h1", ThreadID: "thr_t18", ThreadTitle: "Ticket 18: operator UI", Sender: "peer_codex", Kind: "text", Priority: "hold", Section: "held", State: "pushed", CreatedAt: at(-2 * time.Minute), Excerpt: "Ticket 18 prototype is up: three variants of the inbox.", Attachments: 2},
			{DeliveryID: "dly_h2", MessageID: "msg_h2", ThreadID: "thr_hark", ThreadTitle: "hark B196: radio end-phrase bug", Sender: "peer_sleuth", Kind: "text", Priority: "hold", Section: "held", State: "accepted", CreatedAt: at(-70 * time.Minute), Excerpt: "Reproduced on x-tuf. Root cause is in the end-phrase matcher."},
		},
		Unread: 2,
	}
	return f
}

// inboxLoaded runs the real inbox screen against f through its first load.
func inboxLoaded(t *testing.T, f *inboxAPI, clock *testClock) (*Model, *pump) {
	t.Helper()
	m := newTestModel(t, f, clock)
	p := newPump(t, m)
	p.run(m.Init())
	p.until("first load", func() bool { return !m.firstLoading() })
	p.until("live", func() bool { return m.Conn == ConnLive })
	p.idle()
	return m, p
}

// inboxReload presses ctrl+l and waits for the inbox load it starts.
func inboxReload(p *pump, f *inboxAPI) {
	p.t.Helper()
	f.reset()
	p.press("ctrl+l")
	p.loaded(f.fakeAPI, "Inbox")
}

// inboxQuiet fails if any action started. It reads the claim start takes on the
// update goroutine, so a refused key cannot pass by racing the fake.
func inboxQuiet(t *testing.T, m *Model) {
	t.Helper()
	if locks := m.screens[tabInbox].(*inboxScreen).locks; len(locks) != 0 {
		t.Fatalf("an action started: %v", locks)
	}
}

func inboxDown(p *pump, n int) {
	p.t.Helper()
	for range n {
		p.press("j")
	}
}

// inboxEvents gives the raw log one event of each kind it renders.
func inboxEvents(t *testing.T, m *Model) {
	t.Helper()
	m.Data.Events = []envelope.Event{
		event(t, "e1.10401", "presence", envelope.Presence{PeerID: "peer_sleuth", State: "working", At: envelope.FormatTime(fixedNow), Location: "lan:x-tuf", Quota: map[string]any{"weekly_remaining_pct": 61.0}}),
		deliveryEvent(t, "e1.10402", "thr_hark"),
		event(t, "e1.10403", "thread_updated", envelope.ThreadSummary{ThreadID: "thr_01JD3S6G8J0M2P4R6T8V0XP5R", Title: ptr("hark B196: radio end-phrase bug"), BudgetRemaining: ptr(496)}),
		event(t, "e1.10404", "receipt", envelope.Receipt{DeliveryID: "dly_x", MessageID: "msg_01JD3S6G8J0M2P4R6T8V0X0X2Z", ThreadID: "thr_x", State: "processed", By: "peer_dot", At: envelope.FormatTime(fixedNow)}),
		event(t, "e1.10405", "approval_expired", map[string]string{"approval_id": "apr_01JD3S6G8J0M2P4R6T8V0X7H", "thread_id": "thr_01JD3S6G8J0M2P4R6T8V0XP5R"}),
		event(t, "e1.10406", "group_updated", map[string]string{"group_id": "grp_7T"}),
		event(t, "e1.10407", "directory_updated", map[string]any{"peer_id": "peer_codex", "change": "updated", "fields": []string{"location", "quota"}}),
	}
}

func ptr[T any](v T) *T { return &v }

// inboxGolden compares a stripped frame with testdata/<name>.golden.
func inboxGolden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name+".golden")
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden (run with UPDATE_GOLDEN=1 once the frame is right): %v", err)
	}
	if !bytes.Equal(want, []byte(got)) {
		t.Errorf("frame differs from %s\n--- got ---\n%s\n--- want ---\n%s", path, got, want)
	}
}

func TestInboxGolden(t *testing.T) {
	m, p := inboxLoaded(t, inboxSeed(), newClock())
	inboxEvents(t, m)

	frame := plainAt(t, m, 140, 42)
	assertFrame(t, frame, 140, 42)
	for _, want := range []string{
		"inbox 12 items · apr 2 · dlq 3", "your event log", "detail",
		"APR", "DLQ", "SUB", "WAKE", "HOLD", "exp 11:39", "exp 18:59",
		"dot-research", "503", "paused_token", "e1.10401", "presence", "receipt", "✓✓ processed", "codex-hark updated location, quota",
		"Spend about 20% of my weekly quota", "[1] yes", "[2] no", "press a to answer",
	} {
		if !strings.Contains(frame, want) {
			t.Errorf("frame lacks %q", want)
		}
	}
	// The log is the event log, not a second copy of the messages.
	if strings.Contains(frame, "e1.10402") {
		t.Errorf("delivery event in the raw log:\n%s", frame)
	}
	inboxGolden(t, "inbox-140x42", frame)

	small := plainAt(t, m, 80, 24)
	assertFrame(t, small, 80, 24)
	inboxGolden(t, "inbox-80x24", small)

	p.press("a")
	prompt := plainAt(t, m, 140, 42)
	assertFrame(t, prompt, 140, 42)
	inboxGolden(t, "inbox-prompt-140x42", prompt)
	p.press("esc")

	inboxDown(p, rowDlq1)
	dlq := plainAt(t, m, 140, 42)
	for _, want := range []string{"dead_letter", "retries exhausted", "HTTP 503 after 8/8 attempts", "Scan the overnight tickets."} {
		if !strings.Contains(dlq, want) {
			t.Errorf("dead letter detail lacks %q:\n%s", want, dlq)
		}
	}
	inboxGolden(t, "inbox-dlq-140x42", dlq)

	inboxDown(p, rowHold1-rowDlq1)
	held := plainAt(t, m, 140, 42)
	for _, want := range []string{"hold", "Ticket 18: operator UI", "Ticket 18 prototype is up"} {
		if !strings.Contains(held, want) {
			t.Errorf("delivery detail lacks %q:\n%s", want, held)
		}
	}
}

func TestInboxEmpty(t *testing.T) {
	f := inboxSeed()
	f.inbox = opapi.Inbox{Now: f.inbox.Now, Me: f.inbox.Me}
	m, p := inboxLoaded(t, f, newClock())
	frame := plainAt(t, m, 140, 42)
	assertFrame(t, frame, 140, 42)
	if !strings.Contains(frame, "inbox 0 items") {
		t.Fatalf("empty inbox:\n%s", frame)
	}
	// None of these may reach past an empty list.
	p.press("j", "k", "enter", "a", "r", "x")
	if m.Mode != ModeNormal || m.Tab != tabInbox {
		t.Fatalf("mode %v tab %d", m.Mode, m.Tab)
	}
}

func TestAnswerFlow(t *testing.T) {
	t.Run("a,1 sends one Answer of option 0 and the item goes", func(t *testing.T) {
		f := inboxSeed()
		f.onAnswer = f.dropApproval
		m, p := inboxLoaded(t, f, newClock())
		p.press("a")
		if m.Mode != ModeInsert {
			t.Fatalf("prompt must take every key: Mode = %v", m.Mode)
		}
		if frame := plainAt(t, m, 140, 42); !strings.Contains(frame, "any other key cancels") {
			t.Fatalf("no prompt:\n%s", frame)
		}
		p.press("1")
		if m.Mode != ModeNormal {
			t.Fatalf("Mode = %v after answering", m.Mode)
		}
		p.until("the item to go", func() bool { return !strings.Contains(plainAt(t, m, 140, 42), "weekly quota") })
		p.idle()
		if got := f.calls(&f.answered); !slices.Equal(got, []string{"apr_1/0"}) {
			t.Fatalf("answers %q", got)
		}
		if m.Tab != tabInbox {
			t.Fatalf("the digit reached the shell: Tab = %d", m.Tab)
		}
	})
	t.Run("a digit picks its option", func(t *testing.T) {
		f := inboxSeed()
		m, p := inboxLoaded(t, f, newClock())
		inboxDown(p, rowApr2)
		p.press("a", "3")
		p.until("answer", func() bool { return len(f.calls(&f.answered)) == 1 })
		p.idle()
		if got := f.calls(&f.answered); got[0] != "apr_2/2" {
			t.Fatalf("answers %q", got)
		}
		if m.Tab != tabInbox {
			t.Fatalf("Tab = %d", m.Tab)
		}
	})
	for _, key := range []string{"9", "3", "0", "esc", "enter", "n", "q"} {
		t.Run("a,"+key+" on a two-option card sends nothing", func(t *testing.T) {
			f := inboxSeed()
			m, p := inboxLoaded(t, f, newClock())
			p.press("a", key)
			inboxQuiet(t, m)
			if got := f.calls(&f.answered); len(got) != 0 {
				t.Fatalf("answers %q", got)
			}
			if m.Mode != ModeNormal || m.Tab != tabInbox || m.ctx.Err() != nil {
				t.Fatalf("mode %v tab %d, quit %v", m.Mode, m.Tab, m.ctx.Err())
			}
			if frame := plainAt(t, m, 140, 42); strings.Contains(frame, "any other key cancels") {
				t.Fatalf("prompt still open:\n%s", frame)
			}
		})
	}
	t.Run("a on another row sends nothing and hints", func(t *testing.T) {
		f := inboxSeed()
		m, p := inboxLoaded(t, f, newClock())
		inboxDown(p, rowHold1)
		p.press("a")
		if m.Mode != ModeNormal {
			t.Fatalf("Mode = %v", m.Mode)
		}
		if frame := plainAt(t, m, 140, 42); !strings.Contains(strings.Split(frame, "\n")[41], "approval") {
			t.Fatalf("no hint:\n%s", frame)
		}
	})
	t.Run("a double press sends one response", func(t *testing.T) {
		f := inboxSeed()
		f.setInbox(func(in *opapi.Inbox) { in.Approvals = in.Approvals[:1] })
		f.onAnswer = f.dropApproval
		m, p := inboxLoaded(t, f, newClock())
		release := f.hold("Answer")
		p.press("a", "1")
		p.until("answer", func() bool { return f.count("Answer") == 1 })
		// Still in flight: the row is claimed.
		p.press("a")
		if m.Mode != ModeNormal {
			t.Fatalf("a second prompt opened: Mode = %v", m.Mode)
		}
		// Answered, and the reload has not landed: the snapshot still lists it.
		reload := f.hold("Inbox")
		release()
		p.until("the row to clear", func() bool { return !strings.Contains(plainAt(t, m, 140, 42), "weekly quota") })
		p.press("a")
		if m.Mode != ModeNormal {
			t.Fatalf("answered approval reopened: Mode = %v", m.Mode)
		}
		reload()
		p.idle()
		if got := f.calls(&f.answered); len(got) != 1 {
			t.Fatalf("answers %q", got)
		}
	})
	t.Run("a load from before the answer cannot bring the row back", func(t *testing.T) {
		f := inboxSeed()
		f.readEarly = true
		f.setInbox(func(in *opapi.Inbox) { in.Approvals = in.Approvals[:1] })
		f.onAnswer = f.dropApproval
		m, p := inboxLoaded(t, f, newClock())
		gone := func() bool { return !strings.Contains(plainAt(t, m, 140, 42), "weekly quota") }
		f.reset()
		stale := f.hold("Inbox")
		p.press("ctrl+l")
		p.until("the stale load", func() bool { return f.count("Inbox") == 1 })
		p.press("a", "1")
		p.until("the answer to land", gone)
		// Nothing starts until the stale load lands, so this gate holds the next.
		stale()
		fresh := f.hold("Inbox")
		p.until("the next load", func() bool { return f.count("Inbox") == 2 })
		if !gone() {
			t.Fatalf("a stale snapshot brought the answered approval back:\n%s", plainAt(t, m, 140, 42))
		}
		p.press("k", "a")
		if m.Mode != ModeNormal {
			t.Fatalf("answered approval reopened from a stale snapshot: Mode = %v", m.Mode)
		}
		fresh()
		p.idle()
		if got := f.calls(&f.answered); !slices.Equal(got, []string{"apr_1/0"}) {
			t.Fatalf("answers %q", got)
		}
		if !gone() {
			t.Fatal("the row came back with the fresh snapshot")
		}
	})
	t.Run("the reload is owed before anything else can land", func(t *testing.T) {
		f := inboxSeed()
		f.setInbox(func(in *opapi.Inbox) { in.Approvals = in.Approvals[:1] })
		m, p := inboxLoaded(t, f, newClock())
		s := m.screens[tabInbox].(*inboxScreen)
		stale := m.Data.Inbox
		const key = "apr:apr_1"
		s.locks[key] = &inboxLock{}
		m.inflight["inbox"] = true // the load the answer overtook
		f.dropApproval("apr_1")
		// In this order, with no turn of the pump between: a Reload here would
		// leave the load unclaimed when the stale snapshot lands.
		p.send(inboxDoneMsg{inboxAction: inboxAction{key: key, verb: "answer", hide: true, tags: []string{"inbox"}}})
		p.send(loadMsg{key: "inbox", apply: func(m *Model) { m.Data.Inbox = stale }})
		if s.locks[key] == nil || strings.Contains(plainAt(t, m, 140, 42), "weekly quota") {
			t.Fatalf("the stale snapshot released the claim: %v\n%s", s.locks, plainAt(t, m, 140, 42))
		}
		p.idle()
		if len(s.locks) != 0 {
			t.Fatalf("the load after the answer did not release it: %v", s.locks)
		}
	})
	t.Run("the digit answers the approval it was shown, not whatever slid under it", func(t *testing.T) {
		f := inboxSeed()
		m, p := inboxLoaded(t, f, newClock())
		p.press("a")
		f.dropApproval("apr_1") // answered from the other client
		inboxReload(p, f)
		if m.Mode != ModeNormal {
			t.Fatalf("prompt outlived its approval: Mode = %v", m.Mode)
		}
		if frame := plainAt(t, m, 140, 42); !strings.Contains(strings.Split(frame, "\n")[41], "closed") {
			t.Fatalf("no closed toast:\n%s", frame)
		}
		p.press("1")
		inboxQuiet(t, m)
		if got := f.calls(&f.answered); len(got) != 0 {
			t.Fatalf("answers %q", got)
		}
	})
	t.Run("a reload that keeps the approval keeps the prompt", func(t *testing.T) {
		f := inboxSeed()
		m, p := inboxLoaded(t, f, newClock())
		p.press("a")
		inboxReload(p, f)
		if m.Mode != ModeInsert {
			t.Fatalf("prompt closed: Mode = %v", m.Mode)
		}
		p.press("2")
		p.until("answer", func() bool { return len(f.calls(&f.answered)) == 1 })
		p.idle()
		if got := f.calls(&f.answered); got[0] != "apr_1/1" {
			t.Fatalf("answers %q", got)
		}
	})
	t.Run("a failure toasts, keeps the row and does not retry", func(t *testing.T) {
		f := inboxSeed()
		f.fail("Answer", &opclient.Error{Status: 500, Code: "internal", Message: "boom"})
		m, p := inboxLoaded(t, f, newClock())
		f.reset()
		p.press("a", "1")
		p.until("reload", func() bool { return f.count("Inbox") > 0 })
		p.idle()
		frame := plainAt(t, m, 140, 42)
		if !strings.Contains(strings.Split(frame, "\n")[41], "boom") || !strings.Contains(frame, "weekly quota") {
			t.Fatalf("failure:\n%s", frame)
		}
		if got := f.calls(&f.answered); len(got) != 1 {
			t.Fatalf("answers %q", got)
		}
	})
}

func TestAnswerClosed(t *testing.T) {
	f := inboxSeed()
	f.setInbox(func(in *opapi.Inbox) { in.Approvals = in.Approvals[:1] })
	f.fail("Answer", &opclient.Error{Status: 422, Code: "approval_closed", Message: "the approval is already answered or expired"})
	m, p := inboxLoaded(t, f, newClock())
	f.reset()
	release := f.hold("Inbox")
	p.press("a", "1")
	p.until("the answer", func() bool { return f.count("Answer") == 1 })
	p.until("the reload", func() bool { return f.count("Inbox") == 1 })

	line := strings.Split(plainAt(t, m, 140, 42), "\n")[41]
	if !strings.Contains(line, "closed") {
		t.Fatalf("no closed toast: %q", line)
	}
	// The operator must not read this as the answer having landed.
	if strings.Contains(strings.ToLower(line), "answered:") {
		t.Fatalf("toast claims success: %q", line)
	}
	// A late second press while the reload runs must not reach the hub.
	p.press("a", "1")
	f.dropApproval("apr_1")
	release()
	p.until("the reload to land", func() bool { return !strings.Contains(plainAt(t, m, 140, 42), "weekly quota") })
	p.idle()
	if got := f.calls(&f.answered); len(got) != 1 {
		t.Fatalf("answers %q", got)
	}
	if got := f.count("Inbox"); got != 1 {
		t.Fatalf("Inbox loaded %d times", got)
	}
}

func TestCountdownHubClock(t *testing.T) {
	frameAt := func(skew time.Duration) string {
		clock := newClock()
		clock.advance(skew)
		m, _ := inboxLoaded(t, inboxSeed(), clock)
		return plainAt(t, m, 140, 42)
	}
	want := frameAt(0)
	if !strings.Contains(want, "exp 11:39") || !strings.Contains(want, "in 11:39") {
		t.Fatalf("countdown:\n%s", want)
	}
	for _, skew := range []time.Duration{5 * time.Minute, -5 * time.Minute, time.Hour} {
		if got := frameAt(skew); got != want {
			t.Errorf("client clock %v off changed the frame:\n%s\n--- want ---\n%s", skew, got, want)
		}
	}

	t.Run("a fast client still answers an open approval", func(t *testing.T) {
		clock := newClock()
		clock.advance(time.Hour)
		m, p := inboxLoaded(t, inboxSeed(), clock)
		p.press("a")
		if m.Mode != ModeInsert {
			t.Fatal("an open approval was refused on the client's clock")
		}
	})
	t.Run("the countdown runs on the hub's clock", func(t *testing.T) {
		clock := newClock()
		clock.advance(-5 * time.Minute)
		m, p := inboxLoaded(t, inboxSeed(), clock)
		clock.advance(39 * time.Second)
		p.send(tickMsg{})
		if frame := plainAt(t, m, 140, 42); !strings.Contains(frame, "exp 11:00") {
			t.Fatalf("countdown did not tick:\n%s", frame)
		}
	})
	t.Run("past expiry the key is refused", func(t *testing.T) {
		f := inboxSeed()
		clock := newClock()
		clock.advance(5 * time.Minute)
		m, p := inboxLoaded(t, f, clock)
		clock.advance(11*time.Minute + 39*time.Second)
		p.send(tickMsg{})
		frame := plainAt(t, m, 140, 42)
		if !strings.Contains(frame, "expired") || strings.Contains(frame, "exp 00:00") {
			t.Fatalf("not shown as expired:\n%s", frame)
		}
		p.press("a")
		if m.Mode != ModeNormal {
			t.Fatalf("expired approval opened a prompt: Mode = %v", m.Mode)
		}
		if frame := plainAt(t, m, 140, 42); !strings.Contains(strings.Split(frame, "\n")[41], "expired") {
			t.Fatalf("no refusal:\n%s", frame)
		}
		p.press("1")
		inboxQuiet(t, m)
		if got := f.calls(&f.answered); len(got) != 0 {
			t.Fatalf("answers %q", got)
		}
	})
	t.Run("expiring under an open prompt sends nothing", func(t *testing.T) {
		f := inboxSeed()
		clock := newClock()
		m, p := inboxLoaded(t, f, clock)
		p.press("a")
		clock.advance(12 * time.Minute)
		p.send(tickMsg{})
		p.press("1")
		inboxQuiet(t, m)
		if got := f.calls(&f.answered); len(got) != 0 {
			t.Fatalf("answers %q", got)
		}
		if frame := plainAt(t, m, 140, 42); !strings.Contains(strings.Split(frame, "\n")[41], "expired") {
			t.Fatalf("no refusal:\n%s", frame)
		}
	})
}

func TestDiscardNeedsConfirm(t *testing.T) {
	t.Run("x alone sends nothing", func(t *testing.T) {
		f := inboxSeed()
		m, p := inboxLoaded(t, f, newClock())
		inboxDown(p, rowDlq1)
		p.press("x")
		inboxQuiet(t, m)
		if got := f.calls(&f.discarded); len(got) != 0 {
			t.Fatalf("discarded %q", got)
		}
		if frame := plainAt(t, m, 140, 42); !strings.Contains(frame, "y discard") {
			t.Fatalf("no confirm prompt:\n%s", frame)
		}
	})
	for _, key := range []string{"n", "esc", "x", "q", "1"} {
		t.Run("x,"+key+" cancels", func(t *testing.T) {
			f := inboxSeed()
			m, p := inboxLoaded(t, f, newClock())
			inboxDown(p, rowDlq1)
			p.press("x", key)
			inboxQuiet(t, m)
			if got := f.calls(&f.discarded); len(got) != 0 {
				t.Fatalf("discarded %q", got)
			}
			if m.Mode != ModeNormal || m.Tab != tabInbox || m.ctx.Err() != nil {
				t.Fatalf("mode %v tab %d, quit %v", m.Mode, m.Tab, m.ctx.Err())
			}
		})
	}
	t.Run("x,y sends one discard of the selected letter", func(t *testing.T) {
		f := inboxSeed()
		m, p := inboxLoaded(t, f, newClock())
		inboxDown(p, rowDlq2)
		f.reset()
		p.press("x", "y")
		f.dropDeadLetter("dly_2")
		p.until("the reload", func() bool { return f.count("Inbox") > 0 })
		p.until("the item to go", func() bool { return !strings.Contains(plainAt(t, m, 140, 42), "410") })
		p.idle()
		if got := f.calls(&f.discarded); !slices.Equal(got, []string{"dly_2"}) {
			t.Fatalf("discarded %q", got)
		}
	})
	t.Run("y alone and x on another row send nothing", func(t *testing.T) {
		f := inboxSeed()
		m, p := inboxLoaded(t, f, newClock())
		inboxDown(p, rowDlq1)
		p.press("y")
		inboxDown(p, rowWake-rowDlq1)
		p.press("x", "y")
		inboxQuiet(t, m)
		if got := f.calls(&f.discarded); len(got) != 0 {
			t.Fatalf("discarded %q", got)
		}
		if m.Mode != ModeNormal {
			t.Fatalf("Mode = %v", m.Mode)
		}
	})
	t.Run("the letter gone under the prompt is not replaced by its neighbour", func(t *testing.T) {
		f := inboxSeed()
		m, p := inboxLoaded(t, f, newClock())
		inboxDown(p, rowDlq1)
		p.press("x")
		f.dropDeadLetter("dly_1")
		inboxReload(p, f)
		if m.Mode != ModeNormal {
			t.Fatalf("prompt outlived its letter: Mode = %v", m.Mode)
		}
		p.press("y")
		inboxQuiet(t, m)
		if got := f.calls(&f.discarded); len(got) != 0 {
			t.Fatalf("discarded %q", got)
		}
	})
}

func TestRetryDeadLetter(t *testing.T) {
	f := inboxSeed()
	m, p := inboxLoaded(t, f, newClock())
	inboxDown(p, rowDlq3)
	release := f.hold("RetryDeadLetter")
	p.press("r")
	p.until("retry", func() bool { return f.count("RetryDeadLetter") == 1 })
	p.press("r")
	f.dropDeadLetter("dly_3")
	release()
	p.until("the item to go", func() bool { return !strings.Contains(plainAt(t, m, 140, 42), "Check the callback") })
	p.idle()
	if got := f.calls(&f.retried); !slices.Equal(got, []string{"dly_3"}) {
		t.Fatalf("retried %q", got)
	}
	// r means nothing on a row that is neither.
	f2 := inboxSeed()
	m2, p2 := inboxLoaded(t, f2, newClock())
	p2.press("r")
	inboxDown(p2, rowWake)
	p2.press("r")
	inboxQuiet(t, m2)
	if got := f2.calls(&f2.retried); len(got) != 0 {
		t.Fatalf("retried %q", got)
	}
}

func TestResumeOnlyWithAction(t *testing.T) {
	for _, row := range []struct {
		name string
		down int
		want string
	}{
		{"paused_auth", rowSubAuth, ""},
		{"paused_token", rowSubToken, ""},
		{"paused_failed", rowSubFailed, "sub_failed"},
		{"paused_operator", rowSubOperator, "sub_operator"},
	} {
		t.Run(row.name, func(t *testing.T) {
			f := inboxSeed()
			m, p := inboxLoaded(t, f, newClock())
			inboxDown(p, row.down)
			f.reset()
			p.press("r")
			if row.want == "" {
				inboxQuiet(t, m)
				if got := f.calls(&f.resumed); len(got) != 0 {
					t.Fatalf("resumed %q", got)
				}
				if f.count("ResumeSubscription") != 0 {
					t.Fatal("resume reached the hub")
				}
				line := strings.Split(plainAt(t, m, 140, 42), "\n")[41]
				if !strings.Contains(line, "resume") {
					t.Fatalf("no hint: %q", line)
				}
				// paused_auth also covers a rotated credential, which relinking
				// does not cure; the detail carries the hub's own note.
				if strings.Contains(line, "relink") {
					t.Fatalf("hint names one cause of several: %q", line)
				}
				return
			}
			p.until("resume", func() bool { return f.count("ResumeSubscription") == 1 })
			p.until("the reload", func() bool { return f.count("Inbox") > 0 })
			p.idle()
			if got := f.calls(&f.resumed); !slices.Equal(got, []string{row.want}) {
				t.Fatalf("resumed %q", got)
			}
		})
	}
	t.Run("a paused row reads as paused and only resumable ones offer it", func(t *testing.T) {
		m, p := inboxLoaded(t, inboxSeed(), newClock())
		inboxDown(p, rowSubFailed)
		if frame := plainAt(t, m, 140, 42); !strings.Contains(frame, "r to resume") {
			t.Fatalf("resume not offered:\n%s", frame)
		}
		p.press("k")
		frame := plainAt(t, m, 140, 42)
		if strings.Contains(frame, "r to resume") || !strings.Contains(frame, "paused_token") {
			t.Fatalf("resume offered on paused_token:\n%s", frame)
		}
	})
}

func TestOpenMarksRead(t *testing.T) {
	f := inboxSeed()
	m, p := inboxLoaded(t, f, newClock())
	inboxDown(p, rowHold1)
	f.reset()
	p.press("enter")
	if m.Tab != tabChat || m.CurrentThread != "thr_t18" {
		t.Fatalf("tab %d thread %q", m.Tab, m.CurrentThread)
	}
	p.until("read", func() bool { return f.count("ReadThread") == 1 })
	p.until("the reload", func() bool { return f.count("Inbox") > 0 })
	p.idle()
	if got := f.calls(&f.read); !slices.Equal(got, []string{"thr_t18"}) {
		t.Fatalf("read %q", got)
	}

	// The thread load can win the race with the write, and a read receipt goes
	// to the sender, so no event would refresh the matrix.
	f3 := inboxSeed()
	m3, p3 := inboxLoaded(t, f3, newClock())
	inboxDown(p3, rowHold1)
	f3.reset()
	release := f3.hold("ReadThread")
	p3.press("enter")
	p3.until("the first thread load", func() bool { return f3.count("Matrix") == 1 && len(m3.inflight) == 0 })
	release()
	p3.until("the thread to reload after the read", func() bool { return f3.count("Matrix") == 2 })
	p3.idle()

	// A subscription has no thread to open.
	f2 := inboxSeed()
	m2, p2 := inboxLoaded(t, f2, newClock())
	inboxDown(p2, rowSubAuth)
	p2.press("enter")
	inboxQuiet(t, m2)
	if m2.Tab != tabInbox || len(f2.calls(&f2.read)) != 0 {
		t.Fatalf("tab %d read %q", m2.Tab, f2.calls(&f2.read))
	}
}

// The cursor stays on its item when a reload adds rows above it, and lands on
// the neighbour when its own item goes.
func TestSelectionFollowsItem(t *testing.T) {
	f := inboxSeed()
	m, p := inboxLoaded(t, f, newClock())
	inboxDown(p, rowDlq1)
	f.setInbox(func(in *opapi.Inbox) {
		in.Approvals = append(slices.Clone(in.Approvals), opapi.OpenApproval{ApprovalID: "apr_3", Question: "new", ExpiresAt: in.Now, Options: []string{"a", "b"}})
	})
	inboxReload(p, f)
	if frame := plainAt(t, m, 140, 42); !strings.Contains(frame, "HTTP 503 after 8/8 attempts") {
		t.Fatalf("selection moved:\n%s", frame)
	}
	f.dropDeadLetter("dly_1")
	inboxReload(p, f)
	if frame := plainAt(t, m, 140, 42); !strings.Contains(frame, "HTTP 410 Gone") {
		t.Fatalf("selection did not land on the neighbour:\n%s", frame)
	}
}

// A list longer than its pane keeps the selected row on screen.
func TestInboxScrolls(t *testing.T) {
	f := inboxSeed()
	f.setInbox(func(in *opapi.Inbox) {
		for i := range 60 {
			in.Deliveries = append(in.Deliveries, opapi.InboxDelivery{DeliveryID: fmt.Sprintf("dly_s%d", i), ThreadID: "thr_s", Sender: "peer_codex", Priority: "hold", Section: "held", State: "accepted", CreatedAt: in.Now, Excerpt: fmt.Sprintf("scrolled-%02d", i)})
		}
	})
	m, p := inboxLoaded(t, f, newClock())
	if strings.Contains(plainAt(t, m, 140, 42), "scrolled-59") {
		t.Fatal("the last row should start off screen")
	}
	inboxDown(p, rowHold2+60)
	frame := plainAt(t, m, 140, 42)
	assertFrame(t, frame, 140, 42)
	if !strings.Contains(frame, "scrolled-59") {
		t.Fatalf("selection scrolled off:\n%s", frame)
	}
}

// inboxAt shows the selected row with its prompt keys pressed at one size.
func inboxAt(t *testing.T, change func(*opapi.Inbox), down int, w, h int, keys ...string) string {
	t.Helper()
	f := inboxSeed()
	f.setInbox(change)
	m, p := inboxLoaded(t, f, newClock())
	inboxDown(p, down)
	p.press(keys...)
	frame := plainAt(t, m, w, h)
	assertFrame(t, frame, w, h)
	return frame
}

func inboxHas(t *testing.T, frame string, wants ...string) {
	t.Helper()
	for _, want := range wants {
		if !strings.Contains(frame, want) {
			t.Errorf("frame lacks %q:\n%s", want, frame)
		}
	}
}

var inboxSizes = []struct{ w, h int }{{80, 24}, {140, 42}}

// inboxDetailLines is the text of each row of the detail pane, without its
// border and left margin.
func inboxDetailLines(frame string, w int) []string {
	left := max(w*55/100, min(w-30, 50))
	var lines []string
	for _, line := range strings.Split(frame, "\n") {
		if r := []rune(line); len(r) > left+2 {
			lines = append(lines, string(r[left+2:]))
		}
	}
	return lines
}

// A peer sets the body. However long it is, and whatever it dresses up as, the
// real question, options and prompt stay in the frame and above it.
func TestApprovalDetailKeepsItsOptions(t *testing.T) {
	const padded = "Delete the staging bucket?\n  [1] no, keep it\n  [2] yes, delete\n"
	long := strings.Repeat("Please read this whole note first. ", 60)
	forged := padded + strings.Repeat("\n", 40) + "."
	cases := []struct {
		name           string
		question, text string
		cut            bool
	}{
		{"long body", "Delete it?", long, true},
		{"padded body", "Delete it?", forged, true},
		{"body as the question", "", long, true},
		{"padded question", forged, "short", false},
		{"question after blank lines", strings.Repeat("\n", 40) + "Delete it?", "short", false},
		{"long question", long, "short", false},
	}
	for _, size := range inboxSizes {
		for _, c := range cases {
			t.Run(fmt.Sprintf("%dx%d %s", size.w, size.h, c.name), func(t *testing.T) {
				change := func(in *opapi.Inbox) {
					a := &in.Approvals[0]
					a.Question, a.Text, a.Options = c.question, c.text, []string{"yes, delete prod", "no"}
				}
				for _, step := range []struct {
					keys []string
					hint string
				}{{nil, "press a to answer"}, {[]string{"a"}, "answer with 1-2"}} {
					frame := inboxAt(t, change, rowApr1, size.w, size.h, step.keys...)
					inboxHas(t, frame, "  [1] yes, delete prod", "  [2] no", "expires ", step.hint)
					if c.cut {
						inboxHas(t, frame, "more lines")
					}
					got := slices.DeleteFunc(inboxDetailLines(frame, size.w), func(line string) bool { return !strings.HasPrefix(line, "  [1] ") })
					if len(got) != 1 {
						t.Errorf("%d lines read as option 1:\n%s", len(got), frame)
					}
					if strings.HasSuffix(c.question, "Delete it?") {
						inboxHas(t, frame, "? Delete it?")
					}
					// Peer text comes after the prompt, marked as a quote.
					at := strings.Index(frame, "no, keep it")
					if prompt := strings.Index(frame, step.hint); at >= 0 && c.question != forged && at < prompt {
						t.Errorf("peer text above the prompt:\n%s", frame)
					}
				}
			})
		}
	}
	t.Run("eight options fit the smallest frame", func(t *testing.T) {
		options := make([]string, 8)
		for i := range options {
			options[i] = fmt.Sprintf("choice-%d", i+1)
		}
		frame := inboxAt(t, func(in *opapi.Inbox) {
			a := &in.Approvals[0]
			a.Question, a.Text, a.Options = long, long, options
		}, rowApr1, 80, 24, "a")
		for i, option := range options {
			inboxHas(t, frame, fmt.Sprintf("  [%d] %s", i+1, option))
		}
		inboxHas(t, frame, "answer with 1-8", "expires ", "? Please read")
	})
	t.Run("options that differ at the end stay apart", func(t *testing.T) {
		frame := inboxAt(t, func(in *opapi.Inbox) {
			in.Approvals[0].Options = []string{"yes, delete the production bucket and all of its logs", "yes, delete the production bucket but keep its logs"}
		}, rowApr1, 80, 24, "a")
		inboxHas(t, frame, "all of its logs", "but keep its logs", "answer with 1-2")
	})
	t.Run("eight options too long for their share say they are cut", func(t *testing.T) {
		option := strings.Repeat("x", 64)
		frame := inboxAt(t, func(in *opapi.Inbox) {
			in.Approvals[0].Options = []string{option, option, option, option, option, option, option, option}
		}, rowApr1, 80, 24, "a")
		inboxHas(t, frame, "[1] xxxxxxxxxxxxxxxxxxx…", "[8] xxxxxxxxxxxxxxxxxxx…", "answer with 1-8", "expires ")
	})
	t.Run("the digit still sends the real option", func(t *testing.T) {
		f := inboxSeed()
		f.setInbox(func(in *opapi.Inbox) {
			a := &in.Approvals[0]
			a.Text, a.Options = forged, []string{"yes, delete production", "no"}
		})
		_, p := inboxLoaded(t, f, newClock())
		p.press("a", "1")
		p.until("answer", func() bool { return len(f.calls(&f.answered)) == 1 })
		p.idle()
		if got := f.calls(&f.answered); got[0] != "apr_1/0" {
			t.Fatalf("answers %q", got)
		}
	})
}

// The same for the other kinds of row: what the keys do outlasts the peer's text.
func TestDetailKeepsItsKeys(t *testing.T) {
	long := strings.Repeat("HTTP 500 from your callback, again. ", 60)
	padded := "see below" + strings.Repeat("\n", 60) + "."
	for _, size := range inboxSizes {
		for _, text := range []string{long, padded} {
			t.Run(fmt.Sprintf("%dx%d", size.w, size.h), func(t *testing.T) {
				letter := func(in *opapi.Inbox) { in.DeadLetters[0].LastError, in.DeadLetters[0].Excerpt = &text, text }
				inboxHas(t, inboxAt(t, letter, rowDlq1, size.w, size.h), "press r to retry", "dead_letter", "more lines")
				inboxHas(t, inboxAt(t, letter, rowDlq1, size.w, size.h, "x"), "press y to discard")
				sub := func(in *opapi.Inbox) { in.Attention[2].Summary = text }
				inboxHas(t, inboxAt(t, sub, rowSubFailed, size.w, size.h), "press r to resume", "more lines")
				held := func(in *opapi.Inbox) { in.Deliveries[1].Excerpt = text }
				inboxHas(t, inboxAt(t, held, rowHold1, size.w, size.h), "press enter to open", "more lines")
			})
		}
	}
}

// An 80 column status line leaves the hints 37 cells before the operator's name.
func TestInboxHintsFit(t *testing.T) {
	m, p := inboxLoaded(t, inboxSeed(), newClock())
	line := func(w, h int) string { return strings.Split(plainAt(t, m, w, h), "\n")[h-2] }
	if got := line(80, 24); !strings.Contains(got, m.active().Hints()+"  max@vecnet") {
		t.Fatalf("hint cut at 80 columns: %q", got)
	}
	if got := line(140, 42); !strings.Contains(got, "enter open") {
		t.Fatalf("a wide status line should name every key: %q", got)
	}
	plainAt(t, m, 80, 24)
	p.press("a")
	if got := line(80, 24); !strings.Contains(got, "1-2 answer  any other key cancels") {
		t.Fatalf("prompt hint cut: %q", got)
	}
}

func TestDeadLetterTries(t *testing.T) {
	m, _ := inboxLoaded(t, inboxSeed(), newClock())
	frame := plainAt(t, m, 140, 42)
	if !strings.Contains(frame, "1 try") || strings.Contains(frame, "1 tries") || !strings.Contains(frame, "8 tries") {
		t.Fatalf("tries:\n%s", frame)
	}
}

var inboxSGR = regexp.MustCompile(`\x1b\[[0-9;:]*m`)

func TestInboxHostile(t *testing.T) {
	const payload = "\x1b]52;c;aGk=\x07\x1b]0;pwned\x07\x1b]8;;http://evil.example\x07link\x1b]8;;\x07\x1b[2J\x1b[H\xc2\x9b31m\xc2\x9d0;c1\xc2\x9c\u202e"
	hostile := "<img src=x onerror=alert(1)>" + payload + "tail"
	f := inboxSeed()
	f.setInbox(func(in *opapi.Inbox) {
		for i := range in.Approvals {
			a := &in.Approvals[i]
			a.ThreadTitle, a.Question, a.Text = hostile, hostile, hostile
			a.Options = []string{hostile, "no"}
		}
		for i := range in.DeadLetters {
			d := &in.DeadLetters[i]
			d.Excerpt, d.LastError, d.Class = hostile, ptr(hostile), hostile
		}
		for i := range in.Attention {
			a := &in.Attention[i]
			a.Summary, a.Health = hostile, hostile
		}
		for i := range in.Deliveries {
			d := &in.Deliveries[i]
			d.ThreadTitle, d.Excerpt, d.Kind, d.State, d.Priority = hostile, hostile, hostile, hostile, hostile
		}
	})
	for i := range f.peers {
		f.peers[i].Peer.Name, f.peers[i].Peer.Kind = hostile, hostile
	}
	m, p := inboxLoaded(t, f, newClock())
	m.Data.Events = []envelope.Event{
		event(t, "e1.1"+payload, "presence", envelope.Presence{PeerID: "peer_dot", State: hostile, Location: hostile, Note: hostile, At: envelope.FormatTime(fixedNow)}),
		event(t, "e1.2", "receipt", envelope.Receipt{MessageID: hostile, By: "peer_dot", State: hostile, Note: hostile}),
		event(t, "e1.3", "thread_updated", envelope.ThreadSummary{ThreadID: hostile, Title: &hostile}),
		event(t, "e1.4", "unknown"+payload, map[string]string{"x": hostile}),
		event(t, "e1.6", "directory_updated", map[string]any{"peer_id": hostile, "change": hostile, "fields": []string{hostile}}),
		event(t, "e1.7", "approval_expired", map[string]string{"approval_id": hostile, "thread_id": hostile}),
		{Cursor: "e1.5", Type: "receipt", At: hostile, Data: []byte(`not json ` + hostile)},
	}
	check := func(what string) {
		t.Helper()
		m.Update(tea.WindowSizeMsg{Width: 140, Height: 42})
		raw := m.View().Content
		rest := inboxSGR.ReplaceAllString(raw, "")
		if strings.ContainsRune(rest, 0x1b) {
			t.Fatalf("%s: ESC outside SGR: %q", what, rest)
		}
		for _, bad := range []string{"\x07", "\u202e", "\u009b", "\u009d", "\u009c", "\x9b", "\x9d", "pwned", "aGk=", "evil.example"} {
			if strings.Contains(raw, bad) {
				t.Fatalf("%s: frame contains %q: %q", what, bad, raw)
			}
		}
		assertFrame(t, plainAt(t, m, 140, 42), 140, 42)
	}
	check("first row")
	if !strings.Contains(plainAt(t, m, 140, 42), "<img src=x onerror=alert(1)>") {
		t.Fatal("hostile text must stay visible as inert text")
	}
	for i := range 12 {
		p.press("a", "esc", "x", "esc", "j")
		check(fmt.Sprintf("row %d", i+1))
	}
	p.press("a")
	check("prompt")
	p.press("esc")
	// The hub's own error text is no more trusted than a peer's.
	f.fail("RetryDeadLetter", &opclient.Error{Status: 500, Code: "x", Message: hostile})
	p.press("k", "k", "k", "k", "k", "k", "k", "r")
	p.until("the retry", func() bool { return f.count("RetryDeadLetter") > 0 })
	p.idle()
	check("toast")
}

func TestPresenceToggle(t *testing.T) {
	t.Run("the status bar moves after the write and the reload", func(t *testing.T) {
		f := inboxSeed()
		f.onPresence = func(state string) { f.setInbox(func(in *opapi.Inbox) { in.Me.Presence = state }) }
		m, p := inboxLoaded(t, f, newClock())
		line := func() string { return strings.Split(plainAt(t, m, 140, 42), "\n")[40] }
		if !strings.Contains(line(), "max@vecnet · working") {
			t.Fatalf("status: %q", line())
		}
		release := f.hold("SetPresence")
		p.press("p")
		p.until("the write", func() bool { return f.count("SetPresence") == 1 })
		// Only a 204 and a reload change what is shown.
		if !strings.Contains(line(), "max@vecnet · working") {
			t.Fatalf("status moved early: %q", line())
		}
		p.press("p") // a double press while it is in flight
		release()
		p.until("the status to move", func() bool { return strings.Contains(line(), "max@vecnet · idle") })
		p.idle()
		if got := f.calls(&f.presence); !slices.Equal(got, []string{"idle"}) {
			t.Fatalf("presence %q", got)
		}
		f.writes.Lock()
		at := f.presenceAt[0]
		f.writes.Unlock()
		if !at.Equal(fixedNow) {
			t.Fatalf("at = %v, want the hub's clock", at)
		}
	})
	for _, c := range []struct{ from, want string }{
		{"working", "idle"}, {"idle", "working"}, {"blocked", "working"}, {"unknown", "working"}, {"offline", "working"},
	} {
		t.Run(c.from, func(t *testing.T) {
			f := inboxSeed()
			f.setInbox(func(in *opapi.Inbox) { in.Me.Presence = c.from })
			_, p := inboxLoaded(t, f, newClock())
			p.press("p")
			p.until("the write", func() bool { return f.count("SetPresence") == 1 })
			p.idle()
			if got := f.calls(&f.presence); !slices.Equal(got, []string{c.want}) {
				t.Fatalf("presence %q", got)
			}
		})
	}
	t.Run("a failed write keeps the old state and toasts", func(t *testing.T) {
		f := inboxSeed()
		f.fail("SetPresence", errors.New("hub went away"))
		m, p := inboxLoaded(t, f, newClock())
		p.press("p")
		p.until("the write", func() bool { return f.count("SetPresence") == 1 })
		p.until("the toast", func() bool { return strings.Contains(plainAt(t, m, 140, 42), "hub went away") })
		if frame := plainAt(t, m, 140, 42); !strings.Contains(frame, "max@vecnet · working") {
			t.Fatalf("status moved:\n%s", frame)
		}
	})
}

// Task 14's rule: re-post what the hub says, never a local choice, so two
// clients cannot fight, and never re-assert unknown or offline.
func TestPresenceHeartbeat(t *testing.T) {
	const beat = 20 * time.Minute
	t.Run("nothing before twenty minutes", func(t *testing.T) {
		f := inboxSeed()
		clock := newClock()
		_, p := inboxLoaded(t, f, clock)
		f.reset()
		clock.advance(beat - time.Second)
		p.send(tickMsg{})
		p.loaded(f.fakeAPI, "Subscriptions")
		if f.count("SetPresence") != 0 {
			t.Fatal("heartbeat ran early")
		}
	})
	t.Run("re-posts the hub's state, not the last one seen", func(t *testing.T) {
		f := inboxSeed()
		clock := newClock()
		m, p := inboxLoaded(t, f, clock)
		// The other client went idle, and no event reloaded this inbox.
		f.setInbox(func(in *opapi.Inbox) { in.Me.Presence = "idle" })
		if m.Data.Inbox.Me.Presence != "working" {
			t.Fatal("test needs a stale snapshot")
		}
		f.setInbox(func(in *opapi.Inbox) { in.Now = envelope.FormatTime(fixedNow.Add(beat)) })
		clock.advance(beat)
		p.send(tickMsg{})
		p.until("heartbeat", func() bool { return f.count("SetPresence") == 1 })
		p.until("reload", func() bool { return m.Data.Inbox.Me.Presence == "idle" })
		p.idle()
		if got := f.calls(&f.presence); !slices.Equal(got, []string{"idle"}) {
			t.Fatalf("presence %q", got)
		}
		f.writes.Lock()
		at := f.presenceAt[0]
		f.writes.Unlock()
		if want := fixedNow.Add(beat); !at.Equal(want) {
			t.Fatalf("at = %v, want %v", at, want)
		}
		// One beat per interval.
		clock.advance(time.Minute)
		p.send(tickMsg{})
		p.loaded(f.fakeAPI, "Subscriptions")
		if got := f.count("SetPresence"); got != 1 {
			t.Fatalf("%d heartbeats", got)
		}
		clock.advance(beat)
		p.send(tickMsg{})
		p.until("next heartbeat", func() bool { return f.count("SetPresence") == 2 })
		p.idle()
	})
	for _, state := range []string{"unknown", "offline"} {
		t.Run(state+" is never re-asserted", func(t *testing.T) {
			f := inboxSeed()
			f.setInbox(func(in *opapi.Inbox) { in.Me.Presence = state })
			clock := newClock()
			_, p := inboxLoaded(t, f, clock)
			f.reset()
			clock.advance(beat)
			p.send(tickMsg{})
			p.until("the heartbeat's read", func() bool { return f.count("Inbox") == 1 })
			p.idle()
			if got := f.calls(&f.presence); len(got) != 0 {
				t.Fatalf("presence %q", got)
			}
		})
	}
	t.Run("a load landing after a 401 sends no beat", func(t *testing.T) {
		f := inboxSeed()
		clock := newClock()
		m, _ := inboxLoaded(t, f, clock)
		m.Conn = ConnRejected
		f.reset()
		clock.advance(beat)
		if _, cmd := m.Update(Loaded{Key: "subs"}); cmd != nil {
			t.Fatal("a heartbeat started on a rejected credential")
		}
		if f.count("Inbox") != 0 || f.count("SetPresence") != 0 {
			t.Fatal("the hub heard from a rejected credential")
		}
	})
	t.Run("runs whichever tab is showing", func(t *testing.T) {
		f := inboxSeed()
		clock := newClock()
		_, p := inboxLoaded(t, f, clock)
		p.press("3")
		clock.advance(beat)
		p.send(tickMsg{})
		p.until("heartbeat", func() bool { return f.count("SetPresence") == 1 })
		p.idle()
	})
}

// Once the hub has refused the credential no key may send anything.
func TestInboxActionsStopWhenRejected(t *testing.T) {
	f := inboxSeed()
	m, p := inboxLoaded(t, f, newClock())
	m.Conn = ConnRejected
	f.reset()
	p.press("p", "a")
	if m.Mode != ModeNormal {
		t.Fatalf("Mode = %v", m.Mode)
	}
	inboxDown(p, rowDlq1)
	p.press("r", "x", "y")
	inboxDown(p, rowHold1-rowDlq1)
	p.press("enter")
	inboxQuiet(t, m)
	for _, name := range []string{"SetPresence", "Answer", "RetryDeadLetter", "DiscardDeadLetter", "ReadThread"} {
		if f.count(name) != 0 {
			t.Errorf("%s reached the hub", name)
		}
	}
}
