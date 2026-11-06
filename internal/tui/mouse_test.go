package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/opapi"
)

func TestProfileHubLabel(t *testing.T) {
	cases := []struct{ profile, hub, want string }{
		{"home", "https://hub.example:8443", "home · hub hub.example:8443"},
		{"home", "http://127.0.0.1:45752", "home · hub 127.0.0.1:45752 local"},
		{"home", "http://localhost:8080", "home · hub localhost:8080 local"},
		{"home", "http://[::1]:8443", "home · hub [::1]:8443 local"},
		{"", "https://hub.example", "profile · hub hub.example"},
		{"home", "http://10.0.0.1:8443", "home · hub 10.0.0.1:8443"},
	}
	for _, tc := range cases {
		if got := profileHubLabel(tc.profile, tc.hub); got != tc.want {
			t.Errorf("profileHubLabel(%q, %q) = %q, want %q", tc.profile, tc.hub, got, tc.want)
		}
	}
}

func TestHeaderShowsProfileAndHub(t *testing.T) {
	m, _ := loaded(t, seeded(), newClock())
	wide := strings.Split(plainAt(t, m, 140, 42), "\n")[0]
	if !strings.Contains(wide, tabBar) || !strings.Contains(wide, "home · hub hub.example:8443") {
		t.Fatalf("wide header: %q", wide)
	}
	if !strings.Contains(wide, "live") || !strings.Contains(wide, "apr 2") || !strings.Contains(wide, "cuts/24h 7") || !strings.Contains(wide, "07:41:00") {
		t.Fatalf("wide header dropped a count: %q", wide)
	}
	example := strings.Split(plainAt(t, m, 80, 24), "\n")[0]
	if !strings.Contains(example, "4:subs ") || !strings.Contains(example, "home · hub hub.example:8443") || !strings.Contains(example, "live") {
		t.Fatalf("80-col header: %q", example)
	}
	m.Hub = "http://127.0.0.1:45752"
	narrow := strings.Split(plainAt(t, m, 80, 24), "\n")[0]
	if !strings.Contains(narrow, "4:subs ") || !strings.Contains(narrow, "home · hub 127.0.0.1:45752 local") {
		t.Fatalf("narrow header: %q", narrow)
	}
}

func TestMouseTabsReleaseAndKeys(t *testing.T) {
	m, p := loaded(t, seeded(), newClock())
	frame := plainAt(t, m, 140, 42)
	if !strings.Contains(frame, "Shift+drag selects text") {
		t.Fatalf("help line missing:\n%s", frame)
	}
	if m.View().MouseMode != tea.MouseModeCellMotion {
		t.Fatalf("MouseMode = %v", m.View().MouseMode)
	}
	clickHit(t, m, "1")
	if m.Tab != 1 {
		t.Fatalf("click tab: Tab = %d", m.Tab)
	}
	p.press("M")
	if m.mouse || m.View().MouseMode != tea.MouseModeNone {
		t.Fatal("M did not release capture")
	}
	clickHit(t, m, "2")
	if m.Tab != 1 {
		t.Fatal("a click after release changed the tab")
	}
	p.press("3")
	if m.Tab != 2 {
		t.Fatalf("keyboard tab after release: Tab = %d", m.Tab)
	}
	submitLine(p, "mouse off")
	if m.mouse {
		t.Fatal(":mouse off left capture on")
	}
	submitLine(p, "mouse on")
	plainAt(t, m, 140, 42)
	if !m.mouse || m.View().MouseMode != tea.MouseModeCellMotion {
		t.Fatal(":mouse on did not capture")
	}
	clickHit(t, m, "0")
	if m.Tab != 0 {
		t.Fatalf("click after :mouse on: Tab = %d", m.Tab)
	}
	for i := 1; i <= 4; i++ {
		p.press(string(rune('0' + i)))
		if m.Tab != i-1 {
			t.Fatalf("key %d: Tab = %d", i, m.Tab)
		}
	}
}

func TestMouseChatRowsAndWheel(t *testing.T) {
	m := newTestModel(t, newFake(), newClock())
	m.Tab = 1
	m.Data.Inbox.Me.PeerID = "peer_me"
	m.Data.MineKnown = true
	m.Data.OthersKnown = true
	var mine []opapi.ThreadRow
	for i := 0; i < 30; i++ {
		mine = append(mine, sideThreadRow(fmt.Sprintf("thr_%02d", i), fmt.Sprintf("title %02d", i), []string{"peer_me"}, "2026-11-04T10:00:00Z"))
	}
	m.Data.Mine.Threads = mine
	m.Data.Others.Threads = []opapi.ThreadRow{
		sideThreadRow("thr_other", "other title", []string{"peer_a"}, "2026-11-04T09:00:00Z"),
	}
	plainAt(t, m, 140, 42)
	clickHit(t, m, "thread:thr_04")
	if m.CurrentThread != "thr_04" {
		t.Fatalf("click row: thread %q", m.CurrentThread)
	}
	chat := m.screens[1].(*chatScreen)
	before := chat.listTop
	wheelHit(t, m, "chat.list", true)
	if chat.listTop != before+3 {
		t.Fatalf("wheel list: top %d, was %d", chat.listTop, before)
	}
	short := newTestModel(t, newFake(), newClock())
	short.Tab = 1
	short.Data.Inbox.Me.PeerID = "peer_me"
	short.Data.MineKnown = true
	short.Data.OthersKnown = true
	short.Data.Mine.Threads = mine[:2]
	short.Data.Others.Threads = m.Data.Others.Threads
	plainAt(t, short, 140, 42)
	chat = short.screens[1].(*chatScreen)
	if !chat.othersOpen || !strings.Contains(plainAt(t, short, 140, 42), "other title") {
		t.Fatal("Other Threads starts open")
	}
	clickHit(t, short, "other-toggle")
	collapsed := plainAt(t, short, 140, 42)
	if chat.othersOpen || strings.Contains(collapsed, "other title") {
		t.Fatalf("clicking Other Threads did not collapse it:\n%s", collapsed)
	}
	st := &ThreadState{Messages: []envelope.Message{{MessageID: "m1"}, {MessageID: "m2"}, {MessageID: "m3"}}}
	m.Data.Thread = map[string]*ThreadState{"thr_04": st}
	long := m.screens[1].(*chatScreen)
	long.sel = "m1"
	long.follow = false
	top := long.listTop
	wheelHit(t, m, "chat.log", true)
	if long.listTop != top {
		t.Fatal("wheel on the log moved the thread list")
	}
	if long.sel != "m2" {
		t.Fatalf("wheel on the log: selected %q", long.sel)
	}
}

func TestChatListMatchesSidebar(t *testing.T) {
	m := newTestModel(t, newFake(), newClock())
	m.Tab = 1
	m.Data.Inbox.Me.PeerID = "peer_me"
	m.Data.Inbox.UnreadByThread = map[string]int{"thr_old": 5, "thr_other_new": 9}
	m.Data.MineKnown = true
	m.Data.OthersKnown = true
	m.Data.Mine.Threads = []opapi.ThreadRow{
		sideThreadRow("thr_old", "older yours", []string{"peer_me", "peer_a"}, "2026-11-04T08:00:00Z"),
		sideThreadRow("thr_new", "newer yours", []string{"peer_me", "peer_b"}, "2026-11-04T11:00:00Z"),
	}
	m.Data.Others.Threads = []opapi.ThreadRow{
		sideThreadRow("thr_other_old", "older other", []string{"peer_c"}, "2026-11-04T07:00:00Z"),
		sideThreadRow("thr_other_new", "newer other", []string{"peer_d"}, "2026-11-04T12:00:00Z"),
	}
	frame := plainAt(t, m, 140, 42)
	yours := strings.Index(frame, "Your Threads")
	other := strings.Index(frame, "Other Threads")
	if yours < 0 || other < yours {
		t.Fatalf("sections:\n%s", frame)
	}
	if strings.Index(frame, "older yours") > strings.Index(frame, "newer yours") {
		t.Fatalf("Your Threads did not keep hub order:\n%s", frame)
	}
	if strings.Index(frame, "newer other") > strings.Index(frame, "older other") {
		t.Fatalf("Other Threads is not newest first:\n%s", frame)
	}
	if !strings.Contains(frame, "older yours 5") || strings.Contains(frame, "newer other 9") {
		t.Fatalf("unread badges:\n%s", frame)
	}
	chat := m.screens[1].(*chatScreen)
	chat.othersOpen = false
	collapsed := plainAt(t, m, 140, 42)
	if strings.Contains(collapsed, "newer other") || !strings.Contains(collapsed, "Other Threads +") {
		t.Fatalf("collapsed:\n%s", collapsed)
	}
	if strings.Index(collapsed, "older yours") > strings.Index(collapsed, "newer yours") {
		t.Fatal("collapse reordered Your Threads")
	}
}

func TestDashClickSelectsPeer(t *testing.T) {
	m := newTestModel(t, newFake(), newClock())
	m.Tab = 2
	m.Data.Peers = []opapi.PeerRow{
		{Peer: opapi.Peer{PeerID: "peer_a", Name: "alpha", Kind: "operator", Presence: "idle"}},
		{Peer: opapi.Peer{PeerID: "peer_b", Name: "beta", Kind: "operator", Presence: "idle"}},
	}
	plainAt(t, m, 140, 42)
	clickHit(t, m, "peer:peer_b")
	d := m.screens[2].(*dashScreen)
	if d.peer != "peer_b" || d.focus != dashOnPeers {
		t.Fatalf("peer click: %+v", d)
	}
}

func clickHit(t *testing.T, m *Model, id string) {
	t.Helper()
	h, ok := hitByID(m, id)
	if !ok {
		t.Fatalf("no hit %s in %d hits", id, len(m.hits))
	}
	m.Update(tea.MouseClickMsg{X: h.x, Y: h.y, Button: tea.MouseLeft})
}

func wheelHit(t *testing.T, m *Model, id string, down bool) {
	t.Helper()
	h, ok := hitByID(m, id)
	if !ok {
		t.Fatalf("no hit %s", id)
	}
	btn := tea.MouseWheelUp
	if down {
		btn = tea.MouseWheelDown
	}
	m.Update(tea.MouseWheelMsg{X: h.x, Y: h.y, Button: btn})
}

func hitByID(m *Model, id string) (hit, bool) {
	for i := len(m.hits) - 1; i >= 0; i-- {
		if m.hits[i].id == id {
			return m.hits[i], true
		}
	}
	return hit{}, false
}

func submitLine(p *pump, line string) {
	p.press(":")
	for _, r := range line {
		p.press(string(r))
	}
	p.press("enter")
}

func sideThreadRow(id, title string, parts []string, at string) opapi.ThreadRow {
	return opapi.ThreadRow{
		Thread:        envelope.ThreadSummary{ThreadID: id, Title: &title, Participants: parts, MessageCount: 1},
		LastMessageAt: at,
	}
}
