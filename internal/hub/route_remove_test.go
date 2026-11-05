package hub_test

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/XertroV/vecnet/internal/hub"
	"github.com/XertroV/vecnet/internal/testhub"
)

func TestRemoveRouteKeepsQueuedDeliveries(t *testing.T) {
	for _, ownerKind := range []string{"peer", "principal"} {
		t.Run(ownerKind, func(t *testing.T) {
			env := testhub.Start(t)
			ctx := context.Background()
			owner, _ := env.NewPeer("session", "route-owner")
			_, senderSecret := env.NewPeer("session", "sender")
			ownerID := owner
			if ownerKind == "principal" {
				if err := env.Hub.DB.QueryRow(`SELECT prn_id FROM peers WHERE peer_id = ?`, owner).Scan(&ownerID); err != nil {
					t.Fatal(err)
				}
			}
			id, _, err := env.Hub.AddRoute(ctx, ownerKind, ownerID, "http://127.0.0.1:9/hook")
			if err != nil {
				t.Fatal(err)
			}
			if err := env.Hub.MarkRouteVerified(ctx, id); err != nil {
				t.Fatal(err)
			}
			sent := mustSend(t, env, senderSecret, req("before-remove", owner))
			deliveryID := sent.Deliveries[0].DeliveryID
			before := deliveryEvents(t, env, owner)
			if err := env.Hub.RemoveRoute(ctx, id); err != nil {
				t.Fatal(err)
			}
			if routes, err := env.Hub.ListRoutes(ctx); err != nil || len(routes) != 0 {
				t.Fatalf("routes after remove = %+v, %v", routes, err)
			}
			if state, _ := deliveryState(t, env, deliveryID); state != "queued" {
				t.Fatalf("removed route delivery state = %s, want queued", state)
			}
			if due, err := env.Hub.ClaimDue(ctx, env.Now.Now(), 10); err != nil || len(due) != 0 {
				t.Fatalf("no-route claims = %+v, %v, want none", due, err)
			}
			if events := deliveryEvents(t, env, owner); len(events) != len(before) || string(events[0]) != string(before[0]) {
				t.Fatalf("remove changed the pull inbox: %s", events)
			}
			if keys, err := env.Hub.RouteKeys(ctx, id, env.Now.Now()); err != nil || len(keys) != 0 {
				t.Fatalf("removed route keys = %v, %v", keys, err)
			}
			if n := count(t, env, `SELECT count(*) FROM audit WHERE actor = 'operator' AND action = 'route_remove' AND json_extract(target_ids, '$[0]') = ?`, id); n != 1 {
				t.Fatalf("route_remove audit rows = %d, want 1", n)
			}
			if err := env.Hub.RemoveRoute(ctx, id); err == nil {
				t.Fatal("removing unknown route succeeded")
			} else {
				var he *hub.APIError
				if !errors.As(err, &he) || he.Status != http.StatusNotFound || he.Code != "not_found" {
					t.Fatalf("unknown route removal = %v, want 404 not_found", err)
				}
			}
			if n := count(t, env, `SELECT count(*) FROM audit WHERE action = 'route_remove'`); n != 1 {
				t.Fatalf("unknown route removal added an audit row: %d", n)
			}
			// A later route can pick up pending work exactly as for an owner
			// that had no route when the Delivery was created.
			next, _, err := env.Hub.AddRoute(ctx, ownerKind, ownerID, "http://127.0.0.1:9/new-hook")
			if err != nil {
				t.Fatal(err)
			}
			if err := env.Hub.MarkRouteVerified(ctx, next); err != nil {
				t.Fatal(err)
			}
			if due, err := env.Hub.ClaimDue(ctx, env.Now.Now(), 10); err != nil || len(due) != 1 || due[0].DeliveryID != deliveryID || due[0].RouteID != next {
				t.Fatalf("replacement route claims = %+v, %v", due, err)
			}
		})
	}
}
