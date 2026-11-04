package hub

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/XertroV/vecnet/internal/audit"
	"github.com/XertroV/vecnet/internal/envelope"
)

// The link-code brute-force guard (PROTOCOL §20.1). Link codes are typed on a
// public form, so the hub counts refused codes across every client: the
// DotLinkFailureBudget-th refusal within DotLinkFailureWindow burns every
// live link code and refuses link codes for DotLinkLockout, or until the
// operator issues a new code. Each refusal is audited ("dot_link_refused"),
// and so is the lockout ("dot_link_lockout"); the lockout bounds those writes
// to the budget per lockout. The count and the lockout are decided in memory
// at once. A refusal's audit row is written after the answer, so a guess never
// waits on the store's write lock; the refusal that spends the budget writes
// its row and the burn before answering. Codes typed while locked out are
// refused unread and not audited: the per-client limiter (PROTOCOL §20.2)
// bounds them, and the lockout row already records the attack.
const (
	DotLinkFailureBudget = 20
	DotLinkFailureWindow = DotLinkTTL
	DotLinkLockout       = DotLinkTTL
)

// linkGuard is the hub's in-memory count of refused link codes. A restart
// forgets it, which costs nothing: a lockout's burned codes stay burned.
type linkGuard struct {
	mu          sync.Mutex
	failures    []time.Time
	lockedUntil time.Time
}

// unlock ends a lockout and forgets the refusals: the operator has issued a
// fresh code.
func (g *linkGuard) unlock() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.failures, g.lockedUntil = nil, time.Time{}
}

// DotLinkLocked reports how long link codes stay refused; 0 when none of
// them is.
func (h *Hub) DotLinkLocked() time.Duration {
	g := &h.linkGuard
	g.mu.Lock()
	defer g.mu.Unlock()
	if d := g.lockedUntil.Sub(h.Now()); d > 0 {
		return d
	}
	return 0
}

// DotLinkFailed records one refused link code and returns how long link codes
// are now locked out (0 while the budget lasts). The refusal that spends the
// budget burns every live code in the same transaction as its audit row.
func (h *Hub) DotLinkFailed() time.Duration {
	now := h.Now()
	g := &h.linkGuard
	g.mu.Lock()
	if d := g.lockedUntil.Sub(now); d > 0 {
		g.mu.Unlock()
		return d
	}
	cut := now.Add(-DotLinkFailureWindow)
	kept := g.failures[:0]
	for _, t := range g.failures {
		if t.After(cut) {
			kept = append(kept, t)
		}
	}
	g.failures = append(kept, now)
	n := len(g.failures)
	lock := n >= DotLinkFailureBudget
	until := now.Add(DotLinkLockout)
	if lock {
		g.failures, g.lockedUntil = nil, until
	}
	g.mu.Unlock()

	if lock {
		// The lockout's burn is written before the answer, so a code the
		// operator issues afterwards (which ends the lockout) is never caught
		// by it. It waits on the write lock once per budget, not per guess.
		if err := h.recordLinkFailure(now, n, lock, until); err != nil {
			slog.Error("hub: link-code lockout", "err", err)
		}
		return DotLinkLockout
	}
	go func() {
		if err := h.recordLinkFailure(now, n, false, until); err != nil {
			slog.Error("hub: record refused link code", "err", err)
		}
	}()
	return 0
}

// recordLinkFailure writes DotLinkFailed's audit rows and, at a lockout,
// burns the live codes.
func (h *Hub) recordLinkFailure(now time.Time, n int, lock bool, until time.Time) error {
	ctx := context.Background()
	return h.DB.Tx(ctx, func(tx *sql.Tx) error {
		if err := audit.Record(ctx, tx, "hub", "dot_link_refused", nil, map[string]any{"failures_in_window": n}); err != nil {
			return err
		}
		if !lock {
			return nil
		}
		at := envelope.FormatTime(now)
		// Only codes issued by now: every code expires DotLinkTTL after issue.
		res, err := tx.ExecContext(ctx, `UPDATE dot_links SET used_at = ? WHERE used_at IS NULL AND expires_at > ? AND expires_at <= ?`,
			at, at, envelope.FormatTime(now.Add(DotLinkTTL)))
		if err != nil {
			return fmt.Errorf("hub: burn link codes: %w", err)
		}
		burned, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("hub: burn link codes: %w", err)
		}
		return audit.Record(ctx, tx, "hub", "dot_link_lockout", nil, map[string]any{
			"failures": n, "codes_burned": burned, "locked_until": envelope.FormatTime(until),
		})
	})
}
