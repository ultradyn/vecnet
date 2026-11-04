package mcpserver_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/XertroV/vecnet/internal/auth"
	"github.com/XertroV/vecnet/internal/events"
	"github.com/XertroV/vecnet/internal/mcpserver"
	"github.com/XertroV/vecnet/internal/testhub"
	"github.com/XertroV/vecnet/internal/webhook"
)

type referenceSequenceOAuth struct {
	principal string
	expires   time.Time
}

func (a referenceSequenceOAuth) Authenticate(context.Context, *http.Request) (mcpserver.Principal, error) {
	return mcpserver.Principal{
		Caller: auth.Caller{Kind: "principal", PrincipalID: a.principal},
		OAuth: &mcpserver.OAuthInfo{
			PrincipalID: a.principal, Expires: a.expires,
			Scopes: []string{"vecnet", "events:read", "events:write"},
		},
	}, nil
}

// TestWebhook2MCPReferenceSequence follows the complete modern envelopes in
// webhook2mcp/docs/mcp-examples.md against a real, isolated httptest hub. Only
// the event name and domain arguments change. OAuth authentication is a seam;
// issuance, refresh and insufficient-scope behavior have separate OAuth tests.
func TestWebhook2MCPReferenceSequence(t *testing.T) {
	for _, oauth := range []bool{false, true} {
		name := "bearer"
		if oauth {
			name = "oauth"
		}
		t.Run(name, func(t *testing.T) {
			env := testhub.Start(t)
			peer, credential := env.NewPeer("session", "reference-client")
			front := env
			if oauth {
				principal, err := env.Hub.PeerPrincipal(context.Background(), peer)
				if err != nil {
					t.Fatal(err)
				}
				front = eventsFront(t, env, referenceSequenceOAuth{principal: principal, expires: env.Now.Now().Add(time.Hour)}, func(s *mcpserver.Server) {
					s.ResolveDotPeer = func(context.Context, string) (string, error) { return peer, nil }
				})
			}
			request := func(method string, params map[string]any) map[string]json.RawMessage {
				t.Helper()
				if params == nil {
					params = map[string]any{}
				}
				params["_meta"] = map[string]any{
					"io.modelcontextprotocol/protocolVersion":    mcpserver.ProtoCurrent,
					"io.modelcontextprotocol/clientCapabilities": map[string]any{},
					"io.modelcontextprotocol/clientInfo": map[string]any{
						"name": "operator-conformance-check", "version": "1.1.0",
					},
				}
				headers := modern()
				headers["Mcp-Method"] = method
				if method == "tools/call" {
					headers["Mcp-Name"] = params["name"].(string)
				}
				resp, body := do(t, front, call{secret: credential, headers: headers, body: map[string]any{
					"jsonrpc": "2.0", "id": method + "-1", "method": method, "params": params,
				}})
				if resp.StatusCode != http.StatusOK {
					t.Fatalf("%s: HTTP %d", method, resp.StatusCode)
				}
				if resp.Header.Get("Mcp-Session-Id") != "" {
					t.Fatalf("%s minted a session", method)
				}
				rpc := decodeRPC(t, body)
				if rpc.Error != nil {
					t.Fatalf("%s: RPC code %d", method, rpc.Error.Code)
				}
				var result map[string]json.RawMessage
				if err := json.Unmarshal(rpc.Result, &result); err != nil {
					t.Fatal(err)
				}
				if string(result["resultType"]) != `"complete"` {
					t.Fatalf("%s: missing complete resultType", method)
				}
				var meta map[string]json.RawMessage
				if err := json.Unmarshal(result["_meta"], &meta); err != nil || len(meta["io.modelcontextprotocol/serverInfo"]) == 0 {
					t.Fatalf("%s: missing namespaced serverInfo", method)
				}
				return result
			}

			discovery := request("server/discover", nil)
			var versions []string
			if err := json.Unmarshal(discovery["supportedVersions"], &versions); err != nil || !slices.Equal(versions, []string{mcpserver.ProtoCurrent}) {
				t.Fatalf("discover supportedVersions = %v, err %v", versions, err)
			}
			if string(discovery["ttlMs"]) != "0" || string(discovery["cacheScope"]) != `"private"` {
				t.Fatal("discover must disable shared caching")
			}
			var capabilities map[string]json.RawMessage
			if err := json.Unmarshal(discovery["capabilities"], &capabilities); err != nil || string(capabilities["events"]) != "{}" {
				t.Fatal("discover must advertise events capability")
			}
			catalog := request("events/list", nil)
			var listed []struct {
				Name string `json:"name"`
			}
			if err := json.Unmarshal(catalog["events"], &listed); err != nil || len(listed) != 1 || listed[0].Name != events.Name {
				t.Fatal("event catalog must expose vecnet.message")
			}
			tools := request("tools/list", nil)
			var toolList []map[string]json.RawMessage
			if err := json.Unmarshal(tools["tools"], &toolList); err != nil || len(toolList) == 0 {
				t.Fatal("tools/list must expose tools")
			}
			for _, tool := range toolList {
				var toolName string
				if err := json.Unmarshal(tool["name"], &toolName); err != nil || toolName == "" {
					t.Fatal("tool missing name")
				}
				if len(tool["securitySchemes"]) == 0 || len(tool["annotations"]) == 0 {
					t.Fatalf("tool %s missing OAuth security or annotations", toolName)
				}
				// MCP outputSchema is optional. Check every declared schema rather
				// than imposing a new catalog-wide requirement on working tools.
				if raw, ok := tool["outputSchema"]; ok {
					var shape struct {
						Type string `json:"type"`
					}
					if err := json.Unmarshal(raw, &shape); err != nil || shape.Type != "object" {
						t.Fatalf("tool %s outputSchema must describe an object", toolName)
					}
					doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
					if err != nil {
						t.Fatalf("tool %s outputSchema JSON: %v", toolName, err)
					}
					compiler := jsonschema.NewCompiler()
					uri := "urn:reference-output:" + toolName
					if err := compiler.AddResource(uri, doc); err != nil {
						t.Fatalf("tool %s outputSchema resource: %v", toolName, err)
					}
					if _, err := compiler.Compile(uri); err != nil {
						t.Fatalf("tool %s outputSchema: %v", toolName, err)
					}
				}
			}

			callback := testhub.NewCallback(t)
			secret := webhook.NewSecret()
			result := request("events/subscribe", subParams(callback.URL, secret, map[string]any{"ttlMs": 3_600_000, "cursor": nil}))
			var id, cursor, refreshBefore string
			for key, target := range map[string]*string{"id": &id, "cursor": &cursor, "refreshBefore": &refreshBefore} {
				if err := json.Unmarshal(result[key], target); err != nil || *target == "" {
					t.Fatalf("subscribe missing %s", key)
				}
			}
			if _, err := time.Parse(time.RFC3339Nano, refreshBefore); err != nil {
				t.Fatal("refreshBefore must be an RFC3339 timestamp")
			}
			challenges := callback.Challenges()
			if len(challenges) != 1 {
				t.Fatalf("challenges = %d, want 1", len(challenges))
			}
			challenge := challenges[0]
			if challenge.Subscription != id || challenge.ID == "" {
				t.Fatal("verification lacks subscription or webhook id headers")
			}
			if err := webhook.Verify([]string{secret}, challenge.ID, challenge.Timestamp, challenge.Signature, challenge.Body, env.Now.Now()); err != nil {
				t.Fatalf("verification signature: %v", err)
			}
			status := request("tools/call", map[string]any{"name": "vecnet_subscriptions", "arguments": map[string]any{}})
			var health struct {
				Subscriptions []struct {
					ID       string `json:"id"`
					Name     string `json:"name"`
					Verified bool   `json:"verified"`
				} `json:"subscriptions"`
			}
			if err := json.Unmarshal(status["structuredContent"], &health); err != nil || len(health.Subscriptions) != 1 {
				t.Fatal("status tool must return the caller's one subscription")
			}
			if sub := health.Subscriptions[0]; sub.ID != id || sub.Name != events.Name || !sub.Verified {
				t.Fatal("status tool must identify the verified subscription")
			}
			request("events/unsubscribe", map[string]any{
				"name": events.Name, "arguments": map[string]any{},
				"delivery": map[string]any{"mode": "webhook", "url": callback.URL},
			})
		})
	}
}
