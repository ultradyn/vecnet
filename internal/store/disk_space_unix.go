//go:build !windows

package store

import "syscall"

func availableDiskBytes(path string) (int64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, err
	}
	if st.Bavail <= 0 || st.Bsize <= 0 {
		return 0, nil
	}
	// Bavail, unlike Bfree, excludes blocks reserved for privileged users.
	return diskSpaceBytes(uint64(st.Bavail), uint64(st.Bsize)), nil
}
