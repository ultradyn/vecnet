//go:build unix

package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestLocalRegistryRejectsUnsafeStorage(t *testing.T) {
	for _, damage := range []string{"inventory-mode", "directory-mode", "parent-mode", "inventory-link", "lock-link", "directory-link", "hardlink", "fifo", "lock-mode", "owner"} {
		t.Run(damage, func(t *testing.T) {
			a, _, home := localRegistryFixture(t)
			if _, err := a.observeLocalProject("v1", localRegistryNow()); err != nil {
				t.Fatal(err)
			}
			path := localRegistryInventory(home)
			dir := filepath.Dir(path)
			sentinel := filepath.Join(t.TempDir(), "untouched")
			if err := os.WriteFile(sentinel, []byte("sentinel"), 0600); err != nil {
				t.Fatal(err)
			}
			var err error
			switch damage {
			case "inventory-mode":
				err = os.Chmod(path, 0644)
			case "directory-mode":
				err = os.Chmod(dir, 0755)
			case "parent-mode":
				err = os.Chmod(filepath.Dir(dir), 0777)
			case "inventory-link":
				err = os.Remove(path)
				if err == nil {
					err = os.Symlink(sentinel, path)
				}
			case "lock-link":
				lock := filepath.Join(dir, "inventory.lock")
				err = os.Remove(lock)
				if err == nil {
					err = os.Symlink(sentinel, lock)
				}
			case "directory-link":
				err = os.Rename(dir, dir+"-moved")
				if err == nil {
					err = os.Symlink(dir+"-moved", dir)
				}
			case "hardlink":
				err = os.Link(path, filepath.Join(dir, "another-name"))
			case "fifo":
				err = os.Remove(path)
				if err == nil {
					err = syscall.Mkfifo(path, 0600)
				}
			case "lock-mode":
				err = os.Chmod(filepath.Join(dir, "inventory.lock"), 0644)
			case "owner":
				old := localRegistryEUID
				localRegistryEUID = func() int { return old() + 1 }
				t.Cleanup(func() { localRegistryEUID = old })
			}
			if err != nil {
				t.Fatal(err)
			}
			var cancel context.CancelFunc
			a.ctx, cancel = context.WithTimeout(context.Background(), 250*time.Millisecond)
			t.Cleanup(cancel)
			// Read-only inspection never opens the lock; mutations must reject unsafe locks.
			if damage != "lock-link" && damage != "lock-mode" {
				if _, err := a.localProjects(); err == nil {
					t.Fatal("inspection trusted unsafe storage")
				}
			}
			if _, err := a.observeLocalProject("v2", localRegistryNow()); err == nil {
				t.Fatal("mutation trusted unsafe storage")
			}
			if raw, _ := os.ReadFile(sentinel); !bytes.Equal(raw, []byte("sentinel")) {
				t.Fatal("unsafe path changed external file")
			}
		})
	}
}
func TestLocalRegistryLockCancellationAndReadOnlyInspection(t *testing.T) {
	a, root, home := localRegistryFixture(t)
	if _, err := a.observeLocalProject("v1", localRegistryNow()); err != nil {
		t.Fatal(err)
	}
	lock, err := os.OpenFile(filepath.Join(filepath.Dir(localRegistryInventory(home)), "inventory.lock"), os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	if rows, err := a.localProjects(); err != nil || len(rows) != 1 {
		t.Fatalf("inspection blocked by writer lock: %+v %v", rows, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	a.ctx = ctx
	if err := a.updateLocalProject(root, func(p *localProject) error { p.Status = "should not persist"; return nil }); err == nil {
		t.Fatal("ignored lock cancellation")
	}
}

func TestLocalRegistryNamedStateDoesNotOverwriteUnsafeTargets(t *testing.T) {
	a, _, home := localRegistryFixture(t)
	if _, err := a.readLocalState("enrollments.json"); !os.IsNotExist(err) {
		t.Fatalf("missing state read: %v", err)
	}
	if _, err := os.Stat(home); !os.IsNotExist(err) {
		t.Fatal("read created HOME")
	}
	if err := a.writeLocalState("enrollments.json", []byte("first")); err != nil {
		t.Fatal(err)
	}
	raw, err := a.readLocalState("enrollments.json")
	if err != nil || string(raw) != "first" {
		t.Fatalf("named read: %q %v", raw, err)
	}
	dir, err := a.localRegistryDirectory(false)
	if err != nil {
		t.Fatal(err)
	}
	defer dir.Close()
	target := filepath.Join(dir.Name(), "enrollments.json")
	if err := os.Chmod(target, 0644); err != nil {
		t.Fatal(err)
	}
	if err := a.writeLocalState("enrollments.json", []byte("second")); err == nil {
		t.Fatal("overwrote unsafe state")
	}
	for _, name := range []string{"", "../outside", "/outside", "inventory.json", "inventory.lock", "inventory.lock.extra", "a/b", "bad\nname", "enrollments.json.lock"} {
		if _, err := a.readLocalState(name); err == nil {
			t.Fatalf("read accepted reserved/unsafe %q", name)
		}
		if err := a.writeLocalState(name, []byte("bad")); err == nil {
			t.Fatalf("write accepted reserved/unsafe %q", name)
		}
	}
	unlock, err := a.lockLocalState(context.Background(), "migration")
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if unlock, err := a.lockLocalState(ctx, "migration"); err == nil {
		unlock()
		t.Fatal("named lock failed to serialize")
	}
}

func TestLocalRegistryNamedLockAcceptsExplicitLockFilename(t *testing.T) {
	a, _, _ := localRegistryFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	unlock, err := a.lockLocalState(ctx, "repair.lock")
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	dir, err := a.localRegistryDirectory(false)
	if err != nil {
		t.Fatal(err)
	}
	defer dir.Close()
	if _, err := os.Stat(filepath.Join(dir.Name(), "repair.lock")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir.Name(), "repair.lock.lock")); !os.IsNotExist(err) {
		t.Fatal("added redundant lock suffix")
	}
}

func TestLocalRegistryArtifactFIFOIsRejectedWithoutBlocking(t *testing.T) {
	a, _, _ := localRegistryFixture(t)
	if _, err := a.observeLocalProject("v1", localRegistryNow()); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "fifo")
	if err := syscall.Mkfifo(path, 0600); err != nil {
		t.Fatal(err)
	}
	// Keep the FIFO open so a blocking implementation terminates and fails on
	// the regular-file check instead of hanging the entire suite.
	fd, err := syscall.Open(path, syscall.O_RDWR|syscall.O_NONBLOCK, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer syscall.Close(fd)
	if err := a.registerLocalArtifact(path, "codex-hooks", "v1", localRegistryNow()); err == nil {
		t.Fatal("accepted artifact FIFO")
	}
}

func TestLocalRegistryNamedStateReservesCaseFoldedInventoryNames(t *testing.T) {
	a, _, home := localRegistryFixture(t)
	for _, name := range []string{"Inventory.json", "INVENTORY.lock", "inventory-backup.json", "Enrollment.LOCK"} {
		if _, err := a.readLocalState(name); err == nil {
			t.Fatalf("read accepted reserved name %s", name)
		}
		if err := a.writeLocalState(name, []byte("bad")); err == nil {
			t.Fatalf("write accepted reserved name %s", name)
		}
	}
	if _, err := os.Stat(home); !os.IsNotExist(err) {
		t.Fatal("reserved name created state")
	}
}
