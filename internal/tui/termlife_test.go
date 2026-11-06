package tui

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"golang.org/x/sys/unix"
)

// TestTerminalLifecycle drives the shared program runner on a real pty.
// Exit, a caught panic, and suspend/resume each have to drop mouse capture,
// and resume has to put it back.
func TestTerminalLifecycle(t *testing.T) {
	if os.Getenv("VN_TUI_PTY") == "child" {
		runProbeChild()
		return
	}
	for _, mode := range []string{"exit", "panic", "suspend"} {
		t.Run(mode, func(t *testing.T) {
			master, slave := openPty(t)
			defer master.Close()
			cmd := exec.Command(os.Args[0], "-test.run=^TestTerminalLifecycle$")
			cmd.Env = append(os.Environ(), "VN_TUI_PTY=child", "VN_TUI_PTY_MODE="+mode, "TERM=xterm-256color")
			cmd.Stdin = slave
			cmd.Stdout = slave
			cmd.Stderr = slave
			cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			slave.Close()
			defer func() {
				if cmd.ProcessState == nil {
					_ = cmd.Process.Kill()
					_ = cmd.Wait()
				}
			}()

			cap := &ptyCapture{f: master}
			cap.start()
			cap.until(t, "\x1b[?1002h", 3*time.Second)
			switch mode {
			case "exit":
				if _, err := master.Write([]byte("q")); err != nil {
					t.Fatal(err)
				}
				cap.until(t, "\x1b[?1002l", 3*time.Second)
				waitCmd(t, cmd, 3*time.Second)
				if !mouseOrder(cap.text(), true) {
					t.Fatalf("exit did not release mouse capture: %q", cap.text())
				}
				if cmd.ProcessState.ExitCode() != 0 {
					t.Fatalf("exit %d", cmd.ProcessState.ExitCode())
				}
			case "panic":
				if _, err := master.Write([]byte("!")); err != nil {
					t.Fatal(err)
				}
				cap.until(t, "\x1b[?1002l", 3*time.Second)
				waitCmd(t, cmd, 3*time.Second)
				if !mouseOrder(cap.text(), true) {
					t.Fatalf("panic did not release mouse capture: %q", cap.text())
				}
				if cmd.ProcessState.ExitCode() == 0 {
					t.Fatal("a caught panic exited 0")
				}
			case "suspend":
				if _, err := master.Write([]byte{0x1a}); err != nil {
					t.Fatal(err)
				}
				cap.until(t, "\x1b[?1002l", 3*time.Second)
				// A host that honours SIGTSTP stays stopped until SIGCONT.
				// One that does not continues immediately and restores capture.
				deadline := time.Now().Add(300 * time.Millisecond)
				for time.Now().Before(deadline) && !taskStopped(cmd.Process.Pid) && cap.count("\x1b[?1002h") < 2 {
					time.Sleep(20 * time.Millisecond)
				}
				if taskStopped(cmd.Process.Pid) {
					if err := cmd.Process.Signal(syscall.SIGCONT); err != nil {
						t.Fatal(err)
					}
				}
				cap.untilCount(t, "\x1b[?1002h", 2, 3*time.Second)
				if _, err := master.Write([]byte("q")); err != nil {
					t.Fatal(err)
				}
				cap.untilCount(t, "\x1b[?1002l", 2, 3*time.Second)
				waitCmd(t, cmd, 3*time.Second)
				if !mouseOrder(cap.text(), false) {
					t.Fatalf("suspend/resume mouse sequence: %q", cap.text())
				}
			}
		})
	}
}

func runProbeChild() {
	_, err := runProgram(&mouseProbe{}, tea.WithInput(os.Stdin), tea.WithOutput(os.Stdout))
	if err != nil {
		fmt.Fprintf(os.Stderr, "probe: %v\n", err)
		os.Exit(1)
	}
	os.Exit(0)
}

// mouseProbe is the smallest view that asks for mouse capture.
type mouseProbe struct{}

func (mouseProbe) Init() tea.Cmd { return nil }

func (mouseProbe) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	key, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return mouseProbe{}, nil
	}
	switch key.String() {
	case "ctrl+z":
		disarmStop()
		return mouseProbe{}, doSuspend
	case "!":
		if os.Getenv("VN_TUI_PTY_MODE") == "panic" {
			panic("probe")
		}
	case "q":
		return mouseProbe{}, tea.Quit
	}
	return mouseProbe{}, nil
}

func (mouseProbe) View() tea.View {
	v := tea.NewView("probe")
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion
	return v
}

func openPty(t *testing.T) (master, slave *os.File) {
	t.Helper()
	master, err := os.OpenFile("/dev/ptmx", os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := unix.IoctlSetPointerInt(int(master.Fd()), unix.TIOCSPTLCK, 0); err != nil {
		master.Close()
		t.Fatal(err)
	}
	n, err := unix.IoctlGetInt(int(master.Fd()), unix.TIOCGPTN)
	if err != nil {
		master.Close()
		t.Fatal(err)
	}
	slave, err = os.OpenFile(fmt.Sprintf("/dev/pts/%d", n), os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		master.Close()
		t.Fatal(err)
	}
	if err := unix.IoctlSetWinsize(int(slave.Fd()), unix.TIOCSWINSZ, &unix.Winsize{Row: 24, Col: 80}); err != nil {
		slave.Close()
		master.Close()
		t.Fatal(err)
	}
	return master, slave
}

// ptyCapture keeps everything the child has written.
type ptyCapture struct {
	f  *os.File
	mu sync.Mutex
	b  []byte
}

func (p *ptyCapture) start() {
	go func() {
		tmp := make([]byte, 4096)
		for {
			n, err := p.f.Read(tmp)
			if n > 0 {
				p.mu.Lock()
				p.b = append(p.b, tmp[:n]...)
				p.mu.Unlock()
			}
			if err != nil {
				return
			}
		}
	}()
}

func (p *ptyCapture) text() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return string(p.b)
}

func (p *ptyCapture) count(needle string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return bytes.Count(p.b, []byte(needle))
}

func (p *ptyCapture) until(t *testing.T, needle string, d time.Duration) {
	t.Helper()
	p.untilCount(t, needle, 1, d)
}

func (p *ptyCapture) untilCount(t *testing.T, needle string, n int, d time.Duration) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if p.count(needle) >= n {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d×%q; so far %q", n, needle, p.text())
}

func taskStopped(pid int) bool {
	entries, err := os.ReadDir(fmt.Sprintf("/proc/%d/task", pid))
	if err != nil {
		return false
	}
	for _, entry := range entries {
		stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/task/%s/stat", pid, entry.Name()))
		if err != nil {
			continue
		}
		if i := bytes.LastIndex(stat, []byte(")")); i >= 0 && i+2 < len(stat) && (stat[i+2] == 'T' || stat[i+2] == 't') {
			return true
		}
	}
	return false
}

func waitCmd(t *testing.T, cmd *exec.Cmd, d time.Duration) {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-done:
	case <-time.After(d):
		t.Fatal("process did not exit")
	}
}

// mouseOrder reports whether capture was enabled and then released.
// resume requires a second enable between the two releases.
func mouseOrder(out string, once bool) bool {
	on, off := "\x1b[?1002h", "\x1b[?1002l"
	i := stringsIndex(out, on)
	if i < 0 {
		return false
	}
	j := stringsIndex(out[i+len(on):], off)
	if j < 0 {
		return false
	}
	if once {
		return true
	}
	rest := out[i+len(on)+j+len(off):]
	k := stringsIndex(rest, on)
	if k < 0 {
		return false
	}
	return stringsIndex(rest[k+len(on):], off) >= 0
}

func stringsIndex(s, sub string) int {
	return bytes.Index([]byte(s), []byte(sub))
}
