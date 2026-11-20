//go:build linux

package cli

// The owner fence of `claude deliver` (task 7): the exact kernel peer
// identity of the one connected socket. SO_PEERCRED supplies the pid/uid the
// kernel attributes the connection to; the /proc start identity read right
// after the connect rules out a reused pid. Both must match the registered
// owner before any byte can leave. Every failure is a validation refusal
// (exit 2): definitely unwritten.

import (
	"net"
	"strconv"

	"golang.org/x/sys/unix"
)

// claudeDeliverVerifyPeer checks the connected peer against the expected
// owner on the same connection that is about to be written.
func claudeDeliverVerifyPeer(conn *net.UnixConn, expectPID, expectUID, expectStart string) error {
	var peerPID, peerUID int
	raw, controlErr := conn.SyscallConn()
	if controlErr == nil {
		controlErr = raw.Control(func(fd uintptr) {
			ucred, err := unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
			if err == nil {
				peerPID, peerUID = int(ucred.Pid), int(ucred.Uid)
			}
		})
	}
	if controlErr != nil || peerPID == 0 {
		return usageErr("claude deliver: cannot read the peer credentials")
	}
	if peerPID <= 1 || strconv.Itoa(peerPID) != expectPID {
		return usageErr("claude deliver: the connected process is not the registered owner")
	}
	if strconv.Itoa(peerUID) != expectUID {
		return usageErr("claude deliver: the connected process runs as another OS user")
	}
	if start := procStart(peerPID); start == "" || start != expectStart {
		return usageErr("claude deliver: the connected process start identity does not match the registered owner (pid reuse?)")
	}
	return nil
}

// claudeDeliverSocketValid accepts only a unix socket owned by the expected
// user with owner-only permissions: exactly what the registered owner bound.
// A path that does not exist is left to the connect phase, which reports the
// refusal as exit 3 exactly as the task-6 contract pins.
func claudeDeliverSocketValid(path, expectUID string) error {
	var st unix.Stat_t
	if err := unix.Lstat(path, &st); err != nil {
		if err == unix.ENOENT {
			return nil
		}
		return usageErr("claude deliver: the messaging endpoint is unavailable")
	}
	if st.Mode&unix.S_IFMT != unix.S_IFSOCK {
		return usageErr("claude deliver: the endpoint must be a socket (symlinks and other types refuse)")
	}
	if strconv.Itoa(int(st.Uid)) != expectUID || st.Mode&0o022 != 0 {
		return usageErr("claude deliver: the messaging socket must be owner-only and owned by the registered user")
	}
	return nil
}
