//go:build unix

package cli

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

var localRegistryEUID = os.Geteuid

func localRegistryCheck(f *os.File, directory, private bool) error {
	info, err := f.Stat()
	if err != nil {
		return err
	}
	var st unix.Stat_t
	if err := unix.Fstat(int(f.Fd()), &st); err != nil {
		return err
	}
	if int(st.Uid) != localRegistryEUID() {
		return fmt.Errorf("unsafe local inventory: %s is owned by another user", f.Name())
	}
	if directory {
		if !info.IsDir() || info.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 || info.Mode().Perm()&0022 != 0 || private && info.Mode().Perm() != 0700 {
			return fmt.Errorf("unsafe local inventory directory %s", f.Name())
		}
	} else if !info.Mode().IsRegular() || info.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 || info.Mode().Perm() != 0600 || st.Nlink != 1 {
		return fmt.Errorf("unsafe local inventory file %s: require a single-link, owner-only regular file (0600)", f.Name())
	}
	return nil
}

// Only HOME may have absent ancestors. Once opened, all state components are
// traversed relative to pinned descriptors, with no symlinks followed.
func localRegistryHome(home string, create bool) (*os.File, error) {
	fd, err := unix.Open(home, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if errors.Is(err, unix.ENOENT) && create {
		parent, err := localRegistryHome(filepath.Dir(home), true)
		if err != nil {
			return nil, err
		}
		defer parent.Close()
		if err := unix.Mkdirat(int(parent.Fd()), filepath.Base(home), 0700); err != nil && !errors.Is(err, unix.EEXIST) {
			return nil, err
		}
		if err := parent.Sync(); err != nil {
			return nil, err
		}
		fd, err = unix.Openat(int(parent.Fd()), filepath.Base(home), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), home)
	if err := localRegistryCheck(f, true, false); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}
func localRegistryOpen(home string, create bool) (*os.File, error) {
	dir, err := localRegistryHome(home, create)
	if err != nil {
		return nil, err
	}
	for _, name := range []string{".local", "state", "vecnet", "projects"} {
		if create {
			err := unix.Mkdirat(int(dir.Fd()), name, 0700)
			if err != nil && !errors.Is(err, unix.EEXIST) {
				dir.Close()
				return nil, err
			}
			if err == nil {
				if err := dir.Sync(); err != nil {
					dir.Close()
					return nil, err
				}
			}
		}
		fd, err := unix.Openat(int(dir.Fd()), name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		nextName := filepath.Join(dir.Name(), name)
		dir.Close()
		if err != nil {
			return nil, err
		}
		dir = os.NewFile(uintptr(fd), nextName)
		if err := localRegistryCheck(dir, true, name == "projects"); err != nil {
			dir.Close()
			return nil, err
		}
	}
	return dir, nil
}
func localRegistryFile(dir *os.File, name string, flags int) (*os.File, error) {
	if !localRegistryName(name) {
		return nil, fmt.Errorf("invalid local state file name")
	}
	fd, err := unix.Openat(int(dir.Fd()), name, flags|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0600)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), filepath.Join(dir.Name(), name))
	if err := localRegistryCheck(f, false, true); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}
func localRegistryLock(ctx context.Context, dir *os.File) (func(), error) {
	return localRegistryNamedLock(ctx, dir, "inventory.lock")
}
func localRegistryNamedLock(ctx context.Context, dir *os.File, name string) (func(), error) {
	if !localRegistryName(name) {
		return nil, fmt.Errorf("invalid local state lock name")
	}
	f, err := localRegistryFile(dir, name, unix.O_RDWR|unix.O_CREAT)
	if err != nil {
		return nil, err
	}
	for {
		if err := ctx.Err(); err != nil {
			f.Close()
			return nil, err
		}
		err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			return func() { unix.Flock(int(f.Fd()), unix.LOCK_UN); f.Close() }, nil
		}
		if !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EAGAIN) && !errors.Is(err, unix.EINTR) {
			f.Close()
			return nil, err
		}
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			f.Close()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}
func localRegistryAtomic(dir *os.File, target string, raw []byte) error {
	if !localRegistryName(target) {
		return fmt.Errorf("invalid local state file name")
	}
	existing, err := localRegistryFile(dir, target, unix.O_RDONLY)
	if err == nil {
		existing.Close()
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return err
	}
	name := ".tmp-" + hex.EncodeToString(nonce[:])
	f, err := localRegistryFile(dir, name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL)
	if err != nil {
		return err
	}
	defer unix.Unlinkat(int(dir.Fd()), name, 0)
	if _, err = f.Write(raw); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err := unix.Renameat(int(dir.Fd()), name, int(dir.Fd()), target); err != nil {
		return err
	}
	return dir.Sync()
}

func localRegistryName(name string) bool {
	return name != "" && name != "." && name != ".." && len(name) <= 128 && !strings.ContainsAny(name, "/\\\x00\r\n") && !strings.ContainsFunc(name, func(c rune) bool { return c < 32 || c > 126 })
}

func localRegistryArtifact(path string) (*os.File, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), path), nil
}
