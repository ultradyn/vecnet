package push_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/XertroV/vecnet/internal/hub"
	"github.com/XertroV/vecnet/internal/push"
	"github.com/XertroV/vecnet/internal/testhub"
)

// A verify whose challenge went out before the route's URL changed must not
// mark the new, never-challenged URL verified: the old receiver's echo
// proves nothing about the new one (PROTOCOL §3.2, route update).
func TestVerifyRacingUpdateLeavesNewURLUnverified(t *testing.T) {
	env := testhub.Start(t)
	ctx := context.Background()
	owner, _ := env.NewPeer("session", "race-owner")

	got, release := make(chan struct{}), make(chan struct{})
	old := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Challenge string `json:"challenge"`
		}
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		close(got)
		<-release // hold the answer while the URL changes
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"challenge": body.Challenge})
	}))
	t.Cleanup(old.Close)

	id, _, err := env.Hub.AddRoute(ctx, "peer", owner, old.URL+"/hook")
	if err != nil {
		t.Fatal(err)
	}
	w := &push.Worker{Hub: env.Hub, Now: env.Now.Now}
	done := make(chan error, 1)
	go func() { done <- w.Verify(ctx, id) }()
	<-got
	next := "http://127.0.0.1:9/never-challenged"
	if err := env.Hub.UpdateRoute(ctx, id, hub.RouteUpdate{URL: &next}); err != nil {
		t.Fatal(err)
	}
	close(release)
	err = <-done
	var he *hub.APIError
	if !errors.As(err, &he) || he.Status != http.StatusConflict || he.Code != "route_changed" {
		t.Fatalf("verify racing an update = %v, want 409 route_changed", err)
	}
	routes, lerr := env.Hub.ListRoutes(ctx)
	if lerr != nil || len(routes) != 1 || routes[0].VerifiedAt != nil {
		t.Fatalf("the new URL was marked verified: %+v, %v", routes, lerr)
	}
	var n int
	if err := env.Hub.DB.QueryRow(`SELECT count(*) FROM audit WHERE action = 'route_verified'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("route_verified audit rows = %d, %v; want 0", n, err)
	}
}
