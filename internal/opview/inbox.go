package opview

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"

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
	// One grouped read gives the total and its per-thread and per-sender
	// splits, so they always agree (the web sidebar's per-thread counts sum
	// to the badge; the peer page's per-sender count is what Mark all from
	// it read would clear).
	byThread, err := r.Hub.DB.QueryContext(ctx,
		`SELECT d.thr_id, m.sender, count(*) FROM deliveries d JOIN messages m ON m.msg_id = d.msg_id WHERE `+visible+unread+` GROUP BY d.thr_id, m.sender`, c.PeerID, out.Now)
	if err != nil {
		return out, fmt.Errorf("opview: inbox unread: %w", err)
	}
	defer byThread.Close()
	out.UnreadByThread = map[string]int{}
	out.UnreadBySender = map[string]int{}
	for byThread.Next() {
		var thr, sender string
		var n int
		if err := byThread.Scan(&thr, &sender, &n); err != nil {
			return out, fmt.Errorf("opview: inbox unread: %w", err)
		}
		out.UnreadByThread[thr] += n
		out.UnreadBySender[sender] += n
		out.Unread += n
	}
	if err := byThread.Err(); err != nil {
		return out, fmt.Errorf("opview: inbox unread: %w", err)
	}
	if err := byThread.Close(); err != nil {
		return out, fmt.Errorf("opview: close inbox unread: %w", err)
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
	var messages []envelope.Message
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
		messages = append(messages, m)
		d.Excerpt, d.Attachments, d.SenderInfo = opapi.Excerpt(m), len(m.Attachments), m.SenderInfo
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

	// Cut the projection to the same page as the visible deliveries.
	if len(messages) > len(out.Deliveries) {
		messages = messages[:len(out.Deliveries)]
	}
	out.BlobStates, err = r.Hub.ProjectBlobStates(ctx, c, messages)
	if err != nil {
		return out, err
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

// ReadAllBatch is how many deliveries one MarkAllRead call marks at most;
// each is its own receipt transaction, so a bounded call keeps the single
// writer free for other requests between batches. A variable so tests can
// page with a small one.
var ReadAllBatch = 500

// MarkAllRead marks read the operator's unread deliveries, as the inbox
// counts them (Inbox's visible and unread rules: not superseded, not an
// open approval, which needs an answer, not one the operator answered),
// created at or before q.Until (now when empty), at or below the immutable
// BeforeSeq delivery event high-water (captured when empty), oldest first, at most
// ReadAllBatch per call. With q.Sender, only that peer's messages: thread
// spam from one agent. Each goes through the normal receipt path, so its
// sender sees a read receipt and the sidebar counts follow. An unknown
// sender is 404 not_found; malformed bounds are 422 invalid_value.
func (r *Reader) MarkAllRead(ctx context.Context, c auth.Caller, q opapi.ReadAllQuery) (opapi.ReadAllResult, error) {
	now := envelope.FormatTime(r.Hub.Now())
	out := opapi.ReadAllResult{Until: now}
	if q.Until != "" {
		at, err := envelope.ParseTime(q.Until)
		if err != nil {
			e := hub.E(http.StatusUnprocessableEntity, "invalid_value", "until must be an RFC 3339 time")
			e.Details["field"] = "until"
			return out, e
		}
		out.Until = envelope.FormatTime(at)
	}
	var beforeSeq int64
	if q.BeforeSeq == "" {
		if err := r.Hub.DB.QueryRowContext(ctx, `SELECT COALESCE(MAX(event_seq), 0) FROM deliveries WHERE recipient = ?`, c.PeerID).Scan(&beforeSeq); err != nil {
			return out, fmt.Errorf("opview: read all high-water: %w", err)
		}
	} else {
		var err error
		beforeSeq, err = strconv.ParseInt(q.BeforeSeq, 10, 64)
		valid := err == nil && beforeSeq >= 0
		for _, ch := range q.BeforeSeq {
			valid = valid && ch >= '0' && ch <= '9'
		}
		if !valid {
			e := hub.E(http.StatusUnprocessableEntity, "invalid_value", "before_seq must be a nonnegative decimal integer string")
			e.Details["field"] = "before_seq"
			return out, e
		}
	}
	out.BeforeSeq = strconv.FormatInt(beforeSeq, 10)
	args := []any{c.PeerID, now}
	where := `d.recipient = ? AND d.state != 'superseded' AND d.read_at IS NULL
  AND NOT EXISTS (SELECT 1 FROM approvals a WHERE a.msg_id = d.msg_id AND a.closed_at IS NULL AND a.expires_at > ?)
  AND NOT EXISTS (SELECT 1 FROM approvals a WHERE a.msg_id = d.msg_id AND a.decided_by = d.recipient)
  AND d.created_at <= ? AND d.event_seq <= ?`
	args = append(args, out.Until, beforeSeq)
	if q.Sender != "" {
		var one int
		err := r.Hub.DB.QueryRowContext(ctx, `SELECT 1 FROM peers WHERE peer_id = ?`, q.Sender).Scan(&one)
		if errors.Is(err, sql.ErrNoRows) {
			e := hub.E(http.StatusNotFound, "not_found", "no such peer")
			e.Details["peer_id"] = q.Sender
			return out, e
		}
		if err != nil {
			return out, fmt.Errorf("opview: read all sender: %w", err)
		}
		where += ` AND EXISTS (SELECT 1 FROM messages m WHERE m.msg_id = d.msg_id AND m.sender = ?)`
		args = append(args, q.Sender)
	}
	rows, err := r.Hub.DB.QueryContext(ctx,
		`SELECT d.dly_id FROM deliveries d WHERE `+where+` ORDER BY d.created_at, d.dly_id LIMIT ?`, append(args, ReadAllBatch+1)...)
	if err != nil {
		return out, fmt.Errorf("opview: read all: %w", err)
	}
	// Close the cursor before ReceiptChanged opens its write transaction.
	var unread []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return out, fmt.Errorf("opview: read all: %w", err)
		}
		unread = append(unread, id)
	}
	err = rows.Err()
	closeErr := rows.Close()
	if err != nil {
		return out, fmt.Errorf("opview: read all: %w", err)
	}
	if closeErr != nil {
		return out, fmt.Errorf("opview: close read all: %w", closeErr)
	}
	if len(unread) > ReadAllBatch {
		unread, out.More = unread[:ReadAllBatch], true
	}
	for _, id := range unread {
		if err := ctx.Err(); err != nil {
			return out, err
		}
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
