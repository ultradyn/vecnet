//go:build windows

package selfupdate

import "os/exec"

// ownProcessGroup does nothing on Windows, where one-click update is never
// offered.
func ownProcessGroup(*exec.Cmd) {}
