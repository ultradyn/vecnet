//go:build darwin || dragonfly || freebsd || netbsd || openbsd

package cli

import (
	"context"
	"io"
	"os"
	"syscall"
)

// hookAtomicPipe returns an all-or-nothing writer when w is a native pipe.
// PIPE_BUF is 512 bytes here, below one maximal item, so atomicity cannot
// come from the write size as on Linux. Instead kqueue's EVFILT_WRITE
// reports the pipe's free space, and the output is written only once it
// fits whole: the hook is the pipe's only writer, so a write that fits
// completes at once and a stalled reader sees nothing rather than a
// truncated JSON prefix. Callers cap the output at hookPipeAtomicBytes,
// well inside the default pipe buffer.
func hookAtomicPipe(w io.Writer) (func(context.Context, []byte) error, bool) {
	f, ok := w.(*os.File)
	if !ok {
		return nil, false
	}
	info, err := f.Stat()
	if err != nil || info.Mode()&os.ModeNamedPipe == 0 {
		return nil, false
	}
	rc, err := f.SyscallConn()
	if err != nil {
		return nil, false
	}
	fd := -1
	if rc.Control(func(raw uintptr) { fd = int(raw) }) != nil || fd < 0 {
		return nil, false
	}
	return func(ctx context.Context, data []byte) error {
		kq, err := syscall.Kqueue()
		if err != nil {
			return err
		}
		defer syscall.Close(kq)
		var changes [1]syscall.Kevent_t
		syscall.SetKevent(&changes[0], fd, syscall.EVFILT_WRITE, syscall.EV_ADD|syscall.EV_ENABLE)
		registered := false
		room := func() (int, error) {
			var in []syscall.Kevent_t
			if !registered {
				in = changes[:]
			}
			var events [1]syscall.Kevent_t
			n, err := syscall.Kevent(kq, in, events[:], &syscall.Timespec{})
			if err != nil {
				return 0, err
			}
			registered = true
			if n == 0 {
				return 0, nil // below the filter's low-water mark
			}
			ev := events[0]
			switch {
			case int(ev.Flags)&syscall.EV_ERROR != 0:
				return 0, syscall.Errno(ev.Data)
			case int(ev.Flags)&syscall.EV_EOF != 0:
				return 0, syscall.EPIPE
			}
			return int(ev.Data), nil
		}
		write := func(data []byte) (int, error) {
			var n int
			var werr error
			if err := rc.Write(func(raw uintptr) bool {
				n, werr = syscall.Write(int(raw), data)
				return true // retry in hookGatedWrite, not through the poller
			}); err != nil {
				return 0, err
			}
			if n < 0 {
				n = 0
			}
			return n, werr
		}
		return hookGatedWrite(ctx, data, room, write)
	}, true
}
