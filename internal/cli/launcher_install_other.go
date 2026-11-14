//go:build !unix

package cli

import "os"

// Installation is Linux-only; never silently accept unavailable owner checks.
func launcherFileIdentity(os.FileInfo) (int, uint64, bool) { return 0, 0, false }
func launcherTryLock(*os.File) (bool, error) {
	return false, usageErr("launcher install requires Linux")
}
