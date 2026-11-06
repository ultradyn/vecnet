package tui

import (
	"reflect"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func isCmd(cmd, want tea.Cmd) bool {
	if cmd == nil || want == nil {
		return cmd == nil && want == nil
	}
	return reflect.ValueOf(cmd).Pointer() == reflect.ValueOf(want).Pointer()
}

func TestCtrlZReturnsSuspend(t *testing.T) {
	m := newTestModel(t, newFake(), newClock())
	_, cmd := m.Update(keyPress("ctrl+z"))
	if !isCmd(cmd, tea.Suspend) {
		t.Fatalf("ctrl+z command = %v, want tea.Suspend", cmd)
	}
}

// modalOwned swallows navigation and mouse. Ctrl+Z is a shell key, like
// ctrl+l, so a composer, the command line, and quit confirmation do not
// take it. The composer must not see the key either.
func TestCtrlZSuspendsWhileModalOwnsInput(t *testing.T) {
	for _, mode := range []struct {
		name string
		set  func(*Model)
	}{
		{"insert", func(m *Model) { m.Mode = ModeInsert }},
		{"command", func(m *Model) { m.Mode = ModeCommand }},
		{"confirm quit", func(m *Model) { m.confirmQuit = true }},
	} {
		t.Run(mode.name, func(t *testing.T) {
			m := newTestModel(t, newFake(), newClock())
			screen := &staticScreen{title: "inbox"}
			m.screens[tabInbox] = screen
			m.Tab = tabInbox
			mode.set(m)
			_, cmd := m.Update(keyPress("ctrl+z"))
			if !isCmd(cmd, tea.Suspend) {
				t.Fatalf("ctrl+z command = %v, want tea.Suspend", cmd)
			}
			if len(screen.keys) != 0 {
				t.Fatalf("composer received ctrl+z: %v", screen.keys)
			}
		})
	}
}

func TestPickCtrlZReturnsSuspend(t *testing.T) {
	p := newPick([]ProfileChoice{{Name: "fly", Hub: "https://hub.example"}})
	_, cmd := p.Update(keyPress("ctrl+z"))
	if !isCmd(cmd, tea.Suspend) {
		t.Fatalf("picker ctrl+z command = %v, want tea.Suspend", cmd)
	}
	p.confirm = true
	_, cmd = p.Update(keyPress("ctrl+z"))
	if !isCmd(cmd, tea.Suspend) {
		t.Fatalf("picker confirm ctrl+z command = %v, want tea.Suspend", cmd)
	}
}

func TestResumeMsgRequestsRedraw(t *testing.T) {
	m := newTestModel(t, newFake(), newClock())
	m.W, m.H = 80, 24
	_, cmd := m.Update(tea.ResumeMsg{})
	if !isCmd(cmd, tea.ClearScreen) {
		t.Fatalf("ResumeMsg command = %v, want tea.ClearScreen", cmd)
	}
	if m.View().MouseMode != tea.MouseModeCellMotion {
		t.Fatal("resume view dropped declared mouse capture")
	}

	p := newPick([]ProfileChoice{{Name: "fly", Hub: "https://hub.example"}})
	_, cmd = p.Update(tea.ResumeMsg{})
	if !isCmd(cmd, tea.ClearScreen) {
		t.Fatalf("picker ResumeMsg command = %v, want tea.ClearScreen", cmd)
	}
	if p.View().MouseMode != tea.MouseModeCellMotion {
		t.Fatal("picker resume view dropped declared mouse capture")
	}
}
