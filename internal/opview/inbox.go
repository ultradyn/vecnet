package opview

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/XertroV/vecnet/internal/auth"
	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/hub"
	"github.com/XertroV/vecnet/internal/opapi"
)

// Inbox collects the operator's actionable items and delivery history. All
// time-dependent fields use the same sample of the injected hub clock.
func (r *Reader) Inbox(ctx context.Context, c auth.Caller, q opapi.InboxQuery) (opapi.Inbox, error) {
	now := r.Hub.Now()
	out := opapi.Inbox{
		Now:       envelope.FormatTime(now),
		Approvals: []opapi.OpenApproval{}, DeadLetters: []opapi.DeadLetter{},
		Attention: []opapi.Attention{}, Deliveries: []opapi.InboxDelivery{},
	}
	var presence hub.PresenceRow
	out.Me.PeerID = c.PeerID
	if err := r.Hub.DB.QueryRowContext(ctx,
		`SELECT name, kind, presence_state, presence_at FROM peers WHERE peer_id = ?`, c.PeerID).
		Scan(&out.Me.Name, &presence.Kind, &presence.State, &presence.At); err != nil {
		return out, fmt.Errorf("opview: inbox peer: %w", err)
	}
	out.Me.PresenceAt = presence.At
	out.Me.Presence = r.Hub.EffectivePresence(presence, now)

	approvals, err := r.inboxApprovals(ctx, c.PeerID, out.Now)
	if err != nil {
		return out, err
	}
	out.Approvals = approvals

	// The same predicate applies to the page and the total unread count, so
	// actionable approvals appear once and never pad the delivery count.
	const visible = `d.recipient = ? AND d.state != 'superseded' AND NOT EXISTS (
  SELECT 1 FROM approvals a WHERE a.msg_id = d.msg_id
  AND a.closed_at IS NULL AND a.expires_at > ?)`
	// An approval the recipient answered was read, whether or not a read
	// receipt followed: answering must not raise the unread count.
	const unread = ` AND d.read_at IS NULL AND NOT EXISTS (
  SELECT 1 FROM approvals a WHERE a.msg_id = d.msg_id AND a.decided_by = d.recipient)`
	if err := r.Hub.DB.QueryRowContext(ctx,
		`SELECT count(*) FROM deliveries d WHERE `+visible+unread, c.PeerID, out.Now).
		Scan(&out.Unread); err != nil {
		return out, fmt.Errorf("opview: inbox unread: %w", err)
	}
	limit := q.Limit
	if limit <= 0 {
		limit = 50
	}
	limit = min(limit, 200)
	query := `SELECT d.dly_id, d.msg_id, d.thr_id, coalesce(t.title, ''), m.sender,
  m.kind, d.priority, d.state, m.created_at,
  coalesce(d.read_at, (SELECT a.closed_at FROM approvals a WHERE a.msg_id = d.msg_id AND a.decided_by = d.recipient)),
  m.body
  FROM deliveries d JOIN messages m ON m.msg_id = d.msg_id
  JOIN threads t ON t.thr_id = d.thr_id WHERE ` + visible
	args := []any{c.PeerID, out.Now}
	if q.Before != "" {
		query += ` AND d.dly_id < ?`
		args = append(args, q.Before)
	}
	if q.UnreadOnly {
		query += unread
	}
	query += ` ORDER BY d.dly_id DESC LIMIT ?`
	args = append(args, limit+1)
	rows, err := r.Hub.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return out, fmt.Errorf("opview: inbox deliveries: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var d opapi.InboxDelivery
		var raw string
		if err := rows.Scan(&d.DeliveryID, &d.MessageID, &d.ThreadID, &d.ThreadTitle,
			&d.Sender, &d.Kind, &d.Priority, &d.State, &d.CreatedAt, &d.ReadAt, &raw); err != nil {
			return out, fmt.Errorf("opview: inbox delivery: %w", err)
		}
		var m envelope.Message
		if err := json.Unmarshal([]byte(raw), &m); err != nil {
			return out, fmt.Errorf("opview: inbox message: %w", err)
		}
		d.Excerpt, d.Attachments = opapi.Excerpt(m), len(m.Attachments)
		d.Section = "held"
		if d.Priority == "wake" {
			d.Section = "woke"
		}
		out.Deliveries = append(out.Deliveries, d)
	}
	if err := rows.Err(); err != nil {
		return out, fmt.Errorf("opview: inbox deliveries: %w", err)
	}
	if err := rows.Close(); err != nil {
		return out, fmt.Errorf("opview: close inbox deliveries: %w", err)
	}
	if len(out.Deliveries) > limit {
		out.Deliveries = out.Deliveries[:limit]
		last := out.Deliveries[limit-1].DeliveryID
		out.NextBefore = &last
	}

	dead, err := r.Hub.DeadLetters(ctx)
	if err != nil {
		return out, err
	}
	for _, d := range dead {
		enriched := opapi.DeadLetter{
			DeliveryID: d.DeliveryID, Recipient: d.Recipient, ThreadID: d.ThreadID,
			UpdatedAt: d.UpdatedAt, Attempt: d.Attempt, LastStatus: d.LastStatus, LastError: d.LastError,
			Class: "terminal",
		}
		var raw string
		if err := r.Hub.DB.QueryRowContext(ctx,
			`SELECT m.msg_id, m.sender, m.body FROM deliveries d
    JOIN messages m ON m.msg_id = d.msg_id WHERE d.dly_id = ?`, d.DeliveryID).
			Scan(&enriched.MessageID, &enriched.Sender, &raw); err != nil {
			return out, fmt.Errorf("opview: inbox dead letter: %w", err)
		}
		var m envelope.Message
		if err := json.Unmarshal([]byte(raw), &m); err != nil {
			return out, fmt.Errorf("opview: dead letter message: %w", err)
		}
		enriched.Excerpt = opapi.Excerpt(m)
		if d.LastStatus == nil {
			enriched.Class = "network"
		} else if status := *d.LastStatus; status == 408 || status == 429 || status >= 500 && status < 600 {
			enriched.Class = "retries_exhausted"
		}
		out.DeadLetters = append(out.DeadLetters, enriched)
	}
	attention, err := r.Attention(ctx)
	if err != nil {
		return out, err
	}
	if attention != nil {
		out.Attention = attention
	}
	return out, nil
}

func (r *Reader) inboxApprovals(ctx context.Context, peer, now string) ([]opapi.OpenApproval, error) {
	rows, err := r.Hub.DB.QueryContext(ctx,
		`SELECT a.apr_id, a.thr_id, coalesce(t.title, ''), a.msg_id, a.requester, a.expires_at, a.options, m.body
   FROM approvals a JOIN messages m ON m.msg_id = a.msg_id
   JOIN threads t ON t.thr_id = a.thr_id
   WHERE a.closed_at IS NULL AND a.expires_at > ? AND EXISTS (
    SELECT 1 FROM deliveries d WHERE d.msg_id = a.msg_id AND d.recipient = ?)
   ORDER BY a.expires_at, a.apr_id`, now, peer)
	if err != nil {
		return nil, fmt.Errorf("opview: inbox approvals: %w", err)
	}
	defer rows.Close()
	out := []opapi.OpenApproval{}
	for rows.Next() {
		var a opapi.OpenApproval
		var opts, raw string
		if err := rows.Scan(&a.ApprovalID, &a.ThreadID, &a.ThreadTitle, &a.MessageID,
			&a.Requester, &a.ExpiresAt, &opts, &raw); err != nil {
			return nil, fmt.Errorf("opview: inbox approval: %w", err)
		}
		var m envelope.Message
		if err := json.Unmarshal([]byte(raw), &m); err != nil {
			return nil, fmt.Errorf("opview: approval message: %w", err)
		}
		if err := json.Unmarshal([]byte(opts), &a.Options); err != nil {
			return nil, fmt.Errorf("opview: approval options: %w", err)
		}
		a.Text = m.Content.Text
		if m.ApprovalRequest != nil {
			a.Question = m.ApprovalRequest.Question
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("opview: inbox approvals: %w", err)
	}
	return out, nil
}

// MarkThreadRead uses the normal receipt path so the sender sees read events
// and repeated calls share the hub's idempotency with individual read receipts.
func (r *Reader) MarkThreadRead(ctx context.Context, c auth.Caller, thrID string) (opapi.ReadResult, error) {
	out := opapi.ReadResult{}
	var exists int
	err := r.Hub.DB.QueryRowContext(ctx, `SELECT 1 FROM threads WHERE thr_id = ?`, thrID).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		e := hub.E(http.StatusNotFound, "unknown_thread", "no such thread")
		e.Details["thread_id"] = thrID
		return out, e
	}
	if err != nil {
		return out, fmt.Errorf("opview: read thread: %w", err)
	}
	rows, err := r.Hub.DB.QueryContext(ctx,
		`SELECT dly_id FROM deliveries WHERE thr_id = ? AND recipient = ? AND read_at IS NULL AND state != 'superseded' ORDER BY dly_id`, thrID, c.PeerID)
	if err != nil {
		return out, fmt.Errorf("opview: unread thread deliveries: %w", err)
	}
	// Close the cursor before ReceiptChanged opens its write transaction.
	var unread []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return out, fmt.Errorf("opview: unread delivery: %w", err)
		}
		unread = append(unread, id)
	}
	err = rows.Err()
	closeErr := rows.Close()
	if err != nil {
		return out, fmt.Errorf("opview: unread thread deliveries: %w", err)
	}
	if closeErr != nil {
		return out, fmt.Errorf("opview: close unread deliveries: %w", closeErr)
	}
	for _, id := range unread {
		changed, err := r.Hub.ReceiptChanged(ctx, c, id, "read", "")
		if err != nil {
			return out, err
		}
		if changed {
			out.Marked++
		}
	}
	return out, nil
}
