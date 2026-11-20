//go:build linux

package cli

// Linux supplies the kernel-attested executable evidence: /proc/<pid>/exe is
// opened once and hashed through that descriptor, never by re-resolving a
// path. The start identity is checked before the open and after the hash so a
// stale or reused pid is never attested, and the opened inode is compared
// against the pid's current executable so an exec during the hash is caught
// even though an exec keeps the start time.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

// claudeBuildOpenExe opens the pid's current executable read-only. It is a
// seam so tests can pose an unreadable /proc.
var claudeBuildOpenExe = func(pid int) (*os.File, error) {
	path := "/proc/" + strconv.Itoa(pid) + "/exe"
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), path), nil
}

// claudeBuildStatIdentity is the decimal
// "<dev>:<ino>:<size>:<mtime_ns>:<ctime_ns>" of one opened executable.
func claudeBuildStatIdentity(st *unix.Stat_t) string {
	return fmt.Sprintf("%d:%d:%d:%d:%d", st.Dev, st.Ino, st.Size,
		claudeTimespecNS(st.Mtim), claudeTimespecNS(st.Ctim))
}

func claudeTimespecNS(t unix.Timespec) int64 { return int64(t.Sec)*1e9 + int64(t.Nsec) }

// claudeIdentityDevIno parses the dev:ino prefix of a recorded identity.
func claudeIdentityDevIno(identity string) (uint64, uint64, bool) {
	parts := strings.Split(identity, ":")
	if len(parts) != 5 {
		return 0, 0, false
	}
	dev, err := strconv.ParseUint(parts[0], 10, 64)
	if err != nil {
		return 0, 0, false
	}
	ino, err := strconv.ParseUint(parts[1], 10, 64)
	if err != nil {
		return 0, 0, false
	}
	return dev, ino, true
}

// claudeExeIdentityLive reports whether pid's current executable is still the
// one a recorded identity came from. Only the dev:ino prefix compares:
// unlinking a file updates its ctime and an auto-update unlinks the version
// file, so the full tuple would wrongly disqualify a running session, while a
// running image cannot be modified in place (ETXTBSY) and its inode cannot be
// reused while mapped. An exec keeps the pid and start time but changes
// dev:ino, which this comparison catches.
func claudeExeIdentityLive(pid int, identity string) bool {
	dev, ino, ok := claudeIdentityDevIno(identity)
	if !ok || pid <= 1 {
		return false
	}
	var st unix.Stat_t
	if err := unix.Stat("/proc/"+strconv.Itoa(pid)+"/exe", &st); err != nil {
		return false
	}
	return st.Dev == dev && st.Ino == ino
}

// claudeBuildExeStillHeld verifies the pid's current executable is still the
// opened one: an exec during the observation changes the inode while keeping
// the start time.
func claudeBuildExeStillHeld(pid int, st *unix.Stat_t) bool {
	var now unix.Stat_t
	if err := unix.Stat("/proc/"+strconv.Itoa(pid)+"/exe", &now); err != nil {
		return false
	}
	return now.Dev == st.Dev && now.Ino == st.Ino
}

// observeClaudeBuild attests the pid's executable: open /proc/<pid>/exe,
// hash it through the opened descriptor (or reuse the digest cached for that
// descriptor's fstat identity) and resolve the pinned table label. The error
// is non-nil only when the start identity stops matching before the open or
// after the hash (stale or reused pid); every other failure is evidence with
// an empty Build and one fixed reason, so a hook never fails on it.
// CLAUDE_CODE_VERSION is never read.
func (a *app) observeClaudeBuild(ctx context.Context, pid int, start string) (claudeBuildEvidence, error) {
	if now := procStart(pid); now == "" || now != start {
		return claudeBuildEvidence{}, usageErr("claude build observation pid no longer matches its start identity (pid reuse?)")
	}
	exe, err := claudeBuildOpenExe(pid)
	if err != nil {
		return claudeBuildEvidence{Reason: claudeBuildReasonUnreadable}, nil
	}
	defer exe.Close()
	var st unix.Stat_t
	if err := unix.Fstat(int(exe.Fd()), &st); err != nil || st.Mode&unix.S_IFMT != unix.S_IFREG {
		return claudeBuildEvidence{Reason: claudeBuildReasonUnreadable}, nil
	}
	identity := claudeBuildStatIdentity(&st)

	sha, cached := a.claudeExeDigestLookup(identity)
	if !cached {
		hashed, timedOut := a.claudeBuildHashExe(ctx, exe)
		if hashed == "" {
			reason := claudeBuildReasonUnreadable
			if timedOut {
				reason = claudeBuildReasonTimeout
			}
			return claudeBuildEvidence{Reason: reason}, nil
		}
		sha = hashed
		// A hash is only evidence when the descriptor still names the same
		// image: the repeated fstat must reproduce the identity, and the
		// pid's current executable must still be this inode. Otherwise no
		// stable read of this pid's executable existed, so nothing is cached.
		var after unix.Stat_t
		if err := unix.Fstat(int(exe.Fd()), &after); err != nil || claudeBuildStatIdentity(&after) != identity {
			return claudeBuildEvidence{Reason: claudeBuildReasonUnreadable}, nil
		}
		if now := procStart(pid); now == "" || now != start {
			return claudeBuildEvidence{}, usageErr("claude build observation pid no longer matches its start identity (pid reuse?)")
		}
		if !claudeBuildExeStillHeld(pid, &st) {
			return claudeBuildEvidence{Reason: claudeBuildReasonUnreadable}, nil
		}
		// Only a complete, stable hash is ever cached; a store failure never
		// fails the observation itself.
		_ = a.claudeExeDigestStore(ctx, identity, sha, time.Now().UTC())
	} else {
		if now := procStart(pid); now == "" || now != start {
			return claudeBuildEvidence{}, usageErr("claude build observation pid no longer matches its start identity (pid reuse?)")
		}
		if !claudeBuildExeStillHeld(pid, &st) {
			return claudeBuildEvidence{Reason: claudeBuildReasonUnreadable}, nil
		}
	}

	// CLAUDE_CODE_EXECPATH is a hint that never qualifies a build: it may
	// only contradict the kernel-attested executable. A hint naming a
	// missing file (an auto-update unlinked it) is stale and ignored; any
	// other unstatable hint fails closed. A hint naming a file replaced in
	// place at the same path names a different inode than the running image,
	// which also fails closed.
	if hint := strings.TrimSpace(a.getenv("CLAUDE_CODE_EXECPATH")); hint != "" {
		var hst unix.Stat_t
		if err := unix.Stat(hint, &hst); err == nil {
			if hst.Dev != st.Dev || hst.Ino != st.Ino {
				return claudeBuildEvidence{Reason: claudeBuildReasonExecPath}, nil
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return claudeBuildEvidence{Reason: claudeBuildReasonExecPath}, nil
		}
	}
	build, qualified := claudeQualifiedBuildFor(sha)
	if !qualified {
		return claudeBuildEvidence{SHA256: sha, Identity: identity, Reason: claudeBuildReasonUnqualified}, nil
	}
	return claudeBuildEvidence{SHA256: sha, Identity: identity, Build: build.Label}, nil
}

// claudeBuildHashExe hashes the opened executable under the observation
// budget and reports whether the budget is what gave out. The bound is
// enforced here, not by the hash function: a blocked or slow hash gives up
// at the deadline.
func (a *app) claudeBuildHashExe(ctx context.Context, exe *os.File) (string, bool) {
	hctx, cancel := context.WithTimeout(ctx, claudeBuildHashBudget)
	defer cancel()
	type outcome struct {
		sha string
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		sha, err := claudeBuildHash(hctx, exe)
		done <- outcome{sha, err}
	}()
	select {
	case res := <-done:
		if res.err == nil {
			return res.sha, false
		}
		if errors.Is(res.err, context.DeadlineExceeded) || errors.Is(res.err, context.Canceled) {
			return "", true
		}
		return "", false // reading the opened executable failed
	case <-hctx.Done():
		return "", true
	}
}
