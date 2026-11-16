//go:build !windows

package tui

import (
	"os"
	"testing"
)

func chatDanglingLink(t *testing.T, link, target string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
}
