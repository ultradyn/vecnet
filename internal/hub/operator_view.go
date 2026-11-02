package hub

import (
	"context"
	"database/sql"
	"fmt"
)

// noteOperatorView invalidates operator snapshots in the same transaction as
// their change. Callers wake the returned log owners only after commit.
func (h *Hub) noteOperatorView(ctx context.Context, tx *sql.Tx, reason string, threadID string) ([]string, error) {
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
		peers = append(peers, peer)
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

// NoteOperatorSubscription is the transactional bridge for the subscription
// store (which lives in package events). It returns log owners to notify after
// commit, and cannot publish another reason or thread from that package.
func (h *Hub) NoteOperatorSubscription(ctx context.Context, tx *sql.Tx) ([]string, error) {
	return h.noteOperatorView(ctx, tx, "subscription", "")
}
