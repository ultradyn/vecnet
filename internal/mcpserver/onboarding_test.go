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
				first := mcpTools(t, env, env.OperatorSecret, opHeaders[era], env.OperatorSecret, peerSecret, principalSecret)
				second := mcpTools(t, env, env.OperatorSecret, opHeaders[era], env.OperatorSecret, peerSecret, principalSecret)
				if !reflect.DeepEqual(first, second) {
					t.Fatal("operator tools/list is not stable")
				}
				peerList := mcpTools(t, env, peerSecret, peerHeaders[era], env.OperatorSecret, peerSecret, principalSecret)
				if len(peerList) != 26 {
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
				if len(first) != 29 {
					t.Fatalf("operator tools = %d, want the catalogue plus the three agent tools", len(first))
				}
				for _, name := range []string{"vecnet_agent_kinds", "vecnet_agent_add", "vecnet_agent_status"} {
					tool := toolByName(t, first, name)
					ann, _ := tool["annotations"].(map[string]any)
					if ann["readOnlyHint"] != true || ann["destructiveHint"] != false || ann["openWorldHint"] != false {
						t.Fatal(discoveryMismatch())
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
							t.Fatal(schemaExposes())
						}
					}
					wantSchemes := []any{map[string]any{"type": "oauth2", "scopes": []any{"vecnet"}}}
					if !reflect.DeepEqual(tool["securitySchemes"], wantSchemes) {
						t.Fatal(discoveryMismatch())
					}
				}
				add := toolByName(t, first, "vecnet_agent_add")
				input := add["inputSchema"].(map[string]any)
				required, _ := input["required"].([]any)
				for _, key := range []string{"kind", "spec_version", "request_id", "inputs"} {
					if !containsAny(required, key) {
						t.Fatal(discoveryMismatch())
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
			{name: "webhook url", args: withTop(base, "webhook_url", routineURL), path: ""},
			{name: "webhook key", args: withTop(base, "webhook_key", routineKey), path: ""},
			{name: "authorization header", args: withTop(base, "authorization", header), path: ""},
			{name: "header", args: withInput(base, "header", header), path: "/inputs"},
			{name: "key file", args: withTop(base, "key_file", filePath), path: ""},
			{name: "file path", args: withInput(base, "file_path", filePath), path: "/inputs"},
			{name: "webhook url in inputs", args: withInput(base, "webhook_url", routineURL), path: "/inputs"},
			{name: "webhook key in inputs", args: withInput(base, "webhook_key", routineKey), path: "/inputs"},
		}
		for _, era := range []string{mcpserver.ProtoCurrent, mcpserver.ProtoLegacy} {
			t.Run(era, func(t *testing.T) {
				for _, tc := range cases {
					t.Run(tc.name, func(t *testing.T) {
						got := mcpInvalidPath(t, env, env.OperatorSecret, opHeaders[era], "vecnet_agent_add", tc.args, peerSecret, principalSecret)
						if got != tc.path {
							t.Fatal("invalid params path mismatch")
						}
						raw := mcpCallRaw(t, env, env.OperatorSecret, opHeaders[era], "tools/call", map[string]any{"name": "vecnet_agent_add", "arguments": tc.args})
						assertNoSecrets(t, raw, routineURL, routineKey, header, filePath, peerSecret, principalSecret)
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
				mcpToolError(t, env, tc.secret, modern(), "vecnet_agent_kinds", map[string]any{}, "forbidden", env.OperatorSecret, peerSecret, principalSecret)
				mcpToolError(t, env, tc.secret, modern(), "vecnet_agent_add", args, "forbidden", env.OperatorSecret, peerSecret, principalSecret)
				mcpToolError(t, env, tc.secret, modern(), "vecnet_agent_status", map[string]any{"request_id": "forged-1"}, "forbidden", env.OperatorSecret, peerSecret, principalSecret)
			})
		}
		if got := mcpInvalidPath(t, env, env.OperatorSecret, modern(), "vecnet_agent_add", withTop(args, "as", env.OperatorPeer), peerSecret, principalSecret); got != "" {
			t.Fatal("forged as path mismatch")
		}
		if got := mcpInvalidPath(t, env, peerSecret, modern(), "vecnet_agent_add", withTop(args, "as", env.OperatorPeer), env.OperatorSecret, principalSecret); got != "" {
			t.Fatal("peer forged as path mismatch")
		}
		if got := mcpInvalidPath(t, env, env.OperatorSecret, modern(), "vecnet_agent_status", map[string]any{"request_id": "forged-1", "as": peerSecret}, peerSecret, principalSecret); got != "" {
			t.Fatal("status forged as path mismatch")
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
				got := mcpToolOK(t, env, env.OperatorSecret, opHeaders[era], "vecnet_agent_add", grokArgs, peerSecret, principalSecret)
				wantPath := agentWebPath("grokbot", grokName, grokID)
				if !reflect.DeepEqual(got, map[string]any{
					"request_id": grokID,
					"status":     "needs_human",
					"web_path":   wantPath,
				}) {
					t.Fatal("grokbot add result mismatch")
				}
				assertNoSecrets(t, mustRaw(t, got))
			})
		}
		if takeMCPCensus(t, env) != before {
			t.Fatal("grokbot add created a peer, route, token, link code or receipt")
		}
	})

	t.Run("legacy add", func(t *testing.T) {
		extra := []string{peerSecret, principalSecret}
		res, raw := mcpToolResult(t, env, env.OperatorSecret, modern(), "vecnet_agent_add", agentAddArgs("session", legacyID, "Legacy One", nil), extra...)
		if res.IsError || len(res.Content) != 1 {
			t.Fatal("legacy add tool error")
		}
		var got map[string]any
		if json.Unmarshal(res.StructuredContent, &got) != nil || got["web_path"] != agentWebPath("session", "Legacy One", legacyID) || got["request_id"] != legacyID {
			t.Fatal("legacy add result mismatch")
		}
		assertNoSecrets(t, raw, extra...)
		mut := withExtraJSONField(t, raw, "leaked", peerSecret)
		if !wireHasSecret(mut, append([]string{env.OperatorSecret}, extra...)...) {
			t.Fatal("legacy add mutation was not scanned")
		}
		dot := mcpToolOK(t, env, env.OperatorSecret, modern(), "vecnet_agent_add", agentAddArgs("dot", "dot-human-1", "Dot One", nil), extra...)
		if dot["web_path"] != agentWebPath("dot", "Dot One", "dot-human-1") {
			t.Fatal("dot add result mismatch")
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
		if got["status"] != "needs_human" || got["request_id"] != grokID {
			t.Fatal("changed add result mismatch")
		}
		changedQuery := addAgentQuery(t, got["web_path"].(string))
		if changedQuery.Get("kind") != "grokbot" || changedQuery.Get("name") != "Helper Bot Two" || changedQuery.Get("request_id") != grokID || changedQuery.Get("connector_name") != "vecnet-helper-two" || changedQuery.Get("skip_mcp") != "true" {
			t.Fatal("changed add fragment mismatch")
		}
		if takeMCPCensus(t, env) != before {
			t.Fatal("changed args stored a pending action")
		}
	})

	t.Run("status before human submit", func(t *testing.T) {
		for _, id := range []string{grokID, legacyID} {
			errBody, wire := mcpToolError(t, env, env.OperatorSecret, modern(), "vecnet_agent_status", map[string]any{"request_id": id}, "not_found")
			if errBody.Error.Details["request_id"] != id {
				t.Fatal("status did not name the request")
			}
			assertNoSecrets(t, wire)
		}
		if takeMCPCensus(t, env) != before {
			t.Fatal("status before submit wrote onboarding state")
		}
	})

	t.Run("no mint on rejected legacy input", func(t *testing.T) {
		errBody, _ := mcpToolError(t, env, env.OperatorSecret, modern(), "vecnet_agent_add", agentAddArgs("session", "ttl-bad", "Bad TTL", map[string]any{"enrol_ttl_seconds": 1}), "invalid_value")
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
				got := mcpToolOK(t, env, env.OperatorSecret, opHeaders[era], "vecnet_agent_status", map[string]any{"request_id": grokID}, peerSecret, principalSecret, routineKey, legacyToken)
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
				legacy := mcpToolOK(t, env, env.OperatorSecret, opHeaders[era], "vecnet_agent_status", map[string]any{"request_id": legacyID}, peerSecret, principalSecret, routineKey, legacyToken)
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
		res, _ := mcpToolResult(t, env, env.OperatorSecret, modern(), "vecnet_agent_kinds", map[string]any{}, peerSecret, principalSecret, routineKey, legacyToken)
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
		errBody, conflictWire := mcpToolError(t, env, env.OperatorSecret, modern(), "vecnet_agent_add", changed, "idempotency_conflict")
		if errBody.Error.Details["request_id"] != grokID {
			t.Fatal("conflict did not name the request")
		}
		assertNoSecrets(t, conflictWire, routineURL, routineKey, legacyToken, "Helper Bot Two")
		same := mcpToolOK(t, env, env.OperatorSecret, modern(), "vecnet_agent_add", grokArgs)
		if same["status"] != "needs_human" || same["request_id"] != grokID || same["web_path"] != agentWebPath("grokbot", grokName, grokID) {
			t.Fatal("same-args add result mismatch")
		}
		assertNoSecrets(t, mustRaw(t, same), routineURL, routineKey, legacyToken)
		_, legacyWire := mcpToolError(t, env, env.OperatorSecret, modern(), "vecnet_agent_add", agentAddArgs("session", legacyID, "Legacy Two", nil), "idempotency_conflict")
		assertNoSecrets(t, legacyWire, legacyToken, "Legacy Two")
		legacySame := mcpToolOK(t, env, env.OperatorSecret, modern(), "vecnet_agent_add", agentAddArgs("session", legacyID, "Legacy One", nil))
		if legacySame["web_path"] != agentWebPath("session", "Legacy One", legacyID) {
			t.Fatal("legacy replay result mismatch")
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
		assertNoSecrets(t, raw, routineURL, routineKey, legacyToken)
		envBody := decodeEnvelope(t, raw)
		if envBody.Error.Code != "unauthenticated" {
			t.Fatal("revoked operator was authenticated")
		}
		if takeMCPCensus(t, env) != afterHuman {
			t.Fatal("a revoked operator credential wrote onboarding state")
		}
	})
	assertNoConductor(t, env)
}

// TestAgentOnboardMCPSafeRetry covers fix round 1. Invalid request ids must
// not echo a credential (t7 I1). The browser fragment must carry non-default
// safe options (t7 I2). A committed request id conflicts when those options
// change (t7 I3) and does not conflict when only the live peer name changes
// (t7 I4).
func TestAgentOnboardMCPSafeRetry(t *testing.T) {
	env := testhub.Start(t)
	opHeaders := map[string]map[string]string{}
	for _, era := range []string{mcpserver.ProtoCurrent, mcpserver.ProtoLegacy} {
		opHeaders[era] = mcpEraHeaders(t, env, env.OperatorSecret, era)
	}
	prn, _ := env.NewPrincipal("fragment principal")

	t.Run("(t7 I1)", func(t *testing.T) {
		routineKey := fakeRoutineKey(t)
		header := "Bearer " + routineKey
		shaped := routineKey + "/"
		for _, era := range []string{mcpserver.ProtoCurrent, mcpserver.ProtoLegacy} {
			t.Run(era, func(t *testing.T) {
				for _, id := range []string{header, shaped} {
					addArgs := agentAddArgs("session", id, "Nope", nil)
					statusArgs := map[string]any{"request_id": id}
					for _, tc := range []struct {
						name string
						args any
					}{
						{name: "vecnet_agent_add", args: addArgs},
						{name: "vecnet_agent_status", args: statusArgs},
					} {
						t.Run(tc.name, func(t *testing.T) {
							raw := mcpCallRaw(t, env, env.OperatorSecret, opHeaders[era], "tools/call", map[string]any{
								"name": tc.name, "arguments": tc.args,
							})
							assertNoSecrets(t, raw, routineKey, header, shaped)
							text := string(raw)
							if !strings.Contains(text, "-32602") || !strings.Contains(text, `"/request_id"`) {
								t.Fatal("invalid request_id was not rejected as invalid params at the field")
							}
						})
					}
				}
			})
		}
	})

	t.Run("(t7 I2)", func(t *testing.T) {
		grok := mcpToolOK(t, env, env.OperatorSecret, modern(), "vecnet_agent_add", agentAddArgs("grokbot", "frag-grok", "Helper Bot", map[string]any{
			"connector_name": "vecnet-custom-1",
			"skip_mcp":       true,
		}))
		q := addAgentQuery(t, grok["web_path"].(string))
		if q.Get("kind") != "grokbot" || q.Get("name") != "Helper Bot" || q.Get("request_id") != "frag-grok" {
			t.Fatal("fragment dropped kind, name, or request_id")
		}
		if q.Get("connector_name") != "vecnet-custom-1" || q.Get("skip_mcp") != "true" {
			t.Fatal("fragment dropped a non-default grok option")
		}
		if queryHas(q, "name_open") || queryHas(q, "enrol_ttl_seconds") || queryHas(q, "principal_id") {
			t.Fatal("fragment included an option the request did not set")
		}

		legacy := mcpToolOK(t, env, env.OperatorSecret, modern(), "vecnet_agent_add", agentAddArgs("session", "frag-legacy", "Suggested", map[string]any{
			"name_open":         true,
			"enrol_ttl_seconds": 3600,
			"principal_id":      prn,
		}))
		lq := addAgentQuery(t, legacy["web_path"].(string))
		if lq.Get("kind") != "session" || lq.Get("name") != "Suggested" || lq.Get("request_id") != "frag-legacy" {
			t.Fatal("fragment dropped kind, name, or request_id")
		}
		if lq.Get("name_open") != "true" || lq.Get("enrol_ttl_seconds") != "3600" || lq.Get("principal_id") != prn {
			t.Fatal("fragment dropped a non-default legacy option")
		}
		if queryHas(lq, "skip_mcp") || queryHas(lq, "connector_name") {
			t.Fatal("fragment included an option the request did not set")
		}

		defaults := mcpToolOK(t, env, env.OperatorSecret, modern(), "vecnet_agent_add", agentAddArgs("grokbot", "frag-default", "Helper Bot", map[string]any{
			"connector_name": "vecnet-helper-bot",
			"skip_mcp":       false,
		}))
		dq := addAgentQuery(t, defaults["web_path"].(string))
		if dq.Get("kind") != "grokbot" || dq.Get("name") != "Helper Bot" || dq.Get("request_id") != "frag-default" {
			t.Fatal("fragment dropped kind, name, or request_id")
		}
		if queryHas(dq, "connector_name") || queryHas(dq, "skip_mcp") || queryHas(dq, "name_open") || queryHas(dq, "enrol_ttl_seconds") || queryHas(dq, "principal_id") {
			t.Fatal("fragment included a default option")
		}
	})

	t.Run("(t7 I3)", func(t *testing.T) {
		routineURL := fakeRoutineURL(t)
		routineKey := fakeRoutineKey(t)
		skipID := "i3-skip"
		skipArgs := agentAddArgs("grokbot", skipID, "Skip Bot", map[string]any{
			"connector_name": "vecnet-skip-bot",
			"skip_mcp":       true,
		})
		if mcpToolOK(t, env, env.OperatorSecret, modern(), "vecnet_agent_add", skipArgs)["status"] != "needs_human" {
			t.Fatal("add result mismatch")
		}
		mustHumanSubmit(t, env, map[string]any{
			"kind": "grokbot", "spec_version": onboarding.SpecVersion, "request_id": skipID,
			"inputs": map[string]any{
				"name": "Skip Bot", "webhook_url": routineURL, "webhook_key": "Bearer " + routineKey,
				"connector_name": "vecnet-skip-bot", "skip_mcp": true,
			},
		})
		if mcpToolOK(t, env, env.OperatorSecret, modern(), "vecnet_agent_add", skipArgs)["status"] != "needs_human" {
			t.Fatal("identical grok retry conflicted")
		}
		skipChanged := agentAddArgs("grokbot", skipID, "Skip Bot", map[string]any{
			"connector_name": "vecnet-skip-bot",
			"skip_mcp":       false,
		})
		got, skipWire := mcpToolError(t, env, env.OperatorSecret, modern(), "vecnet_agent_add", skipChanged, "idempotency_conflict")
		assertNoSecrets(t, skipWire, routineKey, routineURL)
		if got.Error.Details["request_id"] != skipID {
			t.Fatal("conflict did not name the request")
		}

		ttlID := "i3-ttl"
		ttlArgs := agentAddArgs("session", ttlID, "TTL Bot", map[string]any{"enrol_ttl_seconds": 3600})
		mustHumanSubmit(t, env, map[string]any{
			"kind": "session", "spec_version": onboarding.SpecVersion, "request_id": ttlID,
			"inputs": map[string]any{"name": "TTL Bot", "enrol_ttl_seconds": 3600},
		})
		if mcpToolOK(t, env, env.OperatorSecret, modern(), "vecnet_agent_add", ttlArgs)["status"] != "needs_human" {
			t.Fatal("identical ttl retry conflicted")
		}
		got, _ = mcpToolError(t, env, env.OperatorSecret, modern(), "vecnet_agent_add", agentAddArgs("session", ttlID, "TTL Bot", map[string]any{"enrol_ttl_seconds": 7200}), "idempotency_conflict")
		if got.Error.Details["request_id"] != ttlID {
			t.Fatal("conflict did not name the request")
		}

		openID := "i3-open"
		openArgs := agentAddArgs("session", openID, "Suggested", map[string]any{"name_open": true})
		mustHumanSubmit(t, env, map[string]any{
			"kind": "session", "spec_version": onboarding.SpecVersion, "request_id": openID,
			"inputs": map[string]any{"name": "Suggested", "name_open": true},
		})
		if mcpToolOK(t, env, env.OperatorSecret, modern(), "vecnet_agent_add", openArgs)["status"] != "needs_human" {
			t.Fatal("identical open-name retry conflicted")
		}
		got, _ = mcpToolError(t, env, env.OperatorSecret, modern(), "vecnet_agent_add", agentAddArgs("session", openID, "Suggested", nil), "idempotency_conflict")
		if got.Error.Details["request_id"] != openID {
			t.Fatal("conflict did not name the request")
		}

		prnID := "i3-prn"
		prnArgs := agentAddArgs("session", prnID, "Owned Bot", map[string]any{"principal_id": prn})
		mustHumanSubmit(t, env, map[string]any{
			"kind": "session", "spec_version": onboarding.SpecVersion, "request_id": prnID,
			"inputs": map[string]any{"name": "Owned Bot", "principal_id": prn},
		})
		if mcpToolOK(t, env, env.OperatorSecret, modern(), "vecnet_agent_add", prnArgs)["status"] != "needs_human" {
			t.Fatal("identical principal retry conflicted")
		}
		got, _ = mcpToolError(t, env, env.OperatorSecret, modern(), "vecnet_agent_add", agentAddArgs("session", prnID, "Owned Bot", nil), "idempotency_conflict")
		if got.Error.Details["request_id"] != prnID {
			t.Fatal("conflict did not name the request")
		}
	})

	t.Run("(t7 I4)", func(t *testing.T) {
		id := "i4-rename"
		args := agentAddArgs("session", id, "Stable Name", nil)
		created := mustHumanSubmit(t, env, map[string]any{
			"kind": "session", "spec_version": onboarding.SpecVersion, "request_id": id,
			"inputs": map[string]any{"name": "Stable Name"},
		})
		if _, err := env.Hub.DB.Exec(`UPDATE peers SET name=? WHERE peer_id=?`, "Renamed Peer", created.PeerID); err != nil {
			t.Fatal(err)
		}
		if mcpToolOK(t, env, env.OperatorSecret, modern(), "vecnet_agent_add", args)["status"] != "needs_human" {
			t.Fatal("unchanged request conflicted after a peer rename")
		}
		if _, err := env.Hub.DB.Exec(`UPDATE peers SET disabled=1 WHERE peer_id=?`, created.PeerID); err != nil {
			t.Fatal(err)
		}
		if mcpToolOK(t, env, env.OperatorSecret, modern(), "vecnet_agent_add", args)["status"] != "needs_human" {
			t.Fatal("unchanged request conflicted after the peer was disabled")
		}

		openID := "i4-open"
		openArgs := agentAddArgs("launcher", openID, "Suggested", map[string]any{"name_open": true})
		opened := mustHumanSubmit(t, env, map[string]any{
			"kind": "launcher", "spec_version": onboarding.SpecVersion, "request_id": openID,
			"inputs": map[string]any{"name": "Suggested", "name_open": true},
		})
		if _, err := env.Hub.DB.Exec(`UPDATE peers SET name=? WHERE peer_id=?`, "Chosen Later", opened.PeerID); err != nil {
			t.Fatal(err)
		}
		if mcpToolOK(t, env, env.OperatorSecret, modern(), "vecnet_agent_add", openArgs)["status"] != "needs_human" {
			t.Fatal("unchanged open-name request conflicted after enrolment renamed the peer")
		}
	})

	t.Run("(t7 M1)", func(t *testing.T) {
		routineKey := fakeRoutineKey(t)
		header := "Bearer " + routineKey
		raw := mcpCallRaw(t, env, env.OperatorSecret, modern(), "tools/call", map[string]any{
			"name": "vecnet_agent_add", "arguments": agentAddArgs("session", header, "Nope", nil),
		})
		assertNoSecrets(t, raw, routineKey, header)
		r := decodeOnboardRPC(t, raw)
		if r.Error == nil || r.Error.Code != mcpserver.ErrInvalidParams {
			t.Fatal("invalid request_id was not an RPC error")
		}
		if strings.Contains(r.Error.Message, routineKey) || strings.Contains(r.Error.Message, "Bearer") {
			t.Fatal("invalid request_id error quoted the rejected value")
		}
	})

	t.Run("(t7 M2)", func(t *testing.T) {
		_, wire := mcpToolError(t, env, env.OperatorSecret, modern(), "vecnet_agent_status", map[string]any{"request_id": "missing-m2"}, "not_found")
		assertNoSecrets(t, wire)
		if !strings.Contains(string(wire), `"content"`) || !strings.Contains(string(wire), "not_found:") {
			t.Fatal("refusal check did not see the complete wire response")
		}
	})
}

// TestAgentOnboardMCPFixRound2 covers the second review. Unknown property
// names must not be quoted (t7 r2 I1). Incomplete historical receipts must
// not treat absent fields as the original request (t7 r2 I2).
// TestEscapedRoutineKeyName is the R35 receipt leak. A printable routine key
// that contains "<" must not be stored when that same value is the peer name.
// json.Marshal writes the bracket as \u003c, so a search of the serialized text
// misses it. Hub submit rolls back, and MCP status in both eras stays free of
// the key.
func TestEscapedRoutineKeyName(t *testing.T) {
	env := testhub.Start(t)
	key := "crsr_leak<key"
	escaped := strings.ReplaceAll(key, "<", `\u003c`)
	routineURL := fakeRoutineURL(t)
	const id = "esc-name-1"
	before := takeMCPCensus(t, env)
	resp, raw := env.Do(env.OperatorSecret, http.MethodPost, "/v1/admin/agents", map[string]any{
		"kind": "grokbot", "spec_version": onboarding.SpecVersion, "request_id": id,
		"inputs": map[string]any{
			"name": key, "webhook_url": routineURL, "webhook_key": key, "skip_mcp": false,
		},
	})
	if resp.StatusCode == http.StatusCreated || strings.Contains(string(raw), key) || strings.Contains(string(raw), escaped) {
		t.Fatal("submit stored or echoed a name that is the routine key")
	}
	if takeMCPCensus(t, env).receipts != before.receipts || takeMCPCensus(t, env).peers != before.peers {
		t.Fatal("submit kept a peer or a receipt")
	}
	var n int
	if err := env.Hub.DB.QueryRow(`SELECT count(*) FROM peers WHERE name=?`, key).Scan(&n); err != nil || n != 0 {
		t.Fatal("submit kept a peer named with the routine key")
	}
	rows, err := env.Hub.DB.Query(`SELECT value FROM meta WHERE key LIKE 'onboarding.v1/%'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(value, key) || strings.Contains(value, escaped) {
			t.Fatal("receipt stored the routine key")
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	for _, era := range []string{mcpserver.ProtoCurrent, mcpserver.ProtoLegacy} {
		t.Run(era, func(t *testing.T) {
			headers := mcpEraHeaders(t, env, env.OperatorSecret, era)
			wire := mcpCallRaw(t, env, env.OperatorSecret, headers, "tools/call", map[string]any{
				"name": "vecnet_agent_status", "arguments": map[string]any{"request_id": id},
			})
			if strings.Contains(string(wire), key) || strings.Contains(string(wire), escaped) {
				t.Fatal("status returned the routine key")
			}
		})
	}
}

func TestAgentOnboardMCPFixRound2(t *testing.T) {
	env := testhub.Start(t)
	opHeaders := map[string]map[string]string{}
	for _, era := range []string{mcpserver.ProtoCurrent, mcpserver.ProtoLegacy} {
		opHeaders[era] = mcpEraHeaders(t, env, env.OperatorSecret, era)
	}

	t.Run("(t7 r2 I1)", func(t *testing.T) {
		routineKey := fakeRoutineKey(t)
		header := "Bearer " + routineKey
		addBase := agentAddArgs("session", "r2-i1", "Nope", map[string]any{"name": "Nope"})
		for _, era := range []string{mcpserver.ProtoCurrent, mcpserver.ProtoLegacy} {
			t.Run(era, func(t *testing.T) {
				cases := []struct {
					tool string
					args map[string]any
					path string
				}{
					{tool: "vecnet_agent_kinds", args: map[string]any{header: "x"}, path: ""},
					{tool: "vecnet_agent_status", args: map[string]any{"request_id": "r2-i1", header: "x"}, path: ""},
					{tool: "vecnet_agent_add", args: withTop(addBase, header, "x"), path: ""},
					{tool: "vecnet_agent_add", args: withInput(addBase, routineKey, "x"), path: "/inputs"},
				}
				for _, tc := range cases {
					t.Run(tc.tool, func(t *testing.T) {
						raw := mcpCallRaw(t, env, env.OperatorSecret, opHeaders[era], "tools/call", map[string]any{
							"name": tc.tool, "arguments": tc.args,
						})
						assertNoSecrets(t, raw, routineKey, header)
						if !strings.Contains(string(raw), "-32602") {
							t.Fatal("unknown property was not invalid params")
						}
						if unknownKeyPath(t, raw) != tc.path {
							t.Fatal("unknown property path was not the container")
						}
					})
				}
			})
		}
	})

	t.Run("(t7 r2 I2)", func(t *testing.T) {
		prn, _ := env.NewPrincipal("historical principal")
		openID := "r2-open"
		mustHumanSubmit(t, env, map[string]any{
			"kind": "session", "spec_version": onboarding.SpecVersion, "request_id": openID,
			"inputs": map[string]any{"name_open": true, "principal_id": prn},
		})
		ageReceipt(t, env, openID, "name", "request_principal_id", "safe_complete")
		openRetry := agentAddArgs("session", openID, "", map[string]any{"name_open": true})
		got, wire := mcpToolError(t, env, env.OperatorSecret, modern(), "vecnet_agent_add", openRetry, "idempotency_conflict")
		assertNoSecrets(t, wire)
		if got.Error.Details["request_id"] != openID {
			t.Fatal("conflict did not name the request")
		}

		namedID := "r2-named"
		namedArgs := agentAddArgs("session", namedID, "Stable Hist", nil)
		mustHumanSubmit(t, env, map[string]any{
			"kind": "session", "spec_version": onboarding.SpecVersion, "request_id": namedID,
			"inputs": map[string]any{"name": "Stable Hist"},
		})
		ageReceipt(t, env, namedID, "name", "safe_complete")
		if mcpToolOK(t, env, env.OperatorSecret, modern(), "vecnet_agent_add", namedArgs)["status"] != "needs_human" {
			t.Fatal("unchanged named historical request conflicted")
		}
		renamed := agentAddArgs("session", namedID, "Other Hist", nil)
		_, renamedWire := mcpToolError(t, env, env.OperatorSecret, modern(), "vecnet_agent_add", renamed, "idempotency_conflict")
		assertNoSecrets(t, renamedWire)
	})

	t.Run("(t7 r3 I1)", func(t *testing.T) {
		routineURL := fakeRoutineURL(t)
		routineKey := fakeRoutineKey(t)
		const (
			id        = "r3-grok"
			name      = "Hist Bot"
			connector = "vecnet-hist-bot"
		)
		same := agentAddArgs("grokbot", id, name, map[string]any{"connector_name": connector})
		mustHumanSubmit(t, env, map[string]any{
			"kind": "grokbot", "spec_version": onboarding.SpecVersion, "request_id": id,
			"inputs": map[string]any{
				"name": name, "webhook_url": routineURL, "webhook_key": "Bearer " + routineKey,
				"connector_name": connector, "skip_mcp": false,
			},
		})
		// f497-era receipt: the safe projection is stored, the later marker is not.
		ageReceipt(t, env, id, "safe_complete")
		changed := agentAddArgs("grokbot", id, name, map[string]any{
			"connector_name": connector, "skip_mcp": true,
		})
		for _, era := range []string{mcpserver.ProtoCurrent, mcpserver.ProtoLegacy} {
			t.Run(era, func(t *testing.T) {
				got := mcpToolOK(t, env, env.OperatorSecret, opHeaders[era], "vecnet_agent_add", same)
				if got["status"] != "needs_human" || got["request_id"] != id {
					t.Fatal("unchanged historical grokbot retry conflicted")
				}
				conflict, wire := mcpToolError(t, env, env.OperatorSecret, opHeaders[era], "vecnet_agent_add", changed, "idempotency_conflict")
				assertNoSecrets(t, wire, routineKey, routineURL)
				if conflict.Error.Details["request_id"] != id {
					t.Fatal("conflict did not name the request")
				}
			})
		}
	})

	t.Run("(t7 r2 M1)", func(t *testing.T) {
		key := fakeRoutineKey(t)
		msg := discoveryMismatch()
		if strings.Contains(msg, key) || strings.Contains(msg, "crsr_") || strings.Contains(msg, "securitySchemes") || strings.Contains(msg, "annotations") {
			t.Fatal("discovery diagnostic quoted returned metadata")
		}
		raw := mcpCallRaw(t, env, env.OperatorSecret, modern(), "tools/list", nil)
		if wireHasSecret(raw, env.OperatorSecret, key) {
			t.Fatal("discovery response contains a fixture credential")
		}
		leaked := []byte(`{"jsonrpc":"2.0","id":1,"result":{"tools":[{"securitySchemes":["` + key + `"],"annotations":{"readOnlyHint":"` + key + `"}}]}}`)
		if !wireHasSecret(leaked, key) {
			t.Fatal("discovery metadata credential was not scanned")
		}
	})

	t.Run("(t7 r2 M2)", func(t *testing.T) {
		key := fakeRoutineKey(t)
		header := "Bearer " + key
		clean := []byte(`{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"{}"}],"structuredContent":{"status":"needs_human"}}}`)
		if wireHasSecret(clean, key, header) {
			t.Fatal("clean wire was reported secret")
		}
		unknown := []byte(`{"jsonrpc":"2.0","id":1,"result":{"status":"needs_human","extra":"` + key + `"}}`)
		if !wireHasSecret(unknown, key) {
			t.Fatal("unknown field credential was not scanned")
		}
		errText := []byte(`{"jsonrpc":"2.0","id":1,"error":{"code":-32602,"message":"invalid ` + header + `"}}`)
		if !wireHasSecret(errText, key, header) {
			t.Fatal("error text credential was not scanned")
		}
	})

	t.Run("(t7 r3 M1)", func(t *testing.T) {
		nonhex := "crsr_Zz-not-hex!"
		prop := "url_" + nonhex
		if strings.Contains(schemaExposes(), nonhex) || strings.Contains(schemaExposes(), prop) {
			t.Fatal("schema diagnostic quoted a property name")
		}
		_, peerBearer := env.NewPeer("session", "wire peer")
		raw := mcpCallRaw(t, env, env.OperatorSecret, modern(), "tools/list", nil)
		rejectToolWire(t, raw, env.OperatorSecret, peerBearer)
		captured := []byte(`{"jsonrpc":"2.0","id":1,"result":{"tools":[{"inputSchema":{"type":"object","properties":{"` + prop + `":{"type":"string"}}}}]}}`)
		if !wireHasSecret(captured, env.OperatorSecret, peerBearer, nonhex) {
			t.Fatal("schema property name credential was not scanned")
		}
	})

	t.Run("(t7 r3 M2)", func(t *testing.T) {
		nonhex := "crsr_Zz-not-hex!"
		_, peerBearer := env.NewPeer("session", "wire peer two")
		help := []byte(`{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"Routine key: a raw crsr_ value, or the same value with one leading Bearer prefix."}],"structuredContent":{"help":"Routine key: a raw crsr_ value, or the same value with one leading Bearer prefix."}}}`)
		rejectToolWire(t, help, env.OperatorSecret, peerBearer)
		success := []byte(`{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"{}"}],"structuredContent":{"status":"needs_human"},"extra":"` + nonhex + `"}}`)
		if !wireHasSecret(success, env.OperatorSecret, peerBearer, nonhex) {
			t.Fatal("nonhex routine key in a success field was not scanned")
		}
		opField := []byte(`{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"{}"}],"structuredContent":{"status":"needs_human","leaked":"` + env.OperatorSecret + `"}}}`)
		if !wireHasSecret(opField, env.OperatorSecret, peerBearer, nonhex) {
			t.Fatal("operator bearer in a success field was not scanned")
		}
		peerField := []byte(`{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"{}"}],"structuredContent":{"status":"needs_human","leaked":"` + peerBearer + `"}}}`)
		if !wireHasSecret(peerField, env.OperatorSecret, peerBearer, nonhex) {
			t.Fatal("peer bearer in a success field was not scanned")
		}
		errText := []byte(`{"jsonrpc":"2.0","id":1,"error":{"code":-32602,"message":"invalid ` + nonhex + `","data":{"path":""}}}`)
		if !wireHasSecret(errText, env.OperatorSecret, peerBearer, nonhex) {
			t.Fatal("nonhex routine key in error text was not scanned")
		}
		// The success helper forwards the caller bearer and the extra fixtures.
		rejectToolWire(t, []byte(`{"jsonrpc":"2.0","id":1,"result":{"ok":true}}`), env.OperatorSecret, peerBearer, nonhex)
		escapedKey := "crsr_Zz<not-hex!"
		escaped := []byte(`{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"{}"}],"structuredContent":{"status":"needs_human"},"extra":"crsr_Zz\u003cnot-hex!"}}`)
		if !wireHasSecret(escaped, env.OperatorSecret, peerBearer, escapedKey) {
			t.Fatal("escaped nonhex fixture was not scanned")
		}
		escapedName := []byte(`{"jsonrpc":"2.0","id":1,"result":{"crsr_Zz\u003cnot-hex!":"x"}}`)
		if !wireHasSecret(escapedName, escapedKey) {
			t.Fatal("escaped property name was not scanned")
		}
		rejectToolWire(t, help, env.OperatorSecret, peerBearer, escapedKey)
	})
}

func unknownKeyPath(t *testing.T, raw []byte) string {
	t.Helper()
	r := decodeOnboardRPC(t, raw)
	if r.Error == nil || r.Error.Data == nil {
		t.Fatal("unknown property was not an RPC error")
	}
	var data struct {
		Path *string `json:"path"`
	}
	if json.Unmarshal(r.Error.Data, &data) != nil || data.Path == nil {
		t.Fatal("unknown property has no path")
	}
	return *data.Path
}

func ageReceipt(t *testing.T, env *testhub.Env, requestID string, drop ...string) {
	t.Helper()
	var prn string
	if err := env.Hub.DB.QueryRow(`SELECT prn_id FROM peers WHERE peer_id=?`, env.OperatorPeer).Scan(&prn); err != nil || prn == "" {
		t.Fatal("operator principal is missing")
	}
	key := "onboarding.v1/" + prn + "/" + requestID
	var raw string
	if err := env.Hub.DB.QueryRow(`SELECT value FROM meta WHERE key=?`, key).Scan(&raw); err != nil {
		t.Fatal("historical receipt is missing")
	}
	var rec map[string]any
	if json.Unmarshal([]byte(raw), &rec) != nil {
		t.Fatal("historical receipt is not JSON")
	}
	for _, field := range drop {
		delete(rec, field)
	}
	body, err := json.Marshal(rec)
	if err != nil {
		t.Fatal("historical receipt could not be rewritten")
	}
	if _, err := env.Hub.DB.Exec(`UPDATE meta SET value=? WHERE key=?`, string(body), key); err != nil {
		t.Fatal("historical receipt could not be rewritten")
	}
}

func mustHumanSubmit(t *testing.T, env *testhub.Env, body map[string]any) onboarding.SubmitResult {
	t.Helper()
	resp, raw := env.Do(env.OperatorSecret, http.MethodPost, "/v1/admin/agents", body)
	var created onboarding.SubmitResult
	if resp.StatusCode != http.StatusCreated || json.Unmarshal(raw, &created) != nil || created.PeerID == "" {
		t.Fatal("human submit did not create the peer")
	}
	return created
}

func addAgentQuery(t *testing.T, webPath string) url.Values {
	t.Helper()
	const prefix = "/ui/#add-agent?"
	if !strings.HasPrefix(webPath, prefix) {
		t.Fatal("web_path is not an Add agent fragment")
	}
	q, err := url.ParseQuery(strings.TrimPrefix(webPath, prefix))
	if err != nil {
		t.Fatal("web_path query is not valid")
	}
	return q
}

func queryHas(q url.Values, key string) bool {
	_, ok := q[key]
	return ok
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

func mcpTools(t *testing.T, env *testhub.Env, secret string, headers map[string]string, secrets ...string) []map[string]any {
	t.Helper()
	raw := mcpCallRaw(t, env, secret, headers, "tools/list", nil)
	if wireHasSecret(raw, secrets...) {
		t.Fatal("response contains a secret or human output")
	}
	r := decodeOnboardRPC(t, raw)
	if r.Error != nil {
		t.Fatal("tools/list returned an RPC error")
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

func decodeOnboardRPC(t *testing.T, raw []byte) rpcResp {
	t.Helper()
	var r rpcResp
	if json.Unmarshal(raw, &r) != nil || r.JSONRPC != "2.0" {
		t.Fatal("response is not a JSON-RPC object")
	}
	return r
}

func mcpToolResult(t *testing.T, env *testhub.Env, secret string, headers map[string]string, name string, args any, extra ...string) (toolResult, []byte) {
	t.Helper()
	raw := mcpCallRaw(t, env, secret, headers, "tools/call", map[string]any{"name": name, "arguments": args})
	rejectToolWire(t, raw, append([]string{secret}, extra...)...)
	r := decodeOnboardRPC(t, raw)
	if r.Error != nil {
		t.Fatalf("%s: unexpected RPC error", name)
	}
	var res toolResult
	if json.Unmarshal(r.Result, &res) != nil {
		t.Fatalf("%s: result is not a tool result", name)
	}
	return res, raw
}

func mcpToolOK(t *testing.T, env *testhub.Env, secret string, headers map[string]string, name string, args any, extra ...string) map[string]any {
	t.Helper()
	res, raw := mcpToolResult(t, env, secret, headers, name, args, extra...)
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

func mcpToolError(t *testing.T, env *testhub.Env, secret string, headers map[string]string, name string, args any, code string, extra ...string) (toolErrorBody, []byte) {
	t.Helper()
	raw := mcpCallRaw(t, env, secret, headers, "tools/call", map[string]any{"name": name, "arguments": args})
	rejectToolWire(t, raw, append([]string{secret}, extra...)...)
	assertNoSecrets(t, raw, extra...)
	r := decodeOnboardRPC(t, raw)
	if r.Error != nil {
		t.Fatal("tool error path returned an RPC error")
	}
	var res toolResult
	if json.Unmarshal(r.Result, &res) != nil || !res.IsError {
		t.Fatalf("%s: error = false, want %s", name, code)
	}
	if len(res.Content) != 1 || !strings.HasPrefix(res.Content[0].Text, code+": ") {
		t.Fatal("tool error text does not start with the code")
	}
	var e toolErrorBody
	if json.Unmarshal(res.StructuredContent, &e) != nil || e.Error.Code != code || e.Error.Details == nil {
		t.Fatal("tool error code mismatch")
	}
	return e, raw
}

func mcpInvalidPath(t *testing.T, env *testhub.Env, secret string, headers map[string]string, name string, args any, extra ...string) string {
	t.Helper()
	raw := mcpCallRaw(t, env, secret, headers, "tools/call", map[string]any{"name": name, "arguments": args})
	rejectToolWire(t, raw, append([]string{secret}, extra...)...)
	assertNoSecrets(t, raw, extra...)
	r := decodeOnboardRPC(t, raw)
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

func discoveryMismatch() string { return "discovery metadata mismatch" }

func schemaExposes() string { return "schema exposes a banned property" }

// rejectToolWire is the scan mcpToolResult runs before it decodes a success
// response. secret is the caller bearer; extra carries the other fixture
// credentials, including nonhex routine keys.
func rejectToolWire(t *testing.T, raw []byte, secrets ...string) {
	t.Helper()
	if wireHasSecret(raw, secrets...) {
		t.Fatal("response contains a secret or human output")
	}
}

// liveRoutineKeyRE matches a generated routine key. The public kind help
// mentions the crsr_ prefix in prose and must not trip this check.
var liveRoutineKeyRE = regexp.MustCompile(`crsr_[0-9a-fA-F]{16,}`)

// wireHasSecret reports whether raw, the complete response, contains a
// fixture credential, a live routine key, or human output. Callers use it
// before they decode or format the body.
func wireHasSecret(raw []byte, secrets ...string) bool {
	if textHasSecret(string(raw), secrets) {
		return true
	}
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return false
	}
	return valueHasSecret(v, secrets)
}

func textHasSecret(text string, secrets []string) bool {
	for _, banned := range []string{"human_output", "vn_enrol_", "api2.cursor.sh"} {
		if strings.Contains(text, banned) {
			return true
		}
	}
	if liveRoutineKeyRE.MatchString(text) {
		return true
	}
	for _, secret := range secrets {
		if secret != "" && strings.Contains(text, secret) {
			return true
		}
	}
	return false
}

// valueHasSecret walks decoded JSON, including unknown fields and property
// names. json.Unmarshal restores escapes such as \u003c before the typed decode.
func valueHasSecret(v any, secrets []string) bool {
	switch x := v.(type) {
	case string:
		return textHasSecret(x, secrets)
	case map[string]any:
		for key, child := range x {
			if textHasSecret(key, secrets) || valueHasSecret(child, secrets) {
				return true
			}
		}
	case []any:
		for _, child := range x {
			if valueHasSecret(child, secrets) {
				return true
			}
		}
	}
	return false
}

func withExtraJSONField(t *testing.T, raw []byte, key, val string) []byte {
	t.Helper()
	var v any
	if json.Unmarshal(raw, &v) != nil {
		t.Fatal("wire is not JSON")
	}
	obj, ok := v.(map[string]any)
	if !ok {
		t.Fatal("wire is not an object")
	}
	obj[key] = val
	out, err := json.Marshal(obj)
	if err != nil {
		t.Fatal("wire mutation could not be encoded")
	}
	return out
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
