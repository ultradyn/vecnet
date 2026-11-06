//go:build unix && !linux

package tui

import (
	"os/signal"
	"runtime"
	"syscall"
)

// stopReplaced is set once SIG_DFL is installed over Go's handler. Restoring
// that handler with the saved pointer drops the trampoline on some Unixes, so
// the runtime installs it again: Ignore clears the "we are handling this"
// flag that Stop leaves set, and the following Notify installs the real one.
var stopReplaced bool

func suspendDefault() {
	if setStopDFL() == nil {
		stopReplaced = true
	}
}

// raiseStop unblocks SIGTSTP on this thread and signals the process. The
// Linux path uses a thread-directed kill; other Unix kernels stop inside this
// call when the signal is unblocked and the disposition is SIG_DFL.
func raiseStop() error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err := unblockTSTP(); err != nil {
		return err
	}
	return syscall.Kill(syscall.Getpid(), syscall.SIGTSTP)
}

func restoreStopHandler() {
	if !stopReplaced {
		return
	}
	stopReplaced = false
	signal.Ignore(syscall.SIGTSTP)
}
