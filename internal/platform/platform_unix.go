//go:build unix

package platform

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"
)

func OpenFile(path string, flags int, mode os.FileMode) (*os.File, error) {
	return os.OpenFile(path, flags|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, mode)
}
func OpenDir(path string) (*os.File, error) { return os.Open(path) }
func OpenAt(dir *os.File, name string, flags int, mode os.FileMode) (*os.File, error) {
	return OpenFile(filepath.Join(dir.Name(), name), flags, mode)
}
func CheckFile(f *os.File, directory, private bool) error {
	st, err := f.Stat()
	if err != nil {
		return err
	}
	native, ok := st.Sys().(*syscall.Stat_t)
	if !ok || int(native.Uid) != os.Geteuid() {
		return fmt.Errorf("private path owned by another user")
	}
	if directory && !st.IsDir() || !directory && !st.Mode().IsRegular() {
		return fmt.Errorf("not the required file type")
	}
	mask := os.FileMode(0022)
	if private {
		mask = 0077
	}
	if st.Mode().Perm()&mask != 0 {
		return fmt.Errorf("unsafe file permissions")
	}
	return nil
}
func CheckPath(path string, directory, private bool) error {
	f, e := OpenFile(path, os.O_RDONLY, 0)
	if e != nil {
		return e
	}
	defer f.Close()
	return CheckFile(f, directory, private)
}
func MkdirAll(path string, mode os.FileMode) error { return os.MkdirAll(path, mode) }
func TryLock(f *os.File) (bool, error) {
	e := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if errors.Is(e, syscall.EWOULDBLOCK) || errors.Is(e, syscall.EAGAIN) || errors.Is(e, syscall.EINTR) {
		return false, nil
	}
	return e == nil, e
}
func Unlock(f *os.File) error { return syscall.Flock(int(f.Fd()), syscall.LOCK_UN) }
func Lock(ctx context.Context, f *os.File) (func(), error) {
	for {
		if e := ctx.Err(); e != nil {
			return nil, e
		}
		ok, e := TryLock(f)
		if e != nil {
			return nil, e
		}
		if ok {
			return func() { _ = Unlock(f) }, nil
		}
		t := time.NewTimer(10 * time.Millisecond)
		select {
		case <-ctx.Done():
			t.Stop()
			return nil, ctx.Err()
		case <-t.C:
		}
	}
}
func SyncDir(f *os.File) error      { return f.Sync() }
func Replace(old, new string) error { return os.Rename(old, new) }

func Background(cmd *exec.Cmd) {}

func Canonical(path string) (string, error) { return filepath.EvalSymlinks(path) }

func IsReparse(path string) (bool, error) {
	st, e := os.Lstat(path)
	if e != nil {
		return false, e
	}
	return st.Mode()&os.ModeSymlink != 0, nil
}
