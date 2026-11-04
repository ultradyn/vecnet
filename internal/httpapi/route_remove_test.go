package httpapi_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/XertroV/vecnet/internal/testhub"
)

func TestAdminRemoveRoute(t *testing.T) {
	env := testhub.Start(t)
	owner, secret := env.NewPeer("session", "route-owner")
	ctx := context.Background()
	id, _, err := env.Hub.AddRoute(ctx, "peer", owner, "http://127.0.0.1:9/hook")
	if err != nil {
		t.Fatal(err)
	}
	path := "/v1/admin/routes/" + id
	resp, raw := env.Do(secret, http.MethodDelete, path, nil)
	mustErr(t, resp, raw, http.StatusForbidden, "forbidden")
	resp, raw = env.Do("", http.MethodDelete, path, nil)
	mustErr(t, resp, raw, http.StatusUnauthorized, "unauthenticated")
	if routes, err := env.Hub.ListRoutes(ctx); err != nil || len(routes) != 1 {
		t.Fatalf("unauthorized removal changed routes: %+v, %v", routes, err)
	}
	if raw := adminDo(t, env, http.MethodDelete, path, nil, http.StatusNoContent); len(raw) != 0 {
		t.Fatalf("DELETE route 204 body = %q, want empty", raw)
	}
	resp, raw = env.Do(env.OperatorSecret, http.MethodDelete, path, nil)
	mustErr(t, resp, raw, http.StatusNotFound, "not_found")
}
