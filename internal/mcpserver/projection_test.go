package mcpserver_test

import (
	"context"
	"encoding/json"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/auth"

	"github.com/XertroV/vecnet/internal/mcpserver"
	"github.com/XertroV/vecnet/internal/testhub"
)

var grokToolNames = []string{
	"vecnet_ack", "vecnet_blob_get", "vecnet_guide", "vecnet_inbox",
	"vecnet_peers", "vecnet_react", "vecnet_receipt", "vecnet_reply",
	"vecnet_send", "vecnet_thread", "vecnet_threads", "vecnet_whoami",
}

var operatorToolNames = []string{
	"vecnet_agent_add", "vecnet_agent_kinds", "vecnet_agent_status",
	"vecnet_peer_invite_get", "vecnet_peer_invite_revoke", "vecnet_peer_lifecycle",
	"vecnet_route_stats",
}

func sortedToolNames(t *testing.T, entries []map[string]any) []string {
	t.Helper()
	names := make([]string, 0, len(entries))
	for name := range toolNameSet(t, entries) {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func TestGrokbotToolsListBudget(t *testing.T) {
	env := testhub.Start(t)
	_, grokSecret := env.NewPeer("grokbot", "discovery gateway")
	_, peerSecret := env.NewPeer("session", "discovery session")
	_, principalSecret := env.NewPrincipal("discovery principal")
	for _, era := range []string{mcpserver.ProtoCurrent, mcpserver.ProtoLegacy} {
		t.Run(era, func(t *testing.T) {
			operator := mcpTools(t, env, env.OperatorSecret, mcpEraHeaders(t, env, env.OperatorSecret, era))
			for _, name := range operatorToolNames {
				if !toolNameSet(t, operator)[name] {
					t.Errorf("operator missing %s", name)
				}
			}
			grok := mcpTools(t, env, grokSecret, mcpEraHeaders(t, env, grokSecret, era))
			if got := sortedToolNames(t, grok); !reflect.DeepEqual(got, grokToolNames) {
				t.Errorf("grok tools=%v, want %v", got, grokToolNames)
			}
			// Discovery entries must be identical to the shared projected catalogue.
			// Filtering cannot mutate schemas, metadata or descriptions by caller.
			for _, entry := range grok {
				if !reflect.DeepEqual(entry, toolByName(t, operator, entry["name"].(string))) {
					t.Errorf("caller-specific descriptor for %s", entry["name"])
				}
			}
			compact, err := json.Marshal(map[string]any{"tools": grok})
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("Grok tools/list: %d compact bytes", len(compact))
			// Ceiling history: plan said "under 12 KB" before the schema diet
			// landed. 2026-11-07: the twelve allowed tools, unchanged from the
			// shared projected catalogue, measure 12,770 B; CoS ruling pins
			// 13 KiB and keeps shared descriptions owned by the diet.
			// 2026-11-07: merging R2 blobs and onboarding measures 13,281 B
			// after tightening shared prose. All descriptions and contracts remain.
			if len(compact) >= 13*1024 {
				t.Errorf("Grok tools/list: %d bytes, must be under 13 KiB", len(compact))
			}
			for _, caller := range []struct{ name, secret string }{{"peer", peerSecret}, {"principal", principalSecret}} {
				entries := mcpTools(t, env, caller.secret, mcpEraHeaders(t, env, caller.secret, era))
				want := []string{}
				for _, entry := range operator {
					name := entry["name"].(string)
					isOperator := false
					for _, restricted := range operatorToolNames {
						isOperator = isOperator || name == restricted
					}
					if !isOperator {
						want = append(want, name)
					}
				}
				sort.Strings(want)
				if got := sortedToolNames(t, entries); !reflect.DeepEqual(got, want) {
					t.Errorf("%s tools=%v, want %v", caller.name, got, want)
				}
			}
		})
	}
}

func TestKindScopedDiscoveryCallAuthority(t *testing.T) {
	env := testhub.Start(t)
	peer, secret := env.NewPeer("grokbot", "authority gateway")
	for _, era := range []string{mcpserver.ProtoCurrent, mcpserver.ProtoLegacy} {
		t.Run(era, func(t *testing.T) {
			headers := mcpEraHeaders(t, env, secret, era)
			listed := toolNameSet(t, mcpTools(t, env, secret, headers))
			call := func(name string, args any) toolResult {
				t.Helper()
				if listed[name] {
					t.Fatalf("%s must be absent from Grok discovery", name)
				}
				raw := mcpCallRaw(t, env, secret, headers, "tools/call", map[string]any{"name": name, "arguments": args})
				r := decodeRPC(t, raw)
				if r.Error != nil {
					t.Fatalf("%s: unexpected RPC error", name)
				}
				var result toolResult
				if err := json.Unmarshal(r.Result, &result); err != nil {
					t.Fatal(err)
				}
				return result
			}
			// Presence is intentionally omitted for Grok discovery, but the peer
			// still has its existing authority to update its own presence.
			res := call("vecnet_presence", map[string]any{"state": "idle", "at": "2030-01-02T03:04:05.006Z"})
			if res.IsError {
				t.Fatal("off-list presence call lost its existing authority")
			}
			for _, name := range operatorToolNames {
				args := map[string]any{"peer_id": peer}
				switch name {
				case "vecnet_route_stats":
					args = map[string]any{"id": peer}
				case "vecnet_agent_kinds":
					args = map[string]any{}
				case "vecnet_agent_status":
					args = map[string]any{"request_id": "off-list"}
				case "vecnet_agent_add":
					args = map[string]any{"kind": "session", "spec_version": "2026-11-06", "request_id": "off-list", "inputs": map[string]any{}}
				case "vecnet_peer_lifecycle":
					args["action"] = "disable"
				}
				res := call(name, args)
				var failure toolErrorBody
				if json.Unmarshal(res.StructuredContent, &failure) != nil || !res.IsError || failure.Error.Code != "forbidden" {
					t.Errorf("off-list %s must remain forbidden", name)
				}
			}
			// Hidden tools retain the original call validator as well.
			raw := mcpCallRaw(t, env, secret, headers, "tools/call", map[string]any{"name": "vecnet_presence", "arguments": map[string]any{"state": "idle", "at": "invalid"}})
			if r := decodeRPC(t, raw); r.Error == nil || r.Error.Code != -32602 {
				t.Error("off-list call bypassed authoritative validation")
			}
		})
	}
}

// An operator credential without an enabled operator peer still has lifecycle
// authority, but cannot use the onboarding services. Discovery uses those same
// gates instead of treating the operator peer kind as a grant of authority.
func TestDiscoveryOperatorWithoutEnabledPeer(t *testing.T) {
	env := testhub.Start(t)
	if _, err := env.Hub.DB.Exec(`UPDATE peers SET disabled=1 WHERE peer_id=?`, env.OperatorPeer); err != nil {
		t.Fatal(err)
	}
	for _, era := range []string{mcpserver.ProtoCurrent, mcpserver.ProtoLegacy} {
		t.Run(era, func(t *testing.T) {
			entries := mcpTools(t, env, env.OperatorSecret, mcpEraHeaders(t, env, env.OperatorSecret, era))
			names := toolNameSet(t, entries)
			for _, name := range operatorToolNames {
				want := name != "vecnet_agent_add" && name != "vecnet_agent_kinds" && name != "vecnet_agent_status"
				if names[name] != want {
					t.Errorf("operator without enabled peer: %s visibility=%t, want %t", name, names[name], want)
				}
			}
		})
	}
}

func TestOAuthGrokDiscovery(t *testing.T) {
	env := testhub.Start(t)
	peer, _ := env.NewPeer("grokbot", "OAuth discovery gateway")
	owner, err := env.Hub.PeerPrincipal(context.Background(), peer)
	if err != nil {
		t.Fatal(err)
	}
	p := mcpserver.Principal{
		Caller: auth.Caller{Kind: "principal", PrincipalID: owner, PeerID: peer, PeerKind: "grokbot"},
		OAuth:  &mcpserver.OAuthInfo{PrincipalID: owner, Scopes: []string{mcpserver.ScopeVecnet}, Expires: env.Now.Now().Add(time.Hour)},
	}
	front := eventsFront(t, env, guideAuth{p}, nil)
	for _, era := range []string{mcpserver.ProtoCurrent, mcpserver.ProtoLegacy} {
		t.Run(era, func(t *testing.T) {
			entries := mcpTools(t, front, "discovery-test", mcpEraHeaders(t, front, "discovery-test", era))
			if got := sortedToolNames(t, entries); !reflect.DeepEqual(got, grokToolNames) {
				t.Errorf("OAuth Grok tools=%v, want %v", got, grokToolNames)
			}
		})
	}
}
