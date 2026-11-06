package events_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/hub"
	"github.com/XertroV/vecnet/internal/testhub"
)

func TestEventsPushRecordsUsage(t *testing.T) {
	env := testhub.Start(t)
	dot, _ := env.NewPeer("dot", "d")
	_, senderSecret := env.NewPeer("session", "s")
	cb := testhub.NewCallback(t)
	sub := subscribe(t, env, cb, dot, nil, time.Time{})
	send(t, env, senderSecret, dot, "short", "wake", "a")
	send(t, env, senderSecret, dot, "long", "wake", strings.Repeat("b", 2000))
	if n := step(t, env); n != 2 {
		t.Fatalf("step = %d, want 2", n)
	}
	posts := wantPosts(t, cb, 2)
	got, err := env.Hub.RouteUsages(context.Background(), []string{sub.ID})
	if err != nil {
		t.Fatal(err)
	}
	u, ok := got[sub.ID]
	if !ok || u.Requests != 2 || u.Successes != 2 || u.Failures != 0 {
		t.Fatalf("usage = %+v ok=%v", u, ok)
	}
	if u.BodyBytes.Samples != 2 || u.BodyBytes.P99 < u.BodyBytes.P95 {
		t.Fatalf("body = %+v", u.BodyBytes)
	}
	var kind string
	if err := env.Hub.DB.QueryRow(`SELECT kind FROM route_usage WHERE route_key = ?`, sub.ID).Scan(&kind); err != nil || kind != hub.RouteKindEvents {
		t.Fatalf("kind = %q %v", kind, err)
	}
	var n int
	if err := env.Hub.DB.QueryRow(`SELECT count(*) FROM route_usage_samples WHERE route_key = ? AND body_bytes IN (?, ?)`,
		sub.ID, len(posts[0].Body), len(posts[1].Body)).Scan(&n); err != nil || n != 2 {
		t.Fatalf("samples matching request sizes = %d, %v (posts %d and %d)", n, err, len(posts[0].Body), len(posts[1].Body))
	}

	resp, raw := env.Do(env.OperatorSecret, "GET", "/v1/admin/subscriptions", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("subscriptions %d %s", resp.StatusCode, raw)
	}
	var rows []struct {
		SubID string          `json:"sub_id"`
		Usage *hub.RouteUsage `json:"usage"`
	}
	if err := json.Unmarshal(raw, &rows); err != nil || len(rows) != 1 || rows[0].SubID != sub.ID || rows[0].Usage == nil || rows[0].Usage.Requests != 2 {
		t.Fatalf("admin usage = %s %v", raw, err)
	}
	_, peerSecret := env.NewPeer("session", "other")
	if resp, _ := env.Do(peerSecret, "GET", "/v1/admin/subscriptions", nil); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("peer status = %d, want 403", resp.StatusCode)
	}
}

func TestEventsStatsFailureDoesNotFailDelivery(t *testing.T) {
	env := testhub.Start(t)
	dot, _ := env.NewPeer("dot", "d")
	_, senderSecret := env.NewPeer("session", "s")
	cb := testhub.NewCallback(t)
	subscribe(t, env, cb, dot, nil, time.Time{})
	dly := send(t, env, senderSecret, dot, "x", "wake", "CANARY-BODY")
	if _, err := env.Hub.DB.Exec(`DROP TABLE route_usage_samples`); err != nil {
		t.Fatal(err)
	}
	if _, err := env.Hub.DB.Exec(`DROP TABLE route_usage`); err != nil {
		t.Fatal(err)
	}
	if n := step(t, env); n != 1 {
		t.Fatalf("step = %d, want 1", n)
	}
	if len(cb.Posts()) != 1 {
		t.Fatalf("posts = %d", len(cb.Posts()))
	}
	var state string
	if err := env.Hub.DB.QueryRow(`SELECT state FROM deliveries WHERE dly_id = ?`, dly).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "accepted" {
		t.Fatalf("state = %q, want accepted", state)
	}
}
