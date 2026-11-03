//go:build darwin || dragonfly || freebsd || netbsd || openbsd

package cli

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

func init() { hookPlatformPipe = hookBSDPipe }

func hookBSDPipe(f *os.File) (*hookPipeWriter, func(), error) {
	// Dup owns the descriptor lifetime. Preserve and restore the shared
	// descriptor flags; the hook has exclusive use of this stdout pipe.
	fd, err := unix.Dup(int(f.Fd()))
	if err != nil {
		return nil, nil, err
	}
	flags, err := unix.FcntlInt(uintptr(fd), unix.F_GETFL, 0)
	if err != nil {
		_ = unix.Close(fd)
		return nil, nil, err
	}
	if err := unix.SetNonblock(fd, true); err != nil {
		_ = unix.Close(fd)
		return nil, nil, err
	}
	kq, err := unix.Kqueue()
	if err != nil {
		_, _ = unix.FcntlInt(uintptr(fd), unix.F_SETFL, flags)
		_ = unix.Close(fd)
		return nil, nil, err
	}
	closeOutput := func() {
		_ = unix.Close(kq)
		_, _ = unix.FcntlInt(uintptr(fd), unix.F_SETFL, flags)
		_ = unix.Close(fd)
	}
	var change unix.Kevent_t
	unix.SetKevent(&change, fd, unix.EVFILT_WRITE, unix.EV_ADD|unix.EV_ENABLE)
	// EVFILT_WRITE's data is the number of bytes of free pipe buffer space.
	// Level-triggered queries with a zero timeout never block the hook.
	if _, err := unix.Kevent(kq, []unix.Kevent_t{change}, nil, &unix.Timespec{}); err != nil {
		closeOutput()
		return nil, nil, err
	}
	limit, _ := hookPipeLimit("darwin")
	w := &hookPipeWriter{limit: limit, write: func(data []byte) (int, error) { return unix.Write(fd, data) }}
	w.available = func() (int, error) {
		var events [1]unix.Kevent_t
		n, err := unix.Kevent(kq, nil, events[:], &unix.Timespec{})
		if err != nil {
			return 0, err
		}
		if n == 0 {
			return 0, nil
		}
		ev := events[0]
		if ev.Flags&(unix.EV_ERROR|unix.EV_EOF) != 0 {
			return 0, fmt.Errorf("hook pipe unavailable")
		}
		return int(ev.Data), nil
	}
	return w, closeOutput, nil
}
