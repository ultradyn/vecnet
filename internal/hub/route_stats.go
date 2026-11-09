package hub

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/XertroV/vecnet/internal/auth"
	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/opapi"
	"github.com/XertroV/vecnet/internal/store"
)

// RouteStats reads one snapshot using the operator credential. A peer's kind,
// current enabled state or ownership never grants this authority.
func (h *Hub) RouteStats(ctx context.Context, caller auth.Caller, id string) (opapi.RouteStats, error) {
	if caller.Kind != "operator" {
		return opapi.RouteStats{}, E(http.StatusForbidden, "forbidden", "route statistics require the operator credential")
	}
	kind, valid := routeStatsSelector(id)
	if !valid {
		e := E(http.StatusUnprocessableEntity, "invalid_value", "id must be a peer, route or subscription ID")
		e.Details["field"] = "id"
		return opapi.RouteStats{}, e
	}
	// Freeze time before even the first subject lookup: concurrent writers or a
	// clock change cannot mix populations or metadata from different snapshots.
	now := h.Now()
	out := opapi.RouteStats{
		Subject: opapi.RouteStatsSubject{Kind: kind, ID: id},
		Window: opapi.RouteStatsWindow{Since: envelope.FormatTime(now.Add(-168 * time.Hour)),
			Until: envelope.FormatTime(now), MaxRows: 1000, MaxAgeHours: 168},
		RetainedDeliveries: opapi.RouteStatsRetainedDeliveries{PeerIDs: []string{}},
		Routes:             []opapi.RouteStatsRow{},
	}
	err := h.DB.ReadSnapshot(ctx, func(q *sql.Conn) error {
		if err := routeStatsSubject(ctx, q, &out); err != nil {
			return err
		}
		for i := range out.Routes {
			attempts, err := store.RouteAttempts(ctx, q, out.Routes[i].RouteID, out.Window.Since, out.Window.Until, out.Window.MaxRows)
			if err != nil {
				return err
			}
			applyRouteStats(&out.Routes[i], attempts)
		}
		return routeStatsDeliveryContext(ctx, q, &out)
	})
	if err != nil {
		// Neither partially collected fields nor driver/SQL text cross a read
		// boundary. Keep non-API failures ordinary errors for MCP's -32603.
		var api *APIError
		if errors.As(err, &api) {
			return opapi.RouteStats{}, api
		}
		return opapi.RouteStats{}, errors.New("hub: route statistics read failed")
	}
	return out, nil
}

func routeStatsSelector(id string) (string, bool) {
	if len(id) > 80 {
		return "", false
	}
	prefix, tail, ok := strings.Cut(id, "_")
	if !ok || tail != strings.ToUpper(tail) {
		return "", false
	}
	var kind string
	switch prefix {
	case "peer", "route":
		kind = prefix
	case "sub":
		kind = "subscription"
	default:
		return "", false
	}
	_, err := ulid.ParseStrict(tail)
	return kind, err == nil
}

// routeStatsSubject resolves current metadata and context on the same pinned
// connection used for samples/counts. Every result set closes before returning
// or starting another query, including empty and failed lookups.
func routeStatsSubject(ctx context.Context, q *sql.Conn, out *opapi.RouteStats) error {
	id := out.Subject.ID
	var query string
	var args []any
	switch out.Subject.Kind {
	case "peer":
		var principal string
		if err := q.QueryRowContext(ctx, "SELECT prn_id FROM peers WHERE peer_id=?", id).Scan(&principal); err != nil {
			return routeStatsLookupError(err)
		}
		out.RetainedDeliveries.PeerIDs = append(out.RetainedDeliveries.PeerIDs, id)
		query = `SELECT route_id,kind,url,owner_kind,owner_id FROM routes
			WHERE (owner_kind='peer' AND owner_id=?) OR (owner_kind='principal' AND owner_id=?)
			UNION ALL SELECT sub_id,'events',url,'peer',peer_id FROM event_subscriptions WHERE peer_id=?
			ORDER BY 1`
		args = []any{id, principal, id}
	case "route":
		query = "SELECT route_id,kind,url,owner_kind,owner_id FROM routes WHERE route_id=?"
		args = []any{id}
	case "subscription":
		query = "SELECT sub_id,'events',url,'peer',peer_id FROM event_subscriptions WHERE sub_id=?"
		args = []any{id}
	}
	if err := routeStatsMetadata(ctx, q, out, query, args); err != nil {
		return err
	}
	if out.Subject.Kind == "peer" {
		return nil
	}
	if len(out.Routes) == 0 {
		return routeStatsLookupError(sql.ErrNoRows)
	}
	row := out.Routes[0]
	if out.Subject.Kind == "subscription" {
		out.RetainedDeliveries.PeerIDs = append(out.RetainedDeliveries.PeerIDs, row.OwnerID)
		return nil
	}
	// UNION deduplicates the owner when it is also a current route member.
	peers, err := q.QueryContext(ctx, `SELECT peer_id FROM peers WHERE route_id=?
		UNION SELECT ? WHERE ?='peer' ORDER BY 1`, id, row.OwnerID, row.OwnerKind)
	if err != nil {
		return err
	}
	defer peers.Close()
	for peers.Next() {
		var peer string
		if err := peers.Scan(&peer); err != nil {
			return err
		}
		out.RetainedDeliveries.PeerIDs = append(out.RetainedDeliveries.PeerIDs, peer)
	}
	return peers.Err()
}

func routeStatsMetadata(ctx context.Context, q *sql.Conn, out *opapi.RouteStats, query string, args []any) error {
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		row := opapi.RouteStatsRow{AttemptScope: "route"}
		var rawURL string
		if err := rows.Scan(&row.RouteID, &row.Kind, &rawURL, &row.OwnerKind, &row.OwnerID); err != nil {
			return err
		}
		if parsed, err := url.Parse(rawURL); err == nil {
			row.Host = parsed.Hostname()
		}
		out.Routes = append(out.Routes, row)
	}
	return rows.Err()
}

func routeStatsLookupError(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return E(http.StatusNotFound, "not_found", "route statistics subject not found")
	}
	return err
}

func applyRouteStats(row *opapi.RouteStatsRow, attempts []store.RouteAttempt) {
	row.Requests = len(attempts)
	distinct := make(map[string]struct{}, len(attempts))
	body, latency := make([]int, 0, len(attempts)), make([]int, 0, len(attempts))
	for i, a := range attempts {
		if i == 0 {
			row.LastUsedAt = &a.At
			row.LastStatus = a.HTTPStatus
		}
		switch a.Outcome {
		case "ok":
			row.OK++
			if row.LastSuccessAt == nil {
				row.LastSuccessAt = &a.At
			}
		case "fail":
			row.Fail++
		case "refused":
			row.Refused++
		}
		if a.Outcome != "ok" && row.LastErrorAt == nil {
			row.LastError, row.LastErrorAt = a.ErrorClass, &a.At
		}
		distinct[a.DeliveryID] = struct{}{}
		body, latency = append(body, a.BodyBytes), append(latency, a.LatencyMS)
	}
	row.DistinctIDs = len(distinct)
	row.BodyBytes, row.LatencyMS = routeStatsDistribution(body), routeStatsDistribution(latency)
}

func routeStatsDistribution(values []int) opapi.RouteStatsDistribution {
	out := opapi.RouteStatsDistribution{Samples: len(values)}
	if len(values) == 0 {
		return out
	}
	sort.Ints(values)
	var sum float64
	for _, value := range values {
		sum += float64(value)
	}
	avg := math.Round(sum/float64(len(values))*1000) / 1000
	p95 := float64(values[(95*len(values)+99)/100-1])
	p99 := float64(values[(99*len(values)+99)/100-1])
	out.Avg, out.P95, out.P99 = &avg, &p95, &p99
	return out
}

func routeStatsDeliveryContext(ctx context.Context, q *sql.Conn, out *opapi.RouteStats) error {
	if len(out.RetainedDeliveries.PeerIDs) == 0 {
		return nil
	}
	// json_each avoids an unbounded parameter list for shared routes. Delivery
	// IDs are unique in deliveries; EXISTS counts their uncapped in-window
	// evidence once, irrespective of retries, subscriptions or selected caps.
	peers, err := json.Marshal(out.RetainedDeliveries.PeerIDs)
	if err != nil {
		return err
	}
	return q.QueryRowContext(ctx, `WITH context AS (
		SELECT state, (attempt>0 OR EXISTS (
			SELECT 1 FROM route_attempts a WHERE a.delivery_id=d.dly_id AND a.at>=? AND a.at<=?
		)) AS evidence FROM deliveries d
		WHERE recipient IN (SELECT value FROM json_each(?)) AND updated_at>=? AND updated_at<=?
	) SELECT count(*),coalesce(sum(evidence),0),coalesce(sum(state='accepted' AND NOT evidence),0) FROM context`,
		out.Window.Since, out.Window.Until, string(peers), out.Window.Since, out.Window.Until).
		Scan(&out.RetainedDeliveries.Total, &out.RetainedDeliveries.WithPushEvidence, &out.RetainedDeliveries.AcceptedWithoutPushEvidence)
}
