//go:build !windows && !linux && !darwin && !dragonfly && !freebsd && !netbsd && !openbsd

package cli

import (
	"context"
	"io"
	"os"
	"testing"
)

// Without an all-or-nothing pipe write, a native pipe receives nothing.
func TestHookPipeOtherEmitsNothing(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	write, ok := hookAtomicPipe(writer)
	if !ok {
		t.Fatal("a native pipe kept the plain write")
	}
	if err := write(context.Background(), []byte("{}\n")); err == nil {
		t.Fatal("write reported success")
	}
	_ = writer.Close()
	if got, err := io.ReadAll(reader); err != nil || len(got) != 0 {
		t.Fatalf("pipe received %q: %v", got, err)
	}
}
