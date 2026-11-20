//go:build linux

package cli

import (
	"os"
	"strconv"
	"strings"
)

// procGrandparent is this process's parent's parent: agy launches hooks as
// agy → sh -c → vecnet, so the grandparent is the agy process itself. 0
// when /proc does not report the chain.
func procGrandparent() int {
	return procParent(procParent(os.Getpid()))
}

// procParent reads pid's parent pid from Linux /proc/<pid>/stat field 4.
func procParent(pid int) int {
	if pid <= 0 {
		return 0
	}
	raw, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return 0
	}
	// The command name (field 2) may hold spaces and ')': split after the last.
	i := strings.LastIndexByte(string(raw), ')')
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
