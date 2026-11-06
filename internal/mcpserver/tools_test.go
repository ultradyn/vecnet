package mcpserver_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/auth"
	venv "github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/httpapi"
	"github.com/XertroV/vecnet/internal/hub"
	"github.com/XertroV/vecnet/internal/mcpserver"
	"github.com/XertroV/vecnet/internal/oauth"
	"github.com/XertroV/vecnet/internal/testhub"
	"github.com/XertroV/vecnet/internal/webhook"
)

// rpc posts one 2026-era JSON-RPC request and returns the HTTP status and
// the decoded response.
func rpc(t *testing.T, env *testhub.Env, secret, method string, params any) (int, rpcResp) {
	t.Helper()
	body := map[string]any{"jsonrpc": "2.0", "id": 1, "method": method}
	if params != nil {
		body["params"] = params
	}
	resp, out := do(t, env, call{secret: secret, headers: modern(), body: body})
	return resp.StatusCode, decodeRPC(t, out)
}

// rpcVerbatim runs tools/call with args spliced into the request as written.
// A test needs it for what json.Marshal would change on the client side: it
// escapes < > & and U+2028/9, drops a repeated key, and renders 5.0 as 5.
func rpcVerbatim(t *testing.T, env *testhub.Env, secret, name, args string) (int, rpcResp) {
	t.Helper()
	body := fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":%q,"arguments":%s}}`, name, args)
	resp, out := do(t, env, call{secret: secret, headers: modern(), body: body})
	return resp.StatusCode, decodeRPC(t, out)
}

// verbatimResult runs rpcVerbatim for a call that must reach the tool.
func verbatimResult(t *testing.T, env *testhub.Env, secret, name, args string) toolResult {
	t.Helper()
	status, r := rpcVerbatim(t, env, secret, name, args)
	if status != http.StatusOK || r.Error != nil {
		t.Fatalf("%s: HTTP %d, JSON-RPC error %+v", name, status, r.Error)
	}
	var res toolResult
	if err := json.Unmarshal(r.Result, &res); err != nil {
		t.Fatalf("%s: decode result %s: %v", name, r.Result, err)
	}
	return res
}

// toolResult is a tools/call result as it appears on the wire.
type toolResult struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	StructuredContent json.RawMessage `json:"structuredContent"`
	IsError           bool            `json:"isError"`
}

// callTool runs tools/call and returns the tool result, failing on a
// JSON-RPC error or a non-200 status.
func callTool(t *testing.T, env *testhub.Env, secret, name string, args any) toolResult {
	t.Helper()
	status, r := rpc(t, env, secret, "tools/call", map[string]any{"name": name, "arguments": args})
	if status != http.StatusOK {
		t.Fatalf("%s: HTTP %d", name, status)
	}
	if r.Error != nil {
		t.Fatalf("%s: JSON-RPC error %+v (data %s)", name, r.Error, r.Error.Data)
	}
	var res toolResult
	if err := json.Unmarshal(r.Result, &res); err != nil {
		t.Fatalf("%s: decode result %s: %v", name, r.Result, err)
	}
	return res
}

// toolOK runs a tool that must succeed and decodes its structured content
// into out (when non-nil). The text content carries the same JSON.
func toolOK(t *testing.T, env *testhub.Env, secret, name string, args, out any) {
	t.Helper()
	res := callTool(t, env, secret, name, args)
	if res.IsError {
		t.Fatalf("%s: tool error %s", name, res.StructuredContent)
	}
	if len(res.Content) != 1 || res.Content[0].Type != "text" {
		t.Fatalf("%s: content = %+v, want one text item", name, res.Content)
	}
	var fromText, fromStruct any
	if err := json.Unmarshal([]byte(res.Content[0].Text), &fromText); err != nil {
		t.Fatalf("%s: text is not JSON: %q", name, res.Content[0].Text)
	}
	if err := json.Unmarshal(res.StructuredContent, &fromStruct); err != nil {
		t.Fatalf("%s: structuredContent %q: %v", name, res.StructuredContent, err)
	}
	if a, _ := json.Marshal(fromText); string(a) != mustJSON(t, fromStruct) {
		t.Fatalf("%s: text %s differs from structuredContent %s", name, a, res.StructuredContent)
	}
	if out != nil {
		if err := json.Unmarshal(res.StructuredContent, out); err != nil {
			t.Fatalf("%s: decode %s: %v", name, res.StructuredContent, err)
		}
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// toolErrorBody is the structured content of a tool-level error.
type toolErrorBody struct {
	Error struct {
		Code    string         `json:"code"`
		Message string         `json:"message"`
		Details map[string]any `json:"details"`
	} `json:"error"`
}

// toolFails runs a tool that must end in a tool-level error with code.
func toolFails(t *testing.T, env *testhub.Env, secret, name string, args any, code string) toolErrorBody {
	t.Helper()
	res := callTool(t, env, secret, name, args)
	if !res.IsError {
		t.Fatalf("%s: IsError false, want %s (%s)", name, code, res.StructuredContent)
	}
	var e toolErrorBody
	if err := json.Unmarshal(res.StructuredContent, &e); err != nil {
		t.Fatalf("%s: structuredContent %q: %v", name, res.StructuredContent, err)
	}
	if e.Error.Code != code {
		t.Fatalf("%s: error.code = %q, want %q (%s)", name, e.Error.Code, code, res.StructuredContent)
	}
	if e.Error.Details == nil {
		t.Fatalf("%s: error.details missing: %s", name, res.StructuredContent)
	}
	if len(res.Content) != 1 || res.Content[0].Type != "text" || !strings.HasPrefix(res.Content[0].Text, code+": ") {
		t.Fatalf("%s: content = %+v, want text \"%s: ...\"", name, res.Content, code)
	}
	return e
}

// invalidPath runs tools/call with bad arguments and returns data.path of
// the -32602 answer.
func invalidPath(t *testing.T, env *testhub.Env, secret, name string, args any) string {
	t.Helper()
	status, r := rpc(t, env, secret, "tools/call", map[string]any{"name": name, "arguments": args})
	if status != http.StatusOK {
		t.Fatalf("%s: HTTP %d", name, status)
	}
	if r.Error == nil {
		t.Fatalf("%s: no JSON-RPC error, result %s", name, r.Result)
	}
	if r.Error.Code != mcpserver.ErrInvalidParams {
		t.Fatalf("%s: code = %d, want %d", name, r.Error.Code, mcpserver.ErrInvalidParams)
	}
	var data struct {
		Path *string `json:"path"`
	}
	if err := json.Unmarshal(r.Error.Data, &data); err != nil || data.Path == nil {
		t.Fatalf("%s: data = %s, want a path (%v)", name, r.Error.Data, err)
	}
	return *data.Path
}

// propertyNames collects every name under a "properties" keyword anywhere in
// a JSON Schema document, $defs included.
func propertyNames(v any, into map[string]bool) {
	switch n := v.(type) {
	case map[string]any:
		if props, ok := n["properties"].(map[string]any); ok {
			for k := range props {
				into[k] = true
			}
		}
		for _, c := range n {
			propertyNames(c, into)
		}
	case []any:
		for _, c := range n {
			propertyNames(c, into)
		}
	}
}

// uriFormats collects every "format" keyword naming a URI-like format,
// anywhere in a JSON Schema document.
func uriFormats(v any, into map[string]bool) {
	switch n := v.(type) {
	case map[string]any:
		if f, ok := n["format"].(string); ok && (strings.Contains(f, "uri") || strings.Contains(f, "iri")) {
			into[f] = true
		}
		for _, c := range n {
			uriFormats(c, into)
		}
	case []any:
		for _, c := range n {
			uriFormats(c, into)
		}
	}
}

type listedTool struct {
	Name         string         `json:"name"`
	Description  string         `json:"description"`
	InputSchema  map[string]any `json:"inputSchema"`
	OutputSchema map[string]any `json:"outputSchema"`
	Annotations  *struct {
		ReadOnlyHint    bool  `json:"readOnlyHint"`
		IdempotentHint  bool  `json:"idempotentHint"`
		DestructiveHint *bool `json:"destructiveHint"`
		OpenWorldHint   *bool `json:"openWorldHint"`
	} `json:"annotations"`
}

func TestToolsListSecuritySchemes(t *testing.T) {
	env := testhub.Start(t)
	_, secret := env.NewPeer("session", "profile")
	for _, era := range []string{mcpserver.ProtoCurrent, mcpserver.ProtoLegacy} {
		t.Run(era, func(t *testing.T) {
			headers := modern()
			if era == mcpserver.ProtoLegacy {
				headers = map[string]string{"Mcp-Session-Id": legacySession(t, env, secret), "Accept": "application/json"}
			}
			resp, body := do(t, env, call{secret: secret, headers: headers,
				body: map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/list"}})
			r := decodeRPC(t, body)
			if resp.StatusCode != http.StatusOK || r.Error != nil {
				t.Fatalf("tools/list: %d %+v", resp.StatusCode, r.Error)
			}
			var result struct {
				Tools []map[string]any `json:"tools"`
			}
			if err := json.Unmarshal(r.Result, &result); err != nil {
				t.Fatal(err)
			}
			found := false
			for _, tool := range result.Tools {
				want := []any{map[string]any{"type": "oauth2", "scopes": []any{oauth.Scope}}}
				if !reflect.DeepEqual(tool["securitySchemes"], want) {
					t.Errorf("%s: securitySchemes = %v, want %v", tool["name"], tool["securitySchemes"], want)
				}
				if tool["name"] != "vecnet_whoami" {
					continue
				}
				found = true
				if !reflect.DeepEqual(tool["_meta"], map[string]any{"openai/profile": true}) {
					t.Errorf("whoami _meta = %v", tool["_meta"])
				}
				annotations, ok := tool["annotations"].(map[string]any)
				if !ok || annotations["readOnlyHint"] != true || annotations["destructiveHint"] != false || annotations["openWorldHint"] != false {
					t.Errorf("whoami annotations = %v", tool["annotations"])
				}
				wantInput := map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false}
				if !reflect.DeepEqual(tool["inputSchema"], wantInput) {
					t.Errorf("whoami inputSchema = %v", tool["inputSchema"])
				}
				wantOutput := map[string]any{"type": "object", "properties": map[string]any{
					"kind":    map[string]any{"enum": []any{"peer", "principal", "operator"}},
					"peer_id": map[string]any{"type": "string"}, "principal_id": map[string]any{"type": "string"},
					"name": map[string]any{"type": "string"},
					"id":   map[string]any{"type": "string", "minLength": float64(1)},
				}, "required": []any{"kind", "id"}, "additionalProperties": false}
				if !reflect.DeepEqual(tool["outputSchema"], wantOutput) {
					t.Errorf("whoami outputSchema = %v, want %v", tool["outputSchema"], wantOutput)
				}
			}
			if !found {
				t.Fatal("tools/list lacks vecnet_whoami")
			}
		})
	}
}

func TestWhoamiIdentity(t *testing.T) {
	env := testhub.Start(t)
	peer, secret := env.NewPeer("session", "Profile name")
	prn, prnSecret := env.NewPrincipal("Profile principal")
	for _, tc := range []struct {
		name, secret string
		want         map[string]string
	}{
		{"peer", secret, map[string]string{"id": peer, "kind": "peer", "peer_id": peer, "name": "Profile name"}},
		{"principal", prnSecret, map[string]string{"id": prn, "kind": "principal", "principal_id": prn}},
		{"operator", env.OperatorSecret, map[string]string{"id": env.OperatorPeer, "kind": "operator"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got map[string]string
			toolOK(t, env, tc.secret, "vecnet_whoami", map[string]any{}, &got)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("whoami = %v, want %v", got, tc.want)
			}
			resp, body := env.Do(tc.secret, "GET", "/v1/peers/me", nil)
			var me map[string]string
			if resp.StatusCode != http.StatusOK || json.Unmarshal(body, &me) != nil {
				t.Fatalf("GET peers/me: %d %s", resp.StatusCode, body)
			}
			if name := tc.want["name"]; name != "" {
				me["name"] = name
			}
			me["id"] = tc.want["id"]
			if !reflect.DeepEqual(got, me) {
				t.Fatalf("whoami = %v, HTTP identity plus name = %v", got, me)
			}
		})
	}
	t.Run("OAuth dot", func(t *testing.T) {
		dot, _ := env.NewPeer("dot", "Bound dot")
		owner, err := env.Hub.PeerPrincipal(context.Background(), dot)
		if err != nil {
			t.Fatal(err)
		}
		front := eventsFront(t, env, fakeOAuth{prn: owner, expires: func() time.Time { return env.Now.Now().Add(time.Hour) }}, func(s *mcpserver.Server) {
			s.ResolveDotPeer = func(_ context.Context, principal string) (string, error) {
				if principal != owner {
					t.Errorf("ResolveDotPeer principal = %s, want %s", principal, owner)
				}
				return dot, nil
			}
		})
		var got map[string]string
		toolOK(t, front, "oauth-token", "vecnet_whoami", map[string]any{}, &got)
		want := map[string]string{"id": dot, "kind": "peer", "peer_id": dot, "name": "Bound dot"}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("OAuth whoami = %v, want %v", got, want)
		}
	})
	if path := invalidPath(t, env, secret, "vecnet_whoami", map[string]any{"as": peer}); path != "/as" {
		t.Fatalf("whoami accepts identity override: path = %q", path)
	}
}

func TestToolsListNamesAndHints(t *testing.T) {
	env := testhub.Start(t)
	_, secret := env.NewPeer("session", "a1")

	status, r := rpc(t, env, secret, "tools/list", nil)
	if status != http.StatusOK || r.Error != nil {
		t.Fatalf("tools/list: %d %+v", status, r.Error)
	}
	var res map[string]json.RawMessage
	if err := json.Unmarshal(r.Result, &res); err != nil {
		t.Fatal(err)
	}
	if _, has := res["nextCursor"]; has {
		t.Fatalf("nextCursor present: %s", r.Result)
	}
	var tools []listedTool
	if err := json.Unmarshal(res["tools"], &tools); err != nil {
		t.Fatalf("tools: %v", err)
	}

	want := []string{
		"vecnet_ack", "vecnet_blob_get", "vecnet_blob_put", "vecnet_clear_avatar", "vecnet_group_add", "vecnet_group_create", "vecnet_groups", "vecnet_guide",
		"vecnet_inbox", "vecnet_peer_create", "vecnet_peer_set_disabled", "vecnet_peers", "vecnet_presence", "vecnet_presence_subscribe", "vecnet_react", "vecnet_receipt",
		"vecnet_reply", "vecnet_send", "vecnet_set_avatar", "vecnet_subscriptions", "vecnet_thread", "vecnet_threads", "vecnet_whoami",
	}
	var got []string
	readOnly := map[string]bool{"vecnet_guide": true, "vecnet_blob_get": true, "vecnet_groups": true, "vecnet_inbox": true, "vecnet_subscriptions": true, "vecnet_threads": true, "vecnet_thread": true, "vecnet_peers": true, "vecnet_whoami": true}
	for _, tool := range tools {
		got = append(got, tool.Name)
		if tool.Annotations == nil {
			t.Fatalf("%s: no annotations", tool.Name)
		}
		if tool.Annotations.ReadOnlyHint != readOnly[tool.Name] {
			t.Errorf("%s: readOnlyHint = %v, want %v", tool.Name, tool.Annotations.ReadOnlyHint, readOnly[tool.Name])
		}
		if tool.Name == "vecnet_peer_create" || tool.Name == "vecnet_peer_set_disabled" {
			if !tool.Annotations.IdempotentHint {
				t.Errorf("%s: idempotentHint = false, want true", tool.Name)
			}
			if hint := tool.Annotations.DestructiveHint; hint == nil || *hint {
				t.Errorf("%s: destructiveHint must be explicitly false", tool.Name)
			}
			if hint := tool.Annotations.OpenWorldHint; hint == nil || *hint {
				t.Errorf("%s: openWorldHint must be explicitly false", tool.Name)
			}
		}
		if tool.Name == "vecnet_group_create" {
			if tool.Annotations.IdempotentHint {
				t.Error("vecnet_group_create: idempotentHint = true, want false: each call creates a new group")
			}
			if hint := tool.Annotations.DestructiveHint; hint == nil || *hint {
				t.Error("vecnet_group_create: destructiveHint must be explicitly false")
			}
			if hint := tool.Annotations.OpenWorldHint; hint == nil || *hint {
				t.Error("vecnet_group_create: openWorldHint must be explicitly false")
			}
		}
		if tool.InputSchema["type"] != "object" {
			t.Errorf("%s: inputSchema.type = %v", tool.Name, tool.InputSchema["type"])
		}
		if tool.Description == "" {
			t.Errorf("%s: no description", tool.Name)
		}
		if strings.Contains(tool.Description, "operator's bus") {
			t.Errorf("%s: description contains redundant operator bus wording: %q", tool.Name, tool.Description)
		}
		if tool.Name == "vecnet_react" {
			props := tool.InputSchema["properties"].(map[string]any)
			if got := props["op"].(map[string]any)["enum"]; mustJSON(t, got) != `["add","remove"]` {
				t.Errorf("reaction op enum = %v", got)
			}
			if got := props["priority"].(map[string]any)["enum"]; mustJSON(t, got) != `["wake","wake_idle","hold"]` {
				t.Errorf("reaction priority enum = %v", got)
			}
		}
		names := map[string]bool{}
		propertyNames(tool.InputSchema, names)
		for name := range names {
			// The one exception (PROTOCOL §4.3, §8.1): vecnet_set_avatar's url,
			// which the hub fetches through the egress guard and re-encodes;
			// it is never stored or passed to another peer.
			if tool.Name == "vecnet_set_avatar" && name == "url" {
				continue
			}
			if strings.Contains(strings.ToLower(name), "url") {
				t.Errorf("%s: inputSchema has a property named %s", tool.Name, name)
			}
		}
		formats := map[string]bool{}
		uriFormats(tool.InputSchema, formats)
		if len(formats) != 0 {
			t.Errorf("%s: inputSchema has URI formats %v", tool.Name, formats)
		}
	}
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Fatalf("tools = %v, want %v", got, want)
	}

	// vecnet_send keeps its required call shape in the MCP projection.
	// TestProjectedProperties compiles every schema to check ref resolution.
	i := slices.IndexFunc(tools, func(x listedTool) bool { return x.Name == "vecnet_send" })
	send := tools[i].InputSchema
	req, _ := send["required"].([]any)
	for _, k := range []string{"idempotency_key", "priority", "content"} {
		if !slices.Contains(req, any(k)) {
			t.Errorf("vecnet_send required = %v, missing %s", req, k)
		}
	}
	props := send["properties"].(map[string]any)
	if props["as"].(map[string]any)["type"] != "string" {
		t.Errorf("vecnet_send acting peer is not a string: %v", props["as"])
	}
	// ChatGPT read the top-level anyOf (thread_id | recipients) as making
	// thread_id required, so a new conversation looked impossible. The rule
	// lives in the description and the hub's own validation instead.
	for _, tool := range tools {
		for _, k := range []string{"anyOf", "oneOf", "allOf"} {
			if _, ok := tool.InputSchema[k]; ok {
				t.Errorf("%s inputSchema has top-level %s", tool.Name, k)
			}
		}
	}
	if desc := tools[i].Description; !strings.Contains(desc, "recipients") || !strings.Contains(desc, "thread_id") {
		t.Errorf("vecnet_send description does not say when to use recipients or thread_id: %q", desc)
	}
}

// A new conversation needs only recipients; neither recipients nor thread_id
// is still refused by the hub's validation.
func TestSendRecipientsOrThread(t *testing.T) {
	env := testhub.Start(t)
	_, aSecret := env.NewPeer("session", "a")
	b, _ := env.NewPeer("session", "b")
	var out sendResult
	toolOK(t, env, aSecret, "vecnet_send", map[string]any{
		"idempotency_key": "new", "recipients": []string{b}, "priority": "hold",
		"content": map[string]any{"text": "hello"},
	}, &out)
	if out.ThreadID == "" {
		t.Fatalf("new send made no thread: %+v", out)
	}
	status, r := rpc(t, env, aSecret, "tools/call", map[string]any{"name": "vecnet_send", "arguments": map[string]any{
		"idempotency_key": "neither", "priority": "hold", "content": map[string]any{"text": "to nobody"},
	}})
	if status != http.StatusOK {
		t.Fatalf("neither: HTTP %d", status)
	}
	if r.Error == nil {
		var res toolResult
		if err := json.Unmarshal(r.Result, &res); err != nil || !res.IsError {
			t.Fatalf("send with neither recipients nor thread_id succeeded: %s", r.Result)
		}
	}
}

// sendResult is the part of a SendResponse these tests read.
type sendResult struct {
	MessageID  string `json:"message_id"`
	ThreadID   string `json:"thread_id"`
	Deliveries []struct {
		DeliveryID string `json:"delivery_id"`
		Recipient  string `json:"recipient"`
		State      string `json:"state"`
	} `json:"deliveries"`
}

type inboxEvent struct {
	Cursor string `json:"cursor"`
	Type   string `json:"type"`
	Data   struct {
		DeliveryID string `json:"delivery_id"`
		Recipient  string `json:"recipient"`
		Ingress    string `json:"ingress"`
		Message    struct {
			MessageID   string         `json:"message_id"`
			ThreadID    string         `json:"thread_id"`
			InReplyTo   *string        `json:"in_reply_to"`
			Sender      string         `json:"sender"`
			Metadata    map[string]any `json:"metadata"`
			Attachments []struct {
				SHA256    string `json:"sha256"`
				OriginURL string `json:"origin_url"`
				BlobURL   string `json:"blob_url"`
			} `json:"attachments"`
		} `json:"message"`
	} `json:"data"`
}

type inboxResult struct {
	Events     []inboxEvent `json:"events"`
	NextCursor string       `json:"next_cursor"`
}

func (r inboxResult) deliveries() []inboxEvent {
	var out []inboxEvent
	for _, e := range r.Events {
		if e.Type == "delivery" {
			out = append(out, e)
		}
	}
	return out
}

func TestSendReplyInboxAckRoundTrip(t *testing.T) {
	env := testhub.Start(t)
	a, aSecret := env.NewPeer("session", "a1")
	b, bSecret := env.NewPeer("session", "b1")

	// idempotency_key is required: its absence is invalid params, not a send.
	if p := invalidPath(t, env, aSecret, "vecnet_send", map[string]any{
		"recipients": []string{b}, "priority": "hold", "content": map[string]any{"text": "hello"},
	}); p != "/idempotency_key" {
		t.Fatalf("data.path = %q, want /idempotency_key", p)
	}

	var sent sendResult
	toolOK(t, env, aSecret, "vecnet_send", map[string]any{
		"idempotency_key": "k1", "recipients": []string{b}, "priority": "hold",
		"content": map[string]any{"text": "hello"},
	}, &sent)
	if !strings.HasPrefix(sent.MessageID, "msg_") || !strings.HasPrefix(sent.ThreadID, "thr_") {
		t.Fatalf("send = %+v", sent)
	}
	if len(sent.Deliveries) != 1 || sent.Deliveries[0].Recipient != b {
		t.Fatalf("deliveries = %+v, want one to %s", sent.Deliveries, b)
	}

	var inbox inboxResult
	toolOK(t, env, bSecret, "vecnet_inbox", map[string]any{}, &inbox)
	ds := inbox.deliveries()
	if len(ds) != 1 {
		t.Fatalf("B's inbox has %d delivery events, want 1: %+v", len(ds), inbox.Events)
	}
	if ds[0].Data.Message.MessageID != sent.MessageID || ds[0].Data.DeliveryID != sent.Deliveries[0].DeliveryID {
		t.Fatalf("delivery = %+v, want message %s", ds[0], sent.MessageID)
	}
	if ds[0].Data.Ingress != "mcp_tool" {
		t.Fatalf("ingress = %q, want mcp_tool", ds[0].Data.Ingress)
	}
	if inbox.NextCursor != inbox.Events[len(inbox.Events)-1].Cursor {
		t.Fatalf("next_cursor = %q, want the last event's cursor", inbox.NextCursor)
	}

	// Nothing new past next_cursor.
	var again inboxResult
	toolOK(t, env, bSecret, "vecnet_inbox", map[string]any{"after": inbox.NextCursor}, &again)
	if len(again.Events) != 0 || again.NextCursor != inbox.NextCursor {
		t.Fatalf("inbox after next_cursor = %+v", again)
	}

	var replied sendResult
	toolOK(t, env, bSecret, "vecnet_reply", map[string]any{
		"message_id": sent.MessageID, "text": "hi back", "priority": "hold", "idempotency_key": "r1",
	}, &replied)
	if replied.ThreadID != sent.ThreadID {
		t.Fatalf("reply thread = %s, want %s", replied.ThreadID, sent.ThreadID)
	}

	var aInbox inboxResult
	toolOK(t, env, aSecret, "vecnet_inbox", map[string]any{}, &aInbox)
	ads := aInbox.deliveries()
	if len(ads) != 1 {
		t.Fatalf("A's inbox has %d delivery events, want 1: %+v", len(ads), aInbox.Events)
	}
	m := ads[0].Data.Message
	if m.MessageID != replied.MessageID || m.ThreadID != sent.ThreadID || m.Sender != b {
		t.Fatalf("A's delivery = %+v", m)
	}
	if m.InReplyTo == nil || *m.InReplyTo != sent.MessageID {
		t.Fatalf("in_reply_to = %v, want %s", m.InReplyTo, sent.MessageID)
	}
	if ads[0].Data.Recipient != a {
		t.Fatalf("recipient = %s, want %s", ads[0].Data.Recipient, a)
	}
	if ads[0].Data.Ingress != "mcp_tool" {
		t.Fatalf("reply ingress = %q, want mcp_tool", ads[0].Data.Ingress)
	}

	toolOK(t, env, bSecret, "vecnet_ack", map[string]any{"cursor": inbox.NextCursor}, nil)
	var state string
	if err := env.Hub.DB.QueryRow(`SELECT state FROM deliveries WHERE dly_id = ?`, sent.Deliveries[0].DeliveryID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "accepted" {
		t.Fatalf("delivery state = %q, want accepted", state)
	}
}

func TestToolErrorIsToolLevel(t *testing.T) {
	env := testhub.Start(t)
	_, secret := env.NewPeer("session", "a1")

	args := map[string]any{
		"idempotency_key": "k1", "recipients": []string{"Max"}, "priority": "hold",
		"content": map[string]any{"text": "hello"},
	}
	// toolFails → callTool already fails on a JSON-RPC error or non-200.
	e := toolFails(t, env, secret, "vecnet_send", args, "name_not_an_address")
	if e.Error.Details["path"] != "/recipients/0" {
		t.Fatalf("details = %v, want path /recipients/0", e.Error.Details)
	}
}

func TestPrincipalAs(t *testing.T) {
	env := testhub.Start(t)
	ctx := context.Background()
	prn, secret := env.NewPrincipal("host")
	p1, _, err := env.Hub.AddPeer(ctx, "session", "p1", prn)
	if err != nil {
		t.Fatal(err)
	}
	p2, _, err := env.Hub.AddPeer(ctx, "session", "p2", prn)
	if err != nil {
		t.Fatal(err)
	}
	x, xSecret := env.NewPeer("session", "x1")

	body := func(key string, extra map[string]any) map[string]any {
		m := map[string]any{
			"idempotency_key": key, "recipients": []string{x}, "priority": "hold",
			"content": map[string]any{"text": "hello"},
		}
		for k, v := range extra {
			m[k] = v
		}
		return m
	}
	toolFails(t, env, secret, "vecnet_send", body("k1", nil), "as_required")

	var sent sendResult
	toolOK(t, env, secret, "vecnet_send", body("k2", map[string]any{"as": p1}), &sent)
	var xInbox inboxResult
	toolOK(t, env, xSecret, "vecnet_inbox", map[string]any{}, &xInbox)
	if ds := xInbox.deliveries(); len(ds) != 1 || ds[0].Data.Message.Sender != p1 {
		t.Fatalf("X's deliveries = %+v, want one from %s", ds, p1)
	}

	// vecnet_reply needs `as` from a principal too.
	toolFails(t, env, secret, "vecnet_reply", map[string]any{
		"message_id": sent.MessageID, "text": "x", "priority": "hold", "idempotency_key": "r0",
	}, "as_required")

	for i, to := range []string{p1, p2} {
		toolOK(t, env, xSecret, "vecnet_send", map[string]any{
			"idempotency_key": "x" + string(rune('0'+i)), "recipients": []string{to}, "priority": "hold",
			"content": map[string]any{"text": "ping"},
		}, nil)
	}

	recipients := func(r inboxResult) []string {
		out := []string{}
		for _, e := range r.deliveries() {
			out = append(out, e.Data.Recipient)
		}
		slices.Sort(out)
		return out
	}
	var all, narrow inboxResult
	toolOK(t, env, secret, "vecnet_inbox", map[string]any{}, &all)
	wantAll := []string{p1, p2}
	slices.Sort(wantAll)
	if got := recipients(all); !slices.Equal(got, wantAll) {
		t.Fatalf("principal inbox recipients = %v, want %v", got, wantAll)
	}
	toolOK(t, env, secret, "vecnet_inbox", map[string]any{"peer": p1}, &narrow)
	if got := recipients(narrow); !slices.Equal(got, []string{p1}) {
		t.Fatalf("narrowed inbox recipients = %v, want [%s]", got, p1)
	}

	// A peer outside the principal cannot be named.
	toolFails(t, env, secret, "vecnet_inbox", map[string]any{"peer": x}, "not_your_peer")

	// Narrowing to a disabled peer is the 403 of GET /v1/events, as a tool error.
	if err := env.Hub.SetPeerDisabled(ctx, p2, true); err != nil {
		t.Fatal(err)
	}
	toolFails(t, env, secret, "vecnet_inbox", map[string]any{"peer": p2}, "forbidden")
}

func TestSchemaValidationPath(t *testing.T) {
	env := testhub.Start(t)
	a, aSecret := env.NewPeer("session", "a1")
	b, bSecret := env.NewPeer("session", "b1")
	_ = a

	var sent sendResult
	toolOK(t, env, aSecret, "vecnet_send", map[string]any{
		"idempotency_key": "k1", "recipients": []string{b}, "priority": "hold",
		"content": map[string]any{"text": "hello"},
	}, &sent)
	dly := sent.Deliveries[0].DeliveryID

	if p := invalidPath(t, env, bSecret, "vecnet_receipt", map[string]any{"delivery_id": dly, "state": "accepted"}); p != "/state" {
		t.Fatalf("data.path = %q, want /state", p)
	}

	// The same arguments with a valid state go through.
	toolOK(t, env, bSecret, "vecnet_receipt", map[string]any{"delivery_id": dly, "state": "processed", "note": "done"}, nil)
}

func TestReplyTargetIsChecked(t *testing.T) {
	env := testhub.Start(t)
	_, aSecret := env.NewPeer("session", "a1")
	b, _ := env.NewPeer("session", "b1")
	_, cSecret := env.NewPeer("session", "c1")

	var sent sendResult
	toolOK(t, env, aSecret, "vecnet_send", map[string]any{
		"idempotency_key": "k1", "recipients": []string{b}, "priority": "hold",
		"content": map[string]any{"text": "hello"},
	}, &sent)

	reply := func(id string) map[string]any {
		return map[string]any{"message_id": id, "text": "x", "priority": "hold", "idempotency_key": "r1"}
	}
	// An outsider is told no more than for a message that does not exist.
	outsider := toolFails(t, env, cSecret, "vecnet_reply", reply(sent.MessageID), "not_found")
	missing := toolFails(t, env, cSecret, "vecnet_reply", reply("msg_00000000000000000000000000"), "not_found")
	if outsider.Error.Message != missing.Error.Message {
		t.Fatalf("outsider %q differs from missing %q", outsider.Error.Message, missing.Error.Message)
	}
	if p := invalidPath(t, env, aSecret, "vecnet_reply", map[string]any{
		"message_id": sent.MessageID, "text": "x", "priority": "hold",
	}); p != "/idempotency_key" {
		t.Fatalf("data.path = %q, want /idempotency_key", p)
	}
	if p := invalidPath(t, env, aSecret, "vecnet_reply", map[string]any{
		"message_id": sent.MessageID, "priority": "hold", "idempotency_key": "r2",
	}); p != "/text" {
		t.Fatalf("data.path = %q, want /text", p)
	}
}

// The operator may reply into a thread it does not participate in, so
// ReplyThread must not hide the message from it.
func TestOperatorReplyOutsideThread(t *testing.T) {
	env := testhub.Start(t)
	_, aSecret := env.NewPeer("session", "a1")
	b, bSecret := env.NewPeer("session", "b1")

	var sent, replied sendResult
	toolOK(t, env, aSecret, "vecnet_send", map[string]any{
		"idempotency_key": "k1", "recipients": []string{b}, "priority": "hold",
		"content": map[string]any{"text": "hello"},
	}, &sent)
	toolOK(t, env, env.OperatorSecret, "vecnet_reply", map[string]any{
		"message_id": sent.MessageID, "text": "noted", "priority": "hold", "idempotency_key": "r1",
	}, &replied)
	if replied.ThreadID != sent.ThreadID {
		t.Fatalf("reply thread = %s, want %s", replied.ThreadID, sent.ThreadID)
	}
	var inbox inboxResult
	toolOK(t, env, bSecret, "vecnet_inbox", map[string]any{}, &inbox)
	ds := inbox.deliveries()
	if len(ds) != 2 || ds[1].Data.Message.Sender != env.OperatorPeer {
		t.Fatalf("B's deliveries = %+v, want a second one from %s", ds, env.OperatorPeer)
	}
}

// PROTOCOL §4.3: write tools MUST NOT accept a URL from the model, and an
// attachment's origin_url would otherwise reach the recipient and excuse an
// unknown hash.
func TestAttachmentURLsRejected(t *testing.T) {
	env := testhub.Start(t)
	_, aSecret := env.NewPeer("session", "a1")
	b, bSecret := env.NewPeer("session", "b1")

	var sent sendResult
	toolOK(t, env, aSecret, "vecnet_send", map[string]any{
		"idempotency_key": "k1", "recipients": []string{b}, "priority": "hold",
		"content": map[string]any{"text": "hello"},
	}, &sent)

	unknown := strings.Repeat("ab", 32)
	att := func(extra ...string) map[string]any {
		m := map[string]any{"sha256": unknown, "size": 1, "media_type": "text/plain"}
		for _, k := range extra {
			m[k] = "https://evil.example/x"
		}
		return m
	}
	send := func(key string, atts ...map[string]any) map[string]any {
		return map[string]any{
			"idempotency_key": key, "recipients": []string{b}, "priority": "hold",
			"content": map[string]any{"text": "x"}, "attachments": atts,
		}
	}
	reply := func(key string, atts ...map[string]any) map[string]any {
		return map[string]any{
			"message_id": sent.MessageID, "text": "x", "priority": "hold",
			"idempotency_key": key, "attachments": atts,
		}
	}
	for _, field := range []string{"origin_url", "blob_url"} {
		want := "/attachments/0/" + field
		if p := invalidPath(t, env, aSecret, "vecnet_send", send("s-"+field, att(field))); p != want {
			t.Errorf("vecnet_send %s: data.path = %q, want %q", field, p, want)
		}
		if p := invalidPath(t, env, bSecret, "vecnet_reply", reply("r-"+field, att(field))); p != want {
			t.Errorf("vecnet_reply %s: data.path = %q, want %q", field, p, want)
		}
	}

	// With no way to name a URL, an unknown hash is always unknown_blob.
	toolFails(t, env, aSecret, "vecnet_send", send("k2", att()), "unknown_blob")
	toolFails(t, env, bSecret, "vecnet_reply", reply("r2", att()), "unknown_blob")

	// A repeated key is validated by its last value, and a struct decoder
	// would merge the earlier one in: the URL must not survive that way.
	hosted := []byte("hosted bytes")
	sum := sha256.Sum256(hosted)
	sha := hex.EncodeToString(sum[:])
	if resp, body := env.Do(aSecret, http.MethodPut, "/v1/blobs", hosted); resp.StatusCode/100 != 2 {
		t.Fatalf("PUT /v1/blobs: %d %s", resp.StatusCode, body)
	}
	smuggled := fmt.Sprintf(`{"idempotency_key":"k3","recipients":[%q],"priority":"hold","content":{"text":"x"},`+
		`"attachments":[{"sha256":%[2]q,"size":%[3]d,"media_type":"text/plain","origin_url":"https://evil.example/x"}],`+
		`"attachments":[{"sha256":%[2]q,"size":%[3]d,"media_type":"text/plain"}]}`, b, sha, len(hosted))
	if res := verbatimResult(t, env, aSecret, "vecnet_send", smuggled); res.IsError {
		t.Fatalf("repeated attachments: %s", res.StructuredContent)
	}

	// A hash the sender uploaded is still sent. The hub's per-recipient
	// blob_url stays off the MCP result (TestInboxCarriesNoBlobToken).
	var inbox inboxResult
	toolOK(t, env, bSecret, "vecnet_inbox", map[string]any{}, &inbox)
	ds := inbox.deliveries()
	if len(ds) != 2 {
		t.Fatalf("B has %d deliveries, want 2", len(ds))
	}
	got := ds[1].Data.Message.Attachments
	if len(got) != 1 || got[0].SHA256 != sha || got[0].OriginURL != "" || got[0].BlobURL != "" {
		t.Fatalf("delivered attachments = %+v, want %s with no URL", got, sha)
	}
}

// §2.2 step 9 caps the body the client sent. Escaping a value on the way to
// the hub must not make it bigger than POST /v1/messages would see.
func TestCapsCountTheBodyAsSent(t *testing.T) {
	env := testhub.Start(t)
	_, aSecret := env.NewPeer("session", "a1")
	b, bSecret := env.NewPeer("session", "b1")

	var first sendResult
	toolOK(t, env, aSecret, "vecnet_send", map[string]any{
		"idempotency_key": "k0", "recipients": []string{b}, "priority": "hold",
		"content": map[string]any{"text": "hello"},
	}, &first)

	n := 0
	key := func() string { n++; return fmt.Sprintf("k%d", n) }
	for _, tc := range []struct {
		name, s string
		count   int
	}{
		{"lt", "<", 3000}, {"gt", ">", 3000}, {"amp", "&", 3000},
		{"line separator", "\u2028", 2000}, {"paragraph separator", "\u2029", 2000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			value := strings.Repeat(tc.s, tc.count)
			meta := fmt.Sprintf(`{"k":"%s"}`, value)
			body := func(k string) string {
				return fmt.Sprintf(`{"idempotency_key":%q,"recipients":[%q],"priority":"hold","content":{"text":"x"},"metadata":%s}`, k, b, meta)
			}
			if resp, out := env.Do(aSecret, http.MethodPost, "/v1/messages", body(key())); resp.StatusCode != http.StatusAccepted {
				t.Fatalf("POST /v1/messages: %d %s", resp.StatusCode, out)
			}
			if res := verbatimResult(t, env, aSecret, "vecnet_send", body(key())); res.IsError {
				t.Fatalf("vecnet_send: %s", res.StructuredContent)
			}
			reply := fmt.Sprintf(`{"message_id":%q,"text":"x","priority":"hold","idempotency_key":%q,"metadata":%s}`, first.MessageID, key(), meta)
			if res := verbatimResult(t, env, bSecret, "vecnet_reply", reply); res.IsError {
				t.Fatalf("vecnet_reply: %s", res.StructuredContent)
			}
		})
	}

	// The value arrives intact, not merely undersized.
	want := "\u2028<" + strings.Repeat("&", 100)
	args := fmt.Sprintf(`{"idempotency_key":"keep","recipients":[%q],"priority":"hold","content":{"text":"x"},"metadata":{"k":"%s"}}`, b, want)
	if res := verbatimResult(t, env, aSecret, "vecnet_send", args); res.IsError {
		t.Fatalf("vecnet_send: %s", res.StructuredContent)
	}
	var inbox inboxResult
	toolOK(t, env, bSecret, "vecnet_inbox", map[string]any{"limit": 200}, &inbox)
	ds := inbox.deliveries()
	if got := ds[len(ds)-1].Data.Message.Metadata["k"]; got != want {
		t.Fatalf("metadata k = %q, want %q", got, want)
	}

	// job_request.input has its own cap and the same exposure.
	job := fmt.Sprintf(`{"idempotency_key":"j1","recipients":[%q],"priority":"hold","content":{"text":"x"},`+
		`"job_request":{"version":1,"job_key":"j1","operation":"build.run","instructions":"go","input":{"k":"%s"}}}`, b, strings.Repeat("<", 6000))
	if res := verbatimResult(t, env, aSecret, "vecnet_send", job); res.IsError {
		t.Fatalf("job_request: %s", res.StructuredContent)
	}
}

// vecnet_reply is sugar for vecnet_send (PROTOCOL §4.3): the same oversize
// fields give the same tool error, whatever the encoding of the text.
func TestReplyOversizeIsPayloadTooLarge(t *testing.T) {
	env := testhub.Start(t)
	_, aSecret := env.NewPeer("session", "a1")
	b, bSecret := env.NewPeer("session", "b1")

	var sent sendResult
	toolOK(t, env, aSecret, "vecnet_send", map[string]any{
		"idempotency_key": "k1", "recipients": []string{b}, "priority": "hold",
		"content": map[string]any{"text": "hello"},
	}, &sent)

	bigMeta := map[string]any{}
	for i := range 40 {
		bigMeta[fmt.Sprintf("k%d", i)] = strings.Repeat("v", 300)
	}
	for _, tc := range []struct {
		name, field string
		limit       float64
		extra       map[string]any
	}{
		{"ascii text", "content.text", 65536, map[string]any{"text": strings.Repeat("a", 70000)}},
		{"multi-byte text", "content.text", 65536, map[string]any{"text": strings.Repeat("\u20ac", 30000)}},
		{"metadata past maxProperties", "metadata", 8192, map[string]any{"metadata": bigMeta}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args := map[string]any{"message_id": sent.MessageID, "text": "x", "priority": "hold", "idempotency_key": "r1"}
			for k, v := range tc.extra {
				args[k] = v
			}
			e := toolFails(t, env, bSecret, "vecnet_reply", args, "payload_too_large")
			if e.Error.Details["field"] != tc.field || e.Error.Details["limit"] != tc.limit {
				t.Fatalf("details = %v, want field %s limit %v", e.Error.Details, tc.field, tc.limit)
			}
		})
	}

	// Text at the cap is a normal reply.
	toolOK(t, env, bSecret, "vecnet_reply", map[string]any{
		"message_id": sent.MessageID, "text": strings.Repeat("a", 65536), "priority": "hold", "idempotency_key": "r2",
	}, nil)
}

func TestIdempotentReplay(t *testing.T) {
	env := testhub.Start(t)
	_, aSecret := env.NewPeer("session", "a1")
	b, bSecret := env.NewPeer("session", "b1")

	args := map[string]any{
		"idempotency_key": "k1", "recipients": []string{b}, "priority": "hold",
		"content": map[string]any{"text": "hello"},
	}
	var first, second sendResult
	toolOK(t, env, aSecret, "vecnet_send", args, &first)
	toolOK(t, env, aSecret, "vecnet_send", args, &second)
	if first.MessageID != second.MessageID {
		t.Fatalf("replay message_id = %s, want %s", second.MessageID, first.MessageID)
	}
	var inbox inboxResult
	toolOK(t, env, bSecret, "vecnet_inbox", map[string]any{}, &inbox)
	if n := len(inbox.deliveries()); n != 1 {
		t.Fatalf("%d deliveries after a replay, want 1", n)
	}

	args["content"] = map[string]any{"text": "different"}
	toolFails(t, env, aSecret, "vecnet_send", args, "idempotency_conflict")
}

func TestReadToolsAndPresence(t *testing.T) {
	env := testhub.Start(t)
	a, aSecret := env.NewPeer("session", "a1")
	b, _ := env.NewPeer("session", "b1")
	_, cSecret := env.NewPeer("session", "c1")

	var sent sendResult
	toolOK(t, env, aSecret, "vecnet_send", map[string]any{
		"idempotency_key": "k1", "recipients": []string{b}, "priority": "hold",
		"thread_title": "t", "content": map[string]any{"text": "hello"},
	}, &sent)

	var threads struct {
		Threads []struct {
			ThreadID string `json:"thread_id"`
		} `json:"threads"`
		Next *string `json:"next"`
	}
	toolOK(t, env, aSecret, "vecnet_threads", map[string]any{}, &threads)
	if len(threads.Threads) != 1 || threads.Threads[0].ThreadID != sent.ThreadID || threads.Next != nil {
		t.Fatalf("threads = %+v", threads)
	}
	var none struct {
		Threads []any `json:"threads"`
	}
	toolOK(t, env, cSecret, "vecnet_threads", map[string]any{}, &none)
	if none.Threads == nil || len(none.Threads) != 0 {
		t.Fatalf("C's threads = %+v, want an empty array", none)
	}

	var hist struct {
		Messages []struct {
			MessageID string `json:"message_id"`
		} `json:"messages"`
		NextAfter *string `json:"next_after"`
	}
	toolOK(t, env, aSecret, "vecnet_thread", map[string]any{"id": sent.ThreadID}, &hist)
	if len(hist.Messages) != 1 || hist.Messages[0].MessageID != sent.MessageID || hist.NextAfter != nil {
		t.Fatalf("history = %+v", hist)
	}
	toolFails(t, env, cSecret, "vecnet_thread", map[string]any{"id": sent.ThreadID}, "not_participant")

	toolOK(t, env, aSecret, "vecnet_presence", map[string]any{
		"state": "working", "at": venv.FormatTime(env.Now.Now()), "location": "here",
		"capabilities": []string{"go"}, "quota": map[string]any{"weekly_remaining_pct": 40},
	}, nil)
	if p := invalidPath(t, env, aSecret, "vecnet_presence", map[string]any{
		"peer_id": a, "state": "idle", "at": venv.FormatTime(env.Now.Now()),
	}); p != "/peer_id" {
		t.Fatalf("data.path = %q, want /peer_id", p)
	}

	var dir struct {
		Peers []map[string]any `json:"peers"`
	}
	toolOK(t, env, aSecret, "vecnet_peers", map[string]any{}, &dir)
	i := slices.IndexFunc(dir.Peers, func(p map[string]any) bool { return p["peer_id"] == a })
	if i < 0 || dir.Peers[i]["location"] != "here" {
		t.Fatalf("peers = %+v, want %s with location here", dir.Peers, a)
	}
	if p := invalidPath(t, env, aSecret, "vecnet_peers", map[string]any{"url": "https://example.com"}); p != "/url" {
		t.Fatalf("data.path = %q, want /url", p)
	}
}

func TestInboxCursors(t *testing.T) {
	env := testhub.Start(t)
	_, aSecret := env.NewPeer("session", "a1")
	b, bSecret := env.NewPeer("session", "b1")

	send := func(key string) {
		t.Helper()
		toolOK(t, env, aSecret, "vecnet_send", map[string]any{
			"idempotency_key": key, "recipients": []string{b}, "priority": "hold",
			"content": map[string]any{"text": "hello"},
		}, nil)
	}
	send("k1")
	send("k2")

	var page inboxResult
	toolOK(t, env, bSecret, "vecnet_inbox", map[string]any{"limit": 1}, &page)
	if len(page.Events) != 1 {
		t.Fatalf("limit 1 returned %d events", len(page.Events))
	}
	// An ack moves where an argument-less read starts.
	toolOK(t, env, bSecret, "vecnet_ack", map[string]any{"cursor": page.NextCursor}, nil)
	var rest inboxResult
	toolOK(t, env, bSecret, "vecnet_inbox", map[string]any{}, &rest)
	if len(rest.deliveries()) != 1 || rest.Events[0].Cursor == page.Events[0].Cursor {
		t.Fatalf("after ack: %+v", rest.Events)
	}

	var head inboxResult
	toolOK(t, env, bSecret, "vecnet_inbox", map[string]any{"after": "head"}, &head)
	if len(head.Events) != 0 || head.NextCursor != rest.NextCursor {
		t.Fatalf("after=head: %+v, want no events at %s", head, rest.NextCursor)
	}

	if p := invalidPath(t, env, bSecret, "vecnet_inbox", map[string]any{"limit": 201}); p != "/limit" {
		t.Fatalf("data.path = %q, want /limit", p)
	}
	if p := invalidPath(t, env, bSecret, "vecnet_ack", map[string]any{"cursor": "nope"}); p != "/cursor" {
		t.Fatalf("data.path = %q, want /cursor", p)
	}

	// A cursor from another epoch is the §2.3 409 as a tool error.
	e := toolFails(t, env, bSecret, "vecnet_inbox", map[string]any{"after": "e999999.1"}, "cursor_purged")
	if e.Error.Details["oldest_cursor"] == "" || e.Error.Details["oldest_cursor"] == nil {
		t.Fatalf("details = %v, want oldest_cursor", e.Error.Details)
	}
}

// JSON Schema's integer admits 5.0 and 1e1, so the tools must too.
func TestLimitAcceptsIntegralNumbers(t *testing.T) {
	env := testhub.Start(t)
	_, aSecret := env.NewPeer("session", "a1")
	b, bSecret := env.NewPeer("session", "b1")

	var sent sendResult
	toolOK(t, env, aSecret, "vecnet_send", map[string]any{
		"idempotency_key": "k1", "recipients": []string{b}, "priority": "hold",
		"content": map[string]any{"text": "hello"},
	}, &sent)

	for _, limit := range []string{"5.0", "1e1"} {
		for _, c := range []struct{ name, args string }{
			{"vecnet_inbox", `{"limit":` + limit + `}`},
			{"vecnet_threads", `{"limit":` + limit + `}`},
			{"vecnet_thread", `{"id":"` + sent.ThreadID + `","limit":` + limit + `}`},
		} {
			if res := verbatimResult(t, env, bSecret, c.name, c.args); res.IsError {
				t.Errorf("%s limit %s: %s", c.name, limit, res.StructuredContent)
			}
		}
	}
	status, r := rpcVerbatim(t, env, bSecret, "vecnet_inbox", `{"limit":1.5}`)
	if status != http.StatusOK || r.Error == nil || r.Error.Code != mcpserver.ErrInvalidParams {
		t.Fatalf("limit 1.5: %d %+v", status, r.Error)
	}
}

func TestToolsCallBadRequests(t *testing.T) {
	env := testhub.Start(t)
	_, secret := env.NewPeer("session", "a1")

	for _, params := range []any{nil, map[string]any{"name": "vecnet_nope"}, map[string]any{"name": 5}} {
		status, r := rpc(t, env, secret, "tools/call", params)
		if status != http.StatusOK || r.Error == nil || r.Error.Code != mcpserver.ErrInvalidParams {
			t.Fatalf("params %v: %d %+v", params, status, r.Error)
		}
	}
	// Absent arguments are an empty object.
	status, r := rpc(t, env, secret, "tools/call", map[string]any{"name": "vecnet_peers"})
	if status != http.StatusOK || r.Error != nil {
		t.Fatalf("no arguments: %d %+v", status, r.Error)
	}
}

func TestToolCallNotificationIsNotRun(t *testing.T) {
	env := testhub.Start(t)
	_, aSecret := env.NewPeer("session", "a1")
	b, bSecret := env.NewPeer("session", "b1")

	resp, _ := do(t, env, call{secret: aSecret, headers: modern(), body: map[string]any{
		"jsonrpc": "2.0", "method": "tools/call",
		"params": map[string]any{"name": "vecnet_send", "arguments": map[string]any{
			"idempotency_key": "k1", "recipients": []string{b}, "priority": "hold",
			"content": map[string]any{"text": "hello"},
		}},
	}})
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", resp.StatusCode)
	}
	var inbox inboxResult
	toolOK(t, env, bSecret, "vecnet_inbox", map[string]any{}, &inbox)
	if n := len(inbox.deliveries()); n != 0 {
		t.Fatalf("a notification sent %d messages", n)
	}
}

func TestToolsOnLegacySession(t *testing.T) {
	env := testhub.Start(t)
	_, secret := env.NewPeer("session", "a1")
	sid := legacySession(t, env, secret)

	resp, body := do(t, env, call{secret: secret,
		headers: map[string]string{"Mcp-Session-Id": sid, "Accept": "application/json, text/event-stream"},
		body: map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/call",
			"params": map[string]any{"name": "vecnet_peers", "arguments": map[string]any{}}}})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d: %s", resp.StatusCode, body)
	}
	r := decodeRPC(t, body)
	var res toolResult
	if r.Error != nil || json.Unmarshal(r.Result, &res) != nil || res.IsError {
		t.Fatalf("result: %+v %s", r.Error, r.Result)
	}
}

func TestToolSendKicksPushWorker(t *testing.T) {
	env := testhub.Start(t)
	var kicks atomic.Int64
	env.Configure(func(d *httpapi.Deps) { d.Kick = func() { kicks.Add(1) } })
	_, secret := env.NewPeer("session", "a1")
	b, _ := env.NewPeer("session", "b1")

	args := map[string]any{
		"idempotency_key": "k1", "recipients": []string{b}, "priority": "wake",
		"content": map[string]any{"text": "hello"},
	}
	toolOK(t, env, secret, "vecnet_send", args, nil)
	if n := kicks.Load(); n != 1 {
		t.Fatalf("kicks after a send = %d, want 1", n)
	}
	toolOK(t, env, secret, "vecnet_send", args, nil) // replay: nothing new to push
	if n := kicks.Load(); n != 1 {
		t.Fatalf("kicks after a replay = %d, want 1", n)
	}
}

func TestReactTool(t *testing.T) {
	setup := func(t *testing.T) (*testhub.Env, string, string, string, string, sendResult) {
		t.Helper()
		env := testhub.Start(t)
		sender, senderSecret := env.NewPeer("session", "sender")
		reactor, reactorSecret := env.NewPeer("session", "reactor")
		var sent sendResult
		toolOK(t, env, senderSecret, "vecnet_send", map[string]any{
			"idempotency_key": "target", "recipients": []string{reactor},
			"priority": "hold", "content": map[string]any{"text": "react to this"},
		}, &sent)
		return env, sender, senderSecret, reactor, reactorSecret, sent
	}

	t.Run("check by name defaults to add and wake_idle", func(t *testing.T) {
		env, sender, senderSecret, reactor, secret, sent := setup(t)
		var kicks atomic.Int64
		env.Configure(func(d *httpapi.Deps) { d.Kick = func() { kicks.Add(1) } })
		var got hub.ReactResponse
		toolOK(t, env, secret, "vecnet_react", map[string]any{
			"message_id": sent.MessageID, "emoji": "check",
		}, &got)
		if !got.Changed || got.Op != "add" || got.Emoji != venv.ReactionNames["check"] ||
			got.MessageID != sent.MessageID || got.ThreadID != sent.ThreadID {
			t.Fatalf("reaction = %+v, want canonical check added to target", got)
		}
		if len(got.Reactions) != 1 || got.Reactions[0].Emoji != got.Emoji ||
			!slices.Equal(got.Reactions[0].Peers, []string{reactor}) {
			t.Fatalf("reactions = %+v, want check by %s", got.Reactions, reactor)
		}
		if got.Superseded == nil || len(got.Superseded) != 0 || got.Wake == nil ||
			got.Wake.Recipient != sender || got.Wake.MessageID == "" || got.Wake.DeliveryID == "" {
			t.Fatalf("wake/superseded = %+v / %v, want new wake to sender and empty array", got.Wake, got.Superseded)
		}
		var inbox struct {
			Events []struct {
				Type string `json:"type"`
				Data struct {
					DeliveryID string       `json:"delivery_id"`
					Ingress    string       `json:"ingress"`
					Message    venv.Message `json:"message"`
				} `json:"data"`
			} `json:"events"`
		}
		toolOK(t, env, senderSecret, "vecnet_inbox", map[string]any{}, &inbox)
		found := false
		for _, event := range inbox.Events {
			if event.Type == "delivery" && event.Data.DeliveryID == got.Wake.DeliveryID {
				found = true
				if event.Data.Message.Priority != "wake_idle" || event.Data.Message.Kind != "reaction" ||
					event.Data.Message.MessageID != got.Wake.MessageID || event.Data.Ingress != "mcp_tool" {
					t.Fatalf("wake delivery = %+v, want wake_idle reaction via mcp_tool", event.Data)
				}
			}
		}
		if !found || kicks.Load() != 1 {
			t.Fatalf("wake found = %v, kicks = %d, want true and 1", found, kicks.Load())
		}
		// A retry succeeds without creating another wake.
		toolOK(t, env, secret, "vecnet_react", map[string]any{
			"message_id": sent.MessageID, "emoji": "check",
		}, &got)
		if got.Changed || got.Wake != nil || got.Superseded == nil || len(got.Superseded) != 0 {
			t.Fatalf("retry = %+v, want unchanged and no wake", got)
		}
	})

	t.Run("+1 has no delivery and remove works", func(t *testing.T) {
		env, _, senderSecret, _, secret, sent := setup(t)
		var got hub.ReactResponse
		toolOK(t, env, secret, "vecnet_react", map[string]any{
			"message_id": sent.MessageID, "emoji": "+1",
		}, &got)
		if !got.Changed || got.Op != "add" || got.Emoji != venv.ReactionNames["+1"] || got.Wake != nil || len(got.Reactions) != 1 {
			t.Fatalf("+1 = %+v, want added with no wake", got)
		}
		var inbox inboxResult
		toolOK(t, env, senderSecret, "vecnet_inbox", map[string]any{}, &inbox)
		if ds := inbox.deliveries(); len(ds) != 0 {
			t.Fatalf("+1 created deliveries: %+v", ds)
		}
		toolOK(t, env, secret, "vecnet_react", map[string]any{
			"message_id": sent.MessageID, "emoji": "+1", "op": "remove",
		}, &got)
		if !got.Changed || got.Op != "remove" || got.Wake != nil || got.Reactions == nil || len(got.Reactions) != 0 {
			t.Fatalf("remove = %+v, want removed without wake", got)
		}
	})

	t.Run("explicit priority overrides the client default", func(t *testing.T) {
		env, _, _, _, secret, sent := setup(t)
		var got hub.ReactResponse
		toolOK(t, env, secret, "vecnet_react", map[string]any{
			"message_id": sent.MessageID, "emoji": "check", "op": "add", "priority": "hold",
		}, &got)
		if !got.Changed || got.Wake != nil {
			t.Fatalf("explicit hold = %+v, want changed without wake", got)
		}
	})

	t.Run("reaction messages are not replyable", func(t *testing.T) {
		env, _, senderSecret, _, secret, sent := setup(t)
		var got hub.ReactResponse
		toolOK(t, env, secret, "vecnet_react", map[string]any{
			"message_id": sent.MessageID, "emoji": "check",
		}, &got)
		if got.Wake == nil {
			t.Fatal("check did not create a wake message")
		}
		toolFails(t, env, senderSecret, "vecnet_react", map[string]any{
			"message_id": got.Wake.MessageID, "emoji": "+1",
		}, "not_replyable")
		toolFails(t, env, senderSecret, "vecnet_reply", map[string]any{
			"message_id": got.Wake.MessageID, "text": "reply", "priority": "hold", "idempotency_key": "bad-reply",
		}, "not_replyable")
	})

	t.Run("unknown emoji reaches hub reason code", func(t *testing.T) {
		env, _, _, _, secret, sent := setup(t)
		toolFails(t, env, secret, "vecnet_react", map[string]any{
			"message_id": sent.MessageID, "emoji": "not-an-emoji",
		}, "emoji_not_allowed")
	})
}

func TestReactValidationParity(t *testing.T) {
	env := testhub.Start(t)
	reactor, secret := env.NewPeer("session", "reactor")
	_, senderSecret := env.NewPeer("session", "sender")
	var sent sendResult
	toolOK(t, env, senderSecret, "vecnet_send", map[string]any{
		"idempotency_key": "validation-target", "recipients": []string{reactor},
		"priority": "hold", "content": map[string]any{"text": "react to this"},
	}, &sent)
	writeCounts := func() [8]int {
		t.Helper()
		var counts [8]int
		for i, table := range []string{"reactions", "reaction_ops", "reaction_wakes", "messages", "deliveries", "events", "audit", "counters"} {
			if err := env.Hub.DB.QueryRow("SELECT count(*) FROM " + table).Scan(&counts[i]); err != nil {
				t.Fatal(err)
			}
		}
		return counts
	}
	// The schema's character bound also has a UTF-8 byte cap. Body shape
	// must win over semantic errors and target lookup on both transports.
	for _, tc := range []struct {
		name, emoji string
	}{
		{"33 ASCII bytes", strings.Repeat("x", 33)},
		{"33 UTF-8 bytes", strings.Repeat("✅", 11)},
		{"32 ASCII bytes", strings.Repeat("x", 32)},
		{"32 UTF-8 bytes", strings.Repeat("👍", 8)},
	} {
		for _, mixed := range []bool{false, true} {
			args := map[string]any{"message_id": sent.MessageID, "emoji": tc.emoji, "op": "add", "priority": "hold"}
			name := tc.name + "/valid fields"
			if mixed {
				name = tc.name + "/invalid op and priority and unknown target"
				args["message_id"] = "msg_00000000000000000000000000"
				args["op"], args["priority"] = "bad", "bad"
			}
			for _, transport := range []string{"HTTP", "MCP"} {
				t.Run("emoji length/"+name+"/"+transport, func(t *testing.T) {
					before := writeCounts()
					t.Cleanup(func() {
						if after := writeCounts(); after != before {
							t.Errorf("rejected reaction wrote state: %v -> %v", before, after)
						}
					})
					if transport == "MCP" {
						if len(tc.emoji) <= 32 {
							toolFails(t, env, secret, "vecnet_react", args, "emoji_not_allowed")
						} else if path := invalidPath(t, env, secret, "vecnet_react", args); path != "/emoji" {
							t.Fatalf("MCP rejected at %q, want /emoji", path)
						}
						return
					}
					resp, body := env.Do(secret, http.MethodPost, "/v1/reactions", args)
					var got toolErrorBody
					if err := json.Unmarshal(body, &got); err != nil {
						t.Fatal(err)
					}
					if len(tc.emoji) <= 32 {
						if resp.StatusCode != 422 || got.Error.Code != "emoji_not_allowed" {
							t.Fatalf("HTTP reaction: %d %s, want 422 emoji_not_allowed", resp.StatusCode, body)
						}
					} else if resp.StatusCode != 400 || got.Error.Code != "invalid_body" || got.Error.Details["path"] != "/emoji" {
						t.Fatalf("HTTP reaction: %d %s, want 400 invalid_body at /emoji", resp.StatusCode, body)
					}
				})
			}
		}
	}
	for _, tc := range []struct {
		name, emoji, op, priority, code, field string
	}{
		{"invalid op", "✅", "bad", "hold", "invalid_value", "op"},
		{"invalid priority", "✅", "add", "bad", "invalid_value", "priority"},
		{"emoji before op", "invalid", "bad", "hold", "emoji_not_allowed", ""},
		{"emoji before priority", "invalid", "add", "bad", "emoji_not_allowed", ""},
		{"op before priority", "✅", "bad", "bad", "invalid_value", "op"},
		{"empty op", "✅", "", "hold", "invalid_value", "op"},
		{"empty priority", "✅", "add", "", "invalid_value", "priority"},
		{"empty priority on remove", "✅", "remove", "", "invalid_value", "priority"},
		{"emoji before empty priority", "invalid", "add", "", "emoji_not_allowed", ""},
		{"op before empty priority", "✅", "bad", "", "invalid_value", "op"},
	} {
		for _, target := range []struct{ name, id string }{
			{"valid target", sent.MessageID},
			{"unknown target", "msg_00000000000000000000000000"},
		} {
			t.Run(tc.name+"/"+target.name, func(t *testing.T) {
				before := writeCounts()
				t.Cleanup(func() {
					if after := writeCounts(); after != before {
						t.Errorf("rejected reaction wrote state: counts %v -> %v", before, after)
					}
				})
				// Semantic errors must win over target lookup on both transports
				// and must not write reactions, wakes, events, rates or audit rows.
				args := map[string]any{"message_id": target.id, "emoji": tc.emoji, "op": tc.op, "priority": tc.priority}
				resp, body := env.Do(secret, http.MethodPost, "/v1/reactions", args)
				var httpErr toolErrorBody
				if err := json.Unmarshal(body, &httpErr); err != nil {
					t.Fatal(err)
				}
				var field any
				if tc.field != "" {
					field = tc.field
				}
				if resp.StatusCode != 422 || httpErr.Error.Code != tc.code || httpErr.Error.Details["field"] != field {
					t.Errorf("HTTP reaction: %d %s, want %s field %q", resp.StatusCode, body, tc.code, tc.field)
				}
				mcpErr := toolFails(t, env, secret, "vecnet_react", args, tc.code)
				if mustJSON(t, mcpErr) != mustJSON(t, httpErr) {
					t.Fatalf("MCP error %+v differs from HTTP %+v", mcpErr, httpErr)
				}
			})
		}
	}
	// MCP retains schema guards for message IDs even when emoji semantics fail.
	for _, id := range []string{
		"peer_00000000000000000000000000", "bad", "",
		"msg_0000000000000000000000000", "msg_000000000000000000000000000",
		"msg_0000000000000000000000000I", "msg_0000000000000000000000000a",
	} {
		for _, emoji := range []string{"✅", "invalid"} {
			t.Run("message ID shape/"+id+"/"+emoji, func(t *testing.T) {
				before := writeCounts()
				t.Cleanup(func() {
					if after := writeCounts(); after != before {
						t.Errorf("malformed reaction wrote state: %v -> %v", before, after)
					}
				})
				path := invalidPath(t, env, secret, "vecnet_react", map[string]any{
					"message_id": id, "emoji": emoji, "op": "add", "priority": "hold",
				})
				if path != "/message_id" {
					t.Fatalf("MCP rejected at %q, want /message_id", path)
				}
			})
		}
	}
	for _, tc := range []struct {
		name string
		args any
	}{
		{"missing message", map[string]any{"emoji": "invalid", "op": "bad"}},
		{"invalid message id", map[string]any{"message_id": "bad", "emoji": "invalid", "op": "bad"}},
		{"unknown property", map[string]any{"message_id": "msg_00000000000000000000000000", "emoji": "invalid", "op": "bad", "extra": true}},
		{"non-string priority", map[string]any{"message_id": "msg_00000000000000000000000000", "emoji": "invalid", "priority": 42}},
		{"null op", map[string]any{"message_id": "msg_00000000000000000000000000", "emoji": "invalid", "op": nil}},
		{"non-object", []any{"invalid"}},
	} {
		t.Run("body shape before semantics/"+tc.name, func(t *testing.T) {
			invalidPath(t, env, secret, "vecnet_react", tc.args)
		})
	}
}

func TestEventsSubscribeRefusesReactionType(t *testing.T) {
	env := testhub.Start(t)
	_, secret := env.NewPeer("session", "subscriber")
	cb := testhub.NewCallback(t)
	r := rpc2026(t, env, secret, "events/subscribe", subParams(cb.URL, webhook.NewSecret(), map[string]any{
		"arguments": map[string]any{"types": []string{"reaction"}},
	}))
	if r.Error == nil || r.Error.Code != -32602 {
		t.Fatalf("reaction subscription: %+v / %s, want -32602", r.Error, r.Result)
	}
	if n := cb.Requests(); n != 0 {
		t.Fatalf("invalid subscription contacted callback %d times, want 0", n)
	}
}

func TestSendToolRefusesReactionKind(t *testing.T) {
	env := testhub.Start(t)
	_, secret := env.NewPeer("session", "sender")
	recipient, _ := env.NewPeer("session", "recipient")
	if path := invalidPath(t, env, secret, "vecnet_send", map[string]any{
		"idempotency_key": "forged-reaction", "recipients": []string{recipient},
		"kind": "reaction", "priority": "hold", "content": map[string]any{"text": "forged"},
	}); path != "/kind" {
		t.Fatalf("reaction kind rejected at %q, want /kind", path)
	}
}

func TestGroupToolsMatchHTTP(t *testing.T) {
	env := testhub.Start(t)
	a, aSecret := env.NewPeer("session", "creator")
	b, bSecret := env.NewPeer("session", "first member")
	c, cSecret := env.NewPeer("session", "second member")
	d, dSecret := env.NewPeer("session", "third member")

	var created map[string]string
	toolOK(t, env, aSecret, "vecnet_group_create", map[string]any{"members": []string{b, c}}, &created)
	group := created["group_id"]
	if len(created) != 1 || !strings.HasPrefix(group, "grp_") {
		t.Fatalf("create = %v, want exactly {group_id: grp_...}", created)
	}

	checkGroups := func(wantMembers []string) {
		t.Helper()
		var listed, fromHTTP struct {
			Groups []hub.GroupInfo `json:"groups"`
		}
		toolOK(t, env, aSecret, "vecnet_groups", map[string]any{}, &listed)
		resp, body := env.Do(aSecret, http.MethodGet, "/v1/groups", nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET /v1/groups = %d: %s", resp.StatusCode, body)
		}
		if err := json.Unmarshal(body, &fromHTTP); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(listed, fromHTTP) {
			t.Fatalf("tool groups = %+v, HTTP groups = %+v", listed, fromHTTP)
		}
		slices.Sort(wantMembers)
		if len(listed.Groups) != 1 {
			t.Fatalf("groups = %+v, want one group", listed.Groups)
		}
		got := listed.Groups[0]
		if got.GroupID != group || got.CreatedBy != a || got.Archived || !slices.Equal(got.Members, wantMembers) {
			t.Fatalf("group = %+v, want %s created by %s with members %v, unarchived", got, group, a, wantMembers)
		}
	}
	// The hub includes the creator automatically; two explicit members are
	// the first two recipients, and adding a third yields three deliveries.
	checkGroups([]string{a, b, c})
	var added map[string]any
	toolOK(t, env, bSecret, "vecnet_group_add", map[string]any{"group_id": group, "members": []string{d}}, &added)
	if added == nil || len(added) != 0 {
		t.Fatalf("add result = %v, want {}", added)
	}
	checkGroups([]string{a, b, c, d})

	var sent sendResult
	toolOK(t, env, aSecret, "vecnet_send", map[string]any{
		"idempotency_key": "group-tool-send", "recipients": []string{group}, "priority": "hold",
		"content": map[string]any{"text": "hello group"},
	}, &sent)
	if !strings.HasPrefix(sent.MessageID, "msg_") {
		t.Fatalf("send = %+v, want message_id", sent)
	}
	var recipients []string
	for _, delivery := range sent.Deliveries {
		recipients = append(recipients, delivery.Recipient)
	}
	slices.Sort(recipients)
	wantRecipients := []string{b, c, d}
	slices.Sort(wantRecipients)
	if !slices.Equal(recipients, wantRecipients) {
		t.Fatalf("recipients = %v, want %v", recipients, wantRecipients)
	}
	for _, member := range []struct{ id, secret string }{{b, bSecret}, {c, cSecret}, {d, dSecret}} {
		var inbox inboxResult
		toolOK(t, env, member.secret, "vecnet_inbox", map[string]any{}, &inbox)
		deliveries := inbox.deliveries()
		if len(deliveries) != 1 || deliveries[0].Data.Recipient != member.id || deliveries[0].Data.Message.MessageID != sent.MessageID {
			t.Fatalf("%s inbox deliveries = %+v, want message %s", member.id, deliveries, sent.MessageID)
		}
	}
}

func TestGroupCreateRepeatedCallsCreateDistinctGroups(t *testing.T) {
	env := testhub.Start(t)
	creator, secret := env.NewPeer("session", "creator")
	member, _ := env.NewPeer("session", "member")
	args := map[string]any{"members": []string{member}}
	var ids []string
	for range 2 {
		var created struct {
			GroupID string `json:"group_id"`
		}
		toolOK(t, env, secret, "vecnet_group_create", args, &created)
		if !strings.HasPrefix(created.GroupID, "grp_") || slices.Contains(ids, created.GroupID) {
			t.Fatalf("repeated create returned %q after %v, want a fresh group id", created.GroupID, ids)
		}
		ids = append(ids, created.GroupID)
	}
	var listed struct {
		Groups []hub.GroupInfo `json:"groups"`
	}
	toolOK(t, env, secret, "vecnet_groups", map[string]any{}, &listed)
	if len(listed.Groups) != len(ids) {
		t.Fatalf("groups = %+v, want two persistent groups", listed.Groups)
	}
	wantMembers := []string{creator, member}
	slices.Sort(wantMembers)
	var listedIDs []string
	for _, group := range listed.Groups {
		listedIDs = append(listedIDs, group.GroupID)
		if group.CreatedBy != creator || !slices.Equal(group.Members, wantMembers) {
			t.Fatalf("group = %+v, want creator %s and members %v", group, creator, wantMembers)
		}
	}
	slices.Sort(ids)
	slices.Sort(listedIDs)
	if !slices.Equal(listedIDs, ids) {
		t.Fatalf("listed group ids = %v, want %v", listedIDs, ids)
	}
}

func TestGroupToolsRejectNonParticipant(t *testing.T) {
	env := testhub.Start(t)
	_, aSecret := env.NewPeer("session", "creator")
	b, _ := env.NewPeer("session", "member")
	outsider, outsiderSecret := env.NewPeer("session", "outsider")
	resp, body := env.Do(aSecret, http.MethodPost, "/v1/groups", map[string]any{"members": []string{b}})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /v1/groups = %d: %s", resp.StatusCode, body)
	}
	var created struct {
		GroupID string `json:"group_id"`
	}
	if err := json.Unmarshal(body, &created); err != nil || created.GroupID == "" {
		t.Fatalf("create body = %s: %v", body, err)
	}
	members := map[string]any{"members": []string{outsider}}
	resp, body = env.Do(outsiderSecret, http.MethodPost, "/v1/groups/"+created.GroupID+"/members", members)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("HTTP add non-participant = %d: %s", resp.StatusCode, body)
	}
	var fromHTTP toolErrorBody
	if err := json.Unmarshal(body, &fromHTTP); err != nil || fromHTTP.Error.Code != "not_member" {
		t.Fatalf("HTTP error = %s, want not_member: %v", body, err)
	}
	fromTool := toolFails(t, env, outsiderSecret, "vecnet_group_add", map[string]any{
		"group_id": created.GroupID, "members": []string{outsider},
	}, fromHTTP.Error.Code)
	if !reflect.DeepEqual(fromTool, fromHTTP) {
		t.Fatalf("tool error = %+v, HTTP error = %+v", fromTool, fromHTTP)
	}
	var listed struct {
		Groups []hub.GroupInfo `json:"groups"`
	}
	toolOK(t, env, outsiderSecret, "vecnet_groups", map[string]any{}, &listed)
	if listed.Groups == nil || len(listed.Groups) != 0 {
		t.Fatalf("outsider groups = %+v, want []", listed.Groups)
	}
}

func TestPresenceSubscribeTool(t *testing.T) {
	env := testhub.Start(t)
	peer, secret := env.NewPeer("session", "subscriber")
	stored := func() string {
		t.Helper()
		var value string
		if err := env.Hub.DB.QueryRow(`SELECT subscribe_presence FROM peers WHERE peer_id = ?`, peer).Scan(&value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	for _, value := range []string{"none", "all", "teams"} {
		t.Run(value, func(t *testing.T) {
			var result map[string]any
			toolOK(t, env, secret, "vecnet_presence_subscribe", map[string]any{"subscribe": value}, &result)
			if result == nil || len(result) != 0 {
				t.Fatalf("subscribe result = %v, want {}", result)
			}
			fromTool := stored()
			if fromTool != value {
				t.Fatalf("stored subscription = %q, want %q", fromTool, value)
			}
			// Change it before PATCH so the HTTP path must persist a change too.
			reset := "none"
			if value == reset {
				reset = "all"
			}
			toolOK(t, env, secret, "vecnet_presence_subscribe", map[string]any{"subscribe": reset}, nil)
			resp, body := env.Do(secret, http.MethodPatch, "/v1/peers/me", map[string]any{"subscribe_presence": value})
			if resp.StatusCode != http.StatusNoContent {
				t.Fatalf("PATCH /v1/peers/me = %d: %s", resp.StatusCode, body)
			}
			if fromHTTP := stored(); fromHTTP != fromTool {
				t.Fatalf("tool stored %q, HTTP stored %q", fromTool, fromHTTP)
			}
		})
	}
	before := stored()
	if path := invalidPath(t, env, secret, "vecnet_presence_subscribe", map[string]any{"subscribe": "bogus"}); path != "/subscribe" {
		t.Fatalf("invalid subscription path = %q, want /subscribe", path)
	}
	if after := stored(); after != before {
		t.Fatalf("invalid subscription changed stored state from %q to %q", before, after)
	}
	_, principalSecret := env.NewPrincipal("no own peer")
	toolFails(t, env, principalSecret, "vecnet_presence_subscribe", map[string]any{"subscribe": "none"}, "forbidden")
}

// peerToolPrincipal enables the same provisioning policy used by HTTP clients.
func peerToolPrincipal(t *testing.T, env *testhub.Env, max int) (string, string) {
	t.Helper()
	principal, secret := env.NewPrincipal("provisioning host")
	if err := env.Hub.SetPrincipalLimits(context.Background(), principal, hub.PrincipalLimits{
		MaxPeers: max, CreatesPerHour: 60, Kinds: []string{"pagchat-bot"},
	}); err != nil {
		t.Fatal(err)
	}
	return principal, secret
}

func peerToolHTTP(t *testing.T, env *testhub.Env, secret, method, path string, args any, status int) map[string]any {
	t.Helper()
	resp, body := env.Do(secret, method, path, args)
	if resp.StatusCode != status {
		t.Fatalf("%s %s = %d: %s, want %d", method, path, resp.StatusCode, body, status)
	}
	var result map[string]any
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatalf("%s %s body = %s: %v", method, path, body, err)
	}
	return result
}

func peerToolErrorLikeHTTP(t *testing.T, env *testhub.Env, secret, tool string, args any, method, path string, body any, status int, code string) toolErrorBody {
	t.Helper()
	fromHTTP := peerToolHTTP(t, env, secret, method, path, body, status)
	fromTool := toolFails(t, env, secret, tool, args, code)
	var got map[string]any
	if err := json.Unmarshal([]byte(mustJSON(t, fromTool)), &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, fromHTTP) {
		t.Fatalf("%s error = %v, HTTP error = %v", tool, got, fromHTTP)
	}
	return fromTool
}

func TestPeerCreateToolPrincipalOnly(t *testing.T) {
	env := testhub.Start(t)
	principal, secret := peerToolPrincipal(t, env, 2)
	_, peerSecret := env.NewPeer("session", "peer credential")
	args := map[string]any{"kind": "pagchat-bot", "name": "seed-bot-2", "idempotency_key": "create-seed-bot-2"}
	for _, tc := range []struct{ name, secret string }{
		{"peer", peerSecret}, {"operator", env.OperatorSecret}, {"principal", secret},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status, r := rpc(t, env, tc.secret, "tools/list", nil)
			if status != http.StatusOK || r.Error != nil {
				t.Fatalf("tools/list: %d %+v", status, r.Error)
			}
			var listed struct {
				Tools []listedTool `json:"tools"`
			}
			if err := json.Unmarshal(r.Result, &listed); err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{"vecnet_peer_create", "vecnet_peer_set_disabled"} {
				if !slices.ContainsFunc(listed.Tools, func(tool listedTool) bool { return tool.Name == name }) {
					t.Errorf("%s tools/list lacks %s", tc.name, name)
				}
			}
			if tc.name != "principal" {
				peerToolErrorLikeHTTP(t, env, tc.secret, "vecnet_peer_create", args,
					http.MethodPost, "/v1/peers", args, http.StatusForbidden, "forbidden")
				return
			}
			var created map[string]any
			toolOK(t, env, secret, "vecnet_peer_create", args, &created)
			peer, ok := created["peer"].(map[string]any)
			if !ok {
				t.Fatalf("create = %v, want {peer, idempotency_key}", created)
			}
			id, _ := peer["peer_id"].(string)
			if !strings.HasPrefix(id, "peer_") {
				t.Fatalf("peer_id = %q, want peer_...", id)
			}
			want := map[string]any{
				"peer":            map[string]any{"peer_id": id, "kind": "pagchat-bot", "name": "seed-bot-2", "created_by": principal, "disabled": false},
				"idempotency_key": "create-seed-bot-2",
			}
			if !reflect.DeepEqual(created, want) {
				t.Fatalf("create = %v, want exactly %v", created, want)
			}
			fromHTTP := peerToolHTTP(t, env, secret, http.MethodPost, "/v1/peers", args, http.StatusOK)
			if !reflect.DeepEqual(created, fromHTTP) {
				t.Fatalf("tool create = %v, HTTP replay = %v", created, fromHTTP)
			}
		})
	}
}

func TestPeerCreateToolIdempotentLikeHTTP(t *testing.T) {
	env := testhub.Start(t)
	_, secret := peerToolPrincipal(t, env, 1)
	args := map[string]any{"kind": "pagchat-bot", "name": "first bot", "idempotency_key": "first-bot"}
	var first, replay map[string]any
	toolOK(t, env, secret, "vecnet_peer_create", args, &first)
	toolOK(t, env, secret, "vecnet_peer_create", args, &replay)
	if !reflect.DeepEqual(first, replay) {
		t.Fatalf("replay = %v, first = %v", replay, first)
	}
	peer, ok := first["peer"].(map[string]any)
	if !ok {
		t.Fatalf("create = %v, want peer", first)
	}
	id, _ := peer["peer_id"].(string)
	if !strings.HasPrefix(id, "peer_") {
		t.Fatalf("peer_id = %q, want peer_...", id)
	}
	fromHTTP := peerToolHTTP(t, env, secret, http.MethodPost, "/v1/peers", args, http.StatusOK)
	if !reflect.DeepEqual(first, fromHTTP) {
		t.Fatalf("tool create = %v, HTTP replay = %v", first, fromHTTP)
	}
	conflict := map[string]any{"kind": "pagchat-bot", "name": "different bot", "idempotency_key": "first-bot"}
	err := peerToolErrorLikeHTTP(t, env, secret, "vecnet_peer_create", conflict,
		http.MethodPost, "/v1/peers", conflict, http.StatusConflict, "idempotency_conflict")
	if !reflect.DeepEqual(err.Error.Details, map[string]any{"idempotency_key": "first-bot"}) {
		t.Fatalf("conflict details = %v", err.Error.Details)
	}
	atCap := map[string]any{"kind": "pagchat-bot", "name": "over cap", "idempotency_key": "over-cap"}
	checkCap := func(disabled float64) {
		t.Helper()
		err := peerToolErrorLikeHTTP(t, env, secret, "vecnet_peer_create", atCap,
			http.MethodPost, "/v1/peers", atCap, http.StatusTooManyRequests, "peer_cap_reached")
		want := map[string]any{"limit": float64(1), "active": float64(1), "disabled": disabled}
		if !reflect.DeepEqual(err.Error.Details, want) {
			t.Fatalf("cap details = %v, want exactly %v", err.Error.Details, want)
		}
	}
	checkCap(0)
	peerToolHTTP(t, env, secret, http.MethodPatch, "/v1/peers/"+id, map[string]any{"disabled": true}, http.StatusOK)
	replacement := map[string]any{"kind": "pagchat-bot", "name": "replacement", "idempotency_key": "replacement"}
	peerToolHTTP(t, env, secret, http.MethodPost, "/v1/peers", replacement, http.StatusCreated)
	checkCap(1)
	// Replay wins over both the active-peer cap and the peer's current disabled state.
	toolOK(t, env, secret, "vecnet_peer_create", args, &replay)
	if !reflect.DeepEqual(first, replay) {
		t.Fatalf("replay at cap = %v, original frozen body = %v", replay, first)
	}
	err = peerToolErrorLikeHTTP(t, env, secret, "vecnet_peer_set_disabled", map[string]any{"peer_id": id, "disabled": false},
		http.MethodPatch, "/v1/peers/"+id, map[string]any{"disabled": false}, http.StatusTooManyRequests, "peer_cap_reached")
	if !reflect.DeepEqual(err.Error.Details, map[string]any{"limit": float64(1), "active": float64(1), "disabled": float64(1)}) {
		t.Fatalf("re-enable cap details = %v", err.Error.Details)
	}
}

func TestPeerSetDisabledTool(t *testing.T) {
	env := testhub.Start(t)
	principal, secret := peerToolPrincipal(t, env, 2)
	_, otherSecret := peerToolPrincipal(t, env, 2)
	args := map[string]any{"kind": "pagchat-bot", "name": "own bot", "idempotency_key": "own-bot"}
	created := peerToolHTTP(t, env, secret, http.MethodPost, "/v1/peers", args, http.StatusCreated)
	id := created["peer"].(map[string]any)["peer_id"].(string)
	other := peerToolHTTP(t, env, otherSecret, http.MethodPost, "/v1/peers", args, http.StatusCreated)
	otherID := other["peer"].(map[string]any)["peer_id"].(string)
	target, peerSecret := env.NewPeer("session", "recipient")
	for _, tc := range []struct{ name, secret string }{{"peer", peerSecret}, {"operator", env.OperatorSecret}} {
		t.Run(tc.name, func(t *testing.T) {
			peerToolErrorLikeHTTP(t, env, tc.secret, "vecnet_peer_set_disabled", map[string]any{"peer_id": id, "disabled": true},
				http.MethodPatch, "/v1/peers/"+id, map[string]any{"disabled": true}, http.StatusForbidden, "forbidden")
		})
	}
	for _, inaccessible := range []string{otherID, target, "peer_missing"} {
		t.Run(inaccessible, func(t *testing.T) {
			peerToolErrorLikeHTTP(t, env, secret, "vecnet_peer_set_disabled", map[string]any{"peer_id": inaccessible, "disabled": true},
				http.MethodPatch, "/v1/peers/"+inaccessible, map[string]any{"disabled": true}, http.StatusNotFound, "not_found")
		})
	}
	send := map[string]any{
		"as": id, "recipients": []string{target}, "priority": "hold", "idempotency_key": "before-disable",
		"content": map[string]any{"text": "from provisioned peer"},
	}
	toolOK(t, env, secret, "vecnet_send", send, nil)
	for i, disabled := range []bool{true, true, false, false} {
		var result map[string]any
		toolOK(t, env, secret, "vecnet_peer_set_disabled", map[string]any{"peer_id": id, "disabled": disabled}, &result)
		want := map[string]any{"peer": map[string]any{
			"peer_id": id, "kind": "pagchat-bot", "name": "own bot", "created_by": principal, "disabled": disabled,
		}}
		if !reflect.DeepEqual(result, want) {
			t.Fatalf("set disabled = %v, want exactly %v", result, want)
		}
		// The tool's own effect, before any HTTP request that could supply
		// it: the stored flag, then a send under a fresh key.
		var stored bool
		if err := env.Hub.DB.QueryRow(`SELECT disabled != 0 FROM peers WHERE peer_id = ?`, id).Scan(&stored); err != nil || stored != disabled {
			t.Fatalf("call %d: stored disabled = %v (%v), want %v", i, stored, err, disabled)
		}
		send["idempotency_key"] = fmt.Sprintf("after-set-%d", i)
		if disabled {
			toolFails(t, env, secret, "vecnet_send", send, "forbidden")
			peerToolErrorLikeHTTP(t, env, secret, "vecnet_send", send,
				http.MethodPost, "/v1/messages", send, http.StatusForbidden, "forbidden")
		} else {
			toolOK(t, env, secret, "vecnet_send", send, nil)
		}
		// Only now compare with the HTTP endpoint, which sets the same value.
		fromHTTP := peerToolHTTP(t, env, secret, http.MethodPatch, "/v1/peers/"+id, map[string]any{"disabled": disabled}, http.StatusOK)
		if !reflect.DeepEqual(result, fromHTTP) {
			t.Fatalf("tool set disabled = %v, HTTP = %v", result, fromHTTP)
		}
	}
}

func TestBlobPutGetRoundTrip(t *testing.T) {
	env := testhub.Start(t)
	_, sender := env.NewPeer("session", "sender")
	recipient, reader := env.NewPeer("session", "recipient")
	data := make([]byte, 150<<10)
	if _, err := rand.Read(data); err != nil {
		t.Fatal(err)
	}
	var put struct {
		SHA256 string `json:"sha256"`
		Size   int    `json:"size"`
	}
	toolOK(t, env, sender, "vecnet_blob_put", map[string]any{
		"data_base64": base64.StdEncoding.EncodeToString(data), "media_type": "application/octet-stream", "ttl_seconds": 3600,
	}, &put)
	if want := fmt.Sprintf("%x", sha256.Sum256(data)); put.SHA256 != want || put.Size != len(data) {
		t.Fatalf("put = %+v, want sha256 %s, size %d", put, want, len(data))
	}
	toolOK(t, env, sender, "vecnet_send", map[string]any{
		"idempotency_key": "blob-round-trip", "recipients": []string{recipient}, "priority": "hold",
		"content": map[string]any{"text": "attached"}, "attachments": []any{map[string]any{
			"sha256": put.SHA256, "size": put.Size, "media_type": "application/octet-stream",
		}},
	}, nil)
	type blobPage struct {
		SHA256     string `json:"sha256"`
		MediaType  string `json:"media_type"`
		Size       int    `json:"size"`
		Offset     int    `json:"offset"`
		NextOffset *int   `json:"next_offset"`
		Encoding   string `json:"encoding"`
		Data       string `json:"data"`
	}
	var combined []byte
	for i, offset := range []int{0, 128 << 10} {
		args := map[string]any{"sha256": put.SHA256}
		if i > 0 {
			args["offset"] = offset
			args["limit"] = 128 << 10
		}
		var page blobPage
		toolOK(t, env, reader, "vecnet_blob_get", args, &page)
		if page.SHA256 != put.SHA256 || page.MediaType != "application/octet-stream" || page.Size != len(data) || page.Offset != offset || page.Encoding != "base64" {
			t.Fatalf("page metadata = sha %s type %s size %d offset %d encoding %s", page.SHA256, page.MediaType, page.Size, page.Offset, page.Encoding)
		}
		decoded, err := base64.StdEncoding.DecodeString(page.Data)
		if err != nil {
			t.Fatal(err)
		}
		wantSize := 128 << 10
		if i == 1 {
			wantSize = 22 << 10
		}
		if len(decoded) != wantSize {
			t.Fatalf("page %d: %d bytes, want %d", i, len(decoded), wantSize)
		}
		if i == 0 && (page.NextOffset == nil || *page.NextOffset != 128<<10) || i == 1 && page.NextOffset != nil {
			t.Fatalf("page %d next_offset = %v", i, page.NextOffset)
		}
		combined = append(combined, decoded...)
	}
	if !bytes.Equal(combined, data) {
		t.Fatal("paginated blob differs from uploaded bytes")
	}
	var small blobPage
	res := verbatimResult(t, env, reader, "vecnet_blob_get", fmt.Sprintf(`{"sha256":%q,"offset":5.0,"limit":1e1}`, put.SHA256))
	if res.IsError || json.Unmarshal(res.StructuredContent, &small) != nil || small.Offset != 5 || small.Data != base64.StdEncoding.EncodeToString(data[5:15]) || small.NextOffset == nil || *small.NextOffset != 15 {
		t.Fatalf("explicit short page = %s", res.StructuredContent)
	}
	for _, offset := range []int{len(data), len(data) + 1} {
		var end map[string]any
		toolOK(t, env, reader, "vecnet_blob_get", map[string]any{"sha256": put.SHA256, "offset": offset}, &end)
		if next, exists := end["next_offset"]; !exists || next != nil || end["data"] != "" || end["offset"] != float64(offset) {
			t.Fatalf("EOF page = %v", end)
		}
	}
	for _, tc := range []struct {
		field string
		value any
	}{
		{"offset", -1}, {"offset", 0.5}, {"limit", 0}, {"limit", (128 << 10) + 1}, {"limit", 1.5},
	} {
		if path := invalidPath(t, env, reader, "vecnet_blob_get", map[string]any{"sha256": put.SHA256, tc.field: tc.value}); path != "/"+tc.field {
			t.Fatalf("%s=%v: path = %q", tc.field, tc.value, path)
		}
	}
}

func TestBlobPutCap(t *testing.T) {
	env := testhub.Start(t)
	_, secret := env.NewPeer("session", "uploader")
	data := bytes.Repeat([]byte{'x'}, (160<<10)+1)
	// From cap+3 the base64 is past maxLength, and a request of 190 KiB
	// still fits the 256 KiB body: each is payload_too_large, not -32602.
	for _, n := range []int{160<<10 + 1, 160<<10 + 3, 190 << 10} {
		over := bytes.Repeat([]byte{'x'}, n)
		e := toolFails(t, env, secret, "vecnet_blob_put", map[string]any{
			"data_base64": base64.StdEncoding.EncodeToString(over), "media_type": "text/plain",
		}, "payload_too_large")
		if e.Error.Details["limit"] != float64(163840) {
			t.Fatalf("%d bytes: limit = %v", n, e.Error.Details["limit"])
		}
	}
	sha := fmt.Sprintf("%x", sha256.Sum256(data))
	resp, body := env.Do(env.OperatorSecret, "GET", "/v1/blobs/"+sha, nil)
	var httpErr toolErrorBody
	if resp.StatusCode != http.StatusNotFound || json.Unmarshal(body, &httpErr) != nil || httpErr.Error.Code != "unknown_blob" {
		t.Fatalf("rejected blob GET: %d %s", resp.StatusCode, body)
	}
	toolOK(t, env, secret, "vecnet_blob_put", map[string]any{
		"data_base64": base64.StdEncoding.EncodeToString(data[:160<<10]), "media_type": "text/plain",
	}, nil)
	if path := invalidPath(t, env, secret, "vecnet_blob_put", map[string]any{"data_base64": "%%%", "media_type": "text/plain"}); path != "/data_base64" {
		t.Fatalf("invalid base64 path = %q", path)
	}
	for _, encoded := range []string{"aGk=", "aGk", " aG\tk=\r\n"} {
		var put map[string]any
		toolOK(t, env, secret, "vecnet_blob_put", map[string]any{"data_base64": encoded, "media_type": "text/plain"}, &put)
		if put["sha256"] != fmt.Sprintf("%x", sha256.Sum256([]byte("hi"))) || put["size"] != float64(2) {
			t.Fatalf("base64 %q: %v", encoded, put)
		}
	}
	env.Hub.Blobs.DailyQuota = 1
	toolFails(t, env, secret, "vecnet_blob_put", map[string]any{"data_base64": "bmV3", "media_type": "text/plain"}, "upload_quota_exceeded")
}

func TestBlobGetParticipantOnly(t *testing.T) {
	env := testhub.Start(t)
	_, sender := env.NewPeer("session", "sender")
	recipient, reader := env.NewPeer("session", "recipient")
	_, outsider := env.NewPeer("session", "outsider")
	var put map[string]any
	toolOK(t, env, sender, "vecnet_blob_put", map[string]any{"data_base64": "aGk=", "media_type": "text/plain"}, &put)
	sha := put["sha256"].(string)
	// Uploading alone does not grant bearer read access.
	toolFails(t, env, sender, "vecnet_blob_get", map[string]any{"sha256": sha}, "forbidden")
	toolOK(t, env, sender, "vecnet_send", map[string]any{
		"idempotency_key": "participant-blob", "recipients": []string{recipient}, "priority": "hold",
		"content": map[string]any{"text": "attached"}, "attachments": []any{map[string]any{
			"sha256": sha, "size": 2, "media_type": "text/plain",
		}},
	}, nil)
	toolOK(t, env, reader, "vecnet_blob_get", map[string]any{"sha256": sha}, nil)
	resp, body := env.Do(outsider, "GET", "/v1/blobs/"+sha, nil)
	var httpErr toolErrorBody
	if resp.StatusCode != http.StatusForbidden || json.Unmarshal(body, &httpErr) != nil || httpErr.Error.Code != "forbidden" {
		t.Fatalf("outsider HTTP GET: %d %s", resp.StatusCode, body)
	}
	toolFails(t, env, outsider, "vecnet_blob_get", map[string]any{"sha256": sha}, httpErr.Error.Code)
	res := callTool(t, env, outsider, "vecnet_blob_get", map[string]any{"sha256": sha})
	var result map[string]any
	if json.Unmarshal(res.StructuredContent, &result) != nil {
		t.Fatal("invalid error result")
	}
	var walk func(any)
	walk = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			for k, child := range x {
				if k == "data" {
					t.Fatal("refused blob read leaked data")
				}
				walk(child)
			}
		case []any:
			for _, child := range x {
				walk(child)
			}
		}
	}
	walk(result)
	// Malformed hashes retain the HTTP endpoint's hub-level refusal.
	toolFails(t, env, outsider, "vecnet_blob_get", map[string]any{"sha256": "bad"}, "unknown_blob")
}

func TestBlobToolsTakeNoURL(t *testing.T) {
	env := testhub.Start(t)
	_, secret := env.NewPeer("session", "uploader")
	status, r := rpc(t, env, secret, "tools/list", nil)
	var listed struct {
		Tools []listedTool `json:"tools"`
	}
	if status != http.StatusOK || r.Error != nil || json.Unmarshal(r.Result, &listed) != nil {
		t.Fatalf("tools/list: %d %s", status, r.Result)
	}
	for _, name := range []string{"vecnet_blob_put", "vecnet_blob_get", "vecnet_send"} {
		i := slices.IndexFunc(listed.Tools, func(x listedTool) bool { return x.Name == name })
		if i < 0 {
			t.Fatalf("tools/list missing %s", name)
		}
		tool := listed.Tools[i]
		if tool.InputSchema["additionalProperties"] != false {
			t.Fatalf("%s input is not closed", name)
		}
		names := map[string]bool{}
		propertyNames(tool.InputSchema, names)
		if name != "vecnet_send" {
			if tool.OutputSchema["additionalProperties"] != false {
				t.Fatalf("%s output is not closed", name)
			}
			propertyNames(tool.OutputSchema, names)
		}
		for key := range names {
			if strings.Contains(strings.ToLower(key), "url") {
				t.Errorf("%s schema exposes %s", name, key)
			}
		}
		if name == "vecnet_blob_put" {
			props := tool.InputSchema["properties"].(map[string]any)
			if props["data_base64"].(map[string]any)["maxLength"] != float64(218456) {
				t.Fatal("blob base64 schema cap differs from avatar cap")
			}
		}
	}
	var put map[string]any
	toolOK(t, env, secret, "vecnet_blob_put", map[string]any{"data_base64": "aGk=", "media_type": "text/plain"}, &put)
	var get map[string]any
	toolOK(t, env, env.OperatorSecret, "vecnet_blob_get", map[string]any{"sha256": put["sha256"]}, &get)
	for name, result := range map[string]map[string]any{"vecnet_blob_put": put, "vecnet_blob_get": get} {
		want := []string{"sha256", "size"}
		if name == "vecnet_blob_get" {
			want = []string{"sha256", "media_type", "size", "offset", "next_offset", "encoding", "data"}
		}
		if len(result) != len(want) {
			t.Fatalf("%s output keys = %v", name, result)
		}
		for _, key := range want {
			if _, ok := result[key]; !ok {
				t.Fatalf("%s output missing %s", name, key)
			}
		}
		for _, key := range []string{"url", "origin_url", "blob_url", "callback_url", "bearer", "extra"} {
			args := map[string]any{"sha256": put["sha256"]}
			if name == "vecnet_blob_put" {
				args = map[string]any{"data_base64": "aGk=", "media_type": "text/plain"}
			}
			args[key] = "https://example.invalid/?t=secret"
			if path := invalidPath(t, env, secret, name, args); path != "/"+key {
				t.Fatalf("%s accepts %s: path %q", name, key, path)
			}
		}
	}
	for _, key := range []string{"origin_url", "blob_url"} {
		if path := invalidPath(t, env, secret, "vecnet_send", map[string]any{
			"idempotency_key": "reject-url-" + key, "recipients": []string{env.OperatorPeer}, "priority": "hold", "content": map[string]any{"text": "attached"},
			"attachments": []any{map[string]any{"sha256": put["sha256"], "size": 2, "media_type": "text/plain", key: "https://example.invalid"}},
		}); path != "/attachments/0/"+key {
			t.Fatalf("send accepts attachment %s: %q", key, path)
		}
	}
}

func TestWhoamiStableID(t *testing.T) {
	env := testhub.Start(t)
	peer, secret := env.NewPeer("session", "Before rename")
	var first, second, renamed map[string]string
	toolOK(t, env, secret, "vecnet_whoami", map[string]any{}, &first)
	rotated, err := env.Hub.RotatePeer(context.Background(), peer)
	if err != nil {
		t.Fatal(err)
	}
	if rotated == secret {
		t.Fatal("rotation did not mint a second credential")
	}
	toolOK(t, env, rotated, "vecnet_whoami", map[string]any{}, &second)
	// No rename API exists; change the directory fixture, then observe whoami.
	if _, err := env.Hub.DB.Exec(`UPDATE peers SET name = ? WHERE peer_id = ?`, "After rename", peer); err != nil {
		t.Fatal(err)
	}
	toolOK(t, env, rotated, "vecnet_whoami", map[string]any{}, &renamed)
	for _, got := range []map[string]string{first, second, renamed} {
		if got["id"] != peer {
			t.Errorf("profile id = %q, want stable peer %s", got["id"], peer)
		}
	}
	if first["name"] != "Before rename" || renamed["name"] != "After rename" {
		t.Fatalf("rename not visible: %v -> %v", first, renamed)
	}
	other, otherSecret := env.NewPeer("session", "Different peer")
	var distinct map[string]string
	toolOK(t, env, otherSecret, "vecnet_whoami", map[string]any{}, &distinct)
	if distinct["id"] != other || distinct["id"] == renamed["id"] {
		t.Fatalf("distinct peer ids: %v and %v", renamed, distinct)
	}
}

func TestWhoamiOperatorFallback(t *testing.T) {
	env := testhub.Start(t)
	front := eventsFront(t, env, diagAuth{mcpserver.Principal{Caller: auth.Caller{Kind: "operator"}}}, nil)
	var got map[string]string
	toolOK(t, front, "operator-test-token", "vecnet_whoami", map[string]any{}, &got)
	want := map[string]string{"kind": "operator", "id": "operator"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("operator without peer = %v, want %v", got, want)
	}
}
