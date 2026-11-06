package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/XertroV/vecnet/internal/opapi"
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
	case !m.first["threads"]:
		rows := m.Data.Threads.Threads
		if me != "" {
			rows = sideFilter(rows, me, true)
		}
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
	case !m.first["threads"]:
		rows := m.Data.Threads.Threads
		if me != "" {
			rows = sideFilter(rows, me, false)
		} else {
			rows = nil
		}
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
	yours, others := s.groups(m)
	var lines []chatListLine
	head := func(text, btn string) {
		lines = append(lines, chatListLine{text: styleAccent.Render(fit(text, w)), btn: btn})
	}
	note := func(text string) {
		lines = append(lines, chatListLine{text: styleMuted.Render(fit(text, w))})
	}
	rowLines := func(rows []sideRow) {
		for _, r := range rows {
			on := r.ID == m.CurrentThread
			lines = append(lines, chatListLine{text: sidePaint(sideTitleLine(r, w), w, on), id: r.ID})
			lines = append(lines, chatListLine{text: sidePaint(sideMetaLine(r, w), w, on), id: r.ID})
		}
	}
	head("Your Threads", "")
	if len(yours.Rows) == 0 {
		note(yours.Note)
	} else {
		rowLines(yours.Rows)
	}
	mark := " -"
	if !s.othersOpen {
		mark = " +"
	}
	head(ansi.Truncate("Other Threads"+mark, w, ""), "other-toggle")
	if s.othersOpen {
		if len(others.Rows) == 0 {
			note(others.Note)
		} else {
			rowLines(others.Rows)
		}
		switch others.More {
		case "show":
			lines = append(lines, chatListLine{text: styleAccent.Render(fit("Show more", w)), btn: "other-more"})
		case "dash":
			lines = append(lines, chatListLine{text: styleMuted.Render(fit("All threads on the dashboard", w)), btn: "other-dash"})
		}
	}
	return lines
}

func sideTitleLine(r sideRow, w int) string {
	title := Clean(r.Title)
	if r.UnreadText == "" {
		return ansi.Truncate(title, w, "…")
	}
	badge := r.UnreadText
	bw := lipgloss.Width(badge) + 1
	if bw >= w {
		return ansi.Truncate(badge, w, "")
	}
	return ansi.Truncate(title, w-bw, "…") + " " + badge
}

func sideMetaLine(r sideRow, w int) string {
	parts := []string{}
	if p := Clean(r.People); p != "" {
		parts = append(parts, p)
	}
	if r.Active != "" {
		parts = append(parts, r.Active)
	}
	if r.Count != "" {
		parts = append(parts, r.Count)
	}
	return ansi.Truncate(strings.Join(parts, " · "), w, "…")
}

func sidePaint(text string, w int, on bool) string {
	text = fit(text, w)
	if on {
		return styleText.Background(colorPick).Render(text)
	}
	return styleText.Render(text)
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

func (s *chatScreen) Click(id string, m *Model) tea.Cmd {
	switch id {
	case "other-toggle":
		s.othersOpen = !s.othersOpen
		return nil
	case "other-more":
		s.otherLimit = nextLimit(s.otherLimit)
		m.OtherLimit = s.otherLimit
		s.followList = true
		return m.Reload("other")
	case "other-dash":
		m.prefsOn = false
		m.Tab = tabDash
		return nil
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
	}
}
