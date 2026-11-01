package mcpserver_test

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	venv "github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/httpapi"
	"github.com/XertroV/vecnet/internal/mcpserver"
	"github.com/XertroV/vecnet/internal/testhub"
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
		"vecnet_ack", "vecnet_inbox", "vecnet_peers", "vecnet_presence", "vecnet_receipt",
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
		names := map[string]bool{}
		propertyNames(tool.InputSchema, names)
		if names["url"] {
			t.Errorf("%s: inputSchema has a property named url", tool.Name)
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
		Message    struct {
			MessageID string  `json:"message_id"`
			ThreadID  string  `json:"thread_id"`
			InReplyTo *string `json:"in_reply_to"`
			Sender    string  `json:"sender"`
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
