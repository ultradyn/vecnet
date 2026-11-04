//go:build unix

package cli

import (
	"errors"
	"os"
	"strconv"
	"strings"
	"syscall"
)

// procAlive reports whether pid exists; known is false when the OS will
// not say. EPERM means it exists under another user.
func procAlive(pid int) (alive, known bool) {
	err := syscall.Kill(pid, 0)
	if err == nil || errors.Is(err, syscall.EPERM) {
		return true, true
	}
	if errors.Is(err, syscall.ESRCH) {
		return false, true
	}
	return false, false
}

// procStart is pid's start time in clock ticks since boot (Linux
// /proc/<pid>/stat field 22), or "" where /proc does not report it.
func procStart(pid int) string {
	raw, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return ""
	}
	// The command name (field 2) may hold spaces and ')': split after the last.
	i := strings.LastIndexByte(string(raw), ')')
	if i < 0 {
		return ""
	}
	fields := strings.Fields(string(raw[i+1:]))
	// fields[0] is field 3 (state), so field 22 is fields[19].
	if len(fields) < 20 {
		return ""
	}
	return fields[19]
}
