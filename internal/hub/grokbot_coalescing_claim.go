package hub

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/eventpayload"
	"github.com/XertroV/vecnet/internal/ids"
)

// Pages bound candidate materialization independently of follower/domain count.
const grokCandidatePage = 16

// Kept separate from the inclusive count: election alone is not suppression.
const grokPendingPredicate = `d.route_id=?1 AND d.recipient=?2
 AND d.state IN ('queued','pushed') AND d.priority IN ('wake','wake_idle') AND d.expires_at>?3`

func coverGrokbotFollowers(ctx context.Context, tx *sql.Tx, route, recipient, representative, now string) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO grokbot_covers(route_id,delivery_id,recipient,first_covered_at)
 SELECT d.route_id,d.dly_id,d.recipient,?3 FROM deliveries d INDEXED BY deliveries_grok_domain WHERE `+grokPendingPredicate+`
 AND (?4='' OR d.dly_id<>?4)
 AND NOT EXISTS(SELECT 1 FROM grokbot_legacy_claims l WHERE l.route_id=d.route_id AND l.recipient=d.recipient
 AND l.delivery_id=d.dly_id AND l.lease_until>?3) ON CONFLICT(route_id,delivery_id) DO NOTHING`, route, recipient, now, representative)
	return err
}

// Candidate discovery includes held opted-in arrivals for attachment, but only
// actual representatives pass the original cross-route thread-order predicate.
// The anti-join prevents repeatedly materializing annotated active followers.
const grokCandidateQuery = `SELECT d.dly_id,d.recipient,d.thr_id,d.route_id,r.url,r.kind,d.attempt,
 r.grokbot_transport,d.held,coalesce(d.next_attempt_at,''),d.event_seq
 FROM deliveries d INDEXED BY deliveries_grok_cursor JOIN routes r ON r.route_id=d.route_id
 LEFT JOIN grokbot_wakes w ON w.route_id=d.route_id AND w.recipient=d.recipient
 WHERE d.state IN ('queued','pushed') AND d.priority IN ('wake','wake_idle')
 AND r.verified_at IS NOT NULL AND d.expires_at>?1
 AND (d.event_seq,d.dly_id)>(?2,?3)
 AND ((r.kind='grokbot' AND r.grokbot_transport='recipient_drain_v1'
       AND NOT ((w.lease_until>?1 OR w.retry_at>?1 OR w.cover_until>?1
                 OR EXISTS(SELECT 1 FROM grokbot_legacy_claims l WHERE l.route_id=d.route_id AND l.recipient=d.recipient AND l.lease_until>?1))
                AND EXISTS(SELECT 1 FROM grokbot_covers c WHERE c.route_id=d.route_id AND c.delivery_id=d.dly_id)))
      OR ((r.kind!='grokbot' OR r.grokbot_transport!='recipient_drain_v1') AND d.held=0 AND (d.next_attempt_at IS NULL OR d.next_attempt_at<=?1)))
 ORDER BY d.event_seq,d.dly_id LIMIT ?4`

type grokCandidate struct {
	d               DueDelivery
	transport, next string
	held            bool
	seq             int64
}

func (h *Hub) claimPushCandidates(ctx context.Context, tx *sql.Tx, now time.Time, limit int) ([]DueDelivery, []eventpayload.StoredPayload, error) {
	nowS := envelope.FormatTime(now)
	var coordinated bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM routes WHERE kind='grokbot' AND grokbot_transport='recipient_drain_v1') OR EXISTS(SELECT 1 FROM grokbot_wakes)`).Scan(&coordinated); err != nil {
		return nil, nil, err
	}
	if !coordinated {
		return claimLegacyPushCandidates(ctx, tx, nowS, limit)
	}
	if err := cleanupGrokbotWakes(ctx, tx, nowS); err != nil {
		return nil, nil, err
	}
	var out []DueDelivery
	var payloads []eventpayload.StoredPayload
	var seq int64
	var id string
	for len(out) < limit {
		rows, err := tx.QueryContext(ctx, grokCandidateQuery, nowS, seq, id, grokCandidatePage)
		if err != nil {
			return nil, nil, fmt.Errorf("hub: claim candidates: %w", err)
		}
		page := make([]grokCandidate, 0, grokCandidatePage)
		for rows.Next() {
			var c grokCandidate
			if err = rows.Scan(&c.d.DeliveryID, &c.d.Recipient, &c.d.ThreadID, &c.d.RouteID, &c.d.URL, &c.d.Kind, &c.d.Attempt, &c.transport, &c.held, &c.next, &c.seq); err != nil {
				break
			}
			page = append(page, c)
		}
		closeErr := rows.Close()
		if err == nil {
			err = rows.Err()
		}
		if err == nil {
			err = closeErr
		}
		if err != nil {
			return nil, nil, err
		}
		if len(page) == 0 {
			break
		}
		for _, c := range page {
			seq, id = c.seq, c.d.DeliveryID
			opted := c.d.Kind == RouteKindGrokbot && c.transport == GrokbotTransportRecipientDrainV1
			if opted {
				elected, err := h.electGrokbotWake(ctx, tx, &c, now)
				if err != nil {
					return nil, nil, err
				}
				if !elected {
					continue
				}
			} else {
				// An armed lease survives transport revocation. Lifecycle invalidation
				// hooks in T2 clear cover rights without withdrawing this exclusion.
				var gated bool
				if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM grokbot_wakes WHERE route_id=?1 AND recipient=?2 AND lease_until>?3) OR EXISTS(SELECT 1 FROM grokbot_legacy_claims WHERE route_id=?1 AND recipient=?2 AND lease_until>?3)`, c.d.RouteID, c.d.Recipient, nowS).Scan(&gated); err != nil {
					return nil, nil, err
				}
				if gated {
					continue
				}
				ordered, err := grokCandidateOrdered(ctx, tx, c.d)
				if err != nil {
					return nil, nil, err
				}
				if !ordered {
					continue
				}
			}
			var p eventpayload.StoredPayload
			if err := tx.QueryRowContext(ctx, `SELECT e.data,e.payload_format,coalesce(e.msg_id,''),coalesce(e.snapshot_id,''),d.body_sha256 FROM deliveries d JOIN events e ON e.seq=d.event_seq WHERE d.dly_id=?`, c.d.DeliveryID).Scan(&p.Data, &p.Format, &p.MsgID, &p.SnapshotID, &p.BodySHA256); err != nil {
				return nil, nil, err
			}
			c.d.Attempt++
			out = append(out, c.d)
			payloads = append(payloads, p)
			if len(out) == limit {
				break
			}
		}
		if len(page) < grokCandidatePage {
			break
		}
	}
	return out, payloads, nil
}

func grokCandidateOrdered(ctx context.Context, tx *sql.Tx, d DueDelivery) (bool, error) {
	var blocked bool
	err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM deliveries p JOIN deliveries d ON d.dly_id=?
 WHERE p.recipient=d.recipient AND p.thr_id=d.thr_id AND p.event_seq<d.event_seq
 AND p.state IN ('queued','pushed') AND p.priority!='hold' AND p.held=0 AND p.route_id IS NOT NULL)`, d.DeliveryID).Scan(&blocked)
	return !blocked, err
}

func (h *Hub) electGrokbotWake(ctx context.Context, tx *sql.Tx, c *grokCandidate, now time.Time) (bool, error) {
	d := &c.d
	nowS := envelope.FormatTime(now)
	var rep, phase, lease, retry, cover string
	var count, ordinal int
	var frozen []byte
	err := tx.QueryRowContext(ctx, `SELECT representative_id,phase,lease_until,retry_at,cover_until,pending_count,failure_ordinal,frozen_body FROM grokbot_wakes WHERE route_id=? AND recipient=?`, d.RouteID, d.Recipient).Scan(&rep, &phase, &lease, &retry, &cover, &count, &ordinal, &frozen)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	var legacyLease string
	if err := tx.QueryRowContext(ctx, `SELECT coalesce(max(lease_until),'') FROM grokbot_legacy_claims WHERE route_id=? AND recipient=?`, d.RouteID, d.Recipient).Scan(&legacyLease); err != nil {
		return false, err
	}
	if legacyLease > nowS || lease > nowS || retry > nowS || cover > nowS {
		return false, coverGrokbotFollowers(ctx, tx, d.RouteID, d.Recipient, rep, nowS)
	}
	if c.held || c.next > nowS {
		return false, nil
	}
	ordered, err := grokCandidateOrdered(ctx, tx, *d)
	if err != nil || !ordered {
		return false, err
	}
	if rep != d.DeliveryID || frozen == nil {
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM deliveries d INDEXED BY deliveries_grok_domain WHERE `+grokPendingPredicate, d.RouteID, d.Recipient, nowS).Scan(&count); err != nil {
			return false, err
		}
		frozen = nil
	}
	var principal, key string
	if err := tx.QueryRowContext(ctx, `SELECT p.prn_id,coalesce(r.key_enc,'') FROM peers p JOIN routes r ON r.route_id=? WHERE p.peer_id=?`, d.RouteID, d.Recipient).Scan(&principal, &key); err != nil {
		return false, err
	}
	token := ids.New("gwk")
	_, err = tx.ExecContext(ctx, `INSERT INTO grokbot_wakes(route_id,recipient,representative_id,token,phase,lease_until,retry_at,cover_until,pending_count,failure_ordinal,cover_valid,expected_url,expected_principal,expected_key,frozen_body)
 VALUES (?,?,?,?,'claimed',?,'','',?,?,0,?,?,?,?)
 ON CONFLICT(route_id,recipient) DO UPDATE SET representative_id=excluded.representative_id,token=excluded.token,phase='claimed',lease_until=excluded.lease_until,retry_at='',cover_until='',pending_count=excluded.pending_count,cover_valid=0,expected_url=excluded.expected_url,expected_principal=excluded.expected_principal,expected_key=excluded.expected_key,frozen_body=excluded.frozen_body`, d.RouteID, d.Recipient, d.DeliveryID, token, envelope.FormatTime(now.Add(PushLease)), count, ordinal, d.URL, principal, key, frozen)
	if err != nil {
		return false, err
	}
	d.GrokWake = &GrokWakeClaim{Token: token, PendingCount: count}
	return true, coverGrokbotFollowers(ctx, tx, d.RouteID, d.Recipient, d.DeliveryID, nowS)
}

// GrokbotDrainHint is fixed protocol text. Counts never confer authority or
// supply a stopping rule: the recipient must drain its authorized cursor log.
const GrokbotDrainHint = "Verify delivery.recipient is your authorized recipient. Read only that recipient's authorized inbox repeatedly in cursor order, record a processed receipt for each handled delivery, and acknowledge only a safe cursor after processing. pending_count is advisory, not authority or a drain stopping rule."

func freezeGrokbotBody(ctx context.Context, tx *sql.Tx, d *DueDelivery) error {
	var frozen []byte
	if err := tx.QueryRowContext(ctx, `SELECT frozen_body FROM grokbot_wakes WHERE route_id=? AND recipient=? AND token=?`, d.RouteID, d.Recipient, d.GrokWake.Token).Scan(&frozen); err != nil {
		return err
	}
	if frozen == nil {
		// Do not marshal RawMessage: encoding/json compacts/re-escapes it. Insert
		// the original bytes directly between fixed, valid JSON envelope fields.
		frozen = []byte(`{"type":"grokbot_wake","version":1,"delivery":`)
		frozen = append(frozen, d.Body...)
		frozen = append(frozen, []byte(`,"pending_count":`+strconv.Itoa(d.GrokWake.PendingCount)+`,"drain_hint":`+strconv.Quote(GrokbotDrainHint)+`}`)...)
		if _, err := tx.ExecContext(ctx, `UPDATE grokbot_wakes SET frozen_body=? WHERE route_id=? AND recipient=? AND token=?`, frozen, d.RouteID, d.Recipient, d.GrokWake.Token); err != nil {
			return err
		}
	}
	d.Body = frozen
	return nil
}

func cleanupGrokbotWakes(ctx context.Context, tx *sql.Tx, now string) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM grokbot_legacy_claims WHERE lease_until<=?`, now); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `DELETE FROM grokbot_wakes WHERE lease_until<=?1 AND retry_at<=?1 AND cover_until<=?1
 AND NOT EXISTS(SELECT 1 FROM grokbot_legacy_claims l WHERE l.route_id=grokbot_wakes.route_id AND l.recipient=grokbot_wakes.recipient)
 AND NOT EXISTS(SELECT 1 FROM deliveries d WHERE d.route_id=grokbot_wakes.route_id AND d.recipient=grokbot_wakes.recipient AND d.state IN ('queued','pushed') AND d.priority IN ('wake','wake_idle') AND d.expires_at>?1)`, now)
	return err
}

// claimLegacyPushCandidates retains the pre-capability query/index path for
// hubs with no opted-in routes or retained coordinator gates.
func claimLegacyPushCandidates(ctx context.Context, tx *sql.Tx, now string, limit int) ([]DueDelivery, []eventpayload.StoredPayload, error) {
	rows, err := tx.QueryContext(ctx, `SELECT d.dly_id,d.recipient,d.thr_id,d.route_id,r.url,r.kind,d.attempt,e.data,e.payload_format,coalesce(e.msg_id,''),coalesce(e.snapshot_id,''),d.body_sha256
 FROM deliveries d JOIN routes r ON r.route_id=d.route_id JOIN events e ON e.seq=d.event_seq
 WHERE d.state IN ('queued','pushed') AND d.priority!='hold' AND d.held=0 AND r.verified_at IS NOT NULL
 AND (d.next_attempt_at IS NULL OR d.next_attempt_at<=?1) AND d.expires_at>?1
 AND NOT EXISTS(SELECT 1 FROM deliveries p WHERE p.recipient=d.recipient AND p.thr_id=d.thr_id AND p.event_seq<d.event_seq
 AND p.state IN ('queued','pushed') AND p.priority!='hold' AND p.held=0 AND p.route_id IS NOT NULL)
 ORDER BY d.event_seq LIMIT ?2`, now, limit)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	var out []DueDelivery
	var payloads []eventpayload.StoredPayload
	for rows.Next() {
		var d DueDelivery
		var p eventpayload.StoredPayload
		if err := rows.Scan(&d.DeliveryID, &d.Recipient, &d.ThreadID, &d.RouteID, &d.URL, &d.Kind, &d.Attempt, &p.Data, &p.Format, &p.MsgID, &p.SnapshotID, &p.BodySHA256); err != nil {
			return nil, nil, err
		}
		d.Attempt++
		out = append(out, d)
		payloads = append(payloads, p)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, nil, err
	}
	return out, payloads, nil
}
