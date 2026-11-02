package opview_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/events"
	"github.com/XertroV/vecnet/internal/opapi"
	"github.com/XertroV/vecnet/internal/opview"
	"github.com/XertroV/vecnet/internal/testhub"
	"github.com/XertroV/vecnet/internal/webhook"
)

const (
	tokenNote    = "Waiting for ChatGPT to re-subscribe with a fresh token. Nothing is lost; delivery resumes from the cursor."
	failedNote   = "One event used all 8 attempts. Fix the callback, then resume. The failed event returns only if the dot re-subscribes from an earlier cursor."
	authNote     = "The peer was disabled or moved, or its login was replaced or revoked. Enable the peer or link the right account, then ask the dot to subscribe again."
	rotatedNote  = "The bearer that subscribed was rotated. The holder of the new bearer must subscribe again."
	operatorNote = "You paused this subscription. A re-subscribe does not lift it."
)

func newSubscription(t *testing.T, env *testhub.Env, name, callback string) events.Subscription {
	t.Helper()
	peer, _ := env.NewPeer("session", name)
	var principal string
	if err := env.Hub.DB.QueryRowContext(context.Background(), `SELECT prn_id FROM peers WHERE peer_id = ?`, peer).Scan(&principal); err != nil {
		t.Fatal(err)
	}
	cur, err := env.Hub.Log.Current(context.Background(), []string{peer})
	if err != nil {
		t.Fatal(err)
	}
	sub, created, err := events.NewStore(env.Hub).Upsert(context.Background(), events.Subscription{PrincipalKind: events.KindVecnet, PrincipalID: principal, PeerID: peer, Name: events.Name, URL: callback, Cursor: cur}, webhook.NewSecret(), 0, time.Time{})
	if err != nil || !created {
		t.Fatalf("new subscription: created=%v err=%v", created, err)
	}
	return sub
}

func setHealth(t *testing.T, env *testhub.Env, id string, reason *string, verified bool, expires *time.Time, attempts int, status *int) {
	t.Helper()
	var expiry, verification any
	if expires != nil {
		expiry = envelope.FormatTime(*expires)
	}
	if verified {
		verification = envelope.FormatTime(env.Now.Now())
	}
	if _, err := env.Hub.DB.ExecContext(context.Background(), `UPDATE event_subscriptions SET paused_reason=?, verified_at=?, expires_at=?, attempts=?, last_status=? WHERE sub_id=?`, reason, verification, expiry, attempts, status, id); err != nil {
		t.Fatal(err)
	}
}

func subscription(t *testing.T, env *testhub.Env, id string) opapi.Subscription {
	t.Helper()
	rows, err := opview.New(env.Hub).Subscriptions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if row.SubID == id {
			return row
		}
	}
	t.Fatalf("subscription %s missing from %+v", id, rows)
	return opapi.Subscription{}
}

func ptr[T any](v T) *T { return &v }

func TestHealthTable(t *testing.T) {
	env := testhub.Start(t)
	now := env.Now.Now()
	later, boundary, expired := now.Add(time.Hour), now.Add(10*time.Minute), now
	cases := []struct {
		name       string
		reason     *string
		verified   bool
		expires    *time.Time
		attempts   int
		status     *int
		want, note string
	}{
		{"ok before boundary", nil, true, ptr(boundary.Add(time.Millisecond)), 0, nil, "ok", ""},
		{"ok 200", nil, true, &later, 0, ptr(200), "ok", ""},
		{"ok 299", nil, true, &later, 0, ptr(299), "ok", ""},
		{"attempt", nil, true, &later, 1, ptr(200), "failing", "Delivery is retrying. Check the callback; repeated failures pause the subscription."},
		{"status 199", nil, true, &later, 0, ptr(199), "failing", "Delivery is retrying. Check the callback; repeated failures pause the subscription."},
		{"status 300", nil, true, &later, 0, ptr(300), "failing", "Delivery is retrying. Check the callback; repeated failures pause the subscription."},
		{"at refresh boundary", nil, true, &boundary, 2, ptr(500), "late", "Refresh is due. ChatGPT must re-subscribe before expiry to keep delivery flowing."},
		{"at expiry", nil, true, &expired, 2, ptr(500), "expired", "The subscription expired. Ask the dot to subscribe again; queued events resume from its cursor."},
		{"past expiry", nil, true, ptr(now.Add(-time.Millisecond)), 0, nil, "expired", "The subscription expired. Ask the dot to subscribe again; queued events resume from its cursor."},
		{"unverified before expired", nil, false, &expired, 2, ptr(500), "unverified", "Waiting for callback verification. No events are sent until a subscribe challenge succeeds."},
		{"no expiry ok", nil, true, nil, 0, nil, "ok", ""},
		{"no expiry failing", nil, true, nil, 1, nil, "failing", "Delivery is retrying. Check the callback; repeated failures pause the subscription."},
		{"token pause wins", ptr("token_expired"), false, &expired, 2, ptr(500), "paused_token", tokenNote},
		{"failed pause wins", ptr("delivery_failed"), false, &expired, 2, ptr(500), "paused_failed", failedNote},
		{"operator pause wins", ptr("operator"), false, &expired, 2, ptr(500), "paused_operator", operatorNote},
		{"unauthorized pause wins", ptr("unauthorized"), false, &expired, 2, ptr(500), "paused_auth", authNote},
		{"rotated pause wins", ptr("credential_rotated"), false, &expired, 2, ptr(500), "paused_auth", rotatedNote},
		{"future reason", ptr("future_reason"), true, &later, 0, nil, "paused_auth", "Paused: future_reason."},
		{"non-null empty reason", ptr(""), true, &later, 0, nil, "paused_auth", "Paused: ."},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sub := newSubscription(t, env, tc.name, "https://example.com/secret-path")
			setHealth(t, env, sub.ID, tc.reason, tc.verified, tc.expires, tc.attempts, tc.status)
			got := subscription(t, env, sub.ID)
			if got.Health != tc.want || got.HealthNote != tc.note {
				t.Fatalf("health=(%q,%q), want (%q,%q)", got.Health, got.HealthNote, tc.want, tc.note)
			}
			if tc.reason != nil && got.Health == "ok" {
				t.Fatal("paused subscription reads healthy")
			}
			if tc.expires == nil && got.RefreshBefore != nil {
				t.Fatalf("no expiry: refresh_before=%v", got.RefreshBefore)
			}
		})
	}
}

func subscribeRPC(t *testing.T, env *testhub.Env, bearer, callback string, ttl time.Duration) (string, string) {
	t.Helper()
	body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "events/subscribe", "params": map[string]any{"name": events.Name, "arguments": map[string]any{}, "delivery": map[string]any{"mode": "webhook", "url": callback, "secret": webhook.NewSecret()}, "ttlMs": ttl.Milliseconds()}})
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest("POST", env.URL+"/mcp", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+bearer)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("MCP-Protocol-Version", "2026-07-28")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	var reply struct {
		Result struct {
			ID            string `json:"id"`
			RefreshBefore string `json:"refreshBefore"`
		} `json:"result"`
		Error json.RawMessage `json:"error"`
	}
	if resp.StatusCode != 200 || json.Unmarshal(raw, &reply) != nil || reply.Result.ID == "" {
		t.Fatalf("subscribe: %d %s", resp.StatusCode, raw)
	}
	return reply.Result.ID, reply.Result.RefreshBefore
}

func TestRefreshBeforeMatchesSubscribe(t *testing.T) {
	if events.RefreshMargin != 10*time.Minute {
		t.Fatalf("refresh margin=%s", events.RefreshMargin)
	}
	env := testhub.Start(t)
	_, bearer := env.NewPeer("session", "subscriber")
	cb := testhub.NewCallback(t)
	for _, ttl := range []time.Duration{24 * time.Hour, 5 * time.Minute} {
		id, refresh := subscribeRPC(t, env, bearer, cb.URL, ttl)
		got := subscription(t, env, id)
		want := envelope.FormatTime(env.Now.Now().Add(ttl - events.RefreshMargin))
		// Under the margin the subscribe reply clamps to now, so a dot is never
		// handed a time already past; the operator view keeps the raw deadline.
		wantReply := want
		if ttl < events.RefreshMargin {
			wantReply = envelope.FormatTime(env.Now.Now())
		}
		if got.RefreshBefore == nil || *got.RefreshBefore != want || refresh != wantReply {
			t.Fatalf("ttl=%s snapshot=%v subscribe=%q want=%q", ttl, got.RefreshBefore, refresh, want)
		}
		if got.ExpiresAt == nil || *got.ExpiresAt != envelope.FormatTime(env.Now.Now().Add(ttl)) {
			t.Fatalf("expiry=%v", got.ExpiresAt)
		}
	}
}

func TestLastRefreshOnResubscribe(t *testing.T) {
	env := testhub.Start(t)
	cb := testhub.NewCallback(t)
	sub := newSubscription(t, env, "subscriber", cb.URL)
	store := events.NewStore(env.Hub)
	if got := subscription(t, env, sub.ID); got.LastRefreshAt != nil {
		t.Fatalf("first Upsert refresh=%v", got.LastRefreshAt)
	}
	if err := store.MarkVerified(context.Background(), sub.ID); err != nil {
		t.Fatal(err)
	}
	env.Now.Advance(time.Hour)
	want := envelope.FormatTime(env.Now.Now())
	refreshed, created, err := store.Upsert(context.Background(), sub, webhook.NewSecret(), 0, time.Time{})
	if err != nil || created || refreshed.ID != sub.ID {
		t.Fatalf("refresh: created=%v sub=%+v err=%v", created, refreshed, err)
	}
	if got := subscription(t, env, sub.ID); got.LastRefreshAt == nil || *got.LastRefreshAt != want {
		t.Fatalf("refresh=%v want=%s", got.LastRefreshAt, want)
	}
	env.Now.Advance(time.Minute)
	resp, raw := env.Do(env.OperatorSecret, "POST", "/v1/messages", map[string]any{"idempotency_key": "health-delivery", "priority": "wake", "recipients": []string{sub.PeerID}, "content": map[string]any{"text": "test"}})
	if resp.StatusCode != 202 {
		t.Fatalf("send: %d %s", resp.StatusCode, raw)
	}
	if _, err := env.Events.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(cb.Posts()) != 1 {
		t.Fatalf("deliveries=%d want=1", len(cb.Posts()))
	}
	got := subscription(t, env, sub.ID)
	if got.LastRefreshAt == nil || *got.LastRefreshAt != want {
		t.Fatalf("delivery changed refresh=%v want=%s", got.LastRefreshAt, want)
	}
	if got.LastDeliveryAt == nil || *got.LastDeliveryAt != envelope.FormatTime(env.Now.Now()) || got.LastStatus == nil || *got.LastStatus != 200 {
		t.Fatalf("delivery health=%+v", got)
	}
}

func TestAdminSubscriptionsAdditive(t *testing.T) {
	env := testhub.Start(t)
	resp, raw := env.Do(env.OperatorSecret, "GET", "/v1/admin/subscriptions", nil)
	if resp.StatusCode != 200 || strings.TrimSpace(string(raw)) != "[]" {
		t.Fatalf("empty list: %d %s", resp.StatusCode, raw)
	}
	sub := newSubscription(t, env, "subscriber", "https://user:password@example.com:8443/private-key?token=secret")
	_, peerBearer := env.NewPeer("session", "other")
	_, principalBearer := env.NewPrincipal("other")
	for _, tc := range []struct {
		bearer string
		code   int
	}{{"", 401}, {peerBearer, 403}, {principalBearer, 403}} {
		resp, _ := env.Do(tc.bearer, "GET", "/v1/admin/subscriptions", nil)
		if resp.StatusCode != tc.code {
			t.Fatalf("auth status=%d want=%d", resp.StatusCode, tc.code)
		}
	}
	check := func() {
		t.Helper()
		stored, err := events.NewStore(env.Hub).ListHealth(context.Background())
		if err != nil || len(stored) != 1 {
			t.Fatalf("stored=%+v err=%v", stored, err)
		}
		h := stored[0]
		resp, raw := env.Do(env.OperatorSecret, "GET", "/v1/admin/subscriptions", nil)
		var rows []map[string]any
		if resp.StatusCode != 200 || json.Unmarshal(raw, &rows) != nil || len(rows) != 1 {
			t.Fatalf("bare array: %d %s", resp.StatusCode, raw)
		}
		expected, err := json.Marshal(map[string]any{"sub_id": h.ID, "principal": h.PrincipalKind + ":" + h.PrincipalID, "peer_id": h.PeerID, "url": "example.com", "name": h.Name, "cursor": h.Cursor.String(), "expires_at": h.ExpiresAt, "verified_at": h.VerifiedAt, "paused_reason": h.PausedReason, "last_refresh_at": h.LastRefreshAt, "last_delivery_at": h.LastDeliveryAt, "last_status": h.LastStatus, "attempts": h.Attempts})
		if err != nil {
			t.Fatal(err)
		}
		var want map[string]any
		if err := json.Unmarshal(expected, &want); err != nil {
			t.Fatal(err)
		}
		got := rows[0]
		for _, key := range []string{"health", "health_note", "refresh_before"} {
			if _, ok := got[key]; !ok {
				t.Errorf("missing %s", key)
			}
			delete(got, key)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("legacy fields changed: got=%v want=%v", got, want)
		}
		for _, secret := range []string{"password", "private-key", "token=secret", "secret_enc", "token_hash", "8443", "https://"} {
			if strings.Contains(string(raw), secret) {
				t.Fatalf("leaked %q in %s", secret, raw)
			}
		}
	}
	check()
	now := env.Now.Now()
	if _, err := env.Hub.DB.ExecContext(context.Background(), `UPDATE event_subscriptions SET verified_at=?, paused_reason='operator', last_refresh_at=?, last_delivery_at=?, last_status=503, attempts=3 WHERE sub_id=?`, envelope.FormatTime(now), envelope.FormatTime(now), envelope.FormatTime(now), sub.ID); err != nil {
		t.Fatal(err)
	}
	check()
}

func TestAttentionActions(t *testing.T) {
	env := testhub.Start(t)
	empty, err := opview.New(env.Hub).Attention(context.Background())
	if err != nil || empty == nil || len(empty) != 0 {
		t.Fatalf("empty attention = %+v, err = %v", empty, err)
	}
	later, expired := env.Now.Now().Add(time.Hour), env.Now.Now()
	cases := []struct {
		name                 string
		reason               *string
		verified             bool
		expiry               *time.Time
		attempts             int
		health, action, note string
	}{
		{"operator", ptr("operator"), true, &later, 0, "paused_operator", "resume", operatorNote},
		{"token", ptr("token_expired"), true, &later, 0, "paused_token", "", tokenNote},
		{"failed", ptr("delivery_failed"), true, &later, 0, "paused_failed", "resume", failedNote},
		{"auth", ptr("unauthorized"), true, &later, 0, "paused_auth", "", authNote},
		{"rotated", ptr("credential_rotated"), true, &later, 0, "paused_auth", "", rotatedNote},
		{"unknown", ptr("future"), true, &later, 0, "paused_auth", "", "Paused: future."},
		{"expired", nil, true, &expired, 0, "expired", "", "The subscription expired. Ask the dot to subscribe again; queued events resume from its cursor."},
		{"late", nil, true, ptr(env.Now.Now().Add(10 * time.Minute)), 0, "late", "", "Refresh is due. ChatGPT must re-subscribe before expiry to keep delivery flowing."},
		{"failing", nil, true, &later, 1, "failing", "", "Delivery is retrying. Check the callback; repeated failures pause the subscription."},
		{"ok", nil, true, &later, 0, "ok", "", ""},
		{"unverified", nil, false, &later, 0, "unverified", "", ""},
	}
	expected := map[string]opapi.Attention{}
	for _, tc := range cases {
		sub := newSubscription(t, env, tc.name, "https://example.com/hook")
		setHealth(t, env, sub.ID, tc.reason, tc.verified, tc.expiry, tc.attempts, nil)
		if tc.health != "ok" && tc.health != "unverified" {
			expected[sub.ID] = opapi.Attention{Kind: "subscription", ID: sub.ID, PeerID: sub.PeerID, Health: tc.health, Action: tc.action, Summary: tc.name + " · " + tc.note}
		}
	}
	rows, err := opview.New(env.Hub).Attention(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != len(expected) {
		t.Fatalf("attention=%+v want %d entries", rows, len(expected))
	}
	rank := map[string]int{"paused_auth": 0, "paused_token": 1, "paused_failed": 2, "expired": 3, "late": 4, "failing": 5, "paused_operator": 6}
	previous := -1
	for _, row := range rows {
		want, ok := expected[row.ID]
		if !ok {
			t.Fatalf("unexpected or duplicate entry %+v", row)
		}
		if _, err := envelope.ParseTime(row.At); err != nil {
			t.Fatalf("attention at=%q: %v", row.At, err)
		}
		want.At = row.At
		if row != want {
			t.Fatalf("attention=%+v want=%+v", row, want)
		}
		if rank[row.Health] < previous {
			t.Fatalf("not worst-first: %+v", rows)
		}
		previous = rank[row.Health]
		delete(expected, row.ID)
	}
	if rows[len(rows)-1].Health != "paused_operator" {
		t.Fatalf("operator pause must be last: %+v", rows)
	}
}

func TestInboxCarriesAttention(t *testing.T) {
	env := testhub.Start(t)
	sub := newSubscription(t, env, "paused dot", "https://example.com/hook")
	if err := events.NewStore(env.Hub).Pause(context.Background(), sub.ID); err != nil {
		t.Fatal(err)
	}
	resp, raw := env.Do(env.OperatorSecret, "GET", "/v1/admin/inbox", nil)
	if resp.StatusCode != 200 {
		t.Fatalf("GET /v1/admin/inbox: %d %s", resp.StatusCode, raw)
	}
	var inbox opapi.Inbox
	if err := json.Unmarshal(raw, &inbox); err != nil {
		t.Fatal(err)
	}
	want, err := opview.New(env.Hub).Attention(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(inbox.Attention) != 1 || !reflect.DeepEqual(inbox.Attention, want) {
		t.Fatalf("inbox attention=%+v want=%+v", inbox.Attention, want)
	}
	if inbox.Attention[0].ID != sub.ID || inbox.Attention[0].Action != "resume" || inbox.Attention[0].Summary != "paused dot · "+operatorNote {
		t.Fatalf("inbox entry=%+v", inbox.Attention[0])
	}
}
