package tui

import (
	"strconv"

	tea "charm.land/bubbletea/v2"
)

// hitKind is what a click or wheel does in one cell of the last frame.
type hitKind int

const (
	hitTab hitKind = iota
	hitRow
	hitButton
	hitScroll
)

// hit is one rectangle recorded while rendering. Later hits win, so a row
// painted over a scroll pane is the click and the pane is the wheel.
type hit struct {
	x, y, w, h int
	kind       hitKind
	id         string
}

// pointerScreen is a tab that takes clicks and wheels. The shell still owns
// tabs. A screen that does not implement this keeps working from the keyboard.
type pointerScreen interface {
	Click(id string, m *Model) tea.Cmd
	Wheel(id string, down bool, m *Model)
}

func (m *Model) addHit(x, y, w, h int, kind hitKind, id string) {
	if w <= 0 || h <= 0 || id == "" {
		return
	}
	m.hits = append(m.hits, hit{x: x, y: y, w: w, h: h, kind: kind, id: id})
}

func (m *Model) hitAt(x, y int, kind hitKind) (hit, bool) {
	for i := len(m.hits) - 1; i >= 0; i-- {
		h := m.hits[i]
		if h.kind != kind {
			continue
		}
		if x >= h.x && x < h.x+h.w && y >= h.y && y < h.y+h.h {
			return h, true
		}
	}
	return hit{}, false
}

// updateMouse handles a click or a wheel. Capture off drops the event: the
// terminal is not asked for mouse reports then, and Shift+drag is the
// terminal's own selection, which this client does not draw.
func (m *Model) updateMouse(msg tea.Msg) tea.Cmd {
	if !m.mouse {
		return nil
	}
	switch msg := msg.(type) {
	case tea.MouseClickMsg:
		if msg.Mod&tea.ModShift != 0 || msg.Button != tea.MouseLeft {
			return nil
		}
		return m.clickAt(msg.X, msg.Y)
	case tea.MouseWheelMsg:
		if msg.Mod&tea.ModShift != 0 {
			return nil
		}
		down := msg.Button == tea.MouseWheelDown
		if msg.Button != tea.MouseWheelDown && msg.Button != tea.MouseWheelUp {
			return nil
		}
		h, ok := m.hitAt(msg.X, msg.Y, hitScroll)
		if !ok {
			return nil
		}
		if p, ok := m.active().(pointerScreen); ok {
			p.Wheel(h.id, down, m)
		}
		return nil
	default:
		return nil
	}
}

// modalOwned is a composer, a command line or a quit confirmation. Keys do
// not change tabs in those states, and neither does a click.
func (m *Model) modalOwned() bool {
	return m.confirmQuit || m.Mode == ModeInsert || m.Mode == ModeCommand
}

func (m *Model) clickAt(x, y int) tea.Cmd {
	if h, ok := m.hitAt(x, y, hitTab); ok {
		if m.modalOwned() {
			return nil
		}
		n, err := strconv.Atoi(h.id)
		if err != nil || n < 0 || n >= len(m.screens) {
			return nil
		}
		m.Help = false
		m.prefsOn = false
		m.Tab = n
		return nil
	}
	for _, kind := range []hitKind{hitButton, hitRow} {
		h, ok := m.hitAt(x, y, kind)
		if !ok {
			continue
		}
		if p, ok := m.active().(pointerScreen); ok {
			return p.Click(h.id, m)
		}
		return nil
	}
	return nil
}

func runMouse(m *Model, args []string) tea.Cmd {
	switch {
	case len(args) == 0:
		m.mouse = !m.mouse
	case len(args) == 1 && (args[0] == "on" || args[0] == "off"):
		m.mouse = args[0] == "on"
	default:
		return usage(m, "mouse")
	}
	if m.mouse {
		m.Toast("mouse on; M or :mouse off releases it. Shift+drag selects text", false)
	} else {
		m.Toast("mouse off; :mouse on captures again", false)
	}
	return nil
}
