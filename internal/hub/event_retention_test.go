package hub_test

import (
	"bytes"
	"context"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/eventlog"
	"github.com/XertroV/vecnet/internal/hub"
	"github.com/XertroV/vecnet/internal/prefs"
	"github.com/XertroV/vecnet/internal/testhub"
)

func retentionEvent(t *testing.T, h *hub.Hub, owner string, at time.Time) eventlog.Cursor {
	t.Helper()
	c, err := h.Log.Append(context.Background(), nil, owner, "thread_updated", at, map[string]any{"thread_id": "thr_synthetic"})
	if err != nil {
		t.Fatal(err)
	}
	return c
}
func retentionAck(t *testing.T, h *hub.Hub, owner, scope string, c eventlog.Cursor) {
	t.Helper()
	if err := h.Log.Ack(context.Background(), nil, owner, scope, c); err != nil {
		t.Fatal(err)
	}
}
func retentionFloor(t *testing.T, h *hub.Hub, owner string, want int64) {
	t.Helper()
	c, err := h.Log.OldestFor(context.Background(), []string{owner})
	if err != nil || c.Seq != want {
		t.Fatalf("floor=%v err=%v want seq=%d", c, err, want)
	}
}
func retentionRun(t *testing.T, h *hub.Hub) {
	t.Helper()
	if _, err := h.PruneEventSlice(context.Background()); err != nil {
		t.Fatal(err)
	}
}
func retentionConfirm(t *testing.T, h *hub.Hub, p prefs.Patch) {
	t.Helper()
	s := readPrefs(t, h)
	v := previewPrefs(t, h.Preferences, prefsOperator, s, p)
	if len(v.Risks) > 0 {
		p.Confirm = []string{v.ConfirmToken}
	}
	patchPrefs(t, h, s, p)
}

func TestAcknowledgedRetentionScopesAndPrefix(t *testing.T) {
	for _, scope := range []string{"peer", "principal", "narrowed", "none", "scan"} {
		t.Run(scope, func(t *testing.T) {
			env := testhub.Start(t)
			h := env.Hub
			peer, _ := env.NewPeer("session", "recipient")
			var prn string
			if err := h.DB.QueryRow(`SELECT prn_id FROM peers WHERE peer_id=?`, peer).Scan(&prn); err != nil {
				t.Fatal(err)
			}
			c := retentionEvent(t, h, peer, h.Now().Add(-8*24*time.Hour))
			switch scope {
			case "peer":
				retentionAck(t, h, peer, "*", c)
			case "principal":
				retentionAck(t, h, prn, "*", c)
			case "narrowed":
				retentionAck(t, h, prn, peer, c)
			case "scan":
				retentionSubscription(t, h, peer, c, "live")
			}
			retentionRun(t, h)
			want := int64(0)
			if scope == "peer" || scope == "principal" || scope == "narrowed" {
				want = c.Seq
			}
			retentionFloor(t, h, peer, want)
		})
	}
	env := testhub.Start(t)
	h := env.Hub
	p, _ := env.NewPeer("session", "prefix")
	a := retentionEvent(t, h, p, h.Now().Add(-9*24*time.Hour))
	retentionEvent(t, h, p, h.Now())
	last := retentionEvent(t, h, p, h.Now().Add(-9*24*time.Hour))
	retentionAck(t, h, p, "*", last)
	retentionRun(t, h)
	retentionFloor(t, h, p, a.Seq)
}

func retentionSubscription(t *testing.T, h *hub.Hub, peer string, c eventlog.Cursor, state string) {
	t.Helper()
	var verified, paused, expiry any
	verified = envelope.FormatTime(h.Now())
	if state == "paused" {
		paused = "operator"
	}
	if state == "expired" {
		expiry = envelope.FormatTime(h.Now().Add(-time.Hour))
	}
	if state == "unverified" {
		verified = nil
	}
	_, err := h.DB.Exec(`INSERT INTO event_subscriptions(sub_id,principal_kind,principal_id,peer_id,name,arguments,url,secret_enc,cursor_epoch,cursor_seq,types,verified_at,paused_reason,expires_at,created_at,updated_at) VALUES('sub_synthetic','vecnet',?,?,'vecnet.message','{}','https://synthetic.invalid/callback','synthetic',?,?, '["delivery"]',?,?,?, ?,?)`, peer, peer, c.Epoch, c.Seq, verified, paused, expiry, envelope.FormatTime(h.Now()), envelope.FormatTime(h.Now()))
	if err != nil {
		t.Fatal(err)
	}
}
func TestRetentionSubscriptionEligibilityAndOutboxPins(t *testing.T) {
	for _, state := range []string{"live_lag", "live_past", "live_exact", "paused", "expired", "unverified", "pending", "parked", "exhausted", "terminal"} {
		t.Run(state, func(t *testing.T) {
			env := testhub.Start(t)
			h := env.Hub
			p, _ := env.NewPeer("session", "subscription")
			c := retentionEvent(t, h, p, h.Now().Add(-8*24*time.Hour))
			retentionAck(t, h, p, "*", c)
			cur := eventlog.Cursor{Epoch: c.Epoch}
			subState := state
			if state == "live_past" {
				cur = c
			}
			retentionSubscription(t, h, p, cur, subState)
			if state == "live_exact" || state == "pending" || state == "parked" || state == "exhausted" || state == "terminal" {
				var done, terminal any
				parked := 0
				if state == "exhausted" {
					done = envelope.FormatTime(h.Now())
					terminal = "exhausted"
				}
				if state == "terminal" || state == "live_exact" {
					done = envelope.FormatTime(h.Now())
					terminal = "http_410"
				}
				if state == "parked" {
					parked = 1
				}
				if _, err := h.DB.Exec(`INSERT INTO event_outbox(sub_id,event_id,seq,body,created_at,done_at,terminal,parked) VALUES('sub_synthetic',? ,?,'{}',?,?,?,?)`, c.String(), c.Seq, envelope.FormatTime(h.Now()), done, terminal, parked); err != nil {
					t.Fatal(err)
				}
			}
			retentionRun(t, h)
			want := c.Seq
			if state == "live_lag" || state == "pending" || state == "parked" || state == "exhausted" {
				want = 0
			}
			retentionFloor(t, h, p, want)
		})
	}
}
func TestRetentionDeliveryPins(t *testing.T) {
	for _, state := range []string{"queued", "pushed", "held", "dispatching", "settled"} {
		t.Run(state, func(t *testing.T) {
			env := testhub.Start(t)
			h := env.Hub
			_, as := env.NewPeer("session", "sender")
			p, _ := env.NewPeer("session", "recipient")
			m := mustSend(t, env, as, req("synthetic", p))
			var seq int64
			if err := h.DB.QueryRow(`SELECT event_seq FROM deliveries WHERE msg_id=?`, m.MessageID).Scan(&seq); err != nil {
				t.Fatal(err)
			}
			s := state
			held, dispatch := 0, 0
			if state == "held" {
				s = "queued"
				held = 1
			}
			if state == "dispatching" {
				s = "accepted"
				dispatch = 1
			}
			if state == "settled" {
				s = "accepted"
			}
			if _, err := h.DB.Exec(`UPDATE deliveries SET state=?,held=?,dispatching=? WHERE msg_id=?`, s, held, dispatch, m.MessageID); err != nil {
				t.Fatal(err)
			}
			retentionAck(t, h, p, "*", eventlog.Cursor{Epoch: h.Log.Epoch(), Seq: seq})
			env.Now.Advance(8 * 24 * time.Hour)
			retentionRun(t, h)
			want := int64(0)
			if state == "settled" {
				want = seq
			}
			retentionFloor(t, h, p, want)
		})
	}
}
func TestRetentionPreferencesActivationAndRestart(t *testing.T) {
	cfg := hub.DefaultConfig()
	cfg.PreferenceBootstrap = map[string]prefs.Effective{"retention.archive_mode": {Value: prefs.Bool(true), Source: prefs.SourceFlag, Origin: "VECNET_RETENTION_ARCHIVE_MODE"}}
	h, path, clock := preferencesHubClock(t, cfg)
	c := retentionEvent(t, h, "peer_synthetic", clock.Now().Add(-2*time.Hour))
	retentionAck(t, h, "peer_synthetic", "*", c)
	p := prefs.Patch{Set: map[string]prefs.Value{"retention.ack_events": prefs.Duration(3600)}, ApplyExisting: true}
	s := readPrefs(t, h)
	preview := previewPrefs(t, h.Preferences, prefsOperator, s, p)
	if preview.Impact.NotBefore == "" || preview.Impact.HighWater == nil || *preview.Impact.HighWater < c.Seq {
		t.Fatal("missing fixed bounds")
	}
	p.Confirm = []string{preview.ConfirmToken}
	next := patchPrefs(t, h, s, p)
	prefValue(t, next, "retention.ack_events", 3600, prefs.SourceStored, "")
	prefsAuditCount(t, h, 1)
	retentionRun(t, h)
	retentionFloor(t, h, "peer_synthetic", 0)
	again, err := hub.New(h.DB, h.Cfg, clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	_ = path
	retentionRun(t, again)
	retentionFloor(t, again, "peer_synthetic", 0)
	nb, err := envelope.ParseTime(preview.Impact.NotBefore)
	if err != nil {
		t.Fatal(err)
	}
	clock.Advance(nb.Sub(clock.Now()))
	retentionRun(t, again)
	retentionFloor(t, again, "peer_synthetic", c.Seq)
	retentionConfirm(t, again, prefs.Patch{Set: map[string]prefs.Value{"retention.ack_events": prefs.Duration(0), "retention.archive_mode": prefs.Bool(false)}})
	e, err := again.Preferences.Current().Get("retention.archive_mode")
	b, ok := e.Value.Bool()
	if err != nil || !ok || b || e.Source != prefs.SourceStored {
		t.Fatal("explicit false lost")
	}
	fresh := retentionEvent(t, again, "peer_synthetic", clock.Now().Add(-30*24*time.Hour))
	retentionAck(t, again, "peer_synthetic", "*", fresh)
	retentionRun(t, again)
	retentionFloor(t, again, "peer_synthetic", c.Seq)
}
func TestRetentionProspectiveOnlyAndProtectiveCancellation(t *testing.T) {
	h, _, clock := preferencesHubClock(t, hub.DefaultConfig())
	p := "peer_synthetic"
	old := retentionEvent(t, h, p, clock.Now().Add(-2*time.Hour))
	retentionAck(t, h, p, "*", old)
	retentionConfirm(t, h, prefs.Patch{Set: map[string]prefs.Value{"retention.ack_events": prefs.Duration(3600)}})
	clock.Advance(2 * time.Hour)
	retentionRun(t, h)
	retentionFloor(t, h, p, 0)
	retentionConfirm(t, h, prefs.Patch{Set: map[string]prefs.Value{"retention.ack_events": prefs.Duration(3600)}, ApplyExisting: true})
	retentionConfirm(t, h, prefs.Patch{Set: map[string]prefs.Value{"retention.ack_events": prefs.Duration(0)}})
	clock.Advance(24 * time.Hour)
	retentionRun(t, h)
	retentionFloor(t, h, p, 0)
}
func TestRetentionPreferenceReadErrorSkipsDeletion(t *testing.T) {
	h, _, clock := preferencesHubClock(t, hub.DefaultConfig())
	c := retentionEvent(t, h, "peer_synthetic", clock.Now().Add(-9*24*time.Hour))
	retentionAck(t, h, "peer_synthetic", "*", c)
	if _, err := h.DB.Exec(`INSERT INTO hub_preferences VALUES('retention.ack_events','"bad"',?,'synthetic')`, envelope.FormatTime(clock.Now())); err != nil {
		t.Fatal(err)
	}
	if _, err := h.PruneEventSlice(context.Background()); err == nil {
		t.Fatal("invalid policy silently used fallback")
	}
	retentionFloor(t, h, "peer_synthetic", 0)
}
func TestRetentionArchiveSuppressesHardExpiryButNotAcknowledged(t *testing.T) {
	cfg := hub.DefaultConfig()
	cfg.UnackedEventRetention = 30 * 24 * time.Hour
	h, _, clock := preferencesHubClock(t, cfg)
	retentionConfirm(t, h, prefs.Patch{Set: map[string]prefs.Value{"retention.archive_mode": prefs.Bool(true)}})
	a := retentionEvent(t, h, "peer_ack", clock.Now().Add(-40*24*time.Hour))
	retentionEvent(t, h, "peer_unacked", clock.Now().Add(-40*24*time.Hour))
	retentionAck(t, h, "peer_ack", "*", a)
	retentionRun(t, h)
	retentionFloor(t, h, "peer_ack", a.Seq)
	retentionFloor(t, h, "peer_unacked", 0)
}
func TestRetentionPurgeAckAndReadRace(t *testing.T) {
	for _, boundary := range []string{"floor", "events", "hydration"} {
		t.Run(boundary, func(t *testing.T) { retentionReadPurgeInterleaving(t, boundary) })
	}
}

func TestRetentionInvalidAckEpochAndFutureAck(t *testing.T) {
	for _, invalid := range []string{"old_epoch", "future_seq", "foreign_principal"} {
		t.Run(invalid, func(t *testing.T) {
			env := testhub.Start(t)
			h := env.Hub
			p, _ := env.NewPeer("session", "recipient")
			c := retentionEvent(t, h, p, h.Now().Add(-9*24*time.Hour))
			owner, seq, epoch := p, c.Seq, c.Epoch
			if invalid == "old_epoch" {
				epoch++
			}
			if invalid == "future_seq" {
				seq += 100
			}
			if invalid == "foreign_principal" {
				owner = "prn_foreign"
			}
			if _, err := h.DB.Exec(`INSERT INTO acks(owner,scope,seq,acked_at,epoch) VALUES(?,'*',?,?,?)`, owner, seq, envelope.FormatTime(h.Now()), epoch); err != nil {
				t.Fatal(err)
			}
			retentionRun(t, h)
			retentionFloor(t, h, p, 0)
		})
	}
}
func TestRetentionBoundsMetricsAndDisabledBytes(t *testing.T) {
	cfg := hub.DefaultConfig()
	cfg.EventRetentionLimits.Rows = 1
	h, _, clock := preferencesHubClock(t, cfg)
	p := "peer_synthetic"
	first := retentionEvent(t, h, p, clock.Now().Add(-9*24*time.Hour))
	last := retentionEvent(t, h, p, clock.Now().Add(-9*24*time.Hour))
	retentionAck(t, h, p, "*", last)
	stat, err := h.PruneEventSlice(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if stat.Deleted != 1 || stat.Scanned != 1 || stat.Bytes == 0 || stat.OldestAgeSeconds != 9*24*3600 {
		t.Fatalf("unbounded or missing metrics %+v", stat)
	}
	retentionFloor(t, h, p, first.Seq)
	retentionConfirm(t, h, prefs.Patch{Set: map[string]prefs.Value{"retention.ack_events": prefs.Duration(0)}})
	before, err := h.Log.Read(context.Background(), []string{p}, first, 100)
	if err != nil {
		t.Fatal(err)
	}
	retentionRun(t, h)
	after, err := h.Log.Read(context.Background(), []string{p}, first, 100)
	if err != nil || len(after) != 1 || len(before) != 1 || string(after[0].Data) != string(before[0].Data) || after[0].Cursor != before[0].Cursor {
		t.Fatal("disabled pruning changed replay bytes")
	}
}
func TestRetentionArchiveExitPreservesEarlierUnackedEvents(t *testing.T) {
	cfg := hub.DefaultConfig()
	cfg.UnackedEventRetention = 30 * 24 * time.Hour
	h, _, clock := preferencesHubClock(t, cfg)
	retentionConfirm(t, h, prefs.Patch{Set: map[string]prefs.Value{"retention.archive_mode": prefs.Bool(true)}})
	retentionEvent(t, h, "peer_old", clock.Now().Add(-40*24*time.Hour))
	retentionConfirm(t, h, prefs.Patch{Set: map[string]prefs.Value{"retention.archive_mode": prefs.Bool(false)}})
	clock.Advance(24 * time.Hour)
	retentionRun(t, h)
	retentionFloor(t, h, "peer_old", 0)
	fresh := retentionEvent(t, h, "peer_new", clock.Now().Add(-40*24*time.Hour))
	retentionRun(t, h)
	retentionFloor(t, h, "peer_new", fresh.Seq)
}

func TestRetentionBootstrapRestartCannotAuthorizeDeletion(t *testing.T) {
	cfg := hub.DefaultConfig()
	cfg.UnackedEventRetention = 30 * 24 * time.Hour
	h, _, clock := preferencesHubClock(t, cfg)
	old := retentionEvent(t, h, "peer_ack", clock.Now().Add(-2*24*time.Hour))
	retentionAck(t, h, "peer_ack", "*", old)
	cfg = h.Cfg
	cfg.AckedEventRetention = time.Hour
	shorter, err := hub.New(h.DB, cfg, clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	retentionRun(t, shorter)
	retentionFloor(t, shorter, "peer_ack", 0)
	// Merely changing provenance cannot bless the pending bootstrap for old rows.
	retentionConfirm(t, shorter, prefs.Patch{Set: map[string]prefs.Value{"retention.ack_events": prefs.Duration(0)}})
	retentionConfirm(t, shorter, prefs.Patch{Set: map[string]prefs.Value{"retention.ack_events": prefs.Duration(3600)}})
	retentionRun(t, shorter)
	retentionFloor(t, shorter, "peer_ack", 0)
	// A flag-only archive exit on restart cannot silently enable hard expiry.
	cfg.PreferenceBootstrap = map[string]prefs.Effective{"retention.archive_mode": {Value: prefs.Bool(true), Source: prefs.SourceFlag, Origin: "VECNET_RETENTION_ARCHIVE_MODE"}}
	archived, err := hub.New(h.DB, cfg, clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	retentionEvent(t, archived, "peer_unacked", clock.Now().Add(-40*24*time.Hour))
	cfg.PreferenceBootstrap = nil
	exited, err := hub.New(h.DB, cfg, clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	retentionRun(t, exited)
	retentionFloor(t, exited, "peer_unacked", 0)
}
func TestRetentionPolicyWritesSerializeWithDeletion(t *testing.T) {
	retentionPolicyWriterInterleaving(t)
}

func TestRetentionPurgeBackfillAndPushRace(t *testing.T) {
	env := testhub.StartWith(t, func(cfg *hub.Config) {
		cfg.BackfillEnabled = true
		cfg.BackfillCapacity = func(context.Context) (hub.BackfillCapacity, error) {
			return hub.BackfillCapacity{AvailableBytes: 1 << 40}, nil
		}
		cfg.BackfillCheckpoint = func(context.Context) (hub.BackfillCheckpoint, error) { return hub.BackfillCheckpoint{}, nil }
	})
	h := env.Hub
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_, as := env.NewPeer("session", "sender")
	p, _ := env.NewPeer("session", "recipient")
	callback := testhub.NewCallback(t)
	route, _, err := h.AddRoute(ctx, "peer", p, callback.URL)
	if err != nil {
		t.Fatal(err)
	}
	if err := env.Worker.Verify(ctx, route); err != nil {
		t.Fatal(err)
	}
	// Install a confirmed prospective 1h policy so this delivery is old
	// enough to prune while remaining within its original 24h TTL.
	retentionConfirm(t, h, prefs.Patch{Set: map[string]prefs.Value{"retention.ack_events": prefs.Duration(3600)}})
	m := mustSend(t, env, as, req("synthetic", p))
	var seq int64
	var frozen []byte
	if err := h.DB.QueryRow(`SELECT d.event_seq,e.data FROM deliveries d JOIN events e ON e.seq=d.event_seq WHERE d.msg_id=?`, m.MessageID).Scan(&seq, &frozen); err != nil {
		t.Fatal(err)
	}
	retentionAck(t, h, p, "*", eventlog.Cursor{Epoch: h.Log.Epoch(), Seq: seq})
	env.Now.Advance(2 * time.Hour)
	entered, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer unblock()
	callback.OnPost(func(_ int, w http.ResponseWriter) {
		close(entered)
		select {
		case <-release:
			w.WriteHeader(http.StatusOK)
		case <-ctx.Done():
			w.WriteHeader(http.StatusServiceUnavailable)
		}
	})
	type pushResult struct {
		sent int
		err  error
	}
	pushed := make(chan pushResult, 1)
	go func() { sent, err := env.Worker.Step(ctx); pushed <- pushResult{sent, err} }()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("queued delivery never reached callback")
	}
	// The push is now actually in flight. Backfill and pruning race while
	// its receiver holds the response, making the pin assertion deterministic.
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	wg.Go(func() { _, err := h.PruneEventSlice(ctx); errs <- err })
	wg.Go(func() { _, err := h.BackfillDeliverySlice(ctx, hub.BackfillLimits{Rows: 1}); errs <- err })
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	retentionFloor(t, h, p, 0)
	var retained int
	if err := h.DB.QueryRow(`SELECT count(*) FROM events WHERE seq=?`, seq).Scan(&retained); err != nil || retained != 1 {
		t.Fatal("in-flight push lost its frozen event")
	}
	posts := callback.Posts()
	if len(posts) != 1 || !bytes.Equal(posts[0].Body, frozen) {
		t.Fatal("callback did not receive the frozen delivery bytes")
	}
	unblock()
	select {
	case result := <-pushed:
		if result.err != nil || result.sent != 1 {
			t.Fatalf("actual pushes=%d err=%v", result.sent, result.err)
		}
	case <-ctx.Done():
		t.Fatal("push did not settle")
	}
	var state string
	if err := h.DB.QueryRow(`SELECT state FROM deliveries WHERE msg_id=?`, m.MessageID).Scan(&state); err != nil || state != "accepted" {
		t.Fatal("push did not accept the delivery")
	}
	retentionRun(t, h)
	retentionFloor(t, h, p, seq)
	var body string
	if err := h.DB.QueryRow(`SELECT body FROM messages WHERE msg_id=?`, m.MessageID).Scan(&body); err != nil || body == "" {
		t.Fatal("event GC removed history")
	}
}

func TestRetentionPreferenceWakeRefreshesStatusWithoutTrim(t *testing.T) {
	h, _, clock := preferencesHubClock(t, hub.DefaultConfig())
	c := retentionEvent(t, h, "peer_synthetic", clock.Now().Add(-2*time.Hour))
	retentionAck(t, h, "peer_synthetic", "*", c)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); h.RunTickers(ctx, time.Hour) }()
	stop := runtimeWorkerCleanup(t, cancel, done)
	defer stop()
	runtimeEventually(t, func() bool { return h.EventRetentionStatus().Policy.Ack.Source == prefs.SourceDefault })
	retentionConfirm(t, h, prefs.Patch{Set: map[string]prefs.Value{"retention.ack_events": prefs.Duration(3600)}, ApplyExisting: true})
	runtimeEventually(t, func() bool { return h.EventRetentionStatus().Policy.Revision == 1 })
	retentionFloor(t, h, "peer_synthetic", 0)
}

func TestRetentionPreviewCountsPinnedPrefix(t *testing.T) {
	env := testhub.Start(t)
	h := env.Hub
	_, as := env.NewPeer("session", "sender")
	p, _ := env.NewPeer("session", "recipient")
	m := mustSend(t, env, as, req("synthetic", p))
	var seq int64
	if err := h.DB.QueryRow(`SELECT event_seq FROM deliveries WHERE msg_id=?`, m.MessageID).Scan(&seq); err != nil {
		t.Fatal(err)
	}
	// Keep the queued delivery pinned while acknowledging its log directly.
	retentionAck(t, h, p, "*", eventlog.Cursor{Epoch: h.Log.Epoch(), Seq: seq})
	env.Now.Advance(8 * 24 * time.Hour)
	s := readPrefs(t, h)
	v := previewPrefs(t, h.Preferences, prefsOperator, s, prefs.Patch{Set: map[string]prefs.Value{"retention.ack_events": prefs.Duration(3600)}, ApplyExisting: true})
	if v.Impact.Pinned == nil || *v.Impact.Pinned != 1 || v.Impact.Eligible == nil || *v.Impact.Eligible != 0 {
		t.Fatalf("preview must distinguish pins from eligible prefixes: %+v", v.Impact)
	}
	retentionFloor(t, h, p, 0)
}

func TestRetentionMixedNoopArchiveDoesNotBlockHardExpiry(t *testing.T) {
	cfg := hub.DefaultConfig()
	cfg.UnackedEventRetention = 30 * 24 * time.Hour
	h, _, clock := preferencesHubClock(t, cfg)
	// First authorize this hard class; the later mixed no-op must preserve it.
	retentionConfirm(t, h, prefs.Patch{Set: map[string]prefs.Value{"retention.archive_mode": prefs.Bool(false)}, ApplyExisting: true})
	c := retentionEvent(t, h, "peer_synthetic", clock.Now().Add(-40*24*time.Hour))
	retentionConfirm(t, h, prefs.Patch{Set: map[string]prefs.Value{"retention.archive_mode": prefs.Bool(false), "agents.thread_budget": prefs.Int(600)}})
	retentionRun(t, h)
	retentionFloor(t, h, "peer_synthetic", c.Seq)
}
func TestRetentionArchiveExitPreviewIncludesHardUnackedExpiry(t *testing.T) {
	cfg := hub.DefaultConfig()
	cfg.UnackedEventRetention = 30 * 24 * time.Hour
	h, _, clock := preferencesHubClock(t, cfg)
	retentionConfirm(t, h, prefs.Patch{Set: map[string]prefs.Value{"retention.archive_mode": prefs.Bool(true)}})
	retentionEvent(t, h, "peer_synthetic", clock.Now().Add(-40*24*time.Hour))
	s := readPrefs(t, h)
	v := previewPrefs(t, h.Preferences, prefsOperator, s, prefs.Patch{Set: map[string]prefs.Value{"retention.archive_mode": prefs.Bool(false)}, ApplyExisting: true})
	if v.Impact.Eligible == nil || *v.Impact.Eligible != 1 {
		t.Fatalf("archive exit preview omitted eligible hard-unacked prefix: %+v", v.Impact)
	}
	retentionFloor(t, h, "peer_synthetic", 0)
}
