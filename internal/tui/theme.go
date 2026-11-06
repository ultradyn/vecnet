package tui

import (
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

// sectionBar is a `▌` section header: the bar in accent, the label bold.
// Both stay under NO_COLOR (design §6.6).
func sectionBar(th *theme.Theme, label string) string {
	return th.Fg(theme.Accent).Render("▌") + sectionHead(th, label)
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
