//go:build !unix

package cli

// procAlive cannot tell outside Unix: a binding is never judged stale.
func procAlive(pid int) (alive, known bool) { return false, false }

// procStart is unknown outside Unix.
func procStart(pid int) string { return "" }
