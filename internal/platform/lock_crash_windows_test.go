//go:build windows

package platform

import (
	"bufio"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestKernelLockCrashHelper(t *testing.T) {
	p := os.Getenv("VECNET_TEST_LOCK_PATH")
	if p == "" {
		return
	}
	f, e := OpenFile(p, os.O_CREATE|os.O_RDWR, 0600)
	if e != nil {
		os.Exit(10)
	}
	ok, e := TryLock(f)
	if e != nil || !ok {
		os.Exit(11)
	}
	os.Stdout.WriteString("locked\n")
	time.Sleep(2 * time.Minute)
	os.Exit(12)
}
func TestKernelLockReleasedOnProcessDeath(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "private")
	if e := MkdirAll(dir, 0700); e != nil {
		t.Fatal(e)
	}
	p := filepath.Join(dir, "crash.lock")
	cmd := exec.Command(os.Args[0], "-test.run=^TestKernelLockCrashHelper$")
	cmd.Env = append(os.Environ(), "VECNET_TEST_LOCK_PATH="+p)
	r, e := cmd.StdoutPipe()
	if e != nil {
		t.Fatal(e)
	}
	if e = cmd.Start(); e != nil {
		t.Fatal(e)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	line, e := bufio.NewReader(r).ReadString('\n')
	if e != nil || line != "locked\n" {
		t.Fatalf("helper %q %v", line, e)
	}
	f, e := OpenFile(p, os.O_RDWR, 0600)
	if e != nil {
		t.Fatal(e)
	}
	defer f.Close()
	if ok, e := TryLock(f); ok || e != nil {
		t.Fatalf("concurrent acquire=%v %v", ok, e)
	}
	if e = cmd.Process.Kill(); e != nil {
		t.Fatal(e)
	}
	_ = cmd.Wait()
	if ok, e := TryLock(f); !ok || e != nil {
		t.Fatalf("crash lock persisted=%v %v", ok, e)
	}
	_ = Unlock(f)
}
