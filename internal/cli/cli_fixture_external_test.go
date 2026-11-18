package cli_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/XertroV/vecnet/internal/cli"
	"github.com/XertroV/vecnet/internal/platform"
)

// Secret fixtures must have real private permissions before bytes are written:
// os.WriteFile(0600) alone does not prevent inherited Windows ACL access.
func cliFixtureWriteFile(path string, raw []byte, mode os.FileMode) error {
	if runtime.GOOS != "windows" || mode.Perm()&0077 != 0 {
		return os.WriteFile(path, raw, mode)
	}
	f, err := platform.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	_, err = f.Write(raw)
	closeErr := f.Close()
	if err != nil {
		return err
	}
	return closeErr
}
func cliFixtureMkdirAll(path string, mode os.FileMode) error {
	if runtime.GOOS == "windows" && mode.Perm()&0077 == 0 {
		return platform.MkdirAll(path, mode)
	}
	return os.MkdirAll(path, mode)
}
func cliFixtureMkdir(path string, mode os.FileMode) error {
	if runtime.GOOS != "windows" || mode.Perm()&0077 != 0 {
		return os.Mkdir(path, mode)
	}
	if _, err := os.Lstat(path); err == nil {
		return &os.PathError{Op: "mkdir", Path: path, Err: os.ErrExist}
	} else if !os.IsNotExist(err) {
		return err
	}
	// Preserve Mkdir's missing-parent behavior rather than silently creating it.
	if _, err := os.Stat(filepath.Dir(path)); err != nil {
		return err
	}
	return platform.MkdirAll(path, mode)
}
func cliFixtureRequireMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS == "windows" {
		if want == 0600 || want == 0700 {
			if err := platform.CheckPath(path, info.IsDir(), true); err != nil {
				t.Fatalf("%s is not owner-private: %v", path, err)
			}
		} else {
			// Windows exposes the writable attribute, not Unix group/execute bits.
			if (info.Mode().Perm()&0200 != 0) != (want&0200 != 0) {
				t.Fatalf("%s writable attribute changed: %v (Unix mode %o)", path, info.Mode(), want)
			}
		}
		return
	}
	if info.Mode().Perm() != want {
		t.Fatal(fmt.Sprintf("%s: mode %o want %o", path, info.Mode().Perm(), want))
	}
}

type cliFixtureProgramSpec = cli.CLIFixtureProgramSpec

func cliFixtureProgram(t *testing.T, dir, name, script string, spec cliFixtureProgramSpec) {
	t.Helper()
	cli.CLIFixtureProgram(t, dir, name, script, spec)
}

func cliFixtureShell(t *testing.T) string {
	t.Helper()
	if sh, err := exec.LookPath("sh"); err == nil {
		return sh
	}
	if runtime.GOOS == "windows" {
		for _, root := range []string{os.Getenv("ProgramFiles"), os.Getenv("ProgramFiles(x86)")} {
			sh := filepath.Join(root, "Git", "bin", "sh.exe")
			if _, err := os.Stat(sh); err == nil {
				return sh
			}
		}
	}
	t.Fatal("shell fixture requires sh (Git for Windows provides it)")
	return ""
}

func cliFixtureChdir(t *testing.T, dir string) {
	t.Helper()
	if runtime.GOOS != "windows" {
		t.Chdir(dir)
		return
	}
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("PWD", dir) // Like t.Chdir, changing global cwd forbids t.Parallel.
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(old); err != nil {
			t.Errorf("restore cwd: %v", err)
		}
	})
}
