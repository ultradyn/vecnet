package hub

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"time"

	"github.com/XertroV/vecnet/internal/envelope"
)

// DefaultTickInterval is the production ticker period.
const DefaultTickInterval = 30 * time.Second

// SlowTickEvery is how many ticks pass between the slow housekeeping jobs
// (ArchiveIdleGroups, Blobs.GC): every 120th tick, an hour at 30 s.
const SlowTickEvery = 120

// RunTickers runs the hub's periodic jobs every `every` (DefaultTickInterval
// when <= 0) until ctx ends: ExpireDeliveries and ExpireApprovals at the
// hub's now, then OnIdle for every peer holding wake_idle deliveries whose
// presence is now idle or unknown (a report that just went stale counts as
// unknown, PROTOCOL §5.2, §8); every SlowTickEvery-th tick also
// ArchiveIdleGroups and Blobs.GC. Errors are logged and the next tick runs.
// Tests pass a short interval and drive the hub's clock.
func (h *Hub) RunTickers(ctx context.Context, every time.Duration) {
	if ctx.Err() != nil {
		return
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	wake := h.Preferences.Subscribe(ctx)
	if err := h.releaseStaleHeld(ctx, h.Now()); err != nil && ctx.Err() == nil {
		slog.Error("hub: initial release held", "err", err)
	}
	if every <= 0 {
		every = DefaultTickInterval
	}
	t := time.NewTicker(every)
	defer t.Stop()
	n := 0
	for {
		if ctx.Err() != nil {
			return
		}
		select {
		case <-ctx.Done():
			return
		case _, ok := <-wake:
			if !ok {
				return
			}
			// A preference wake only re-evaluates presence. It must never
			// advance slow housekeeping or run deletion jobs.
			if err := h.releaseStaleHeld(ctx, h.Now()); err != nil && ctx.Err() == nil {
				slog.Error("hub: preferences release held", "err", err)
			}
			continue
		case <-t.C:
		}
		n++
		h.Tick(ctx, n%SlowTickEvery == 0)
	}
}

// Tick runs one round of RunTickers' jobs; slow adds the hourly ones.
func (h *Hub) Tick(ctx context.Context, slow bool) {
	now := h.Now()
	if _, err := h.ExpireNotices(ctx); err != nil && ctx.Err() == nil {
		slog.Error("hub: tick expire notices", "err", err)
	}
	if _, err := h.ExpireDeliveries(ctx, now); err != nil && ctx.Err() == nil {
		slog.Error("hub: tick expire deliveries", "err", err)
	}
	if _, err := h.ExpireApprovals(ctx, now); err != nil && ctx.Err() == nil {
		slog.Error("hub: tick expire approvals", "err", err)
	}
	if err := h.releaseStaleHeld(ctx, now); err != nil && ctx.Err() == nil {
		slog.Error("hub: tick release held", "err", err)
	}
	if !slow {
		return
	}
	if _, err := h.DB.ExecContext(ctx, `DELETE FROM reaction_ops WHERE at < ?`, envelope.FormatTime(now.Add(-time.Hour))); err != nil && ctx.Err() == nil {
		slog.Error("hub: tick prune reaction ops", "err", err)
	}
	if _, err := h.ArchiveIdleGroups(ctx, now); err != nil && ctx.Err() == nil {
		slog.Error("hub: tick archive groups", "err", err)
	}
	if h.Cfg.BackfillEnabled {
		if _, err := h.BackfillDeliverySlice(ctx, h.Cfg.BackfillLimits); err != nil && ctx.Err() == nil {
			slog.Error("hub: backfill slice", "reason", "slice_failed")
		}
	}
	if _, err := h.Blobs.GC(ctx); err != nil && ctx.Err() == nil {
		slog.Error("hub: tick blob gc", "err", err)
	}
}

// releaseStaleHeld evaluates presence and releases eligible held deliveries in
// one writer transaction. A longer TTL, new presence report or newly held send
// cannot commit between the policy read and the release.
func (h *Hub) releaseStaleHeld(ctx context.Context, now time.Time) error {
	return h.DB.Tx(ctx, func(tx *sql.Tx) error {
		snapshot, err := h.Preferences.LoadSnapshot(ctx, tx)
		if err != nil {
			return err
		}
		rows, err := tx.QueryContext(ctx,
			`SELECT p.peer_id, p.kind, p.presence_state, p.presence_at FROM peers p
    WHERE EXISTS (SELECT 1 FROM deliveries d WHERE d.recipient = p.peer_id AND d.held = 1 AND d.state = 'queued')`)
		if err != nil {
			return fmt.Errorf("hub: held peers: %w", err)
		}
		var release []string
		for rows.Next() {
			var peer string
			var p PresenceRow
			if err := rows.Scan(&peer, &p.Kind, &p.State, &p.At); err != nil {
				rows.Close()
				return fmt.Errorf("hub: held peers: %w", err)
			}
			switch effectivePresence(snapshot, p, now) {
			case "idle", "unknown":
				release = append(release, peer)
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return fmt.Errorf("hub: held peers: %w", err)
		}
		if err := rows.Close(); err != nil {
			return fmt.Errorf("hub: held peers: %w", err)
		}
		for _, peer := range release {
			if _, err := tx.ExecContext(ctx,
				`UPDATE deliveries SET held = 0, updated_at = ? WHERE recipient = ? AND held = 1 AND state = 'queued'`,
				envelope.FormatTime(now), peer); err != nil {
				return fmt.Errorf("hub: release held deliveries: %w", err)
			}
		}
		return nil
	})
}
