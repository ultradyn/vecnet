package tui

import (
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/XertroV/vecnet/internal/tui/theme"
)

// Every colour comes from m.th (internal/tui/theme, docs/design/tui-color.md).
// This file holds the few drawing helpers more than one screen shares; it
// holds no colour value of its own.

// sectionHead is a section label: bold, so it keeps its meaning under
// NO_COLOR (design §6.3, §6.6).
func sectionHead(th *theme.Theme, label string) string {
	return th.Fg(theme.Bold).Render(label)
}

// selMark is a list row's marker column: `>` in accent on the selected row
// (on the selection fill), blanks on any other (design §6.5).
func selMark(th *theme.Theme, sel bool) string {
	if sel {
		return th.Sel(theme.Accent).Render(">")
	}
	return " "
}

// selPad fills the rest of a row to w cells: on the selection fill when sel,
// so the fill covers the pane's inner width; plain otherwise.
func selPad(th *theme.Theme, line string, w int, sel bool) string {
	if !sel {
		return fit(line, w)
	}
	return fitFill(line, w, th.Sel(theme.Text))
}

// paint is s in st, or nothing for an empty s: a run of styled segments
// never carries an empty SGR pair.
func paint(st lipgloss.Style, s string) string {
	if s == "" {
		return ""
	}
	return st.Render(s)
}

// joinStyled renders parts in order, each in its own style, so one segment's
// reset cannot leak into the next (design §6.8).
func joinStyled(parts ...styled) string {
	var b strings.Builder
	for _, p := range parts {
		b.WriteString(paint(p.st, p.s))
	}
	return b.String()
}

type styled struct {
	st lipgloss.Style
	s  string
}
