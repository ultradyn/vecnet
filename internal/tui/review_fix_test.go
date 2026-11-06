package tui

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/opapi"
	"github.com/XertroV/vecnet/internal/tui/theme"
	"github.com/XertroV/vecnet/internal/tui/theme/themetest"
)

func TestSidebarColourProfiles(t *testing.T) {
	row := sideRow{Title: "older yours", Unread: 5, UnreadText: "5"}
	paint := func(p theme.Profile) string {
		th := theme.New(p, true)
		return sectionHead(th, "Other Threads", "▾", 2, 40) + "\n" + sectionRule(th, 40) + "\n" + sideTitleLine(th, row, 40, false)
	}
	raw := paint(theme.TrueColor)
	if !strings.Contains(raw, "▌") || !strings.Contains(raw, "─") {
		t.Fatal("section header lost its bar or rule")
	}
	if !strings.Contains(raw, "38;2;251;191;36") {
		t.Fatalf("unread is not wake amber:\n%s", raw)
	}
	if !strings.Contains(raw, "\x1b[38;2;168;85;247m▌") {
		t.Fatalf("section bar is not the accent colour:\n%q", raw)
	}
	if !boldOn(raw) {
		t.Fatal("unread title is not bold")
	}

	raw = paint(theme.ANSI256)
	if strings.Contains(raw, "38;2;") || !strings.Contains(raw, "38;5;") {
		t.Fatalf("256-colour rung kept truecolor:\n%s", raw)
	}

	raw = paint(theme.ANSI16)
	if strings.Contains(raw, "38;2;") || strings.Contains(raw, "38;5;") {
		t.Fatalf("16-colour rung kept a high-colour sequence:\n%s", raw)
	}
	if !strings.Contains(plain(raw), "▌") {
		t.Fatal("16-colour header dropped the bar")
	}

	raw = paint(theme.NoColor)
	if bad := themetest.Violations(theme.NoColor, raw); len(bad) > 0 {
		t.Fatalf("NO_COLOR kept colour: %v\n%q", bad, raw)
	}
	if !boldOn(raw) || !strings.Contains(plain(raw), "▌") || !strings.Contains(plain(raw), "─") {
		t.Fatalf("NO_COLOR dropped bold or the header glyphs:\n%s", raw)
	}
}

func boldOn(raw string) bool {
	return strings.Contains(raw, "\x1b[1m") || strings.Contains(raw, "\x1b[1;")
}

func TestListMetaKeepsActivityAndCount(t *testing.T) {
	m := chatListModel(t)
	title := "Quarterly planning session"
	m.Data.Mine.Threads = []opapi.ThreadRow{{
		Thread: envelope.ThreadSummary{
			ThreadID: "thr_q", Title: &title, MessageCount: 128,
			Participants: []string{"peer_me", "peer_ada"},
		},
		LastMessageAt: "2026-11-02T04:41:00Z",
	}}
	m.Data.Peers = []opapi.PeerRow{{Peer: opapi.Peer{PeerID: "peer_ada", Name: "Ada Lovelace"}}}
	for _, width := range []int{80, 140} {
		frame := plainAt(t, m, width, 42)
		if !strings.Contains(frame, "128") || !strings.Contains(frame, "3h ago") {
			t.Fatalf("%d columns dropped the count or the activity:\n%s", width, frame)
		}
	}
	narrow := plainAt(t, m, 80, 42)
	wide := plainAt(t, m, 140, 42)
	if strings.Contains(narrow, "Ada Lovelace") {
		t.Fatalf("80 columns did not truncate the participant:\n%s", narrow)
	}
	if !strings.Contains(wide, "Ada Lovelace") {
		t.Fatalf("140 columns dropped the participant:\n%s", wide)
	}
}

func TestUnknownIdentityDoesNotClaimThreads(t *testing.T) {
	m := chatListModel(t)
	m.Data.Inbox.Me.PeerID = ""
	m.Data.MineKnown = false
	m.Data.ThreadsKnown = true
	m.Data.Threads.Threads = []opapi.ThreadRow{
		mineRow("thr_own", "mine later", []string{"peer_me"}, "2026-11-02T04:41:00Z", 1),
		mineRow("thr_other", "someone else", []string{"peer_x"}, "2026-11-02T04:41:00Z", 1),
	}
	frame := plainAt(t, m, 140, 42)
	if strings.Contains(frame, "mine later") || strings.Contains(frame, "someone else") {
		t.Fatalf("unknown identity classified threads:\n%s", frame)
	}
	chat := m.screens[tabChat].(*chatScreen)
	if cmd := chat.sync(m); cmd != nil || m.CurrentThread != "" {
		t.Fatal("unknown identity auto-selected a thread")
	}

	m.Data.Inbox.Me.PeerID = "peer_me"
	m.Data.MineKnown = true
	m.Data.OthersKnown = true
	m.Data.Mine.Threads = []opapi.ThreadRow{m.Data.Threads.Threads[0]}
	m.Data.Others.Threads = []opapi.ThreadRow{m.Data.Threads.Threads[1]}
	if cmd := chat.sync(m); cmd == nil {
		t.Fatal("known identity did not open the operator's thread")
	}
	if m.CurrentThread != "thr_own" {
		t.Fatalf("opened %q", m.CurrentThread)
	}
	frame = plainAt(t, m, 140, 42)
	yours, other := strings.Index(frame, "Your Threads"), strings.Index(frame, "Other Threads")
	own, them := strings.Index(frame, "mine later"), strings.Index(frame, "someone else")
	if yours < 0 || other < yours || own < 0 || them < 0 || own > other || them < other {
		t.Fatalf("grouping:\n%s", frame)
	}
}

func TestThreadLoadFailureIsNotAnEmptyList(t *testing.T) {
	m := newTestModel(t, newFake(), newClock())
	m.Tab = tabChat
	m.Data.Errs = map[string]error{"threads": errors.New("hub down")}
	frame := plainAt(t, m, 140, 42)
	if strings.Contains(frame, "No threads yet") || !strings.Contains(frame, "Threads unavailable") {
		t.Fatalf("initial failure:\n%s", frame)
	}

	m.Data.Inbox.Me.PeerID = "peer_me"
	m.Data.MineKnown = true
	m.Data.Mine.Threads = []opapi.ThreadRow{mineRow("thr_kept", "kept row", []string{"peer_me"}, "2026-11-02T04:41:00Z", 1)}
	m.Data.Errs["mine"] = errors.New("reload failed")
	frame = plainAt(t, m, 140, 42)
	if !strings.Contains(frame, "kept row") {
		t.Fatalf("failed reload dropped the last good rows:\n%s", frame)
	}
}

func TestOpeningAThreadRevealsIt(t *testing.T) {
	m := chatListModel(t)
	var rows []opapi.ThreadRow
	for i := 0; i < 40; i++ {
		rows = append(rows, mineRow(fmt.Sprintf("thr_%02d", i), fmt.Sprintf("title %02d", i), []string{"peer_me"}, "2026-11-02T04:41:00Z", 1))
	}
	m.Data.Mine.Threads = rows
	chat := m.screens[tabChat].(*chatScreen)
	chat.thread = "thr_00"
	m.CurrentThread = "thr_00"
	plainAt(t, m, 80, 24)
	m.OpenThread("thr_39")
	frame := plainAt(t, m, 80, 24)
	if !strings.Contains(frame, "title 39") {
		t.Fatalf("command open left the thread offscreen:\n%s", frame)
	}
	top := chat.listTop
	chat.Wheel("chat.list", false, m)
	moved := chat.listTop
	if moved == top {
		t.Fatal("wheel did not move the list")
	}
	plainAt(t, m, 80, 24)
	if chat.listTop != moved || m.CurrentThread != "thr_39" {
		t.Fatalf("render undid the wheel: top %d want %d thread %q", chat.listTop, moved, m.CurrentThread)
	}

	chat.listTop = 0
	chat.thread = m.CurrentThread
	m.OpenThread("thr_00")
	frame = plainAt(t, m, 80, 24)
	if !strings.Contains(frame, "title 00") {
		t.Fatalf("inbox open left the thread offscreen:\n%s", frame)
	}
}

func TestShowMoreAndDashboardHandoff(t *testing.T) {
	api := newFake()
	next := "page-2"
	api.byCursor = map[string]opapi.ThreadPage{
		"page-2": {Threads: []opapi.ThreadRow{mineRow("thr_page2", "page two thread", []string{"peer_z"}, "2026-11-02T04:41:00Z", 1)}},
	}
	m := newTestModel(t, api, newClock())
	m.Tab = tabChat
	m.mouse = false
	m.Data.Inbox.Me.PeerID = "peer_me"
	m.Data.OthersKnown = true
	m.Data.Others.Threads = make([]opapi.ThreadRow, 201)
	for i := range m.Data.Others.Threads {
		m.Data.Others.Threads[i] = mineRow(fmt.Sprintf("thr_o_%03d", i), fmt.Sprintf("other %03d", i), []string{"peer_x"}, "2026-11-02T04:41:00Z", 1)
	}
	m.Data.Threads = opapi.ThreadPage{Threads: []opapi.ThreadRow{mineRow("thr_hub", "hub page", []string{"peer_me"}, "2026-11-02T04:41:00Z", 1)}, Next: &next}
	chat := m.screens[tabChat].(*chatScreen)
	chat.othersOpen = true
	for _, want := range []int{100, 150, 200} {
		m.Update(keyPress("n"))
		if chat.otherLimit != want {
			t.Fatalf("limit %d, want %d", chat.otherLimit, want)
		}
	}
	dash := m.screens[tabDash].(*dashScreen)
	dash.narrow = &dashNarrow{peer: "peer_a", page: opapi.ThreadPage{Threads: []opapi.ThreadRow{mineRow("thr_narrow", "narrowed only", nil, "2026-11-02T04:41:00Z", 1)}}}
	m.Update(keyPress("n"))
	if m.Tab != tabDash || dash.narrow != nil || dash.focus != dashOnThreads {
		t.Fatalf("handoff tab %d narrow %v focus %d", m.Tab, dash.narrow != nil, dash.focus)
	}
	frame := plainAt(t, m, 140, 42)
	if strings.Contains(frame, "narrowed only") || !strings.Contains(frame, "hub page") {
		t.Fatalf("unfiltered dashboard:\n%s", frame)
	}
	cmd := dash.onKey("n", m)
	if cmd == nil {
		t.Fatal("n did not ask for the next cursor page")
	}
	dash.Update(cmd(), m)
	if !hasThread(dash.threadRows(m), "thr_page2") {
		t.Fatal("the second cursor page is not in the dashboard")
	}
	api.mu.Lock()
	queries := append([]opapi.ThreadQuery(nil), api.threadQueries...)
	api.mu.Unlock()
	if len(queries) != 1 || queries[0].Cursor != "page-2" || queries[0].Participant != "" {
		t.Fatalf("query %+v", queries)
	}
}

func TestMouseTabsDeferToModal(t *testing.T) {
	m := chatListModel(t)
	chat := m.screens[tabChat].(*chatScreen)
	m.CurrentThread = "thr_old"
	chat.thread = "thr_old"
	chat.draft("thr_old").text = "keep this draft"
	m.Mode = ModeInsert
	plainAt(t, m, 140, 42)
	clickHit(t, m, "0")
	if m.Tab != tabChat || m.Mode != ModeInsert || chat.draft("thr_old").text != "keep this draft" {
		t.Fatalf("click during composition: tab %d mode %v draft %q", m.Tab, m.Mode, chat.draft("thr_old").text)
	}

	m.Mode = ModeNormal
	m.Tab = tabInbox
	inbox := m.screens[tabInbox].(*inboxScreen)
	inbox.prompt = &inboxPrompt{discard: &opapi.DeadLetter{DeliveryID: "dly_1"}}
	m.Mode = ModeInsert
	plainAt(t, m, 140, 42)
	clickHit(t, m, "2")
	if m.Tab != tabInbox || inbox.prompt == nil {
		t.Fatal("click during a discard prompt changed tabs")
	}

	m.Mode = ModeNormal
	inbox.prompt = nil
	m.confirmQuit = true
	plainAt(t, m, 140, 42)
	clickHit(t, m, "1")
	if !m.confirmQuit || m.Tab != tabInbox {
		t.Fatal("click during quit confirmation changed tabs")
	}
}

func TestHeaderBudgetsHostAndLocal(t *testing.T) {
	m := newTestModel(t, newFake(), newClock())
	m.Profile = "very-long-operator-profile"
	m.Hub = "http://[::1]:45752"
	m.Data.Subs = make([]opapi.Subscription, 12)
	for _, conn := range []string{ConnOffline, ConnConnecting} {
		m.Conn = conn
		line := strings.Split(plainAt(t, m, 80, 24), "\n")[0]
		for _, want := range []string{"4:subs", "::1", "local", conn} {
			if !strings.Contains(line, want) {
				t.Fatalf("%s header %q", conn, line)
			}
		}
		if strings.Contains(line, ":45752") {
			t.Fatalf("%s header kept the port: %q", conn, line)
		}
	}
}

func TestPickerCleansNamesAndKeepsWheel(t *testing.T) {
	raw := "bad\nname\x1b[2J"
	choices := make([]ProfileChoice, 30)
	for i := range choices {
		choices[i] = ProfileChoice{Name: fmt.Sprintf("profile-%02d", i), Hub: "https://hub.example"}
	}
	choices[4].Name = raw
	p := newPick(choices)
	p.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	p.Update(tea.MouseWheelMsg{Button: tea.MouseWheelDown})
	plain := strip(p.View().Content)
	if strings.Contains(plain, "profile-00") || !strings.Contains(plain, "profile-03") {
		t.Fatalf("wheel snapped back to the selection:\n%s", plain)
	}
	if strings.Contains(p.View().Content, "\x1b[2J") || !strings.Contains(plain, "bad name") {
		t.Fatalf("list drew the raw name:\n%s", plain)
	}
	p.View()
	clickPick(t, p, "4")
	if p.cursor != 4 || p.choices[p.cursor].Name != raw {
		t.Fatalf("click lost the original name: %+v", p.choices[p.cursor])
	}
	shown := strip(p.View().Content)
	if !strings.Contains(shown, "run bad name for this session") || strings.Contains(shown, "\x1b[2J") {
		t.Fatalf("confirm:\n%s", shown)
	}
	_, cmd := p.Update(keyPress("y"))
	if cmd == nil || p.result.Name != raw || !p.result.MakeDefault {
		t.Fatalf("result %+v", p.result)
	}
}

func TestOverlayWheels(t *testing.T) {
	m := chatListModel(t)
	chat := m.screens[tabChat].(*chatScreen)
	m.CurrentThread = "thr_old"
	chat.thread = "thr_old"
	rows := make([]opapi.MatrixRow, 40)
	for i := range rows {
		rows[i] = opapi.MatrixRow{MessageID: fmt.Sprintf("m%d", i), Sender: "peer_me", Excerpt: fmt.Sprintf("excerpt %d", i)}
	}
	m.Data.Thread = map[string]*ThreadState{"thr_old": {Matrix: opapi.Matrix{Recipients: []string{"peer_a"}, Rows: rows}}}
	chat.overlay = chatMatrix
	plainAt(t, m, 140, 42)
	wheelHit(t, m, "chat.matrix", true)
	if chat.matrixY != 1 {
		t.Fatalf("matrix wheel %d", chat.matrixY)
	}
	plainAt(t, m, 140, 42)
	if chat.matrixY != 1 {
		t.Fatal("render clamped a row that is in range")
	}

	chat.overlay = chatPager
	chat.reader = chatReader{title: "note", body: strings.Repeat("paragraph\n", 50)}
	plainAt(t, m, 140, 42)
	wheelHit(t, m, "chat.pager", true)
	if chat.reader.top != 1 {
		t.Fatalf("pager wheel %d", chat.reader.top)
	}
	for i := 0; i < 80; i++ {
		chat.Wheel("chat.pager", true, m)
	}
	frame := plainAt(t, m, 140, 42)
	if !strings.Contains(frame, "paragraph") {
		t.Fatalf("pager scrolled past its last line:\n%s", frame)
	}
}

func TestInboxLogWheelDirection(t *testing.T) {
	m := newTestModel(t, newFake(), newClock())
	m.Tab = tabInbox
	s := m.screens[tabInbox].(*inboxScreen)
	m.Data.Events = []envelope.Event{
		{Cursor: "c-oldest", Type: "mark", Data: json.RawMessage(`"oldest"`)},
		{Cursor: "c-older", Type: "mark", Data: json.RawMessage(`"older"`)},
		{Cursor: "c-newer", Type: "mark", Data: json.RawMessage(`"newer"`)},
		{Cursor: "c-newest", Type: "mark", Data: json.RawMessage(`"newest"`)},
	}
	peers := inboxPeers{}
	latest := s.viewInboxLog(peers, m, 70, 2)
	if !strings.Contains(latest, "c-newest") || !strings.Contains(latest, "c-newer") || strings.Contains(latest, "c-oldest") {
		t.Fatalf("latest window:\n%s", latest)
	}
	s.scrollLog(false)
	step := s.viewInboxLog(peers, m, 70, 2)
	if !strings.Contains(step, "c-older") || strings.Contains(step, "c-newest") {
		t.Fatalf("wheel up should reveal older events:\n%s", step)
	}
	s.scrollLog(true)
	back := s.viewInboxLog(peers, m, 70, 2)
	if !strings.Contains(back, "c-newest") {
		t.Fatalf("wheel down should return toward the latest:\n%s", back)
	}
	for i := 0; i < 10; i++ {
		s.scrollLog(false)
	}
	oldest := s.viewInboxLog(peers, m, 70, 2)
	if !strings.Contains(oldest, "c-oldest") || strings.TrimSpace(plain(oldest)) == "" {
		t.Fatalf("scrolled past the last event:\n%s", oldest)
	}
	for i := 0; i < 4; i++ {
		s.scrollLog(true)
	}
	s.viewInboxLog(peers, m, 70, 2)
	before := s.logTop
	s.onKey(keyPress("ctrl+u"), m)
	if s.logTop <= before {
		t.Fatalf("ctrl+u logTop %d, was %d", s.logTop, before)
	}
	s.onKey(keyPress("ctrl+d"), m)
	if s.logTop != before {
		t.Fatalf("ctrl+d logTop %d, want %d", s.logTop, before)
	}
}

func TestWhoScrollAndSuspendKey(t *testing.T) {
	m := chatListModel(t)
	chat := m.screens[tabChat].(*chatScreen)
	parts := make([]string, 30)
	for i := range parts {
		parts[i] = fmt.Sprintf("who-peer-%02d", i)
	}
	m.CurrentThread = "thr_old"
	chat.thread = "thr_old"
	m.Data.Threads.Threads = []opapi.ThreadRow{{Thread: envelope.ThreadSummary{ThreadID: "thr_old", Participants: parts}}}
	m.mouse = false
	for i := 0; i < 20; i++ {
		m.Update(keyPress(")"))
	}
	frame := plainAt(t, m, 140, 42)
	if strings.Contains(frame, "who-peer-00") || !strings.Contains(frame, "who-peer-29") {
		t.Fatalf("participant pane:\n%s", frame)
	}
	if cmd := m.updateKey(keyPress("ctrl+z")); cmd == nil {
		t.Fatal("ctrl+z produced no command")
	}
}

func chatListModel(t *testing.T) *Model {
	t.Helper()
	m := newTestModel(t, newFake(), newClock())
	m.Tab = tabChat
	m.Data.Inbox.Me.PeerID = "peer_me"
	m.Data.MineKnown = true
	m.Data.OthersKnown = true
	m.Data.Mine.Threads = []opapi.ThreadRow{mineRow("thr_old", "older yours", []string{"peer_me"}, "2026-11-02T04:41:00Z", 1)}
	return m
}

func mineRow(id, title string, parts []string, at string, n int) opapi.ThreadRow {
	return opapi.ThreadRow{
		Thread:        envelope.ThreadSummary{ThreadID: id, Title: &title, Participants: parts, MessageCount: n},
		LastMessageAt: at,
	}
}

func hasThread(rows []opapi.ThreadRow, id string) bool {
	for _, row := range rows {
		if row.Thread.ThreadID == id {
			return true
		}
	}
	return false
}

func plain(s string) string { return strip(s) }
