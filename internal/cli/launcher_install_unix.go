//go:build unix

package cli

import (
	"github.com/XertroV/vecnet/internal/platform"
	"os"
	"syscall"
)

func launcherFileIdentity(info os.FileInfo) (uid int, links uint64, ok bool) {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, 0, false
	}
	return int(st.Uid), uint64(st.Nlink), true
}

func launcherTryLock(file *os.File) (bool, error) { return platform.TryLock(file) }

func launcherRunningExecutable() (os.FileInfo, error) { return os.Stat("/proc/self/exe") }

func launcherOpenExecutable(root *launcherRoot, name string) (*os.File, error) {
	return root.Open(name)
}
