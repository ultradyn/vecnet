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
	stats, err := env.Hub.RouteStats(context.Background(), sub.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(stats.Routes) != 1 || stats.Routes[0].Requests != 2 || stats.Routes[0].OK != 2 || stats.Routes[0].Kind != hub.RouteKindEvents {
		t.Fatalf("stats = %+v", stats.Routes)
	}
	if stats.Routes[0].Pushed != 2 {
		t.Fatalf("pushed = %d, want 2", stats.Routes[0].Pushed)
	}
	var n int
	if err := env.Hub.DB.QueryRow(`SELECT count(*) FROM route_attempts WHERE route_id = ? AND body_bytes IN (?, ?)`,
		sub.ID, len(posts[0].Body), len(posts[1].Body)).Scan(&n); err != nil || n != 2 {
		t.Fatalf("attempts matching request sizes = %d, %v (posts %d and %d)", n, err, len(posts[0].Body), len(posts[1].Body))
	}
	resp, raw := env.Do(env.OperatorSecret, "GET", "/v1/admin/route-stats?id="+dot, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("route-stats %d %s", resp.StatusCode, raw)
	}
	var body hub.RouteStats
	if err := json.Unmarshal(raw, &body); err != nil || body.Deliveries.Pushed < 2 {
		t.Fatalf("admin stats = %s %v", raw, err)
	}
	_, peerSecret := env.NewPeer("session", "other")
	if resp, _ := env.Do(peerSecret, "GET", "/v1/admin/route-stats?id="+dot, nil); resp.StatusCode != http.StatusForbidden {
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
	if _, err := env.Hub.DB.Exec(`DROP TABLE route_attempts`); err != nil {
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
