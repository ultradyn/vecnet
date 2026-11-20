//go:build !windows

package selfupdate

import (
	"os/exec"
	"syscall"
	"time"
)

// StopGrace is how long a cancelled command's process group has, after
// SIGTERM, to finish (npm to stop between file operations) before SIGKILL.
const StopGrace = 5 * time.Second

// ownProcessGroup starts cmd in a new process group. Cancellation sends the
// whole group SIGTERM, then SIGKILL after StopGrace if the command has not
// exited, so npm's children (node, its workers) cannot outlive a cancelled
// or timed-out install, and npm gets a chance to stop cleanly first.
func ownProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		pgid := cmd.Process.Pid
		err := syscall.Kill(-pgid, syscall.SIGTERM)
		time.AfterFunc(StopGrace, func() {
			// The group outlives its leader only if npm left children
			// behind; Kill on an empty group just fails with ESRCH.
			_ = syscall.Kill(-pgid, syscall.SIGKILL)
		})
		return err
	}
	// After Cancel, Wait returns at most this long later, killing the
	// leader (os.ErrProcessDone otherwise) if it still runs.
	cmd.WaitDelay = StopGrace + time.Second
}
