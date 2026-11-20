//go:build linux

package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// agyProcFS is the process tree the agy ancestor walk reads: /proc in
// production, a fake tree in tests.
var agyProcFS = "/proc"

// agyProcNames identifies the agy process by comm value or exe basename.
// agy 1.5.13 reports comm "agy" and exe basename "agy" (observed with the
// hooks probe of scripts/agy-canary-probe.py; the hook launch chain is
// agy → sh -c → vecnet). Tests add stand-in names.
var agyProcNames = []string{"agy"}

// agyHookProc is the Antigravity process that ran this hook. agy exports no
// PID variable to hooks (evidence H3) but launches them through sh -c, so
// the agy process is an ancestor of the hook: walk up from the hook's
// parent, at most three ancestors, and take the first whose /proc identity
// names agy. /bin/sh being bash execs the hook in place, making agy the
// parent itself; under dash the hook keeps its sh parent and agy is the
// grandparent. A fixed depth would misidentify the user's shell as agy when
// /bin/sh is bash. Unknown stays the zero value, which never matches, and
// /proc/<pid>/environ is never read.
func agyHookProc() hookProc {
	return agyProcAt(procStatParent(agyProcFS, os.Getpid()))
}

// agyProcAt walks up from pid, at most three ancestors (pid, its parent and
// grandparent), returning the first agy process; the zero hookProc when
// none matches.
func agyProcAt(pid int) hookProc {
	for i := 0; i < 3 && pid > 1; i++ {
		if agyProcNamed(procComm(agyProcFS, pid), procExeBase(agyProcFS, pid)) {
			host, _ := os.Hostname()
			return hookProc{PID: pid, Start: procStart(pid), Host: host}
		}
		pid = procStatParent(agyProcFS, pid)
	}
	return hookProc{}
}

// agyProcNamed reports whether a comm value or exe basename identifies agy.
func agyProcNamed(comm, exeBase string) bool {
	for _, name := range agyProcNames {
		if name != "" && (comm == name || exeBase == name) {
			return true
		}
	}
	return false
}

// procStatParent reads pid's parent pid from <root>/<pid>/stat field 4.
func procStatParent(root string, pid int) int {
	if pid <= 0 {
		return 0
	}
	raw, err := os.ReadFile(root + "/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return 0
	}
	// The command name (field 2) may hold spaces and ')': split after the last.
	i := bytes.LastIndexByte(raw, ')')
	if i < 0 {
		return 0
	}
	fields := strings.Fields(string(raw[i+1:]))
	// fields[0] is field 3 (state), so field 4 (ppid) is fields[1].
	if len(fields) < 2 {
		return 0
	}
	ppid, err := strconv.Atoi(fields[1])
	if err != nil || ppid <= 0 {
		return 0
	}
	return ppid
}

// procComm reads pid's comm value (Linux caps it at 15 bytes).
func procComm(root string, pid int) string {
	raw, err := os.ReadFile(root + "/" + strconv.Itoa(pid) + "/comm")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(raw))
}

// procExeBase reads the basename of pid's exe symlink.
func procExeBase(root string, pid int) string {
	exe, err := os.Readlink(root + "/" + strconv.Itoa(pid) + "/exe")
	if err != nil {
		return ""
	}
	return filepath.Base(exe)
}
