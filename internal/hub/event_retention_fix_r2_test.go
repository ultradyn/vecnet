package hub_test

import (
	"context"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/hub"
	"github.com/XertroV/vecnet/internal/prefs"
)

func TestRetentionAckBootstrapProvenanceRestart(t *testing.T) {
	cfg := hub.DefaultConfig()
	cfg.PreferenceBootstrap = map[string]prefs.Effective{
		"retention.ack_events": {Value: prefs.Duration(14 * 24 * 3600), Source: prefs.SourceFlag, Origin: "VECNET_RETENTION_ACK_EVENTS"},
	}
	h, _, clock := preferencesHubClock(t, cfg)
	old := retentionEvent(t, h, "peer_before_restart", clock.Now())
	retentionAck(t, h, "peer_before_restart", "*", old)
	retentionConfirm(t, h, prefs.Patch{Set: map[string]prefs.Value{"retention.ack_events": prefs.Duration(14 * 24 * 3600)}})
	retentionConfirm(t, h, prefs.Patch{Reset: []string{"retention.ack_events"}})
	clock.Advance(15 * 24 * time.Hour)
	cfg = h.Cfg
	cfg.PreferenceBootstrap = map[string]prefs.Effective{
		"retention.ack_events": {Value: prefs.Duration(14 * 24 * 3600), Source: prefs.SourceFlag, Origin: "--retention-ack-events"},
	}
	restarted, err := hub.New(h.DB, cfg, clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	e, err := readPrefs(t, restarted).Get("retention.ack_events")
	if err != nil || e.Value != prefs.Duration(14*24*3600) || e.Source != prefs.SourceFlag || e.Origin != "--retention-ack-events" {
		t.Fatalf("restart effective policy=%+v err=%v", e, err)
	}
	// An unrelated revision must not be needed to repair retention, or break it.
	for _, edit := range []bool{false, true} {
		if edit {
			retentionConfirm(t, restarted, prefs.Patch{Set: map[string]prefs.Value{"agents.thread_budget": prefs.Int(600)}})
		}
		retentionRun(t, restarted)
		retentionFloor(t, restarted, "peer_before_restart", old.Seq)
	}
	future := retentionEvent(t, restarted, "peer_after_restart", clock.Now())
	retentionAck(t, restarted, "peer_after_restart", "*", future)
	clock.Advance(15 * 24 * time.Hour)
	// Reconciliation must persist across a second restart as well.
	restarted, err = hub.New(h.DB, cfg, clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	retentionRun(t, restarted)
	retentionFloor(t, restarted, "peer_after_restart", future.Seq)
}

func TestRetentionAckProvenanceRestartKeepsRetrospectiveBounds(t *testing.T) {
	cfg := hub.DefaultConfig()
	cfg.PreferenceBootstrap = map[string]prefs.Effective{
		"retention.ack_events": {Value: prefs.Duration(14 * 24 * 3600), Source: prefs.SourceFlag, Origin: "VECNET_RETENTION_ACK_EVENTS"},
	}
	h, _, clock := preferencesHubClock(t, cfg)
	// Rows born under disabled retention need retrospective authority. Only
	// the first is old enough at preview; the second cannot age into it later.
	retentionConfirm(t, h, prefs.Patch{Set: map[string]prefs.Value{"retention.ack_events": prefs.Duration(0)}})
	old := retentionEvent(t, h, "peer_approved", clock.Now().Add(-15*24*time.Hour))
	young := retentionEvent(t, h, "peer_outside_preview", clock.Now().Add(-13*24*time.Hour))
	retentionAck(t, h, "peer_approved", "*", old)
	retentionAck(t, h, "peer_outside_preview", "*", young)
	retentionConfirm(t, h, prefs.Patch{Reset: []string{"retention.ack_events"}, ApplyExisting: true})
	var activationBefore string
	if err := h.DB.QueryRow(`SELECT json_array(not_before,cutoff_at,high_water) FROM hub_preference_activations WHERE key='retention.ack_events' ORDER BY revision DESC LIMIT 1`).Scan(&activationBefore); err != nil {
		t.Fatal(err)
	}
	cfg = h.Cfg
	cfg.PreferenceBootstrap = map[string]prefs.Effective{
		"retention.ack_events": {Value: prefs.Duration(14 * 24 * 3600), Source: prefs.SourceFlag, Origin: "--retention-ack-events"},
	}
	restarted, err := hub.New(h.DB, cfg, clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	var activationAfter string
	if err := h.DB.QueryRow(`SELECT json_array(not_before,cutoff_at,high_water) FROM hub_preference_activations WHERE key='retention.ack_events' ORDER BY revision DESC LIMIT 1`).Scan(&activationAfter); err != nil || activationAfter != activationBefore {
		t.Fatalf("provenance changed fixed approval: err=%v", err)
	}
	retentionRun(t, restarted)
	retentionFloor(t, restarted, "peer_approved", 0)
	clock.Advance(2 * 24 * time.Hour)
	retentionRun(t, restarted)
	retentionFloor(t, restarted, "peer_approved", old.Seq)
	retentionFloor(t, restarted, "peer_outside_preview", 0)
}

func TestRetentionAckProvenanceRestartKeepsPendingShortening(t *testing.T) {
	cfg := hub.DefaultConfig()
	cfg.PreferenceBootstrap = map[string]prefs.Effective{
		"retention.ack_events": {Value: prefs.Duration(30 * 24 * 3600), Source: prefs.SourceFlag, Origin: "VECNET_RETENTION_ACK_EVENTS"},
	}
	h, _, clock := preferencesHubClock(t, cfg)
	retentionConfirm(t, h, prefs.Patch{Set: map[string]prefs.Value{"retention.ack_events": prefs.Duration(30 * 24 * 3600)}})
	retentionConfirm(t, h, prefs.Patch{Reset: []string{"retention.ack_events"}})
	cfg = h.Cfg
	cfg.PreferenceBootstrap = map[string]prefs.Effective{
		"retention.ack_events": {Value: prefs.Duration(14 * 24 * 3600), Source: prefs.SourceFlag, Origin: "VECNET_RETENTION_ACK_EVENTS"},
	}
	restarted, err := hub.New(h.DB, cfg, clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	var checkpointBefore string
	if err := h.DB.QueryRow(`SELECT value FROM meta WHERE key='event_retention_bootstrap_v1'`).Scan(&checkpointBefore); err != nil {
		t.Fatal(err)
	}
	cfg.PreferenceBootstrap = map[string]prefs.Effective{
		"retention.ack_events": {Value: prefs.Duration(14 * 24 * 3600), Source: prefs.SourceFlag, Origin: "--retention-ack-events"},
	}
	restarted, err = hub.New(h.DB, cfg, clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	var checkpointAfter string
	if err := h.DB.QueryRow(`SELECT value FROM meta WHERE key='event_retention_bootstrap_v1'`).Scan(&checkpointAfter); err != nil || checkpointBefore != checkpointAfter {
		t.Fatalf("provenance changed pending checkpoint: err=%v", err)
	}
	c := retentionEvent(t, restarted, "peer_pending_provenance", clock.Now().Add(-40*24*time.Hour))
	retentionAck(t, restarted, "peer_pending_provenance", "*", c)
	status, err := restarted.PruneEventSlice(context.Background())
	if err != nil || status.Reason != "activation_required" || status.Deleted != 0 {
		t.Fatalf("pending shortening lost: reason=%s deleted=%d err=%v", status.Reason, status.Deleted, err)
	}
}

func TestRetentionBootstrapArchiveConfirmedProspectiveExit(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		name := "reconciled_bootstrap"
		if legacy {
			name = "legacy_projection"
		}
		t.Run(name, func(t *testing.T) {
			h, _, clock := preferencesHubClock(t, hub.DefaultConfig())
			retentionConfirm(t, h, prefs.Patch{Set: map[string]prefs.Value{"retention.archive_mode": prefs.Bool(true)}})
			retentionConfirm(t, h, prefs.Patch{Reset: []string{"retention.archive_mode"}})
			var priorProjection string
			if err := h.DB.QueryRow(`SELECT value FROM meta WHERE key='event_retention_projection_v1_archive'`).Scan(&priorProjection); err != nil {
				t.Fatal(err)
			}
			cfg := h.Cfg
			cfg.UnackedEventRetention = 30 * 24 * time.Hour
			cfg.PreferenceBootstrap = map[string]prefs.Effective{
				"retention.archive_mode": {Value: prefs.Bool(true), Source: prefs.SourceFlag, Origin: "VECNET_RETENTION_ARCHIVE_MODE"},
			}
			archived, err := hub.New(h.DB, cfg, clock.Now)
			if err != nil {
				t.Fatal(err)
			}
			if legacy {
				// Previous binaries saved the bootstrap checkpoint without a
				// protective archive boundary. A real confirmed exit must work
				// even when that historical projection differs from the snapshot.
				if _, err := h.DB.Exec(`UPDATE meta SET value=? WHERE key='event_retention_projection_v1_archive'`, priorProjection); err != nil {
					t.Fatal(err)
				}
			}
			retentionEvent(t, archived, "peer_archive_old", clock.Now())
			clock.Advance(40 * 24 * time.Hour)
			retentionRun(t, archived)
			retentionFloor(t, archived, "peer_archive_old", 0)
			retentionConfirm(t, archived, prefs.Patch{Set: map[string]prefs.Value{"retention.archive_mode": prefs.Bool(false)}})
			future := retentionEvent(t, archived, "peer_archive_new", clock.Now())
			retentionRun(t, archived)
			retentionFloor(t, archived, "peer_archive_old", 0)
			retentionFloor(t, archived, "peer_archive_new", 0)
			clock.Advance(40 * 24 * time.Hour)
			retentionRun(t, archived)
			retentionFloor(t, archived, "peer_archive_old", 0)
			retentionFloor(t, archived, "peer_archive_new", future.Seq)
			// The old row remains protected and the prospective grant survives restart.
			exited, err := hub.New(h.DB, cfg, clock.Now)
			if err != nil {
				t.Fatal(err)
			}
			retentionRun(t, exited)
			retentionFloor(t, exited, "peer_archive_old", 0)
			retentionFloor(t, exited, "peer_archive_new", future.Seq)
		})
	}
}
