package cli

import (
	"bytes"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"

	"github.com/XertroV/vecnet/internal/platform"
)

var (
	launcherFixtureNodeDir    string // created by OnceValues; cleaned by TestMain
	launcherFixtureSharedNode = sync.OnceValues(func() (string, error) {
		base, err := filepath.EvalSymlinks(os.TempDir())
		if err != nil {
			return "", err
		}
		launcherFixtureNodeDir, err = platform.MkdirTemp(base, "vecnet-launcher-node-private copy-")
		if err != nil {
			return "", err
		}
		return copyLauncherFixtureNode(launcherFixtureNodeDir)
	})
)

type launcherFixtureNodeMode bool

const (
	sharedLauncherNode launcherFixtureNodeMode = false
	freshLauncherNode  launcherFixtureNodeMode = true
)

// Share one private, single-link Node image across cases that only execute
// or inspect it. This avoids new executable identities without bypassing
// the real install's trust checks or --version invocation. Mutators instead
// stage a fresh image into their own home, including a distinct parent ACL.
func launcherFixtureNode(home string, mode launcherFixtureNodeMode) (string, error) {
	if mode == sharedLauncherNode {
		return launcherFixtureSharedNode()
	}
	return copyLauncherFixtureNode(filepath.Join(home, "private node"))
}

func copyLauncherFixtureNode(dir string) (string, error) {
	source, err := exec.LookPath(platform.Exe("node"))
	if err != nil {
		return "", err
	}
	// Snapshot genuine Node bytes so admission is independent of the machine's
	// package-manager ACLs. Never hardlink either of these images.
	if err := platform.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	input, err := os.Open(source)
	if err != nil {
		return "", err
	}
	defer input.Close()
	path := filepath.Join(dir, platform.Exe("node"))
	output, err := platform.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o700)
	if err != nil {
		return "", err
	}
	_, err = io.Copy(output, input)
	return path, errors.Join(err, output.Close())
}

func TestLauncherFixtureNodeIsolation(t *testing.T) {
	source, err := exec.LookPath(platform.Exe("node"))
	if err != nil {
		t.Skipf("native Node required for the fixture contract: %v", err)
	}
	identity := func(path string) (os.FileInfo, []byte) {
		t.Helper()
		file, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		defer file.Close()
		// On Windows only the open-handle stat captures the file ID now.
		info, err := file.Stat()
		if err != nil {
			t.Fatal(err)
		}
		prefix := make([]byte, 16)
		if _, err := io.ReadFull(file, prefix); err != nil {
			t.Fatal(err)
		}
		return info, prefix
	}
	get := func(home string, mode launcherFixtureNodeMode) string {
		t.Helper()
		path, err := launcherFixtureNode(home, mode)
		if err != nil {
			t.Fatal(err)
		}
		return path
	}
	private := func(path string) {
		t.Helper()
		if err := platform.CheckPath(filepath.Dir(path), true, true); err != nil {
			t.Fatal(err)
		}
		if err := platform.PrivateFile(path); err != nil {
			t.Fatal(err)
		}
	}
	sourceID, prefix := identity(source)
	firstHome := t.TempDir()
	first := get(firstHome, sharedLauncherNode)
	firstID, got := identity(first)
	if !bytes.Equal(got, prefix) || firstID.Size() != sourceID.Size() || os.SameFile(firstID, sourceID) {
		t.Fatal("shared Node is not an independent copy of the source image")
	}
	private(first)
	secondHome := t.TempDir()
	second := get(secondHome, sharedLauncherNode)
	if err := os.RemoveAll(firstHome); err != nil {
		t.Fatal(err)
	}
	// Cleaning the first consumer's home must not remove the shared image.
	after, _ := identity(first)
	secondID, _ := identity(second)
	if !os.SameFile(firstID, after) || !os.SameFile(firstID, secondID) {
		t.Fatal("read-only launcher fixtures did not reuse the same Node image")
	}
	fresh := get(secondHome, freshLauncherNode)
	freshID, _ := identity(fresh)
	if os.SameFile(freshID, firstID) || os.SameFile(freshID, sourceID) {
		t.Fatal("mutable Node aliases the shared or source image")
	}
	private(fresh)
	if err := os.WriteFile(fresh, []byte("test-local mutation"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(secondHome); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(fresh); !os.IsNotExist(err) {
		t.Errorf("mutable Node cleanup: want absence, got %v", err)
	}
	for _, unchanged := range []struct {
		path string
		info os.FileInfo
	}{{source, sourceID}, {first, firstID}} {
		info, got := identity(unchanged.path)
		if !os.SameFile(info, unchanged.info) || info.Size() != unchanged.info.Size() || !bytes.Equal(got, prefix) {
			t.Errorf("mutable fixture changed %s", unchanged.path)
		}
	}
	private(first)
}
