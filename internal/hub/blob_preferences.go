package hub

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/XertroV/vecnet/internal/prefs"
	"github.com/XertroV/vecnet/internal/store"
)

// Install all production consumers and their policy before loading stored
// overrides. Retained R2 support stays installed when uploads roll back to disk.
func newProductionPreferences(h *Hub) (*Preferences, error) {
	policy := &productionPreferencePolicy{retention: &HistoryRetentionAdapter{Hub: h}}
	installed := []string{"retention.ack_events", "retention.archive_mode", "retention.history"}
	if h.Cfg.BlobBackend == "r2" || h.Cfg.R2.AccountID != "" {
		if err := ValidateBlobConfig(h.Cfg); err != nil {
			return nil, err
		}
		policy.blobs = &r2PreferencePolicy{db: h.DB, quota: h.Cfg.R2.StorageQuotaBytes}
		installed = append(installed, "blobs.expiry")
	}
	return newPreferencesWithPolicy(h.DB, h.Cfg, h.Now, policy, installed...)
}

// Each consumer receives only its own keys. Shared preferences still owns
// validation, confirmation and the one transaction for all activation metadata.
type productionPreferencePolicy struct {
	retention *HistoryRetentionAdapter
	blobs     *r2PreferencePolicy
}

func (p *productionPreferencePolicy) preferenceGeneration() string {
	generation := "db-retention-v1"
	if p.blobs != nil {
		generation += ":" + p.blobs.preferenceGeneration()
	}
	return generation
}

func dbRetentionPatch(p prefs.Patch) prefs.Patch {
	out := p
	out.Set = map[string]prefs.Value{}
	out.Reset = nil
	for key, value := range p.Set {
		if key != "blobs.expiry" {
			out.Set[key] = value
		}
	}
	for _, key := range p.Reset {
		if key != "blobs.expiry" {
			out.Reset = append(out.Reset, key)
		}
	}
	return out
}

func (p *productionPreferencePolicy) Check(ctx context.Context, before, after prefs.Snapshot, patch prefs.Patch) error {
	if err := p.retention.Check(ctx, before, after, dbRetentionPatch(patch)); err != nil {
		return err
	}
	if p.blobs != nil {
		return p.blobs.Check(ctx, before, after, patch)
	}
	return nil
}

func (p *productionPreferencePolicy) Preview(ctx context.Context, before, after prefs.Snapshot, patch prefs.Patch) (prefs.Impact, error) {
	impact, err := p.retention.Preview(ctx, before, after, dbRetentionPatch(patch))
	if err != nil || p.blobs == nil {
		return impact, err
	}
	blobs, err := p.blobs.Preview(ctx, before, after, patch)
	impact.Storage = blobs.Storage
	return impact, err
}

func (p *productionPreferencePolicy) Activate(ctx context.Context, tx *sql.Tx, before, after prefs.Snapshot, patch prefs.Patch, impact prefs.Impact, digest string) error {
	if err := p.retention.Activate(ctx, tx, before, after, dbRetentionPatch(patch), impact, digest); err != nil {
		return err
	}
	if p.blobs != nil {
		return p.blobs.Activate(ctx, tx, before, after, patch, impact, digest)
	}
	return nil
}

type r2PreferencePolicy struct {
	db    *store.DB
	quota int64
}

// Bind approvals to the installed consumer and immutable bootstrap quota. A
// restart with a different quota requires a new preview, even at the same revision.
func (p *r2PreferencePolicy) preferenceGeneration() string {
	return fmt.Sprintf("r2-sha256-keep-v1:%d", p.quota)
}

func (p *r2PreferencePolicy) Check(_ context.Context, _, _ prefs.Snapshot, patch prefs.Patch) error {
	if touchesBlobExpiry(patch) && (p.db == nil || p.quota <= 0) {
		return preferenceError(prefs.CodePolicyLocked)
	}
	return nil
}

func touchesBlobExpiry(patch prefs.Patch) bool {
	for _, key := range preferencePatchKeys(patch) {
		if key == "blobs.expiry" {
			return true
		}
	}
	return false
}

func (p *r2PreferencePolicy) storage(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}) (*prefs.StorageUsage, error) {
	usage := &prefs.StorageUsage{LimitBytes: p.quota}
	if err := q.QueryRowContext(ctx, `SELECT retained_bytes,storage_reserved_bytes FROM blob_r2_storage WHERE singleton=1`).Scan(&usage.RetainedBytes, &usage.ReservedBytes); err != nil {
		return nil, preferenceError(prefs.CodeStorageUnavailable)
	}
	usage.UsedBytes = usage.RetainedBytes + usage.ReservedBytes
	if !usage.Valid() {
		return nil, preferenceError(prefs.CodeStorageUnavailable)
	}
	return usage, nil
}

func (p *r2PreferencePolicy) Preview(ctx context.Context, _, _ prefs.Snapshot, patch prefs.Patch) (prefs.Impact, error) {
	if !touchesBlobExpiry(patch) {
		return prefs.Impact{}, nil
	}
	usage, err := p.storage(ctx, p.db)
	return prefs.Impact{Storage: usage}, err
}

func (p *r2PreferencePolicy) Activate(ctx context.Context, tx *sql.Tx, _, after prefs.Snapshot, patch prefs.Patch, _ prefs.Impact, _ string) error {
	if !touchesBlobExpiry(patch) {
		return nil
	}
	value, err := after.Get("blobs.expiry")
	if err != nil {
		return err
	}
	if expiry, _ := value.Value.Enum(); expiry == "off" {
		// Read through the PATCH transaction, never a nested DB transaction.
		// Over-limit storage remains valid: admission stops new grants without
		// deleting existing objects. Shared preferences records confirmation/audit.
		_, err = p.storage(ctx, tx)
	}
	return err
}
