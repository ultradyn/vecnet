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

	dmu    sync.Mutex // orders the writer's deadlines with close's
	closed bool
}

// sseWriteTimeout bounds one frame's write and flush. The server's
// WriteTimeout covers a whole response, which a stream outlives, so each
// frame gets its own deadline instead (PROTOCOL §19.3).
const sseWriteTimeout = 30 * time.Second

// holdOpen lifts the server's read deadline for a long-lived response:
// net/http's background read would otherwise time out at ReadTimeout and
// cancel the request's context. write is the new write deadline (zero
// lifts it).
func holdOpen(rc *http.ResponseController, write time.Time) {
	_ = rc.SetReadDeadline(time.Time{})
	_ = rc.SetWriteDeadline(write)
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
	rc := http.NewResponseController(w)
	holdOpen(rc, time.Now().Add(sseWriteTimeout))
	w.WriteHeader(http.StatusOK)
	c := &sseConn{
		w:      w,
		rc:     rc,
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
			c.dmu.Lock()
			if c.closed {
				c.dmu.Unlock()
				return
			}
			_ = c.rc.SetWriteDeadline(time.Now().Add(sseWriteTimeout))
			c.dmu.Unlock()
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
		c.dmu.Lock()
		c.closed = true
		_ = c.rc.SetWriteDeadline(time.Now())
		c.dmu.Unlock()
		<-c.done
	})
}
