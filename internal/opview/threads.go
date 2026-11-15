package opview

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/XertroV/vecnet/internal/auth"
	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/hub"
	"github.com/XertroV/vecnet/internal/ids"
	"github.com/XertroV/vecnet/internal/opapi"
)

func threadPageLimit(limit int) int {
	if limit <= 0 {
		return hub.DefaultPageLimit
	}
	return min(limit, hub.MaxPageLimit)
}

func threadInvalid(field, message string) *hub.APIError {
	e := hub.E(422, "invalid_value", message)
	e.Details["field"] = field
	return e
}

// Threads is the operator's view of every thread, including those the
// operator has never joined. Its cursor uses the same ordering and encoding
// as Hub.ListThreads: last_message_at and thread id, descending.
// ExcludeParticipant filters in the same query, before the page is cut.
func (r *Reader) Threads(ctx context.Context, q opapi.ThreadQuery) (opapi.ThreadPage, error) {
	out := opapi.ThreadPage{Threads: []opapi.ThreadRow{}}
	if q.Participant != "" && !ids.Is(ids.Peer, q.Participant) {
		return out, threadInvalid("participant", "participant must be a peer_ id")
	}
	if q.ExcludeParticipant != "" && !ids.Is(ids.Peer, q.ExcludeParticipant) {
		return out, threadInvalid("exclude_participant", "exclude_participant must be a peer_ id")
	}
	if utf8.RuneCountInString(q.Q) > 128 {
		return out, threadInvalid("q", "q must be at most 128 runes")
	}
	var at, id string
	if q.Cursor != "" {
		raw, err := base64.RawURLEncoding.DecodeString(q.Cursor)
		var ok bool
		at, id, ok = strings.Cut(string(raw), " ")
		if err != nil || !ok || !ids.Is(ids.Thr, id) {
			return out, threadInvalid("cursor", "cursor is not a paging token from this endpoint")
		}
		if _, err = envelope.ParseTime(at); err != nil {
			return out, threadInvalid("cursor", "cursor is not a paging token from this endpoint")
		}
	}
	limit := threadPageLimit(q.Limit)
	now := r.Hub.Now()
	err := r.Hub.DB.Tx(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT t.thr_id FROM threads t
   WHERE (?1='' OR EXISTS(SELECT 1 FROM thread_participants p WHERE p.thr_id=t.thr_id AND p.peer_id=?1))
   AND (?5='' OR NOT EXISTS(SELECT 1 FROM thread_participants x WHERE x.thr_id=t.thr_id AND x.peer_id=?5))
   AND (?2='' OR t.last_message_at<?2 OR (t.last_message_at=?2 AND t.thr_id<?3))
   AND (?6 IS NULL OR (t.archived_at IS NOT NULL)=?6)
   AND (?7='' OR instr(lower(t.title),lower(?7))>0)
   ORDER BY t.last_message_at DESC,t.thr_id DESC LIMIT ?4`, q.Participant, at, id, limit+1, q.ExcludeParticipant, q.Archived, q.Q)
		if err != nil {
			return err
		}
		var keys []string
		for rows.Next() {
			var key string
			if err := rows.Scan(&key); err != nil {
				rows.Close()
				return err
			}
			keys = append(keys, key)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		more := len(keys) > limit
		if more {
			keys = keys[:limit]
		}
		for _, key := range keys {
			row, err := readThreadRow(ctx, tx, key, now)
			if err != nil {
				return err
			}
			out.Threads = append(out.Threads, row)
		}
		if more {
			last := out.Threads[len(out.Threads)-1]
			next := base64.RawURLEncoding.EncodeToString([]byte(last.LastMessageAt + " " + last.Thread.ThreadID))
			out.Next = &next
		}
		return nil
	})
	if err != nil {
		return opapi.ThreadPage{}, fmt.Errorf("opview: threads: %w", err)
	}
	return out, nil
}

// readThreadRow runs inside the same transaction as the rest of a snapshot.
func readThreadRow(ctx context.Context, tx *sql.Tx, id string, now time.Time) (opapi.ThreadRow, error) {
	var out opapi.ThreadRow
	out.Thread.ThreadID = id
	out.Thread.Participants = []string{}
	err := tx.QueryRowContext(ctx, `SELECT title,group_id,created_by,created_at,last_message_at,
  message_count,budget_remaining,history_access,locked_participants,archived_at,archived_by FROM threads WHERE thr_id=?`, id).
		Scan(&out.Thread.Title, &out.Thread.Group, &out.CreatedBy, &out.CreatedAt, &out.LastMessageAt,
			&out.Thread.MessageCount, &out.Thread.BudgetRemaining, &out.Thread.HistoryAccess, &out.Thread.LockedParticipants, &out.ArchivedAt, &out.ArchivedBy)
	if errors.Is(err, sql.ErrNoRows) {
		e := hub.E(404, "unknown_thread", "no such thread")
		e.Details["thread_id"] = id
		return out, e
	}
	if err != nil {
		return out, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT peer_id FROM thread_participants WHERE thr_id=? ORDER BY rowid`, id)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var peer string
		if err := rows.Scan(&peer); err != nil {
			rows.Close()
			return out, err
		}
		out.Thread.Participants = append(out.Thread.Participants, peer)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return out, err
	}
	var cut opapi.CutInfo
	err = tx.QueryRowContext(ctx, `SELECT json_extract(a.detail,'$.reason'),a.at FROM audit a
  WHERE a.action='loop_cut' AND a.at>=?
  AND EXISTS(SELECT 1 FROM json_each(a.target_ids) target WHERE target.value=?)
  ORDER BY a.at DESC,a.id DESC LIMIT 1`, envelope.FormatTime(now.Add(-24*time.Hour)), id).Scan(&cut.Reason, &cut.At)
	switch {
	case err == nil:
		out.Cut = &cut
	case !errors.Is(err, sql.ErrNoRows):
		return out, err
	case out.Thread.BudgetRemaining != nil && *out.Thread.BudgetRemaining == 0:
		out.Cut = &opapi.CutInfo{Reason: "thread_budget_exhausted", At: out.LastMessageAt}
	}
	return out, nil
}

// Thread includes every attachment, even externally hosted and purged ones.
// Expiry and addressed_to_me use the hub clock and the caller's delivery,
// independent of whether the operator is a thread participant.
func (r *Reader) Thread(ctx context.Context, c auth.Caller, thrID string) (opapi.ThreadDetail, error) {
	now := r.Hub.Now()
	out := opapi.ThreadDetail{Now: envelope.FormatTime(now), Files: []opapi.ThreadFile{}, OpenApprovals: []opapi.ThreadApproval{}}
	err := r.Hub.DB.Tx(ctx, func(tx *sql.Tx) error {
		var err error
		if out.Row, err = readThreadRow(ctx, tx, thrID, now); err != nil {
			return err
		}
		visibility, err := r.Hub.HistoryVisibilitySQL(ctx, tx)
		if err != nil {
			return err
		}
		rows, err := tx.QueryContext(ctx, `SELECT json_extract(a.value,'$.sha256'),coalesce(json_extract(a.value,'$.blob_id'),''),
   coalesce(json_extract(a.value,'$.name'),''),json_extract(a.value,'$.media_type'),
   m.msg_id,m.sender,m.created_at,json_extract(a.value,'$.size'),
   coalesce(json_extract(m.body,'$.metadata."vecnet.spilled"')=1,0),
   b.purged_at IS NOT NULL
   FROM messages m JOIN json_each(m.body,'$.attachments') a
   LEFT JOIN blobs b ON b.sha256=json_extract(a.value,'$.sha256')
   WHERE m.thr_id=? AND `+visibility+` ORDER BY m.seq DESC,CAST(a.key AS INTEGER)`, thrID)
		if err != nil {
			return err
		}
		for rows.Next() {
			var f opapi.ThreadFile
			if err := rows.Scan(&f.SHA256, &f.BlobID, &f.Name, &f.MediaType, &f.MessageID, &f.Sender, &f.CreatedAt, &f.Size, &f.Spilled, &f.Purged); err != nil {
				rows.Close()
				return err
			}
			out.Files = append(out.Files, f)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		rows, err = tx.QueryContext(ctx, `SELECT a.apr_id,a.msg_id,a.expires_at,a.options,
   EXISTS(SELECT 1 FROM deliveries d WHERE d.msg_id=a.msg_id AND d.recipient=?)
   FROM approvals a JOIN messages m ON m.msg_id=a.msg_id
   WHERE a.thr_id=? AND a.closed_at IS NULL AND a.expires_at>?
   ORDER BY a.expires_at,m.seq`, c.PeerID, thrID, out.Now)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var a opapi.ThreadApproval
			var options string
			if err := rows.Scan(&a.ApprovalID, &a.MessageID, &a.ExpiresAt, &options, &a.AddressedToMe); err != nil {
				return err
			}
			if err := json.Unmarshal([]byte(options), &a.Options); err != nil {
				return err
			}
			if a.Options == nil {
				a.Options = []string{}
			}
			out.OpenApprovals = append(out.OpenApprovals, a)
		}
		return rows.Err()
	})
	if err != nil {
		return opapi.ThreadDetail{}, fmt.Errorf("opview: thread: %w", err)
	}
	return out, nil
}

// Matrix pages messages, never deliveries. Recipient columns are the union
// across the entire thread, in participant join order, so paging cannot move
// columns or truncate a message with more recipients than the page limit.
func (r *Reader) Matrix(ctx context.Context, thrID, after string, limit int) (opapi.Matrix, error) {
	out := opapi.Matrix{Recipients: []string{}, Rows: []opapi.MatrixRow{}}
	if after != "" && !ids.Is(ids.Msg, after) {
		return out, threadInvalid("after", "after must be a msg_ id")
	}
	limit = threadPageLimit(limit)
	err := r.Hub.DB.Tx(ctx, func(tx *sql.Tx) error {
		var exists int
		err := tx.QueryRowContext(ctx, `SELECT 1 FROM threads WHERE thr_id=?`, thrID).Scan(&exists)
		if errors.Is(err, sql.ErrNoRows) {
			e := hub.E(404, "unknown_thread", "no such thread")
			e.Details["thread_id"] = thrID
			return e
		}
		if err != nil {
			return err
		}
		from := 0
		if after != "" {
			err := tx.QueryRowContext(ctx, `SELECT seq FROM messages WHERE thr_id=?1 AND msg_id=?2 UNION ALL SELECT seq FROM message_history_locators WHERE thr_id=?1 AND msg_id=?2 LIMIT 1`, thrID, after).Scan(&from)
			if errors.Is(err, sql.ErrNoRows) {
				e := hub.E(422, "not_in_thread", "after is not a message of this thread")
				e.Details["after"] = after
				return e
			}
			if err != nil {
				return err
			}
		}
		rows, err := tx.QueryContext(ctx, `SELECT p.peer_id FROM thread_participants p WHERE p.thr_id=?
   AND EXISTS(SELECT 1 FROM deliveries d WHERE d.thr_id=p.thr_id AND d.recipient=p.peer_id AND d.msg_id IN (SELECT msg_id FROM messages WHERE thr_id=p.thr_id AND kind != 'reaction'))
   ORDER BY p.rowid`, thrID)
		if err != nil {
			return err
		}
		for rows.Next() {
			var peer string
			if err := rows.Scan(&peer); err != nil {
				rows.Close()
				return err
			}
			out.Recipients = append(out.Recipients, peer)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		visibility, err := r.Hub.HistoryVisibilitySQL(ctx, tx)
		if err != nil {
			return err
		}
		rows, err = tx.QueryContext(ctx, `SELECT m.msg_id,m.created_at,m.sender,m.body FROM messages m
   WHERE m.thr_id=? AND m.seq>? AND m.kind != 'reaction' AND `+visibility+` ORDER BY m.seq LIMIT ?`, thrID, from, limit+1)
		if err != nil {
			return err
		}
		for rows.Next() {
			var row opapi.MatrixRow
			var body string
			if err := rows.Scan(&row.MessageID, &row.CreatedAt, &row.Sender, &body); err != nil {
				rows.Close()
				return err
			}
			var message envelope.Message
			if err := json.Unmarshal([]byte(body), &message); err != nil {
				rows.Close()
				return err
			}
			row.Excerpt = opapi.Excerpt(message)
			row.Cells = []opapi.MatrixCell{}
			out.Rows = append(out.Rows, row)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		if len(out.Rows) > limit {
			out.Rows = out.Rows[:limit]
			last := out.Rows[limit-1].MessageID
			out.NextAfter = &last
		}
		for i := range out.Rows {
			row := &out.Rows[i]
			cells, err := tx.QueryContext(ctx, `SELECT d.recipient,d.dly_id,d.state,d.updated_at,
    d.attempt,d.last_status,d.read_at FROM deliveries d
    LEFT JOIN thread_participants p ON p.thr_id=d.thr_id AND p.peer_id=d.recipient
    WHERE d.msg_id=? ORDER BY p.rowid,d.rowid`, row.MessageID)
			if err != nil {
				return err
			}
			for cells.Next() {
				var cell opapi.MatrixCell
				if err := cells.Scan(&cell.Recipient, &cell.DeliveryID, &cell.State, &cell.UpdatedAt, &cell.Attempt, &cell.LastStatus, &cell.ReadAt); err != nil {
					cells.Close()
					return err
				}
				cell.Display = opapi.DisplayState(cell.State, cell.ReadAt)
				row.Cells = append(row.Cells, cell)
			}
			cells.Close()
			if err := cells.Err(); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return opapi.Matrix{}, fmt.Errorf("opview: matrix: %w", err)
	}
	return out, nil
}
