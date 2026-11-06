//go:build !unix

package tui

func suspendDefault()     {}
func restoreStopHandler() {}
func unblockTSTP() error  { return nil }
