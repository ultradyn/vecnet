package hub

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"database/sql/driver"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/XertroV/vecnet/internal/auth"
	"github.com/XertroV/vecnet/internal/eventpayload"
)

const backfillMetaKey = "delivery_backfill_v1"

// BackfillLimits bounds a single transaction. Zero fields use the initial
// operational defaults. Target is a cooperative writer-time target, not a SQL
// statement timeout. One larger event may occupy a slice on its own.
// RescanExceptions explicitly starts/resumes an exception-only pass, retaining
// the original forward high-water mark. Stop passing it when Done is true.
type BackfillLimits struct {
	Rows             int
	Bytes            int64
	Target           time.Duration
	DryRun           bool
	RescanExceptions bool
}

func DefaultBackfillLimits() BackfillLimits {
	return BackfillLimits{Rows: 100, Bytes: 1 << 20, Target: 50 * time.Millisecond}
}
func (l BackfillLimits) normalized() (BackfillLimits, error) {
	if l.Rows < 0 || l.Bytes < 0 || l.Target < 0 || l.Bytes > (math.MaxInt64-(64<<20))/4 {
		return l, errors.New("backfill: invalid_limits")
	}
	d := DefaultBackfillLimits()
	if l.Rows == 0 {
		l.Rows = d.Rows
	}
	if l.Bytes == 0 {
		l.Bytes = d.Bytes
	}
	if l.Target == 0 {
		l.Target = d.Target
	}
	return l, nil
}

// ValidateBackfillLimits validates bootstrap settings without opening a store.
func ValidateBackfillLimits(l BackfillLimits) error { _, err := l.normalized(); return err }

// BackfillCapacity and BackfillCheckpoint are filesystem/SQLite probe results.
// AvailableBytes excludes reserved filesystem blocks. No path or payload is
// returned in diagnostics. Checkpoints are PASSIVE; readers are never evicted.
type BackfillCapacity struct {
	AvailableBytes int64
	WALBytes       int64
}
type BackfillCheckpoint struct {
	Busy              bool
	LogPages          int64
	CheckpointedPages int64
}

type BackfillException struct {
	Seq    int64  `json:"seq"`
	Reason string `json:"reason"`
}

// BackfillProgress contains only bounded ids, counts, byte totals and codes.
// Counts describe this slice; totals and checkpoints survive restart atomically.
// PausedReason is empty, disabled, low_headroom, wal_pressure, lock_pressure,
// sqlite_full or exception_reconciliation. BytesRemoved is the event JSON delta;
// it excludes new snapshot, index and WAL allocation and is not a physical
// disk-savings measurement.
// LastSliceNanos includes transaction completion in the returned/live status.
// The durable checkpoint retains the elapsed time sampled before its own write
// and commit; their final duration is available only in the running process.
type BackfillProgress struct {
	Enabled               bool                `json:"enabled"`
	HighSeq               int64               `json:"high_seq"`
	LastSeq               int64               `json:"last_seq"`
	RescanHighSeq         int64               `json:"rescan_high_seq"`
	RescanLastSeq         int64               `json:"rescan_last_seq"`
	Rescanning            bool                `json:"rescanning"`
	Processed             int                 `json:"processed"`
	Converted             int                 `json:"converted"`
	Skipped               int                 `json:"skipped"`
	Failed                int                 `json:"failed"`
	TotalConverted        int64               `json:"total_converted"`
	TotalSkipped          int64               `json:"total_skipped"`
	TotalFailed           int64               `json:"total_failed"`
	SourceBytes           int64               `json:"source_bytes"`
	StoredBytes           int64               `json:"stored_bytes"`
	BytesRemoved          int64               `json:"bytes_removed"`
	LastSliceNanos        int64               `json:"last_slice_nanos"`
	RequiredHeadroomBytes int64               `json:"required_headroom_bytes"`
	AvailableBytes        int64               `json:"available_bytes"`
	WALBytes              int64               `json:"wal_bytes"`
	PausedReason          string              `json:"paused_reason"`
	Done                  bool                `json:"done"`
	DryRun                bool                `json:"dry_run"`
	Seqs                  []int64             `json:"seqs"`
	ExceptionCountPending bool                `json:"exception_count_pending,omitempty"`
	ExceptionCountSeq     int64               `json:"exception_count_seq,omitempty"`
	ExceptionCount        int64               `json:"exception_count"`
	Exceptions            []BackfillException `json:"exceptions"`
}

func (h *Hub) backfillCapacity(ctx context.Context) (BackfillCapacity, error) {
	if h.Cfg.BackfillCapacity != nil {
		return h.Cfg.BackfillCapacity(ctx)
	}
	if err := ctx.Err(); err != nil {
		return BackfillCapacity{}, err
	}
	var stat syscall.Statfs_t
	if err := syscall.Statfs(h.Cfg.DataDir, &stat); err != nil {
		return BackfillCapacity{}, errors.New("backfill: capacity_probe")
	}
	c := BackfillCapacity{AvailableBytes: int64(stat.Bavail) * int64(stat.Bsize)}
	info, err := os.Stat(filepath.Join(h.Cfg.DataDir, "hub.db-wal"))
	if err == nil {
		c.WALBytes = info.Size()
	} else if !errors.Is(err, os.ErrNotExist) {
		return c, errors.New("backfill: wal_probe")
	}
	return c, nil
}
func (h *Hub) backfillCheckpoint(ctx context.Context) (BackfillCheckpoint, error) {
	if h.Cfg.BackfillCheckpoint != nil {
		return h.Cfg.BackfillCheckpoint(ctx)
	}
	var p BackfillCheckpoint
	var busy int
	err := h.DB.QueryRowContext(ctx, `PRAGMA wal_checkpoint(PASSIVE)`).Scan(&busy, &p.LogPages, &p.CheckpointedPages)
	p.Busy = busy != 0 || p.LogPages > p.CheckpointedPages
	return p, err
}
func backfillLoad(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}) (BackfillProgress, bool, error) {
	var p BackfillProgress
	var raw []byte
	err := q.QueryRowContext(ctx, `SELECT value FROM meta WHERE key=?`, backfillMetaKey).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return p, false, nil
	}
	if err != nil {
		return p, false, err
	}
	if json.Unmarshal(raw, &p) != nil || p.LastSeq < 0 || p.HighSeq < p.LastSeq || p.ExceptionCount < 0 || p.ExceptionCountSeq < 0 {
		return p, false, errors.New("backfill: checkpoint_invalid")
	}
	return p, true, nil
}
func backfillCode(err error) string {
	var code interface{ Code() int }
	if errors.As(err, &code) {
		switch code.Code() & 255 {
		case 13:
			return "sqlite_full"
		case 5, 6:
			return "lock_pressure"
		}
	}
	return ""
}

// captureTx preserves driver errors that eventpayload deliberately sanitizes.
// This lets SQLITE_FULL abort/pause the entire slice without exposing SQL text.
type captureTx struct {
	*sql.Tx
	writeErr error
}

func (t *captureTx) ExecContext(ctx context.Context, q string, args ...any) (sql.Result, error) {
	r, e := t.Tx.ExecContext(ctx, q, args...)
	if e != nil {
		t.writeErr = e
	}
	return r, e
}

// BackfillDeliverySlice converts at most one bounded transaction. Snapshot,
// conditional event update, durable exceptions and progress commit together.
// All transient errors roll the whole slice back. No maintenance runs in New.
func (h *Hub) BackfillDeliverySlice(ctx context.Context, limits BackfillLimits) (out BackfillProgress, err error) {
	if err = ctx.Err(); err != nil {
		return out, err
	}
	limits, err = limits.normalized()
	if err != nil {
		return out, err
	}
	if !h.backfillMu.TryLock() {
		return BackfillProgress{PausedReason: "lock_pressure"}, nil
	}
	defer h.backfillMu.Unlock()
	defer func() { h.backfillLast = out }()
	out.Enabled = h.Cfg.BackfillEnabled
	out.DryRun = limits.DryRun
	if !out.Enabled {
		out.PausedReason = "disabled"
		return out, nil
	}
	// Gate filesystem capacity before even a PASSIVE checkpoint can write.
	capacity, e := h.backfillCapacity(ctx)
	if e != nil {
		if ctx.Err() != nil {
			return out, ctx.Err()
		}
		return out, errors.New("backfill: capacity_probe")
	}
	if capacity.AvailableBytes < 0 || capacity.WALBytes < 0 || capacity.WALBytes > math.MaxInt64-max(int64(64<<20), 4*limits.Bytes) {
		return out, errors.New("backfill: capacity_invalid")
	}
	out.RequiredHeadroomBytes = max(int64(64<<20), 4*limits.Bytes) + capacity.WALBytes
	out.AvailableBytes, out.WALBytes = capacity.AvailableBytes, capacity.WALBytes
	if capacity.AvailableBytes <= out.RequiredHeadroomBytes {
		out.PausedReason = "low_headroom"
		if capacity.WALBytes > 0 {
			out.PausedReason = "wal_pressure"
		}
		return out, nil
	}
	// Checkpoint outside the writer transaction, never wait on readers.
	probeCtx, cancelProbe := context.WithTimeout(ctx, min(limits.Target, 50*time.Millisecond))
	checkpoint, e := h.backfillCheckpoint(probeCtx)
	cancelProbe()
	if e != nil {
		if ctx.Err() != nil {
			return out, ctx.Err()
		}
		if errors.Is(e, context.DeadlineExceeded) {
			out.PausedReason = "lock_pressure"
			return out, nil
		}
		if reason := backfillCode(e); reason != "" {
			out.PausedReason = reason
			return out, nil
		}
		return out, errors.New("backfill: checkpoint_probe")
	}
	if checkpoint.Busy {
		out.PausedReason = "wal_pressure"
		return out, nil
	}
	// A maintenance writer yields immediately to an existing writer. Restore the
	// connection's ordinary busy timeout even on cancellation before pooling it.
	acquireCtx, cancelAcquire := context.WithTimeout(ctx, min(limits.Target, 50*time.Millisecond))
	conn, e := h.DB.Conn(acquireCtx)
	cancelAcquire()
	if e != nil {
		if ctx.Err() != nil {
			return out, ctx.Err()
		}
		if errors.Is(e, context.DeadlineExceeded) {
			out.PausedReason = "lock_pressure"
			return out, nil
		}
		return out, errors.New("backfill: connection")
	}
	defer conn.Close()
	if _, e = conn.ExecContext(ctx, `PRAGMA busy_timeout=1`); e != nil {
		// Cancellation can race a pragma that already took effect.
		_ = conn.Raw(func(any) error { return driver.ErrBadConn })
		if ctx.Err() != nil {
			return out, ctx.Err()
		}
		return out, errors.New("backfill: connection")
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if _, e := conn.ExecContext(cleanup, `PRAGMA busy_timeout=5000`); e != nil {
			_ = conn.Raw(func(any) error { return driver.ErrBadConn })
		}
	}()
	start := h.Now()
	// Run after commit/rollback, including the checkpoint write and commit work.
	defer func() { out.LastSliceNanos = h.Now().Sub(start).Nanoseconds() }()
	tx, e := conn.BeginTx(ctx, nil)
	if e != nil {
		if ctx.Err() != nil {
			return out, ctx.Err()
		}
		if reason := backfillCode(e); reason != "" {
			out.PausedReason = reason
			return out, nil
		}
		return out, errors.New("backfill: begin")
	}
	defer tx.Rollback()
	tracker := &captureTx{Tx: tx}
	old, exists, e := backfillLoad(ctx, tx)
	if e != nil {
		return out, errors.New("backfill: checkpoint_read")
	}
	old.Enabled = out.Enabled
	old.RequiredHeadroomBytes = out.RequiredHeadroomBytes
	old.AvailableBytes = out.AvailableBytes
	old.WALBytes = out.WALBytes
	out.HighSeq, out.LastSeq = old.HighSeq, old.LastSeq
	out.RescanHighSeq, out.RescanLastSeq, out.Rescanning = old.RescanHighSeq, old.RescanLastSeq, old.Rescanning
	out.TotalConverted, out.TotalSkipped = old.TotalConverted, old.TotalSkipped
	out.TotalFailed = old.TotalFailed
	out.ExceptionCount = old.ExceptionCount
	out.ExceptionCountPending, out.ExceptionCountSeq = old.ExceptionCountPending, old.ExceptionCountSeq
	// Any error below rolls back, and reports no uncommitted counts or ids.
	defer func() {
		if err == nil {
			return
		}
		reason := backfillCode(err)
		if tracker.writeErr != nil {
			if r := backfillCode(tracker.writeErr); r != "" {
				reason = r
			}
		}
		out = old
		out.Processed = 0
		out.Converted = 0
		out.Skipped = 0
		out.Failed = 0
		out.SourceBytes = 0
		out.StoredBytes = 0
		out.BytesRemoved = 0
		out.Seqs = nil
		if ctx.Err() != nil {
			err = ctx.Err()
		} else if reason != "" {
			out.PausedReason = reason
			err = nil
		} else {
			err = errors.New("backfill: slice_failed")
		}
	}()
	if !exists {
		if err = tx.QueryRowContext(ctx, `SELECT coalesce(max(seq),0) FROM events`).Scan(&out.HighSeq); err != nil {
			return out, err
		}
		// Normally exceptions and their exact count already share a checkpoint.
		// If metadata is absent (e.g. a manual repair), reconcile separately in
		// bounded seq ranges before converting any events. Never COUNT the table.
		if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM delivery_backfill_exceptions)`).Scan(&out.ExceptionCountPending); err != nil {
			return out, err
		}
	}
	if out.ExceptionCountPending {
		if err = backfillReconcileCount(ctx, tx, &out, limits, h.Now, start); err != nil {
			return out, err
		}
		out.PausedReason = "exception_reconciliation"
		return out, backfillSave(ctx, tx, &out, limits.DryRun, false, h.Now, start)
	}
	if limits.RescanExceptions && !out.Rescanning {
		out.Rescanning = true
		out.RescanLastSeq = 0
		if err = tx.QueryRowContext(ctx, `SELECT coalesce(max(seq),0) FROM delivery_backfill_exceptions`).Scan(&out.RescanHighSeq); err != nil {
			return out, err
		}
	}
	lower, upper := out.LastSeq, out.HighSeq
	query := `SELECT e.seq,e.data FROM events e WHERE e.seq>? AND e.seq<=? AND e.type='delivery' AND e.payload_format='inline' ORDER BY e.seq LIMIT ?`
	if limits.RescanExceptions {
		lower, upper = out.RescanLastSeq, out.RescanHighSeq
		// Bound examined exception keys before fetching events. Exceptions survive
		// pruning, so limiting eligible join results would not bound this scan.
		query = `SELECT seq FROM delivery_backfill_exceptions WHERE seq>? AND seq<=? ORDER BY seq LIMIT ?`
	}
	rows, e := tx.QueryContext(ctx, query, lower, upper, limits.Rows)
	if e != nil {
		return out, e
	}
	type row struct {
		seq  int64
		data []byte
	}
	var candidates []row
	var selectedBytes int64
	for rows.Next() {
		var r row
		if limits.RescanExceptions {
			e = rows.Scan(&r.seq)
		} else {
			e = rows.Scan(&r.seq, &r.data)
		}
		if e != nil {
			rows.Close()
			return out, e
		}
		if len(candidates) > 0 && int64(len(r.data)) > limits.Bytes-selectedBytes {
			break
		}
		candidates = append(candidates, r)
		selectedBytes += int64(len(r.data))
		if selectedBytes >= limits.Bytes {
			break
		}
	}
	rowErr := rows.Err()
	closeErr := rows.Close()
	if rowErr != nil {
		return out, rowErr
	}
	if closeErr != nil {
		return out, closeErr
	}
	for i, r := range candidates {
		if i > 0 && (h.Now().Sub(start) >= limits.Target || out.SourceBytes >= limits.Bytes) {
			break
		}
		if err = ctx.Err(); err != nil {
			return out, err
		}
		if limits.RescanExceptions {
			e := tx.QueryRowContext(ctx, `SELECT data FROM events WHERE seq=? AND type='delivery' AND payload_format='inline'`, r.seq).Scan(&r.data)
			if errors.Is(e, sql.ErrNoRows) {
				// Keep diagnostics for missing/ineligible events, but checkpoint the
				// examined key so it cannot stall or bypass the bounded rescan.
				out.RescanLastSeq = r.seq
				continue
			}
			if e != nil {
				return out, e
			}
			if out.Processed > 0 && int64(len(r.data)) > limits.Bytes-out.SourceBytes {
				break
			}
		}
		var wire struct {
			Message struct {
				ID string `json:"message_id"`
			} `json:"message"`
		}
		reason := ""
		if json.Unmarshal(r.data, &wire) != nil || wire.Message.ID == "" {
			reason = "invalid_shape"
		}
		// The event sequence is authoritative even if embedded ids disagree.
		// Use the event-sequence index and check every linked hash before
		// EncodeDelivery can insert a snapshot.
		if reason == "" {
			hashes, e := tx.QueryContext(ctx, `SELECT body_sha256 FROM deliveries WHERE event_seq=?`, r.seq)
			if e != nil {
				return out, e
			}
			sum := sha256.Sum256(r.data)
			expected := hex.EncodeToString(sum[:])
			for hashes.Next() {
				var hash string
				if e = hashes.Scan(&hash); e != nil {
					hashes.Close()
					return out, e
				}
				if hash != expected {
					reason = "delivery_hash_mismatch"
				}
			}
			e = hashes.Err()
			closeErr := hashes.Close()
			if e != nil {
				return out, e
			}
			if closeErr != nil {
				return out, closeErr
			}
		}
		var p eventpayload.StoredPayload
		if reason == "" {
			p, err = eventpayload.EncodeDelivery(ctx, tracker, wire.Message.ID, r.data)
			if err != nil {
				var invalid *eventpayload.NotConvertibleError
				var storage *eventpayload.StorageError
				switch {
				case errors.As(err, &invalid):
					reason = invalid.Reason
				case errors.As(err, &storage) && storage.Reason == "delivery_hash_mismatch":
					reason = storage.Reason
				default:
					return out, err
				}
				err = nil
			}
		}
		if reason == "" {
			var got [][]byte
			got, err = eventpayload.HydrateBatch(ctx, tx, []eventpayload.StoredPayload{p})
			if err != nil {
				return out, err
			}
			if !bytes.Equal(got[0], r.data) {
				return out, errors.New("backfill: roundtrip_mismatch")
			}
		}
		if reason != "" {
			result, e := tx.ExecContext(ctx, `INSERT INTO delivery_backfill_exceptions(seq,reason) VALUES (?,?) ON CONFLICT(seq) DO NOTHING`, r.seq, reason)
			if e != nil {
				return out, e
			}
			n, e := result.RowsAffected()
			if e != nil {
				return out, e
			}
			out.ExceptionCount += n
			if n == 0 {
				if _, err = tx.ExecContext(ctx, `UPDATE delivery_backfill_exceptions SET reason=? WHERE seq=?`, reason, r.seq); err != nil {
					return out, err
				}
			}
			if reason == "delivery_hash_mismatch" {
				out.Failed++
			} else {
				out.Skipped++
			}
		} else {
			var result sql.Result
			result, err = tx.ExecContext(ctx, `UPDATE events SET data=?,payload_format=?,msg_id=?,snapshot_id=NULLIF(?,'') WHERE seq=? AND payload_format='inline' AND data=?`, string(p.Data), p.Format, p.MsgID, p.SnapshotID, r.seq, string(r.data))
			if err != nil {
				return out, err
			}
			var n int64
			n, err = result.RowsAffected()
			if err != nil {
				return out, err
			}
			if n != 1 {
				return out, errors.New("backfill: conflicting_mutation")
			}
			result, err = tx.ExecContext(ctx, `DELETE FROM delivery_backfill_exceptions WHERE seq=?`, r.seq)
			if err != nil {
				return out, err
			}
			n, err = result.RowsAffected()
			if err != nil {
				return out, err
			}
			out.ExceptionCount -= n
			out.Converted++
			out.StoredBytes += int64(len(p.Data))
			out.BytesRemoved += int64(len(r.data) - len(p.Data))
		}
		out.Processed++
		out.SourceBytes += int64(len(r.data))
		out.Seqs = append(out.Seqs, r.seq)
		if limits.RescanExceptions {
			out.RescanLastSeq = r.seq
		} else {
			out.LastSeq = r.seq
		}
	}
	var remaining bool
	if limits.RescanExceptions {
		err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM delivery_backfill_exceptions WHERE seq>? AND seq<=?)`, out.RescanLastSeq, out.RescanHighSeq).Scan(&remaining)
	} else {
		err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM events WHERE seq>? AND seq<=? AND type='delivery' AND payload_format='inline')`, out.LastSeq, out.HighSeq).Scan(&remaining)
	}
	if err != nil {
		return out, err
	}
	out.Done = !remaining
	if out.Done {
		if limits.RescanExceptions {
			out.RescanLastSeq = out.RescanHighSeq
			out.Rescanning = false
		} else {
			out.LastSeq = out.HighSeq
		}
	}
	out.TotalConverted += int64(out.Converted)
	out.TotalSkipped += int64(out.Skipped)
	out.TotalFailed += int64(out.Failed)
	return out, backfillSave(ctx, tx, &out, limits.DryRun, true, h.Now, start)
}

// Reconciliation is a separate, resumable phase only for a missing checkpoint.
// Normal slices and status reuse the counter committed with exception mutations.
func backfillReconcileCount(ctx context.Context, tx *sql.Tx, out *BackfillProgress, limits BackfillLimits, now func() time.Time, start time.Time) error {
	rows, err := tx.QueryContext(ctx, `SELECT seq FROM delivery_backfill_exceptions WHERE seq>? ORDER BY seq LIMIT ?`, out.ExceptionCountSeq, limits.Rows)
	if err != nil {
		return err
	}
	n := 0
	for rows.Next() {
		if n > 0 && now().Sub(start) >= limits.Target {
			break
		}
		if err := rows.Scan(&out.ExceptionCountSeq); err != nil {
			rows.Close()
			return err
		}
		out.ExceptionCount++
		n++
	}
	err = rows.Err()
	closeErr := rows.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM delivery_backfill_exceptions WHERE seq>?)`, out.ExceptionCountSeq).Scan(&out.ExceptionCountPending)
}

func backfillSave(ctx context.Context, tx *sql.Tx, out *BackfillProgress, dryRun, enableRefs bool, now func() time.Time, start time.Time) error {
	out.LastSliceNanos = now().Sub(start).Nanoseconds()
	if dryRun {
		return nil
	}
	data, err := json.Marshal(out)
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO meta(key,value) VALUES (?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, backfillMetaKey, string(data)); err != nil {
		return err
	}
	if enableRefs {
		if _, err = tx.ExecContext(ctx, `INSERT INTO meta(key,value) VALUES ('event_payload_refs_enabled','1') ON CONFLICT(key) DO UPDATE SET value='1'`); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// BackfillDeliveryStatus is an operator-only diagnostic seam. Exception details
// are capped at 100. ExceptionCount is exact unless ExceptionCountPending marks
// a bounded reconciliation after a missing checkpoint; no status call recounts.
func (h *Hub) BackfillDeliveryStatus(ctx context.Context, c auth.Caller) (BackfillProgress, error) {
	if c.Kind != "operator" {
		return BackfillProgress{}, E(403, "forbidden", "operator required")
	}
	h.backfillMu.Lock()
	defer h.backfillMu.Unlock()
	var out BackfillProgress
	err := h.DB.ReadSnapshot(ctx, func(q *sql.Conn) error {
		var err error
		var exists bool
		out, exists, err = backfillLoad(ctx, q)
		if err != nil {
			return err
		}
		if !exists {
			if err = q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM delivery_backfill_exceptions)`).Scan(&out.ExceptionCountPending); err != nil {
				return err
			}
		}
		rows, err := q.QueryContext(ctx, `SELECT seq,reason FROM delivery_backfill_exceptions ORDER BY seq LIMIT 100`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var ex BackfillException
			if err = rows.Scan(&ex.Seq, &ex.Reason); err != nil {
				return err
			}
			out.Exceptions = append(out.Exceptions, ex)
		}
		return rows.Err()
	})
	if err != nil {
		return out, errors.New("backfill: status_failed")
	}
	out.Enabled = h.Cfg.BackfillEnabled
	if h.backfillLast.Enabled && !h.backfillLast.DryRun {
		out.LastSliceNanos = h.backfillLast.LastSliceNanos
	}
	if h.backfillLast.PausedReason != "" {
		out.PausedReason = h.backfillLast.PausedReason
		out.AvailableBytes = h.backfillLast.AvailableBytes
		out.WALBytes = h.backfillLast.WALBytes
		out.RequiredHeadroomBytes = h.backfillLast.RequiredHeadroomBytes
	}
	return out, nil
}
