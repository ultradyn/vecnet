//go:build windows

package selfupdate

// writable is false on Windows, where one-click update is not offered.
func writable(string) bool { return false }
