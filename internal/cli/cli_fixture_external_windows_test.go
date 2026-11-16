//go:build windows

package cli_test

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
		cmd := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-Command", "New-Item -ItemType Junction -Path $env:VECNET_FIXTURE_LINK -Target $env:VECNET_FIXTURE_TARGET -ErrorAction Stop | Out-Null")
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
