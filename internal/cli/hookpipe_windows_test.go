//go:build windows

package cli

import (
	"bytes"
	"context"
	"io"
	"os"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func TestWindowsHookPipeCompleteOrNothing(t *testing.T) {
	r, w, e := os.Pipe()
	if e != nil {
		t.Fatal(e)
	}
	defer r.Close()
	defer w.Close()
	writer, ok := hookAtomicPipe(w)
	if !ok {
		t.Fatal("pipe not recognized")
	}
	data := []byte("{\"ok\":true}\n")
	if e = writer(context.Background(), data); e != nil {
		t.Fatal(e)
	}
	out := make([]byte, len(data))
	if _, e = io.ReadFull(r, out); e != nil || !bytes.Equal(out, data) {
		t.Fatalf("output %q %v", out, e)
	}
	quota, e := hookWindowsPipeRoom(w)
	if e != nil || quota == 0 {
		t.Fatalf("quota %d %v", quota, e)
	}
	var n uint32
	if e = windows.WriteFile(windows.Handle(w.Fd()), bytes.Repeat([]byte{'x'}, quota), &n, nil); e != nil || int(n) != quota {
		t.Fatalf("fill %d/%d %v", n, quota, e)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 35*time.Millisecond)
	defer cancel()
	if e = writer(ctx, data); e == nil {
		t.Fatal("full pipe reported successful output")
	}
	w.Close()
	all, e := io.ReadAll(r)
	if e != nil || len(all) != quota || bytes.Count(all, []byte{'x'}) != quota {
		t.Fatalf("partial output: len=%d expected=%d %v", len(all), quota, e)
	}
}
