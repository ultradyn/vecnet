//go:build linux

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
)

func TestLockProcessHelper(t *testing.T) {
	path := os.Getenv("VECNET_TEST_INSTALL_LOCK")
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
func TestInstallationLockExcludesOtherProcesses(t *testing.T) {
	path := filepath.Join(t.TempDir(), "install.lock")
	c := exec.Command(os.Args[0], "-test.run=^TestLockProcessHelper$")
	c.Env = []string{"VECNET_TEST_INSTALL_LOCK=" + path}
	output, e := c.StdoutPipe()
	if e != nil {
		t.Fatal(e)
	}
	input, e := c.StdinPipe()
	if e != nil {
		t.Fatal(e)
	}
	c.Stderr = os.Stderr
	if e = c.Start(); e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = input.Close(); _ = c.Process.Kill(); _ = c.Wait() })
	reader := bufio.NewReader(output)
	if s, e := reader.ReadString('\n'); e != nil || s != "locked\n" {
		t.Fatalf("lock child: %q %v", s, e)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	release, e := lock(ctx, path)
	if release != nil {
		release()
		t.Fatal("concurrent process acquired installation lock")
	}
	if !errors.Is(e, context.DeadlineExceeded) {
		t.Fatalf("lock contention: %v", e)
	}
	if e = input.Close(); e != nil {
		t.Fatal(e)
	}
	if e = c.Wait(); e != nil {
		t.Fatal(e)
	}
	release, e = lock(context.Background(), path)
	if e != nil {
		t.Fatal(e)
	}
	release()
}
