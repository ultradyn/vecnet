// Package eventlog is the hub's durable per-peer event log: append inside a
// store transaction, read by cursor, ack per stream owner and scope, and
// in-process wakeups for SSE and long-poll readers (PROTOCOL §1.3, §2.3, §2.4).
//
// Events are owned by a peer. A principal stream reads owners = all its peers
// and acks with owner = prn_id, scope = "*"; a narrowed stream acks
// owner = prn_id, scope = <peer_id>; a peer credential acks owner = peer_id,
// scope = "*" [R05]. Every event has one global seq, so reads across several
// owners merge by that seq and a single cursor covers them all.
package eventlog

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/store"
)

// Cursor is a position in the log: e<Epoch>.<Seq> (PROTOCOL §1.3). Reading
// after a cursor yields every event with a greater seq.
type Cursor struct{ Epoch, Seq int64 }

// Head is the sentinel for "?after=head": read from now on. Read resolves it
// to the owners' Current cursor.
var Head = Cursor{Seq: -1}

// MaxReadLimit caps one Read; a limit <= 0 or above it reads MaxReadLimit.
const MaxReadLimit = 1000

// ErrInvalidCursor is returned by ParseCursor for a string that does not
// match ^e[0-9]+\.[0-9]+$ (or overflows int64).
var ErrInvalidCursor = errors.New("eventlog: invalid cursor")

// ErrCursorPurged reports a cursor from another epoch or below retention
// (PROTOCOL §2.3, HTTP 409). Oldest is the oldest valid cursor in the live
// epoch; reading after it misses nothing still retained.
type ErrCursorPurged struct{ Oldest Cursor }

func (e ErrCursorPurged) Error() string {
	return "eventlog: cursor purged; oldest valid cursor is " + e.Oldest.String()
}

// ParseCursor parses "e<epoch>.<seq>"; anything else is ErrInvalidCursor.
func ParseCursor(s string) (Cursor, error) {
	rest, ok := strings.CutPrefix(s, "e")
	if !ok {
		return Cursor{}, fmt.Errorf("%w: %q", ErrInvalidCursor, s)
	}
	ep, sq, ok := strings.Cut(rest, ".")
	if !ok || !allDigits(ep) || !allDigits(sq) {
		return Cursor{}, fmt.Errorf("%w: %q", ErrInvalidCursor, s)
	}
	epoch, err1 := strconv.ParseInt(ep, 10, 64)
	seq, err2 := strconv.ParseInt(sq, 10, 64)
	if err1 != nil || err2 != nil {
		return Cursor{}, fmt.Errorf("%w: %q", ErrInvalidCursor, s)
	}
	return Cursor{Epoch: epoch, Seq: seq}, nil
}

func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// String renders the wire form, e.g. "e1.42".
func (c Cursor) String() string {
	return "e" + strconv.FormatInt(c.Epoch, 10) + "." + strconv.FormatInt(c.Seq, 10)
}

// Log is the event log over one store. The epoch is read from meta.epoch at
// New; the broker wakes in-process waiters after commits.
type Log struct {
	DB    *store.DB
	epoch int64

	mu      sync.Mutex
	waiters map[string]map[*waiter]struct{} // owner → waiters touching it
}

// waiter is one Wait call: ch closes once, on the first Notify touching any of
// its owners or when its ctx is done.
type waiter struct {
	ch     chan struct{}
	owners []string
	stop   func() bool // unregisters the ctx AfterFunc
	fired  bool
}

// New returns a Log over db, reading the epoch from meta.epoch.
func New(db *store.DB) (*Log, error) {
	v, err := db.Meta(context.Background(), "epoch")
	if err != nil {
		return nil, fmt.Errorf("eventlog: %w", err)
	}
	epoch, err := strconv.ParseInt(v, 10, 64)
	if err != nil || epoch < 0 {
		return nil, fmt.Errorf("eventlog: meta.epoch %q is not a non-negative integer", v)
	}
	return &Log{DB: db, epoch: epoch, waiters: map[string]map[*waiter]struct{}{}}, nil
}

// Epoch returns the live epoch.
func (l *Log) Epoch() int64 { return l.epoch }

// execer is the part of *sql.Tx and *sql.DB the log writes through.
type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func (l *Log) via(tx *sql.Tx) execer {
	if tx != nil {
		return tx
	}
	return l.DB
}

// Append inserts one event for owner inside tx (nil tx writes directly) and
// returns its cursor. data is JSON-encoded (json.RawMessage passes through).
// Call Notify(owner) after the transaction commits.
func (l *Log) Append(ctx context.Context, tx *sql.Tx, owner, typ string, at time.Time, data any) (Cursor, error) {
	if owner == "" || typ == "" {
		return Cursor{}, errors.New("eventlog: append needs an owner and a type")
	}
	b, err := json.Marshal(data)
	if err != nil {
		return Cursor{}, fmt.Errorf("eventlog: append %s: encode data: %w", typ, err)
	}
	res, err := l.via(tx).ExecContext(ctx,
		`INSERT INTO events(owner, type, at, data) VALUES (?, ?, ?, ?)`,
		owner, typ, at.UTC().Format("2006-01-02T15:04:05.000Z"), string(b))
	if err != nil {
		return Cursor{}, fmt.Errorf("eventlog: append %s: %w", typ, err)
	}
	seq, err := res.LastInsertId()
	if err != nil {
		return Cursor{}, fmt.Errorf("eventlog: append %s: seq: %w", typ, err)
	}
	return Cursor{Epoch: l.epoch, Seq: seq}, nil
}

// oldestValid is the lowest seq a cursor may name without missing a retained
// event: one below the oldest retained row, or the high-water mark when every
// row has been purged (AUTOINCREMENT never reuses a seq), or 0 on a new log.
// It is global, not per owner, so an owner with no early events is never
// mistaken for one whose events were purged.
func oldestValid(ctx context.Context, q execer) (int64, error) {
	var seq int64
	err := q.QueryRowContext(ctx, `SELECT coalesce(
		(SELECT min(seq) - 1 FROM events),
		(SELECT seq FROM sqlite_sequence WHERE name = 'events'),
		0)`).Scan(&seq)
	if err != nil {
		return 0, fmt.Errorf("eventlog: oldest seq: %w", err)
	}
	return seq, nil
}

// Oldest returns the oldest valid cursor: reading after it yields every
// retained event.
func (l *Log) Oldest(ctx context.Context) (Cursor, error) {
	seq, err := oldestValid(ctx, l.DB)
	if err != nil {
		return Cursor{}, err
	}
	return Cursor{Epoch: l.epoch, Seq: seq}, nil
}

// Read returns up to limit events of owners with seq > after.Seq, in global
// seq order. A cursor from another epoch or below retention is
// ErrCursorPurged; Head reads nothing (callers tail with Wait).
func (l *Log) Read(ctx context.Context, owners []string, after Cursor, limit int) ([]envelope.Event, error) {
	if after == Head {
		return []envelope.Event{}, nil
	}
	if after.Epoch != l.epoch {
		oldest, err := oldestValid(ctx, l.DB)
		if err != nil {
			return nil, err
		}
		return nil, ErrCursorPurged{Oldest: Cursor{Epoch: l.epoch, Seq: oldest}}
	}
	if after.Seq < 0 {
		return nil, fmt.Errorf("%w: negative seq %d", ErrInvalidCursor, after.Seq)
	}
	if limit <= 0 || limit > MaxReadLimit {
		limit = MaxReadLimit
	}
	out := []envelope.Event{}
	if len(owners) > 0 {
		args := make([]any, 0, len(owners)+2)
		for _, o := range owners {
			args = append(args, o)
		}
		args = append(args, after.Seq, limit)
		rows, err := l.DB.QueryContext(ctx,
			`SELECT seq, type, at, data FROM events WHERE owner IN (`+placeholders(len(owners))+`)
			 AND seq > ? ORDER BY seq LIMIT ?`, args...)
		if err != nil {
			return nil, fmt.Errorf("eventlog: read: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var (
				seq  int64
				ev   envelope.Event
				data string
			)
			if err := rows.Scan(&seq, &ev.Type, &ev.At, &data); err != nil {
				return nil, fmt.Errorf("eventlog: read: %w", err)
			}
			ev.Cursor = Cursor{Epoch: l.epoch, Seq: seq}.String()
			ev.Data = json.RawMessage(data)
			out = append(out, ev)
		}
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("eventlog: read: %w", err)
		}
	}
	// Checked after the query: a purge racing the read is still caught, since
	// the retention floor only ever rises.
	oldest, err := oldestValid(ctx, l.DB)
	if err != nil {
		return nil, err
	}
	if after.Seq < oldest {
		return nil, ErrCursorPurged{Oldest: Cursor{Epoch: l.epoch, Seq: oldest}}
	}
	return out, nil
}

func placeholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

// Current returns the cursor of the newest event across owners: reading after
// it yields only future events. With no events it is e<epoch>.0, raised to the
// retention floor so the result is always a valid Read cursor.
func (l *Log) Current(ctx context.Context, owners []string) (Cursor, error) {
	var seq int64
	if len(owners) > 0 {
		args := make([]any, len(owners))
		for i, o := range owners {
			args[i] = o
		}
		err := l.DB.QueryRowContext(ctx,
			`SELECT coalesce(max(seq), 0) FROM events WHERE owner IN (`+placeholders(len(owners))+`)`,
			args...).Scan(&seq)
		if err != nil {
			return Cursor{}, fmt.Errorf("eventlog: current: %w", err)
		}
	}
	oldest, err := oldestValid(ctx, l.DB)
	if err != nil {
		return Cursor{}, err
	}
	return Cursor{Epoch: l.epoch, Seq: max(seq, oldest)}, nil
}

// Notify wakes every Wait touching any of owners. Call it after the
// appending transaction commits. It never blocks: waiters are woken by
// closing their channels. A principal id is a wake key too, though it owns no
// events: the hub notifies it when the principal's peer set changes.
func (l *Log) Notify(owners ...string) {
	var fired []*waiter
	l.mu.Lock()
	for _, o := range owners {
		for w := range l.waiters[o] {
			l.fireLocked(w)
			fired = append(fired, w)
		}
	}
	l.mu.Unlock()
	for _, w := range fired {
		w.stop()
	}
}

// Wait returns a channel closed on the next Notify touching any of owners, or
// when ctx is done. Call it before Read so an append between the Read and the
// wait is not missed.
func (l *Log) Wait(ctx context.Context, owners []string) <-chan struct{} {
	w := &waiter{ch: make(chan struct{}), owners: append([]string(nil), owners...)}
	l.mu.Lock()
	for _, o := range w.owners {
		set := l.waiters[o]
		if set == nil {
			set = map[*waiter]struct{}{}
			l.waiters[o] = set
		}
		set[w] = struct{}{}
	}
	// Set under the lock so Notify never sees a waiter without stop.
	w.stop = context.AfterFunc(ctx, func() {
		l.mu.Lock()
		l.fireLocked(w)
		l.mu.Unlock()
	})
	l.mu.Unlock()
	return w.ch
}

// Waiters reports how many pending Wait calls touch owner. Tests use it to
// know a long-poll or stream is parked before they append.
func (l *Log) Waiters(owner string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.waiters[owner])
}

// fireLocked closes w.ch once and unregisters w from every owner. l.mu held.
func (l *Log) fireLocked(w *waiter) {
	if w.fired {
		return
	}
	w.fired = true
	close(w.ch)
	for _, o := range w.owners {
		if set := l.waiters[o]; set != nil {
			delete(set, w)
			if len(set) == 0 {
				delete(l.waiters, o)
			}
		}
	}
}

// Ack records c as the acked cursor of (owner, scope) inside tx (nil tx
// writes directly). The stored cursor never moves backwards. A cursor from
// another epoch is ErrCursorPurged; a negative seq is ErrInvalidCursor.
func (l *Log) Ack(ctx context.Context, tx *sql.Tx, owner, scope string, c Cursor) error {
	if owner == "" || scope == "" {
		return errors.New("eventlog: ack needs an owner and a scope")
	}
	if c.Epoch != l.epoch {
		oldest, err := oldestValid(ctx, l.via(tx))
		if err != nil {
			return err
		}
		return ErrCursorPurged{Oldest: Cursor{Epoch: l.epoch, Seq: oldest}}
	}
	if c.Seq < 0 {
		return fmt.Errorf("%w: negative seq %d", ErrInvalidCursor, c.Seq)
	}
	_, err := l.via(tx).ExecContext(ctx,
		`INSERT INTO acks(owner, scope, seq, acked_at) VALUES (?, ?, ?, ?)
		 ON CONFLICT(owner, scope) DO UPDATE SET seq = excluded.seq, acked_at = excluded.acked_at
		 WHERE excluded.seq > acks.seq`,
		owner, scope, c.Seq, time.Now().UTC().Format("2006-01-02T15:04:05.000Z"))
	if err != nil {
		return fmt.Errorf("eventlog: ack: %w", err)
	}
	return nil
}

// Acked returns the acked cursor of (owner, scope); ok is false when nothing
// has been acked yet.
func (l *Log) Acked(ctx context.Context, owner, scope string) (Cursor, bool, error) {
	var seq int64
	err := l.DB.QueryRowContext(ctx, `SELECT seq FROM acks WHERE owner = ? AND scope = ?`, owner, scope).Scan(&seq)
	if errors.Is(err, sql.ErrNoRows) {
		return Cursor{}, false, nil
	}
	if err != nil {
		return Cursor{}, false, fmt.Errorf("eventlog: acked: %w", err)
	}
	return Cursor{Epoch: l.epoch, Seq: seq}, true, nil
}
