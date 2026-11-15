package hub_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/hub"
	"github.com/XertroV/vecnet/internal/testhub"
)

func apiErr(t *testing.T, err error, status int, code, field string) {
	t.Helper()
	var he *hub.APIError
	if !errors.As(err, &he) || he.Status != status || he.Code != code {
		t.Fatalf("err = %v, want %d %s", err, status, code)
	}
	if field != "" && he.Details["field"] != field {
		t.Fatalf("err field = %v, want %s", he.Details["field"], field)
	}
}

func routeRow(t *testing.T, env *testhub.Env, id string) (url string, verified bool, keyEnc string) {
	t.Helper()
	var v, k *string
	if err := env.Hub.DB.QueryRow(`SELECT url, verified_at, key_enc FROM routes WHERE route_id = ?`, id).Scan(&url, &v, &k); err != nil {
		t.Fatal(err)
	}
	if k != nil {
		keyEnc = *k
	}
	return url, v != nil, keyEnc
}

// An http route's URL changes in place: same id, owner, keys and queued
// deliveries; the route is unverified until its new receiver answers, so
// nothing is claimed through it meanwhile, and nothing falls back to pull.
func TestUpdateRouteHTTPKeepsIdentityAndUnverifies(t *testing.T) {
	env := testhub.Start(t)
	ctx := context.Background()
	owner, _ := env.NewPeer("session", "route-owner")
	_, senderSecret := env.NewPeer("session", "sender")
	id, whsec, err := env.Hub.AddRoute(ctx, "peer", owner, "http://127.0.0.1:9/old-hook")
	if err != nil {
		t.Fatal(err)
	}
	if err := env.Hub.MarkRouteVerified(ctx, id); err != nil {
		t.Fatal(err)
	}
	sent := mustSend(t, env, senderSecret, req("before-update", owner))
	dly := sent.Deliveries[0].DeliveryID

	next := "http://127.0.0.1:9/new-hook?token=private"
	if err := env.Hub.UpdateRoute(ctx, id, hub.RouteUpdate{URL: &next}); err != nil {
		t.Fatal(err)
	}
	url, verified, _ := routeRow(t, env, id)
	if url != next || verified {
		t.Fatalf("after update url=%q verified=%v, want new url and unverified", url, verified)
	}
	var peerRoute, dlyRoute string
	if err := env.Hub.DB.QueryRow(`SELECT route_id FROM peers WHERE peer_id = ?`, owner).Scan(&peerRoute); err != nil || peerRoute != id {
		t.Fatalf("peer route = %q, %v; want %s", peerRoute, err, id)
	}
	if err := env.Hub.DB.QueryRow(`SELECT route_id FROM deliveries WHERE dly_id = ?`, dly).Scan(&dlyRoute); err != nil || dlyRoute != id {
		t.Fatalf("delivery route = %q, %v; want %s", dlyRoute, err, id)
	}
	if keys, err := env.Hub.RouteKeys(ctx, id, env.Now.Now()); err != nil || len(keys) != 1 || keys[0] != whsec {
		t.Fatalf("route keys after update = %v, %v; want the same whsec", keys, err)
	}
	if due, err := env.Hub.ClaimDue(ctx, env.Now.Now(), 10); err != nil || len(due) != 0 {
		t.Fatalf("an unverified route was claimed: %+v, %v", due, err)
	}
	// Audit names the field, never the URL.
	var detail string
	if err := env.Hub.DB.QueryRow(`SELECT coalesce(detail,'') FROM audit WHERE action = 'route_update' AND json_extract(target_ids, '$[0]') = ?`, id).Scan(&detail); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(detail, `"url"`) || strings.Contains(detail, "new-hook") || strings.Contains(detail, "private") {
		t.Fatalf("route_update detail = %s", detail)
	}
	// Verified again (the new receiver answered): the queued delivery pushes there.
	if err := env.Hub.MarkRouteVerified(ctx, id); err != nil {
		t.Fatal(err)
	}
	if due, err := env.Hub.ClaimDue(ctx, env.Now.Now(), 10); err != nil || len(due) != 1 || due[0].DeliveryID != dly || due[0].URL != next {
		t.Fatalf("claims after re-verify = %+v, %v", due, err)
	}

	// The same URL again is a no-op: still verified, no second audit row.
	same := next
	if err := env.Hub.UpdateRoute(ctx, id, hub.RouteUpdate{URL: &same}); err != nil {
		t.Fatal(err)
	}
	if _, verified, _ := routeRow(t, env, id); !verified {
		t.Fatal("an unchanged URL cleared verification")
	}
	if n := count(t, env, `SELECT count(*) FROM audit WHERE action = 'route_update'`); n != 1 {
		t.Fatalf("route_update audit rows = %d, want 1", n)
	}
}

func TestUpdateRouteRefusals(t *testing.T) {
	env := testhub.Start(t)
	ctx := context.Background()
	owner, _ := env.NewPeer("session", "route-owner")
	id, _, err := env.Hub.AddRoute(ctx, "peer", owner, "http://127.0.0.1:9/hook")
	if err != nil {
		t.Fatal(err)
	}
	key := fakeRoutineKey(t)
	bad, good := "ftp://example.com/x", "http://127.0.0.1:9/x"
	cases := []struct {
		name               string
		route              string
		u                  hub.RouteUpdate
		status             int
		code, field, leaks string
	}{
		{"empty", id, hub.RouteUpdate{}, http.StatusUnprocessableEntity, "invalid_value", "url", ""},
		{"unknown", "route_nope", hub.RouteUpdate{URL: &good}, http.StatusNotFound, "not_found", "", ""},
		{"bad url", id, hub.RouteUpdate{URL: &bad}, http.StatusUnprocessableEntity, "invalid_url", "url", "ftp://example.com"},
		{"key on http", id, hub.RouteUpdate{Key: &key}, http.StatusUnprocessableEntity, "invalid_value", "key", key},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := env.Hub.UpdateRoute(ctx, c.route, c.u)
			apiErr(t, err, c.status, c.code, c.field)
			if c.leaks != "" && strings.Contains(err.Error(), c.leaks) {
				t.Fatalf("error quotes the input: %v", err)
			}
		})
	}
	if url, _, _ := routeRow(t, env, id); url != "http://127.0.0.1:9/hook" {
		t.Fatalf("a refused update changed the route: %s", url)
	}
	if n := count(t, env, `SELECT count(*) FROM audit WHERE action = 'route_update'`); n != 0 {
		t.Fatalf("a refused update was audited: %d", n)
	}
}

// A grokbot route takes a new routine URL (on the routine pattern) and key
// together or apart; it stays live, and the key is sealed, never stored
// plain or audited.
func TestUpdateRouteGrokbot(t *testing.T) {
	env := testhub.Start(t)
	ctx := context.Background()
	owner, _ := env.NewPeer("grokbot", "grok-route-owner")
	first := fakeRoutineURL(t)
	id, err := env.Hub.AddGrokbotRoute(ctx, "peer", owner, first, fakeRoutineKey(t))
	if err != nil {
		t.Fatal(err)
	}
	_, _, before := routeRow(t, env, id)

	nextURL, nextKey := fakeRoutineURL(t), fakeRoutineKey(t)
	if err := env.Hub.UpdateRoute(ctx, id, hub.RouteUpdate{URL: &nextURL, Key: &nextKey}); err != nil {
		t.Fatal(err)
	}
	url, verified, sealed := routeRow(t, env, id)
	if url != nextURL || !verified || sealed == before || strings.Contains(sealed, nextKey) {
		t.Fatalf("grokbot update url=%q verified=%v sealed changed=%v", url, verified, sealed != before)
	}
	if got, err := env.Hub.GrokbotKey(ctx, id); err != nil || got != nextKey {
		t.Fatalf("GrokbotKey after update = %v (match %v)", err, got == nextKey)
	}
	var detail string
	if err := env.Hub.DB.QueryRow(`SELECT coalesce(detail,'') FROM audit WHERE action = 'route_update'`).Scan(&detail); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(detail, nextKey) || strings.Contains(detail, nextURL) || !strings.Contains(detail, `"key"`) || !strings.Contains(detail, `"url"`) {
		t.Fatalf("route_update detail = %s", detail)
	}

	// Key only.
	third := fakeRoutineKey(t)
	if err := env.Hub.UpdateRoute(ctx, id, hub.RouteUpdate{Key: &third}); err != nil {
		t.Fatal(err)
	}
	if got, _ := env.Hub.GrokbotKey(ctx, id); got != third {
		t.Fatal("key-only update did not replace the key")
	}
	// Off the routine pattern, or a bad key: refused, nothing changes.
	off := "https://example.com/automations/webhook/x"
	apiErr(t, env.Hub.UpdateRoute(ctx, id, hub.RouteUpdate{URL: &off}), 422, "invalid_url", "url")
	junk := "Bearer " + fakeRoutineKey(t)
	err = env.Hub.UpdateRoute(ctx, id, hub.RouteUpdate{URL: &nextURL, Key: &junk})
	apiErr(t, err, 422, "invalid_key", "key")
	if strings.Contains(err.Error(), junk) {
		t.Fatal("invalid_key quotes the key")
	}
	if url, _, _ := routeRow(t, env, id); url != nextURL {
		t.Fatalf("a refused update changed the URL: %s", url)
	}
	if got, _ := env.Hub.GrokbotKey(ctx, id); got != third {
		t.Fatal("a refused update changed the key")
	}
}

// A delivery backing off after failures against a broken URL is due again
// as soon as the route is fixed (and, for http, verified), with its
// attempts kept; not after up to an hour's backoff.
func TestUpdateRouteReschedulesBackedOffDeliveries(t *testing.T) {
	env := testhub.Start(t)
	ctx := context.Background()
	owner, _ := env.NewPeer("session", "route-owner")
	_, senderSecret := env.NewPeer("session", "sender")
	id, _, err := env.Hub.AddRoute(ctx, "peer", owner, "http://127.0.0.1:9/broken")
	if err != nil {
		t.Fatal(err)
	}
	if err := env.Hub.MarkRouteVerified(ctx, id); err != nil {
		t.Fatal(err)
	}
	sent := mustSend(t, env, senderSecret, req("backed-off", owner))
	dly := sent.Deliveries[0].DeliveryID
	later := env.Now.Now().Add(45 * time.Minute)
	if _, err := env.Hub.DB.Exec(`UPDATE deliveries SET next_attempt_at = ?, attempt = 3, attempts_left = 5 WHERE dly_id = ?`, envelope.FormatTime(later), dly); err != nil {
		t.Fatal(err)
	}
	if due, err := env.Hub.ClaimDue(ctx, env.Now.Now(), 10); err != nil || len(due) != 0 {
		t.Fatalf("backed-off delivery claimed early: %+v, %v", due, err)
	}
	next := "http://127.0.0.1:9/fixed"
	if err := env.Hub.UpdateRoute(ctx, id, hub.RouteUpdate{URL: &next}); err != nil {
		t.Fatal(err)
	}
	if err := env.Hub.MarkRouteVerified(ctx, id); err != nil {
		t.Fatal(err)
	}
	due, err := env.Hub.ClaimDue(ctx, env.Now.Now(), 10)
	if err != nil || len(due) != 1 || due[0].DeliveryID != dly || due[0].URL != next {
		t.Fatalf("after the fix claims = %+v, %v; want the backed-off delivery at the new URL", due, err)
	}
	var left int
	if err := env.Hub.DB.QueryRow(`SELECT attempts_left FROM deliveries WHERE dly_id = ?`, dly).Scan(&left); err != nil || left != 5 {
		t.Fatalf("attempts_left = %d, %v; want 5 kept", left, err)
	}
}

// Error order: existence and kind come before the key's own check, so a
// junk key on an http route is "only a grokbot route takes a key", and on
// an unknown route is 404.
func TestUpdateRouteErrorOrder(t *testing.T) {
	env := testhub.Start(t)
	ctx := context.Background()
	owner, _ := env.NewPeer("session", "route-owner")
	id, _, err := env.Hub.AddRoute(ctx, "peer", owner, "http://127.0.0.1:9/hook")
	if err != nil {
		t.Fatal(err)
	}
	junk := "not-a-key"
	apiErr(t, env.Hub.UpdateRoute(ctx, id, hub.RouteUpdate{Key: &junk}), http.StatusUnprocessableEntity, "invalid_value", "key")
	apiErr(t, env.Hub.UpdateRoute(ctx, "route_nope", hub.RouteUpdate{Key: &junk}), http.StatusNotFound, "not_found", "")
}
