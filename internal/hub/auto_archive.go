package hub

import (
	"context"
	"database/sql"
	"time"

	"github.com/XertroV/vecnet/internal/audit"
	"github.com/XertroV/vecnet/internal/envelope"
)

// ArchiveIdlePeers revokes at most 200 idle peers per run. Only hub-observed
// times count: presence_at is peer supplied and may lie arbitrarily in the future.
// reachMu precedes the writer transaction, matching touchLocked/StreamOpened, so
// no open stream or new observed activity can race the eligibility check.
func (h *Hub) ArchiveIdlePeers(ctx context.Context, now time.Time) (int, error) {
	h.reachMu.Lock()
	var archived []string
	err := h.DB.Tx(ctx, func(tx *sql.Tx) error {
		snapshot, err := h.Preferences.LoadSnapshot(ctx, tx)
		if err != nil {
			return err
		}
		after := preferenceDuration(snapshot, "agents.auto_archive_after")
		if after == 0 {
			return nil
		}
		effective, err := snapshot.Get("agents.auto_archive_all_kinds")
		if err != nil {
			return err
		}
		all, _ := effective.Value.Bool()
		rows, err := tx.QueryContext(ctx, `SELECT peer_id,last_seen,enrolled_at,unarchived_at FROM peers WHERE archived_at IS NULL AND disabled=0 AND kind NOT IN ('operator','source') AND (? OR kind='session') ORDER BY peer_id`, all)
		if err != nil {
			return err
		}
		type candidate struct {
			id   string
			last time.Time
		}
		var candidates []candidate
		for rows.Next() {
			var id string
			var times [3]sql.NullString
			if err := rows.Scan(&id, &times[0], &times[1], &times[2]); err != nil {
				rows.Close()
				return err
			}
			if h.streams[id] > 0 {
				continue
			}
			var last time.Time
			valid := true
			for _, at := range times {
				if !at.Valid || at.String == "" {
					continue
				}
				parsed, err := time.Parse(time.RFC3339Nano, at.String)
				if err != nil {
					valid = false
					break
				}
				if parsed.After(last) {
					last = parsed
				}
			}
			// Unknown/malformed observation times are left alone, not guessed idle.
			if valid && !last.IsZero() && last.Before(now.Add(-after)) {
				candidates = append(candidates, candidate{id, last})
				if len(candidates) == 200 {
					break
				}
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		for _, c := range candidates {
			changed, err := h.setPeerArchivedTx(ctx, tx, c.id, true, "hub", map[string]any{"auto": true, "idle_seconds": int64(now.Sub(c.last) / time.Second)}, now)
			if err != nil {
				return err
			}
			if changed {
				archived = append(archived, c.id)
			}
		}
		return nil
	})
	h.reachMu.Unlock()
	if err != nil {
		return 0, err
	}
	if len(archived) > 0 {
		h.notifyEnabled(ctx)
		h.Log.Notify(archived...)
	}
	return len(archived), nil
}

// ArchiveAbandonedThreads archives at most 500 qualifying operator-view rows.
func (h *Hub) ArchiveAbandonedThreads(ctx context.Context, now time.Time) (int, error) {
	return h.archiveAbandonedThreads(ctx, now, "")
}

// peerID narrows a manual peer archival's follow-up to that peer's threads.
func (h *Hub) archiveAbandonedThreads(ctx context.Context, now time.Time, peerID string) (int, error) {
	var targets, notify []string
	err := h.DB.Tx(ctx, func(tx *sql.Tx) error {
		snapshot, err := h.Preferences.LoadSnapshot(ctx, tx)
		if err != nil {
			return err
		}
		share := preferenceNumber(snapshot, "threads.auto_archive_share")
		if share == 0 {
			return nil
		}
		rows, err := tx.QueryContext(ctx, `SELECT t.thr_id FROM threads t
   JOIN thread_participants tp ON tp.thr_id=t.thr_id JOIN peers p ON p.peer_id=tp.peer_id AND p.kind!='operator'
   WHERE t.archived_at IS NULL AND (?='' OR EXISTS(SELECT 1 FROM thread_participants mine WHERE mine.thr_id=t.thr_id AND mine.peer_id=?))
   GROUP BY t.thr_id
   HAVING count(*)>0 AND count(p.archived_at)*100>=?*count(*)
   AND (t.unarchived_at IS NULL OR julianday(t.unarchived_at)<max(julianday(p.archived_at)))
   ORDER BY t.thr_id LIMIT 500`, peerID, peerID, share)
		if err != nil {
			return err
		}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			targets = append(targets, id)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		if len(targets) == 0 {
			return nil
		}
		for _, id := range targets {
			if _, err := tx.ExecContext(ctx, `UPDATE threads SET archived_at=?,archived_by='hub' WHERE thr_id=?`, envelope.FormatTime(now), id); err != nil {
				return err
			}
		}
		if err := audit.Record(ctx, tx, "hub", "thread_archive", targets, map[string]any{"auto": true, "reason": "participants_archived"}); err != nil {
			return err
		}
		notify, err = h.noteOperatorView(ctx, tx, "archive", "")
		return err
	})
	if err != nil {
		return 0, err
	}
	h.Log.Notify(notify...)
	return len(targets), nil
}
