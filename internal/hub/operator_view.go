package hub

import (
	"context"
	"database/sql"
	"fmt"
)

// noteOperatorView invalidates operator snapshots in the same transaction as
// their change. Callers wake the returned log owners only after commit.
// An optional except set excludes peers already receiving the underlying event.
// Omitting it preserves the existing callers' empty exclusion set.
func (h *Hub) noteOperatorView(ctx context.Context, tx *sql.Tx, reason string, threadID string, except ...map[string]bool) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT peer_id FROM peers WHERE kind = 'operator' AND disabled = 0 ORDER BY peer_id`)
	if err != nil {
		return nil, fmt.Errorf("hub: operator view peers: %w", err)
	}
	var peers []string
	for rows.Next() {
		var peer string
		if err := rows.Scan(&peer); err != nil {
			rows.Close()
			return nil, err
		}
		if len(except) == 0 || !except[0][peer] {
			peers = append(peers, peer)
		}
	}
	err = rows.Err()
	closeErr := rows.Close()
	if err != nil {
		return nil, err
	}
	if closeErr != nil {
		return nil, closeErr
	}
	data := struct {
		Reason   string `json:"reason"`
		ThreadID any    `json:"thread_id"`
	}{Reason: reason, ThreadID: nullIfEmpty(threadID)}
	now := h.Now()
	for _, peer := range peers {
		if _, err := h.Log.Append(ctx, tx, peer, "operator_view", now, data); err != nil {
			return nil, err
		}
	}
	return peers, nil
}

// noteThreadActivity tells enabled operators that are not participants of
// thr that a message was stored in it (reason "activity"). Participants learn
// of it from their own delivery Event, so they get nothing extra. Called in
// the transaction that inserts the message; replays never reach it.
func (h *Hub) noteThreadActivity(ctx context.Context, tx *sql.Tx, threadID string, participants []string) ([]string, error) {
	except := make(map[string]bool, len(participants))
	for _, p := range participants {
		except[p] = true
	}
	return h.noteOperatorView(ctx, tx, "activity", threadID, except)
}

// NoteOperatorSubscription is the transactional bridge for the subscription
// store (which lives in package events). It returns log owners to notify after
// commit, and cannot publish another reason or thread from that package.
func (h *Hub) NoteOperatorSubscription(ctx context.Context, tx *sql.Tx) ([]string, error) {
	return h.noteOperatorView(ctx, tx, "subscription", "")
}
