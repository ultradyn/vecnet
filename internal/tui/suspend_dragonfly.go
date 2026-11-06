//go:build dragonfly

package tui

import (
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

type dragonflySigaction struct {
	handler uintptr
	flags   int32
	mask    [4]uint32
}

func setStopDFL() error {
	var sa dragonflySigaction
	_, _, errno := unix.RawSyscall(unix.SYS_SIGACTION, uintptr(syscall.SIGTSTP), uintptr(unsafe.Pointer(&sa)), 0)
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
	_, _, errno := unix.RawSyscall(unix.SYS_SIGPROCMASK, 2, uintptr(unsafe.Pointer(&set)), 0)
	if errno != 0 {
		return errno
	}
	return nil
}
