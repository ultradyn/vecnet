package hub

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/XertroV/vecnet/internal/prefs"
	"github.com/XertroV/vecnet/internal/store"
)

// Install the R2 new-grant consumer and its policy together, before loading
// stored overrides. ValidateBlobConfig accepts only the supported SHA-256
// adapter and a finite positive quota. No provider call is needed for Settings.
// Retained R2 support stays installed when new uploads roll back to disk.
func newProductionPreferences(db *store.DB, cfg Config, now func() time.Time) (*Preferences, error) {
	if cfg.BlobBackend != "r2" && cfg.R2.AccountID == "" {
		return newPreferences(db, cfg, now)
	}
	if err := ValidateBlobConfig(cfg); err != nil {
		return nil, err
	}
	policy := &r2PreferencePolicy{db: db, quota: cfg.R2.StorageQuotaBytes}
	return newPreferencesWithPolicy(db, cfg, now, policy, "blobs.expiry")
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
