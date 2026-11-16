//go:build windows

package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/XertroV/vecnet/internal/platform"
)

func lockLegacySourceWith(ctx context.Context, home, path string) (func(), error) {
	rel, e := filepath.Rel(home, path)
	if e != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return nil, usageErr("legacy source escapes HOME")
	}
	if e = platform.CheckPath(filepath.Dir(path), true, false); e != nil {
		return nil, e
	}
	f, e := platform.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0600)
	if e != nil {
		return nil, e
	}
	release, e := platform.Lock(ctx, f)
	if e != nil {
		f.Close()
		return nil, e
	}
	return func() { release(); f.Close() }, nil
}
