package tui

import (
	"fmt"
	"math"
	"net"
	"strings"
	"time"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/XertroV/vecnet/internal/envelope"
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
	m.hits = m.hits[:0]
	small := w < minW || h < minH
	// Below four rows there is no body row between the bars, so the too-small
	// line takes the whole frame.
	if small && h < 4 {
		return block(tooSmall, w, h)
	}
	bodyH := h - 3
	banner := ""
	if !small {
		banner = viewNotices(m.Data.Notices, m.HubNow(), m.loc, w)
	}
	if banner != "" {
		bodyH--
	}
	m.hitX, m.hitY = 0, 1
	if banner != "" {
		m.hitY = 2
	}
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
	rows := []string{m.viewTop(w)}
	if banner != "" {
		rows = append(rows, banner)
	}
	return strings.Join(append(rows, block(body, w, bodyH), m.viewStatus(w), m.viewCommandLine(w)), "\n")
}

// viewTop is `vecnet | 1:inbox 2:chat 3:dash 4:subs` and, always, the profile
// and hub. The profile line is never the thing that drops: connection, counts
// and the clock give way first, in the same order they used to after the hub.
func (m *Model) viewTop(w int) string {
	var left strings.Builder
	cell := 0
	write := func(s string) {
		left.WriteString(s)
		cell += lipgloss.Width(s)
	}
	write(styleBrand.Render(" vecnet "))
	write(styleMuted.Render("| "))
	for i, s := range m.screens {
		if i > 0 {
			write(" ")
		}
		label := fmt.Sprintf("%d:%s", i+1, s.Title())
		var styled string
		if i == m.Tab && !m.prefsOn {
			styled = styleTabOn.Render(label)
		} else {
			styled = styleMuted.Render(label)
		}
		m.addHit(cell, 0, lipgloss.Width(styled), 1, hitTab, fmt.Sprint(i))
		write(styled)
	}
	if m.prefsOn {
		write(" ")
		write(styleTabOn.Render("prefs"))
	}
	apr, dlq := len(m.Data.Inbox.Approvals), len(m.Data.Inbox.DeadLetters)
	// Display order after the profile: conn, apr, dlq, cuts, clock.
	// The connection state stays, as it did before the profile took the hub's
	// old slot. Cuts, the clock, dlq and apr give way first.
	extras := []string{
		m.connStyle().Render(m.Conn),
		countStyle(apr, styleWarn).Render(fmt.Sprintf("apr %d", apr)),
		countStyle(dlq, styleBad).Render(fmt.Sprintf("dlq %d", dlq)),
		styleMuted.Render(fmt.Sprintf("cuts/24h %d", m.Data.Cuts.Total)),
		styleText.Render(m.HubNow().In(m.loc).Format("15:04:05")),
	}
	display := []int{0, 1, 2, 3, 4}
	survive := []int{0, 1, 2, 4, 3} // conn, apr, dlq, clock, cuts
	leftPlain := left.String()
	leftW := lipgloss.Width(leftPlain)
	// Two columns stay between the tabs and the profile, so an 80-column bar
	// never glues the profile name onto "4:subs".
	const headerGap = 2
	place := func(label string, minKeep int) (string, bool) {
		id := styleMuted.Render(label)
		for n := len(survive); n >= minKeep; n-- {
			keep := map[int]bool{}
			for _, i := range survive[:n] {
				keep[i] = true
			}
			shown := []string{id}
			for _, i := range display {
				if keep[i] {
					shown = append(shown, extras[i])
				}
			}
			right := strings.Join(shown, "  ") + "  "
			if rw := lipgloss.Width(right); leftW+headerGap+rw <= w {
				return leftPlain + strings.Repeat(" ", w-leftW-rw) + right, true
			}
		}
		return "", false
	}
	full := profileHubLabel(m.Profile, m.Hub)
	if line, ok := place(full, 1); ok {
		return line
	}
	// The short form still has the profile and the host. It is how a long
	// connection state fits beside them at 80 columns.
	compact := compactHubLabel(m.Profile, m.Hub)
	if line, ok := place(compact, 1); ok {
		return line
	}
	// The connection state stays on the right. The profile gives up cells
	// first, then the port. The host and a loopback marker stay visible.
	tail := "  " + extras[0] + "  "
	room := w - leftW - headerGap - lipgloss.Width(tail)
	label := budgetHubLabel(m.Profile, m.Hub, room)
	right := styleMuted.Render(label) + tail
	if gap := w - leftW - lipgloss.Width(right); gap >= headerGap {
		return leftPlain + strings.Repeat(" ", gap) + right
	}
	return fit(leftPlain+"  "+right, w)
}

// budgetHubLabel fits profile, host, an optional port and the loopback marker
// into room cells. The profile is truncated first and the port is dropped
// next, so the host and the local marker stay on the line.
func budgetHubLabel(profile, hub string, room int) string {
	if room < 1 {
		return ""
	}
	name := Clean(profile)
	if name == "" {
		name = "profile"
	}
	raw := hubHost(hub)
	host, port, err := net.SplitHostPort(raw)
	if err != nil {
		host, port = raw, ""
	}
	show := host
	if strings.Contains(host, ":") {
		show = "[" + host + "]"
	}
	local := ""
	if loopbackHost(raw) {
		local = " local"
	}
	portS := ""
	if port != "" {
		portS = ":" + port
	}
	const sep = "·"
	join := func(n, h, p, loc string) string { return n + sep + h + p + loc }
	if full := join(name, show, portS, local); lipgloss.Width(full) <= room {
		return full
	}
	if noPort := join(name, show, "", local); lipgloss.Width(noPort) <= room {
		return noPort
	}
	tail := sep + show + local
	if lw := lipgloss.Width(tail); lw < room && show != "" {
		return join(ansi.Truncate(name, room-lw, "…"), show, "", local)
	}
	if local != "" && lipgloss.Width(show)+lipgloss.Width(local) <= room && show != "" {
		return show + local
	}
	if local != "" && lipgloss.Width(local) < room && show != "" {
		h := ansi.Truncate(show, room-lipgloss.Width(local), "…")
		if h == "" {
			h = ansi.Truncate(show, 1, "")
		}
		if got := h + local; lipgloss.Width(got) <= room {
			return got
		}
	}
	if show == "" {
		return ansi.Truncate(name, room, "…")
	}
	return ansi.Truncate(show, room, "…")
}

// profileHubLabel is the header's profile and hub. A loopback hub is marked
// local so a dev hub cannot be mistaken for the one on Fly.
func profileHubLabel(profile, hub string) string {
	name := Clean(profile)
	if name == "" {
		name = "profile"
	}
	host := hubHost(hub)
	label := name + " · hub " + host
	if loopbackHost(host) {
		label += " local"
	}
	return label
}

// compactHubLabel is profileHubLabel without the word hub, for a full bar.
func compactHubLabel(profile, hub string) string {
	name := Clean(profile)
	if name == "" {
		name = "profile"
	}
	host := hubHost(hub)
	label := name + "·" + host
	if loopbackHost(host) {
		label += " local"
	}
	return label
}

// loopbackHost reports a loopback host, with or without a port. LAN addresses
// are not local: only 127.0.0.0/8, ::1 and the name localhost.
func loopbackHost(host string) bool {
	h := host
	if name, _, err := net.SplitHostPort(host); err == nil {
		h = name
	}
	h = strings.Trim(h, "[]")
	if strings.EqualFold(h, "localhost") {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
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
	case ConnConnecting, ConnOffline, ConnRestarting:
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
	return fit(styleMuted.Render(": command  ? help  Shift+drag selects text"), w)
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

// noticeRank orders the banner: a move matters most, then a restart, then
// maintenance. A kind this client does not know comes last.
var noticeRank = map[string]int{"moving": 0, "restart": 1, "maintenance": 2}

// activeNotice reports whether a listed notice still stands at now. The hub
// lists only active notices, but one can expire between fetches.
func activeNotice(n envelope.HubNotice, now time.Time) bool {
	if n.ClearedAt != "" {
		return false
	}
	exp, err := envelope.ParseTime(n.ExpiresAt)
	return err != nil || now.Before(exp)
}

// viewNotices is the one-row banner under the top bar, or "" with no active
// notice: "! MOVING 02:00 -> vecnet.xk.io  Moving to Fly tonight  (+1 more)".
// It shows the most important notice and counts the rest. The message is
// operator text, rendered like peer text: cleaned, flattened to ASCII so every
// cell width is exact, and cut before the count is.
func viewNotices(ns []envelope.HubNotice, now time.Time, loc *time.Location, w int) string {
	var shown *envelope.HubNotice
	active := 0
	for i := range ns {
		n := &ns[i]
		if !activeNotice(*n, now) {
			continue
		}
		active++
		if shown == nil || rank(n.Kind) < rank(shown.Kind) {
			shown = n
		}
	}
	if shown == nil || w <= 0 {
		return ""
	}
	label := "NOTICE"
	if _, ok := noticeRank[shown.Kind]; ok {
		label = strings.ToUpper(shown.Kind)
	}
	head := "! " + label
	detail := ""
	if at, err := envelope.ParseTime(shown.At); shown.At != "" && err == nil {
		// As the web banner: relative, then the clock time, dated unless
		// it is today, so 01:55 at 23:02 does not read as already past.
		local, today := at.In(loc), now.In(loc)
		layout := "15:04"
		if local.YearDay() != today.YearDay() || local.Year() != today.Year() {
			layout = "Jan 02 15:04"
		}
		detail += " " + relTime(at.Sub(now)) + " " + local.Format(layout)
	}
	if shown.Kind == "moving" && shown.NewURL != "" {
		detail += " -> " + asciiLine(hubHost(shown.NewURL))
	}
	msg := asciiLine(Clean(shown.Message))
	more := ""
	if active > 1 {
		more = fmt.Sprintf("  (+%d more)", active-1)
	}
	// Everything is ASCII from here, so bytes are cells.
	if msg != "" {
		room := w - len(head) - len(detail) - 2 - len(more)
		switch {
		case room >= len(msg):
		case room >= 4:
			msg = strings.TrimRight(msg[:room-3], " ") + "..."
		default:
			msg = ""
		}
	}
	// Coloured by kind like the web chips, bold text rather than an inverse
	// block: a planned maintenance must not read as an alarm.
	headStyle, detailStyle := noticeStyles(shown.Kind)
	line := headStyle.Render(head) + detailStyle.Render(detail)
	if msg != "" {
		line += styleText.Render("  " + msg)
	}
	line += styleMuted.Render(more)
	return fit(line, w)
}

func noticeStyles(kind string) (head, detail lipgloss.Style) {
	switch kind {
	case "moving":
		return styleAccent, lipgloss.NewStyle().Foreground(colorAccent2)
	case "restart":
		return styleWarn.Bold(true), styleWarn
	default:
		return styleText.Bold(true), styleMuted
	}
}

// relTime is the web banner's relative time: in 5m, 3h ago, in 2d.
func relTime(d time.Duration) string {
	a := d.Abs()
	var u string
	switch {
	case a < time.Second:
		return "now"
	case a < time.Minute:
		u = fmt.Sprintf("%ds", int(a.Round(time.Second)/time.Second))
	case a < time.Hour:
		u = fmt.Sprintf("%dm", int(a/time.Minute))
	case a < 24*time.Hour:
		u = fmt.Sprintf("%dh", int(math.Round(a.Hours())))
	default:
		u = fmt.Sprintf("%dd", int(math.Round(a.Hours()/24)))
	}
	if d > 0 {
		return "in " + u
	}
	return u + " ago"
}

func rank(kind string) int {
	if r, ok := noticeRank[kind]; ok {
		return r
	}
	return len(noticeRank)
}

// asciiReplace spells common typography in ASCII.
var asciiReplace = map[rune]string{
	'‐': "-", '‑': "-", '‒': "-", '–': "-", '—': "-", '―': "-", '−': "-",
	'‘': "'", '’': "'", '‚': "'", '′': "'", '“': `"`, '”': `"`, '„': `"`, '″': `"`,
	'…': "...", '•': "*", '→': "->", '←': "<-",
}

// asciiLine makes cleaned one-line text printable ASCII: typography gets its
// ASCII spelling, other non-ASCII runes become '?', and runs of spaces fold
// into one, so a banner's width is exactly its byte count.
func asciiLine(s string) string {
	var b strings.Builder
	space := false
	for _, r := range s {
		var out string
		switch {
		case r == ' ' || unicode.IsSpace(r):
			if !space && b.Len() > 0 {
				space = true
			}
			continue
		case r >= 0x21 && r <= 0x7e:
			out = string(r)
		case asciiReplace[r] != "":
			out = asciiReplace[r]
		case unicode.Is(unicode.Mn, r) || unicode.Is(unicode.Cf, r):
			continue // combining marks and invisible format runes take no cell
		default:
			out = "?"
		}
		if space {
			b.WriteByte(' ')
			space = false
		}
		b.WriteString(out)
	}
	return b.String()
}
