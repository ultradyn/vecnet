package hub

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"time"

	"github.com/XertroV/vecnet/internal/audit"
	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/ids"
)

// HubSender is Message.sender of hub-authored messages ($defs/actorId).
const HubSender = "hub"

// ApprovalExpired is the decision the hub records, and posts, when an
// approval reaches its expires_at unanswered (PROTOCOL §9, R23).
const ApprovalExpired = "expired"

// defaultApprovalOptions are the options of an approval_request that names
// none ($defs/approvalRequest default).
var defaultApprovalOptions = []string{"yes", "no"}

// prepareApprovalRequest checks an approval_request and returns the effect
// that records it as an open approval of thr, asked by the new Message
// (PROTOCOL §9 step 1). The sender chooses approval_id; one already in use
// is 422 invalid_value.
func prepareApprovalRequest(ctx context.Context, tx *sql.Tx, snd sender, thr *threadRow, ar *envelope.ApprovalRequest) (*kindEffect, error) {
	if _, err := envelope.ParseTime(ar.ExpiresAt); err != nil {
		e := E(http.StatusUnprocessableEntity, "invalid_value", "approval_request.expires_at is not a timestamp")
		e.Details["field"] = "approval_request.expires_at"
		return nil, e
	}
	var one int
	err := tx.QueryRowContext(ctx, `SELECT 1 FROM approvals WHERE apr_id = ?`, ar.ApprovalID).Scan(&one)
	if err == nil {
		e := E(http.StatusUnprocessableEntity, "invalid_value", "approval_id is already in use")
		e.Details["field"] = "approval_request.approval_id"
		e.Details["approval_id"] = ar.ApprovalID
		return nil, e
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("hub: find approval: %w", err)
	}
	options := ar.Options
	if len(options) == 0 {
		options = defaultApprovalOptions
	}
	opts, err := json.Marshal(options)
	if err != nil {
		return nil, fmt.Errorf("hub: encode options: %w", err)
	}
	return &kindEffect{approvalID: ar.ApprovalID, apply: func(ctx context.Context, tx *sql.Tx, msgID, _ string) error {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO approvals(apr_id, thr_id, msg_id, requester, options, expires_at) VALUES (?, ?, ?, ?, ?, ?)`,
			ar.ApprovalID, thr.ID, msgID, snd.peer, string(opts), ar.ExpiresAt); err != nil {
			return fmt.Errorf("hub: insert approval: %w", err)
		}
		return nil
	}}, nil
}

// prepareApprovalResponse checks an approval_response (PROTOCOL §9 step 2,
// §15): the approval belongs to thr (404 unknown_approval); the sender was
// addressed by the request, i.e. received a Delivery of it (403
// not_addressed); the approval is open: neither answered nor past its
// expires_at (422 approval_closed); the decision is one of its options
// (422 invalid_option). The effect closes the approval with this decision.
func prepareApprovalResponse(ctx context.Context, tx *sql.Tx, snd sender, thr *threadRow, ar *envelope.ApprovalResponse, now time.Time) (*kindEffect, error) {
	unknown := func() error {
		e := E(http.StatusNotFound, "unknown_approval", "no approval with this id in this thread")
		e.Details["approval_id"] = ar.ApprovalID
		return e
	}
	if thr.isNew {
		return nil, unknown()
	}
	var reqMsg, optsJSON, expires string
	var closed sql.NullString
	err := tx.QueryRowContext(ctx,
		`SELECT msg_id, options, expires_at, closed_at FROM approvals WHERE apr_id = ? AND thr_id = ?`,
		ar.ApprovalID, thr.ID).Scan(&reqMsg, &optsJSON, &expires, &closed)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, unknown()
	}
	if err != nil {
		return nil, fmt.Errorf("hub: load approval: %w", err)
	}
	var addressed int
	if err := tx.QueryRowContext(ctx,
		`SELECT count(*) FROM deliveries WHERE msg_id = ? AND recipient = ?`, reqMsg, snd.peer).Scan(&addressed); err != nil {
		return nil, fmt.Errorf("hub: approval addressee: %w", err)
	}
	if addressed == 0 {
		e := E(http.StatusForbidden, "not_addressed", "the approval_request did not address this sender")
		e.Details["approval_id"] = ar.ApprovalID
		return nil, e
	}
	exp, perr := envelope.ParseTime(expires)
	if closed.Valid || perr != nil || !now.Before(exp) {
		e := E(http.StatusUnprocessableEntity, "approval_closed", "the approval is already answered or expired")
		e.Details["approval_id"] = ar.ApprovalID
		return nil, e
	}
	var options []string
	if err := json.Unmarshal([]byte(optsJSON), &options); err != nil {
		return nil, fmt.Errorf("hub: decode approval options: %w", err)
	}
	if !slices.Contains(options, ar.Decision) {
		e := E(http.StatusUnprocessableEntity, "invalid_option", "decision is not one of the approval's options")
		e.Details["approval_id"] = ar.ApprovalID
		e.Details["options"] = options
		return nil, e
	}
	return &kindEffect{approvalID: ar.ApprovalID, apply: func(ctx context.Context, tx *sql.Tx, _ string, now string) error {
		res, err := tx.ExecContext(ctx,
			`UPDATE approvals SET closed_at = ?, decision = ?, decided_by = ? WHERE apr_id = ? AND closed_at IS NULL`,
			now, ar.Decision, snd.peer, ar.ApprovalID)
		if err != nil {
			return fmt.Errorf("hub: close approval: %w", err)
		}
		if n, err := res.RowsAffected(); err != nil || n != 1 {
			return fmt.Errorf("hub: close approval %s: %d rows, %v", ar.ApprovalID, n, err)
		}
		return nil
	}}, nil
}

// ExpireApprovals closes every open approval whose expires_at is at or
// before now with decision "expired" (PROTOCOL §9 step 3, R23). For each it
// appends an approval_expired Event {approval_id, thread_id} to every
// participant of the thread, then posts a hub-authored Message of kind
// approval_response {approval_id, decision: "expired"} into the thread:
// sender "hub", ingress "hub", priority hold, in reply to the request,
// addressed to the thread's participants. That Message counts against
// neither the thread budget nor depth (§6, R20): its loop keeps the
// request's depth, max_depth, sender_chain ("hub" is not an originator
// in the chain) and expires_at, which is never raised; only its
// deliveries get a fresh expiry. It returns how many approvals expired.
func (h *Hub) ExpireApprovals(ctx context.Context, now time.Time) (n int, err error) {
	nowS := envelope.FormatTime(now)
	var notify []string
	var pub []ActivityEvent
	err = h.DB.Tx(ctx, func(tx *sql.Tx) error {
		n, notify, pub = 0, nil, nil
		rows, err := tx.QueryContext(ctx,
			`SELECT apr_id, thr_id, msg_id FROM approvals WHERE closed_at IS NULL AND expires_at <= ?
			 ORDER BY expires_at, apr_id`, nowS)
		if err != nil {
			return fmt.Errorf("hub: expired approvals: %w", err)
		}
		type row struct{ apr, thr, msg string }
		var due []row
		for rows.Next() {
			var r row
			if err := rows.Scan(&r.apr, &r.thr, &r.msg); err != nil {
				rows.Close()
				return fmt.Errorf("hub: expired approvals: %w", err)
			}
			due = append(due, r)
		}
		if err := rows.Close(); err != nil {
			return fmt.Errorf("hub: expired approvals: %w", err)
		}
		if err := rows.Err(); err != nil {
			return fmt.Errorf("hub: expired approvals: %w", err)
		}
		for _, r := range due {
			woke, ev, err := h.expireApproval(ctx, tx, r.apr, r.thr, r.msg, now)
			if err != nil {
				return err
			}
			notify = append(notify, woke...)
			if ev != nil {
				pub = append(pub, *ev)
			}
			n++
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	if len(notify) > 0 {
		h.Log.Notify(notify...)
	}
	h.Activity.Publish(pub...)
	return n, nil
}

// expireApproval closes approval apr of thread thrID, asked by message
// reqMsg, and posts the expiry Event and hub Message inside tx. It returns
// the peers whose logs it appended to, and the notice's activity feed entry
// (nil when no notice was posted) for the caller to publish after commit.
func (h *Hub) expireApproval(ctx context.Context, tx *sql.Tx, apr, thrID, reqMsg string, nowT time.Time) ([]string, *ActivityEvent, error) {
	now := envelope.FormatTime(nowT)
	if _, err := tx.ExecContext(ctx,
		`UPDATE approvals SET closed_at = ?, decision = ?, decided_by = ? WHERE apr_id = ? AND closed_at IS NULL`,
		now, ApprovalExpired, HubSender, apr); err != nil {
		return nil, nil, fmt.Errorf("hub: expire approval: %w", err)
	}
	thr, err := loadThread(ctx, tx, thrID)
	if err != nil {
		return nil, nil, err
	}
	for _, p := range thr.participants {
		if _, err := h.Log.Append(ctx, tx, p, "approval_expired", nowT,
			map[string]string{"approval_id": apr, "thread_id": thrID}); err != nil {
			return nil, nil, err
		}
	}
	if len(thr.participants) == 0 {
		return nil, nil, audit.Record(ctx, tx, HubSender, "approval_expired", []string{apr, thrID}, nil)
	}

	parent, _, found, err := loadMessage(ctx, tx, reqMsg)
	if err != nil {
		return nil, nil, err
	}
	if !found {
		return nil, nil, fmt.Errorf("hub: approval %s: request message %s is missing", apr, reqMsg)
	}
	// Not a reply in the §6 sense: depth, max_depth and sender_chain stay the
	// request's. loop.expires_at stays the request's too, even when already
	// past, so the notice never raises the thread's inherited TTL (§6). Its
	// deliveries instead expire at max(loop.expires_at, now + DefaultTTL), so
	// the notice itself is still deliverable.
	loop := envelope.Loop{
		Depth:       parent.Loop.Depth,
		MaxDepth:    parent.Loop.MaxDepth,
		ExpiresAt:   parent.Loop.ExpiresAt,
		SenderChain: append([]string{}, parent.Loop.SenderChain...),
	}
	dlyExpires := envelope.FormatTime(nowT.Add(h.Cfg.DefaultTTL))
	if pExp, err := envelope.ParseTime(parent.Loop.ExpiresAt); err == nil && pExp.After(nowT.Add(h.Cfg.DefaultTTL)) {
		dlyExpires = parent.Loop.ExpiresAt
	}
	recipients := thr.participants
	if len(recipients) > maxMessageRecipients {
		// $defs/message caps recipients at 32; every participant still gets
		// a Delivery.
		recipients = recipients[:maxMessageRecipients]
	}
	inReplyTo := parent.MessageID
	msg := envelope.Message{
		SchemaVersion:    envelope.SchemaVersion,
		MessageID:        ids.New(ids.Msg),
		ThreadID:         thrID,
		InReplyTo:        &inReplyTo,
		Kind:             "approval_response",
		Sender:           HubSender,
		SenderInfo:       &envelope.SenderInfo{Name: HubSender, Kind: HubSender},
		Recipients:       append([]string{}, recipients...),
		Priority:         "hold",
		CreatedAt:        now,
		Content:          envelope.Content{Text: fmt.Sprintf("Approval %s expired without a decision.", apr)},
		Loop:             loop,
		ApprovalResponse: &envelope.ApprovalResponse{ApprovalID: apr, Decision: ApprovalExpired},
	}
	msgJSON, err := json.Marshal(msg)
	if err != nil {
		return nil, nil, fmt.Errorf("hub: encode expiry message: %w", err)
	}
	if err := h.insertMessage(ctx, tx, thr, &msg, msgJSON, "", apr, false); err != nil {
		return nil, nil, err
	}
	targets := make([]target, len(thr.participants))
	for i, p := range thr.participants {
		targets[i] = target{peer: p}
	}
	if _, _, err := h.fanOut(ctx, tx, thr, &msg, msgJSON, targets, nil, "hub", dlyExpires, nowT); err != nil {
		return nil, nil, err
	}
	if err := audit.Record(ctx, tx, HubSender, "approval_expired", []string{apr, thrID, msg.MessageID}, nil); err != nil {
		return nil, nil, err
	}
	ev := messageActivity(thrID, msg.MessageID, msg.Sender, msg.Kind, msg.CreatedAt)
	return append([]string{}, thr.participants...), &ev, nil
}
