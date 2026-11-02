package tui

import (
	"sort"
	"strings"

	tea "charm.land/bubbletea/v2"
)

// updateCommand edits the command line while Mode is ModeCommand. Task 22
// replaces this stub with parsing and dispatch through m.Commands.
func updateCommand(m *Model, msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case tea.PasteMsg:
		m.Input += Clean(msg.Content)
	case tea.KeyPressMsg:
		switch msg.String() {
		case "esc", "ctrl+c", "enter":
			m.Mode = ModeNormal
			m.Input = ""
		case "backspace":
			if r := []rune(m.Input); len(r) > 0 {
				m.Input = string(r[:len(r)-1])
			}
		default:
			m.Input += msg.Text
		}
	}
	return nil
}

// viewHelp is the body while Help is open. Task 22 replaces this stub.
func viewHelp(m *Model) string {
	lines := []string{"1-4     switch tab", "q       quit", ":       command line", "?       this help", "ctrl+l  reload everything"}
	names := make([]string, 0, len(m.Commands))
	for name := range m.Commands {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		lines = append(lines, ":"+name+"  "+m.Commands[name].Help)
	}
	return strings.Join(lines, "\n")
}
