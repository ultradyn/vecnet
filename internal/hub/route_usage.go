package hub

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/store"
)

// Route attempt stats (PROTOCOL §3, §4.4, §5.2, §13.3).
//
// An attempt row is written inside the delivery's outcome transaction when
// that transaction exists. A failed insert is logged and does not fail the
// delivery. Counts and percentiles are computed at read time over the rows
// still inside the retention window: the newest 1000 per route, and nothing
// older than 7 days.

const routeAttemptMaxAge = 7 * 24 * time.Hour

// RouteKindEvents is an MCP Events callback. It is not a routes.kind.
const RouteKindEvents = "events"

// Attempt outcomes stored on route_attempts.outcome.
const (
	AttemptOK      = "ok"
	AttemptFail    = "fail"
	AttemptRefused = "refused"
)

// RouteStats is the operator readout for one peer or one route.
type RouteStats struct {
	Subject    RouteStatsSubject `json:"subject"`
	Window     RouteStatsWindow  `json:"window"`
	Deliveries DeliveryTraffic   `json:"deliveries"`
	Routes     []RouteStat       `json:"routes"`
}

// RouteStatsSubject is what the id resolved to.
type RouteStatsSubject struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

// RouteStatsWindow is the retention window the numbers were computed over.
type RouteStatsWindow struct {
	Since       string `json:"since"`
	Until       string `json:"until"`
	MaxRows     int    `json:"max_rows"`
	MaxAgeHours int    `json:"max_age_hours"`
}

// DeliveryTraffic counts deliveries to the peer or peers in scope.
// Pushed is how many a route handed to an outbound attempt. Pulled is how
// many were accepted from the inbox, MCP or a stream without one.
type DeliveryTraffic struct {
	Pushed int `json:"pushed"`
	Pulled int `json:"pulled"`
}

// RouteStat is one route's attempt stats over the window.
type RouteStat struct {
	RouteID       string         `json:"route_id"`
	Kind          string         `json:"kind"`
	Host          string         `json:"host"`
	OwnerKind     string         `json:"owner_kind"`
	OwnerID       string         `json:"owner_id"`
	Requests      int            `json:"requests"`
	OK            int            `json:"ok"`
	Fail          int            `json:"fail"`
	Refused       int            `json:"refused"`
	Pushed        int            `json:"pushed"`
	LastError     *string        `json:"last_error"`
	LastErrorAt   *string        `json:"last_error_at"`
	LastStatus    *int           `json:"last_status"`
	LastUsedAt    *string        `json:"last_used_at"`
	LastSuccessAt *string        `json:"last_success_at"`
	BodyBytes     RouteUsageDist `json:"body_bytes"`
	LatencyMS     RouteUsageDist `json:"latency_ms"`
}

// RouteUsageDist is avg and nearest-rank p95/p99 over the window.
type RouteUsageDist struct {
	Avg     float64 `json:"avg"`
	P95     float64 `json:"p95"`
	P99     float64 `json:"p99"`
	Samples int     `json:"samples"`
}

// PercentileNearestRank is the nearest-rank percentile of an ascending
// sample. p is in (0, 100]. Rank is ceil(p/100*n). An empty sample returns
// 0, false.
func PercentileNearestRank(sorted []int64, p float64) (int64, bool) {
	n := len(sorted)
	if n == 0 || p <= 0 || p > 100 || math.IsNaN(p) {
		return 0, false
	}
	rank := int(math.Ceil(p / 100 * float64(n)))
	if rank < 1 {
		rank = 1
	}
	if rank > n {
		rank = n
	}
	return sorted[rank-1], true
}

// notePushAttempt records d's HTTP attempt inside tx. It logs and swallows
// a store error so the delivery outcome still commits.
func (h *Hub) notePushAttempt(ctx context.Context, tx *sql.Tx, d DueDelivery, outcome pushOutcome, status int, netErr error, now time.Time) {
	if !d.RecordAttempt || d.RouteID == "" {
		return
	}
	row := attemptFromPush(d, outcome, status, netErr, now)
	if err := store.InsertRouteAttempt(ctx, tx, row); err != nil {
		slog.Error("hub: route attempt", "route", d.RouteID, "delivery", d.DeliveryID, "err", err)
	}
}

// NoteEventAttempt records one MCP Events callback POST inside tx.
// A store error is logged and swallowed.
func (h *Hub) NoteEventAttempt(ctx context.Context, tx *sql.Tx, subID, eventID string, body int, latency time.Duration, status int, netErr error, now time.Time) {
	if subID == "" || eventID == "" {
		return
	}
	outcome, class := classifyHTTP(status, netErr)
	row := attemptRow(subID, eventID, outcome, class, status, netErr, body, latency, now)
	if err := store.InsertRouteAttempt(ctx, tx, row); err != nil {
		slog.Error("hub: route attempt", "route", subID, "delivery", eventID, "err", err)
	}
}

func attemptFromPush(d DueDelivery, outcome pushOutcome, status int, netErr error, now time.Time) store.RouteAttemptInsert {
	result, class := classifyHTTP(status, netErr)
	switch outcome {
	case pushAccepted:
		result, class = AttemptOK, ""
	case pushAuthFailed:
		result, class = AttemptRefused, "routine_auth"
	case pushConflict:
		result, class = AttemptRefused, "http_409"
	case pushTerminal:
		result = AttemptRefused
		if d.Kind == RouteKindGrokbot && netErr == nil && status == http.StatusOK {
			class = "routine_not_started"
		} else if class == "" {
			class = httpClass(status, AttemptRefused)
		}
	case pushRetry:
		result = AttemptFail
		if class == "" {
			class = httpClass(status, AttemptFail)
		}
	}
	return attemptRow(d.RouteID, d.DeliveryID, result, class, status, netErr, d.AttemptBody, d.AttemptLatency, now)
}

func classifyHTTP(status int, netErr error) (outcome, class string) {
	if netErr != nil {
		return AttemptFail, fixedClass(EgressErr(netErr))
	}
	if status >= 200 && status < 300 {
		return AttemptOK, ""
	}
	if status == http.StatusRequestTimeout || status == http.StatusTooManyRequests || (status >= 500 && status < 600) {
		return AttemptFail, httpClass(status, AttemptFail)
	}
	return AttemptRefused, httpClass(status, AttemptRefused)
}

func httpClass(status int, outcome string) string {
	switch {
	case status == http.StatusConflict:
		return "http_409"
	case status == http.StatusRequestTimeout:
		return "http_408"
	case status == http.StatusTooManyRequests:
		return "http_429"
	case status >= 500 && status < 600:
		return "http_5xx"
	case status >= 400 && status < 500:
		return "http_4xx"
	case status >= 300 && status < 400:
		return "http_3xx"
	case outcome == AttemptFail:
		return "request_failed"
	default:
		return "http_status"
	}
}

func fixedClass(s string) string {
	if s == "" {
		return "request_failed"
	}
	for _, r := range s {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '_' {
			return "request_failed"
		}
	}
	if len(s) > 64 {
		return "request_failed"
	}
	return s
}

func attemptRow(routeID, deliveryID, outcome, class string, status int, netErr error, body int, latency time.Duration, now time.Time) store.RouteAttemptInsert {
	if body < 0 {
		body = 0
	}
	if body > 1<<20 {
		body = 1 << 20
	}
	ms := int(latency.Round(time.Millisecond) / time.Millisecond)
	if ms < 0 {
		ms = 0
	}
	if ms > 600000 {
		ms = 600000
	}
	var httpStatus sql.NullInt64
	if netErr == nil && status >= 100 && status <= 599 {
		httpStatus = sql.NullInt64{Int64: int64(status), Valid: true}
	}
	var errClass sql.NullString
	if outcome != AttemptOK {
		if class == "" {
			class = "request_failed"
		}
		errClass = sql.NullString{String: class, Valid: true}
	}
	return store.RouteAttemptInsert{
		RouteID: routeID, DeliveryID: deliveryID,
		At: envelope.FormatTime(now), Cutoff: envelope.FormatTime(now.Add(-routeAttemptMaxAge)),
		Outcome: outcome, HTTPStatus: httpStatus, ErrorClass: errClass,
		BodyBytes: body, LatencyMS: ms,
	}
}

// RouteStats reads attempt rows and delivery rows for id, a peer_ id, a
// route_ id, or a sub_ id. Unknown ids are 404 not_found.
func (h *Hub) RouteStats(ctx context.Context, id string) (RouteStats, error) {
	now := h.Now()
	since := now.Add(-routeAttemptMaxAge)
	sinceS := envelope.FormatTime(since)
	report := RouteStats{
		Window: RouteStatsWindow{
			Since: sinceS, Until: envelope.FormatTime(now),
			MaxRows: store.RouteAttemptKeep, MaxAgeHours: int(routeAttemptMaxAge / time.Hour),
		},
		Routes: []RouteStat{},
	}
	switch {
	case strings.HasPrefix(id, "peer_"):
		report.Subject = RouteStatsSubject{Kind: "peer", ID: id}
		var one int
		err := h.DB.QueryRowContext(ctx, `SELECT 1 FROM peers WHERE peer_id = ?`, id).Scan(&one)
		if errors.Is(err, sql.ErrNoRows) {
			return RouteStats{}, unknownPeer(id)
		}
		if err != nil {
			return RouteStats{}, fmt.Errorf("hub: route stats: %w", err)
		}
		routes, err := h.statsRoutesForPeer(ctx, id)
		if err != nil {
			return RouteStats{}, err
		}
		report.Routes = routes
		pushed, pulled, err := h.deliveryTraffic(ctx, []string{id}, sinceS)
		if err != nil {
			return RouteStats{}, err
		}
		report.Deliveries = DeliveryTraffic{Pushed: pushed, Pulled: pulled}
		return h.fillRouteStats(ctx, report, sinceS)
	case strings.HasPrefix(id, "route_"):
		report.Subject = RouteStatsSubject{Kind: "route", ID: id}
		route, peers, err := h.statsRoute(ctx, id)
		if err != nil {
			return RouteStats{}, err
		}
		report.Routes = []RouteStat{route}
		pushed, pulled, err := h.deliveryTraffic(ctx, peers, sinceS)
		if err != nil {
			return RouteStats{}, err
		}
		report.Deliveries = DeliveryTraffic{Pushed: pushed, Pulled: pulled}
		return h.fillRouteStats(ctx, report, sinceS)
	case strings.HasPrefix(id, "sub_"):
		report.Subject = RouteStatsSubject{Kind: "route", ID: id}
		route, peer, err := h.statsSubscription(ctx, id)
		if err != nil {
			return RouteStats{}, err
		}
		report.Routes = []RouteStat{route}
		peers := []string{}
		if peer != "" {
			peers = []string{peer}
		}
		pushed, pulled, err := h.deliveryTraffic(ctx, peers, sinceS)
		if err != nil {
			return RouteStats{}, err
		}
		report.Deliveries = DeliveryTraffic{Pushed: pushed, Pulled: pulled}
		return h.fillRouteStats(ctx, report, sinceS)
	default:
		return RouteStats{}, invalidRouteField("id", "id is a peer_, route_ or sub_ id")
	}
}

func (h *Hub) statsRoutesForPeer(ctx context.Context, peerID string) ([]RouteStat, error) {
	var prn string
	if err := h.DB.QueryRowContext(ctx, `SELECT prn_id FROM peers WHERE peer_id = ?`, peerID).Scan(&prn); err != nil {
		return nil, fmt.Errorf("hub: route stats: %w", err)
	}
	rows, err := h.DB.QueryContext(ctx, `
		SELECT route_id, kind, url, owner_kind, owner_id FROM routes
		WHERE (owner_kind = 'peer' AND owner_id = ?)
		   OR (owner_kind = 'principal' AND owner_id = ?)
		ORDER BY created_at, route_id`, peerID, prn)
	if err != nil {
		return nil, fmt.Errorf("hub: route stats: %w", err)
	}
	defer rows.Close()
	var out []RouteStat
	for rows.Next() {
		var id, kind, raw, ownerKind, ownerID string
		if err := rows.Scan(&id, &kind, &raw, &ownerKind, &ownerID); err != nil {
			return nil, fmt.Errorf("hub: route stats: %w", err)
		}
		out = append(out, RouteStat{RouteID: id, Kind: kind, Host: hostOf(raw), OwnerKind: ownerKind, OwnerID: ownerID})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("hub: route stats: %w", err)
	}
	subs, err := h.DB.QueryContext(ctx, `SELECT sub_id, url FROM event_subscriptions WHERE peer_id = ? ORDER BY sub_id`, peerID)
	if err != nil {
		return nil, fmt.Errorf("hub: route stats: %w", err)
	}
	defer subs.Close()
	for subs.Next() {
		var id, raw string
		if err := subs.Scan(&id, &raw); err != nil {
			return nil, fmt.Errorf("hub: route stats: %w", err)
		}
		out = append(out, RouteStat{RouteID: id, Kind: RouteKindEvents, Host: hostOf(raw), OwnerKind: "peer", OwnerID: peerID})
	}
	if err := subs.Err(); err != nil {
		return nil, fmt.Errorf("hub: route stats: %w", err)
	}
	if out == nil {
		out = []RouteStat{}
	}
	return out, nil
}

func (h *Hub) statsRoute(ctx context.Context, id string) (RouteStat, []string, error) {
	var kind, raw, ownerKind, ownerID string
	err := h.DB.QueryRowContext(ctx, `SELECT kind, url, owner_kind, owner_id FROM routes WHERE route_id = ?`, id).
		Scan(&kind, &raw, &ownerKind, &ownerID)
	if errors.Is(err, sql.ErrNoRows) {
		return RouteStat{}, nil, unknownRoute(id)
	}
	if err != nil {
		return RouteStat{}, nil, fmt.Errorf("hub: route stats: %w", err)
	}
	peers, err := h.routePeers(ctx, id, ownerKind, ownerID)
	if err != nil {
		return RouteStat{}, nil, err
	}
	return RouteStat{RouteID: id, Kind: kind, Host: hostOf(raw), OwnerKind: ownerKind, OwnerID: ownerID}, peers, nil
}

func (h *Hub) routePeers(ctx context.Context, routeID, ownerKind, ownerID string) ([]string, error) {
	q := `SELECT peer_id FROM peers WHERE route_id = ? ORDER BY peer_id`
	args := []any{routeID}
	if ownerKind == "peer" {
		q = `SELECT peer_id FROM peers WHERE peer_id = ? OR route_id = ? ORDER BY peer_id`
		args = []any{ownerID, routeID}
	}
	rows, err := h.DB.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("hub: route stats: %w", err)
	}
	defer rows.Close()
	var peers []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("hub: route stats: %w", err)
		}
		peers = append(peers, id)
	}
	return peers, rows.Err()
}

func (h *Hub) statsSubscription(ctx context.Context, id string) (RouteStat, string, error) {
	var peer, raw string
	err := h.DB.QueryRowContext(ctx, `SELECT peer_id, url FROM event_subscriptions WHERE sub_id = ?`, id).Scan(&peer, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return RouteStat{}, "", unknownRoute(id)
	}
	if err != nil {
		return RouteStat{}, "", fmt.Errorf("hub: route stats: %w", err)
	}
	return RouteStat{RouteID: id, Kind: RouteKindEvents, Host: hostOf(raw), OwnerKind: "peer", OwnerID: peer}, peer, nil
}

func hostOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return u.Host
}

func (h *Hub) fillRouteStats(ctx context.Context, report RouteStats, since string) (RouteStats, error) {
	for i := range report.Routes {
		rows, err := store.RouteAttempts(ctx, h.DB.DB, report.Routes[i].RouteID, since, store.RouteAttemptKeep)
		if err != nil {
			return RouteStats{}, err
		}
		report.Routes[i] = applyAttempts(report.Routes[i], rows)
		n, err := h.pushedByRoute(ctx, report.Routes[i].RouteID, since)
		if err != nil {
			return RouteStats{}, err
		}
		report.Routes[i].Pushed = n
	}
	return report, nil
}

func applyAttempts(stat RouteStat, rows []store.RouteAttempt) RouteStat {
	stat.Requests = len(rows)
	var body, lat []int64
	for _, row := range rows {
		switch row.Outcome {
		case AttemptOK:
			stat.OK++
		case AttemptRefused:
			stat.Refused++
		default:
			stat.Fail++
		}
		body = append(body, int64(row.BodyBytes))
		lat = append(lat, int64(row.LatencyMS))
	}
	// rows are newest first.
	if len(rows) > 0 {
		used := rows[0].At
		stat.LastUsedAt = &used
		if rows[0].HTTPStatus.Valid {
			s := int(rows[0].HTTPStatus.Int64)
			stat.LastStatus = &s
		}
	}
	for _, row := range rows {
		if row.Outcome == AttemptOK {
			ok := row.At
			stat.LastSuccessAt = &ok
			break
		}
	}
	for _, row := range rows {
		if row.Outcome != AttemptOK && row.ErrorClass.Valid {
			class := row.ErrorClass.String
			at := row.At
			stat.LastError = &class
			stat.LastErrorAt = &at
			break
		}
	}
	stat.BodyBytes = usageDist(body, 1)
	stat.LatencyMS = usageDist(lat, 1)
	return stat
}

func usageDist(values []int64, scale float64) RouteUsageDist {
	if len(values) == 0 || scale == 0 {
		return RouteUsageDist{}
	}
	sorted := append([]int64(nil), values...)
	slices.Sort(sorted)
	var sum int64
	for _, v := range sorted {
		sum += v
	}
	p95, _ := PercentileNearestRank(sorted, 95)
	p99, _ := PercentileNearestRank(sorted, 99)
	return RouteUsageDist{
		Avg:     round3(float64(sum) / float64(len(sorted)) / scale),
		P95:     round3(float64(p95) / scale),
		P99:     round3(float64(p99) / scale),
		Samples: len(sorted),
	}
}

func round3(v float64) float64 {
	return math.Round(v*1000) / 1000
}

func (h *Hub) pushedByRoute(ctx context.Context, routeID, since string) (int, error) {
	var n int
	err := h.DB.QueryRowContext(ctx, `
		SELECT count(DISTINCT delivery_id) FROM route_attempts
		WHERE route_id = ? AND at >= ?`, routeID, since).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("hub: route stats: %w", err)
	}
	return n, nil
}

func (h *Hub) deliveryTraffic(ctx context.Context, peers []string, since string) (pushed, pulled int, err error) {
	if len(peers) == 0 {
		return 0, 0, nil
	}
	in := "?" + strings.Repeat(",?", len(peers)-1)
	// since, peers..., since, since, peers..., since
	args := make([]any, 0, 4+2*len(peers))
	args = append(args, since)
	for _, p := range peers {
		args = append(args, p)
	}
	args = append(args, since)
	args = append(args, since)
	for _, p := range peers {
		args = append(args, p)
	}
	args = append(args, since)
	err = h.DB.QueryRowContext(ctx, `
		SELECT
		  (SELECT count(*) FROM deliveries
		    WHERE updated_at >= ? AND recipient IN (`+in+`) AND (
		      attempt > 0 OR dly_id IN (SELECT delivery_id FROM route_attempts WHERE at >= ?)
		    )),
		  (SELECT count(*) FROM deliveries
		    WHERE updated_at >= ? AND recipient IN (`+in+`) AND state = 'accepted' AND attempt = 0
		      AND dly_id NOT IN (SELECT delivery_id FROM route_attempts WHERE at >= ?))`,
		args...).Scan(&pushed, &pulled)
	if err != nil {
		return 0, 0, fmt.Errorf("hub: route stats: %w", err)
	}
	return pushed, pulled, nil
}
