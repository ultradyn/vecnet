package mcpserver_test

// SOURCE DRAFT / UNRUN. Semantic wire maps deliberately avoid guessing the
// unspecified exported names of nested opapi DTOs.
import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/auth"
	venv "github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/hub"
	"github.com/XertroV/vecnet/internal/mcpserver"
	"github.com/XertroV/vecnet/internal/testhub"
)

func t2MCPID(prefix string, n int) string { return fmt.Sprintf("%s_%026d", prefix, n) }
func t2MCPExec(t *testing.T, e *testhub.Env, q string, args ...any) {
	t.Helper()
	if _, err := e.Hub.DB.Exec(q, args...); err != nil {
		t.Fatal(err)
	}
}
func t2MCPJSON(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil {
		t.Fatal("expected semantic JSON object")
	}
	return m
}
func t2MCPEncode(t *testing.T, v any) []byte {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
func t2MCPNoCanary(t *testing.T, raw []byte, canaries ...string) {
	t.Helper()
	for _, c := range canaries {
		if c != "" && bytes.Contains(raw, []byte(c)) {
			t.Fatal("MCP/storage/logging leaked synthetic canary")
		}
	}
}
func t2MCPEqual(t *testing.T, a, b any) {
	t.Helper()
	aa := t2MCPJSON(t, t2MCPEncode(t, map[string]any{"value": a}))
	bb := t2MCPJSON(t, t2MCPEncode(t, map[string]any{"value": b}))
	if !reflect.DeepEqual(aa, bb) {
		t.Fatal("semantic JSON parity mismatch")
	}
}
func t2MCPFixture(t *testing.T) (*testhub.Env, string, string, string, string) {
	t.Helper()
	e := testhub.Start(t)
	e.Now.Advance(time.Date(2030, 1, 10, 12, 0, 0, 0, time.UTC).Sub(e.Now.Now()))
	peer, secret := e.NewPeer("session", "route-stats-mcp")
	r, g, s := t2MCPID("route", 61), t2MCPID("route", 62), t2MCPID("sub", 61)
	at := venv.FormatTime(e.Now.Now())
	for _, route := range []struct{ id, kind, rawURL string }{
		{r, "http", "https://http-stats.example:8443/hook"},
		{g, "grokbot", "https://routine-stats.example:9443/routine"},
	} {
		t2MCPExec(t, e, "INSERT INTO routes(route_id,owner_kind,owner_id,kind,url,created_at) VALUES(?,'peer',?,?,?,?)", route.id, peer, route.kind, route.rawURL, at)
	}
	t2MCPExec(t, e, "INSERT INTO event_subscriptions(sub_id,principal_kind,principal_id,peer_id,name,arguments,url,secret_enc,cursor_epoch,cursor_seq,types,created_at,updated_at,paused_reason,expires_at) VALUES(?,'vecnet',?,?,'synthetic','{}','https://events-stats.example:8443/callback','synthetic-sealed',1,0,'[]',?,?,'exhausted',?)", s, peer, peer, at, at, venv.FormatTime(e.Now.Now().Add(-time.Hour)))
	for _, entry := range []struct {
		id, delivery, outcome string
		status, class         any
		body, latency         int
	}{
		{r, "dly-retry", "fail", 503, "http_5xx", 7, 3},
		{r, "dly-retry", "ok", 202, nil, 0, 0},
		{g, "dly-routine", "refused", 200, "routine_not_started", 11, 5},
		{s, "e1.92", "fail", nil, "timeout", 5, 9},
	} {
		t2MCPExec(t, e, "INSERT INTO route_attempts(route_id,delivery_id,at,outcome,http_status,error_class,body_bytes,latency_ms) VALUES(?,?,?,?,?,?,?,?)", entry.id, entry.delivery, at, entry.outcome, entry.status, entry.class, entry.body, entry.latency)
	}
	return e, peer, secret, r, s
}

func t2MCPResult(t *testing.T, e *testhub.Env, secret string, headers map[string]string, args any, rpcCode int, toolCode string) map[string]any {
	t.Helper()
	raw := mcpCallRaw(t, e, secret, headers, "tools/call", map[string]any{"name": "vecnet_route_stats", "arguments": args})
	rpc := decodeRPC(t, raw)
	if rpcCode != 0 {
		if rpc.Error == nil || rpc.Error.Code != rpcCode {
			t.Fatalf("expected JSON-RPC code %d", rpcCode)
		}
		if len(rpc.Result) > 0 && string(rpc.Result) != "null" {
			t.Fatal("RPC failure returned partial result")
		}
		if rpcCode == -32602 {
			data := t2MCPJSON(t, t2MCPEncode(t, rpc.Error.Data))
			path, ok := data["path"].(string)
			if !ok || (path != "" && !strings.HasPrefix(path, "/")) {
				t.Fatal("schema failure must retain sanitized path")
			}
		}
		return t2MCPJSON(t, raw)
	}
	if rpc.Error != nil {
		t.Fatal("unexpected JSON-RPC error")
	}
	m := t2MCPJSON(t, rpc.Result)
	var result toolResult
	if json.Unmarshal(rpc.Result, &result) != nil {
		t.Fatal("invalid tool result")
	}
	if len(result.Content) != 1 || result.Content[0].Type != "text" {
		t.Fatal("expected one standard text item")
	}
	if toolCode != "" {
		if !result.IsError {
			t.Fatal("expected tool-level error")
		}
		body := t2MCPJSON(t, result.StructuredContent)
		errorBody, ok := body["error"].(map[string]any)
		if !ok || errorBody["code"] != toolCode {
			t.Fatalf("expected tool failure %s", toolCode)
		}
		if _, ok := errorBody["details"].(map[string]any); !ok {
			t.Fatal("tool error details must be object")
		}
		if !strings.HasPrefix(result.Content[0].Text, toolCode+": ") {
			t.Fatal("tool failure text envelope changed")
		}
		return m
	}
	if result.IsError {
		t.Fatal("valid operator read failed")
	}
	t2MCPEqual(t, t2MCPJSON(t, []byte(result.Content[0].Text)), t2MCPJSON(t, result.StructuredContent))
	return m
}

func TestRouteStatsMCPParityAndErrors(t *testing.T) {
	e, peer, peerSecret, route, sub := t2MCPFixture(t)
	for _, era := range []string{mcpserver.ProtoCurrent, mcpserver.ProtoLegacy} {
		t.Run(era, func(t *testing.T) {
			headers := mcpEraHeaders(t, e, e.OperatorSecret, era)
			for _, id := range []string{peer, route, sub} {
				resp, httpRaw := e.Do(e.OperatorSecret, http.MethodGet, "/v1/admin/route-stats?id="+url.QueryEscape(id), nil)
				if resp.StatusCode != 200 {
					t.Fatal("HTTP parity fixture failed")
				}
				result := t2MCPResult(t, e, e.OperatorSecret, headers, map[string]any{"id": id}, 0, "")
				t2MCPEqual(t, t2MCPJSON(t, httpRaw), result["structuredContent"])
				if id == peer {
					// Literal Grok oracle is independent of shared-reader parity.
					grokID := t2MCPID("route", 62)
					at := venv.FormatTime(e.Now.Now())
					rows := t2MCPJSON(t, httpRaw)["routes"].([]any)
					found := 0
					for _, value := range rows {
						row := value.(map[string]any)
						if row["route_id"] != grokID {
							continue
						}
						found++
						t2MCPEqual(t, row, map[string]any{
							"route_id": grokID, "kind": "grokbot", "host": "routine-stats.example",
							"owner_kind": "peer", "owner_id": peer, "attempt_scope": "route",
							"requests": 1, "ok": 0, "fail": 0, "refused": 1, "distinct_ids": 1,
							"last_used_at": at, "last_success_at": nil, "last_error": "routine_not_started",
							"last_error_at": at, "last_status": 200,
							"body_bytes": map[string]any{"avg": 11, "p95": 11, "p99": 11, "samples": 1},
							"latency_ms": map[string]any{"avg": 5, "p95": 5, "p99": 5, "samples": 1},
						})
					}
					if found != 1 {
						t.Fatal("expected exactly one Grok route")
					}
				}
				if era == mcpserver.ProtoCurrent {
					if result["resultType"] != "complete" {
						t.Fatal("current resultType missing")
					}
					if _, ok := result["_meta"].(map[string]any); !ok {
						t.Fatal("current server metadata missing")
					}
				} else if _, exists := result["resultType"]; exists {
					t.Fatal("legacy envelope gained resultType")
				}
				// Existing generic object _meta is accepted and ignored as a
				// source of selector/authority. It cannot replace id.
				withMeta := t2MCPResult(t, e, e.OperatorSecret, headers, map[string]any{"id": id, "_meta": map[string]any{"synthetic": "canary-meta"}}, 0, "")
				t2MCPEqual(t, result["structuredContent"], withMeta["structuredContent"])
				t2MCPNoCanary(t, t2MCPEncode(t, withMeta), "canary-meta")
				for _, meta := range []any{nil, "ignored-meta-canary", []string{"ignored-meta-canary"}} {
					ignored := t2MCPResult(t, e, e.OperatorSecret, headers, map[string]any{"id": id, "_meta": meta}, 0, "")
					t2MCPEqual(t, result["structuredContent"], ignored["structuredContent"])
					t2MCPNoCanary(t, t2MCPEncode(t, ignored), "ignored-meta-canary")
				}
			}
			// Closed call schema keeps required/string/null/length bounds,
			// unlike the in-range strict ULID errors delegated to the hub.
			for _, args := range []any{
				map[string]any{}, map[string]any{"id": nil}, map[string]any{"id": 17},
				map[string]any{"id": []string{peer}}, map[string]any{"id": ""},
				map[string]any{"id": strings.Repeat("length-canary", 7)},
				map[string]any{"id": strings.Repeat("x", 81)},
				map[string]any{"id": peer, "unexpected-key-canary": "unexpected-value-canary"},
				map[string]any{"id": peer, "as": peer}, map[string]any{"id": peer, "limit": 1000},
				map[string]any{"_meta": map[string]any{"id": peer}},
			} {
				m := t2MCPResult(t, e, e.OperatorSecret, headers, args, -32602, "")
				t2MCPNoCanary(t, t2MCPEncode(t, m), "length-canary", "unexpected-key-canary", "unexpected-value-canary", "invalid-meta-canary")
			}
			var invalidMessage string
			for _, id := range []string{
				"selector-canary", "self", "default", " " + peer, peer + " ", peer + "," + route,
				"x", strings.Repeat("x", 80),
				"prn_00000000000000000000000001",
				"peer_80000000000000000000000000", "route_0000000000000000000000000I",
				"sub_0000000000000000000000000a", "sub_0000000000000000000000000",
			} {
				m := t2MCPResult(t, e, e.OperatorSecret, headers, map[string]any{"id": id}, 0, "invalid_value")
				// A one-byte selector can occur in required envelope keys.
				// Compare its fixed message/details instead of scanning that byte.
				if id != "x" {
					t2MCPNoCanary(t, t2MCPEncode(t, m), id)
				}
				errorBody := m["structuredContent"].(map[string]any)["error"].(map[string]any)
				if errorBody["details"].(map[string]any)["field"] != "id" {
					t.Fatal("hub syntax error lost fixed field")
				}
				message, ok := errorBody["message"].(string)
				if !ok || message == "" {
					t.Fatal("invalid selector needs a fixed message")
				}
				if invalidMessage == "" {
					invalidMessage = message
				}
				if message != invalidMessage {
					t.Fatal("invalid selector message varies with rejected input")
				}
				t2MCPEqual(t, errorBody["details"], map[string]any{"field": "id"})
				content := m["content"].([]any)[0].(map[string]any)
				if content["text"] != "invalid_value: "+invalidMessage {
					t.Fatal("invalid selector text differs from fixed error message")
				}
			}
			for _, prefix := range []string{"peer", "route", "sub"} {
				id := t2MCPID(prefix, 999)
				m := t2MCPResult(t, e, e.OperatorSecret, headers, map[string]any{"id": id}, 0, "not_found")
				t2MCPNoCanary(t, t2MCPEncode(t, m), id)
			}
			peerHeaders := mcpEraHeaders(t, e, peerSecret, era)
			t2MCPResult(t, e, peerSecret, peerHeaders, map[string]any{"id": peer}, 0, "forbidden")
			for _, spoof := range []map[string]any{
				{"id": peer, "_meta": map[string]any{"Kind": "operator", "as": e.OperatorPeer}},
			} {
				t2MCPResult(t, e, peerSecret, peerHeaders, spoof, 0, "forbidden")
			}
			t2MCPResult(t, e, peerSecret, peerHeaders, map[string]any{"id": "selector-canary"}, 0, "forbidden")
			// Rename only synthetic storage after the legacy session exists.
			t2MCPExec(t, e, "ALTER TABLE route_attempts RENAME TO route_attempts_fixture_hidden")
			m := t2MCPResult(t, e, e.OperatorSecret, headers, map[string]any{"id": route}, -32603, "")
			t2MCPNoCanary(t, t2MCPEncode(t, m), "route_attempts_fixture_hidden", "SELECT", "no such table")
			if e.Hub.DB.Stats().InUse != 0 {
				t.Fatal("MCP failure leaked connection")
			}
			t2MCPExec(t, e, "ALTER TABLE route_attempts_fixture_hidden RENAME TO route_attempts")
			t2MCPResult(t, e, e.OperatorSecret, headers, map[string]any{"id": route}, 0, "")
		})
	}
}

// Existing achieved-size pins copied literally from tools_budget_test.go.
// This draft adds a contractual ceiling, not an invented measured baseline.
// T2 implementation must separately add its actually measured achieved pin
// and coverage entry to that existing guard under its own authorization.
func t2MCPCatalogBudget(t *testing.T, entries []map[string]any, operator bool) {
	t.Helper()
	pins := map[string]int{
		"vecnet_send": 2826, "vecnet_reply": 1696, "vecnet_react": 844,
		"vecnet_inbox": 706, "vecnet_subscriptions": 432, "vecnet_ack": 624,
		"vecnet_receipt": 610, "vecnet_presence": 1005, "vecnet_presence_subscribe": 514,
		"vecnet_threads": 791, "vecnet_thread": 670, "vecnet_peers": 370,
		"vecnet_peer_create": 747, "vecnet_peer_set_disabled": 555, "vecnet_groups": 389,
		"vecnet_group_create": 627, "vecnet_group_add": 613, "vecnet_whoami": 726,
		"vecnet_blob_put": 873, "vecnet_blob_get": 1340, "vecnet_set_avatar": 670,
		"vecnet_clear_avatar": 459, "vecnet_guide": 1534,
		"vecnet_peer_lifecycle": 634, "vecnet_peer_invite_get": 468, "vecnet_peer_invite_revoke": 495,
	}
	oldTotal := 21276
	r2Total := 25348
	if operator {
		pins["vecnet_agent_kinds"] = 471
		pins["vecnet_agent_add"] = 1977
		pins["vecnet_agent_status"] = 2615
		oldTotal += 5066
		r2Total = 32014
	} else {
		delete(pins, "vecnet_peer_lifecycle")
		delete(pins, "vecnet_peer_invite_get")
		delete(pins, "vecnet_peer_invite_revoke")
		oldTotal -= 634 + 468 + 495 + 3
	}
	r2Pins := map[string]int{"vecnet_blob_put": 4676, "vecnet_blob_get": 2753, "vecnet_blob_status": 1558}
	expected := len(pins) + 1 // Existing R2 blob_status addition.
	if operator {
		expected++
	} // Exactly one new operator-only entry.
	if len(entries) != expected {
		t.Fatalf("catalog budget coverage=%d want=%d", len(entries), expected)
	}
	compact := t2MCPEncode(t, map[string]any{"tools": entries})
	t.Logf("route stats draft catalog, operator=%v: %d compact bytes", operator, len(compact))
	if len(compact) > r2Total*110/100 {
		t.Fatal("existing full catalog ceiling exceeded")
	}
	legacyTotal := len(compact)
	for _, entry := range entries {
		name := entry["name"].(string)
		n := len(t2MCPEncode(t, entry))
		t.Logf("%s: %d compact bytes (unmeasured until this source is run)", name, n)
		if name == "vecnet_route_stats" {
			if !operator || n > 1024 {
				t.Fatal("route stats violates operator-only 1024-byte ceiling")
			}
			// Keep this entry in BOTH existing catalog totals; only the
			// already approved R2 normalization below may adjust legacyTotal.
			continue
		}
		baseline, ok := pins[name]
		if r2, exists := r2Pins[name]; exists {
			legacyTotal += baseline - n
			if !ok {
				legacyTotal--
			}
			baseline, ok = r2, true
		}
		if !ok {
			t.Fatalf("uncovered existing tool %s", name)
		}
		if n > baseline*115/100 {
			t.Fatalf("existing %s size pin exceeded", name)
		}
	}
	if legacyTotal > oldTotal*110/100 {
		t.Fatal("existing catalog legacy ceiling exceeded")
	}
}

func TestRouteStatsMCPDiscoveryAndBudget(t *testing.T) {
	e, peer, peerSecret, _, _ := t2MCPFixture(t)
	_, principalSecret := e.NewPrincipal("route-stats-discovery-principal")
	grok, grokSecret := e.NewPeer("grokbot", "route-stats-discovery-grok")
	dot, _ := e.NewPeer("dot", "route-stats-discovery-dot")
	owner, err := e.Hub.PeerPrincipal(context.Background(), dot)
	if err != nil {
		t.Fatal(err)
	}
	grokOwner, err := e.Hub.PeerPrincipal(context.Background(), grok)
	if err != nil {
		t.Fatal(err)
	}
	oauthDot := eventsFront(t, e, guideAuth{mcpserver.Principal{
		Caller: auth.Caller{Kind: "principal", PrincipalID: owner, PeerID: dot, PeerKind: "dot"},
		OAuth:  &mcpserver.OAuthInfo{PrincipalID: owner, Scopes: []string{mcpserver.ScopeVecnet}, Expires: e.Now.Now().Add(time.Hour)},
	}}, nil)
	oauthGrok := eventsFront(t, e, guideAuth{mcpserver.Principal{
		Caller: auth.Caller{Kind: "principal", PrincipalID: grokOwner, PeerID: grok, PeerKind: "grokbot"},
		OAuth:  &mcpserver.OAuthInfo{PrincipalID: grokOwner, Scopes: []string{mcpserver.ScopeVecnet}, Expires: e.Now.Now().Add(time.Hour)},
	}}, nil)
	for _, era := range []string{mcpserver.ProtoCurrent, mcpserver.ProtoLegacy} {
		t.Run(era, func(t *testing.T) {
			opHeaders := mcpEraHeaders(t, e, e.OperatorSecret, era)
			entries := mcpTools(t, e, e.OperatorSecret, opHeaders)
			descriptor := toolByName(t, entries, "vecnet_route_stats")
			t2MCPCatalogBudget(t, entries, true)
			if _, exists := descriptor["outputSchema"]; exists {
				t.Fatal("route stats discovery must omit outputSchema")
			}
			annotations, ok := descriptor["annotations"].(map[string]any)
			if !ok || annotations["readOnlyHint"] != true || annotations["destructiveHint"] != false || annotations["openWorldHint"] != false {
				t.Fatal("route stats must use existing read annotations")
			}
			security := []any{map[string]any{"type": "oauth2", "scopes": []any{mcpserver.ScopeVecnet}}}
			t2MCPEqual(t, descriptor["securitySchemes"], security)
			input := descriptor["inputSchema"].(map[string]any)
			t2MCPEqual(t, input["required"], []string{"id"})
			if input["type"] != "object" || input["additionalProperties"] != false {
				t.Fatal("input must be closed object")
			}
			properties := input["properties"].(map[string]any)
			id := properties["id"].(map[string]any)
			if len(properties) != 1 || id["type"] != "string" {
				t.Fatal("discovery id shape changed")
			}
			// Projection currently drops scalar bounds; the authoritative
			// compiled validator is exercised at both bounds in the parity test.
			if strings.TrimSpace(descriptor["description"].(string)) == "" || strings.TrimSpace(id["description"].(string)) == "" {
				t.Fatal("route stats needs compact per-tool and id wording")
			}
			listRaw := mcpCallRaw(t, e, e.OperatorSecret, opHeaders, "tools/list", nil)
			list := t2MCPJSON(t, decodeRPC(t, listRaw).Result)
			if era == mcpserver.ProtoCurrent {
				if list["resultType"] != "complete" || list["cacheScope"] != "private" || list["ttlMs"] != float64(0) {
					t.Fatal("current discovery metadata changed")
				}
			}
			for _, tc := range []struct {
				name, secret string
				front        *testhub.Env
				grok         bool
			}{
				{"peer", peerSecret, e, false}, {"principal", principalSecret, e, false},
				{"grok", grokSecret, e, true}, {"oauth-dot", "synthetic-oauth", oauthDot, false},
				{"oauth-grok", "synthetic-oauth", oauthGrok, true},
			} {
				headers := mcpEraHeaders(t, tc.front, tc.secret, era)
				listed := mcpTools(t, tc.front, tc.secret, headers)
				if toolNameSet(t, listed)["vecnet_route_stats"] {
					t.Fatalf("%s saw operator tool", tc.name)
				}
				if tc.grok {
					if !reflect.DeepEqual(sortedToolNames(t, listed), grokToolNames) {
						t.Fatal("exact twelve Grok tools changed")
					}
					if len(t2MCPEncode(t, map[string]any{"tools": listed})) >= 13*1024 {
						t.Fatal("existing Grok bound exceeded")
					}
				} else if tc.name == "peer" || tc.name == "principal" {
					t2MCPCatalogBudget(t, listed, false)
				}
				t2MCPResult(t, tc.front, tc.secret, headers, map[string]any{"id": peer}, 0, "forbidden")
			}
			// Reclassifying the peer does not change its credential Kind.
			t2MCPExec(t, e, "UPDATE peers SET kind='operator' WHERE peer_id=?", peer)
			peerHeaders := mcpEraHeaders(t, e, peerSecret, era)
			if toolNameSet(t, mcpTools(t, e, peerSecret, peerHeaders))["vecnet_route_stats"] {
				t.Fatal("PeerKind elevated discovery")
			}
			t2MCPResult(t, e, peerSecret, peerHeaders, map[string]any{"id": peer}, 0, "forbidden")
			for _, spoof := range []map[string]any{
				{"id": peer, "_meta": map[string]any{"Kind": "operator", "as": e.OperatorPeer}},
			} {
				t2MCPResult(t, e, peerSecret, peerHeaders, spoof, 0, "forbidden")
			}
			t2MCPExec(t, e, "UPDATE peers SET kind='session' WHERE peer_id=?", peer)
			t2MCPExec(t, e, "UPDATE peers SET disabled=1 WHERE peer_id=?", e.OperatorPeer)
			disabledHeaders := mcpEraHeaders(t, e, e.OperatorSecret, era)
			toolByName(t, mcpTools(t, e, e.OperatorSecret, disabledHeaders), "vecnet_route_stats")
			t2MCPResult(t, e, e.OperatorSecret, disabledHeaders, map[string]any{"id": peer}, 0, "")
			t2MCPExec(t, e, "UPDATE peers SET disabled=0 WHERE peer_id=?", e.OperatorPeer)
		})
	}
}

func TestRouteStatsPrivacyCanaries(t *testing.T) {
	e, peer, _, route, sub := t2MCPFixture(t)
	// Capture telemetry/read diagnostics. Tests are intentionally serial because
	// slog's default logger is process-global.
	oldLogger := slog.Default()
	var logs t2MCPLog
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(oldLogger) })
	user, pass, path, query, fragment := "userinfo_canary", "password_canary", "path_canary", "query_canary", "fragment_canary"
	key, header, body, rawError, safeReason := "whsec_synthetic_key_canary", "header_canary", "body_canary", "raw_network_canary", "safe_lowercase_canary"
	privateURL := "https://" + user + ":" + pass + "@privacy.example:8743/" + path + "?token=" + query + "#" + fragment
	t2MCPExec(t, e, "UPDATE routes SET url=? WHERE route_id=?", privateURL, route)
	t2MCPExec(t, e, "INSERT INTO route_keys(route_id,whsec,created_at) VALUES(?,?,?)", route, key, venv.FormatTime(e.Now.Now()))
	t2MCPExec(t, e, "UPDATE event_subscriptions SET url=?,secret_enc=? WHERE sub_id=?", privateURL, key, sub)
	// Use the implemented immutable observation API. No injected JSON can set
	// last_error. A safe-looking arbitrary OutboundError.Reason is not an enum.
	e.Hub.NoteRouteAttempt(context.Background(), hub.RouteAttemptObservation{
		RouteID: route, DeliveryID: "privacy-delivery", Kind: "http", Attempted: true,
		NetError:  &hub.OutboundError{Reason: safeReason, Err: errors.New(rawError + " " + privateURL + " " + header + " " + body + " " + key)},
		BodyBytes: 17, Elapsed: 1500 * time.Microsecond, CompletedAt: e.Now.Now(),
	})
	var class string
	var status sql.NullInt64
	var storedBody, storedLatency int
	// Minimal scan destination alias below avoids any invented DTO type.
	if err := e.Hub.DB.QueryRow("SELECT error_class,http_status,body_bytes,latency_ms FROM route_attempts WHERE route_id=? AND delivery_id='privacy-delivery'", route).Scan(&class, &status, &storedBody, &storedLatency); err != nil {
		t.Fatal(err)
	}
	if class != "request_failed" || status.Valid || storedBody != 17 || storedLatency != 2 {
		t.Fatal("normalizer retained raw data or wrong measurement")
	}
	canaries := []string{user, pass, ":8743", path, query, fragment, key, header, body, rawError, safeReason, "privacy-delivery"}
	for _, id := range []string{peer, route, sub} {
		resp, httpRaw := e.Do(e.OperatorSecret, http.MethodGet, "/v1/admin/route-stats?id="+id, nil)
		if resp.StatusCode != 200 {
			t.Fatal("privacy HTTP fixture failed")
		}
		t2MCPNoCanary(t, httpRaw, canaries...)
		root := t2MCPJSON(t, httpRaw)
		rows := root["routes"].([]any)
		for _, v := range rows {
			row := v.(map[string]any)
			if row["route_id"] == route || row["route_id"] == sub {
				if row["host"] != "privacy.example" {
					t.Fatal("host must exclude all URL components except hostname")
				}
			}
			if row["route_id"] == route && row["last_error"] != "request_failed" {
				t.Fatal("safe enum missing")
			}
		}
		for _, era := range []string{mcpserver.ProtoCurrent, mcpserver.ProtoLegacy} {
			headers := mcpEraHeaders(t, e, e.OperatorSecret, era)
			result := t2MCPResult(t, e, e.OperatorSecret, headers, map[string]any{"id": id}, 0, "")
			t2MCPEqual(t, root, result["structuredContent"])
			t2MCPNoCanary(t, t2MCPEncode(t, result), canaries...)
		}
	}
	// Invalid stored URLs must not fall back to returning the raw authority.
	t2MCPExec(t, e, "UPDATE routes SET url='https://%zz/url_parse_canary' WHERE route_id=?", route)
	resp, raw := e.Do(e.OperatorSecret, http.MethodGet, "/v1/admin/route-stats?id="+route, nil)
	if resp.StatusCode != 200 {
		t.Fatal("invalid legacy URL should have empty host")
	}
	rows := t2MCPJSON(t, raw)["routes"].([]any)
	if len(rows) != 1 || rows[0].(map[string]any)["host"] != "" {
		t.Fatal("invalid URL host must be empty")
	}
	t2MCPNoCanary(t, raw, "url_parse_canary")
	// Telemetry failure diagnostics must remain fixed-code/stage-only.
	t2MCPExec(t, e, "ALTER TABLE route_attempts RENAME TO route_attempts_fixture_hidden")
	e.Hub.NoteRouteAttempt(context.Background(), hub.RouteAttemptObservation{
		RouteID: route, DeliveryID: "safe-delivery", Kind: "http", Attempted: true,
		NetError: errors.New(rawError + " " + body), CompletedAt: e.Now.Now(),
	})
	t2MCPExec(t, e, "ALTER TABLE route_attempts_fixture_hidden RENAME TO route_attempts")
	t2MCPNoCanary(t, logs.Bytes(), append(canaries, "route_attempts_fixture_hidden", "url_parse_canary")...)
	if !strings.Contains(logs.String(), "write_failed") {
		t.Fatal("fixture did not exercise telemetry failure diagnostic")
	}
	// Inspect all stored attempt values, not just the newest API field.
	rawRows, err := e.Hub.DB.Query("SELECT route_id,delivery_id,at,outcome,http_status,error_class,body_bytes,latency_ms FROM route_attempts ORDER BY id")
	if err != nil {
		t.Fatal(err)
	}
	defer rawRows.Close()
	for rawRows.Next() {
		values, ptrs := make([]any, 8), make([]any, 8)
		for i := range values {
			ptrs[i] = &values[i]
		}
		if rawRows.Scan(ptrs...) != nil {
			t.Fatal("cannot inspect stored attempt")
		}
		for i, v := range values {
			if b, ok := v.([]byte); ok {
				values[i] = string(b)
			}
		}
		t2MCPNoCanary(t, t2MCPEncode(t, values), user, pass, path, query, fragment, key, header, body, rawError, safeReason)
	}
	if rawRows.Err() != nil {
		t.Fatal(rawRows.Err())
	}
}

// slog may be called by request handling goroutines. Snapshot copies keep the
// privacy assertions independent of concurrent diagnostics.
type t2MCPLog struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *t2MCPLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}
func (l *t2MCPLog) Bytes() []byte {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]byte(nil), l.b.Bytes()...)
}
func (l *t2MCPLog) String() string { return string(l.Bytes()) }
