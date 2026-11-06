//go:build linux

package tui

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
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

// TestTerminalExternalSuspendResume sends SIGTSTP to the foreground process
// group of the picker and the main TUI. A session leader in that session, and
// not this test, is the parent, so the group is not orphaned. The child has to
// actually stop, with mouse capture released, and SIGCONT has to put capture back.
func TestTerminalExternalSuspendResume(t *testing.T) {
	switch os.Getenv("VN_TUI_PTY") {
	case "child":
		runExternalChild()
		return
	case "leader":
		runExternalLeader()
		return
	}
	for _, mode := range []string{"picker", "tui"} {
		t.Run(mode, func(t *testing.T) {
			master, slave := openPty(t)
			defer master.Close()
			pr, pw, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer pr.Close()
			cmd := exec.Command(os.Args[0], "-test.run=^TestTerminalExternalSuspendResume$")
			cmd.Env = append(os.Environ(), "VN_TUI_PTY=leader", "VN_TUI_PTY_MODE="+mode, "TERM=xterm-256color")
			cmd.Stdin = slave
			cmd.Stdout = slave
			cmd.Stderr = slave
			cmd.ExtraFiles = []*os.File{pw}
			cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
			if err := cmd.Start(); err != nil {
				pw.Close()
				t.Fatal(err)
			}
			pw.Close()
			slave.Close()
			var child int
			defer func() {
				if child > 0 && !taskExited(child) {
					_ = syscall.Kill(child, syscall.SIGCONT)
					_ = syscall.Kill(child, syscall.SIGKILL)
				}
				if cmd.ProcessState == nil {
					_ = cmd.Process.Kill()
					_ = cmd.Wait()
				}
			}()
			cap := &ptyCapture{f: master}
			cap.start()
			child = readLeaderPid(t, pr)
			_, ppid, pgrp, sid, tpgid, ok := procStat(child)
			if !ok || ppid != cmd.Process.Pid || pgrp != child || tpgid != child {
				t.Fatalf("foreground group pid %d ppid %d pgrp %d tpgid %d leader %d ok %v", child, ppid, pgrp, tpgid, cmd.Process.Pid, ok)
			}
			leaderSID, err := unix.Getsid(cmd.Process.Pid)
			if err != nil {
				t.Fatal(err)
			}
			parentSID, err := unix.Getsid(os.Getpid())
			if err != nil {
				t.Fatal(err)
			}
			if sid != leaderSID || sid == parentSID {
				t.Fatalf("process group is orphaned: child sid %d leader sid %d test sid %d", sid, leaderSID, parentSID)
			}
			cap.until(t, "\x1b[?1002h", 5*time.Second)
			if err := syscall.Kill(-child, syscall.SIGTSTP); err != nil {
				t.Fatal(err)
			}
			deadline := time.Now().Add(3 * time.Second)
			for time.Now().Before(deadline) && !(taskStopped(child) && cap.count("\x1b[?1002l") >= 1) {
				time.Sleep(20 * time.Millisecond)
			}
			if !taskStopped(child) {
				state, ppid, pgrp, sid, tpgid, ok := procStat(child)
				t.Fatalf("SIGTSTP did not stop the foreground group state %q ppid %d pgrp %d sid %d tpgid %d ok %v mouseOff %d out %q", state, ppid, pgrp, sid, tpgid, ok, cap.count("\x1b[?1002l"), cap.text())
			}
			if cap.count("\x1b[?1002l") < 1 {
				t.Fatal("stopping did not release mouse capture")
			}
			if err := syscall.Kill(-child, syscall.SIGCONT); err != nil {
				t.Fatal(err)
			}
			cap.untilCount(t, "\x1b[?1002h", 2, 3*time.Second)
			if _, err := master.Write([]byte("q")); err != nil {
				t.Fatal(err)
			}
			cap.untilCount(t, "\x1b[?1002l", 2, 3*time.Second)
			waitCmd(t, cmd, 3*time.Second)
			if !mouseOrder(cap.text(), false) {
				t.Fatalf("external suspend/resume mouse sequence: %q", cap.text())
			}
			if cmd.ProcessState.ExitCode() != 0 {
				t.Fatalf("exit %d", cmd.ProcessState.ExitCode())
			}
		})
	}
}

// runExternalLeader is the session leader. It starts the TUI in a new process
// group of the same session and makes that group the terminal's foreground, so
// SIGTSTP is not discarded as an orphaned-group signal.
func runExternalLeader() {
	mode := os.Getenv("VN_TUI_PTY_MODE")
	env := make([]string, 0, len(os.Environ())+2)
	for _, e := range os.Environ() {
		if strings.HasPrefix(e, "VN_TUI_PTY=") {
			continue
		}
		env = append(env, e)
	}
	env = append(env, "VN_TUI_PTY=child", "VN_TUI_PTY_MODE="+mode)
	cmd := exec.Command(os.Args[0], "-test.run=^TestTerminalExternalSuspendResume$")
	cmd.Env = env
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "leader: %v\n", err)
		os.Exit(1)
	}
	if err := unix.IoctlSetPointerInt(int(os.Stdin.Fd()), unix.TIOCSPGRP, cmd.Process.Pid); err != nil {
		fmt.Fprintf(os.Stderr, "leader pgrp: %v\n", err)
		os.Exit(1)
	}
	f := os.NewFile(3, "pid")
	fmt.Fprintf(f, "%d\n", cmd.Process.Pid)
	f.Close()
	if err := cmd.Wait(); err != nil {
		os.Exit(1)
	}
	os.Exit(0)
}

func runExternalChild() {
	var err error
	switch os.Getenv("VN_TUI_PTY_MODE") {
	case "picker":
		_, err = Pick(context.Background(), []ProfileChoice{{Name: "fly", Hub: "https://hub.example"}}, os.Stdin, os.Stdout)
	case "tui":
		err = Run(context.Background(), Options{API: newFake(), Profile: "fly", Hub: "https://hub.example", In: os.Stdin, Out: os.Stdout})
	default:
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "external: %v\n", err)
		os.Exit(1)
	}
	os.Exit(0)
}

func readLeaderPid(t *testing.T, r *os.File) int {
	t.Helper()
	got := make(chan int, 1)
	fail := make(chan error, 1)
	go func() {
		var pid int
		_, err := fmt.Fscan(r, &pid)
		if err != nil {
			fail <- err
			return
		}
		got <- pid
	}()
	select {
	case pid := <-got:
		if pid <= 0 {
			t.Fatalf("leader reported pid %d", pid)
		}
		return pid
	case err := <-fail:
		t.Fatalf("leader did not report a foreground pid: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("leader did not report a foreground pid")
	}
	return 0
}

// procStat reads the fields job control cares about. comm can contain spaces,
// so the parse starts after the last ')'.
func procStat(pid int) (state byte, ppid, pgrp, sid, tpgid int, ok bool) {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0, 0, 0, 0, 0, false
	}
	i := bytes.LastIndexByte(b, ')')
	if i < 0 || i+2 >= len(b) {
		return 0, 0, 0, 0, 0, false
	}
	fields := strings.Fields(string(b[i+2:]))
	if len(fields) < 6 || fields[0] == "" {
		return 0, 0, 0, 0, 0, false
	}
	ppid, err1 := strconv.Atoi(fields[1])
	pgrp, err2 := strconv.Atoi(fields[2])
	sid, err3 := strconv.Atoi(fields[3])
	tpgid, err4 := strconv.Atoi(fields[5])
	if err1 != nil || err2 != nil || err3 != nil || err4 != nil {
		return 0, 0, 0, 0, 0, false
	}
	return fields[0][0], ppid, pgrp, sid, tpgid, true
}

func taskExited(pid int) bool {
	return syscall.Kill(pid, 0) != nil
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
