package hub_test

import (
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/hub"
	"github.com/XertroV/vecnet/internal/prefs"
)

func TestRetentionProtectiveBootstrapSurvivesProspectiveShortening(t *testing.T) {
	cfg := hub.DefaultConfig()
	cfg.AckedEventRetention = 14 * 24 * time.Hour
	h, _, clock := preferencesHubClock(t, cfg)
	c := retentionEvent(t, h, "peer_old", clock.Now())
	retentionAck(t, h, "peer_old", "*", c)
	retentionConfirm(t, h, prefs.Patch{Set: map[string]prefs.Value{"retention.ack_events": prefs.Duration(14 * 24 * 3600)}})
	retentionConfirm(t, h, prefs.Patch{Reset: []string{"retention.ack_events"}})
	clock.Advance(10 * 24 * time.Hour)
	cfg = h.Cfg
	cfg.AckedEventRetention = 30 * 24 * time.Hour
	restarted, err := hub.New(h.DB, cfg, clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	// The protective bootstrap must survive another restart before the Save.
	restarted, err = hub.New(h.DB, cfg, clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	clock.Advance(15 * 24 * time.Hour)
	retentionConfirm(t, restarted, prefs.Patch{Set: map[string]prefs.Value{"retention.ack_events": prefs.Duration(20 * 24 * 3600)}})
	future := retentionEvent(t, restarted, "peer_future", clock.Now().Add(-21*24*time.Hour))
	retentionAck(t, restarted, "peer_future", "*", future)
	retentionRun(t, restarted)
	retentionFloor(t, restarted, "peer_old", 0)
	retentionFloor(t, restarted, "peer_future", future.Seq)
	clock.Advance(5 * 24 * time.Hour)
	retentionRun(t, restarted)
	retentionFloor(t, restarted, "peer_old", c.Seq)
}

func TestRetentionConfigZeroDisablesAndStoredValueWins(t *testing.T) {
	cfg := hub.DefaultConfig()
	cfg.AckedEventRetention = 0
	h, _, clock := preferencesHubClock(t, cfg)
	e, err := readPrefs(t, h).Get("retention.ack_events")
	if err != nil || e.Value != prefs.Duration(0) || e.Source != prefs.SourceFlag {
		t.Errorf("explicit Config zero lost: effective=%+v err=%v", e, err)
	}
	c := retentionEvent(t, h, "peer_disabled", clock.Now())
	retentionAck(t, h, "peer_disabled", "*", c)
	clock.Advance(8 * 24 * time.Hour)
	retentionRun(t, h)
	retentionFloor(t, h, "peer_disabled", 0)
	retentionConfirm(t, h, prefs.Patch{Set: map[string]prefs.Value{"retention.ack_events": prefs.Duration(14 * 24 * 3600)}})
	cfg = h.Cfg
	restarted, err := hub.New(h.DB, cfg, clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	e, err = readPrefs(t, restarted).Get("retention.ack_events")
	if err != nil || e.Value != prefs.Duration(14*24*3600) || e.Source != prefs.SourceStored {
		t.Fatalf("stored value lost to Config zero: effective=%+v err=%v", e, err)
	}
	retentionRun(t, restarted)
	retentionFloor(t, restarted, "peer_disabled", 0)
	future := retentionEvent(t, restarted, "peer_stored", clock.Now().Add(-15*24*time.Hour))
	retentionAck(t, restarted, "peer_stored", "*", future)
	retentionRun(t, restarted)
	retentionFloor(t, restarted, "peer_stored", future.Seq)
}

func TestRetentionConfigUnsetUsesDefault(t *testing.T) {
	for _, tc := range []struct {
		name    string
		cfg     hub.Config
		seconds int64
		source  prefs.Source
	}{
		{"unset", hub.Config{}, 7 * 24 * 3600, prefs.SourceDefault},
		{"explicit_zero", hub.Config{AckedEventRetentionSet: true}, 0, prefs.SourceFlag},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, _, _ := preferencesHubClock(t, tc.cfg)
			e, err := readPrefs(t, h).Get("retention.ack_events")
			if err != nil || e.Value != prefs.Duration(tc.seconds) || e.Source != tc.source {
				t.Fatalf("Config presence lost: effective=%+v err=%v", e, err)
			}
		})
	}
}
