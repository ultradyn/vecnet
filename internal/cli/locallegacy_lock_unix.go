//go:build unix

package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

// Pin each parent below HOME and the exact persistent source lock inode. Never
// unlink the lock: all cooperating migrations must serialize on the same inode.
func lockLegacySourceWith(parent context.Context, home, path string) (func(), error) {
	held, err := acquireLegacySourceWith(parent, home, path)
	if err != nil {
		return nil, err
	}
	return held.close, nil
}

type localLegacySourceLock struct {
	dir  *os.File
	dirs []*os.File
	file *os.File
	name string
	once sync.Once
}

func (h *localLegacySourceLock) close() {
	h.once.Do(func() {
		_ = unix.Flock(int(h.file.Fd()), unix.LOCK_UN)
		_ = h.file.Close()
		for _, d := range h.dirs {
			_ = d.Close()
		}
	})
}

func acquireLegacySourceWith(parent context.Context, home, path string) (*localLegacySourceLock, error) {
	if !filepath.IsAbs(home) || !filepath.IsAbs(path) || filepath.Clean(home) != home || filepath.Clean(path) != path {
		return nil, fmt.Errorf("unsafe legacy source lock path: require clean absolute HOME and source")
	}
	resolved, err := filepath.EvalSymlinks(home)
	if err != nil || resolved != home {
		return nil, fmt.Errorf("unsafe legacy source HOME: symlinked or unavailable ancestor")
	}
	rel, err := filepath.Rel(home, path)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return nil, fmt.Errorf("legacy source lock must be inside HOME")
	}
	ctx, cancel := context.WithTimeout(parent, 2*time.Second)
	defer cancel()
	dir, err := localRegistryHome(home, false)
	if err != nil {
		return nil, fmt.Errorf("unsafe legacy source HOME: %w", err)
	}
	dirs := []*os.File{dir}
	success := false
	defer func() {
		if !success {
			for _, d := range dirs {
				d.Close()
			}
		}
	}()
	components := strings.Split(rel, string(filepath.Separator))
	name := components[len(components)-1] + ".lock"
	if !localRegistryName(name) {
		return nil, fmt.Errorf("unsafe legacy source lock name")
	}
	for _, component := range components[:len(components)-1] {
		fd, e := unix.Openat(int(dir.Fd()), component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if e != nil {
			return nil, fmt.Errorf("cannot safely open legacy source parent: %w", e)
		}
		dir = os.NewFile(uintptr(fd), filepath.Join(dir.Name(), component))
		dirs = append(dirs, dir)
		if e := localRegistryCheck(dir, true, false); e != nil {
			return nil, fmt.Errorf("unsafe legacy source parent: %w", e)
		}
	}
	f, err := localRegistryFile(dir, name, unix.O_RDWR|unix.O_CREAT)
	if err != nil {
		return nil, fmt.Errorf("unsafe legacy source lock: %w", err)
	}
	defer func() {
		if !success {
			f.Close()
		}
	}()
	for {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("legacy source lock wait: %w", err)
		}
		err = unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			break
		}
		if !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EAGAIN) && !errors.Is(err, unix.EINTR) {
			return nil, err
		}
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, fmt.Errorf("legacy source lock wait: %w", ctx.Err())
		case <-timer.C:
		}
	}
	held := &localLegacySourceLock{dir: dir, dirs: dirs, file: f, name: name}
	if err := held.check(); err != nil {
		return nil, err
	}
	success = true
	return held, nil
}

// Check both the locked inode and the absolute parent names before archival.
// All actual source operations remain relative to the retained descriptor.
func (h *localLegacySourceLock) check() error {
	f, dir, dirs, name := h.file, h.dir, h.dirs, h.name
	if err := localRegistryCheck(f, false, true); err != nil {
		return fmt.Errorf("legacy source lock changed: %w", err)
	}
	var opened, named unix.Stat_t
	if err := unix.Fstat(int(f.Fd()), &opened); err != nil {
		return err
	}
	if err := unix.Fstatat(int(dir.Fd()), name, &named, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return err
	}
	if opened.Dev != named.Dev || opened.Ino != named.Ino {
		return fmt.Errorf("legacy source lock inode changed while waiting")
	}
	for _, d := range dirs {
		if err := localRegistryCheck(d, true, false); err != nil {
			return err
		}
		pinned, err := d.Stat()
		if err != nil {
			return err
		}
		named, err := os.Lstat(d.Name())
		if err != nil {
			return err
		}
		if named.Mode()&os.ModeSymlink != 0 || !os.SameFile(pinned, named) {
			return fmt.Errorf("legacy source parent changed while waiting")
		}
	}
	return nil
}
