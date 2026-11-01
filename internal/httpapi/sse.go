package httpapi

import (
	"context"
	"net/http"
	"sync"
	"time"
)

// sseBuffer is the per-connection frame queue (Review Focus 4). A full queue
// holds the producer back while the writer drains it; only a queue that
// stays full for the stall timeout drops the connection, and the client
// resumes from its cursor.
const sseBuffer = 64

// sseConn owns the writing side of one SSE response. Frames go through a
// buffered channel to a single writer goroutine. The producer (the handler
// reading the log) waits at most stall for queue space, so a reading client
// gets backpressure while a stuck or vanished one is dropped. The log is
// pull-based: Notify never waits on any of this.
type sseConn struct {
	w      http.ResponseWriter
	rc     *http.ResponseController
	stall  time.Duration
	frames chan []byte
	stop   chan struct{} // closed by close
	done   chan struct{} // closed when the writer goroutine exits
	once   sync.Once
}

// startSSE writes the SSE response headers (PROTOCOL §2.3) and starts the
// writer goroutine. stall bounds how long send waits for queue space. Call
// close before the handler returns.
func startSSE(w http.ResponseWriter, stall time.Duration) *sseConn {
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Accel-Buffering", "no")
	h.Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	c := &sseConn{
		w:      w,
		rc:     http.NewResponseController(w),
		stall:  stall,
		frames: make(chan []byte, sseBuffer),
		stop:   make(chan struct{}),
		done:   make(chan struct{}),
	}
	go c.writer()
	return c
}

// writer writes and flushes each frame until close or a write error.
func (c *sseConn) writer() {
	defer close(c.done)
	for {
		select {
		case <-c.stop:
			return
		case f := <-c.frames:
			if _, err := c.w.Write(f); err != nil {
				return
			}
			if err := c.rc.Flush(); err != nil {
				return
			}
		}
	}
}

// send queues one frame, waiting while the queue is full. It reports false
// when the writer has stopped, ctx is done, or the queue made no room for
// stall (the consumer is stuck): the handler must then drop the connection.
func (c *sseConn) send(ctx context.Context, frame []byte) bool {
	select {
	case <-c.done:
		return false
	default:
	}
	select {
	case c.frames <- frame:
		return true
	default:
	}
	t := time.NewTimer(c.stall)
	defer t.Stop()
	select {
	case c.frames <- frame:
		return true
	case <-c.done:
		return false
	case <-ctx.Done():
		return false
	case <-t.C:
		return false
	}
}

// failed is closed when the writer stops on a write error.
func (c *sseConn) failed() <-chan struct{} { return c.done }

// close stops the writer and waits for it. A write stuck on a connection
// whose peer stopped reading is cut by a write deadline in the past, which
// also makes net/http drop the connection rather than reuse it.
func (c *sseConn) close() {
	c.once.Do(func() {
		close(c.stop)
		_ = c.rc.SetWriteDeadline(time.Now())
		<-c.done
	})
}
