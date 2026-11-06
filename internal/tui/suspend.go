package tui

import (
	"fmt"

	tea "charm.land/bubbletea/v2"
)

// life runs a model through the shared panic path. A panic in Update quits,
// so the program's shutdown restores the terminal.
//
// Ctrl+Z returns tea.Suspend. Bubble Tea releases the terminal and stops the
// process group, then sends tea.ResumeMsg when the program continues. Mouse
// capture is declared on the view, so that redraw turns it back on. A SIGTSTP
// from another process is not handled.
type life struct {
	inner tea.Model
	panic error
}

func (l *life) Init() tea.Cmd { return l.inner.Init() }

func (l *life) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var next tea.Model
	var cmd tea.Cmd
	panicked := false
	func() {
		defer func() {
			if rec := recover(); rec != nil {
				panicked = true
				l.panic = fmt.Errorf("tui: %v", rec)
			}
		}()
		next, cmd = l.inner.Update(msg)
	}()
	if panicked {
		return l, tea.Quit
	}
	if next != nil {
		l.inner = next
	}
	return l, cmd
}

func (l *life) View() tea.View { return l.inner.View() }

// runProgram is how the TUI and the profile picker take the terminal.
// Modes such as mouse capture come from the view. Nothing is enabled by
// hand, so suspend has nothing extra to turn off.
func runProgram(m tea.Model, opts ...tea.ProgramOption) (tea.Model, error) {
	wrapped := &life{inner: m}
	p := tea.NewProgram(wrapped, opts...)
	final, err := p.Run()
	if got, ok := final.(*life); ok {
		if got.panic != nil && err == nil {
			err = got.panic
		}
		return got.inner, err
	}
	return final, err
}
