package tui

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/opapi"
)

const (
	subsResource = "subs" // the resource key, as data.go names it
	// subsDetailH is the detail pane's height: a three-line note and its facts,
	// which is all of it at 80 columns.
	subsDetailH = 9
	subsGap     = "  "
	subsMarkW   = 2
	subsNone    = "-"
	// Times are en-AU 24-hour local.
	subsTimeShort = "02/01 15:04"
	subsTimeFull  = "02/01/2006 15:04:05"
)

// The table's columns, in display order. Peer and host share what is left over,
// host taking more because a callback host runs longer than a peer name.
const (
	subsColPeer = iota
	subsColHost
	subsColHealth
	subsColExpires
	subsColRefresh
	subsColStatus
	subsColAttempts
	subsColCursor
)

var subsCols = []struct {
	head  string
	width int
}{
	subsColPeer:     {"peer", 10},
	subsColHost:     {"host", 12},
	subsColHealth:   {"health", 15},
	subsColExpires:  {"expires in", 19}, // "expired 125d ago" and "expired 2h 5m ago" fit
	subsColRefresh:  {"last refresh", 12},
	subsColStatus:   {"last status", 11},
	subsColAttempts: {"attempts", 8},
	subsColCursor:   {"cursor", 14},
}

// subsDropOrder is what leaves the table first as the pane narrows. The detail
// pane repeats each of these, so nothing is lost at 80 columns.
var subsDropOrder = []int{subsColCursor, subsColAttempts, subsColHost}

// subsScreen is the subscriptions tab. It is read-only: resume lives on the
// inbox's SUB rows, as it does on the web, so no key here can reach a write.
type subsScreen struct {
	// sel is the selected row's sub_id, which keeps the selection on its row
	// when a reload reorders the table. Empty or gone means the first row.
	sel string
	// bad counts rows that are not ok, as the last load left them.
	bad    int
	vis    []string
	rowAt  int
	tableH int
}

func newSubsScreen() Screen { return &subsScreen{} }

// Title also carries the top bar's `subs n`: the shell draws every tab from its
// title, and layout.go is not this task's to edit.
func (s *subsScreen) Title() string {
	if s.bad > 0 {
		return fmt.Sprintf("subs %d", s.bad)
	}
	return "subs"
}

// Hints names no action: resume works only for some paused rows, and the
// selected row's note says what to do.
func (s *subsScreen) Hints() string { return "j/k move  read-only" }

func (s *subsScreen) Update(msg tea.Msg, m *Model) tea.Cmd {
	switch msg := msg.(type) {
	case Loaded:
		if msg.Key == subsResource {
			s.bad = 0
			for _, row := range m.Data.Subs {
				if row.Health != opapi.HealthOK {
					s.bad++
				}
			}
		}
	case tea.KeyPressMsg:
		rows := m.Data.Subs
		if len(rows) == 0 {
			return nil
		}
		at := s.selected(rows)
		switch msg.String() {
		case "j", "down":
			at = min(at+1, len(rows)-1)
		case "k", "up":
			at = max(at-1, 0)
		case "g", "home":
			at = 0
		case "G", "end":
			at = len(rows) - 1
		default:
			return nil
		}
		s.sel = rows[at].SubID
	}
	return nil
}

func (s *subsScreen) selected(rows []opapi.Subscription) int {
	for i, row := range rows {
		if row.SubID == s.sel {
			return i
		}
	}
	return 0
}

func (s *subsScreen) View(m *Model, w, h int) string {
	tableH := max(h-subsDetailH, 2)
	s.tableH = tableH
	title := fmt.Sprintf("subs · %d rows", len(m.Data.Subs))
	if m.inflight[subsResource] {
		title += " · reloading"
	}
	table := Pane(m.th, title, w, tableH, true, s.viewTable(m, w-2, tableH-2))
	s.noteHits(m, w)
	return table + "\n" + s.viewDetail(m, w, h-tableH)
}

func (s *subsScreen) noteHits(m *Model, w int) {
	m.addHit(m.hitX, m.hitY, w, s.tableH, hitScroll, "subs.table")
	for i, id := range s.vis {
		m.addHit(m.hitX+1, m.hitY+1+s.rowAt+i, max(w-2, 1), 1, hitRow, "sub:"+id)
	}
}

func (s *subsScreen) Click(id string, m *Model) tea.Cmd {
	if sid, ok := strings.CutPrefix(id, "sub:"); ok {
		s.sel = sid
	}
	return nil
}

func (s *subsScreen) Wheel(id string, down bool, m *Model) {
	if id != "subs.table" {
		return
	}
	rows := m.Data.Subs
	if len(rows) == 0 {
		return
	}
	at := s.selected(rows)
	if down {
		at = min(at+1, len(rows)-1)
	} else {
		at = max(at-1, 0)
	}
	s.sel = rows[at].SubID
}

// viewTable is the header and as many rows as fit, keeping the selection in view.
func (s *subsScreen) viewTable(m *Model, w, h int) string {
	th := m.th
	var lines []string
	rows := m.Data.Subs
	s.vis = s.vis[:0]
	if err := m.Data.Errs[subsResource]; err != nil {
		if rows == nil { // a load that succeeded, even empty, leaves a non-nil slice
			// Nothing ever loaded: there is no last data, and no count to give.
			return th.Fg(theme.Error).Render("load failed: " + Clean(err.Error()))
		}
		lines = append(lines, th.Fg(theme.Error).Render("load failed: "+Clean(err.Error())+"; showing the last data"))
	}
	if len(rows) == 0 {
		return strings.Join(append(lines, th.Fg(theme.Muted).Render("no subscriptions")), "\n")
	}
	cols, widths := subsLayout(w)
	head := make([]string, len(cols))
	for i, col := range cols {
		head[i] = th.Fg(theme.Muted).Render(fit(subsCols[col].head, widths[i]))
	}
	lines = append(lines, strings.Repeat(" ", subsMarkW)+strings.Join(head, subsGap))

	at := s.selected(rows)
	room := max(h-len(lines), 1)
	first := max(at-room+1, 0)
	s.rowAt = len(lines)
	for i := first; i < min(first+room, len(rows)); i++ {
		s.vis = append(s.vis, rows[i].SubID)
		lines = append(lines, subsTableRow(m, rows[i], i == at, cols, widths, w))
	}
	return strings.Join(lines, "\n")
}

// subsLayout picks the columns that fit in w and their widths.
func subsLayout(w int) (cols, widths []int) {
	shown := make([]bool, len(subsCols))
	for col := range shown {
		shown[col] = true
	}
	total := func() int {
		sum := subsMarkW - len(subsGap)
		for col, on := range shown {
			if on {
				sum += subsCols[col].width + len(subsGap)
			}
		}
		return sum
	}
	for _, col := range subsDropOrder {
		if total() <= w {
			break
		}
		shown[col] = false
	}
	spare := max(w-total(), 0)
	for col, on := range shown {
		if !on {
			continue
		}
		width := subsCols[col].width
		switch {
		case col == subsColPeer && shown[subsColHost]:
			width += spare / 3
		case col == subsColPeer:
			width += spare
		case col == subsColHost:
			width += spare - spare/3
		}
		cols, widths = append(cols, col), append(widths, width)
	}
	return cols, widths
}

// subsTableRow is one row. The selected row has `>` and the selection fill
// across the pane's inner width (design §6.5); its state cells keep their hue.
func subsTableRow(m *Model, row opapi.Subscription, selected bool, cols, widths []int, w int) string {
	th := m.th
	var b strings.Builder
	b.WriteString(selMark(th, selected) + th.On(selected, theme.Text).Render(strings.Repeat(" ", subsMarkW-1)))
	for i, col := range cols {
		if i > 0 {
			b.WriteString(th.On(selected, theme.Text).Render(subsGap))
		}
		text, role := subsCell(m, row, col)
		b.WriteString(th.On(selected, role).Render(fit(text, widths[i])))
	}
	return selPad(th, b.String(), w, selected)
}

// subsCell is one cleaned table cell and its role (design §5.5): the health
// map, expiry in error once past and in warn while the row is late, the last
// status in error outside 2xx.
func subsCell(m *Model, row opapi.Subscription, col int) (string, theme.Role) {
	switch col {
	case subsColPeer:
		return subsPeer(m, row.PeerID), theme.Text
	case subsColHost:
		return Clean(row.URL), theme.Muted
	case subsColHealth:
		return Clean(row.Health), theme.Health(row.Health)
	case subsColExpires:
		text := subsExpires(row.ExpiresAt, m.HubNow())
		switch {
		case row.ExpiresAt == nil:
			return text, theme.Dim
		case strings.HasPrefix(text, "expired"):
			return text, theme.Error
		case row.Health == opapi.HealthLate:
			return text, theme.Warn
		}
		return text, theme.Text
	case subsColRefresh:
		return subsWhen(m, row.LastRefreshAt, subsTimeShort), theme.Muted
	case subsColStatus:
		return subsStatus(row.LastStatus), subsStatusRole(row.LastStatus)
	case subsColAttempts:
		return strconv.Itoa(row.Attempts), theme.Count(row.Attempts, theme.Warn)
	}
	return Clean(row.Cursor), theme.Muted
}

// subsPeer is the peer's name, or its id for a peer this client has no row for.
func subsPeer(m *Model, id string) string {
	for _, row := range m.Data.Peers {
		if row.Peer.PeerID == id && row.Peer.Name != "" {
			return Clean(row.Peer.Name)
		}
	}
	return Clean(id)
}

// subsExpires is the time left on the hub's clock, or how long ago it ran out.
func subsExpires(at *string, hubNow time.Time) string {
	if at == nil {
		return subsNone
	}
	t, err := envelope.ParseTime(*at)
	if err != nil {
		return "unknown"
	}
	if left := t.Sub(hubNow); left > 0 {
		return subsSpan(left)
	}
	return "expired " + subsSpan(hubNow.Sub(t)) + " ago"
}

// subsSpan is a duration in its largest two units, rounded down.
func subsSpan(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh %dm", int(d.Hours()), int(d.Minutes())%60)
	}
	return fmt.Sprintf("%dd", int(d.Hours())/24)
}

// subsWhen is a hub timestamp in the operator's time zone.
func subsWhen(m *Model, at *string, layout string) string {
	if at == nil {
		return "never"
	}
	t, err := envelope.ParseTime(*at)
	if err != nil {
		return "unknown"
	}
	return t.In(m.loc).Format(layout)
}

// subsStatus is the last delivery's status; null means none was attempted.
func subsStatus(status *int) string {
	if status == nil {
		return subsNone
	}
	return strconv.Itoa(*status)
}

// subsStatusRole is the last status's role: none attempted is dim, anything
// outside 2xx is an error (a callback that redirects did not take the event).
func subsStatusRole(status *int) theme.Role {
	switch {
	case status == nil:
		return theme.Dim
	case *status < 200 || *status >= 300:
		return theme.Error
	}
	return theme.Text
}

// viewDetail is the selected row's health_note, then every fact the table may
// have dropped. The note comes first, so a short pane still shows it.
func (s *subsScreen) viewDetail(m *Model, w, h int) string {
	th := m.th
	rows := m.Data.Subs
	if len(rows) == 0 {
		return Pane(th, "detail", w, h, false, "")
	}
	row := rows[s.selected(rows)]
	note := Clean(row.HealthNote)
	if note == "" {
		note = "no note"
	}
	lines := strings.Split(ansi.Wordwrap(note, w-2, ""), "\n")
	for i, line := range lines {
		lines[i] = th.Fg(theme.Text).Render(line)
	}
	if row.PausedReason != nil {
		lines = append(lines, th.Fg(theme.Error).Render("paused: "+Clean(*row.PausedReason)))
	}
	muted := th.Fg(theme.Muted)
	fact := func(parts ...string) {
		lines = append(lines, muted.Render(strings.Join(parts, "  ")))
	}
	lines = append(lines, muted.Render("status ")+th.Fg(subsStatusRole(row.LastStatus)).Render(subsStatus(row.LastStatus))+
		muted.Render("  attempts ")+th.Fg(theme.Count(row.Attempts, theme.Warn)).Render(strconv.Itoa(row.Attempts))+
		muted.Render("  cursor "+Clean(row.Cursor)))
	fact("sub "+Clean(row.SubID), "host "+Clean(row.URL), "name "+Clean(row.Name))
	fact("expires "+subsWhen(m, row.ExpiresAt, subsTimeFull), "refresh before "+subsWhen(m, row.RefreshBefore, subsTimeFull))
	fact("verified "+subsWhen(m, row.VerifiedAt, subsTimeFull), "last delivery "+subsWhen(m, row.LastDeliveryAt, subsTimeFull))
	head := PaneTitle(th, false)
	title := head.Render(subsPeer(m, row.PeerID)+" · ") + th.Fg(theme.Health(row.Health)).Render(Clean(row.Health))
	return PaneRich(th, title, w, h, false, strings.Join(lines, "\n"))
}
