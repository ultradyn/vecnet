//go:build darwin || dragonfly || freebsd || netbsd || openbsd

package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"testing"
	"time"
)

// A native pipe with some room left and a reader that has stopped gets
// nothing until the whole output fits, then the complete output: never a
// truncated prefix (PROTOCOL §13.9 "Never block a turn").
func TestHookPipePartialRoomBSD(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	defer writer.Close()
	write, ok := hookAtomicPipe(writer)
	if !ok {
		t.Fatal("a native pipe has no all-or-nothing writer")
	}
	filled := fillPipe(t, writer)
	if _, err := io.ReadFull(reader, make([]byte, 1024)); err != nil {
		t.Fatal(err)
	}
	filled -= 1024
	data := append(bytes.Repeat([]byte("j"), 3000), '\n')
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if err := write(ctx, data); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("stalled write: %v", err)
	}
	// Drain the filler only; the output must not have started.
	got := make([]byte, filled)
	if _, err := io.ReadFull(reader, got); err != nil {
		t.Fatal(err)
	}
	if bytes.IndexByte(got, 'j') >= 0 {
		t.Fatal("a truncated prefix reached the pipe")
	}
	ctx2, cancel2 := context.WithTimeout(context.Background(), time.Second)
	defer cancel2()
	if err := write(ctx2, data); err != nil {
		t.Fatalf("write with room: %v", err)
	}
	_ = writer.Close()
	rest, err := io.ReadAll(reader)
	if err != nil || !bytes.Equal(rest, data) {
		t.Fatalf("read %d bytes, want the complete %d: %v", len(rest), len(data), err)
	}
}
