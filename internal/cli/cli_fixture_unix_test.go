//go:build !windows

package cli

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

func cliFixtureDenyDirectoryRead(t *testing.T, path string) {
	t.Helper()
	if err := os.Chmod(path, 0000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0700) })
}
