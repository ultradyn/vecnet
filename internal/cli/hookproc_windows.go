//go:build windows

package cli

import (
	"errors"
	"fmt"

	"golang.org/x/sys/windows"
)

func procAlive(pid int) (alive, known bool) {
	if pid <= 0 {
		return false, true
	}
	h, e := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if errors.Is(e, windows.ERROR_INVALID_PARAMETER) {
		return false, true
	}
	if errors.Is(e, windows.ERROR_ACCESS_DENIED) {
		return true, true
	}
	if e != nil {
		return false, false
	}
	defer windows.CloseHandle(h)
	var code uint32
	if e = windows.GetExitCodeProcess(h, &code); e != nil {
		return false, false
	}
	return code == 259, true
}
func procStart(pid int) string {
	h, e := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if e != nil {
		return ""
	}
	defer windows.CloseHandle(h)
	var creation, exit, kernel, user windows.Filetime
	if e = windows.GetProcessTimes(h, &creation, &exit, &kernel, &user); e != nil {
		return ""
	}
	return fmt.Sprintf("%d", uint64(creation.HighDateTime)<<32|uint64(creation.LowDateTime))
}
