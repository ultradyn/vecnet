//go:build windows

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

	"github.com/XertroV/vecnet/internal/platform"
	"golang.org/x/sys/windows"
)

func windowsHome(home string) (string, error) {
	if home == "" || !filepath.IsAbs(home) || unsafeWindowsPath(home) {
		return "", errors.New("runtime requires an absolute local Windows HOME")
	}
	home = filepath.Clean(home)
	if e := platform.CheckPath(home, true, false); e != nil {
		return "", fmt.Errorf("unsafe runtime HOME: %w", e)
	}
	f, e := platform.OpenDir(home)
	if e != nil {
		return "", e
	}
	defer f.Close()
	buf := make([]uint16, 32768)
	n, e := windows.GetFinalPathNameByHandle(windows.Handle(f.Fd()), &buf[0], uint32(len(buf)), 0)
	if e != nil || int(n) >= len(buf) {
		return "", errors.New("cannot canonicalize Windows runtime HOME")
	}
	final := strings.TrimPrefix(windows.UTF16ToString(buf[:n]), `\\?\`)
	return filepath.Clean(final), nil
}
func unsafeWindowsPath(p string) bool {
	return unsafeString(p) || strings.Contains(p, `"`) || strings.HasPrefix(p, `\\`) || strings.Contains(strings.TrimPrefix(p, filepath.VolumeName(p)), ":")
}
func runtimeRelative(home, path string) (string, error) {
	if unsafeWindowsPath(path) {
		return "", errors.New("unsafe runtime path")
	}
	rel, e := filepath.Rel(home, path)
	if e != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", errors.New("runtime path escapes HOME")
	}
	return rel, nil
}
func privateRuntimePath(home, path string) bool {
	rel, e := filepath.Rel(root(home), path)
	return e == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

// Check each existing directory with native handle/SID/DACL checks. Windows mode
// bits are synthetic and must never authorize a runtime path.
func checkPath(home, path string) error {
	rel, e := runtimeRelative(home, path)
	if e != nil {
		return e
	}
	if e = platform.CheckPath(home, true, false); e != nil {
		return e
	}
	p := home
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		if part == "." {
			continue
		}
		p = filepath.Join(p, part)
		e = platform.CheckPath(p, true, privateRuntimePath(home, p))
		if errors.Is(e, os.ErrNotExist) {
			return nil
		}
		if e != nil {
			return fmt.Errorf("unsafe runtime directory %s: %w", p, e)
		}
	}
	return nil
}

// Keep each ancestor open across mutation. The no-share-delete handles prevent
// junction/rename swaps of the namespace while the installer owns its lock.
func pinRuntimePath(home, path string) (func(), error) {
	rel, e := runtimeRelative(home, path)
	if e != nil {
		return nil, e
	}
	var pins []*os.File
	closeAll := func() {
		for i := len(pins) - 1; i >= 0; i-- {
			_ = pins[i].Close()
		}
	}
	p := home
	for _, part := range append([]string{"."}, strings.Split(rel, string(filepath.Separator))...) {
		if part != "." {
			p = filepath.Join(p, part)
		}
		f, e := platform.OpenDir(p)
		if e != nil {
			closeAll()
			return nil, e
		}
		pins = append(pins, f)
		if e = platform.CheckFile(f, true, privateRuntimePath(home, p)); e != nil {
			closeAll()
			return nil, e
		}
	}
	return closeAll, nil
}
func mkdirSafe(home, path string, mode fs.FileMode) error {
	if e := checkPath(home, path); e != nil {
		return e
	}
	if e := platform.MkdirAll(path, mode); e != nil {
		return e
	}
	return checkPath(home, path)
}
func atomicWrite(path string, b []byte, mode fs.FileMode) error {
	if e := platform.CheckPath(path, false, true); e != nil && !errors.Is(e, os.ErrNotExist) {
		return e
	}
	dir, e := platform.OpenDir(filepath.Dir(path))
	if e != nil {
		return e
	}
	defer dir.Close()
	if e = platform.CheckFile(dir, true, true); e != nil {
		return e
	}
	f, e := platform.CreateTemp(filepath.Dir(path), ".install-*")
	if e != nil {
		return e
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	_, e = f.Write(b)
	if e == nil {
		e = f.Sync()
	}
	ce := f.Close()
	if e != nil {
		return e
	}
	if ce != nil {
		return ce
	}
	if e = platform.Replace(tmp, path); e != nil {
		return e
	}
	return platform.SyncDir(dir)
}
func syncDir(path string) error {
	f, e := platform.OpenDir(path)
	if e != nil {
		return e
	}
	defer f.Close()
	return platform.SyncDir(f)
}
func readPrivate(path string, limit int64) ([]byte, error) {
	f, e := platform.OpenFile(path, os.O_RDONLY, 0)
	if e != nil {
		return nil, e
	}
	defer f.Close()
	st, e := f.Stat()
	if e != nil {
		return nil, e
	}
	if st.Size() > limit {
		return nil, errors.New("runtime file exceeds integrity check limit")
	}
	b, e := io.ReadAll(io.LimitReader(f, limit+1))
	if int64(len(b)) > limit {
		return nil, errors.New("runtime file exceeds integrity check limit")
	}
	return b, e
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
	b, e := readPrivate(filepath.Join(root(home), name), 64<<10)
	if e != nil {
		return m, e
	}
	if e = decodePrivateJSON(b, &m); e != nil {
		return m, errors.New("invalid runtime manifest JSON")
	}
	hash := filepath.Base(m.Release)
	_, he := hex.DecodeString(hash)
	if m.Schema != 1 || m.Version == "" || len(m.Version) > 256 || unsafeString(m.Version) || len(hash) != 64 || he != nil || filepath.Dir(m.Release) != root(home) || m.Unit != unitPath(home) || !filepath.IsAbs(m.Node) || unsafeWindowsPath(m.Node) {
		return m, errors.New("incompatible or unsafe runtime manifest; setup pending")
	}
	unit, e := windowsDefinition(home, m.Node, m.Release)
	if e != nil {
		return m, e
	}
	if m.UnitHash != sum(unit) {
		return m, errors.New("incompatible Windows runtime task definition; setup pending")
	}
	return m, nil
}
func decodePrivateJSON(b []byte, v any) error {
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if e := d.Decode(v); e != nil {
		return e
	}
	if d.Decode(new(any)) != io.EOF {
		return errors.New("trailing runtime JSON")
	}
	// Duplicate keys are not alternate authorizations for pending/applied state.
	d = json.NewDecoder(bytes.NewReader(b))
	var walk func() error
	walk = func() error {
		t, e := d.Token()
		if e != nil {
			return e
		}
		delim, ok := t.(json.Delim)
		if !ok {
			return nil
		}
		switch delim {
		case '{':
			seen := map[string]bool{}
			for d.More() {
				k, e := d.Token()
				if e != nil {
					return e
				}
				s, ok := k.(string)
				if !ok || seen[s] {
					return errors.New("duplicate runtime JSON key")
				}
				seen[s] = true
				if e = walk(); e != nil {
					return e
				}
			}
		case '[':
			for d.More() {
				if e = walk(); e != nil {
					return e
				}
			}
		default:
			return errors.New("invalid runtime JSON")
		}
		_, e = d.Token()
		return e
	}
	return walk()
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
	// MkdirTemp inherits the already protected private runtime directory DACL.
	tmp, e := os.MkdirTemp(root(home), ".release-*")
	if e != nil {
		return false, e
	}
	defer os.RemoveAll(tmp)
	if e = platform.CheckPath(tmp, true, true); e != nil {
		return false, e
	}
	for path, b := range files {
		if e = ctx.Err(); e != nil {
			return false, e
		}
		target := filepath.Join(tmp, filepath.FromSlash(path))
		if e = platform.MkdirAll(filepath.Dir(target), 0700); e != nil {
			return false, e
		}
		f, e := platform.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if e != nil {
			return false, e
		}
		_, e = f.Write(b)
		if e == nil {
			e = f.Sync()
		}
		ce := f.Close()
		if e != nil {
			return false, e
		}
		if ce != nil {
			return false, ce
		}
	}
	if e = verifyRelease(ctx, tmp, files); e != nil {
		return false, e
	}
	if repairAllowed {
		if e = beforeReplace(); e != nil {
			return false, e
		}
	}
	if damaged {
		backups := filepath.Join(root(home), "repair-backups")
		if e = mkdirSafe(home, backups, 0700); e != nil {
			return false, e
		}
		backup, e := os.MkdirTemp(backups, filepath.Base(release)+"-")
		if e != nil {
			return false, e
		}
		if e = os.Remove(backup); e != nil {
			return false, e
		}
		if e = platform.Replace(release, backup); e != nil {
			return false, e
		}
	}
	if e = platform.Replace(tmp, release); e != nil {
		return false, e
	}
	return damaged || repairAllowed, syncDir(root(home))
}
func verifyRelease(ctx context.Context, release string, files map[string][]byte) error {
	seen, entries := 0, 0
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
		if e = platform.CheckPath(path, d.IsDir(), true); e != nil {
			return e
		}
		if d.IsDir() {
			return nil
		}
		rel, e := filepath.Rel(release, path)
		if e != nil {
			return e
		}
		expected, ok := files[filepath.ToSlash(rel)]
		if !ok {
			return errors.New("unexpected staged runtime asset")
		}
		b, e := readPrivate(path, int64(len(expected)))
		if e != nil {
			return e
		}
		if !bytes.Equal(b, expected) {
			return errors.New("modified staged runtime asset; repair required")
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
		// Refuse reparse points/hard links even while preserving an unknown tree.
		return platform.CheckPath(p, d.IsDir(), true)
	})
}
