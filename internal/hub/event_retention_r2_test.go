package hub_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/eventlog"
	"github.com/XertroV/vecnet/internal/hub"
	"github.com/XertroV/vecnet/internal/prefs"
	"github.com/XertroV/vecnet/internal/testhub"
)

func TestRetentionDisabledArchiveExitCannotAuthorizeHardRestart(t *testing.T) {
	for _, initialHard := range []time.Duration{0, 90 * 24 * time.Hour} {
		t.Run(initialHard.String(), func(t *testing.T) {
			cfg := hub.DefaultConfig()
			cfg.UnackedEventRetention = initialHard
			h, _, clock := preferencesHubClock(t, cfg)
			retentionConfirm(t, h, prefs.Patch{Set: map[string]prefs.Value{"retention.ack_events": prefs.Duration(0), "retention.archive_mode": prefs.Bool(true)}})
			c := retentionEvent(t, h, "peer_archived", clock.Now().Add(-100*24*time.Hour))
			retentionConfirm(t, h, prefs.Patch{Set: map[string]prefs.Value{"retention.archive_mode": prefs.Bool(false)}, ApplyExisting: true})
			clock.Advance(24 * time.Hour) // The disabled/90d approval has reached its nightly window.
			cfg = h.Cfg
			cfg.UnackedEventRetention = 30 * 24 * time.Hour
			restarted, err := hub.New(h.DB, cfg, clock.Now)
			if err != nil {
				t.Fatal(err)
			}
			retentionRun(t, restarted)
			retentionFloor(t, restarted, "peer_archived", 0)
			// New rows are also protected until the new hard class is confirmed.
			future := retentionEvent(t, restarted, "peer_future", clock.Now().Add(-40*24*time.Hour))
			retentionRun(t, restarted)
			retentionFloor(t, restarted, "peer_future", 0)
			retentionConfirm(t, restarted, prefs.Patch{Set: map[string]prefs.Value{"retention.archive_mode": prefs.Bool(false)}, ApplyExisting: true})
			retentionRun(t, restarted)
			retentionFloor(t, restarted, "peer_archived", 0)
			clock.Advance(24 * time.Hour)
			retentionRun(t, restarted)
			retentionFloor(t, restarted, "peer_archived", c.Seq)
			retentionFloor(t, restarted, "peer_future", future.Seq)
		})
	}
}

func TestRetentionHardConfigInvalidatesSharedPreview(t *testing.T) {
	for _, duration := range []time.Duration{30 * 24 * time.Hour, 90 * 24 * time.Hour, 0} {
		t.Run(duration.String(), func(t *testing.T) {
			cfg := hub.DefaultConfig()
			h, _, clock := preferencesHubClock(t, cfg)
			retentionConfirm(t, h, prefs.Patch{Set: map[string]prefs.Value{"retention.archive_mode": prefs.Bool(true)}})
			patch := prefs.Patch{Set: map[string]prefs.Value{"retention.archive_mode": prefs.Bool(false)}, ApplyExisting: true}
			snapshot := readPrefs(t, h)
			preview := previewPrefs(t, h.Preferences, prefsOperator, snapshot, patch)
			cfg = h.Cfg
			cfg.UnackedEventRetention = duration
			if duration == 0 {
				cfg.UnackedEventRetention = time.Hour
			} // Also test returning to the original disabled duration.
			changed, err := hub.New(h.DB, cfg, clock.Now)
			if err != nil {
				t.Fatal(err)
			}
			if duration == 0 {
				cfg.UnackedEventRetention = 0
				changed, err = hub.New(h.DB, cfg, clock.Now)
				if err != nil {
					t.Fatal(err)
				}
			}
			current := readPrefs(t, changed)
			if current.ETag == snapshot.ETag {
				t.Error("hard config transition reused shared ETag")
			}
			patch.Confirm = []string{preview.ConfirmToken}
			_, err = changed.Preferences.PatchPreferences(context.Background(), prefsOperator, current.ETag, patch)
			prefsCode(t, err, prefs.CodeConfirmationStale)
		})
	}
}

func TestRetentionReadAndReceiptAreNotPullAcks(t *testing.T) {
	for _, state := range []string{"read", "processed"} {
		t.Run(state, func(t *testing.T) {
			env := testhub.Start(t)
			h := env.Hub
			peer, secret := env.NewPeer("session", "recipient")
			_, sender := env.NewPeer("session", "sender")
			m := mustSend(t, env, sender, req("synthetic", peer))
			var seq int64
			if err := h.DB.QueryRow(`SELECT event_seq FROM deliveries WHERE msg_id=?`, m.MessageID).Scan(&seq); err != nil {
				t.Fatal(err)
			}
			// Remove earlier setup/thread events, as a prior successful prefix sweep
			// would, so this aged delivery is the first candidate, not a younger
			// row hidden behind an unrelated unacknowledged prefix.
			if _, err := h.DB.Exec(`DELETE FROM events WHERE owner=? AND seq<?`, peer, seq); err != nil {
				t.Fatal(err)
			}
			base, err := h.Log.OldestFor(context.Background(), []string{peer})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := h.DB.Exec(`UPDATE events SET at=? WHERE seq=?`, envelope.FormatTime(h.Now().Add(-8*24*time.Hour)), seq); err != nil {
				t.Fatal(err)
			}
			if state == "read" {
				// Transport acceptance removes the retry pin, without a pull ack or processed receipt.
				if _, err := h.DB.Exec(`UPDATE deliveries SET state='accepted' WHERE msg_id=?`, m.MessageID); err != nil {
					t.Fatal(err)
				}
			}
			resp, _ := env.Do(secret, "POST", "/v1/receipts", map[string]string{"delivery_id": m.Deliveries[0].DeliveryID, "state": state})
			if resp.StatusCode != http.StatusNoContent {
				t.Fatalf("receipt status=%d", resp.StatusCode)
			}
			var gotState string
			var read bool
			if err := h.DB.QueryRow(`SELECT state,read_at IS NOT NULL FROM deliveries WHERE msg_id=?`, m.MessageID).Scan(&gotState, &read); err != nil {
				t.Fatal(err)
			}
			if read != (state == "read") || state == "processed" && gotState != "processed" {
				t.Fatal("receipt/read cases were not independent")
			}
			retentionRun(t, h)
			retentionFloor(t, h, peer, base.Seq)
			retentionAck(t, h, peer, "*", eventlog.Cursor{Epoch: 1, Seq: seq})
			retentionRun(t, h)
			retentionFloor(t, h, peer, seq)
		})
	}
}

func TestRetentionHardApprovalKeepsClassSpecificCutoff(t *testing.T) {
	cfg := hub.DefaultConfig()
	cfg.UnackedEventRetention = 30 * 24 * time.Hour
	h, _, clock := preferencesHubClock(t, cfg)
	retentionConfirm(t, h, prefs.Patch{Set: map[string]prefs.Value{"retention.archive_mode": prefs.Bool(true)}})
	old := retentionEvent(t, h, "peer_old", clock.Now().Add(-40*24*time.Hour))
	retentionEvent(t, h, "peer_young", clock.Now().Add(-29*24*time.Hour))
	retentionConfirm(t, h, prefs.Patch{Set: map[string]prefs.Value{"retention.archive_mode": prefs.Bool(false)}, ApplyExisting: true})
	clock.Advance(2 * 24 * time.Hour)
	retentionRun(t, h)
	retentionFloor(t, h, "peer_old", old.Seq)
	// This row has now reached hard age, but was outside that class's fixed
	// preview cutoff. The shorter 7d acknowledged class cannot authorize it.
	retentionFloor(t, h, "peer_young", 0)
}
