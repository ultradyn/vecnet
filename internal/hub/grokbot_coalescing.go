package hub

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"time"

	"github.com/XertroV/vecnet/internal/envelope"
)

const (
	GrokbotTransportDeliveryV1       = "delivery_v1"
	GrokbotTransportRecipientDrainV1 = "recipient_drain_v1"
)

// GrokWakeClaim identifies one durable recipient-domain claim. PendingCount
// includes the representative; coverage evidence excludes its election.
type GrokWakeClaim struct {
	Token        string
	PendingCount int
}

func validateGrokbotTransport(transport string) error {
	if transport != GrokbotTransportDeliveryV1 && transport != GrokbotTransportRecipientDrainV1 {
		return invalidRouteField("grokbot_transport", "transport is delivery_v1 or recipient_drain_v1")
	}
	return nil
}

// BeginGrokbotPOST is the no-withdrawal boundary. Validation, armed exclusion
// and the unique budget reservation commit together before client entry.
func (h *Hub) BeginGrokbotPOST(ctx context.Context, d DueDelivery, now time.Time) (bool, time.Duration, error) {
	if d.GrokWake == nil {
		return false, 0, errors.New("hub: missing Grok wake claim")
	}
	ok, wait := false, grokbotBudgetErrWait
	nowS := envelope.FormatTime(now)
	err := h.DB.Tx(ctx, func(tx *sql.Tx) error {
		ok, wait = false, grokbotBudgetErrWait
		var phase, lease string
		err := tx.QueryRowContext(ctx, `SELECT phase,lease_until FROM grokbot_wakes WHERE route_id=? AND recipient=? AND token=? AND representative_id=?`, d.RouteID, d.Recipient, d.GrokWake.Token, d.DeliveryID).Scan(&phase, &lease)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if phase != "claimed" || lease <= nowS {
			return nil
		}
		var valid bool
		err = tx.QueryRowContext(ctx, `SELECT EXISTS(
   SELECT 1 FROM grokbot_wakes w JOIN deliveries d ON d.dly_id=w.representative_id
   JOIN routes r ON r.route_id=w.route_id JOIN peers p ON p.peer_id=w.recipient
   WHERE w.route_id=?1 AND w.recipient=?2 AND w.token=?3
   AND NOT EXISTS(SELECT 1 FROM grokbot_legacy_claims l WHERE l.route_id=w.route_id AND l.recipient=w.recipient AND l.lease_until>?5)
   AND d.state='pushed' AND d.attempt=?4 AND d.route_id=w.route_id AND d.recipient=w.recipient AND d.held=0 AND d.expires_at>?5
   AND r.kind='grokbot' AND r.grokbot_transport='recipient_drain_v1' AND r.verified_at IS NOT NULL
   AND r.url=w.expected_url AND r.url=?6 AND coalesce(r.key_enc,'')=w.expected_key
   AND p.disabled=0 AND p.archived_at IS NULL AND p.prn_id=w.expected_principal
   AND (r.owner_kind='peer' AND r.owner_id=p.peer_id OR r.owner_kind='principal' AND r.owner_id=p.prn_id)
   AND (p.route_id=r.route_id OR p.route_id IS NULL AND r.owner_kind='principal'))`, d.RouteID, d.Recipient, d.GrokWake.Token, d.Attempt, nowS, d.URL).Scan(&valid)
		if err != nil {
			return err
		}
		if !valid {
			return deferGrokbotClaim(ctx, tx, d, now.Add(wait), false, false, now)
		}
		ok, wait, err = takeGrokbotRun(ctx, tx, d.RouteID, d.GrokWake.Token, now)
		if err != nil {
			return err
		}
		if !ok {
			return deferGrokbotClaim(ctx, tx, d, now.Add(wait), false, false, now)
		}
		_, err = tx.ExecContext(ctx, `UPDATE grokbot_wakes SET phase='armed',begin_idle_counter=idle_counter,cover_valid=1 WHERE route_id=? AND recipient=? AND token=?`, d.RouteID, d.Recipient, d.GrokWake.Token)
		return err
	})
	if err != nil {
		return false, grokbotBudgetErrWait, err
	}
	return ok, wait, nil
}

// DeferGrokbotPOST compensates a known noninvocation, including key/body/request
// preflight failures. Ambiguous client entry must never use this path.
func (h *Hub) DeferGrokbotPOST(ctx context.Context, d DueDelivery, failed bool, now time.Time) error {
	if d.GrokWake == nil {
		return errors.New("hub: missing Grok wake claim")
	}
	return h.DB.Tx(ctx, func(tx *sql.Tx) error {
		return deferGrokbotClaim(ctx, tx, d, now.Add(grokbotBudgetErrWait), failed, false, now)
	})
}

// CancelUninvokedGrokbotPOST is for the caller that just won Begin but has
// proof it has not entered the client (e.g. cancellation in that interval).
// Preflight callers must use DeferGrokbotPOST, which cannot withdraw an arm.
func (h *Hub) CancelUninvokedGrokbotPOST(ctx context.Context, d DueDelivery, now time.Time) error {
	if d.GrokWake == nil {
		return errors.New("hub: missing Grok wake claim")
	}
	return h.DB.Tx(ctx, func(tx *sql.Tx) error {
		// Reservation ownership does not expire when coordinator ownership does.
		if _, err := tx.ExecContext(ctx, `DELETE FROM route_runs WHERE route_id=? AND grok_wake_token=? AND grok_wake_settled_200=0`, d.RouteID, d.GrokWake.Token); err != nil {
			return err
		}
		return deferGrokbotClaim(ctx, tx, d, now.Add(grokbotBudgetErrWait), false, true, now)
	})
}

func deferGrokbotClaim(ctx context.Context, tx *sql.Tx, d DueDelivery, next time.Time, failed, allowArmed bool, now time.Time) error {
	var ordinal int
	var phase, lease string
	err := tx.QueryRowContext(ctx, `SELECT failure_ordinal,phase,lease_until FROM grokbot_wakes w
 WHERE route_id=?1 AND recipient=?2 AND token=?3 AND representative_id=?4 AND (?6 OR lease_until>?5)
 AND NOT EXISTS(SELECT 1 FROM route_runs rr WHERE rr.route_id=w.route_id AND rr.grok_wake_token=w.token AND rr.grok_wake_settled_200=1)
 AND EXISTS(SELECT 1 FROM deliveries d WHERE d.dly_id=w.representative_id AND d.route_id=w.route_id
 AND d.recipient=w.recipient AND d.state='pushed' AND d.attempt=?7 AND d.next_attempt_at=w.lease_until)`, d.RouteID, d.Recipient, d.GrokWake.Token, d.DeliveryID, envelope.FormatTime(now), allowArmed, d.Attempt).Scan(&ordinal, &phase, &lease)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if phase != "claimed" && !(allowArmed && phase == "armed") {
		return nil
	}
	if failed {
		ordinal = max(ordinal+1, d.Attempt)
		next = now.Add(PushBackoff(ordinal, 0, nil))
	}
	// An armed call here carries an explicit proof of noninvocation from the
	// worker, so only its own reservation can be returned.
	if _, err := tx.ExecContext(ctx, `DELETE FROM route_runs WHERE route_id=? AND grok_wake_token=? AND grok_wake_settled_200=0`, d.RouteID, d.GrokWake.Token); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE grokbot_wakes SET phase='waiting',lease_until='',cover_until='',cover_valid=0,retry_at=?,failure_ordinal=? WHERE route_id=? AND recipient=? AND token=?`, envelope.FormatTime(next), ordinal, d.RouteID, d.Recipient, d.GrokWake.Token); err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx, `UPDATE deliveries SET state='queued',attempt=attempt-1,next_attempt_at=?,updated_at=? WHERE dly_id=? AND state='pushed' AND attempt=? AND route_id=? AND recipient=? AND next_attempt_at=?`, envelope.FormatTime(next), envelope.FormatTime(now), d.DeliveryID, d.Attempt, d.RouteID, d.Recipient, lease)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil || n == 0 {
		return err
	}
	return supersedeRequeuedReaction(ctx, tx, d.DeliveryID, envelope.FormatTime(now))
}

// GrokbotWakePushResult settles domain cadence and the existing delivery result
// atomically. The token can settle once, even if ack/collection advanced its
// representative. A stale refusal can return only its own run reservation.
func (h *Hub) GrokbotWakePushResult(ctx context.Context, d DueDelivery, status int, body []byte, retryAfter time.Duration, netErr error, now time.Time) error {
	if d.GrokWake == nil {
		return errors.New("hub: missing Grok wake claim")
	}
	outcome := grokbotPushOutcome(status, body, netErr)
	var retryAt time.Time
	return h.recordPushGated(ctx, d, outcome, status, retryAfter, netErr, now, func(tx *sql.Tx) (bool, error) {
		nowS := envelope.FormatTime(now)
		if netErr == nil && status == http.StatusOK {
			if _, err := tx.ExecContext(ctx, `UPDATE route_runs SET grok_wake_settled_200=1 WHERE route_id=? AND grok_wake_token=?`, d.RouteID, d.GrokWake.Token); err != nil {
				return false, err
			}
		}
		if netErr == nil && status != http.StatusOK {
			if _, err := tx.ExecContext(ctx, `DELETE FROM route_runs WHERE route_id=? AND grok_wake_token=? AND grok_wake_settled_200=0`, d.RouteID, d.GrokWake.Token); err != nil {
				return false, err
			}
		}
		var phase, lease string
		var ordinal int
		var valid bool
		err := tx.QueryRowContext(ctx, `SELECT phase,lease_until,failure_ordinal,cover_valid=1 AND idle_counter=begin_idle_counter FROM grokbot_wakes WHERE route_id=? AND recipient=? AND token=? AND representative_id=?`, d.RouteID, d.Recipient, d.GrokWake.Token, d.DeliveryID).Scan(&phase, &lease, &ordinal, &valid)
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		if phase != "armed" || lease <= nowS {
			return false, nil
		}
		cover := ""
		retry := ""
		nextPhase := "waiting"
		if outcome == pushAccepted && valid {
			cover = envelope.FormatTime(now.Add(PushLease))
			lease = ""
			ordinal = 0
			nextPhase = "covered"
		} else if outcome == pushAccepted {
			// Invalidated success retains the original finite exclusion, without
			// resetting cadence, reopening cover, or rewriting a repaired token.
		} else {
			ordinal = max(ordinal+1, d.Attempt)
			retryAt = now.Add(PushBackoff(ordinal, retryAfter, nil))
			retry = envelope.FormatTime(retryAt)
			if netErr == nil && status != http.StatusOK {
				lease = ""
			}
		}
		_, err = tx.ExecContext(ctx, `UPDATE grokbot_wakes SET phase=?,lease_until=?,retry_at=?,cover_until=?,failure_ordinal=?,cover_valid=0 WHERE route_id=? AND recipient=? AND token=?`, nextPhase, lease, retry, cover, ordinal, d.RouteID, d.Recipient, d.GrokWake.Token)
		return err == nil, err
	}, &retryAt)
}

// invalidateGrokbotWakes is the transaction-local seam for T2 lifecycle hooks.
// It never deletes armed/ambiguous exclusions or rolling-hour reservations.
// resetCadence is appropriate for repair only when no armed lease remains.
func invalidateGrokbotWakes(ctx context.Context, tx *sql.Tx, route, recipient, now string, resetCadence bool) error {
	_, err := tx.ExecContext(ctx, `UPDATE grokbot_wakes SET cover_valid=0,cover_until='',
 failure_ordinal=CASE WHEN ?4 AND NOT(lease_until>?3 AND phase IN ('armed','waiting')) THEN 0 ELSE failure_ordinal END,
 retry_at=CASE WHEN ?4 AND NOT(lease_until>?3 AND phase IN ('armed','waiting')) THEN '' ELSE retry_at END
 WHERE (?1='' OR route_id=?1) AND (?2='' OR recipient=?2)`, route, recipient, now, resetCadence)
	return err
}

// Capability changes belong to the persisted T1 route contract. Other
// lifecycle/authority and explicit-idle invalidations are T2 hook consumers.
func setGrokbotTransport(ctx context.Context, tx *sql.Tx, route, transport string, now time.Time) error {
	var old string
	if err := tx.QueryRowContext(ctx, `SELECT grokbot_transport FROM routes WHERE route_id=?`, route).Scan(&old); err != nil {
		return err
	}
	if old == transport {
		return nil
	}
	if transport == GrokbotTransportRecipientDrainV1 {
		if err := importLegacyGrokbotClaims(ctx, tx, route, envelope.FormatTime(now)); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE routes SET grokbot_transport=? WHERE route_id=?`, transport, route); err != nil {
		return err
	}
	if err := invalidateGrokbotWakes(ctx, tx, route, "", envelope.FormatTime(now), false); err != nil {
		return err
	}
	// Unarmed claims have proven noninvocation. Give back their existing claim
	// attempt without deleting gates for armed or ambiguous invocations.
	if _, err := tx.ExecContext(ctx, `UPDATE deliveries SET state='queued',attempt=attempt-1,next_attempt_at=NULL,updated_at=?1
 WHERE state='pushed' AND route_id=?2 AND EXISTS(SELECT 1 FROM grokbot_wakes w WHERE w.route_id=?2 AND w.recipient=deliveries.recipient AND w.representative_id=deliveries.dly_id AND w.phase='claimed')`, envelope.FormatTime(now), route); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `UPDATE grokbot_wakes SET phase='waiting',lease_until='',retry_at='' WHERE route_id=? AND phase='claimed'`, route)
	return err
}

// importLegacyGrokbotClaims runs in the capability writer transaction, before
// legacy settlement can clear/replace next_attempt_at. Existing opted claims
// retain their own coordinator identity rather than becoming legacy imports.
func importLegacyGrokbotClaims(ctx context.Context, tx *sql.Tx, route, now string) error {
	if _, err := tx.ExecContext(ctx, `INSERT INTO grokbot_legacy_claims(route_id,recipient,delivery_id,lease_until)
 SELECT d.route_id,d.recipient,d.dly_id,d.next_attempt_at FROM deliveries d
 WHERE d.route_id=?1 AND d.state='pushed' AND d.priority IN ('wake','wake_idle') AND d.next_attempt_at>?2
 AND NOT EXISTS(SELECT 1 FROM grokbot_wakes w WHERE w.route_id=d.route_id AND w.recipient=d.recipient
 AND w.representative_id=d.dly_id AND w.token<>'' AND w.lease_until=d.next_attempt_at)
 ON CONFLICT(route_id,recipient,delivery_id) DO UPDATE SET lease_until=max(lease_until,excluded.lease_until)`, route, now); err != nil {
		return err
	}
	// Seed only absent coordinators. Imports remain an independent exclusion:
	// settlement/cancellation of an existing opted token cannot clear them.
	_, err := tx.ExecContext(ctx, `INSERT INTO grokbot_wakes(route_id,recipient,phase,lease_until)
 SELECT route_id,recipient,'waiting',max(lease_until) FROM grokbot_legacy_claims
 WHERE route_id=?1 AND lease_until>?2 GROUP BY route_id,recipient
 ON CONFLICT(route_id,recipient) DO NOTHING`, route, now)
	return err
}
