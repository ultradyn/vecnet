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
	sel int
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
		body = s.list(m, w-2)
	}
	return Pane("preferences", w, h, true, body)
}

func (s *prefsScreen) list(m *Model, width int) string {
	rows := prefRows(m.Data.Prefs)
	if s.sel >= len(rows) {
		s.sel = 0
	}
	if s.sel < 0 {
		s.sel = 0
	}
	lines := make([]string, 0, len(rows)+8)
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
	lines = append(lines, "")
	lines = append(lines, s.helpLines(rows, width)...)
	for i, line := range lines {
		lines[i] = fit(line, width)
	}
	return strings.Join(lines, "\n")
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
	if r := prefRangeText(d); r != "" {
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
		return strings.Join(d.AllowedValues, "|")
	}
	if d.Min == nil || d.Max == nil {
		return ""
	}
	text := fmt.Sprintf("%d..%d", *d.Min, *d.Max)
	if d.Unit != "" {
		text += " " + d.Unit
	}
	return text
}
