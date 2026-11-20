//go:build !linux

package cli

// procGrandparent is unknown outside Linux: an agy binding never matches a
// process there.
func procGrandparent() int { return 0 }
