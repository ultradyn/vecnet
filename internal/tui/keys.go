package tui

import tea "charm.land/bubbletea/v2"

// updateKey is the single place a key is routed. See Mode for who gets what.
func (m *Model) updateKey(msg tea.KeyPressMsg) tea.Cmd {
	key := msg.String()
	if m.confirmQuit {
		m.confirmQuit = false
		if key == "y" {
			return m.quit()
		}
		return nil
	}
	switch m.Mode {
	case ModeCommand:
		return updateCommand(m, msg)
	case ModeInsert:
		switch key {
		case "ctrl+c", "ctrl+q":
			// q itself is text here: a composer has to be able to type it.
			m.confirmQuit = true
			return nil
		case "ctrl+l":
			return m.reloadAll()
		}
		return m.active().Update(msg, m)
	}
	if m.Help {
		if key == "ctrl+c" {
			return m.quit()
		}
		m.Help = false
		return nil
	}
	switch key {
	case "q", "ctrl+c", "ctrl+q":
		return m.quit()
	case "1", "2", "3", "4":
		m.Tab = int(key[0] - '1')
		return nil
	case ":":
		m.Mode = ModeCommand
		m.Input = ""
		return nil
	case "?":
		m.Help = true
		return nil
	case "ctrl+l":
		return m.reloadAll()
	}
	return m.active().Update(msg, m)
}

// updatePaste sends bracketed paste to whatever is taking text.
func (m *Model) updatePaste(msg tea.PasteMsg) tea.Cmd {
	switch m.Mode {
	case ModeCommand:
		return updateCommand(m, msg)
	case ModeInsert:
		return m.active().Update(msg, m)
	}
	return nil
}

func (m *Model) reloadAll() tea.Cmd {
	if m.stopped() {
		m.Toast(m.Conn+": restart with a valid profile", true)
		return nil
	}
	return m.Reload(TagAll)
}
