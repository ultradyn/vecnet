package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/XertroV/vecnet/internal/envelope"
)

// Watch timing (PROTOCOL §13.1).
const (
	watchSilence      = 45 * time.Second // reconnect after this long without a byte (the hub sends ": hb" every 15 s)
	watchDefaultRetry = time.Second
	watchPollWait     = 25 // long-poll fallback hold, seconds
	watchSSEFailures  = 3  // consecutive SSE failures before alternating with long-poll
	dedupeWindow      = 1024
	maxSSELine        = 4 << 20
	monitorTextRunes  = 120
	peerNameRunes     = 32
)

const inboxHeader = "CURSOR TYPE THREAD FROM MSG DELIVERY TEXT"

// --- event lines ---

// eventFields are the parts of an Event the line formats print.
type eventFields struct {
	thread, sender, messageID, deliveryID, text string
	attachments                                 int
	// senderName is a delivery's send-time sender_info.name (PROTOCOL §2.2
	// step 12), preferred over the cached directory; "" when absent.
	senderName string
}

// nameOf is the display name for f's sender: the delivery's own
// sender_info, else the cached directory, else "".
func (f eventFields) nameOf(names map[string]string) string {
	if f.senderName != "" {
		return f.senderName
	}
	return names[f.sender]
}

// eventFieldsOf extracts eventFields from ev's data by type: a delivery's
// message; a receipt's state (by its "by" peer); a presence state.
func eventFieldsOf(ev envelope.Event) eventFields {
	var f eventFields
	switch ev.Type {
	case "delivery":
		var d struct {
			DeliveryID string `json:"delivery_id"`
			Message    struct {
				MessageID   string               `json:"message_id"`
				ThreadID    string               `json:"thread_id"`
				Sender      string               `json:"sender"`
				SenderInfo  *envelope.SenderInfo `json:"sender_info"`
				Content     envelope.Content     `json:"content"`
				Attachments []json.RawMessage    `json:"attachments"`
			} `json:"message"`
		}
		if json.Unmarshal(ev.Data, &d) == nil {
			f = eventFields{thread: d.Message.ThreadID, sender: d.Message.Sender, messageID: d.Message.MessageID,
				deliveryID: d.DeliveryID, text: d.Message.Content.Text, attachments: len(d.Message.Attachments)}
			if d.Message.SenderInfo != nil {
				f.senderName = d.Message.SenderInfo.Name
			}
		}
	case "receipt":
		var r envelope.Receipt
		if json.Unmarshal(ev.Data, &r) == nil {
			f = eventFields{thread: r.ThreadID, sender: r.By, messageID: r.MessageID, deliveryID: r.DeliveryID, text: r.State}
			if r.Note != "" {
				f.text += " " + r.Note
			}
		}
	case "reaction":
		var r envelope.ReactionEvent
		if json.Unmarshal(ev.Data, &r) == nil {
			sign := "+"
			if r.Op == "remove" {
				sign = "-"
			}
			f = eventFields{thread: r.ThreadID, sender: r.PeerID, messageID: r.MessageID, text: sign + r.Emoji}
		}
	case "presence":
		var p envelope.Presence
		if json.Unmarshal(ev.Data, &p) == nil {
			f = eventFields{sender: p.PeerID, text: p.State}
			if p.Note != "" {
				f.text += " " + p.Note
			}
		}
	default:
		var g struct {
			ThreadID string `json:"thread_id"`
		}
		if json.Unmarshal(ev.Data, &g) == nil {
			f.thread = g.ThreadID
		}
	}
	return f
}

// flatten puts untrusted text on one safe terminal line: CRLF and every
// control character (C0 including ESC, DEL, C1) and the Unicode line and
// paragraph separators become one space each, so a sender cannot inject
// ANSI/OSC escapes or fake lines (PROTOCOL §14 inv. 2).
func flatten(s string) string {
	s = strings.ReplaceAll(s, "\r\n", " ")
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || r == '\u2028' || r == '\u2029' {
			return ' '
		}
		return r
	}, s)
}

// unbidi removes invisible formatting and Unicode line separators from
// untrusted display text so peers cannot reorder or hide terminal output.
func unbidi(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.Is(unicode.Cf, r) || unicode.Is(unicode.Zl, r) || unicode.Is(unicode.Zp, r) {
			return ' '
		}
		return r
	}, s)
}

// firstRunes returns the first n characters of s.
func firstRunes(s string, n int) string {
	i := 0
	for pos := range s {
		if i == n {
			return s[:pos]
		}
		i++
	}
	return s
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// peerName makes an untrusted display name safe for one output column.
func peerName(name string) string {
	return firstRunes(strings.ReplaceAll(unbidi(flatten(name)), " ", "_"), peerNameRunes)
}

// monitorLine is the `watch --for-monitor` line (PROTOCOL §13.7):
// "<cursor> <type> <thread_id> <sender> <text>", with an optional name
// prefix in text: a delivery's sender_info.name, else the cached directory. The prefix and text together are capped at 120 runes.
func monitorLine(ev envelope.Event, names map[string]string) string {
	f := eventFieldsOf(ev)
	text := unbidi(flatten(f.text))
	if name := peerName(f.nameOf(names)); name != "" {
		text = name + ": " + text
	}
	text = firstRunes(text, monitorTextRunes)
	return strings.TrimRight(fmt.Sprintf("%s %s %s %s %s", ev.Cursor, ev.Type, dash(f.thread), dash(f.sender), text), " ")
}

// inboxLine is the human line of `inbox` and plain `watch`:
// "<cursor> <type> <thread_id> <from> <message_id> <delivery_id> <text>",
// with from rendered as name(peer_id) when known (a delivery's
// sender_info.name, else the cached directory) and text on one line.
func inboxLine(ev envelope.Event, names map[string]string) string {
	f := eventFieldsOf(ev)
	from := f.sender
	if name := peerName(f.nameOf(names)); name != "" {
		from = name + "(" + f.sender + ")"
	}
	text := unbidi(flatten(f.text))
	if f.attachments > 0 {
		text += fmt.Sprintf(" [+%d attachment(s)]", f.attachments)
	}
	return fmt.Sprintf("%s %s %s %s %s %s %s", ev.Cursor, ev.Type, dash(f.thread), dash(from),
		dash(f.messageID), dash(f.deliveryID), dash(text))
}

// --- watch ---

func (a *app) cmdWatch(args []string) error {
	o, err := parseFlags("watch", args, map[string]flagKind{
		"for-monitor": boolFlag, "since": strFlag, "auto-ack": boolFlag, "deliveries-only": boolFlag,
	})
	if err != nil {
		return err
	}
	if len(o.pos) != 0 {
		return usageErr("usage: vecnet watch [--for-monitor] [--since CURSOR] [--auto-ack] [--deliveries-only]")
	}
	c, name, ch, err := a.connect()
	if err != nil {
		return err
	}
	w := &watcher{
		a: a, c: c, ch: ch,
		names:      ch.peerNames(),
		forMonitor: o.has("for-monitor"), autoAck: o.has("auto-ack"), deliveriesOnly: o.has("deliveries-only"),
		retry: watchDefaultRetry, seen: newDedupe(dedupeWindow),
	}
	if w.forMonitor && !a.json {
		if path, err := statePath(a.getenv, name, a.as); err == nil {
			w.shown = &stateFile{path: path}
			id := a.resolveIdentity(c, nil)
			w.identity = &id
		}
	}
	// Without --since the first connect leaves out ?after=, so the hub
	// starts at the stream's shared acked cursor (PROTOCOL §2.3).
	w.cursor = o.str("since")
	return w.run()
}

// watcher holds one `watch` session: an SSE stream that reconnects per
// PROTOCOL §13.1, falling back to long-poll after repeated SSE failures.
type watcher struct {
	a     *app
	c     *client
	ch    cache
	names map[string]string

	forMonitor, autoAck, deliveriesOnly bool

	cursor     string        // last event id seen ("" until the hub names one: start at the acked cursor)
	connected  bool          // a stream was open before: resume with Last-Event-ID, not ?after=
	retry      time.Duration // the hub's "retry:" value
	seen       *dedupe
	pendingAck string
	shown      *stateFile
	identity   *hookIdentity // binds shown to this run's hub and caller
	replayed   bool
	now        func() time.Time
	sleep      func(context.Context, time.Duration) bool
	outage     reconnectStatus
	hadLive    bool
}

func (w *watcher) pause(d time.Duration) bool {
	if w.sleep != nil {
		return w.sleep(w.a.ctx, d)
	}
	return sleepCtx(w.a.ctx, d)
}

// errPeerDisabled is the 204 answer: stop reconnecting (PROTOCOL §2.3).
var errPeerDisabled = &hubError{Status: http.StatusNoContent, Code: "peer_disabled", Message: "the hub reports this peer disabled; not reconnecting"}

// terminal reports errors that end the watch: 401/403 (re-auth), 204, and
// other 4xx answers except 408, 409 and 429.
func terminal(err error) bool {
	var he *hubError
	if !errors.As(err, &he) {
		return false
	}
	switch {
	case he.Status == http.StatusNoContent:
		return true
	case he.Status == http.StatusRequestTimeout, he.Status == http.StatusConflict, he.Status == http.StatusTooManyRequests:
		return false
	}
	return he.Status >= 400 && he.Status < 500
}

func (w *watcher) run() error {
	ctx := w.a.ctx
	w.outage = reconnectStatus{out: w.a.stderr, name: "watch", now: w.now}
	if !w.forMonitor && !w.a.json {
		fmt.Fprintln(w.a.stdout, inboxHeader)
	}
	w.refreshNames()
	failures, purges := 0, 0
	for {
		if ctx.Err() != nil {
			return nil
		}
		var err error
		polled := false
		w.hadLive = false
		if w.forMonitor && !w.a.json && !w.replayed {
			err = w.replay()
			if err == nil {
				err = w.stream()
			}
		} else if failures >= watchSSEFailures && failures%2 == 1 {
			polled = true
			err = w.longPoll()
		} else {
			err = w.stream()
		}
		w.flushAck()
		if ctx.Err() != nil {
			return nil
		}
		if oldest, ok := oldestCursor(err); ok && purges < 2 {
			fmt.Fprintf(w.a.stderr, "cursor_purged: resuming from %s; re-sync threads with `vecnet thread <id>`\n", oldest)
			w.cursor, w.connected = oldest, false
			purges++
			continue
		}
		purges = 0
		if polled && err == nil {
			failures = 0
			continue // a completed long-poll response is a healthy connection
		}
		switch {
		case terminal(err):
			return err
		default:
			if w.hadLive {
				failures = 0
			}
			failures++
			detail := ""
			if err != nil {
				detail = err.Error()
			}
			w.outage.failed(detail)
			if !w.pause(w.backoff(failures)) {
				return nil
			}
		}
	}
}

// refreshNames keeps the previous directory if fetching or decoding fails.
// Display names are optional: a directory outage must never end the watch.
func (w *watcher) refreshNames() {
	var body struct {
		Peers []struct {
			PeerID string `json:"peer_id"`
			Name   string `json:"name"`
		} `json:"peers"`
	}
	if err := w.c.do(w.a.ctx, http.MethodGet, "/v1/peers", nil, nil, &body); err != nil {
		return
	}
	names := make(map[string]string, len(body.Peers))
	for _, p := range body.Peers {
		if p.PeerID != "" {
			names[p.PeerID] = peerName(p.Name)
		}
	}
	w.names = names
	if w.ch.dir != "" {
		if err := w.ch.writeJSON("peers.json", names); err != nil {
			w.a.warn("could not cache peer names: %v", err)
		}
	}
}

// backoff uses the same short restart policy for EOF and transport failures;
// the hub's ordinary retry preface must not make the first attempt slow.
func (w *watcher) backoff(failures int) time.Duration {
	return reconnectDelay(failures)
}

func (w *watcher) live() { w.hadLive = true; w.outage.live() }

func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// stream holds one SSE connection until it ends: nil when it closed or
// went silent after opening (reconnect), an error otherwise.
func (w *watcher) stream() error {
	ctx, cancel := context.WithCancel(w.a.ctx)
	defer cancel()
	q := w.a.peerQuery()
	resume := w.connected && w.cursor != ""
	if !resume && w.cursor != "" {
		q.Set("after", w.cursor)
	}
	req, err := w.c.newRequest(ctx, http.MethodGet, "/v1/events", q, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "text/event-stream")
	if resume {
		req.Header.Set("Last-Event-ID", w.cursor)
	}
	resp, err := w.c.send(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNoContent {
		return errPeerDisabled
	}
	if mt, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type")); mt != "text/event-stream" {
		return fmt.Errorf("events: unexpected content type %q", resp.Header.Get("Content-Type"))
	}
	w.connected = true

	lines := make(chan string, 64)
	readErr := make(chan error, 1)
	go readLines(ctx, resp.Body, lines, readErr)

	idle := time.NewTimer(watchSilence)
	defer idle.Stop()
	var (
		data    []string
		hasData bool
		idBuf   = w.cursor
		evType  string
	)
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-idle.C:
			return nil // silence: reconnect
		case line, ok := <-lines:
			if !ok {
				err := <-readErr // all queued lines precede their terminal read error
				if ctx.Err() != nil || errors.Is(err, io.EOF) {
					return nil
				}
				return &netError{err: err}
			}
			if !idle.Stop() {
				select {
				case <-idle.C:
				default:
				}
			}
			idle.Reset(watchSilence)
			if line == "" { // dispatch
				if evType == "hub_restarting" {
					return nil // transport control: no event, acknowledgement or cursor
				}
				if hasData {
					w.live()
				}
				// Empty data (the hub's priming event) only moves the
				// cursor; it is not an event (PROTOCOL §13.1).
				if joined := strings.Join(data, "\n"); hasData && joined != "" {
					w.handle(evType, joined, false)
				}
				w.cursor = idBuf
				data, hasData, evType = nil, false, ""
				if len(lines) == 0 {
					w.flushAck()
				}
				continue
			}
			if strings.HasPrefix(line, ":") {
				w.live()
				continue // comment, e.g. ": hb"
			}
			field, value, _ := strings.Cut(line, ":")
			value = strings.TrimPrefix(value, " ")
			switch field {
			case "event":
				evType = value
			case "data":
				data, hasData = append(data, value), true
			case "id":
				if !strings.ContainsRune(value, 0) {
					idBuf = value
				}
			case "retry":
				if ms, err := strconv.ParseInt(value, 10, 64); err == nil && ms >= 0 && strings.Trim(value, "0123456789") == "" {
					w.retry = time.Duration(ms) * time.Millisecond
				}
			}
		}
	}
}

// readLines splits r into SSE lines (CR, LF or CRLF terminated) onto out,
// then reports the read error (io.EOF at the end).
func readLines(ctx context.Context, r io.Reader, out chan<- string, errc chan<- error) {
	var terminal error
	defer func() {
		if terminal == nil {
			terminal = ctx.Err()
			if terminal == nil {
				terminal = io.EOF
			}
		}
		errc <- terminal // every exit publishes its result before closing lines
		close(out)
	}()
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64<<10), maxSSELine)
	sc.Split(splitSSELines)
	for sc.Scan() {
		select {
		case out <- sc.Text():
		case <-ctx.Done():
			return
		}
	}
	terminal = sc.Err()
}

// splitSSELines is a bufio.SplitFunc for CR, LF and CRLF line ends.
func splitSSELines(data []byte, atEOF bool) (int, []byte, error) {
	i := bytes.IndexAny(data, "\r\n")
	if i < 0 {
		if atEOF && len(data) > 0 {
			return len(data), data, nil
		}
		return 0, nil, nil
	}
	if data[i] == '\n' {
		return i + 1, data[:i], nil
	}
	// CR: swallow a following LF; wait for more input to know.
	if i+1 < len(data) {
		if data[i+1] == '\n' {
			return i + 2, data[:i], nil
		}
		return i + 1, data[:i], nil
	}
	if atEOF {
		return i + 1, data[:i], nil
	}
	return 0, nil, nil
}

// longPoll is the fallback after repeated SSE failures: one long-poll read.
func (w *watcher) longPoll() error {
	body, err := w.a.poll(w.c, w.cursor, watchPollWait)
	if err != nil {
		return err
	}
	w.live()
	for _, ev := range body.Events {
		raw, err := json.Marshal(ev)
		if err != nil {
			continue
		}
		w.handle(ev.Type, string(raw), false)
		w.cursor = ev.Cursor
	}
	if body.NextCursor != "" {
		w.cursor = body.NextCursor
	}
	return nil
}

// replay prints the unacked backlog even if another consumer showed it.
// Only the subsequent live stream consults the shared shown set.
func (w *watcher) replay() error {
	for {
		if err := w.a.ctx.Err(); err != nil {
			return err
		}
		body, err := w.a.poll(w.c, w.cursor, 0)
		if err != nil {
			return err
		}
		for _, ev := range body.Events {
			raw, err := json.Marshal(ev)
			if err != nil {
				return err
			}
			w.handle(ev.Type, string(raw), true)
		}
		w.cursor = body.NextCursor
		if len(body.Events) == 0 {
			break
		}
	}
	w.replayed = true
	w.connected = false
	return nil
}

// handle prints one event (deduped on delivery_id), caches its message
// thread and queues its cursor for --auto-ack.
func (w *watcher) handle(evType, data string, replay bool) {
	var ev envelope.Event
	if err := json.Unmarshal([]byte(data), &ev); err != nil {
		fmt.Fprintf(w.a.stderr, "vecnet: watch: undecodable %s event: %v\n", evType, err)
		return
	}
	if ev.Type == "" {
		ev.Type = evType
	}
	if ev.Type == "directory_updated" {
		w.refreshNames()
	}
	if w.autoAck && ev.Cursor != "" {
		w.pendingAck = ev.Cursor
	}
	if ev.Type == "delivery" {
		f := eventFieldsOf(ev)
		if f.deliveryID != "" && !w.seen.add(f.deliveryID) {
			return
		}
		if w.forMonitor && !w.a.json && w.shown != nil && f.deliveryID != "" {
			ctx, cancel := context.WithTimeout(w.a.ctx, 250*time.Millisecond)
			claimed := w.shown.claimFor(ctx, w.identity, f.deliveryID)
			cancel()
			if !replay && !claimed {
				return
			}
		}
		w.a.rememberEvents(w.ch, []envelope.Event{ev})
	} else if w.deliveriesOnly {
		return
	}
	switch {
	case w.a.json:
		raw, err := json.Marshal(ev)
		if err == nil {
			w.a.stdout.Write(append(raw, '\n'))
		}
	case w.forMonitor:
		fmt.Fprintln(w.a.stdout, monitorLine(ev, w.names))
	default:
		fmt.Fprintln(w.a.stdout, inboxLine(ev, w.names))
	}
}

// flushAck acks the newest handled cursor (one ack per batch).
func (w *watcher) flushAck() {
	if w.pendingAck == "" {
		return
	}
	cur := w.pendingAck
	if err := w.a.ack(w.c, cur); err != nil {
		if w.a.ctx.Err() == nil {
			fmt.Fprintf(w.a.stderr, "vecnet: watch: ack %s: %v\n", cur, err)
		}
		return
	}
	w.pendingAck = ""
}

// dedupe remembers the last n delivery ids.
type dedupe struct {
	ring []string
	next int
	set  map[string]struct{}
}

func newDedupe(n int) *dedupe {
	return &dedupe{ring: make([]string, n), set: make(map[string]struct{}, n)}
}

// add reports whether id is new, remembering it.
func (d *dedupe) add(id string) bool {
	if _, ok := d.set[id]; ok {
		return false
	}
	if old := d.ring[d.next]; old != "" {
		delete(d.set, old)
	}
	d.ring[d.next] = id
	d.next = (d.next + 1) % len(d.ring)
	d.set[id] = struct{}{}
	return true
}
