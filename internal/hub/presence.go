package hub

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"github.com/XertroV/vecnet/internal/auth"
	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/prefs"
)

// Presence staleness defaults (PROTOCOL §8): a report older than its
// per-kind TTL is shown as "unknown".
const (
	DefaultPresenceTTL         = time.Hour
	DefaultGrokbotPresenceTTL  = 10 * time.Minute
	DefaultSessionPresenceTTL  = 15 * time.Minute
	DefaultLauncherPresenceTTL = 15 * time.Minute
)

// PresenceReport is one self-reported presence (PROTOCOL §2.6). A nil
// Capabilities or Quota, or an empty Location, keeps the stored value;
// ClearLocation (an explicit "location": "" in the body) with an empty
// Location clears it. Note is replaced by every report. A zero At, or one in
// the future, is the hub's now.
type PresenceReport struct {
	State         string
	At            time.Time
	Capabilities  []string
	Quota         map[string]any
	Location      string
	ClearLocation bool
	Note          string
}

// PresenceRow is the part of a peers row EffectivePresence reads: the peer's
// kind and its stored presence_state / presence_at (nil when never reported).
type PresenceRow struct {
	Kind  string
	State *string
	At    *string
}

// EffectivePresence is the presence the hub shows for p at now: the stored
// state, or "unknown" when there is none or presence_at is older than the
// resolved kind TTL (factory: Grok 10 min, session/launcher 15 min, others 1 h).
// Exactly the TTL remains fresh. The hub never invents a state (PROTOCOL §8).
func (h *Hub) EffectivePresence(p PresenceRow, now time.Time) string {
	// This legacy projection has no context/transaction parameter and is also
	// called inside transactions. Use the immutable committed publication rather
	// than opening a nested DB read. Transactional operations pass their snapshot
	// directly to effectivePresence.
	return EffectivePresenceSnapshot(h.Preferences.Current(), p, now)
}

// EffectivePresenceSnapshot projects a row using the operation's captured
// preference snapshot. Batch readers load once before iterating; callers in a
// transaction pass their transaction's snapshot without opening a nested read.
func EffectivePresenceSnapshot(s prefs.Snapshot, p PresenceRow, now time.Time) string {
	return effectivePresence(s, p, now)
}

// SetPresence stores a presence report for the caller's peer (asPeer names
// it for a principal credential, as for sends) and fans it out as a presence
// Event to every enabled peer subscribed with "all", and to the peer's
// teammates subscribed with "teams"; never to the peer itself (PROTOCOL
// §2.6, §8). A change to capabilities, quota or location also emits
// directory_updated "updated". The report is validated against
// $defs/presence (400 invalid_body). After commit, an idle report calls
// IdleHook when one is set (by default OnIdle, the held wake_idle release); the hook is
// best-effort, and its error is logged, not returned.
func (h *Hub) SetPresence(ctx context.Context, c auth.Caller, asPeer string, p PresenceReport) error {
	var notify []string
	var peer string
	dirChanged, stale := false, false
	err := h.DB.Tx(ctx, func(tx *sql.Tx) error {
		notify, dirChanged, stale = nil, false, false
		snapshot, err := h.Preferences.LoadSnapshot(ctx, tx)
		if err != nil {
			return err
		}
		snd, err := resolveSender(ctx, tx, c, asPeer)
		if err != nil {
			return err
		}
		peer = snd.peer
		var capsRaw, quotaRaw string
		var loc sql.NullString
		var oldState, oldAt *string
		if err := tx.QueryRowContext(ctx, `SELECT capabilities, quota, location, presence_state, presence_at FROM peers WHERE peer_id = ?`, peer).
			Scan(&capsRaw, &quotaRaw, &loc, &oldState, &oldAt); err != nil {
			return fmt.Errorf("hub: presence peer: %w", err)
		}
		caps := p.Capabilities
		if caps == nil {
			if err := json.Unmarshal([]byte(capsRaw), &caps); err != nil {
				return fmt.Errorf("hub: stored capabilities: %w", err)
			}
		}
		quota := p.Quota
		if quota == nil {
			dec := json.NewDecoder(bytes.NewReader([]byte(quotaRaw)))
			dec.UseNumber()
			if err := dec.Decode(&quota); err != nil {
				return fmt.Errorf("hub: stored quota: %w", err)
			}
		}
		location := p.Location
		if location == "" && loc.Valid && !p.ClearLocation {
			location = loc.String
		}
		nowT := h.Now()
		at := p.At
		if at.IsZero() || at.After(nowT) {
			at = nowT
		}
		// The operator may have two clients open, each with a heartbeat that
		// re-posts the state it last read. A report older than the stored one
		// was overtaken by a newer choice from the other client, so it changes
		// nothing (PROTOCOL §2.6). Both clients date reports on the hub's clock.
		if snd.kind == "operator" && oldAt != nil {
			if prev, err := envelope.ParseTime(*oldAt); err == nil && at.Before(prev) {
				stale = true
				return nil
			}
		}
		pres := envelope.Presence{
			PeerID: peer, State: p.State, At: envelope.FormatTime(at),
			Capabilities: caps, Quota: quota, Location: location, Note: p.Note,
		}
		data, err := json.Marshal(pres)
		if err != nil {
			return fmt.Errorf("hub: encode presence: %w", err)
		}
		if err := envelope.Validate("presence", data); err != nil {
			return err
		}
		if caps == nil {
			caps = []string{}
		}
		if quota == nil {
			quota = map[string]any{}
		}
		newCaps, _ := json.Marshal(caps)
		newQuota, _ := json.Marshal(quota)
		var changed []string
		if !jsonEqual(capsRaw, newCaps) {
			changed = append(changed, "capabilities")
		}
		if !jsonEqual(quotaRaw, newQuota) {
			changed = append(changed, "quota")
		}
		if loc.String != location {
			changed = append(changed, "location")
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE peers SET presence_state = ?, presence_at = ?, capabilities = ?, quota = ?, location = ?, note = ?
			 WHERE peer_id = ?`,
			p.State, pres.At, string(newCaps), string(newQuota), nullIfEmpty(location), nullIfEmpty(p.Note), peer); err != nil {
			return fmt.Errorf("hub: store presence: %w", err)
		}
		subs, err := presenceSubscribers(ctx, tx, peer)
		if err != nil {
			return err
		}
		for _, s := range subs {
			if _, err := h.Log.Append(ctx, tx, s, "presence", nowT, json.RawMessage(data)); err != nil {
				return err
			}
		}
		notify = subs
		if snd.kind == "operator" &&
			effectivePresence(snapshot, PresenceRow{Kind: snd.kind, State: oldState, At: oldAt}, nowT) !=
				effectivePresence(snapshot, PresenceRow{Kind: snd.kind, State: &p.State, At: &pres.At}, nowT) {
			operators, err := h.noteOperatorView(ctx, tx, "presence", "")
			if err != nil {
				return err
			}
			notify = append(notify, operators...)
		}
		if len(changed) > 0 {
			dirChanged = true
			return h.DirectoryUpdated(ctx, tx, peer, "updated", changed)
		}
		return nil
	})
	if err != nil {
		return err
	}
	if stale {
		return nil
	}
	if len(notify) > 0 {
		h.Log.Notify(notify...)
	}
	if dirChanged {
		h.notifyEnabled(ctx)
	}
	if peer != c.PeerID {
		// A principal acting with ?as= is an inbound call by that peer
		// (PROTOCOL §8, §13.6); the middleware only sees the credential.
		h.TouchReachability(ctx, peer, "reachable")
	}
	if p.State == "idle" && h.IdleHook != nil {
		// Best-effort: the report has taken effect, so a hook failure must
		// not turn it into an error the client would retry.
		if err := h.IdleHook(ctx, peer); err != nil {
			slog.Error("hub: idle hook", "peer", peer, "err", err)
		}
	}
	return nil
}

// presenceSubscribers lists the enabled peers, other than peer, that receive
// peer's presence Events: subscribe_presence "all", or "teams" and sharing a
// team with peer.
func presenceSubscribers(ctx context.Context, tx *sql.Tx, peer string) ([]string, error) {
	rows, err := tx.QueryContext(ctx,
		`SELECT p.peer_id FROM peers p
		 WHERE p.disabled = 0 AND p.peer_id != ?1 AND (
		   p.subscribe_presence = 'all' OR (p.subscribe_presence = 'teams' AND EXISTS (
		     SELECT 1 FROM team_members mine JOIN team_members theirs ON theirs.team_id = mine.team_id
		     WHERE mine.peer_id = ?1 AND theirs.peer_id = p.peer_id)))
		 ORDER BY p.peer_id`, peer)
	if err != nil {
		return nil, fmt.Errorf("hub: presence subscribers: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, fmt.Errorf("hub: presence subscribers: %w", err)
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("hub: presence subscribers: %w", err)
	}
	return out, nil
}

// jsonEqual compares a stored JSON text with freshly encoded JSON by value.
func jsonEqual(stored string, fresh []byte) bool {
	var a, b any
	if json.Unmarshal([]byte(stored), &a) != nil || json.Unmarshal(fresh, &b) != nil {
		return false
	}
	ab, _ := json.Marshal(a)
	bb, _ := json.Marshal(b)
	return slices.Equal(ab, bb)
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}
