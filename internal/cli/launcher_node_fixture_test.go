package cli

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/platform"
)

var (
	launcherFixtureNodeDir    string // created by OnceValues; cleaned by TestMain
	launcherFixtureSharedNode = sync.OnceValues(func() (string, error) {
		if node, configured := os.LookupEnv("CI_LAUNCHER_NODE_FIXTURE"); configured {
			return borrowLauncherFixtureNode(node)
		}
		base, err := filepath.EvalSymlinks(os.TempDir())
		if err != nil {
			return "", err
		}
		launcherFixtureNodeDir, err = platform.MkdirTemp(base, "vecnet-launcher-node-private copy-")
		if err != nil {
			return "", err
		}
		node, err := copyLauncherFixtureNode(launcherFixtureNodeDir)
		if err != nil {
			return "", err
		}
		if err := prepareLauncherFixtureNode(node); err != nil {
			return "", err
		}
		return node, nil
	})
)

func prepareLauncherFixtureNode(node string) error {
	// One real execution on the just-created, unmodified private image.
	// The enclosing test/owner still owns this fixture work; this is not
	// a retry or an extension of any install/repair probe's deadline.
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	start := time.Now()
	version, err := launcherNodeOutput(ctx, node)
	elapsed := time.Since(start)
	if err != nil || ctx.Err() != nil || !launcherNodeVersion(string(version)) {
		var exitErr *exec.ExitError
		var stderr []byte
		if errors.As(err, &exitErr) {
			stderr = exitErr.Stderr[:min(len(exitErr.Stderr), 1024)]
		}
		return fmt.Errorf("prepare private Node --version after %s: command=%v context=%v output=%.80q stderr=%q", elapsed, err, ctx.Err(), version, stderr)
	}
	fmt.Fprintf(os.Stderr, "launcher Node fixture: prepared private image in %s, version=%.80q\n", elapsed, version)
	return nil
}

// The job owns this image. Borrowers must not set launcherFixtureNodeDir: their
// TestMain cleanup owns only images created by that process.
func borrowLauncherFixtureNode(node string) (string, error) {
	if !filepath.IsAbs(node) || filepath.Clean(node) != node || strings.ContainsAny(node, "\r\n") || filepath.Base(node) != platform.Exe("node") {
		return "", errors.New("invalid shared launcher Node fixture path")
	}
	if err := platform.CheckPath(filepath.Dir(node), true, true); err != nil {
		return "", err
	}
	if err := platform.PrivateFile(node); err != nil {
		return "", err
	}
	release, err := launcherPinExecutable(node)
	if err != nil {
		return "", err
	}
	defer release()
	source, err := exec.LookPath(platform.Exe("node"))
	if err != nil {
		return "", err
	}
	identity := func(path string) (os.FileInfo, [sha256.Size]byte, error) {
		f, err := os.Open(path)
		if err != nil {
			return nil, [sha256.Size]byte{}, err
		}
		defer f.Close()
		info, err := f.Stat()
		if err != nil {
			return nil, [sha256.Size]byte{}, err
		}
		h := sha256.New()
		_, err = io.Copy(h, f)
		return info, [sha256.Size]byte(h.Sum(nil)), err
	}
	sourceID, sourceHash, err := identity(source)
	if err != nil {
		return "", err
	}
	privateID, privateHash, err := identity(node)
	if err != nil {
		return "", err
	}
	if os.SameFile(sourceID, privateID) || sourceHash != privateHash {
		return "", errors.New("shared launcher Node fixture must be an independent copy of genuine Node")
	}
	return node, nil
}

type launcherFixtureNodeMode uint8

const (
	preparedLauncherNode launcherFixtureNodeMode = iota
	coldLauncherNode
	preparedMutableLauncherNode
)

// Read-only callers share one prepared, private, single-link image per job in
// Windows CI, or per process locally. The real install still validates and
// executes it. Cold admission receives a never-executed independent copy.
// Mutation cases prepare their own independent copy before the real install;
// they must never borrow or mutate the shared image.
func launcherFixtureNode(home string, mode launcherFixtureNodeMode) (string, error) {
	switch mode {
	case preparedLauncherNode:
		return launcherFixtureSharedNode()
	case coldLauncherNode, preparedMutableLauncherNode:
		node, err := copyLauncherFixtureNode(filepath.Join(home, "private node"))
		if err != nil {
			return "", err
		}
		if mode == preparedMutableLauncherNode {
			if err := prepareLauncherFixtureNode(node); err != nil {
				return "", err
			}
		}
		return node, nil
	default:
		return "", fmt.Errorf("unknown launcher Node fixture mode %d", mode)
	}
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
	_, err = copyFixtureImage(output, input)
	return path, errors.Join(err, output.Close())
}

func TestLauncherFixtureNodeIsolation(t *testing.T) {
	source, err := exec.LookPath(platform.Exe("node"))
	if err != nil {
		t.Skipf("native Node required for the fixture contract: %v", err)
	}
	identity := func(path string) (os.FileInfo, [sha256.Size]byte) {
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
		h := sha256.New()
		if _, err := io.Copy(h, file); err != nil {
			t.Fatal(err)
		}
		return info, [sha256.Size]byte(h.Sum(nil))
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
	sourceID, sourceDigest := identity(source)
	firstHome := t.TempDir()
	first := get(firstHome, preparedLauncherNode)
	if shared, configured := os.LookupEnv("CI_LAUNCHER_NODE_FIXTURE"); configured && first != shared {
		t.Fatal("prepared fixture did not borrow the configured job image")
	}
	firstID, got := identity(first)
	if got != sourceDigest || firstID.Size() != sourceID.Size() || os.SameFile(firstID, sourceID) {
		t.Fatal("shared Node is not an independent copy of the source image")
	}
	private(first)
	secondHome := t.TempDir()
	second := get(secondHome, preparedLauncherNode)
	if err := os.RemoveAll(firstHome); err != nil {
		t.Fatal(err)
	}
	// Cleaning the first consumer's home must not remove the shared image.
	after, _ := identity(first)
	secondID, _ := identity(second)
	if !os.SameFile(firstID, after) || !os.SameFile(firstID, secondID) {
		t.Fatal("read-only launcher fixtures did not reuse the same Node image")
	}
	for _, mode := range []launcherFixtureNodeMode{coldLauncherNode, preparedMutableLauncherNode} {
		mutableHome := filepath.Join(secondHome, fmt.Sprint(mode))
		fresh := get(mutableHome, mode)
		freshID, freshDigest := identity(fresh)
		if freshDigest != sourceDigest {
			t.Fatal("independent Node is not a byte-for-byte copy of the source image")
		}
		if os.SameFile(freshID, firstID) || os.SameFile(freshID, sourceID) {
			t.Fatal("mutable Node aliases the shared or source image")
		}
		private(fresh)
		if err := os.WriteFile(fresh, []byte("test-local mutation"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.RemoveAll(mutableHome); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(fresh); !os.IsNotExist(err) {
			t.Errorf("mutable Node cleanup: want absence, got %v", err)
		}
	}
	for _, unchanged := range []struct {
		path string
		info os.FileInfo
	}{{source, sourceID}, {first, firstID}} {
		info, got := identity(unchanged.path)
		if !os.SameFile(info, unchanged.info) || info.Size() != unchanged.info.Size() || got != sourceDigest {
			t.Errorf("mutable fixture changed %s", unchanged.path)
		}
	}
	private(first)
}

// An invalid executable is a negative control, never a successful Node stand-in.
// Cold copies must defer admission; mutable security setup must really probe it.
func TestLauncherFixtureNodeMutableRequiresPreparation(t *testing.T) {
	parent := launcherFixturePhysicalTemp(t)
	sourceDir := filepath.Join(parent, "invalid source")
	if err := platform.MkdirAll(sourceDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sourceDir, platform.Exe("node")), []byte("invalid executable image"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", sourceDir)
	if _, err := launcherFixtureNode(filepath.Join(parent, "cold"), coldLauncherNode); err != nil {
		t.Fatalf("cold staging unexpectedly probed the image: %v", err)
	}
	if _, err := launcherFixtureNode(filepath.Join(parent, "mutable"), preparedMutableLauncherNode); err == nil || !strings.Contains(err.Error(), "prepare private Node --version") {
		t.Fatalf("mutable staging must reject an invalid image during preparation: %v", err)
	}
}

// This root only borrows the shared image. Lifecycle children use it to prove
// that borrowing neither prepares a replacement nor takes cleanup ownership.
func TestLauncherFixtureNodeReadOnlyBorrow(t *testing.T) {
	node, err := launcherFixtureSharedNode()
	if err != nil {
		t.Fatal(err)
	}
	if shared, configured := os.LookupEnv("CI_LAUNCHER_NODE_FIXTURE"); configured && node != shared {
		t.Fatal("read-only borrower did not use the configured job image")
	}
	if _, err := borrowLauncherFixtureNode(node); err != nil {
		t.Fatal("read-only borrower image is not a private independent copy of genuine Node:", err)
	}
}
