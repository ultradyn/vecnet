package mcpserver

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"
)

// streamWriteTimeout bounds one SSE write, so a client that stopped reading
// fails the stream instead of pinning a goroutine.
const streamWriteTimeout = 30 * time.Second

// ErrStreamClosed is returned by Streamer methods after Close or a failed write.
var ErrStreamClosed = errors.New("mcpserver: stream closed")

// Streamer writes one text/event-stream response: the 2025-11-25 GET stream
// and, from Task 4, 2026-07-28 subscriptions/listen responses. It emits a
// ": hb" comment every Heartbeat. It never writes event ids for replay and
// never reads Last-Event-ID (PROTOCOL §4.1).
type Streamer struct {
	w  http.ResponseWriter
	rc *http.ResponseController

	mu     sync.Mutex // serialises writes
	err    error      // first write failure
	stop   chan struct{}
	dead   chan struct{} // closed on Close or the first write failure
	hbDone chan struct{}
	once   sync.Once
	dOnce  sync.Once
}

// Stream starts an SSE response on w: Content-Type text/event-stream,
// Cache-Control no-store, X-Accel-Buffering no, a "retry: 3000" field, then
// a ": hb" comment every s.Heartbeat until Close. When r is the request of a
// dispatched handler, ServeHTTP writes nothing after the handler returns.
func (s *Server) Stream(w http.ResponseWriter, r *http.Request) (*Streamer, error) {
	rc := http.NewResponseController(w)
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Accel-Buffering", "no")
	if st, ok := r.Context().Value(reqStateKey{}).(*reqState); ok {
		st.streamed = true
	}
	w.WriteHeader(http.StatusOK)
	st := &Streamer{w: w, rc: rc, stop: make(chan struct{}), dead: make(chan struct{}), hbDone: make(chan struct{})}
	if err := st.write("retry: 3000\n\n"); err != nil {
		close(st.hbDone)
		return nil, err
	}
	go st.heartbeat(s.heartbeat())
	return st, nil
}

func (st *Streamer) heartbeat(every time.Duration) {
	defer close(st.hbDone)
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-st.stop:
			return
		case <-st.dead:
			return
		case <-t.C:
			if st.write(": hb\n\n") != nil {
				return
			}
		}
	}
}

// write writes and flushes one frame under the write deadline.
func (st *Streamer) write(frame string) error {
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.err != nil {
		return st.err
	}
	select {
	case <-st.stop:
		return ErrStreamClosed
	default:
	}
	_ = st.rc.SetWriteDeadline(time.Now().Add(streamWriteTimeout))
	_, err := st.w.Write([]byte(frame))
	if err == nil {
		err = st.rc.Flush()
	}
	if err != nil {
		st.err = errors.Join(ErrStreamClosed, err)
		st.dOnce.Do(func() { close(st.dead) })
		return st.err
	}
	return nil
}

// Send writes one event. id and event are omitted when empty; data is JSON.
func (st *Streamer) Send(id, event string, data any) error {
	b, err := json.Marshal(data)
	if err != nil {
		return err
	}
	var sb strings.Builder
	if id != "" {
		sb.WriteString("id: " + oneLine(id) + "\n")
	}
	if event != "" {
		sb.WriteString("event: " + oneLine(event) + "\n")
	}
	sb.WriteString("data: ")
	sb.Write(b)
	sb.WriteString("\n\n")
	return st.write(sb.String())
}

// Comment writes an SSE comment line (": text").
func (st *Streamer) Comment(text string) error {
	return st.write(": " + oneLine(text) + "\n\n")
}

// Done is closed when the stream is closed or a write failed.
func (st *Streamer) Done() <-chan struct{} { return st.dead }

// Close stops the heartbeat; later writes fail with ErrStreamClosed. The
// handler returning ends the HTTP response.
func (st *Streamer) Close() {
	st.once.Do(func() {
		close(st.stop)
		st.dOnce.Do(func() { close(st.dead) })
	})
	<-st.hbDone
}

func oneLine(s string) string {
	return strings.NewReplacer("\r", " ", "\n", " ").Replace(s)
}
