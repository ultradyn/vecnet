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
