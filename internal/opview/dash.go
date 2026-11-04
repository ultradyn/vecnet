package opview

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/hub"
	"github.com/XertroV/vecnet/internal/opapi"
)

// Peers includes disabled peers as well as the enabled public directory.
// Credentials and route keys are never selected; only their status is read.
func (r *Reader) Peers(ctx context.Context) ([]opapi.PeerRow, error) {
	teams := map[string][]string{}
	rows, err := r.Hub.DB.QueryContext(ctx, `SELECT peer_id, team_id FROM team_members ORDER BY peer_id, team_id`)
	if err != nil {
		return nil, fmt.Errorf("opview: peer teams: %w", err)
	}
	for rows.Next() {
		var peer, team string
		if err := rows.Scan(&peer, &team); err != nil {
			rows.Close()
			return nil, fmt.Errorf("opview: peer teams: %w", err)
		}
		teams[peer] = append(teams[peer], team)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// owner ids are globally typed (peer_ or prn_); both route kinds expose
	// only the parsed host. In particular, do not select grokbot key_enc.
	routes := map[string][]opapi.RouteInfo{}
	rows, err = r.Hub.DB.QueryContext(ctx, `SELECT owner_id, route_id, kind, url, verified_at FROM routes ORDER BY created_at, route_id`)
	if err != nil {
		return nil, fmt.Errorf("opview: peer routes: %w", err)
	}
	for rows.Next() {
		var owner, rawURL string
		var route opapi.RouteInfo
		if err := rows.Scan(&owner, &route.RouteID, &route.Kind, &rawURL, &route.VerifiedAt); err != nil {
			rows.Close()
			return nil, fmt.Errorf("opview: peer routes: %w", err)
		}
		// Malformed legacy URLs expose no host, and are never echoed in errors.
		if parsed, err := url.Parse(rawURL); err == nil {
			route.Host = parsed.Host
		}
		routes[owner] = append(routes[owner], route)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	rows, err = r.Hub.DB.QueryContext(ctx, `SELECT p.created_by, p.peer_id, p.prn_id, p.name, p.kind, p.disabled,
  p.capabilities, p.quota, p.location, p.presence_state, p.presence_at,
  p.reachability, p.reach_via, p.last_seen, p.enrolled_at,
  CASE
   WHEN EXISTS (SELECT 1 FROM credentials c WHERE c.subject_id IN (p.peer_id, p.prn_id) AND c.revoked_at IS NULL) THEN 'active'
   WHEN EXISTS (SELECT 1 FROM credentials c WHERE c.subject_id IN (p.peer_id, p.prn_id)) THEN 'revoked'
   ELSE 'none'
  END
  FROM peers p ORDER BY p.peer_id`)
	if err != nil {
		return nil, fmt.Errorf("opview: peers: %w", err)
	}
	out := []opapi.PeerRow{}
	now := r.Hub.Now()
	for rows.Next() {
		var row opapi.PeerRow
		var caps, quota, reach string
		var state *string
		p := &row.Peer
		if err := rows.Scan(&p.CreatedBy, &p.PeerID, &row.PrincipalID, &p.Name, &p.Kind, &row.Disabled,
			&caps, &quota, &p.Location, &state, &row.PresenceAt, &reach, &row.ReachVia, &p.LastSeen, &p.EnrolledAt, &row.Credential); err != nil {
			rows.Close()
			return nil, fmt.Errorf("opview: peers: %w", err)
		}
		if err := json.Unmarshal([]byte(caps), &p.Capabilities); err != nil || p.Capabilities == nil {
			p.Capabilities = []string{}
		}
		dec := json.NewDecoder(strings.NewReader(quota))
		dec.UseNumber()
		if err := dec.Decode(&p.Quota); err != nil || p.Quota == nil {
			p.Quota = map[string]any{}
		}
		p.Presence = r.Hub.EffectivePresence(hub.PresenceRow{Kind: p.Kind, State: state, At: row.PresenceAt}, now)
		p.Reachability = hub.EffectiveReachability(reach, row.ReachVia, p.LastSeen, now)
		p.Teams = teams[p.PeerID]
		if p.Teams == nil {
			p.Teams = []string{}
		}
		row.Routes = append([]opapi.RouteInfo{}, routes[p.PeerID]...)
		row.Routes = append(row.Routes, routes[row.PrincipalID]...)
		out = append(out, row)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Public directory sources are not credential holders or recipients.
	rows, err = r.Hub.DB.QueryContext(ctx, `SELECT src_id, name, created_at FROM sources WHERE disabled = 0 ORDER BY src_id`)
	if err != nil {
		return nil, fmt.Errorf("opview: directory sources: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		row := opapi.PeerRow{Credential: "none", Routes: []opapi.RouteInfo{}, Peer: opapi.Peer{
			Kind: "source", Capabilities: []string{}, Quota: map[string]any{}, Teams: []string{}, Presence: "unknown", Reachability: "unknown",
		}}
		if err := rows.Scan(&row.Peer.PeerID, &row.Peer.Name, &row.Peer.EnrolledAt); err != nil {
			return nil, fmt.Errorf("opview: directory sources: %w", err)
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func (r *Reader) LoopCuts(ctx context.Context, hours int) (opapi.LoopCuts, error) {
	if hours < 1 || hours > 168 {
		err := hub.E(422, "invalid_value", "hours must be between 1 and 168")
		err.Details["field"] = "hours"
		return opapi.LoopCuts{}, err
	}
	now := r.Hub.Now()
	out := opapi.LoopCuts{Since: envelope.FormatTime(now.Add(-time.Duration(hours) * time.Hour)), Hours: hours, ByReason: map[string]int{}, Recent: []opapi.LoopCut{}}
	until := envelope.FormatTime(now)
	rows, err := r.Hub.DB.QueryContext(ctx, `SELECT COALESCE(json_extract(detail, '$.reason'), ''), COUNT(*)
  FROM audit WHERE action = 'loop_cut' AND at >= ? AND at <= ? GROUP BY json_extract(detail, '$.reason')`, out.Since, until)
	if err != nil {
		return out, fmt.Errorf("opview: loop cut counts: %w", err)
	}
	for rows.Next() {
		var reason string
		var count int
		if err := rows.Scan(&reason, &count); err != nil {
			rows.Close()
			return out, fmt.Errorf("opview: loop cut counts: %w", err)
		}
		out.ByReason[reason] = count
		out.Total += count
	}
	if err := rows.Close(); err != nil {
		return out, err
	}
	if err := rows.Err(); err != nil {
		return out, err
	}
	rows, err = r.Hub.DB.QueryContext(ctx, `SELECT at, COALESCE(json_extract(target_ids, '$[0]'), ''), actor, COALESCE(json_extract(detail, '$.reason'), '')
  FROM audit WHERE action = 'loop_cut' AND at >= ? AND at <= ? ORDER BY at DESC, id DESC LIMIT 50`, out.Since, until)
	if err != nil {
		return out, fmt.Errorf("opview: recent loop cuts: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var cut opapi.LoopCut
		if err := rows.Scan(&cut.At, &cut.ThreadID, &cut.Actor, &cut.Reason); err != nil {
			return out, fmt.Errorf("opview: recent loop cuts: %w", err)
		}
		out.Recent = append(out.Recent, cut)
	}
	if err := rows.Err(); err != nil {
		return out, err
	}
	return out, nil
}

func (r *Reader) DeliveryHealth(ctx context.Context) (opapi.DeliveryHealth, error) {
	out := opapi.DeliveryHealth{Blocked: []opapi.BlockedThread{}, Failed: []opapi.FailedDelivery{}}
	// Mirror the predecessor predicate in hub.ClaimDue, not its due-time,
	// verification, or expiry gates: a backed-off, in-flight, unverified or
	// not-yet-expired-by-the-ticker predecessor still serializes the thread.
	rows, err := r.Hub.DB.QueryContext(ctx, `WITH pushable AS (
  SELECT * FROM deliveries WHERE state IN ('queued', 'pushed')
   AND priority != 'hold' AND held = 0 AND route_id IS NOT NULL
 )
 SELECT d.recipient, d.thr_id, d.dly_id, d.attempt,
  (SELECT COUNT(*) FROM pushable w WHERE w.recipient = d.recipient AND w.thr_id = d.thr_id AND w.event_seq > d.event_seq),
  d.next_attempt_at, d.last_status, d.last_error
 FROM pushable d WHERE d.attempt >= 1 AND NOT EXISTS (
  SELECT 1 FROM pushable p WHERE p.recipient = d.recipient AND p.thr_id = d.thr_id AND p.event_seq < d.event_seq
 ) ORDER BY d.recipient, d.thr_id, d.event_seq`)
	if err != nil {
		return out, fmt.Errorf("opview: blocked deliveries: %w", err)
	}
	for rows.Next() {
		var blocked opapi.BlockedThread
		if err := rows.Scan(&blocked.Recipient, &blocked.ThreadID, &blocked.DeliveryID, &blocked.Attempt, &blocked.Waiting, &blocked.NextAttemptAt, &blocked.LastStatus, &blocked.LastError); err != nil {
			rows.Close()
			return out, fmt.Errorf("opview: blocked deliveries: %w", err)
		}
		out.Blocked = append(out.Blocked, blocked)
	}
	if err := rows.Close(); err != nil {
		return out, err
	}
	if err := rows.Err(); err != nil {
		return out, err
	}
	since := envelope.FormatTime(r.Hub.Now().Add(-7 * 24 * time.Hour))
	rows, err = r.Hub.DB.QueryContext(ctx, `SELECT dly_id, recipient, thr_id, updated_at, last_status, last_error
  FROM deliveries WHERE state = 'failed' AND updated_at >= ? ORDER BY updated_at DESC, dly_id DESC LIMIT 100`, since)
	if err != nil {
		return out, fmt.Errorf("opview: failed deliveries: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var failed opapi.FailedDelivery
		if err := rows.Scan(&failed.DeliveryID, &failed.Recipient, &failed.ThreadID, &failed.UpdatedAt, &failed.LastStatus, &failed.LastError); err != nil {
			return out, fmt.Errorf("opview: failed deliveries: %w", err)
		}
		out.Failed = append(out.Failed, failed)
	}
	if err := rows.Err(); err != nil {
		return out, err
	}
	return out, nil
}
