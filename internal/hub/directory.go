package hub

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/XertroV/vecnet/internal/auth"
	"github.com/XertroV/vecnet/internal/envelope"
)

// DirectoryEntry is one peer's public directory fields (PROTOCOL §8). It
// never carries credential status, hashes, scopes or routes: those are
// operator-only (§2.10).
type DirectoryEntry struct {
	CreatedBy    *string        `json:"created_by,omitempty"`
	PeerID       string         `json:"peer_id"`
	Name         string         `json:"name"`
	Kind         string         `json:"kind"`
	Capabilities []string       `json:"capabilities"`
	Quota        map[string]any `json:"quota"`
	Location     *string        `json:"location"`
	Presence     string         `json:"presence"`
	Reachability string         `json:"reachability"`
	LastSeen     *string        `json:"last_seen"`
	EnrolledAt   *string        `json:"enrolled_at"`
	Teams        []string       `json:"teams"`
}

// Reachability values (PROTOCOL §8).
var reachabilities = map[string]bool{"connected": true, "reachable": true, "unreachable": true, "unknown": true}

// ReachabilityCallWindow is how long an inbound call (or a closed stream)
// keeps a peer "reachable" (PROTOCOL §8: "last inbound call within 24 h").
// Reachability from an accepted push has no window.
const ReachabilityCallWindow = 24 * time.Hour

// EffectiveReachability is the reachability the hub shows at now for a
// stored value set by the observation via ("call", "push", or nil for none
// or a pre-reach_via row, read as "call"): a call-based "reachable" whose
// last_seen is more than ReachabilityCallWindow before now (or missing) is
// "unknown"; every other value is shown as stored (PROTOCOL §8).
func EffectiveReachability(stored string, via, lastSeen *string, now time.Time) string {
	if stored != "reachable" || (via != nil && *via == "push") {
		return stored
	}
	if lastSeen == nil {
		return "unknown"
	}
	at, err := envelope.ParseTime(*lastSeen)
	if err != nil || now.Sub(at) > ReachabilityCallWindow {
		return "unknown"
	}
	return stored
}

// enrolledFields are the fields a directory_updated "enrolled" Event names.
var enrolledFields = []string{"name", "kind", "enrolled_at"}

// Directory returns every enabled peer's public entry, ordered by peer id,
// with presence rendered through EffectivePresence and reachability through
// EffectiveReachability, then every enabled source's as kind "source",
// ordered by source id (PROTOCOL §2.10, §8, §10). A source reports no
// presence and the hub observes no transport for it, so both are "unknown".
func (h *Hub) Directory(ctx context.Context) ([]DirectoryEntry, error) {
	sources, err := h.directorySources(ctx)
	if err != nil {
		return nil, err
	}
	teams := map[string][]string{}
	rows, err := h.DB.QueryContext(ctx, `SELECT peer_id, team_id FROM team_members ORDER BY peer_id, team_id`)
	if err != nil {
		return nil, fmt.Errorf("hub: directory teams: %w", err)
	}
	for rows.Next() {
		var p, t string
		if err := rows.Scan(&p, &t); err != nil {
			rows.Close()
			return nil, fmt.Errorf("hub: directory teams: %w", err)
		}
		teams[p] = append(teams[p], t)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("hub: directory teams: %w", err)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("hub: directory teams: %w", err)
	}

	rows, err = h.DB.QueryContext(ctx,
		`SELECT peer_id, name, kind, capabilities, quota, location, presence_state, presence_at,
		        reachability, reach_via, last_seen, enrolled_at, created_by
		 FROM peers WHERE disabled = 0 ORDER BY peer_id`)
	if err != nil {
		return nil, fmt.Errorf("hub: directory: %w", err)
	}
	defer rows.Close()
	now := h.Now()
	out := []DirectoryEntry{}
	for rows.Next() {
		var (
			e                                        DirectoryEntry
			caps, quota, enrolled, reach             string
			loc, state, at, via, lastSeen, createdBy sql.NullString
		)
		if err := rows.Scan(&e.PeerID, &e.Name, &e.Kind, &caps, &quota, &loc, &state, &at,
			&reach, &via, &lastSeen, &enrolled, &createdBy); err != nil {
			return nil, fmt.Errorf("hub: directory: %w", err)
		}
		if err := json.Unmarshal([]byte(caps), &e.Capabilities); err != nil || e.Capabilities == nil {
			e.Capabilities = []string{}
		}
		dec := json.NewDecoder(bytes.NewReader([]byte(quota)))
		dec.UseNumber()
		if err := dec.Decode(&e.Quota); err != nil || e.Quota == nil {
			e.Quota = map[string]any{}
		}
		e.CreatedBy = nullable(createdBy)
		e.Location = nullable(loc)
		e.LastSeen = nullable(lastSeen)
		e.Reachability = EffectiveReachability(reach, nullable(via), e.LastSeen, now)
		e.EnrolledAt = &enrolled
		e.Presence = h.EffectivePresence(PresenceRow{Kind: e.Kind, State: nullable(state), At: nullable(at)}, now)
		e.Teams = teams[e.PeerID]
		if e.Teams == nil {
			e.Teams = []string{}
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("hub: directory: %w", err)
	}
	return append(out, sources...), nil
}

func nullable(s sql.NullString) *string {
	if !s.Valid {
		return nil
	}
	v := s.String
	return &v
}

// SetSubscribePresence sets the caller's presence fan-out (PROTOCOL §8,
// R22): "all", "teams" or "none", else 422 invalid_value. It needs a
// credential with its own peer (a peer, or the operator); a bare principal
// credential is 403 forbidden.
func (h *Hub) SetSubscribePresence(ctx context.Context, c auth.Caller, v string) error {
	switch v {
	case "all", "teams", "none":
	default:
		e := E(http.StatusUnprocessableEntity, "invalid_value", "subscribe_presence must be all, teams or none")
		e.Details["field"] = "subscribe_presence"
		return e
	}
	if c.PeerID == "" {
		return E(http.StatusForbidden, "forbidden", "this credential has no peer of its own")
	}
	if _, err := h.DB.ExecContext(ctx, `UPDATE peers SET subscribe_presence = ? WHERE peer_id = ?`, v, c.PeerID); err != nil {
		return fmt.Errorf("hub: subscribe_presence: %w", err)
	}
	return nil
}

// DirectoryUpdated appends a directory_updated Event, data {"peer_id",
// "change", "fields"}, to every enabled peer inside tx (PROTOCOL §8, R22).
// change is "enrolled", "updated" or "removed". The subject of an
// "enrolled" change is skipped: it has just joined and reads the directory
// itself. Callers wake the readers after commit (notifyEnabled).
func (h *Hub) DirectoryUpdated(ctx context.Context, tx *sql.Tx, peerID, change string, fields []string) error {
	switch change {
	case "enrolled", "updated", "removed":
	default:
		return fmt.Errorf("hub: directory_updated: unknown change %q", change)
	}
	if fields == nil {
		fields = []string{}
	}
	data := map[string]any{"peer_id": peerID, "change": change, "fields": fields}
	rows, err := tx.QueryContext(ctx, `SELECT peer_id FROM peers WHERE disabled = 0 ORDER BY peer_id`)
	if err != nil {
		return fmt.Errorf("hub: directory_updated: %w", err)
	}
	var owners []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			rows.Close()
			return fmt.Errorf("hub: directory_updated: %w", err)
		}
		if change == "enrolled" && p == peerID {
			continue
		}
		owners = append(owners, p)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("hub: directory_updated: %w", err)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("hub: directory_updated: %w", err)
	}
	now := h.Now()
	for _, o := range owners {
		if _, err := h.Log.Append(ctx, tx, o, "directory_updated", now, data); err != nil {
			return err
		}
	}
	return nil
}

// notifyEnabled wakes the log readers of every enabled peer, after a
// directory_updated commit. A spurious wakeup only costs the reader a read.
func (h *Hub) notifyEnabled(ctx context.Context) {
	rows, err := h.DB.QueryContext(ctx, `SELECT peer_id FROM peers WHERE disabled = 0`)
	if err != nil {
		slog.Error("hub: notify directory readers", "err", err)
		return
	}
	defer rows.Close()
	var owners []string
	for rows.Next() {
		var p string
		if rows.Scan(&p) == nil {
			owners = append(owners, p)
		}
	}
	if len(owners) > 0 {
		h.Log.Notify(owners...)
	}
}

// TouchReachability records a hub observation of peerID from an inbound
// connection (PROTOCOL §8): "reachable" (an authenticated inbound call),
// "connected" (a stream opened), "unreachable" (a failed or dead-lettered
// push) or "unknown". Push results should use ObservePush instead, so that
// an accepted push is recorded without the call window: a "reachable" from
// here decays to "unknown" ReachabilityCallWindow after last_seen
// (EffectiveReachability). While the peer has an open stream it stays
// "connected" whatever else is observed. "reachable" and "connected" also
// set last_seen. Failures are logged, never returned: an observation must
// not fail the request that made it.
func (h *Hub) TouchReachability(ctx context.Context, peerID, value string) {
	if peerID == "" || !reachabilities[value] {
		slog.Error("hub: bad reachability observation", "peer", peerID, "value", value)
		return
	}
	via := "call"
	switch value {
	case "unreachable":
		via = "push"
	case "unknown":
		via = ""
	}
	h.reachMu.Lock()
	defer h.reachMu.Unlock()
	h.touchLocked(ctx, peerID, value, via)
}

// ObservePush records a push result for peerID (PROTOCOL §8): accepted
// (2xx) is "reachable" with no time window; otherwise (failed or
// dead-lettered) "unreachable". An open stream still wins ("connected").
func (h *Hub) ObservePush(ctx context.Context, peerID string, accepted bool) {
	if peerID == "" {
		slog.Error("hub: push observation without a peer")
		return
	}
	value := "unreachable"
	if accepted {
		value = "reachable"
	}
	h.reachMu.Lock()
	defer h.reachMu.Unlock()
	h.touchLocked(ctx, peerID, value, "push")
}

// touchLocked writes the observation and the kind of observation that made
// it (via: "call", "push", or "" for none); h.reachMu held.
func (h *Hub) touchLocked(ctx context.Context, peerID, value, via string) {
	if h.streams[peerID] > 0 {
		value, via = "connected", "call"
	}
	var err error
	if value == "connected" || value == "reachable" {
		_, err = h.DB.ExecContext(ctx, `UPDATE peers SET reachability = ?, reach_via = ?, last_seen = ? WHERE peer_id = ?`,
			value, nullIfEmpty(via), h.nowStr(), peerID)
	} else {
		_, err = h.DB.ExecContext(ctx, `UPDATE peers SET reachability = ?, reach_via = ? WHERE peer_id = ?`,
			value, nullIfEmpty(via), peerID)
	}
	if err != nil {
		slog.Error("hub: touch reachability", "peer", peerID, "err", err)
	}
}

// StreamOpened marks peerID connected for the life of an open SSE stream;
// pair every call with StreamClosed.
func (h *Hub) StreamOpened(ctx context.Context, peerID string) {
	h.reachMu.Lock()
	defer h.reachMu.Unlock()
	if h.streams == nil {
		h.streams = map[string]int{}
	}
	h.streams[peerID]++
	h.touchLocked(ctx, peerID, "connected", "call")
}

// StreamClosed ends one stream of peerID; when it was the last, the peer
// falls back to reachable, as from an inbound call (it decays after
// ReachabilityCallWindow).
func (h *Hub) StreamClosed(ctx context.Context, peerID string) {
	h.reachMu.Lock()
	defer h.reachMu.Unlock()
	if h.streams[peerID] <= 1 {
		delete(h.streams, peerID)
	} else {
		h.streams[peerID]--
	}
	h.touchLocked(ctx, peerID, "reachable", "call")
}
