package tui

import (
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/XertroV/vecnet/internal/opapi"
	"github.com/XertroV/vecnet/internal/tui/theme"
)

// chatListLine is one visual line of the chat thread list.
type chatListLine struct {
	text string
	id   string // thread id when the line opens that thread
	btn  string // button id when the line is not a thread
}

func (s *chatScreen) groups(m *Model) (yours, others sideList) {
	me := m.Data.Inbox.Me.PeerID
	now := m.HubNow().UnixMilli()
	nameOf := func(id string) string { return m.sideName(id) }
	unread := sideAnyUnread(m.Data.Inbox.UnreadByThread)
	var page *sidePage
	switch {
	case m.Data.MineKnown:
		p := sidePageOf(m.Data.Mine)
		page = &p
	case m.Data.ThreadsKnown && me != "":
		// An unfiltered page is not "yours" until the operator is known.
		// A failed first load leaves ThreadsKnown false, so it stays an error
		// rather than an empty successful list.
		rows := sideFilter(m.Data.Threads.Threads, me, true)
		p := sidePageOf(opapi.ThreadPage{Threads: rows})
		page = &p
	}
	err := m.Data.Errs["mine"]
	if page == nil && err == nil {
		err = m.Data.Errs["threads"]
	}
	yours = yourThreads(sideYours{Page: page, Err: err, Me: me, NameOf: nameOf, Now: now, Unread: unread})

	var op *sidePage
	switch {
	case m.Data.OthersKnown:
		p := sidePageOf(m.Data.Others)
		op = &p
	case m.Data.ThreadsKnown && me != "":
		rows := sideFilter(m.Data.Threads.Threads, me, false)
		p := sidePageOf(opapi.ThreadPage{Threads: rows})
		op = &p
	}
	oerr := m.Data.Errs["other"]
	if op == nil && oerr == nil {
		oerr = m.Data.Errs["threads"]
	}
	mine := make([]string, len(yours.Rows))
	for i, r := range yours.Rows {
		mine[i] = r.ID
	}
	limit := s.otherLimit
	if limit <= 0 {
		limit = otherPage
	}
	others = otherThreads(sideOthers{Page: op, Err: oerr, Mine: mine, Me: me, NameOf: nameOf, Now: now, Limit: limit})
	return yours, others
}

func (m *Model) sideName(id string) string {
	for _, row := range m.Data.Peers {
		if row.Peer.PeerID == id {
			if name := cleanSideName(row.Peer.Name); name != "" {
				return name
			}
		}
	}
	return sideShort(id)
}

func sideAnyUnread(by map[string]int) map[string]any {
	if by == nil {
		return nil
	}
	out := make(map[string]any, len(by))
	for k, v := range by {
		out[k] = v
	}
	return out
}

func sidePageOf(p opapi.ThreadPage) sidePage {
	out := sidePage{Next: p.Next != nil && *p.Next != ""}
	for _, row := range p.Threads {
		title := ""
		if row.Thread.Title != nil {
			title = *row.Thread.Title
		}
		n := row.Thread.MessageCount
		out.Threads = append(out.Threads, sideSrc{
			Thread:        sideThread{ID: row.Thread.ThreadID, Title: title, Participants: row.Thread.Participants, Count: &n},
			LastMessageAt: row.LastMessageAt,
		})
	}
	return out
}

// sideFilter keeps rows the operator is in when mine is true, and the rest otherwise.
func sideFilter(rows []opapi.ThreadRow, me string, mine bool) []opapi.ThreadRow {
	out := make([]opapi.ThreadRow, 0, len(rows))
	for _, row := range rows {
		in := containsString(row.Thread.Participants, me)
		if in == mine {
			out = append(out, row)
		}
	}
	return out
}

func (s *chatScreen) selectable(m *Model) []string {
	yours, others := s.groups(m)
	ids := make([]string, 0, len(yours.Rows)+len(others.Rows))
	for _, r := range yours.Rows {
		ids = append(ids, r.ID)
	}
	if s.othersOpen {
		for _, r := range others.Rows {
			ids = append(ids, r.ID)
		}
	}
	return ids
}

// chatSelectableIDs is the chat list in the order :thread counts. A shell test
// that swapped in static screens falls back to the hub page.
func chatSelectableIDs(m *Model) []string {
	for _, s := range m.screens {
		if c, ok := s.(*chatScreen); ok {
			return c.selectable(m)
		}
	}
	rows := m.Data.Threads.Threads
	ids := make([]string, len(rows))
	for i, r := range rows {
		ids[i] = r.Thread.ThreadID
	}
	return ids
}

func (s *chatScreen) listLines(m *Model, w int) []chatListLine {
	th := m.th
	yours, others := s.groups(m)
	var lines []chatListLine
	note := func(text string) {
		lines = append(lines, chatListLine{text: th.Fg(theme.Muted).Render(fit(text, w))})
	}
	rowLines := func(rows []sideRow) {
		for _, r := range rows {
			on := r.ID == m.CurrentThread
			lines = append(lines, chatListLine{text: sideTitleLine(th, r, w, on), id: r.ID})
			lines = append(lines, chatListLine{text: sideMetaLine(th, r, w, on), id: r.ID})
		}
	}
	section := func(label, mark, btn string, n int) {
		lines = append(lines,
			chatListLine{text: fit("", w)},
			chatListLine{text: sectionHead(th, label, mark, n, w), btn: btn},
			chatListLine{text: sectionRule(th, w), btn: btn},
		)
	}
	section("Your Threads", "", "", len(yours.Rows))
	if len(yours.Rows) == 0 {
		note(yours.Note)
	} else {
		rowLines(yours.Rows)
	}
	mark := "▾"
	if !s.othersOpen {
		mark = "▸"
	}
	section("Other Threads", mark, "other-toggle", len(others.Rows))
	if s.othersOpen {
		if len(others.Rows) == 0 {
			note(others.Note)
		} else {
			rowLines(others.Rows)
		}
		switch others.More {
		case "show":
			lines = append(lines, chatListLine{text: th.Fg(theme.Action).Render(fit("Show more", w)), btn: "other-more"})
		case "dash":
			lines = append(lines, chatListLine{text: th.Fg(theme.Muted).Render(fit("All threads on the dashboard", w)), btn: "other-dash"})
		}
	}
	return lines
}

// sectionHead is the accent bar, a bold label, the collapse mark and a
// right-aligned muted count (design §6.6). The label gives up cells before the
// bar, the mark or the count do. Colour is foreground only.
func sectionHead(th *theme.Theme, label, mark string, n, w int) string {
	if w < 1 {
		return ""
	}
	bar := th.Fg(theme.Accent).Render("▌")
	count := strconv.Itoa(n)
	markPart := ""
	if mark != "" {
		markPart = " " + mark
	}
	// bar, the space after it, the mark, and the space before the count.
	fixed := 2 + lipgloss.Width(markPart) + 1 + lipgloss.Width(count)
	room := max(w-fixed, 0)
	lab := ansi.Truncate(label, room, "")
	gap := w - fixed - lipgloss.Width(lab)
	if gap < 0 {
		gap = 0
	}
	body := th.Fg(theme.Bold).Render(lab + markPart)
	return bar + " " + body + strings.Repeat(" ", gap) + th.Fg(theme.Muted).Render(" "+count)
}

// sectionRule is the light rule under a section head.
func sectionRule(th *theme.Theme, w int) string {
	if w < 1 {
		return ""
	}
	return th.Fg(theme.Rule).Render(strings.Repeat("─", w))
}

// sideTitleLine keeps the unread badge, or else the message count, and
// truncates the title into whatever room is left. An unread title is bold
// with its count in unread (design §6.7); the current thread is filled.
func sideTitleLine(th *theme.Theme, r sideRow, w int, on bool) string {
	title := Clean(r.Title)
	titleRole, badgeRole := theme.Text, theme.Muted
	var badge string
	switch {
	case r.Unread > 0 && r.UnreadText != "":
		titleRole = theme.Bold
		badge, badgeRole = r.UnreadText, theme.Unread
	case r.Count != "":
		badge = r.Count
	}
	if badge == "" {
		return paintFit(th, titleRole, title, w, on)
	}
	bw := lipgloss.Width(badge) + 1
	if bw >= w {
		return paintFit(th, badgeRole, badge, w, on)
	}
	return paintFit(th, titleRole, ansi.Truncate(title, w-bw, "…"), w-bw, on) + paintFit(th, badgeRole, " "+badge, bw, on)
}

// sideMetaLine reserves the activity time and truncates the participant text
// into the cells that remain. The message count lives on the title line.
func sideMetaLine(th *theme.Theme, r sideRow, w int, on bool) string {
	people, active := Clean(r.People), Clean(r.Active)
	if active == "" {
		return paintFit(th, theme.Muted, people, w, on)
	}
	aw := lipgloss.Width(active)
	if aw >= w {
		return paintFit(th, theme.Muted, active, w, on)
	}
	room := w - aw - 1
	if people == "" || room < 1 {
		return paintFit(th, theme.Muted, active, w, on)
	}
	shown := ansi.Truncate(people, room, "…")
	gap := room - lipgloss.Width(shown)
	if gap < 0 {
		gap = 0
	}
	return paintFit(th, theme.Muted, shown, lipgloss.Width(shown), on) + paintFit(th, theme.Muted, strings.Repeat(" ", gap), gap, on) + paintFit(th, theme.Muted, " "+active, aw+1, on)
}

// paintFit is text in role r, cut or padded to w cells. On the current thread
// (on) the padding is filled too, so both its lines are filled edge to edge.
func paintFit(th *theme.Theme, r theme.Role, text string, w int, on bool) string {
	if w < 1 {
		return ""
	}
	text = ansi.Truncate(text, w, "")
	pad := w - lipgloss.Width(text)
	if pad < 0 {
		pad = 0
	}
	if on {
		return th.Sel(r).Render(text + strings.Repeat(" ", pad))
	}
	return th.Fg(r).Render(text) + strings.Repeat(" ", pad)
}

// renderList draws the thread list and records click and wheel targets for it.
func (s *chatScreen) renderList(m *Model, listW, paneH int) string {
	w, h := listW-2, paneH-2
	if w < 1 || h < 1 {
		return ""
	}
	lines := s.listLines(m, w)
	s.listN = len(lines)
	s.listH = h
	if s.followList {
		s.reveal(m.CurrentThread, lines, h)
		s.followList = false
	}
	maxTop := max(len(lines)-h, 0)
	if s.listTop > maxTop {
		s.listTop = maxTop
	}
	if s.listTop < 0 {
		s.listTop = 0
	}
	m.addHit(m.hitX, m.hitY, listW, paneH, hitScroll, "chat.list")
	shown := lines[s.listTop:min(s.listTop+h, len(lines))]
	out := make([]string, len(shown))
	for i, ln := range shown {
		out[i] = ln.text
		switch {
		case ln.btn != "":
			m.addHit(m.hitX+1, m.hitY+1+i, w, 1, hitButton, ln.btn)
		case ln.id != "":
			m.addHit(m.hitX+1, m.hitY+1+i, w, 1, hitRow, "thread:"+ln.id)
		}
	}
	return strings.Join(out, "\n")
}

func (s *chatScreen) reveal(id string, lines []chatListLine, h int) {
	if id == "" || h <= 0 {
		return
	}
	for i, ln := range lines {
		if ln.id != id {
			continue
		}
		end := i
		if i+1 < len(lines) && lines[i+1].id == id {
			end = i + 1
		}
		if i < s.listTop {
			s.listTop = i
		}
		if end >= s.listTop+h {
			s.listTop = end - h + 1
		}
		return
	}
}

// toggleOthers collapses or expands Other Threads. The mouse and `o` share it.
func (s *chatScreen) toggleOthers() {
	s.othersOpen = !s.othersOpen
	s.followList = true
}

// moreOthers is Show more until the 200-thread cap, then the dashboard handoff.
// The mouse buttons and `n` share it.
func (s *chatScreen) moreOthers(m *Model) tea.Cmd {
	if !s.othersOpen {
		return nil
	}
	_, others := s.groups(m)
	switch others.More {
	case "show":
		s.otherLimit = nextLimit(s.otherLimit)
		m.OtherLimit = s.otherLimit
		s.followList = true
		return m.Reload("other")
	case "dash":
		s.openDashboard(m)
	}
	return nil
}

// openDashboard leaves chat for an unfiltered dashboard that can walk every
// cursor page. A peer narrowing left from an earlier visit is cleared.
func (s *chatScreen) openDashboard(m *Model) {
	m.prefsOn = false
	m.Tab = tabDash
	d, ok := m.screens[tabDash].(*dashScreen)
	if !ok {
		return
	}
	d.narrow = nil
	d.pageGen++
	d.generationSnap = ""
	d.extra = nil
	d.extraNext = nil
	d.extraBase = ""
	d.focus = dashOnThreads
}

func (s *chatScreen) Click(id string, m *Model) tea.Cmd {
	switch id {
	case "other-toggle":
		s.toggleOthers()
		return nil
	case "other-more", "other-dash":
		return s.moreOthers(m)
	}
	if tid, ok := strings.CutPrefix(id, "thread:"); ok {
		s.followList = true
		cmd := m.OpenThread(tid)
		s.track(m)
		return cmd
	}
	return nil
}

func (s *chatScreen) Wheel(id string, down bool, m *Model) {
	by := 1
	if !down {
		by = -1
	}
	switch id {
	case "chat.list":
		s.followList = false
		step := 3 * by
		maxTop := max(s.listN-s.listH, 0)
		s.listTop = min(max(s.listTop+step, 0), maxTop)
	case "chat.log":
		s.move(m.Data.Thread[m.CurrentThread], by)
	case "chat.who":
		s.whoFrom += by
		if s.whoFrom < 0 {
			s.whoFrom = 0
		}
	case "chat.matrix":
		if down {
			s.matrixY++
		} else {
			s.matrixY--
		}
	case "chat.pager":
		if down {
			s.reader.top++
		} else {
			s.reader.top--
		}
	}
}
