//go:build netbsd

package tui

import (
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

// NetBSD's __sigprocmask14 is syscall 293. The generated unix list skips it.
const sysSigprocmask14 = 293

type netbsdSigaction struct {
	handler uintptr
	mask    [4]uint32
	flags   int32
}

func setStopDFL() error {
	var sa netbsdSigaction
	// Version 2. A SIG_DFL action does not need a trampoline.
	_, _, errno := unix.Syscall6(unix.SYS___SIGACTION_SIGTRAMP, uintptr(syscall.SIGTSTP), uintptr(unsafe.Pointer(&sa)), 0, 0, 2, 0)
	if errno != 0 {
		return errno
	}
	return nil
}

func unblockTSTP() error {
	var set [4]uint32
	bit := uint32(syscall.SIGTSTP) - 1
	set[bit/32] = 1 << (bit % 32)
	// SIG_UNBLOCK is 2.
	_, _, errno := unix.RawSyscall(sysSigprocmask14, 2, uintptr(unsafe.Pointer(&set)), 0)
	if errno != 0 {
		return errno
	}
	return nil
}
