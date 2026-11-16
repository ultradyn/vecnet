//go:build !unix && !windows

package cli

import (
	"context"
	"fmt"
	"os"
)

// Fail closed where private ownership checks and descriptor-relative locking
// cannot be provided. Ordinary-use callers can report/defer this capability.
func localRegistryOpen(home string, create bool) (*os.File, error) {
	return nil, fmt.Errorf("private local inventory is unsupported on this platform")
}
func localRegistryFile(dir *os.File, name string, flags int) (*os.File, error) {
	return nil, fmt.Errorf("private local inventory is unsupported on this platform")
}
func localRegistryLock(ctx context.Context, dir *os.File) (func(), error) {
	return nil, fmt.Errorf("private local inventory is unsupported on this platform")
}
func localRegistryAtomic(dir *os.File, name string, raw []byte) error {
	return fmt.Errorf("private local inventory is unsupported on this platform")
}

func localRegistryNamedLock(ctx context.Context, dir *os.File, name string) (func(), error) {
	return nil, fmt.Errorf("private local inventory is unsupported on this platform")
}

func localRegistryArtifact(path string) (*os.File, error) { return os.Open(path) }
