package hub_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/eventlog"
	"github.com/XertroV/vecnet/internal/hub"
)

func TestRetentionOneRowSliceIndependentOfExpiredSubscriptions(t *testing.T) {
	for _, analyze := range []bool{false, true} {
		var small int
		for _, n := range []int{100, 10000} {
			cfg := hub.DefaultConfig()
			cfg.EventRetentionLimits = eventlog.RetentionLimits{Rows: 1, Target: time.Hour}
			h, _, clock := preferencesHubClock(t, cfg)
			c := retentionEvent(t, h, "peer_target", clock.Now().Add(-8*24*time.Hour))
			retentionAck(t, h, "peer_target", "*", c)
			// Grow subscription history independently of queue size. Include expired
			// leases and expired tokens whose leases have no deadline, still verified.
			_, err := h.DB.Exec(`WITH RECURSIVE n(i) AS (VALUES(1) UNION ALL SELECT i+1 FROM n WHERE i<?)
    INSERT INTO event_subscriptions(sub_id,principal_kind,principal_id,peer_id,name,arguments,url,secret_enc,cursor_epoch,cursor_seq,types,verified_at,expires_at,token_expires_at,created_at,updated_at)
    SELECT 'sub_'||i,'vecnet','prn_synthetic','peer_target','vecnet.message','{}','https://synthetic.invalid/'||i,'synthetic',1,0,'[]','2026-01-01T00:00:00.000Z',
    CASE WHEN i%2=0 THEN '2026-01-01T00:00:00.000Z' END,
    CASE WHEN i%2=1 THEN '2026-01-01T00:00:00.000Z' END,'2026-01-01T00:00:00.000Z','2026-01-01T00:00:00.000Z' FROM n`, n)
			if err != nil {
				t.Fatal(err)
			}
			if analyze {
				if _, err := h.DB.Exec(`ANALYZE`); err != nil {
					t.Fatal(err)
				}
			}
			stat, reads := retentionSlicePageReads(t, h)
			t.Logf("analyze=%v expired_subscriptions=%d pages=%d scanned=%d deleted=%d", analyze, n, reads, stat.Scanned, stat.Deleted)
			if stat.Scanned != 1 || stat.Deleted != 1 {
				t.Fatalf("expired rows changed eligibility: %+v", stat)
			}
			if n == 100 {
				small = reads
			} else if reads > small+64 {
				t.Errorf("writer scans expired subscriptions: small=%d large=%d", small, reads)
			}
		}
	}
}

func TestRetentionOneRowSliceIndependentOfPolicyHistory(t *testing.T) {
	// This fixture owns its database and clock; overlap its expensive setup
	// with other independent scale guards without reducing their coverage.
	t.Parallel()
	var small int
	for _, n := range []int{100, 10000} {
		cfg := hub.DefaultConfig()
		cfg.EventRetentionLimits = eventlog.RetentionLimits{Rows: 1, Target: time.Hour}
		h, _, clock := preferencesHubClock(t, cfg)
		c := retentionEvent(t, h, "peer_target", clock.Now().Add(-24*time.Hour))
		retentionAck(t, h, "peer_target", "*", c)
		// Model a populated pre-upgrade policy ledger. All entries are valid and
		// prospective at the same sequence boundary, with only one retained event.
		ack, _ := readPrefs(t, h).Get("retention.ack_events")
		tag, _ := json.Marshal(struct {
			Value  any
			Source any
			Origin string
		}{ack.Value, ack.Source, ack.Origin})
		bounds := make([]map[string]any, n)
		for i := range bounds {
			bounds[i] = map[string]any{"revision": i + 1, "from_seq": 0, "before_seconds": 604800, "after_seconds": 604800, "policy_tag": string(tag)}
		}
		raw, _ := json.Marshal(bounds)
		if _, err := h.DB.Exec(`INSERT INTO meta(key,value) VALUES('event_retention_projection_v1',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, string(raw)); err != nil {
			t.Fatal(err)
		}
		// Grow the shared activation history too. The absent archive approval
		// must not require scanning every acknowledged-policy activation.
		if _, err := h.DB.Exec(`WITH RECURSIVE n(i) AS (VALUES(1) UNION ALL SELECT i+1 FROM n WHERE i<?)
			INSERT INTO hub_preference_activations(revision,key,effective_from,approval_digest)
			SELECT i,'retention.ack_events','2026-11-01T00:00:00.000Z','synthetic' FROM n`, n); err != nil {
			t.Fatal(err)
		}
		if _, err := h.DB.Exec(`UPDATE hub_preferences_state SET revision=? WHERE id=1`, n); err != nil {
			t.Fatal(err)
		}
		// Startup upgrades historical projections before allowing pruning.
		var err error
		h, err = hub.New(h.DB, h.Cfg, clock.Now)
		if err != nil {
			t.Fatal(err)
		}
		stat, reads := retentionSlicePageReads(t, h)
		t.Logf("boundaries=%d pages=%d scanned=%d deleted=%d", n, reads, stat.Scanned, stat.Deleted)
		if stat.Scanned != 1 || stat.Deleted != 0 {
			t.Fatalf("young event eligibility changed: %+v", stat)
		}
		if n == 100 {
			small = reads
		} else if reads > small+64 {
			t.Errorf("writer loads policy history: small=%d large=%d", small, reads)
		}
	}
}

func TestRetentionExpiredSubscriptionsRetainExactCopies(t *testing.T) {
	for _, state := range []string{"pending", "parked", "exhausted", "terminal"} {
		t.Run(state, func(t *testing.T) {
			cfg := hub.DefaultConfig()
			cfg.EventRetentionLimits = eventlog.RetentionLimits{Rows: 1, Target: time.Hour}
			h, _, clock := preferencesHubClock(t, cfg)
			c := retentionEvent(t, h, "peer_target", clock.Now().Add(-8*24*time.Hour))
			retentionAck(t, h, "peer_target", "*", c)
			_, err := h.DB.Exec(`WITH RECURSIVE n(i) AS (VALUES(1) UNION ALL SELECT i+1 FROM n WHERE i<10000)
    INSERT INTO event_subscriptions(sub_id,principal_kind,principal_id,peer_id,name,arguments,url,secret_enc,cursor_epoch,cursor_seq,types,expires_at,created_at,updated_at)
    SELECT 'sub_'||i,'vecnet','prn_synthetic','peer_target','vecnet.message','{}','https://synthetic.invalid/'||i,'synthetic',1,0,'[]','2026-01-01T00:00:00.000Z','2026-01-01T00:00:00.000Z','2026-01-01T00:00:00.000Z' FROM n`)
			if err != nil {
				t.Fatal(err)
			}
			var done, terminal any
			parked := 0
			if state == "parked" {
				parked = 1
			}
			if state == "exhausted" || state == "terminal" {
				done = "2026-11-01T00:00:00.000Z"
				terminal = "exhausted"
			}
			if state == "terminal" {
				terminal = "http_410"
			}
			_, err = h.DB.Exec(`INSERT INTO event_outbox(sub_id,event_id,seq,body,created_at,done_at,terminal,parked) VALUES('sub_10000','event_candidate',?,'{"synthetic":"exact callback bytes"}','2026-11-01T00:00:00.000Z',?,?,?)`, c.Seq, done, terminal, parked)
			if err != nil {
				t.Fatal(err)
			}
			for _, analyze := range []bool{false, true} {
				if analyze {
					if _, err := h.DB.Exec(`ANALYZE`); err != nil {
						t.Fatal(err)
					}
				}
				stat, reads := retentionSlicePageReads(t, h)
				t.Logf("state=%s analyze=%v pages=%d deleted=%d pinned=%d", state, analyze, reads, stat.Deleted, stat.Pinned)
				want := 1
				if state == "terminal" {
					want = 0
				}
				if stat.Pinned != want || (state != "terminal" && stat.Deleted != 0) {
					t.Fatalf("copy lost its pin: %+v", stat)
				}
				if state == "terminal" && !analyze && stat.Deleted != 1 {
					t.Fatalf("terminal copy retained event: %+v", stat)
				}
				var body string
				if err := h.DB.QueryRow(`SELECT body FROM event_outbox WHERE sub_id='sub_10000'`).Scan(&body); err != nil || body != `{"synthetic":"exact callback bytes"}` {
					t.Fatal("callback body changed", err)
				}
			}
		})
	}
}
