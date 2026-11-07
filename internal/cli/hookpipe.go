package cli

import (
	"context"
	"errors"
	"io"
	"syscall"
	"time"
)

// errHookOutputTooLarge refuses an output above the capped write size.
var errHookOutputTooLarge = errors.New("hook output exceeds the atomic pipe write size")

// hookGatedWrite publishes data in one write once room reports at least
// len(data) free bytes, so a pipe whose reader has stalled receives either
// the complete output or nothing. room and write act on a pipe with no
// other writer: free space then only grows until this write. On expiry it
// returns ctx's error without having written anything.
func hookGatedWrite(ctx context.Context, data []byte, room func() (int, error), write func([]byte) (int, error)) error {
	if len(data) > hookPipeAtomicBytes {
		return errHookOutputTooLarge
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		free, err := room()
		if err != nil {
			return err
		}
		if free >= len(data) {
			n, err := write(data)
			switch {
			case err == nil && n == len(data):
				return nil
			case err == nil || n > 0:
				return io.ErrShortWrite
			case !errors.Is(err, syscall.EAGAIN) && !errors.Is(err, syscall.EINTR):
				return err
			}
		}
		t := time.NewTimer(5 * time.Millisecond)
		select {
		case <-ctx.Done():
			t.Stop()
			return ctx.Err()
		case <-t.C:
		}
	}
}
