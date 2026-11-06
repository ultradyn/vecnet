package hub_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/hub"
	"github.com/XertroV/vecnet/internal/prefs"
)

func TestRetentionArchiveProvenanceResetPreservesHardExpiry(t *testing.T) {
	for _, restart := range []bool{false, true} {
		name := "live"
		if restart {
			name = "restart"
		}
		t.Run(name, func(t *testing.T) {
			cfg := hub.DefaultConfig()
			cfg.UnackedEventRetention = 30 * 24 * time.Hour
			cfg.PreferenceBootstrap = map[string]prefs.Effective{
				"retention.archive_mode": {Value: prefs.Bool(false), Source: prefs.SourceFlag, Origin: "--retention-archive-mode"},
			}
			h, _, clock := preferencesHubClock(t, cfg)
			retentionConfirm(t, h, prefs.Patch{Set: map[string]prefs.Value{"retention.archive_mode": prefs.Bool(false)}, ApplyExisting: true})
			first := retentionEvent(t, h, "peer_synthetic", clock.Now())
			clock.Advance(31 * 24 * time.Hour)
			retentionConfirm(t, h, prefs.Patch{Reset: []string{"retention.archive_mode"}})
			e, err := readPrefs(t, h).Get("retention.archive_mode")
			if err != nil || e.Value != prefs.Bool(false) || e.Source != prefs.SourceFlag || e.Origin != "--retention-archive-mode" {
				t.Fatalf("reset effective archive=%+v err=%v", e, err)
			}
			if restart {
				h, err = hub.New(h.DB, h.Cfg, clock.Now)
				if err != nil {
					t.Fatal(err)
				}
			}
			// The eligible prefix and subsequent rows under the same rule
			// must both expire, without another approval.
			retentionRun(t, h)
			retentionFloor(t, h, "peer_synthetic", first.Seq)
			next := retentionEvent(t, h, "peer_synthetic", clock.Now())
			clock.Advance(31 * 24 * time.Hour)
			if restart {
				h, err = hub.New(h.DB, h.Cfg, clock.Now)
				if err != nil {
					t.Fatal(err)
				}
			}
			retentionRun(t, h)
			retentionFloor(t, h, "peer_synthetic", next.Seq)
		})
	}
}

func TestRetentionArchiveProvenanceResetPreservesApprovalAndPending(t *testing.T) {
	for _, pending := range []bool{false, true} {
		name := "fixed_approval"
		if pending {
			name = "pending_hard_shortening"
		}
		t.Run(name, func(t *testing.T) {
			cfg := hub.DefaultConfig()
			cfg.UnackedEventRetention = 30 * 24 * time.Hour
			cfg.PreferenceBootstrap = map[string]prefs.Effective{
				"retention.archive_mode": {Value: prefs.Bool(false), Source: prefs.SourceFlag, Origin: "--retention-archive-mode"},
			}
			h, _, clock := preferencesHubClock(t, cfg)
			approved := retentionEvent(t, h, "peer_approved", clock.Now().Add(-40*24*time.Hour))
			retentionEvent(t, h, "peer_outside_cutoff", clock.Now().Add(-29*24*time.Hour))
			retentionConfirm(t, h, prefs.Patch{Set: map[string]prefs.Value{"retention.archive_mode": prefs.Bool(false)}, ApplyExisting: true})
			if pending {
				cfg = h.Cfg
				cfg.UnackedEventRetention = 14 * 24 * time.Hour
				var err error
				h, err = hub.New(h.DB, cfg, clock.Now)
				if err != nil {
					t.Fatal(err)
				}
			}
			// Compare all historical eligibility/authority metadata; only the
			// latest projection's provenance tag may change on this Reset.
			queries := []string{
				`SELECT json_remove(value,'$[#-1].policy_tag') FROM meta WHERE key='event_retention_projection_v1_archive'`,
				`SELECT value FROM meta WHERE key='event_retention_bootstrap_v1'`,
				`SELECT json_group_array(json_array(revision,effective_from,not_before,cutoff_at,high_water,approval_digest)) FROM (SELECT * FROM hub_preference_activations WHERE key='retention.archive_mode' ORDER BY revision)`,
			}
			before := make([]string, len(queries))
			for i, query := range queries {
				if err := h.DB.QueryRow(query).Scan(&before[i]); err != nil {
					t.Fatal(err)
				}
			}
			retentionConfirm(t, h, prefs.Patch{Reset: []string{"retention.archive_mode"}})
			for i, query := range queries {
				var after string
				if err := h.DB.QueryRow(query).Scan(&after); err != nil || after != before[i] {
					t.Fatalf("provenance reset changed eligibility metadata %d: err=%v", i, err)
				}
			}
			// The Reset must also publish the new provenance to the ledger.
			var tag string
			if err := h.DB.QueryRow(`SELECT json_extract(value,'$[#-1].policy_tag') FROM meta WHERE key='event_retention_projection_v1_archive'`).Scan(&tag); err != nil {
				t.Fatal(err)
			}
			var provenance struct {
				Source prefs.Source
				Origin string
			}
			if err := json.Unmarshal([]byte(tag), &provenance); err != nil || provenance.Source != prefs.SourceFlag || provenance.Origin != "--retention-archive-mode" {
				t.Fatalf("projection provenance not refreshed: err=%v", err)
			}
			retentionRun(t, h)
			retentionFloor(t, h, "peer_approved", 0) // Still before not_before.
			clock.Advance(2 * 24 * time.Hour)
			restarted, err := hub.New(h.DB, h.Cfg, clock.Now)
			if err != nil {
				t.Fatal(err)
			}
			retentionRun(t, restarted)
			want := approved.Seq
			if pending {
				want = 0
			}
			retentionFloor(t, restarted, "peer_approved", want)
			// This row has aged past 30d, but was outside the fixed preview
			// cutoff and must never drift into that historical authorization.
			retentionFloor(t, restarted, "peer_outside_cutoff", 0)
		})
	}
}
