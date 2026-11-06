package store

import (
	"context"
	"database/sql"
	"fmt"
)

// RouteAttemptKeep is the most attempts one route retains.
// RouteAttemptMaxAge is applied by the caller as a cutoff timestamp:
// a row must be among the newest RouteAttemptKeep and at or after that cutoff.
const RouteAttemptKeep = 1000

// RouteAttempt is one stored outbound attempt.
type RouteAttempt struct {
	RouteID    string
	DeliveryID string
	At         string
	Outcome    string
	HTTPStatus sql.NullInt64
	ErrorClass sql.NullString
	BodyBytes  int
	LatencyMS  int
}

// RouteAttemptInsert is one attempt plus the prune cutoff (the attempt time
// minus the retention age) formatted like At.
type RouteAttemptInsert struct {
	RouteID    string
	DeliveryID string
	At         string
	Cutoff     string
	Outcome    string
	HTTPStatus sql.NullInt64
	ErrorClass sql.NullString
	BodyBytes  int
	LatencyMS  int
}

// InsertRouteAttempt writes one attempt and prunes that route in the same
// transaction: keep at most RouteAttemptKeep rows, and none older than Cutoff.
func InsertRouteAttempt(ctx context.Context, tx *sql.Tx, a RouteAttemptInsert) error {
	var status, class any
	if a.HTTPStatus.Valid {
		status = a.HTTPStatus.Int64
	}
	if a.ErrorClass.Valid {
		class = a.ErrorClass.String
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO route_attempts(route_id, delivery_id, at, outcome, http_status, error_class, body_bytes, latency_ms)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		a.RouteID, a.DeliveryID, a.At, a.Outcome, status, class, a.BodyBytes, a.LatencyMS); err != nil {
		return fmt.Errorf("store: route attempt: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		DELETE FROM route_attempts
		WHERE route_id = ?1
		  AND id NOT IN (
		    SELECT id FROM route_attempts
		    WHERE route_id = ?1 AND at >= ?2
		    ORDER BY at DESC, id DESC
		    LIMIT ?3
		  )`, a.RouteID, a.Cutoff, RouteAttemptKeep); err != nil {
		return fmt.Errorf("store: prune route attempts: %w", err)
	}
	return nil
}

// RouteAttempts returns up to limit attempts for routeID at or after since,
// newest first. limit <= 0 means RouteAttemptKeep.
func RouteAttempts(ctx context.Context, db *sql.DB, routeID, since string, limit int) ([]RouteAttempt, error) {
	if limit <= 0 {
		limit = RouteAttemptKeep
	}
	rows, err := db.QueryContext(ctx, `
		SELECT route_id, delivery_id, at, outcome, http_status, error_class, body_bytes, latency_ms
		FROM route_attempts
		WHERE route_id = ? AND at >= ?
		ORDER BY at DESC, id DESC
		LIMIT ?`, routeID, since, limit)
	if err != nil {
		return nil, fmt.Errorf("store: route attempts: %w", err)
	}
	defer rows.Close()
	var out []RouteAttempt
	for rows.Next() {
		var a RouteAttempt
		if err := rows.Scan(&a.RouteID, &a.DeliveryID, &a.At, &a.Outcome, &a.HTTPStatus, &a.ErrorClass, &a.BodyBytes, &a.LatencyMS); err != nil {
			return nil, fmt.Errorf("store: route attempts: %w", err)
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: route attempts: %w", err)
	}
	return out, nil
}
