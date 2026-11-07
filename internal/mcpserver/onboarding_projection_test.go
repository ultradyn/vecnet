package mcpserver_test

import (
	"encoding/json"
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
