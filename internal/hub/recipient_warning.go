package hub

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/XertroV/vecnet/internal/envelope"
)

// recipientWarnings snapshots last-seen evidence in the send transaction so
// idempotent replays return the original advice. Targets are already unique.
// A connected stream is ongoing activity, even if it opened hours ago. The
// persisted state is reset on hub startup, so a dead stream cannot survive it.
func recipientWarnings(ctx context.Context, tx *sql.Tx, targets []target, now time.Time, after time.Duration) ([]SendWarning, error) {
	if after == 0 {
		return nil, nil
	}
	var warnings []SendWarning
	for _, target := range targets {
		var kind, reachability, enrolled string
		var lastSeen sql.NullString
		if err := tx.QueryRowContext(ctx, `SELECT kind, reachability, last_seen, enrolled_at FROM peers WHERE peer_id=?`, target.peer).
			Scan(&kind, &reachability, &lastSeen, &enrolled); err != nil {
			return nil, fmt.Errorf("hub: recipient last seen: %w", err)
		}
		if kind == "operator" || kind == "source" || reachability == "connected" {
			continue
		}
		at := enrolled
		if lastSeen.Valid {
			at = lastSeen.String
		}
		seen, err := envelope.ParseTime(at)
		if err != nil {
			// Missing or malformed evidence cannot establish an age for this
			// advisory and must not prevent an otherwise valid delivery.
			continue
		}
		age := now.Sub(seen)
		if age <= after {
			continue
		}
		message := fmt.Sprintf("Send succeeded. Recipient was last seen %d minutes ago and may not respond promptly.", int(age/time.Minute))
		if !lastSeen.Valid {
			message = fmt.Sprintf("Send succeeded. Recipient has never been seen; enrolled %d minutes ago and may not respond promptly.", int(age/time.Minute))
		}
		warnings = append(warnings, SendWarning{
			Code: "recipient_not_seen", Message: message, Recipient: target.peer,
			LastSeen: lastSeen.String, AgeSeconds: int(age / time.Second), ThresholdSeconds: int(after / time.Second),
		})
	}
	return warnings, nil
}
