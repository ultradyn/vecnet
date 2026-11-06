package tui

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/opapi"
	"github.com/XertroV/vecnet/internal/tui/theme"
)

// This screen only reads. Dead-letter retry and discard and
// subscription resume live in the inbox, on purpose.

const (
	dashGutter = 2 // the cursor column of the peer table and the thread list
	// Below this inner width a bottom pane switches to its short row.
	dashWidePane = 36
	dashCapsMin  = 10
	dashErrLines = 3
	dashLowPct   = 10
	dashWarnPct  = 25
	dashBarCells = 10
)

type dashFocus int

const (
	dashOnPeers dashFocus = iota
	dashOnThreads
)

// dashScreen keeps the selection by id, so the cursor follows its row through a
// reload that reorders the table.
type dashScreen struct {
	focus        dashFocus
	peer, thread string
	narrow       *dashNarrow
	peerVis      []string
	peerRowAt    int
	threadVis    []string
	threadRowAt  int
	// extra is the unfiltered pages fetched after Data.Threads. extraBase is
	// the snapshot those pages belong to; a newer snapshot drops them.
	extra     []opapi.ThreadRow
	extraNext *string
	extraBase string
}

// dashNarrow is the thread pane narrowed to one peer. The shell's Data.Threads
// is the whole hub, so the screen holds this query's page itself.
type dashNarrow struct {
	peer       string
	page       opapi.ThreadPage
	err        error
	done       bool
	busy, more bool // one request at a time; more asks for one after it
}

// dashThreadsMsg is one narrowed load. A narrow the operator has left is no
// longer d.narrow, so its answer drops.
type dashThreadsMsg struct {
	narrow *dashNarrow
	page   opapi.ThreadPage
	err    error
}

func newDashScreen() Screen { return &dashScreen{} }

func (d *dashScreen) Title() string { return "dash" }

func (d *dashScreen) Hints() string {
	if d.focus == dashOnThreads {
		return "j/k thread  n page  enter open  esc peers"
	}
	return "j/k peer  enter threads  tab pane"
}

func (d *dashScreen) Update(msg tea.Msg, m *Model) tea.Cmd {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		return d.onKey(msg.String(), m)
	case Loaded:
		if msg.Key == "threads" && msg.Err == nil {
			return d.loadNarrow(m)
		}
	case dashThreadsMsg:
		return d.onNarrow(msg, m)
	case dashPageMsg:
		d.onPage(msg)
	}
	return nil
}

// dashPageMsg is one more unfiltered cursor page.
type dashPageMsg struct {
	base string
	page opapi.ThreadPage
	err  error
}

func (d *dashScreen) onKey(key string, m *Model) tea.Cmd {
	switch key {
	case "j", "down":
		d.move(m, 1)
	case "k", "up":
		d.move(m, -1)
	case "tab", "shift+tab":
		if d.focus == dashOnPeers {
			d.focus = dashOnThreads
		} else {
			d.focus = dashOnPeers
		}
	case "esc":
		d.narrow = nil
		d.focus = dashOnPeers
	case "n":
		if d.focus == dashOnThreads && d.narrow == nil {
			return d.loadMore(m)
		}
	case "enter":
		if d.focus == dashOnThreads {
			rows := d.threadRows(m)
			if len(rows) == 0 {
				return nil
			}
			return m.OpenThread(rows[d.threadIndex(rows)].Thread.ThreadID)
		}
		if len(m.Data.Peers) == 0 {
			return nil
		}
		row := m.Data.Peers[d.peerIndex(m)].Peer
		if row.Kind == "source" { // never a recipient; the hub refuses a src_ participant
			m.Toast(row.Name+" is a source and has no threads", false)
			return nil
		}
		// A stopped client sends nothing, so a narrowed pane would wait forever
		// on a request that never starts.
		if m.stopped() {
			m.Toast(m.Conn+": restart with a valid profile", true)
			return nil
		}
		d.peer, d.thread, d.focus = row.PeerID, "", dashOnThreads
		d.narrow = &dashNarrow{peer: row.PeerID}
		return d.loadNarrow(m)
	}
	return nil
}

func (d *dashScreen) move(m *Model, by int) {
	if d.focus == dashOnThreads {
		if rows := d.threadRows(m); len(rows) > 0 {
			d.thread = rows[min(max(d.threadIndex(rows)+by, 0), len(rows)-1)].Thread.ThreadID
		}
		return
	}
	if peers := m.Data.Peers; len(peers) > 0 {
		d.peer = peers[min(max(d.peerIndex(m)+by, 0), len(peers)-1)].Peer.PeerID
	}
}

func (d *dashScreen) peerIndex(m *Model) int {
	return max(slices.IndexFunc(m.Data.Peers, func(r opapi.PeerRow) bool { return r.Peer.PeerID == d.peer }), 0)
}

func (d *dashScreen) threadIndex(rows []opapi.ThreadRow) int {
	return max(slices.IndexFunc(rows, func(r opapi.ThreadRow) bool { return r.Thread.ThreadID == d.thread }), 0)
}

// threadPage is what the thread pane lists. Unfiltered, that is the hub page
// plus every cursor page `n` has fetched for this snapshot.
func (d *dashScreen) threadPage(m *Model) opapi.ThreadPage {
	if d.narrow != nil {
		return d.narrow.page
	}
	page := m.Data.Threads
	if d.extraBase != threadSnap(page) || len(d.extra) == 0 {
		return page
	}
	out := page
	out.Threads = append(append([]opapi.ThreadRow{}, page.Threads...), d.extra...)
	out.Next = d.extraNext
	return out
}

func threadSnap(page opapi.ThreadPage) string {
	id := ""
	if len(page.Threads) > 0 {
		id = page.Threads[0].Thread.ThreadID
	}
	next := ""
	if page.Next != nil {
		next = *page.Next
	}
	return fmt.Sprintf("%s/%d/%s", id, len(page.Threads), next)
}

// loadMore fetches the next unfiltered cursor page. A peer narrowing is a
// different query and stays on its own load.
func (d *dashScreen) loadMore(m *Model) tea.Cmd {
	if d.narrow != nil || m.stopped() {
		return nil
	}
	page := d.threadPage(m)
	if page.Next == nil || *page.Next == "" {
		return nil
	}
	cursor := *page.Next
	base := threadSnap(m.Data.Threads)
	api, ctx := m.API, m.ctx
	return func() tea.Msg {
		v, err := api.Threads(ctx, opapi.ThreadQuery{Limit: pageLimit, Cursor: cursor})
		return dashPageMsg{base: base, page: v, err: err}
	}
}

func (d *dashScreen) onPage(msg dashPageMsg) {
	if msg.err != nil {
		return
	}
	// A page fetched for a snapshot this screen has since left is dropped.
	if d.extraBase != "" && d.extraBase != msg.base {
		return
	}
	d.extraBase = msg.base
	seen := map[string]bool{}
	for _, row := range d.extra {
		seen[row.Thread.ThreadID] = true
	}
	for _, row := range msg.page.Threads {
		if seen[row.Thread.ThreadID] {
			continue
		}
		d.extra = append(d.extra, row)
	}
	d.extraNext = msg.page.Next
}

func (d *dashScreen) threadRows(m *Model) []opapi.ThreadRow { return d.threadPage(m).Threads }

// loadNarrow fetches the narrowed page. Calls made during a request fold into
// one more request after it, as the shell's own loads do.
func (d *dashScreen) loadNarrow(m *Model) tea.Cmd {
	n := d.narrow
	if n == nil || m.stopped() {
		return nil
	}
	if n.busy {
		n.more = true
		return nil
	}
	n.busy = true
	api, ctx, peer := m.API, m.ctx, n.peer
	return func() tea.Msg {
		page, err := api.Threads(ctx, opapi.ThreadQuery{Participant: peer, Limit: pageLimit})
		return dashThreadsMsg{narrow: n, page: page, err: err}
	}
}

func (d *dashScreen) onNarrow(msg dashThreadsMsg, m *Model) tea.Cmd {
	n := msg.narrow
	if n != d.narrow {
		return nil
	}
	n.busy = false
	if isStatus(msg.err, 401) {
		m.reject(msg.err)
		return nil
	}
	if msg.err == nil {
		n.page, n.done = msg.page, true
	}
	n.err = msg.err
	if n.more {
		n.more = false
		return d.loadNarrow(m)
	}
	return nil
}

// dashNames resolves the ids a frame shows. It is built once per frame, from
// cleaned text only.
type dashNames struct {
	peers, threads map[string]string
}

func newDashNames(m *Model) dashNames {
	names := dashNames{peers: map[string]string{}, threads: map[string]string{}}
	owned := map[string]int{}
	for _, row := range m.Data.Peers {
		names.peers[row.Peer.PeerID] = Clean(row.Peer.Name)
		owned[row.PrincipalID]++
	}
	// An audit actor can be a principal, which may own many peers (PROTOCOL §1.3).
	// Only a principal's sole peer can stand for it; a source has no principal.
	for _, row := range m.Data.Peers {
		if row.PrincipalID != "" && owned[row.PrincipalID] == 1 {
			names.peers[row.PrincipalID] = Clean(row.Peer.Name)
		}
	}
	for _, row := range m.Data.Threads.Threads {
		names.threads[row.Thread.ThreadID] = names.thread(row, m.Data.Inbox.Me.PeerID)
	}
	return names
}

func (n dashNames) peer(id string) string {
	if name, ok := n.peers[id]; ok && name != "" {
		return name
	}
	return dashShort(id)
}

// thread is a thread's title, or who else is in it.
func (n dashNames) thread(row opapi.ThreadRow, me string) string {
	if t := row.Thread.Title; t != nil && Clean(*t) != "" {
		return Clean(*t)
	}
	var others []string
	for _, id := range row.Thread.Participants {
		if id != me {
			others = append(others, n.peer(id))
		}
	}
	if len(others) > 0 {
		return strings.Join(others, ", ")
	}
	return dashShort(row.Thread.ThreadID)
}

func (n dashNames) threadByID(id string) string {
	if name, ok := n.threads[id]; ok {
		return name
	}
	return dashShort(id)
}

// View is the peer table above three panes. It never shows a pane's failure in
// another: each resource keeps its own error and its last good data.
func (d *dashScreen) View(m *Model, w, h int) string {
	names := newDashNames(m)
	top, topH := d.viewPeers(m, w, h)
	bottomH := h - topH
	threadsW, healthW, auditW := (w+2)/3, (w+1)/3, w/3
	threads := d.viewThreads(m, names, threadsW, bottomH)
	d.noteHits(m, w, topH, threadsW, bottomH)
	return top + "\n" + lipgloss.JoinHorizontal(lipgloss.Top,
		threads,
		viewDashHealth(m, names, healthW, bottomH),
		viewDashAudit(m, names, auditW, bottomH),
	)
}

func (d *dashScreen) noteHits(m *Model, w, topH, threadsW, bottomH int) {
	m.addHit(m.hitX, m.hitY, w, topH, hitScroll, "dash.peers")
	for i, id := range d.peerVis {
		m.addHit(m.hitX+1, m.hitY+1+d.peerRowAt+i, max(w-2, 1), 1, hitRow, "peer:"+id)
	}
	m.addHit(m.hitX, m.hitY+topH, threadsW, bottomH, hitScroll, "dash.threads")
	for i, id := range d.threadVis {
		m.addHit(m.hitX+1, m.hitY+topH+1+d.threadRowAt+i, max(threadsW-2, 1), 1, hitRow, "thread:"+id)
	}
}

func (d *dashScreen) Click(id string, m *Model) tea.Cmd {
	if pid, ok := strings.CutPrefix(id, "peer:"); ok {
		d.peer = pid
		d.focus = dashOnPeers
		return nil
	}
	if tid, ok := strings.CutPrefix(id, "thread:"); ok {
		d.thread = tid
		d.focus = dashOnThreads
		return nil
	}
	return nil
}

func (d *dashScreen) Wheel(id string, down bool, m *Model) {
	by := 1
	if !down {
		by = -1
	}
	switch id {
	case "dash.peers":
		d.focus = dashOnPeers
		d.move(m, by)
	case "dash.threads":
		d.focus = dashOnThreads
		d.move(m, by)
	}
}

// dashTitle tags a pane title while one of its resources is loading.
func dashTitle(m *Model, title string, keys ...string) string {
	if slices.ContainsFunc(keys, func(key string) bool { return m.inflight[key] }) {
		return title + " · loading"
	}
	return title
}

// dashFailure is a pane's error in at most limit lines, then, if that is its
// full room, a label over the old data it kept.
func dashFailure(th *theme.Theme, label string, err error, w, limit int, kept bool) []string {
	if err == nil {
		return nil
	}
	bad := th.Fg(theme.Error)
	text := label + " load failed: " + Clean(err.Error())
	if limit == 1 { // a wrap could end before the word failed
		return []string{bad.Render(ansi.Truncate(text, w, "…"))}
	}
	wrapped := strings.Split(ansi.Wrap(text, w, " "), "\n")
	var lines []string
	for _, line := range wrapped[:min(len(wrapped), limit)] {
		lines = append(lines, bad.Render(line))
	}
	if kept && limit == dashErrLines {
		lines = append(lines, th.Fg(theme.Muted).Render("last good data:"))
	}
	return lines
}

// dashPad makes clean plain text exactly w cells.
func dashPad(s string, w int) string {
	if lipgloss.Width(s) > w {
		s = ansi.Truncate(s, w, "…")
	}
	return s + strings.Repeat(" ", max(w-lipgloss.Width(s), 0))
}

// dashRowPaint styles one row: on the selection fill under the cursor (design
// §6.5). dim marks a disabled peer, whose name is muted.
type dashRowPaint struct {
	th          *theme.Theme
	cursor, dim bool
}

func (p dashRowPaint) style(r theme.Role) lipgloss.Style {
	return p.th.On(p.cursor, r)
}

func (p dashRowPaint) cell(r theme.Role, text string, w int) string {
	return p.style(r).Render(dashPad(text, w))
}

func (p dashRowPaint) gutter() string {
	if p.cursor {
		return selMark(p.th, true) + p.style(theme.Text).Render(strings.Repeat(" ", dashGutter-1))
	}
	return strings.Repeat(" ", dashGutter)
}

// dashWindow is the first visible index of n rows in a window of size, holding
// the cursor in view.
func dashWindow(cursor, n, size int) int {
	return max(min(cursor-size/2, n-size), 0)
}

// dashCol is one peer-table column; cell returns exactly w cells.
type dashCol struct {
	title string
	width int
	cell  func(m *Model, r opapi.PeerRow, p dashRowPaint, w int) string
}

// dashPeerCols lists the columns left to right. dashDropOrder is the fixed order
// they leave in as the table narrows: PEER, PRESENCE and REACH always stay,
// because presence and reachability are the two signals this table exists for.
var (
	dashPeerCols = []dashCol{
		{"PEER", 18, dashCellPeer},
		{"KIND", 12, dashCellKind},
		{"PRESENCE", 9, dashCellPresence},
		{"REACH", 12, dashCellReach},
		{"QUOTA", 20, dashCellQuota},
		{"LOCATION", 14, dashCellLocation},
		{"LAST SEEN", 9, dashCellSeen},
		{"CAPS", dashCapsMin, dashCellCaps},
	}
	dashDropOrder = []string{"CAPS", "LOCATION", "KIND", "LAST SEEN", "QUOTA"}
)

func dashColsWidth(cols []dashCol) int {
	width := dashGutter + len(cols) - 1
	for _, c := range cols {
		width += c.width
	}
	return width
}

// dashColsFor drops columns until the table fits inner cells, then hands the
// slack to CAPS if it survived, else to PEER, the first column.
func dashColsFor(inner int) []dashCol {
	cols := slices.Clone(dashPeerCols)
	for _, drop := range dashDropOrder {
		if dashColsWidth(cols) <= inner {
			break
		}
		cols = slices.DeleteFunc(cols, func(c dashCol) bool { return c.title == drop })
	}
	wide := 0
	if last := len(cols) - 1; cols[last].title == "CAPS" {
		wide = last
	}
	cols[wide].width += max(inner-dashColsWidth(cols), 0)
	return cols
}

// dashCellPeer is the peer's name in its kind's hue (design §5.4), or muted
// for a disabled peer, then a disabled or revoked tag in error.
func dashCellPeer(_ *Model, r opapi.PeerRow, p dashRowPaint, w int) string {
	role := theme.Kind(r.Peer.Kind)
	if p.dim {
		role = theme.Muted
	}
	tag := ""
	switch {
	case r.Disabled:
		tag = "disabled"
	case r.Credential == "revoked":
		tag = "revoked"
	}
	if tag == "" {
		return p.cell(role, Clean(r.Peer.Name), w)
	}
	room := max(w-len(tag)-1, 0)
	if room == 0 {
		return p.cell(theme.Error, dashPad(Clean(r.Peer.Name), 0)+" "+tag, w)
	}
	return p.cell(role, dashPad(Clean(r.Peer.Name), room)+" ", room+1) + p.cell(theme.Error, tag, w-room-1)
}

func dashCellKind(_ *Model, r opapi.PeerRow, p dashRowPaint, w int) string {
	return p.cell(theme.Muted, Clean(r.Peer.Kind), w)
}

// dashCellPresence shows what the hub reports and invents nothing: an unknown
// presence reads unknown, never offline (PROTOCOL §8).
func dashCellPresence(_ *Model, r opapi.PeerRow, p dashRowPaint, w int) string {
	presence := Clean(r.Peer.Presence)
	if presence == "" {
		presence = "unknown"
	}
	role := theme.Presence(presence)
	if r.Peer.Kind == "source" { // a source only pushes in; it reports no state
		presence, role = "n/a", theme.Dim
	}
	return p.cell(role, presence, w)
}

func dashCellReach(_ *Model, r opapi.PeerRow, p dashRowPaint, w int) string {
	reach := Clean(r.Peer.Reachability)
	if reach == "" {
		reach = "unknown"
	}
	return p.cell(theme.Reach(reach), reach, w)
}

// dashCellQuota follows format.js quotaLabel: a *_pct value reads 61%, here
// beside a bar; an empty quota reads none; anything else is compact k=v.
func dashCellQuota(_ *Model, r opapi.PeerRow, p dashRowPaint, w int) string {
	key, pct, ok := dashQuotaPct(r.Peer.Quota)
	if !ok {
		if len(r.Peer.Quota) == 0 {
			return p.cell(theme.Muted, "none", w)
		}
		return p.cell(theme.Muted, dashCompact(r.Peer.Quota), w)
	}
	role := theme.Text
	if strings.Contains(key, "remaining") {
		role = theme.Quota(pct, dashWarnPct, dashLowPct)
	}
	// The brackets are dim; the bar and the figure take the level's role.
	cells := int(min(max(pct, 0), 100)*dashBarCells/100 + 0.5)
	text := fmt.Sprintf("[%s%s] %3.0f%%", strings.Repeat("|", cells), strings.Repeat(" ", dashBarCells-cells), pct)
	if lipgloss.Width(text) > w {
		return p.cell(role, text, w)
	}
	return p.cell(theme.Dim, "[", 1) + p.cell(role, strings.Repeat("|", cells), cells) +
		p.cell(theme.Dim, strings.Repeat(" ", dashBarCells-cells)+"]", dashBarCells-cells+1) +
		p.cell(role, fmt.Sprintf(" %3.0f%%", pct), w-dashBarCells-2)
}

// dashQuotaPct finds the quota's percentage: the standard key if it is a number
// (PROTOCOL §2.6), else the first *_pct key that is.
func dashQuotaPct(quota map[string]any) (string, float64, bool) {
	const standard = "weekly_remaining_pct"
	keys := slices.Sorted(maps.Keys(quota))
	for _, key := range append([]string{standard}, keys...) {
		if v, ok := quota[key].(float64); ok && strings.HasSuffix(key, "_pct") {
			return key, v, true
		}
	}
	return "", 0, false
}

// dashCompact is a free-form object as sorted k=v pairs.
func dashCompact(values map[string]any) string {
	pairs := make([]string, 0, len(values))
	for _, key := range slices.Sorted(maps.Keys(values)) {
		value, ok := values[key].(string)
		if !ok {
			raw, err := json.Marshal(values[key])
			if err != nil {
				continue
			}
			value = string(raw)
		}
		pairs = append(pairs, Clean(key)+"="+Clean(value))
	}
	return strings.Join(pairs, " ")
}

func dashCellLocation(_ *Model, r opapi.PeerRow, p dashRowPaint, w int) string {
	location := "-"
	if r.Peer.Location != nil && *r.Peer.Location != "" {
		location = Clean(*r.Peer.Location)
	}
	return p.cell(theme.Muted, location, w)
}

func dashCellSeen(m *Model, r opapi.PeerRow, p dashRowPaint, w int) string {
	seen := "-"
	if r.Peer.LastSeen != nil {
		if age := dashAge(m, *r.Peer.LastSeen); age != "" {
			seen = age + " ago"
		}
	}
	return p.cell(theme.Muted, seen, w)
}

func dashCellCaps(_ *Model, r opapi.PeerRow, p dashRowPaint, w int) string {
	caps := make([]string, 0, len(r.Peer.Capabilities))
	for _, c := range r.Peer.Capabilities {
		caps = append(caps, Clean(c))
	}
	return p.cell(theme.Muted, strings.Join(caps, ","), w)
}

// dashAge is how long ago ts was on the hub's clock, empty if it does not parse.
func dashAge(m *Model, ts string) string {
	t, err := envelope.ParseTime(ts)
	if err != nil {
		return ""
	}
	return dashSpan(m.HubNow().Sub(t))
}

// dashSpan is a duration in its largest whole unit. A negative one is 0s.
func dashSpan(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(max(d, 0).Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}

// dashShort is prefix_…last4 for a long id, such as dly_…3A5C.
func dashShort(id string) string {
	const keep = 4
	clean := []rune(Clean(id))
	cut := slices.Index(clean, '_')
	if cut < 0 || len(clean) <= cut+2*keep+1 {
		return string(clean)
	}
	return string(clean[:cut+1]) + "…" + string(clean[len(clean)-keep:])
}

// viewPeers returns the peer pane and its height: its rows, up to three fifths
// of the body, so the panes below keep their room on a large hub.
func (d *dashScreen) viewPeers(m *Model, w, h int) (string, int) {
	th := m.th
	inner := w - 2
	peers := m.Data.Peers
	failure := dashFailure(th, "peers", m.Data.Errs["peers"], inner, dashErrLines, len(peers) > 0)
	height := min(len(failure)+1+max(len(peers), 1)+2, h*3/5)
	focused := d.focus == dashOnPeers
	title := PaneTitle(th, focused).Render(dashTitle(m, fmt.Sprintf("peers · %d", len(peers)), "peers")+" · ") +
		th.Fg(theme.Muted).Render("presence is self-reported, reach is hub-observed")
	lines := slices.Clone(failure)
	d.peerVis = d.peerVis[:0]
	switch {
	case len(peers) > 0:
		cols := dashColsFor(inner)
		lines = append(lines, dashPeerHeader(th, cols))
		size := max(height-2-len(lines), 1)
		cursor := d.peerIndex(m)
		start := dashWindow(cursor, len(peers), size)
		d.peerRowAt = len(lines)
		for i := start; i < min(start+size, len(peers)); i++ {
			d.peerVis = append(d.peerVis, peers[i].Peer.PeerID)
			lines = append(lines, dashPeerLine(m, cols, peers[i], i == cursor))
		}
	case failure == nil:
		lines = append(lines, th.Fg(theme.Muted).Render("no peers"))
	}
	return PaneRich(th, title, w, height, focused, strings.Join(lines, "\n")), height
}

// dashPeerHeader is the column heads in muted with no fill, as subs draws
// them (design §7.4: fills are only for selection and the header bar).
func dashPeerHeader(th *theme.Theme, cols []dashCol) string {
	cells := make([]string, 0, len(cols))
	for _, c := range cols {
		cells = append(cells, dashPad(c.title, c.width))
	}
	return strings.Repeat(" ", dashGutter) + th.Fg(theme.Muted).Render(strings.Join(cells, " "))
}

func dashPeerLine(m *Model, cols []dashCol, row opapi.PeerRow, cursor bool) string {
	paint := dashRowPaint{th: m.th, cursor: cursor, dim: row.Disabled}
	var line strings.Builder
	line.WriteString(paint.gutter())
	for i, c := range cols {
		if i > 0 {
			line.WriteString(paint.cell(theme.Text, " ", 1))
		}
		line.WriteString(c.cell(m, row, paint, c.width))
	}
	return line.String()
}

func (d *dashScreen) viewThreads(m *Model, names dashNames, w, h int) string {
	inner := w - 2
	label, loading, err, done := "threads", m.inflight["threads"], m.Data.Errs["threads"], true
	if n := d.narrow; n != nil {
		label += " · " + names.peer(n.peer)
		loading, err, done = n.busy, n.err, n.done
	}
	page := d.threadPage(m)
	rows := page.Threads
	failure := dashFailure(m.th, "threads", err, inner, dashErrLines, len(rows) > 0)
	title := label + " · " + fmt.Sprint(len(rows))
	if page.Next != nil {
		title += "+"
	}
	if loading {
		title = label + " · loading"
	}
	lines := slices.Clone(failure)
	d.threadVis = d.threadVis[:0]
	switch {
	case len(rows) > 0:
		size := max(h-2-len(lines), 1)
		cursor := d.threadIndex(rows)
		start := dashWindow(cursor, len(rows), size)
		d.threadRowAt = len(lines)
		for i := start; i < min(start+size, len(rows)); i++ {
			d.threadVis = append(d.threadVis, rows[i].Thread.ThreadID)
			lines = append(lines, d.threadLine(m, names, rows[i], inner, d.focus == dashOnThreads && i == cursor))
		}
	case !done && failure == nil:
		lines = append(lines, m.th.Fg(theme.Muted).Render("loading threads for "+names.peer(d.narrow.peer)))
	case failure == nil:
		lines = append(lines, m.th.Fg(theme.Muted).Render("no threads"))
	}
	return Pane(m.th, title, w, h, d.focus == dashOnThreads, strings.Join(lines, "\n"))
}

// threadLine is title, age and budget; the age leaves first as the pane narrows.
func (d *dashScreen) threadLine(m *Model, names dashNames, row opapi.ThreadRow, inner int, cursor bool) string {
	const ageCells, stateCells = 4, 7
	paint := dashRowPaint{th: m.th, cursor: cursor}
	// The budget (design §5.5): a cut is an error, none left a warning,
	// otherwise quiet.
	state, role := "-", theme.Muted
	if b := row.Thread.BudgetRemaining; b != nil {
		state, role = fmt.Sprint(*b), theme.Budget(*b, false)
	}
	if row.Cut != nil {
		state, role = "cut "+state, theme.Error
	}
	gap := paint.cell(theme.Text, " ", 1)
	title := inner - dashGutter - stateCells - 1
	age := ""
	if inner >= dashWidePane {
		title -= ageCells + 1
		age = paint.cell(theme.Muted, dashAge(m, row.LastMessageAt), ageCells) + gap
	}
	return paint.gutter() + paint.cell(theme.Text, names.thread(row, m.Data.Inbox.Me.PeerID), title) + gap + age + paint.cell(role, state, stateCells)
}

// viewDashHealth holds three resources, so a failure here competes with the
// others' data. Failures share half of this pane, and dashStack keeps them and
// the loop-cut total, so each still shows on the smallest screen.
func viewDashHealth(m *Model, names dashNames, w, h int) string {
	inner, rows := w-2, h-2
	cuts, counters := m.Data.Cuts, m.Data.Counters
	cutsErr, countersErr := m.Data.Errs["cuts"], m.Data.Errs["counters"]
	limit := dashErrLines
	if n := dashFailed(m.Data.Errs["health"], cutsErr, countersErr); n > 0 {
		limit = min(limit, max(rows/2/n, 1))
	}
	head := dashHealthLines(m, names, inner, limit)
	cutsFailure := dashFailure(m.th, "loop cuts", cutsErr, inner, limit, len(cuts.ByReason) > 0)
	countersFailure := dashFailure(m.th, "counters", countersErr, inner, limit, len(counters) > 0)
	cutLines, counterLines := dashCutLines(m, inner), dashCounterLines(m, inner)
	tail := slices.Concat(cutsFailure, cutLines, []string{""}, countersFailure, counterLines)
	if len(head)+1+len(tail) > rows {
		tail = slices.Concat(cutsFailure, countersFailure, cutLines, []string{""}, counterLines)
	}
	title := dashTitle(m, "delivery health", "health", "cuts", "counters")
	keep := len(cutsFailure) + len(countersFailure) + min(len(cutLines), 1)
	return Pane(m.th, title, w, h, false, strings.Join(dashStack(m.th, head, tail, keep, rows), "\n"))
}

func dashFailed(errs ...error) int {
	n := 0
	for _, err := range errs {
		if err != nil {
			n++
		}
	}
	return n
}

// dashStack fits head above tail in h lines, keeping tail's first keep lines. A
// long head gives way first, so a hub with many blocked threads still shows its
// loop cuts.
func dashStack(th *theme.Theme, head, tail []string, keep, h int) []string {
	if len(head)+1+len(tail) <= h {
		return append(append(head, ""), tail...)
	}
	tail = tail[:min(len(tail), max(h/2, keep, 1))]
	room := max(h-len(tail)-1, 1)
	if len(head) > room {
		hidden := len(head) - room + 1
		head = append(head[:room-1], th.Fg(theme.Muted).Render(fmt.Sprintf("+%d more lines", hidden)))
	}
	return append(append(head, ""), tail...)
}

// dashHealthLines is delivery health. A blocked thread names its peer: per-peer
// serialisation can hold one thread for hours while others flow (PROTOCOL §16.4).
func dashHealthLines(m *Model, names dashNames, inner, limit int) []string {
	th := m.th
	health := m.Data.Health
	kept := len(health.Blocked)+len(health.Failed) > 0
	lines := dashFailure(th, "delivery health", m.Data.Errs["health"], inner, limit, kept)
	if !kept && lines == nil {
		return []string{th.Fg(theme.Muted).Render("no blocked or failed deliveries")}
	}
	lead := "thread blocked on delivery "
	if inner < dashWidePane {
		lead = "blocked on "
	}
	for _, b := range health.Blocked {
		lines = append(lines,
			th.Fg(theme.Text).Render(names.peer(b.Recipient)+" · "+names.threadByID(b.ThreadID)),
			"  "+th.Fg(theme.Warn).Render(lead)+th.Fg(theme.Dim).Render(dashShort(b.DeliveryID)),
			th.Fg(theme.Muted).Render("  "+dashBlockedNote(m, b)),
		)
	}
	for _, f := range health.Failed {
		status, reason := "---", names.threadByID(f.ThreadID)
		if f.LastStatus != nil {
			status = fmt.Sprint(*f.LastStatus)
		}
		if f.LastError != nil && *f.LastError != "" {
			reason = Clean(*f.LastError)
		}
		// A failed delivery is an error whatever its status says.
		lines = append(lines, th.Fg(theme.Error).Render(status)+" "+th.Fg(theme.Text).Render(names.peer(f.Recipient))+" "+th.Fg(theme.Muted).Render(reason))
	}
	return lines
}

func dashBlockedNote(m *Model, b opapi.BlockedThread) string {
	note := []string{fmt.Sprintf("attempt %d", b.Attempt), fmt.Sprintf("%d waiting", b.Waiting)}
	if b.NextAttemptAt != nil {
		if t, err := envelope.ParseTime(*b.NextAttemptAt); err == nil {
			note = append(note, "next "+dashSpan(t.Sub(m.HubNow())))
		}
	}
	if b.LastStatus != nil {
		note = append(note, fmt.Sprint(*b.LastStatus))
	}
	return strings.Join(note, " · ")
}

// dashCutLines is the loop-cut total, then its reasons by count.
func dashCutLines(m *Model, inner int) []string {
	cuts := m.Data.Cuts
	// A load that failed before any landed leaves no total to report: zero
	// would read as a fact the client never received.
	if m.Data.Errs["cuts"] != nil && cuts.Since == "" && cuts.Hours == 0 && cuts.Total == 0 && len(cuts.ByReason) == 0 && len(cuts.Recent) == 0 {
		return nil
	}
	hours := cuts.Hours
	if hours == 0 {
		hours = 24
	}
	th := m.th
	total := sectionLabel(th, fmt.Sprintf("loop cuts / %dh: ", hours)) + th.Fg(theme.Count(cuts.Total, theme.Warn)).Render(fmt.Sprint(cuts.Total))
	return append([]string{total}, dashCounts(th, cuts.ByReason, inner)...)
}

func dashCounterLines(m *Model, inner int) []string {
	if len(m.Data.Counters) == 0 {
		return nil
	}
	// Running totals in the hub's database, not current state: the top bar's
	// dlq is what waits now, dead-lettered is every one ever.
	return append([]string{sectionLabel(m.th, "counters (all-time totals)")}, dashCounts(m.th, m.Data.Counters, inner)...)
}

// dashCounts is one line per key, highest count first, then by name.
func dashCounts(th *theme.Theme, counts map[string]int, inner int) []string {
	const indent, countCells = "  ", 5
	nameCells := min(25, inner-len(indent)-countCells)
	keys := slices.SortedFunc(maps.Keys(counts), func(a, b string) int {
		if counts[a] != counts[b] {
			return counts[b] - counts[a]
		}
		return strings.Compare(a, b)
	})
	lines := make([]string, 0, len(keys))
	for _, key := range keys {
		label := Clean(key)
		if key == "dead_letters" {
			label = "dead-lettered"
		}
		lines = append(lines, th.Fg(theme.Muted).Render(indent+dashPad(label, nameCells))+" "+th.Fg(theme.Text).Render(fmt.Sprint(counts[key])))
	}
	return lines
}

func viewDashAudit(m *Model, names dashNames, w, h int) string {
	inner := w - 2
	entries := m.Data.Audit.Entries
	lines := dashFailure(m.th, "audit tail", m.Data.Errs["audit"], inner, dashErrLines, len(entries) > 0)
	if len(entries) == 0 && lines == nil {
		lines = []string{m.th.Fg(theme.Muted).Render("no audit entries")}
	}
	for _, entry := range entries {
		lines = append(lines, dashAuditLine(m, names, entry, inner))
	}
	return Pane(m.th, dashTitle(m, "audit tail -f", "audit"), w, h, false, strings.Join(lines, "\n"))
}

// dashAuditLine is time, action, then who and on what; that last column leaves
// first as the pane narrows.
func dashAuditLine(m *Model, names dashNames, e opapi.AuditEntry, inner int) string {
	const clockCells, actionCells = 8, 18
	clock := Clean(e.At)
	if t, err := envelope.ParseTime(e.At); err == nil {
		clock = t.In(m.loc).Format("15:04:05")
	}
	th := m.th
	line := th.Fg(theme.Muted).Render(dashPad(clock, clockCells)) + " "
	if inner < dashWidePane {
		return line + th.Fg(theme.Text).Render(dashPad(Clean(e.Action), inner-clockCells-1))
	}
	actor := names.peer(e.Actor)
	rest := make([]string, 0, len(e.Targets)+1)
	for _, target := range e.Targets {
		rest = append(rest, names.peer(target))
	}
	if detail := dashCompact(e.Detail); detail != "" {
		rest = append(rest, detail)
	}
	room := inner - clockCells - 1 - actionCells - 1
	actor = dashPad(actor, min(lipgloss.Width(actor), room))
	line += th.Fg(theme.Text).Render(dashPad(Clean(e.Action), actionCells)) + " " + th.Fg(theme.Text).Render(actor)
	if left := room - lipgloss.Width(actor) - 1; left > 0 {
		line += " " + th.Fg(theme.Muted).Render(dashPad(strings.Join(rest, " "), left))
	}
	return line
}
