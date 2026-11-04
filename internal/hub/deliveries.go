package hub

import (
	"cmp"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"slices"
	"strconv"
	"time"

	"github.com/XertroV/vecnet/internal/audit"
	"github.com/XertroV/vecnet/internal/envelope"
)

// Push delivery state machine (PROTOCOL §3.3, §3.4, §5.1, §5.2, §5.5).
const (
	// PushLease is how long a claimed delivery stays "pushed" before a
	// crashed worker's claim lapses and it is due again.
	PushLease = 10 * time.Minute
	// MaxPushBackoff caps the retry backoff and an honoured Retry-After.
	MaxPushBackoff = time.Hour
	// PushResponseCap is how much of a receiver's response body is read;
	// the rest is ignored, its status still counts (§3.3).
	PushResponseCap = 4 << 10
	// MaxPushBody is the largest push body (§3.1).
	MaxPushBody = 256 << 10
)

// Counter names for push outcomes (the dashboard counters, §5.5).
const (
	CounterDeadLetters   = "dead_letters"
	CounterPushConflicts = "push_conflicts"
	// CounterPushAuthFailed counts routine POSTs refused for a dead key or
	// a disabled routine (PROTOCOL §13.3).
	CounterPushAuthFailed = "push_auth_failed"
	// CounterEventsPushFailed counts failed MCP Events delivery attempts.
	CounterEventsPushFailed = "events_push_failed"
	// CounterEventsPaused counts MCP Events pauses caused by delivery or auth failures.
	CounterEventsPaused = "events_paused"
)

// PushBackoff is the retry delay after attempt failed (PROTOCOL §3.3):
// base min(1 h, 2^attempt s), or min(1 h, retryAfter) when the receiver sent
// a positive Retry-After, plus jitter of up to 10 % of the base drawn from
// rnd (math/rand when nil). push.Backoff is this function.
func PushBackoff(attempt int, retryAfter time.Duration, rnd func() float64) time.Duration {
	base := MaxPushBackoff
	if retryAfter > 0 {
		base = min(MaxPushBackoff, retryAfter)
	} else if attempt < 0 {
		base = time.Second
	} else if attempt < 12 { // 2^12 s > 1 h
		base = min(MaxPushBackoff, time.Duration(1<<attempt)*time.Second)
	}
	if rnd == nil {
		rnd = rand.Float64
	}
	j := rnd()
	if j < 0 || j >= 1 {
		j = 0
	}
	return base + time.Duration(j*float64(base)/10)
}

// DueDelivery is one claimed push: the route to push through (Kind is its
// routes.kind), its active keys (newest first; none on a grokbot route) and
// the exact bytes of the Delivery Event, which every attempt resends
// unchanged (§3.3).
type DueDelivery struct {
	DeliveryID, Recipient, ThreadID, RouteID, URL, Kind string
	Keys                                                []string
	Attempt                                             int    // this attempt's number, 1-based
	Body                                                []byte // the Delivery JSON (with per-recipient blob_url), as appended to the log
}

// ClaimDue claims up to limit deliveries due for a push at now and marks
// them pushed (attempt+1, next_attempt_at = now + PushLease). A delivery is
// due when it is queued (or pushed with a lapsed lease), not hold, not held,
// not past expires_at, has a route that answered its verification challenge
// (§3.2), its next_attempt_at has come, and no earlier pushable delivery to
// the same recipient in the same thread is still queued or pushed (§3.4).
// Deliveries of peers without a route are never claimed.
func (h *Hub) ClaimDue(ctx context.Context, now time.Time, limit int) ([]DueDelivery, error) {
	if limit <= 0 {
		return nil, nil
	}
	nowS := envelope.FormatTime(now)
	var out []DueDelivery
	err := h.DB.Tx(ctx, func(tx *sql.Tx) error {
		out = nil
		rows, err := tx.QueryContext(ctx,
			`SELECT d.dly_id, d.recipient, d.thr_id, d.route_id, r.url, r.kind, d.attempt, e.data
			 FROM deliveries d
			 JOIN routes r ON r.route_id = d.route_id
			 JOIN events e ON e.seq = d.event_seq
			 WHERE d.state IN ('queued', 'pushed') AND d.priority != 'hold' AND d.held = 0
			   AND r.verified_at IS NOT NULL
			   AND (d.next_attempt_at IS NULL OR d.next_attempt_at <= ?1)
			   AND d.expires_at > ?1
			   AND NOT EXISTS (
			     SELECT 1 FROM deliveries p
			     WHERE p.recipient = d.recipient AND p.thr_id = d.thr_id AND p.event_seq < d.event_seq
			       AND p.state IN ('queued', 'pushed') AND p.priority != 'hold' AND p.held = 0
			       AND p.route_id IS NOT NULL)
			 ORDER BY d.event_seq
			 LIMIT ?2`, nowS, limit)
		if err != nil {
			return fmt.Errorf("hub: claim due: %w", err)
		}
		for rows.Next() {
			var d DueDelivery
			var body string
			if err := rows.Scan(&d.DeliveryID, &d.Recipient, &d.ThreadID, &d.RouteID, &d.URL, &d.Kind, &d.Attempt, &body); err != nil {
				rows.Close()
				return fmt.Errorf("hub: claim due: %w", err)
			}
			d.Body = []byte(body)
			d.Attempt++
			out = append(out, d)
		}
		if err := rows.Close(); err != nil {
			return fmt.Errorf("hub: claim due: %w", err)
		}
		if err := rows.Err(); err != nil {
			return fmt.Errorf("hub: claim due: %w", err)
		}
		lease := envelope.FormatTime(now.Add(PushLease))
		keys := map[string][]string{}
		for i := range out {
			d := &out[i]
			if _, err := tx.ExecContext(ctx,
				`UPDATE deliveries SET state = 'pushed', attempt = ?, next_attempt_at = ?, updated_at = ? WHERE dly_id = ?`,
				d.Attempt, lease, nowS, d.DeliveryID); err != nil {
				return fmt.Errorf("hub: claim delivery: %w", err)
			}
			ks, ok := keys[d.RouteID]
			if !ok {
				if ks, err = routeKeys(ctx, tx, d.RouteID, now); err != nil {
					return err
				}
				keys[d.RouteID] = ks
			}
			d.Keys = ks
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// DeferPush gives back a claim that was not attempted: the delivery is
// queued again until next with its attempt and attempts_left as they were
// before ClaimDue. It stays in line, so §3.4 keeps its thread's later
// deliveries behind it, unless a newer reaction wake supersedes it (§17.5).
// A delivery that moved on meanwhile is left alone.
func (h *Hub) DeferPush(ctx context.Context, d DueDelivery, next time.Time) error {
	at := h.nowStr()
	return h.DB.Tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			`UPDATE deliveries SET state = 'queued', attempt = attempt - 1, next_attempt_at = ?, updated_at = ?
		 WHERE dly_id = ? AND state = 'pushed' AND attempt = ?`,
			envelope.FormatTime(next), at, d.DeliveryID, d.Attempt)
		if err != nil {
			return fmt.Errorf("hub: defer push: %w", err)
		}
		if n, err := res.RowsAffected(); err != nil {
			return fmt.Errorf("hub: defer push: %w", err)
		} else if n == 0 {
			return nil
		}
		return supersedeRequeuedReaction(ctx, tx, d.DeliveryID, at)
	})
}

// pushOutcome classifies a push result (§3.3).
type pushOutcome int

const (
	pushAccepted pushOutcome = iota
	pushRetry
	pushConflict
	pushTerminal
	pushAuthFailed // grokbot only: dead letter, counted apart (§13.3)
)

func classifyPush(status int, netErr error) pushOutcome {
	switch {
	case netErr != nil:
		return pushRetry
	case status >= 200 && status < 300:
		return pushAccepted
	case status == 408 || status == 429 || (status >= 500 && status < 600):
		return pushRetry
	case status == 409:
		return pushConflict
	case status >= 300 && status < 500:
		return pushTerminal // 3xx (never followed), 410, 413, any other 4xx
	default:
		return pushRetry // 1xx or a nonsense status: treat like a network error
	}
}

// PushResult records the outcome of pushing d (PROTOCOL §3.3, §5.1, §5.5):
//
//   - 2xx: accepted; receipt accepted, by hub, to the sender; reachable.
//   - 408, 429, 5xx, network error: one attempt used; with none left
//     dead_letter, else queued again at now + PushBackoff(d.Attempt,
//     retryAfter).
//   - 409: failed, never retried; receipt failed to the sender, counter
//     push_conflicts, audit push_conflict.
//   - 3xx, 410, 413, any other 4xx: dead_letter at once.
//
// A dead letter sends a dead_letter receipt to the sender, bumps the
// dead_letters counter and is audited. Every non-2xx marks the recipient
// unreachable. A result for a delivery that is no longer pushed at
// d.Attempt (acked, expired or re-claimed meanwhile) is ignored.
func (h *Hub) PushResult(ctx context.Context, d DueDelivery, status int, retryAfter time.Duration, netErr error, now time.Time) error {
	return h.recordPush(ctx, d, classifyPush(status, netErr), status, retryAfter, netErr, now)
}

// recordPush applies outcome; see PushResult and GrokbotPushResult.
func (h *Hub) recordPush(ctx context.Context, d DueDelivery, outcome pushOutcome, status int, retryAfter time.Duration, netErr error, now time.Time) error {
	nowS := envelope.FormatTime(now)
	var lastStatus any
	if netErr == nil {
		lastStatus = status
	}
	var lastErr any
	if netErr != nil {
		msg := EgressErr(netErr)
		if len(msg) > 512 {
			msg = msg[:512]
		}
		lastErr = msg
	} else if outcome != pushAccepted {
		lastErr = "HTTP " + strconv.Itoa(status)
	}
	var notify string
	var alert *envelope.Event // dead_letter / push_conflict receipt for the operator's notify routes
	applied := false
	err := h.DB.Tx(ctx, func(tx *sql.Tx) error {
		notify, applied, alert = "", false, nil
		var state, msgID, thr, sender string
		var attempt, left int
		var senderIsPeer bool
		err := tx.QueryRowContext(ctx,
			`SELECT d.state, d.attempt, d.attempts_left, d.msg_id, d.thr_id, m.sender,
			        EXISTS (SELECT 1 FROM peers s WHERE s.peer_id = m.sender)
			 FROM deliveries d JOIN messages m ON m.msg_id = d.msg_id WHERE d.dly_id = ?`, d.DeliveryID).
			Scan(&state, &attempt, &left, &msgID, &thr, &sender, &senderIsPeer)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("hub: push result: %w", err)
		}
		if state != "pushed" || attempt != d.Attempt {
			return nil // stale: the delivery moved on without this result
		}
		applied = true
		alerts := func(state string) bool {
			return state == "dead_letter" || outcome == pushConflict
		}
		receipt := func(state, note string) error {
			if !senderIsPeer {
				if alerts(state) {
					// No sender log to append to: the operator still hears of it.
					r := envelope.Receipt{DeliveryID: d.DeliveryID, MessageID: msgID, ThreadID: thr,
						State: state, By: "hub", At: nowS, Note: note}
					data, err := json.Marshal(r)
					if err != nil {
						return fmt.Errorf("hub: encode receipt: %w", err)
					}
					alert = &envelope.Event{Type: "receipt", At: nowS, Data: data}
				}
				return nil
			}
			r := envelope.Receipt{DeliveryID: d.DeliveryID, MessageID: msgID, ThreadID: thr,
				State: state, By: "hub", At: nowS, Note: note}
			cur, err := h.Log.Append(ctx, tx, sender, "receipt", now, r)
			if err != nil {
				return err
			}
			notify = sender
			if alerts(state) {
				data, err := json.Marshal(r)
				if err != nil {
					return fmt.Errorf("hub: encode receipt: %w", err)
				}
				alert = &envelope.Event{Cursor: cur.String(), Type: "receipt", At: nowS, Data: data}
			}
			return nil
		}
		set := func(state string, left int, next any) error {
			if _, err := tx.ExecContext(ctx,
				`UPDATE deliveries SET state = ?, attempts_left = ?, next_attempt_at = ?, last_status = ?, last_error = ?, updated_at = ?
				 WHERE dly_id = ?`, state, left, next, lastStatus, lastErr, nowS, d.DeliveryID); err != nil {
				return fmt.Errorf("hub: push result: %w", err)
			}
			return nil
		}
		deadLetter := func(left int, note string) error {
			if err := set("dead_letter", max(left, 0), nil); err != nil {
				return err
			}
			if err := receipt("dead_letter", note); err != nil {
				return err
			}
			if err := BumpCounter(ctx, tx, CounterDeadLetters, 1); err != nil {
				return err
			}
			return audit.Record(ctx, tx, "hub", "dead_letter", []string{d.DeliveryID, msgID, d.Recipient},
				map[string]any{"attempt": d.Attempt, "status": lastStatus})
		}
		switch outcome {
		case pushAccepted:
			if err := set("accepted", left, nil); err != nil {
				return err
			}
			return receipt("accepted", "")
		case pushRetry:
			left--
			if left <= 0 {
				return deadLetter(left, "push retries exhausted")
			}
			next := envelope.FormatTime(now.Add(PushBackoff(d.Attempt, retryAfter, nil)))
			if err := set("queued", left, next); err != nil {
				return err
			}
			return supersedeRequeuedReaction(ctx, tx, d.DeliveryID, nowS)
		case pushConflict:
			if err := set("failed", left-1, nil); err != nil {
				return err
			}
			if err := receipt("failed", "push_conflict: the receiver holds different bytes for this delivery_id"); err != nil {
				return err
			}
			if err := BumpCounter(ctx, tx, CounterPushConflicts, 1); err != nil {
				return err
			}
			return audit.Record(ctx, tx, "hub", "push_conflict", []string{d.DeliveryID, msgID, d.Recipient},
				map[string]any{"attempt": d.Attempt})
		case pushAuthFailed:
			if err := deadLetter(left-1, "push_auth_failed: the routine refused its key or is disabled: HTTP "+strconv.Itoa(status)); err != nil {
				return err
			}
			if err := BumpCounter(ctx, tx, CounterPushAuthFailed, 1); err != nil {
				return err
			}
			return audit.Record(ctx, tx, "hub", "push_auth_failed", []string{d.DeliveryID, msgID, d.Recipient},
				map[string]any{"attempt": d.Attempt, "status": status})
		default:
			return deadLetter(left-1, "push rejected: HTTP "+strconv.Itoa(status))
		}
	})
	if err != nil {
		return err
	}
	if !applied {
		return nil
	}
	if notify != "" {
		h.Log.Notify(notify)
	}
	if alert != nil {
		h.Notify(ctx, *alert, NotifyDeadLetters)
	}
	h.ObservePush(ctx, d.Recipient, outcome == pushAccepted)
	return nil
}

// PushGate is what priority and state say about pushing a Delivery to a dot.
type PushGate int

const (
	// GateNever: hold, a terminal state, or no such delivery.
	GateNever PushGate = iota
	// GatePush: it may go now.
	GatePush
	// GateHeld: a held wake_idle, which goes once when released.
	GateHeld
)

// DeliveryPushGate decides whether a dot may be pushed the Delivery Event of
// deliveryID (PROTOCOL §5.2): never for hold, for a delivery in a terminal
// state (§5.1: failed, expired or dead_letter, whose sender may already have
// been told), or for an id with no delivery row; not yet for a held wake_idle.
// The Delivery stays readable through vecnet_inbox either way.
func (h *Hub) DeliveryPushGate(ctx context.Context, deliveryID string) (PushGate, error) {
	var priority, state string
	var held bool
	err := h.DB.QueryRowContext(ctx, `SELECT priority, held, state FROM deliveries WHERE dly_id = ?`, deliveryID).
		Scan(&priority, &held, &state)
	if errors.Is(err, sql.ErrNoRows) {
		return GateNever, nil
	}
	if err != nil {
		return GateNever, fmt.Errorf("hub: delivery gate: %w", err)
	}
	switch {
	case state == "failed", state == "expired", state == "dead_letter", state == "superseded", priority == "hold":
		return GateNever, nil
	case held:
		return GateHeld, nil
	}
	return GatePush, nil
}

// DeliveryGate reports whether a dot may be pushed the Delivery Event of
// deliveryID right now (DeliveryPushGate).
func (h *Hub) DeliveryGate(ctx context.Context, deliveryID string) (push bool, err error) {
	gate, err := h.DeliveryPushGate(ctx, deliveryID)
	return gate == GatePush, err
}

// DeliveryUnread reports whether a released delivery still needs its one push
// (PROTOCOL §5.2): not held, and nothing has accepted it yet. An ack, a
// receipt or a push through another subscription means it needs none.
func (h *Hub) DeliveryUnread(ctx context.Context, deliveryID string) (bool, error) {
	var due bool
	err := h.DB.QueryRowContext(ctx,
		`SELECT state IN ('queued', 'pushed') AND held = 0 FROM deliveries WHERE dly_id = ?`, deliveryID).Scan(&due)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("hub: delivery unread: %w", err)
	}
	return due, nil
}

// AcceptEventPush records a 2xx from a dot's Events callback for deliveryID:
// accepted = 2xx on push (PROTOCOL §5.1), with the receipt by hub of §3.3. A
// delivery past queued or pushed (a second subscription's 2xx, an ack, an
// expiry) is left alone.
func (h *Hub) AcceptEventPush(ctx context.Context, deliveryID string, now time.Time) error {
	var notify string
	err := h.DB.Tx(ctx, func(tx *sql.Tx) error {
		notify = ""
		var msgID, thr, sender string
		var senderIsPeer bool
		err := tx.QueryRowContext(ctx,
			`UPDATE deliveries SET state = 'accepted', held = 0, next_attempt_at = NULL, updated_at = ?
			 WHERE dly_id = ? AND state IN ('queued', 'pushed') RETURNING msg_id, thr_id`,
			envelope.FormatTime(now), deliveryID).Scan(&msgID, &thr)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("hub: accept event push: %w", err)
		}
		if err := tx.QueryRowContext(ctx,
			`SELECT m.sender, EXISTS (SELECT 1 FROM peers s WHERE s.peer_id = m.sender) FROM messages m WHERE m.msg_id = ?`,
			msgID).Scan(&sender, &senderIsPeer); err != nil {
			return fmt.Errorf("hub: delivery sender: %w", err)
		}
		if !senderIsPeer {
			return nil // a hub-authored message: no peer log to receive it
		}
		r := envelope.Receipt{DeliveryID: deliveryID, MessageID: msgID, ThreadID: thr,
			State: "accepted", By: "hub", At: envelope.FormatTime(now)}
		if _, err := h.Log.Append(ctx, tx, sender, "receipt", now, r); err != nil {
			return err
		}
		notify = sender
		return nil
	})
	if err != nil {
		return err
	}
	if notify != "" {
		h.Log.Notify(notify)
	}
	return nil
}

// BumpCounter adds n to the dashboard counter name inside tx. Zero is a no-op.
func BumpCounter(ctx context.Context, tx *sql.Tx, name string, n int64) error {
	if n == 0 {
		return nil
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO counters(name, value) VALUES (?, ?) ON CONFLICT(name) DO UPDATE SET value = value + excluded.value`, name, n); err != nil {
		return fmt.Errorf("hub: count %s: %w", name, err)
	}
	return nil
}

// delivery is a Delivery row being written by the send pipeline.
type delivery struct {
	recipient string
	priority  string
	held      bool
}

// GateWakeIdle decides at send time whether d is held (PROTOCOL §5.2): a
// wake_idle delivery is held unless the recipient's effective presence is
// idle or unknown (stale presence counts as unknown). Every other priority
// is never held.
func (h *Hub) GateWakeIdle(ctx context.Context, tx *sql.Tx, d *delivery) error {
	d.held = false
	if d.priority != "wake_idle" {
		return nil
	}
	var p PresenceRow
	if err := tx.QueryRowContext(ctx, `SELECT kind, presence_state, presence_at FROM peers WHERE peer_id = ?`, d.recipient).
		Scan(&p.Kind, &p.State, &p.At); err != nil {
		return fmt.Errorf("hub: recipient presence: %w", err)
	}
	switch h.EffectivePresence(p, h.Now()) {
	case "idle", "unknown":
	default:
		d.held = true
	}
	return nil
}

// OnIdle releases peerID's held wake_idle deliveries so each is pushed
// once (PROTOCOL §5.2): the push worker sees the flag clear, and the Events
// worker its parked outbox row become due. New wires it as the IdleHook of an
// idle presence report; the staleness ticker calls it when a presence turns
// unknown.
func (h *Hub) OnIdle(ctx context.Context, peerID string) error {
	if _, err := h.DB.ExecContext(ctx,
		`UPDATE deliveries SET held = 0, updated_at = ? WHERE recipient = ? AND held = 1 AND state = 'queued'`,
		h.nowStr(), peerID); err != nil {
		return fmt.Errorf("hub: release held deliveries: %w", err)
	}
	return nil
}

// ExpireDeliveries moves every queued or pushed delivery whose expires_at
// is before now to expired, and appends an expired receipt, by hub, to each
// sender's log (PROTOCOL §5.1). It returns how many expired. Expired is
// terminal: a later ack or push result does not change it.
func (h *Hub) ExpireDeliveries(ctx context.Context, now time.Time) (n int, err error) {
	nowS := envelope.FormatTime(now)
	var notify []string
	err = h.DB.Tx(ctx, func(tx *sql.Tx) error {
		n, notify = 0, nil
		rows, err := tx.QueryContext(ctx,
			`UPDATE deliveries SET state = 'expired', held = 0, next_attempt_at = NULL, updated_at = ?1
			 WHERE state IN ('queued', 'pushed') AND expires_at < ?1
			 RETURNING dly_id, msg_id, thr_id, event_seq`, nowS)
		if err != nil {
			return fmt.Errorf("hub: expire deliveries: %w", err)
		}
		type row struct {
			dly, msg, thr string
			seq           int64
		}
		var got []row
		for rows.Next() {
			var r row
			if err := rows.Scan(&r.dly, &r.msg, &r.thr, &r.seq); err != nil {
				rows.Close()
				return fmt.Errorf("hub: expire deliveries: %w", err)
			}
			got = append(got, r)
		}
		if err := rows.Close(); err != nil {
			return fmt.Errorf("hub: expire deliveries: %w", err)
		}
		if err := rows.Err(); err != nil {
			return fmt.Errorf("hub: expire deliveries: %w", err)
		}
		slices.SortFunc(got, func(a, b row) int { return cmp.Compare(a.seq, b.seq) })
		for _, r := range got {
			var sender string
			var senderIsPeer bool
			if err := tx.QueryRowContext(ctx,
				`SELECT m.sender, EXISTS (SELECT 1 FROM peers s WHERE s.peer_id = m.sender) FROM messages m WHERE m.msg_id = ?`,
				r.msg).Scan(&sender, &senderIsPeer); err != nil {
				return fmt.Errorf("hub: expired delivery sender: %w", err)
			}
			if !senderIsPeer {
				continue
			}
			rc := envelope.Receipt{DeliveryID: r.dly, MessageID: r.msg, ThreadID: r.thr, State: "expired", By: "hub", At: nowS}
			if _, err := h.Log.Append(ctx, tx, sender, "receipt", now, rc); err != nil {
				return err
			}
			notify = append(notify, sender)
		}
		n = len(got)
		return nil
	})
	if err != nil {
		return 0, err
	}
	if len(notify) > 0 {
		h.Log.Notify(notify...)
	}
	return n, nil
}
