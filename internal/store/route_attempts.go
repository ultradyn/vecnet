package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

const routeAttemptTimeFormat = "2006-01-02T15:04:05.000Z"

// RouteAttemptWriteError exposes a fixed failure stage without exposing SQL or
// driver text to the telemetry logger. Unwrap remains available for inspection.
type RouteAttemptWriteError struct {
	Stage string
	Err   error
}

func (e *RouteAttemptWriteError) Error() string { return "route attempt write failed: " + e.Stage }
func (e *RouteAttemptWriteError) Unwrap() error { return e.Err }

func routeAttemptClass(class string) bool {
	switch class {
	case "http_3xx", "http_4xx", "http_408", "http_409", "http_410", "http_413", "http_429", "http_5xx", "http_status",
		"routine_auth", "routine_rate_limited", "routine_not_started",
		"redirect_refused", "private_address", "scheme_refused", "userinfo_refused", "invalid_url", "canceled", "timeout", "dns_error", "tls_error", "connection_refused", "connection_reset", "connection_closed", "network_error", "request_failed":
		return true
	}
	return false
}

// RouteAttemptInsert is normalized telemetry, independent of delivery state.
type RouteAttemptInsert struct {
	RouteID, DeliveryID, At, Outcome string
	HTTPStatus                       *int
	ErrorClass                       *string
	BodyBytes, LatencyMS             int
}

// RouteAttempt is one retained observation, ordered by completion and insertion.
type RouteAttempt struct {
	ID int64
	RouteAttemptInsert
}

// InsertRouteAttempt owns its transaction; it never accepts a settlement tx.
func InsertRouteAttempt(ctx context.Context, db *DB, a RouteAttemptInsert, now time.Time) error {
	at, err := time.Parse(routeAttemptTimeFormat, a.At)
	valid := err == nil && at.UTC().Format(routeAttemptTimeFormat) == a.At && len(a.At) == 24 &&
		len(a.RouteID) > 0 && len(a.RouteID) <= 80 && len(a.DeliveryID) > 0 && len(a.DeliveryID) <= 80 &&
		(a.Outcome == "ok" || a.Outcome == "fail" || a.Outcome == "refused") &&
		((a.Outcome == "ok" && a.ErrorClass == nil) || (a.Outcome != "ok" && a.ErrorClass != nil && routeAttemptClass(*a.ErrorClass))) &&
		(a.HTTPStatus == nil || (*a.HTTPStatus >= 100 && *a.HTTPStatus <= 599))
	if !valid {
		return &RouteAttemptWriteError{Stage: "normalize", Err: errors.New("invalid route attempt")}
	}
	a.BodyBytes = min(max(a.BodyBytes, 0), 1048576)
	a.LatencyMS = min(max(a.LatencyMS, 0), 600000)
	stage := "begin"
	err = db.Tx(ctx, func(tx *sql.Tx) error {
		stage = "insert"
		_, err := tx.ExecContext(ctx, `INSERT INTO route_attempts(route_id,delivery_id,at,outcome,http_status,error_class,body_bytes,latency_ms) VALUES(?,?,?,?,?,?,?,?)`, a.RouteID, a.DeliveryID, a.At, a.Outcome, a.HTTPStatus, a.ErrorClass, a.BodyBytes, a.LatencyMS)
		if err != nil {
			return err
		}
		stage = "prune"
		_, err = tx.ExecContext(ctx, `DELETE FROM route_attempts WHERE route_id=? AND id NOT IN (
			SELECT id FROM route_attempts WHERE route_id=? AND at>=? ORDER BY at DESC,id DESC LIMIT 1000
		)`, a.RouteID, a.RouteID, now.UTC().Add(-168*time.Hour).Format(routeAttemptTimeFormat))
		if err != nil {
			return err
		}
		stage = "commit"
		return nil
	})
	if err != nil {
		return &RouteAttemptWriteError{Stage: stage, Err: err}
	}
	return nil
}

// RouteAttempts uses the caller's pinned snapshot connection and both bounds.
func RouteAttempts(ctx context.Context, q *sql.Conn, route, since, until string, limit int) ([]RouteAttempt, error) {
	if limit <= 0 || limit > 1000 {
		limit = 1000
	}
	rows, err := q.QueryContext(ctx, `SELECT id,route_id,delivery_id,at,outcome,http_status,error_class,body_bytes,latency_ms FROM route_attempts WHERE route_id=? AND at>=? AND at<=? ORDER BY at DESC,id DESC LIMIT ?`, route, since, until, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []RouteAttempt{}
	for rows.Next() {
		var a RouteAttempt
		if err := rows.Scan(&a.ID, &a.RouteID, &a.DeliveryID, &a.At, &a.Outcome, &a.HTTPStatus, &a.ErrorClass, &a.BodyBytes, &a.LatencyMS); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}
