package hub

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/XertroV/vecnet/internal/envelope"
)

// Route usage (PROTOCOL §3, §4.4, §5.2, §13.3).
//
// A delivery route is one outbound push target: an http or grokbot row in
// routes, or a dot's MCP Events callback (event_subscriptions). Pull
// transports never touch one. Counters are lifetime. Percentiles come from
// a fixed ring of the most recent RouteUsageSamples attempts, so a read
// sorts at most that many integers and no request body is stored.

const (
	// RouteKindEvents is an MCP Events callback. It is not a routes.kind.
	RouteKindEvents = "events"
	// RouteUsageSamples is the ring length. 256 keeps a real p99 sample
	// (nearest-rank of the recent window) without an unbounded history.
	RouteUsageSamples = 256

	maxUsageBody    = 1 << 20
	maxUsageLatency = 10 * time.Minute
	maxUsageError   = 200
)

// RouteAttempt is one outbound HTTP attempt that was actually handed to the
// client. Verification challenges and grokbot budget deferrals are not
// attempts: nothing was a delivery push.
type RouteAttempt struct {
	Key        string
	Kind       string
	BodyBytes  int
	Latency    time.Duration
	HasLatency bool
	OK         bool
	Status     int // 0 when the attempt got no HTTP status
	Error      string
	At         time.Time
}

// RouteUsage is the operator view of one route. Distributions are over the
// retained ring (Samples says how full it is), not over the lifetime counters.
type RouteUsage struct {
	Requests      int            `json:"requests"`
	Successes     int            `json:"successes"`
	Failures      int            `json:"failures"`
	LastError     *string        `json:"last_error"`
	LastErrorAt   *string        `json:"last_error_at"`
	LastStatus    *int           `json:"last_status"`
	LastUsedAt    string         `json:"last_used_at"`
	LastSuccessAt *string        `json:"last_success_at"`
	BodyBytes     RouteUsageDist `json:"body_bytes"`
	LatencyMS     RouteUsageDist `json:"latency_ms"`
}

// RouteUsageDist is avg / nearest-rank p95 / p99 over the retained samples.
type RouteUsageDist struct {
	Avg     float64 `json:"avg"`
	P95     float64 `json:"p95"`
	P99     float64 `json:"p99"`
	Samples int     `json:"samples"`
}

// RouteAttemptError is a stored failure string: a fixed egress reason, or
// "HTTP <status>". It never includes a body, URL or header.
func RouteAttemptError(status int, netErr error) string {
	if netErr != nil {
		if msg := EgressErr(netErr); msg != "" {
			return msg
		}
		return "request_failed"
	}
	if status >= 200 && status < 300 {
		return ""
	}
	if status >= 100 && status <= 599 {
		return "HTTP " + strconv.Itoa(status)
	}
	return "request_failed"
}

// NoteRouteAttempt records a. A store error is logged and swallowed: the
// delivery that prompted the attempt has already been recorded, and this
// write must not fail it.
func (h *Hub) NoteRouteAttempt(ctx context.Context, a RouteAttempt) {
	if err := h.recordRouteAttempt(ctx, a); err != nil {
		slog.Error("hub: route usage", "route", a.Key, "err", err)
	}
}

// PercentileNearestRank is the nearest-rank percentile of an ascending
// sample. p is in (0, 100]. Rank is ceil(p/100*n), and the value is
// sorted[rank-1]. An empty sample or a p outside that range returns 0, false.
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

func (h *Hub) recordRouteAttempt(ctx context.Context, a RouteAttempt) error {
	switch a.Kind {
	case RouteKindHTTP, RouteKindGrokbot, RouteKindEvents:
	default:
		return fmt.Errorf("hub: route usage: kind %q", a.Kind)
	}
	if a.Key == "" || len(a.Key) > 80 {
		return fmt.Errorf("hub: route usage: empty or overlong key")
	}
	at := a.At
	if at.IsZero() {
		at = h.Now()
	}
	atS := envelope.FormatTime(at)
	body := a.BodyBytes
	if body < 0 {
		body = 0
	}
	if body > maxUsageBody {
		body = maxUsageBody
	}
	var latency any
	if a.HasLatency {
		d := a.Latency
		if d < 0 {
			d = 0
		}
		if d > maxUsageLatency {
			d = maxUsageLatency
		}
		latency = d.Microseconds()
	}
	okN, failN := 0, 1
	var errText, errAt, successAt any
	if a.OK {
		okN, failN = 1, 0
		successAt = atS
	} else {
		msg := clipUsageError(a.Error)
		if msg == "" {
			msg = "request_failed"
		}
		errText = msg
		errAt = atS
	}
	var status any
	if a.Status >= 100 && a.Status <= 599 {
		status = a.Status
	}
	return h.DB.Tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO route_usage(
				route_key, kind, requests, successes, failures,
				last_error, last_error_at, last_status, last_used_at, last_success_at, sample_n)
			VALUES (?1, ?2, 1, ?3, ?4, ?5, ?6, ?7, ?8, ?9, 1)
			ON CONFLICT(route_key) DO UPDATE SET
				requests = requests + 1,
				successes = successes + excluded.successes,
				failures = failures + excluded.failures,
				last_error = CASE WHEN excluded.failures = 1 THEN excluded.last_error ELSE route_usage.last_error END,
				last_error_at = CASE WHEN excluded.failures = 1 THEN excluded.last_error_at ELSE route_usage.last_error_at END,
				last_status = excluded.last_status,
				last_used_at = excluded.last_used_at,
				last_success_at = CASE WHEN excluded.successes = 1 THEN excluded.last_success_at ELSE route_usage.last_success_at END,
				sample_n = sample_n + 1`,
			a.Key, a.Kind, okN, failN, errText, errAt, status, atS, successAt); err != nil {
			return fmt.Errorf("hub: route usage: %w", err)
		}
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT sample_n FROM route_usage WHERE route_key = ?`, a.Key).Scan(&n); err != nil {
			return fmt.Errorf("hub: route usage: %w", err)
		}
		slot := (n - 1) % RouteUsageSamples
		if slot < 0 {
			slot = 0
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO route_usage_samples(route_key, slot, body_bytes, latency_us)
			VALUES (?, ?, ?, ?)
			ON CONFLICT(route_key, slot) DO UPDATE SET
				body_bytes = excluded.body_bytes,
				latency_us = excluded.latency_us`,
			a.Key, slot, body, latency); err != nil {
			return fmt.Errorf("hub: route usage sample: %w", err)
		}
		return nil
	})
}

func clipUsageError(s string) string {
	s = strings.TrimSpace(s)
	if len(s) <= maxUsageError {
		return s
	}
	s = s[:maxUsageError]
	for s != "" && !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
}

// RouteUsages returns usage for the keys that have at least one attempt.
// Keys with none are absent. Missing keys in the result are null on the wire.
func (h *Hub) RouteUsages(ctx context.Context, keys []string) (map[string]RouteUsage, error) {
	out := make(map[string]RouteUsage)
	if len(keys) == 0 {
		return out, nil
	}
	const chunk = 200
	for i := 0; i < len(keys); i += chunk {
		end := min(i+chunk, len(keys))
		if err := h.routeUsagesChunk(ctx, keys[i:end], out); err != nil {
			return nil, err
		}
	}
	return out, nil
}

type usageAcc struct {
	u    RouteUsage
	body []int64
	lat  []int64
}

func (h *Hub) routeUsagesChunk(ctx context.Context, keys []string, out map[string]RouteUsage) error {
	ph := "?" + strings.Repeat(",?", len(keys)-1)
	args := make([]any, len(keys))
	for i, k := range keys {
		args[i] = k
	}
	q := `SELECT route_key, requests, successes, failures, last_error, last_error_at, last_status, last_used_at, last_success_at
		FROM route_usage WHERE route_key IN (` + ph + `)`
	rows, err := h.DB.QueryContext(ctx, q, args...)
	if err != nil {
		return fmt.Errorf("hub: route usage: %w", err)
	}
	defer rows.Close()
	acc := map[string]*usageAcc{}
	for rows.Next() {
		var key string
		var u RouteUsage
		var lastErr, lastErrAt, lastOK sql.NullString
		var lastStatus sql.NullInt64
		if err := rows.Scan(&key, &u.Requests, &u.Successes, &u.Failures, &lastErr, &lastErrAt, &lastStatus, &u.LastUsedAt, &lastOK); err != nil {
			return fmt.Errorf("hub: route usage: %w", err)
		}
		u.LastError = nullStr(lastErr)
		u.LastErrorAt = nullStr(lastErrAt)
		u.LastSuccessAt = nullStr(lastOK)
		if lastStatus.Valid {
			s := int(lastStatus.Int64)
			u.LastStatus = &s
		}
		acc[key] = &usageAcc{u: u}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("hub: route usage: %w", err)
	}
	if len(acc) == 0 {
		return nil
	}
	srows, err := h.DB.QueryContext(ctx, `SELECT route_key, body_bytes, latency_us FROM route_usage_samples WHERE route_key IN (`+ph+`)`, args...)
	if err != nil {
		return fmt.Errorf("hub: route usage samples: %w", err)
	}
	defer srows.Close()
	for srows.Next() {
		var key string
		var body int64
		var lat sql.NullInt64
		if err := srows.Scan(&key, &body, &lat); err != nil {
			return fmt.Errorf("hub: route usage samples: %w", err)
		}
		a := acc[key]
		if a == nil {
			continue
		}
		a.body = append(a.body, body)
		if lat.Valid {
			a.lat = append(a.lat, lat.Int64)
		}
	}
	if err := srows.Err(); err != nil {
		return fmt.Errorf("hub: route usage samples: %w", err)
	}
	for key, a := range acc {
		a.u.BodyBytes = usageDist(a.body, 1)
		a.u.LatencyMS = usageDist(a.lat, 1000)
		out[key] = a.u
	}
	return nil
}

func nullStr(n sql.NullString) *string {
	if !n.Valid {
		return nil
	}
	s := n.String
	return &s
}

// usageDist sorts values and reports avg and nearest-rank p95/p99.
// scale divides the stored unit into the reported one (1 for bytes,
// 1000 for microseconds to milliseconds).
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

// deleteRouteUsage removes one route's counters and, by cascade, its ring.
func deleteRouteUsage(ctx context.Context, tx *sql.Tx, key string) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM route_usage WHERE route_key = ?`, key); err != nil {
		return fmt.Errorf("hub: delete route usage: %w", err)
	}
	return nil
}
