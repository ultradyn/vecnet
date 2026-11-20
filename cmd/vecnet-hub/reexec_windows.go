//go:build windows

package main

import "errors"

// reexec is unsupported on Windows, where one-click update is never offered.
func reexec(string, []string, []string) error {
	return errors.New("restart in place is not supported on Windows")
}
