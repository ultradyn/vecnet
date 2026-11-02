package tui

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

const (
	minW, minH = 80, 24
	tooSmall   = "terminal too small: need 80x24"
	// firstLoadPatience is how long the body says connecting before it names
	// what the first load is still waiting on.
	firstLoadPatience = 2 * time.Second
)

// fit makes one styled line exactly w cells wide.
func fit(s string, w int) string {
	if w <= 0 {
		return ""
	}
	s = strings.ReplaceAll(s, "\t", "    ")
	if lipgloss.Width(s) > w {
		s = ansi.Truncate(s, w, "")
	}
	if pad := w - lipgloss.Width(s); pad > 0 {
		s += strings.Repeat(" ", pad)
	}
	return s
}

// block makes styled text exactly w by h cells, so a screen cannot push the
// bars out of place whatever it returns.
func block(s string, w, h int) string {
	lines := strings.Split(s, "\n")
	if len(lines) > h {
		lines = lines[:h]
	}
	for len(lines) < h {
		lines = append(lines, "")
	}
	for i, line := range lines {
		lines[i] = fit(line, w)
	}
	return strings.Join(lines, "\n")
}

// Pane draws body in a titled box of exactly w by h cells. The caller cleans
// peer-supplied title and body text. Lines past the box are cut, not wrapped.
func Pane(title string, w, h int, focused bool, body string) string {
	if w < 2 || h < 2 {
		return ""
	}
	border := styleBorder
	heading := styleMuted
	if focused {
		border = styleFocused
		heading = styleAccent
	}
	inner := w - 2
	label := ""
	if title != "" {
		label = " " + title + " "
		if lipgloss.Width(label) > inner-1 {
			label = ansi.Truncate(label, max(inner-1, 0), "")
		}
	}
	rule := strings.Repeat("─", max(inner-1-lipgloss.Width(label), 0))
	rows := make([]string, 0, h)
	top := border.Render("┌─")
	if inner == 0 {
		top = border.Render("┌")
	}
	rows = append(rows, top+heading.Render(label)+border.Render(rule+"┐"))
	side := border.Render("│")
	if h > 2 { // a 2-row pane is all border: block would still yield one empty row
		for _, line := range strings.Split(block(body, inner, h-2), "\n") {
			rows = append(rows, side+line+side)
		}
	}
	rows = append(rows, border.Render("└"+strings.Repeat("─", inner)+"┘"))
	return strings.Join(rows, "\n")
}

// stubScreen holds a tab's place until its task lands. It lives here rather than
// in a screen file, because each of those files is replaced whole by its task.
type stubScreen struct{ title string }

func (s stubScreen) Title() string                  { return s.title }
func (s stubScreen) Update(tea.Msg, *Model) tea.Cmd { return nil }
func (s stubScreen) Hints() string                  { return "1-4 tabs  : command  ? help  q quit" }
func (s stubScreen) View(_ *Model, w, h int) string {
	return Pane(s.title, w, h, true, styleMuted.Render("this tab is not built yet"))
}

// render lays out top bar, body, status line and command line.
func (m *Model) render() string {
	w, h := m.W, m.H
	if w <= 0 || h <= 0 {
		return ""
	}
	small := w < minW || h < minH
	// Below four rows there is no body row between the bars, so the too-small
	// line takes the whole frame.
	if small && h < 4 {
		return block(tooSmall, w, h)
	}
	bodyH := h - 3
	var body string
	switch {
	case small:
		body = tooSmall
	case m.Help:
		body = Pane("help", w, bodyH, true, viewHelp(m))
	case m.firstLoading():
		body = m.viewFirstLoad(w, bodyH)
	default:
		body = m.active().View(m, w, bodyH)
	}
	return strings.Join([]string{m.viewTop(w), block(body, w, bodyH), m.viewStatus(w), m.viewCommandLine(w)}, "\n")
}

// viewTop is `vecnet | 1:inbox 2:chat 3:dash 4:subs` and the hub's state.
func (m *Model) viewTop(w int) string {
	var left strings.Builder
	left.WriteString(styleBrand.Render(" vecnet ") + styleMuted.Render("| "))
	for i, s := range m.screens {
		if i > 0 {
			left.WriteString(" ")
		}
		label := fmt.Sprintf("%d:%s", i+1, s.Title())
		if i == m.Tab {
			left.WriteString(styleTabOn.Render(label))
		} else {
			left.WriteString(styleMuted.Render(label))
		}
	}
	apr, dlq := len(m.Data.Inbox.Approvals), len(m.Data.Inbox.DeadLetters)
	// Dropped from the front as the bar narrows, so the connection state and
	// what needs the operator survive at 80 columns.
	parts := []string{
		styleMuted.Render("hub " + hubHost(m.Hub)),
		styleMuted.Render(fmt.Sprintf("cuts/24h %d", m.Data.Cuts.Total)),
		styleText.Render(m.HubNow().In(m.loc).Format("15:04:05")),
		countStyle(dlq, styleBad).Render(fmt.Sprintf("dlq %d", dlq)),
		countStyle(apr, styleWarn).Render(fmt.Sprintf("apr %d", apr)),
		m.connStyle().Render(m.Conn),
	}
	order := []int{0, 5, 4, 3, 1, 2} // left to right: hub, conn, apr, dlq, cuts, clock
	room := w - lipgloss.Width(left.String()) - 1
	for drop := 0; drop < len(parts); drop++ {
		var shown []string
		for _, i := range order {
			if i >= drop {
				shown = append(shown, parts[i])
			}
		}
		right := strings.Join(shown, "  ") + " "
		if width := lipgloss.Width(right); width <= room {
			return left.String() + strings.Repeat(" ", w-lipgloss.Width(left.String())-width) + right
		}
	}
	return fit(left.String(), w)
}

func countStyle(n int, hot lipgloss.Style) lipgloss.Style {
	if n > 0 {
		return hot
	}
	return styleMuted
}

func (m *Model) connStyle() lipgloss.Style {
	switch m.Conn {
	case ConnLive:
		return styleGood
	case ConnConnecting:
		return styleWarn
	}
	return styleBad
}

// viewStatus is the mode chip, the screen's hints and who the operator is.
func (m *Model) viewStatus(w int) string {
	chip := styleModeChip.Render(" " + m.Mode.String() + " ")
	name, presence := Clean(strings.ToLower(m.Data.Inbox.Me.Name)), Clean(m.Data.Inbox.Me.Presence)
	if name == "" {
		name = "operator"
	}
	if presence == "" {
		presence = "unknown"
	}
	who := styleMuted.Render(fmt.Sprintf("%s@vecnet · %s · prio %s ", name, presence, Clean(m.Prio)))
	room := w - lipgloss.Width(chip) - lipgloss.Width(who) - 2
	return fit(chip+"  "+fit(styleMuted.Render(m.active().Hints()), room)+who, w)
}

// viewCommandLine is the last row: the command being typed, a confirmation, a
// toast or the reminder.
func (m *Model) viewCommandLine(w int) string {
	switch {
	case m.confirmQuit:
		return fit(styleWarn.Render("quit and lose this draft? y/n"), w)
	case m.Mode == ModeCommand:
		return fit(styleText.Render(":"+Clean(m.Input)), w)
	case m.toast.text != "":
		if m.toast.bad {
			return fit(styleBad.Render(m.toast.text), w)
		}
		return fit(styleGood.Render(m.toast.text), w)
	}
	return fit(styleMuted.Render(": command  ? help"), w)
}

// viewFirstLoad holds the body until the first load ends. It says connecting
// at first, then, when the hub is slow, names what it is still waiting on.
func (m *Model) viewFirstLoad(w, h int) string {
	line := "connecting to " + hubHost(m.Hub)
	if waited := m.Now().Sub(m.started); waited >= firstLoadPatience {
		var pending []string
		for _, key := range allKeys {
			if m.first[key] {
				pending = append(pending, key)
			}
		}
		line = fmt.Sprintf("first load: waiting on %s (%ds)", strings.Join(pending, ", "), int(waited.Seconds()))
	}
	return Pane(m.active().Title(), w, h, true, styleMuted.Render(line))
}
