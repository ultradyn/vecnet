//go:build windows

package cli

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

// Chmod has no ACL effect on Windows. Set the equivalent fixture DACL instead,
// including a public-read ACE when a test deliberately makes state nonprivate.
func cliFixtureChmod(path string, mode os.FileMode) error {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return err
	}
	access := "FA"
	if mode&0200 == 0 {
		access = "GRGX"
	}
	sddl := "D:P(A;OICI;" + access + ";;;" + user.User.Sid.String() + ")(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)"
	if mode&0200 == 0 {
		// Elevated tokens also receive the Administrators allow ACE. Deny data
		// writes/child creation explicitly, while retaining WRITE_DAC for cleanup.
		sddl = "D:P(D;OICI;0x00000156;;;WD)" + sddl[len("D:P"):]
	}
	if mode&0077 != 0 {
		sddl += "(A;OICI;GR;;;WD)"
	}
	sd, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		return err
	}
	acl, _, err := sd.DACL()
	if err != nil {
		return err
	}
	return windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, acl, nil)
}

func cliFixtureSymlink(t *testing.T, target, link string) error {
	t.Helper()
	err := os.Symlink(target, link)
	if err == nil {
		return nil
	}
	if !errors.Is(err, windows.ERROR_PRIVILEGE_NOT_HELD) {
		return err
	}
	resolved := target
	if !filepath.IsAbs(resolved) {
		resolved = filepath.Join(filepath.Dir(link), resolved)
	}
	info, statErr := os.Stat(resolved)
	if statErr == nil && info.IsDir() {
		// A directory junction exercises canonicalization and reparse-point refusal
		// without requiring Developer Mode or SeCreateSymbolicLinkPrivilege.
		// Plain-directory fixtures deliberately exclude ambient tools from PATH.
		// Keep the native junction fallback available through the system binary.
		systemDir, err := windows.GetSystemDirectory()
		if err != nil {
			t.Fatal(err)
		}
		powershell := filepath.Join(systemDir, "WindowsPowerShell", "v1.0", "powershell.exe")
		cmd := exec.Command(powershell, "-NoProfile", "-NonInteractive", "-Command", "New-Item -ItemType Junction -Path $env:VECNET_FIXTURE_LINK -Target $env:VECNET_FIXTURE_TARGET -ErrorAction Stop | Out-Null")
		cmd.Env = append(os.Environ(), "VECNET_FIXTURE_LINK="+link, "VECNET_FIXTURE_TARGET="+resolved)
		out, createErr := cmd.CombinedOutput()
		if createErr != nil {
			t.Fatalf("junction fixture: %v: %s", createErr, out)
		}
		return nil
	}
	t.Skip("file/dangling symlink primitive needs Windows Developer Mode; directory-junction cases run here, and native platform tests cover DACL/reparse refusal")
	return err
}

// Snapshot native security, not the DOS read-only attribute exposed by Perm.
func cliFixturePermissions(path string) (string, error) {
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return "", err
	}
	return sd.String(), nil
}

// A handle that does not share reads makes directory inspection unavailable
// without relying on the current process being a non-administrator token.
func cliFixtureDenyDirectoryRead(t *testing.T, path string) {
	t.Helper()
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	h, err := windows.CreateFile(p, windows.GENERIC_READ, 0, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = windows.CloseHandle(h) })
}

func TestFixtureReadOnlyDirectoryDeniesElevatedWrites(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "private")
	if err := cliFixtureMkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := cliFixtureChmod(dir, 0500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := cliFixtureChmod(dir, 0700); err != nil {
			t.Error(err)
		}
	})
	if err := os.WriteFile(filepath.Join(dir, "forbidden"), []byte("must not publish"), 0600); err == nil {
		t.Fatal("read-only fixture allowed a child write")
	}
}
