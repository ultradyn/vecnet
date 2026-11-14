//go:build !linux

package localruntime

import (
	"context"
	"errors"
	"os"
)

func owned(st os.FileInfo) error { return nil }
func lock(ctx context.Context, path string) (func(), error) {
	return nil, errors.New("managed delivery activation requires Linux user systemd")
}
