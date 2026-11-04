//go:build unix

package cli

import (
	"context"
	"errors"
	"os"
	"syscall"
	"time"
)

// lockPath keeps a stable inode for flock; removing a Unix lock file would
// allow a second process to lock a different inode at the same path.
func lockPath(ctx context.Context, path string) (func(), error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	for {
		if ctx.Err() != nil {
			f.Close()
			return nil, errStateBusy
		}
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return func() { syscall.Flock(int(f.Fd()), syscall.LOCK_UN); f.Close() }, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) && !errors.Is(err, syscall.EINTR) {
			f.Close()
			return nil, err
		}
		if !sleepCtx(ctx, 10*time.Millisecond) {
			f.Close()
			return nil, errStateBusy
		}
	}
}
