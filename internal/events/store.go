// Package events is the MCP Events webhook profile (PROTOCOL §4.4): durable
// subscriptions, the verification challenge, and the worker that tails each
// subscription's peer log into signed POSTs. A subscription's cursor is a
// position in that log, so replay and outage queueing need no second queue;
// event_outbox holds what was read, with its body until it settles.
package events

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"sync"
	"time"

	"github.com/XertroV/vecnet/internal/audit"
	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/eventlog"
	"github.com/XertroV/vecnet/internal/hub"
	"github.com/XertroV/vecnet/internal/ids"
	"github.com/XertroV/vecnet/internal/store"
)

// Name is the one event in the catalogue.
const Name = "vecnet.message"

const (
	// MaxTTL caps a subscription's lifetime (PROTOCOL §4.4).
	MaxTTL = 24 * time.Hour
	// RefreshMargin is how long before expiry ChatGPT should re-subscribe.
	// The operator snapshot and events/subscribe use the same deadline.
	RefreshMargin = 10 * time.Minute
	// RefreshInterval is the default cadence for clients to re-subscribe.
	RefreshInterval = 30 * time.Minute
	// SecretOverlap is how long the previous secret keeps signing after a
	// refresh supplies a new one.
	SecretOverlap = 10 * time.Minute
	// ChallengeMaxAge bounds verification reuse for an unchanged signing key.
	// Rotating the key always invalidates a row's previous verification.
	ChallengeMaxAge = 5 * time.Minute
	// MaxPerPrincipal bounds one principal's live subscriptions, since each is
	// served by every worker Step.
	MaxPerPrincipal = 16
)

// subPrefix tags a subscription id.
const subPrefix ids.Prefix = "sub"

// Principal kinds.
const (
	KindOAuth  = "oauth"
	KindVecnet = "vecnet"
)

// Paused reasons.
const (
	PausedTokenExpired   = "token_expired"
	PausedDeliveryFailed = "delivery_failed"
	PausedUnauthorized   = "unauthorized"
	// PausedCredentialRotated is set when the bearer that subscribed is
	// rotated; the holder of the new one subscribes again.
	PausedCredentialRotated = hub.PausedCredentialRotated
	// PausedOperator is the operator's stop. A re-subscribe, which clears every
	// other reason, leaves it in place.
	PausedOperator = hub.PausedOperator
)

var (
	// ErrNotFound reports a subscription id with no row.
	ErrNotFound = errors.New("events: no such subscription")
	// ErrTooMany reports a principal at MaxPerPrincipal live subscriptions.
	ErrTooMany = errors.New("events: too many subscriptions")
)

// DefaultTypes is what a subscription receives when its arguments list none
// (PROTOCOL §4.4).
var DefaultTypes = []string{"delivery"}

// Subscription is one row of event_subscriptions with its worker schedule.
// The id is a routing handle, not a credential.
type Subscription struct {
	ID, PrincipalKind, PrincipalID, PeerID, Name, URL string
	Arguments                                         map[string]any
	Types                                             []string
	Cursor                                            eventlog.Cursor
	Expires                                           *time.Time
	Verified                                          bool
	PausedReason                                      string

	RefreshBefore     *time.Time // deadline returned by the last successful subscription
	NextRefreshBefore *time.Time // mutable worker schedule; never projected as client advice
	VerifiedAt        *time.Time // the last passed challenge; nil when one is due
	TokenExpires      *time.Time // nil for a vecnet bearer, whose credential has no expiry
	TokenHash         string     // the subscribing OAuth access token's hash; "" for a vecnet bearer
	Truncated         bool       // events were skipped as purged since the last events/subscribe
}

// Store is the subscription table. Key is meta.hub_key, under which the
// secrets are sealed; a nil Now means time.Now.
type Store struct {
	hub *hub.Hub // non-nil for the live hub; owns operator invalidations
	DB  *store.DB
	Key []byte
	Now func() time.Time

	verificationMu sync.Mutex
	verifications  map[verificationKey]time.Time
	callbackOnce   sync.Once
	callbackLocks  [64]chan struct{}

	// Protect against overlapping Step and publication wakes. The applied
	// scheduling policy is checkpointed with its deadlines in the database.
	refreshMu sync.Mutex
}

// NewStore returns the Store of hub h.
func NewStore(h *hub.Hub) *Store {
	s := &Store{DB: h.DB, Now: h.Now, hub: h}
	if h.Blobs != nil {
		s.Key = h.Blobs.Key
	}
	return s
}

func (st *Store) now() time.Time {
	if st.Now != nil {
		return st.Now()
	}
	return time.Now()
}

// canonicalArgs is the stored and compared form of arguments: encoding/json
// sorts map keys, so equal arguments are equal strings.
func canonicalArgs(args map[string]any) (string, error) {
	if args == nil {
		return "{}", nil
	}
	b, err := json.Marshal(args)
	if err != nil {
		return "", fmt.Errorf("events: encode arguments: %w", err)
	}
	return string(b), nil
}

// Outbox predicates. terminal holds "http_410" or "http_413" for a callback's
// final refusal, "exhausted" when attempts ran out and "gated" when the
// delivery may no longer be pushed.
const (
	// neverRetried rows stay settled through a rewind (PROTOCOL §4.4).
	neverRetried = `coalesce(terminal, '') IN ('http_410', 'http_413')`
	// undelivered rows are events the client has not been given and that it
	// could ask for again by cursor. A parked row is neither: the hub pushes
	// it on release whatever the client's cursor says (PROTOCOL §5.2).
	undelivered = `(parked = 0 AND (done_at IS NULL OR terminal = 'exhausted'))`
)

// Upsert creates or refreshes the subscription keyed by (principal, url,
// name, arguments). A refresh keeps the cursor, rotates the secret (the old
// one signs too for SecretOverlap), clears paused_reason (except
// PausedOperator) and any backoff, and moves peer_id, types, the token hash
// and the expiries to s's. It keeps verification only for the unchanged secret, unless the row had
// lapsed or its pass is older than ChallengeMaxAge: then verified_at clears
// here, in one write with the new expiry, so the worker sends nothing until a
// new challenge passes. Either way expires is now+ttl, clamped to MaxTTL
// (ttl <= 0 means MaxTTL) and to tokenExp; a zero tokenExp is a credential
// that does not expire. s.Cursor applies only to a new row. ErrTooMany when a
// new or lapsed row would take the principal past MaxPerPrincipal. Refresh
// scheduling here changes only the worker deadline; SetRefreshBefore saves
// client advice after a successful subscription.
func (st *Store) Upsert(ctx context.Context, s Subscription, secret string, ttl time.Duration, tokenExp time.Time) (Subscription, bool, error) {
	args, err := canonicalArgs(s.Arguments)
	if err != nil {
		return Subscription{}, false, err
	}
	types := s.Types
	if len(types) == 0 {
		types = DefaultTypes
	}
	typesJSON, err := json.Marshal(types)
	if err != nil {
		return Subscription{}, false, fmt.Errorf("events: encode types: %w", err)
	}
	sealed, err := hub.SealWith(st.Key, []byte(secret))
	if err != nil {
		return Subscription{}, false, err
	}
	now := st.now()
	if ttl <= 0 || ttl > MaxTTL {
		ttl = MaxTTL
	}
	expires := now.Add(ttl)
	var tokenExpires any
	if !tokenExp.IsZero() {
		tokenExpires = envelope.FormatTime(tokenExp)
		if tokenExp.Before(expires) {
			expires = tokenExp
		}
	}
	var tokenHash any
	if s.TokenHash != "" {
		tokenHash = s.TokenHash
	}
	nowS, expiresS := envelope.FormatTime(now), envelope.FormatTime(expires)

	var id string
	var created bool
	err = st.DB.Tx(ctx, func(tx *sql.Tx) error {
		id, created = "", false
		interval := RefreshInterval
		if st.hub != nil {
			snapshot, err := st.hub.Preferences.LoadSnapshot(ctx, tx)
			if err != nil {
				return err
			}
			e, err := snapshot.Get("events.refresh_interval")
			if err != nil {
				return err
			}
			n, _ := e.Value.Int64()
			interval = time.Duration(n) * time.Second
		}
		var oldSealed string
		var lapsed bool
		err := tx.QueryRowContext(ctx,
			`SELECT sub_id, secret_enc, coalesce(expires_at <= ?, 0) FROM event_subscriptions
			 WHERE principal_kind = ? AND principal_id = ? AND url = ? AND name = ? AND arguments = ?`,
			nowS, s.PrincipalKind, s.PrincipalID, s.URL, s.Name, args).Scan(&id, &oldSealed, &lapsed)
		missing := errors.Is(err, sql.ErrNoRows)
		if err != nil && !missing {
			return fmt.Errorf("events: find subscription: %w", err)
		}
		if missing || lapsed {
			var live int
			if err := tx.QueryRowContext(ctx,
				`SELECT count(*) FROM event_subscriptions
				 WHERE principal_kind = ? AND principal_id = ? AND (expires_at IS NULL OR expires_at > ?)`,
				s.PrincipalKind, s.PrincipalID, nowS).Scan(&live); err != nil {
				return fmt.Errorf("events: count subscriptions: %w", err)
			}
			if live >= MaxPerPrincipal {
				return ErrTooMany
			}
		}
		if missing {
			id, created = ids.New(subPrefix), true
			_, err = tx.ExecContext(ctx,
				`INSERT INTO event_subscriptions(sub_id, principal_kind, principal_id, peer_id, name, arguments, url,
				                                 secret_enc, cursor_epoch, cursor_seq, types, expires_at,
				                                 token_expires_at, token_hash, created_at, updated_at)
				 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				id, s.PrincipalKind, s.PrincipalID, s.PeerID, s.Name, args, s.URL,
				sealed, s.Cursor.Epoch, s.Cursor.Seq, string(typesJSON), expiresS, tokenExpires, tokenHash, nowS, nowS)
			if err != nil {
				return fmt.Errorf("events: create subscription: %w", err)
			}
			return saveRefreshSchedule(ctx, tx, id, interval, RefreshDeadline(now, expires, interval))
		}
		old, err := hub.UnsealWith(st.Key, oldSealed)
		if err != nil {
			return fmt.Errorf("events: open old secret: %w", err)
		}
		if string(old) != secret {
			if _, err := tx.ExecContext(ctx,
				`UPDATE event_subscriptions SET prev_secret_enc = ?, prev_secret_until = ? WHERE sub_id = ?`,
				oldSealed, envelope.FormatTime(now.Add(SecretOverlap)), id); err != nil {
				return fmt.Errorf("events: rotate secret: %w", err)
			}
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE event_subscriptions SET peer_id = ?1, secret_enc = ?2, types = ?3, expires_at = ?4, token_expires_at = ?5,
			                                token_hash = ?6, last_refresh_at = ?7,
			                                paused_reason = CASE WHEN paused_reason = ?8 THEN paused_reason END,
			                                verified_at = CASE WHEN expires_at <= ?7 OR verified_at <= ?9 OR ?11 THEN NULL ELSE verified_at END,
			                                attempts = 0, next_attempt_at = NULL, updated_at = ?7
			 WHERE sub_id = ?10`,
			s.PeerID, sealed, string(typesJSON), expiresS, tokenExpires, tokenHash, nowS, PausedOperator,
			envelope.FormatTime(now.Add(-ChallengeMaxAge)), id, string(old) != secret); err != nil {
			return fmt.Errorf("events: refresh subscription: %w", err)
		}
		if err := clearBackoff(ctx, tx, id); err != nil {
			return err
		}
		return saveRefreshSchedule(ctx, tx, id, interval, RefreshDeadline(now, expires, interval))
	})
	if err != nil {
		return Subscription{}, false, err
	}
	got, err := st.Get(ctx, id)
	return got, created, err
}

// clearBackoff makes every unsettled outbox row of sub due now.
func clearBackoff(ctx context.Context, tx *sql.Tx, sub string) error {
	if _, err := tx.ExecContext(ctx,
		`UPDATE event_outbox SET next_attempt_at = NULL WHERE sub_id = ? AND done_at IS NULL`, sub); err != nil {
		return fmt.Errorf("events: clear backoff: %w", err)
	}
	return nil
}

// Delete removes the subscription with this key and its outbox; no such
// subscription is success.
func (st *Store) Delete(ctx context.Context, principalKind, principalID, url, name string, args map[string]any) error {
	canon, err := canonicalArgs(args)
	if err != nil {
		return err
	}
	return st.DB.Tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM meta WHERE key IN (
			 SELECT 'events_refresh_schedule:' || sub_id FROM event_subscriptions
			 WHERE principal_kind = ? AND principal_id = ? AND url = ? AND name = ? AND arguments = ?)`,
			principalKind, principalID, url, name, canon); err != nil {
			return fmt.Errorf("events: delete refresh schedule: %w", err)
		}
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM event_outbox WHERE sub_id IN (
			   SELECT sub_id FROM event_subscriptions
			   WHERE principal_kind = ? AND principal_id = ? AND url = ? AND name = ? AND arguments = ?)`,
			principalKind, principalID, url, name, canon); err != nil {
			return fmt.Errorf("events: delete outbox: %w", err)
		}
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM event_subscriptions
			 WHERE principal_kind = ? AND principal_id = ? AND url = ? AND name = ? AND arguments = ?`,
			principalKind, principalID, url, name, canon); err != nil {
			return fmt.Errorf("events: delete subscription: %w", err)
		}
		return nil
	})
}

const subColumns = `sub_id, principal_kind, principal_id, peer_id, name, arguments, url, cursor_epoch, cursor_seq,
	truncated, types, expires_at, verified_at, paused_reason, token_expires_at, token_hash, refresh_before_at,
	(SELECT value FROM meta WHERE key = 'events_refresh_schedule:' || event_subscriptions.sub_id)`

func scanSub(row interface{ Scan(...any) error }) (Subscription, error) {
	var s Subscription
	var args, types string
	var expires, verified, paused, token, tokenHash, refresh, schedule *string
	if err := row.Scan(&s.ID, &s.PrincipalKind, &s.PrincipalID, &s.PeerID, &s.Name, &args, &s.URL,
		&s.Cursor.Epoch, &s.Cursor.Seq, &s.Truncated, &types, &expires, &verified, &paused, &token, &tokenHash, &refresh, &schedule); err != nil {
		return Subscription{}, err
	}
	if err := json.Unmarshal([]byte(args), &s.Arguments); err != nil {
		return Subscription{}, fmt.Errorf("events: stored arguments: %w", err)
	}
	if err := json.Unmarshal([]byte(types), &s.Types); err != nil {
		return Subscription{}, fmt.Errorf("events: stored types: %w", err)
	}
	var err error
	if s.Expires, err = parseOptTime(expires); err != nil {
		return Subscription{}, err
	}
	if s.RefreshBefore, err = parseOptTime(refresh); err != nil {
		return Subscription{}, err
	}
	if schedule != nil {
		var saved refreshSchedule
		if err := json.Unmarshal([]byte(*schedule), &saved); err != nil {
			return Subscription{}, fmt.Errorf("events: stored refresh schedule: %w", err)
		}
		s.NextRefreshBefore = &saved.Next
	}
	if s.TokenExpires, err = parseOptTime(token); err != nil {
		return Subscription{}, err
	}
	if s.VerifiedAt, err = parseOptTime(verified); err != nil {
		return Subscription{}, err
	}
	s.Verified = verified != nil
	if paused != nil {
		s.PausedReason = *paused
	}
	if tokenHash != nil {
		s.TokenHash = *tokenHash
	}
	return s, nil
}

func parseOptTime(s *string) (*time.Time, error) {
	if s == nil {
		return nil, nil
	}
	t, err := envelope.ParseTime(*s)
	if err != nil {
		return nil, fmt.Errorf("events: stored time: %w", err)
	}
	return &t, nil
}

// Find returns the caller-scoped subscription matching the canonical key.
func (st *Store) Find(ctx context.Context, kind, principal, callback, name string, args map[string]any) (Subscription, error) {
	canon, err := canonicalArgs(args)
	if err != nil {
		return Subscription{}, err
	}
	sub, err := scanSub(st.DB.QueryRowContext(ctx, `SELECT `+subColumns+` FROM event_subscriptions WHERE principal_kind = ? AND principal_id = ? AND url = ? AND name = ? AND arguments = ?`, kind, principal, callback, name, canon))
	if errors.Is(err, sql.ErrNoRows) {
		return Subscription{}, ErrNotFound
	}
	return sub, err
}

// Get returns one subscription, or ErrNotFound.
func (st *Store) Get(ctx context.Context, id string) (Subscription, error) {
	s, err := scanSub(st.DB.QueryRowContext(ctx, `SELECT `+subColumns+` FROM event_subscriptions WHERE sub_id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Subscription{}, ErrNotFound
	}
	if err != nil {
		return Subscription{}, fmt.Errorf("events: get subscription: %w", err)
	}
	return s, nil
}

func (st *Store) list(ctx context.Context, where string, args ...any) ([]Subscription, error) {
	rows, err := st.DB.QueryContext(ctx, `SELECT `+subColumns+` FROM event_subscriptions `+where+` ORDER BY sub_id`, args...)
	if err != nil {
		return nil, fmt.Errorf("events: list subscriptions: %w", err)
	}
	defer rows.Close()
	out := []Subscription{}
	for rows.Next() {
		s, err := scanSub(rows)
		if err != nil {
			return nil, fmt.Errorf("events: list subscriptions: %w", err)
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("events: list subscriptions: %w", err)
	}
	return out, nil
}

// ListForPrincipal returns one principal's subscriptions.
func (st *Store) ListForPrincipal(ctx context.Context, principalKind, principalID string) ([]Subscription, error) {
	return st.list(ctx, `WHERE principal_kind = ? AND principal_id = ?`, principalKind, principalID)
}

// Active returns the subscriptions unexpired at now, paused or not.
func (st *Store) Active(ctx context.Context, now time.Time) ([]Subscription, error) {
	return st.list(ctx, `WHERE expires_at IS NULL OR expires_at > ?`, envelope.FormatTime(now))
}

// MarkVerified records a passed challenge.
func (st *Store) MarkVerified(ctx context.Context, id string) error {
	return st.MarkVerifiedAt(ctx, id, st.now())
}

// MarkVerifiedAt retains the original verification time when reusing a cached pass.
func (st *Store) MarkVerifiedAt(ctx context.Context, id string, verified time.Time) error {
	now := envelope.FormatTime(st.now())
	if _, err := st.DB.ExecContext(ctx,
		`UPDATE event_subscriptions SET verified_at = ?, updated_at = ? WHERE sub_id = ?`, envelope.FormatTime(verified), now, id); err != nil {
		return fmt.Errorf("events: mark verified: %w", err)
	}
	return nil
}

// SetCursor moves a subscription to cur, which the caller vouches is retained,
// and clears the truncated flag, now reported. The worker reads everything
// after cur again, so outbox rows past it go, keeping order, except the ones
// the callback refused for good: those stay settled, so reading them again
// changes nothing and a rewind never retries a 410 or 413 (PROTOCOL §4.4).
//
// clientHas says the client named cur as the position it holds, which wins
// (ChatGPT passes its last saved cursor): undelivered rows at or before it go
// too. Hub-reported cursors never pass an undelivered row (ReplayCursor), so
// such a row is one the client got and the hub did not hear about. Without
// clientHas, as when a purged cursor is replaced, those rows stay queued.
func (st *Store) SetCursor(ctx context.Context, id string, cur eventlog.Cursor, clientHas bool) error {
	return st.DB.Tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM event_outbox WHERE sub_id = ?1 AND (
			   (seq > ?2 AND NOT `+neverRetried+`) OR (?3 AND seq <= ?2 AND `+undelivered+`))`,
			id, cur.Seq, clientHas); err != nil {
			return fmt.Errorf("events: rewind outbox: %w", err)
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE event_subscriptions SET cursor_epoch = ?, cursor_seq = ?, truncated = 0, updated_at = ? WHERE sub_id = ?`,
			cur.Epoch, cur.Seq, envelope.FormatTime(st.now()), id); err != nil {
			return fmt.Errorf("events: set cursor: %w", err)
		}
		return nil
	})
}

// ReplayCursor is the position to report to a client when the subscription
// stands at cur: cur, or just before the oldest undelivered row if that comes
// first. The tail cursor alone would pass rows that are queued or given up on,
// and a client that later passed it back would be saying it holds them.
func (st *Store) ReplayCursor(ctx context.Context, id string, cur eventlog.Cursor) (eventlog.Cursor, error) {
	var oldest *int64
	if err := st.DB.QueryRowContext(ctx,
		`SELECT min(seq) FROM event_outbox WHERE sub_id = ? AND `+undelivered, id).Scan(&oldest); err != nil {
		return eventlog.Cursor{}, fmt.Errorf("events: replay cursor: %w", err)
	}
	if oldest != nil && *oldest-1 < cur.Seq {
		cur.Seq = *oldest - 1
	}
	return cur, nil
}

// ClearTruncated marks the truncated flag reported to the client.
func (st *Store) ClearTruncated(ctx context.Context, id string) error {
	if _, err := st.DB.ExecContext(ctx, `UPDATE event_subscriptions SET truncated = 0 WHERE sub_id = ?`, id); err != nil {
		return fmt.Errorf("events: clear truncated: %w", err)
	}
	return nil
}

// Pause is the operator's stop for one subscription: nothing more is sent,
// the cursor stays, and only Resume lifts it. ErrNotFound when id has no row.
func (st *Store) Pause(ctx context.Context, id string) error {
	var notify []string
	changed := false
	err := st.DB.Tx(ctx, func(tx *sql.Tx) error {
		changed = false
		var paused sql.NullString
		if err := tx.QueryRowContext(ctx, `SELECT paused_reason FROM event_subscriptions WHERE sub_id = ?`, id).Scan(&paused); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}

		res, err := tx.ExecContext(ctx,
			`UPDATE event_subscriptions SET paused_reason = ?, updated_at = ? WHERE sub_id = ?`,
			PausedOperator, envelope.FormatTime(st.now()), id)
		if err != nil {
			return fmt.Errorf("events: operator pause: %w", err)
		}
		if n, err := res.RowsAffected(); err != nil {
			return fmt.Errorf("events: operator pause: %w", err)
		} else if n == 0 {
			return ErrNotFound
		}
		if err := audit.Record(ctx, tx, "operator", "subscription_pause", []string{id}, nil); err != nil {
			return err
		}
		changed = paused.String != PausedOperator
		if st.hub != nil && changed {
			notify, err = st.hub.NoteOperatorSubscription(ctx, tx)
			return err
		}
		return nil
	})
	if err == nil && len(notify) > 0 {
		st.hub.Log.Notify(notify...)
	}
	if err == nil && changed {
		if sub, getErr := st.Get(ctx, id); getErr == nil {
			LogLifecycle("events.pause", sub, map[string]any{"reason": PausedOperator, "result": "paused"})
		}
	}
	return err
}

// Resume clears paused_reason and any backoff, the operator's nudge;
// ErrNotFound when id has no row.
func (st *Store) Resume(ctx context.Context, id string) error {
	var notify []string
	err := st.DB.Tx(ctx, func(tx *sql.Tx) error {
		var paused sql.NullString
		var attempts int
		var next sql.NullString
		if err := tx.QueryRowContext(ctx, `SELECT paused_reason, attempts, next_attempt_at FROM event_subscriptions WHERE sub_id = ?`, id).Scan(&paused, &attempts, &next); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}

		var pending bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM event_outbox WHERE sub_id = ? AND done_at IS NULL AND next_attempt_at IS NOT NULL)`, id).Scan(&pending); err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx,
			`UPDATE event_subscriptions SET paused_reason = NULL, attempts = 0, next_attempt_at = NULL, updated_at = ?
			 WHERE sub_id = ?`, envelope.FormatTime(st.now()), id)
		if err != nil {
			return fmt.Errorf("events: resume: %w", err)
		}
		if n, err := res.RowsAffected(); err != nil {
			return fmt.Errorf("events: resume: %w", err)
		} else if n == 0 {
			return ErrNotFound
		}
		if err := clearBackoff(ctx, tx, id); err != nil {
			return err
		}
		if st.hub != nil && (paused.Valid || attempts != 0 || next.Valid || pending) {
			notify, err = st.hub.NoteOperatorSubscription(ctx, tx)
			return err
		}
		return nil
	})
	if err == nil && len(notify) > 0 {
		st.hub.Log.Notify(notify...)
	}
	return err
}

// Health is a subscription's row as the operator sees it. A callback path can
// carry a credential: whoever displays URL shows its host only.
type Health struct {
	ID, PrincipalKind, PrincipalID, PeerID, URL, Name  string
	Cursor                                             eventlog.Cursor
	ExpiresAt, VerifiedAt, PausedReason, RefreshBefore *string
	LastRefreshAt, LastDeliveryAt                      *string
	LastStatus                                         *int
	Attempts                                           int
}

// ListHealth returns every subscription for the operator.
func (st *Store) ListHealth(ctx context.Context) ([]Health, error) {
	rows, err := st.DB.QueryContext(ctx,
		`SELECT sub_id, principal_kind, principal_id, peer_id, url, name, cursor_epoch, cursor_seq,
		        expires_at, verified_at, paused_reason, last_refresh_at, last_delivery_at, last_status, attempts, refresh_before_at
		 FROM event_subscriptions ORDER BY sub_id`)
	if err != nil {
		return nil, fmt.Errorf("events: list health: %w", err)
	}
	defer rows.Close()
	out := []Health{}
	for rows.Next() {
		var h Health
		if err := rows.Scan(&h.ID, &h.PrincipalKind, &h.PrincipalID, &h.PeerID, &h.URL, &h.Name,
			&h.Cursor.Epoch, &h.Cursor.Seq, &h.ExpiresAt, &h.VerifiedAt, &h.PausedReason,
			&h.LastRefreshAt, &h.LastDeliveryAt, &h.LastStatus, &h.Attempts, &h.RefreshBefore); err != nil {
			return nil, fmt.Errorf("events: list health: %w", err)
		}
		out = append(out, h)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("events: list health: %w", err)
	}
	return out, nil
}

// secrets returns the keys to sign with at now: the current one, then the
// previous one inside its overlap.
func (st *Store) secrets(ctx context.Context, id string, now time.Time) ([]string, error) {
	var cur string
	var prev, until *string
	err := st.DB.QueryRowContext(ctx,
		`SELECT secret_enc, prev_secret_enc, prev_secret_until FROM event_subscriptions WHERE sub_id = ?`, id).
		Scan(&cur, &prev, &until)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("events: secrets: %w", err)
	}
	plain, err := hub.UnsealWith(st.Key, cur)
	if err != nil {
		return nil, fmt.Errorf("events: open secret: %w", err)
	}
	out := []string{string(plain)}
	if prev != nil && until != nil && *until > envelope.FormatTime(now) {
		old, err := hub.UnsealWith(st.Key, *prev)
		if err != nil {
			return nil, fmt.Errorf("events: open previous secret: %w", err)
		}
		out = append(out, string(old))
	}
	return out, nil
}

// pauseExpiredTokens sets paused_reason on every unpaused subscription whose
// token has expired. Nothing is dropped and the cursor stays.
func (st *Store) pauseExpiredTokens(ctx context.Context, now time.Time) error {
	nowS := envelope.FormatTime(now)
	var changed []Subscription
	err := st.DB.Tx(ctx, func(tx *sql.Tx) error {
		changed = nil
		rows, err := tx.QueryContext(ctx, `SELECT `+subColumns+` FROM event_subscriptions WHERE paused_reason IS NULL AND token_expires_at IS NOT NULL AND token_expires_at <= ?`, nowS)
		if err != nil {
			return err
		}
		for rows.Next() {
			sub, err := scanSub(rows)
			if err != nil {
				rows.Close()
				return err
			}
			changed = append(changed, sub)
		}
		scanErr := rows.Err()
		rows.Close()
		if scanErr != nil {
			return scanErr
		}
		res, err := tx.ExecContext(ctx,
			`UPDATE event_subscriptions SET paused_reason = ?, updated_at = ?
			 WHERE paused_reason IS NULL AND token_expires_at IS NOT NULL AND token_expires_at <= ?`,
			PausedTokenExpired, nowS, nowS)
		if err != nil {
			return fmt.Errorf("events: pause expired tokens: %w", err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("events: pause expired tokens: %w", err)
		}
		return hub.BumpCounter(ctx, tx, hub.CounterEventsPaused, n)
	})
	if err == nil {
		for _, sub := range changed {
			LogLifecycle("events.pause", sub, map[string]any{"reason": PausedTokenExpired, "result": "paused"})
		}
	}
	return err
}

func (st *Store) pause(ctx context.Context, id, reason string, now time.Time) error {
	changed := false
	err := st.DB.Tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			`UPDATE event_subscriptions SET paused_reason = ?, updated_at = ? WHERE sub_id = ? AND paused_reason IS NULL`,
			reason, envelope.FormatTime(now), id)
		if err != nil {
			return fmt.Errorf("events: pause: %w", err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("events: pause: %w", err)
		}
		changed = n == 1
		if n == 1 && (reason == PausedDeliveryFailed || reason == PausedTokenExpired || reason == PausedUnauthorized) {
			return hub.BumpCounter(ctx, tx, hub.CounterEventsPaused, 1)
		}
		return nil
	})
	if err == nil && changed {
		if sub, getErr := st.Get(ctx, id); getErr == nil {
			LogLifecycle("events.pause", sub, map[string]any{"reason": reason, "result": "paused"})
		}
	}
	return err
}

// outboxRow is one unsettled event.
type outboxRow struct {
	eventID  string
	seq      int64
	body     string
	attempts int
	next     *string
	parked   bool // read while its delivery was held; waits for the release
}

// enqueue inserts rows and advances the cursor from `from` to `to` in one
// transaction. A cursor no longer at `from` means a refresh moved it during
// the read: the batch is stale, and nothing is written.
func (st *Store) enqueue(ctx context.Context, id string, from, to eventlog.Cursor, rows []outboxRow, now time.Time) error {
	nowS := envelope.FormatTime(now)
	return st.DB.Tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			`UPDATE event_subscriptions SET cursor_epoch = ?, cursor_seq = ?, updated_at = ?
			 WHERE sub_id = ? AND cursor_epoch = ? AND cursor_seq = ?`,
			to.Epoch, to.Seq, nowS, id, from.Epoch, from.Seq)
		if err != nil {
			return fmt.Errorf("events: advance cursor: %w", err)
		}
		if n, err := res.RowsAffected(); err != nil {
			return fmt.Errorf("events: advance cursor: %w", err)
		} else if n == 0 {
			return nil
		}
		for _, r := range rows {
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO event_outbox(sub_id, event_id, seq, body, parked, created_at) VALUES (?, ?, ?, ?, ?, ?)
				 ON CONFLICT(sub_id, event_id) DO NOTHING`,
				id, r.eventID, r.seq, r.body, r.parked, nowS); err != nil {
				return fmt.Errorf("events: enqueue: %w", err)
			}
		}
		return nil
	})
}

// skipPurged moves a subscription whose cursor fell below retention to
// oldest and flags the loss for its next events/subscribe, if it is still
// at `from`.
func (st *Store) skipPurged(ctx context.Context, id string, from, oldest eventlog.Cursor, now time.Time) error {
	if _, err := st.DB.ExecContext(ctx,
		`UPDATE event_subscriptions SET cursor_epoch = ?, cursor_seq = ?, truncated = 1, updated_at = ?
		 WHERE sub_id = ? AND cursor_epoch = ? AND cursor_seq = ?`,
		oldest.Epoch, oldest.Seq, envelope.FormatTime(now), id, from.Epoch, from.Seq); err != nil {
		return fmt.Errorf("events: skip purged: %w", err)
	}
	return nil
}

// pending returns up to limit unsettled rows of sub, oldest first. A row whose
// delivery is still held is not due and takes no place in the batch, so held
// rows cannot starve the rest however many there are.
func (st *Store) pending(ctx context.Context, id string, limit int) ([]outboxRow, error) {
	rows, err := st.DB.QueryContext(ctx,
		`SELECT event_id, seq, body, attempts, next_attempt_at, parked FROM event_outbox o
		 WHERE sub_id = ? AND done_at IS NULL
		   AND NOT (parked = 1 AND EXISTS (SELECT 1 FROM deliveries d WHERE d.dly_id = o.event_id AND d.held = 1))
		 ORDER BY seq LIMIT ?`, id, limit)
	if err != nil {
		return nil, fmt.Errorf("events: pending: %w", err)
	}
	defer rows.Close()
	var out []outboxRow
	for rows.Next() {
		var r outboxRow
		if err := rows.Scan(&r.eventID, &r.seq, &r.body, &r.attempts, &r.next, &r.parked); err != nil {
			return nil, fmt.Errorf("events: pending: %w", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("events: pending: %w", err)
	}
	return out, nil
}

// settle closes a row (terminal "" for a delivery) and records the response.
// status 0 is a network failure and leaves last_status NULL. A settled row's
// body is never sent again, so it is cleared: a replay reads the log.
func (st *Store) settle(ctx context.Context, id, eventID, terminal string, status int, now time.Time) error {
	nowS := envelope.FormatTime(now)
	var term any
	if terminal != "" {
		term = terminal
	}
	return st.DB.Tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx,
			`UPDATE event_outbox SET done_at = ?, terminal = ?, next_attempt_at = NULL, body = '' WHERE sub_id = ? AND event_id = ?`,
			nowS, term, id, eventID); err != nil {
			return fmt.Errorf("events: settle: %w", err)
		}
		if terminal == "exhausted" {
			if err := hub.BumpCounter(ctx, tx, hub.CounterEventsPushFailed, 1); err != nil {
				return err
			}
		}
		return recordResult(ctx, tx, id, status, 0, nil, nowS)
	})
}

// withdraw closes a row the delivery gate no longer passes. No POST was made,
// so the last response stands; the operator's attempt count moves on.
func (st *Store) withdraw(ctx context.Context, id, eventID string, now time.Time) error {
	nowS := envelope.FormatTime(now)
	return st.DB.Tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx,
			`UPDATE event_outbox SET done_at = ?, terminal = ?, next_attempt_at = NULL, body = '' WHERE sub_id = ? AND event_id = ?`,
			nowS, terminalGated, id, eventID); err != nil {
			return fmt.Errorf("events: withdraw: %w", err)
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE event_subscriptions SET attempts = 0, next_attempt_at = NULL, updated_at = ? WHERE sub_id = ?`,
			nowS, id); err != nil {
			return fmt.Errorf("events: withdraw: %w", err)
		}
		return nil
	})
}

// retryLater records a failed attempt of a row.
func (st *Store) retryLater(ctx context.Context, id, eventID string, attempts, status int, next, now time.Time) error {
	nextS := envelope.FormatTime(next)
	return st.DB.Tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx,
			`UPDATE event_outbox SET attempts = ?, next_attempt_at = ? WHERE sub_id = ? AND event_id = ?`,
			attempts, nextS, id, eventID); err != nil {
			return fmt.Errorf("events: retry: %w", err)
		}
		if err := hub.BumpCounter(ctx, tx, hub.CounterEventsPushFailed, 1); err != nil {
			return err
		}
		return recordResult(ctx, tx, id, status, attempts, nextS, envelope.FormatTime(now))
	})
}

func recordResult(ctx context.Context, tx *sql.Tx, id string, status, attempts int, next any, now string) error {
	var last any
	if status != 0 {
		last = status
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE event_subscriptions SET last_delivery_at = ?, last_status = ?, attempts = ?, next_attempt_at = ?, updated_at = ?
		 WHERE sub_id = ?`, now, last, attempts, next, now, id); err != nil {
		return fmt.Errorf("events: record result: %w", err)
	}
	return nil
}

// pruneDone forgets rows settled before cutoff. A row refused for good lasts
// until the log has purged its event: a rewind can still read it again until
// then, however old it is, and only this row stops it being retried.
func (st *Store) pruneDone(ctx context.Context, cutoff time.Time, oldest eventlog.Cursor) error {
	if _, err := st.DB.ExecContext(ctx,
		`DELETE FROM event_outbox WHERE done_at IS NOT NULL AND done_at < ? AND (NOT `+neverRetried+` OR seq <= ?)`,
		envelope.FormatTime(cutoff), oldest.Seq); err != nil {
		return fmt.Errorf("events: prune outbox: %w", err)
	}
	return nil
}

// peerAuthorised reports whether the subscription's peer exists, is enabled
// and, for a vecnet bearer's subscription, still belongs to its principal: a
// peer credential's own peer, or a peer of the principal credential. For an
// OAuth subscription, the access token that made it must also stand unrevoked:
// reuse detection revokes a token's whole family, and a row whose token has
// gone is refused the same way.
func (st *Store) peerAuthorised(ctx context.Context, s Subscription) (bool, error) {
	var prn string
	var disabled bool
	err := st.DB.QueryRowContext(ctx, `SELECT prn_id, disabled FROM peers WHERE peer_id = ?`, s.PeerID).Scan(&prn, &disabled)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("events: peer: %w", err)
	}
	if disabled {
		return false, nil
	}
	if s.TokenHash != "" {
		var revoked bool
		err := st.DB.QueryRowContext(ctx,
			`SELECT revoked_at IS NOT NULL FROM oauth_tokens WHERE token_hash = ? AND kind = 'access'`, s.TokenHash).Scan(&revoked)
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		if err != nil {
			return false, fmt.Errorf("events: subscribing token: %w", err)
		}
		if revoked {
			return false, nil
		}
	}
	return s.PrincipalKind != KindVecnet || s.PrincipalID == s.PeerID || s.PrincipalID == prn, nil
}

// RefreshDeadline asks clients to refresh periodically and before lease expiry.
// A short lease must never hand back a time already in the past.
func RefreshDeadline(now, expires time.Time, interval time.Duration) time.Time {
	if interval <= 0 {
		interval = RefreshInterval
	}
	deadline := expires.Add(-RefreshMargin)
	if periodic := now.Add(interval); periodic.Before(deadline) {
		deadline = periodic
	}
	if deadline.Before(now) {
		deadline = now
	}
	return deadline
}

// SetRefreshBefore saves the exact advice returned by a successful subscribe.
// The worker schedule independently uses the captured preference and lease.
// Subscribe callers can use RefreshDeadline to calculate the advice they return.
func (st *Store) SetRefreshBefore(ctx context.Context, id string, deadline time.Time) error {
	return st.DB.Tx(ctx, func(tx *sql.Tx) error {
		interval := RefreshInterval
		if st.hub != nil {
			snapshot, err := st.hub.Preferences.LoadSnapshot(ctx, tx)
			if err != nil {
				return err
			}
			e, err := snapshot.Get("events.refresh_interval")
			if err != nil {
				return err
			}
			n, _ := e.Value.Int64()
			interval = time.Duration(n) * time.Second
		}
		result, err := tx.ExecContext(ctx, `UPDATE event_subscriptions SET refresh_before_at = ? WHERE sub_id = ?`, envelope.FormatTime(deadline), id)
		if err != nil {
			return fmt.Errorf("events: save refresh deadline: %w", err)
		}
		if count, err := result.RowsAffected(); err != nil || count == 0 {
			return err // preserve the legacy no-op for a missing row
		}
		var raw string
		if err := tx.QueryRowContext(ctx, `SELECT expires_at FROM event_subscriptions WHERE sub_id = ?`, id).Scan(&raw); err != nil {
			return err
		}
		expires, err := envelope.ParseTime(raw)
		if err != nil {
			return err
		}
		return saveRefreshSchedule(ctx, tx, id, interval, RefreshDeadline(st.now(), expires, interval))
	})
}

// refreshSchedule is consumer state in the existing meta table, never a second
// preference store. Policy is derived only from LoadSnapshot. Each subscribe
// transaction records the policy it actually used, so A -> B -> A coalescing
// cannot hide rows materialized under B between worker passes.
type refreshSchedule struct {
	Policy string    `json:"policy"`
	Next   time.Time `json:"next"`
}

const refreshSchedulePrefix = "events_refresh_schedule:"

func refreshPolicy(interval time.Duration) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(fmt.Sprintf("refresh-v1:%d", int64(interval/time.Second)))))
}

func saveRefreshSchedule(ctx context.Context, tx *sql.Tx, id string, interval time.Duration, next time.Time) error {
	encoded, err := json.Marshal(refreshSchedule{Policy: refreshPolicy(interval), Next: next})
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO meta(key, value) VALUES (?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, refreshSchedulePrefix+id, string(encoded))
	return err
}

// refreshSchedules consumes one committed snapshot. A burst collapses to the
// newest cadence; unrelated preference writes do not slide deadlines. Wakes
// update only worker schedules: PROTOCOL section 4.4 preserves client-returned
// advice for status reads until re-subscription. No callback, lease renewal,
// cursor move or backoff reset occurs here.
func (st *Store) refreshSchedules(ctx context.Context) error {
	if st.hub == nil {
		return nil
	}
	st.refreshMu.Lock()
	defer st.refreshMu.Unlock()
	return st.DB.Tx(ctx, func(tx *sql.Tx) error {
		snapshot, err := st.hub.Preferences.LoadSnapshot(ctx, tx)
		if err != nil {
			return err
		}
		e, err := snapshot.Get("events.refresh_interval")
		if err != nil {
			return err
		}
		n, _ := e.Value.Int64()
		interval := time.Duration(n) * time.Second
		// Keep the earlier global checkpoint, but never use it to skip rows:
		// subscribe paths can apply an intermediate cadence between passes.
		// Per-subscription policy and deadline commit atomically below.
		const checkpointKey = "events_refresh_schedule_policy"
		fingerprint := refreshPolicy(interval)
		var applied string
		err = tx.QueryRowContext(ctx, `SELECT value FROM meta WHERE key = ?`, checkpointKey).Scan(&applied)
		missing := errors.Is(err, sql.ErrNoRows)
		if err != nil && !missing {
			return err
		}
		now := st.now()
		rows, err := tx.QueryContext(ctx, `SELECT sub_id, expires_at, coalesce(last_refresh_at, created_at), refresh_before_at,
			(SELECT value FROM meta WHERE key = 'events_refresh_schedule:' || event_subscriptions.sub_id)
			FROM event_subscriptions WHERE expires_at > ? ORDER BY sub_id`, envelope.FormatTime(now))
		if err != nil {
			return err
		}
		type schedule struct {
			id string
			at time.Time
		}
		var pending []schedule
		for rows.Next() {
			var id, raw, anchorRaw string
			var previous, state *string
			if err := rows.Scan(&id, &raw, &anchorRaw, &previous, &state); err != nil {
				rows.Close()
				return err
			}
			expires, err := envelope.ParseTime(raw)
			if err != nil {
				rows.Close()
				return err
			}
			if state != nil {
				var saved refreshSchedule
				if err := json.Unmarshal([]byte(*state), &saved); err != nil {
					rows.Close()
					return fmt.Errorf("events: stored refresh schedule: %w", err)
				}
				if saved.Policy == fingerprint {
					continue
				}
			}
			// Spread the next worker deadlines within 30 seconds (or 10% of
			// the cadence), always capped by the unchanged lease margin.
			hash := fnv.New64a()
			_, _ = hash.Write([]byte(id))
			spread := min(30*time.Second, interval/10)
			jitter := time.Duration(hash.Sum64() % uint64(spread))
			if state == nil && previous != nil {
				// Bootstrap per-row state without sliding matching legacy advice.
				// Later passes use the saved row policy, including after Reset.
				anchor, err := envelope.ParseTime(anchorRaw)
				if err != nil {
					rows.Close()
					return err
				}
				if e.UpdatedAt != "" {
					updated, err := envelope.ParseTime(e.UpdatedAt)
					if err != nil {
						rows.Close()
						return err
					}
					if updated.After(anchor) {
						anchor = updated
					}
				}
				old, err := envelope.ParseTime(*previous)
				if err != nil {
					rows.Close()
					return err
				}
				first := RefreshDeadline(anchor, expires, interval)
				last := RefreshDeadline(anchor, expires, interval+spread)
				if !old.Before(first) && !old.After(last) {
					pending = append(pending, schedule{id, old})
					continue
				}
			}
			pending = append(pending, schedule{id, RefreshDeadline(now, expires, interval+jitter)})
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		if err := rows.Close(); err != nil {
			return err
		}
		for _, p := range pending {
			if err := saveRefreshSchedule(ctx, tx, p.id, interval, p.at); err != nil {
				return err
			}
		}
		if !missing && applied == fingerprint {
			return nil
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO meta(key, value) VALUES (?, ?)
			ON CONFLICT(key) DO UPDATE SET value = excluded.value`, checkpointKey, fingerprint)
		return err
	})
}
