//go:build !windows

package selfupdate

import "golang.org/x/sys/unix"

// writable reports whether this process may create and remove entries in dir.
func writable(dir string) bool {
	return unix.Access(dir, unix.W_OK|unix.X_OK) == nil
}
