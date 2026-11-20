package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/platform"
	"github.com/XertroV/vecnet/internal/testfixture/cliprogram"
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

// Export only in the test build so cli and cli_test use the same installer,
// dispatcher and per-process snapshot.
type CLIFixtureProgramSpec = cliprogram.Spec

type cliFixtureProgramSpec = CLIFixtureProgramSpec

func cliFixtureProgram(t *testing.T, dir, name, script string, spec cliFixtureProgramSpec) {
	t.Helper()
	CLIFixtureProgram(t, dir, name, script, spec)
}

// ExtendLocalGitProbeBudget raises the per-subprocess Git probe budget for
// one test whose subject is not that deadline. No internal/cli test runs in
// parallel, and the cleanup restores the product value.
func ExtendLocalGitProbeBudget(t *testing.T, budget time.Duration) {
	t.Helper()
	previous := localGitProbeBudget
	localGitProbeBudget = budget
	t.Cleanup(func() { localGitProbeBudget = previous })
}

// CLIFixtureProgram installs a synthetic subprocess, never a real clipboard
// or agent binary. Windows uses a distinct sidecar for each helper pathname.
func CLIFixtureProgram(t *testing.T, dir, name, script string, spec CLIFixtureProgramSpec) {
	t.Helper()
	if runtime.GOOS != "windows" {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0755); err != nil {
			t.Fatal(err)
		}
		return
	}
	if filepath.Ext(name) == "" {
		name += ".exe"
	}
	snapshot, err := cliFixtureSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name)
	// Only link the independent snapshot, never the running executable:
	// launcher repair requires the latter to remain single-link. Treat
	// aliases as execute-only fixtures: never write or chmod the shared
	// image through an alias. All per-test state lives outside the image.
	if err := os.Link(snapshot, path); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	if err := cliFixtureWriteFile(path+".cli-fixture.json", raw, 0600); err != nil {
		t.Fatal(err)
	}
}

var (
	cliFixtureSnapshotDir string // owned by TestMain; set only inside OnceValues
	cliFixtureSnapshot    = sync.OnceValues(makeCLIFixtureSnapshot)
)

func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "--ci-launcher-node-fixture" {
		if err := launcherFixtureNodeUtility(os.Args[2:], os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	code := m.Run()
	for _, dir := range []string{cliFixtureSnapshotDir, launcherFixtureNodeDir} {
		if dir == "" {
			continue
		}
		if err := os.RemoveAll(dir); err != nil {
			fmt.Fprintf(os.Stderr, "remove CLI fixture image directory: %v\n", err)
			code = 1
		}
	}
	os.Exit(code)
}

func makeCLIFixtureSnapshot() (string, error) {
	exe, configured := os.LookupEnv("CI_CLI_FIXTURE_HELPER")
	if !configured {
		var err error
		exe, err = os.Executable()
		if err != nil {
			return "", err
		}
	} else {
		if !filepath.IsAbs(exe) {
			return "", fmt.Errorf("CLI fixture helper must be absolute")
		}
		info, err := os.Lstat(exe)
		if err != nil {
			return "", err
		}
		if !info.Mode().IsRegular() {
			return "", fmt.Errorf("CLI fixture helper must be a regular file")
		}
	}
	src, err := os.Open(exe)
	if err != nil {
		return "", err
	}
	defer src.Close()
	base, err := filepath.EvalSymlinks(os.TempDir())
	if err != nil {
		return "", err
	}
	// Use the same temp volume as t.TempDir for hardlinks, but do not depend
	// on any test's cleanup or on the location of the running executable.
	cliFixtureSnapshotDir, err = platform.MkdirTemp(base, "vecnet-cli-fixture-")
	if err != nil {
		return "", err
	}
	path := filepath.Join(cliFixtureSnapshotDir, "helper.exe")
	dst, err := platform.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0700)
	if err != nil {
		return "", err
	}
	_, err = copyFixtureImage(dst, src)
	return path, errors.Join(err, dst.Close())
}

func TestCLIFixtureSnapshotConfiguredImage(t *testing.T) {
	oldDir := cliFixtureSnapshotDir
	t.Cleanup(func() { cliFixtureSnapshotDir = oldDir })
	for _, source := range []string{"", "relative.exe", t.TempDir()} {
		t.Run("invalid source "+source, func(t *testing.T) {
			t.Setenv("CI_CLI_FIXTURE_HELPER", source)
			if _, err := makeCLIFixtureSnapshot(); err == nil {
				t.Fatal("invalid configured image accepted")
			}
		})
	}
	t.Run("unset retains local fallback", func(t *testing.T) {
		t.Setenv("CI_CLI_FIXTURE_HELPER", "restore on cleanup")
		if err := os.Unsetenv("CI_CLI_FIXTURE_HELPER"); err != nil {
			t.Fatal(err)
		}
		path, err := makeCLIFixtureSnapshot()
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.RemoveAll(filepath.Dir(path)) })
		exe, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		a, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		b, err := os.ReadFile(exe)
		if err != nil {
			t.Fatal(err)
		}
		if string(a) != string(b) {
			t.Fatal("unset helper did not copy current executable")
		}
	})
	t.Run("missing configured image fails closed", func(t *testing.T) {
		t.Setenv("CI_CLI_FIXTURE_HELPER", filepath.Join(t.TempDir(), "absent.exe"))
		path, err := makeCLIFixtureSnapshot()
		if path != "" {
			t.Cleanup(func() { os.RemoveAll(filepath.Dir(path)) })
		}
		if err == nil {
			t.Fatal("configured missing helper fell back to test executable")
		}
	})
	t.Run("configured bytes copied independently", func(t *testing.T) {
		source := filepath.Join(t.TempDir(), "helper.exe")
		want := []byte("independent fixture executable image")
		if err := cliFixtureWriteFile(source, want, 0700); err != nil {
			t.Fatal(err)
		}
		t.Setenv("CI_CLI_FIXTURE_HELPER", source)
		path, err := makeCLIFixtureSnapshot()
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.RemoveAll(filepath.Dir(path)) })
		got, err := os.ReadFile(path)
		if err != nil || string(got) != string(want) {
			t.Fatal("snapshot did not use configured image")
		}
		a, _ := os.Stat(source)
		b, _ := os.Stat(path)
		if os.SameFile(a, b) {
			t.Fatal("snapshot aliases configured source")
		}
	})
}

func init() {
	if runtime.GOOS != "windows" {
		return
	}
	exe, err := os.Executable()
	if err != nil {
		return
	}
	spec, err := cliprogram.ReadSpec(exe)
	if os.IsNotExist(err) {
		return
	}
	if err != nil {
		panic(err)
	}
	os.Exit(cliprogram.Run(spec))
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
