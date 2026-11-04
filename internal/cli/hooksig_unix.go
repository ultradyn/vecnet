//go:build unix

package cli

import (
	"os"
	"os/signal"
	"sync"
	"syscall"
)

var hookSIGPIPEOnce sync.Once

// hookCatchSIGPIPE makes a write to a closed stdout or stderr fail with
// EPIPE instead of killing the hook: Go raises SIGPIPE for fds 1 and 2
// unless the program is notified of it. The hook is the process's final
// act, so the notification is kept rather than stopped, which would let a
// write still blocked at the deadline die after cmdHook returns.
func hookCatchSIGPIPE() {
	hookSIGPIPEOnce.Do(func() {
		signal.Notify(make(chan os.Signal, 1), syscall.SIGPIPE)
	})
}
