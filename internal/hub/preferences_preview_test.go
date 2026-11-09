package hub_test

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"reflect"
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
	activationCalls int
	locked          bool
	quota           int64
	fail            bool
	notBefore       string
	impact          prefs.Impact
	activated       prefs.Impact
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
	a.activationCalls++
	a.activated = impact
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

// A rejected approval must have no persistent or published side effects,
// including adapter calls that would not insert a retention activation row.
func assertUnconfirmedPreferencePatch(t *testing.T, h *hub.Hub, p *hub.Preferences, a *syntheticPreferencePolicy, s prefs.Snapshot, patch prefs.Patch) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	updates := p.Subscribe(ctx)
	counts := func() [3]int {
		t.Helper()
		var out [3]int
		for i, query := range []string{`SELECT count(*) FROM hub_preferences`, `SELECT count(*) FROM hub_preference_activations`, `SELECT count(*) FROM audit`} {
			if err := h.DB.QueryRow(query).Scan(&out[i]); err != nil {
				t.Fatal(err)
			}
		}
		return out
	}
	beforeCounts, beforeCalls := counts(), a.activationCalls
	_, err := p.PatchPreferences(ctx, prefsOperator, s.ETag, patch)
	prefsCode(t, err, prefs.CodeConfirmationRequired)
	persisted, err := p.LoadSnapshot(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, snapshot := range []prefs.Snapshot{persisted, p.Current()} {
		if snapshot.Revision != s.Revision || snapshot.ETag != s.ETag || !reflect.DeepEqual(snapshot.Descriptors(), s.Descriptors()) {
			t.Fatal("unconfirmed patch changed persisted or published preferences")
		}
	}
	if counts() != beforeCounts || a.activationCalls != beforeCalls {
		t.Fatal("unconfirmed patch changed preferences, activations or audit")
	}
	select {
	case <-updates:
		t.Fatal("unconfirmed patch notified subscriber")
	default:
	}
}

func assertApprovedPreferenceRisk(t *testing.T, h *hub.Hub, want string) {
	t.Helper()
	var detail string
	if err := h.DB.QueryRow(`SELECT detail FROM audit WHERE action='prefs_changed'`).Scan(&detail); err != nil {
		t.Fatal(err)
	}
	var row struct {
		Approved []string `json:"approved_risks"`
	}
	if json.Unmarshal([]byte(detail), &row) != nil || !reflect.DeepEqual(row.Approved, []string{want}) {
		t.Fatal("confirmed change did not audit the exact approved risk")
	}
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
	// The cost confirmation needs the verified retained/reserved/used/limit
	// figures: absent, inconsistent or unlimited figures cannot be approved.
	for _, storage := range []*prefs.StorageUsage{
		nil,
		{RetainedBytes: 600, ReservedBytes: 100, UsedBytes: 700, LimitBytes: 0},
		{RetainedBytes: 600, ReservedBytes: 100, UsedBytes: 700, LimitBytes: -1},
		{RetainedBytes: 600, ReservedBytes: 100, UsedBytes: 650, LimitBytes: 1024},
		{RetainedBytes: -1, ReservedBytes: 100, UsedBytes: 99, LimitBytes: 1024},
		{RetainedBytes: 1 << 62, ReservedBytes: 1 << 62, UsedBytes: -(1 << 63), LimitBytes: 1024},
	} {
		a.impact = prefs.Impact{Eligible: &zero, Pinned: &zero, EstimatedBytes: &zero, Storage: storage}
		_, err = p.PreviewPreferences(context.Background(), prefsOperator, s.ETag, patch)
		prefsCode(t, err, prefs.CodeUnavailable)
	}
	figures := prefs.StorageUsage{RetainedBytes: 600, ReservedBytes: 100, UsedBytes: 700, LimitBytes: 1024}
	a.impact = prefs.Impact{Eligible: &zero, Pinned: &zero, EstimatedBytes: &zero, Storage: &figures}
	assertUnconfirmedPreferencePatch(t, h, p, a, s, patch)
	out := previewPrefs(t, p, prefsOperator, s, patch)
	if len(out.Risks) != 1 || out.Risks[0].Code != "blob_expiry_off" || out.Impact.EstimatedBytes == nil {
		t.Fatal("missing storage cost risk/estimate")
	}
	if out.Impact.Storage == nil || *out.Impact.Storage != figures {
		t.Fatalf("preview omitted the storage figures: %+v", out.Impact.Storage)
	}
	// Over-limit usage is shown, not refused: lowering the quota stops new
	// grants without deleting retained objects.
	over := prefs.StorageUsage{RetainedBytes: 2000, ReservedBytes: 0, UsedBytes: 2000, LimitBytes: 1024}
	a.impact.Storage = &over
	if o := previewPrefs(t, p, prefsOperator, s, patch); *o.Impact.Storage != over {
		t.Fatal("over-limit figures lost")
	}
	// The signed approval binds the figures the operator reviewed; a later
	// estimate from the policy cannot replace them, and a forged token fails.
	parts := strings.Split(out.ConfirmToken, ".")
	raw, _ := base64.RawURLEncoding.DecodeString(parts[0])
	forged := strings.Replace(string(raw), `"used_bytes":700`, `"used_bytes":701`, 1)
	if forged == string(raw) {
		t.Fatal("approval does not carry the storage figures")
	}
	patch.Confirm = []string{base64.RawURLEncoding.EncodeToString([]byte(forged)) + "." + parts[1]}
	_, err = p.PatchPreferences(context.Background(), prefsOperator, s.ETag, patch)
	prefsCode(t, err, prefs.CodeConfirmationStale)
	patch.Confirm = []string{out.ConfirmToken}
	a.locked = true
	_, err = p.PatchPreferences(context.Background(), prefsOperator, s.ETag, patch)
	prefsCode(t, err, prefs.CodePolicyLocked)
	a.locked = false
	next, err := p.PatchPreferences(context.Background(), prefsOperator, s.ETag, patch)
	if err != nil || next.Revision != 1 {
		t.Fatal("verified off confirmation failed")
	}
	if a.activated.Storage == nil || *a.activated.Storage != figures {
		t.Fatalf("activation did not receive the approved figures: %+v", a.activated.Storage)
	}
	prefsAuditCount(t, h, 1)
	assertApprovedPreferenceRisk(t, h, "blob_expiry_off")
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
			prefValue(t, s, "retention.history", 0, prefs.SourceDefault, "")
			patch := prefs.Patch{Set: map[string]prefs.Value{"retention.history": prefs.Duration(0)}, ApplyExisting: true}
			assertUnconfirmedPreferencePatch(t, h, p, a, s, patch)
			out := previewPrefs(t, p, prefsOperator, s, patch)
			if len(out.Risks) != 1 || out.Risks[0].Code != "retention_apply_existing" || !reflect.DeepEqual(out.Risks[0].Keys, []string{"retention.history"}) {
				t.Fatal("unchanged policy preview omitted the retrospective risk")
			}
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
				assertApprovedPreferenceRisk(t, h, "retention_apply_existing")
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
	p, err := hub.NewPreferences(h.DB, h.Cfg, h.Now)
	if err != nil {
		t.Fatal(err)
	}
	s, err := p.ReadPreferences(context.Background(), prefsOperator)
	if err != nil {
		t.Fatal(err)
	}
	for _, patch := range []prefs.Patch{{Set: map[string]prefs.Value{"retention.history": prefs.Duration(0)}, ApplyExisting: true}, {Set: map[string]prefs.Value{"blobs.expiry": prefs.Enum("off")}}} {
		_, err := p.PreviewPreferences(context.Background(), prefsOperator, s.ETag, patch)
		prefsCode(t, err, prefs.CodeUnavailable)
		_, err = p.PatchPreferences(context.Background(), prefsOperator, s.ETag, patch)
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

func TestPreferencesConfirmedOrdinaryNoopDoesNotActivate(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		t.Run(map[bool]string{false: "policy", true: "legacy_hook"}[legacy], func(t *testing.T) {
			h, _ := preferencesHub(t, hub.DefaultConfig())
			a := &syntheticPreferencePolicy{}
			p := installedPreviewPrefs(t, h, a, "retention.history")
			s := p.Current()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			updates := p.Subscribe(ctx)
			patch := prefs.Patch{Set: map[string]prefs.Value{"retention.history": prefs.Duration(0)}}
			preview := previewPrefs(t, p, prefsOperator, s, patch)
			patch.Confirm = []string{preview.ConfirmToken}
			hookCalled := false
			if legacy {
				p.AuthorizePatch = func(context.Context, *sql.Tx, auth.Caller, prefs.Snapshot, prefs.Snapshot, prefs.Patch) ([]string, error) {
					hookCalled = true
					return nil, nil
				}
			}
			for range 2 {
				next, err := p.PatchPreferences(ctx, prefsOperator, s.ETag, patch)
				if err != nil {
					t.Fatal("confirmed no-op failed")
				}
				var activations int
				if err := h.DB.QueryRow(`SELECT count(*) FROM hub_preference_activations`).Scan(&activations); err != nil {
					t.Fatal(err)
				}
				if activations != 0 || hookCalled {
					t.Fatal("confirmed ordinary no-op invoked persistent activation")
				}
				persisted, err := p.LoadSnapshot(ctx, nil)
				if err != nil || next.Revision != s.Revision || next.ETag != s.ETag || persisted.ETag != s.ETag || p.Current().ETag != s.ETag {
					t.Fatal("confirmed no-op changed snapshot")
				}
				prefsAuditCount(t, h, 0)
				select {
				case <-updates:
					t.Fatal("confirmed no-op notified subscriber")
				default:
				}
			}
			patch.Confirm = []string{"invalid-confirmation"}
			_, err := p.PatchPreferences(ctx, prefsOperator, s.ETag, patch)
			prefsCode(t, err, prefs.CodeConfirmationStale)
			if hookCalled {
				t.Fatal("invalid no-op confirmation invoked authorization hook")
			}
		})
	}
}

func TestGrokTTLProvenanceResetRequiresConfirmation(t *testing.T) {
	h, _, clock := preferencesHubClock(t, grokTTLExplicitDefault(hub.DefaultConfig(), 7200))
	runtimePatch(t, h, map[string]prefs.Value{"presence.default_ttl": prefs.Duration(3600)})
	h = grokTTLReopen(t, h, hub.DefaultConfig())
	before := readPrefs(t, h)
	prefValue(t, before, "presence.default_ttl", 3600, prefs.SourceStored, "")
	state, at := "working", envelope.FormatTime(clock.Now())
	clock.Advance(20 * time.Minute)
	row := hub.PresenceRow{Kind: "grokbot", State: &state, At: &at}
	if got := h.EffectivePresence(row, clock.Now()); got != "working" {
		t.Fatalf("stored D3600 control = %q, want working", got)
	}
	ctx, cancel := context.WithCancel(context.Background())
	updates := h.Preferences.Subscribe(ctx)
	t.Cleanup(func() {
		cancel()
		runtimeEventually(t, func() bool { return h.Preferences.SubscriberCount() == 0 })
	})
	unchanged := func() {
		t.Helper()
		for _, s := range []prefs.Snapshot{readPrefs(t, h), h.Preferences.Current()} {
			if s.ETag != before.ETag || s.Revision != before.Revision || !reflect.DeepEqual(s.Descriptors(), before.Descriptors()) {
				t.Error("preview or rejected reset changed persistent/published preferences")
			}
		}
		prefsAuditCount(t, h, 1)
		select {
		case <-updates:
			t.Error("preview or rejected reset published a notification")
		default:
		}
	}
	p := prefs.Patch{Reset: []string{"presence.default_ttl"}}
	preview := previewPrefs(t, h.Preferences, prefsOperator, before, p)
	if len(preview.Risks) != 1 || preview.Risks[0].Code != "presence_shortened" ||
		!reflect.DeepEqual(preview.Risks[0].Keys, []string{"presence.default_ttl"}) ||
		!strings.Contains(strings.ToLower(preview.Risks[0].Message), "grok") {
		t.Errorf("raw3600 -> raw3600 reset must identify Grok 3600 -> 600 shortening: %+v", preview.Risks)
	}
	unchanged()
	_, err := h.Preferences.PatchPreferences(context.Background(), prefsOperator, before.ETag, p)
	if err == nil {
		t.Error("provenance-only shortening RESET committed without confirmation")
		unchanged()
		return
	}
	prefsCode(t, err, prefs.CodeConfirmationRequired)
	unchanged()
	p.Confirm = []string{preview.ConfirmToken}
	after := patchPrefs(t, h, before, p)
	prefValue(t, after, "presence.default_ttl", 3600, prefs.SourceDefault, "")
	if after.Revision != before.Revision+1 || after.ETag == before.ETag {
		t.Error("confirmed provenance-only reset must revise its ETag")
	}
	if got := h.EffectivePresence(row, clock.Now()); got != "unknown" {
		t.Errorf("confirmed reset must change Grok from inherited3600 to factory600: %q", got)
	}
	prefsAuditCount(t, h, 2)
	select {
	case revision := <-updates:
		if revision != after.Revision {
			t.Errorf("notification revision = %d, want %d", revision, after.Revision)
		}
	default:
		t.Error("confirmed reset did not notify")
	}
	_, err = h.Preferences.PatchPreferences(context.Background(), prefsOperator, before.ETag, p)
	prefsCode(t, err, prefs.CodeRevisionConflict)
}

func TestGrokTTLExplicitGFactorySetRequiresConfirmation(t *testing.T) {
	for _, scenario := range []string{"confirmed", "tampered", "expired", "patch", "revision", "commit-failure"} {
		t.Run(scenario, func(t *testing.T) {
			h, _, clock := preferencesHubClock(t, grokTTLExplicitDefault(hub.DefaultConfig(), 7200))
			s := readPrefs(t, h)
			prefValue(t, s, "presence.grokbot_ttl", 600, prefs.SourceDefault, "")
			prefValue(t, s, "presence.default_ttl", 7200, prefs.SourceFlag, "--presence-ttl")
			old, oldRows := h.Preferences.Current(), s.Descriptors()
			state, at := "working", envelope.FormatTime(clock.Now())
			row := hub.PresenceRow{Kind: "grokbot", State: &state, At: &at}
			clock.Advance(601 * time.Second)
			if got := h.EffectivePresence(row, clock.Now()); got != "working" {
				t.Fatalf("unset G must inherit explicit D7200: %q", got)
			}
			ctx, cancel := context.WithCancel(context.Background())
			wake := h.Preferences.Subscribe(ctx)
			t.Cleanup(func() {
				cancel()
				runtimeEventually(t, func() bool { return h.Preferences.SubscriberCount() == 0 })
			})
			audits := 0
			unchanged := func() {
				t.Helper()
				for _, got := range []prefs.Snapshot{readPrefs(t, h), h.Preferences.Current()} {
					if got.Revision != s.Revision || got.ETag != s.ETag || !reflect.DeepEqual(got.Descriptors(), s.Descriptors()) {
						t.Fatal("preview/rejection/rollback changed durable or published preferences")
					}
				}
				if !reflect.DeepEqual(old.Descriptors(), oldRows) {
					t.Fatal("old publication was mutated")
				}
				prefsAuditCount(t, h, audits)
				select {
				case <-wake:
					t.Fatal("preview/rejection/rollback published a wake")
				default:
				}
			}
			p := prefs.Patch{Set: map[string]prefs.Value{"presence.grokbot_ttl": prefs.Duration(600)}}
			preview := previewPrefs(t, h.Preferences, prefsOperator, s, p)
			if len(preview.Risks) != 1 || preview.Risks[0].Code != "presence_shortened" ||
				!reflect.DeepEqual(preview.Risks[0].Keys, []string{"presence.grokbot_ttl"}) ||
				!strings.Contains(strings.ToLower(preview.Risks[0].Message), "grok") {
				t.Fatalf("explicit G600 replacing inherited7200 must identify Grok shortening: %+v", preview.Risks)
			}
			unchanged()
			_, err := h.Preferences.PatchPreferences(ctx, prefsOperator, s.ETag, p)
			prefsCode(t, err, prefs.CodeConfirmationRequired)
			unchanged()
			p.Confirm = []string{preview.ConfirmToken}
			switch scenario {
			case "tampered":
				p.Confirm[0] = "x" + preview.ConfirmToken[1:]
			case "expired":
				clock.Advance(5 * time.Minute)
			case "patch":
				p.Set["presence.grokbot_ttl"] = prefs.Duration(601)
			case "revision":
				s = patchPrefs(t, h, s, prefs.Patch{Set: map[string]prefs.Value{"agents.thread_budget": prefs.Int(600)}})
				audits++
				select {
				case revision := <-wake:
					if revision != s.Revision {
						t.Fatal("unrelated commit notified the wrong revision")
					}
				default:
					t.Fatal("unrelated commit failed to notify")
				}
			case "commit-failure":
				// Fault injection only: no preference rows or auth/policy are patched.
				// The trigger INSERT succeeds; its deferred FK fails at COMMIT.
				for _, statement := range []string{
					"CREATE TABLE grok_ttl_parent (id INTEGER PRIMARY KEY)",
					"CREATE TABLE grok_ttl_child (parent_id INTEGER REFERENCES grok_ttl_parent(id) DEFERRABLE INITIALLY DEFERRED)",
					"CREATE TRIGGER grok_ttl_commit AFTER INSERT ON hub_preferences WHEN NEW.key='presence.grokbot_ttl' BEGIN INSERT INTO grok_ttl_child VALUES(1); END",
				} {
					if _, err := h.DB.Exec(statement); err != nil {
						t.Fatal(err)
					}
				}
			}
			if scenario != "confirmed" {
				_, err = h.Preferences.PatchPreferences(ctx, prefsOperator, s.ETag, p)
				want := prefs.CodeConfirmationStale
				if scenario == "commit-failure" {
					want = prefs.CodeStorageUnavailable
				}
				prefsCode(t, err, want)
				unchanged()
				if scenario == "commit-failure" {
					var children int
					if err := h.DB.QueryRow("SELECT count(*) FROM grok_ttl_child").Scan(&children); err != nil || children != 0 {
						t.Fatal("failed commit left fault-injection rows", err)
					}
					if _, err := h.DB.Exec("DROP TRIGGER grok_ttl_commit"); err != nil {
						t.Fatal(err)
					}
				}
			}
			p = prefs.Patch{Set: map[string]prefs.Value{"presence.grokbot_ttl": prefs.Duration(600)}}
			fresh := previewPrefs(t, h.Preferences, prefsOperator, s, p)
			unchanged()
			p.Confirm = []string{fresh.ConfirmToken}
			after := patchPrefs(t, h, s, p)
			prefValue(t, after, "presence.grokbot_ttl", 600, prefs.SourceStored, "")
			if after.Revision != s.Revision+1 || after.ETag == s.ETag || h.Preferences.Current().ETag != after.ETag || readPrefs(t, h).ETag != after.ETag {
				t.Fatal("fresh confirmed factory-equal intent did not commit/publish")
			}
			if h.EffectivePresence(row, clock.Now()) != "unknown" || !reflect.DeepEqual(old.Descriptors(), oldRows) {
				t.Fatal("explicit G600 did not shorten Grok or mutated the old publication")
			}
			prefsAuditCount(t, h, audits+1)
			select {
			case revision := <-wake:
				if revision != after.Revision {
					t.Fatal("confirmed intent notified the wrong revision")
				}
			default:
				t.Fatal("confirmed intent did not publish a wake")
			}
		})
	}
}
