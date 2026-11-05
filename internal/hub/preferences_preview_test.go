package hub_test

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/auth"
	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/hub"
	"github.com/XertroV/vecnet/internal/prefs"
)

func previewPrefs(t *testing.T, p *hub.Preferences, caller auth.Caller, s prefs.Snapshot, patch prefs.Patch) prefs.Preview {
	t.Helper()
	out, err := p.PreviewPreferences(context.Background(), caller, s.ETag, patch)
	if err != nil {
		t.Fatal(err)
	}
	if out.ConfirmToken == "" {
		t.Fatal("preview must issue a bound confirmation")
	}
	return out
}
func TestPreferencesPreviewTokenBindings(t *testing.T) {
	for _, scenario := range []string{"tampered", "expired", "operator", "patch", "bootstrap", "deployment", "hub", "missing_risk", "extra_risk", "extra_confirmation"} {
		t.Run(scenario, func(t *testing.T) {
			cfg := hub.DefaultConfig()
			h, _, clock := preferencesHubClock(t, cfg)
			s := readPrefs(t, h)
			caller := auth.Caller{Kind: "operator", PrincipalID: "prn_operator", PeerID: "peer_operator", PeerKind: "operator"}
			patch := prefs.Patch{Set: map[string]prefs.Value{"presence.default_ttl": prefs.Duration(1800)}}
			preview := previewPrefs(t, h.Preferences, caller, s, patch)
			patch.Confirm = []string{preview.ConfirmToken}
			target := h.Preferences
			tag := s.ETag
			switch scenario {
			case "tampered":
				patch.Confirm[0] = "x" + preview.ConfirmToken[1:]
			case "expired":
				clock.Advance(5 * time.Minute)
			case "operator":
				caller.PrincipalID = "prn_other"
			case "patch":
				patch.Set["presence.default_ttl"] = prefs.Duration(1700)
			case "bootstrap", "deployment":
				cfg.DataDir = h.Cfg.DataDir
				if scenario == "bootstrap" {
					cfg.ThreadBudget = 700
				} else {
					cfg.PreferencePolicyGeneration = "changed"
				}
				again, err := hub.New(h.DB, cfg, clock.Now)
				if err != nil {
					t.Fatal(err)
				}
				target = again.Preferences
				tag = target.Current().ETag
			case "hub":
				other, _ := preferencesHub(t, cfg)
				target = other.Preferences
				tag = target.Current().ETag
			case "missing_risk":
				patch.Set["presence.session_ttl"] = prefs.Duration(300)
			case "extra_risk":
				original := prefs.Patch{Set: map[string]prefs.Value{"presence.default_ttl": prefs.Duration(1800), "presence.session_ttl": prefs.Duration(300)}}
				extra := previewPrefs(t, h.Preferences, caller, s, original)
				patch.Confirm = []string{extra.ConfirmToken}
			case "extra_confirmation":
				other := previewPrefs(t, h.Preferences, caller, s, prefs.Patch{Set: map[string]prefs.Value{"presence.session_ttl": prefs.Duration(300)}})
				patch.Confirm = append(patch.Confirm, other.ConfirmToken)
			}
			_, err := target.PatchPreferences(context.Background(), caller, tag, patch)
			prefsCode(t, err, prefs.CodeConfirmationStale)
			if target.Current().Revision != 0 {
				t.Fatal("bad confirmation committed")
			}
			prefsAuditCount(t, h, 0)
		})
	}
}
func TestPreferencesPreviewCanonicalPatchAndReplay(t *testing.T) {
	h, _ := preferencesHub(t, hub.DefaultConfig())
	s := readPrefs(t, h)
	s = patchPrefs(t, h, s, prefs.Patch{Set: map[string]prefs.Value{"agents.thread_budget": prefs.Int(600), "rates.sends_per_minute": prefs.Int(70)}})
	p := prefs.Patch{Set: map[string]prefs.Value{"presence.default_ttl": prefs.Duration(1800), "presence.session_ttl": prefs.Duration(300)}, Reset: []string{"agents.thread_budget", "rates.sends_per_minute"}}
	out := previewPrefs(t, h.Preferences, prefsOperator, s, p)
	// Map insertion and reset order have no meaning; confirmations are excluded.
	p.Reset = []string{"rates.sends_per_minute", "agents.thread_budget"}
	p.Set = map[string]prefs.Value{"presence.session_ttl": prefs.Duration(300), "presence.default_ttl": prefs.Duration(1800)}
	p.Confirm = []string{out.ConfirmToken}
	next := patchPrefs(t, h, s, p)
	if next.Revision != 2 {
		t.Fatal("confirmed patch did not commit")
	}
	_, err := h.Preferences.PatchPreferences(context.Background(), prefsOperator, s.ETag, p)
	prefsCode(t, err, prefs.CodeRevisionConflict)
	// A confirmation for a previously committed proposal cannot authorize a no-op.
	_, err = h.Preferences.PatchPreferences(context.Background(), prefsOperator, next.ETag, p)
	prefsCode(t, err, prefs.CodeConfirmationStale)
}
func TestPreferencesPreviewReadOnlyAndNoWriterLock(t *testing.T) {
	h, _ := preferencesHub(t, hub.DefaultConfig())
	s := readPrefs(t, h)
	ctx := context.Background()
	tx, err := h.DB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `UPDATE hub_preferences_state SET revision=revision WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	readCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	p, err := h.Preferences.PreviewPreferences(readCtx, prefsOperator, s.ETag, prefs.Patch{Set: map[string]prefs.Value{"presence.default_ttl": prefs.Duration(1800)}})
	if err != nil || p.ConfirmToken == "" {
		t.Fatal("preview needs no writer lock")
	}
	_ = tx.Rollback()
	if readPrefs(t, h).ETag != s.ETag {
		t.Fatal("preview changed revision")
	}
	prefsAuditCount(t, h, 0)
}

// syntheticPreferencePolicy stands in for a later consumer adapter, never a
// provider or data deletion implementation. Check runs inside PATCH's writer;
// Preview runs after the read transaction; Activate shares the atomic commit.
type syntheticPreferencePolicy struct {
	locked    bool
	quota     int64
	fail      bool
	notBefore string
	impact    prefs.Impact
}

func (a *syntheticPreferencePolicy) Check(_ context.Context, _, _ prefs.Snapshot, p prefs.Patch) error {
	if _, ok := p.Set["blobs.expiry"]; ok && (a.locked || a.quota <= 0) {
		return &prefs.Error{Code: prefs.CodePolicyLocked}
	}
	return nil
}
func (a *syntheticPreferencePolicy) Preview(_ context.Context, _, _ prefs.Snapshot, _ prefs.Patch) (prefs.Impact, error) {
	return a.impact, nil
}
func (a *syntheticPreferencePolicy) Activate(ctx context.Context, tx *sql.Tx, _, after prefs.Snapshot, p prefs.Patch, impact prefs.Impact, digest string) error {
	if p.ApplyExisting && impact.NotBefore != a.notBefore {
		return &prefs.Error{Code: prefs.CodeConfirmationStale}
	}
	for key := range p.Set {
		if !strings.HasPrefix(key, "retention.") {
			continue
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO hub_preference_activations VALUES(?,?,?,?,?,?,?)`, after.Revision, key, "synthetic-effective-from", impact.NotBefore, impact.CutoffAt, impact.HighWater, digest)
		if err != nil {
			return err
		}
	}
	if a.fail {
		return errors.New("request-canary")
	}
	return nil
}
func installedPreviewPrefs(t *testing.T, h *hub.Hub, adapter *syntheticPreferencePolicy, keys ...string) *hub.Preferences {
	t.Helper()
	p, err := hub.NewPreferences(h.DB, h.Cfg, h.Now, keys...)
	if err != nil {
		t.Fatal(err)
	}
	p.Policy = adapter
	return p
}
func TestPreferencesHostedLocksAndVerifiedOff(t *testing.T) {
	h, _ := preferencesHub(t, hub.DefaultConfig())
	a := &syntheticPreferencePolicy{locked: true, quota: 1024}
	p := installedPreviewPrefs(t, h, a, "blobs.expiry")
	s := p.Current()
	patch := prefs.Patch{Set: map[string]prefs.Value{"blobs.expiry": prefs.Enum("off")}}
	_, err := p.PreviewPreferences(context.Background(), prefsOperator, s.ETag, patch)
	prefsCode(t, err, prefs.CodePolicyLocked)
	a.locked = false
	a.quota = 0
	_, err = p.PreviewPreferences(context.Background(), prefsOperator, s.ETag, patch)
	prefsCode(t, err, prefs.CodePolicyLocked)
	a.quota = 1024
	zero := int64(0)
	a.impact = prefs.Impact{Eligible: &zero, Pinned: &zero, EstimatedBytes: &zero}
	out := previewPrefs(t, p, prefsOperator, s, patch)
	if len(out.Risks) != 1 || out.Risks[0].Code != "blob_expiry_off" || out.Impact.EstimatedBytes == nil {
		t.Fatal("missing storage cost risk/estimate")
	}
	patch.Confirm = []string{out.ConfirmToken}
	a.locked = true
	_, err = p.PatchPreferences(context.Background(), prefsOperator, s.ETag, patch)
	prefsCode(t, err, prefs.CodePolicyLocked)
	a.locked = false
	next, err := p.PatchPreferences(context.Background(), prefsOperator, s.ETag, patch)
	if err != nil || next.Revision != 1 {
		t.Fatal("verified off confirmation failed")
	}
	retro := prefs.Patch{Set: patch.Set, ApplyExisting: true}
	_, err = p.PreviewPreferences(context.Background(), prefsOperator, next.ETag, retro)
	prefsCode(t, err, prefs.CodeInvalidValue)
}
func TestPreferencesApplyExistingFixedBoundsAndAtomicActivation(t *testing.T) {
	for _, scenario := range []string{"confirmed", "crossed_schedule", "changed_schedule", "rollback", "impact_tampered"} {
		t.Run(scenario, func(t *testing.T) {
			h, _, clock := preferencesHubClock(t, hub.DefaultConfig())
			high := int64(42)
			zero := int64(0)
			notBefore := envelope.FormatTime(time.Date(2026, 11, 5, 2, 0, 0, 0, time.UTC))
			// Use a confirmation within five minutes of the nightly boundary.
			clock.Advance(57 * time.Minute)
			a := &syntheticPreferencePolicy{notBefore: notBefore, impact: prefs.Impact{CutoffAt: envelope.FormatTime(clock.Now()), HighWater: &high, NotBefore: notBefore, Eligible: &zero, Pinned: &zero, EstimatedBytes: &zero}}
			p := installedPreviewPrefs(t, h, a, "retention.history")
			s := p.Current()
			patch := prefs.Patch{Set: map[string]prefs.Value{"retention.history": prefs.Duration(0)}, ApplyExisting: true}
			out := previewPrefs(t, p, prefsOperator, s, patch)
			if out.Impact.NotBefore != notBefore || *out.Impact.HighWater != 42 {
				t.Fatal("preview lost bounded approval")
			}
			if p.Current().Revision != 0 {
				t.Fatal("preview mutated")
			}
			patch.Confirm = []string{out.ConfirmToken}
			switch scenario {
			case "crossed_schedule":
				clock.Advance(time.Minute)
			case "changed_schedule":
				a.notBefore = envelope.FormatTime(clock.Now().Add(24 * time.Hour))
			case "rollback":
				a.fail = true
			case "impact_tampered":
				parts := strings.Split(out.ConfirmToken, ".")
				if len(parts) != 2 {
					t.Fatal("unexpected signed format")
				}
				raw, _ := base64.RawURLEncoding.DecodeString(parts[0])
				raw = []byte(strings.Replace(string(raw), `"high_water":42`, `"high_water":43`, 1))
				parts[0] = base64.RawURLEncoding.EncodeToString(raw)
				patch.Confirm = []string{strings.Join(parts, ".")}
			}
			next, err := p.PatchPreferences(context.Background(), prefsOperator, s.ETag, patch)
			var count int
			if qerr := h.DB.QueryRow(`SELECT count(*) FROM hub_preference_activations`).Scan(&count); qerr != nil {
				t.Fatal(qerr)
			}
			if scenario == "confirmed" {
				if err != nil || next.Revision != 1 || count != 1 {
					t.Fatal("unchanged policy approval not atomic")
				}
				prefsAuditCount(t, h, 1)
				var nb, cut, digest string
				var hi int64
				if err := h.DB.QueryRow(`SELECT not_before,cutoff_at,high_water,approval_digest FROM hub_preference_activations`).Scan(&nb, &cut, &hi, &digest); err != nil {
					t.Fatal(err)
				}
				if nb != notBefore || cut != out.Impact.CutoffAt || hi != 42 || len(digest) != 64 || strings.Contains(digest, out.ConfirmToken) {
					t.Fatal("persisted approval differs")
				}
			} else {
				want := prefs.CodeConfirmationStale
				if scenario == "rollback" {
					want = prefs.CodeStorageUnavailable
				}
				prefsCode(t, err, want)
				if count != 0 || p.Current().Revision != 0 {
					t.Fatal("failed activation escaped transaction")
				}
				prefsAuditCount(t, h, 0)
			}
		})
	}
}
func TestPreferencesUninstalledConsumersStayUnavailable(t *testing.T) {
	h, _ := preferencesHub(t, hub.DefaultConfig())
	s := readPrefs(t, h)
	for _, patch := range []prefs.Patch{{Set: map[string]prefs.Value{"retention.history": prefs.Duration(0)}, ApplyExisting: true}, {Set: map[string]prefs.Value{"blobs.expiry": prefs.Enum("off")}}} {
		_, err := h.Preferences.PreviewPreferences(context.Background(), prefsOperator, s.ETag, patch)
		prefsCode(t, err, prefs.CodeUnavailable)
		_, err = h.Preferences.PatchPreferences(context.Background(), prefsOperator, s.ETag, patch)
		prefsCode(t, err, prefs.CodeUnavailable)
	}
}
func TestPreferencesConfirmationPayloadNotAudited(t *testing.T) {
	h, _ := preferencesHub(t, hub.DefaultConfig())
	s := readPrefs(t, h)
	p := prefs.Patch{Set: map[string]prefs.Value{"presence.default_ttl": prefs.Duration(1800)}}
	out := previewPrefs(t, h.Preferences, prefsOperator, s, p)
	p.Confirm = []string{out.ConfirmToken}
	patchPrefs(t, h, s, p)
	var detail string
	if err := h.DB.QueryRow(`SELECT detail FROM audit WHERE action='prefs_changed'`).Scan(&detail); err != nil {
		t.Fatal(err)
	}
	var audit struct {
		Approved []string `json:"approved_risks"`
	}
	if json.Unmarshal([]byte(detail), &audit) != nil || len(audit.Approved) != 1 || audit.Approved[0] != "presence_shortened" || strings.Contains(detail, out.ConfirmToken) {
		t.Fatal("unsafe or inaccurate risk audit")
	}
}

func TestPreferencesRetentionAndArchiveRiskSchedule(t *testing.T) {
	for _, key := range []string{"retention.ack_events", "retention.history", "retention.archive_mode"} {
		t.Run(key, func(t *testing.T) {
			h, _ := preferencesHub(t, hub.DefaultConfig())
			a := &syntheticPreferencePolicy{}
			p := installedPreviewPrefs(t, h, a, key)
			s := p.Current()
			value := prefs.Duration(3600)
			if key == "retention.history" {
				value = prefs.Duration(86400)
			}
			if key == "retention.archive_mode" {
				safe := prefs.Patch{Set: map[string]prefs.Value{key: prefs.Bool(true)}}
				var err error
				s, err = p.PatchPreferences(context.Background(), prefsOperator, s.ETag, safe)
				if err != nil {
					t.Fatal(err)
				}
				value = prefs.Bool(false)
			}
			patch := prefs.Patch{Set: map[string]prefs.Value{key: value}}
			_, err := p.PatchPreferences(context.Background(), prefsOperator, s.ETag, patch)
			prefsCode(t, err, prefs.CodeConfirmationRequired)
			out := previewPrefs(t, p, prefsOperator, s, patch)
			want := "retention_shortened"
			if key == "retention.archive_mode" {
				want = "archive_disabled"
			}
			if len(out.Risks) != 1 || out.Risks[0].Code != want || out.Impact.NotBefore != "" {
				t.Fatal("prospective risk/scope differs")
			}
			patch.Confirm = []string{out.ConfirmToken}
			next, err := p.PatchPreferences(context.Background(), prefsOperator, s.ETag, patch)
			if err != nil || next.Revision != s.Revision+1 {
				t.Fatal("prospective confirmed policy failed")
			}
			if key != "retention.archive_mode" {
				protective := prefs.Patch{Set: map[string]prefs.Value{key: prefs.Duration(0)}}
				next, err = p.PatchPreferences(context.Background(), prefsOperator, next.ETag, protective)
				if err != nil {
					t.Fatal("protective expiry disable must not need confirmation")
				}
			}
		})
	}
}
func TestPreferencesPreviewTokenDoesNotGrantServiceAuthority(t *testing.T) {
	h, _ := preferencesHub(t, hub.DefaultConfig())
	s := readPrefs(t, h)
	patch := prefs.Patch{Set: map[string]prefs.Value{"presence.default_ttl": prefs.Duration(1800)}}
	out := previewPrefs(t, h.Preferences, prefsOperator, s, patch)
	patch.Confirm = []string{out.ConfirmToken}
	for _, caller := range []auth.Caller{{Kind: "peer", PeerKind: "operator"}, {Kind: "principal", PeerKind: "dot"}, {Kind: "operator", PeerKind: "session"}} {
		_, err := h.Preferences.PatchPreferences(context.Background(), caller, s.ETag, patch)
		var api *hub.APIError
		if !errors.As(err, &api) || api.Status != 403 || api.Code != "forbidden" {
			t.Fatal("confirmation granted authority")
		}
	}
	prefsAuditCount(t, h, 0)
}
