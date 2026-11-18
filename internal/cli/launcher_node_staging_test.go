package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/XertroV/vecnet/internal/platform"
)

// Invoked explicitly by CI, before m.Run and outside test discovery. The fixed
// child directory belongs to the native bundle's job, never a packet home.
func launcherFixtureNodeUtility(args []string, out io.Writer) (err error) {
	if len(args) != 2 || (args[0] != "prepare" && args[0] != "cleanup") {
		return errors.New("expected launcher Node fixture prepare|cleanup DIRECTORY")
	}
	dir := args[1]
	if !filepath.IsAbs(dir) || filepath.Clean(dir) != dir || strings.ContainsAny(dir, "\r\n") || filepath.Base(dir) != "launcher-node-fixture" {
		return errors.New("invalid launcher Node fixture directory")
	}
	// Reject aliases before creating or recursively removing anything. Production
	// admission still pins the image and its ancestors before every real execution.
	parent, err := platform.Canonical(filepath.Dir(dir))
	if err != nil || parent != filepath.Dir(dir) {
		return fmt.Errorf("launcher Node fixture parent must be physical: %s: %v", dir, err)
	}
	_, statErr := os.Lstat(dir)
	if args[0] == "cleanup" {
		if errors.Is(statErr, os.ErrNotExist) {
			return nil
		}
		if statErr != nil {
			return statErr
		}
		reparse, err := platform.IsReparse(dir)
		if err != nil {
			return err
		}
		if reparse {
			return errors.New("refuse cleanup of aliased launcher Node fixture directory")
		}
		if err := platform.CheckPath(dir, true, true); err != nil {
			return err
		}
		return os.RemoveAll(dir)
	}
	if !errors.Is(statErr, os.ErrNotExist) {
		return fmt.Errorf("launcher Node fixture directory must be new: %s: %v", dir, statErr)
	}
	if err := platform.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, os.RemoveAll(dir))
		}
	}()
	node, err := copyLauncherFixtureNode(dir)
	if err != nil {
		return err
	}
	if _, err = borrowLauncherFixtureNode(node); err != nil {
		return err
	}
	if err = prepareLauncherFixtureNode(node); err != nil {
		return err
	}
	// No path is published until real preparation succeeds. A broken publication
	// also removes the just-created image; the always cleanup step covers kills.
	_, err = fmt.Fprintln(out, node)
	return err
}
