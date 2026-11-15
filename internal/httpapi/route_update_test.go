package httpapi_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/XertroV/vecnet/internal/testhub"
)

// PATCH /v1/admin/routes/{id}: operator only, 204 with no body, in place
// (same id), unverified after an http URL change, and no echo of the URL
// or key in any answer.
func TestAdminUpdateRoute(t *testing.T) {
	env := testhub.Start(t)
	owner, secret := env.NewPeer("session", "route-owner")
	ctx := context.Background()
	id, _, err := env.Hub.AddRoute(ctx, "peer", owner, "http://127.0.0.1:9/hook")
	if err != nil {
		t.Fatal(err)
	}
	if err := env.Hub.MarkRouteVerified(ctx, id); err != nil {
		t.Fatal(err)
	}
	path := "/v1/admin/routes/" + id
	next := "http://127.0.0.1:9/next-hook?token=canary-secret"
	resp, raw := env.Do(secret, http.MethodPatch, path, map[string]any{"url": next})
	mustErr(t, resp, raw, http.StatusForbidden, "forbidden")
	resp, raw = env.Do("", http.MethodPatch, path, map[string]any{"url": next})
	mustErr(t, resp, raw, http.StatusUnauthorized, "unauthenticated")

	if raw := adminDo(t, env, http.MethodPatch, path, map[string]any{"url": next}, http.StatusNoContent); len(raw) != 0 {
		t.Fatalf("PATCH route 204 body = %q", raw)
	}
	routes, err := env.Hub.ListRoutes(ctx)
	if err != nil || len(routes) != 1 || routes[0].RouteID != id || routes[0].VerifiedAt != nil {
		t.Fatalf("routes after PATCH = %+v, %v", routes, err)
	}

	// Refusals: unknown fields, wrong types, empty body, key on an http
	// route, a refused URL, an unknown route. None quotes the input.
	key := "crsr_canary-key-value"
	for _, c := range []struct {
		body       any
		status     int
		code, leak string
	}{
		{map[string]any{"owner": owner}, http.StatusBadRequest, "invalid_body", ""},
		{map[string]any{"url": 7}, http.StatusBadRequest, "invalid_body", ""},
		{map[string]any{}, http.StatusUnprocessableEntity, "invalid_value", ""},
		{map[string]any{"key": key}, http.StatusUnprocessableEntity, "invalid_value", key},
		{map[string]any{"url": "ftp://canary.example/x"}, http.StatusUnprocessableEntity, "invalid_url", "canary.example"},
	} {
		resp, raw := env.Do(env.OperatorSecret, http.MethodPatch, path, c.body)
		mustErr(t, resp, raw, c.status, c.code)
		if c.leak != "" && strings.Contains(string(raw), c.leak) {
			t.Fatalf("error echoes the input: %s", raw)
		}
	}
	resp, raw = env.Do(env.OperatorSecret, http.MethodPatch, "/v1/admin/routes/route_nope", map[string]any{"url": next})
	mustErr(t, resp, raw, http.StatusNotFound, "not_found")
	if strings.Contains(string(raw), "canary-secret") {
		t.Fatalf("404 echoes the URL: %s", raw)
	}
}

// A Grok Bot route over HTTP: PATCH with a new routine URL and key is 204,
// takes effect (the key used at push time), stays verified, and the answer
// never carries the key. Body shapes match the documented error order.
func TestAdminUpdateRouteGrokbotAndBodies(t *testing.T) {
	env := testhub.Start(t)
	owner, _ := env.NewPeer("grokbot", "g")
	ctx := context.Background()
	const routine1 = "https://api2.cursor.sh/automations/webhook/123e4567-e89b-12d3-a456-426614174000"
	const routine2 = "https://api2.cursor.sh/automations/webhook/89abcdef-0123-4567-89ab-cdef01234567"
	const key1, key2 = "crsr_first-canary-key", "crsr_second-canary-key"
	id, err := env.Hub.AddGrokbotRoute(ctx, "peer", owner, routine1, key1)
	if err != nil {
		t.Fatal(err)
	}
	path := "/v1/admin/routes/" + id
	raw := adminDo(t, env, http.MethodPatch, path, map[string]any{"url": routine2, "key": key2}, http.StatusNoContent)
	if strings.Contains(string(raw), key2) {
		t.Fatalf("PATCH answer carries the key: %s", raw)
	}
	if u, err := env.Hub.RouteURL(ctx, id); err != nil || u != routine2 {
		t.Fatalf("grokbot url after PATCH = %q, %v", u, err)
	}
	if k, err := env.Hub.GrokbotKey(ctx, id); err != nil || k != key2 {
		t.Fatalf("grokbot key after PATCH: match %v, %v", k == key2, err)
	}
	routes, err := env.Hub.ListRoutes(ctx)
	if err != nil || len(routes) != 1 || routes[0].VerifiedAt == nil {
		t.Fatalf("grokbot route went unverified: %+v, %v", routes, err)
	}
	if _, listed := env.Do(env.OperatorSecret, "GET", "/v1/admin/routes", nil); strings.Contains(string(listed), key2) || strings.Contains(string(listed), "89abcdef") {
		t.Fatalf("route list leaks the key or URL path: %s", listed)
	}
	// Body shapes: {} is 422; no body, null, a null value are 400.
	for _, c := range []struct {
		body   any
		status int
		code   string
	}{
		{"", http.StatusBadRequest, "invalid_body"},
		{"null", http.StatusBadRequest, "invalid_body"},
		{`{"url":null}`, http.StatusBadRequest, "invalid_body"},
		{map[string]any{}, http.StatusUnprocessableEntity, "invalid_value"},
		{map[string]any{"key": "junk"}, http.StatusUnprocessableEntity, "invalid_key"},
		{map[string]any{"url": "https://example.com/hook"}, http.StatusUnprocessableEntity, "invalid_url"},
	} {
		resp, raw := env.Do(env.OperatorSecret, http.MethodPatch, path, c.body)
		mustErr(t, resp, raw, c.status, c.code)
	}
}
