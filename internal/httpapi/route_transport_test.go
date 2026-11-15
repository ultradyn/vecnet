package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/XertroV/vecnet/internal/hub"
	"github.com/XertroV/vecnet/internal/testhub"
)

func TestRouteTransportPatch204ThenList(t *testing.T) {
	env := testhub.Start(t)
	peer, secret := env.NewPeer("grokbot", "transport")
	const u = "https://api2.cursor.sh/automations/webhook/123e4567-e89b-12d3-a456-426614174000"
	const key = "crsr_synthetic-key"
	created := adminDo(t, env, "POST", "/v1/admin/routes", map[string]any{"owner": peer, "kind": "grokbot", "url": u, "key": key}, http.StatusCreated)
	var result map[string]json.RawMessage
	if err := json.Unmarshal(created, &result); err != nil {
		t.Fatal(err)
	}
	var id, transport string
	json.Unmarshal(result["route_id"], &id)
	json.Unmarshal(result["grokbot_transport"], &transport)
	if id == "" || transport != hub.GrokbotTransportDeliveryV1 {
		t.Fatalf("create=%s", created)
	}
	path := "/v1/admin/routes/" + id
	resp, raw := env.Do(secret, "PATCH", path, map[string]any{"grokbot_transport": hub.GrokbotTransportRecipientDrainV1})
	mustErr(t, resp, raw, http.StatusForbidden, "forbidden")
	for _, selection := range []string{hub.GrokbotTransportRecipientDrainV1, hub.GrokbotTransportDeliveryV1} {
		raw = adminDo(t, env, "PATCH", path, map[string]any{"grokbot_transport": selection}, http.StatusNoContent)
		if len(raw) != 0 {
			t.Fatalf("PATCH body=%s", raw)
		}
		raw = adminDo(t, env, "GET", "/v1/admin/routes", nil, http.StatusOK)
		var list struct {
			Routes []hub.RouteInfo `json:"routes"`
		}
		if err := json.Unmarshal(raw, &list); err != nil {
			t.Fatal(err)
		}
		if len(list.Routes) != 1 || list.Routes[0].GrokbotTransport == nil || *list.Routes[0].GrokbotTransport != selection {
			t.Fatalf("list=%s", raw)
		}
		if bytes.Contains(raw, []byte(key)) || bytes.Contains(raw, []byte(u)) {
			t.Fatalf("list leaks secret: %s", raw)
		}
	}
	for _, value := range []any{nil, 7, true, "unknown-canary"} {
		resp, raw = env.Do(env.OperatorSecret, "PATCH", path, map[string]any{"grokbot_transport": value})
		mustErr(t, resp, raw, http.StatusUnprocessableEntity, "invalid_value")
		if !bytes.Contains(raw, []byte(`"field":"grokbot_transport"`)) || bytes.Contains(raw, []byte("unknown-canary")) {
			t.Fatalf("unsafe field error=%s", raw)
		}
		resp, raw = env.Do(env.OperatorSecret, "POST", "/v1/admin/routes", map[string]any{"owner": peer, "kind": "grokbot", "url": u, "key": key, "grokbot_transport": value})
		mustErr(t, resp, raw, http.StatusUnprocessableEntity, "invalid_value")
	}
	httpID, _, err := env.Hub.AddRoute(context.Background(), "peer", peer, "http://127.0.0.1:9/hook")
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []any{nil, hub.GrokbotTransportDeliveryV1, hub.GrokbotTransportRecipientDrainV1} {
		resp, raw = env.Do(env.OperatorSecret, "PATCH", "/v1/admin/routes/"+httpID, map[string]any{"grokbot_transport": value})
		mustErr(t, resp, raw, http.StatusUnprocessableEntity, "invalid_value")
		resp, raw = env.Do(env.OperatorSecret, "POST", "/v1/admin/routes", map[string]any{"owner": peer, "url": "http://127.0.0.1:9/hook", "grokbot_transport": value})
		mustErr(t, resp, raw, http.StatusUnprocessableEntity, "invalid_value")
	}
	routes, err := env.Hub.ListRoutes(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range routes {
		if r.Kind == hub.RouteKindHTTP && r.GrokbotTransport != nil {
			t.Fatal("non-Grok transport must be null")
		}
	}
}
