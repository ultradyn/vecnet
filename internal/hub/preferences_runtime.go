package hub

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/prefs"
)

// preferenceNumber is only used with complete, validated snapshots produced
// by Preferences. Missing registry keys or nonnumeric types are programming
// errors, never a reason to silently substitute a different runtime policy.
func preferenceNumber(s prefs.Snapshot, key string) int {
	e, err := s.Get(key)
	if err != nil {
		panic("hub: incomplete runtime preference snapshot")
	}
	n, ok := e.Value.Int64()
	if !ok {
		panic("hub: nonnumeric runtime preference")
	}
	return int(n)
}

func preferenceDuration(s prefs.Snapshot, key string) time.Duration {
	return time.Duration(preferenceNumber(s, key)) * time.Second
}

func effectivePresence(s prefs.Snapshot, p PresenceRow, now time.Time) string {
	if p.State == nil || *p.State == "" || p.At == nil {
		return "unknown"
	}
	key := "presence.default_ttl"
	switch p.Kind {
	case "session":
		key = "presence.session_ttl"
	case "launcher":
		key = "presence.launcher_ttl"
	}
	at, err := envelope.ParseTime(*p.At)
	if err != nil || now.Sub(at) > preferenceDuration(s, key) {
		return "unknown"
	}
	return *p.State
}

// A send passes its transaction's one snapshot through fanOut as well as loop
// and budget evaluation. The legacy GateWakeIdle entry point remains usable by
// other callers without opening a nested transaction.
func (h *Hub) gateWakeIdleSnapshot(ctx context.Context, tx *sql.Tx, d *delivery, s prefs.Snapshot, now time.Time) error {
	d.held = false
	if d.priority != "wake_idle" {
		return nil
	}
	var p PresenceRow
	if err := tx.QueryRowContext(ctx, `SELECT kind, presence_state, presence_at FROM peers WHERE peer_id = ?`, d.recipient).
		Scan(&p.Kind, &p.State, &p.At); err != nil {
		return fmt.Errorf("hub: recipient presence: %w", err)
	}
	switch effectivePresence(s, p, now) {
	case "idle", "unknown":
	default:
		d.held = true
	}
	return nil
}
