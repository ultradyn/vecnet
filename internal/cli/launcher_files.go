package cli

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/XertroV/vecnet/internal/platform"
)

// os.Root confines relative lookups on Unix. On Windows the additional
// no-reparse handles pin every ancestor against replacement for its lifetime.
type launcherRoot struct {
	*os.Root
	pins []*os.File
}

func (r *launcherRoot) Close() error {
	if r == nil || r.Root == nil {
		return nil
	}
	err := r.Root.Close()
	r.Root = nil
	for i := len(r.pins) - 1; i >= 0; i-- {
		err = errors.Join(err, r.pins[i].Close())
	}
	r.pins = nil
	return err
}
func (r *launcherRoot) OpenRoot(name string) (*launcherRoot, error) {
	if runtime.GOOS == "windows" {
		return launcherOpenDir(filepath.Join(r.Name(), name), r.Name(), false)
	}
	root, err := r.Root.OpenRoot(name)
	if err != nil {
		return nil, err
	}
	return &launcherRoot{Root: root}, nil
}
func (r *launcherRoot) OpenFile(name string, flags int, mode fs.FileMode) (*os.File, error) {
	if runtime.GOOS == "windows" {
		if !filepath.IsLocal(name) {
			return nil, usageErr("invalid launcher file path")
		}
		parent, err := platform.OpenDir(filepath.Join(r.Name(), filepath.Dir(name)))
		if err != nil {
			return nil, err
		}
		defer parent.Close()
		return platform.OpenAt(parent, filepath.Base(name), flags, mode)
	}
	return r.Root.OpenFile(name, flags, mode)
}
func (r *launcherRoot) Mkdir(name string, mode fs.FileMode) error {
	if runtime.GOOS == "windows" {
		if !filepath.IsLocal(name) {
			return usageErr("invalid launcher directory path")
		}
		if _, err := r.Lstat(name); !errors.Is(err, os.ErrNotExist) {
			if err == nil {
				return os.ErrExist
			}
			return err
		}
		return platform.MkdirAll(filepath.Join(r.Name(), name), mode)
	}
	return r.Root.Mkdir(name, mode)
}
func (r *launcherRoot) MkdirAll(name string, mode fs.FileMode) error {
	if runtime.GOOS == "windows" {
		if name != "." && !filepath.IsLocal(name) {
			return usageErr("invalid launcher directory path")
		}
		return platform.MkdirAll(filepath.Join(r.Name(), name), mode)
	}
	return r.Root.MkdirAll(name, mode)
}
func (r *launcherRoot) Rename(old, new string) error {
	if runtime.GOOS == "windows" {
		if !filepath.IsLocal(old) || !filepath.IsLocal(new) {
			return usageErr("invalid launcher publication path")
		}
		return platform.Replace(filepath.Join(r.Name(), old), filepath.Join(r.Name(), new))
	}
	return r.Root.Rename(old, new)
}

// Windows privacy is established from the actual owner SID and DACL, never
// from synthetic POSIX permission bits. Also validate the opened handle.
func launcherCheckInfo(root *launcherRoot, name string, info os.FileInfo, directory, private bool) error {
	if runtime.GOOS == "windows" {
		return platform.CheckPath(filepath.Join(root.Name(), name), directory, private)
	}
	return launcherSafeInfo(info, directory, private)
}
func launcherCheckFile(file *os.File, directory, private bool) error {
	if runtime.GOOS == "windows" {
		return platform.CheckFile(file, directory, private)
	}
	info, err := file.Stat()
	if err != nil {
		return err
	}
	return launcherSafeInfo(info, directory, private)
}
func launcherCheckMode(root *launcherRoot, name string, info os.FileInfo, mode fs.FileMode) error {
	if runtime.GOOS == "windows" {
		return platform.CheckPath(filepath.Join(root.Name(), name), false, true)
	}
	if info.Mode()&0o7777 != mode {
		return errors.New("launcher managed file permissions changed")
	}
	return nil
}
func launcherSetMode(file *os.File, mode fs.FileMode) error {
	if runtime.GOOS == "windows" {
		return platform.CheckFile(file, false, true)
	}
	return file.Chmod(mode)
}
func launcherExecutableInfo(path string, info os.FileInfo) bool {
	if !info.Mode().IsRegular() {
		return false
	}
	if runtime.GOOS == "windows" {
		return strings.EqualFold(filepath.Ext(path), ".exe")
	}
	return info.Mode().Perm()&0o111 != 0
}

func launcherWindowsOpenDir(path, managed string, create bool) (*launcherRoot, error) {
	return launcherWindowsOpenDirWith(path, managed, create, platform.OpenDir, platform.OpenWindowsDirAt)
}

func launcherWindowsOpenDirWith(path, managed string, create bool,
	openDir func(string) (*os.File, error), openDirAt func(*os.File, string) (*os.File, error),
) (_ *launcherRoot, err error) {
	volume := filepath.VolumeName(path)
	if volume == "" || strings.HasPrefix(volume, `\\`) || strings.ContainsAny(strings.TrimPrefix(path, volume), ":\x00\r\n") {
		return nil, usageErr("launcher requires a local absolute Windows path")
	}
	prefix := volume + string(filepath.Separator)
	pins := []*os.File{}
	defer func() {
		if err != nil {
			for i := len(pins) - 1; i >= 0; i-- {
				pins[i].Close()
			}
		}
	}()
	pin := func(path string, private bool) error {
		var f *os.File
		var e error
		if len(pins) == 0 {
			f, e = openDir(path)
		} else {
			// Every ancestor remains pinned. Resolve only this child instead of
			// walking again from the volume root for every successive prefix.
			f, e = openDirAt(pins[len(pins)-1], filepath.Base(path))
		}
		if e != nil {
			return e
		}
		// C:\Users is the OS profile container: other users may create their own
		// profile, not mutate our pinned child. Every ancestor from a HOME (and
		// every other non-system-container path) receives an actual DACL check.
		systemProfiles := strings.EqualFold(path, filepath.Join(volume+string(filepath.Separator), "Users"))
		if private || !systemProfiles {
			if e = platform.CheckFile(f, true, private); e != nil {
				f.Close()
				return e
			}
		}
		pins = append(pins, f)
		return nil
	}
	// The volume root is not our state. Pin the profile container, then validate
	// non-private ancestor DACLs without trying to rewrite their ownership.
	for _, part := range strings.Split(strings.TrimPrefix(filepath.Clean(path), prefix), string(filepath.Separator)) {
		if part == "" {
			continue
		}
		prefix = filepath.Join(prefix, part)
		if _, e := os.Lstat(prefix); errors.Is(e, os.ErrNotExist) && create {
			if err = platform.MkdirAll(prefix, 0o700); err != nil {
				return nil, err
			}
		}
		strict := managed != "" && (strings.EqualFold(prefix, managed) || strings.HasPrefix(strings.ToLower(prefix), strings.ToLower(managed+string(filepath.Separator))))
		if err = pin(prefix, strict); err != nil {
			return nil, fmtLauncherDirectoryError(prefix, err)
		}
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		return nil, err
	}
	if len(pins) == 0 {
		root.Close()
		return nil, errors.New("launcher root cannot be a volume root")
	}
	info, e := pins[len(pins)-1].Stat()
	pinned, e2 := root.Stat(".")
	if e != nil || e2 != nil || !os.SameFile(info, pinned) {
		root.Close()
		return nil, errors.New("launcher directory changed during validation")
	}
	return &launcherRoot{Root: root, pins: pins}, nil
}
func fmtLauncherDirectoryError(path string, err error) error {
	return &os.PathError{Op: "launcher directory", Path: path, Err: err}
}

func launcherSetupWrite(p launcherPaths, config any, credential []byte) error {
	root, err := launcherOpenDir(filepath.Dir(p.Config), filepath.Dir(p.Config), false)
	if err != nil {
		return err
	}
	defer root.Close()
	for _, name := range []string{filepath.Base(p.Credential), filepath.Base(p.Config)} {
		if _, err = launcherReadOwned(root, name, true); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	if err = launcherAtomicWrite(root, filepath.Base(p.Credential), credential); err != nil {
		return err
	}
	return launcherAtomicWrite(root, filepath.Base(p.Config), launcherManagedJSON(config))
}
func launcherDefaultPath(getenv func(string) string) string {
	if runtime.GOOS == "windows" {
		if root := getenv("SystemRoot"); filepath.IsAbs(root) {
			return filepath.Join(root, "System32")
		}
		return ""
	}
	return "/usr/local/bin:/usr/bin:/bin"
}
func launcherUncertainManagedPath(path string) bool {
	if runtime.GOOS == "windows" {
		return strings.ContainsAny(path, "\x00\r\n\"") || strings.HasPrefix(path, `\\`) || strings.Contains(strings.TrimPrefix(path, filepath.VolumeName(path)), ":")
	}
	return strings.ContainsAny(path, ";{}\"\\")
}
func launcherExpectedUnitPath(m launcherManaged, id string) string {
	if runtime.GOOS == "windows" {
		return filepath.Join(filepath.Dir(m.ConfigPath), "vecnet-launcher-"+id+".task.json")
	}
	return filepath.Join(filepath.Dir(m.UnitPath), "vecnet-launcher-"+id+".service")
}
func launcherPrivateBinaryMode(path string, info os.FileInfo) error {
	if runtime.GOOS == "windows" {
		return platform.CheckPath(path, false, true)
	}
	if info.Mode()&0o7777 != 0o500 {
		return errors.New("managed private binary requires mode0500")
	}
	return nil
}

// Windows byte-range locks deny reads of byte zero even to would-be waiters.
// Ownership/type/inode checks do not require reading lock contents.
func launcherValidateLock(root *launcherRoot, name string, optional bool) error {
	info, err := root.Lstat(name)
	if optional && errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if err = launcherCheckInfo(root, name, info, false, true); err != nil {
		return err
	}
	return launcherCheckMode(root, name, info, 0o600)
}

// Validate and retain the actual executable plus every parent before executing
// any bytes. Windows denies write/delete sharing while this proof is held.
func launcherPinExecutable(path string) (func(), error) {
	if !filepath.IsAbs(path) {
		return nil, errors.New("launcher executable must be absolute")
	}
	root, err := launcherOpenDir(filepath.Dir(path), "", false)
	if err != nil {
		return nil, err
	}
	fail := func(err error) (func(), error) { root.Close(); return nil, err }
	name := filepath.Base(path)
	info, err := root.Lstat(name)
	if err != nil {
		return fail(err)
	}
	if !launcherExecutableInfo(path, info) {
		return fail(errors.New("launcher path is not an executable regular file"))
	}
	if err = launcherCheckInfo(root, name, info, false, false); err != nil {
		return fail(err)
	}
	file, err := launcherOpenExecutable(root, name)
	if err != nil {
		return fail(err)
	}
	pinned, err := file.Stat()
	if err == nil && !os.SameFile(info, pinned) {
		err = errors.New("launcher executable changed during validation")
	}
	if err == nil {
		err = launcherCheckFile(file, false, false)
	}
	if err != nil {
		file.Close()
		return fail(err)
	}
	return func() { file.Close(); root.Close() }, nil
}
