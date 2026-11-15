package mcpserver_test

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/XertroV/vecnet/internal/mcpdiag"
	"github.com/XertroV/vecnet/internal/mcpserver"
	"github.com/XertroV/vecnet/internal/testhub"
)

// Cover the whole wire response and diagnostics, including prechecks, schema
// errors and registry validation. The values are deliberately synthetic.
func TestOnboardingValidationDoesNotEchoArguments(t *testing.T) {
	env := testhub.Start(t)
	_, logs := captureDiag(t)
	canary := "private-value-canary-" + strings.Repeat("long-canary-", 12) + "private-tail-canary"
	composite := map[string]any{"private-field-canary": canary}
	cases := []struct {
		name, tool, path, code string
		args                   map[string]any
	}{
		{name: "kinds unknown key", tool: "vecnet_agent_kinds", args: map[string]any{canary: composite}},
		{name: "add unknown key", tool: "vecnet_agent_add", args: withTop(agentAddArgs("session", "setup-1", "Agent", nil), canary, composite)},
		{name: "nested unknown key", tool: "vecnet_agent_add", path: "/inputs", args: withInput(agentAddArgs("session", "setup-1", "Agent", nil), canary, composite)},
		{name: "request syntax", tool: "vecnet_agent_add", path: "/request_id", args: withTop(agentAddArgs("session", "setup-1", "Agent", nil), "request_id", canary+" ")},
		{name: "request object", tool: "vecnet_agent_status", path: "/request_id", args: map[string]any{"request_id": composite}},
		{name: "status unknown key", tool: "vecnet_agent_status", args: map[string]any{"request_id": "setup-1", canary: composite}},
		{name: "kind enum", tool: "vecnet_agent_add", path: "/kind", args: agentAddArgs(canary, "setup-1", "Agent", nil)},
		{name: "spec object", tool: "vecnet_agent_add", path: "/spec_version", args: withTop(agentAddArgs("session", "setup-1", "Agent", nil), "spec_version", composite)},
		{name: "name object", tool: "vecnet_agent_add", path: "/inputs/name", args: withInput(agentAddArgs("session", "setup-1", "Agent", nil), "name", composite)},
		{name: "skip array", tool: "vecnet_agent_add", path: "/inputs/skip_mcp", args: withInput(agentAddArgs("session", "setup-1", "Agent", nil), "skip_mcp", []any{canary, composite})},
		{name: "ttl type", tool: "vecnet_agent_add", path: "/inputs/enrol_ttl_seconds", args: withInput(agentAddArgs("session", "setup-1", "Agent", nil), "enrol_ttl_seconds", canary)},
		{name: "stale spec", tool: "vecnet_agent_add", code: "stale_spec", args: withTop(agentAddArgs("session", "setup-1", "Agent", nil), "spec_version", canary)},
		{name: "connector syntax", tool: "vecnet_agent_add", code: "invalid_value", args: withInput(agentAddArgs("grokbot", "setup-1", "Agent", nil), "connector_name", canary)},
	}
	before := takeMCPCensus(t, env)
	for _, era := range []string{mcpserver.ProtoCurrent, mcpserver.ProtoLegacy} {
		headers := mcpEraHeaders(t, env, env.OperatorSecret, era)
		for _, tc := range cases {
			t.Run(era+"/"+tc.name, func(t *testing.T) {
				var raw []byte
				if tc.code == "" {
					got := mcpInvalidPath(t, env, env.OperatorSecret, headers, tc.tool, tc.args)
					if got != tc.path {
						t.Fatalf("path=%q, want %q", got, tc.path)
					}
					raw = mcpCallRaw(t, env, env.OperatorSecret, headers, "tools/call", map[string]any{"name": tc.tool, "arguments": tc.args})
				} else {
					_, raw = mcpToolError(t, env, env.OperatorSecret, headers, tc.tool, tc.args, tc.code)
				}
				ring, err := json.Marshal(mcpdiag.Snapshot(mcpdiag.Filter{Peer: env.OperatorPeer}))
				if err != nil {
					t.Fatal(err)
				}
				for _, forbidden := range []string{"original_value", "private-value-canary", "private-tail-canary", "long-canary", "private-field-canary"} {
					if strings.Contains(string(raw), forbidden) || strings.Contains(logs.String(), forbidden) || strings.Contains(string(ring), forbidden) {
						t.Fatalf("response or diagnostics echoed %s", forbidden)
					}
				}
			})
		}
	}
	if takeMCPCensus(t, env) != before {
		t.Fatal("validation wrote onboarding state")
	}
}

// The saved transport is safe status metadata, never an implicit add opt-in.
func TestOnboardingTransportProjectionContract(t *testing.T) {
	env := testhub.Start(t)
	for _, era := range []string{mcpserver.ProtoCurrent, mcpserver.ProtoLegacy} {
		t.Run(era, func(t *testing.T) {
			headers := mcpEraHeaders(t, env, env.OperatorSecret, era)
			tools := mcpTools(t, env, env.OperatorSecret, headers)
			status := toolByName(t, tools, "vecnet_agent_status")["outputSchema"].(map[string]any)
			field := status["properties"].(map[string]any)["grokbot_transport"].(map[string]any)
			if field["type"] != "string" || !reflect.DeepEqual(field["enum"], []any{"delivery_v1", "recipient_drain_v1"}) {
				t.Fatalf("transport schema = %#v", field)
			}
			description, _ := field["description"].(string)
			if !strings.Contains(description, "Saved request transport") || !strings.Contains(description, "historical Grok omission means delivery_v1") {
				t.Errorf("transport description = %q", description)
			}
			if required, ok := status["required"].([]any); ok {
				for _, name := range required {
					if name == "grokbot_transport" {
						t.Error("transport must remain optional")
					}
				}
			}
			add := toolByName(t, tools, "vecnet_agent_add")["inputSchema"].(map[string]any)
			inputs := add["properties"].(map[string]any)["inputs"].(map[string]any)
			if _, exists := inputs["properties"].(map[string]any)["grokbot_transport"]; exists {
				t.Error("human handoff must not expose transport opt-in")
			}
		})
	}
}
