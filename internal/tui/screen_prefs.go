package tui

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/XertroV/vecnet/internal/prefs"
)

// prefsScreen is the read-only preferences view opened with :prefs.
// It loads and refreshes through GET and has no write key.
type prefsScreen struct {
	sel    int
	offset int // first visible list line; kept so resize and j/k stay put
}

func (s *prefsScreen) Title() string { return "prefs" }

func (s *prefsScreen) Hints() string { return "j/k move  r refresh" }

func (s *prefsScreen) Update(msg tea.Msg, m *Model) tea.Cmd {
	key, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return nil
	}
	rows := prefRows(m.Data.Prefs)
	switch key.String() {
	case "j", "down":
		if s.sel < len(rows)-1 {
			s.sel++
		}
	case "k", "up":
		if s.sel > 0 {
			s.sel--
		}
	case "r":
		if m.stopped() {
			m.Toast(m.Conn+": restart with a valid profile", true)
			return nil
		}
		return m.Reload("prefs")
	}
	return nil
}

func (s *prefsScreen) View(m *Model, w, h int) string {
	m.addHit(m.hitX, m.hitY, w, h, hitScroll, "prefs.list")
	var body string
	switch {
	case m.Data.PrefsNote != "":
		body = styleWarn.Render("preferences unavailable") + "\n" + styleMuted.Render(Clean(m.Data.PrefsNote))
	case m.Data.Errs["prefs"] != nil:
		body = styleBad.Render("could not load preferences") + "\n" + styleMuted.Render(Clean(m.Data.Errs["prefs"].Error()))
	case !m.Data.PrefsKnown:
		body = styleMuted.Render("loading preferences")
	case len(prefRows(m.Data.Prefs)) == 0:
		body = styleMuted.Render("no preferences")
	default:
		body = s.list(m, w-2, h-2)
	}
	return Pane("preferences", w, h, true, body)
}

func (s *prefsScreen) list(m *Model, width, height int) string {
	rows := prefRows(m.Data.Prefs)
	if s.sel >= len(rows) || s.sel < 0 {
		s.sel = 0
		s.offset = 0
	}
	if height < 1 || len(rows) == 0 {
		return ""
	}
	listLines := s.prefListLines(rows)
	help := s.helpLines(rows, width)
	lines := make([]string, 0, height)
	need := len(listLines) + 1 + len(help)
	from, n := 0, len(listLines)
	if need <= height {
		lines = append(lines, listLines...)
		lines = append(lines, "")
		lines = append(lines, help...)
	} else {
		listN, helpN := prefsPaneBudget(height, len(help))
		selLine := 0
		if s.sel < len(rows) {
			selLine = s.rowLine(rows, s.sel)
		}
		s.offset = prefsScroll(s.offset, selLine, listN, len(listLines))
		end := min(s.offset+listN, len(listLines))
		from, n = s.offset, end-s.offset
		lines = append(lines, listLines[s.offset:end]...)
		if helpN > 0 {
			lines = append(lines, "")
			lines = append(lines, help[:helpN]...)
		}
	}
	s.notePrefHits(m, rows, from, n, width)
	for i, line := range lines {
		lines[i] = fit(line, width)
	}
	return strings.Join(lines, "\n")
}

func (s *prefsScreen) Click(id string, m *Model) tea.Cmd {
	n, err := strconv.Atoi(strings.TrimPrefix(id, "pref:"))
	if err != nil {
		return nil
	}
	rows := prefRows(m.Data.Prefs)
	if n >= 0 && n < len(rows) {
		s.sel = n
	}
	return nil
}

func (s *prefsScreen) Wheel(id string, down bool, m *Model) {
	if id != "prefs.list" {
		return
	}
	rows := prefRows(m.Data.Prefs)
	if down {
		if s.sel < len(rows)-1 {
			s.sel++
		}
		return
	}
	if s.sel > 0 {
		s.sel--
	}
}

func (s *prefsScreen) notePrefHits(m *Model, rows []prefs.Descriptor, from, n, width int) {
	indexes := prefLineIndexes(rows)
	for i := 0; i < n; i++ {
		idx := from + i
		if idx < 0 || idx >= len(indexes) || indexes[idx] < 0 {
			continue
		}
		m.addHit(m.hitX+1, m.hitY+1+i, width, 1, hitRow, fmt.Sprintf("pref:%d", indexes[idx]))
	}
}

func prefLineIndexes(rows []prefs.Descriptor) []int {
	out := make([]int, 0, len(rows)+4)
	last := ""
	for i, d := range rows {
		if d.Group != last {
			last = d.Group
			out = append(out, -1)
		}
		out = append(out, i)
	}
	return out
}

func (s *prefsScreen) prefListLines(rows []prefs.Descriptor) []string {
	lines := make([]string, 0, len(rows)+4)
	last := ""
	for i, d := range rows {
		if d.Group != last {
			last = d.Group
			lines = append(lines, styleAccent.Render(prefGroupTitle(d.Group)))
		}
		marker := "  "
		if i == s.sel {
			marker = "> "
		}
		line := fmt.Sprintf("%s%-32s %-8s %-8s %s", marker, d.Key, formatTUIPref(d), d.Source, prefLiveLabel(d))
		lines = append(lines, styleText.Render(line))
	}
	return lines
}

func (s *prefsScreen) rowLine(rows []prefs.Descriptor, sel int) int {
	line := 0
	last := ""
	for i, d := range rows {
		if d.Group != last {
			last = d.Group
			line++
		}
		if i == sel {
			return line
		}
		line++
	}
	return 0
}

// prefsPaneBudget splits the pane between the grouped list and the selected
// row's help, keeping at least one list line when there is any height.
func prefsPaneBudget(height, helpLines int) (listN, helpN int) {
	const sep = 1
	if height <= 1 {
		return height, 0
	}
	helpN = helpLines
	if helpN+sep >= height {
		helpN = height - sep - 1
	}
	listN = height - sep - helpN
	if listN < 1 {
		listN = 1
		helpN = height - sep - listN
	}
	if helpN < 0 {
		helpN = 0
	}
	return listN, helpN
}

// prefsScroll moves the list window so the selected line stays visible.
func prefsScroll(offset, selLine, window, total int) int {
	if window < 1 {
		return 0
	}
	maxOff := max(total-window, 0)
	if offset > maxOff {
		offset = maxOff
	}
	if offset < 0 {
		offset = 0
	}
	if selLine < offset {
		offset = selLine
	}
	if selLine >= offset+window {
		offset = selLine - window + 1
	}
	if offset < 0 {
		return 0
	}
	if offset > maxOff {
		return maxOff
	}
	return offset
}

func (s *prefsScreen) helpLines(rows []prefs.Descriptor, width int) []string {
	if len(rows) == 0 || s.sel >= len(rows) {
		return []string{styleMuted.Render("no preferences")}
	}
	d := rows[s.sel]
	label := Clean(d.Label)
	if label == "" {
		label = d.Key
	}
	lines := []string{styleAccent.Render(label)}
	if desc := Clean(d.Description); desc != "" {
		for _, line := range strings.Split(ansi.Wordwrap(desc, max(width, 1), ""), "\n") {
			lines = append(lines, styleMuted.Render(line))
		}
	}
	if r := Clean(prefRangeText(d)); r != "" {
		lines = append(lines, styleMuted.Render(r))
	}
	source := "source " + string(d.Source)
	if d.Origin != "" {
		source += " " + Clean(d.Origin)
	}
	lines = append(lines, styleMuted.Render(source))
	lines = append(lines, styleMuted.Render(prefLiveLabel(d)))
	if d.UnavailableReason != "" {
		lines = append(lines, styleMuted.Render("lock: "+Clean(d.UnavailableReason)))
	}
	return lines
}

func prefRows(snap prefs.Snapshot) []prefs.Descriptor {
	rows := snap.Descriptors()
	order := map[string]int{
		prefs.GroupRetentionStorage:      0,
		prefs.GroupFiles:                 1,
		prefs.GroupNotificationsPresence: 2,
		prefs.GroupAgentsDefaults:        3,
	}
	sort.SliceStable(rows, func(i, j int) bool {
		gi, oki := order[rows[i].Group]
		gj, okj := order[rows[j].Group]
		if !oki {
			gi = len(order)
		}
		if !okj {
			gj = len(order)
		}
		if gi != gj {
			return gi < gj
		}
		return rows[i].Key < rows[j].Key
	})
	return rows
}

func prefGroupTitle(id string) string {
	switch id {
	case prefs.GroupRetentionStorage:
		return "Retention & storage"
	case prefs.GroupFiles:
		return "Files"
	case prefs.GroupNotificationsPresence:
		return "Notifications/presence"
	case prefs.GroupAgentsDefaults:
		return "Agents defaults"
	default:
		return Clean(id)
	}
}

func prefLiveLabel(d prefs.Descriptor) string {
	if d.Runtime == "live" && d.Editable && d.UnavailableReason == "" {
		return "live"
	}
	return "unavailable"
}

func formatTUIPref(d prefs.Descriptor) string {
	switch d.Type {
	case prefs.TypeBool:
		v, _ := d.Value.Bool()
		return strconv.FormatBool(v)
	case prefs.TypeEnum:
		v, _ := d.Value.Enum()
		return v
	case prefs.TypeDuration:
		n, _ := d.Value.Int64()
		return formatTUIDuration(n)
	default:
		n, _ := d.Value.Int64()
		return strconv.FormatInt(n, 10)
	}
}

func formatTUIDuration(sec int64) string {
	switch {
	case sec == 0:
		return "0s"
	case sec%86400 == 0:
		return fmt.Sprintf("%dd", sec/86400)
	case sec%3600 == 0:
		return fmt.Sprintf("%dh", sec/3600)
	case sec%60 == 0:
		return fmt.Sprintf("%dm", sec/60)
	default:
		return fmt.Sprintf("%ds", sec)
	}
}

func prefRangeText(d prefs.Descriptor) string {
	if len(d.AllowedValues) > 0 {
		parts := make([]string, len(d.AllowedValues))
		for i, v := range d.AllowedValues {
			parts[i] = Clean(v)
		}
		return strings.Join(parts, "|")
	}
	if d.Min == nil || d.Max == nil {
		return ""
	}
	text := fmt.Sprintf("%d..%d", *d.Min, *d.Max)
	if unit := Clean(d.Unit); unit != "" {
		text += " " + unit
	}
	return text
}
