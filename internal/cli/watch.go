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
	"os"
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
	case "hub_notice":
		return noticeEventFields(ev.Data)
	case "hub_moved", "relocation_unverified":
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
	if labels := eventBlobLabels(ev); labels != "" {
		text += " " + labels
	}
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
		retry: watchStartRetry, seen: newDedupe(dedupeWindow),
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

// monitorDeferred retains only the blocked event, never its transport suffix
// or an unverified page tail. resume is the exact transport position for it.
type monitorDeferred struct {
	kind, data    string
	after, resume string
	hub           string
}

// watcher holds one `watch` session: an SSE stream that reconnects per
// PROTOCOL §13.1, falling back to long-poll after repeated SSE failures.
type watcher struct {
	a     *app
	c     *client
	ch    cache
	names map[string]string

	forMonitor, autoAck, deliveriesOnly bool

	// identityOff skips the per-connection hub identity proof and the moving
	// notice refresh. Only unit tests of the reconnect loop set it: they talk
	// to a bare handler with no profile. cmdWatch never does, so a real watch
	// always verifies (zero value: on).
	identityOff bool

	cursor       string        // last event id seen ("" until the hub names one: start at the acked cursor)
	connected    bool          // a stream was open before: resume with Last-Event-ID, not ?after=
	retry        time.Duration // the hub's "retry:" value
	seen         *dedupe
	pendingAck   string
	shown        *stateFile
	identity     *hookIdentity // binds shown to this run's hub and caller
	replayed     bool
	deferred     *monitorDeferred
	name         string
	profile      Profile
	move         *envelope.HubNotice
	moveTimer    *time.Timer
	moveC        <-chan time.Time
	moveDue      time.Time
	restartUntil time.Time     // restart grace: failures until then neither back off nor fall back
	restartDelay time.Duration // the hub's jittered retry_after_ms hint

	bootstrapDisplay      bool // initial absence only; cleared after the first successful locked preparation
	displayStarted        bool // a followed hub cannot renew initial bootstrap permission
	prepareDisplayPending bool
	displayMovedFrom      *hookIdentity // exact scope before an existing verified follow transition
	initialized           bool          // names, notices and hook identity fetched from the current hub
	announceMove          bool          // print the hub_moved line after the next verified connection
	pin                   string        // the key the current hub last proved ("" while unpinned)
	unverifiedWarned      bool          // the unpinned-identity warning is printed once per watch
	followWarned          string        // the notice whose unreachable destination was reported
	followAttempts        int           // consecutive scheduled follows that found the destination unreachable

	// Reconnect reporting (PROTOCOL §13.1): quiet for brief outages.
	now       func() time.Time
	sleep     func(context.Context, time.Duration) bool
	outage    reconnectStatus
	hadLive   bool
	ackOutage bool // a retryable acknowledgement failure still owes pendingAck
}

func (w *watcher) clock() time.Time {
	if w.now != nil {
		return w.now()
	}
	return time.Now()
}

// pause waits d, returning early (true) when a scheduled move falls due and
// false when the watch is cancelled. Tests replace it with sleep.
func (w *watcher) pause(d time.Duration) bool {
	if w.sleep != nil {
		return w.sleep(w.a.ctx, d)
	}
	timer := time.NewTimer(d)
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

// moveJitter spreads a scheduled follow over 0-30 s after the notice's at
// (Task 7); tests replace it to follow without waiting.
var moveJitter = func() time.Duration { return rand.N(30*time.Second + 1) }

// watchStartRetry is the retry before the hub names one; watchMinBackoff is
// where the reconnect backoff starts and the floor of a restart-grace retry.
// Tests shorten both.
var (
	watchStartRetry = watchDefaultRetry
	watchMinBackoff = time.Second
)

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
var errMoveUnavailable = errors.New("move destination unavailable")
var errWatchOutput = errors.New("watch output failed")

func (w *watcher) run() error {
	ctx := w.a.ctx
	defer w.stopMoveTimer()
	defer func() { w.deferred = nil }()
	w.outage = reconnectStatus{out: w.a.stderr, name: "watch", now: w.now}
	if !w.forMonitor && !w.a.json {
		fmt.Fprintln(w.a.stdout, inboxHeader)
	}
	failures, purges := 0, 0
	for {
		if ctx.Err() != nil {
			return nil
		}
		var err error
		polled := false
		w.hadLive = false
		if !w.moveDue.IsZero() && !time.Now().Before(w.moveDue) {
			err = errMoveDue
		}
		if err == nil && !w.identityOff {
			err = w.verifyConnection()
		}
		verified := err == nil
		if verified {
			if w.announceMove {
				w.announceMove = false
				fp, _ := hubid.Fingerprint(w.pin)
				where := "the hub"
				if shown, ok := printableHub(w.c.base, w.profile.AllowInsecureHTTP || w.a.getenv(insecureEnv) == "1"); ok {
					where = shown
				}
				w.systemLine("hub_moved", fmt.Sprintf("profile %s now uses %s (key %s verified)", w.name, where, fp))
			}
			if !w.initialized {
				if w.shown != nil {
					id := w.resolveDisplayIdentity()
					w.identity = &id
					if !w.displayStarted {
						_, stateErr := os.Stat(w.shown.path)
						w.bootstrapDisplay = errors.Is(stateErr, os.ErrNotExist)
						w.displayStarted = true
					}
					w.prepareDisplayPending = true
					w.prepareDisplayState()
				}
				w.refreshNames()
				if !w.identityOff {
					w.refreshMoving()
				}
				w.initialized = true
			}
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
		}
		if verified {
			w.flushAck()
		}
		if errors.Is(err, errDisplayDeferred) {
			err = w.retryDeferred()
			if err == nil {
				failures = 0
				continue
			}
		}
		if errors.Is(err, errWatchOutput) {
			return err
		}
		if ctx.Err() != nil {
			return nil
		}
		if errors.Is(err, errMoveDue) {
			n := w.move
			w.stopMoveTimer()
			if n != nil {
				if err = w.followNotice(*n, true); err != nil {
					return err
				}
			}
			failures = 0
			continue
		}
		if followed, followErr := w.followError(err); followed {
			switch {
			case errors.Is(followErr, errMoveUnavailable):
				err = followErr // back off and retry like any outage
			case followErr != nil:
				return followErr
			default:
				failures = 0
				continue
			}
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
		var identityErr *relocationError
		switch {
		case errors.Is(err, errRestarting):
			// The hub announced a restart (PROTOCOL §21.3): a reconnect hint,
			// not a failure. Wait about the hinted time without a word; the
			// outage clock starts here, so only a restart that stays away
			// past the quiet window is reported.
			failures = 0
			w.outage.failed("")
			if !w.pause(w.restartDelay) {
				return nil
			}
		case errors.As(err, &identityErr):
			return err
		case terminal(err):
			w.stoppedLine(err)
			return err
		case w.clock().Before(w.restartUntil):
			// Within the restart grace, failures neither grow the backoff nor
			// switch to long-poll; they are reported like any outage.
			failures = 0
			w.outage.failed(errDetail(err))
			if !w.pause(max(w.restartDelay, watchMinBackoff)) {
				return nil
			}
		default:
			if w.hadLive {
				failures = 0
			}
			failures++
			w.outage.failed(errDetail(err))
			if !w.pause(w.backoff(failures)) {
				return nil
			}
		}
	}
}

// errDetail is err's text for reconnect reporting ("" for a clean end).
func errDetail(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
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
	w.c.allowInsecureHTTP = p.AllowInsecureHTTP || w.a.getenv(insecureEnv) == "1"
	pin, _ := w.a.effectivePin(name, p)
	// A pinned profile may name an alias of the hub (DEPLOY.md: enrol through
	// vecnet.fly.dev pins the key from the enrol answer): prove the pinned key
	// with a fresh challenge for the served public_url, and exit only on a key
	// mismatch. Unpinned profiles and a just-followed hub keep the strict
	// public_url check; follow targets and repoint always use it.
	matchURL := pin == "" || w.announceMove
	doc, rotated, err := verifyHubAt(w.a.ctx, p.Hub, pin, w.c.allowInsecureHTTP, matchURL)
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
	w.c.allowInsecureHTTP = p.AllowInsecureHTTP || w.a.getenv(insecureEnv) == "1"
	w.switchHub(p.Hub)
	return true
}

// switchHub points the watch at a followed hub. Names, notices and the hook
// identity are refetched, and the move announced, only after the next
// verified connection.
func (w *watcher) switchHub(hub string) {
	if w.deferred == nil && w.identity != nil && w.identity.Caller != "" && w.identity.Hub == normalizeHub(w.c.base) {
		old := *w.identity
		w.displayMovedFrom = &old
	}
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

// stoppedLine ends `watch --json` after a terminal answer with one
// client-made watch_stopped Event, data {text, status}: a machine-readable
// "not reconnecting" for supervisors such as the Codex connector, which must
// stop on 401/403/204 (§13.1) instead of respawning the watch. Only the HTTP
// status is carried; hub-supplied code and message text stay on stderr.
func (w *watcher) stoppedLine(err error) {
	var he *hubError
	if !w.a.json || !errors.As(err, &he) {
		return
	}
	data, _ := json.Marshal(map[string]any{"text": fmt.Sprintf("not reconnecting: hub answered HTTP %d", he.Status), "status": he.Status})
	_ = w.a.emitValue(envelope.Event{Type: "watch_stopped", Data: data})
}

func (w *watcher) restarting(h restartHint) {
	// Bound untrusted transport delays to §21.3's 0-60000 ms before
	// converting milliseconds to a duration.
	d := time.Duration(max(int64(0), min(h.RetryAfterMS, int64(time.Minute/time.Millisecond)))) * time.Millisecond
	w.restartUntil = w.clock().Add(d + time.Minute)
	w.restartDelay = d*8/10 + rand.N(d*4/10+1)
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
	own, _ := hubid.NormalizeURL(w.profile.Hub)
	if moving != nil {
		// The move that brought this watch here, still listed by the new hub
		// (its database was copied from the old one), is already done.
		if next, e := hubid.NormalizeURL(moving.NewURL); e == nil && next == own {
			moving = nil
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
	// A notice this watch cannot follow is printed once (w.move remembers it)
	// and ignored: the hub still serves, and if it does relocate, the 410
	// runs the full follow checks and a refusal then ends the watch (§13.7).
	pin, _ := w.a.effectivePin(w.name, w.profile)
	allow := w.profile.AllowInsecureHTTP || w.a.getenv(insecureEnv) == "1"
	if pin == "" {
		// verifyConnection pins every hub that first use can pin, so an
		// unpinned profile here names an alias or a pre-§21 hub: follow
		// could not pin the old hub either (§21.5 step 2).
		w.systemLine("relocation_unverified", relocationLine(moving.NoticeID, moving.NewURL, "this hub is not pinned", allow))
		return
	}
	if hubid.VerifyNotice(pin, *moving) != nil {
		w.systemLine("relocation_unverified", relocationLine(moving.NoticeID, moving.NewURL, "invalid moving notice signature", allow))
		return
	}
	source, e := hubid.NormalizeURL(moving.Hub)
	if e != nil || source != own {
		if e != nil {
			w.systemLine("relocation_unverified", relocationLine(moving.NoticeID, moving.NewURL, "notice does not describe this hub moving", allow))
			return
		}
		// Signed by the pinned key: the profile names an alias of the hub's
		// public_url (DEPLOY.md), which a follow never accepts. The hint
		// names the URL only after CheckHubURL accepts it.
		shown, ok := printableHub(source, allow)
		if !ok {
			shown = "the hub"
		}
		w.systemLine("relocation_unverified", relocationLine(moving.NoticeID, moving.NewURL, "notice names hub "+shown, allow))
		if ok {
			fmt.Fprintf(w.a.stderr, "vecnet: watch: profile %s uses an alias of %s; run `vecnet --net %s profile repoint %s` to follow its moves\n",
				peerName(w.name), shown, peerName(w.name), shown)
		} else {
			fmt.Fprintf(w.a.stderr, "vecnet: watch: profile %s uses an alias of its hub; run `vecnet --net %s profile repoint` to follow its moves\n",
				peerName(w.name), peerName(w.name))
		}
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

// followNotice follows n (scheduled: its timer fired; otherwise a 410). A
// destination that is not reachable yet (DNS lag on a domain move, a proxy
// 502) is an outage, not a refusal: the line prints once per notice and the
// follow retries with backoff (PROTOCOL §13.1), a scheduled one while the
// watch stays on the current hub. Refusals (signature, key, public_url,
// replay) end the watch.
func (w *watcher) followNotice(n envelope.HubNotice, scheduled bool) error {
	next, err := w.a.follow(w.a.ctx, w.name, w.profile, n)
	if err != nil {
		// Another watch or command on this hub may already have followed it
		// for every profile there (Decision 6): adopt that move.
		if w.adoptFollowed(&n) {
			w.followAttempts = 0
			return nil
		}
		reason := err.Error()
		var re *relocationError
		if errors.As(err, &re) {
			reason = re.Reason
		}
		transient := re != nil && (re.Reason == "hub identity unavailable" || re.Reason == "hub did not serve an identity")
		if !transient || w.followWarned != n.NoticeID {
			allow := w.profile.AllowInsecureHTTP || w.a.getenv(insecureEnv) == "1"
			w.systemLine("relocation_unverified", relocationLine(n.NoticeID, n.NewURL, reason, allow))
		}
		if !transient {
			return err
		}
		w.followWarned = n.NoticeID
		if !scheduled {
			return errMoveUnavailable
		}
		w.followAttempts++
		copy := n
		w.move = &copy
		w.moveDue = time.Now().Add(w.backoff(w.followAttempts))
		w.moveTimer = time.NewTimer(time.Until(w.moveDue))
		w.moveC = w.moveTimer.C
		return nil
	}
	w.followAttempts = 0
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
	return true, w.followNotice(n, false)
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
	return reconnectDelayFrom(watchMinBackoff, failures)
}

// live records a healthy frame: it ends an outage (unless an acknowledgement
// is still owed) and any restart grace, since the hub is back.
func (w *watcher) live() {
	w.hadLive = true
	w.restartUntil = time.Time{}
	if !w.ackOutage {
		w.outage.live()
	}
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
		data      []string
		hasData   bool
		idBuf     = w.cursor
		evType    string
		dataBytes int
	)
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-w.moveC:
			return errMoveDue
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
					// Transport control (PROTOCOL §21.3): no event,
					// acknowledgement or cursor, and never printed. A
					// retry_after_ms hint is honoured; without one the
					// ordinary reconnect policy applies.
					var h struct {
						RetryAfterMS *int64 `json:"retry_after_ms"`
					}
					if json.Unmarshal([]byte(strings.Join(data, "\n")), &h) == nil && h.RetryAfterMS != nil {
						w.restarting(restartHint{RetryAfterMS: *h.RetryAfterMS})
						return errRestarting
					}
					return nil
				}
				if hasData {
					w.live()
				}
				// Empty data (the hub's priming event) only moves the
				// cursor; it is not an event (PROTOCOL §13.1).
				if joined := strings.Join(data, "\n"); hasData && joined != "" {
					if err := w.handle(evType, joined, false); err != nil {
						if errors.Is(err, errDisplayDeferred) && w.deferred != nil {
							w.deferred.resume = idBuf
						}
						return err
					}
				}
				w.cursor = idBuf
				data, hasData, evType = nil, false, ""
				dataBytes = 0
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
				dataBytes += len(value) + 1
				if w.sharedDisplay() && dataBytes > maxSSELine {
					return fmt.Errorf("%w: monitor event exceeds size limit", errWatchOutput)
				}
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
	body, err := w.poll(watchPollWait)
	if err != nil {
		return err
	}
	w.live()
	for _, ev := range body.Events {
		raw, err := json.Marshal(ev)
		if err != nil {
			continue
		}
		if err := w.handle(ev.Type, string(raw), false); err != nil {
			return err
		}
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
// Only the subsequent live stream consults shared display proof.
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
			if err := w.handle(ev.Type, string(raw), true); err != nil {
				return err
			}
			w.cursor = ev.Cursor
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

// resolveDisplayIdentity recognizes the authenticated operator role for
// monitor output only. Its reserved subject cannot be supplied as a peer or
// principal ID. Ordinary hook/binding identity resolution remains separate.
func (w *watcher) resolveDisplayIdentity() hookIdentity {
	id := hookIdentity{Hub: normalizeHub(w.c.base), As: w.a.as, Cred: hookCredential(w.c.secret)}
	ctx, cancel := context.WithTimeout(w.a.ctx, 2*time.Second)
	defer cancel()
	var me struct {
		Kind        string `json:"kind"`
		PeerID      string `json:"peer_id"`
		PrincipalID string `json:"principal_id"`
	}
	if w.c.do(ctx, http.MethodGet, "/v1/peers/me", nil, nil, &me) != nil {
		return id
	}
	switch me.Kind {
	case "operator":
		id.Caller = monitorOperatorCaller
	case "peer":
		if hookID.MatchString(me.PeerID) {
			id.Caller = me.PeerID
		}
	case "principal":
		if hookID.MatchString(me.PrincipalID) {
			id.Caller = me.PrincipalID
		}
	}
	return id
}

func (w *watcher) prepareDisplayState() {
	if !w.prepareDisplayPending || w.shown == nil {
		return
	}
	ctx, cancel := context.WithTimeout(w.a.ctx, 250*time.Millisecond)
	defer cancel()
	if w.shown.prepareDisplay(ctx, w.identity, w.bootstrapDisplay, w.displayMovedFrom) == nil {
		w.prepareDisplayPending, w.bootstrapDisplay = false, false
		w.displayMovedFrom = nil
	}
}

func (w *watcher) sharedDisplay() bool {
	return w.forMonitor && !w.a.json && (w.shown != nil || w.identity != nil || w.name != "")
}

// retryDeferred runs without a transport. It never releases ownership and
// always rechecks shared state, so receipt, release and reclaim races serialize
// with the eventual print. A replay that waited must honor subsequent proof.
func (w *watcher) retryDeferred() error {
	d := w.deferred
	if d == nil {
		return errDisplayDeferred
	}
	for delay := time.Second; ; delay = min(delay*2, 15*time.Second) {
		if !w.pause(delay) {
			return w.a.ctx.Err()
		}
		if !w.moveDue.IsZero() && !time.Now().Before(w.moveDue) {
			return &relocationError{Code: "display_pending_move", Reason: "monitor delivery pending during hub move; re-run the command"}
		}
		if !w.identityOff {
			if err := w.verifyConnection(); err != nil {
				var identityErr *relocationError
				if errors.As(err, &identityErr) {
					return err
				}
				continue // identity/network uncertainty retains the envelope
			}
		}
		if normalizeHub(w.c.base) != d.hub {
			return &relocationError{Code: "profile_changed", Reason: "monitor delivery belongs to the previous hub; re-run the command"}
		}
		if w.identity == nil || w.identity.Caller == "" {
			id := w.resolveDisplayIdentity()
			w.identity = &id
		}
		w.prepareDisplayState()
		if err := w.handle(d.kind, d.data, false); err != nil {
			if errors.Is(err, errDisplayDeferred) {
				continue
			}
			return err
		}
		w.cursor = d.resume
		w.deferred = nil
		w.flushAck()
		return nil
	}
}

// handle prints one event (deduped on delivery_id), caches its message
// thread and queues its cursor for --auto-ack.
func (w *watcher) handle(evType, data string, replay bool) error {
	if w.sharedDisplay() && len(data) > maxSSELine {
		return fmt.Errorf("%w: monitor event exceeds size limit", errWatchOutput)
	}
	var ev envelope.Event
	if err := json.Unmarshal([]byte(data), &ev); err != nil {
		fmt.Fprintf(w.a.stderr, "vecnet: watch: undecodable %s event: %v\n", evType, err)
		return nil
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
	queueAck := func() {
		if w.autoAck && ev.Cursor != "" {
			w.pendingAck = ev.Cursor
		}
	}
	printEvent := func() error {
		var line []byte
		switch {
		case w.a.json:
			raw, err := json.Marshal(ev)
			if err != nil {
				return fmt.Errorf("%w: %v", errWatchOutput, err)
			}
			line = append(raw, '\n')
		case w.forMonitor:
			line = []byte(monitorLine(ev, w.names) + "\n")
		default:
			line = []byte(inboxLine(ev, w.names) + "\n")
		}
		n, err := w.a.stdout.Write(line)
		if err == nil && n != len(line) {
			err = io.ErrShortWrite
		}
		if err != nil {
			return fmt.Errorf("%w: %v", errWatchOutput, err)
		}
		return nil
	}
	if ev.Type == "delivery" {
		f := eventFieldsOf(ev)
		if _, seen := w.seen.set[f.deliveryID]; f.deliveryID != "" && seen {
			queueAck() // this watch already successfully handled the delivery
			return nil
		}
		if w.sharedDisplay() && f.deliveryID != "" {
			err := errDisplayDeferred
			if w.shown != nil {
				ctx, cancel := context.WithTimeout(w.a.ctx, 250*time.Millisecond)
				err = w.shown.displayFor(ctx, w.identity, f.deliveryID, replay, printEvent)
				cancel()
			}
			if err != nil {
				if errors.Is(err, errDisplayDeferred) && w.deferred == nil {
					w.deferred = &monitorDeferred{kind: evType, data: data,
						after: w.cursor, resume: ev.Cursor, hub: normalizeHub(w.c.base)}
				}
				return err
			}
		} else if err := printEvent(); err != nil {
			return err
		}
		if f.deliveryID != "" {
			w.seen.add(f.deliveryID)
		}
		w.a.rememberEvents(w.ch, []envelope.Event{ev})
	} else if !w.deliveriesOnly {
		if err := printEvent(); err != nil {
			return err
		}
	}
	queueAck()
	return nil
}

// flushAck acks the newest handled cursor (one ack per batch).
func (w *watcher) flushAck() {
	if w.pendingAck == "" {
		return
	}
	cur := w.pendingAck
	if err := w.a.ack(w.c, cur); err != nil {
		if w.a.ctx.Err() == nil {
			var network *netError
			var hub *hubError
			if errors.As(err, &network) || (errors.As(err, &hub) && (hub.Status == 502 || hub.Status == 503 || hub.Status == 504)) {
				w.ackOutage = true
				w.outage.failed(fmt.Sprintf("ack %s: %v", cur, err))
			} else {
				fmt.Fprintf(w.a.stderr, "vecnet: watch: ack %s: %v\n", cur, err)
			}
		}
		return
	}
	w.pendingAck = ""
	if w.ackOutage {
		w.ackOutage = false
		// Ack success alone cannot establish recovery while the stream is
		// still unavailable. Conversely, live frames must not announce a
		// recovery followed by another outage while this ack remains owed.
		if w.hadLive {
			w.outage.live()
		}
	}
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
