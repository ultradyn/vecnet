package httpapi_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/events"
	"github.com/XertroV/vecnet/internal/testhub"
)

func TestAdminRemoveSubscription(t *testing.T) {
	env := testhub.Start(t)
	peer, peerSecret := env.NewPeer("session", "receiver")
	_, principalSecret := env.NewPrincipal("principal")
	const callback = "https://callback.invalid/private-capability?token=synthetic-sensitive-value"
	newSub := func() events.Subscription {
		t.Helper()
		sub, _, err := env.Events.Store.Upsert(context.Background(), events.Subscription{
			PrincipalKind: events.KindVecnet, PrincipalID: peer, PeerID: peer, Name: events.Name, URL: callback,
		}, "synthetic-signing-secret", time.Hour, time.Time{})
		if err != nil {
			t.Fatal("could not create subscription fixture")
		}
		return sub
	}
	sub := newSub()
	path := "/v1/admin/subscriptions/" + sub.ID
	for _, tc := range []struct {
		name, secret string
		want         int
	}{
		{"anonymous", "", 401}, {"peer", peerSecret, 403}, {"principal", principalSecret, 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp, raw := env.Do(tc.secret, "DELETE", path, nil)
			if resp.StatusCode != tc.want {
				t.Fatalf("status=%d want=%d", resp.StatusCode, tc.want)
			}
			if strings.Contains(string(raw), callback) || strings.Contains(string(raw), "synthetic-sensitive-value") {
				t.Fatal("response leaked callback capability")
			}
			if _, err := env.Events.Store.Get(context.Background(), sub.ID); err != nil {
				t.Fatal("unauthorized removal changed subscription")
			}
		})
	}
	for _, body := range []string{`{"url":"synthetic"}`, `null`, `[]`, `{`, `{} {}`} {
		resp, raw := env.Do(env.OperatorSecret, "DELETE", path, body)
		if resp.StatusCode != http.StatusBadRequest || !strings.Contains(string(raw), `"invalid_body"`) {
			t.Fatalf("body validation returned %d", resp.StatusCode)
		}
		if _, err := env.Events.Store.Get(context.Background(), sub.ID); err != nil {
			t.Fatal("invalid body removed subscription")
		}
	}
	for _, body := range []any{nil, map[string]any{}} {
		sub = newSub()
		raw := adminDo(t, env, "DELETE", "/v1/admin/subscriptions/"+sub.ID, body, http.StatusNoContent)
		if len(raw) != 0 {
			t.Fatal("204 had response body")
		}
		if _, err := env.Events.Store.Get(context.Background(), sub.ID); !errors.Is(err, events.ErrNotFound) {
			t.Fatal("successful delete did not remove subscription")
		}
		raw = adminDo(t, env, "DELETE", "/v1/admin/subscriptions/"+sub.ID, nil, http.StatusNotFound)
		if !strings.Contains(string(raw), `"not_found"`) {
			t.Fatal("missing stable not_found error")
		}
	}
	adminDo(t, env, "DELETE", "/v1/admin/subscriptions/sub_unknown", nil, http.StatusNotFound)
}

func TestAdminRemoveSubscriptionFailureIsSafe(t *testing.T) {
	env := testhub.Start(t)
	peer, _ := env.NewPeer("session", "receiver")
	sub, _, err := env.Events.Store.Upsert(context.Background(), events.Subscription{
		PrincipalKind: events.KindVecnet, PrincipalID: peer, PeerID: peer, Name: events.Name, URL: "https://callback.invalid/synthetic-sensitive-value",
	}, "synthetic-signing-secret", time.Hour, time.Time{})
	if err != nil {
		t.Fatal("could not create subscription fixture")
	}
	if _, err := env.Hub.DB.Exec(`CREATE TRIGGER reject_subscription_remove BEFORE DELETE ON event_subscriptions BEGIN SELECT RAISE(ABORT,'test failure'); END`); err != nil {
		t.Fatal(err)
	}
	raw := adminDo(t, env, "DELETE", "/v1/admin/subscriptions/"+sub.ID, nil, http.StatusInternalServerError)
	if strings.Contains(string(raw), "synthetic-sensitive-value") || strings.Contains(string(raw), "synthetic-signing-secret") {
		t.Fatal("internal error leaked sensitive data")
	}
	if _, err := env.Events.Store.Get(context.Background(), sub.ID); err != nil {
		t.Fatal("failed request removed subscription")
	}
}
