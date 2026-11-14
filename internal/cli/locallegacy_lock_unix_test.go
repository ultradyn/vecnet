//go:build unix

package cli

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLocalLegacySourceLockSafety(t *testing.T) {
	for _, kind := range []string{"normal", "symlink lock", "writable lock", "hardlink lock", "symlink parent", "writable parent", "foreign owner", "outside home"} {
		t.Run(kind, func(t *testing.T) {
			home := localLegacyLockHome(t)
			parent := filepath.Join(home, "config")
			if e := os.Mkdir(parent, 0700); e != nil {
				t.Fatal(e)
			}
			path := filepath.Join(parent, "bindings.json")
			outside := filepath.Join(t.TempDir(), "victim")
			if e := os.WriteFile(outside, []byte("unchanged"), 0600); e != nil {
				t.Fatal(e)
			}
			switch kind {
			case "symlink lock":
				if e := os.Symlink(outside, path+".lock"); e != nil {
					t.Fatal(e)
				}
			case "writable lock":
				if e := os.WriteFile(path+".lock", nil, 0600); e != nil {
					t.Fatal(e)
				}
				if e := os.Chmod(path+".lock", 0666); e != nil {
					t.Fatal(e)
				}
			case "hardlink lock":
				if e := os.Link(outside, path+".lock"); e != nil {
					t.Fatal(e)
				}
			case "symlink parent":
				real := parent + ".real"
				if e := os.Rename(parent, real); e != nil {
					t.Fatal(e)
				}
				if e := os.Symlink(real, parent); e != nil {
					t.Fatal(e)
				}
			case "writable parent":
				if e := os.Chmod(parent, 0777); e != nil {
					t.Fatal(e)
				}
			case "foreign owner":
				old := localRegistryEUID
				localRegistryEUID = func() int { return os.Geteuid() + 1 }
				defer func() { localRegistryEUID = old }()
			case "outside home":
				path = outside
			}
			unlock, err := lockLegacySourceWith(context.Background(), home, path)
			if kind == "normal" {
				if err != nil {
					t.Fatal(err)
				}
				unlock()
				unlock()
			} else {
				if err == nil {
					unlock()
					t.Fatal("unsafe source lock accepted")
				}
			}
			raw, e := os.ReadFile(outside)
			if e != nil || string(raw) != "unchanged" {
				t.Fatalf("changed external file: %q %v", raw, e)
			}
		})
	}
}

func TestLocalLegacySourceLockStableAndBounded(t *testing.T) {
	home := localLegacyLockHome(t)
	path := filepath.Join(home, "bindings.json")
	unlock, e := lockLegacySourceWith(context.Background(), home, path)
	if e != nil {
		t.Fatal(e)
	}
	defer unlock()
	before, e := os.Lstat(path + ".lock")
	if e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	second, e := lockLegacySourceWith(ctx, home, path)
	if e == nil {
		second()
		t.Fatal("contending lock acquired")
	}
	unlock()
	next, e := lockLegacySourceWith(context.Background(), home, path)
	if e != nil {
		t.Fatal(e)
	}
	defer next()
	after, e := os.Lstat(path + ".lock")
	if e != nil || !os.SameFile(before, after) {
		t.Fatal("lock inode replaced")
	}
}

func TestLocalLegacySourceLockRejectsReplacementWhileWaiting(t *testing.T) {
	if _, err := os.Stat("/proc/self/fd"); err != nil {
		t.Skip("requires procfs descriptor synchronization")
	}
	home := localLegacyLockHome(t)
	path := filepath.Join(home, "bindings.json")
	lock := path + ".lock"
	held, err := lockLegacySourceWith(context.Background(), home, path)
	if err != nil {
		t.Fatal(err)
	}
	defer held()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	result := make(chan error, 1)
	go func() {
		unlock, err := lockLegacySourceWith(ctx, home, path)
		if err == nil {
			unlock()
		}
		result <- err
	}()
	deadline := time.Now().Add(time.Second)
	for {
		entries, err := os.ReadDir("/proc/self/fd")
		if err != nil {
			t.Fatal(err)
		}
		count := 0
		for _, entry := range entries {
			target, _ := os.Readlink(filepath.Join("/proc/self/fd", entry.Name()))
			if target == lock {
				count++
			}
		}
		if count >= 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("contender never opened lock")
		}
		time.Sleep(time.Millisecond)
	}
	if err := os.Rename(lock, lock+".old"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(lock, nil, 0600); err != nil {
		t.Fatal(err)
	}
	held()
	if err := <-result; err == nil {
		t.Fatal("accepted replaced lock inode")
	}
}

func localLegacyLockHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	if err := os.Chmod(home, 0700); err != nil {
		t.Fatal(err)
	}
	return home
}
