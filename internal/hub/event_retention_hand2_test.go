package hub_test

import (
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/hub"
	"github.com/XertroV/vecnet/internal/prefs"
)

func hardGrantBounds(t *testing.T, h *hub.Hub) string {
	t.Helper()
	var bounds string
	if err := h.DB.QueryRow(`SELECT json_array(not_before,cutoff_at,high_water) FROM hub_preference_activations WHERE key='retention.archive_mode' ORDER BY revision DESC LIMIT 1`).Scan(&bounds); err != nil {
		t.Fatal(err)
	}
	return bounds
}

func TestRetentionHardGrantSurvivesAckBootstrapIncrease(t *testing.T) {
	for _, tc := range []struct {
		name   string
		days   int
		legacy bool
	}{{"14d", 14, false}, {"disabled", 0, false}, {"legacy_checkpoint", 14, true}} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := hub.DefaultConfig()
			cfg.UnackedEventRetention = 30 * 24 * time.Hour
			h, _, clock := preferencesHubClock(t, cfg)
			old := retentionEvent(t, h, "peer_hard", clock.Now().Add(-40*24*time.Hour))
			retentionEvent(t, h, "peer_outside_cutoff", clock.Now().Add(-29*24*time.Hour))
			// This older cohort requires the fixed retrospective grant.
			retentionConfirm(t, h, prefs.Patch{Set: map[string]prefs.Value{"retention.archive_mode": prefs.Bool(false)}, ApplyExisting: true})
			bounds := hardGrantBounds(t, h)
			if tc.legacy {
				// Model the previous binary: no dedicated hard generation, and
				// archive boundaries stamped with the shared generation.
				if _, err := h.DB.Exec(`UPDATE meta SET value=json_set(value,'$[0].bootstrap_generation',(SELECT json_extract(value,'$.generation') FROM meta WHERE key='event_retention_bootstrap_v1')) WHERE key='event_retention_projection_v2_archive'`); err != nil {
					t.Fatal(err)
				}
				if _, err := h.DB.Exec(`UPDATE meta SET value=json_remove(value,'$.hard_generation') WHERE key='event_retention_bootstrap_v1'`); err != nil {
					t.Fatal(err)
				}
			}
			before := readPrefs(t, h)
			// Outside the captured high-water, but covered by the prospective rule.
			later := retentionEvent(t, h, "peer_hard", clock.Now().Add(-35*24*time.Hour))
			cfg = h.Cfg
			cfg.AckedEventRetention = time.Duration(tc.days) * 24 * time.Hour
			var err error
			h, err = hub.New(h.DB, cfg, clock.Now)
			if err != nil {
				t.Fatal(err)
			}
			if readPrefs(t, h).ETag == before.ETag {
				t.Fatal("bootstrap change did not invalidate shared ETag")
			}
			if got := hardGrantBounds(t, h); got != bounds {
				t.Fatalf("fixed grant changed: %s -> %s", bounds, got)
			}
			retentionRun(t, h)
			retentionFloor(t, h, "peer_hard", 0) // not_before still binds
			clock.Advance(2 * 24 * time.Hour)
			for i := 0; i < 2; i++ {
				h, err = hub.New(h.DB, cfg, clock.Now)
				if err != nil {
					t.Fatal(err)
				}
				if got := hardGrantBounds(t, h); got != bounds {
					t.Fatalf("restart changed fixed grant: %s -> %s", bounds, got)
				}
				retentionRun(t, h)
				retentionFloor(t, h, "peer_hard", later.Seq)
				retentionFloor(t, h, "peer_outside_cutoff", 0)
			}
			if old.Seq >= later.Seq {
				t.Fatal("invalid owner prefix fixture")
			}
		})
	}
}

func TestRetentionHardGrantSurvivesPendingAckBootstrap(t *testing.T) {
	cfg := hub.DefaultConfig()
	cfg.UnackedEventRetention = 30 * 24 * time.Hour
	h, _, clock := preferencesHubClock(t, cfg)
	old := retentionEvent(t, h, "peer_hard", clock.Now().Add(-40*24*time.Hour))
	ack := retentionEvent(t, h, "peer_ack", clock.Now().Add(-40*24*time.Hour))
	retentionAck(t, h, "peer_ack", "*", ack)
	retentionConfirm(t, h, prefs.Patch{Set: map[string]prefs.Value{"retention.archive_mode": prefs.Bool(false)}, ApplyExisting: true})
	bounds := hardGrantBounds(t, h)
	cfg = h.Cfg
	cfg.AckedEventRetention = 24 * time.Hour
	var err error
	h, err = hub.New(h.DB, cfg, clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	later := retentionEvent(t, h, "peer_hard", clock.Now().Add(-35*24*time.Hour))
	retentionRun(t, h)
	retentionFloor(t, h, "peer_hard", 0)
	clock.Advance(24 * time.Hour)
	for i := 0; i < 2; i++ {
		h, err = hub.New(h.DB, cfg, clock.Now)
		if err != nil {
			t.Fatal(err)
		}
		if got := hardGrantBounds(t, h); got != bounds {
			t.Fatalf("ack shortening changed hard grant: %s -> %s", bounds, got)
		}
		retentionRun(t, h)
		retentionFloor(t, h, "peer_hard", later.Seq)
		retentionFloor(t, h, "peer_ack", 0) // pending ack confirmation remains required
	}
	if old.Seq >= later.Seq {
		t.Fatal("invalid owner prefix fixture")
	}
}
