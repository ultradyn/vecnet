//go:build !windows

package main

import "syscall"

// reexec replaces this process with exe. It returns only on failure.
func reexec(exe string, argv, env []string) error {
	return syscall.Exec(exe, argv, env)
}
