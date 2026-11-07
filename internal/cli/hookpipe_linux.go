//go:build linux

package cli

import (
	"context"
	"errors"
	"io"
	"os"
	"syscall"
	"time"
)

// hookAtomicPipe returns an all-or-nothing writer when w is a native pipe.
// A blocking write larger than PIPE_BUF can emit a prefix and then stall on
// a reader that stopped; the hook would exit at its deadline and leave a
// truncated JSON object. A nonblocking write of at most PIPE_BUF bytes to a
// pipe instead either writes everything or fails with EAGAIN, so the hook
// retries until the deadline and publishes nothing on expiry. Callers cap
// the encoded output at hookPipeAtomicBytes.
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
	return func(ctx context.Context, data []byte) error {
		if len(data) > hookPipeAtomicBytes {
			return errHookOutputTooLarge
		}
		for {
			if err := ctx.Err(); err != nil {
				return err
			}
			var n int
			var werr error
			if err := rc.Write(func(fd uintptr) bool {
				n, werr = hookWriteNonblock(int(fd), data)
				return true // retry here, not through the poller
			}); err != nil {
				return err
			}
			switch {
			case werr == nil && n == len(data):
				return nil
			case werr == nil:
				return io.ErrShortWrite // impossible for n <= PIPE_BUF
			case !errors.Is(werr, syscall.EAGAIN) && !errors.Is(werr, syscall.EINTR):
				return werr // EPIPE included; SIGPIPE is not fatal here
			}
			t := time.NewTimer(5 * time.Millisecond)
			select {
			case <-ctx.Done():
				t.Stop()
				return ctx.Err()
			case <-t.C:
			}
		}
	}, true
}

// hookWriteNonblock writes once with O_NONBLOCK set, restoring a blocking
// descriptor afterwards. (If the process exits between the two, only a
// writer sharing this pipe's write end could notice the flag.)
func hookWriteNonblock(fd int, data []byte) (int, error) {
	flags, _, errno := syscall.Syscall(syscall.SYS_FCNTL, uintptr(fd), syscall.F_GETFL, 0)
	if errno != 0 {
		return 0, errno
	}
	if flags&syscall.O_NONBLOCK == 0 {
		if err := syscall.SetNonblock(fd, true); err != nil {
			return 0, err
		}
		defer func() { _ = syscall.SetNonblock(fd, false) }()
	}
	n, err := syscall.Write(fd, data)
	if err != nil {
		return 0, err
	}
	return n, nil
}
