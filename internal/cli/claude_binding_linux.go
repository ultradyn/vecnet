//go:build linux

package cli

// Linux (the initial qualified baseline, Claude Code 2.1.322) supplies the
// live process evidence for a claimed Claude root: PID liveness plus start
// identity from /proc, boot identity, OS-user ownership, the canonical
// config root with its session registry row, and the private messaging
// socket's type and ownership. Every check is read-only: nothing here
// connects to, or writes through, the socket.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

// claudeBootID is this host's current boot identity.
func claudeBootID() string {
	raw, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(raw))
}

// validateClaudeRoot proves the claimed root is anchored to this host:
// same boot, a live process with the claimed start identity owned by the
// claimed user, its canonical config root, the session registry row for
// that exact pid, and (when supplied) a socket of the right type and
// ownership. CLAUDE_PID alone is never proof of any of this.
func validateClaudeRoot(root claudeRoot) error {
	if err := claudeRootShape(root); err != nil {
		return err
	}
	if root.UID != os.Geteuid() {
		return usageErr("claude root claims another OS user")
	}
	boot := claudeBootID()
	if boot == "" || root.BootID != boot {
		return usageErr("claude root boot identity is not this host's")
	}
	alive, known := procAlive(root.PID)
	if !known || !alive {
		return usageErr("claimed claude process is not live")
	}
	start := procStart(root.PID)
	if start == "" || start != root.ProcessStart {
		return usageErr("claimed claude process start identity mismatch (pid reuse?)")
	}
	if err := claudeProcUser(root.PID, root.UID); err != nil {
		return err
	}
	if err := claudeConfigRootValid(root.ConfigRoot, root.UID); err != nil {
		return err
	}
	if err := claudeSessionRecordValid(root); err != nil {
		return err
	}
	if root.SocketPath != "" {
		if err := claudeSocketValid(root.SocketPath, root.UID); err != nil {
			return err
		}
	}
	return nil
}

func claudeProcUser(pid, uid int) error {
	var st unix.Stat_t
	if err := unix.Stat("/proc/"+strconv.Itoa(pid), &st); err != nil {
		return usageErr("cannot inspect the claimed claude process")
	}
	if int(st.Uid) != uid {
		return usageErr("claimed claude process belongs to another OS user")
	}
	return nil
}

// claudeConfigRootValid rejects symlinks, foreign owners and group- or
// world-writable roots: the config root must be a private directory.
func claudeConfigRootValid(path string, uid int) error {
	var st unix.Stat_t
	if err := unix.Lstat(path, &st); err != nil {
		return usageErr("claude config root is unavailable")
	}
	if st.Mode&unix.S_IFMT != unix.S_IFDIR {
		return usageErr("claude config root must be a directory (no symlinks)")
	}
	if int(st.Uid) != uid || st.Mode&0o022 != 0 {
		return usageErr("claude config root must be owner-only")
	}
	return nil
}

// claudeSessionRecordValid ties the invocation to its root: Claude Code's
// own session registry under the config root must hold a private row for
// the exact pid reporting this conversation (and endpoint, when it names
// one). This is the "invocation targets its root" evidence.
func claudeSessionRecordValid(root claudeRoot) error {
	path := filepath.Join(root.ConfigRoot, "sessions", strconv.Itoa(root.PID)+".json")
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return usageErr("claude session registry has no row for this process under its config root")
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil || st.Mode&unix.S_IFMT != unix.S_IFREG || int(st.Uid) != root.UID || st.Mode&0o022 != 0 {
		return usageErr("claude session registry row must be an owner-only regular file")
	}
	raw, err := io.ReadAll(io.LimitReader(f, 64<<10+1))
	if err != nil || len(raw) > 64<<10 {
		return usageErr("claude session registry row is unreadable")
	}
	var record struct {
		SessionID           string `json:"sessionId"`
		MessagingSocketPath string `json:"messagingSocketPath"`
	}
	if err := json.Unmarshal(raw, &record); err != nil || record.SessionID != root.SessionID {
		return usageErr("claude session registry does not report this session for this process")
	}
	if record.MessagingSocketPath != "" && root.SocketPath != "" && record.MessagingSocketPath != root.SocketPath {
		return usageErr("claude session registry reports a different messaging endpoint")
	}
	return nil
}

// claudeSocketValid accepts only a unix socket owned by the claimed user
// with owner-only permissions. A symlink or any other file type refuses:
// the endpoint must be exactly what Claude bound.
func claudeSocketValid(path string, uid int) error {
	var st unix.Stat_t
	if err := unix.Lstat(path, &st); err != nil {
		return usageErr("claude messaging socket is unavailable")
	}
	if st.Mode&unix.S_IFMT != unix.S_IFSOCK {
		return usageErr("claude messaging endpoint must be a socket (symlinks and other types refuse)")
	}
	if int(st.Uid) != uid || st.Mode&0o022 != 0 {
		return usageErr("claude messaging socket must be owner-only")
	}
	return nil
}

// claudeRootAncestry requires the registering process to be a descendant
// of the claimed Claude process: a hook or in-session registration is a
// child of the session that owns it. CLAUDE_PID alone proves nothing.
func claudeRootAncestry(root claudeRoot) error {
	if root.PID <= 1 {
		return usageErr("claude root requires a real ancestor process")
	}
	pid := os.Getppid() // the claimed process may not be the registrant itself
	for hops := 0; pid > 1; hops++ {
		if hops >= 1024 {
			break
		}
		if pid == root.PID {
			return nil
		}
		next, err := claudePPid(pid)
		if err != nil {
			break
		}
		pid = next
	}
	return usageErr("registration is not a child of the claimed claude process")
}

func claudePPid(pid int) (int, error) {
	raw, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/status")
	if err != nil {
		return 0, err
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if v, ok := strings.CutPrefix(line, "PPid:"); ok {
			n, err := strconv.Atoi(strings.TrimSpace(v))
			if err != nil || n < 0 {
				return 0, fmt.Errorf("invalid PPid")
			}
			return n, nil
		}
	}
	return 0, errors.New("no PPid")
}

// claudeProcessFenced reports whether the previous owner is provably gone:
// a different boot, a dead pid, or a pid now belonging to a later process
// (start identity). Unknown means not fenced.
func claudeProcessFenced(pid int, start, boot string) bool {
	if pid <= 1 || start == "" || boot == "" {
		return false
	}
	if current := claudeBootID(); current != "" && current != boot {
		return true
	}
	alive, known := procAlive(pid)
	if known && !alive {
		return true
	}
	if now := procStart(pid); now != "" && now != start {
		return true
	}
	return false
}
