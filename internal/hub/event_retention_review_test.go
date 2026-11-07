package hub_test

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/eventlog"
	"github.com/XertroV/vecnet/internal/hub"
	"github.com/XertroV/vecnet/internal/prefs"
	"github.com/XertroV/vecnet/internal/testhub"
)

func TestRetentionBootstrapSourceSavePreservesApprovedRule(t *testing.T) {
	cfg := hub.DefaultConfig()
	cfg.AckedEventRetention = 30 * 24 * time.Hour
	h, _, clock := preferencesHubClock(t, cfg)
	c := retentionEvent(t, h, "peer_old", clock.Now())
	retentionAck(t, h, "peer_old", "*", c)
	clock.Advance(20 * 24 * time.Hour)
	cfg = h.Cfg
	cfg.AckedEventRetention = 14 * 24 * time.Hour
	restarted, err := hub.New(h.DB, cfg, clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	retentionRun(t, restarted)
	retentionFloor(t, restarted, "peer_old", 0)
	// Equal values need no shared confirmation. Saving only provenance must
	// neither activate the pending shorter bootstrap nor rewrite the old rule.
	patchPrefs(t, restarted, readPrefs(t, restarted), prefs.Patch{Set: map[string]prefs.Value{"retention.ack_events": prefs.Duration(14 * 24 * 3600)}})
	retentionRun(t, restarted)
	retentionFloor(t, restarted, "peer_old", 0)
	restarted, err = hub.New(h.DB, cfg, clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	retentionRun(t, restarted)
	retentionFloor(t, restarted, "peer_old", 0)
	// A confirmed prospective shortening still preserves the approved 30d
	// rule for this old event; a future event may use the new 10d rule.
	retentionConfirm(t, restarted, prefs.Patch{Set: map[string]prefs.Value{"retention.ack_events": prefs.Duration(10 * 24 * 3600)}})
	future := retentionEvent(t, restarted, "peer_future", clock.Now().Add(-11*24*time.Hour))
	retentionAck(t, restarted, "peer_future", "*", future)
	retentionRun(t, restarted)
	retentionFloor(t, restarted, "peer_old", 0)
	retentionFloor(t, restarted, "peer_future", future.Seq)
	clock.Advance(11 * 24 * time.Hour)
	retentionRun(t, restarted)
	retentionFloor(t, restarted, "peer_old", c.Seq)
}

func TestRetentionArchiveBootstrapGenerationRequiresFreshApproval(t *testing.T) {
	cfg := hub.DefaultConfig()
	cfg.UnackedEventRetention = 30 * 24 * time.Hour
	h, _, clock := preferencesHubClock(t, cfg)
	retentionConfirm(t, h, prefs.Patch{Set: map[string]prefs.Value{"retention.archive_mode": prefs.Bool(true)}})
	// Reset leaves a confirmed false/default boundary for an earlier exit.
	retentionConfirm(t, h, prefs.Patch{Reset: []string{"retention.archive_mode"}})
	cfg = h.Cfg
	cfg.PreferenceBootstrap = map[string]prefs.Effective{"retention.archive_mode": {Value: prefs.Bool(true), Source: prefs.SourceFlag, Origin: "VECNET_RETENTION_ARCHIVE_MODE"}}
	archived, err := hub.New(h.DB, cfg, clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	c := retentionEvent(t, archived, "peer_archived", clock.Now().Add(-40*24*time.Hour))
	cfg.PreferenceBootstrap = nil
	exited, err := hub.New(h.DB, cfg, clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	retentionRun(t, exited)
	retentionFloor(t, exited, "peer_archived", 0)
	// An equal-value Save and another restart cannot reuse the old exit.
	patchPrefs(t, exited, readPrefs(t, exited), prefs.Patch{Set: map[string]prefs.Value{"retention.archive_mode": prefs.Bool(false)}})
	exited, err = hub.New(h.DB, cfg, clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	retentionRun(t, exited)
	retentionFloor(t, exited, "peer_archived", 0)
	retentionConfirm(t, exited, prefs.Patch{Set: map[string]prefs.Value{"retention.archive_mode": prefs.Bool(false)}, ApplyExisting: true})
	retentionRun(t, exited)
	retentionFloor(t, exited, "peer_archived", 0)
	clock.Advance(24 * time.Hour)
	retentionRun(t, exited)
	retentionFloor(t, exited, "peer_archived", c.Seq)
}

func TestRetentionDeadLetterRetryKeepsFrozenDelivery(t *testing.T) {
	for _, references := range []bool{false, true} {
		name := "inline"
		if references {
			name = "reference"
		}
		t.Run(name, func(t *testing.T) {
			env := testhub.StartWith(t, func(cfg *hub.Config) { cfg.DeliveryReferences = references })
			h := env.Hub
			ctx := context.Background()
			_, sender := env.NewPeer("session", "sender")
			peer, secret := env.NewPeer("session", "recipient")
			cb := testhub.NewCallback(t)
			route, _, err := h.AddRoute(ctx, "peer", peer, cb.URL)
			if err != nil {
				t.Fatal(err)
			}
			if err := env.Worker.Verify(ctx, route); err != nil {
				t.Fatal(err)
			}
			retentionConfirm(t, h, prefs.Patch{Set: map[string]prefs.Value{"retention.ack_events": prefs.Duration(3600)}})
			m := mustSend(t, env, sender, req("dead_letter", peer))
			first, err := h.ClaimDue(ctx, h.Now(), 1)
			if err != nil || len(first) != 1 {
				t.Fatalf("initial claims=%d err=%v", len(first), err)
			}
			if err := h.PushResult(ctx, first[0], 410, 0, nil, h.Now()); err != nil {
				t.Fatal(err)
			}
			var seq int64
			if err := h.DB.QueryRow(`SELECT event_seq FROM deliveries WHERE msg_id=?`, m.MessageID).Scan(&seq); err != nil {
				t.Fatal(err)
			}
			caller, err := h.Auth.Resolve(ctx, secret)
			if err != nil {
				t.Fatal(err)
			}
			if err := h.Ack(ctx, caller, "", eventlog.Cursor{Epoch: h.Log.Epoch(), Seq: seq}); err != nil {
				t.Fatal(err)
			}
			env.Now.Advance(2 * time.Hour)
			retentionRun(t, h)
			if err := h.RetryDeadLetter(ctx, first[0].DeliveryID); err != nil {
				t.Fatal(err)
			}
			var budget int
			if err := h.DB.QueryRow(`SELECT attempts_left FROM deliveries WHERE dly_id=?`, first[0].DeliveryID).Scan(&budget); err != nil {
				t.Fatal(err)
			}
			if budget != hub.PushAttempts {
				t.Fatalf("retry budget=%d", budget)
			}
			retry, err := h.ClaimDue(ctx, h.Now(), 1)
			if err != nil || len(retry) != 1 {
				t.Fatalf("retry lost its replay source: claims=%d err=%v", len(retry), err)
			}
			if retry[0].DeliveryID != first[0].DeliveryID || retry[0].Attempt != first[0].Attempt+1 || !bytes.Equal(retry[0].Body, first[0].Body) {
				t.Fatal("retry changed identity, frozen bytes or attempt continuation")
			}
			if err := h.PushResult(ctx, retry[0], 410, 0, nil, h.Now()); err != nil {
				t.Fatal(err)
			}
			retentionRun(t, h)
			retentionFloor(t, h, peer, 0)
			if err := h.DiscardDeadLetter(ctx, retry[0].DeliveryID); err != nil {
				t.Fatal(err)
			}
			retentionRun(t, h)
			retentionFloor(t, h, peer, seq)
		})
	}
}

func TestRetentionBootstrapCycleInvalidatesEarlierPreview(t *testing.T) {
	cfg := hub.DefaultConfig()
	cfg.UnackedEventRetention = 30 * 24 * time.Hour
	h, _, clock := preferencesHubClock(t, cfg)
	p := prefs.Patch{Set: map[string]prefs.Value{"retention.archive_mode": prefs.Bool(false)}, ApplyExisting: true}
	snapshot := readPrefs(t, h)
	preview := previewPrefs(t, h.Preferences, prefsOperator, snapshot, p)
	cfg = h.Cfg
	cfg.PreferenceBootstrap = map[string]prefs.Effective{"retention.archive_mode": {Value: prefs.Bool(true), Source: prefs.SourceFlag, Origin: "VECNET_RETENTION_ARCHIVE_MODE"}}
	archived, err := hub.New(h.DB, cfg, clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	c := retentionEvent(t, archived, "peer_archived", clock.Now().Add(-40*24*time.Hour))
	cfg.PreferenceBootstrap = nil
	exited, err := hub.New(h.DB, cfg, clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	p.Confirm = []string{preview.ConfirmToken}
	// Even refreshing If-Match cannot make an earlier generation's preview
	// authorize this new exit. No time passes, so token TTL is not the guard.
	_, err = exited.Preferences.PatchPreferences(context.Background(), prefsOperator, readPrefs(t, exited).ETag, p)
	prefsCode(t, err, prefs.CodeConfirmationStale)
	retentionRun(t, exited)
	retentionFloor(t, exited, "peer_archived", 0)
	retentionConfirm(t, exited, prefs.Patch{Set: p.Set, ApplyExisting: true})
	clock.Advance(24 * time.Hour)
	retentionRun(t, exited)
	retentionFloor(t, exited, "peer_archived", c.Seq)
}

func TestRetentionPendingBootstrapPartialLengtheningIsNotApproval(t *testing.T) {
	cfg := hub.DefaultConfig()
	cfg.AckedEventRetention = 30 * 24 * time.Hour
	h, _, clock := preferencesHubClock(t, cfg)
	cfg = h.Cfg
	cfg.AckedEventRetention = 14 * 24 * time.Hour
	restarted, err := hub.New(h.DB, cfg, clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	// 20d is safer than the visible 14d, but still shorter than the approved
	// 30d. The shared service asks no confirmation for this Save.
	patchPrefs(t, restarted, readPrefs(t, restarted), prefs.Patch{Set: map[string]prefs.Value{"retention.ack_events": prefs.Duration(20 * 24 * 3600)}})
	c := retentionEvent(t, restarted, "peer_pending", clock.Now().Add(-25*24*time.Hour))
	retentionAck(t, restarted, "peer_pending", "*", c)
	retentionRun(t, restarted)
	retentionFloor(t, restarted, "peer_pending", 0)
	restarted, err = hub.New(h.DB, cfg, clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	retentionRun(t, restarted)
	retentionFloor(t, restarted, "peer_pending", 0)
}
