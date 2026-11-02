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
	bad int
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
	title := fmt.Sprintf("subs · %d rows", len(m.Data.Subs))
	if m.inflight[subsResource] {
		title += " · reloading"
	}
	table := Pane(title, w, tableH, true, s.viewTable(m, w-2, tableH-2))
	return table + "\n" + s.viewDetail(m, w, h-tableH)
}

// viewTable is the header and as many rows as fit, keeping the selection in view.
func (s *subsScreen) viewTable(m *Model, w, h int) string {
	var lines []string
	rows := m.Data.Subs
	if err := m.Data.Errs[subsResource]; err != nil {
		if rows == nil { // a load that succeeded, even empty, leaves a non-nil slice
			// Nothing ever loaded: there is no last data, and no count to give.
			return styleBad.Render("load failed: " + Clean(err.Error()))
		}
		lines = append(lines, styleBad.Render("load failed: "+Clean(err.Error())+"; showing the last data"))
	}
	if len(rows) == 0 {
		return strings.Join(append(lines, styleMuted.Render("no subscriptions")), "\n")
	}
	cols, widths := subsLayout(w)
	head := make([]string, len(cols))
	for i, col := range cols {
		head[i] = styleMuted.Render(fit(subsCols[col].head, widths[i]))
	}
	lines = append(lines, strings.Repeat(" ", subsMarkW)+strings.Join(head, subsGap))

	at := s.selected(rows)
	room := max(h-len(lines), 1)
	first := max(at-room+1, 0)
	for i := first; i < min(first+room, len(rows)); i++ {
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

func subsTableRow(m *Model, row opapi.Subscription, selected bool, cols, widths []int, w int) string {
	paint := func(style lipgloss.Style, text string) string {
		if selected {
			style = style.Background(colorBorder)
		}
		return style.Render(text)
	}
	mark := "  "
	if selected {
		mark = "> "
	}
	var b strings.Builder
	b.WriteString(paint(styleAccent, mark))
	for i, col := range cols {
		if i > 0 {
			b.WriteString(paint(styleText, subsGap))
		}
		text, style := subsCell(m, row, col)
		b.WriteString(paint(style, fit(text, widths[i])))
	}
	if pad := w - lipgloss.Width(b.String()); pad > 0 {
		b.WriteString(paint(styleText, strings.Repeat(" ", pad)))
	}
	return b.String()
}

// subsCell is one cleaned table cell and its style.
func subsCell(m *Model, row opapi.Subscription, col int) (string, lipgloss.Style) {
	switch col {
	case subsColPeer:
		return subsPeer(m, row.PeerID), styleText
	case subsColHost:
		return Clean(row.URL), styleMuted
	case subsColHealth:
		return Clean(row.Health), subsHealthStyle(row.Health)
	case subsColExpires:
		text := subsExpires(row.ExpiresAt, m.HubNow())
		if strings.HasPrefix(text, "expired") {
			return text, styleBad
		}
		return text, styleText
	case subsColRefresh:
		return subsWhen(m, row.LastRefreshAt, subsTimeShort), styleMuted
	case subsColStatus:
		return subsStatus(row.LastStatus), subsStatusStyle(row.LastStatus)
	case subsColAttempts:
		return strconv.Itoa(row.Attempts), countStyle(row.Attempts, styleWarn)
	}
	return Clean(row.Cursor), styleMuted
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

// subsHealthStyle colours the nine health values. A value this client does not
// know stays neutral: it must not read as healthy or as broken.
func subsHealthStyle(health string) lipgloss.Style {
	switch health {
	case opapi.HealthOK:
		return styleGood
	case opapi.HealthLate, opapi.HealthUnverified, opapi.HealthPausedOperator:
		return styleWarn
	case opapi.HealthFailing, opapi.HealthExpired, opapi.HealthPausedToken, opapi.HealthPausedFailed, opapi.HealthPausedAuth:
		return styleBad
	}
	return styleMuted
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

func subsStatusStyle(status *int) lipgloss.Style {
	switch {
	case status == nil:
		return styleMuted
	case *status < 200 || *status >= 300:
		return styleBad
	}
	return styleText
}

// viewDetail is the selected row's health_note, then every fact the table may
// have dropped. The note comes first, so a short pane still shows it.
func (s *subsScreen) viewDetail(m *Model, w, h int) string {
	rows := m.Data.Subs
	if len(rows) == 0 {
		return Pane("detail", w, h, false, "")
	}
	row := rows[s.selected(rows)]
	note := Clean(row.HealthNote)
	if note == "" {
		note = "no note"
	}
	lines := strings.Split(ansi.Wordwrap(note, w-2, ""), "\n")
	for i, line := range lines {
		lines[i] = styleText.Render(line)
	}
	if row.PausedReason != nil {
		lines = append(lines, styleBad.Render("paused: "+Clean(*row.PausedReason)))
	}
	fact := func(parts ...string) {
		lines = append(lines, styleMuted.Render(strings.Join(parts, "  ")))
	}
	fact("status "+subsStatus(row.LastStatus), fmt.Sprintf("attempts %d", row.Attempts), "cursor "+Clean(row.Cursor))
	fact("sub "+Clean(row.SubID), "host "+Clean(row.URL), "name "+Clean(row.Name))
	fact("expires "+subsWhen(m, row.ExpiresAt, subsTimeFull), "refresh before "+subsWhen(m, row.RefreshBefore, subsTimeFull))
	fact("verified "+subsWhen(m, row.VerifiedAt, subsTimeFull), "last delivery "+subsWhen(m, row.LastDeliveryAt, subsTimeFull))
	return Pane(subsPeer(m, row.PeerID)+" · "+Clean(row.Health), w, h, false, strings.Join(lines, "\n"))
}
