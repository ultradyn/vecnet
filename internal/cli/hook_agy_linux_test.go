//go:build linux

package cli

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"
)

// agyNameSelf makes this test binary identify as the agy process, so the
// sh -c → test-binary launch chain stands in for agy → sh -c → vecnet.
func agyNameSelf(t *testing.T) {
	t.Helper()
	old := agyProcNames
	names := append([]string{filepath.Base(os.Args[0])}, old...)
	agyProcNames = names
	t.Cleanup(func() { agyProcNames = old })
}

// fakeProc is one entry of a fake /proc tree: its parent pid, comm value
// and exe symlink target ("" for none). environ never exists.
type fakeProc struct {
	ppid int
	comm string
	exe  string
}

// fakeProcTree redirects the agy ancestor walk to a throwaway tree.
func fakeProcTree(t *testing.T, procs map[int]fakeProc) {
	t.Helper()
	root := t.TempDir()
	for pid, p := range procs {
		dir := filepath.Join(root, strconv.Itoa(pid))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		stat := fmt.Sprintf("%d (%s) S %d 1 1 0 -1 4194304 1 1 1 0", pid, p.comm, p.ppid)
		if err := os.WriteFile(filepath.Join(dir, "stat"), []byte(stat+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "comm"), []byte(p.comm+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if p.exe != "" {
			if err := os.Symlink(p.exe, filepath.Join(dir, "exe")); err != nil {
				t.Fatal(err)
			}
		}
	}
	old := agyProcFS
	agyProcFS = root
	t.Cleanup(func() { agyProcFS = old })
}

// The agy process is found by name, not by a fixed ancestor depth: bash
// execs the hook in place (agy is the parent), dash keeps an sh parent
// (agy is the grandparent), and anything deeper or unnamed is not agy.
func TestAgyHookProcWalksNamedAncestors(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the agy ancestor walk reads Linux /proc")
	}
	agyBin := filepath.Join(t.TempDir(), "agy")
	if err := os.WriteFile(agyBin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Run("dash-style sh then agy", func(t *testing.T) {
		fakeProcTree(t, map[int]fakeProc{
			99: {ppid: 98, comm: "sh", exe: "/usr/bin/dash"},
			98: {ppid: 97, comm: "agy", exe: agyBin},
			97: {ppid: 1, comm: "zsh", exe: "/usr/bin/zsh"},
		})
		if p := agyProcAt(99); p.PID != 98 {
			t.Fatalf("walk found %+v, want the agy pid 98", p)
		}
	})
	t.Run("bash-style agy directly", func(t *testing.T) {
		fakeProcTree(t, map[int]fakeProc{
			98: {ppid: 97, comm: "agy", exe: agyBin},
			97: {ppid: 1, comm: "zsh", exe: "/usr/bin/zsh"},
		})
		if p := agyProcAt(98); p.PID != 98 {
			t.Fatalf("walk found %+v, want the agy pid 98", p)
		}
	})
	t.Run("exe basename only", func(t *testing.T) {
		// A renamed comm must still match through the exe basename.
		fakeProcTree(t, map[int]fakeProc{
			99: {ppid: 98, comm: "sh", exe: "/usr/bin/dash"},
			98: {ppid: 97, comm: "agy-wrapped", exe: agyBin},
		})
		if p := agyProcAt(99); p.PID != 98 {
			t.Fatalf("walk found %+v, want the agy pid 98", p)
		}
	})
	t.Run("no agy ancestor", func(t *testing.T) {
		fakeProcTree(t, map[int]fakeProc{
			99: {ppid: 98, comm: "sh", exe: "/usr/bin/dash"},
			98: {ppid: 97, comm: "bash", exe: "/usr/bin/bash"},
			97: {ppid: 1, comm: "zsh", exe: "/usr/bin/zsh"},
		})
		if p := agyProcAt(99); p != (hookProc{}) {
			t.Fatalf("walk matched %+v, want the zero proc", p)
		}
	})
	t.Run("past three ancestors", func(t *testing.T) {
		fakeProcTree(t, map[int]fakeProc{
			99: {ppid: 98, comm: "sh", exe: "/usr/bin/dash"},
			98: {ppid: 97, comm: "bash", exe: "/usr/bin/bash"},
			97: {ppid: 96, comm: "zsh", exe: "/usr/bin/zsh"},
			96: {ppid: 1, comm: "agy", exe: agyBin},
		})
		if p := agyProcAt(99); p != (hookProc{}) {
			t.Fatalf("walk reached past three ancestors: %+v", p)
		}
	})
	if agyProcAt(0).PID != 0 || agyProcAt(1).PID != 0 {
		t.Fatal("a non-process pid must give the zero proc")
	}
}

// The agy process identity is an ancestor found by name: under sh -c this
// test binary stands in for agy, and a reused or unknown pid never matches.
func TestAgyHookProcAncestorUnderSh(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the agy ancestor lookup reads Linux /proc")
	}
	agyNameSelf(t)
	var out bytes.Buffer
	cmd := agyChild(t, "proc", nil, "", "")
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		t.Fatalf("proc child: %v", err)
	}
	var pid int
	var start, host string
	if _, err := fmt.Sscanf(out.String(), "%d %s %s", &pid, &start, &host); err != nil {
		t.Fatalf("proc child output=%q: %v", out.String(), err)
	}
	self, _ := os.Hostname()
	if pid != os.Getpid() || start != procStart(os.Getpid()) || host != self {
		t.Fatalf("ancestor=(%d,%q,%q), want this test binary (%d,%q,%q)", pid, start, host, os.Getpid(), procStart(os.Getpid()), self)
	}
	if (hookProc{PID: 0}).same(hookProc{PID: 0}) {
		t.Fatal("zero proc must never match")
	}
}
