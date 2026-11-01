package hub

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"time"

	"github.com/XertroV/vecnet/internal/audit"
	"github.com/XertroV/vecnet/internal/envelope"
)

// SenderChainCap is how many originators loop.sender_chain keeps (PROTOCOL §6).
const SenderChainCap = 32

// Counter names (the counters table) for loop cuts and rate-limit
// rejections; each is also the audit action of the rejection (PROTOCOL §6,
// §16.2).
const (
	CounterLoopCut     = "loop_cut"
	CounterRateLimited = "rate_limited"
)

// cutError is a send rejection whose bookkeeping (counter, audit row,
// thread_updated events) must commit although the send itself does not.
// Send commits the transaction and returns the wrapped *APIError.
type cutError struct{ api *APIError }

func (c *cutError) Error() string { return c.api.Error() }
func (c *cutError) Unwrap() error { return c.api }

// buildLoop fills Message.loop for a send by senderID (of peer kind
// senderKind) replying to parent (nil for a fresh message) at now
// (PROTOCOL §6, §16.1):
//
//   - depth: 0 fresh; parent.depth+1 for a reply; 0 when the sender is the
//     operator, which resets the subtree.
//   - max_depth: req.MaxDepth, else the parent's, else Cfg.DefaultMaxDepth;
//     a reply never raises the parent's value (clamped down).
//   - expires_at: req.ExpiresAt, else min(parent's, now+DefaultTTL) for a
//     reply, now+DefaultTTL fresh; a reply never raises the parent's value.
//     An operator reply resets the TTL like depth: it is not bound by the
//     parent's expires_at, passed or not, and its subtree inherits the
//     operator's.
//   - sender_chain: the parent's chain + senderID, last 32 kept (the
//     operator's included).
//
// The parent is the in_reply_to message, or the thread's latest message
// when a send into an existing thread names none (PROTOCOL §6).
//
// It returns 422 expired when the parent's expires_at has passed and the
// sender is not the operator (or the resulting expires_at is not in the
// future), and 422 max_depth_exceeded when depth would exceed max_depth.
func (h *Hub) buildLoop(parent *envelope.Message, senderID, senderKind string, req *envelope.SendRequest, now time.Time) (envelope.Loop, *APIError) {
	ttl := now.Add(h.Cfg.DefaultTTL)
	expires := ttl
	l := envelope.Loop{MaxDepth: h.Cfg.DefaultMaxDepth}
	operator := senderKind == "operator"
	if parent != nil {
		pExp, err := envelope.ParseTime(parent.Loop.ExpiresAt)
		if err != nil {
			pExp = now // an unreadable stored expiry is treated as passed
		}
		if !operator && !pExp.After(now) {
			e := E(http.StatusUnprocessableEntity, "expired", "the parent message's expires_at has passed")
			e.Details["expires_at"] = parent.Loop.ExpiresAt
			e.Details["in_reply_to"] = parent.MessageID
			return envelope.Loop{}, e
		}
		l.Depth = parent.Loop.Depth + 1
		l.MaxDepth = parent.Loop.MaxDepth
		if !operator && pExp.Before(expires) {
			expires = pExp
		}
		l.SenderChain = append(l.SenderChain, parent.Loop.SenderChain...)
	}
	if operator {
		l.Depth = 0 // §16.1: operator replies reset depth for their subtree
	}
	if req.MaxDepth != nil && (parent == nil || *req.MaxDepth < l.MaxDepth) {
		l.MaxDepth = *req.MaxDepth
	}
	if req.ExpiresAt != "" {
		asked, err := envelope.ParseTime(req.ExpiresAt)
		if err != nil {
			e := E(http.StatusUnprocessableEntity, "invalid_value", "expires_at is not a timestamp")
			e.Details["field"] = "expires_at"
			return envelope.Loop{}, e
		}
		if parent == nil || operator || asked.Before(expires) {
			expires = asked
		}
	}
	if !expires.After(now) {
		e := E(http.StatusUnprocessableEntity, "expired", "expires_at has passed")
		e.Details["expires_at"] = envelope.FormatTime(expires)
		return envelope.Loop{}, e
	}
	l.ExpiresAt = envelope.FormatTime(expires)
	l.SenderChain = append(l.SenderChain, senderID)
	if n := len(l.SenderChain); n > SenderChainCap {
		l.SenderChain = append([]string(nil), l.SenderChain[n-SenderChainCap:]...)
	}
	if l.Depth > l.MaxDepth {
		e := E(http.StatusUnprocessableEntity, "max_depth_exceeded", "the reply's depth would exceed max_depth")
		e.Details["depth"] = l.Depth
		e.Details["max_depth"] = l.MaxDepth
		return envelope.Loop{}, e
	}
	return l, nil
}

// checkBudget returns 422 thread_budget_exhausted when thr's
// budget_remaining is 0 (nil means unlimited), after appending a
// thread_updated Event carrying thr's ThreadSummary to every participant
// inside tx (PROTOCOL §6). Hub-authored messages (R20) do not pass through
// the send pipeline and so skip both this check and the decrement.
func (h *Hub) checkBudget(ctx context.Context, tx *sql.Tx, thr *threadRow, now time.Time) (*APIError, error) {
	if thr.isNew || thr.Budget == nil || *thr.Budget > 0 {
		return nil, nil
	}
	sum := thr.summary()
	for _, p := range thr.participants {
		if _, err := h.Log.Append(ctx, tx, p, "thread_updated", now, sum); err != nil {
			return nil, err
		}
	}
	e := E(http.StatusUnprocessableEntity, "thread_budget_exhausted", "the thread's message budget is exhausted")
	e.Details["thread_id"] = thr.ID
	return e, nil
}

// countCut adds one to the dashboard counter name and appends an audit row
// with action name (PROTOCOL §6 "each cut is an audit entry and a dashboard
// counter", §16.2), inside tx.
func (h *Hub) countCut(ctx context.Context, tx *sql.Tx, name, actor string, targets []string, detail map[string]any) error {
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO counters(name, value) VALUES (?, 1) ON CONFLICT(name) DO UPDATE SET value = value + 1`, name); err != nil {
		return fmt.Errorf("hub: count %s: %w", name, err)
	}
	return audit.Record(ctx, tx, actor, name, targets, detail)
}
