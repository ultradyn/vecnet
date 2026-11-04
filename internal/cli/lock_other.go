//go:build !unix

package cli

import (
	"context"
	"errors"
	"os"
	"time"
)

// Without flock, an exclusive file represents the lock. A crashed owner's
// file expires after ten seconds; an unlock never removes a replacement.
func lockPath(ctx context.Context, path string) (func(), error) {
	for {
		if ctx.Err() != nil {
			return nil, errStateBusy
		}
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
		if err == nil {
			info, statErr := f.Stat()
			if statErr != nil {
				f.Close()
				os.Remove(path)
				return nil, statErr
			}
			return func() {
				f.Close()
				if current, err := os.Stat(path); err == nil && os.SameFile(info, current) {
					os.Remove(path)
				}
			}, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, err
		}
		if info, err := os.Stat(path); err == nil && time.Since(info.ModTime()) > 10*time.Second {
			if err := os.Remove(path); err == nil || errors.Is(err, os.ErrNotExist) {
				continue
			}
		}
		if !sleepCtx(ctx, 10*time.Millisecond) {
			return nil, errStateBusy
		}
	}
}
