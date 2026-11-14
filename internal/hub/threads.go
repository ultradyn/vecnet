package hub

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/XertroV/vecnet/internal/auth"
	"github.com/XertroV/vecnet/internal/envelope"
)

// MaxParticipants caps a thread's participants (PROTOCOL §2.2 step 3, R21).
const MaxParticipants = 64

// threadRow is one row of the threads table.
type threadRow struct {
	ID            string
	Title         *string
	Group         *string
	CreatedBy     string
	MessageCount  int
	Budget        *int // nil = unlimited
	HistoryAccess string
	Locked        bool
	participants  []string        // in join order
	isParticipant map[string]bool // over participants
	archiveNotify []string        // operator log owners to wake after message revival commits
	isNew         bool            // created by this send
}

// loadThread reads thread id and its participants inside tx. A missing
// thread is 404 unknown_thread.
func loadThread(ctx context.Context, tx *sql.Tx, id string) (*threadRow, error) {
	t := &threadRow{ID: id}
	var title, group sql.NullString
	var budget sql.NullInt64
	var locked int
	err := tx.QueryRowContext(ctx,
		`SELECT title, group_id, created_by, message_count, budget_remaining, history_access, locked_participants
		 FROM threads WHERE thr_id = ?`, id).
		Scan(&title, &group, &t.CreatedBy, &t.MessageCount, &budget, &t.HistoryAccess, &locked)
	if errors.Is(err, sql.ErrNoRows) {
		e := E(http.StatusNotFound, "unknown_thread", "no such thread")
		e.Details["thread_id"] = id
		return nil, e
	}
	if err != nil {
		return nil, fmt.Errorf("hub: load thread: %w", err)
	}
	if title.Valid {
		t.Title = &title.String
	}
	if group.Valid {
		t.Group = &group.String
	}
	if budget.Valid {
		b := int(budget.Int64)
		t.Budget = &b
	}
	t.Locked = locked != 0
	if t.participants, err = threadParticipants(ctx, tx, id); err != nil {
		return nil, err
	}
	t.isParticipant = make(map[string]bool, len(t.participants))
	for _, p := range t.participants {
		t.isParticipant[p] = true
	}
	return t, nil
}

// threadParticipants returns the peers of thread id in join order.
func threadParticipants(ctx context.Context, tx *sql.Tx, id string) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT peer_id FROM thread_participants WHERE thr_id = ? ORDER BY rowid`, id)
	if err != nil {
		return nil, fmt.Errorf("hub: thread participants: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, fmt.Errorf("hub: thread participants: %w", err)
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("hub: thread participants: %w", err)
	}
	return out, nil
}

// insertThread creates the row for t (MessageCount 0) inside tx.
func insertThread(ctx context.Context, tx *sql.Tx, t *threadRow, now string) error {
	var budget any
	if t.Budget != nil {
		budget = *t.Budget
	}
	locked := 0
	if t.Locked {
		locked = 1
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO threads(thr_id, title, group_id, created_by, created_at, message_count, budget_remaining,
		                     history_access, locked_participants, last_message_at)
		 VALUES (?, ?, ?, ?, ?, 0, ?, ?, ?, ?)`,
		t.ID, t.Title, t.Group, t.CreatedBy, now, budget, t.HistoryAccess, locked, now); err != nil {
		return fmt.Errorf("hub: insert thread: %w", err)
	}
	return nil
}

// addParticipants inserts peers as participants of thread thr who see
// messages with seq > joinedAfterSeq (PROTOCOL §16.3).
func addParticipants(ctx context.Context, tx *sql.Tx, thr string, peers []string, joinedAfterSeq int, now string) error {
	for _, p := range peers {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO thread_participants(thr_id, peer_id, joined_at, joined_after_seq) VALUES (?, ?, ?, ?)`,
			thr, p, now, joinedAfterSeq); err != nil {
			return fmt.Errorf("hub: add participant: %w", err)
		}
	}
	return nil
}

// summary renders t as the ThreadSummary every Delivery carries.
func (t *threadRow) summary() envelope.ThreadSummary {
	return envelope.ThreadSummary{
		ThreadID:           t.ID,
		Title:              t.Title,
		Participants:       append([]string(nil), t.participants...),
		Group:              t.Group,
		MessageCount:       t.MessageCount,
		BudgetRemaining:    t.Budget,
		HistoryAccess:      t.HistoryAccess,
		LockedParticipants: t.Locked,
	}
}

// loadMessage returns the stored Message msgID and its thread, or found false.
func loadMessage(ctx context.Context, tx *sql.Tx, msgID string) (msg *envelope.Message, thr string, found bool, err error) {
	var body string
	err = tx.QueryRowContext(ctx, `SELECT thr_id, body FROM messages WHERE msg_id = ?`, msgID).Scan(&thr, &body)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, "", false, nil
	}
	if err != nil {
		return nil, "", false, fmt.Errorf("hub: load message: %w", err)
	}
	var m envelope.Message
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		return nil, "", false, fmt.Errorf("hub: decode stored message %s: %w", msgID, err)
	}
	return &m, thr, true, nil
}

// ReplyThread returns the thread of message msgID, for a reply by caller c
// acting as `as` (the vecnet_reply lookup, PROTOCOL §4.3). The sender is
// resolved as Send resolves it (as_required, forbidden). A message that does
// not exist, or whose thread the sender does not participate in, is 404
// not_found, so an outsider learns nothing about it; an operator peer may
// reply in any thread, as it may send into any.
func (h *Hub) ReplyThread(ctx context.Context, c auth.Caller, as, msgID string) (string, error) {
	var thr string
	err := h.DB.Tx(ctx, func(tx *sql.Tx) error {
		snd, err := h.resolveSender(ctx, tx, c, as)
		if err != nil {
			return err
		}
		m, mThr, found, err := loadMessage(ctx, tx, msgID)
		if err != nil {
			return err
		}
		visible := found && snd.kind == "operator"
		if found && !visible {
			if visible, err = isParticipant(ctx, tx, mThr, snd.peer); err != nil {
				return err
			}
		}
		if !visible {
			e := E(http.StatusNotFound, "not_found", "no such message")
			e.Details["message_id"] = msgID
			return e
		}
		if m.Kind == "reaction" {
			e := E(http.StatusUnprocessableEntity, "not_replyable", "reaction messages cannot be replied to")
			e.Details["in_reply_to"] = msgID
			return e
		}
		thr = mThr
		return nil
	})
	return thr, err
}

// latestMessage returns the newest non-reaction loop parent of thread thr,
// or nil. Only its id and loop are populated. Retention may erase its body,
// but cannot reset the implicit loop parent (PROTOCOL §6). Compare both
// retained and erased parents by sequence so neither an older pinned body
// nor an older tombstone can override the latest loop.
func latestMessage(ctx context.Context, tx *sql.Tx, thr string) (*envelope.Message, error) {
	var id, loop string
	err := tx.QueryRowContext(ctx, `SELECT msg_id,loop FROM (
    SELECT msg_id,seq,json_extract(body,'$.loop') AS loop FROM
      (SELECT msg_id,seq,body FROM messages WHERE thr_id=? AND kind!='reaction' ORDER BY seq DESC LIMIT 1)
    UNION ALL
    SELECT msg_id,seq,loop FROM thread_history_loop_parents WHERE thr_id=?
    ) ORDER BY seq DESC LIMIT 1`, thr, thr).Scan(&id, &loop)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("hub: latest message: %w", err)
	}
	m := &envelope.Message{MessageID: id}
	if err := json.Unmarshal([]byte(loop), &m.Loop); err != nil {
		return nil, errors.New("hub: invalid_loop_parent")
	}
	return m, nil
}

// isParticipant reports whether peer participates in thread thr.
func isParticipant(ctx context.Context, tx *sql.Tx, thr, peer string) (bool, error) {
	var one int
	err := tx.QueryRowContext(ctx, `SELECT 1 FROM thread_participants WHERE thr_id = ? AND peer_id = ?`, thr, peer).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("hub: participant check: %w", err)
	}
	return true, nil
}
