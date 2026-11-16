//go:build windows

package localruntime

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/platform"
)

func TestWindowsRuntimeLockHelper(t *testing.T) {
	path := os.Getenv("VECNET_TEST_RUNTIME_LOCK")
	if path == "" {
		return
	}
	release, e := lock(context.Background(), path)
	if e != nil {
		t.Fatal(e)
	}
	defer release()
	fmt.Println("locked")
	_, _ = bufio.NewReader(os.Stdin).ReadByte()
}
func TestWindowsRuntimeLockExcludesProcessesAndRecovers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "install.lock")
	c := exec.Command(os.Args[0], "-test.run=^TestWindowsRuntimeLockHelper$")
	c.Env = append(os.Environ(), "VECNET_TEST_RUNTIME_LOCK="+path)
	out, e := c.StdoutPipe()
	if e != nil {
		t.Fatal(e)
	}
	in, e := c.StdinPipe()
	if e != nil {
		t.Fatal(e)
	}
	c.Stderr = os.Stderr
	if e = c.Start(); e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = in.Close(); _ = c.Process.Kill(); _ = c.Wait() })
	if line, e := bufio.NewReader(out).ReadString('\n'); e != nil || line != "locked\n" {
		t.Fatalf("lock helper %q %v", line, e)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 75*time.Millisecond)
	defer cancel()
	release, e := lock(ctx, path)
	if release != nil {
		release()
		t.Fatal("second process acquired runtime lock")
	}
	if !errors.Is(e, context.DeadlineExceeded) {
		t.Fatalf("lock contention %v", e)
	}
	// The open handle also prevents unlink/recreate from forking the lock domain.
	if e = os.Remove(path); e == nil {
		t.Fatal("held runtime lock could be replaced")
	}
	if e = c.Process.Kill(); e != nil {
		t.Fatal(e)
	}
	_ = c.Wait()
	release, e = lock(context.Background(), path)
	if e != nil {
		t.Fatal(e)
	}
	release()
	if e = platform.CheckPath(path, false, true); e != nil {
		t.Fatal(e)
	}
}
func TestWindowsRuntimeRejectsHardlinksAndJunctions(t *testing.T) {
	o, _ := windowsFixture(t)
	if e := platform.MkdirAll(root(o.Home), 0700); e != nil {
		t.Fatal(e)
	}
	p := filepath.Join(root(o.Home), manifestName)
	f, e := platform.OpenFile(p, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		t.Fatal(e)
	}
	f.Write([]byte("{}"))
	f.Close()
	if e = os.Link(p, filepath.Join(root(o.Home), "manifest-alias.json")); e != nil {
		t.Fatal(e)
	}
	if _, e = Read(o.Home); e == nil {
		t.Fatal("hard-linked metadata accepted")
	}
	// Junctions require no developer-mode symlink privilege. Never remove a
	// computed recursive target: the exact temporary link is deleted nonrecursively.
	home := t.TempDir()
	outside := t.TempDir()
	if e = platform.MkdirAll(filepath.Join(home, ".local/share"), 0700); e != nil {
		t.Fatal(e)
	}
	junction := root(home)
	sys, e := systemDirectory()
	if e != nil {
		t.Fatal(e)
	}
	// cmd is used only for mklink (no delete/move or interpolated user text).
	c := exec.Command(filepath.Join(sys, "cmd.exe"), "/d", "/c", "mklink", "/J", junction, outside)
	if out, e := c.CombinedOutput(); e != nil {
		t.Skipf("junction unavailable %v %s", e, out)
	}
	defer os.Remove(junction)
	o.Home = home
	if _, e = Ensure(context.Background(), o); e == nil {
		t.Fatal("reparse runtime directory accepted")
	}
	entries, e := os.ReadDir(outside)
	if e != nil || len(entries) != 0 {
		t.Fatalf("wrote outside HOME %v %v", entries, e)
	}
}
