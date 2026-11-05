package hub

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/XertroV/vecnet/internal/audit"
	"github.com/XertroV/vecnet/internal/auth"
	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/prefs"
	"github.com/XertroV/vecnet/internal/store"
)

// Preferences owns the hub's one persistent preference store. Bootstrap is
// copied at construction. Writes serialize commit, immutable publication and
// notification; no write mutates Hub.Cfg. LoadSnapshot is the transactional
// reader seam for later consumers; Current is the last locally published view.
type Preferences struct {
	db          *store.DB
	now         func() time.Time
	bootstrap   map[string]prefs.Effective
	digest      string
	maxExpiry   int64
	installed   map[string]bool
	writeMu     sync.Mutex
	current     atomic.Pointer[prefs.Snapshot]
	subMu       sync.Mutex
	subscribers map[chan uint64]struct{}
	previewKey  []byte
	// Policy is an optional verified consumer adapter, installed before serving.
	Policy PreferencePreviewPolicy

	// Optional consumer overrides are installed before requests start. Without
	// them the shared preview/confirmation implementation applies. Authorization runs inside
	// the writer transaction and may persist activation/approval boundaries.
	// Its result contains server-derived approved risk codes, never confirmation
	// strings or other client-supplied approval material.
	// Retrospective writes also require a verified Policy adapter.
	PreviewHook    func(context.Context, auth.Caller, prefs.Snapshot, prefs.Patch) (prefs.Preview, error)
	AuthorizePatch func(context.Context, *sql.Tx, auth.Caller, prefs.Snapshot, prefs.Snapshot, prefs.Patch) ([]string, error)
}

func preferenceError(code string) error { return &prefs.Error{Code: code} }

// preferenceBootstrapSeconds validates the original duration before conversion.
// Exactly zero retains Config's unset semantics; truncation must never make a
// nonzero duration look unset or silently change its effective value.
func preferenceBootstrapSeconds(key string, value time.Duration, min, max int64) (int64, error) {
	if value == 0 {
		return 0, nil
	}
	constraint := ""
	switch {
	case value < 0:
		constraint = "nonnegative"
	case value%time.Second != 0:
		constraint = "whole_seconds"
	case value < time.Duration(min)*time.Second || value > time.Duration(max)*time.Second:
		constraint = "range"
	}
	if constraint != "" {
		return 0, &prefs.Error{Code: prefs.CodeInvalidValue, Details: prefs.ErrorDetails{Field: key, Constraint: constraint}}
	}
	return int64(value / time.Second), nil
}

// newPreferences builds the service. installed names gated keys whose
// runtime adapter is present (Task 3 supplies them at construction, so the
// startup load accepts their stored rows); every other gated key stays
// unavailable and writes to it fail closed.
func newPreferences(db *store.DB, cfg Config, now func() time.Time, installed ...string) (*Preferences, error) {
	maxExpiry, err := preferenceBootstrapSeconds("agents.max_expiry", cfg.MaxExpiry, 1, prefs.MaxExpirySeconds)
	if err != nil {
		return nil, err
	}
	p := &Preferences{db: db, now: now, bootstrap: map[string]prefs.Effective{}, maxExpiry: maxExpiry, installed: map[string]bool{}, subscribers: map[chan uint64]struct{}{}}
	if err := p.loadPreviewKey(); err != nil {
		return nil, err
	}
	if p.maxExpiry == 0 {
		p.maxExpiry = prefs.MaxExpirySeconds
	}
	for _, key := range installed {
		d, err := prefs.Lookup(key)
		if err != nil {
			return nil, err
		}
		if d.Runtime != "unavailable" {
			return nil, preferenceError(prefs.CodeUnavailable)
		}
		p.installed[key] = true
	}
	for _, d := range prefs.Definitions() {
		p.bootstrap[d.Key] = prefs.Effective{Value: d.Default, Source: prefs.SourceDefault}
	}
	// Programmatic Config values remain a supported bootstrap surface. Explicit
	// flag/env provenance, including values equal to defaults, takes precedence.
	// Read-only descriptors for existing Config fields (text cap, avatar rate,
	// max expiry, blob token lifetime) report the value the hub enforces.
	configured := map[string]prefs.Value{
		"agents.default_max_depth":      prefs.Int(int64(cfg.DefaultMaxDepth)),
		"agents.thread_budget":          prefs.Int(int64(cfg.ThreadBudget)),
		"rates.sends_per_minute":        prefs.Int(int64(cfg.SendsPerMin)),
		"rates.threads_per_hour":        prefs.Int(int64(cfg.ThreadsPerHour)),
		"provisioning.creates_per_hour": prefs.Int(int64(cfg.CreatesPerHour)),
		"agents.text_cap":               prefs.Int(int64(cfg.TextCap)),
		"rates.avatar_changes_per_hour": prefs.Int(int64(cfg.AvatarChangesPerHour)),
	}
	for _, duration := range []struct {
		key   string
		value time.Duration
	}{
		{"retention.ack_events", cfg.AckedEventRetention},
		{"events.refresh_interval", cfg.EventsRefreshInterval},
		{"presence.default_ttl", cfg.PresenceTTL},
		{"presence.session_ttl", cfg.SessionPresenceTTL},
		{"presence.launcher_ttl", cfg.LauncherPresenceTTL},
		{"agents.default_ttl", cfg.DefaultTTL},
		{"agents.max_expiry", cfg.MaxExpiry},
		{"files.read_token_ttl", cfg.BlobTokenTTL},
	} {
		d, _ := prefs.Lookup(duration.key)
		seconds, err := preferenceBootstrapSeconds(duration.key, duration.value, *d.Min, *d.Max)
		if err != nil {
			return nil, err
		}
		configured[duration.key] = prefs.Duration(seconds)
	}
	for key, v := range configured {
		d, _ := prefs.Lookup(key)
		n, _ := v.Int64()
		if v != d.Default && n != 0 {
			p.bootstrap[key] = prefs.Effective{Value: v, Source: prefs.SourceFlag}
		}
	}
	for key, e := range cfg.PreferenceBootstrap {
		d, err := prefs.Lookup(key)
		if err != nil {
			return nil, err
		}
		if !d.Editable && !p.installed[key] {
			return nil, preferenceError(prefs.CodeUnavailable)
		}
		if e.Source != prefs.SourceFlag {
			return nil, preferenceError(prefs.CodeInvalidValue)
		}
		if err := prefs.Validate(d, e.Value); err != nil {
			return nil, err
		}
		e.Value = normalizePreference(d, e.Value)
		e.Revision = 0
		e.UpdatedAt, e.UpdatedBy = "", ""
		p.bootstrap[key] = e
	}
	values := map[string]prefs.Value{}
	for key, e := range p.bootstrap {
		values[key] = e.Value
	}
	if err := prefs.ValidatePolicy(values, p.maxExpiry); err != nil {
		return nil, err
	}
	// encoding/json orders map keys, producing a canonical bounded scalar input.
	canonical, err := json.Marshal(struct {
		Bootstrap          map[string]prefs.Effective
		Registry           string
		Policy             string
		MaxExpiry          int64
		Installed          map[string]bool
		HardEventRetention time.Duration
	}{p.bootstrap, prefs.RegistryVersion, cfg.PreferencePolicyGeneration, p.maxExpiry, p.installed, cfg.UnackedEventRetention})
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(canonical)
	p.digest = hex.EncodeToString(sum[:])
	snapshot, err := p.LoadSnapshot(context.Background(), nil)
	if err != nil {
		return nil, preferenceStartupError(err)
	}
	p.current.Store(&snapshot)
	return p, nil
}

// bindRetentionGeneration runs only during hub construction, before workers or
// requests can observe Preferences. Retention's persisted bootstrap generation
// is part of the shared ETag/confirmation domain; returning to the same flags
// cannot revive an unused preview from an earlier destructive transition.
func (p *Preferences) bindRetentionGeneration(ctx context.Context, generation uint64) error {
	if generation == 0 {
		return nil
	}
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s:retention:%d", p.digest, generation)))
	p.digest = hex.EncodeToString(sum[:])
	snapshot, err := p.LoadSnapshot(ctx, nil)
	if err != nil {
		return err
	}
	p.current.Store(&snapshot)
	return nil
}

func preferenceStartupError(err error) error {
	var e *prefs.Error
	if errors.As(err, &e) {
		// ErrorDetails never contains a raw unknown key or rejected scalar.
		return fmt.Errorf("preferences startup: %s %s %s; see operator recovery runbook (docs/OPERATOR.md#hub-preferences-recovery)", e.Details.Field, e.Code, e.Details.Constraint)
	}
	return errors.New("preferences startup: prefs_storage_unavailable; see operator recovery runbook (docs/OPERATOR.md#hub-preferences-recovery)")
}

// Current returns a value copy; Snapshot's descriptors are defensively copied.
func (p *Preferences) Current() prefs.Snapshot { return *p.current.Load() }

// LoadSnapshot reads both revision and overrides in the caller's transaction.
// nil opens one consistent standalone transaction, never two independent reads.
func (p *Preferences) LoadSnapshot(ctx context.Context, tx *sql.Tx) (prefs.Snapshot, error) {
	if tx != nil {
		s, _, err := p.load(ctx, tx)
		return s, err
	}
	tx, err := p.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return prefs.Snapshot{}, preferenceError(prefs.CodeStorageUnavailable)
	}
	defer tx.Rollback()
	s, _, err := p.load(ctx, tx)
	if err != nil {
		return prefs.Snapshot{}, err
	}
	if err := tx.Commit(); err != nil {
		return prefs.Snapshot{}, preferenceError(prefs.CodeStorageUnavailable)
	}
	return s, nil
}

func (p *Preferences) load(ctx context.Context, tx *sql.Tx) (prefs.Snapshot, map[string]prefs.Effective, error) {
	var revision uint64
	if err := tx.QueryRowContext(ctx, `SELECT revision FROM hub_preferences_state WHERE id=1`).Scan(&revision); err != nil {
		return prefs.Snapshot{}, nil, preferenceError(prefs.CodeStorageUnavailable)
	}
	rows, err := tx.QueryContext(ctx, `SELECT key,value_json,updated_at,updated_by FROM hub_preferences ORDER BY key`)
	if err != nil {
		return prefs.Snapshot{}, nil, preferenceError(prefs.CodeStorageUnavailable)
	}
	defer rows.Close()
	stored := map[string]prefs.Effective{}
	for rows.Next() {
		var key, raw, updatedAt, updatedBy string
		if err := rows.Scan(&key, &raw, &updatedAt, &updatedBy); err != nil {
			return prefs.Snapshot{}, nil, preferenceError(prefs.CodeStorageUnavailable)
		}
		d, err := prefs.Lookup(key)
		if err != nil {
			return prefs.Snapshot{}, nil, err
		}
		if !p.editable(d) {
			return prefs.Snapshot{}, nil, &prefs.Error{Code: prefs.CodeUnavailable, Details: prefs.ErrorDetails{Field: key}}
		}
		var value prefs.Value
		if err := json.Unmarshal([]byte(raw), &value); err != nil {
			return prefs.Snapshot{}, nil, &prefs.Error{Code: prefs.CodeInvalidValue, Details: prefs.ErrorDetails{Field: key, Constraint: "scalar"}}
		}
		if err := prefs.Validate(d, value); err != nil {
			return prefs.Snapshot{}, nil, err
		}
		stored[key] = prefs.Effective{Value: normalizePreference(d, value), Source: prefs.SourceStored, UpdatedAt: updatedAt, UpdatedBy: updatedBy}
	}
	if err := rows.Err(); err != nil {
		return prefs.Snapshot{}, nil, preferenceError(prefs.CodeStorageUnavailable)
	}
	s, err := p.snapshot(revision, stored)
	return s, stored, err
}
func normalizePreference(d prefs.Definition, v prefs.Value) prefs.Value {
	if d.Type == prefs.TypeDuration {
		if n, ok := v.Int64(); ok {
			return prefs.Duration(n)
		}
	}
	return v
}
func (p *Preferences) snapshot(revision uint64, stored map[string]prefs.Effective) (prefs.Snapshot, error) {
	values := map[string]prefs.Effective{}
	policy := map[string]prefs.Value{}
	for key, e := range p.bootstrap {
		values[key] = e
		policy[key] = e.Value
	}
	for key, v := range stored {
		values[key] = v
		policy[key] = v.Value
	}
	if err := prefs.ValidatePolicy(policy, p.maxExpiry); err != nil {
		return prefs.Snapshot{}, err
	}
	return prefs.NewSnapshot(revision, fmt.Sprintf("\"prefs-%d-%s\"", revision, p.digest), p.definitions(), values)
}

// editable reports runtime writability: available keys, or gated keys whose
// adapter was installed at construction.
func (p *Preferences) editable(d prefs.Definition) bool { return d.Editable || p.installed[d.Key] }

// definitions are the registry copies this hub publishes, with installed
// gated keys shown as live.
func (p *Preferences) definitions() []prefs.Definition {
	defs := prefs.Definitions()
	for i := range defs {
		if p.installed[defs[i].Key] {
			defs[i].Editable, defs[i].Runtime, defs[i].UnavailableReason = true, "live", ""
		}
	}
	return defs
}

// applyExistingKey reports whether apply_existing may authorize existing-data
// work for key: only DB retention keys (plan Decision 5).
func applyExistingKey(key string) bool { return strings.HasPrefix(key, "retention.") }
func preferenceAuthority(caller auth.Caller) error {
	if caller.Kind != "operator" || caller.PeerKind != "" && caller.PeerKind != "operator" {
		return E(http.StatusForbidden, "forbidden", "Operator credential required.")
	}
	return nil
}
func (p *Preferences) ReadPreferences(ctx context.Context, caller auth.Caller) (prefs.Snapshot, error) {
	if err := preferenceAuthority(caller); err != nil {
		return prefs.Snapshot{}, err
	}
	return p.LoadSnapshot(ctx, nil)
}
func preferenceMatch(expected string, s prefs.Snapshot) error {
	valid := len(expected) >= 2 && expected[0] == '"' && expected[len(expected)-1] == '"'
	if valid {
		for _, b := range []byte(expected[1 : len(expected)-1]) {
			if b == '"' || b < 0x21 || b == 0x7f {
				valid = false
				break
			}
		}
	}
	if !valid {
		return preferenceError(prefs.CodePreconditionRequired)
	}
	if expected != s.ETag {
		return preferenceError(prefs.CodeRevisionConflict)
	}
	return nil
}

func (p *Preferences) prospective(s prefs.Snapshot, stored map[string]prefs.Effective, patch prefs.Patch) (prefs.Snapshot, []string, error) {
	if err := patch.Validate(); err != nil {
		return prefs.Snapshot{}, nil, err
	}
	if patch.ApplyExisting && p.AuthorizePatch == nil && p.Policy == nil {
		return prefs.Snapshot{}, nil, preferenceError(prefs.CodeUnavailable)
	}
	changed := []string{}
	for _, key := range preferencePatchKeys(patch) {
		d, _ := prefs.Lookup(key)
		if !p.editable(d) {
			return prefs.Snapshot{}, nil, &prefs.Error{Code: prefs.CodeUnavailable, Details: prefs.ErrorDetails{Field: key}}
		}
		if patch.ApplyExisting && !applyExistingKey(key) {
			return prefs.Snapshot{}, nil, &prefs.Error{Code: prefs.CodeInvalidValue, Details: prefs.ErrorDetails{Field: key, Constraint: "apply_existing"}}
		}
		old, exists := stored[key]
		next, set := patch.Set[key]
		next = normalizePreference(d, next)
		if set && next == d.Default && p.bootstrap[key].Source == prefs.SourceDefault {
			set = false
		}
		if set {
			if !exists || old.Value != next {
				stored[key] = prefs.Effective{Value: next, Source: prefs.SourceStored, UpdatedAt: envelope.FormatTime(p.now()), UpdatedBy: "operator"}
				changed = append(changed, key)
			}
		} else if exists {
			delete(stored, key)
			changed = append(changed, key)
		}
	}
	// An apply_existing authorization is a revision of its own even without a
	// value delta: it records activation boundaries and an audit entry.
	revision := s.Revision
	if len(changed) > 0 || patch.ApplyExisting {
		revision++
	}
	next, err := p.snapshot(revision, stored)
	return next, changed, err
}

// PreviewPreferences validates a read-only proposal, then collects estimates
// and signs its exact confirmation bounds without holding a DB transaction.
func (p *Preferences) PreviewPreferences(ctx context.Context, caller auth.Caller, expectedETag string, patch prefs.Patch) (prefs.Preview, error) {
	if err := preferenceAuthority(caller); err != nil {
		return prefs.Preview{}, err
	}
	// A read snapshot never acquires the serialized writer transaction. Impact
	// estimation happens only after this transaction is closed.
	tx, err := p.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return prefs.Preview{}, preferenceError(prefs.CodeStorageUnavailable)
	}
	defer tx.Rollback()
	before, stored, err := p.load(ctx, tx)
	if err != nil {
		return prefs.Preview{}, sanitizePreferenceError(err)
	}
	if err := preferenceMatch(expectedETag, before); err != nil {
		return prefs.Preview{}, err
	}
	after, _, err := p.prospective(before, stored, patch)
	if err != nil {
		return prefs.Preview{}, sanitizePreferenceError(err)
	}
	if err := tx.Commit(); err != nil {
		return prefs.Preview{}, preferenceError(prefs.CodeStorageUnavailable)
	}
	if p.PreviewHook != nil {
		return p.PreviewHook(ctx, caller, before, patch)
	}
	return p.previewConfirmation(ctx, caller, before, after, patch)
}

func preferenceRisks(before, after prefs.Snapshot) []prefs.Risk {
	risks := []prefs.Risk{}
	for _, d := range after.Descriptors() {
		if !strings.HasPrefix(d.Key, "presence.") || len(d.Danger) == 0 {
			continue
		}
		old, _ := before.Get(d.Key)
		oldN, oldOK := old.Value.Int64()
		newN, newOK := d.Value.Int64()
		if oldOK && newOK && newN < oldN {
			risks = append(risks, prefs.Risk{Code: d.Danger[0], Keys: []string{d.Key}, Message: "Shorter presence TTL may release held wakes."})
		}
	}
	return risks
}
func sanitizePreferenceError(err error) error {
	if err == nil {
		return nil
	}
	var pe *prefs.Error
	var api *APIError
	if errors.As(err, &pe) || errors.As(err, &api) {
		return err
	}
	return preferenceError(prefs.CodeStorageUnavailable)
}

func (p *Preferences) PatchPreferences(ctx context.Context, caller auth.Caller, expectedETag string, patch prefs.Patch) (prefs.Snapshot, error) {
	if err := preferenceAuthority(caller); err != nil {
		return prefs.Snapshot{}, err
	}
	p.writeMu.Lock()
	defer p.writeMu.Unlock()
	var next prefs.Snapshot
	var changed []string
	err := p.db.Tx(ctx, func(tx *sql.Tx) error {
		before, stored, err := p.load(ctx, tx)
		if err != nil {
			return err
		}
		if err := preferenceMatch(expectedETag, before); err != nil {
			return err
		}
		next, changed, err = p.prospective(before, stored, patch)
		if err != nil {
			return err
		}
		if len(changed) == 0 && !patch.ApplyExisting {
			// Supplied approvals must still be valid, but an ordinary no-op
			// cannot activate policy or invoke a persistent authorization hook.
			if len(patch.Confirm) > 0 {
				_, err = p.validateConfirmation(ctx, caller, before, next, patch)
			}
			return err
		}
		approvedRisks := []string{}
		if p.AuthorizePatch != nil {
			approvedRisks, err = p.AuthorizePatch(ctx, tx, caller, before, next, patch)
			if err != nil {
				return err
			}
		} else {
			approvedRisks, err = p.authorizeConfirmation(ctx, tx, caller, before, next, patch)
			if err != nil {
				return err
			}
		}
		changes := make([]any, 0, len(changed))
		for _, key := range changed {
			operation := "reset"
			if value, ok := stored[key]; ok {
				operation = "set"
				raw, err := json.Marshal(value.Value)
				if err != nil {
					return err
				}
				if _, err := tx.ExecContext(ctx, `INSERT INTO hub_preferences(key,value_json,updated_at,updated_by) VALUES(?,?,?,?) ON CONFLICT(key) DO UPDATE SET value_json=excluded.value_json,updated_at=excluded.updated_at,updated_by=excluded.updated_by`, key, string(raw), value.UpdatedAt, value.UpdatedBy); err != nil {
					return err
				}
			} else {
				if _, err := tx.ExecContext(ctx, `DELETE FROM hub_preferences WHERE key=?`, key); err != nil {
					return err
				}
			}
			old, _ := before.Get(key)
			value, _ := next.Get(key)
			changes = append(changes, map[string]any{"key": key, "operation": operation, "old": old, "new": value})
		}
		if _, err := tx.ExecContext(ctx, `UPDATE hub_preferences_state SET revision=? WHERE id=1`, next.Revision); err != nil {
			return err
		}
		targets := changed
		if len(targets) == 0 {
			targets = preferencePatchKeys(patch)
		}
		return audit.Record(ctx, tx, "operator", "prefs_changed", targets, map[string]any{"revision": next.Revision, "changes": changes, "approved_risks": append([]string{}, approvedRisks...), "apply_existing": patch.ApplyExisting})
	})
	if err != nil {
		return prefs.Snapshot{}, sanitizePreferenceError(err)
	}
	p.current.Store(&next)
	if len(changed) > 0 || patch.ApplyExisting {
		p.notify(next.Revision)
	}
	return next, nil
}

// preferencePatchKeys returns the patch's set and reset keys in order.
func preferencePatchKeys(patch prefs.Patch) []string {
	out := make([]string, 0, len(patch.Set)+len(patch.Reset))
	for key := range patch.Set {
		out = append(out, key)
	}
	out = append(out, patch.Reset...)
	sort.Strings(out)
	return out
}

// Subscribe returns a capacity-one latest-generation wakeup. Cancellation
// removes and closes the channel under the same lock used by fan-out.
func (p *Preferences) Subscribe(ctx context.Context) <-chan uint64 {
	ch := make(chan uint64, 1)
	p.subMu.Lock()
	if ctx.Err() != nil {
		close(ch)
		p.subMu.Unlock()
		return ch
	}
	p.subscribers[ch] = struct{}{}
	p.subMu.Unlock()
	go func() { <-ctx.Done(); p.subMu.Lock(); delete(p.subscribers, ch); close(ch); p.subMu.Unlock() }()
	return ch
}
func (p *Preferences) notify(revision uint64) {
	p.subMu.Lock()
	defer p.subMu.Unlock()
	for ch := range p.subscribers {
		select {
		case <-ch:
		default:
		}
		ch <- revision
	}
}
