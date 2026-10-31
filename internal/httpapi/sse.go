package httpapi

import (
	"net/http"
	"sync"
	"time"
)

// sseBuffer is the per-connection frame queue. When it is full the consumer
// is not keeping up and the handler drops the connection; the client resumes
// from its cursor (Review Focus 4).
const sseBuffer = 64

// sseConn owns the writing side of one SSE response. Frames go through a
// buffered channel to a single writer goroutine, so the producer (the
// handler reading the log) never blocks on a slow or vanished client.
type sseConn struct {
	w      http.ResponseWriter
	rc     *http.ResponseController
	frames chan []byte
	stop   chan struct{} // closed by close
	done   chan struct{} // closed when the writer goroutine exits
	once   sync.Once
}

// startSSE writes the SSE response headers (PROTOCOL §2.3) and starts the
// writer goroutine. Call close before the handler returns.
func startSSE(w http.ResponseWriter) *sseConn {
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Accel-Buffering", "no")
	h.Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	c := &sseConn{
		w:      w,
		rc:     http.NewResponseController(w),
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

// send queues one frame without blocking. It reports false when the queue is
// full or the writer has stopped: the handler must then drop the connection.
func (c *sseConn) send(frame []byte) bool {
	select {
	case <-c.done:
		return false
	default:
	}
	select {
	case c.frames <- frame:
		return true
	default:
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
