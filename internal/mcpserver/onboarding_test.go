package mcpserver_test

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/url"
	"reflect"
	"regexp"
	"strings"
	"testing"

	venv "github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/mcpserver"
	"github.com/XertroV/vecnet/internal/onboarding"
	"github.com/XertroV/vecnet/internal/testhub"
)

var mcpEnrolTokenRE = regexp.MustCompile(`vn_enrol_[A-Za-z0-9_-]+`)

// TestAgentOnboardMCPHumanBoundary is Task 7. Decisions §2 and Review Focus
// of docs/plans/2026-11-06-agent-onboarding-kinds.md. PROTOCOL §§4.1, 4.3,
// 4.5–4.6, 12 and 20.1.
//
// Operator MCP may list kinds and read safe status. vecnet_agent_add accepts
// no webhook URL, key, header or file path, creates nothing, and returns a
// browser path. It does not mint an enrol token or a link code and then drop
// it from the result. Status stays free of those secrets before and after
// the human HTTP submit. A named connector is not reported connected, and
// this test does not link any peer.
func TestAgentOnboardMCPHumanBoundary(t *testing.T) {
	env := testhub.Start(t)
	_, peerSecret := env.NewPeer("session", "ordinary peer")
	_, principalSecret := env.NewPrincipal("ordinary principal")
	before := takeMCPCensus(t, env)
	if before.links != 0 {
		t.Fatal("fixture already has a link code")
	}
	assertNoConductor(t, env)

	opHeaders := map[string]map[string]string{}
	peerHeaders := map[string]map[string]string{}
	for _, era := range []string{mcpserver.ProtoCurrent, mcpserver.ProtoLegacy} {
		opHeaders[era] = mcpEraHeaders(t, env, env.OperatorSecret, era)
		peerHeaders[era] = mcpEraHeaders(t, env, peerSecret, era)
	}

	t.Run("discovery", func(t *testing.T) {
		for _, era := range []string{mcpserver.ProtoCurrent, mcpserver.ProtoLegacy} {
			t.Run(era, func(t *testing.T) {
				first := mcpTools(t, env, env.OperatorSecret, opHeaders[era])
				second := mcpTools(t, env, env.OperatorSecret, opHeaders[era])
				if !reflect.DeepEqual(first, second) {
					t.Fatal("operator tools/list is not stable")
				}
				peerList := mcpTools(t, env, peerSecret, peerHeaders[era])
				if len(peerList) != 23 {
					t.Fatalf("ordinary peer tools = %d, want the existing catalogue", len(peerList))
				}
				names := toolNameSet(t, first)
				for _, name := range []string{"vecnet_agent_kinds", "vecnet_agent_add", "vecnet_agent_status"} {
					if _, ok := names[name]; !ok {
						t.Fatalf("operator tools/list lacks %s", name)
					}
					if toolNameSet(t, peerList)[name] {
						t.Fatalf("ordinary peer tools/list includes %s", name)
					}
				}
				if len(first) != 26 {
					t.Fatalf("operator tools = %d, want the catalogue plus the three agent tools", len(first))
				}
				for _, name := range []string{"vecnet_agent_kinds", "vecnet_agent_add", "vecnet_agent_status"} {
					tool := toolByName(t, first, name)
					ann, _ := tool["annotations"].(map[string]any)
					if ann["readOnlyHint"] != true || ann["destructiveHint"] != false || ann["openWorldHint"] != false {
						t.Fatalf("%s annotations = %v", name, ann)
					}
					if strings.Contains(strings.ToLower(tool["description"].(string)), "connector is connected") {
						t.Fatalf("%s description asserts a connector is connected", name)
					}
					if strings.Contains(strings.ToLower(tool["description"].(string)), "skip whoami") {
						t.Fatalf("%s description overrides the whoami stop", name)
					}
					schema, _ := tool["inputSchema"].(map[string]any)
					if schema["additionalProperties"] != false || schema["type"] != "object" {
						t.Fatalf("%s input schema is not a closed object", name)
					}
					for _, banned := range []string{"anyOf", "oneOf", "allOf"} {
						if _, ok := schema[banned]; ok {
							t.Fatalf("%s input schema has top-level %s", name, banned)
						}
					}
					props := map[string]bool{}
					propertyNames(schema, props)
					if out, ok := tool["outputSchema"].(map[string]any); ok {
						propertyNames(out, props)
					}
					for prop := range props {
						if strings.Contains(strings.ToLower(prop), "url") || prop == "webhook_key" || prop == "human_output" || prop == "authorization" || prop == "key_file" || prop == "file_path" {
							t.Fatalf("%s schema exposes %s", name, prop)
						}
					}
					wantSchemes := []any{map[string]any{"type": "oauth2", "scopes": []any{"vecnet"}}}
					if !reflect.DeepEqual(tool["securitySchemes"], wantSchemes) {
						t.Fatalf("%s securitySchemes = %v", name, tool["securitySchemes"])
					}
				}
				add := toolByName(t, first, "vecnet_agent_add")
				input := add["inputSchema"].(map[string]any)
				required, _ := input["required"].([]any)
				for _, key := range []string{"kind", "spec_version", "request_id", "inputs"} {
					if !containsAny(required, key) {
						t.Fatalf("vecnet_agent_add required = %v, missing %s", required, key)
					}
				}
				inputs := input["properties"].(map[string]any)["inputs"].(map[string]any)
				if inputs["additionalProperties"] != false {
					t.Fatal("vecnet_agent_add inputs are not closed")
				}
				allowed := inputs["properties"].(map[string]any)
				for _, key := range []string{"name", "name_open", "enrol_ttl_seconds", "principal_id", "connector_name", "skip_mcp"} {
					if _, ok := allowed[key]; !ok {
						t.Fatalf("vecnet_agent_add inputs missing safe field %s", key)
					}
				}
				for _, banned := range []string{"webhook_url", "webhook_key", "authorization", "header", "key_file", "file_path", "path", "as"} {
					if _, ok := allowed[banned]; ok {
						t.Fatalf("vecnet_agent_add inputs allow %s", banned)
					}
				}
				out := add["outputSchema"].(map[string]any)
				if out["additionalProperties"] != false {
					t.Fatal("vecnet_agent_add output is not closed")
				}
				statusTool := toolByName(t, first, "vecnet_agent_status")
				statusOut := statusTool["outputSchema"].(map[string]any)
				statusProps := statusOut["properties"].(map[string]any)
				if _, ok := statusProps["human_output"]; ok {
					t.Fatal("vecnet_agent_status output schema includes human_output")
				}
			})
		}
		if takeMCPCensus(t, env) != before {
			t.Fatal("discovery wrote onboarding state")
		}
	})

	t.Run("schema rejection", func(t *testing.T) {
		routineURL := fakeRoutineURL(t)
		routineKey := fakeRoutineKey(t)
		header := "Bearer " + routineKey
		filePath := "/var/lib/vecnet/routine.key"
		base := agentAddArgs("grokbot", "schema-1", "Helper Bot", nil)
		cases := []struct {
			name string
			args map[string]any
			path string
		}{
			{name: "webhook url", args: withTop(base, "webhook_url", routineURL), path: "/webhook_url"},
			{name: "webhook key", args: withTop(base, "webhook_key", routineKey), path: "/webhook_key"},
			{name: "authorization header", args: withTop(base, "authorization", header), path: "/authorization"},
			{name: "header", args: withInput(base, "header", header), path: "/inputs/header"},
			{name: "key file", args: withTop(base, "key_file", filePath), path: "/key_file"},
			{name: "file path", args: withInput(base, "file_path", filePath), path: "/inputs/file_path"},
			{name: "webhook url in inputs", args: withInput(base, "webhook_url", routineURL), path: "/inputs/webhook_url"},
			{name: "webhook key in inputs", args: withInput(base, "webhook_key", routineKey), path: "/inputs/webhook_key"},
		}
		for _, era := range []string{mcpserver.ProtoCurrent, mcpserver.ProtoLegacy} {
			t.Run(era, func(t *testing.T) {
				for _, tc := range cases {
					t.Run(tc.name, func(t *testing.T) {
						got := mcpInvalidPath(t, env, env.OperatorSecret, opHeaders[era], "vecnet_agent_add", tc.args)
						if got != tc.path {
							t.Fatalf("path = %s", got)
						}
						raw := mcpCallRaw(t, env, env.OperatorSecret, opHeaders[era], "tools/call", map[string]any{"name": "vecnet_agent_add", "arguments": tc.args})
						assertNoSecrets(t, raw, routineURL, routineKey, header, filePath)
					})
				}
			})
		}
		if takeMCPCensus(t, env) != before {
			t.Fatal("a schema rejection wrote onboarding state")
		}
	})

	t.Run("ordinary peer and forged as", func(t *testing.T) {
		args := agentAddArgs("session", "forged-1", "Nope", nil)
		for _, tc := range []struct{ name, secret string }{
			{"peer", peerSecret},
			{"principal", principalSecret},
		} {
			t.Run(tc.name, func(t *testing.T) {
				mcpToolError(t, env, tc.secret, modern(), "vecnet_agent_kinds", map[string]any{}, "forbidden")
				mcpToolError(t, env, tc.secret, modern(), "vecnet_agent_add", args, "forbidden")
				mcpToolError(t, env, tc.secret, modern(), "vecnet_agent_status", map[string]any{"request_id": "forged-1"}, "forbidden")
			})
		}
		if got := mcpInvalidPath(t, env, env.OperatorSecret, modern(), "vecnet_agent_add", withTop(args, "as", env.OperatorPeer)); got != "/as" {
			t.Fatalf("forged as path = %s", got)
		}
		if got := mcpInvalidPath(t, env, peerSecret, modern(), "vecnet_agent_add", withTop(args, "as", env.OperatorPeer)); got != "/as" {
			t.Fatalf("peer forged as path = %s", got)
		}
		if got := mcpInvalidPath(t, env, env.OperatorSecret, modern(), "vecnet_agent_status", map[string]any{"request_id": "forged-1", "as": peerSecret}); got != "/as" {
			t.Fatalf("status forged as path = %s", got)
		}
		var opWho, peerWho map[string]string
		toolOK(t, env, env.OperatorSecret, "vecnet_whoami", map[string]any{}, &opWho)
		toolOK(t, env, peerSecret, "vecnet_whoami", map[string]any{}, &peerWho)
		if opWho["id"] == "" || peerWho["id"] == "" || opWho["id"] == peerWho["id"] || peerWho["kind"] != "peer" {
			t.Fatal("whoami did not keep the operator and the ordinary peer distinct")
		}
		if takeMCPCensus(t, env) != before {
			t.Fatal("a refused caller wrote onboarding state")
		}
	})

	const (
		grokID   = "grok-human-1"
		grokName = "Helper Bot"
		legacyID = "legacy-human-1"
	)
	grokArgs := agentAddArgs("grokbot", grokID, grokName, map[string]any{
		"connector_name": "vecnet-helper-bot",
		"skip_mcp":       false,
	})

	t.Run("grokbot add creates nothing", func(t *testing.T) {
		for _, era := range []string{mcpserver.ProtoCurrent, mcpserver.ProtoLegacy} {
			t.Run(era, func(t *testing.T) {
				got := mcpToolOK(t, env, env.OperatorSecret, opHeaders[era], "vecnet_agent_add", grokArgs)
				wantPath := agentWebPath("grokbot", grokName, grokID)
				if !reflect.DeepEqual(got, map[string]any{
					"request_id": grokID,
					"status":     "needs_human",
					"web_path":   wantPath,
				}) {
					t.Fatalf("grokbot add = %#v", got)
				}
				assertNoSecrets(t, mustRaw(t, got))
			})
		}
		if takeMCPCensus(t, env) != before {
			t.Fatal("grokbot add created a peer, route, token, link code or receipt")
		}
	})

	t.Run("legacy add", func(t *testing.T) {
		got := mcpToolOK(t, env, env.OperatorSecret, modern(), "vecnet_agent_add", agentAddArgs("session", legacyID, "Legacy One", nil))
		if got["web_path"] != agentWebPath("session", "Legacy One", legacyID) || got["request_id"] != legacyID {
			t.Fatalf("legacy add = %#v", got)
		}
		assertNoSecrets(t, mustRaw(t, got))
		dot := mcpToolOK(t, env, env.OperatorSecret, modern(), "vecnet_agent_add", agentAddArgs("dot", "dot-human-1", "Dot One", nil))
		if dot["web_path"] != agentWebPath("dot", "Dot One", "dot-human-1") {
			t.Fatalf("dot add = %#v", dot)
		}
		assertNoSecrets(t, mustRaw(t, dot))
		if takeMCPCensus(t, env) != before {
			t.Fatal("legacy add minted an enrol token or a link code")
		}
	})

	t.Run("changed args before a receipt", func(t *testing.T) {
		changed := agentAddArgs("grokbot", grokID, "Helper Bot Two", map[string]any{
			"connector_name": "vecnet-helper-two",
			"skip_mcp":       true,
		})
		got := mcpToolOK(t, env, env.OperatorSecret, modern(), "vecnet_agent_add", changed)
		if got["status"] != "needs_human" || got["request_id"] != grokID || got["web_path"] != agentWebPath("grokbot", "Helper Bot Two", grokID) {
			t.Fatalf("changed add = %#v", got)
		}
		if takeMCPCensus(t, env) != before {
			t.Fatal("changed args stored a pending action")
		}
	})

	t.Run("status before human submit", func(t *testing.T) {
		for _, id := range []string{grokID, legacyID} {
			errBody := mcpToolError(t, env, env.OperatorSecret, modern(), "vecnet_agent_status", map[string]any{"request_id": id}, "not_found")
			if errBody.Error.Details["request_id"] != id {
				t.Fatal("status did not name the request")
			}
			assertNoSecrets(t, mustRaw(t, errBody))
		}
		if takeMCPCensus(t, env) != before {
			t.Fatal("status before submit wrote onboarding state")
		}
	})

	t.Run("no mint on rejected legacy input", func(t *testing.T) {
		errBody := mcpToolError(t, env, env.OperatorSecret, modern(), "vecnet_agent_add", agentAddArgs("session", "ttl-bad", "Bad TTL", map[string]any{"enrol_ttl_seconds": 1}), "invalid_value")
		if errBody.Error.Details["field"] != "enrol_ttl_seconds" {
			t.Fatal("details.field mismatch")
		}
		stale := agentAddArgs("session", "stale-1", "Stale", nil)
		stale["spec_version"] = "1999-01-01"
		mcpToolError(t, env, env.OperatorSecret, modern(), "vecnet_agent_add", stale, "stale_spec")
		badConnector := agentAddArgs("grokbot", "bad-connector", "Helper Bot", map[string]any{"connector_name": "not-a-connector"})
		mcpToolError(t, env, env.OperatorSecret, modern(), "vecnet_agent_add", badConnector, "invalid_value")
		if takeMCPCensus(t, env) != before {
			t.Fatal("a rejected add minted a token or wrote a receipt")
		}
	})

	routineURL := fakeRoutineURL(t)
	routineKey := fakeRoutineKey(t)
	var createdPeer string
	var legacyToken string
	t.Run("human http submit", func(t *testing.T) {
		resp, raw := env.Do(env.OperatorSecret, http.MethodPost, "/v1/admin/agents", map[string]any{
			"kind": "grokbot", "spec_version": onboarding.SpecVersion, "request_id": grokID,
			"inputs": map[string]any{
				"name": grokName, "webhook_url": routineURL, "webhook_key": "Bearer " + routineKey,
				"connector_name": "vecnet-helper-bot", "skip_mcp": false,
			},
		})
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("grokbot human submit status = %d", resp.StatusCode)
		}
		var created onboarding.SubmitResult
		if json.Unmarshal(raw, &created) != nil || created.PeerID == "" || created.RequestID != grokID {
			t.Fatal("grokbot human submit did not return a peer")
		}
		createdPeer = created.PeerID
		resp, raw = env.Do(env.OperatorSecret, http.MethodPost, "/v1/admin/agents", map[string]any{
			"kind": "session", "spec_version": onboarding.SpecVersion, "request_id": legacyID,
			"inputs": map[string]any{"name": "Legacy One"},
		})
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("legacy human submit status = %d", resp.StatusCode)
		}
		var legacy onboarding.SubmitResult
		if json.Unmarshal(raw, &legacy) != nil || legacy.PeerID == "" {
			t.Fatal("legacy human submit did not return a peer")
		}
		legacyToken = mcpEnrolTokenRE.FindString(legacy.HumanOutput)
		if legacyToken == "" {
			t.Fatal("legacy human submit did not return an enrolment token")
		}
	})
	afterHuman := takeMCPCensus(t, env)
	if afterHuman.peers != before.peers+2 || afterHuman.enrols != before.enrols+1 || afterHuman.links != 0 || afterHuman.routes != before.routes+1 || afterHuman.receipts <= before.receipts {
		t.Fatal("human submit did not record the grokbot and the legacy invite, or it minted a link code")
	}

	t.Run("status after human submit", func(t *testing.T) {
		for _, era := range []string{mcpserver.ProtoCurrent, mcpserver.ProtoLegacy} {
			t.Run(era, func(t *testing.T) {
				got := mcpToolOK(t, env, env.OperatorSecret, opHeaders[era], "vecnet_agent_status", map[string]any{"request_id": grokID})
				raw := mustRaw(t, got)
				assertNoSecrets(t, raw, routineURL, routineKey, "Bearer "+routineKey, legacyToken)
				if got["request_id"] != grokID || got["kind"] != "grokbot" || got["peer_id"] != createdPeer {
					t.Fatal("grokbot status did not return the human submit")
				}
				if got["auth_state"] != onboarding.AuthUnlinked || got["connector_name"] != "vecnet-helper-bot" {
					t.Fatal("grokbot status asserted a connector or credential the human has not linked")
				}
				if _, ok := got["human_output"]; ok || strings.Contains(string(raw), "human_output") || strings.Contains(string(raw), "connected") {
					t.Fatal("grokbot status is not secret-free")
				}
				legacy := mcpToolOK(t, env, env.OperatorSecret, opHeaders[era], "vecnet_agent_status", map[string]any{"request_id": legacyID})
				legacyRaw := mustRaw(t, legacy)
				assertNoSecrets(t, legacyRaw, routineURL, routineKey, legacyToken)
				if legacy["request_id"] != legacyID || legacy["kind"] != "session" || legacy["peer_id"] == "" || legacy["peer_id"] == createdPeer {
					t.Fatal("legacy status did not return its own peer")
				}
				if _, ok := legacy["human_output"]; ok || strings.Contains(string(legacyRaw), "human_output") {
					t.Fatal("legacy status returned human output")
				}
			})
		}
		res, _ := mcpToolResult(t, env, env.OperatorSecret, modern(), "vecnet_agent_kinds", map[string]any{})
		var listed struct {
			Kinds []onboarding.KindSpec `json:"kinds"`
		}
		if json.Unmarshal(res.StructuredContent, &listed) != nil || len(listed.Kinds) != 6 {
			t.Fatal("kinds result is not the registry list")
		}
		rawKinds, err := json.Marshal(listed.Kinds)
		if err != nil {
			t.Fatal(err)
		}
		wantKinds, err := json.Marshal(onboarding.Specs())
		if err != nil {
			t.Fatal(err)
		}
		if string(rawKinds) != string(wantKinds) {
			t.Fatal("kinds are not the hub registry")
		}
		if !strings.Contains(string(rawKinds), "If whoami does not match, stop") {
			t.Fatal("kinds dropped the whoami mismatch stop")
		}
		if takeMCPCensus(t, env) != afterHuman {
			t.Fatal("status or kinds after submit wrote onboarding state")
		}
	})

	t.Run("changed args and no mint-then-strip", func(t *testing.T) {
		changed := agentAddArgs("grokbot", grokID, "Helper Bot Two", map[string]any{"connector_name": "vecnet-helper-two"})
		errBody := mcpToolError(t, env, env.OperatorSecret, modern(), "vecnet_agent_add", changed, "idempotency_conflict")
		if errBody.Error.Details["request_id"] != grokID {
			t.Fatal("conflict did not name the request")
		}
		assertNoSecrets(t, mustRaw(t, errBody), routineURL, routineKey, legacyToken, "Helper Bot Two")
		same := mcpToolOK(t, env, env.OperatorSecret, modern(), "vecnet_agent_add", grokArgs)
		if same["status"] != "needs_human" || same["request_id"] != grokID || same["web_path"] != agentWebPath("grokbot", grokName, grokID) {
			t.Fatalf("same-args add = %#v", same)
		}
		assertNoSecrets(t, mustRaw(t, same), routineURL, routineKey, legacyToken)
		legacyChanged := mcpToolError(t, env, env.OperatorSecret, modern(), "vecnet_agent_add", agentAddArgs("session", legacyID, "Legacy Two", nil), "idempotency_conflict")
		assertNoSecrets(t, mustRaw(t, legacyChanged), legacyToken, "Legacy Two")
		legacySame := mcpToolOK(t, env, env.OperatorSecret, modern(), "vecnet_agent_add", agentAddArgs("session", legacyID, "Legacy One", nil))
		if legacySame["web_path"] != agentWebPath("session", "Legacy One", legacyID) {
			t.Fatalf("legacy replay = %#v", legacySame)
		}
		assertNoSecrets(t, mustRaw(t, legacySame), legacyToken)
		if takeMCPCensus(t, env) != afterHuman {
			t.Fatal("changed args or a replay minted another token or peer")
		}
	})

	t.Run("disabled operator credential", func(t *testing.T) {
		if _, err := env.Hub.DB.Exec(`UPDATE peers SET disabled=1 WHERE peer_id=?`, env.OperatorPeer); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if _, err := env.Hub.DB.Exec(`UPDATE peers SET disabled=0 WHERE peer_id=?`, env.OperatorPeer); err != nil {
				t.Errorf("restore operator peer: %v", err)
			}
		})
		mcpToolError(t, env, env.OperatorSecret, modern(), "vecnet_agent_add", grokArgs, "forbidden")
		mcpToolError(t, env, env.OperatorSecret, modern(), "vecnet_agent_status", map[string]any{"request_id": grokID}, "forbidden")
		mcpToolError(t, env, env.OperatorSecret, modern(), "vecnet_agent_kinds", map[string]any{}, "forbidden")
		if takeMCPCensus(t, env) != afterHuman {
			t.Fatal("a disabled operator peer wrote onboarding state")
		}
	})

	t.Run("revoked operator credential", func(t *testing.T) {
		if _, err := env.Hub.DB.Exec(`UPDATE credentials SET revoked_at=? WHERE kind='operator' AND revoked_at IS NULL`, venv.FormatTime(env.Now.Now())); err != nil {
			t.Fatal(err)
		}
		status, raw := mcpPost(t, env, env.OperatorSecret, modern(), "tools/call", map[string]any{
			"name": "vecnet_agent_add", "arguments": grokArgs,
		})
		if status != http.StatusUnauthorized {
			t.Fatalf("revoked operator status = %d", status)
		}
		envBody := decodeEnvelope(t, raw)
		if envBody.Error.Code != "unauthenticated" {
			t.Fatal("revoked operator was authenticated")
		}
		assertNoSecrets(t, raw, routineURL, routineKey, legacyToken)
		if takeMCPCensus(t, env) != afterHuman {
			t.Fatal("a revoked operator credential wrote onboarding state")
		}
	})
	assertNoConductor(t, env)
}

func agentWebPath(kind, name, requestID string) string {
	q := url.Values{}
	q.Set("kind", kind)
	q.Set("name", name)
	q.Set("request_id", requestID)
	return "/ui/#add-agent?" + q.Encode()
}

func agentAddArgs(kind, id, name string, extra map[string]any) map[string]any {
	inputs := map[string]any{"name": name}
	for k, v := range extra {
		inputs[k] = v
	}
	return map[string]any{
		"kind":         kind,
		"spec_version": onboarding.SpecVersion,
		"request_id":   id,
		"inputs":       inputs,
	}
}

func withTop(base map[string]any, key string, val any) map[string]any {
	out := map[string]any{}
	for k, v := range base {
		out[k] = v
	}
	out[key] = val
	return out
}

func withInput(base map[string]any, key string, val any) map[string]any {
	out := withTop(base, "inputs", nil)
	inputs := map[string]any{}
	for k, v := range base["inputs"].(map[string]any) {
		inputs[k] = v
	}
	inputs[key] = val
	out["inputs"] = inputs
	return out
}

func mcpEraHeaders(t *testing.T, env *testhub.Env, secret, era string) map[string]string {
	t.Helper()
	if era == mcpserver.ProtoLegacy {
		return map[string]string{
			"Mcp-Session-Id":       legacySession(t, env, secret),
			"MCP-Protocol-Version": mcpserver.ProtoLegacy,
			"Accept":               "application/json",
		}
	}
	return modern()
}

func mcpPost(t *testing.T, env *testhub.Env, secret string, headers map[string]string, method string, params any) (int, []byte) {
	t.Helper()
	body := map[string]any{"jsonrpc": "2.0", "id": 1, "method": method}
	if params != nil {
		body["params"] = params
	}
	resp, raw := do(t, env, call{secret: secret, headers: headers, body: body})
	return resp.StatusCode, raw
}

func mcpCallRaw(t *testing.T, env *testhub.Env, secret string, headers map[string]string, method string, params any) []byte {
	t.Helper()
	status, raw := mcpPost(t, env, secret, headers, method, params)
	if status != http.StatusOK {
		t.Fatalf("%s: HTTP %d", method, status)
	}
	return raw
}

func mcpTools(t *testing.T, env *testhub.Env, secret string, headers map[string]string) []map[string]any {
	t.Helper()
	raw := mcpCallRaw(t, env, secret, headers, "tools/list", nil)
	r := decodeRPC(t, raw)
	if r.Error != nil {
		t.Fatalf("tools/list: %s", r.Error.Message)
	}
	var listed struct {
		Tools []map[string]any `json:"tools"`
	}
	if json.Unmarshal(r.Result, &listed) != nil {
		t.Fatal("tools/list result is not a tool catalogue")
	}
	return listed.Tools
}

func toolNameSet(t *testing.T, tools []map[string]any) map[string]bool {
	t.Helper()
	names := map[string]bool{}
	for _, tool := range tools {
		name, _ := tool["name"].(string)
		if name == "" || names[name] {
			t.Fatal("tools/list has a missing or repeated name")
		}
		names[name] = true
	}
	return names
}

func toolByName(t *testing.T, tools []map[string]any, name string) map[string]any {
	t.Helper()
	for _, tool := range tools {
		if tool["name"] == name {
			return tool
		}
	}
	t.Fatalf("tools/list lacks %s", name)
	return nil
}

func containsAny(list []any, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}

func mcpToolResult(t *testing.T, env *testhub.Env, secret string, headers map[string]string, name string, args any) (toolResult, []byte) {
	t.Helper()
	raw := mcpCallRaw(t, env, secret, headers, "tools/call", map[string]any{"name": name, "arguments": args})
	r := decodeRPC(t, raw)
	if r.Error != nil {
		t.Fatalf("%s: %s", name, r.Error.Message)
	}
	var res toolResult
	if json.Unmarshal(r.Result, &res) != nil {
		t.Fatalf("%s: result is not a tool result", name)
	}
	return res, raw
}

func mcpToolOK(t *testing.T, env *testhub.Env, secret string, headers map[string]string, name string, args any) map[string]any {
	t.Helper()
	res, raw := mcpToolResult(t, env, secret, headers, name, args)
	if res.IsError {
		t.Fatalf("%s: tool error", name)
	}
	if len(res.Content) != 1 || res.Content[0].Type != "text" {
		t.Fatalf("%s: content is not one text item", name)
	}
	var fromText, fromStruct map[string]any
	if json.Unmarshal([]byte(res.Content[0].Text), &fromText) != nil || json.Unmarshal(res.StructuredContent, &fromStruct) != nil {
		t.Fatalf("%s: tool text is not the structured object", name)
	}
	if !reflect.DeepEqual(fromText, fromStruct) {
		t.Fatalf("%s: text differs from structuredContent", name)
	}
	if strings.Contains(string(raw), "human_output") {
		t.Fatalf("%s: result contains human_output", name)
	}
	return fromStruct
}

func mcpToolError(t *testing.T, env *testhub.Env, secret string, headers map[string]string, name string, args any, code string) toolErrorBody {
	t.Helper()
	res, _ := mcpToolResult(t, env, secret, headers, name, args)
	if !res.IsError {
		t.Fatalf("%s: error = false, want %s", name, code)
	}
	var e toolErrorBody
	if json.Unmarshal(res.StructuredContent, &e) != nil || e.Error.Code != code || e.Error.Details == nil {
		t.Fatalf("%s: error = %s, want %s", name, e.Error.Code, code)
	}
	if len(res.Content) != 1 || !strings.HasPrefix(res.Content[0].Text, code+": ") {
		t.Fatalf("%s: error text does not start with the code", name)
	}
	return e
}

func mcpInvalidPath(t *testing.T, env *testhub.Env, secret string, headers map[string]string, name string, args any) string {
	t.Helper()
	raw := mcpCallRaw(t, env, secret, headers, "tools/call", map[string]any{"name": name, "arguments": args})
	r := decodeRPC(t, raw)
	if r.Error == nil || r.Error.Code != mcpserver.ErrInvalidParams {
		t.Fatalf("%s: want invalid params", name)
	}
	var data struct {
		Path *string `json:"path"`
	}
	if json.Unmarshal(r.Error.Data, &data) != nil || data.Path == nil {
		t.Fatalf("%s: invalid params have no path", name)
	}
	return *data.Path
}

func assertNoSecrets(t *testing.T, raw []byte, secrets ...string) {
	t.Helper()
	text := string(raw)
	for _, banned := range []string{"human_output", "vn_enrol_", "crsr_", "api2.cursor.sh", "Bearer "} {
		if strings.Contains(text, banned) {
			t.Fatal("response contains a secret or human output")
		}
	}
	for _, secret := range secrets {
		if secret != "" && strings.Contains(text, secret) {
			t.Fatal("response contains a secret or human output")
		}
	}
}

func mustRaw(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func assertNoConductor(t *testing.T, env *testhub.Env) {
	t.Helper()
	var n int
	if err := env.Hub.DB.QueryRow(`SELECT count(*) FROM peers WHERE lower(name) LIKE '%conductor%'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatal("fixture created a conductor peer")
	}
	if err := env.Hub.DB.QueryRow(`SELECT count(*) FROM dot_links`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatal("fixture linked a peer")
	}
}

type mcpCensus struct {
	peers, routes, receipts, messages, enrols, links, creds int
}

func takeMCPCensus(t *testing.T, env *testhub.Env) mcpCensus {
	t.Helper()
	q := func(sql string) int {
		t.Helper()
		var n int
		if err := env.Hub.DB.QueryRow(sql).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	return mcpCensus{
		peers:    q(`SELECT count(*) FROM peers`),
		routes:   q(`SELECT count(*) FROM routes`),
		receipts: q(`SELECT count(*) FROM meta WHERE key LIKE 'onboarding.v1/%'`),
		messages: q(`SELECT count(*) FROM messages`),
		enrols:   q(`SELECT count(*) FROM enrol_tokens`),
		links:    q(`SELECT count(*) FROM dot_links`),
		creds:    q(`SELECT count(*) FROM credentials`),
	}
}

func fakeRoutineKey(t *testing.T) string {
	t.Helper()
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		t.Fatal(err)
	}
	return "crsr_" + hex.EncodeToString(raw[:])
}

func fakeRoutineURL(t *testing.T) string {
	t.Helper()
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		t.Fatal(err)
	}
	h := hex.EncodeToString(raw[:])
	return "https://api2.cursor.sh/automations/webhook/" + h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}
