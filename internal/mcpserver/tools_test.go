package mcpserver_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	venv "github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/httpapi"
	"github.com/XertroV/vecnet/internal/hub"
	"github.com/XertroV/vecnet/internal/mcpserver"
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
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
	Annotations *struct {
		ReadOnlyHint bool `json:"readOnlyHint"`
	} `json:"annotations"`
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
		"vecnet_ack", "vecnet_inbox", "vecnet_peers", "vecnet_presence", "vecnet_react", "vecnet_receipt",
		"vecnet_reply", "vecnet_send", "vecnet_thread", "vecnet_threads",
	}
	var got []string
	readOnly := map[string]bool{"vecnet_inbox": true, "vecnet_threads": true, "vecnet_thread": true, "vecnet_peers": true}
	for _, tool := range tools {
		got = append(got, tool.Name)
		if tool.Annotations == nil {
			t.Fatalf("%s: no annotations", tool.Name)
		}
		if tool.Annotations.ReadOnlyHint != readOnly[tool.Name] {
			t.Errorf("%s: readOnlyHint = %v, want %v", tool.Name, tool.Annotations.ReadOnlyHint, readOnly[tool.Name])
		}
		if tool.InputSchema["type"] != "object" {
			t.Errorf("%s: inputSchema.type = %v", tool.Name, tool.InputSchema["type"])
		}
		if tool.Description == "" {
			t.Errorf("%s: no description", tool.Name)
		}
		if !readOnly[tool.Name] && !strings.Contains(tool.Description, "operator's bus") {
			t.Errorf("%s: a write tool's description must say it writes to the operator's bus: %q", tool.Name, tool.Description)
		}
		if tool.Name == "vecnet_react" {
			props := tool.InputSchema["properties"].(map[string]any)
			if got := props["op"].(map[string]any)["enum"]; mustJSON(t, got) != `["add","remove"]` {
				t.Errorf("reaction op enum = %v", got)
			}
			defs := tool.InputSchema["$defs"].(map[string]any)
			if got := defs["priority"].(map[string]any)["enum"]; mustJSON(t, got) != `["wake","wake_idle","hold"]` {
				t.Errorf("reaction priority enum = %v", got)
			}
		}
		names := map[string]bool{}
		propertyNames(tool.InputSchema, names)
		for name := range names {
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

	// vecnet_send's schema is $defs/sendRequest, with its $refs resolvable
	// inside the tool's own document.
	i := slices.IndexFunc(tools, func(x listedTool) bool { return x.Name == "vecnet_send" })
	send := tools[i].InputSchema
	req, _ := send["required"].([]any)
	for _, k := range []string{"idempotency_key", "priority", "content"} {
		if !slices.Contains(req, any(k)) {
			t.Errorf("vecnet_send required = %v, missing %s", req, k)
		}
	}
	defs, _ := send["$defs"].(map[string]any)
	if _, ok := defs["peerId"]; !ok {
		t.Errorf("vecnet_send has no $defs/peerId: %v", send["$defs"])
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

	// A hash the sender uploaded is still sent, and the hub sets blob_url.
	var inbox inboxResult
	toolOK(t, env, bSecret, "vecnet_inbox", map[string]any{}, &inbox)
	ds := inbox.deliveries()
	if len(ds) != 2 {
		t.Fatalf("B has %d deliveries, want 2", len(ds))
	}
	got := ds[1].Data.Message.Attachments
	if len(got) != 1 || got[0].SHA256 != sha || got[0].OriginURL != "" || got[0].BlobURL == "" {
		t.Fatalf("delivered attachments = %+v, want %s with only a hub blob_url", got, sha)
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
