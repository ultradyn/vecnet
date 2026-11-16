//go:build windows

package cli

import (
	"io"
	"os"

	"github.com/XertroV/vecnet/internal/platform"
)

func readCodexBindings(path string) ([]byte, error) {
	f, e := platform.OpenFile(path, os.O_RDONLY, 0)
	if e != nil {
		return nil, e
	}
	defer f.Close()
	raw, e := io.ReadAll(io.LimitReader(f, (1<<20)+1))
	if len(raw) > 1<<20 {
		return nil, unsafeCodexBindings(path, "exceeds 1 MiB")
	}
	return raw, e
}
