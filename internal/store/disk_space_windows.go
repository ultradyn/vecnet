package store

import (
	"path/filepath"

	"golang.org/x/sys/windows"
)

func availableDiskBytes(path string) (int64, error) {
	path, err := filepath.Abs(path)
	if err != nil {
		return 0, err
	}
	// UNC share roots require the trailing separator; directories on local
	// volumes accept it too. Abs also handles relative fixture directories.
	if path[len(path)-1] != filepath.Separator {
		path += string(filepath.Separator)
	}
	dir, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, err
	}
	var available uint64
	// Use the caller's available space, not the volume-wide free space: disk
	// quotas can make the latter larger than the space SQLite can actually use.
	if err := windows.GetDiskFreeSpaceEx(dir, &available, nil, nil); err != nil {
		return 0, err
	}
	return diskSpaceBytes(available, 1), nil
}
