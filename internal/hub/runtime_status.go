package hub

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"time"

	"github.com/XertroV/vecnet/internal/auth"
	"github.com/XertroV/vecnet/internal/envelope"
)

// RuntimeStatusReport is a bounded runtime self-report. Clock fields belong
// to the hub. Optional strings distinguish absence from an invalid empty value.
type RuntimeStatusReport struct {
	Runtime    string
	State      string
	Delivery   string
	Reason     *string
	Version    *string
	TTLSeconds int
}

// RuntimeStatus is the public snapshot, never a delivery or binding authority.
// Expiry replaces state/delivery with unknown and reason with stale, preserving
// the other fields. Nil snapshots mean the peer has never reported.
type RuntimeStatus struct {
	Runtime   string  `json:"runtime"`
	State     string  `json:"state"`
	Delivery  string  `json:"delivery"`
	Reason    *string `json:"reason,omitempty"`
	Version   *string `json:"version,omitempty"`
	UpdatedAt string  `json:"updated_at"`
	ExpiresAt string  `json:"expires_at"`
}

var runtimeStatusReasons = map[string]bool{
	"connecting": true, "unreachable": true, "refused_version": true,
	"stopped": true, "expired": true, "archived": true, "not_loaded": true,
	"idle_stale": true, "approval_policy": true, "waiting_approval": true,
	"waiting_input": true, "over_limit": true, "identity_changed": true,
	"ownership_changed": true, "unverified_thread": true,
	"identity_unverified": true, "watch_stopped": true, "delivery_unresolved": true,
}

// SemVer 2.0.0: exactly three numeric core components, no v prefix or leading
// zeroes, and nonempty dot-separated prerelease/build identifiers. Numeric
// prerelease identifiers cannot have leading zeroes; build identifiers can.
var runtimeVersion = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-((0|[1-9][0-9]*|[0-9]*[A-Za-z-][0-9A-Za-z-]*)(\.(0|[1-9][0-9]*|[0-9]*[A-Za-z-][0-9A-Za-z-]*))*))?(\+[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?$`)

func validateRuntimeStatus(p RuntimeStatusReport) error {
	invalid := func(field string) error {
		return &envelope.SchemaError{Path: "/" + field, Message: "invalid runtime status field"}
	}
	if p.Runtime != "codex" {
		return invalid("runtime")
	}
	switch p.State {
	case "idle", "working", "blocked", "offline":
	default:
		return invalid("state")
	}
	switch p.Delivery {
	case "ready", "unavailable", "unsupported":
	default:
		return invalid("delivery")
	}
	if p.State == "offline" && p.Delivery != "unavailable" || p.Delivery == "ready" && p.State != "idle" && p.State != "working" {
		return invalid("delivery")
	}
	if p.TTLSeconds < 1 || p.TTLSeconds > 90 {
		return invalid("ttl_seconds")
	}
	if p.Reason != nil && !runtimeStatusReasons[*p.Reason] {
		return invalid("reason")
	}
	if p.Version != nil && (len(*p.Version) > 128 || !runtimeVersion.MatchString(*p.Version)) {
		return invalid("version")
	}
	return nil
}

func (s RuntimeStatus) stale(now time.Time) bool {
	expires, err := envelope.ParseTime(s.ExpiresAt)
	return err != nil || !now.Before(expires)
}

func sameRuntimeString(a, b *string) bool {
	return a == nil && b == nil || a != nil && b != nil && *a == *b
}

// SetRuntimeStatus replaces the caller's single snapshot in the same writer
// transaction as any directory invalidation. Pure fresh heartbeats update
// timestamps only; reviving a stale snapshot is a material change.
func (h *Hub) SetRuntimeStatus(ctx context.Context, c auth.Caller, asPeer string, p RuntimeStatusReport) error {
	if c.Kind != "peer" && c.Kind != "principal" {
		return E(http.StatusForbidden, "forbidden", "this credential cannot report runtime status")
	}
	if c.Kind == "principal" && asPeer == "" {
		return E(http.StatusUnprocessableEntity, "as_required", "a principal credential must name the reporting peer with as")
	}
	if err := validateRuntimeStatus(p); err != nil {
		return err
	}
	var peer string
	changed := false
	err := h.DB.Tx(ctx, func(tx *sql.Tx) error {
		snd, err := h.resolveSender(ctx, tx, c, asPeer)
		if err != nil {
			return err
		}
		peer = snd.peer
		var old RuntimeStatus
		err = tx.QueryRowContext(ctx, `SELECT runtime,state,delivery,reason,version,updated_at,expires_at FROM peer_runtime_status WHERE peer_id=?`, peer).Scan(&old.Runtime, &old.State, &old.Delivery, &old.Reason, &old.Version, &old.UpdatedAt, &old.ExpiresAt)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("hub: read runtime status: %w", err)
		}
		now := h.Now().UTC().Truncate(time.Millisecond)
		changed = errors.Is(err, sql.ErrNoRows) || old.Runtime != p.Runtime || old.State != p.State || old.Delivery != p.Delivery || !sameRuntimeString(old.Reason, p.Reason) || !sameRuntimeString(old.Version, p.Version) || old.stale(now)
		_, err = tx.ExecContext(ctx, `INSERT INTO peer_runtime_status(peer_id,runtime,state,delivery,reason,version,updated_at,expires_at)
   VALUES (?,?,?,?,?,?,?,?) ON CONFLICT(peer_id) DO UPDATE SET
   runtime=excluded.runtime,state=excluded.state,delivery=excluded.delivery,
   reason=excluded.reason,version=excluded.version,updated_at=excluded.updated_at,expires_at=excluded.expires_at`,
			peer, p.Runtime, p.State, p.Delivery, p.Reason, p.Version, envelope.FormatTime(now), envelope.FormatTime(now.Add(time.Duration(p.TTLSeconds)*time.Second)))
		if err != nil {
			return fmt.Errorf("hub: write runtime status: %w", err)
		}
		if changed {
			return h.DirectoryUpdated(ctx, tx, peer, "updated", []string{"runtime_status"})
		}
		return nil
	})
	if err != nil {
		return err
	}
	if changed {
		h.notifyEnabled(ctx)
	}
	if peer != c.PeerID {
		h.TouchReachability(ctx, peer, "reachable")
	}
	return nil
}

// RuntimeStatuses loads every persisted snapshot in one query for directory
// and admin batch projections, including disabled peers in the latter. Sources
// cannot have a row. Reads project expiry without writes or durable events.
func (h *Hub) RuntimeStatuses(ctx context.Context) (map[string]*RuntimeStatus, error) {
	rows, err := h.DB.QueryContext(ctx, `SELECT peer_id,runtime,state,delivery,reason,version,updated_at,expires_at FROM peer_runtime_status`)
	if err != nil {
		return nil, fmt.Errorf("hub: runtime statuses: %w", err)
	}
	defer rows.Close()
	out := map[string]*RuntimeStatus{}
	now := h.Now()
	for rows.Next() {
		var peer string
		var s RuntimeStatus
		if err := rows.Scan(&peer, &s.Runtime, &s.State, &s.Delivery, &s.Reason, &s.Version, &s.UpdatedAt, &s.ExpiresAt); err != nil {
			return nil, fmt.Errorf("hub: runtime statuses: %w", err)
		}
		if s.stale(now) {
			reason := "stale"
			s.State, s.Delivery, s.Reason = "unknown", "unknown", &reason
		}
		out[peer] = &s
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("hub: runtime statuses: %w", err)
	}
	return out, nil
}
