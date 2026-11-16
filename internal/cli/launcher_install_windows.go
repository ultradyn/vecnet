//go:build windows

package cli

import (
	"github.com/XertroV/vecnet/internal/platform"
	"golang.org/x/sys/windows"
	"os"
	"path/filepath"
)

// The info-only Unix check is deliberately unavailable on Windows: use the
// opened-handle SID/DACL checks, never fabricated uid/mode evidence.
func launcherFileIdentity(os.FileInfo) (int, uint64, bool) { return 0, 0, false }
func launcherTryLock(file *os.File) (bool, error)          { return platform.TryLock(file) }
func launcherRunningExecutable() (os.FileInfo, error) {
	var path [32768]uint16
	size := uint32(len(path))
	if err := windows.QueryFullProcessImageName(windows.CurrentProcess(), 0, &path[0], &size); err != nil {
		return nil, err
	}
	return os.Stat(windows.UTF16ToString(path[:size]))
}

func launcherOpenExecutable(root *launcherRoot, name string) (*os.File, error) {
	path := filepath.Join(root.Name(), name)
	value, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	handle, err := windows.CreateFile(value, windows.GENERIC_READ|windows.READ_CONTROL, windows.FILE_SHARE_READ, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return nil, &os.PathError{Op: "pin launcher executable", Path: path, Err: err}
	}
	return os.NewFile(uintptr(handle), path), nil
}
