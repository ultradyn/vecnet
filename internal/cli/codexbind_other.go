//go:build !unix

package cli

import "os"

// readCodexBindings has no owner or mode bits to check off unix.
func readCodexBindings(path string) ([]byte, error) { return os.ReadFile(path) }
