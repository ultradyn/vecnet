//go:build linux

package tui

import (
	"runtime"
	"syscall"
	"unsafe"
)

// kernelSigaction matches the Linux rt_sigaction argument. The mask is one
// word: the kernel is told so by the sigsetsize argument.
type kernelSigaction struct {
	handler  uintptr
	flags    uint64
	restorer uintptr
	mask     uint64
}

var (
	savedStop     kernelSigaction
	haveSavedStop bool
)

func suspendDefault() {
	var dfl kernelSigaction // handler 0 is SIG_DFL
	if rtSigaction(syscall.SIGTSTP, &dfl, &savedStop) == nil {
		haveSavedStop = true
	}
}

// raiseStop unblocks SIGTSTP on this thread and sends it here. With SIG_DFL
// installed, the kernel stops the process before the syscall returns.
func raiseStop() error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err := unblockTSTP(); err != nil {
		return err
	}
	tid, _, errno := syscall.RawSyscall(syscall.SYS_GETTID, 0, 0, 0)
	if errno != 0 {
		return errno
	}
	_, _, errno = syscall.RawSyscall(syscall.SYS_TGKILL, uintptr(syscall.Getpid()), tid, uintptr(syscall.SIGTSTP))
	if errno != 0 {
		return errno
	}
	return nil
}

func unblockTSTP() error {
	set := uint64(1) << (uint(syscall.SIGTSTP) - 1)
	_, _, errno := syscall.Syscall6(syscall.SYS_RT_SIGPROCMASK, 1, uintptr(unsafe.Pointer(&set)), 0, unsafe.Sizeof(set), 0, 0)
	if errno != 0 {
		return errno
	}
	return nil
}

func restoreStopHandler() {
	if !haveSavedStop {
		return
	}
	_ = rtSigaction(syscall.SIGTSTP, &savedStop, nil)
	haveSavedStop = false
}

func rtSigaction(sig syscall.Signal, new, old *kernelSigaction) error {
	_, _, errno := syscall.Syscall6(syscall.SYS_RT_SIGACTION, uintptr(sig), uintptr(unsafe.Pointer(new)), uintptr(unsafe.Pointer(old)), unsafe.Sizeof(uint64(0)), 0, 0)
	if errno != 0 {
		return errno
	}
	return nil
}
