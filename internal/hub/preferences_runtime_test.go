package hub_test

import (
	"context"
	"fmt"
	"net/http"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/events"
	"github.com/XertroV/vecnet/internal/hub"
	"github.com/XertroV/vecnet/internal/opapi"
	"github.com/XertroV/vecnet/internal/opview"
	"github.com/XertroV/vecnet/internal/prefs"
	"github.com/XertroV/vecnet/internal/testhub"
	"github.com/XertroV/vecnet/internal/webhook"
)

func runtimePatch(t *testing.T, h *hub.Hub, values map[string]prefs.Value) {
	t.Helper()
	s := readPrefs(t, h)
	p := prefs.Patch{Set: values}
	preview, err := h.Preferences.PreviewPreferences(context.Background(), prefsOperator, s.ETag, p)
	if err != nil {
		t.Fatal(err)
	}
	if len(preview.Risks) > 0 {
		p.Confirm = []string{preview.ConfirmToken}
	}
	patchPrefs(t, h, s, p)
}

// A stale deadline cannot satisfy a live publication barrier.
func runtimeDeadlineInWindow(at *time.Time, now time.Time, cadence time.Duration) bool {
	return at != nil && !at.Before(now.Add(cadence)) && !at.After(now.Add(cadence+30*time.Second))
}

func runtimeEventually(t *testing.T, check func() bool) {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	poll := time.NewTicker(time.Millisecond)
	defer poll.Stop()
	for !check() {
		select {
		case <-deadline.C:
			t.Fatal("runtime consumer did not converge")
		case <-poll.C:
		}
	}
}

// Register before launching workers so every Fatal path cancels and joins.
// Closed completion channels make the explicit join and cleanup idempotent.
func runtimeWorkerCleanup(t *testing.T, cancel context.CancelFunc, done ...<-chan struct{}) func() {
	t.Helper()
	stop := func() {
		t.Helper()
		cancel()
		timer := time.NewTimer(5 * time.Second)
		defer timer.Stop()
		for _, ch := range done {
			select {
			case <-ch:
			case <-timer.C:
				t.Error("runtime worker did not stop on cancellation")
				return
			}
		}
	}
	t.Cleanup(stop)
	return stop
}

func TestPreferencesRuntimePresenceChangesWithoutRestart(t *testing.T) {
	h, _, clock := preferencesHubClock(t, hub.DefaultConfig())
	state, at := "working", envelope.FormatTime(clock.Now())
	clock.Advance(20 * time.Minute)
	for _, kind := range []string{"session", "launcher", "pagchat-bot"} {
		key := "presence.default_ttl"
		if kind == "session" || kind == "launcher" {
			key = "presence." + kind + "_ttl"
		}
		runtimePatch(t, h, map[string]prefs.Value{key: prefs.Duration(3600)})
		if got := h.EffectivePresence(hub.PresenceRow{Kind: kind, State: &state, At: &at}, clock.Now()); got != "working" {
			t.Fatalf("%s presence after lengthening = %s", kind, got)
		}
		runtimePatch(t, h, map[string]prefs.Value{key: prefs.Duration(60)})
		if got := h.EffectivePresence(hub.PresenceRow{Kind: kind, State: &state, At: &at}, clock.Now()); got != "unknown" {
			t.Fatalf("%s presence after shortening = %s", kind, got)
		}
	}
	if h.Cfg.SessionPresenceTTL != 15*time.Minute || h.Cfg.PresenceTTL != time.Hour {
		t.Fatal("runtime patch mutated bootstrap configuration")
	}
}

func TestPreferencesRuntimeRequestDefaultsRemainFrozen(t *testing.T) {
	env := testhub.Start(t)
	_, secret := env.NewPeer("session", "A")
	b, bSecret := env.NewPeer("session", "B")
	old := mustSend(t, env, secret, req("before", b))
	oldLoop := storedLoop(t, env, old.MessageID)
	runtimePatch(t, env.Hub, map[string]prefs.Value{
		"agents.default_max_depth": prefs.Int(7), "agents.default_ttl": prefs.Duration(600), "agents.thread_budget": prefs.Int(9),
	})
	fresh := mustSend(t, env, secret, req("after", b))
	l := storedLoop(t, env, fresh.MessageID)
	if l.MaxDepth != 7 || l.ExpiresAt != envelope.FormatTime(env.Now.Now().Add(10*time.Minute)) {
		t.Fatalf("new message defaults = %+v", l)
	}
	var budget int
	if err := env.Hub.DB.QueryRow(`SELECT budget_remaining FROM threads WHERE thr_id=?`, fresh.ThreadID).Scan(&budget); err != nil || budget != 8 {
		t.Fatalf("new budget = %d, %v; want 8", budget, err)
	}
	if got := storedLoop(t, env, old.MessageID); !reflect.DeepEqual(got, oldLoop) {
		t.Fatal("existing message changed")
	}
	if err := env.Hub.DB.QueryRow(`SELECT budget_remaining FROM threads WHERE thr_id=?`, old.ThreadID).Scan(&budget); err != nil || budget != 499 {
		t.Fatalf("existing budget changed: %d %v", budget, err)
	}
	// A new reply takes the new TTL default, clamped to its parent's deadline,
	// while max_depth and budget continue to be inherited from the old thread.
	reply := mustSend(t, env, bSecret, replyReq("reply", old.ThreadID, old.MessageID))
	if got := storedLoop(t, env, reply.MessageID); got.MaxDepth != 32 || got.ExpiresAt != l.ExpiresAt || got.Depth != 1 {
		t.Fatalf("inherited reply = %+v", got)
	}
}

func TestPreferencesRuntimeConcurrentRequestDefaults(t *testing.T) {
	env := testhub.Start(t)
	_, secret := env.NewPeer("session", "A")
	b, _ := env.NewPeer("session", "B")
	set := func(n int64) map[string]prefs.Value {
		return map[string]prefs.Value{"agents.default_max_depth": prefs.Int(n), "agents.default_ttl": prefs.Duration(n * 60), "agents.thread_budget": prefs.Int(n)}
	}
	runtimePatch(t, env.Hub, set(7))
	start := make(chan struct{})
	errs := make(chan error, 1)
	var wg sync.WaitGroup
	wg.Go(func() {
		<-start
		for i := 0; i < 20; i++ {
			s, err := env.Hub.Preferences.LoadSnapshot(context.Background(), nil)
			if err == nil {
				_, err = env.Hub.Preferences.PatchPreferences(context.Background(), prefsOperator, s.ETag, prefs.Patch{Set: set(int64(7 + i%2))})
			}
			if err != nil {
				errs <- err
				return
			}
		}
	})
	close(start)
	for i := 0; i < 20; i++ {
		m := mustSend(t, env, secret, req(fmt.Sprintf("snapshot-%d", i), b))
		l := storedLoop(t, env, m.MessageID)
		var budget int
		if err := env.Hub.DB.QueryRow(`SELECT budget_remaining FROM threads WHERE thr_id=?`, m.ThreadID).Scan(&budget); err != nil {
			t.Fatal(err)
		}
		if (l.MaxDepth != 7 && l.MaxDepth != 8) || budget != l.MaxDepth-1 || l.ExpiresAt != envelope.FormatTime(env.Now.Now().Add(time.Duration(l.MaxDepth)*time.Minute)) {
			t.Errorf("mixed operation snapshot: loop=%+v budget=%d", l, budget)
		}
	}
	wg.Wait()
	select {
	case err := <-errs:
		t.Fatal(err)
	default:
	}
}

func TestPreferencesRuntimeNewPeerRatesAndPrincipalPolicies(t *testing.T) {
	env, caller, _ := provisionSetup(t, 10, 2)
	old, _ := env.NewPeer("session", "old")
	oldDefault, _ := env.NewPeer("session", "old-default")
	if _, err := env.Hub.DB.Exec(`UPDATE peers SET sends_per_min=13,threads_per_hour=14 WHERE peer_id=?`, old); err != nil {
		t.Fatal(err)
	}
	other, otherSecret := env.NewPrincipal("default-off")
	runtimePatch(t, env.Hub, map[string]prefs.Value{
		"rates.sends_per_minute": prefs.Int(17), "rates.threads_per_hour": prefs.Int(18), "provisioning.creates_per_hour": prefs.Int(3),
	})
	fresh, _ := env.NewPeer("session", "new")
	created := createProvisioned(t, env, caller, "new-rates")
	for _, peer := range []string{fresh, created.Peer.PeerID, old, oldDefault} {
		var sends, threads int
		if err := env.Hub.DB.QueryRow(`SELECT sends_per_min,threads_per_hour FROM peers WHERE peer_id=?`, peer).Scan(&sends, &threads); err != nil {
			t.Fatal(err)
		}
		wantSends, wantThreads := 17, 18
		if peer == old {
			wantSends, wantThreads = 13, 14
		} else if peer == oldDefault {
			wantSends, wantThreads = 60, 20
		}
		if sends != wantSends || threads != wantThreads {
			t.Errorf("peer rates = %d/%d, want %d/%d", sends, threads, wantSends, wantThreads)
		}
	}
	principals, err := env.Hub.Principals(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range principals {
		if p.PrincipalID == other && (p.Limits.CreatesPerHour != 3 || p.Limits.MaxPeers != 0 || len(p.Limits.Kinds) != 0) {
			t.Errorf("fallback policy widened authority or missed rate: %+v", p.Limits)
		}
		if p.PrincipalID == caller.PrincipalID && (p.Limits.CreatesPerHour != 2 || p.Limits.MaxPeers != 10 || !reflect.DeepEqual(p.Limits.Kinds, []string{"pagchat-bot"})) {
			t.Errorf("explicit principal policy changed: %+v", p.Limits)
		}
	}
	createProvisioned(t, env, caller, "second")
	_, err = env.Hub.CreatePeer(context.Background(), caller, hub.CreatePeerRequest{Kind: "pagchat-bot", Name: "third", IdempotencyKey: "third"})
	provisionError(t, err, 429, "peer_create_rate_limited")
	_, err = env.Hub.CreatePeer(context.Background(), provisionCaller(t, env, otherSecret), hub.CreatePeerRequest{Kind: "pagchat-bot", Name: "blocked", IdempotencyKey: "blocked"})
	provisionError(t, err, 403, "kind_not_allowed")
}

func runtimeSubscription(t *testing.T, env *testhub.Env, identity ...string) events.Subscription {
	t.Helper()
	args := map[string]any{}
	if len(identity) > 0 {
		args["fixture"] = identity[0]
	}
	s, _, err := env.Events.Store.Upsert(context.Background(), events.Subscription{
		PrincipalKind: events.KindVecnet, PrincipalID: env.OperatorPeer, PeerID: env.OperatorPeer,
		Name: events.Name, URL: "https://example.invalid/callback", Arguments: args,
	}, webhook.NewSecret(), 6*time.Hour, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	effective, err := env.Hub.Preferences.Current().Get("events.refresh_interval")
	if err != nil {
		t.Fatal(err)
	}
	n, _ := effective.Value.Int64()
	if s.NextRefreshBefore == nil || !s.NextRefreshBefore.Equal(env.Now.Now().Add(time.Duration(n)*time.Second)) {
		t.Fatal("Upsert did not apply explicit refresh policy", err)
	}
	// Match the successful subscribe path, which alone saves client advice.
	if err := env.Events.Store.SetRefreshBefore(context.Background(), s.ID, env.Now.Now().Add(30*time.Minute)); err != nil {
		t.Fatal(err)
	}
	got, err := env.Events.Store.Get(context.Background(), s.ID)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestPreferencesRuntimeRefreshCoalescesWithoutLeaseOrCatchup(t *testing.T) {
	env := testhub.Start(t)
	before := runtimeSubscription(t, env)
	for _, n := range []int64{600, 900, 1200} {
		runtimePatch(t, env.Hub, map[string]prefs.Value{"events.refresh_interval": prefs.Duration(n)})
	}
	if _, err := env.Events.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	after, err := env.Events.Store.Get(context.Background(), before.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.NextRefreshBefore == nil || after.NextRefreshBefore.Before(env.Now.Now().Add(20*time.Minute)) || after.NextRefreshBefore.After(env.Now.Now().Add(21*time.Minute)) {
		t.Fatalf("latest refresh schedule = %v, want next 20 minutes with bounded staggering", after.NextRefreshBefore)
	}
	if !reflect.DeepEqual(after.RefreshBefore, before.RefreshBefore) {
		t.Fatal("worker cadence update changed saved client advice")
	}
	if !reflect.DeepEqual(after.Expires, before.Expires) || !reflect.DeepEqual(after.TokenExpires, before.TokenExpires) || after.Cursor != before.Cursor || after.Verified != before.Verified {
		t.Fatal("rescheduling changed lease, token, cursor or verification")
	}
	deadline := *after.NextRefreshBefore
	env.Now.Advance(time.Minute)
	if _, err := env.Events.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	again, err := env.Events.Store.Get(context.Background(), before.ID)
	if err != nil || again.NextRefreshBefore == nil || !again.NextRefreshBefore.Equal(deadline) {
		t.Fatal("unchanged generation slid the refresh deadline", err)
	}
	// The worker uses the stored cadence independently of the exact advice
	// returned by the successful subscribe caller.
	fresh := runtimeSubscription(t, env, "fresh")
	if fresh.ID == before.ID {
		t.Fatal("new-subscription acceptance reused the existing subscription")
	}
	if fresh.RefreshBefore == nil || !fresh.RefreshBefore.Equal(env.Now.Now().Add(30*time.Minute)) {
		t.Fatalf("saved advice differs from successful subscribe advice: %v", fresh.RefreshBefore)
	}
	if fresh.NextRefreshBefore == nil || !fresh.NextRefreshBefore.Equal(env.Now.Now().Add(20*time.Minute)) {
		t.Fatalf("new subscription ignored stored worker cadence: %v", fresh.NextRefreshBefore)
	}
	advice := env.Now.Now().Add(25 * time.Minute)
	if err := env.Events.Store.SetRefreshBefore(context.Background(), fresh.ID, advice); err != nil {
		t.Fatal(err)
	}
	got, err := env.Events.Store.Get(context.Background(), fresh.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.RefreshBefore == nil || !got.RefreshBefore.Equal(advice) {
		t.Fatalf("deadline setter changed supplied client advice: %v, want %v", got.RefreshBefore, advice)
	}
	if got.NextRefreshBefore == nil || !got.NextRefreshBefore.Equal(*fresh.NextRefreshBefore) {
		t.Fatalf("client advice changed worker cadence: %v, want %v", got.NextRefreshBefore, fresh.NextRefreshBefore)
	}
}

func TestPreferencesRuntimeRefreshRecoversMissedReset(t *testing.T) {
	env := testhub.Start(t)
	sub := runtimeSubscription(t, env)
	runtimePatch(t, env.Hub, map[string]prefs.Value{"events.refresh_interval": prefs.Duration(600)})
	if _, err := env.Events.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	patchPrefs(t, env.Hub, readPrefs(t, env.Hub), prefs.Patch{Reset: []string{"events.refresh_interval"}})
	// A fresh worker has missed the reset publication, as after a crash between
	// preference commit and consumer application. It must load the default too.
	restarted := &events.Worker{Hub: env.Hub}
	if _, err := restarted.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, err := env.Events.Store.Get(context.Background(), sub.ID)
	if err != nil || got.NextRefreshBefore == nil || got.NextRefreshBefore.Before(env.Now.Now().Add(30*time.Minute)) || got.NextRefreshBefore.After(env.Now.Now().Add(31*time.Minute)) {
		t.Fatalf("missed reset left stale refresh policy: %v %v", got.NextRefreshBefore, err)
	}
}

func TestPreferencesRuntimeUnchangedRefreshAdviceSurvivesWorkerStart(t *testing.T) {
	env := testhub.Start(t)
	sub := runtimeSubscription(t, env)
	env.Now.Advance(5 * time.Minute)
	restarted := &events.Worker{Hub: env.Hub}
	if _, err := restarted.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, err := env.Events.Store.Get(context.Background(), sub.ID)
	if err != nil || got.NextRefreshBefore == nil || !reflect.DeepEqual(got.NextRefreshBefore, sub.NextRefreshBefore) {
		t.Fatal("unchanged refresh advice slid on worker start", err)
	}
}

// Both reset and a delayed consumer application used to lose their scheduling
// anchor on restart, moving an already applied deadline forward each time.
func TestPreferencesRuntimeAppliedRefreshSurvivesRestart(t *testing.T) {
	for _, reset := range []bool{false, true} {
		t.Run(fmt.Sprintf("reset=%t", reset), func(t *testing.T) {
			env := testhub.Start(t)
			sub := runtimeSubscription(t, env)
			env.Now.Advance(5 * time.Minute)
			runtimePatch(t, env.Hub, map[string]prefs.Value{"events.refresh_interval": prefs.Duration(600)})
			if reset {
				if _, err := env.Events.Step(context.Background()); err != nil {
					t.Fatal(err)
				}
				env.Now.Advance(5 * time.Minute)
				patchPrefs(t, env.Hub, readPrefs(t, env.Hub), prefs.Patch{Reset: []string{"events.refresh_interval"}})
			} else {
				// The worker observes the committed change seven minutes late.
				env.Now.Advance(7 * time.Minute)
			}
			if _, err := env.Events.Step(context.Background()); err != nil {
				t.Fatal(err)
			}
			applied, err := env.Events.Store.Get(context.Background(), sub.ID)
			if err != nil || applied.NextRefreshBefore == nil {
				t.Fatal("missing applied advice", err)
			}
			for i := 0; i < 2; i++ {
				env.Now.Advance(5 * time.Minute)
				// Rebuild the hub and worker over the same durable store, so no
				// publication or process-local scheduling cache survives.
				h, err := hub.New(env.Hub.DB, env.Hub.Cfg, env.Now.Now)
				if err != nil {
					t.Fatal(err)
				}
				restarted := &events.Worker{Hub: h}
				if _, err := restarted.Step(context.Background()); err != nil {
					t.Fatal(err)
				}
				got, err := restarted.Store.Get(context.Background(), sub.ID)
				if err != nil || got.NextRefreshBefore == nil || !got.NextRefreshBefore.Equal(*applied.NextRefreshBefore) {
					t.Fatalf("restart %d slid applied advice from %v to %v: %v", i+1, applied.NextRefreshBefore, got.NextRefreshBefore, err)
				}
			}
		})
	}
}

func TestPreferencesRuntimeRefreshCheckpointIsAtomic(t *testing.T) {
	env := testhub.Start(t)
	sub := runtimeSubscription(t, env)
	if _, err := env.Events.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	env.Now.Advance(5 * time.Minute)
	runtimePatch(t, env.Hub, map[string]prefs.Value{"events.refresh_interval": prefs.Duration(600)})
	// Abort the checkpoint write after deadlines have been updated. A failed
	// application must roll back both, and a new worker must retry the change.
	if _, err := env.Hub.DB.Exec(`CREATE TRIGGER fail_refresh_checkpoint BEFORE INSERT ON meta
		WHEN NEW.key = 'events_refresh_schedule_policy'
		BEGIN SELECT RAISE(ABORT, 'fixture checkpoint failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := env.Events.Step(context.Background()); err == nil {
		t.Fatal("checkpoint failure did not abort refresh application")
	}
	got, err := env.Events.Store.Get(context.Background(), sub.ID)
	if err != nil || got.NextRefreshBefore == nil || !got.NextRefreshBefore.Equal(*sub.NextRefreshBefore) {
		t.Fatal("checkpoint failure committed a partial schedule", err)
	}
	if _, err := env.Hub.DB.Exec(`DROP TRIGGER fail_refresh_checkpoint`); err != nil {
		t.Fatal(err)
	}
	env.Now.Advance(5 * time.Minute)
	restarted := &events.Worker{Hub: env.Hub}
	if _, err := restarted.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, err = restarted.Store.Get(context.Background(), sub.ID)
	if err != nil || got.NextRefreshBefore == nil || got.NextRefreshBefore.Before(env.Now.Now().Add(10*time.Minute)) || got.NextRefreshBefore.After(env.Now.Now().Add(11*time.Minute)) {
		t.Fatal("new worker did not recover the rolled-back schedule", err)
	}
}

func TestPreferencesRuntimeWakeDoesNotDispatchOrResetBackoff(t *testing.T) {
	env := testhub.Start(t)
	peer, _ := env.NewPeer("session", "subscriber")
	cb := testhub.NewCallback(t)
	cb.OnPost(func(_ int, w http.ResponseWriter) { w.WriteHeader(http.StatusServiceUnavailable) })
	cur, err := env.Hub.Log.Current(context.Background(), []string{peer})
	if err != nil {
		t.Fatal(err)
	}
	sub, _, err := env.Events.Store.Upsert(context.Background(), events.Subscription{
		PrincipalKind: events.KindVecnet, PrincipalID: peer, PeerID: peer,
		Name: events.Name, URL: cb.URL, Cursor: cur,
	}, webhook.NewSecret(), 6*time.Hour, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if err := env.Events.Store.MarkVerified(context.Background(), sub.ID); err != nil {
		t.Fatal(err)
	}
	mustSend(t, env, env.OperatorSecret, req("backoff", peer))
	if _, err := env.Events.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	before, err := env.Events.Store.ListHealth(context.Background())
	if err != nil || len(before) != 1 || before[0].Attempts != 1 || cb.Requests() != 1 {
		t.Fatal("did not establish callback retry backoff", err)
	}
	var subRetry, outboxRetry string
	if err := env.Hub.DB.QueryRow(`SELECT next_attempt_at FROM event_subscriptions WHERE sub_id=?`, sub.ID).Scan(&subRetry); err != nil {
		t.Fatal(err)
	}
	if err := env.Hub.DB.QueryRow(`SELECT next_attempt_at FROM event_outbox WHERE sub_id=? AND done_at IS NULL`, sub.ID).Scan(&outboxRetry); err != nil {
		t.Fatal(err)
	}
	// The retry is due on the fake clock, but advice publication must not
	// dispatch it or tail the additional event before the regular worker tick.
	env.Now.Advance(time.Hour)
	mustSend(t, env, env.OperatorSecret, req("still-queued", peer))
	// Make startup observable independently of the subsequent wake. The old
	// deadline is already in the past, so every acceptance window is future-only.
	runtimePatch(t, env.Hub, map[string]prefs.Value{"events.refresh_interval": prefs.Duration(1200)})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	env.Events.Interval = time.Hour
	stop := runtimeWorkerCleanup(t, cancel, done)
	go func() { _ = env.Events.Run(ctx); close(done) }()
	runtimeEventually(t, func() bool {
		got, err := env.Events.Store.Get(context.Background(), sub.ID)
		return err == nil && runtimeDeadlineInWindow(got.NextRefreshBefore, env.Now.Now(), 20*time.Minute)
	})
	initial, err := env.Events.Store.Get(context.Background(), sub.ID)
	if err != nil {
		t.Fatal(err)
	}
	runtimePatch(t, env.Hub, map[string]prefs.Value{"events.refresh_interval": prefs.Duration(600)})
	runtimeEventually(t, func() bool {
		got, err := env.Events.Store.Get(context.Background(), sub.ID)
		return err == nil && runtimeDeadlineInWindow(got.NextRefreshBefore, env.Now.Now(), 10*time.Minute) && !got.NextRefreshBefore.Equal(*initial.NextRefreshBefore)
	})
	stop()
	after, err := env.Events.Store.ListHealth(context.Background())
	if err != nil || len(after) != 1 {
		t.Fatal("health after publication", err)
	}
	var afterSubRetry, afterOutboxRetry string
	if err := env.Hub.DB.QueryRow(`SELECT next_attempt_at FROM event_subscriptions WHERE sub_id=?`, sub.ID).Scan(&afterSubRetry); err != nil {
		t.Fatal(err)
	}
	if err := env.Hub.DB.QueryRow(`SELECT next_attempt_at FROM event_outbox WHERE sub_id=? AND done_at IS NULL`, sub.ID).Scan(&afterOutboxRetry); err != nil {
		t.Fatal(err)
	}
	if afterSubRetry != subRetry || afterOutboxRetry != outboxRetry {
		t.Fatal("preference wake changed persisted callback backoff")
	}
	if cb.Requests() != 1 || after[0].Attempts != before[0].Attempts || after[0].Cursor != before[0].Cursor || !reflect.DeepEqual(after[0].LastDeliveryAt, before[0].LastDeliveryAt) || !reflect.DeepEqual(after[0].ExpiresAt, before[0].ExpiresAt) {
		t.Fatal("preference wake dispatched or changed retry/cursor/lease state")
	}
}

func TestPreferencesRuntimeWorkersApplyPublicationAndCancel(t *testing.T) {
	env := testhub.Start(t)
	sub := runtimeSubscription(t, env)
	// Startup must release this independently stale delivery before the later
	// revision is published. A second recipient remains held until that wake.
	stale, staleSecret := env.NewPeer("session", "startup-stale")
	if err := env.Hub.SetPresence(context.Background(), provisionCaller(t, env, staleSecret), "", hub.PresenceReport{State: "working"}); err != nil {
		t.Fatal(err)
	}
	startupBody := req("startup-held", stale)
	startupBody["priority"] = "wake_idle"
	startup := mustSend(t, env, env.OperatorSecret, startupBody)
	env.Now.Advance(20 * time.Minute)
	runtimePatch(t, env.Hub, map[string]prefs.Value{"events.refresh_interval": prefs.Duration(1200)})
	peer, secret := env.NewPeer("session", "working")
	caller := provisionCaller(t, env, secret)
	if err := env.Hub.SetPresence(context.Background(), caller, "", hub.PresenceReport{State: "working"}); err != nil {
		t.Fatal(err)
	}
	body := req("held", peer)
	body["priority"] = "wake_idle"
	msg := mustSend(t, env, env.OperatorSecret, body)
	env.Now.Advance(2 * time.Minute)
	ctx, cancel := context.WithCancel(context.Background())
	eventsDone, tickersDone := make(chan struct{}), make(chan struct{})
	stop := runtimeWorkerCleanup(t, cancel, eventsDone, tickersDone)
	env.Events.Interval = time.Hour
	go func() { _ = env.Events.Run(ctx); close(eventsDone) }()
	go func() { env.Hub.RunTickers(ctx, time.Hour); close(tickersDone) }()
	// Committed effects form a barrier after each worker's initial pass;
	// merely registering Subscribe would permit startup to consume our patch.
	runtimeEventually(t, func() bool {
		got, err := env.Events.Store.Get(context.Background(), sub.ID)
		var held int
		err2 := env.Hub.DB.QueryRow(`SELECT held FROM deliveries WHERE msg_id=? AND recipient=?`, startup.MessageID, stale).Scan(&held)
		return err == nil && err2 == nil && held == 0 && runtimeDeadlineInWindow(got.NextRefreshBefore, env.Now.Now(), 20*time.Minute)
	})
	initial, err := env.Events.Store.Get(context.Background(), sub.ID)
	if err != nil {
		t.Fatal(err)
	}
	var held int
	if err := env.Hub.DB.QueryRow(`SELECT held FROM deliveries WHERE msg_id=? AND recipient=?`, msg.MessageID, peer).Scan(&held); err != nil || held != 1 {
		t.Fatal("startup released the wake fixture", err)
	}
	runtimePatch(t, env.Hub, map[string]prefs.Value{"presence.session_ttl": prefs.Duration(60), "events.refresh_interval": prefs.Duration(600)})
	runtimeEventually(t, func() bool {
		got, err := env.Events.Store.Get(context.Background(), sub.ID)
		var held int
		err2 := env.Hub.DB.QueryRow(`SELECT held FROM deliveries WHERE msg_id=? AND recipient=?`, msg.MessageID, peer).Scan(&held)
		return err == nil && err2 == nil && held == 0 && runtimeDeadlineInWindow(got.NextRefreshBefore, env.Now.Now(), 10*time.Minute) && !got.NextRefreshBefore.Equal(*initial.NextRefreshBefore)
	})
	stop()
	runtimeEventually(t, func() bool { return env.Hub.Preferences.SubscriberCount() == 0 })
	// Publications after worker shutdown cannot revive a worker or hit a closed channel.
	runtimePatch(t, env.Hub, map[string]prefs.Value{"events.refresh_interval": prefs.Duration(700)})
	if n := env.Hub.Preferences.SubscriberCount(); n != 0 {
		t.Fatalf("worker subscriptions leaked: %d", n)
	}
}

func TestPreferencesRuntimeCanceledWorkersDoNotRun(t *testing.T) {
	env := testhub.Start(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan struct{}, 2)
	go func() { _ = env.Events.Run(ctx); done <- struct{}{} }()
	go func() { env.Hub.RunTickers(ctx, time.Hour); done <- struct{}{} }()
	for range 2 {
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("already canceled worker blocked")
		}
	}
	runtimeEventually(t, func() bool { return env.Hub.Preferences.SubscriberCount() == 0 })
}

func TestPreferencesRuntimePublicationRacesWorkerCancellation(t *testing.T) {
	env := testhub.Start(t)
	env.Events.Interval = time.Hour
	for i := 0; i < 8; i++ {
		ctx, cancel := context.WithCancel(context.Background())
		eventsDone, tickersDone := make(chan struct{}), make(chan struct{})
		stop := runtimeWorkerCleanup(t, cancel, eventsDone, tickersDone)
		go func() { _ = env.Events.Run(ctx); close(eventsDone) }()
		go func() { env.Hub.RunTickers(ctx, time.Hour); close(tickersDone) }()
		runtimeEventually(t, func() bool { return env.Hub.Preferences.SubscriberCount() == 2 })
		s := readPrefs(t, env.Hub)
		written := make(chan error, 1)
		go func() {
			_, err := env.Hub.Preferences.PatchPreferences(context.Background(), prefsOperator, s.ETag, prefs.Patch{Set: map[string]prefs.Value{"events.refresh_interval": prefs.Duration(int64(600 + i))}})
			written <- err
		}()
		stop()
		select {
		case err := <-written:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("publication/cancel race stranded the preference writer")
		}
		runtimeEventually(t, func() bool { return env.Hub.Preferences.SubscriberCount() == 0 })
	}
}

// Every subscribe path can materialize policy B after the worker checkpoints A.
// Resetting to A before the next pass must reconcile those rows individually.
func TestPreferencesRuntimeRefreshCoalescedResetReconcilesSubscriptions(t *testing.T) {
	for _, path := range []string{"create", "refresh", "set"} {
		t.Run(path, func(t *testing.T) {
			env := testhub.Start(t)
			sub := runtimeSubscription(t, env)
			untouched := runtimeSubscription(t, env, "untouched-A")
			if _, err := env.Events.Step(context.Background()); err != nil {
				t.Fatal(err)
			}
			runtimePatch(t, env.Hub, map[string]prefs.Value{"events.refresh_interval": prefs.Duration(3600)})
			switch path {
			case "create":
				sub = runtimeSubscription(t, env, "under-B")
			case "refresh":
				sub = runtimeSubscription(t, env)
			case "set":
				if err := env.Events.Store.SetRefreshBefore(context.Background(), sub.ID, env.Now.Now().Add(time.Hour)); err != nil {
					t.Fatal(err)
				}
			}
			patchPrefs(t, env.Hub, readPrefs(t, env.Hub), prefs.Patch{Reset: []string{"events.refresh_interval"}})
			var applied time.Time
			for i := 0; i < 3; i++ {
				if i > 0 {
					env.Now.Advance(5 * time.Minute)
				}
				h, err := hub.New(env.Hub.DB, env.Hub.Cfg, env.Now.Now)
				if err != nil {
					t.Fatal(err)
				}
				worker := &events.Worker{Hub: h}
				if _, err := worker.Step(context.Background()); err != nil {
					t.Fatal(err)
				}
				stable, err := worker.Store.Get(context.Background(), untouched.ID)
				if err != nil || !reflect.DeepEqual(stable.NextRefreshBefore, untouched.NextRefreshBefore) {
					t.Fatal("reconciliation slid an already-correct row", err)
				}
				got, err := worker.Store.Get(context.Background(), sub.ID)
				if err != nil || got.NextRefreshBefore == nil {
					t.Fatal("missing schedule", err)
				}
				if i == 0 {
					applied = *got.NextRefreshBefore
					if applied.Before(env.Now.Now().Add(30*time.Minute)) || applied.After(env.Now.Now().Add(31*time.Minute)) {
						t.Fatalf("coalesced reset retained B schedule: %v", applied)
					}
				} else if !got.NextRefreshBefore.Equal(applied) {
					t.Fatalf("restart slid corrected schedule: %v -> %v", applied, got.NextRefreshBefore)
				}
			}
		})
	}
}

func TestPreferencesRuntimeRefreshPreservesClientDeadline(t *testing.T) {
	for _, saved := range []bool{false, true} {
		t.Run(fmt.Sprintf("saved=%t", saved), func(t *testing.T) {
			env := testhub.Start(t)
			sub, _, err := env.Events.Store.Upsert(context.Background(), events.Subscription{
				PrincipalKind: events.KindVecnet, PrincipalID: env.OperatorPeer, PeerID: env.OperatorPeer,
				Name: events.Name, URL: "https://example.invalid/callback",
			}, webhook.NewSecret(), 6*time.Hour, time.Time{})
			if err != nil {
				t.Fatal(err)
			}
			if saved {
				sub = runtimeSubscription(t, env)
			}
			runtimePatch(t, env.Hub, map[string]prefs.Value{"events.refresh_interval": prefs.Duration(600)})
			if _, err := env.Events.Step(context.Background()); err != nil {
				t.Fatal(err)
			}
			got, err := env.Events.Store.Get(context.Background(), sub.ID)
			if err != nil || !reflect.DeepEqual(got.RefreshBefore, sub.RefreshBefore) {
				t.Fatalf("policy alone changed client advice: %v -> %v (%v)", sub.RefreshBefore, got.RefreshBefore, err)
			}
			health, err := env.Events.Store.ListHealth(context.Background())
			if err != nil || len(health) != 1 {
				t.Fatal("health", err)
			}
			if sub.RefreshBefore == nil && health[0].RefreshBefore != nil || sub.RefreshBefore != nil && (health[0].RefreshBefore == nil || *health[0].RefreshBefore != envelope.FormatTime(*sub.RefreshBefore)) {
				t.Fatal("status lost last successful subscribe advice")
			}
			views, err := opview.New(env.Hub).Subscriptions(context.Background())
			want := sub.Expires.Add(-events.RefreshMargin)
			if sub.RefreshBefore != nil {
				want = *sub.RefreshBefore
			}
			if err != nil || len(views) != 1 || views[0].RefreshBefore == nil || *views[0].RefreshBefore != envelope.FormatTime(want) {
				t.Fatal("status display changed saved advice or legacy expiry-margin fallback", err)
			}
			if !runtimeDeadlineInWindow(got.NextRefreshBefore, env.Now.Now(), 10*time.Minute) {
				t.Fatal("worker schedule did not adopt new policy")
			}
			refreshed := runtimeSubscription(t, env)
			if refreshed.RefreshBefore == nil || !refreshed.RefreshBefore.Equal(env.Now.Now().Add(30*time.Minute)) {
				t.Fatal("re-subscribe did not save the supplied client advice")
			}
			if refreshed.NextRefreshBefore == nil || !refreshed.NextRefreshBefore.Equal(env.Now.Now().Add(10*time.Minute)) {
				t.Fatal("re-subscribe did not preserve stored worker cadence")
			}
			health, err = env.Events.Store.ListHealth(context.Background())
			if err != nil || len(health) != 1 || health[0].RefreshBefore == nil || *health[0].RefreshBefore != envelope.FormatTime(*refreshed.RefreshBefore) {
				t.Fatal("health did not adopt successful subscribe advice", err)
			}
		})
	}
}

type grokTTLDeliveryState struct {
	Held, Attempt, AttemptsLeft, LastStatus, Receipts, ReceiptAudits int
	Priority, State, NextAttempt, ReadAt, LastError, Expires         string
}

func grokTTLDelivery(t *testing.T, h *hub.Hub, msg string) grokTTLDeliveryState {
	t.Helper()
	var d grokTTLDeliveryState
	err := h.DB.QueryRow(`SELECT held, attempt, attempts_left, coalesce(last_status,0),
		(SELECT count(*) FROM events WHERE type='receipt'),
		(SELECT count(*) FROM audit WHERE action='receipt'),
		priority, state, coalesce(next_attempt_at,''), coalesce(read_at,''), coalesce(last_error,''), expires_at
		FROM deliveries WHERE msg_id=?`, msg).Scan(&d.Held, &d.Attempt, &d.AttemptsLeft, &d.LastStatus,
		&d.Receipts, &d.ReceiptAudits, &d.Priority, &d.State, &d.NextAttempt, &d.ReadAt, &d.LastError, &d.Expires)
	if err != nil {
		t.Fatal(err)
	}
	if expiry, err := envelope.ParseTime(d.Expires); err != nil || !expiry.After(h.Now()) {
		t.Fatal("held fixture must outlive the sampled fake time", err)
	}
	return d
}

func grokTTLAssertDelivery(t *testing.T, h *hub.Hub, msg string, before grokTTLDeliveryState, held int) {
	t.Helper()
	before.Held = held
	if got := grokTTLDelivery(t, h, msg); got != before {
		t.Errorf("delivery %s = %+v, want %+v (only held may change)", msg, got, before)
	}
}

func grokTTLSend(t *testing.T, env *testhub.Env, peer, key, priority string) string {
	t.Helper()
	body := req(key, peer)
	body["priority"] = priority
	msg := mustSend(t, env, env.OperatorSecret, body).MessageID
	d := grokTTLDelivery(t, env.Hub, msg)
	wantHeld := 0
	if priority == "wake_idle" {
		wantHeld = 1
	}
	if d.Priority != priority || d.Held != wantHeld || d.State != "queued" || d.Attempt != 0 || d.ReadAt != "" {
		t.Fatalf("fresh busy delivery control = %+v, want priority=%s held=%d queued/unattempted/unread", d, priority, wantHeld)
	}
	return msg
}

func TestGrokTTLHeldReleaseAtTick(t *testing.T) {
	env := testhub.Start(t)
	peer, secret := env.NewPeer("grokbot", "busy")
	reportPresence(t, env, secret, "", "working")
	msg := grokTTLSend(t, env, peer, "held-at-T", "wake_idle")
	hold := grokTTLSend(t, env, peer, "priority-hold", "hold")
	before, holdBefore := grokTTLDelivery(t, env.Hub, msg), grokTTLDelivery(t, env.Hub, hold)
	env.Now.Advance(600 * time.Second)
	env.Hub.Tick(context.Background(), false)
	grokTTLAssertDelivery(t, env.Hub, msg, before, 1)
	env.Now.Advance(time.Millisecond)
	env.Hub.Tick(context.Background(), false)
	grokTTLAssertDelivery(t, env.Hub, msg, before, 0)
	grokTTLAssertDelivery(t, env.Hub, hold, holdBefore, 0)
	body := req("fresh-after-boundary", peer)
	body["priority"] = "wake_idle"
	fresh := mustSend(t, env, env.OperatorSecret, body).MessageID
	d := grokTTLDelivery(t, env.Hub, fresh)
	if d.Held != 0 || d.State != "queued" || d.Attempt != 0 || d.ReadAt != "" || d.Receipts != before.Receipts || d.ReceiptAudits != before.ReceiptAudits {
		t.Errorf("fresh send after 600s+1ms = %+v, want unheld without dispatch or receipt", d)
	}
}

func TestGrokTTLProjectionConsumers(t *testing.T) {
	env := testhub.Start(t)
	peer, secret := env.NewPeer("grokbot", "projection")
	caller := provisionCaller(t, env, secret)
	reportPresence(t, env, secret, "", "working")
	state, at := "working", envelope.FormatTime(env.Now.Now())
	env.Now.Advance(601 * time.Second)
	row := hub.PresenceRow{Kind: "grokbot", State: &state, At: &at}
	snapshot, err := env.Hub.Preferences.LoadSnapshot(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{
		"EffectivePresence":  env.Hub.EffectivePresence(row, env.Now.Now()),
		"Current":            hub.EffectivePresenceSnapshot(env.Hub.Preferences.Current(), row, env.Now.Now()),
		"operation snapshot": hub.EffectivePresenceSnapshot(snapshot, row, env.Now.Now()),
		"Directory":          directoryPresence(t, env, peer),
	}
	reader := opview.New(env.Hub)
	peers, err := reader.Peers(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, p := range peers {
		if p.Peer.PeerID == peer {
			found = true
			got["Peers"] = p.Peer.Presence
		}
	}
	if !found {
		t.Fatal("Grok missing from operator peer projection")
	}
	// Resolve the real peer credential and project only its own inbox.
	inbox, err := reader.Inbox(context.Background(), caller, opapi.InboxQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if inbox.Me.PeerID != peer {
		t.Fatal("inbox projection used a foreign peer")
	}
	got["own-peer Inbox"] = inbox.Me.Presence
	for consumer, presence := range got {
		if presence != "unknown" {
			t.Errorf("%s at age601s = %q, want unknown", consumer, presence)
		}
	}
}

func TestGrokTTLPublicationAndStartupRelease(t *testing.T) {
	for _, mode := range []string{"startup", "notification", "missed-publication-restart"} {
		t.Run(mode, func(t *testing.T) {
			env := testhub.StartWith(t, func(cfg *hub.Config) {
				if mode != "startup" {
					*cfg = grokTTLExplicitDefault(*cfg, 7200)
				}
			})
			if mode != "startup" {
				runtimePatch(t, env.Hub, map[string]prefs.Value{"presence.default_ttl": prefs.Duration(3600)})
			}
			peer, secret := env.NewPeer("grokbot", "busy")
			reportPresence(t, env, secret, "", "working")
			msg := grokTTLSend(t, env, peer, "grok-held", "wake_idle")
			hold := grokTTLSend(t, env, peer, "hold-control", "hold")
			stale, staleSecret := env.NewPeer("session", "independent-startup-barrier")
			reportPresence(t, env, staleSecret, "", "working")
			barrier := grokTTLSend(t, env, stale, "session-held", "wake_idle")
			before, holdBefore := grokTTLDelivery(t, env.Hub, msg), grokTTLDelivery(t, env.Hub, hold)
			if mode == "startup" {
				env.Now.Advance(601 * time.Second)
			} else {
				env.Now.Advance(20 * time.Minute)
				env.Hub = grokTTLReopen(t, env.Hub, hub.DefaultConfig())
				prefValue(t, readPrefs(t, env.Hub), "presence.default_ttl", 3600, prefs.SourceStored, "")
			}
			reset := func() {
				t.Helper()
				s := readPrefs(t, env.Hub)
				p := prefs.Patch{Reset: []string{"presence.default_ttl"}}
				preview, err := env.Hub.Preferences.PreviewPreferences(context.Background(), prefsOperator, s.ETag, p)
				if err != nil {
					t.Fatal(err)
				}
				// Missing baseline risk must not hide the independent release oracle.
				if len(preview.Risks) > 0 {
					p.Confirm = []string{preview.ConfirmToken}
				}
				patchPrefs(t, env.Hub, s, p)
			}
			if mode == "missed-publication-restart" {
				if env.Hub.Preferences.SubscriberCount() != 0 {
					t.Fatal("missed-publication fixture already has a subscriber")
				}
				reset()
				grokTTLAssertDelivery(t, env.Hub, msg, before, 1)
				env.Hub = grokTTLReopen(t, env.Hub, hub.DefaultConfig())
				prefValue(t, readPrefs(t, env.Hub), "presence.default_ttl", 3600, prefs.SourceDefault, "")
			}
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan struct{})
			stop := runtimeWorkerCleanup(t, cancel, done)
			t.Cleanup(func() {
				stop()
				runtimeEventually(t, func() bool { return env.Hub.Preferences.SubscriberCount() == 0 })
			})
			go func() { env.Hub.RunTickers(ctx, time.Hour); close(done) }()
			if mode == "notification" {
				// A committed stale-session release proves the initial pass ran.
				runtimeEventually(t, func() bool {
					return grokTTLDelivery(t, env.Hub, barrier).Held == 0
				})
				grokTTLAssertDelivery(t, env.Hub, msg, before, 1)
				reset()
			}
			runtimeEventually(t, func() bool {
				return grokTTLDelivery(t, env.Hub, msg).Held == 0
			})
			stop()
			runtimeEventually(t, func() bool { return env.Hub.Preferences.SubscriberCount() == 0 })
			grokTTLAssertDelivery(t, env.Hub, msg, before, 0)
			grokTTLAssertDelivery(t, env.Hub, hold, holdBefore, 0)
		})
	}
}
