package events_test

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/auth"
	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/events"
	"github.com/XertroV/vecnet/internal/hub"
	"github.com/XertroV/vecnet/internal/opview"
	"github.com/XertroV/vecnet/internal/prefs"
	"github.com/XertroV/vecnet/internal/testhub"
	"github.com/XertroV/vecnet/internal/webhook"
)

func TestFailedRefreshPreservesSuccessfulAdvice(t *testing.T) {
	env := testhub.Start(t)
	ctx := context.Background()
	st := events.NewStore(env.Hub)
	cb := testhub.NewCallback(t)
	client := env.Hub.EgressPolicy().Client(events.ChallengeTimeout)
	peer, _ := env.NewPeer("session", "subscriber")
	cur, err := env.Hub.Log.Current(ctx, []string{peer})
	if err != nil {
		t.Fatal(err)
	}
	spec := events.Subscription{
		PrincipalKind: events.KindVecnet, PrincipalID: peer, PeerID: peer,
		Name: events.Name, URL: cb.URL, Cursor: cur,
	}
	secret := webhook.NewSecret()
	sub, created, err := st.Upsert(ctx, spec, secret, 6*time.Hour, time.Time{})
	if err != nil || !created {
		t.Fatalf("first subscribe: created=%t err=%v", created, err)
	}
	if err := events.Challenge(ctx, client, sub, secret, env.Now.Now()); err != nil {
		t.Fatal(err)
	}
	if err := st.MarkVerified(ctx, sub.ID); err != nil {
		t.Fatal(err)
	}
	advice := env.Now.Now().Add(30 * time.Minute)
	if err := st.SetRefreshBefore(ctx, sub.ID, advice); err != nil {
		t.Fatal(err)
	}

	assertAdvice := func(want time.Time, verified bool) {
		t.Helper()
		got, err := st.Get(ctx, sub.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.RefreshBefore == nil || !got.RefreshBefore.Equal(want) {
			t.Errorf("Get refresh advice = %v, want %v", got.RefreshBefore, want)
		}
		if got.Verified != verified || got.Cursor != cur {
			t.Errorf("Get verified=%t cursor=%v, want verified=%t cursor=%v", got.Verified, got.Cursor, verified, cur)
		}
		health, err := st.ListHealth(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if len(health) != 1 || health[0].ID != sub.ID || health[0].RefreshBefore == nil || *health[0].RefreshBefore != envelope.FormatTime(want) {
			t.Errorf("ListHealth did not preserve advice %v: %+v", want, health)
		}
		views, err := opview.New(env.Hub).Subscriptions(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if len(views) != 1 || views[0].SubID != sub.ID || views[0].RefreshBefore == nil || *views[0].RefreshBefore != envelope.FormatTime(want) {
			t.Errorf("operator status did not preserve advice %v: %+v", want, views)
		}
	}
	assertAdvice(advice, true)

	env.Now.Advance(5 * time.Minute)
	operator := auth.Caller{Kind: "operator", PeerKind: "operator"}
	snapshot, err := env.Hub.Preferences.ReadPreferences(ctx, operator)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := env.Hub.Preferences.PatchPreferences(ctx, operator, snapshot.ETag, prefs.Patch{
		Set: map[string]prefs.Value{"events.refresh_interval": prefs.Duration(600)},
	}); err != nil {
		t.Fatal(err)
	}
	secret = webhook.NewSecret()
	refreshed, created, err := st.Upsert(ctx, spec, secret, 6*time.Hour, time.Time{})
	if err != nil || created || refreshed.ID != sub.ID || refreshed.Verified {
		t.Fatalf("secret-changing refresh: created=%t verified=%t same_id=%t err=%v", created, refreshed.Verified, refreshed.ID == sub.ID, err)
	}
	wantSchedule := env.Now.Now().Add(10 * time.Minute)
	if refreshed.NextRefreshBefore == nil || !refreshed.NextRefreshBefore.Equal(wantSchedule) {
		t.Fatalf("worker schedule = %v, want %v", refreshed.NextRefreshBefore, wantSchedule)
	}
	cb.OnChallenge(func(_ string, w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"challenge":"wrong"}`))
	})
	err = events.Challenge(ctx, client, refreshed, secret, env.Now.Now())
	var failed events.ErrChallenge
	if !errors.As(err, &failed) || failed.Reason != events.ReasonChallengeFailed {
		t.Fatalf("challenge error = %v, want challenge_failed", err)
	}
	assertAdvice(advice, false)

	// A later successful subscribe may finally replace the saved advice.
	env.Now.Advance(time.Minute)
	cb.OnChallenge(nil)
	refreshed, created, err = st.Upsert(ctx, spec, secret, 6*time.Hour, time.Time{})
	if err != nil || created || refreshed.ID != sub.ID {
		t.Fatalf("later refresh: created=%t same_id=%t err=%v", created, refreshed.ID == sub.ID, err)
	}
	assertAdvice(advice, false)
	if err := events.Challenge(ctx, client, refreshed, secret, env.Now.Now()); err != nil {
		t.Fatal(err)
	}
	if err := st.MarkVerified(ctx, sub.ID); err != nil {
		t.Fatal(err)
	}
	if err := st.SetRefreshBefore(ctx, sub.ID, env.Now.Now().Add(30*time.Minute)); err != nil {
		t.Fatal(err)
	}
	assertAdvice(env.Now.Now().Add(30*time.Minute), true)
	got, err := st.Get(ctx, sub.ID)
	if err != nil {
		t.Fatal(err)
	}
	wantSchedule = env.Now.Now().Add(10 * time.Minute)
	if got.NextRefreshBefore == nil || !got.NextRefreshBefore.Equal(wantSchedule) {
		t.Fatalf("successful subscribe changed worker cadence: %v, want %v", got.NextRefreshBefore, wantSchedule)
	}
}

// The advice a successful events/subscribe returns follows the live
// events.refresh_interval preference: a saved override applies to the next
// subscription without restart, survives restart over the bootstrap default,
// and a failed save leaves the previously returned advice untouched.
func TestSaveRefreshAdviceUsesLivePreference(t *testing.T) {
	env := testhub.Start(t)
	ctx := context.Background()
	st := events.NewStore(env.Hub)
	cb := testhub.NewCallback(t)
	peer, _ := env.NewPeer("session", "advice")
	cur, err := env.Hub.Log.Current(ctx, []string{peer})
	if err != nil {
		t.Fatal(err)
	}
	spec := events.Subscription{
		PrincipalKind: events.KindVecnet, PrincipalID: peer, PeerID: peer,
		Name: events.Name, URL: cb.URL, Cursor: cur,
	}
	secret := webhook.NewSecret()
	sub, _, err := st.Upsert(ctx, spec, secret, 6*time.Hour, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	saved := func(s *events.Store, want time.Time) {
		t.Helper()
		got, err := s.Get(ctx, sub.ID)
		if err != nil || got.RefreshBefore == nil || !got.RefreshBefore.Equal(want) {
			t.Fatalf("saved advice = %v, %v; want %v", got.RefreshBefore, err, want)
		}
		views, err := opview.New(env.Hub).Subscriptions(ctx)
		if err != nil || len(views) != 1 || views[0].RefreshBefore == nil || *views[0].RefreshBefore != envelope.FormatTime(want) {
			t.Fatalf("operator status advice = %+v, %v; want %v", views, err, want)
		}
	}

	// Bootstrap default: 1800 seconds.
	advice, err := st.SaveRefreshAdvice(ctx, sub.ID)
	if want := env.Now.Now().Add(30 * time.Minute); err != nil || !advice.Equal(want) {
		t.Fatalf("default advice = %v, %v; want %v", advice, err, want)
	}
	saved(st, advice)

	// A live save to 600 seconds changes the next subscription's advice, but
	// not the advice already returned until that subscription succeeds.
	env.Now.Advance(2 * time.Minute)
	operator := auth.Caller{Kind: "operator", PeerKind: "operator"}
	snapshot, err := env.Hub.Preferences.ReadPreferences(ctx, operator)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := env.Hub.Preferences.PatchPreferences(ctx, operator, snapshot.ETag, prefs.Patch{
		Set: map[string]prefs.Value{"events.refresh_interval": prefs.Duration(600)},
	}); err != nil {
		t.Fatal(err)
	}
	saved(st, advice)
	if _, _, err := st.Upsert(ctx, spec, secret, 6*time.Hour, time.Time{}); err != nil {
		t.Fatal(err)
	}
	saved(st, advice)
	live, err := st.SaveRefreshAdvice(ctx, sub.ID)
	if want := env.Now.Now().Add(10 * time.Minute); err != nil || !live.Equal(want) {
		t.Fatalf("live advice = %v, %v; want %v", live, err, want)
	}
	saved(st, live)

	// A missing subscription changes nothing and reports ErrNotFound.
	if _, err := st.SaveRefreshAdvice(ctx, "sub_missing"); !errors.Is(err, events.ErrNotFound) {
		t.Fatalf("missing subscription err = %v, want ErrNotFound", err)
	}
	saved(st, live)

	// Restart over the same store with the bootstrap default still 1800: the
	// stored override keeps governing new advice.
	env.Now.Advance(3 * time.Minute)
	h, err := hub.New(env.Hub.DB, env.Hub.Cfg, env.Now.Now)
	if err != nil {
		t.Fatal(err)
	}
	restarted := events.NewStore(h)
	saved(restarted, live)
	again, err := restarted.SaveRefreshAdvice(ctx, sub.ID)
	if want := env.Now.Now().Add(10 * time.Minute); err != nil || !again.Equal(want) {
		t.Fatalf("restarted advice = %v, %v; want %v", again, err, want)
	}
	saved(restarted, again)

	// The lease margin still caps the advice.
	short, _, err := restarted.Upsert(ctx, spec, secret, 15*time.Minute, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	capped, err := restarted.SaveRefreshAdvice(ctx, sub.ID)
	if want := short.Expires.Add(-events.RefreshMargin); err != nil || !capped.Equal(want) {
		t.Fatalf("short-lease advice = %v, %v; want %v", capped, err, want)
	}
}
