package httpapi_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/XertroV/vecnet/internal/testhub"
)

func TestOnboardingTransportInputClosedTopLevel(t *testing.T) {
	env := testhub.Start(t)
	before := takeOnboardCensus(t, env)
	body := agentBody("grokbot", "closed-transport", map[string]any{"name": "Closed", "webhook_url": "https://api2.cursor.sh/automations/webhook/00000000-0000-0000-0000-000000000001", "webhook_key": "crsr_SYNTHETIC_http"})
	body["grokbot_transport"] = "recipient_drain_v1"
	resp, raw := env.Do(env.OperatorSecret, http.MethodPost, "/v1/admin/agents", body)
	onboardMustErr(t, resp, raw, 400, "invalid_body")
	if takeOnboardCensus(t, env) != before {
		t.Fatal("closed request wrote state")
	}
	delete(body, "grokbot_transport")
	inputs := body["inputs"].(map[string]any)
	for _, bad := range []any{nil, 7, "unknown-secret-canary"} {
		inputs["grokbot_transport"] = bad
		resp, raw = env.Do(env.OperatorSecret, http.MethodPost, "/v1/admin/agents", body)
		if resp.StatusCode != 422 || strings.Contains(string(raw), "unknown-secret-canary") {
			t.Errorf("invalid transport status=%d", resp.StatusCode)
		}
	}
	inputs["grokbot_transport"] = "recipient_drain_v1"
	resp, raw = env.Do(env.OperatorSecret, http.MethodPost, "/v1/admin/agents", body)
	if resp.StatusCode != 201 {
		t.Fatalf("explicit input status=%d body=%s", resp.StatusCode, raw)
	}
	var res map[string]any
	json.Unmarshal(raw, &res)
	if res["grokbot_transport"] != "recipient_drain_v1" {
		t.Fatal("HTTP result lost transport")
	}
	id := res["route_id"].(string)
	resp, raw = env.Do(env.OperatorSecret, http.MethodPatch, "/v1/admin/routes/"+id, map[string]any{"grokbot_transport": "delivery_v1"})
	if resp.StatusCode != 204 || len(raw) != 0 {
		t.Fatalf("PATCH status=%d bytes=%d", resp.StatusCode, len(raw))
	}
	resp, raw = env.Do(env.OperatorSecret, http.MethodGet, "/v1/admin/routes", nil)
	if resp.StatusCode != 200 {
		t.Fatal(resp.StatusCode)
	}
	var listed struct {
		Routes []struct {
			ID        string `json:"route_id"`
			Transport string `json:"grokbot_transport"`
		} `json:"routes"`
	}
	json.Unmarshal(raw, &listed)
	found := false
	for _, r := range listed.Routes {
		if r.ID == id {
			found = r.Transport == "delivery_v1"
		}
	}
	if !found {
		t.Fatalf("separate list lost revocation: %s", raw)
	}
	resp, raw = env.Do(env.OperatorSecret, http.MethodGet, "/v1/admin/agents/onboarding/closed-transport", nil)
	if resp.StatusCode != 200 || !strings.Contains(string(raw), `"grokbot_transport":"recipient_drain_v1"`) || strings.Contains(string(raw), "crsr_") || strings.Contains(string(raw), "human_output") {
		t.Fatalf("unsafe/mutable saved status: %d %s", resp.StatusCode, raw)
	}
}
