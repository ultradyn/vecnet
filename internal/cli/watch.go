package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/hubid"
)

// Watch timing (PROTOCOL §13.1).
const (
	watchSilence      = 45 * time.Second // reconnect after this long without a byte (the hub sends ": hb" every 15 s)
	watchDefaultRetry = 3 * time.Second  // until the hub's "retry:" field says otherwise
	watchMaxBackoff   = 60 * time.Second
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
	case "hub_notice":
		return noticeEventFields(ev.Data)
	case "hub_restarting", "hub_moved", "relocation_unverified":
		var body struct {
			Text string `json:"text"`
		}
		_ = json.Unmarshal(ev.Data, &body)
		return eventFields{sender: "hub", senderName: "hub", text: body.Text}
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
	return strings.TrimRight(fmt.Sprintf("%s %s %s %s %s", dash(ev.Cursor), ev.Type, dash(f.thread), dash(f.sender), text), " ")
}

// inboxLine is the human line of `inbox` and plain `watch`:
// "<cursor> <type> <thread_id> <from> <message_id> <delivery_id> <text>",
// with from rendered as name(peer_id) when known (a delivery's
// sender_info.name, else the cached directory) and text on one line.
func inboxLine(ev envelope.Event, names map[string]string) string {
	f := eventFieldsOf(ev)
	from := f.sender
	if name := peerName(f.nameOf(names)); name != "" && f.sender != "hub" {
		from = name + "(" + f.sender + ")"
	}
	text := unbidi(flatten(f.text))
	if f.attachments > 0 {
		text += fmt.Sprintf(" [+%d attachment(s)]", f.attachments)
	}
	return fmt.Sprintf("%s %s %s %s %s %s %s", dash(ev.Cursor), ev.Type, dash(f.thread), dash(from),
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
		a: a, c: c, ch: ch, name: name,
		names:      ch.peerNames(),
		forMonitor: o.has("for-monitor"), autoAck: o.has("auto-ack"), deliveriesOnly: o.has("deliveries-only"),
		retry: watchDefaultRetry, seen: newDedupe(dedupeWindow),
	}
	if w.forMonitor && !a.json {
		if path, err := statePath(a.getenv, name, a.as); err == nil {
			w.shown = &stateFile{path: path}
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

	cursor        string        // last event id seen ("" until the hub names one: start at the acked cursor)
	connected     bool          // a stream was open before: resume with Last-Event-ID, not ?after=
	retry         time.Duration // the hub's "retry:" value
	seen          *dedupe
	pendingAck    string
	shown         *stateFile
	identity      *hookIdentity // binds shown to this run's hub and caller
	replayed      bool
	name          string
	profile       Profile
	move          *envelope.HubNotice
	moveTimer     *time.Timer
	moveC         <-chan time.Time
	moveDue       time.Time
	restartUntil  time.Time
	restartDelay  time.Duration
	restartWarned bool

	initialized      bool   // names, notices and hook identity fetched from the current hub
	announceMove     bool   // print the hub_moved line after the next verified connection
	pin              string // the key the current hub last proved ("" while unpinned)
	unverifiedWarned bool   // the unpinned-identity warning is printed once per watch
}

// moveJitter spreads a scheduled follow over 0-30 s after the notice's at
// (Task 7); tests replace it to follow without waiting.
var moveJitter = func() time.Duration { return rand.N(30*time.Second + 1) }

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

var errRestarting = errors.New("hub restarting")
var errMoveDue = errors.New("moving notice due")

func (w *watcher) run() error {
	ctx := w.a.ctx
	defer w.stopMoveTimer()
	if !w.forMonitor && !w.a.json {
		fmt.Fprintln(w.a.stdout, inboxHeader)
	}
	failures, purges := 0, 0
	for {
		if ctx.Err() != nil {
			return nil
		}
		var err error
		if !w.moveDue.IsZero() && !time.Now().Before(w.moveDue) {
			err = errMoveDue
		}
		if err == nil {
			err = w.verifyConnection()
		}
		verified := err == nil
		if verified {
			if w.announceMove {
				w.announceMove = false
				fp, _ := hubid.Fingerprint(w.pin)
				w.systemLine("hub_moved", fmt.Sprintf("profile %s now uses %s (key %s verified)", w.name, w.c.base, fp))
			}
			if !w.initialized {
				if w.shown != nil {
					id := w.a.resolveIdentity(w.c, nil)
					w.identity = &id
				}
				w.refreshNames()
				w.refreshMoving()
				w.initialized = true
			}
			if w.forMonitor && !w.a.json && !w.replayed {
				err = w.replay()
				if err == nil {
					err = w.stream()
				}
			} else if failures >= watchSSEFailures && failures%2 == 1 {
				err = w.longPoll()
			} else {
				err = w.stream()
			}
		}
		if verified {
			w.flushAck()
		}
		if ctx.Err() != nil {
			return nil
		}
		if errors.Is(err, errMoveDue) {
			n := w.move
			w.stopMoveTimer()
			if n != nil {
				if err = w.followNotice(*n); err != nil {
					return err
				}
			}
			failures = 0
			continue
		}
		if followed, followErr := w.followError(err); followed {
			if followErr != nil {
				return followErr
			}
			failures = 0
			continue
		}
		if oldest, ok := oldestCursor(err); ok && purges < 2 {
			fmt.Fprintf(w.a.stderr, "cursor_purged: resuming from %s; re-sync threads with `vecnet thread <id>`\n", oldest)
			w.cursor, w.connected = oldest, false
			purges++
			continue
		}
		purges = 0
		var identityErr *relocationError
		switch {
		case errors.Is(err, errRestarting):
			failures = 0
			if !w.pause(w.restartDelay) {
				return nil
			}
		case errors.As(err, &identityErr):
			return err
		case err == nil:
			failures = 0
			if !w.pause(w.retry) {
				return nil
			}
		case terminal(err):
			return err
		case time.Now().Before(w.restartUntil):
			if !w.restartWarned {
				fmt.Fprintln(w.a.stderr, "vecnet: watch: hub restarting; waiting for it to return")
				w.restartWarned = true
			}
			failures = 0
			if !w.pause(max(w.restartDelay, time.Second)) {
				return nil
			}
		default:
			failures++
			fmt.Fprintf(w.a.stderr, "vecnet: watch: %v; reconnecting\n", err)
			if !w.pause(w.backoff(failures)) {
				return nil
			}
		}
	}
}

// Identity proofs are public and checked before any stream, replay or poll.
// Old hubs with no identity endpoint remain usable only while unpinned.
func (w *watcher) verifyConnection() error {
	_, name, p, err := w.a.profileSelection()
	if err != nil {
		return err
	}
	expected, e := hubid.NormalizeURL(p.Hub)
	connected, ce := hubid.NormalizeURL(w.c.base)
	if e == nil && ce == nil && expected != connected && p.Secret == w.c.secret && w.adoptFollowed(nil) {
		connected = expected
	}
	if e != nil || ce != nil || expected != connected || p.Secret != w.c.secret {
		return &relocationError{Code: "profile_changed", Reason: "watch profile changed; re-run the command"}
	}
	w.name, w.profile = name, p
	pin, _ := w.a.effectivePin(name, p)
	doc, rotated, err := verifyHub(w.a.ctx, p.Hub, pin, p.AllowInsecureHTTP || w.a.getenv(insecureEnv) == "1")
	if err != nil {
		var re *relocationError
		if !errors.As(err, &re) {
			return err
		}
		switch re.Reason {
		case "hub key does not match the pin":
			// The only identity change: the bearer never reaches this hub.
			return &relocationError{Code: "hub_identity_changed", Reason: "hub key does not match the pin; verify the fingerprint before reconnecting"}
		case "hub identity unavailable", "hub did not serve an identity":
			if pin == "" && re.Reason == "hub did not serve an identity" && w.legacyIdentity(p.Hub) {
				w.pin = ""
				return nil
			}
			// Proxy 502/503, the unauthenticated limiter's 429 and transport
			// errors are outages: back off and retry (PROTOCOL §13.1).
			return fmt.Errorf("hub identity unavailable")
		}
		if pin != "" {
			if re.Reason == "invalid hub identity response" {
				return fmt.Errorf("hub identity unavailable")
			}
			return &relocationError{Code: "hub_identity_unverified", Reason: re.Reason}
		}
		// Unpinned: as one-shot commands do (ensurePin), keep the legacy
		// behaviour, e.g. a profile on an alias URL the hub's public_url does
		// not name. Nothing is pinned, so nothing can be enforced yet.
		if !w.unverifiedWarned {
			fmt.Fprintf(w.a.stderr, "vecnet: watch: hub identity not verified (%s); continuing unpinned\n", re.Reason)
			w.unverifiedWarned = true
		}
		w.pin = ""
		return nil
	}
	w.pin = doc.Pubkey
	if pin == "" {
		if err := w.a.updatePins(func(records map[string]pinRecord) error {
			current, _ := w.a.effectivePin(name, p)
			if current != "" && current != doc.Pubkey {
				return fmt.Errorf("pin changed during identity verification")
			}
			r := records[name]
			r.Hub, r.Pubkey, r.How, r.PinnedAt = expected, doc.Pubkey, "tofu", time.Now().UTC().Format(time.RFC3339Nano)
			r.TofuTriedAt = ""
			records[name] = r
			return nil
		}); err != nil {
			return err
		}
	}
	if rotated {
		path, e := configPath(w.a.getenv)
		if e != nil {
			return e
		}
		normalized, _ := hubid.NormalizeURL(p.Hub)
		err = w.a.editProfileSnapshot(path, name, p, pin, map[string]string{"hub_pubkey": doc.Pubkey}, func(records map[string]pinRecord) {
			r := records[name]
			r.Hub, r.Pubkey, r.How = normalized, doc.Pubkey, "rotation"
			r.PinnedAt = time.Now().UTC().Format(time.RFC3339Nano)
			r.TofuTriedAt = ""
			records[name] = r
		})
		if err != nil {
			return err
		}
		w.profile.HubPubkey = doc.Pubkey
	}
	return nil
}

var errPinsReadOnly = errors.New("pins read only")

// adoptFollowed accepts a move another vecnet process already made for this
// profile (Decision 6 rewrites every profile on the hub): the config names
// the recorded destination, pins.json records a move from the hub this watch
// uses (for n, that very notice) with the pin this watch last verified, and
// the secret is unchanged. Config and pins are read under the pins lock, which
// follow holds while it writes both. The watch then switches hubs; the next
// verifyConnection proves the new hub against the pin before any bearer use.
func (w *watcher) adoptFollowed(n *envelope.HubNotice) bool {
	var (
		r       pinRecord
		name    string
		p       Profile
		profErr error
	)
	err := w.a.updatePins(func(records map[string]pinRecord) error {
		_, name, p, profErr = w.a.profileSelection()
		r = records[name]
		return errPinsReadOnly
	})
	if !errors.Is(err, errPinsReadOnly) || profErr != nil || name != w.name || p.Secret != w.c.secret {
		return false
	}
	hub, e1 := hubid.NormalizeURL(p.Hub)
	from, e2 := hubid.NormalizeURL(w.c.base)
	m := r.LastMove
	if e1 != nil || e2 != nil || hub == from || m == nil || m.From != from || m.To != hub || r.Hub != hub || r.Pubkey == "" {
		return false
	}
	if w.pin != "" && r.Pubkey != w.pin {
		return false
	}
	if n != nil {
		next, e := hubid.NormalizeURL(n.NewURL)
		if e != nil || next != hub || m.NoticeID != n.NoticeID {
			return false
		}
	}
	w.switchHub(p.Hub)
	return true
}

// switchHub points the watch at a followed hub. Names, notices and the hook
// identity are refetched, and the move announced, only after the next
// verified connection.
func (w *watcher) switchHub(hub string) {
	w.c.base = strings.TrimRight(hub, "/")
	w.stopMoveTimer()
	w.move = nil
	w.initialized = false
	w.announceMove = true
}

func (w *watcher) legacyIdentity(hub string) bool {
	ctx, cancel := context.WithTimeout(w.a.ctx, 2*time.Second)
	defer cancel()
	c := newClient(hub, "")
	req, err := c.newRequest(ctx, http.MethodGet, "/v1/hub/identity", nil, nil)
	if err != nil {
		return false
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusNotFound
}

func (w *watcher) systemLine(kind, text string) {
	data, _ := json.Marshal(map[string]string{"text": text})
	ev := envelope.Event{Type: kind, Data: data}
	switch {
	case w.a.json:
		_ = w.a.emitValue(ev)
	case w.forMonitor:
		fmt.Fprintln(w.a.stdout, monitorLine(ev, nil))
	default:
		fmt.Fprintln(w.a.stdout, inboxLine(ev, nil))
	}
}

func (w *watcher) restarting(h restartHint) {
	// Bound untrusted transport delays before converting milliseconds to a duration.
	d := time.Duration(max(int64(0), min(h.RetryAfterMS, int64((24*time.Hour)/time.Millisecond)))) * time.Millisecond
	w.restartUntil = time.Now().Add(d + time.Minute)
	w.restartDelay = d*8/10 + rand.N(d*4/10+1)
	w.restartWarned = false
	w.systemLine("hub_restarting", fmt.Sprintf("restarting; reconnecting in ~%gs", d.Seconds()))
}

func (w *watcher) stopMoveTimer() {
	if w.moveTimer != nil {
		w.moveTimer.Stop()
	}
	w.moveTimer, w.moveC = nil, nil
	w.moveDue = time.Time{}
}

func (w *watcher) refreshMoving() {
	notices, err := w.a.activeNotices(w.c)
	if err != nil {
		return
	}
	var moving *envelope.HubNotice
	for _, n := range notices {
		if n.Kind == "moving" {
			copy := n
			moving = &copy
		}
	}
	if moving == nil {
		w.stopMoveTimer()
		w.move = nil
		return
	}
	if w.move != nil && w.move.NoticeID == moving.NoticeID {
		return
	}
	w.stopMoveTimer()
	w.move = moving
	if w.a.getenv("VECNET_NO_FOLLOW") == "1" {
		fmt.Fprintf(w.a.stderr, "hub moving; run `vecnet --net %s profile follow`\n", peerName(w.name))
		return
	}
	if moving.At == "" {
		return
	}
	pin, _ := w.a.effectivePin(w.name, w.profile)
	source, e := hubid.NormalizeURL(moving.Hub)
	own, _ := hubid.NormalizeURL(w.profile.Hub)
	if e != nil || source != own || hubid.VerifyNotice(pin, *moving) != nil {
		w.systemLine("relocation_unverified", "not following "+moving.NewURL+": invalid moving notice signature")
		return
	}
	at, e := envelope.ParseTime(moving.At)
	if e != nil {
		return
	}
	expiry, e := envelope.ParseTime(moving.ExpiresAt)
	if e != nil || !expiry.After(time.Now()) || moving.ClearedAt != "" {
		return
	}
	w.moveDue = at.Add(moveJitter())
	w.moveTimer = time.NewTimer(max(time.Duration(0), time.Until(w.moveDue)))
	w.moveC = w.moveTimer.C
}

func (w *watcher) followNotice(n envelope.HubNotice) error {
	next, err := w.a.follow(w.a.ctx, w.name, w.profile, n)
	if err != nil {
		// Another watch or command on this hub may already have followed it
		// for every profile there (Decision 6): adopt that move.
		if w.adoptFollowed(&n) {
			return nil
		}
		reason := err.Error()
		var re *relocationError
		if errors.As(err, &re) {
			reason = re.Reason
		}
		w.systemLine("relocation_unverified", "not following "+n.NewURL+": "+reason)
		return err
	}
	w.switchHub(next)
	return nil
}

func (w *watcher) followError(err error) (bool, error) {
	var he *hubError
	if !errors.As(err, &he) || he.Status != http.StatusGone || he.Code != "hub_moved" {
		return false, err
	}
	if w.a.getenv("VECNET_NO_FOLLOW") == "1" {
		return true, &relocationError{Code: "relocation_unverified", Reason: fmt.Sprintf("hub moved; run `vecnet --net %s profile follow`", peerName(w.name))}
	}
	n, ok := movedNotice(err)
	if !ok {
		return true, &relocationError{Code: "relocation_unverified", Reason: "hub returned an invalid moving notice"}
	}
	w.stopMoveTimer()
	return true, w.followNotice(n)
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

// backoff is the jittered exponential reconnect delay, from the hub's retry
// up to watchMaxBackoff.
func (w *watcher) backoff(failures int) time.Duration {
	d := max(w.retry, time.Second)
	for i := 1; i < failures && d < watchMaxBackoff; i++ {
		d *= 2
	}
	d = min(d, watchMaxBackoff)
	return d/2 + rand.N(d/2+1)
}

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
	defer func() { cancel(); resp.Body.Close() }()
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
		data           []string
		hasData        bool
		idBuf          = w.cursor
		evType         string
		pendingReadErr error
	)
	for {
		// A reader may reach EOF with complete frame lines still buffered.
		// Dispatch those lines before acting on its terminal read result.
		if pendingReadErr != nil && len(lines) == 0 {
			if errors.Is(pendingReadErr, io.EOF) {
				return nil
			}
			return &netError{err: pendingReadErr}
		}
		select {
		case <-ctx.Done():
			return nil
		case <-w.moveC:
			return errMoveDue
		case <-idle.C:
			return nil // silence: reconnect
		case err := <-readErr:
			pendingReadErr = err
			readErr = nil
		case line := <-lines:
			if !idle.Stop() {
				select {
				case <-idle.C:
				default:
				}
			}
			idle.Reset(watchSilence)
			if line == "" { // dispatch
				// Empty data (the hub's priming event) only moves the
				// cursor; it is not an event (PROTOCOL §13.1).
				if joined := strings.Join(data, "\n"); hasData && joined != "" {
					if evType == "hub_restarting" {
						var h restartHint
						if json.Unmarshal([]byte(joined), &h) == nil {
							w.cursor = idBuf
							w.restarting(h)
							return errRestarting
						}
					} else {
						w.handle(evType, joined, false)
					}
				}
				w.cursor = idBuf
				data, hasData, evType = nil, false, ""
				if len(lines) == 0 {
					w.flushAck()
				}
				continue
			}
			if strings.HasPrefix(line, ":") {
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
	err := sc.Err()
	if err == nil {
		err = io.EOF
	}
	errc <- err
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
	body, err := w.poll(watchPollWait)
	if err != nil {
		return err
	}
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
	if body.HubRestarting != nil {
		w.restarting(*body.HubRestarting)
		return errRestarting
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
		body, err := w.poll(0)
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
		if body.HubRestarting != nil {
			w.restarting(*body.HubRestarting)
			return errRestarting
		}
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
	if ev.Type == "hub_notice" {
		var n envelope.HubNoticeEvent
		if json.Unmarshal(ev.Data, &n) == nil && n.Notice.Kind == "moving" {
			w.refreshMoving()
		}
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

// poll also lets a scheduled move interrupt a fallback's held request.
func (w *watcher) poll(wait int) (pollBody, error) {
	worker := *w.a
	if !w.moveDue.IsZero() {
		ctx, cancel := context.WithDeadline(w.a.ctx, w.moveDue)
		defer cancel()
		worker.ctx = ctx
	}
	body, err := worker.poll(w.c, w.cursor, wait)
	if err != nil && !w.moveDue.IsZero() && !time.Now().Before(w.moveDue) {
		return body, errMoveDue
	}
	return body, err
}

type restartHint struct {
	RetryAfterMS int64 `json:"retry_after_ms"`
}

func (w *watcher) pause(delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-w.a.ctx.Done():
		return false
	case <-w.moveC:
		return true
	case <-timer.C:
		return true
	}
}
