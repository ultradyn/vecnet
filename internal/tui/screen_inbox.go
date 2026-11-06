package tui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/opapi"
	"github.com/XertroV/vecnet/internal/opclient"
	"github.com/XertroV/vecnet/internal/tui/theme"
)

const (
	// inboxBeatEvery keeps presence inside its 1 h TTL.
	inboxBeatEvery = 20 * time.Minute
	inboxTagW      = 4
	inboxTimeW     = 5
	inboxTailW     = 11
	inboxPresence  = "presence" // the lock key for the operator's own presence
	// inboxHintsW is the width from which the status line holds every key.
	inboxHintsW = 120
)

// inboxRow is one line of the typed list. Exactly one of apr, dlq, sub and dly
// is set. key names the item across reloads.
type inboxRow struct {
	tag, key string
	apr      *opapi.OpenApproval
	dlq      *opapi.DeadLetter
	sub      *opapi.Attention
	dly      *opapi.InboxDelivery
}

// inboxPrompt is an open answer or discard prompt. It holds its own copy of the
// item, so a reload that shifts the list cannot point the key at another row.
type inboxPrompt struct {
	key     string
	answer  *opapi.OpenApproval
	discard *opapi.DeadLetter
}

// inboxLock claims an item from the moment an action starts until a snapshot
// taken after it finished lands, because until then the list may still offer a
// row the hub has closed (Review Focus 2). hide takes the row off the list for
// that time: the hub said it is gone, and the snapshot has not caught up.
type inboxLock struct{ done, hide bool }

type inboxAction struct {
	key, verb, good string
	hide            bool
	tags            []string
}

type inboxDoneMsg struct {
	inboxAction
	err error
}

type inboxBeatMsg struct {
	posted bool
	err    error
}

// inboxScreen is the inbox tab: the typed list over the raw event log, with the
// selected item's detail on the right.
type inboxScreen struct {
	sel      string // the selected row's key
	at, top  int    // its index, kept for when it leaves, and the first row shown
	compact  bool   // the last frame was too narrow for the full hints
	prompt   *inboxPrompt
	locks    map[string]*inboxLock
	nextBeat time.Time
	logTop   int
	logN     int // events the log can show
	logH     int // rows the log pane has
}

func newInboxScreen() Screen { return &inboxScreen{locks: map[string]*inboxLock{}} }

func (s *inboxScreen) Title() string { return "inbox" }

func (s *inboxScreen) Hints() string {
	switch {
	case s.prompt == nil && s.compact:
		return "a answer  r retry  x drop  p status"
	case s.prompt == nil:
		return "j/k move  a answer  r retry  x discard  p presence  enter open  ctrl+u/d log  ? help"
	case s.prompt.answer != nil:
		return fmt.Sprintf("1-%d answer  any other key cancels", len(s.prompt.answer.Options))
	}
	return "y discard  any other key cancels"
}

func (s *inboxScreen) Update(msg tea.Msg, m *Model) tea.Cmd {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		return s.onKey(msg, m)
	case Loaded:
		return s.onLoaded(msg, m)
	case inboxDoneMsg:
		return s.onDone(msg, m)
	case inboxBeatMsg:
		if msg.posted && msg.err == nil {
			return m.Reload("inbox", "peers")
		}
	}
	return nil
}

// rows is the list in the prototype's order, without items an action removed.
func (s *inboxScreen) rows(m *Model) []inboxRow {
	in := &m.Data.Inbox
	rows := make([]inboxRow, 0, len(in.Approvals)+len(in.DeadLetters)+len(in.Attention)+len(in.Deliveries))
	add := func(row inboxRow) {
		if l := s.locks[row.key]; l == nil || !l.hide {
			rows = append(rows, row)
		}
	}
	for i := range in.Approvals {
		a := &in.Approvals[i]
		add(inboxRow{tag: "APR", key: "apr:" + a.ApprovalID, apr: a})
	}
	for i := range in.DeadLetters {
		d := &in.DeadLetters[i]
		add(inboxRow{tag: "DLQ", key: "dlq:" + d.DeliveryID, dlq: d})
	}
	for i := range in.Attention {
		a := &in.Attention[i]
		add(inboxRow{tag: "SUB", key: "sub:" + a.ID, sub: a})
	}
	for _, section := range []struct{ name, tag string }{{"woke", "WAKE"}, {"held", "HOLD"}} {
		for i := range in.Deliveries {
			if d := &in.Deliveries[i]; d.Section == section.name {
				add(inboxRow{tag: section.tag, key: "dly:" + d.DeliveryID, dly: d})
			}
		}
	}
	return rows
}

// cursor finds the selection in rows. An item that has gone leaves it on the
// row now at the same index.
func (s *inboxScreen) cursor(rows []inboxRow) int {
	if i := slices.IndexFunc(rows, func(r inboxRow) bool { return r.key == s.sel }); i >= 0 {
		s.at = i
		return i
	}
	s.at = max(min(s.at, len(rows)-1), 0)
	s.sel = ""
	if len(rows) > 0 {
		s.sel = rows[s.at].key
	}
	return s.at
}

func (s *inboxScreen) onKey(msg tea.KeyPressMsg, m *Model) tea.Cmd {
	if s.prompt != nil {
		return s.onPromptKey(msg.String(), m)
	}
	if m.firstLoading() {
		return nil
	}
	key := msg.String()
	rows := s.rows(m)
	at := s.cursor(rows)
	switch key {
	case "j", "down":
		s.moveTo(rows, at+1)
		return nil
	case "k", "up":
		s.moveTo(rows, at-1)
		return nil
	case "p":
		return s.togglePresence(m)
	case "ctrl+u":
		s.scrollLog(false)
		return nil
	case "ctrl+d":
		s.scrollLog(true)
		return nil
	}
	if len(rows) == 0 {
		return nil
	}
	row := rows[at]
	switch key {
	case "enter":
		return s.open(row, m)
	case "a":
		s.askAnswer(row, m)
	case "r":
		return s.retry(row, m)
	case "x":
		s.askDiscard(row, m)
	}
	return nil
}

func (s *inboxScreen) moveTo(rows []inboxRow, at int) {
	if at >= 0 && at < len(rows) {
		s.at, s.sel = at, rows[at].key
	}
}

// onPromptKey ends the prompt whatever the key is: every key but the one that
// confirms is a cancel, and the prompt's Insert mode keeps it from the shell.
func (s *inboxScreen) onPromptKey(key string, m *Model) tea.Cmd {
	p := s.prompt
	s.closePrompt(m)
	switch {
	case p.discard != nil && key == "y":
		return s.discard(*p.discard, m)
	case p.answer != nil && len(key) == 1 && key[0] >= '1' && key[0] <= '9':
		if option := int(key[0] - '1'); option < len(p.answer.Options) {
			return s.answer(*p.answer, option, m)
		}
	}
	return nil
}

func (s *inboxScreen) openPrompt(p *inboxPrompt, m *Model) {
	s.prompt = p
	m.Mode = ModeInsert
}

func (s *inboxScreen) closePrompt(m *Model) {
	s.prompt = nil
	m.Mode = ModeNormal
}

// free reports whether an action may start on key, and says why not if it may
// not. A rejected credential gets no more requests.
func (s *inboxScreen) free(key string, m *Model) bool {
	if m.stopped() {
		m.Toast(m.Conn+": restart with a valid profile", true)
		return false
	}
	if s.locks[key] != nil {
		m.Toast("already in progress", true)
		return false
	}
	return true
}

// start claims a.key and runs call. Its result reaches onDone on any tab.
func (s *inboxScreen) start(a inboxAction, m *Model, call func(context.Context) error) tea.Cmd {
	s.locks[a.key] = &inboxLock{}
	ctx := m.ctx
	return func() tea.Msg { return inboxDoneMsg{inboxAction: a, err: call(ctx)} }
}

func (s *inboxScreen) askAnswer(row inboxRow, m *Model) {
	switch {
	case row.apr == nil:
		m.Toast("a answers an approval", true)
	case inboxExpired(*row.apr, m.HubNow()):
		m.Toast("approval expired: nothing sent", true)
	case s.free(row.key, m):
		a := *row.apr
		s.openPrompt(&inboxPrompt{key: row.key, answer: &a}, m)
	}
}

func (s *inboxScreen) answer(a opapi.OpenApproval, option int, m *Model) tea.Cmd {
	key := "apr:" + a.ApprovalID
	if inboxExpired(a, m.HubNow()) {
		m.Toast("approval expired: nothing sent", true)
		return nil
	}
	if !s.free(key, m) {
		return nil
	}
	api := m.API
	action := inboxAction{key: key, verb: "answer", good: "answered: " + a.Options[option], hide: true, tags: []string{"inbox", threadPrefix + a.ThreadID}}
	return s.start(action, m, func(ctx context.Context) error { return api.Answer(ctx, a, option) })
}

func (s *inboxScreen) askDiscard(row inboxRow, m *Model) {
	switch {
	case row.dlq == nil:
		m.Toast("x discards a dead letter", true)
	case s.free(row.key, m):
		d := *row.dlq
		s.openPrompt(&inboxPrompt{key: row.key, discard: &d}, m)
	}
}

func (s *inboxScreen) discard(d opapi.DeadLetter, m *Model) tea.Cmd {
	key := "dlq:" + d.DeliveryID
	if !s.free(key, m) {
		return nil
	}
	api := m.API
	action := inboxAction{key: key, verb: "discard", good: "discarded", hide: true, tags: []string{"inbox", "health", threadPrefix + d.ThreadID}}
	return s.start(action, m, func(ctx context.Context) error { return api.DiscardDeadLetter(ctx, d.DeliveryID) })
}

// retry is r: a dead letter retries, and a SUB row resumes only if the hub
// offers it. A paused_token or paused_auth subscription would only re-pause.
func (s *inboxScreen) retry(row inboxRow, m *Model) tea.Cmd {
	api := m.API
	switch {
	case row.dlq != nil:
		if !s.free(row.key, m) {
			return nil
		}
		id := row.dlq.DeliveryID
		action := inboxAction{key: row.key, verb: "retry", good: "retrying", hide: true, tags: []string{"inbox", "health", threadPrefix + row.dlq.ThreadID}}
		return s.start(action, m, func(ctx context.Context) error { return api.RetryDeadLetter(ctx, id) })
	case row.sub != nil && row.sub.Action == "resume":
		if !s.free(row.key, m) {
			return nil
		}
		id := row.sub.ID
		action := inboxAction{key: row.key, verb: "resume", good: "resumed", tags: []string{"inbox", "subs"}}
		return s.start(action, m, func(ctx context.Context) error { return api.ResumeSubscription(ctx, id) })
	case row.sub != nil:
		m.Toast(inboxNoResume(row.sub.Health), true)
	default:
		m.Toast("r retries a dead letter or resumes a subscription", true)
	}
	return nil
}

func inboxNoResume(health string) string {
	switch health {
	case opapi.HealthPausedToken:
		return "no resume here: ChatGPT must re-subscribe"
	case opapi.HealthPausedAuth:
		// The hub reports several causes as this one, and only its note in the
		// detail says which.
		return "no resume here: the detail says what it needs"
	}
	return "no resume here: it would not help this subscription"
}

// open shows the item's thread on the chat tab and marks it read.
func (s *inboxScreen) open(row inboxRow, m *Model) tea.Cmd {
	thread := ""
	switch {
	case row.apr != nil:
		thread = row.apr.ThreadID
	case row.dlq != nil:
		thread = row.dlq.ThreadID
	case row.dly != nil:
		thread = row.dly.ThreadID
	default:
		m.Toast("a subscription has no thread to open", true)
		return nil
	}
	opened := m.OpenThread(thread)
	if m.stopped() {
		return opened
	}
	api := m.API
	action := inboxAction{key: "read:" + thread, verb: "mark read", tags: []string{"inbox", threadPrefix + thread}}
	return tea.Batch(opened, s.start(action, m, func(ctx context.Context) error {
		_, err := api.ReadThread(ctx, thread)
		return err
	}))
}

// toggleTarget is Task 14's rule: idle and working swap, and anything else,
// including a stale unknown, goes to working.
func toggleTarget(presence string) string {
	if presence == "working" {
		return "idle"
	}
	return "working"
}

// togglePresence changes nothing on screen by itself: the status bar moves
// when the reload after the write brings the hub's state.
func (s *inboxScreen) togglePresence(m *Model) tea.Cmd {
	if !s.free(inboxPresence, m) {
		return nil
	}
	api, at, target := m.API, m.HubNow(), toggleTarget(m.Data.Inbox.Me.Presence)
	action := inboxAction{key: inboxPresence, verb: "presence", good: "presence: " + target, tags: []string{"inbox", "peers"}}
	return s.start(action, m, func(ctx context.Context) error { return api.SetPresence(ctx, target, at) })
}

// onDone reports one action, once, and reloads either way: the snapshot is the
// only account of what the hub now holds. startLoads, not Reload, so a load
// that lands after this one was asked for cannot be the action's own.
func (s *inboxScreen) onDone(msg inboxDoneMsg, m *Model) tea.Cmd {
	closed := inboxClosed(msg.err)
	switch {
	case msg.err == nil:
		if msg.good != "" {
			m.Toast(msg.good, false)
		}
	case closed:
		m.Toast("approval already closed: your answer did not land", true)
	case !errors.Is(msg.err, context.Canceled):
		m.Toast(msg.verb+" failed: "+msg.err.Error(), true)
	}
	if msg.err == nil || closed {
		s.locks[msg.key] = &inboxLock{done: true, hide: msg.hide}
	} else {
		delete(s.locks, msg.key)
	}
	return m.startLoads(msg.tags, nil)
}

func inboxClosed(err error) bool {
	var e *opclient.Error
	return errors.As(err, &e) && e.Code == "approval_closed"
}

// onLoaded releases finished actions once a snapshot from after them has
// landed: m.inflight means a later load is already on its way, and that one
// may be it. It also closes a prompt whose item has gone.
func (s *inboxScreen) onLoaded(msg Loaded, m *Model) tea.Cmd {
	if msg.Err != nil {
		return nil
	}
	if msg.Key == "inbox" {
		if !m.inflight["inbox"] {
			for key, lock := range s.locks {
				if lock.done {
					delete(s.locks, key)
				}
			}
		}
		s.checkPrompt(m)
	}
	return s.beat(m)
}

func (s *inboxScreen) checkPrompt(m *Model) {
	p := s.prompt
	if p == nil || slices.ContainsFunc(s.rows(m), func(r inboxRow) bool { return r.key == p.key }) {
		return
	}
	s.closePrompt(m)
	if p.answer != nil {
		m.Toast("approval closed before you answered: nothing sent", true)
	} else {
		m.Toast("dead letter already handled: nothing sent", true)
	}
}

// beat is the presence heartbeat, Task 14's rule. It asks the hub for the state
// and re-posts that, never this client's idea of it, so two clients cannot
// fight; unknown and offline are not re-asserted. The shell's slow refresh
// supplies the Loaded that wakes it, at m.slowEvery while the hub is up, so it
// follows the model's clock on any tab.
func (s *inboxScreen) beat(m *Model) tea.Cmd {
	now := m.Now()
	if s.nextBeat.IsZero() {
		s.nextBeat = now.Add(m.beatEvery)
		return nil
	}
	if now.Before(s.nextBeat) || m.Conn != ConnLive {
		return nil
	}
	s.nextBeat = now.Add(m.beatEvery)
	api, ctx, at := m.API, m.ctx, m.HubNow()
	return func() tea.Msg {
		fresh, err := api.Inbox(ctx, opapi.InboxQuery{Limit: 1})
		if err != nil {
			return inboxBeatMsg{err: err}
		}
		// Dated when the hub served the state it re-posts: a manual change in
		// another client after that is newer, and the hub keeps it (§2.6).
		if served, err := envelope.ParseTime(fresh.Now); err == nil {
			at = served
		}
		switch state := fresh.Me.Presence; state {
		case "idle", "working", "blocked":
			return inboxBeatMsg{posted: true, err: api.SetPresence(ctx, state, at)}
		}
		return inboxBeatMsg{}
	}
}

// inboxExpired reads expiry on the hub's clock, as the hub does. An unreadable
// time leaves the decision to the hub.
func inboxExpired(a opapi.OpenApproval, hubNow time.Time) bool {
	at, err := envelope.ParseTime(a.ExpiresAt)
	return err == nil && !hubNow.Before(at)
}

// inboxCountdown is the time left as mm:ss, or h:mm:ss from an hour up. It
// rounds up, so it reaches 00:00 only when the approval has expired.
func inboxCountdown(left time.Duration) string {
	sec := int((left + time.Second - 1) / time.Second)
	if sec >= 3600 {
		return fmt.Sprintf("%d:%02d:%02d", sec/3600, sec/60%60, sec%60)
	}
	return fmt.Sprintf("%02d:%02d", sec/60, sec%60)
}

// inboxLeft is what the row and the detail show about an approval's expiry.
func inboxLeft(a opapi.OpenApproval, m *Model) (string, bool) {
	at, err := envelope.ParseTime(a.ExpiresAt)
	switch {
	case err != nil:
		return "unknown", false
	case !m.HubNow().Before(at):
		return "expired", true
	}
	return inboxCountdown(at.Sub(m.HubNow())), false
}

func inboxClock(ts, layout string, m *Model) string {
	at, err := envelope.ParseTime(ts)
	if err != nil {
		return ""
	}
	return at.In(m.loc).Format(layout)
}

// inboxShort is a cleaned id as thr_…P5R.
func inboxShort(id string) string {
	r := []rune(Clean(id))
	if len(r) <= 10 {
		return string(r)
	}
	head := slices.Index(r, '_') + 1
	if head == 0 || head > 6 {
		head = 3
	}
	return string(r[:head]) + "…" + string(r[len(r)-4:])
}

// inboxCut makes cleaned text exactly w cells, marking a cut.
func inboxCut(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if lipgloss.Width(s) > w {
		s = ansi.Truncate(s, w, "…")
	}
	return s + strings.Repeat(" ", max(w-lipgloss.Width(s), 0))
}

// inboxPeers looks peers up by id. Their names are peer-supplied; name and kind
// clean them.
type inboxPeers map[string]opapi.Peer

func newInboxPeers(m *Model) inboxPeers {
	peers := make(inboxPeers, len(m.Data.Peers))
	for _, row := range m.Data.Peers {
		peers[row.Peer.PeerID] = row.Peer
	}
	return peers
}

func (p inboxPeers) name(id string) string {
	if name := Clean(p[id].Name); name != "" {
		return name
	}
	return Clean(id)
}

func (p inboxPeers) kind(id string) string { return Clean(p[id].Kind) }

// View lays the tab out as shots/c-inbox.png: list over event log, detail on
// the right. Everything a peer supplied is cleaned before it reaches a style.
func (s *inboxScreen) View(m *Model, w, h int) string {
	s.compact = w < inboxHintsW
	rows := s.rows(m)
	at := s.cursor(rows)
	peers := newInboxPeers(m)
	leftW := max(w*55/100, min(w-30, 50))
	listH := h * 57 / 100
	counts := map[string]int{}
	for _, row := range rows {
		counts[row.tag]++
	}
	listBody := s.viewRows(rows, at, peers, m, leftW-2, listH-2)
	s.noteListHits(m, rows, leftW, listH, h)
	list := PaneRich(m.th, inboxTitle(m.th, len(rows), m.Data.Inbox.Unread, counts["APR"], counts["DLQ"]), leftW, listH, true, listBody)
	events := Pane(m.th, "your event log  GET /v1/events -f · not messages, never replied to", leftW, h-listH, false, s.viewInboxLog(peers, m, leftW-2, h-listH-2))
	var row *inboxRow
	if at < len(rows) {
		row = &rows[at]
	}
	detail := Pane(m.th, "detail", w-leftW, h, false, s.viewDetail(row, peers, m, w-leftW-4, h-2))
	return lipgloss.JoinHorizontal(lipgloss.Top, lipgloss.JoinVertical(lipgloss.Left, list, events), detail)
}

// inboxTitle is the list's title: `2 unread` in unread, `apr 2` in warn and
// `dlq 3` in error when they are above zero, the rest in the title's style.
func inboxTitle(th *theme.Theme, items, unread, apr, dlq int) string {
	head := PaneTitle(th, true)
	count := func(n int, label string, hot theme.Role) string {
		if n > 0 {
			return th.Fg(hot).Render(label)
		}
		return head.Render(label)
	}
	sep := head.Render(" · ")
	return head.Render(fmt.Sprintf("inbox %d items", items)) + sep +
		count(unread, fmt.Sprintf("%d unread", unread), theme.Unread) + sep +
		count(apr, fmt.Sprintf("apr %d", apr), theme.Warn) + sep +
		count(dlq, fmt.Sprintf("dlq %d", dlq), theme.Error)
}

func (s *inboxScreen) viewRows(rows []inboxRow, at int, peers inboxPeers, m *Model, w, h int) string {
	if at < s.top {
		s.top = at
	}
	if at >= s.top+h {
		s.top = at - h + 1
	}
	s.top = max(min(s.top, len(rows)-h), 0)
	lines := make([]string, 0, h)
	for i := s.top; i < len(rows) && len(lines) < h; i++ {
		lines = append(lines, s.viewRow(rows[i], i == at, peers, m, w))
	}
	return strings.Join(lines, "\n")
}

func (s *inboxScreen) noteListHits(m *Model, rows []inboxRow, leftW, listH, h int) {
	m.addHit(m.hitX, m.hitY, leftW, listH, hitScroll, "inbox.list")
	innerW, innerH := leftW-2, listH-2
	for i := 0; i < innerH; i++ {
		idx := s.top + i
		if idx < 0 || idx >= len(rows) {
			break
		}
		m.addHit(m.hitX+1, m.hitY+1+i, innerW, 1, hitRow, "in:"+rows[idx].key)
	}
	m.addHit(m.hitX, m.hitY+listH, leftW, max(h-listH, 0), hitScroll, "inbox.log")
}

func (s *inboxScreen) Click(id string, m *Model) tea.Cmd {
	key, ok := strings.CutPrefix(id, "in:")
	if !ok {
		return nil
	}
	rows := s.rows(m)
	for i, row := range rows {
		if row.key == key {
			s.moveTo(rows, i)
			return nil
		}
	}
	return nil
}

func (s *inboxScreen) Wheel(id string, down bool, m *Model) {
	by := 1
	if !down {
		by = -1
	}
	switch id {
	case "inbox.list":
		rows := s.rows(m)
		s.moveTo(rows, s.cursor(rows)+by)
	case "inbox.log":
		// Wheel down returns toward the latest events. Wheel up reveals older ones.
		s.scrollLog(down)
	}
}

// scrollLog moves the event log. towardLatest decreases the offset into the
// newest-first list. The offset stays inside a full or partial window.
func (s *inboxScreen) scrollLog(towardLatest bool) {
	if towardLatest {
		s.logTop--
	} else {
		s.logTop++
	}
	if s.logTop < 0 {
		s.logTop = 0
	}
	maxTop := 0
	if s.logH > 0 {
		maxTop = max(s.logN-s.logH, 0)
	}
	if s.logTop > maxTop {
		s.logTop = maxTop
	}
}

type inboxCell struct {
	role theme.Role
	text string
	w    int
}

// viewRow is one list row. The selected row starts with `>` and is filled
// across the pane's inner width (design §6.5, §7.3). Roles come from the row's
// type and hub enums (§5.5): an unread delivery is bold, a read one muted.
func (s *inboxScreen) viewRow(row inboxRow, selected bool, peers inboxPeers, m *Model, w int) string {
	th := m.th
	nameW := max(min(w/5, 15), 8)
	textW := w - (1 + inboxTagW + 1 + inboxTimeW + 1 + nameW + 1 + 1 + inboxTailW)
	tagRole, textRole := theme.InboxTag(row.tag), theme.Text
	var when, name string
	var text []inboxCell
	tail := inboxCell{role: theme.Muted}
	sending := s.locks[row.key] != nil
	switch {
	case row.apr != nil:
		a := row.apr
		tagRole = theme.Accent
		// An open approval carries no creation time; its expiry is the time it has.
		when, name = inboxClock(a.ExpiresAt, "15:04", m), peers.name(a.Requester)
		text = []inboxCell{{textRole, Clean(inboxQuestion(*a)), textW}}
		if left, gone := inboxLeft(*a, m); gone {
			tail = inboxCell{role: theme.Error, text: left}
		} else {
			tail = inboxCell{role: theme.Warn, text: "exp " + left}
		}
	case row.dlq != nil:
		d := row.dlq
		tagRole = theme.Error
		when, name = inboxClock(d.UpdatedAt, "15:04", m), peers.name(d.Recipient)
		status := "---"
		if d.LastStatus != nil {
			status = fmt.Sprint(*d.LastStatus)
		}
		text = []inboxCell{{theme.Error, status, 3}, {theme.Muted, inboxClass(d.Class), textW - 4}}
		tail.text = inboxTries(d.Attempt)
	case row.sub != nil:
		a := row.sub
		// One colour per health in every screen: the subs tab's.
		health := theme.Health(a.Health)
		tagRole = health
		when, name = inboxClock(a.At, "15:04", m), peers.name(a.PeerID)
		text = []inboxCell{{health, Clean(a.Health), textW}}
		if textW > 16 {
			// The name column already says whose it is.
			summary := strings.TrimPrefix(Clean(a.Summary), name+" · ")
			text = []inboxCell{{health, Clean(a.Health), 15}, {theme.Muted, summary, textW - 16}}
		}
		if a.Action == "resume" {
			tail = inboxCell{role: theme.Action, text: "r resume"}
		}
	case row.dly != nil:
		d := row.dly
		textRole = theme.Bold
		if d.ReadAt != nil {
			textRole = theme.Muted
		}
		when, name = inboxClock(d.CreatedAt, "15:04", m), peers.name(d.Sender)
		text = []inboxCell{{textRole, Clean(d.Excerpt), textW}}
		state := inboxState(*d)
		tail = inboxCell{role: theme.Receipt(state), text: inboxGlyph(state)}
	}
	if sending {
		tail = inboxCell{role: theme.Accent, text: "sending"}
	}
	tail.w = inboxTailW
	cells := append([]inboxCell{{tagRole, row.tag, inboxTagW}, {theme.Muted, when, inboxTimeW}, {theme.Text, name, nameW}}, text...)
	cells = append(cells, tail)
	gap := th.On(selected, theme.Text).Render(" ")
	var b strings.Builder
	b.WriteString(selMark(th, selected))
	for i, cell := range cells {
		if i > 0 {
			b.WriteString(gap)
		}
		b.WriteString(th.On(selected, cell.role).Render(inboxCut(cell.text, cell.w)))
	}
	return selPad(th, b.String(), w, selected)
}

func inboxQuestion(a opapi.OpenApproval) string {
	if a.Question != "" {
		return a.Question
	}
	return a.Text
}

func inboxTries(n int) string {
	if n == 1 {
		return "1 try"
	}
	return fmt.Sprintf("%d tries", n)
}

func inboxClass(class string) string {
	switch class {
	case "retries_exhausted":
		return "retries exhausted"
	case "terminal":
		return "terminal 4xx"
	}
	return Clean(class)
}

// inboxState is a delivery's receipt state, read winning.
func inboxState(d opapi.InboxDelivery) string {
	if d.ReadAt != nil {
		return "read"
	}
	return Clean(d.State)
}

func inboxGlyph(state string) string {
	switch state {
	case "queued":
		return "·"
	case "pushed":
		return "›"
	case "accepted":
		return "✓"
	case "processed":
		return "✓✓"
	case "read":
		return "◉"
	case "failed", "dead_letter":
		return "✗"
	}
	return "?"
}

func inboxField(th *theme.Theme, label, value string) string {
	return inboxFieldAs(th, label, value, theme.Text)
}

// inboxFieldAs is a detail field whose value takes role r: an id is dim.
func inboxFieldAs(th *theme.Theme, label, value string, r theme.Role) string {
	return th.Fg(theme.Muted).Render(fmt.Sprintf("%-9s", label)) + th.Fg(r).Render(value)
}

// inboxKind is a peer's kind label after its name, in the kind's hue.
func inboxKind(th *theme.Theme, kind string) string {
	return th.Fg(theme.Kind(kind)).Render(kind)
}

// inboxPara wraps cleaned multi-line text to w, styling each line.
func inboxPara(st lipgloss.Style, text string, w int) []string {
	lines := strings.Split(ansi.Wrap(strings.TrimSpace(strings.ReplaceAll(text, "\t", "    ")), max(w, 1), " "), "\n")
	for i, line := range lines {
		lines[i] = st.Render(line)
	}
	return lines
}

// inboxQuote is peer text behind a rule, so no line of it can pass for one of
// the screen's own.
func inboxQuote(th *theme.Theme, st lipgloss.Style, text string, w int) []string {
	if strings.TrimSpace(text) == "" {
		return nil
	}
	lines := inboxPara(st, text, w-2)
	for i, line := range lines {
		lines[i] = th.Fg(theme.Rule).Render("│") + " " + line
	}
	return lines
}

// inboxFold is cleaned text as one paragraph. A peer's line breaks and padding
// do not survive it, so they cannot set out a fake option.
func inboxFold(text string, w int) []string {
	return strings.Split(ansi.Wrap(strings.Join(strings.Fields(text), " "), max(w, 1), " "), "\n")
}

// inboxFit is the first n lines of w cells, marking a cut.
func inboxFit(lines []string, w, n int) []string {
	if len(lines) <= n {
		return lines
	}
	lines = slices.Clone(lines[:n])
	lines[n-1] = ansi.Truncate(lines[n-1], max(w-1, 0), "") + "…"
	return lines
}

// inboxDetail sets out h lines of detail. fixed is what the operator acts on and
// always shows whole; body, a peer's text, comes after it and gets what is left,
// so nothing a peer writes can move the prompt or push it off the pane. where
// finishes the marker on a cut.
func inboxDetail(th *theme.Theme, fixed, body []string, h int, where string) []string {
	left := h - len(fixed)
	if len(body) == 0 || left <= 0 {
		return fixed
	}
	if left > 1 {
		fixed = append(fixed, "")
		left--
	}
	if len(body) > left {
		more := th.Fg(theme.Muted).Render(fmt.Sprintf("… %d more lines%s", len(body)-left+1, where))
		body = append(body[:left-1:left-1], more)
	}
	return append(fixed, body...)
}

func (s *inboxScreen) viewDetail(row *inboxRow, peers inboxPeers, m *Model, w, h int) string {
	var lines []string
	switch {
	case row == nil:
		lines = []string{m.th.Fg(theme.Muted).Render("nothing waiting on you")}
	case row.apr != nil:
		lines = s.viewApproval(*row.apr, peers, m, w, h)
	case row.dlq != nil:
		lines = s.viewDeadLetter(*row.dlq, peers, m, w, h)
	case row.sub != nil:
		lines = viewInboxSub(*row.sub, peers, m, w, h)
	case row.dly != nil:
		lines = viewInboxDelivery(*row.dly, peers, m, w, h)
	}
	for i, line := range lines {
		lines[i] = " " + line
	}
	return strings.Join(lines, "\n")
}

func (s *inboxScreen) viewApproval(a opapi.OpenApproval, peers inboxPeers, m *Model, w, h int) []string {
	th := m.th
	head := []string{
		th.Fg(theme.Accent).Render("approval_request") + "  " + th.Fg(theme.Priority("wake")).Render("wake"),
		"",
		inboxField(th, "from", peers.name(a.Requester)+" ") + inboxKind(th, peers.kind(a.Requester)),
		inboxField(th, "thread", Clean(a.ThreadTitle)),
		inboxFieldAs(th, "message", Clean(a.MessageID), theme.Dim),
		"",
	}
	left, gone := inboxLeft(a, m)
	when := inboxClock(a.ExpiresAt, "15:04", m)
	expires := th.Fg(theme.Muted).Render("expires "+when+", in ") + th.Fg(theme.Warn).Render(left)
	var foot []string
	switch {
	case gone:
		foot = []string{th.Fg(theme.Muted).Render("expired at " + when), th.Fg(theme.Error).Render("expired")}
	case s.locks["apr:"+a.ApprovalID] != nil:
		foot = []string{expires, th.Fg(theme.Accent).Render("sending your answer")}
	case s.prompt != nil && s.prompt.answer != nil:
		foot = append([]string{expires}, inboxPara(th.Fg(theme.Action), fmt.Sprintf("answer with 1-%d, any other key cancels", len(a.Options)), w)...)
	default:
		foot = []string{expires, th.Fg(theme.Action).Render("press a to answer")}
	}
	// What a digit sends comes first. Every option gets an equal share of the
	// lines left beyond one for the question and one for a marker, which the
	// protocol's eight options and the 80x24 floor keep at one or more. A wrapped
	// line carries a gutter, so it cannot pass for the next option's label.
	room := h - len(head) - len(foot)
	share := max((room-2)/max(len(a.Options), 1), 1)
	var options []string
	for i, option := range a.Options {
		for j, line := range inboxFit(inboxFold(Clean(option), w-6), w-6, share) {
			label := "  " + th.Fg(theme.Rule).Render("│") + " "
			if j == 0 {
				label = th.Fg(theme.Action).Render(fmt.Sprintf("[%d]", i+1)) + " "
			}
			options = append(options, "  "+label+th.Fg(theme.Text).Render(line))
		}
	}
	room -= len(options)
	gap := []string{""}
	if room > 2 {
		room--
	} else {
		gap = nil
	}
	question := inboxQuestion(a)
	var body []string
	if a.Text != question {
		body = inboxQuote(th, th.Fg(theme.Muted), CleanBody(a.Text), w)
	}
	// The question has what is left, ahead of the requester's text, less the
	// line the marker needs if anything will not fit. The pane does not scroll,
	// so the marker names where the rest is.
	all := inboxFold(Clean(question), w-2)
	shown := len(all)
	if len(body) > 0 || shown > room {
		shown = max(min(shown, room-1), 1)
	}
	ask := slices.Clone(inboxFit(all, w-2, shown))
	for i, line := range ask {
		ask[i] = th.Fg(theme.Accent).Render("?") + " " + th.Fg(theme.Bold).Render(line)
	}
	return inboxDetail(th, slices.Concat(head, ask, options, gap, foot), slices.Concat(all[shown:], body), h, " in thread")
}

func (s *inboxScreen) viewDeadLetter(d opapi.DeadLetter, peers inboxPeers, m *Model, w, h int) []string {
	status := "none"
	if d.LastStatus != nil {
		status = fmt.Sprint(*d.LastStatus)
	}
	th := m.th
	statusRole := theme.Muted
	if d.LastStatus != nil {
		statusRole = theme.HTTPStatus(*d.LastStatus)
	}
	lines := []string{
		th.Fg(theme.Error).Render("dead_letter") + "  " + th.Fg(theme.Muted).Render(inboxClass(d.Class)),
		"",
		inboxField(th, "from", peers.name(d.Sender)),
		inboxField(th, "to", peers.name(d.Recipient)+" ") + inboxKind(th, peers.kind(d.Recipient)),
		inboxField(th, "thread", inboxThreadTitle(m, d.ThreadID)),
		inboxFieldAs(th, "message", Clean(d.MessageID), theme.Dim),
		inboxFieldAs(th, "delivery", Clean(d.DeliveryID), theme.Dim),
		inboxFieldAs(th, "attempts", fmt.Sprint(d.Attempt), theme.Warn),
		inboxFieldAs(th, "status", status, statusRole),
		inboxField(th, "updated", inboxClock(d.UpdatedAt, "15:04:05", m)),
		"",
	}
	switch {
	case s.locks["dlq:"+d.DeliveryID] != nil:
		lines = append(lines, th.Fg(theme.Accent).Render("sending"))
	case s.prompt != nil && s.prompt.discard != nil:
		lines = append(lines, inboxPara(th.Fg(theme.Warn), "press y to discard, any other key cancels", w)...)
	default:
		lines = append(lines, inboxPara(th.Fg(theme.Action), "press r to retry, x to discard", w)...)
	}
	// Two labelled blocks, the hub's error then the message; the message is
	// left out when it only repeats the error.
	var body []string
	lastErr := ""
	if d.LastError != nil {
		lastErr = CleanBody(*d.LastError)
	}
	if quoted := inboxQuote(th, th.Fg(theme.Error), lastErr, w); len(quoted) > 0 {
		body = append(append(body, th.Fg(theme.Muted).Render("error")), quoted...)
	}
	if text := CleanBody(d.Excerpt); strings.TrimSpace(text) != strings.TrimSpace(lastErr) {
		if quoted := inboxQuote(th, th.Fg(theme.Text), text, w); len(quoted) > 0 {
			if len(body) > 0 {
				body = append(body, "")
			}
			body = append(append(body, th.Fg(theme.Muted).Render("message")), quoted...)
		}
	}
	return inboxDetail(th, lines, body, h, "")
}

// inboxThreadTitle is the thread's title, as the web shows it, with the id
// when the title is not loaded.
func inboxThreadTitle(m *Model, id string) string {
	if row := chatRowOf(m, id); row != nil {
		return chatTitleOf(row.Thread)
	}
	return Clean(id)
}

func viewInboxSub(a opapi.Attention, peers inboxPeers, m *Model, w, h int) []string {
	th := m.th
	lines := []string{
		th.Fg(theme.Warn).Render("subscription") + "  " + th.Fg(theme.Health(a.Health)).Render(Clean(a.Health)),
		"",
		inboxField(th, "peer", peers.name(a.PeerID)+" ") + inboxKind(th, peers.kind(a.PeerID)),
		inboxFieldAs(th, "id", Clean(a.ID), theme.Dim),
		inboxField(th, "seen", inboxClock(a.At, "15:04:05", m)),
		"",
	}
	if a.Action == "resume" {
		lines = append(lines, th.Fg(theme.Action).Render("press r to resume"))
	} else {
		lines = append(lines, th.Fg(theme.Muted).Render("resume would not help here"))
	}
	return inboxDetail(th, lines, inboxQuote(th, th.Fg(theme.Text), CleanBody(a.Summary), w), h, "")
}

func viewInboxDelivery(d opapi.InboxDelivery, peers inboxPeers, m *Model, w, h int) []string {
	th := m.th
	state := inboxState(d)
	lines := []string{
		th.Fg(theme.Accent).Render(Clean(d.Kind)) + "  " + th.Fg(theme.Priority(d.Priority)).Render(Clean(d.Priority)),
		"",
		inboxField(th, "from", peers.name(d.Sender)+" ") + inboxKind(th, peers.kind(d.Sender)),
		inboxField(th, "thread", Clean(d.ThreadTitle)),
		inboxFieldAs(th, "message", Clean(d.MessageID), theme.Dim),
		inboxField(th, "at", inboxClock(d.CreatedAt, "15:04:05", m)),
		inboxField(th, "receipt", "") + th.Fg(theme.Receipt(state)).Render(inboxGlyph(state)+" "+state),
	}
	if d.Attachments > 0 {
		lines = append(lines, inboxFieldAs(th, "files", fmt.Sprintf("⎘ %d", d.Attachments), theme.Info))
	}
	lines = append(append(lines, ""), inboxPara(th.Fg(theme.Action), "press enter to open the thread", w)...)
	return inboxDetail(th, lines, inboxQuote(th, th.Fg(theme.Text), CleanBody(d.Excerpt), w), h, "")
}

// viewInboxLog is the newest events that fit, oldest first. Deliveries are
// messages, which the list above already shows.
func (s *inboxScreen) viewInboxLog(peers inboxPeers, m *Model, w, h int) string {
	const cursorW, typeW = 9, 17
	var evs []envelope.Event
	for i := len(m.Data.Events) - 1; i >= 0; i-- {
		if m.Data.Events[i].Type != "delivery" {
			evs = append(evs, m.Data.Events[i])
		}
	}
	s.logN = len(evs)
	s.logH = h
	maxTop := 0
	if h > 0 {
		maxTop = max(len(evs)-h, 0)
	}
	if s.logTop < 0 {
		s.logTop = 0
	}
	if s.logTop > maxTop {
		s.logTop = maxTop
	}
	lines := make([]string, 0, h)
	for i := s.logTop; i < len(evs) && len(lines) < h; i++ {
		ev := evs[i]
		th := m.th
		lines = append(lines, " "+th.Fg(theme.Dim).Render(inboxCut(Clean(ev.Cursor), cursorW))+
			" "+th.Fg(theme.Muted).Render(inboxCut(inboxClock(ev.At, "15:04:05", m), 8))+
			" "+th.Fg(theme.EventType(ev.Type)).Render(inboxCut(Clean(ev.Type), typeW))+
			" "+th.Fg(theme.Text).Render(inboxCut(inboxEventText(ev, peers), w-1-cursorW-1-8-1-typeW-1)))
	}
	slices.Reverse(lines)
	return strings.Join(lines, "\n")
}

// inboxEventText is one event's payload on one line. A type it does not know,
// or data that does not decode, shows as cleaned raw text.
func inboxEventText(ev envelope.Event, peers inboxPeers) string {
	raw := Clean(string(ev.Data))
	switch ev.Type {
	case "operator_view":
		var data struct {
			Reason   string `json:"reason"`
			ThreadID string `json:"thread_id"`
		}
		if json.Unmarshal(ev.Data, &data) != nil {
			return raw
		}
		text := Clean(data.Reason)
		if data.ThreadID != "" {
			text += " " + inboxShort(data.ThreadID)
		}
		return text
	case "presence":
		var p envelope.Presence
		if json.Unmarshal(ev.Data, &p) != nil {
			return raw
		}
		parts := []string{peers.name(p.PeerID), Clean(p.State)}
		if p.Location != "" {
			parts = append(parts, Clean(p.Location))
		}
		if pct, ok := p.Quota["weekly_remaining_pct"].(float64); ok {
			parts = append(parts, fmt.Sprintf("quota %.0f%%", pct))
		}
		if p.Note != "" {
			parts = append(parts, Clean(p.Note))
		}
		return strings.Join(parts, " ")
	case "receipt":
		var r envelope.Receipt
		if json.Unmarshal(ev.Data, &r) != nil {
			return raw
		}
		state := Clean(r.State)
		text := fmt.Sprintf("%s → %s %s %s", inboxShort(r.MessageID), peers.name(r.By), inboxGlyph(state), state)
		if r.Note != "" {
			text += " " + Clean(r.Note)
		}
		return text
	case "thread_updated":
		var t envelope.ThreadSummary
		if json.Unmarshal(ev.Data, &t) != nil {
			return raw
		}
		text := inboxShort(t.ThreadID)
		if t.Title != nil {
			text += fmt.Sprintf(" title %q", Clean(*t.Title))
		}
		if t.BudgetRemaining != nil {
			text += fmt.Sprintf(" budget %d", *t.BudgetRemaining)
		}
		return text
	case "approval_expired":
		var e struct {
			ApprovalID string `json:"approval_id"`
			ThreadID   string `json:"thread_id"`
		}
		if json.Unmarshal(ev.Data, &e) != nil {
			return raw
		}
		return inboxShort(e.ApprovalID) + " in " + inboxShort(e.ThreadID)
	case "directory_updated":
		var d struct {
			PeerID string   `json:"peer_id"`
			Change string   `json:"change"`
			Fields []string `json:"fields"`
		}
		if json.Unmarshal(ev.Data, &d) != nil {
			return raw
		}
		return strings.TrimSpace(peers.name(d.PeerID) + " " + Clean(d.Change) + " " + Clean(strings.Join(d.Fields, ", ")))
	case "hub_notice":
		var e envelope.HubNoticeEvent
		if json.Unmarshal(ev.Data, &e) != nil {
			return raw
		}
		n := e.Notice
		text := Clean(e.Change) + " " + Clean(n.Kind) + " " + inboxShort(n.NoticeID)
		if n.Kind == "moving" && n.NewURL != "" {
			text += " → " + Clean(hubHost(n.NewURL))
		}
		if n.Message != "" {
			text += " " + Clean(n.Message)
		}
		return text
	}
	return raw
}
