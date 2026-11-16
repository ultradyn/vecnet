//go:build windows

package cli

import (
	"context"
	"os"

	"github.com/XertroV/vecnet/internal/platform"
)

// Locks are owned by a live kernel file handle, never by a timestamp. Crashes
// release them immediately; long-running owners cannot lose them to a timeout.
func lockPath(ctx context.Context, path string) (func(), error) {
	f, e := platform.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if e != nil {
		return nil, e
	}
	release, e := platform.Lock(ctx, f)
	if e != nil {
		f.Close()
		if ctx.Err() != nil {
			return nil, errStateBusy
		}
		return nil, e
	}
	return func() { release(); f.Close() }, nil
}
