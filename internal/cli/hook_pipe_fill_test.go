//go:build linux || darwin || dragonfly || freebsd || netbsd || openbsd

package cli

import (
	"bytes"
	"os"
	"testing"
	"time"
)

// fillPipe fills a pipe's buffer completely through its write deadline.
func fillPipe(t *testing.T, writer *os.File) int {
	t.Helper()
	filled := 0
	until := time.Now().Add(5 * time.Second)
	for {
		if err := writer.SetWriteDeadline(time.Now().Add(200 * time.Millisecond)); err != nil {
			t.Skipf("pipe deadlines unavailable: %v", err)
		}
		n, err := writer.Write(bytes.Repeat([]byte("x"), 1<<20))
		filled += n
		if !os.IsTimeout(err) {
			t.Fatalf("prefill: n=%d err=%v", filled, err)
		}
		if n == 0 && filled > 0 {
			break
		}
		if time.Now().After(until) {
			t.Fatal("could not fill output pipe")
		}
	}
	if err := writer.SetWriteDeadline(time.Time{}); err != nil {
		t.Fatal(err)
	}
	return filled
}
