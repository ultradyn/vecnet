//go:build windows

package localruntime

import (
	"context"
	"errors"
	"os"

	"github.com/XertroV/vecnet/internal/platform"
)

// FileInfo has no trustworthy Windows SID or DACL. Never accept a mode-only
// ownership check: all Windows paths use descriptor checks in windows_files.go.
func owned(os.FileInfo) error {
	return errors.New("Windows runtime requires native descriptor ownership checks")
}
func lock(ctx context.Context, path string) (func(), error) {
	f, e := platform.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if e != nil {
		return nil, e
	}
	unlock, e := platform.Lock(ctx, f)
	if e != nil {
		f.Close()
		return nil, e
	}
	return func() { unlock(); _ = f.Close() }, nil
}
