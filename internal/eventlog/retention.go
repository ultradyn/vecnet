package eventlog

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/XertroV/vecnet/internal/envelope"
)

// floorFor is shared by read snapshots and writer transactions. Missing owners
// have never been purged. A scalar merged cursor uses the most protective floor.
func floorFor(ctx context.Context, q execer, owners []string) (int64, error) {
	var floor int64
	for start := 0; start < len(owners); start += 200 {
		end := min(start+200, len(owners))
		args := make([]any, end-start)
		for i, o := range owners[start:end] {
			args[i] = o
		}
		var n int64
		if err := q.QueryRowContext(ctx, `SELECT coalesce(max(purged_through_seq),0) FROM event_retention WHERE owner IN (`+placeholders(len(args))+`)`, args...).Scan(&n); err != nil {
			return 0, err
		}
		floor = max(floor, n)
	}
	return floor, nil
}

// OldestFor returns the safe replay floor over the current owners. Events below
// a merged floor can still be read through an unpurged narrowed owner/history.
func (l *Log) OldestFor(ctx context.Context, owners []string) (Cursor, error) {
	return l.OldestForTx(ctx, nil, owners)
}
func (l *Log) OldestForTx(ctx context.Context, tx *sql.Tx, owners []string) (Cursor, error) {
	n, err := floorFor(ctx, l.via(tx), owners)
	return Cursor{Epoch: l.epoch, Seq: n}, err
}

// RetentionRule authorizes one event's minimum acknowledged age. The hub
// projects it from a fresh validated prefs snapshot and activation ledger in tx.
type RetentionRule func(seq int64, at time.Time) (time.Duration, bool)
type RetentionLimits struct {
	Rows   int
	Bytes  int64
	Target time.Duration
}
type RetentionStats struct {
	Scanned          int    `json:"scanned"`
	Deleted          int    `json:"deleted"`
	Bytes            int64  `json:"bytes"`
	Floor            int64  `json:"floor"`
	AckLag           int64  `json:"ack_lag"`
	OldestAgeSeconds int64  `json:"oldest_age_seconds"`
	Pinned           int    `json:"pinned"`
	Reason           string `json:"reason"`
}

// PrunePrefix deletes at most one bounded owner prefix. All acknowledgements,
// dependencies, event deletes and monotonic floor writes share the caller's
// writer transaction. No age index or sequence arithmetic infers missing rows.
func (l *Log) PrunePrefix(ctx context.Context, tx *sql.Tx, owner string, now time.Time, rule RetentionRule, hard time.Duration, hardAllowed func(int64, time.Time) bool, limits RetentionLimits) (RetentionStats, error) {
	var out RetentionStats
	if tx == nil || rule == nil {
		return out, errors.New("eventlog: retention_requires_transaction")
	}
	if limits.Rows <= 0 {
		limits.Rows = 100
	}
	if limits.Bytes <= 0 {
		limits.Bytes = 1 << 20
	}
	if limits.Target <= 0 {
		limits.Target = 50 * time.Millisecond
	}
	start := time.Now()
	floor, err := floorFor(ctx, tx, []string{owner})
	if err != nil {
		return out, err
	}
	out.Floor = floor
	ack, err := l.effectiveAck(ctx, tx, owner)
	if err != nil {
		return out, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT seq,at,length(CAST(data AS BLOB)) FROM events WHERE owner=? ORDER BY seq LIMIT ?`, owner, limits.Rows)
	if err != nil {
		return out, err
	}
	type candidate struct {
		seq   int64
		at    string
		bytes int64
	}
	var candidates []candidate
	for rows.Next() {
		var c candidate
		if err := rows.Scan(&c.seq, &c.at, &c.bytes); err != nil {
			rows.Close()
			return out, err
		}
		candidates = append(candidates, c)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	if len(candidates) > 0 {
		at, err := envelope.ParseTime(candidates[0].at)
		if err != nil {
			return out, errors.New("eventlog: retention_invalid_time")
		}
		out.OldestAgeSeconds = max(0, int64(now.Sub(at)/time.Second))
		out.AckLag = max(0, candidates[len(candidates)-1].seq-ack)
	}
	for _, c := range candidates {
		if ctx.Err() != nil {
			return out, ctx.Err()
		}
		if out.Scanned > 0 && (out.Bytes+c.bytes > limits.Bytes || time.Since(start) >= limits.Target) {
			out.Reason = "slice_limit"
			break
		}
		out.Scanned++
		at, err := envelope.ParseTime(c.at)
		if err != nil {
			return out, errors.New("eventlog: retention_invalid_time")
		}
		age, allowed := rule(c.seq, at)
		acknowledged := ack >= c.seq
		if !allowed || (acknowledged && (age == 0 || now.Sub(at) < age)) || (!acknowledged && (hard <= 0 || now.Sub(at) < hard || hardAllowed != nil && !hardAllowed(c.seq, at))) {
			out.Reason = "policy_or_age"
			if !acknowledged {
				out.Reason = "unacknowledged"
			}
			break
		}
		pinned, err := l.retentionPinned(ctx, tx, owner, c.seq, now)
		if err != nil {
			return out, err
		}
		if pinned {
			out.Pinned++
			out.Reason = "dependency_pinned"
			break
		}
		if _, err = tx.ExecContext(ctx, `DELETE FROM events WHERE owner=? AND seq=?`, owner, c.seq); err != nil {
			return out, err
		}
		out.Deleted++
		out.Bytes += c.bytes
		out.Floor = c.seq
	}
	if out.Deleted > 0 {
		_, err = tx.ExecContext(ctx, `INSERT INTO event_retention(owner,purged_through_seq,updated_at) VALUES(?,?,?) ON CONFLICT(owner) DO UPDATE SET purged_through_seq=max(purged_through_seq,excluded.purged_through_seq),updated_at=excluded.updated_at`, owner, out.Floor, envelope.FormatTime(now))
	}
	return out, err
}

func (l *Log) effectiveAck(ctx context.Context, q execer, owner string) (int64, error) {
	var ack int64
	err := q.QueryRowContext(ctx, `SELECT coalesce(max(a.seq),0) FROM acks a WHERE a.epoch=? AND a.seq <= coalesce((SELECT seq FROM sqlite_sequence WHERE name='events'),0) AND (
 (a.owner=? AND a.scope='*') OR EXISTS(SELECT 1 FROM peers p WHERE p.peer_id=? AND p.prn_id=a.owner AND a.scope IN ('*',p.peer_id)))`, l.epoch, owner, owner).Scan(&ack)
	return ack, err
}
func (l *Log) retentionPinned(ctx context.Context, q execer, owner string, seq int64, now time.Time) (bool, error) {
	var pinned bool
	err := q.QueryRowContext(ctx, `SELECT
   EXISTS(SELECT 1 FROM deliveries d WHERE d.event_seq=? AND (d.state IN ('queued','pushed','dead_letter') OR d.held=1 OR d.dispatching>0)) OR
   EXISTS(SELECT 1 FROM event_outbox o JOIN event_subscriptions s ON s.sub_id=o.sub_id WHERE s.peer_id=? AND o.seq=? AND (o.done_at IS NULL OR o.terminal='exhausted')) OR
   EXISTS(SELECT 1 FROM event_subscriptions s WHERE s.peer_id=?
     AND s.verified_at IS NOT NULL AND s.paused_reason IS NULL
     AND (s.expires_at IS NULL OR s.expires_at>?) AND (s.token_expires_at IS NULL OR s.token_expires_at>?)
     AND (s.cursor_epoch!=? OR s.cursor_seq<?)
     AND NOT EXISTS(SELECT 1 FROM event_outbox o WHERE o.sub_id=s.sub_id AND o.seq=?))`, seq, owner, seq, owner, envelope.FormatTime(now), envelope.FormatTime(now), l.epoch, seq, seq).Scan(&pinned)
	return pinned, err
}

// PreviewRetention estimates bounded acknowledged and opt-in hard prefixes in a consistent
// read snapshot, using the same ack and pin predicates as deletion. It samples
// at most 10,000 metadata rows and never reads or reports payload bytes.
func (l *Log) PreviewRetention(ctx context.Context, q *sql.Conn, now time.Time, age, hard time.Duration, high int64) (eligible, pinned, bytes int64, err error) {
	if age <= 0 && hard <= 0 {
		return 0, 0, 0, nil
	}
	rows, err := q.QueryContext(ctx, `SELECT owner,seq,at,length(CAST(data AS BLOB)) FROM events WHERE seq<=? ORDER BY owner,seq LIMIT 10000`, high)
	if err != nil {
		return 0, 0, 0, err
	}
	type row struct {
		owner, at  string
		seq, bytes int64
	}
	var candidates []row
	for rows.Next() {
		var c row
		if err := rows.Scan(&c.owner, &c.seq, &c.at, &c.bytes); err != nil {
			rows.Close()
			return 0, 0, 0, err
		}
		candidates = append(candidates, c)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return 0, 0, 0, err
	}
	var owner string
	var ack int64
	var blocked bool
	for _, c := range candidates {
		if c.owner != owner {
			owner = c.owner
			blocked = false
			ack, err = l.effectiveAck(ctx, q, owner)
			if err != nil {
				return 0, 0, 0, err
			}
		}
		if blocked {
			continue
		}
		at, err := envelope.ParseTime(c.at)
		if err != nil {
			return 0, 0, 0, errors.New("eventlog: retention_invalid_time")
		}
		candidateAge := age
		if ack < c.seq {
			candidateAge = hard
		}
		if candidateAge <= 0 || now.Sub(at) < candidateAge {
			blocked = true
			continue
		}
		pin, err := l.retentionPinned(ctx, q, owner, c.seq, now)
		if err != nil {
			return 0, 0, 0, err
		}
		if pin {
			pinned++
			blocked = true
			continue
		}
		eligible++
		bytes += c.bytes
	}
	return eligible, pinned, bytes, nil
}
