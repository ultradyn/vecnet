//go:build unix

package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// An existing bindings file whose rows another user could have edited is
// refused by every binding command, before any row is decoded or preserved,
// and is left as it was. An owner-owned 0644 file is still repaired to 0600
// (TestCodexBindFileMode).
func TestCodexBindRefusesUnsafeFile(t *testing.T) {
	commands := [][]string{
		{"--net", "p", "bind", "--thread", bindT2},
		{"--net", "q", "bind", "--thread", bindT1},
		{"--net", "q", "bind", "--thread", bindT1, "--renew"},
		{"--net", "p", "unbind"},
		{"bindings"},
	}
	for _, mode := range []os.FileMode{0666, 0660, 0620, 0602} {
		t.Run(fmt.Sprintf("mode=%o", mode), func(t *testing.T) {
			f := newBindFixture(t)
			f.bind("q", bindT1)
			if err := os.Chmod(f.path, mode); err != nil {
				t.Fatal(err)
			}
			for _, args := range commands {
				f.rejected("unsafe codex bindings file", args...)
			}
			if s, err := os.Stat(f.path); err != nil || s.Mode().Perm() != mode {
				t.Fatalf("unsafe file was rewritten: %v %v", s.Mode(), err)
			}
		})
	}
	t.Run("foreign owner", func(t *testing.T) {
		f := newBindFixture(t)
		f.bind("q", bindT1)
		defer func(old func() int) { codexBindingsEUID = old }(codexBindingsEUID)
		codexBindingsEUID = func() int { return os.Geteuid() + 1 }
		for _, args := range commands {
			f.rejected("owned by another user", args...)
		}
	})
	t.Run("fifo", func(t *testing.T) {
		f := newBindFixture(t)
		if err := os.MkdirAll(filepath.Dir(f.path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := syscall.Mkfifo(f.path, 0600); err != nil {
			t.Fatal(err)
		}
		done := make(chan string, 1)
		go func() {
			code, _, errs := f.run("--net", "p", "bind", "--thread", bindT2)
			done <- fmt.Sprint(code, errs)
		}()
		select {
		case got := <-done:
			if !strings.HasPrefix(got, "1") || !strings.Contains(got, "not a regular file") {
				t.Fatalf("fifo: %s", got)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("bind blocked on a FIFO bindings file")
		}
	})
}

// The per-home limit compares canonical homes on both sides: a home that was
// canonical at bind time and is now a symlink still counts against its target,
// and a stored home that no longer resolves keeps its slot (it is never freed
// silently) and is named in the refusal.
func TestCodexBindLimitCanonicalisesStoredHomes(t *testing.T) {
	f := newBindFixture(t)
	f.env["VECNET_CODEX_MAX_BINDINGS"] = "2"
	f.bind("p0", bindID(0))
	f.bind("p1", bindID(1))
	moved := filepath.Join(filepath.Dir(f.home), "moved")
	if err := os.Rename(f.home, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(moved, f.home); err != nil {
		t.Fatal(err)
	}
	f.rejected("binding limit 2 reached for "+moved, "--net", "p2", "bind", "--thread", bindID(2), "--codex-home", moved)
	f.rejected("binding limit 2 reached for "+moved, "--net", "p2", "bind", "--thread", bindID(2), "--codex-home", f.home)
	// Another home is unaffected while the stored homes resolve.
	other := filepath.Join(filepath.Dir(f.home), "other")
	if err := os.Mkdir(other, 0700); err != nil {
		t.Fatal(err)
	}
	f.bind("p3", bindID(3), "--codex-home", other)
	f.ok("--net", "p3", "unbind")
	// The stored home stops resolving: its bindings still hold their slots.
	if err := os.Remove(f.home); err != nil {
		t.Fatal(err)
	}
	f.rejected("no longer resolves", "--net", "p2", "bind", "--thread", bindID(2), "--codex-home", moved)
	// Expiry frees them as usual.
	f.now = f.now.Add(25 * time.Hour)
	f.bind("p2", bindID(2), "--codex-home", moved)
}
