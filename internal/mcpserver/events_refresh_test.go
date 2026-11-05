package mcpserver_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/auth"
	"github.com/XertroV/vecnet/internal/events"
	"github.com/XertroV/vecnet/internal/hub"
	"github.com/XertroV/vecnet/internal/mcpdiag"
	"github.com/XertroV/vecnet/internal/mcpserver"
	"github.com/XertroV/vecnet/internal/prefs"
	"github.com/XertroV/vecnet/internal/testhub"
	"github.com/XertroV/vecnet/internal/webhook"
)

func TestSubscribeFiftyIdenticalRefreshes(t *testing.T) {
	env := testhub.Start(t)
	peer, credential := env.NewPeer("session", "frequent-refresh")
	cb := testhub.NewCallback(t)
	key := webhook.NewSecret()
	first := subscribeOK(t, env, credential, subParams(cb.URL, key, nil))
	var originalCreated string
	if err := env.Hub.DB.QueryRow(`SELECT created_at FROM event_subscriptions WHERE sub_id = ?`, first.ID).Scan(&originalCreated); err != nil {
		t.Fatal(err)
	}
	for i := range 50 {
		env.Now.Advance(time.Second)
		// Map insertion order and absent/null arguments do not alter identity.
		args := map[string]any{}
		extra := map[string]any{"arguments": args, "cursor": nil, "_meta": map[string]any{"refresh": i}}
		got := subscribeOK(t, env, credential, subParams(cb.URL, key, extra))
		if got.ID != first.ID || got.Cursor != first.Cursor {
			t.Fatalf("refresh %d changed identity/cursor", i)
		}
	}
	var count int
	var created string
	if err := env.Hub.DB.QueryRow(`SELECT count(*), min(created_at) FROM event_subscriptions WHERE principal_id = ?`, peer).Scan(&count, &created); err != nil {
		t.Fatal(err)
	}
	if count != 1 || created != originalCreated {
		t.Fatalf("refreshes recreated or duplicated the subscription: rows=%d", count)
	}
	if len(cb.Challenges()) != 1 {
		t.Fatalf("identical refreshes issued %d challenges, want 1", len(cb.Challenges()))
	}
}

func TestConcurrentSubscribeSingleVerification(t *testing.T) {
	env := testhub.Start(t)
	peer, credential := env.NewPeer("session", "parallel-refresh")
	cb := testhub.NewCallback(t)
	key := webhook.NewSecret()
	const callers = 16
	start := make(chan struct{})
	replies := make(chan rpcResp, callers)
	var wg sync.WaitGroup
	for range callers {
		wg.Go(func() {
			<-start
			replies <- rpc2026(t, env, credential, "events/subscribe", subParams(cb.URL, key, nil))
		})
	}
	close(start)
	wg.Wait()
	close(replies)
	id, count := "", 0
	for reply := range replies {
		count++
		if reply.Error != nil {
			t.Fatalf("parallel subscribe failed: code %d", reply.Error.Code)
		}
		var got subscribeResult
		if err := json.Unmarshal(reply.Result, &got); err != nil {
			t.Fatal(err)
		}
		if id == "" {
			id = got.ID
		}
		if got.ID != id {
			t.Fatal("parallel subscribe created a second identity")
		}
	}
	if count != callers {
		t.Fatalf("completed %d of %d subscribes", count, callers)
	}
	var rows int
	if err := env.Hub.DB.QueryRow(`SELECT count(*) FROM event_subscriptions WHERE principal_id = ?`, peer).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 1 || len(cb.Challenges()) != 1 {
		t.Fatalf("parallel refresh: rows=%d challenges=%d, want 1 each", rows, len(cb.Challenges()))
	}
}

func TestCallbackVerificationScopeAndRotation(t *testing.T) {
	env := testhub.Start(t)
	_, credential := env.NewPeer("session", "cache-owner")
	_, other := env.NewPeer("session", "cache-other")
	cb := testhub.NewCallback(t)
	key := webhook.NewSecret()
	first := subscribeOK(t, env, credential, subParams(cb.URL, key, nil))
	filtered := subscribeOK(t, env, credential, subParams(cb.URL, key, map[string]any{"arguments": map[string]any{"types": []string{"receipt"}}}))
	if first.ID == filtered.ID || len(cb.Challenges()) != 1 {
		t.Fatal("same principal and callback should reuse proof across different argument identities")
	}
	subscribeOK(t, env, other, subParams(cb.URL, key, nil))
	if len(cb.Challenges()) != 2 {
		t.Fatal("another principal reused the callback proof")
	}
	subscribeOK(t, env, credential, subParams(cb.URL+"/different", key, nil))
	if len(cb.Challenges()) != 3 {
		t.Fatal("another callback URL reused the proof")
	}
	rotated := webhook.NewSecret()
	cb.OnChallenge(func(_ string, w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusForbidden) })
	reply := rpc2026(t, env, credential, "events/subscribe", subParams(cb.URL, rotated, nil))
	if reply.Error == nil || errReason(t, reply) != "http_4xx" {
		t.Fatal("rotated key bypassed a failing challenge")
	}
	sub, err := events.NewStore(env.Hub).Get(context.Background(), first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if sub.Verified {
		t.Fatal("failed key rotation left the subscription active")
	}
	cb.OnChallenge(nil)
	got := subscribeOK(t, env, credential, subParams(cb.URL, rotated, nil))
	if got.ID != first.ID || len(cb.Challenges()) != 5 {
		t.Fatal("successful key rotation did not reverify the original identity")
	}
}

func TestEventsControlFieldsStrict(t *testing.T) {
	env := testhub.Start(t)
	_, credential := env.NewPeer("session", "strict-controls")
	cb := testhub.NewCallback(t)
	key := webhook.NewSecret()
	for _, method := range []string{"events/subscribe", "events/unsubscribe"} {
		for name, extra := range map[string]map[string]any{
			"outer unknown":    {"unexpected": true},
			"delivery unknown": {"delivery": map[string]any{"mode": "webhook", "url": cb.URL, "secret": key, "unexpected": true}},
			"metadata scalar":  {"_meta": "opaque"},
			"metadata null":    {"_meta": nil},
			"fractional ttl":   {"ttlMs": 1.5},
			"empty cursor":     {"cursor": ""},
			"long url":         {"delivery": map[string]any{"mode": "webhook", "url": cb.URL + strings.Repeat("x", 2048), "secret": key}},
			"wrong event":      {"name": "other"},
			"wrong mode":       {"delivery": map[string]any{"mode": "other", "url": cb.URL, "secret": key}},
		} {
			t.Run(method+"/"+name, func(t *testing.T) {
				reply := rpc2026(t, env, credential, method, subParams(cb.URL, key, extra))
				if reply.Error == nil || reply.Error.Code != mcpserver.ErrInvalidParams {
					t.Fatal("invalid control fields were accepted")
				}
			})
		}
	}
	if cb.Requests() != 0 {
		t.Fatal("invalid control request reached callback")
	}
}

func TestEventLifecycleDiagnosticsExcludePayloads(t *testing.T) {
	env := testhub.Start(t)
	peer, credential := env.NewPeer("session", "diag-lifecycle")
	_, sender := env.NewPeer("session", "diag-sender")
	cb := testhub.NewCallback(t)
	key := webhook.NewSecret()
	const pathCanary = "callback-path-canary-private"
	callback := cb.URL + "/" + pathCanary
	res := subscribeOK(t, env, credential, subParams(callback, key, nil))
	subscribeOK(t, env, credential, subParams(callback, key, nil))
	cb.OnPost(func(_ int, w http.ResponseWriter) { w.WriteHeader(http.StatusServiceUnavailable) })
	sendPriority(t, env, sender, peer, "message-canary-private", "wake")
	stepEvents(t, env)
	rpc2026(t, env, credential, "events/unsubscribe", subParams(callback, key, nil))
	records := mcpdiag.Snapshot(mcpdiag.Filter{Peer: peer})
	seen := map[string]bool{}
	for _, record := range records {
		seen[record.Kind] = true
		if strings.HasPrefix(record.Kind, "events.") && record.Kind != "events.subscribe_parsed" && record.Fields["subscription_id"] != res.ID {
			t.Errorf("lifecycle record omitted subscription routing ID: %s", record.Kind)
		}
	}
	for _, kind := range []string{"events.verification", "events.activation", "events.refresh", "events.delivery", "events.unsubscribe"} {
		if !seen[kind] {
			t.Errorf("missing %s diagnostic", kind)
		}
	}
	raw, err := json.Marshal(records)
	if err != nil {
		t.Fatal(err)
	}
	for _, canary := range []string{credential, key, pathCanary, "message-canary-private", callback} {
		if strings.Contains(string(raw), canary) {
			t.Fatal("lifecycle diagnostics retained credential, callback path or message text")
		}
	}
}

func TestShortLeaseRefreshReusesCallbackProof(t *testing.T) {
	env := testhub.Start(t)
	_, credential := env.NewPeer("session", "short-lease-refresh")
	cb := testhub.NewCallback(t)
	key := webhook.NewSecret()
	started := env.Now.Now()
	params := subParams(cb.URL, key, map[string]any{"ttlMs": 60000})
	first := subscribeOK(t, env, credential, params)
	env.Now.Advance(2 * time.Minute)
	// Exercise expiry cleanup before authenticated refresh. The callback proof
	// belongs to the principal+URL+key and outlives this short subscription lease.
	stepEvents(t, env)
	store := events.NewStore(env.Hub)
	expired, err := store.Get(context.Background(), first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if expired.Verified {
		t.Fatal("expired subscription was still active")
	}
	refreshed := subscribeOK(t, env, credential, params)
	if refreshed.ID != first.ID || len(cb.Challenges()) != 1 {
		t.Fatal("short lease refresh re-challenged a still-valid callback proof")
	}
	sub, err := store.Get(context.Background(), first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !sub.Verified || sub.VerifiedAt == nil || !sub.VerifiedAt.Equal(started) {
		t.Fatal("cached lease refresh did not retain original proof timestamp")
	}
	// A cache hit must not slide the proof's five minute verification deadline.
	env.Now.Advance(events.ChallengeMaxAge - 2*time.Minute + time.Second)
	got := subscribeOK(t, env, credential, params)
	if got.ID != first.ID || len(cb.Challenges()) != 2 {
		t.Fatal("refresh extended the original callback proof lifetime")
	}
}

func TestChallengeRedirectDiagnosticsRetainStatus(t *testing.T) {
	env := testhub.Start(t)
	peer, credential := env.NewPeer("session", "redirect-status")
	cb := testhub.NewCallback(t)
	cb.OnChallenge(func(_ string, w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/redirected", http.StatusFound)
	})
	reply := rpc2026(t, env, credential, "events/subscribe", subParams(cb.URL, webhook.NewSecret(), nil))
	if reply.Error == nil || errReason(t, reply) != "challenge_failed" {
		t.Fatal("redirect was accepted as verification")
	}
	if len(cb.Challenges()) != 1 {
		t.Fatal("challenge redirect was followed")
	}
	for _, record := range mcpdiag.Snapshot(mcpdiag.Filter{Peer: peer}) {
		if record.Kind == "events.verification" && record.Fields["http_status"] == http.StatusFound && record.Fields["reason"] == "challenge_failed" {
			return
		}
	}
	t.Fatal("refused redirect diagnostic omitted HTTP 302 status")
}

func TestRefreshDeadlineStatusDoesNotSlide(t *testing.T) {
	// The bootstrap layer (flag/env) supplies the first cadence.
	env := testhub.StartWith(t, func(c *hub.Config) { c.EventsRefreshInterval = 7 * time.Minute })
	_, bearer := env.NewPeer("session", "refresh-status")
	cb := testhub.NewCallback(t)
	params := subParams(cb.URL, webhook.NewSecret(), nil)
	first := subscribeOK(t, env, bearer, params)
	want := env.Now.Now().Add(7 * time.Minute).Format("2006-01-02T15:04:05.000Z")
	if first.RefreshBefore == nil || *first.RefreshBefore != want {
		t.Fatalf("custom interval = %v, want %s", first.RefreshBefore, want)
	}
	// The original deadline survives passing time and a live preference save.
	env.Now.Advance(8 * time.Minute)
	setRefreshInterval(t, env, 3600)
	var status subscriptionStatusResult
	toolOK(t, env, bearer, "vecnet_subscriptions", nil, &status)
	if len(status.Subscriptions) != 1 || status.Subscriptions[0].RefreshBefore == nil || *status.Subscriptions[0].RefreshBefore != want {
		t.Fatalf("status deadline slid: %+v", status)
	}
	refreshed := subscribeOK(t, env, bearer, params)
	want = env.Now.Now().Add(time.Hour).Format("2006-01-02T15:04:05.000Z")
	if refreshed.ID != first.ID || refreshed.RefreshBefore == nil || *refreshed.RefreshBefore != want {
		t.Fatalf("refresh did not replace deadline: %+v", refreshed)
	}
}

// A live 1800-to-600 save governs the next successful events/subscribe
// response, while a failed re-subscription keeps the advice already returned.
func TestSubscribeAdviceFollowsLiveRefreshPreference(t *testing.T) {
	env := testhub.Start(t)
	_, bearer := env.NewPeer("session", "refresh-live")
	cb := testhub.NewCallback(t)
	params := subParams(cb.URL, webhook.NewSecret(), nil)
	first := subscribeOK(t, env, bearer, params)
	want := env.Now.Now().Add(30 * time.Minute).Format("2006-01-02T15:04:05.000Z")
	if first.RefreshBefore == nil || *first.RefreshBefore != want {
		t.Fatalf("default advice = %v, want %s", first.RefreshBefore, want)
	}
	env.Now.Advance(2 * time.Minute)
	setRefreshInterval(t, env, 600)

	// A new secret needs a fresh challenge; a failed one changes no advice.
	cb.OnChallenge(func(_ string, w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"challenge":"wrong"}`))
	})
	failed := subParams(cb.URL, webhook.NewSecret(), nil)
	if reply := rpc2026(t, env, bearer, "events/subscribe", failed); reply.Error == nil || errReason(t, reply) != "challenge_failed" {
		t.Fatal("wrong challenge answer was accepted")
	}
	var status subscriptionStatusResult
	toolOK(t, env, bearer, "vecnet_subscriptions", nil, &status)
	if len(status.Subscriptions) != 1 || status.Subscriptions[0].RefreshBefore == nil || *status.Subscriptions[0].RefreshBefore != want {
		t.Fatalf("failed refresh replaced advice: %+v", status)
	}

	cb.OnChallenge(nil)
	refreshed := subscribeOK(t, env, bearer, failed)
	want = env.Now.Now().Add(10 * time.Minute).Format("2006-01-02T15:04:05.000Z")
	if refreshed.ID != first.ID || refreshed.RefreshBefore == nil || *refreshed.RefreshBefore != want {
		t.Fatalf("live preference not applied: %+v, want %s", refreshed, want)
	}
	toolOK(t, env, bearer, "vecnet_subscriptions", nil, &status)
	if len(status.Subscriptions) != 1 || status.Subscriptions[0].RefreshBefore == nil || *status.Subscriptions[0].RefreshBefore != want {
		t.Fatalf("status does not show returned advice: %+v", status)
	}
}

func setRefreshInterval(t *testing.T, env *testhub.Env, seconds int64) {
	t.Helper()
	ctx := context.Background()
	operator := auth.Caller{Kind: "operator", PeerKind: "operator"}
	snapshot, err := env.Hub.Preferences.ReadPreferences(ctx, operator)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := env.Hub.Preferences.PatchPreferences(ctx, operator, snapshot.ETag, prefs.Patch{
		Set: map[string]prefs.Value{"events.refresh_interval": prefs.Duration(seconds)},
	}); err != nil {
		t.Fatal(err)
	}
}
