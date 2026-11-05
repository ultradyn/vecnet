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
	done   chan struct{} // closed when the writer goroutine exits
	once   sync.Once

	dmu    sync.Mutex // orders the writer's deadlines with close's
	closed bool       // close owns the write deadline while draining
	final  []byte
}

// sseWriteTimeout bounds one frame's write and flush. The server's
// WriteTimeout covers a whole response, which a stream outlives, so each
// frame gets its own deadline instead (PROTOCOL §20.3).
const sseWriteTimeout = 30 * time.Second

// holdOpen lifts the server's read deadline for a long-lived response:
// net/http's background read would otherwise time out at ReadTimeout and
// cancel the request's context. write is the new write deadline (zero
// lifts it).
func holdOpen(rc *http.ResponseController, write time.Time) {
	_ = rc.SetReadDeadline(time.Time{})
	_ = rc.SetWriteDeadline(write)
}

// drainWritesOnStop shortens a long poll's write deadline on shutdown, even
// when it has already left its wait and is blocked writing JSON. Join the
// watcher before returning the handler so it cannot touch a reused connection.
func drainWritesOnStop(rc *http.ResponseController, stop <-chan struct{}) func() {
	if stop == nil {
		return func() {}
	}
	finished, joined := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(joined)
		select {
		case <-stop:
			_ = rc.SetWriteDeadline(time.Now().Add(streamDrainTimeout))
		case <-finished:
		}
	}()
	return func() {
		close(finished)
		<-joined
	}
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
		done:   make(chan struct{}),
	}
	go c.writer()
	return c
}

// writer drains queued frames and the optional final frame before returning.
func (c *sseConn) writer() {
	defer close(c.done)
	for f := range c.frames {
		if !c.write(f) {
			return
		}
	}
	if len(c.final) > 0 {
		c.write(c.final)
	}
}

func (c *sseConn) write(frame []byte) bool {
	c.dmu.Lock()
	if !c.closed {
		_ = c.rc.SetWriteDeadline(time.Now().Add(sseWriteTimeout))
	}
	c.dmu.Unlock()
	if _, err := c.w.Write(frame); err != nil {
		return false
	}
	return c.rc.Flush() == nil
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

// streamDrainTimeout leaves ample time inside the hub's 10 s shutdown grace
// for net/http to finish the response and for the store to close.
const streamDrainTimeout = time.Second

// close drains healthy writers before returning. An expired deadline at handler
// return prevents net/http from writing its chunk terminator / HTTP2 END_STREAM,
// so always lift it after the writer has exited.
func (c *sseConn) close() { c.finish(nil) }

func (c *sseConn) closeForShutdown(ctx context.Context, stop <-chan struct{}) {
	if ctx.Err() == nil {
		select {
		case <-stop:
			// No id: the restart hint must not advance the delivery cursor.
			c.finish([]byte("event: hub_restarting\nretry: 1000\ndata: {\"retry_after_ms\":1000}\n\n"))
			return
		default:
		}
	}
	c.close()
}

func (c *sseConn) finish(final []byte) {
	c.once.Do(func() {
		c.dmu.Lock()
		c.closed = true
		c.final = final
		_ = c.rc.SetWriteDeadline(time.Now().Add(streamDrainTimeout))
		c.dmu.Unlock()
		close(c.frames)
		timer := time.NewTimer(streamDrainTimeout)
		defer timer.Stop()
		select {
		case <-c.done:
		case <-timer.C:
			// Also interrupts writers whose deadline support needs an explicit
			// expired deadline, rather than its own timer.
			_ = c.rc.SetWriteDeadline(time.Now())
			<-c.done
		}
		_ = c.rc.SetWriteDeadline(time.Time{})
	})
}
