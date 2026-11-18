package cli

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf16"

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

// Export only in the test build so cli and cli_test use the same installer,
// dispatcher and per-process snapshot.
type CLIFixtureProgramSpec struct {
	Kind   string
	Log    string
	Calls  string
	Marker string
	Exit   int
}

type cliFixtureProgramSpec = CLIFixtureProgramSpec

func cliFixtureProgram(t *testing.T, dir, name, script string, spec cliFixtureProgramSpec) {
	t.Helper()
	CLIFixtureProgram(t, dir, name, script, spec)
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
	exe, err := os.Executable()
	if err != nil {
		return "", err
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

func init() {
	if runtime.GOOS != "windows" {
		return
	}
	exe, err := os.Executable()
	if err != nil {
		return
	}
	raw, err := os.ReadFile(exe + ".cli-fixture.json")
	if os.IsNotExist(err) {
		return
	}
	if err != nil {
		panic(err)
	}
	var spec cliFixtureProgramSpec
	if err := json.Unmarshal(raw, &spec); err != nil {
		panic(err)
	}
	switch spec.Kind {
	case "clipboard":
		raw, err := io.ReadAll(os.Stdin)
		if err != nil {
			panic(err)
		}
		if len(raw) < 2 || raw[0] != 0xff || raw[1] != 0xfe || len(raw)%2 != 0 {
			panic("Windows clipboard fixture requires BOM-prefixed UTF-16LE")
		}
		units := make([]uint16, (len(raw)-2)/2)
		for i := range units {
			units[i] = binary.LittleEndian.Uint16(raw[2+2*i:])
		}
		if err := os.WriteFile(spec.Log, []byte(string(utf16.Decode(units))), 0600); err != nil {
			panic(err)
		}
		if spec.Calls != "" {
			f, err := os.OpenFile(spec.Calls, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
			if err != nil {
				panic(err)
			}
			fmt.Fprintln(f, "called")
			f.Close()
		}
	case "claude":
		cwd, err := os.Getwd()
		if err != nil {
			panic(err)
		}
		cwd, err = filepath.EvalSymlinks(cwd)
		if err != nil {
			panic(err)
		}
		f, err := os.OpenFile(spec.Log, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			panic(err)
		}
		fmt.Fprintf(f, "cwd=%s argv=%s\n", cwd, strings.Join(os.Args[1:], " "))
		f.Close()
	case "argv":
		raw, err := json.Marshal(os.Args[1:])
		if err != nil {
			panic(err)
		}
		if err := os.WriteFile(spec.Log, raw, 0600); err != nil {
			panic(err)
		}
	case "git-tracking":
		if len(os.Args) > 3 && os.Args[3] == "rev-parse" {
			fmt.Println(filepath.ToSlash(os.Args[2]))
			os.Exit(0)
		}
		if len(os.Args) <= 3 || os.Args[3] != "ls-files" {
			panic("unexpected git fixture arguments")
		}
	case "git-timeout":
		time.Sleep(5 * time.Second)
	case "git-non-repository":
		fmt.Fprintln(os.Stderr, "fatal: not a git repository")
	case "git-failure":
	case "never-run":
		if err := os.WriteFile(spec.Marker, nil, 0600); err != nil {
			panic(err)
		}
	default:
		panic("unknown CLI fixture program kind")
	}
	os.Exit(spec.Exit)
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
