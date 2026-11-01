package hub

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"net/http"
	"time"

	"github.com/XertroV/vecnet/internal/envelope"
)

// Rate windows of the per-peer limits (PROTOCOL §16.2).
const (
	sendWindow   = time.Minute
	threadWindow = time.Hour
)

// checkRate enforces peerID's per-peer limits at now (PROTOCOL §16.2): the
// messages it sent in the last 60 s against peers.sends_per_min, and, when
// the send would create a thread, the threads it created in the last hour
// against peers.threads_per_hour. Exceeding either is 429 rate_limited with
// details.retry_after_seconds (the HTTP layer adds Retry-After): the whole
// seconds until enough of the window's entries age out to admit one more.
// Windows use the hub clock (h.Now), never time.Now.
func (h *Hub) checkRate(ctx context.Context, tx *sql.Tx, peerID string, newThread bool, now time.Time) (*APIError, error) {
	var perMin, perHour int
	err := tx.QueryRowContext(ctx, `SELECT sends_per_min, threads_per_hour FROM peers WHERE peer_id = ?`, peerID).
		Scan(&perMin, &perHour)
	if err != nil {
		return nil, fmt.Errorf("hub: rate limits of %s: %w", peerID, err)
	}
	return rateVerdict(ctx, tx, "per-peer", peerID, perMin, perHour, newThread, now)
}

// rateVerdict runs the two windows of PROTOCOL §16.2 for actor, a peer or a
// source, against its limits: messages in the last 60 s, then, when the send
// would create a thread, threads created in the last hour. what names the
// limit in the error message.
func rateVerdict(ctx context.Context, tx *sql.Tx, what, actor string, perMin, perHour int, newThread bool, now time.Time) (*APIError, error) {
	retry, err := windowRetry(ctx, tx,
		`SELECT created_at FROM messages WHERE sender = ? AND created_at > ? ORDER BY created_at`,
		actor, perMin, sendWindow, now)
	if err != nil {
		return nil, err
	}
	limit, window := "sends_per_min", perMin
	if retry == 0 && newThread {
		retry, err = windowRetry(ctx, tx,
			`SELECT created_at FROM threads WHERE created_by = ? AND created_at > ? ORDER BY created_at`,
			actor, perHour, threadWindow, now)
		if err != nil {
			return nil, err
		}
		limit, window = "threads_per_hour", perHour
	}
	if retry == 0 {
		return nil, nil
	}
	e := E(http.StatusTooManyRequests, "rate_limited", what+" rate limit exceeded")
	e.Details["limit"] = limit
	e.Details["max"] = window
	e.Details["retry_after_seconds"] = retry
	return e, nil
}

// countRateCut records a rate-limit rejection of actor: a dashboard counter
// and an audit row (PROTOCOL §16.2).
func (h *Hub) countRateCut(ctx context.Context, tx *sql.Tx, actor string, apiErr *APIError) error {
	detail := map[string]any{"reason": apiErr.Details["limit"], "retry_after_seconds": apiErr.Details["retry_after_seconds"]}
	return h.countCut(ctx, tx, CounterRateLimited, actor, []string{actor}, detail)
}

// windowRetry counts the rows q returns for (peer, now-window) — their
// created_at ascending — and returns 0 when fewer than max, else the whole
// seconds (at least 1) until the row whose expiry brings the count under
// max leaves the window.
func windowRetry(ctx context.Context, tx *sql.Tx, q, peer string, max int, window time.Duration, now time.Time) (int, error) {
	if max < 0 {
		max = 0
	}
	var n int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM (`+q+`)`, peer, envelope.FormatTime(now.Add(-window))).Scan(&n); err != nil {
		return 0, fmt.Errorf("hub: rate window: %w", err)
	}
	if n < max {
		return 0, nil
	}
	var at string
	err := tx.QueryRowContext(ctx, q+` LIMIT 1 OFFSET ?`, peer, envelope.FormatTime(now.Add(-window)), n-max).Scan(&at)
	if errors.Is(err, sql.ErrNoRows) {
		return int(window / time.Second), nil // max is 0: nothing will admit a send sooner
	}
	if err != nil {
		return 0, fmt.Errorf("hub: rate window: %w", err)
	}
	t, err := envelope.ParseTime(at)
	if err != nil {
		return 0, fmt.Errorf("hub: rate window: %w", err)
	}
	secs := int(math.Ceil(t.Add(window).Sub(now).Seconds()))
	if secs < 1 {
		secs = 1
	}
	return secs, nil
}
