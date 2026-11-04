package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/eventlog"
	"github.com/XertroV/vecnet/internal/hub"
)

// Defaults for GET /v1/events (PROTOCOL §2.3); Deps fields override them.
const (
	DefaultHeartbeat = 15 * time.Second // SSE ": hb" interval while idle
	DefaultWait      = 25 * time.Second // long-poll hold when ?wait= is absent
	MaxWait          = 30 * time.Second // long-poll hold cap, kept under proxy timeouts
	DefaultSSEStall  = 15 * time.Second // SSE queue-full wait before a stuck client is dropped
)

// eventsReadLimit is the batch size of one log read.
const eventsReadLimit = 500

// activeStreams counts running SSE handlers (tests watch it fall to zero
// once clients go away).
var activeStreams atomic.Int64

func (s *server) heartbeat() time.Duration {
	if s.Heartbeat > 0 {
		return s.Heartbeat
	}
	return DefaultHeartbeat
}

func (s *server) sseStall() time.Duration {
	if s.SSEStall > 0 {
		return s.SSEStall
	}
	return DefaultSSEStall
}

// waitFor converts the ?wait= query (seconds) into a hold duration:
// absent → DefaultWait, capped at MaxWait; WaitUnit scales one second.
func (s *server) waitFor(q string) (time.Duration, error) {
	maxWait, def, unit := s.MaxWait, s.DefaultWait, s.WaitUnit
	if maxWait <= 0 {
		maxWait = MaxWait
	}
	if def <= 0 {
		def = DefaultWait
	}
	if unit <= 0 {
		unit = time.Second
	}
	if q == "" {
		return min(def, maxWait), nil
	}
	n, err := strconv.Atoi(q)
	if err != nil || n < 0 {
		e := hub.E(http.StatusUnprocessableEntity, "invalid_value", "wait must be a non-negative number of seconds")
		e.Details["field"] = "wait"
		return 0, e
	}
	if time.Duration(n) > maxWait/unit {
		return maxWait, nil
	}
	return time.Duration(n) * unit, nil
}

// getEvents is GET /v1/events (PROTOCOL §2.3): SSE when Accept names
// text/event-stream, else long-poll JSON.
func (s *server) getEvents(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	c := callerFrom(ctx)
	owners, ackOwner, ackScope, err := s.Hub.StreamOwners(ctx, c, r.URL.Query().Get("peer"))
	if errors.Is(err, hub.ErrPeerDisabled) {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if err != nil {
		writeError(w, err)
		return
	}
	cur, err := s.startCursor(ctx, r, owners, ackOwner, ackScope)
	if err != nil {
		writeError(w, err)
		return
	}
	sse := strings.Contains(r.Header.Get("Accept"), "text/event-stream")
	var wait time.Duration
	if !sse {
		if wait, err = s.waitFor(r.URL.Query().Get("wait")); err != nil {
			writeError(w, err)
			return
		}
	}
	if !sse {
		// A long poll holds the request for up to wait: lift the server's
		// read deadline and give the write the hold plus a margin (§20.3).
		holdOpen(http.NewResponseController(w), time.Now().Add(wait+sseWriteTimeout))
	}
	// The first read validates the cursor (409 before any 200 is written).
	first, err := s.Hub.Log.Read(ctx, owners, cur, eventsReadLimit)
	if err != nil {
		writeError(w, err)
		return
	}
	// A stream outlives the check that let it in, so it repeats that check.
	peer, token := r.URL.Query().Get("peer"), bearer(r)
	alive := func(ctx context.Context) bool {
		c, err := s.Hub.Auth.Resolve(ctx, token)
		if err != nil {
			return false
		}
		now, _, _, err := s.Hub.StreamOwners(ctx, c, peer)
		return err == nil && slices.Equal(now, owners)
	}
	if sse {
		s.streamSSE(w, r, c.PrincipalID, owners, cur, first, alive)
		return
	}
	s.longPoll(w, r, c.PrincipalID, owners, cur, first, wait, alive)
}

// startCursor picks the cursor (PROTOCOL §2.3): Last-Event-ID, else ?after=,
// else the stream's acked cursor; both given and different is 400
// cursor_mismatch. A stream that has never acked has acked nothing, so it
// starts at the oldest retained event: a consumer's first read sees what
// arrived before it connected (hold deliveries wait for exactly that), and
// clients never need to build a cursor. after=head ("from now") resolves to
// the owners' current cursor.
func (s *server) startCursor(ctx context.Context, r *http.Request, owners []string, ackOwner, ackScope string) (eventlog.Cursor, error) {
	lei := r.Header.Get("Last-Event-ID")
	after := r.URL.Query().Get("after")
	if lei != "" && after != "" && lei != after {
		e := hub.E(http.StatusBadRequest, "cursor_mismatch", "Last-Event-ID and ?after= differ")
		e.Details["last_event_id"], e.Details["after"] = lei, after
		return eventlog.Cursor{}, e
	}
	raw, field := lei, "Last-Event-ID"
	if raw == "" {
		raw, field = after, "after"
	}
	cur := eventlog.Head
	switch {
	case raw == "head":
	case raw != "":
		c, err := eventlog.ParseCursor(raw)
		if err != nil {
			e := hub.E(http.StatusUnprocessableEntity, "invalid_value", "cursor must look like e<epoch>.<seq>")
			e.Details["field"] = field
			return eventlog.Cursor{}, e
		}
		cur = c
	default:
		c, ok, err := s.Hub.Log.Acked(ctx, ackOwner, ackScope)
		if err != nil {
			return eventlog.Cursor{}, err
		}
		if !ok {
			if c, err = s.Hub.Log.Oldest(ctx); err != nil {
				return eventlog.Cursor{}, err
			}
		}
		cur = c
	}
	if cur == eventlog.Head {
		return s.Hub.Log.Current(ctx, owners)
	}
	return cur, nil
}

// pollBody is the long-poll response [EX06].
type pollBody struct {
	Events        []envelope.Event `json:"events"`
	NextCursor    string           `json:"next_cursor"`
	HubRestarting *restartHint     `json:"hub_restarting,omitempty"`
}

type restartHint struct {
	RetryAfterMS int64 `json:"retry_after_ms"`
}

// longPoll answers at once when first has events, else holds until an
// append for the owners, a change to the credential or wait elapses; once
// alive reports false it answers empty, and the next poll meets the new state.
func (s *server) longPoll(w http.ResponseWriter, r *http.Request, prn string, owners []string, cur eventlog.Cursor, first []envelope.Event, wait time.Duration, alive func(context.Context) bool) {
	drain, done := s.Drain.join()
	defer done()
	ctx := r.Context()
	var restarting *restartHint
	evs := first
	if len(evs) == 0 && wait > 0 {
		timer := time.NewTimer(wait)
		defer timer.Stop()
	hold:
		for {
			wctx, cancel := context.WithCancel(ctx)
			woke := s.Hub.Log.Wait(wctx, waitKeys(prn, owners))
			if !alive(ctx) {
				cancel()
				evs = nil
				break hold
			}
			// Re-read after registering, so an append that landed between
			// the first read and Wait is not missed.
			var err error
			evs, err = s.Hub.Log.Read(ctx, owners, cur, eventsReadLimit)
			if err != nil || len(evs) > 0 {
				cancel()
				if err != nil {
					writeError(w, err)
					return
				}
				break hold
			}
			select {
			case <-woke:
				cancel()
				if ctx.Err() != nil {
					return
				}
			case <-timer.C:
				cancel()
				break hold
			case <-drain.C():
				cancel()
				evs = []envelope.Event{}
				restarting = &restartHint{RetryAfterMS: drain.RetryAfter().Milliseconds()}
				break hold
			case <-s.Stop: // nil (never) unless the server is shutting down
				cancel()
				break hold
			}
		}
	}
	next := cur.String()
	if len(evs) > 0 {
		next = evs[len(evs)-1].Cursor
	}
	writeJSON(w, http.StatusOK, pollBody{Events: evs, NextCursor: next, HubRestarting: restarting})
}

// waitKeys are what a stream waits on: its owners' logs, and the principal,
// which is notified when its credential or peer set changes.
func waitKeys(prn string, owners []string) []string {
	return slices.Concat([]string{prn}, owners)
}

// streamSSE serves the SSE mode: retry and priming frames, replay from cur,
// then tail with heartbeats. It exits when the client goes away, when the
// server shuts down (Deps.Stop), when a write fails, when the frame queue stays
// full for the stall timeout, or when alive reports the credential gone or the
// owners changed: rotation, a move and disabling all reach an open stream that
// way, and the client's reconnect meets the new state. A client that keeps
// reading gets backpressure, so a replay larger than the queue completes on one
// connection. No database transaction is open while frames are queued or
// written: reads finish before their frames are queued.
func (s *server) streamSSE(w http.ResponseWriter, r *http.Request, prn string, owners []string, cur eventlog.Cursor, first []envelope.Event, alive func(context.Context) bool) {
	drain, done := s.Drain.join()
	defer done()
	activeStreams.Add(1)
	defer activeStreams.Add(-1)
	defer s.streamReach(owners)() // connected while open; runs before the counter drops
	// The stream ends when the client goes away or the server shuts down.
	ctx, stop := context.WithCancel(r.Context())
	defer stop()
	if s.Stop != nil {
		go func() {
			select {
			case <-s.Stop:
				stop()
			case <-ctx.Done():
			}
		}()
	}
	conn := startSSE(w, s.sseStall())
	defer conn.close()
	conn.draining = drain.C()
	// A drain can also interrupt send while the replay queue is full. In
	// that case the handler exits before reaching its idle select.
	defer func() {
		select {
		case <-conn.draining:
			conn.finish(restartFrame(drain.RetryAfter()), drainFlush)
		default:
		}
	}()

	if !conn.send(ctx, []byte("retry: 3000\n\n")) || !conn.send(ctx, []byte("id: "+cur.String()+"\ndata:\n\n")) {
		return
	}
	// emit queues evs and advances cur; false means drop the connection.
	emit := func(evs []envelope.Event) bool {
		for _, ev := range evs {
			f, err := sseFrame(ev)
			if err != nil || !conn.send(ctx, f) {
				return false
			}
			c, err := eventlog.ParseCursor(ev.Cursor)
			if err != nil {
				return false
			}
			cur = c
		}
		return true
	}
	if !emit(first) {
		return
	}
	hb := time.NewTicker(s.heartbeat())
	defer hb.Stop()
	for {
		if !s.sseTail(ctx, conn, prn, owners, &cur, hb.C, emit, alive) {
			return
		}
	}
}

// sseTail registers a wakeup, checks the stream may go on, replays everything
// after *cur (the read runs after the registration, so no append is missed),
// then waits for an append, a heartbeat tick, the client leaving or the writer
// failing. Every wake and every tick comes back through here, so no frame is
// read for a credential or an owner set that has gone. It reports false when
// the stream must end.
func (s *server) sseTail(ctx context.Context, conn *sseConn, prn string, owners []string, cur *eventlog.Cursor, tick <-chan time.Time, emit func([]envelope.Event) bool, alive func(context.Context) bool) bool {
	wctx, cancel := context.WithCancel(ctx)
	defer cancel() // unregisters the waiter
	woke := s.Hub.Log.Wait(wctx, waitKeys(prn, owners))
	if !alive(ctx) {
		return false
	}
	for {
		evs, err := s.Hub.Log.Read(ctx, owners, *cur, eventsReadLimit)
		if err != nil {
			return false // cursor purged mid-stream or ctx done: the client reconnects
		}
		if !emit(evs) {
			return false
		}
		if len(evs) < eventsReadLimit {
			break
		}
	}
	select {
	case <-conn.draining:
		return false // streamSSE flushes the joined generation's final frame
	case <-ctx.Done():
		return false
	case <-conn.failed():
		return false
	case <-woke:
		return true
	case <-tick:
		return conn.send(ctx, []byte(": hb\n\n"))
	}
}

// sseFrame renders one Event as "id: <cursor>\nevent: <type>\ndata: <json>\n\n".
// The JSON is compact, so it holds no newline.
func sseFrame(ev envelope.Event) ([]byte, error) {
	data, err := json.Marshal(ev)
	if err != nil {
		return nil, err
	}
	var b bytes.Buffer
	b.Grow(len(data) + len(ev.Cursor) + len(ev.Type) + 24)
	b.WriteString("id: ")
	b.WriteString(ev.Cursor)
	b.WriteString("\nevent: ")
	b.WriteString(ev.Type)
	b.WriteString("\ndata: ")
	b.Write(data)
	b.WriteString("\n\n")
	return b.Bytes(), nil
}

// ackBody is the POST /v1/ack body.
type ackBody struct {
	Cursor string `json:"cursor"`
}

// postAck is POST /v1/ack (PROTOCOL §2.4): 204. ?peer= narrows a principal's
// ack to one of its peers, as on GET /v1/events.
func (s *server) postAck(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, err)
		return
	}
	body, err := decodeAck(raw)
	if err != nil {
		writeError(w, err)
		return
	}
	cur, err := eventlog.ParseCursor(body.Cursor)
	if err != nil {
		e := hub.E(http.StatusUnprocessableEntity, "invalid_value", "cursor must look like e<epoch>.<seq>")
		e.Details["field"] = "cursor"
		writeError(w, e)
		return
	}
	if err := s.Hub.Ack(r.Context(), callerFrom(r.Context()), r.URL.Query().Get("peer"), cur); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// decodeAck parses {"cursor": "<string>"}; anything else is 400
// invalid_body with the JSON pointer of the first violation.
func decodeAck(raw []byte) (ackBody, error) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil || top == nil {
		return ackBody{}, &envelope.SchemaError{Path: "", Message: "body must be a JSON object"}
	}
	for k := range top {
		if k != "cursor" {
			return ackBody{}, &envelope.SchemaError{Path: "/" + k, Message: "unknown field"}
		}
	}
	c, ok := top["cursor"]
	if !ok {
		return ackBody{}, &envelope.SchemaError{Path: "/cursor", Message: "cursor is required"}
	}
	var b ackBody
	if err := json.Unmarshal(c, &b.Cursor); err != nil {
		return ackBody{}, &envelope.SchemaError{Path: "/cursor", Message: "cursor must be a string"}
	}
	return b, nil
}
