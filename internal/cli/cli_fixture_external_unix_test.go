//go:build !windows

package cli_test

import (
	"os"
	"testing"
)

func cliFixtureChmod(path string, mode os.FileMode) error { return os.Chmod(path, mode) }
func cliFixtureSymlink(t *testing.T, target, link string) error {
	t.Helper()
	return os.Symlink(target, link)
}

func cliFixturePermissions(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	return info.Mode().String(), nil
}
