//go:build !unix

package cli

// hookCatchSIGPIPE: there is no SIGPIPE outside Unix.
func hookCatchSIGPIPE() {}
