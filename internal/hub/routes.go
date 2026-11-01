package hub

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/XertroV/vecnet/internal/audit"
	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/ids"
	"github.com/XertroV/vecnet/internal/webhook"
)

// Route kinds (routes.kind).
const (
	RouteKindHTTP    = "http"    // signed webhooks (PROTOCOL §3)
	RouteKindGrokbot = "grokbot" // Grok Bot routine (PROTOCOL §13.3)
)

// RouteKeyOverlap is how long a rotated-out route key stays valid; pushes
// during the overlap carry both signatures (PROTOCOL §3.5).
const RouteKeyOverlap = 24 * time.Hour

// routePrefix tags route ids ("route_<ULID>"); routes are operator config,
// not one of the ten wire id kinds of PROTOCOL §1.3.
const routePrefix ids.Prefix = "route"

// AddRoute creates a push route (PROTOCOL §3) owned by a peer (ownerKind
// "peer") or a principal ("principal") and its first whsec_ key, returned
// once. A peer route becomes that peer's route; a principal route becomes
// the route of every peer of the principal that has none, and serves peers
// enrolled later (routeFor falls back to it at send time). Queued
// deliveries of the affected peers that have no route yet pick this one up.
//
// The route is not live until Worker.Verify has answered its verification
// challenge (MarkRouteVerified): ClaimDue skips unverified routes (§3.2).
// An unknown owner or an unknown ownerKind is 422 invalid_value; a URL the
// egress guard refuses (egress.Policy.CheckURL: https only unless
// Cfg.AllowPrivateEgress, no userinfo, public host) is 422 invalid_url.
func (h *Hub) AddRoute(ctx context.Context, ownerKind, ownerID, rawURL string) (routeID, whsec string, err error) {
	if ownerKind != "peer" && ownerKind != "principal" {
		return "", "", invalidRouteField("owner_kind", "a route is owned by a peer or a principal")
	}
	if _, uerr := h.EgressPolicy().CheckURL(rawURL); uerr != nil {
		return "", "", invalidURL("url", uerr)
	}
	err = h.DB.Tx(ctx, func(tx *sql.Tx) error {
		var now string
		var err error
		routeID, now, err = h.insertRoute(ctx, tx, newRoute{ownerKind: ownerKind, ownerID: ownerID, kind: RouteKindHTTP, url: rawURL})
		if err != nil {
			return err
		}
		whsec = webhook.NewSecret()
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO route_keys(route_id, whsec, created_at) VALUES (?, ?, ?)`, routeID, whsec, now); err != nil {
			return fmt.Errorf("hub: insert route key: %w", err)
		}
		return nil
	})
	if err != nil {
		return "", "", err
	}
	return routeID, whsec, nil
}

// newRoute is a route being created. keyEnc is a sealed routine key
// (grokbot routes only); verified marks it live at once.
type newRoute struct {
	ownerKind, ownerID, kind, url string
	keyEnc                        string
	verified                      bool
}

// insertRoute inserts r for an existing owner, makes it its peer's route (a
// principal route: of every peer of the principal that has none), and hands
// it the queued deliveries of those peers that have no route yet. An unknown
// owner is 422 invalid_value. It returns the new id and its creation time.
func (h *Hub) insertRoute(ctx context.Context, tx *sql.Tx, r newRoute) (routeID, now string, err error) {
	var one int
	q := `SELECT 1 FROM peers WHERE peer_id = ?`
	if r.ownerKind == "principal" {
		q = `SELECT 1 FROM principals WHERE prn_id = ?`
	}
	err = tx.QueryRowContext(ctx, q, r.ownerID).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", invalidRouteField("owner", "unknown route owner")
	}
	if err != nil {
		return "", "", fmt.Errorf("hub: route owner: %w", err)
	}
	now = h.nowStr()
	routeID = ids.New(routePrefix)
	var verifiedAt, keyEnc any
	if r.verified {
		verifiedAt = now
	}
	if r.keyEnc != "" {
		keyEnc = r.keyEnc
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO routes(route_id, owner_kind, owner_id, kind, url, verified_at, created_at, key_enc) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		routeID, r.ownerKind, r.ownerID, r.kind, r.url, verifiedAt, now, keyEnc); err != nil {
		return "", "", fmt.Errorf("hub: insert route: %w", err)
	}
	if r.ownerKind == "peer" {
		_, err = tx.ExecContext(ctx, `UPDATE peers SET route_id = ? WHERE peer_id = ?`, routeID, r.ownerID)
	} else {
		_, err = tx.ExecContext(ctx, `UPDATE peers SET route_id = ? WHERE prn_id = ? AND route_id IS NULL`, routeID, r.ownerID)
	}
	if err != nil {
		return "", "", fmt.Errorf("hub: assign route: %w", err)
	}
	// Backfill: queued deliveries resolved at send time to no route
	// (pull only) now push through the peer's route.
	if _, err := tx.ExecContext(ctx,
		`UPDATE deliveries SET route_id = (SELECT p.route_id FROM peers p WHERE p.peer_id = deliveries.recipient), updated_at = ?
		 WHERE route_id IS NULL AND state = 'queued'
		   AND recipient IN (SELECT peer_id FROM peers WHERE route_id = ?)`, now, routeID); err != nil {
		return "", "", fmt.Errorf("hub: backfill route: %w", err)
	}
	if err := audit.Record(ctx, tx, "operator", "route_add", []string{routeID, r.ownerID},
		map[string]any{"owner_kind": r.ownerKind, "kind": r.kind}); err != nil {
		return "", "", err
	}
	return routeID, now, nil
}

// RouteInfo is one route as the operator sees it (GET /v1/admin/routes). It
// never carries a key: not a whsec_, not a routine key, sealed or otherwise.
type RouteInfo struct {
	RouteID     string  `json:"route_id"`
	OwnerKind   string  `json:"owner_kind"`
	OwnerID     string  `json:"owner_id"`
	Kind        string  `json:"kind"`
	URL         string  `json:"url"`
	VerifiedAt  *string `json:"verified_at"`
	CreatedAt   string  `json:"created_at"`
	RunsPerHour int     `json:"runs_per_hour"`
}

// ListRoutes returns every push route, oldest first.
func (h *Hub) ListRoutes(ctx context.Context) ([]RouteInfo, error) {
	rows, err := h.DB.QueryContext(ctx,
		`SELECT route_id, owner_kind, owner_id, kind, url, verified_at, created_at, runs_per_hour
		 FROM routes ORDER BY created_at, route_id`)
	if err != nil {
		return nil, fmt.Errorf("hub: list routes: %w", err)
	}
	defer rows.Close()
	out := []RouteInfo{}
	for rows.Next() {
		var r RouteInfo
		if err := rows.Scan(&r.RouteID, &r.OwnerKind, &r.OwnerID, &r.Kind, &r.URL, &r.VerifiedAt, &r.CreatedAt, &r.RunsPerHour); err != nil {
			return nil, fmt.Errorf("hub: list routes: %w", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("hub: list routes: %w", err)
	}
	return out, nil
}

// RotateRoute issues a new key for routeID and retires every active older
// key RouteKeyOverlap from now (PROTOCOL §3.5). An unknown route is 404
// not_found; a grokbot route, which has no whsec_ to rotate, is 422
// invalid_value.
func (h *Hub) RotateRoute(ctx context.Context, routeID string) (newWhsec string, err error) {
	err = h.DB.Tx(ctx, func(tx *sql.Tx) error {
		kind, err := routeKind(ctx, tx, routeID)
		if err != nil {
			return err
		}
		if kind != RouteKindHTTP {
			return invalidRouteField("route_id", "only an http route has a whsec_ key to rotate")
		}
		nowT := h.Now()
		now := envelope.FormatTime(nowT)
		if _, err := tx.ExecContext(ctx,
			`UPDATE route_keys SET retired_at = ? WHERE route_id = ? AND (retired_at IS NULL OR retired_at > ?)`,
			envelope.FormatTime(nowT.Add(RouteKeyOverlap)), routeID, envelope.FormatTime(nowT.Add(RouteKeyOverlap))); err != nil {
			return fmt.Errorf("hub: retire route keys: %w", err)
		}
		newWhsec = webhook.NewSecret()
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO route_keys(route_id, whsec, created_at) VALUES (?, ?, ?)`, routeID, newWhsec, now); err != nil {
			return fmt.Errorf("hub: insert route key: %w", err)
		}
		return audit.Record(ctx, tx, "operator", "route_rotate", []string{routeID}, nil)
	})
	if err != nil {
		return "", err
	}
	return newWhsec, nil
}

// RouteKeys returns routeID's keys valid at now (retired_at NULL or after
// now), newest first.
func (h *Hub) RouteKeys(ctx context.Context, routeID string, now time.Time) ([]string, error) {
	return routeKeys(ctx, h.DB, routeID, now)
}

type querier interface {
	QueryContext(ctx context.Context, q string, args ...any) (*sql.Rows, error)
}

func routeKeys(ctx context.Context, q querier, routeID string, now time.Time) ([]string, error) {
	rows, err := q.QueryContext(ctx,
		`SELECT whsec FROM route_keys WHERE route_id = ? AND (retired_at IS NULL OR retired_at > ?)
		 ORDER BY created_at DESC, rowid DESC`, routeID, envelope.FormatTime(now))
	if err != nil {
		return nil, fmt.Errorf("hub: route keys: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, fmt.Errorf("hub: route keys: %w", err)
		}
		out = append(out, k)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("hub: route keys: %w", err)
	}
	return out, nil
}

// RouteURL returns routeID's receiver URL; an unknown route is 404 not_found.
func (h *Hub) RouteURL(ctx context.Context, routeID string) (string, error) {
	var u string
	err := h.DB.QueryRowContext(ctx, `SELECT url FROM routes WHERE route_id = ?`, routeID).Scan(&u)
	if errors.Is(err, sql.ErrNoRows) {
		return "", unknownRoute(routeID)
	}
	if err != nil {
		return "", fmt.Errorf("hub: route url: %w", err)
	}
	return u, nil
}

// MarkRouteVerified records that routeID answered its verification
// challenge (PROTOCOL §3.2); from now on ClaimDue pushes through it.
func (h *Hub) MarkRouteVerified(ctx context.Context, routeID string) error {
	return h.DB.Tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE routes SET verified_at = ? WHERE route_id = ?`, h.nowStr(), routeID)
		if err != nil {
			return fmt.Errorf("hub: verify route: %w", err)
		}
		if n, err := res.RowsAffected(); err != nil {
			return fmt.Errorf("hub: verify route: %w", err)
		} else if n == 0 {
			return unknownRoute(routeID)
		}
		return audit.Record(ctx, tx, "operator", "route_verified", []string{routeID}, nil)
	})
}

func routeKind(ctx context.Context, tx *sql.Tx, routeID string) (string, error) {
	var kind string
	err := tx.QueryRowContext(ctx, `SELECT kind FROM routes WHERE route_id = ?`, routeID).Scan(&kind)
	if errors.Is(err, sql.ErrNoRows) {
		return "", unknownRoute(routeID)
	}
	if err != nil {
		return "", fmt.Errorf("hub: find route: %w", err)
	}
	return kind, nil
}

func invalidRouteField(field, msg string) *APIError {
	e := E(http.StatusUnprocessableEntity, "invalid_value", msg)
	e.Details["field"] = field
	return e
}

func unknownRoute(routeID string) *APIError {
	e := E(http.StatusNotFound, "not_found", "no such route")
	e.Details["route_id"] = routeID
	return e
}
