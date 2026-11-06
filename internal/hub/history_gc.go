package hub

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/eventlog"
)

// HistoryRetentionStatus separates body reclamation from lifetime locators.
// Pin diagnostics contain fixed reason codes and counts only.
type HistoryRetentionStatus struct {
	Policy              RetentionPolicy `json:"policy"`
	ConfiguredSeconds   int64           `json:"configured_seconds"`
	EffectiveSeconds    int64           `json:"effective_seconds"`
	ArchiveMode         bool            `json:"archive_mode"`
	PendingConfirmation bool            `json:"pending_confirmation"`
	Scanned             int             `json:"scanned"`
	Marked              int             `json:"marked"`
	Deleted             int             `json:"deleted"`
	Pinned              int             `json:"pinned"`
	Pins                map[string]int  `json:"pins"`
	Bytes               int64           `json:"bytes"`
	Tombstones          int64           `json:"tombstones"`
	Reason              string          `json:"reason,omitempty"`
	ErrorReason         string          `json:"error_reason,omitempty"`
}

func (h *Hub) HistoryRetentionStatus() HistoryRetentionStatus {
	h.retentionMu.Lock()
	defer h.retentionMu.Unlock()
	s := h.historyRetentionLast
	s.Pins = copyHistoryPins(s.Pins)
	return s
}
func copyHistoryPins(p map[string]int) map[string]int {
	out := map[string]int{}
	for k, v := range p {
		out[k] = v
	}
	return out
}
func historyStatus(p RetentionPolicy, a historyActivation) HistoryRetentionStatus {
	seconds, _ := p.History.Value.Int64()
	archive, _ := p.Archive.Value.Bool()
	s := HistoryRetentionStatus{Policy: p, ConfiguredSeconds: seconds, EffectiveSeconds: seconds, ArchiveMode: archive, PendingConfirmation: a.Pending || a.ArchivePending, Pins: map[string]int{}}
	if archive || s.PendingConfirmation {
		s.EffectiveSeconds = 0
	}
	return s
}
func (h *Hub) refreshHistoryRetentionStatus(ctx context.Context) error {
	var result HistoryRetentionStatus
	err := h.DB.Tx(ctx, func(q *sql.Tx) error {
		s, err := h.Preferences.LoadSnapshot(ctx, q)
		if err != nil {
			return err
		}
		p, err := retentionPolicy(s)
		if err != nil {
			return err
		}
		a, err := readHistoryActivation(ctx, q)
		if err != nil {
			return err
		}
		result = historyStatus(p, a)
		return q.QueryRowContext(ctx, `SELECT CAST(value AS INTEGER) FROM meta WHERE key='history_locator_count'`).Scan(&result.Tombstones)
	})
	h.retentionMu.Lock()
	defer h.retentionMu.Unlock()
	if err != nil {
		h.historyRetentionLast.ErrorReason = "policy_read_failed"
	} else {
		h.historyRetentionLast = result
	}
	return err
}

// HistoryVisibilitySQL applies only to messages aliased m in the caller's
// transaction. Read failures preserve history; deletion fails closed separately.
func (h *Hub) HistoryVisibilitySQL(ctx context.Context, tx *sql.Tx) (string, error) {
	s, err := h.Preferences.LoadSnapshot(ctx, tx)
	if err != nil {
		return "1", nil
	}
	p, err := retentionPolicy(s)
	if err != nil {
		return "1", nil
	}
	a, err := readHistoryActivation(ctx, tx)
	if err != nil {
		return "1", nil
	}
	status := historyStatus(p, a)
	if status.ArchiveMode || status.EffectiveSeconds == 0 {
		return "1", nil
	}
	// Expiry marks belong to the persisted history activation, whose revision
	// changes only when history/archive policy changes. The global snapshot
	// revision also advances for unrelated preferences and cannot invalidate
	// history visibility while that activation remains in effect.
	return fmt.Sprintf("(m.history_expired_at IS NULL OR m.history_expired_revision IS NULL OR m.history_expired_revision<>%d)", a.Revision), nil
}

type historyQuery interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

// Unknown dependency retirement semantics pin the body. All probes use
// indexed ids or bounded per-message dependency counts.
func historyPin(ctx context.Context, q historyQuery, id, thread string, limit int) (string, error) {
	checks := []struct {
		reason, sql string
		args        []any
	}{
		{"direct_delivery", `SELECT EXISTS(SELECT 1 FROM deliveries WHERE msg_id=? AND (state IN ('queued','pushed','dead_letter','processed') OR held=1 OR dispatching>0))`, []any{id}},
		{"reference_event", `SELECT EXISTS(SELECT 1 FROM events WHERE msg_id=?)`, []any{id}},
		{"inline_event", `SELECT EXISTS(SELECT 1 FROM events WHERE payload_format='inline' AND json_extract(data,'$.message.message_id')=?)`, []any{id}},
		{"history_event", `SELECT EXISTS(SELECT 1 FROM events WHERE payload_format='inline' AND json_extract(data,'$.message_id')=?)`, []any{id}},
		{"receipt_event", `SELECT EXISTS(SELECT 1 FROM deliveries d WHERE d.msg_id=? AND EXISTS(SELECT 1 FROM events e WHERE e.payload_format='inline' AND json_extract(e.data,'$.delivery_id')=d.dly_id))`, []any{id}},
		{"events_outbox", `SELECT EXISTS(SELECT 1 FROM deliveries d WHERE d.msg_id=? AND EXISTS(SELECT 1 FROM event_outbox o WHERE o.event_id=d.dly_id))`, []any{id}},
		{"unsupported_job", `SELECT EXISTS(SELECT 1 FROM jobs WHERE thr_id=?)`, []any{thread}},
		{"unsupported_approval", `SELECT EXISTS(SELECT 1 FROM approvals WHERE msg_id=?)`, []any{id}},
		{"reaction_wake_dedup", `SELECT EXISTS(SELECT 1 FROM reaction_wakes WHERE msg_id=?)`, []any{id}},
		{"unsupported_reactions", `SELECT EXISTS(SELECT 1 FROM reactions WHERE msg_id=?)`, []any{id}},
		{"legacy_snapshot", `SELECT EXISTS(SELECT 1 FROM event_message_snapshots WHERE msg_id=?)`, []any{id}},
		{"r2_blob_reference", `SELECT EXISTS(SELECT 1 FROM blob_object_refs WHERE msg_id=?)`, []any{id}},
		{"durable_blob_reference", `SELECT EXISTS(SELECT 1 FROM durable_blob_refs WHERE msg_id=?)`, []any{id}},
		{"unsupported_message_class", `SELECT EXISTS(SELECT 1 FROM messages WHERE msg_id=? AND (kind<>'message' OR job_id IS NOT NULL OR approval_id IS NOT NULL OR sender LIKE 'src_%'))`, []any{id}},
	}
	for _, c := range checks {
		var found bool
		if err := q.QueryRowContext(ctx, c.sql, c.args...).Scan(&found); err != nil {
			return "", err
		}
		if found {
			return c.reason, nil
		}
	}
	for _, table := range []string{"deliveries", "blob_refs"} {
		var n int
		if err := q.QueryRowContext(ctx, `SELECT count(*) FROM (SELECT 1 FROM `+table+` WHERE msg_id=? LIMIT ?)`, id, limit+1).Scan(&n); err != nil {
			return "", err
		}
		if n > limit {
			return "dependency_budget", nil
		}
	}
	return "", nil
}
func historyLimits(l eventlog.RetentionLimits) eventlog.RetentionLimits {
	if l.Rows <= 0 {
		l.Rows = 100
	}
	if l.Bytes <= 0 {
		l.Bytes = 1 << 20
	}
	if l.Target <= 0 {
		l.Target = 50 * time.Millisecond
	}
	return l
}

// PruneHistorySlice marks first and sweeps on a subsequent slice, under the
// writer lock shared with preference changes. Timers and wakeups confer no
// extra authorization. Events, audit and thread counters are never removed.
func (h *Hub) PruneHistorySlice(ctx context.Context) (HistoryRetentionStatus, error) {
	result := HistoryRetentionStatus{Pins: map[string]int{}}
	limits := historyLimits(h.Cfg.EventRetentionLimits)
	var committedLocators int64
	err := h.DB.Tx(ctx, func(tx *sql.Tx) error {
		snapshot, err := h.Preferences.LoadSnapshot(ctx, tx)
		if err != nil {
			return err
		}
		policy, err := retentionPolicy(snapshot)
		if err != nil {
			return err
		}
		a, err := readHistoryActivation(ctx, tx)
		if err != nil {
			return err
		}
		result = historyStatus(policy, a)
		if err := tx.QueryRowContext(ctx, `SELECT CAST(value AS INTEGER) FROM meta WHERE key='history_locator_count'`).Scan(&result.Tombstones); err != nil {
			return err
		}
		committedLocators = result.Tombstones
		if result.ArchiveMode {
			result.Reason = "archive"
			return nil
		}
		if result.PendingConfirmation {
			result.Reason = "activation_required"
			return nil
		}
		if result.EffectiveSeconds == 0 {
			result.Reason = "disabled"
			return nil
		}
		if result.ConfiguredSeconds != a.Seconds || a.Archive {
			return errors.New("retention: history_policy_mismatch")
		}
		var nb, cutoff sql.NullString
		var high sql.NullInt64
		if a.ActivationKey != "" {
			if err := tx.QueryRowContext(ctx, `SELECT not_before,cutoff_at,high_water FROM hub_preference_activations WHERE revision=? AND key=?`, a.Revision, a.ActivationKey).Scan(&nb, &cutoff, &high); err != nil {
				return err
			}
		}
		var cursor [2]string
		var raw string
		err = tx.QueryRowContext(ctx, `SELECT value FROM meta WHERE key='history_scan_cursor'`).Scan(&raw)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if raw != "" && json.Unmarshal([]byte(raw), &cursor) != nil {
			return errors.New("retention: invalid_history_cursor")
		}
		cutoffAge := envelope.FormatTime(h.Now().Add(-time.Duration(result.EffectiveSeconds) * time.Second))
		start := cursor
		wrapped := false
		started := time.Now()
		var sourceBytes int64
		rowsUsed := 0
		for result.Scanned < limits.Rows && rowsUsed < limits.Rows && sourceBytes < limits.Bytes && (result.Scanned == 0 || time.Since(started) < limits.Target) {
			var id, thread, at string
			var seq int
			var bodyBytes int64
			var expired sql.NullString
			var born sql.NullInt64
			query := `SELECT m.msg_id,m.thr_id,m.seq,m.created_at,length(CAST(m.body AS BLOB)),m.history_expired_at,
    coalesce(m.history_event_seq,(SELECT max(d.event_seq) FROM deliveries d WHERE d.msg_id=m.msg_id))
    FROM messages m WHERE m.created_at<=? AND (m.created_at,m.msg_id)>(?,?)`
			args := []any{cutoffAge, cursor[0], cursor[1]}
			if wrapped {
				query += ` AND (m.created_at,m.msg_id)<=(?,?)`
				args = append(args, start[0], start[1])
			}
			query += ` ORDER BY m.created_at,m.msg_id LIMIT 1`
			err = tx.QueryRowContext(ctx, query, args...).Scan(&id, &thread, &seq, &at, &bodyBytes, &expired, &born)
			if errors.Is(err, sql.ErrNoRows) {
				if wrapped || start[0] == "" {
					break
				}
				wrapped = true
				cursor = [2]string{}
				continue
			}
			if err != nil {
				return err
			}
			if result.Scanned > 0 && sourceBytes+bodyBytes > limits.Bytes {
				break
			}
			cursor = [2]string{at, id}
			result.Scanned++
			rowsUsed++
			sourceBytes += bodyBytes
			if !born.Valid || born.Int64 <= 0 {
				result.Pinned++
				result.Pins["untracked_history"]++
				continue
			}
			seconds, err := historyRuleSeconds(ctx, tx, born.Int64)
			if err != nil {
				return err
			}
			if a.ActivationKey != "" && nb.Valid && cutoff.Valid && high.Valid && h.nowStr() >= nb.String && born.Int64 <= high.Int64 && a.CutoffAt != "" && at <= a.CutoffAt {
				seconds = result.EffectiveSeconds
			}
			if seconds == 0 || at > envelope.FormatTime(h.Now().Add(-time.Duration(seconds)*time.Second)) {
				continue
			}
			if !expired.Valid {
				if _, err := tx.ExecContext(ctx, `UPDATE messages SET history_expired_at=?,history_expired_revision=?,history_event_seq=? WHERE msg_id=?`, h.nowStr(), a.Revision, born.Int64, id); err != nil {
					return err
				}
				result.Marked++
			} else {
				if _, err := tx.ExecContext(ctx, `UPDATE messages SET history_expired_revision=? WHERE msg_id=?`, a.Revision, id); err != nil {
					return err
				}
			}
			reason, err := historyPin(ctx, tx, id, thread, max(0, limits.Rows-rowsUsed))
			if err != nil {
				return err
			}
			if reason != "" {
				result.Pinned++
				result.Pins[reason]++
				continue
			}
			if !expired.Valid {
				continue
			}
			var dependencies int
			if err := tx.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM deliveries WHERE msg_id=?)+(SELECT count(*) FROM blob_refs WHERE msg_id=?)`, id, id).Scan(&dependencies); err != nil {
				return err
			}
			if rowsUsed+dependencies > limits.Rows {
				result.Pinned++
				result.Pins["dependency_budget"]++
				continue
			}
			// Adopt legacy maxima before deleting a suffix, including the final body.
			if _, err := tx.ExecContext(ctx, `UPDATE threads SET last_allocated_seq=max(last_allocated_seq,(SELECT coalesce(max(seq),0) FROM messages WHERE thr_id=?)) WHERE thr_id=?`, thread, thread); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `DELETE FROM deliveries WHERE msg_id=?`, id); err != nil {
				return err
			}
			reason, err = historyPin(ctx, tx, id, thread, limits.Rows)
			if err != nil {
				return err
			}
			if reason != "" {
				return errors.New("retention: history_dependency_changed")
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO message_history_locators(msg_id,thr_id,seq,expired_at) VALUES(?,?,?,?)`, id, thread, seq, expired.String); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `DELETE FROM blob_refs WHERE msg_id=?`, id); err != nil {
				return err
			}
			// Body retention must not reset loop control. Keep only the newest
			// erased non-reaction parent's compact loop block; an older body
			// swept later cannot replace it. Reactions never become parents.
			if _, err := tx.ExecContext(ctx, `INSERT INTO thread_history_loop_parents(thr_id,msg_id,seq,loop)
    SELECT thr_id,msg_id,seq,json_extract(body,'$.loop') FROM messages WHERE msg_id=? AND kind!='reaction'
    ON CONFLICT(thr_id) DO UPDATE SET msg_id=excluded.msg_id,seq=excluded.seq,loop=excluded.loop
    WHERE excluded.seq>thread_history_loop_parents.seq`, id); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `DELETE FROM messages WHERE msg_id=?`, id); err != nil {
				return err
			}
			result.Deleted++
			result.Tombstones++
			result.Bytes += bodyBytes
			rowsUsed += dependencies
		}
		if _, err := tx.ExecContext(ctx, `UPDATE meta SET value=? WHERE key='history_locator_count'`, fmt.Sprint(result.Tombstones)); err != nil {
			return err
		}
		encoded, _ := json.Marshal(cursor)
		_, err = tx.ExecContext(ctx, `INSERT INTO meta(key,value) VALUES('history_scan_cursor',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, string(encoded))
		return err
	})
	if err != nil {
		result.Marked = 0
		result.Deleted = 0
		result.Bytes = 0
		result.Tombstones = committedLocators
		result.ErrorReason = "policy_or_slice_failed"
	}
	h.retentionMu.Lock()
	h.historyRetentionLast = result
	h.retentionMu.Unlock()
	return result, err
}
