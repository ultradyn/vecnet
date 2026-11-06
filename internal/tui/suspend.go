package tui

import (
	"fmt"
	"os"
	"os/signal"
	"sync"
	"syscall"

	tea "charm.land/bubbletea/v2"
)

// stopWatch turns an external SIGTSTP into Bubble Tea's suspend path, and
// disarms itself first. Bubble Tea's suspend sends SIGTSTP to the process
// group; a Notify that is still installed would swallow that signal and the
// resume would never arrive.
type stopWatch struct {
	ch    chan os.Signal
	mu    sync.Mutex
	armed bool
	done  chan struct{}
}

var (
	stopMu         sync.Mutex
	stopCur        *stopWatch
	progMu         sync.Mutex
	currentProgram *tea.Program
)

// suspendNow asks the event loop to release the terminal and stop.
type suspendNow struct{}

func newStopWatch() *stopWatch {
	w := &stopWatch{ch: make(chan os.Signal, 1), done: make(chan struct{})}
	w.arm()
	return w
}

func (w *stopWatch) arm() {
	w.mu.Lock()
	defer w.mu.Unlock()
	select {
	case <-w.done:
		return
	default:
	}
	restoreStopHandler()
	if w.armed {
		return
	}
	w.armed = true
	signal.Notify(w.ch, syscall.SIGTSTP)
}

func (w *stopWatch) disarm() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.armed {
		return
	}
	w.armed = false
	// Leave the Go listener registered. Swapping the kernel handler to the
	// default is what lets the following SIGTSTP stop the process; signal.Reset
	// leaves Go's handler in place and that handler swallows the signal once
	// it has been enabled.
	suspendDefault()
}

func (w *stopWatch) stop() {
	w.disarm()
	select {
	case <-w.done:
	default:
		close(w.done)
	}
}

func (w *stopWatch) loop(p *tea.Program) {
	for {
		select {
		case <-w.done:
			return
		case <-w.ch:
			w.disarm()
			p.Send(suspendNow{})
		}
	}
}

// doSuspend releases the terminal, stops the process, and restores the
// terminal after it continues. It runs on the event loop so the stop happens
// on a thread whose SIGTSTP block can be lifted.
func doSuspend() tea.Msg {
	progMu.Lock()
	p := currentProgram
	progMu.Unlock()
	if p != nil {
		if err := p.ReleaseTerminal(); err != nil {
			armStop()
			return nil
		}
	}
	_ = unblockTSTP()
	_ = syscall.Kill(syscall.Getpid(), syscall.SIGTSTP)
	restoreStopHandler()
	if p != nil {
		_ = p.RestoreTerminal()
	}
	return tea.ResumeMsg{}
}

func setStop(w *stopWatch) {
	stopMu.Lock()
	stopCur = w
	stopMu.Unlock()
}

func clearStop(w *stopWatch) {
	stopMu.Lock()
	if stopCur == w {
		stopCur = nil
	}
	stopMu.Unlock()
}

// disarmStop releases the process-wide SIGTSTP watch. Ctrl+Z calls it before
// returning tea.Suspend, so the suspension that follows can actually stop.
func disarmStop() {
	stopMu.Lock()
	w := stopCur
	stopMu.Unlock()
	if w != nil {
		w.disarm()
	}
}

// armStop puts the watch back after the program continues.
func armStop() {
	stopMu.Lock()
	w := stopCur
	stopMu.Unlock()
	if w != nil {
		w.arm()
	}
}

// life runs a model through the shared suspend and panic path. A panic in
// Update quits, so the program's shutdown restores the terminal.
type life struct {
	inner tea.Model
	panic error
}

func (l *life) Init() tea.Cmd { return l.inner.Init() }

func (l *life) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg.(type) {
	case tea.ResumeMsg:
		armStop()
	case suspendNow:
		return l, doSuspend
	}
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

// runProgram is how the TUI and the profile picker take the terminal. It
// releases mouse capture on suspend and puts back whatever the view asks for
// when the program continues.
func runProgram(m tea.Model, opts ...tea.ProgramOption) (tea.Model, error) {
	w := newStopWatch()
	setStop(w)
	defer func() {
		w.stop()
		clearStop(w)
		progMu.Lock()
		currentProgram = nil
		progMu.Unlock()
	}()
	wrapped := &life{inner: m}
	p := tea.NewProgram(wrapped, opts...)
	progMu.Lock()
	currentProgram = p
	progMu.Unlock()
	go w.loop(p)
	final, err := p.Run()
	if got, ok := final.(*life); ok {
		if got.panic != nil && err == nil {
			err = got.panic
		}
		return got.inner, err
	}
	return final, err
}
