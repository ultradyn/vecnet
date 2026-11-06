//go:build solaris || illumos

package tui

import (
	"syscall"
	"unsafe"
)

//go:cgo_import_dynamic libc_sigaction sigaction "libc.so"
//go:cgo_import_dynamic libc_sigprocmask sigprocmask "libc.so"

//go:linkname libcSigaction libc_sigaction
//go:linkname libcSigprocmask libc_sigprocmask

var (
	libcSigaction   uintptr
	libcSigprocmask uintptr
)

//go:linkname rawSysvicall6 syscall.rawSysvicall6
func rawSysvicall6(trap, nargs, a1, a2, a3, a4, a5, a6 uintptr) (r1, r2 uintptr, err syscall.Errno)

// solarisSigaction matches the libc sigaction struct the runtime uses.
// A zero handler is SIG_DFL.
type solarisSigaction struct {
	flags   int32
	_       [4]byte
	handler [8]byte
	mask    [4]uint32
}

func setStopDFL() error {
	var sa solarisSigaction
	_, _, errno := rawSysvicall6(uintptr(unsafe.Pointer(&libcSigaction)), 3, uintptr(syscall.SIGTSTP), uintptr(unsafe.Pointer(&sa)), 0, 0, 0, 0)
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
	_, _, errno := rawSysvicall6(uintptr(unsafe.Pointer(&libcSigprocmask)), 3, 2, uintptr(unsafe.Pointer(&set)), 0, 0, 0, 0)
	if errno != 0 {
		return errno
	}
	return nil
}
