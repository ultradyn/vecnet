//go:build windows

package platform

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

func TestOpenWindowsDirAtContract(t *testing.T) {
	path := filepath.Join(t.TempDir(), "parent")
	if err := MkdirAll(filepath.Join(path, "child"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "file"), []byte("unchanged"), 0600); err != nil {
		t.Fatal(err)
	}
	parent, err := OpenDir(path)
	if err != nil {
		t.Fatal(err)
	}
	defer parent.Close()
	child, err := OpenWindowsDirAt(parent, "child")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(child.Name(), child.Name()+"-moved"); err == nil {
		child.Close()
		t.Fatal("opened directory allowed delete sharing")
	}
	info, err := child.Stat()
	child.Close()
	want, statErr := os.Stat(filepath.Join(path, "child"))
	if err != nil || statErr != nil || !info.IsDir() || !os.SameFile(info, want) {
		t.Fatalf("wrong directory: %v, %v", err, statErr)
	}
	for _, name := range []string{"", ".", "..", "child/../child", `child\..\child`, "child:stream", "\x00", "file", "missing"} {
		if f, err := OpenWindowsDirAt(parent, name); err == nil {
			f.Close()
			t.Errorf("accepted invalid/non-directory child %q", name)
		}
	}
	if _, err := os.Stat(filepath.Join(path, "missing")); !os.IsNotExist(err) {
		t.Fatalf("missing child was created: %v", err)
	}
	t.Run("symlink", func(t *testing.T) {
		if err := os.Symlink(filepath.Join(path, "child"), filepath.Join(path, "link")); err != nil {
			if errors.Is(err, windows.ERROR_PRIVILEGE_NOT_HELD) {
				t.Skipf("native symlink privilege unavailable: %v", err)
			}
			t.Fatal(err)
		}
		if f, err := OpenWindowsDirAt(parent, "link"); err == nil {
			f.Close()
			t.Fatal("followed a directory symlink")
		}
	})
	parent.Close()
	if f, err := OpenWindowsDirAt(parent, "child"); err == nil {
		f.Close()
		t.Fatal("opened child through a closed parent")
	}
}
