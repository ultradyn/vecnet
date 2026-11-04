package mcpserver_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/auth"
	venv "github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/events"
	"github.com/XertroV/vecnet/internal/mcpserver"
	"github.com/XertroV/vecnet/internal/testhub"
	"github.com/XertroV/vecnet/internal/webhook"
)

type subscriptionStatusResult struct {
	Subscriptions []struct {
		ID             string         `json:"id"`
		Name           string         `json:"name"`
		Arguments      map[string]any `json:"arguments"`
		CreatedAt      string         `json:"created_at"`
		RefreshBefore  *string        `json:"refresh_before"`
		ExpiresAt      *string        `json:"expires_at"`
		Paused         bool           `json:"paused"`
		Verified       bool           `json:"verified"`
		PausedReason   *string        `json:"paused_reason"`
		LastDeliveryAt *string        `json:"last_delivery_at"`
		LastStatus     *int           `json:"last_status"`
		CallbackHost   string         `json:"callback_host"`
	} `json:"subscriptions"`
	Hint string `json:"hint"`
}

func TestSubscriptionsFailedRefreshShowsUnverified(t *testing.T) {
	env := testhub.Start(t)
	_, secret := env.NewPeer("session", "reader")
	cb := testhub.NewCallback(t)
	params := subParams(cb.URL, webhook.NewSecret(), map[string]any{"ttlMs": 1000})
	sub := subscribeOK(t, env, secret, params)
	env.Now.Advance(2 * time.Second)
	cb.OnChallenge(func(_ string, w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusForbidden) })
	res := rpc2026(t, env, secret, "events/subscribe", params)
	if res.Error == nil || res.Error.Code != mcpserver.ErrCodeCallback {
		t.Fatalf("refresh = %+v", res.Error)
	}
	var got subscriptionStatusResult
	toolOK(t, env, secret, "vecnet_subscriptions", nil, &got)
	if len(got.Subscriptions) != 1 || got.Subscriptions[0].ID != sub.ID || got.Subscriptions[0].Verified || got.Subscriptions[0].Paused {
		t.Fatalf("retained unverified refresh = %+v", got)
	}
}

func TestSubscriptionsEmptyAndStrictInput(t *testing.T) {
	env := testhub.Start(t)
	peer, secret := env.NewPeer("session", "reader")
	_, principalSecret := env.NewPrincipal("principal")
	for _, credential := range []string{secret, principalSecret, env.OperatorSecret} {
		var got subscriptionStatusResult
		toolOK(t, env, credential, "vecnet_subscriptions", nil, &got)
		if got.Subscriptions == nil || len(got.Subscriptions) != 0 {
			t.Fatalf("empty subscriptions = %+v, want []", got.Subscriptions)
		}
		for _, phrase := range []string{"not active", "cannot subscribe", "no model tool", "vecnet.message", "Rescan", "Work + Cloud", "Workspace controls", "vecnet_inbox", "Never claim you subscribed", "Subscribe to the vecnet.message event from the vecnet plugin with no arguments, and when an event arrives, read it and reply via vecnet."} {
			if !strings.Contains(got.Hint, phrase) {
				t.Errorf("empty hint %q lacks %q", got.Hint, phrase)
			}
		}
	}
	for _, field := range []string{"peer", "peer_id", "as"} {
		if path := invalidPath(t, env, env.OperatorSecret, "vecnet_subscriptions", map[string]any{field: peer}); path != "/"+field {
			t.Errorf("identity override %s: path = %q", field, path)
		}
	}
	_, listed := rpc(t, env, secret, "tools/list", nil)
	var result struct {
		Tools []map[string]any `json:"tools"`
	}
	if err := json.Unmarshal(listed.Result, &result); err != nil {
		t.Fatal(err)
	}
	for _, tool := range result.Tools {
		if tool["name"] == "vecnet_subscriptions" {
			assertSimpleEventSchema(t, tool["inputSchema"])
		}
	}
}

func TestSubscriptionsHealthAndPeerIsolation(t *testing.T) {
	env := testhub.Start(t)
	_, secret := env.NewPeer("session", "reader")
	_, otherSecret := env.NewPeer("session", "other")
	cb := testhub.NewCallback(t)
	key := webhook.NewSecret()
	sub := subscribeOK(t, env, secret, subParams(cb.URL, key, map[string]any{
		"arguments": map[string]any{"types": []string{"delivery", "receipt"}},
	}))
	other := subscribeOK(t, env, otherSecret, subParams(cb.URL, webhook.NewSecret(), nil))
	operator := subscribeOK(t, env, env.OperatorSecret, subParams(cb.URL, webhook.NewSecret(), nil))
	// The stored callback carries canaries in every URL component that must
	// remain private, even though the model may inspect health freely.
	privateURL := "https://user-canary:password-canary@callback.example:8443/private-path-canary?token=query-canary#fragment-canary"
	now := venv.FormatTime(env.Now.Now())
	if _, err := env.Hub.DB.Exec(`UPDATE event_subscriptions SET url = ?, paused_reason = ?, last_delivery_at = ?, last_status = ? WHERE sub_id = ?`,
		privateURL, events.PausedDeliveryFailed, now, 503, sub.ID); err != nil {
		t.Fatal(err)
	}
	var got subscriptionStatusResult
	res := callTool(t, env, secret, "vecnet_subscriptions", nil)
	if res.IsError || json.Unmarshal(res.StructuredContent, &got) != nil || len(got.Subscriptions) != 1 {
		t.Fatalf("subscription status = %s", res.StructuredContent)
	}
	h := got.Subscriptions[0]
	if h.ID != sub.ID || h.Name != events.Name || h.CallbackHost != "callback.example" || !h.Verified || !h.Paused || h.PausedReason == nil || *h.PausedReason != events.PausedDeliveryFailed || h.LastDeliveryAt == nil || *h.LastDeliveryAt != now || h.LastStatus == nil || *h.LastStatus != 503 {
		t.Fatalf("health = %+v", h)
	}
	if h.CreatedAt != now || h.ExpiresAt == nil || *h.ExpiresAt != venv.FormatTime(env.Now.Now().Add(events.MaxTTL)) || h.RefreshBefore == nil || *h.RefreshBefore != *sub.RefreshBefore {
		t.Fatalf("health timing = %+v", h)
	}
	if mustJSON(t, h.Arguments) != `{"types":["delivery","receipt"]}` || got.Hint != "" {
		t.Fatalf("arguments/hint = %s / %q", mustJSON(t, h.Arguments), got.Hint)
	}
	for _, canary := range []string{key, "user-canary", "password-canary", "8443", "private-path-canary", "query-canary", "fragment-canary", other.ID, operator.ID} {
		if strings.Contains(string(res.StructuredContent), canary) {
			t.Errorf("subscription result leaks %q", canary)
		}
	}
	for _, tc := range []struct{ secret, id string }{{otherSecret, other.ID}, {env.OperatorSecret, operator.ID}} {
		var own subscriptionStatusResult
		toolOK(t, env, tc.secret, "vecnet_subscriptions", nil, &own)
		if len(own.Subscriptions) != 1 || own.Subscriptions[0].ID != tc.id {
			t.Fatalf("own subscriptions = %+v, want only %s", own, tc.id)
		}
		if own.Subscriptions[0].Paused || own.Subscriptions[0].PausedReason != nil || own.Subscriptions[0].LastDeliveryAt != nil || own.Subscriptions[0].LastStatus != nil {
			t.Fatalf("new subscription health = %+v", own.Subscriptions[0])
		}
	}
	// Expiry, including the exact boundary, is reflected without waiting for
	// the delivery worker to prune its rows.
	env.Now.Advance(events.MaxTTL)
	var expired subscriptionStatusResult
	toolOK(t, env, secret, "vecnet_subscriptions", nil, &expired)
	if len(expired.Subscriptions) != 0 || expired.Hint == "" {
		t.Fatalf("expired subscriptions = %+v", expired)
	}
}

func TestSubscriptionsOAuthResolvesOwnPeer(t *testing.T) {
	env := testhub.Start(t)
	dot, secret := env.NewPeer("session", "dot")
	other, _ := env.NewPeer("session", "stale-bound-peer")
	principal, err := env.Hub.PeerPrincipal(context.Background(), dot)
	if err != nil {
		t.Fatal(err)
	}
	cb := testhub.NewCallback(t)
	sub := subscribeOK(t, env, secret, subParams(cb.URL, webhook.NewSecret(), nil))
	front := eventsFront(t, env, diagAuth{mcpserver.Principal{
		Caller: auth.Caller{Kind: "principal", PeerID: other},
		OAuth:  &mcpserver.OAuthInfo{PrincipalID: principal, Expires: env.Now.Now().Add(time.Hour)},
	}}, func(s *mcpserver.Server) {
		s.ResolveDotPeer = func(_ context.Context, id string) (string, error) {
			if id != principal {
				t.Errorf("resolve principal %s, want %s", id, principal)
			}
			return dot, nil
		}
	})
	var got subscriptionStatusResult
	toolOK(t, front, "oauth", "vecnet_subscriptions", nil, &got)
	if len(got.Subscriptions) != 1 || got.Subscriptions[0].ID != sub.ID {
		t.Fatalf("OAuth subscriptions = %+v", got)
	}
	unlinked := eventsFront(t, env, fakeOAuth{prn: principal, expires: func() time.Time { return env.Now.Now().Add(time.Hour) }}, nil)
	toolFails(t, unlinked, "oauth", "vecnet_subscriptions", nil, "not_linked")
}
