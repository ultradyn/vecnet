package mcpserver_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/XertroV/vecnet/internal/onboarding"
	"github.com/XertroV/vecnet/internal/testhub"
)

func TestSafeTransportStatusCLIAndMCP(t *testing.T) {
	env := testhub.Start(t)
	const key = "crsr_SYNTHETIC_mcp_transport"
	const url = "https://api2.cursor.sh/automations/webhook/00000000-0000-0000-0000-000000000001"
	args := agentAddArgs("grokbot", "mcp-transport", "Transport MCP", map[string]any{"skip_mcp": true})
	before := takeMCPCensus(t, env)
	handoff := mcpToolOK(t, env, env.OperatorSecret, modern(), "vecnet_agent_add", args)
	if strings.Contains(handoff["web_path"].(string), "grokbot_transport") {
		t.Fatal("handoff gained transport parameter")
	}
	if takeMCPCensus(t, env) != before {
		t.Fatal("handoff wrote state")
	}
	// MCP agent_add remains a closed legacy-only human handoff.
	invalid := withInput(args, "grokbot_transport", "recipient_drain_v1")
	if path := mcpInvalidPath(t, env, env.OperatorSecret, modern(), "vecnet_agent_add", invalid); path != "/inputs" {
		t.Fatalf("MCP accepted explicit transport: %s", path)
	}
	body := map[string]any{"kind": "grokbot", "spec_version": onboarding.SpecVersion, "request_id": "mcp-transport", "inputs": map[string]any{"name": "Transport MCP", "skip_mcp": true, "webhook_url": url, "webhook_key": key, "grokbot_transport": "recipient_drain_v1"}}
	resp, raw := env.Do(env.OperatorSecret, http.MethodPost, "/v1/admin/agents", body)
	if resp.StatusCode != 201 {
		t.Fatalf("submit %d %s", resp.StatusCode, raw)
	}
	var created onboarding.SubmitResult
	json.Unmarshal(raw, &created)
	status := mcpToolOK(t, env, env.OperatorSecret, modern(), "vecnet_agent_status", map[string]any{"request_id": "mcp-transport"}, key, url)
	if status["grokbot_transport"] != "recipient_drain_v1" {
		t.Fatal("MCP status lost transport")
	}
	// Legacy-only handoff must conflict with a saved opted-in request.
	mcpToolError(t, env, env.OperatorSecret, modern(), "vecnet_agent_add", args, "idempotency_conflict")
	resp, raw = env.Do(env.OperatorSecret, http.MethodPatch, "/v1/admin/routes/"+created.RouteID, map[string]any{"grokbot_transport": "delivery_v1"})
	if resp.StatusCode != 204 || len(raw) != 0 {
		t.Fatal("revocation PATCH")
	}
	status = mcpToolOK(t, env, env.OperatorSecret, modern(), "vecnet_agent_status", map[string]any{"request_id": "mcp-transport"}, key, url)
	if status["grokbot_transport"] != "recipient_drain_v1" {
		t.Fatal("MCP status followed mutable route")
	}
}

func TestTransportHistoricalMCPSafeguards(t *testing.T) {
	env := testhub.Start(t)
	const id = "mcp-legacy-transport"
	args := agentAddArgs("grokbot", id, "Historical MCP", nil)
	mustHumanSubmit(t, env, map[string]any{"kind": "grokbot", "spec_version": onboarding.SpecVersion, "request_id": id, "inputs": map[string]any{"name": "Historical MCP", "webhook_url": fakeRoutineURL(t), "webhook_key": fakeRoutineKey(t)}})
	ageReceipt(t, env, id, "grokbot_transport", "safe_complete")
	status := mcpToolOK(t, env, env.OperatorSecret, modern(), "vecnet_agent_status", map[string]any{"request_id": id})
	if status["grokbot_transport"] != "delivery_v1" || status["safe_complete"] == true {
		t.Fatal("historical legacy status promoted incomplete receipt")
	}
	// Existing pre-marker safe-projection compatibility remains legacy-only.
	mcpToolOK(t, env, env.OperatorSecret, modern(), "vecnet_agent_add", args)
	ageReceipt(t, env, id, "name")
	mcpToolError(t, env, env.OperatorSecret, modern(), "vecnet_agent_add", args, "idempotency_conflict")
}
