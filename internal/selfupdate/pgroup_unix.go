//go:build !windows

package selfupdate

import (
	"os/exec"
	"syscall"
)

// ownProcessGroup starts cmd in a new process group and makes cancellation
// kill the whole group, so npm's children (node, its workers) cannot
// outlive a cancelled or timed-out install.
func ownProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}
