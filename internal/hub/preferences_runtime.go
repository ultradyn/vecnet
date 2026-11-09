package hub

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/prefs"
)

// preferenceNumber is only used with complete, validated snapshots produced
// by Preferences. Missing registry keys or nonnumeric types are programming
// errors, never a reason to silently substitute a different runtime policy.
func preferenceNumber(s prefs.Snapshot, key string) int {
	e, err := s.Get(key)
	if err != nil {
		panic("hub: incomplete runtime preference snapshot")
	}
	n, ok := e.Value.Int64()
	if !ok {
		panic("hub: nonnumeric runtime preference")
	}
	return int(n)
}

func preferenceDuration(s prefs.Snapshot, key string) time.Duration {
	return time.Duration(preferenceNumber(s, key)) * time.Second
}

type resolvedPresenceTTL struct {
	Duration time.Duration
	Key      string
	Source   prefs.Source
	Origin   string
}

func presencePreference(s prefs.Snapshot, key string) prefs.Effective {
	value, err := s.Get(key)
	if err != nil {
		panic("hub: incomplete runtime preference snapshot")
	}
	return value
}

// Resolution uses provenance, never numeric equality, to identify explicit intent.
func resolvePresenceTTL(s prefs.Snapshot, kind string) resolvedPresenceTTL {
	return resolvePresenceTTLUsing(kind, func(key string) prefs.Effective {
		return presencePreference(s, key)
	})
}

// The same pure selection also supports restoring one key for risk attribution.
func resolvePresenceTTLUsing(kind string, lookup func(string) prefs.Effective) resolvedPresenceTTL {
	key := "presence.default_ttl"
	switch kind {
	case "session":
		key = "presence.session_ttl"
	case "launcher":
		key = "presence.launcher_ttl"
	case "grokbot":
		key = "presence.grokbot_ttl"
	}
	value := lookup(key)
	if kind == "grokbot" && value.Source == prefs.SourceDefault {
		inherited := lookup("presence.default_ttl")
		if inherited.Source != prefs.SourceDefault {
			key, value = "presence.default_ttl", inherited
		}
	}
	seconds, ok := value.Value.Int64()
	if !ok {
		panic("hub: nonnumeric runtime preference")
	}
	return resolvedPresenceTTL{Duration: time.Duration(seconds) * time.Second, Key: key, Source: value.Source, Origin: value.Origin}
}

func presencePolicyChanged(before, after prefs.Snapshot, key string) bool {
	old, next := presencePreference(before, key), presencePreference(after, key)
	return old.Value != next.Value || old.Source != next.Source || old.Origin != next.Origin
}

// Attribute shortening to changed dependency keys whose restoration would
// lengthen the resulting policy. This excludes a simultaneously lengthened D
// when an explicit G is responsible, while retaining joint G/D dependencies.
func presenceShorteningKeys(before, after prefs.Snapshot, kind string, next resolvedPresenceTTL) []string {
	candidates := []string{"presence.default_ttl"}
	switch kind {
	case "grokbot":
		// D can affect inheritance even when G controls both endpoint policies.
		candidates = []string{"presence.grokbot_ttl", "presence.default_ttl"}
	case "session":
		candidates = []string{"presence.session_ttl"}
	case "launcher":
		candidates = []string{"presence.launcher_ttl"}
	}
	changed, responsible := []string{}, []string{}
	for _, key := range candidates {
		if !presencePolicyChanged(before, after, key) {
			continue
		}
		changed = append(changed, key)
		restored := resolvePresenceTTLUsing(kind, func(candidate string) prefs.Effective {
			if candidate == key {
				return presencePreference(before, candidate)
			}
			return presencePreference(after, candidate)
		})
		if restored.Duration > next.Duration {
			responsible = append(responsible, key)
		}
	}
	// Redundant simultaneous changes can each independently shorten the policy;
	// restoring either alone then leaves the same result. Retain both causes.
	if len(responsible) == 0 {
		responsible = changed
	}
	sort.Strings(responsible)
	return responsible
}

func presenceShorteningRisks(before, after prefs.Snapshot) []prefs.Risk {
	byKey := map[string][]string{}
	// These four policy classes cover every kind without inspecting peer rows.
	for _, kind := range []string{"grokbot", "session", "launcher", "other"} {
		old, next := resolvePresenceTTL(before, kind), resolvePresenceTTL(after, kind)
		if next.Duration >= old.Duration {
			continue
		}
		change := fmt.Sprintf("%s %s -> %s", kind, old.Duration, next.Duration)
		for _, key := range presenceShorteningKeys(before, after, kind, next) {
			byKey[key] = append(byKey[key], change)
		}
	}
	keys := make([]string, 0, len(byKey))
	for key := range byKey {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	risks := []prefs.Risk{}
	for _, key := range keys {
		risks = append(risks, prefs.Risk{
			Code: "presence_shortened", Keys: []string{key},
			Message: "Shorter presence TTL may release held wakes: " + strings.Join(byKey[key], "; ") + ".",
		})
	}
	return risks
}

func effectivePresence(s prefs.Snapshot, p PresenceRow, now time.Time) string {
	if p.State == nil || *p.State == "" || p.At == nil {
		return "unknown"
	}
	at, err := envelope.ParseTime(*p.At)
	if err != nil || now.Sub(at) > resolvePresenceTTL(s, p.Kind).Duration {
		return "unknown"
	}
	return *p.State
}

// A send passes its transaction's one snapshot through fanOut as well as loop
// and budget evaluation. The legacy GateWakeIdle entry point remains usable by
// other callers without opening a nested transaction.
func (h *Hub) gateWakeIdleSnapshot(ctx context.Context, tx *sql.Tx, d *delivery, s prefs.Snapshot, now time.Time) error {
	d.held = false
	if d.priority != "wake_idle" {
		return nil
	}
	var p PresenceRow
	if err := tx.QueryRowContext(ctx, `SELECT kind, presence_state, presence_at FROM peers WHERE peer_id = ?`, d.recipient).
		Scan(&p.Kind, &p.State, &p.At); err != nil {
		return fmt.Errorf("hub: recipient presence: %w", err)
	}
	switch effectivePresence(s, p, now) {
	case "idle", "unknown":
	default:
		d.held = true
	}
	return nil
}
