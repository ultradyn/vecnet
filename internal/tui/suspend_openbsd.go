//go:build openbsd

package tui

import (
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

type openbsdSigaction struct {
	handler uintptr
	mask    uint32
	flags   int32
}

func setStopDFL() error {
	var sa openbsdSigaction
	_, _, errno := unix.RawSyscall(unix.SYS_SIGACTION, uintptr(syscall.SIGTSTP), uintptr(unsafe.Pointer(&sa)), 0)
	if errno != 0 {
		return errno
	}
	return nil
}

func unblockTSTP() error {
	// OpenBSD's sigprocmask syscall takes the mask by value.
	mask := uintptr(uint32(1) << (uint32(syscall.SIGTSTP) - 1))
	// SIG_UNBLOCK is 2.
	_, _, errno := unix.RawSyscall(unix.SYS_SIGPROCMASK, 2, mask, 0)
	if errno != 0 {
		return errno
	}
	return nil
}
