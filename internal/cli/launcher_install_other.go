//go:build !unix && !windows

package cli

import "os"

// Installation is Linux-only; never silently accept unavailable owner checks.
func launcherFileIdentity(os.FileInfo) (int, uint64, bool) { return 0, 0, false }
func launcherTryLock(*os.File) (bool, error) {
	return false, usageErr("launcher install requires Linux")
}

func launcherRunningExecutable() (os.FileInfo, error) {
	return nil, usageErr("managed launcher repair unsupported")
}

func launcherOpenExecutable(root *launcherRoot, name string) (*os.File, error) {
	return root.Open(name)
}
