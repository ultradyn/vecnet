package hub

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/XertroV/vecnet/internal/auth"
	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/prefs"
)

// PreferencePreviewPolicy is the nil-able seam for later verified DB/R2
// consumers. It does not install or enable a consumer. Install it before
// serving, together with the matching installed keys and deployment generation.
// Check validates current backend/quota/policy without estimates or I/O requiring
// another writer. Preview runs outside the preference read transaction and
// returns fixed, explicitly estimated impact. Activate runs in the PATCH writer
// transaction: it rechecks maintenance schedule/bounds, records effective_from,
// cancels incompatible approvals and persists retrospective authorization with
// the signed not_before/cutoff/high_water. No method deletes data or calls a
// provider. Any failure rolls back preferences, activation and audit together.
type PreferencePreviewPolicy interface {
	Check(context.Context, prefs.Snapshot, prefs.Snapshot, prefs.Patch) error
	Preview(context.Context, prefs.Snapshot, prefs.Snapshot, prefs.Patch) (prefs.Impact, error)
	Activate(context.Context, *sql.Tx, prefs.Snapshot, prefs.Snapshot, prefs.Patch, prefs.Impact, string) error
}

const preferencePreviewKeyInfo = "vecnet preferences preview key v1"
const preferencePreviewDomain = "vecnet preferences preview payload v1\x00"
const preferencePreviewTTL = 5 * time.Minute

func (p *Preferences) loadPreviewKey() error {
	raw, err := p.db.Meta(context.Background(), "hub_key")
	if err != nil {
		return preferenceError(prefs.CodeStorageUnavailable)
	}
	key, err := hex.DecodeString(raw)
	if err != nil || len(key) < 32 {
		return preferenceError(prefs.CodeStorageUnavailable)
	}
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(preferencePreviewKeyInfo))
	p.previewKey = mac.Sum(nil)
	return nil
}

// One composite approval binds the entire risk set, rather than permitting
// independent acknowledgements to be combined across proposals.
type preferenceApproval struct {
	Caller      auth.Caller  `json:"caller"`
	PatchDigest string       `json:"patch_digest"`
	ETag        string       `json:"etag"`
	Generation  string       `json:"generation"`
	Risks       []prefs.Risk `json:"risks"`
	Impact      prefs.Impact `json:"impact"`
	IssuedAt    string       `json:"issued_at"`
	ExpiresAt   string       `json:"expires_at"`
}

func canonicalPreferencePatch(patch prefs.Patch) (string, error) {
	patch.Confirm = nil
	patch.Reset = append([]string{}, patch.Reset...)
	sort.Strings(patch.Reset)
	raw, err := json.Marshal(patch)
	if err != nil {
		return "", preferenceError(prefs.CodeInvalidBody)
	}
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:]), nil
}
func (p *Preferences) approvalMAC(raw []byte) []byte {
	mac := hmac.New(sha256.New, p.previewKey)
	_, _ = mac.Write([]byte(preferencePreviewDomain))
	_, _ = mac.Write(raw)
	return mac.Sum(nil)
}
func (p *Preferences) signApproval(a preferenceApproval) (string, error) {
	raw, err := json.Marshal(a)
	if err != nil {
		return "", preferenceError(prefs.CodeStorageUnavailable)
	}
	return base64.RawURLEncoding.EncodeToString(raw) + "." + base64.RawURLEncoding.EncodeToString(p.approvalMAC(raw)), nil
}
func (p *Preferences) openApproval(token string) (preferenceApproval, error) {
	var a preferenceApproval
	invalid := preferenceError(prefs.CodeConfirmationStale)
	if len(token) > prefs.MaxPatchBytes {
		return a, invalid
	}
	left, right, ok := strings.Cut(token, ".")
	if !ok {
		return a, invalid
	}
	raw, err := base64.RawURLEncoding.DecodeString(left)
	if err != nil {
		return a, invalid
	}
	signature, err := base64.RawURLEncoding.DecodeString(right)
	if err != nil || !hmac.Equal(signature, p.approvalMAC(raw)) {
		return a, invalid
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if dec.Decode(&a) != nil {
		return a, invalid
	}
	if _, err := dec.Token(); err != io.EOF {
		return a, invalid
	}
	issued, err := envelope.ParseTime(a.IssuedAt)
	if err != nil {
		return a, invalid
	}
	expiry, err := envelope.ParseTime(a.ExpiresAt)
	if err != nil || !expiry.Equal(issued.Add(preferencePreviewTTL)) || p.now().Before(issued) || !p.now().Before(expiry) {
		return a, invalid
	}
	return a, nil
}
func (p *Preferences) checkPreviewPolicy(ctx context.Context, before, after prefs.Snapshot, patch prefs.Patch) error {
	patch.Confirm = nil // Consumers receive proposals, never confirmation material.
	// An installed key still needs the verified backend policy/activation adapter.
	// Legacy explicit hooks are retained for consumers installed by earlier tasks.
	for _, key := range preferencePatchKeys(patch) {
		if p.installed[key] && p.Policy == nil {
			return preferenceError(prefs.CodeUnavailable)
		}
	}
	if p.Policy != nil {
		return sanitizePreferenceError(p.Policy.Check(ctx, before, after, patch))
	}
	return nil
}
func confirmationRisks(before, after prefs.Snapshot, patch prefs.Patch) []prefs.Risk {
	risks := preferenceRisks(before, after)
	for _, key := range preferencePatchKeys(patch) {
		old, _ := before.Get(key)
		next, _ := after.Get(key)
		code, message := "", ""
		switch key {
		case "retention.archive_mode":
			was, _ := old.Value.Bool()
			now, _ := next.Value.Bool()
			if was && !now {
				code = "archive_disabled"
				message = "Disabling archive protection permits future history expiry; existing data needs separate approval."
			}
		case "retention.ack_events", "retention.history":
			was, _ := old.Value.Int64()
			now, _ := next.Value.Int64()
			if now > 0 && (was == 0 || now < was) {
				code = "retention_shortened"
				message = "New or shorter retention affects future rows; existing data needs separate approval."
			}
		case "blobs.expiry":
			was, _ := old.Value.Enum()
			now, _ := next.Value.Enum()
			if now == "off" && was != now {
				code = "blob_expiry_off"
				message = "New attachments have no disaster expiry; retained and reserved storage incurs cost within the verified finite quota. Estimates are not guarantees."
			}
		}
		if code != "" {
			risks = append(risks, prefs.Risk{Code: code, Keys: []string{key}, Message: message})
		}
	}
	if patch.ApplyExisting {
		risks = append(risks, prefs.Risk{Code: "retention_apply_existing", Keys: preferencePatchKeys(patch), Message: "Apply this bounded policy to existing data at the next nightly sweep. Counts and bytes are estimates; pins are rechecked and deleted data cannot be restored by Undo."})
	}
	sort.Slice(risks, func(i, j int) bool {
		if risks[i].Code != risks[j].Code {
			return risks[i].Code < risks[j].Code
		}
		return strings.Join(risks[i].Keys, "\x00") < strings.Join(risks[j].Keys, "\x00")
	})
	return risks
}
func validPreviewImpact(impact prefs.Impact, existing bool, now time.Time) bool {
	for _, count := range []*int64{impact.HighWater, impact.Eligible, impact.Pinned, impact.EstimatedBytes} {
		if count != nil && *count < 0 {
			return false
		}
	}
	if existing {
		if impact.CutoffAt == "" || impact.HighWater == nil || impact.NotBefore == "" {
			return false
		}
		cutoff, err := envelope.ParseTime(impact.CutoffAt)
		if err != nil || cutoff.After(now) {
			return false
		}
		nb, err := envelope.ParseTime(impact.NotBefore)
		if err != nil || !now.Before(nb) {
			return false
		}
	}
	return true
}
func (p *Preferences) previewConfirmation(ctx context.Context, caller auth.Caller, before, after prefs.Snapshot, patch prefs.Patch) (prefs.Preview, error) {
	if err := p.checkPreviewPolicy(ctx, before, after, patch); err != nil {
		return prefs.Preview{}, err
	}
	impact := prefs.Impact{}
	if p.Policy != nil {
		var err error
		proposal := patch
		proposal.Confirm = nil
		impact, err = p.Policy.Preview(ctx, before, after, proposal)
		if err != nil {
			return prefs.Preview{}, sanitizePreferenceError(err)
		}
	}
	if !validPreviewImpact(impact, patch.ApplyExisting, p.now()) {
		return prefs.Preview{}, preferenceError(prefs.CodeUnavailable)
	}
	digest, err := canonicalPreferencePatch(patch)
	if err != nil {
		return prefs.Preview{}, err
	}
	risks := confirmationRisks(before, after, patch)
	issued := p.now().UTC().Truncate(time.Millisecond)
	expires := issued.Add(preferencePreviewTTL)
	a := preferenceApproval{caller, digest, before.ETag, p.digest, risks, impact, envelope.FormatTime(issued), envelope.FormatTime(expires)}
	token, err := p.signApproval(a)
	if err != nil {
		return prefs.Preview{}, err
	}
	return prefs.Preview{BaseRevision: before.Revision, Risks: risks, Impact: impact, ConfirmToken: token, ExpiresAt: a.ExpiresAt}, nil
}
func (p *Preferences) authorizeConfirmation(ctx context.Context, tx *sql.Tx, caller auth.Caller, before, after prefs.Snapshot, patch prefs.Patch) ([]string, error) {
	if err := p.checkPreviewPolicy(ctx, before, after, patch); err != nil {
		return nil, err
	}
	risks := confirmationRisks(before, after, patch)
	impact := prefs.Impact{}
	approvalDigest := ""
	if len(patch.Confirm) == 0 && len(risks) > 0 {
		return nil, preferenceError(prefs.CodeConfirmationRequired)
	}
	if len(patch.Confirm) > 0 {
		if len(patch.Confirm) != 1 {
			return nil, preferenceError(prefs.CodeConfirmationStale)
		}
		a, err := p.openApproval(patch.Confirm[0])
		if err != nil {
			return nil, err
		}
		digest, err := canonicalPreferencePatch(patch)
		if err != nil {
			return nil, err
		}
		actual, _ := json.Marshal(risks)
		approved, _ := json.Marshal(a.Risks)
		if a.Caller != caller || a.PatchDigest != digest || a.ETag != before.ETag || a.Generation != p.digest || !bytes.Equal(actual, approved) || !validPreviewImpact(a.Impact, patch.ApplyExisting, p.now()) {
			return nil, preferenceError(prefs.CodeConfirmationStale)
		}
		impact = a.Impact
		sum := sha256.Sum256([]byte(patch.Confirm[0]))
		approvalDigest = hex.EncodeToString(sum[:])
	}
	if p.Policy != nil {
		proposal := patch
		proposal.Confirm = nil
		if err := p.Policy.Activate(ctx, tx, before, after, proposal, impact, approvalDigest); err != nil {
			return nil, sanitizePreferenceError(err)
		}
	}
	codes := []string{}
	for _, risk := range risks {
		if len(codes) == 0 || codes[len(codes)-1] != risk.Code {
			codes = append(codes, risk.Code)
		}
	}
	return codes, nil
}
