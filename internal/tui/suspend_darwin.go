//go:build darwin || ios

package tui

import (
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

// darwinSigaction is the kernel usigactiont. The handler union is 8 bytes on
// every Darwin arch; 0 is SIG_DFL.
type darwinSigaction struct {
	handler [8]byte
	mask    uint32
	flags   int32
}

func setStopDFL() error {
	var sa darwinSigaction
	_, _, errno := unix.RawSyscall(unix.SYS_SIGACTION, uintptr(syscall.SIGTSTP), uintptr(unsafe.Pointer(&sa)), 0)
	if errno != 0 {
		return errno
	}
	return nil
}

func unblockTSTP() error {
	var set uint32 = 1 << (uint32(syscall.SIGTSTP) - 1)
	// SIG_UNBLOCK is 2 on Darwin.
	_, _, errno := unix.RawSyscall(unix.SYS_SIGPROCMASK, 2, uintptr(unsafe.Pointer(&set)), 0)
	if errno != 0 {
		return errno
	}
	return nil
}
