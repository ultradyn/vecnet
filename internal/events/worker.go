package events

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/XertroV/vecnet/internal/egress"
	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/eventlog"
	"github.com/XertroV/vecnet/internal/hub"
	"github.com/XertroV/vecnet/internal/ids"
	"github.com/XertroV/vecnet/internal/push"
	"github.com/XertroV/vecnet/internal/webhook"
)

// Worker defaults and bounds.
const (
	DefaultInterval = time.Second // Run's poll interval
	ClientTimeout   = 30 * time.Second
	tailBatch       = 50 // log events read per subscription per Step
	drainBatch      = 50 // outbox rows considered per subscription per Step
	stepParallel    = 8  // subscriptions served at once; a Step waits for all of them
	previewBytes    = 1 << 10
	doneRetention   = 24 * time.Hour // how long settled outbox rows are kept
	// drainBudget stops a subscription starting a POST once it has held a Step
	// this long. A dead callback's 30 s timeout still slows the Step it is in,
	// but a slow one cannot hold it for a whole batch.
	drainBudget = ClientTimeout

	terminalGated = "gated" // the delivery may no longer be pushed
)

// deliveryPrefix marks an outbox row carrying a delivery Event.
const deliveryPrefix = string(ids.Dly) + "_"

// pushable are the only Event types a dot is ever pushed [R14].
var pushable = []string{"delivery", "receipt", "thread_updated", "approval_expired"}

// Worker tails each live subscription's log into event_outbox and drains the
// outbox to its callback. Zero fields take defaults: Store NewStore(Hub), Log
// Hub.Log, Client Hub.EgressPolicy().Client(ClientTimeout), Now Hub.Now,
// Interval DefaultInterval.
type Worker struct {
	Hub      *hub.Hub
	Store    *Store
	Log      *eventlog.Log
	Client   *http.Client
	Now      func() time.Time
	Interval time.Duration

	// ResolveDotPeer is the same seam as mcpserver.Server.ResolveDotPeer: it
	// names the peer an OAuth principal is bound to ("" when unbound), for
	// the authorisation re-check before each send. While nil, every OAuth
	// subscription fails that check, as it does at events/subscribe.
	ResolveDotPeer func(ctx context.Context, principalID string) (peerID string, err error)

	once sync.Once
	mu   sync.Mutex // one Step at a time
}

func (w *Worker) init() {
	w.once.Do(func() {
		if w.Store == nil {
			w.Store = NewStore(w.Hub)
		}
		if w.Log == nil {
			w.Log = w.Hub.Log
		}
		if w.Client == nil {
			w.Client = w.Hub.EgressPolicy().Client(ClientTimeout)
		}
	})
}

func (w *Worker) now() time.Time {
	if w.Now != nil {
		return w.Now()
	}
	return w.Hub.Now()
}

// Run calls Step every Interval until ctx ends. Errors are logged and the
// next interval runs.
func (w *Worker) Run(ctx context.Context) error {
	interval := w.Interval
	if interval <= 0 {
		interval = DefaultInterval
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		}
		if _, err := w.Step(ctx); err != nil && ctx.Err() == nil {
			slog.Error("events: step", "err", err)
		}
	}
}

// Step pauses subscriptions whose token has expired, then, for each live,
// verified, unpaused subscription, reads new log events into the outbox and
// drains what is due. It returns how many events were delivered (2xx).
func (w *Worker) Step(ctx context.Context) (int, error) {
	w.init()
	w.mu.Lock()
	defer w.mu.Unlock()
	now := w.now()
	if err := w.Store.pauseExpiredTokens(ctx, now); err != nil {
		return 0, err
	}
	subs, err := w.Store.Active(ctx, now)
	if err != nil {
		return 0, err
	}
	var (
		sent atomic.Int64
		wg   sync.WaitGroup
		sem  = make(chan struct{}, stepParallel)
		errs = make([]error, len(subs))
	)
	for i, s := range subs {
		if !s.Verified || s.PausedReason != "" {
			continue
		}
		sem <- struct{}{}
		wg.Go(func() {
			defer func() { <-sem }()
			if err := w.tail(ctx, s); err != nil {
				errs[i] = err
				return
			}
			n, err := w.drain(ctx, s)
			sent.Add(int64(n))
			errs[i] = err
		})
	}
	wg.Wait()
	oldest, err := w.Log.Oldest(ctx)
	if err != nil {
		return int(sent.Load()), err
	}
	if err := w.Store.pruneDone(ctx, now.Add(-doneRetention), oldest); err != nil {
		return int(sent.Load()), err
	}
	return int(sent.Load()), errors.Join(errs...)
}

// tail moves the next batch of s's log into the outbox. The cursor advances
// past every event read, pushed or filtered. A held wake_idle goes in parked:
// the release only clears a flag, so the row is what gets it pushed, once, with
// the original seq (PROTOCOL §5.2).
func (w *Worker) tail(ctx context.Context, s Subscription) error {
	evs, err := w.Log.Read(ctx, []string{s.PeerID}, s.Cursor, tailBatch)
	var purged eventlog.ErrCursorPurged
	if errors.As(err, &purged) {
		return w.Store.skipPurged(ctx, s.ID, s.Cursor, purged.Oldest, w.now())
	}
	if err != nil {
		return err
	}
	if len(evs) == 0 {
		return nil
	}
	types := s.Types
	if len(types) == 0 {
		types = DefaultTypes
	}
	thread, _ := s.Arguments["thread_id"].(string)
	rows := make([]outboxRow, 0, len(evs))
	to := s.Cursor
	for _, e := range evs {
		if to, err = eventlog.ParseCursor(e.Cursor); err != nil {
			return err
		}
		if !slices.Contains(types, e.Type) || !slices.Contains(pushable, e.Type) {
			continue
		}
		f := parseFields(e.Data)
		if thread != "" && f.threadID() != thread {
			continue
		}
		parked := false
		if e.Type == "delivery" {
			gate, err := w.Hub.DeliveryPushGate(ctx, f.DeliveryID)
			if err != nil {
				return err
			}
			if gate == hub.GateNever {
				continue
			}
			parked = gate == hub.GateHeld
		}
		id := e.Cursor
		if e.Type == "delivery" {
			id = f.DeliveryID
		}
		body, err := eventBody(id, s.Name, e, f)
		if err != nil {
			return err
		}
		rows = append(rows, outboxRow{eventID: id, seq: to.Seq, body: string(body), parked: parked})
	}
	return w.Store.enqueue(ctx, s.ID, s.Cursor, to, rows, w.now())
}

// eventFields are the parts of an Event's data the worker reads.
type eventFields struct {
	DeliveryID string `json:"delivery_id"`
	ThreadID   string `json:"thread_id"`
	Thread     struct {
		ThreadID string `json:"thread_id"`
	} `json:"thread"`
	MessageID string `json:"message_id"`
	Message   struct {
		MessageID string `json:"message_id"`
		ThreadID  string `json:"thread_id"`
		Content   struct {
			Text string `json:"text"`
		} `json:"content"`
		Attachments []struct {
			BlobURL string `json:"blob_url"`
		} `json:"attachments"`
	} `json:"message"`
}

func parseFields(data json.RawMessage) eventFields {
	var f eventFields
	_ = json.Unmarshal(data, &f) // a shape without these fields leaves them empty
	return f
}

func (f eventFields) threadID() string {
	switch {
	case f.Thread.ThreadID != "":
		return f.Thread.ThreadID
	case f.Message.ThreadID != "":
		return f.Message.ThreadID
	}
	return f.ThreadID
}

// preview is the data of an event over the push cap (PROTOCOL §4.4).
type preview struct {
	DeliveryID string `json:"delivery_id,omitempty"`
	MessageID  string `json:"message_id,omitempty"`
	ThreadID   string `json:"thread_id,omitempty"`
	Preview    string `json:"preview"`
	BlobURL    string `json:"blob_url,omitempty"`
}

// callbackBody is one event POST, the bytes every retry resends.
type callbackBody struct {
	EventID   string `json:"eventId"`
	Name      string `json:"name"`
	Timestamp string `json:"timestamp"`
	Data      any    `json:"data"`
	Cursor    string `json:"cursor"`
}

// eventBody renders e, replacing its data with a preview when the whole
// event would exceed the 256 KiB cap.
func eventBody(id, name string, e envelope.Event, f eventFields) ([]byte, error) {
	cb := callbackBody{EventID: id, Name: name, Timestamp: e.At, Data: e, Cursor: e.Cursor}
	body, err := json.Marshal(cb)
	if err != nil {
		return nil, fmt.Errorf("events: encode event: %w", err)
	}
	if len(body) <= hub.MaxPushBody {
		return body, nil
	}
	p := preview{DeliveryID: f.DeliveryID, MessageID: f.Message.MessageID, ThreadID: f.threadID(),
		Preview: clip(f.Message.Content.Text, previewBytes)}
	if p.MessageID == "" {
		p.MessageID = f.MessageID
	}
	if p.Preview == "" {
		p.Preview = clip(string(e.Data), previewBytes)
	}
	for _, a := range f.Message.Attachments {
		if a.BlobURL != "" {
			p.BlobURL = a.BlobURL
			break
		}
	}
	cb.Data = p
	if body, err = json.Marshal(cb); err != nil {
		return nil, fmt.Errorf("events: encode preview: %w", err)
	}
	return body, nil
}

// clip is the longest prefix of s of at most n bytes ending on a rune boundary.
func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// drain POSTs s's due rows, oldest first, stopping at the first row that is
// not due or fails: a callback in backoff waits as a whole, so an outage
// spends one row's attempts rather than every row's. A terminal answer closes
// only its row, as does a delivery the gate no longer passes. Each send first
// re-checks the subscription, its authorisation and the gate as they stand
// then: a POST can take 30 s, and much can change over a batch of them. A
// released parked row has a stricter gate than a read one: an ack or a push
// through another subscription has already put the message in front of the
// dot, and the point of the row was to do that.
func (w *Worker) drain(ctx context.Context, sub Subscription) (int, error) {
	rows, err := w.Store.pending(ctx, sub.ID, drainBatch)
	if err != nil {
		return 0, err
	}
	sent := 0
	started := w.now()
	for _, r := range rows {
		now := w.now()
		if r.next != nil && *r.next > envelope.FormatTime(now) {
			break
		}
		s, live, err := w.current(ctx, sub.ID, now)
		if err != nil || !live {
			return sent, err
		}
		if now.Sub(started) >= drainBudget {
			break
		}
		claimed := false
		if strings.HasPrefix(r.eventID, deliveryPrefix) {
			gate := w.Hub.DeliveryGate
			if r.parked {
				gate = w.Hub.DeliveryUnread
			}
			ok, err := gate(ctx, r.eventID)
			if err == nil && ok {
				// The claim, not the read gate, is what a concurrent §17.5
				// supersession cannot pass: from here the wake stays out of
				// its reach until the attempt is recorded.
				ok, err = w.Hub.BeginDispatch(ctx, r.eventID)
				claimed = ok
			}
			if err != nil {
				return sent, err
			}
			if !ok {
				if err := w.Store.withdraw(ctx, s.ID, r.eventID, now); err != nil {
					return sent, err
				}
				continue
			}
		}
		status, retryAfter, netErr := w.post(ctx, s, r)
		var delivered, stop bool
		if netErr == nil || ctx.Err() == nil {
			delivered, stop, err = w.record(context.WithoutCancel(ctx), s, r, status, retryAfter, netErr)
		}
		if claimed {
			if endErr := w.Hub.EndDispatch(ctx, r.eventID, false); err == nil {
				err = endErr
			}
		}
		if netErr != nil && ctx.Err() != nil {
			return sent, err // shutting down: this attempt does not count
		}
		if err != nil {
			return sent, err
		}
		if delivered {
			sent++
		}
		if stop {
			break
		}
	}
	return sent, nil
}

// current re-reads a subscription and reports whether it may send at now
// (PROTOCOL §4.4), pausing it if authorisation has gone. Nothing is dropped
// and the cursor stays. An unsubscribed, lapsed, paused or unverified one
// simply stops, as it would for Step. Reading the row afresh means a refresh
// during the drain counts: its new token is not paused as the old one's
// expiry, and a pause or a challenge falling due mid-batch stops the batch.
func (w *Worker) current(ctx context.Context, id string, now time.Time) (Subscription, bool, error) {
	s, err := w.Store.Get(ctx, id)
	if errors.Is(err, ErrNotFound) {
		return Subscription{}, false, nil
	}
	if err != nil {
		return Subscription{}, false, err
	}
	if s.TokenExpires != nil && !s.TokenExpires.After(now) {
		return s, false, w.Store.pause(ctx, id, PausedTokenExpired, now)
	}
	if (s.Expires != nil && !s.Expires.After(now)) || s.PausedReason != "" || !s.Verified {
		return s, false, nil
	}
	ok, err := w.authorised(ctx, s)
	if err != nil {
		return s, false, err
	}
	if !ok {
		return s, false, w.Store.pause(ctx, id, PausedUnauthorized, now)
	}
	return s, true, nil
}

// authorised reports whether s's peer may still be pushed to on its
// principal's behalf.
func (w *Worker) authorised(ctx context.Context, s Subscription) (bool, error) {
	ok, err := w.Store.peerAuthorised(ctx, s)
	if err != nil || !ok {
		return false, err
	}
	if s.PrincipalKind != KindOAuth {
		return true, nil
	}
	if w.ResolveDotPeer == nil {
		return false, nil
	}
	bound, err := w.ResolveDotPeer(ctx, s.PrincipalID)
	if err != nil {
		return false, err
	}
	return bound == s.PeerID, nil
}

// post signs and sends one row. webhook-timestamp and webhook-signature are
// fresh on every attempt; the body and webhook-id never change.
func (w *Worker) post(ctx context.Context, s Subscription, r outboxRow) (status int, retryAfter time.Duration, err error) {
	now := w.now()
	secrets, err := w.Store.secrets(ctx, s.ID, now)
	if err != nil {
		return 0, 0, err
	}
	sig, err := webhook.SignAll(secrets, r.eventID, now, []byte(r.body))
	if err != nil {
		return 0, 0, fmt.Errorf("events: sign: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.URL, bytes.NewReader([]byte(r.body)))
	if err != nil {
		return 0, 0, errors.New("events: stored callback URL does not parse")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(webhook.HeaderID, r.eventID)
	req.Header.Set(webhook.HeaderTimestamp, strconv.FormatInt(now.Unix(), 10))
	req.Header.Set(webhook.HeaderSignature, sig)
	req.Header.Set(HeaderSubscription, s.ID)
	resp, err := w.Client.Do(req)
	if err != nil {
		if resp != nil && errors.Is(err, egress.ErrRedirect) {
			return resp.StatusCode, 0, nil // refused, not followed; the body is already closed
		}
		return 0, 0, err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, hub.PushResponseCap))
	return resp.StatusCode, push.ParseRetryAfter(resp.Header.Get("Retry-After"), now), nil
}

// record applies one POST's outcome: 2xx delivers; 410 and 413 are terminal;
// anything else uses an attempt, and the last attempt gives the row up and
// pauses the subscription. Only a 2xx changes the Delivery: a failure is one
// subscription's, the Delivery stays readable through vecnet_inbox, and a
// re-subscribe from the cursor still replays an exhausted row, which a
// dead_letter would gate. If nothing reaches it, it expires (PROTOCOL §5.1).
func (w *Worker) record(ctx context.Context, s Subscription, r outboxRow, status int, retryAfter time.Duration, netErr error) (delivered, stop bool, err error) {
	now := w.now()
	w.Hub.ObservePush(ctx, s.PeerID, netErr == nil && status >= 200 && status < 300)
	switch {
	case netErr == nil && status >= 200 && status < 300:
		if err := w.Store.settle(ctx, s.ID, r.eventID, "", status, now); err != nil {
			return false, true, err
		}
		if strings.HasPrefix(r.eventID, deliveryPrefix) {
			if err := w.Hub.AcceptEventPush(ctx, r.eventID, now); err != nil {
				return true, true, err
			}
		}
		return true, false, nil
	case netErr == nil && (status == http.StatusGone || status == http.StatusRequestEntityTooLarge):
		return false, false, w.Store.settle(ctx, s.ID, r.eventID, "http_"+strconv.Itoa(status), status, now)
	}
	if netErr != nil {
		slog.Warn("events: callback failed", "subscription", s.ID, "event", r.eventID, "err", hub.EgressErr(netErr))
	}
	attempts := r.attempts + 1
	if attempts >= hub.PushAttempts {
		if err := w.Store.settle(ctx, s.ID, r.eventID, "exhausted", status, now); err != nil {
			return false, true, err
		}
		return false, true, w.Store.pause(ctx, s.ID, PausedDeliveryFailed, now)
	}
	next := now.Add(hub.PushBackoff(attempts, retryAfter, nil))
	return false, true, w.Store.retryLater(ctx, s.ID, r.eventID, attempts, status, next, now)
}
