//go:build windows

package cli

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/XertroV/vecnet/internal/platform"
)

func TestLauncherWindowsDirectoryWalkLinear(t *testing.T) {
	base, err := platform.MkdirTemp(t.TempDir(), "walk-")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(base, "one", "two", "three")
	if err := platform.MkdirAll(path, 0700); err != nil {
		t.Fatal(err)
	}
	absolute, relative := 0, 0
	root, err := launcherWindowsOpenDirWith(path, base, false,
		func(path string) (*os.File, error) {
			absolute++
			return platform.OpenDir(path)
		},
		func(parent *os.File, name string) (*os.File, error) {
			relative++
			return platform.OpenWindowsDirAt(parent, name)
		})
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	depth := strings.Count(strings.TrimPrefix(path, filepath.VolumeName(path)+string(filepath.Separator)), string(filepath.Separator)) + 1
	if absolute != 1 || relative != depth-1 || len(root.pins) != depth {
		t.Fatalf("repeated walk or missing pins: absolute=%d relative=%d pins=%d depth=%d", absolute, relative, len(root.pins), depth)
	}
	t.Logf("depth=%d absolute=%d relative=%d; component opens=%d (old repeated walk=%d)", depth, absolute, relative, absolute+relative, depth*(depth+1)/2)
	pins := append([]*os.File(nil), root.pins...)
	for _, pin := range pins {
		if err := os.Rename(pin.Name(), pin.Name()+"-moved"); err == nil {
			t.Fatal("ancestor was not pinned: ", pin.Name())
		}
	}
	if err := root.Close(); err != nil {
		t.Fatal(err)
	}
	for _, pin := range pins {
		if _, err := pin.Stat(); !errors.Is(err, os.ErrClosed) {
			t.Fatalf("pin retained after Close: %s: %v", pin.Name(), err)
		}
	}
}

func TestLauncherWindowsDirectoryWalkRejectsUnsafeChild(t *testing.T) {
	for _, kind := range []string{"private-ancestor", "reparse"} {
		t.Run(kind, func(t *testing.T) {
			base, err := platform.MkdirTemp(t.TempDir(), "walk-")
			if err != nil {
				t.Fatal(err)
			}
			ancestor := filepath.Join(base, "ancestor")
			if err := platform.MkdirAll(filepath.Join(ancestor, "leaf"), 0700); err != nil {
				t.Fatal(err)
			}
			if kind == "private-ancestor" {
				if err := cliFixtureChmod(ancestor, 0755); err != nil {
					t.Fatal(err)
				}
			} else {
				link := filepath.Join(base, "link")
				if err := cliFixtureSymlink(t, ancestor, link); err != nil {
					t.Fatal(err)
				}
				ancestor = link
			}
			var opened []*os.File
			track := func(f *os.File, err error) (*os.File, error) {
				if f != nil {
					opened = append(opened, f)
				}
				return f, err
			}
			root, err := launcherWindowsOpenDirWith(filepath.Join(ancestor, "leaf"), base, false,
				func(path string) (*os.File, error) { return track(platform.OpenDir(path)) },
				func(parent *os.File, name string) (*os.File, error) {
					return track(platform.OpenWindowsDirAt(parent, name))
				})
			if err == nil {
				root.Close()
				t.Fatal("unsafe ancestor accepted")
			}
			if len(opened) == 0 {
				t.Fatal("refusal did not exercise the directory walk")
			}
			for _, pin := range opened {
				if _, err := pin.Stat(); !errors.Is(err, os.ErrClosed) {
					t.Fatalf("pin leaked after refusal: %s: %v", pin.Name(), err)
				}
			}
		})
	}
}
