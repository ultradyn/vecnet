package tui

import (
	"context"
	"errors"
	"fmt"
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

// dashWrites is every API method that changes the hub.
var dashWrites = []string{"ReadThread", "ResumeSubscription", "RetryDeadLetter", "DiscardDeadLetter", "Send", "Answer", "SetPresence"}

// dashAPI answers a participant query from byPeer, which the shared fake cannot:
// it ignores the query. A narrowed load counts as "Threads:<peer>", so a test
// can hold or fail it apart from the shell's own reload.
type dashAPI struct {
	*fakeAPI
	queries []opapi.ThreadQuery
	byPeer  map[string]opapi.ThreadPage
	mu2     sync.Mutex
}

func (f *dashAPI) Threads(ctx context.Context, q opapi.ThreadQuery) (opapi.ThreadPage, error) {
	f.mu2.Lock()
	f.queries = append(f.queries, q)
	page := f.byPeer[q.Participant]
	f.mu2.Unlock()
	if q.Participant == "" {
		return f.fakeAPI.Threads(ctx, q)
	}
	return page, f.hit(ctx, "Threads:"+q.Participant)
}

func (f *dashAPI) narrowed(peer string) int {
	f.mu2.Lock()
	defer f.mu2.Unlock()
	n := 0
	for _, q := range f.queries {
		if q.Participant == peer {
			n++
		}
	}
	return n
}

func dashBefore(d time.Duration) *string {
	s := envelope.FormatTime(fixedNow.Add(-d))
	return &s
}

func dashText(s string) *string { return &s }

func dashThread(id, title string, ago time.Duration, budget int, participants ...string) opapi.ThreadRow {
	return opapi.ThreadRow{
		Thread:        envelope.ThreadSummary{ThreadID: id, Title: dashText(title), Participants: participants, MessageCount: 3, BudgetRemaining: &budget},
		LastMessageAt: *dashBefore(ago),
	}
}

func dashPeer(id, name, kind, presence, reach string, seen time.Duration, quota map[string]any, location string, caps ...string) opapi.PeerRow {
	return opapi.PeerRow{Peer: opapi.Peer{PeerID: id, Name: name, Kind: kind, Presence: presence, Reachability: reach, Quota: quota, Location: dashText(location), LastSeen: dashBefore(seen), Capabilities: caps}, Credential: "active"}
}

// seededDash is c-dash.png's hub: the dot has unknown presence and is reachable,
// and one thread is held behind a delivery under retry.
func seededDash() *dashAPI {
	f := newFake()
	f.inbox = opapi.Inbox{Now: envelope.FormatTime(fixedNow), Me: opapi.Me{PeerID: "peer_op", Name: "Max", Presence: "working"}}
	source := dashPeer("peer_gh", "gh-ultradyn", "source", "unknown", "reachable", 39*time.Minute, nil, "github.com")
	disabled := dashPeer("peer_old", "old-bot", "session", "idle", "unknown", 30*time.Hour, map[string]any{}, "lan:xsm")
	disabled.Disabled = true
	dot := dashPeer("peer_dot", "dot-research", "dot", "unknown", "reachable", 18*time.Minute, map[string]any{"weekly_remaining_pct": 35.0}, "chatgpt", "deep-research")
	dot.PrincipalID = "prn_dot"
	f.peers = []opapi.PeerRow{
		dashPeer("peer_op", "max", "operator", "working", "connected", 0, map[string]any{}, "xsm", "approve", "admin"),
		dashPeer("peer_codex", "codex-hark", "session", "working", "connected", 0, map[string]any{"weekly_remaining_pct": 44.0}, "lan:xsm", "shell", "repo:ultradyn/hark"),
		dot,
		dashPeer("peer_triage", "grok-triage", "grokbot", "unknown", "unreachable", 3*time.Hour, map[string]any{"weekly_remaining_pct": 3.0}, "cloud:cursor", "triage"),
		dashPeer("peer_claw", "claw-ops", "openclaw", "idle", "connected", 0, map[string]any{"plan": "pro", "tokens": 1200.0}, "lan:x-alpha", "git:push"),
		source,
		disabled,
	}
	f.threads = opapi.ThreadPage{Threads: []opapi.ThreadRow{
		dashThread("thr_ticket18", "vecnet ticket 18: operator UI", time.Minute, 499, "peer_op", "peer_codex"),
		dashThread("thr_hosting", "Hosting cost deep dive", 3*time.Minute, 497, "peer_op", "peer_dot"),
		dashThread("thr_quota", "Weekly quota rebalance", 2*time.Minute, 484, "peer_op", "peer_triage"),
		dashThread("thr_nightly", "Nightly digest 2026-11-01", 2*time.Hour, 0, "peer_op", "peer_claw"),
	}}
	f.threads.Threads[2].Cut = &opapi.CutInfo{Reason: "max_depth_exceeded", At: *dashBefore(time.Minute)}
	f.cuts = opapi.LoopCuts{Hours: 24, Total: 7, ByReason: map[string]int{"expired": 1, "max_depth_exceeded": 4, "thread_budget_exhausted": 2}}
	f.health = opapi.DeliveryHealth{
		Blocked: []opapi.BlockedThread{{Recipient: "peer_triage", ThreadID: "thr_quota", DeliveryID: "dly_01ABCDEF3A5C", Attempt: 3, Waiting: 2, NextAttemptAt: dashText(envelope.FormatTime(fixedNow.Add(5 * time.Minute))), LastStatus: dashInt(503), LastError: dashText("HTTP 503")}},
		Failed:  []opapi.FailedDelivery{{DeliveryID: "dly_01ABCDEF7Q9X", Recipient: "peer_dot", ThreadID: "thr_hosting", UpdatedAt: *dashBefore(time.Hour), LastStatus: dashInt(409), LastError: dashText("HTTP 409 Conflict")}},
	}
	f.audit = opapi.AuditPage{Entries: []opapi.AuditEntry{
		{At: *dashBefore(19 * time.Minute), Actor: "prn_dot", Action: "approval.open", Targets: []string{"apr_01ABCDEF3A5C"}},
		{At: *dashBefore(23 * time.Minute), Actor: "hub", Action: "delivery.conflict", Targets: []string{"dly_01ABCDEF3A5C"}, Detail: map[string]any{"status": 409.0}},
		{At: *dashBefore(113 * time.Minute), Actor: "peer_op", Action: "thread.rename", Targets: []string{"thr_quota"}},
	}}
	f.counters = map[string]int{"dead_letters": 3, "loop_cut": 7, "rate_limited": 2}
	return &dashAPI{fakeAPI: f, byPeer: map[string]opapi.ThreadPage{
		"peer_codex": {Threads: f.threads.Threads[:1]},
		"peer_dot":   {Threads: f.threads.Threads[1:2]},
	}}
}

func dashInt(n int) *int { return &n }

// openDash returns a model on the dash tab after its first load. The other tabs
// are static, so these tests and goldens pin this screen alone.
func openDash(t *testing.T, f *dashAPI, clock *testClock) (*Model, *pump) {
	t.Helper()
	m := newTestModel(t, f, clock)
	m.screens = staticScreens()
	m.screens[tabDash] = newDashScreen()
	p := newPump(t, m)
	p.run(m.Init())
	p.until("first load", func() bool { return !m.firstLoading() })
	p.until("live", func() bool { return m.Conn == ConnLive })
	p.idle()
	p.press("3")
	return m, p
}

// dashTable returns the peer table's header and rows at width w.
func dashTable(t *testing.T, m *Model, w, h, rows int) (string, []string) {
	t.Helper()
	lines := strings.Split(plainAt(t, m, w, h), "\n")
	for i, line := range lines {
		if strings.Contains(line, "PEER") {
			return line, lines[i+1 : i+1+rows]
		}
	}
	t.Fatalf("no peer table at width %d:\n%s", w, strings.Join(lines, "\n"))
	return "", nil
}

func dashRow(t *testing.T, m *Model, w, h int, name string) string {
	t.Helper()
	_, rows := dashTable(t, m, w, h, len(m.Data.Peers))
	for _, row := range rows {
		if strings.Contains(row, name) {
			return row
		}
	}
	t.Fatalf("no row for %s in:\n%s", name, strings.Join(rows, "\n"))
	return ""
}

func dashHas(t *testing.T, frame string, wants ...string) {
	t.Helper()
	for _, want := range wants {
		if !strings.Contains(frame, want) {
			t.Errorf("frame lacks %q:\n%s", want, frame)
		}
	}
}

func dashLacks(t *testing.T, frame string, unwanted ...string) {
	t.Helper()
	for _, s := range unwanted {
		if strings.Contains(frame, s) {
			t.Errorf("frame holds %q:\n%s", s, frame)
		}
	}
}

func TestDashGolden(t *testing.T) {
	m, _ := openDash(t, seededDash(), newClock())
	t.Run("140x42", func(t *testing.T) {
		frame := renderAt(t, m, 140, 42)
		assertFrame(t, frame, 140, 42)
		dashHas(t, frame, "peers", "threads", "delivery health", "loop cuts", "audit tail", "codex-hark", "vecnet ticket 18: operator UI", "thread.rename")
		header, _ := dashTable(t, m, 140, 42, 7)
		dashHas(t, header, "PEER", "KIND", "PRESENCE", "REACH", "QUOTA", "LOCATION", "LAST SEEN", "CAPS")
	})
	t.Run("80x24", func(t *testing.T) {
		frame := renderAt(t, m, 80, 24)
		assertFrame(t, frame, 80, 24)
		dashHas(t, frame, "codex-hark", "unreachable", "delivery health", "audit tail")
		header, _ := dashTable(t, m, 80, 24, 7)
		dashHas(t, header, "PEER", "PRESENCE", "REACH")
		dashLacks(t, header, "CAPS", "LOCATION", "KIND")
	})

	// Narrowing removes whole columns in one fixed order, and a row stays one
	// row at every width.
	drops := []string{"CAPS", "LOCATION", "KIND", "LAST SEEN", "QUOTA"}
	dropped := func(header string) int {
		n := 0
		for n < len(drops) && !strings.Contains(header, drops[n]) {
			n++
		}
		for _, name := range drops[n:] {
			if !strings.Contains(header, name) {
				t.Fatalf("%s went before %s: %q", name, drops[n], header)
			}
		}
		return n
	}
	last := 0
	for w := 140; w >= 80; w-- {
		header, rows := dashTable(t, m, w, 42, len(m.Data.Peers))
		n := dropped(header)
		if n < last {
			t.Fatalf("width %d shows a column width %d dropped: %q", w, w+1, header)
		}
		last = n
		for i, row := range m.Data.Peers {
			if !strings.Contains(rows[i], Clean(row.Peer.Name)) {
				t.Fatalf("width %d: row %d is not %s: a row wrapped:\n%s", w, i, row.Peer.Name, strings.Join(rows, "\n"))
			}
		}
		assertFrame(t, plainAt(t, m, w, 42), w, 42)
	}
	if last == 0 {
		t.Fatal("80 columns dropped nothing")
	}
}

func TestDashReadOnly(t *testing.T) {
	f := seededDash()
	m, p := openDash(t, f, newClock())

	// The shell keeps these before the screen sees them; q would end the test.
	claimed := []rune{'q', '1', '2', '4', ':', '?'}
	var keys []tea.KeyPressMsg
	for r := rune(0x20); r < 0x7f; r++ {
		if !slices.Contains(claimed, r) {
			keys = append(keys, tea.KeyPressMsg{Code: r, Text: string(r)})
		}
	}
	for _, code := range []rune{tea.KeyEnter, tea.KeyEscape, tea.KeyTab, tea.KeyBackspace, tea.KeyDelete, tea.KeyInsert, tea.KeyUp, tea.KeyDown, tea.KeyLeft, tea.KeyRight, tea.KeyHome, tea.KeyEnd, tea.KeyPgUp, tea.KeyPgDown} {
		keys = append(keys, tea.KeyPressMsg{Code: code})
	}
	keys = append(keys, tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift})
	for _, r := range "abdefghijkmnoprstuvwxyz" {
		keys = append(keys, tea.KeyPressMsg{Code: r, Mod: tea.ModCtrl})
	}

	// Each key runs on the peer table, on a narrowed one and on the threads.
	for _, setup := range [][]tea.KeyPressMsg{
		{},
		{{Code: 'j', Text: "j"}, {Code: tea.KeyEnter}},
		{{Code: tea.KeyTab}},
	} {
		for _, key := range keys {
			p.press("3")
			for _, s := range setup {
				p.send(s)
			}
			p.send(key)
			if m.Mode != ModeNormal {
				t.Fatalf("key %q left mode %v: this screen takes no text", key.String(), m.Mode)
			}
			p.press("3", "esc")
		}
	}
	p.idle()
	for _, name := range dashWrites {
		if n := f.count(name); n != 0 {
			t.Errorf("%s called %d times from the dashboard", name, n)
		}
	}
}

func TestDashPartialError(t *testing.T) {
	for _, tc := range []struct{ key, method, pane string }{
		{"health", "DeliveryHealth", "delivery health"},
		{"peers", "Peers", "peers"},
		{"threads", "Threads", "threads"},
		{"audit", "Audit", "audit tail"},
		{"cuts", "LoopCuts", "loop cuts"},
		{"counters", "Counters", "counters"},
	} {
		t.Run(tc.key, func(t *testing.T) {
			f := seededDash()
			f.fail(tc.method, errors.New("hub exploded\x1b[2J"))
			m, _ := openDash(t, f, newClock())
			if m.Data.Errs[tc.key] == nil {
				t.Fatalf("Data.Errs[%q] not set", tc.key)
			}
			frame := plainAt(t, m, 140, 42)
			dashHas(t, frame, tc.pane+" load failed: hub exploded")
			dashLacks(t, m.View().Content, "\x1b[2J")
			want := map[string]string{"health": "thread blocked on delivery", "peers": "codex-hark", "threads": "Hosting cost deep dive", "audit": "thread.rename", "cuts": "max_depth_exceeded", "counters": "rate_limited"}
			delete(want, tc.key)
			for key, text := range want {
				if !strings.Contains(frame, text) {
					t.Errorf("%s failed, and %s no longer renders:\n%s", tc.key, key, frame)
				}
			}
		})
	}

	t.Run("a failed reload keeps the last data under the error", func(t *testing.T) {
		f := seededDash()
		m, p := openDash(t, f, newClock())
		f.fail("DeliveryHealth", errors.New("hub exploded"))
		f.reset()
		p.press("ctrl+l")
		p.loaded(f.fakeAPI, "DeliveryHealth")
		frame := plainAt(t, m, 140, 42)
		dashHas(t, frame, "load failed: hub exploded", "last good data", "thread blocked on delivery", "codex-hark")
	})
}

func TestUnknownPresenceNotOffline(t *testing.T) {
	m, _ := openDash(t, seededDash(), newClock())
	for _, size := range [][2]int{{140, 42}, {80, 24}} {
		row := dashRow(t, m, size[0], size[1], "dot-research")
		dashHas(t, row, "unknown", "reachable")
		dashLacks(t, row, "offline", "unreachable")
		// A source has no presence to report, so it does not read as unknown.
		row = dashRow(t, m, size[0], size[1], "gh-ultradyn")
		dashHas(t, row, "n/a", "reachable")
		dashLacks(t, row, "offline", "unknown")
	}
	dashLacks(t, plainAt(t, m, 140, 42), "offline")
}

func TestDashEmptyPresenceReadsUnknown(t *testing.T) {
	f := seededDash()
	f.peers[2].Peer.Presence = ""
	m, _ := openDash(t, f, newClock())
	dashHas(t, dashRow(t, m, 140, 42, "dot-research"), "unknown")
}

func TestDashQuota(t *testing.T) {
	m, _ := openDash(t, seededDash(), newClock())
	dashHas(t, dashRow(t, m, 140, 42, "codex-hark"), "[||||      ]  44%")
	dashHas(t, dashRow(t, m, 140, 42, "grok-triage"), "[          ]   3%")
	dashHas(t, dashRow(t, m, 140, 42, "claw-ops"), "plan=pro tokens=1200")
	dashHas(t, dashRow(t, m, 140, 42, "max"), "none")
	dashHas(t, dashRow(t, m, 80, 24, "codex-hark"), "44%")

	t.Run("any *_pct key draws a bar and anything else stays k=v", func(t *testing.T) {
		f := seededDash()
		f.peers[1].Peer.Quota = map[string]any{"plan": "pro", "monthly_used_pct": 150.0}
		f.peers[2].Peer.Quota = map[string]any{"weekly_remaining_pct": "61"}
		m, _ := openDash(t, f, newClock())
		dashHas(t, dashRow(t, m, 140, 42, "codex-hark"), "[||||||||||] 150%")
		dashHas(t, dashRow(t, m, 140, 42, "dot-research"), "weekly_remaining_p")
	})
}

func TestDashDisabledPeerFlagged(t *testing.T) {
	m, _ := openDash(t, seededDash(), newClock())
	dashHas(t, dashRow(t, m, 140, 42, "old-bot"), "disabled")
	dashHas(t, dashRow(t, m, 80, 24, "old-bot"), "disabled")
}

func TestDashBlockedOnDelivery(t *testing.T) {
	m, _ := openDash(t, seededDash(), newClock())
	frame := plainAt(t, m, 140, 42)
	dashHas(t, frame, "thread blocked on delivery dly_…3A5C", "grok-triage · Weekly quota rebalance", "attempt 3", "2 waiting", "next 5m", "503")
	dashHas(t, frame, "dot-research", "HTTP 409 Conflict")
	frame = plainAt(t, m, 80, 24)
	dashHas(t, frame, "blocked on dly_…3A5C")
	dashHas(t, frame, "max_depth_exceeded")
	for _, line := range strings.Split(frame, "\n") {
		if strings.Contains(line, "max_depth_exceeded") && !strings.Contains(line, " 4 ") {
			t.Errorf("a reason's count fell off the pane: %q", line)
		}
	}

	t.Run("a loaded hub keeps the loop cuts on screen", func(t *testing.T) {
		f := seededDash()
		blocked := f.health.Blocked[0]
		f.health.Blocked = nil
		for range 40 {
			f.health.Blocked = append(f.health.Blocked, blocked)
		}
		m, _ := openDash(t, f, newClock())
		for _, size := range [][2]int{{140, 42}, {80, 24}} {
			dashHas(t, plainAt(t, m, size[0], size[1]), "loop cuts / 24h: 7", "more")
		}
	})
}

func TestDashLoopCutsOrderedByCount(t *testing.T) {
	m, _ := openDash(t, seededDash(), newClock())
	frame := plainAt(t, m, 140, 42)
	dashHas(t, frame, "loop cuts / 24h: 7")
	depth, budget, expired := strings.Index(frame, "max_depth_exceeded  "), strings.Index(frame, "thread_budget_exhausted"), strings.Index(frame, "expired ")
	if depth < 0 || budget < depth || expired < budget {
		t.Fatalf("reasons not ordered by count (%d, %d, %d):\n%s", depth, budget, expired, frame)
	}
}

func TestDashLoadingPanes(t *testing.T) {
	f := seededDash()
	m, p := openDash(t, f, newClock())
	release := f.hold("DeliveryHealth")
	defer release()
	f.reset()
	p.press("ctrl+l")
	p.until("the reload", func() bool { return f.count("DeliveryHealth") == 1 })
	frame := plainAt(t, m, 140, 42)
	dashHas(t, frame, "delivery health · loading", "thread blocked on delivery")
}

func TestDashNarrowThreads(t *testing.T) {
	f := seededDash()
	m, p := openDash(t, f, newClock())
	frame := plainAt(t, m, 140, 42)
	dashHas(t, frame, "Hosting cost deep dive", "vecnet ticket 18")

	release := f.hold("Threads:peer_codex")
	p.press("j", "enter")
	p.until("the narrowed request", func() bool { return f.narrowed("peer_codex") == 1 })
	frame = plainAt(t, m, 140, 42)
	dashHas(t, frame, "threads · codex-hark · loading")
	dashLacks(t, frame, "Hosting cost deep dive")

	release()
	p.until("narrowed rows", func() bool { return strings.Contains(plainAt(t, m, 140, 42), "vecnet ticket 18") })
	frame = plainAt(t, m, 140, 42)
	dashHas(t, frame, "threads · codex-hark · 1")
	dashLacks(t, frame, "Hosting cost deep dive", "Nightly digest")

	// A narrowed pane follows the events that invalidate threads.
	before := f.narrowed("peer_codex")
	p.send(reloadMsg{tags: []string{"threads"}})
	p.until("the narrowed reload", func() bool { return f.narrowed("peer_codex") == before+1 })

	p.press("esc")
	frame = plainAt(t, m, 140, 42)
	dashHas(t, frame, "Hosting cost deep dive", "vecnet ticket 18")
	dashLacks(t, frame, "threads · codex-hark")
}

// The hub refuses a participant that is not a peer_ id, and a source never is.
func TestDashSourceDoesNotNarrow(t *testing.T) {
	f := seededDash()
	f.peers[5].Peer.PeerID = "src_gh"
	m, p := openDash(t, f, newClock())
	for range 5 {
		p.press("j")
	}
	p.press("enter")
	frame := plainAt(t, m, 140, 42)
	dashHas(t, frame, "gh-ultradyn is a source and has no threads", "Hosting cost deep dive")
	dashLacks(t, frame, "threads · gh-ultradyn")
	if n := f.narrowed("src_gh"); n != 0 {
		t.Fatalf("%d requests for a source's threads", n)
	}
}

func TestDashNarrowFailure(t *testing.T) {
	t.Run("shows in the thread pane alone", func(t *testing.T) {
		f := seededDash()
		f.fail("Threads:peer_codex", errors.New("no threads\x1b[2J"))
		m, p := openDash(t, f, newClock())
		p.press("j", "enter")
		p.until("the error", func() bool { return strings.Contains(plainAt(t, m, 140, 42), "load failed: no threads") })
		dashHas(t, plainAt(t, m, 140, 42), "codex-hark", "delivery health", "thread.rename")
		dashLacks(t, m.View().Content, "\x1b[2J")
	})
	t.Run("401 stops everything", func(t *testing.T) {
		f := seededDash()
		f.fail("Threads:peer_codex", &opclient.Error{Status: 401, Code: "unauthenticated", Message: "no"})
		m, p := openDash(t, f, newClock())
		p.press("j", "enter")
		p.until("rejection", func() bool { return m.Conn == ConnRejected })
		n := f.narrowed("peer_codex")
		p.send(Loaded{Key: "threads"})
		p.press("enter")
		if got := f.narrowed("peer_codex"); got != n {
			t.Fatalf("%d more requests after a rejected credential", got-n)
		}
	})
}

// A late answer for the peer the operator has left must not overwrite the pane.
func TestDashNarrowStaleAnswerDropped(t *testing.T) {
	f := seededDash()
	m, p := openDash(t, f, newClock())
	spy := &dashSpy{Screen: m.screens[tabDash]}
	m.screens[tabDash] = spy
	release := f.hold("Threads:peer_codex")
	p.press("j", "enter")
	p.until("the first request", func() bool { return f.narrowed("peer_codex") == 1 })
	p.press("esc", "j", "enter")
	p.until("the second answer", func() bool { return strings.Contains(plainAt(t, m, 140, 42), "Hosting cost deep dive") })
	release()
	p.until("the stale answer to land", func() bool { return spy.answers == 2 })
	frame := plainAt(t, m, 140, 42)
	dashHas(t, frame, "threads · dot-research · 1", "Hosting cost deep dive")
	dashLacks(t, frame, "vecnet ticket 18")
}

// dashSpy counts the narrowed answers that reach the screen.
type dashSpy struct {
	Screen
	answers int
}

func (s *dashSpy) Update(msg tea.Msg, m *Model) tea.Cmd {
	if _, ok := msg.(dashThreadsMsg); ok {
		s.answers++
	}
	return s.Screen.Update(msg, m)
}

func TestDashEnterOpensThread(t *testing.T) {
	f := seededDash()
	m, p := openDash(t, f, newClock())
	p.send(tea.KeyPressMsg{Code: tea.KeyTab})
	p.press("j", "enter")
	if m.Tab != tabChat || m.CurrentThread != "thr_hosting" {
		t.Fatalf("Tab %d, CurrentThread %q", m.Tab, m.CurrentThread)
	}

	t.Run("from a narrowed pane", func(t *testing.T) {
		f := seededDash()
		m, p := openDash(t, f, newClock())
		p.press("j", "j", "enter")
		p.until("narrowed rows", func() bool { return strings.Contains(plainAt(t, m, 140, 42), "threads · dot-research · 1") })
		p.press("enter")
		if m.Tab != tabChat || m.CurrentThread != "thr_hosting" {
			t.Fatalf("Tab %d, CurrentThread %q", m.Tab, m.CurrentThread)
		}
	})
	t.Run("an empty pane opens nothing", func(t *testing.T) {
		f := seededDash()
		f.threads = opapi.ThreadPage{}
		m, p := openDash(t, f, newClock())
		p.send(tea.KeyPressMsg{Code: tea.KeyTab})
		p.press("enter")
		if m.Tab != tabDash || m.CurrentThread != "" {
			t.Fatalf("Tab %d, CurrentThread %q", m.Tab, m.CurrentThread)
		}
	})
}

func TestDashMoveKeys(t *testing.T) {
	f := seededDash()
	m, p := openDash(t, f, newClock())
	marked := func() string {
		_, rows := dashTable(t, m, 140, 42, len(m.Data.Peers))
		for _, row := range rows {
			if strings.Contains(row, "│> ") {
				return row
			}
		}
		t.Fatalf("no row is marked:\n%s", strings.Join(rows, "\n"))
		return ""
	}
	dashHas(t, marked(), "max")
	p.press("k")
	dashHas(t, marked(), "max")
	p.press("j", "j")
	dashHas(t, marked(), "dot-research")

	// The cursor follows its peer through a reload that reorders the table.
	f.peers[0], f.peers[2] = f.peers[2], f.peers[0]
	f.reset()
	p.send(reloadMsg{tags: []string{"peers"}})
	p.loaded(f.fakeAPI, "Peers")
	dashHas(t, marked(), "dot-research")

	for range 40 {
		p.press("j")
	}
	dashHas(t, marked(), "old-bot")
}

func TestDashScrollsLongTable(t *testing.T) {
	f := seededDash()
	f.peers = nil
	for i := range 60 {
		f.peers = append(f.peers, dashPeer(fmt.Sprintf("peer_%02d", i), fmt.Sprintf("scale-%02d", i), "session", "idle", "connected", 0, nil, "lan"))
	}
	m, p := openDash(t, f, newClock())
	for range 45 {
		p.press("j")
	}
	for _, size := range [][2]int{{140, 42}, {80, 24}} {
		frame := plainAt(t, m, size[0], size[1])
		assertFrame(t, frame, size[0], size[1])
		dashHas(t, frame, "> scale-45", "delivery health", "audit tail")
	}
}

// Ages count from the hub's clock, not the client's, which may be minutes out.
func TestDashAgesUseHubClock(t *testing.T) {
	clock := newClock()
	clock.advance(11 * time.Minute) // the hub's now stays fixedNow
	m, _ := openDash(t, seededDash(), clock)
	dashHas(t, dashRow(t, m, 140, 42, "dot-research"), "18m ago")
	dashHas(t, plainAt(t, m, 140, 42), "next 5m")
}

func TestDashPeerStringsAreCleaned(t *testing.T) {
	const hostile = "<img src=x onerror=alert(1)>\x1b]52;c;aGk=\x07\x1b[2Jpwn\u202e\x9b31m"
	f := seededDash()
	bad := dashPeer("peer_bad", hostile, hostile, hostile, hostile, time.Minute, map[string]any{hostile: hostile}, hostile, hostile, hostile)
	f.peers = append(f.peers, bad)
	f.threads.Threads = append(f.threads.Threads, dashThread("thr_bad", hostile, time.Minute, 5, "peer_bad"))
	f.cuts.ByReason[hostile] = 9
	f.counters[hostile] = 9
	f.audit.Entries = append(f.audit.Entries, opapi.AuditEntry{At: hostile, Actor: hostile, Action: hostile, Targets: []string{hostile}, Detail: map[string]any{hostile: hostile}})
	f.health.Blocked = append(f.health.Blocked, opapi.BlockedThread{Recipient: hostile, ThreadID: hostile, DeliveryID: hostile, LastError: dashText(hostile)})
	f.health.Failed = append(f.health.Failed, opapi.FailedDelivery{DeliveryID: hostile, Recipient: hostile, ThreadID: hostile, UpdatedAt: hostile, LastError: dashText(hostile)})
	m, p := openDash(t, f, newClock())
	for _, focus := range []string{"peers", "threads"} {
		for _, size := range [][2]int{{140, 42}, {80, 24}} {
			plainAt(t, m, size[0], size[1])
			content := m.View().Content
			dashLacks(t, content, "\x1b]", "\x07", "\x1b[2J", "\u202e", "\x9b", "\u0080")
			assertFrame(t, plainAt(t, m, size[0], size[1]), size[0], size[1])
		}
		dashHas(t, plainAt(t, m, 140, 42), "<img src=x onerror=alert(1)>pwn")
		if focus == "peers" {
			p.send(tea.KeyPressMsg{Code: tea.KeyTab})
		}
	}
}
