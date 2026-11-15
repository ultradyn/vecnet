package events

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/XertroV/vecnet/internal/audit"
)

// Remove is the operator's permanent removal of an exact subscription ID and
// its queued callbacks and refresh schedule. ErrNotFound means no such ID.
// Like client unsubscribe, an already-started callback may finish; the worker
// rechecks the subscription before sending another. A client may subscribe
// again with a fresh ID. Callback verification proofs remain unchanged.
func (st *Store) Remove(ctx context.Context, id string) error {
	var notify []string
	err := st.DB.Tx(ctx, func(tx *sql.Tx) error {
		// Delete by ID without decoding the row or opening its sealed secret, so
		// even a broken subscription can be removed. All cleanup rolls back if
		// the audit or operator invalidation cannot be recorded.
		res, err := tx.ExecContext(ctx, `DELETE FROM event_subscriptions WHERE sub_id = ?`, id)
		if err != nil {
			return fmt.Errorf("events: operator remove: %w", err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("events: operator remove: %w", err)
		}
		if n == 0 {
			return ErrNotFound
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM event_outbox WHERE sub_id = ?`, id); err != nil {
			return fmt.Errorf("events: remove outbox: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM meta WHERE key = 'events_refresh_schedule:' || ?`, id); err != nil {
			return fmt.Errorf("events: remove refresh schedule: %w", err)
		}
		if err := audit.Record(ctx, tx, "operator", "subscription_remove", []string{id}, nil); err != nil {
			return err
		}
		if st.hub != nil {
			notify, err = st.hub.NoteOperatorSubscription(ctx, tx)
			return err
		}
		return nil
	})
	if err == nil && len(notify) > 0 {
		st.hub.Log.Notify(notify...)
	}
	return err
}
