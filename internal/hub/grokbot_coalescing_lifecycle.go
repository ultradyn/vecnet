package hub

import (
	"context"
	"database/sql"
)

// cancelUnarmedGrokbotClaims only compensates the exact still-current claim.
// It never touches reservations or independent imported legacy exclusions.
func cancelUnarmedGrokbotClaims(ctx context.Context, tx *sql.Tx, route, recipient, now string) error {
	const claims = `EXISTS(SELECT 1 FROM grokbot_wakes w WHERE w.route_id=deliveries.route_id
 AND w.recipient=deliveries.recipient AND w.representative_id=deliveries.dly_id AND w.phase='claimed'
 AND w.token<>'' AND w.lease_until=deliveries.next_attempt_at
 AND NOT EXISTS(SELECT 1 FROM route_runs rr WHERE rr.route_id=w.route_id AND rr.grok_wake_token=w.token))`
	if _, err := tx.ExecContext(ctx, `UPDATE deliveries SET state='queued',attempt=attempt-1,next_attempt_at=NULL,updated_at=?3
 WHERE (?1='' OR route_id=?1) AND (?2='' OR recipient=?2) AND state='pushed' AND attempt>0 AND `+claims, route, recipient, now); err != nil {
		return err
	}
	// Match the existing DeferPush reaction withdrawal rule in this transaction,
	// set-wise, before a cancelled claim can be elected again.
	if _, err := tx.ExecContext(ctx, `UPDATE deliveries SET state='superseded',held=0,next_attempt_at=NULL,updated_at=?3
 WHERE (?1='' OR route_id=?1) AND (?2='' OR recipient=?2) AND state='queued' AND dispatching=0
 AND EXISTS(SELECT 1 FROM grokbot_wakes w WHERE w.route_id=deliveries.route_id AND w.recipient=deliveries.recipient
 AND w.representative_id=deliveries.dly_id AND w.phase='claimed') AND `+reactionWithdrawn, route, recipient, now); err != nil {
		return err
	}
	// Keep a representative retry chain's frozen body/count across cancellation.
	// Election rebuilds them only when the representative actually changes.
	_, err := tx.ExecContext(ctx, `UPDATE grokbot_wakes SET phase='waiting',lease_until='',retry_at=''
 WHERE (?1='' OR route_id=?1) AND (?2='' OR recipient=?2) AND phase='claimed'
 AND NOT EXISTS(SELECT 1 FROM route_runs rr WHERE rr.route_id=grokbot_wakes.route_id AND rr.grok_wake_token=grokbot_wakes.token)`, route, recipient)
	return err
}

// explicitIdleGrokbotWakes is called only after SetPresence applied an idle
// report inside its writer transaction. Direct/ticker hold release is distinct.
// A claimed token survives: Begin captures the then-current counter.
func explicitIdleGrokbotWakes(ctx context.Context, tx *sql.Tx, recipient string) error {
	_, err := tx.ExecContext(ctx, `UPDATE grokbot_wakes SET idle_counter=idle_counter+1,cover_valid=0,cover_until='' WHERE recipient=?`, recipient)
	return err
}

// invalidateGrokbotDelivery scopes repair/retry to the current domain. Discard
// invalidates only that representative and preserves its finite shared wait.
func invalidateGrokbotDelivery(ctx context.Context, tx *sql.Tx, delivery, now string, repair bool) error {
	var route sql.NullString
	var recipient string
	if err := tx.QueryRowContext(ctx, `SELECT route_id,recipient FROM deliveries WHERE dly_id=?`, delivery).Scan(&route, &recipient); err != nil {
		return err
	}
	if !route.Valid {
		return nil
	}
	if !repair {
		_, err := tx.ExecContext(ctx, `UPDATE grokbot_wakes SET cover_valid=0,cover_until='' WHERE route_id=? AND recipient=? AND representative_id=?`, route.String, recipient, delivery)
		return err
	}
	return invalidateGrokbotWakes(ctx, tx, route.String, recipient, now, true)
}

// coverHandledGrokbotDelivery is called only for a real queued/pushed ->
// processed/scoped-accepted change, authorized in the same transaction. A
// claimed token proves noninvocation; absence of best-effort telemetry does not.
func coverHandledGrokbotDelivery(ctx context.Context, tx *sql.Tx, delivery, now string) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO grokbot_covers(route_id,delivery_id,recipient,first_covered_at)
 SELECT d.route_id,d.dly_id,d.recipient,?2 FROM deliveries d JOIN routes r ON r.route_id=d.route_id
 WHERE d.dly_id=?1 AND d.priority IN ('wake','wake_idle') AND d.expires_at>?2
 AND r.kind='grokbot' AND r.grokbot_transport='recipient_drain_v1'
 AND (d.attempt=0 OR EXISTS(SELECT 1 FROM grokbot_wakes w WHERE w.route_id=d.route_id AND w.recipient=d.recipient
 AND w.representative_id=d.dly_id AND w.phase='claimed' AND w.token<>'' AND w.lease_until=d.next_attempt_at
 AND NOT EXISTS(SELECT 1 FROM route_runs rr WHERE rr.route_id=w.route_id AND rr.grok_wake_token=w.token)))
 AND NOT EXISTS(SELECT 1 FROM grokbot_legacy_claims l WHERE l.route_id=d.route_id AND l.recipient=d.recipient
 AND l.delivery_id=d.dly_id AND l.lease_until>?2)
 ON CONFLICT(route_id,delivery_id) DO NOTHING`, delivery, now)
	return err
}
