package localruntime

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// checkPath never follows symlinks below HOME, including existing ancestors of
// missing paths. Existing shared-readable parents are allowed; writable ones are not.
func checkPath(home, path string) error {
	rel, e := filepath.Rel(home, path)
	if e != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return errors.New("runtime path escapes HOME")
	}
	p := home
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		if part == "." {
			continue
		}
		p = filepath.Join(p, part)
		st, e := os.Lstat(p)
		if errors.Is(e, os.ErrNotExist) {
			return nil
		}
		if e != nil {
			return e
		}
		if !st.IsDir() || st.Mode()&os.ModeSymlink != 0 || st.Mode().Perm()&0022 != 0 {
			return fmt.Errorf("unsafe runtime directory %s", p)
		}
		if e = owned(st); e != nil {
			return e
		}
	}
	return nil
}
func mkdirSafe(home, path string, mode fs.FileMode) error {
	if e := checkPath(home, path); e != nil {
		return e
	}
	rel, e := filepath.Rel(home, path)
	if e != nil {
		return e
	}
	p := home
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		if part == "." {
			continue
		}
		p = filepath.Join(p, part)
		e = os.Mkdir(p, 0700)
		created := e == nil
		if e != nil && !errors.Is(e, os.ErrExist) {
			return e
		}
		if e = checkPath(home, p); e != nil {
			return e
		}
		if created {
			if e = syncDir(filepath.Dir(p)); e != nil {
				return e
			}
		}
	}
	if mode == 0700 {
		st, e := os.Stat(path)
		if e != nil {
			return e
		}
		if st.Mode().Perm()&0077 != 0 {
			return os.Chmod(path, 0700)
		}
	}
	return nil
}
func atomicWrite(path string, b []byte, mode fs.FileMode) error {
	if st, e := os.Lstat(path); e == nil {
		if !st.Mode().IsRegular() {
			return errors.New("unsafe runtime file")
		}
		if e = owned(st); e != nil {
			return e
		}
	} else if !errors.Is(e, os.ErrNotExist) {
		return e
	}
	f, e := os.CreateTemp(filepath.Dir(path), ".install-*")
	if e != nil {
		return e
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if e = f.Chmod(mode); e == nil {
		_, e = f.Write(b)
	}
	if e == nil {
		e = f.Sync()
	}
	closeErr := f.Close()
	if e != nil {
		return e
	}
	if closeErr != nil {
		return closeErr
	}
	if e = os.Rename(tmp, path); e != nil {
		return e
	}
	return syncDir(filepath.Dir(path))
}
func syncDir(path string) error {
	f, e := os.Open(path)
	if e != nil {
		return e
	}
	defer f.Close()
	return f.Sync()
}
func optionalManifest(home, name string) (*manifest, error) {
	m, e := readManifest(home, name)
	if errors.Is(e, os.ErrNotExist) {
		return nil, nil
	}
	if e != nil {
		return nil, e
	}
	return &m, nil
}
func readManifest(home, name string) (manifest, error) {
	var m manifest
	if e := checkPath(home, root(home)); e != nil {
		return m, e
	}
	p := filepath.Join(root(home), name)
	st, e := os.Lstat(p)
	if e != nil {
		return m, e
	}
	if !st.Mode().IsRegular() || st.Mode().Perm()&0077 != 0 {
		return m, errors.New("unsafe runtime manifest")
	}
	if e = owned(st); e != nil {
		return m, e
	}
	if st.Size() > 64*1024 {
		return m, errors.New("invalid runtime manifest size")
	}
	b, e := os.ReadFile(p)
	if e != nil {
		return m, e
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if e = dec.Decode(&m); e != nil {
		return m, errors.New("invalid runtime manifest JSON")
	}
	if dec.Decode(new(any)) != io.EOF {
		return m, errors.New("invalid runtime manifest trailing data")
	}
	hash := filepath.Base(m.Release)
	_, hexErr := hex.DecodeString(hash)
	if m.Schema != 1 || m.Version == "" || unsafeString(m.Version) || len(hash) != 64 || hexErr != nil || filepath.Dir(m.Release) != root(home) || m.Unit != unitPath(home) || !filepath.IsAbs(m.Node) || unsafeString(m.Node) || m.UnitHash != sum(makeUnit(home, m.Node, m.Release, true)) {
		return m, errors.New("incompatible or unsafe runtime manifest; setup pending")
	}
	return m, nil
}
func writeManifest(home, name string, m manifest) error {
	b, e := json.MarshalIndent(m, "", "  ")
	if e != nil {
		return e
	}
	return atomicWrite(filepath.Join(root(home), name), append(b, '\n'), 0600)
}
func stage(ctx context.Context, home, release string, files map[string][]byte, repairAllowed bool, beforeReplace func() error) (bool, error) {
	if e := ctx.Err(); e != nil {
		return false, e
	}
	if e := checkPath(home, release); e != nil {
		return false, e
	}
	damaged := false
	if _, e := os.Lstat(release); e == nil {
		if e = verifyRelease(ctx, release, files); e == nil {
			return false, nil
		}
		if !repairAllowed {
			return false, errors.New("unrecognized staged release is modified; refusing replacement")
		}
		if e = safeBackupTree(ctx, release); e != nil {
			return false, e
		}
		damaged = true
	} else if !errors.Is(e, os.ErrNotExist) {
		return false, e
	}
	tmp, e := os.MkdirTemp(root(home), ".release-*")
	if e != nil {
		return false, e
	}
	defer os.RemoveAll(tmp)
	dirs := map[string]bool{tmp: true}
	for path, b := range files {
		if e := ctx.Err(); e != nil {
			return false, e
		}
		target := filepath.Join(tmp, filepath.FromSlash(path))
		dir := filepath.Dir(target)
		if e = os.MkdirAll(dir, 0700); e != nil {
			return false, e
		}
		for p := dir; p != tmp; p = filepath.Dir(p) {
			dirs[p] = true
		}
		mode := fs.FileMode(0600)
		if path == "vecnet" {
			mode = 0700
		}
		f, e := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
		if e != nil {
			return false, e
		}
		_, e = f.Write(b)
		if e == nil {
			e = f.Sync()
		}
		closeErr := f.Close()
		if e != nil {
			return false, e
		}
		if closeErr != nil {
			return false, closeErr
		}
	}
	for dir := range dirs {
		if e = syncDir(dir); e != nil {
			return false, e
		}
	}
	// Persist retry intent before a same-version repair can replace live paths.
	if repairAllowed {
		if e = beforeReplace(); e != nil {
			return false, e
		}
	}
	if damaged {
		backupRoot := filepath.Join(root(home), "repair-backups")
		if e = mkdirSafe(home, backupRoot, 0700); e != nil {
			return false, e
		}
		backup, e := os.MkdirTemp(backupRoot, filepath.Base(release)+"-")
		if e != nil {
			return false, e
		}
		if e = os.Remove(backup); e != nil {
			return false, e
		}
		if e = os.Rename(release, backup); e != nil {
			_ = os.Remove(backup)
			return false, e
		}
		if e = syncDir(backupRoot); e != nil {
			return false, e
		}
		if e = syncDir(root(home)); e != nil {
			return false, e
		}
	}
	if e = os.Rename(tmp, release); e != nil {
		return false, e
	}
	return damaged || repairAllowed, syncDir(root(home))
}
func verifyRelease(ctx context.Context, release string, files map[string][]byte) error {
	seen := 0
	entries := 0
	e := filepath.WalkDir(release, func(path string, d fs.DirEntry, e error) error {
		if ce := ctx.Err(); ce != nil {
			return ce
		}
		if e != nil {
			return e
		}
		entries++
		if entries > len(files)*4+32 {
			return errors.New("too many runtime integrity entries; repair required")
		}
		st, e := d.Info()
		if e != nil {
			return e
		}
		if e = owned(st); e != nil {
			return e
		}
		if st.Mode().Perm()&0077 != 0 {
			return errors.New("release is not private")
		}
		if d.IsDir() {
			return nil
		}
		if !st.Mode().IsRegular() {
			return errors.New("unsafe staged runtime asset")
		}
		rel, e := filepath.Rel(release, path)
		if e != nil {
			return e
		}
		expected, ok := files[filepath.ToSlash(rel)]
		if !ok {
			return errors.New("unexpected staged runtime asset")
		}
		if st.Size() != int64(len(expected)) {
			return errors.New("modified staged runtime asset size; repair required")
		}
		b, e := os.ReadFile(path)
		if e != nil {
			return e
		}
		if !bytes.Equal(b, expected) {
			return errors.New("modified staged runtime asset; preserve state and restore the managed release before retrying")
		}
		if rel == "vecnet" && st.Mode().Perm()&0100 == 0 {
			return errors.New("private CLI is not executable")
		}
		seen++
		return nil
	})
	if e != nil {
		return e
	}
	if seen != len(files) {
		return errors.New("incomplete staged runtime release; setup pending")
	}
	return nil
}

// Backups preserve unknown entries without following symlinks or opening files.
// Foreign-owned trees and unbounded traversals remain explicit conflicts.
func safeBackupTree(ctx context.Context, path string) error {
	entries := 0
	return filepath.WalkDir(path, func(p string, d fs.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if e = ctx.Err(); e != nil {
			return e
		}
		entries++
		if entries > 100000 {
			return errors.New("managed release backup inspection limit exceeded")
		}
		st, e := d.Info()
		if e != nil {
			return e
		}
		if e = owned(st); e != nil {
			return e
		}
		return nil
	})
}
