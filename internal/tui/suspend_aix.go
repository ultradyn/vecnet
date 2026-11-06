//go:build aix

package tui

import (
	"syscall"
	"unsafe"
)

//go:cgo_import_dynamic libc_sigaction sigaction "libc.a/shr_64.o"
//go:cgo_import_dynamic libc_sigprocmask sigprocmask "libc.a/shr_64.o"

//go:linkname libcSigaction libc_sigaction
//go:linkname libcSigprocmask libc_sigprocmask

var (
	libcSigaction   uintptr
	libcSigprocmask uintptr
)

//go:linkname aixSyscall6 syscall.syscall6
func aixSyscall6(trap, nargs, a1, a2, a3, a4, a5, a6 uintptr) (r1, r2 uintptr, err syscall.Errno)

type aixSigaction struct {
	handler uintptr
	mask    [4]uint64
	flags   int32
	_       [4]byte
}

func setStopDFL() error {
	var sa aixSigaction
	_, _, errno := aixSyscall6(uintptr(unsafe.Pointer(&libcSigaction)), 3, uintptr(syscall.SIGTSTP), uintptr(unsafe.Pointer(&sa)), 0, 0, 0, 0)
	if errno != 0 {
		return errno
	}
	return nil
}

func unblockTSTP() error {
	var set [4]uint64
	bit := uint32(syscall.SIGTSTP) - 1
	set[bit/64] = 1 << (bit % 64)
	// SIG_UNBLOCK is 2.
	_, _, errno := aixSyscall6(uintptr(unsafe.Pointer(&libcSigprocmask)), 3, 2, uintptr(unsafe.Pointer(&set)), 0, 0, 0, 0)
	if errno != 0 {
		return errno
	}
	return nil
}
