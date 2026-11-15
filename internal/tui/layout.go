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
	"github.com/XertroV/vecnet/internal/hubnotice"
	"github.com/XertroV/vecnet/internal/tui/theme"
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
// A focused pane is heavy (┏━┓) in focus with an accent bold title; any other
// is light (┌─┐) in rule with a muted title (design §6.4). Exactly one pane
// per frame should be focused.
func Pane(th *theme.Theme, title string, w, h int, focused bool, body string) string {
	_, _, heading := th.Pane(focused)
	if title != "" {
		title = heading.Render(title)
	}
	return PaneRich(th, title, w, h, focused, body)
}

// PaneTitle is the style a pane's title takes, for a caller that builds a
// title from several styled parts for PaneRich.
func PaneTitle(th *theme.Theme, focused bool) lipgloss.Style {
	_, _, heading := th.Pane(focused)
	return heading
}

// PaneRich is Pane with a title the caller has already styled part by part:
// PaneTitle for the plain parts, a state role for a count or a health.
func PaneRich(th *theme.Theme, title string, w, h int, focused bool, body string) string {
	if w < 2 || h < 2 {
		return ""
	}
	b, edge, _ := th.Pane(focused)
	inner := w - 2
	label := ""
	if title != "" {
		label = " " + title + " "
		if lipgloss.Width(label) > inner-1 {
			label = ansi.Truncate(label, max(inner-1, 0), "")
		}
	}
	rule := strings.Repeat(b.Top, max(inner-1-lipgloss.Width(label), 0))
	rows := make([]string, 0, h)
	top := edge.Render(b.TopLeft + b.Top)
	if inner == 0 {
		top = edge.Render(b.TopLeft)
	}
	rows = append(rows, top+label+edge.Render(rule+b.TopRight))
	left, right := edge.Render(b.Left), edge.Render(b.Right)
	if h > 2 { // a 2-row pane is all border: block would still yield one empty row
		for _, line := range strings.Split(block(body, inner, h-2), "\n") {
			rows = append(rows, left+line+right)
		}
	}
	rows = append(rows, edge.Render(b.BottomLeft+strings.Repeat(b.Bottom, inner)+b.BottomRight))
	return strings.Join(rows, "\n")
}

// fitFill is fit with its padding drawn in style pad, so a filled row (the
// header bar, a selected row) reaches the edge.
func fitFill(s string, w int, pad lipgloss.Style) string {
	if w <= 0 {
		return ""
	}
	s = strings.ReplaceAll(s, "\t", "    ")
	if lipgloss.Width(s) > w {
		s = ansi.Truncate(s, w, "")
	}
	if n := w - lipgloss.Width(s); n > 0 {
		s += pad.Render(strings.Repeat(" ", n))
	}
	return s
}

// stubScreen holds a tab's place until its task lands. It lives here rather than
// in a screen file, because each of those files is replaced whole by its task.
type stubScreen struct{ title string }

func (s stubScreen) Title() string                  { return s.title }
func (s stubScreen) Update(tea.Msg, *Model) tea.Cmd { return nil }
func (s stubScreen) Hints() string                  { return "1-4 tabs  : command  ? help  q quit" }
func (s stubScreen) View(m *Model, w, h int) string {
	return Pane(m.th, s.title, w, h, true, m.th.Fg(theme.Muted).Render("this tab is not built yet"))
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
		banner = viewNotices(m.th, m.Data.Notices, m.HubNow(), m.loc, w)
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
		body = Pane(m.th, "help", w, bodyH, true, viewHelp(m))
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

// badged is a screen whose tab carries an attention count, drawn after its
// name in unread: `4:subs 8`.
type badged interface {
	Badge() (name string, n int)
}

// viewTop is `vecnet | 1:inbox 2:chat 3:dash 4:subs` and, always, the profile
// and hub. The profile line is never the thing that drops: connection, counts
// and the clock give way first, in the same order they used to after the hub.
// It is the header bar fill, edge to edge (design §5.2).
func (m *Model) viewTop(w int) string {
	th := m.th
	bar, muted := th.Fill(theme.Bar), th.OnBar(theme.Muted)
	var left strings.Builder
	cell := 0
	write := func(s string) {
		left.WriteString(s)
		cell += lipgloss.Width(s)
	}
	write(th.Brand(" vecnet "))
	write(muted.Render("| "))
	for i, s := range m.screens {
		if i > 0 {
			write(bar.Render(" "))
		}
		label := fmt.Sprintf("%d:%s", i+1, s.Title())
		var styled string
		switch {
		case i == m.Tab && !m.prefsOn:
			styled = th.Fill(theme.Tab).Render(label)
		default:
			styled = muted.Render(label)
			if b, ok := s.(badged); ok {
				if name, n := b.Badge(); n > 0 {
					styled = muted.Render(fmt.Sprintf("%d:%s ", i+1, name)) + th.OnBar(theme.Unread).Render(fmt.Sprint(n))
				}
			}
		}
		m.addHit(cell, 0, lipgloss.Width(styled), 1, hitTab, fmt.Sprint(i))
		write(styled)
	}
	if m.prefsOn {
		write(bar.Render(" "))
		write(th.Fill(theme.Tab).Render("prefs"))
	}
	apr, dlq := len(m.Data.Inbox.Approvals), len(m.Data.Inbox.DeadLetters)
	// Display order after the profile: conn, apr, dlq, cuts, clock.
	// The connection state stays, as it did before the profile took the hub's
	// old slot. Cuts, the clock, dlq and apr give way first.
	extras := []string{
		th.OnBar(theme.Conn(m.Conn)).Render(m.Conn),
		th.OnBar(theme.Count(apr, theme.Warn)).Render(fmt.Sprintf("apr %d", apr)),
		th.OnBar(theme.Count(dlq, theme.Error)).Render(fmt.Sprintf("dlq %d", dlq)),
		muted.Render(fmt.Sprintf("cuts/24h %d", m.Data.Cuts.Total)),
		th.OnBar(theme.Text).Render(m.HubNow().In(m.loc).Format("15:04:05")),
	}
	gap := func(n int) string { return bar.Render(strings.Repeat(" ", max(n, 0))) }
	sep := gap(2)
	display := []int{0, 1, 2, 3, 4}
	survive := []int{0, 1, 2, 4, 3} // conn, apr, dlq, clock, cuts
	leftPlain := left.String()
	leftW := lipgloss.Width(leftPlain)
	// Two columns stay between the tabs and the profile, so an 80-column bar
	// never glues the profile name onto "4:subs".
	const headerGap = 2
	place := func(label string, minKeep int) (string, bool) {
		id := m.hubLabel(label, true)
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
			right := strings.Join(shown, sep) + sep
			if rw := lipgloss.Width(right); leftW+headerGap+rw <= w {
				return leftPlain + gap(w-leftW-rw) + right, true
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
	tail := sep + extras[0] + sep
	room := w - leftW - headerGap - lipgloss.Width(tail)
	label := budgetHubLabel(m.Profile, m.Hub, room)
	right := m.hubLabel(label, true) + tail
	if g := w - leftW - lipgloss.Width(right); g >= headerGap {
		return leftPlain + gap(g) + right
	}
	return fitFill(leftPlain+sep+right, w, bar)
}

// hubLabel draws a profile·hub label in muted, with the " local" that
// profileHubLabel adds for a loopback hub in warn. The marker comes from the
// hub's host, never from the profile's name.
func (m *Model) hubLabel(label string, onBar bool) string {
	return hubLabelStyled(m.th, label, loopbackHost(hubHost(m.Hub)), onBar)
}

func hubLabelStyled(th *theme.Theme, label string, local, onBar bool) string {
	role := th.Fg
	if onBar {
		role = th.OnBar
	}
	if local && strings.HasSuffix(label, " local") {
		base := strings.TrimSuffix(label, " local")
		return role(theme.Muted).Render(base) + role(theme.Muted).Render(" ") + role(theme.Warn).Render("local")
	}
	return role(theme.Muted).Render(label)
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

// connStyle is the header's connection state in its role.
func (m *Model) connStyle() lipgloss.Style {
	return m.th.Fg(theme.Conn(m.Conn))
}

// viewStatus is the mode chip, the screen's hints and who the operator is.
func (m *Model) viewStatus(w int) string {
	th := m.th
	chip := th.Mode(m.Mode.String()).Render(" " + m.Mode.String() + " ")
	name, presence := Clean(strings.ToLower(m.Data.Inbox.Me.Name)), Clean(m.Data.Inbox.Me.Presence)
	if name == "" {
		name = "operator"
	}
	if presence == "" {
		presence = "unknown"
	}
	prio := Clean(m.Prio)
	who := th.Fg(theme.Muted).Render(fmt.Sprintf("%s@vecnet · %s · prio ", name, presence)) +
		th.Fg(theme.Priority(m.Prio)).Render(prio) + " "
	room := w - lipgloss.Width(chip) - lipgloss.Width(who) - 2
	return fit(chip+"  "+fit(hintLine(th, m.active().Hints()), room)+who, w)
}

// hintLine draws a Hints() string: pairs are separated by two spaces, and a
// pair's key is its leading run of key words, in text; the words after it are
// muted (§7.5). So `[ ] thread` keys `[ ]`, a narrow all-keys string is all
// key, and `other keys cancel` has no key.
func hintLine(th *theme.Theme, hints string) string {
	var b strings.Builder
	for i, pair := range strings.Split(hints, "  ") {
		if i > 0 {
			b.WriteString("  ")
		}
		words := strings.Split(pair, " ")
		n := 0
		for n < len(words) && hintKey(words[n]) {
			n++
		}
		key, rest := strings.Join(words[:n], " "), strings.Join(words[n:], " ")
		if key != "" {
			b.WriteString(th.Fg(theme.Text).Render(key))
			if rest != "" {
				rest = " " + rest
			}
		}
		if rest != "" {
			b.WriteString(th.Fg(theme.Muted).Render(rest))
		}
	}
	return b.String()
}

// hintKeyNames are the named keys Hints() strings use.
var hintKeyNames = map[string]bool{"enter": true, "esc": true, "tab": true, "space": true, "digit": true}

// hintKey reports whether a Hints() word names a key: one printable ASCII
// character, an alternation or chord (`j/k`, `ctrl+u/d`), a digit range
// (`1-4`), a command (`:react`) or a named key.
func hintKey(w string) bool {
	switch {
	case len(w) == 1:
		return w[0] > ' ' && w[0] < 0x7f
	case strings.ContainsAny(w, "/+"), strings.HasPrefix(w, ":"):
		return true
	case len(w) == 3 && w[1] == '-' && unicode.IsDigit(rune(w[0])) && unicode.IsDigit(rune(w[2])):
		return true
	}
	return hintKeyNames[w]
}

// viewCommandLine is the last row: the command being typed, a confirmation, a
// toast or the reminder.
func (m *Model) viewCommandLine(w int) string {
	th := m.th
	switch {
	case m.confirmQuit:
		return fit(th.Fg(theme.Warn).Render("quit and lose this draft? y/n"), w)
	case m.Mode == ModeCommand:
		return fit(th.Fg(theme.Accent).Render(":")+th.Fg(theme.Text).Render(Clean(m.Input)), w)
	case m.toast.text != "":
		if m.toast.bad {
			return fit(th.Fg(theme.Error).Render(m.toast.text), w)
		}
		return fit(th.Fg(theme.Success).Render(m.toast.text), w)
	}
	return fit(th.Fg(theme.Muted).Render(": command  ? help  Shift+drag selects text"), w)
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
	return Pane(m.th, m.active().Title(), w, h, true, m.th.Fg(theme.Muted).Render(line))
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
func viewNotices(th *theme.Theme, ns []envelope.HubNotice, now time.Time, loc *time.Location, w int) string {
	var shown *envelope.HubNotice
	active := 0
	for i := range ns {
		n := &ns[i]
		if hubnotice.ValidateNotice(*n, true) != nil || !activeNotice(*n, now) {
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
	// Coloured by kind like the web chips, a bold head rather than an inverse
	// block: a planned maintenance must not read as an alarm. The row is not
	// filled (design §8.1).
	headRole, detailRole := theme.Notice(shown.Kind)
	line := th.Fg(headRole).Bold(true).Render(head) + th.Fg(detailRole).Render(detail)
	if msg != "" {
		line += th.Fg(theme.Text).Render("  " + msg)
	}
	line += th.Fg(theme.Muted).Render(more)
	return fit(line, w)
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
