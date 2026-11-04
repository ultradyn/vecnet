package hub

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"time"
	"unicode/utf8"

	"github.com/XertroV/vecnet/internal/audit"
	"github.com/XertroV/vecnet/internal/auth"
	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/ids"
)

// NeverProvisionable reserves peers with special authority or lifecycle to
// operator enrolment. Dots bind to a different principal through OAuth.
var NeverProvisionable = map[string]bool{"operator": true, "source": true, "session": true, "dot": true}

const DefaultCreatesPerHour = 60
const PeerCreateKeyWindow = 24 * time.Hour

var peerCreateKeyPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)

type PrincipalLimits struct {
	MaxPeers       int      `json:"max_peers"`
	CreatesPerHour int      `json:"creates_per_hour"`
	Kinds          []string `json:"kinds"`
}

type ProvisionedPeer struct {
	PeerID    string `json:"peer_id"`
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	CreatedBy string `json:"created_by"`
	Disabled  bool   `json:"disabled"`
}

type CreatePeerRequest struct {
	Kind           string `json:"kind"`
	Name           string `json:"name"`
	IdempotencyKey string `json:"idempotency_key"`
}

type CreatePeerResult struct {
	Peer           ProvisionedPeer `json:"peer"`
	IdempotencyKey string          `json:"idempotency_key"`
	Replayed       bool            `json:"-"`
}

type PrincipalInfo struct {
	PrincipalID     string          `json:"principal_id"`
	Name            string          `json:"name"`
	Limits          PrincipalLimits `json:"limits"`
	Active          int             `json:"active"`
	Disabled        int             `json:"disabled"`
	CreatesLastHour int             `json:"creates_last_hour"`
	ScopedPeers     int             `json:"scoped_peers"`
	DefaultAllow    []string        `json:"default_allow"`
	DefaultDeny     []string        `json:"default_deny"`
}

// provisionRefused records just reason and kind, never the requested name or
// body. A cutError lets the caller commit the audit before returning the API error.
func provisionRefused(ctx context.Context, tx *sql.Tx, c auth.Caller, action string, api *APIError, detail map[string]any) error {
	actor := c.PrincipalID
	if c.Kind == "peer" {
		actor = c.PeerID
	}
	if detail == nil {
		detail = map[string]any{}
	}
	detail["reason"] = api.Code
	if err := audit.Record(ctx, tx, actor, action, nil, detail); err != nil {
		return err
	}
	return &cutError{api}
}

// CreatePeer provisions an identity under the authenticated principal. The
// write lock covers replay, all limits, scopes and events as one operation.
func (h *Hub) CreatePeer(ctx context.Context, c auth.Caller, req CreatePeerRequest) (*CreatePeerResult, error) {
	var result *CreatePeerResult
	var cut *APIError
	err := h.DB.Tx(ctx, func(tx *sql.Tx) error {
		var err error
		result, err = h.createPeerTx(ctx, tx, c, req)
		var ce *cutError
		if errors.As(err, &ce) {
			cut = ce.api
			return nil
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	if cut != nil {
		return nil, cut
	}
	if !result.Replayed {
		h.notifyEnabled(ctx)
		h.Log.Notify(c.PrincipalID)
	}
	return result, nil
}

func (h *Hub) createPeerTx(ctx context.Context, tx *sql.Tx, c auth.Caller, req CreatePeerRequest) (*CreatePeerResult, error) {
	refuse := func(api *APIError) (*CreatePeerResult, error) {
		return nil, provisionRefused(ctx, tx, c, "peer_create_refused", api, map[string]any{"kind": req.Kind})
	}
	if c.Kind != "principal" {
		return refuse(E(http.StatusForbidden, "forbidden", "a principal credential is required"))
	}
	if n := utf8.RuneCountInString(req.Name); n == 0 || n > 128 {
		return refuse(invalidValue("name", "name must contain 1 to 128 runes"))
	}
	if !peerCreateKeyPattern.MatchString(req.IdempotencyKey) {
		return refuse(invalidValue("idempotency_key", "invalid idempotency key"))
	}
	if !PeerKinds[req.Kind] {
		return refuse(invalidValue("kind", "unknown peer kind"))
	}
	raw, err := json.Marshal(map[string]string{"kind": req.Kind, "name": req.Name})
	if err != nil {
		return nil, err
	}
	hash, err := canonicalSHA256(raw)
	if err != nil {
		return nil, err
	}
	now := h.Now()
	result, err := h.peerCreateKeyLookup(ctx, tx, c.PrincipalID, req.IdempotencyKey, hash, now)
	if err != nil {
		var api *APIError
		if errors.As(err, &api) {
			return refuse(api)
		}
		return nil, err
	}
	if result != nil {
		return result, nil
	}
	limits, err := principalLimitsTx(ctx, tx, c.PrincipalID)
	if err != nil {
		return nil, err
	}
	if NeverProvisionable[req.Kind] || !slices.Contains(limits.Kinds, req.Kind) {
		api := E(http.StatusForbidden, "kind_not_allowed", "this kind is not provisionable by this principal")
		api.Details["kind"] = req.Kind
		return refuse(api)
	}
	if api, err := principalPeerCap(ctx, tx, c.PrincipalID, limits.MaxPeers); err != nil {
		return nil, err
	} else if api != nil {
		return refuse(api)
	}
	retry, err := windowRetry(ctx, tx, `SELECT enrolled_at FROM peers WHERE created_by = ? AND enrolled_at > ? ORDER BY enrolled_at`, c.PrincipalID, limits.CreatesPerHour, time.Hour, now)
	if err != nil {
		return nil, err
	}
	if retry > 0 {
		api := E(http.StatusTooManyRequests, "peer_create_rate_limited", "principal peer create rate limit exceeded")
		api.Details["retry_after_seconds"] = retry
		return refuse(api)
	}
	peer := ids.New(ids.Peer)
	at := envelope.FormatTime(now)
	if err := h.insertPeer(ctx, tx, peer, c.PrincipalID, req.Name, req.Kind, at); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE peers SET created_by = ? WHERE peer_id = ?`, c.PrincipalID, peer); err != nil {
		return nil, fmt.Errorf("hub: set peer creator: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO scopes(peer_id,mode,target_id) SELECT ?,mode,target_id FROM principal_default_scopes WHERE prn_id = ?`, peer, c.PrincipalID); err != nil {
		return nil, fmt.Errorf("hub: copy default scopes: %w", err)
	}
	result = &CreatePeerResult{Peer: ProvisionedPeer{PeerID: peer, Kind: req.Kind, Name: req.Name, CreatedBy: c.PrincipalID}, IdempotencyKey: req.IdempotencyKey}
	response, err := json.Marshal(result)
	if err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO peer_create_keys(prn_id,key,body_sha256,response,created_at) VALUES (?,?,?,?,?)`, c.PrincipalID, req.IdempotencyKey, hash, string(response), at); err != nil {
		return nil, fmt.Errorf("hub: store peer create key: %w", err)
	}
	if err := h.DirectoryUpdated(ctx, tx, peer, "enrolled", enrolledFields); err != nil {
		return nil, err
	}
	// The shared fan-out skips the enrolled subject for operator enrolment.
	// Principal-created peers also need the event on their own durable log.
	if _, err := h.Log.Append(ctx, tx, peer, "directory_updated", h.Now(), map[string]any{"peer_id": peer, "change": "enrolled", "fields": enrolledFields}); err != nil {
		return nil, err
	}
	if err := audit.Record(ctx, tx, c.PrincipalID, "peer_create", []string{peer, c.PrincipalID}, map[string]any{"kind": req.Kind}); err != nil {
		return nil, err
	}
	return result, nil
}

func (h *Hub) peerCreateKeyLookup(ctx context.Context, tx *sql.Tx, prn, key, hash string, now time.Time) (*CreatePeerResult, error) {
	var storedHash, response, created string
	err := tx.QueryRowContext(ctx, `SELECT body_sha256,response,created_at FROM peer_create_keys WHERE prn_id = ? AND key = ?`, prn, key).Scan(&storedHash, &response, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("hub: peer create key lookup: %w", err)
	}
	if at, err := envelope.ParseTime(created); err != nil || now.Sub(at) >= PeerCreateKeyWindow {
		if _, err := tx.ExecContext(ctx, `DELETE FROM peer_create_keys WHERE prn_id = ? AND key = ?`, prn, key); err != nil {
			return nil, fmt.Errorf("hub: expire peer create key: %w", err)
		}
		return nil, nil
	}
	if storedHash != hash {
		api := E(http.StatusConflict, "idempotency_conflict", "idempotency_key was already used with a different body")
		api.Details["idempotency_key"] = key
		return nil, api
	}
	var result CreatePeerResult
	if err := json.Unmarshal([]byte(response), &result); err != nil {
		return nil, fmt.Errorf("hub: decode peer create response: %w", err)
	}
	result.Replayed = true
	return &result, nil
}

func principalLimitsTx(ctx context.Context, tx *sql.Tx, prn string) (PrincipalLimits, error) {
	l := PrincipalLimits{CreatesPerHour: DefaultCreatesPerHour, Kinds: []string{}}
	var kinds string
	err := tx.QueryRowContext(ctx, `SELECT max_peers,creates_per_hour,kinds FROM principal_limits WHERE prn_id = ?`, prn).Scan(&l.MaxPeers, &l.CreatesPerHour, &kinds)
	if errors.Is(err, sql.ErrNoRows) {
		return l, nil
	}
	if err != nil {
		return l, fmt.Errorf("hub: principal limits: %w", err)
	}
	if err := json.Unmarshal([]byte(kinds), &l.Kinds); err != nil {
		return l, fmt.Errorf("hub: principal kinds: %w", err)
	}
	if l.Kinds == nil {
		l.Kinds = []string{}
	}
	return l, nil
}

func principalPeerCap(ctx context.Context, tx *sql.Tx, prn string, limit int) (*APIError, error) {
	var active, disabled int
	err := tx.QueryRowContext(ctx, `SELECT count(CASE WHEN disabled = 0 THEN 1 END),count(CASE WHEN disabled = 1 THEN 1 END) FROM peers WHERE prn_id = ? AND created_by = ?`, prn, prn).Scan(&active, &disabled)
	if err != nil {
		return nil, fmt.Errorf("hub: principal peer counts: %w", err)
	}
	if active < limit {
		return nil, nil
	}
	api := E(http.StatusTooManyRequests, "peer_cap_reached", "principal active peer cap reached")
	api.Details = map[string]any{"limit": limit, "active": active, "disabled": disabled}
	return api, nil
}

// SetOwnPeerDisabled requires both immutable creator provenance and current
// ownership. Even a moved operator-enrolled peer remains outside this power.
func (h *Hub) SetOwnPeerDisabled(ctx context.Context, c auth.Caller, peerID string, disabled bool) (*ProvisionedPeer, error) {
	var result ProvisionedPeer
	var cut *APIError
	var changed bool
	err := h.DB.Tx(ctx, func(tx *sql.Tx) error {
		refuse := func(api *APIError) error {
			err := provisionRefused(ctx, tx, c, "peer_patch_refused", api, nil)
			var ce *cutError
			if errors.As(err, &ce) {
				cut = ce.api
				return nil
			}
			return err
		}
		if c.Kind != "principal" {
			return refuse(E(http.StatusForbidden, "forbidden", "a principal credential is required"))
		}
		err := tx.QueryRowContext(ctx, `SELECT peer_id,kind,name,created_by,disabled FROM peers WHERE peer_id = ? AND prn_id = ? AND created_by = ?`, peerID, c.PrincipalID, c.PrincipalID).Scan(&result.PeerID, &result.Kind, &result.Name, &result.CreatedBy, &result.Disabled)
		if errors.Is(err, sql.ErrNoRows) {
			return refuse(unknownPeer(peerID))
		}
		if err != nil {
			return fmt.Errorf("hub: find own peer: %w", err)
		}
		if result.Disabled == disabled {
			return nil
		}
		if !disabled {
			l, err := principalLimitsTx(ctx, tx, c.PrincipalID)
			if err != nil {
				return err
			}
			api, err := principalPeerCap(ctx, tx, c.PrincipalID, l.MaxPeers)
			if err != nil {
				return err
			}
			if api != nil {
				return refuse(api)
			}
		}
		changed, err = h.setPeerDisabledTx(ctx, tx, peerID, disabled, c.PrincipalID)
		if err != nil {
			var api *APIError
			if errors.As(err, &api) {
				return refuse(api)
			}
			return err
		}
		if changed && !disabled {
			if _, err := h.Log.Append(ctx, tx, peerID, "directory_updated", h.Now(), map[string]any{"peer_id": peerID, "change": "enrolled", "fields": enrolledFields}); err != nil {
				return err
			}
		}
		result.Disabled = disabled
		return nil
	})
	if err != nil {
		return nil, err
	}
	if cut != nil {
		return nil, cut
	}
	if changed {
		h.notifyEnabled(ctx)
		h.Log.Notify(peerID)
	}
	return &result, nil
}

func requirePrincipalTx(ctx context.Context, tx *sql.Tx, prn string) error {
	var one int
	err := tx.QueryRowContext(ctx, `SELECT 1 FROM principals WHERE prn_id = ?`, prn).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return E(http.StatusNotFound, "not_found", "no such principal")
	}
	if err != nil {
		return fmt.Errorf("hub: find principal: %w", err)
	}
	return nil
}

// SetPrincipalLimits is an operator operation. An absent row defaults to no
// permitted kinds and a zero active-peer cap.
func (h *Hub) SetPrincipalLimits(ctx context.Context, prnID string, l PrincipalLimits) error {
	return h.DB.Tx(ctx, func(tx *sql.Tx) error {
		if err := requirePrincipalTx(ctx, tx, prnID); err != nil {
			return err
		}
		if l.MaxPeers < 0 {
			return invalidValue("max_peers", "max_peers must be nonnegative")
		}
		if l.CreatesPerHour < 1 {
			return invalidValue("creates_per_hour", "creates_per_hour must be at least 1")
		}
		for _, kind := range l.Kinds {
			if !PeerKinds[kind] || NeverProvisionable[kind] {
				return invalidValue("kinds", "kinds must contain only provisionable peer kinds")
			}
		}
		if l.Kinds == nil {
			l.Kinds = []string{}
		}
		kinds, err := json.Marshal(l.Kinds)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO principal_limits(prn_id,max_peers,creates_per_hour,kinds,updated_at) VALUES (?,?,?,?,?) ON CONFLICT(prn_id) DO UPDATE SET max_peers=excluded.max_peers,creates_per_hour=excluded.creates_per_hour,kinds=excluded.kinds,updated_at=excluded.updated_at`, prnID, l.MaxPeers, l.CreatesPerHour, string(kinds), h.nowStr()); err != nil {
			return fmt.Errorf("hub: set principal limits: %w", err)
		}
		return audit.Record(ctx, tx, "operator", "principal_limits", []string{prnID}, map[string]any{"max_peers": l.MaxPeers, "creates_per_hour": l.CreatesPerHour, "kinds": l.Kinds})
	})
}

// SetPrincipalDefaultScopes replaces the policy copied on subsequent creates;
// existing peers keep their scopes. Targets follow SetScopes validation.
func (h *Hub) SetPrincipalDefaultScopes(ctx context.Context, prnID string, allow, deny []string) error {
	return h.DB.Tx(ctx, func(tx *sql.Tx) error {
		if err := requirePrincipalTx(ctx, tx, prnID); err != nil {
			return err
		}
		inAllow := map[string]bool{}
		for _, id := range allow {
			inAllow[id] = true
		}
		for _, list := range []struct {
			mode string
			ids  []string
		}{{"allow", allow}, {"deny", deny}} {
			for _, id := range list.ids {
				if list.mode == "deny" && inAllow[id] {
					return invalidValue("deny", "an id is in both allow and deny")
				}
				var ok bool
				if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM peers WHERE peer_id = ?1) OR EXISTS(SELECT 1 FROM teams WHERE team_id = ?1) OR EXISTS(SELECT 1 FROM groups WHERE grp_id = ?1)`, id).Scan(&ok); err != nil {
					return fmt.Errorf("hub: default scope target: %w", err)
				}
				if !ok {
					api := invalidValue(list.mode, "unknown peer, team or group")
					api.Details["id"] = id
					return api
				}
			}
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM principal_default_scopes WHERE prn_id = ?`, prnID); err != nil {
			return fmt.Errorf("hub: clear principal default scopes: %w", err)
		}
		for mode, list := range map[string][]string{"allow": allow, "deny": deny} {
			for _, id := range list {
				if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO principal_default_scopes(prn_id,mode,target_id) VALUES (?,?,?)`, prnID, mode, id); err != nil {
					return fmt.Errorf("hub: set principal default scope: %w", err)
				}
			}
		}
		return audit.Record(ctx, tx, "operator", "principal_default_scopes", []string{prnID}, map[string]any{"allow": len(allow), "deny": len(deny)})
	})
}

// Principals reports one consistent snapshot, ordered by principal id. Create
// rate counts retain moved peers; active and disabled counts require ownership.
func (h *Hub) Principals(ctx context.Context) ([]PrincipalInfo, error) {
	out := []PrincipalInfo{}
	err := h.DB.Tx(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT p.prn_id,p.name,
   (SELECT count(*) FROM peers x WHERE x.prn_id=p.prn_id AND x.created_by=p.prn_id AND x.disabled=0),
   (SELECT count(*) FROM peers x WHERE x.prn_id=p.prn_id AND x.created_by=p.prn_id AND x.disabled=1),
   (SELECT count(*) FROM peers x WHERE x.created_by=p.prn_id AND x.enrolled_at > ?),
   (SELECT count(*) FROM peers x WHERE x.prn_id=p.prn_id AND EXISTS(SELECT 1 FROM scopes s WHERE s.peer_id=x.peer_id))
   FROM principals p ORDER BY p.prn_id`, envelope.FormatTime(h.Now().Add(-time.Hour)))
		if err != nil {
			return fmt.Errorf("hub: principals: %w", err)
		}
		for rows.Next() {
			p := PrincipalInfo{DefaultAllow: []string{}, DefaultDeny: []string{}}
			if err := rows.Scan(&p.PrincipalID, &p.Name, &p.Active, &p.Disabled, &p.CreatesLastHour, &p.ScopedPeers); err != nil {
				rows.Close()
				return err
			}
			out = append(out, p)
		}
		if err := rows.Close(); err != nil {
			return err
		}
		if err := rows.Err(); err != nil {
			return err
		}
		for i := range out {
			p := &out[i]
			p.Limits, err = principalLimitsTx(ctx, tx, p.PrincipalID)
			if err != nil {
				return err
			}
			rows, err := tx.QueryContext(ctx, `SELECT mode,target_id FROM principal_default_scopes WHERE prn_id = ? ORDER BY mode,target_id`, p.PrincipalID)
			if err != nil {
				return fmt.Errorf("hub: principal default scopes: %w", err)
			}
			for rows.Next() {
				var mode, id string
				if err := rows.Scan(&mode, &id); err != nil {
					rows.Close()
					return err
				}
				if mode == "allow" {
					p.DefaultAllow = append(p.DefaultAllow, id)
				} else {
					p.DefaultDeny = append(p.DefaultDeny, id)
				}
			}
			if err := rows.Close(); err != nil {
				return err
			}
			if err := rows.Err(); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
