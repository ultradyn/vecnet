//go:build windows

package platform

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

func currentSID() (*windows.SID, error) {
	u, e := windows.GetCurrentProcessToken().GetTokenUser()
	if e != nil {
		return nil, e
	}
	return u.User.Sid, nil
}

// Windows servicing owns standard Program Files/System32 executables. It is
// trusted only for shared-readable software, never private client authority.
func trustedInstaller(sid *windows.SID) bool {
	return sid.String() == "S-1-5-80-956008885-3418522649-1831038044-1853292631-2271478464"
}
func privateSD() (*windows.SECURITY_DESCRIPTOR, error) {
	sid, e := currentSID()
	if e != nil {
		return nil, e
	}
	return windows.SecurityDescriptorFromString("O:" + sid.String() + "D:P(A;OICI;FA;;;" + sid.String() + ")(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)")
}

// CheckFile validates the opened object, never a second pathname lookup. Reparse
// points, device files and hard-linked authority files cannot become trusted.
func CheckFile(f *os.File, directory, private bool) error {
	h := windows.Handle(f.Fd())
	typ, e := windows.GetFileType(h)
	if e != nil {
		return e
	}
	if typ != windows.FILE_TYPE_DISK {
		return fmt.Errorf("private state must be on a local disk")
	}
	var info windows.ByHandleFileInformation
	if e = windows.GetFileInformationByHandle(h, &info); e != nil {
		return e
	}
	if info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return fmt.Errorf("reparse points are not private state")
	}
	isDir := info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0
	if isDir != directory || !directory && info.NumberOfLinks != 1 {
		return fmt.Errorf("private state requires a single-link regular file or directory")
	}
	sd, e := windows.GetSecurityInfo(h, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if e != nil {
		return e
	}
	owner, _, e := sd.Owner()
	if e != nil {
		return e
	}
	sid, e := currentSID()
	if e != nil {
		return e
	}
	trustedOwner := windows.EqualSid(owner, sid) || !private && (owner.IsWellKnown(windows.WinLocalSystemSid) || owner.IsWellKnown(windows.WinBuiltinAdministratorsSid) || trustedInstaller(owner))
	if !trustedOwner {
		return fmt.Errorf("private state is owned by another Windows user")
	}
	acl, _, e := sd.DACL()
	if e != nil {
		return e
	}
	if acl == nil {
		return fmt.Errorf("private state has an unrestricted DACL")
	}
	// Unknown allow ACE types fail closed. INHERIT_ONLY entries do not grant
	// access to this object. Deny entries only restrict access further.
	for i := uint32(0); i < uint32(acl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if e = windows.GetAce(acl, i, &ace); e != nil {
			return e
		}
		if ace.Header.AceFlags&windows.INHERIT_ONLY_ACE != 0 || ace.Header.AceType == windows.ACCESS_DENIED_ACE_TYPE {
			continue
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
			return fmt.Errorf("unsupported private-state ACL entry")
		}
		trustee := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		trusted := windows.EqualSid(trustee, sid) || trustee.IsWellKnown(windows.WinLocalSystemSid) || trustee.IsWellKnown(windows.WinBuiltinAdministratorsSid) || !private && trustedInstaller(trustee)
		mask := uint32(ace.Mask)
		dangerous := mask != 0
		if !private {
			dangerous = mask&(windows.GENERIC_WRITE|windows.GENERIC_ALL|windows.FILE_WRITE_DATA|windows.FILE_APPEND_DATA|windows.FILE_WRITE_EA|windows.FILE_WRITE_ATTRIBUTES|windows.DELETE|windows.WRITE_DAC|windows.WRITE_OWNER|0x40) != 0
		}
		if !trusted && dangerous {
			return fmt.Errorf("private state is accessible to another Windows user")
		}
	}
	return nil
}

func ntError(e error) error {
	var status windows.NTStatus
	if errors.As(e, &status) {
		return status.Errno()
	}
	return e
}

// OpenAt resolves one safe name relative to a pinned directory handle. Both
// directories and files omit FILE_SHARE_DELETE so a held lock cannot be unlinked
// and replaced by a second lock. Directory-relative opens also prevent junction
// swaps in the inventory's authority namespace.
func openAt(dir *os.File, name string, flags int, directory, create bool) (*os.File, error) {
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\\:\x00") {
		return nil, fmt.Errorf("invalid private state name")
	}
	object, e := windows.NewNTUnicodeString(name)
	if e != nil {
		return nil, e
	}
	sd, e := privateSD()
	if e != nil {
		return nil, e
	}
	oa := windows.OBJECT_ATTRIBUTES{Length: uint32(unsafe.Sizeof(windows.OBJECT_ATTRIBUTES{})), RootDirectory: windows.Handle(dir.Fd()), ObjectName: object, Attributes: windows.OBJ_CASE_INSENSITIVE | windows.OBJ_DONT_REPARSE, SecurityDescriptor: sd}
	access := uint32(windows.GENERIC_READ | windows.SYNCHRONIZE | windows.READ_CONTROL)
	if flags&(os.O_WRONLY|os.O_RDWR) != 0 {
		access |= windows.GENERIC_WRITE | windows.WRITE_DAC
	}
	disposition := uint32(windows.FILE_OPEN)
	if create || flags&os.O_CREATE != 0 {
		disposition = windows.FILE_OPEN_IF
	}
	if flags&os.O_EXCL != 0 && flags&os.O_CREATE != 0 {
		disposition = windows.FILE_CREATE
	}
	options := uint32(windows.FILE_OPEN_REPARSE_POINT | windows.FILE_SYNCHRONOUS_IO_NONALERT)
	if directory {
		options |= windows.FILE_DIRECTORY_FILE
	} else {
		options |= windows.FILE_NON_DIRECTORY_FILE
	}
	var h windows.Handle
	var ios windows.IO_STATUS_BLOCK
	e = windows.NtCreateFile(&h, access, &oa, &ios, nil, windows.FILE_ATTRIBUTE_NORMAL, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, disposition, options, 0, 0)
	if e != nil {
		return nil, &os.PathError{Op: "open", Path: filepath.Join(dir.Name(), name), Err: ntError(e)}
	}
	f := os.NewFile(uintptr(h), filepath.Join(dir.Name(), name))
	var info windows.ByHandleFileInformation
	if e = windows.GetFileInformationByHandle(h, &info); e != nil || info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		f.Close()
		if e == nil {
			e = fmt.Errorf("reparse point refused")
		}
		return nil, e
	}
	return f, nil
}
func OpenAt(dir *os.File, name string, flags int, mode os.FileMode) (*os.File, error) {
	f, e := openAt(dir, name, flags, false, false)
	if e != nil {
		return nil, e
	}
	if e = CheckFile(f, false, true); e != nil {
		f.Close()
		return nil, e
	}
	// Validate owner, ACL, type and link count BEFORE any destructive action.
	if flags&os.O_TRUNC != 0 {
		if e = f.Truncate(0); e != nil {
			f.Close()
			return nil, e
		}
	}
	return f, nil
}

func openDir(path string, create bool) (*os.File, error) {
	abs, e := filepath.Abs(path)
	if e != nil {
		return nil, e
	}
	volume := filepath.VolumeName(abs)
	if volume == "" || strings.HasPrefix(volume, "\\\\") {
		return nil, fmt.Errorf("private state requires an absolute local disk path")
	}
	root := volume + string(filepath.Separator)
	p, e := windows.UTF16PtrFromString(root)
	if e != nil {
		return nil, e
	}
	h, e := windows.CreateFile(p, windows.GENERIC_READ|windows.READ_CONTROL, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if e != nil {
		return nil, e
	}
	dir := os.NewFile(uintptr(h), root)
	rel := strings.TrimPrefix(abs, root)
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		if part == "" || part == "." {
			continue
		}
		next, err := openAt(dir, part, os.O_RDONLY, true, create)
		dir.Close()
		if err != nil {
			return nil, err
		}
		dir = next
	}
	return dir, nil
}
func OpenDir(path string) (*os.File, error) { return openDir(path, false) }

// OpenWindowsDirAt opens one existing directory component relative to a held parent.
// It refuses reparse points and omits delete sharing, like OpenDir. The caller
// must check the returned handle's owner/permissions for its trust policy.
func OpenWindowsDirAt(parent *os.File, name string) (*os.File, error) {
	return openAt(parent, name, os.O_RDONLY, true, false)
}

func MkdirAll(path string, mode os.FileMode) error {
	dir, e := openDir(path, true)
	if e != nil {
		return e
	}
	return dir.Close()
}

// MkdirTemp creates a new directory with an explicit TokenUser owner and
// protected private DACL. Inheriting a parent's DACL alone does not set its owner.
func MkdirTemp(dir, pattern string) (string, error) {
	if strings.ContainsAny(pattern, `/\`) {
		return "", errors.New("temporary directory pattern contains a separator")
	}
	if dir == "" {
		dir = os.TempDir()
	}
	parent, err := OpenDir(dir)
	if err != nil {
		return "", err
	}
	defer parent.Close()
	for range 100 {
		name := pattern + rand.Text()
		if i := strings.LastIndexByte(pattern, '*'); i >= 0 {
			name = pattern[:i] + rand.Text() + pattern[i+1:]
		}
		f, err := openAt(parent, name, os.O_CREATE|os.O_EXCL, true, true)
		if errors.Is(err, os.ErrExist) {
			continue
		}
		if err != nil {
			return "", err
		}
		err = CheckFile(f, true, true)
		closeErr := f.Close()
		if err != nil {
			return "", err
		}
		if closeErr != nil {
			return "", closeErr
		}
		return filepath.Join(dir, name), nil
	}
	return "", os.ErrExist
}
func OpenFile(path string, flags int, mode os.FileMode) (*os.File, error) {
	dir, e := OpenDir(filepath.Dir(path))
	if e != nil {
		return nil, e
	}
	defer dir.Close()
	return OpenAt(dir, filepath.Base(path), flags, mode)
}
func CheckPath(path string, directory, private bool) error {
	var f *os.File
	var e error
	if directory {
		f, e = OpenDir(path)
	} else {
		dir, err := OpenDir(filepath.Dir(path))
		if err != nil {
			return err
		}
		defer dir.Close()
		f, e = openAt(dir, filepath.Base(path), os.O_RDONLY, false, false)
	}
	if e != nil {
		return e
	}
	defer f.Close()
	return CheckFile(f, directory, private)
}
func TryLock(f *os.File) (bool, error) {
	e := windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &windows.Overlapped{})
	if errors.Is(e, windows.ERROR_LOCK_VIOLATION) || errors.Is(e, windows.ERROR_IO_PENDING) {
		return false, nil
	}
	return e == nil, e
}
func Unlock(f *os.File) error {
	return windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, &windows.Overlapped{})
}
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
func SyncDir(f *os.File) error { return nil }
func Replace(old, new string) error {
	a, e := windows.UTF16PtrFromString(old)
	if e != nil {
		return e
	}
	b, e := windows.UTF16PtrFromString(new)
	if e != nil {
		return e
	}
	return windows.MoveFileEx(a, b, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH)
}

// Background avoids a console flash for noninteractive maintenance.
func Background(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW}
}

// Canonical resolves junctions as well as symlinks and normalizes the actual
// filesystem spelling, matching Node realpath. filepath.EvalSymlinks on Windows
// can leave mount-point junctions unresolved.
func Canonical(path string) (string, error) {
	p, e := windows.UTF16PtrFromString(path)
	if e != nil {
		return "", e
	}
	h, e := windows.CreateFile(p, windows.FILE_READ_ATTRIBUTES, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if e != nil {
		return "", &os.PathError{Op: "canonical", Path: path, Err: e}
	}
	defer windows.CloseHandle(h)
	buf := make([]uint16, 512)
	for {
		n, e := windows.GetFinalPathNameByHandle(h, &buf[0], uint32(len(buf)), 0)
		if e != nil {
			return "", e
		}
		if int(n) < len(buf) {
			result := windows.UTF16ToString(buf[:n])
			if strings.HasPrefix(result, `\\?\UNC\`) {
				result = `\\` + strings.TrimPrefix(result, `\\?\UNC\`)
			} else {
				result = strings.TrimPrefix(result, `\\?\`)
			}
			return filepath.Clean(result), nil
		}
		buf = make([]uint16, n+1)
	}
}

func IsReparse(path string) (bool, error) {
	p, e := windows.UTF16PtrFromString(path)
	if e != nil {
		return false, e
	}
	attributes, e := windows.GetFileAttributes(p)
	if e != nil {
		return false, &os.PathError{Op: "attributes", Path: path, Err: e}
	}
	return attributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0, nil
}
