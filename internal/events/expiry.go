package events

import (
	"context"
	"database/sql"
	"time"

	"github.com/XertroV/vecnet/internal/envelope"
)

// expire deactivates lapsed proofs once, so the expiry lifecycle transition is
// observable without polling the same expired subscription into diagnostics.
func (st *Store) expire(ctx context.Context, now time.Time) error {
	var expired []Subscription
	err := st.DB.Tx(ctx, func(tx *sql.Tx) error {
		expired = nil
		rows, err := tx.QueryContext(ctx, `SELECT `+subColumns+` FROM event_subscriptions WHERE expires_at <= ? AND verified_at IS NOT NULL`, envelope.FormatTime(now))
		if err != nil {
			return err
		}
		for rows.Next() {
			sub, err := scanSub(rows)
			if err != nil {
				rows.Close()
				return err
			}
			expired = append(expired, sub)
		}
		scanErr := rows.Err()
		rows.Close()
		if scanErr != nil {
			return scanErr
		}
		_, err = tx.ExecContext(ctx, `UPDATE event_subscriptions SET verified_at = NULL WHERE expires_at <= ? AND verified_at IS NOT NULL`, envelope.FormatTime(now))
		return err
	})
	if err == nil {
		for _, sub := range expired {
			LogLifecycle("events.expiry", sub, map[string]any{"result": "expired"})
		}
	}
	return err
}
