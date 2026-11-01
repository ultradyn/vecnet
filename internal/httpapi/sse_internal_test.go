package httpapi

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"
)

// stuckWriter is a ResponseWriter whose Write blocks until a write deadline
// in the past is set, like a TCP connection whose peer stopped reading.
type stuckWriter struct {
	hdr       http.Header
	mu        sync.Mutex
	unblock   chan struct{}
	unblocked bool
	writes    int
}

func newStuckWriter() *stuckWriter {
	return &stuckWriter{hdr: http.Header{}, unblock: make(chan struct{})}
}

func (w *stuckWriter) Header() http.Header { return w.hdr }
func (w *stuckWriter) WriteHeader(int)     {}
func (w *stuckWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	w.writes++
	w.mu.Unlock()
	<-w.unblock
	return 0, errors.New("write deadline exceeded")
}
func (w *stuckWriter) Flush() {}
func (w *stuckWriter) SetWriteDeadline(t time.Time) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !t.IsZero() && !t.After(time.Now()) && !w.unblocked {
		w.unblocked = true
		close(w.unblock)
	}
	return nil
}

// TestSSEDropsStuckConsumer pins Review Focus 4 at the writer: frames queue
// while the connection is stuck, a full queue that makes no room for the
// stall timeout turns into a refusal (the handler then drops the connection)
// and close unblocks the stuck write so the handler goroutine can exit.
func TestSSEDropsStuckConsumer(t *testing.T) {
	const stall = 50 * time.Millisecond
	ctx := context.Background()
	w := newStuckWriter()
	c := startSSE(w, stall)
	if got := w.hdr.Get("Content-Type"); got != "text/event-stream" {
		t.Fatalf("Content-Type = %q", got)
	}
	refused := make(chan int, 1)
	start := time.Now()
	go func() {
		for i := 0; i < 10*sseBuffer; i++ {
			if !c.send(ctx, []byte("data: x\n\n")) {
				refused <- i
				return
			}
		}
		refused <- -1
	}()
	select {
	case i := <-refused:
		if i < 0 || i > sseBuffer+1 {
			t.Fatalf("send refused at frame %d, want once the %d-frame buffer is full", i, sseBuffer)
		}
		if took := time.Since(start); took < stall {
			t.Fatalf("send refused after %v, want it to wait the %v stall first", took, stall)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("send blocked on a stuck consumer")
	}
	closed := make(chan struct{})
	go func() { c.close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(10 * time.Second):
		t.Fatal("close did not unblock the stuck writer")
	}
	if c.send(ctx, []byte("data: y\n\n")) {
		t.Fatal("send after close succeeded")
	}
}

// TestSSESendWaitsForRoomAndHonoursCtx: with a full queue, send returns as
// soon as ctx is done rather than waiting out the stall.
func TestSSESendWaitsForRoomAndHonoursCtx(t *testing.T) {
	w := newStuckWriter()
	c := startSSE(w, time.Hour)
	defer c.close()
	ctx, cancel := context.WithCancel(context.Background())
	// One frame is taken by the stuck writer; fill the queue behind it.
	for i := 0; i <= sseBuffer; i++ {
		if !c.send(ctx, []byte("data: x\n\n")) {
			t.Fatalf("send %d refused before the queue was full", i)
		}
	}
	res := make(chan bool, 1)
	go func() { res <- c.send(ctx, []byte("data: x\n\n")) }()
	select {
	case ok := <-res:
		t.Fatalf("send on a full queue returned %v without waiting", ok)
	case <-time.After(50 * time.Millisecond):
	}
	cancel()
	select {
	case ok := <-res:
		if ok {
			t.Fatal("send succeeded after ctx was cancelled")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("send ignored ctx cancellation")
	}
}
