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
	writeMu     sync.Mutex
	current     atomic.Pointer[prefs.Snapshot]
	subMu       sync.Mutex
	subscribers map[chan uint64]struct{}

	// Task 4 installs these before requests start. Authorization runs inside
	// the writer transaction and may persist activation/approval boundaries.
	// With no hook, dangerous or retrospective writes fail closed.
	PreviewHook    func(context.Context, auth.Caller, prefs.Snapshot, prefs.Patch) (prefs.Preview, error)
	AuthorizePatch func(context.Context, *sql.Tx, auth.Caller, prefs.Snapshot, prefs.Snapshot, prefs.Patch) error
}

func preferenceError(code string) error { return &prefs.Error{Code: code} }

func newPreferences(db *store.DB, cfg Config, now func() time.Time) (*Preferences, error) {
	p := &Preferences{db: db, now: now, bootstrap: map[string]prefs.Effective{}, maxExpiry: int64(cfg.MaxExpiry / time.Second), subscribers: map[chan uint64]struct{}{}}
	if p.maxExpiry == 0 {
		p.maxExpiry = prefs.MaxExpirySeconds
	}
	for _, d := range prefs.Definitions() {
		p.bootstrap[d.Key] = prefs.Effective{Value: d.Default, Source: prefs.SourceDefault}
	}
	// Programmatic Config values remain a supported bootstrap surface. Explicit
	// flag/env provenance, including values equal to defaults, takes precedence.
	configured := map[string]prefs.Value{
		"events.refresh_interval":       prefs.Duration(int64(cfg.EventsRefreshInterval / time.Second)),
		"presence.default_ttl":          prefs.Duration(int64(cfg.PresenceTTL / time.Second)),
		"presence.session_ttl":          prefs.Duration(int64(cfg.SessionPresenceTTL / time.Second)),
		"presence.launcher_ttl":         prefs.Duration(int64(cfg.LauncherPresenceTTL / time.Second)),
		"agents.default_max_depth":      prefs.Int(int64(cfg.DefaultMaxDepth)),
		"agents.default_ttl":            prefs.Duration(int64(cfg.DefaultTTL / time.Second)),
		"agents.thread_budget":          prefs.Int(int64(cfg.ThreadBudget)),
		"rates.sends_per_minute":        prefs.Int(int64(cfg.SendsPerMin)),
		"rates.threads_per_hour":        prefs.Int(int64(cfg.ThreadsPerHour)),
		"provisioning.creates_per_hour": prefs.Int(int64(cfg.CreatesPerHour)),
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
		if !d.Editable {
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
		Bootstrap map[string]prefs.Effective
		Registry  string
		Policy    string
		MaxExpiry int64
	}{p.bootstrap, prefs.RegistryVersion, cfg.PreferencePolicyGeneration, p.maxExpiry})
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

func (p *Preferences) load(ctx context.Context, tx *sql.Tx) (prefs.Snapshot, map[string]prefs.Value, error) {
	var revision uint64
	if err := tx.QueryRowContext(ctx, `SELECT revision FROM hub_preferences_state WHERE id=1`).Scan(&revision); err != nil {
		return prefs.Snapshot{}, nil, preferenceError(prefs.CodeStorageUnavailable)
	}
	rows, err := tx.QueryContext(ctx, `SELECT key,value_json FROM hub_preferences ORDER BY key`)
	if err != nil {
		return prefs.Snapshot{}, nil, preferenceError(prefs.CodeStorageUnavailable)
	}
	defer rows.Close()
	stored := map[string]prefs.Value{}
	for rows.Next() {
		var key, raw string
		if err := rows.Scan(&key, &raw); err != nil {
			return prefs.Snapshot{}, nil, preferenceError(prefs.CodeStorageUnavailable)
		}
		d, err := prefs.Lookup(key)
		if err != nil {
			return prefs.Snapshot{}, nil, err
		}
		if !d.Editable {
			return prefs.Snapshot{}, nil, &prefs.Error{Code: prefs.CodeUnavailable, Details: prefs.ErrorDetails{Field: key}}
		}
		var value prefs.Value
		if err := json.Unmarshal([]byte(raw), &value); err != nil {
			return prefs.Snapshot{}, nil, &prefs.Error{Code: prefs.CodeInvalidValue, Details: prefs.ErrorDetails{Field: key, Constraint: "scalar"}}
		}
		if err := prefs.Validate(d, value); err != nil {
			return prefs.Snapshot{}, nil, err
		}
		stored[key] = normalizePreference(d, value)
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
func (p *Preferences) snapshot(revision uint64, stored map[string]prefs.Value) (prefs.Snapshot, error) {
	values := map[string]prefs.Effective{}
	policy := map[string]prefs.Value{}
	for key, e := range p.bootstrap {
		values[key] = e
		policy[key] = e.Value
	}
	for key, v := range stored {
		values[key] = prefs.Effective{Value: v, Source: prefs.SourceStored}
		policy[key] = v
	}
	if err := prefs.ValidatePolicy(policy, p.maxExpiry); err != nil {
		return prefs.Snapshot{}, err
	}
	return prefs.NewSnapshot(revision, fmt.Sprintf("\"prefs-%d-%s\"", revision, p.digest), prefs.Definitions(), values)
}
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
	if expected == "" || expected == "*" || strings.HasPrefix(expected, "W/") || strings.Contains(expected, ",") || !strings.HasPrefix(expected, "\"prefs-") || !strings.HasSuffix(expected, "\"") {
		return preferenceError(prefs.CodePreconditionRequired)
	}
	if expected != s.ETag {
		return preferenceError(prefs.CodeRevisionConflict)
	}
	return nil
}
func (p *Preferences) prospective(s prefs.Snapshot, stored map[string]prefs.Value, patch prefs.Patch) (prefs.Snapshot, []string, error) {
	if err := patch.Validate(); err != nil {
		return prefs.Snapshot{}, nil, err
	}
	if patch.ApplyExisting && p.AuthorizePatch == nil {
		return prefs.Snapshot{}, nil, preferenceError(prefs.CodeUnavailable)
	}
	keys := make([]string, 0, len(patch.Set)+len(patch.Reset))
	for key := range patch.Set {
		keys = append(keys, key)
	}
	keys = append(keys, patch.Reset...)
	sort.Strings(keys)
	changed := []string{}
	for _, key := range keys {
		d, _ := prefs.Lookup(key)
		if !d.Editable {
			return prefs.Snapshot{}, nil, &prefs.Error{Code: prefs.CodeUnavailable, Details: prefs.ErrorDetails{Field: key}}
		}
		old, exists := stored[key]
		next, set := patch.Set[key]
		next = normalizePreference(d, next)
		if set && next == d.Default && p.bootstrap[key].Source == prefs.SourceDefault {
			set = false
		}
		if set {
			if !exists || old != next {
				stored[key] = next
				changed = append(changed, key)
			}
		} else if exists {
			delete(stored, key)
			changed = append(changed, key)
		}
	}
	revision := s.Revision
	if len(changed) > 0 {
		revision++
	}
	next, err := p.snapshot(revision, stored)
	return next, changed, err
}

// PreviewPreferences provides the validated core seam. Task 4 supplies the
// bound preview/confirmation implementation through PreviewHook.
func (p *Preferences) PreviewPreferences(ctx context.Context, caller auth.Caller, expectedETag string, patch prefs.Patch) (prefs.Preview, error) {
	if err := preferenceAuthority(caller); err != nil {
		return prefs.Preview{}, err
	}
	var before, after prefs.Snapshot
	err := p.db.Tx(ctx, func(tx *sql.Tx) error {
		var stored map[string]prefs.Value
		var err error
		before, stored, err = p.load(ctx, tx)
		if err != nil {
			return err
		}
		if err := preferenceMatch(expectedETag, before); err != nil {
			return err
		}
		after, _, err = p.prospective(before, stored, patch)
		return err
	})
	if err != nil {
		return prefs.Preview{}, sanitizePreferenceError(err)
	}
	if p.PreviewHook != nil {
		return p.PreviewHook(ctx, caller, before, patch)
	}
	return prefs.Preview{BaseRevision: before.Revision, Risks: preferenceRisks(before, after)}, nil
}
func preferenceRisks(before, after prefs.Snapshot) []prefs.Risk {
	risks := []prefs.Risk{}
	for _, d := range after.Descriptors() {
		if len(d.Danger) == 0 {
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
		if len(changed) == 0 {
			return nil
		}
		if p.AuthorizePatch != nil {
			if err := p.AuthorizePatch(ctx, tx, caller, before, next, patch); err != nil {
				return err
			}
		} else if len(preferenceRisks(before, next)) > 0 || len(patch.Confirm) > 0 {
			return preferenceError(prefs.CodeConfirmationRequired)
		}
		changes := make([]any, 0, len(changed))
		for _, key := range changed {
			operation := "reset"
			if value, ok := stored[key]; ok {
				operation = "set"
				raw, err := json.Marshal(value)
				if err != nil {
					return err
				}
				if _, err := tx.ExecContext(ctx, `INSERT INTO hub_preferences(key,value_json,updated_at,updated_by) VALUES(?,?,?,?) ON CONFLICT(key) DO UPDATE SET value_json=excluded.value_json,updated_at=excluded.updated_at,updated_by=excluded.updated_by`, key, string(raw), envelope.FormatTime(p.now()), "operator"); err != nil {
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
		return audit.Record(ctx, tx, "operator", "prefs_changed", changed, map[string]any{"revision": next.Revision, "changes": changes, "approved_risks": append([]string{}, patch.Confirm...)})
	})
	if err != nil {
		return prefs.Snapshot{}, sanitizePreferenceError(err)
	}
	p.current.Store(&next)
	if len(changed) > 0 {
		p.notify(next.Revision)
	}
	return next, nil
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
