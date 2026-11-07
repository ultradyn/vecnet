//go:build !linux

package cli_test

import (
	"os"
	"testing"
)

// prefsPTY is the non-Linux stand-in. The Linux pty ioctls do not exist on
// macOS or BSD, so the interactive confirmation test skips there. The rest
// of the preferences suite still builds and runs.
func prefsPTY(t *testing.T) (master, stdin, stdout *os.File) {
	t.Helper()
	t.Skip("interactive prefs confirmation requires a Linux pseudoterminal")
	return nil, nil, nil
}
