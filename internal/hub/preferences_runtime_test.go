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

func TestPreferencesRuntimeSingleOperationSnapshot(t *testing.T) {
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
	if err != nil || effective.Source != prefs.SourceDefault && s.RefreshBefore == nil {
		t.Fatal("Upsert did not apply explicit refresh policy", err)
	}
	if s.RefreshBefore == nil {
		// Match the successful subscribe path for an inherited default. A
		// stored override must already have been applied by Upsert itself.
		if err := env.Events.Store.SetRefreshBefore(context.Background(), s.ID, env.Now.Now().Add(30*time.Minute)); err != nil {
			t.Fatal(err)
		}
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
	if after.RefreshBefore == nil || after.RefreshBefore.Before(env.Now.Now().Add(20*time.Minute)) || after.RefreshBefore.After(env.Now.Now().Add(21*time.Minute)) {
		t.Fatalf("latest refresh schedule = %v, want next 20 minutes with bounded staggering", after.RefreshBefore)
	}
	if !reflect.DeepEqual(after.Expires, before.Expires) || !reflect.DeepEqual(after.TokenExpires, before.TokenExpires) || after.Cursor != before.Cursor || after.Verified != before.Verified {
		t.Fatal("rescheduling changed lease, token, cursor or verification")
	}
	deadline := *after.RefreshBefore
	env.Now.Advance(time.Minute)
	if _, err := env.Events.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	again, err := env.Events.Store.Get(context.Background(), before.ID)
	if err != nil || again.RefreshBefore == nil || !again.RefreshBefore.Equal(deadline) {
		t.Fatal("unchanged generation slid the refresh deadline", err)
	}
	// New subscriptions and the legacy SetRefreshBefore seam must also use
	// the stored cadence, even if their caller supplies the old config default.
	fresh := runtimeSubscription(t, env, "fresh")
	if fresh.ID == before.ID {
		t.Fatal("new-subscription acceptance reused the existing subscription")
	}
	if fresh.RefreshBefore == nil || fresh.RefreshBefore.Before(env.Now.Now().Add(20*time.Minute)) || fresh.RefreshBefore.After(env.Now.Now().Add(21*time.Minute)) {
		t.Fatalf("new subscription ignored stored refresh cadence: %v", fresh.RefreshBefore)
	}
	if err := env.Events.Store.SetRefreshBefore(context.Background(), fresh.ID, env.Now.Now().Add(30*time.Minute)); err != nil {
		t.Fatal(err)
	}
	got, err := env.Events.Store.Get(context.Background(), fresh.ID)
	if err != nil || got.RefreshBefore == nil || !got.RefreshBefore.Equal(*fresh.RefreshBefore) {
		t.Fatal("legacy deadline setter bypassed stored cadence", err)
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
	if err != nil || got.RefreshBefore == nil || got.RefreshBefore.Before(env.Now.Now().Add(30*time.Minute)) || got.RefreshBefore.After(env.Now.Now().Add(31*time.Minute)) {
		t.Fatalf("missed reset left stale refresh policy: %v %v", got.RefreshBefore, err)
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
	if err != nil || got.RefreshBefore == nil || !reflect.DeepEqual(got.RefreshBefore, sub.RefreshBefore) {
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
			if err != nil || applied.RefreshBefore == nil {
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
				if err != nil || got.RefreshBefore == nil || !got.RefreshBefore.Equal(*applied.RefreshBefore) {
					t.Fatalf("restart %d slid applied advice from %v to %v: %v", i+1, applied.RefreshBefore, got.RefreshBefore, err)
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
	if err != nil || got.RefreshBefore == nil || !got.RefreshBefore.Equal(*sub.RefreshBefore) {
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
	if err != nil || got.RefreshBefore == nil || got.RefreshBefore.Before(env.Now.Now().Add(10*time.Minute)) || got.RefreshBefore.After(env.Now.Now().Add(11*time.Minute)) {
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
	// The retry is due on the fake clock, but advice publication must not
	// dispatch it or tail the additional event before the regular worker tick.
	env.Now.Advance(time.Hour)
	mustSend(t, env, env.OperatorSecret, req("still-queued", peer))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	env.Events.Interval = time.Hour
	go func() { _ = env.Events.Run(ctx); close(done) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("events worker did not stop")
		}
	})
	runtimeEventually(t, func() bool { return env.Hub.Preferences.SubscriberCount() == 1 })
	runtimePatch(t, env.Hub, map[string]prefs.Value{"events.refresh_interval": prefs.Duration(600)})
	runtimeEventually(t, func() bool {
		got, err := env.Events.Store.Get(context.Background(), sub.ID)
		return err == nil && got.RefreshBefore != nil && got.RefreshBefore.Before(env.Now.Now().Add(11*time.Minute))
	})
	cancel()
	<-done
	after, err := env.Events.Store.ListHealth(context.Background())
	if err != nil || len(after) != 1 {
		t.Fatal("health after publication", err)
	}
	if cb.Requests() != 1 || after[0].Attempts != before[0].Attempts || after[0].Cursor != before[0].Cursor || !reflect.DeepEqual(after[0].LastDeliveryAt, before[0].LastDeliveryAt) || !reflect.DeepEqual(after[0].ExpiresAt, before[0].ExpiresAt) {
		t.Fatal("preference wake dispatched or changed retry/cursor/lease state")
	}
}

func TestPreferencesRuntimeWorkersApplyPublicationAndCancel(t *testing.T) {
	env := testhub.Start(t)
	sub := runtimeSubscription(t, env)
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
	done := make(chan struct{}, 2)
	env.Events.Interval = time.Hour
	go func() { _ = env.Events.Run(ctx); done <- struct{}{} }()
	go func() { env.Hub.RunTickers(ctx, time.Hour); done <- struct{}{} }()
	t.Cleanup(cancel)
	runtimeEventually(t, func() bool { return env.Hub.Preferences.SubscriberCount() == 2 })
	runtimePatch(t, env.Hub, map[string]prefs.Value{"presence.session_ttl": prefs.Duration(60), "events.refresh_interval": prefs.Duration(600)})
	runtimeEventually(t, func() bool {
		got, err := env.Events.Store.Get(context.Background(), sub.ID)
		var held int
		err2 := env.Hub.DB.QueryRow(`SELECT held FROM deliveries WHERE msg_id=? AND recipient=?`, msg.MessageID, peer).Scan(&held)
		return err == nil && err2 == nil && held == 0 && got.RefreshBefore != nil && got.RefreshBefore.Before(env.Now.Now().Add(11*time.Minute))
	})
	cancel()
	for range 2 {
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("worker did not stop on cancellation")
		}
	}
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
		done := make(chan struct{}, 2)
		go func() { _ = env.Events.Run(ctx); done <- struct{}{} }()
		go func() { env.Hub.RunTickers(ctx, time.Hour); done <- struct{}{} }()
		runtimeEventually(t, func() bool { return env.Hub.Preferences.SubscriberCount() == 2 })
		s := readPrefs(t, env.Hub)
		written := make(chan error, 1)
		go func() {
			_, err := env.Hub.Preferences.PatchPreferences(context.Background(), prefsOperator, s.ETag, prefs.Patch{Set: map[string]prefs.Value{"events.refresh_interval": prefs.Duration(int64(600 + i))}})
			written <- err
		}()
		cancel()
		for range 2 {
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("publication/cancel race stranded a worker")
			}
		}
		if err := <-written; err != nil {
			t.Fatal(err)
		}
		runtimeEventually(t, func() bool { return env.Hub.Preferences.SubscriberCount() == 0 })
	}
}
