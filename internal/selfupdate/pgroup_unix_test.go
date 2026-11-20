//go:build !windows

package selfupdate

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// N1: cancelling a command sends its process group SIGTERM first, so a
// child that traps it can clean up, and the run returns promptly.
func TestExecRunnerCancelSendsGroupSIGTERM(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh")
	}
	dir := t.TempDir()
	marker := filepath.Join(dir, "term")
	ready := filepath.Join(dir, "ready")
	// The child (a subshell, so a group member other than the leader)
	// records SIGTERM; the leader waits for it.
	script := `( trap 'echo term > "$1"; exit 0' TERM; : > "$2"; while :; do sleep 0.05; done ) & wait`
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := ExecRunner(ctx, "sh", []string{"-c", script, "sh", marker, ready}, []string{"PATH=/usr/bin:/bin"})
		done <- err
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("child never started")
		}
		time.Sleep(10 * time.Millisecond)
	}
	start := time.Now()
	cancel()
	select {
	case <-done:
	case <-time.After(StopGrace + 3*time.Second):
		t.Fatal("cancelled command did not return")
	}
	if time.Since(start) > StopGrace+2*time.Second {
		t.Fatalf("returned after %v", time.Since(start))
	}
	deadline = time.Now().Add(2 * time.Second)
	for {
		if b, err := os.ReadFile(marker); err == nil && strings.TrimSpace(string(b)) == "term" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the group member never got SIGTERM")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestExecStdoutSeparatesStreams(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh")
	}
	out, errOut, err := ExecStdout(context.Background(), "sh", []string{"-c", "echo out; echo err >&2"}, []string{"PATH=/usr/bin:/bin"})
	if err != nil || string(out) != "out\n" || string(errOut) != "err\n" {
		t.Fatalf("stdout %q stderr %q err %v", out, errOut, err)
	}
}
