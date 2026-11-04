package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/XertroV/vecnet/internal/cli"
	"github.com/XertroV/vecnet/internal/httpapi"
	"github.com/XertroV/vecnet/internal/testhub"
)

type relayReply struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  json.RawMessage `json:"result"`
	Error   *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func relayLine(id int, method string, params any) string {
	b, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
	if err != nil {
		panic(err)
	}
	return string(b)
}
func relayInit(version string) string {
	return relayLine(1, "initialize", map[string]any{"protocolVersion": version, "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "relay-test", "version": "1"}})
}
func relayCall(id int, name string, args any) string {
	return relayLine(id, "tools/call", map[string]any{"name": name, "arguments": args})
}
func relayRun(t *testing.T, u *user, input string, args ...string) (int, string, string) {
	t.Helper()
	var out, errs bytes.Buffer
	code := cli.Run(append(args, "mcp"), strings.NewReader(input), &out, &errs, u.getenv)
	return code, out.String(), errs.String()
}
func relayOK(t *testing.T, u *user, input []string, args ...string) []relayReply {
	t.Helper()
	code, out, errs := relayRun(t, u, strings.Join(input, "\n")+"\n", args...)
	if code != 0 || errs != "" {
		t.Fatalf("relay exit %d, stdout %s, stderr %s", code, out, errs)
	}
	return relayReplies(t, out)
}
func relayReplies(t *testing.T, out string) []relayReply {
	t.Helper()
	var replies []relayReply
	for _, line := range nonEmptyLines(out) {
		var r relayReply
		if err := json.Unmarshal([]byte(line), &r); err != nil || r.JSONRPC != "2.0" {
			t.Fatalf("non JSON-RPC stdout %q: %v", line, err)
		}
		replies = append(replies, r)
	}
	return replies
}
func relayIDs(t *testing.T, replies []relayReply, ids ...int) {
	t.Helper()
	if len(replies) != len(ids) {
		t.Fatalf("replies = %+v, want ids %v", replies, ids)
	}
	for i, id := range ids {
		if string(replies[i].ID) != fmt.Sprint(id) {
			t.Fatalf("response %d id = %s, want %d", i, replies[i].ID, id)
		}
	}
}
func relayTool(t *testing.T, r relayReply, out any) {
	t.Helper()
	if r.Error != nil {
		t.Fatalf("RPC error %+v", r.Error)
	}
	var result struct {
		IsError           bool            `json:"isError"`
		StructuredContent json.RawMessage `json:"structuredContent"`
	}
	if err := json.Unmarshal(r.Result, &result); err != nil || result.IsError {
		t.Fatalf("tool result %s: %v", r.Result, err)
	}
	if out != nil {
		if err := json.Unmarshal(result.StructuredContent, out); err != nil {
			t.Fatalf("tool content %s: %v", result.StructuredContent, err)
		}
	}
}
func relayError(t *testing.T, r relayReply, code int, message string) {
	t.Helper()
	if r.Error == nil || r.Error.Code != code || r.Error.Message != message {
		t.Fatalf("reply %+v, want error %d %q", r, code, message)
	}
}
func relaySend(to, key string) map[string]any {
	return map[string]any{"recipients": []string{to}, "content": map[string]any{"text": "hello from relay"}, "priority": "hold", "idempotency_key": key}
}

func TestMCPRelayInitializeAndCall(t *testing.T) {
	env := testhub.Start(t)
	a, secret := env.NewPeer("session", "A")
	b, bSecret := env.NewPeer("session", "B")
	replies := relayOK(t, profileUser(t, env.URL, secret), []string{
		relayInit("2025-11-25"), `{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		relayLine(2, "tools/list", map[string]any{}), relayCall(3, "vecnet_send", relaySend(b, "relay-send")),
	})
	relayIDs(t, replies, 1, 2, 3)
	if replies[0].Error != nil || !strings.Contains(string(replies[0].Result), `"protocolVersion":"2025-11-25"`) {
		t.Fatalf("initialize: %+v", replies[0])
	}
	var listed struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(replies[1].Result, &listed); err != nil {
		t.Fatal(err)
	}
	found := map[string]bool{}
	for _, tool := range listed.Tools {
		found[tool.Name] = true
	}
	if !found["vecnet_send"] || !found["vecnet_inbox"] {
		t.Fatalf("tools: %+v", listed)
	}
	relayTool(t, replies[2], nil)
	deliveries := profileUser(t, env.URL, bSecret).inboxJSON()
	if len(deliveries) != 1 || deliveries[0].Data.Message.Sender != a || deliveries[0].Data.Message.Content.Text != "hello from relay" {
		t.Fatalf("B inbox = %+v", deliveries)
	}
}

func TestMCPRelayOlderClientVersion(t *testing.T) {
	env := testhub.Start(t)
	_, secret := env.NewPeer("session", "A")
	replies := relayOK(t, profileUser(t, env.URL, secret), []string{relayInit("2025-06-18"), relayCall(2, "vecnet_peers", map[string]any{})})
	relayIDs(t, replies, 1, 2)
	var init struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	if err := json.Unmarshal(replies[0].Result, &init); err != nil || init.ProtocolVersion != "2025-11-25" {
		t.Fatalf("initialize = %s: %v", replies[0].Result, err)
	}
	relayTool(t, replies[1], nil)
}

func TestMCPRelayAsBound(t *testing.T) {
	env := testhub.Start(t)
	prn, secret := env.NewPrincipal("relay principal")
	p1, _, err := env.Hub.AddPeer(context.Background(), "session", "P1", prn)
	if err != nil {
		t.Fatal(err)
	}
	p2, _, err := env.Hub.AddPeer(context.Background(), "session", "P2", prn)
	if err != nil {
		t.Fatal(err)
	}
	b, bSecret := env.NewPeer("session", "B")
	sender := profileUser(t, env.URL, bSecret)
	sender.send(p1, "only P1", "--hold")
	sender.send(p2, "only P2", "--hold")
	wrong := relaySend(b, "wrong-as")
	wrong["as"] = p2
	replies := relayOK(t, profileUser(t, env.URL, secret), []string{
		relayInit("2025-11-25"), // No tools/list: the relay must fetch it itself.
		relayCall(2, "vecnet_send", relaySend(b, "bound-as")), relayCall(3, "vecnet_send", wrong),
		relayCall(4, "vecnet_inbox", map[string]any{}), relayCall(5, "vecnet_ack", map[string]any{"peer": p2, "cursor": "e1.1"}),
		relayCall(6, "vecnet_peers", map[string]any{}),
	}, "--as", p1)
	relayIDs(t, replies, 1, 2, 3, 4, 5, 6)
	relayTool(t, replies[1], nil)
	relayError(t, replies[2], -32602, "relay is bound to --as "+p1)
	var inbox struct {
		Events []inboxEvent `json:"events"`
	}
	relayTool(t, replies[3], &inbox)
	n := 0
	for _, event := range inbox.Events {
		if event.Type == "delivery" {
			n++
			if event.Data.Recipient != p1 || event.Data.Message.Content.Text != "only P1" {
				t.Fatalf("bound inbox event %+v", event)
			}
		}
	}
	if n != 1 {
		t.Fatalf("bound inbox deliveries = %d", n)
	}
	relayError(t, replies[4], -32602, "relay is bound to --as "+p1)
	relayTool(t, replies[5], nil)
	n = 0
	for _, event := range sender.inboxJSON() {
		if event.Type == "delivery" {
			n++
			if event.Data.Message.Sender != p1 {
				t.Fatalf("sender = %s, want %s", event.Data.Message.Sender, p1)
			}
		}
	}
	if n != 1 {
		t.Fatalf("B deliveries = %d, want only the permitted send", n)
	}
}

// relayFake checks the HTTP boundary for every post, including internal posts.
func relayFake(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Old hubs can lack identity; TOFU is best effort before MCP POSTs.
		if r.Method == http.MethodGet && r.URL.Path == "/v1/hub/identity" {
			if r.Header.Get("Authorization") != "" {
				t.Error("identity request carried a credential")
			}
			http.NotFound(w, r)
			return
		}
		if r.Method != "POST" || r.URL.Path != "/mcp" || r.Header.Get("Content-Type") != "application/json" || r.Header.Get("Accept") != "application/json, text/event-stream" || r.Header.Get("Authorization") != "Bearer relay-test-secret" {
			t.Errorf("unexpected relay request: %s %s, content-type=%q accept=%q auth matches=%v", r.Method, r.URL.Path, r.Header.Get("Content-Type"), r.Header.Get("Accept"), r.Header.Get("Authorization") == "Bearer relay-test-secret")
		}
		handler(w, r)
	}))
	t.Cleanup(server.Close)
	return server
}
func relayRequest(t *testing.T, r *http.Request) map[string]json.RawMessage {
	t.Helper()
	var req map[string]json.RawMessage
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		t.Errorf("request: %v", err)
	}
	return req
}
func relayResult(w http.ResponseWriter, id json.RawMessage, result any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
}

func TestMCPRelayReinitOnSessionLoss(t *testing.T) {
	for _, persistent := range []bool{false, true} {
		t.Run(fmt.Sprint(persistent), func(t *testing.T) {
			var inits, calls atomic.Int32
			original := relayInit("2025-11-25")
			server := relayFake(t, func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				var req map[string]json.RawMessage
				if err := json.Unmarshal(body, &req); err != nil {
					t.Error(err)
				}
				if string(req["method"]) == `"initialize"` {
					if string(body) != original {
						t.Errorf("initialize changed: %s", body)
					}
					n := inits.Add(1)
					w.Header().Set("Mcp-Session-Id", fmt.Sprintf("session-%d", n))
					relayResult(w, req["id"], map[string]any{"protocolVersion": "2025-11-25"})
					return
				}
				n := calls.Add(1)
				if got := r.Header.Get("Mcp-Session-Id"); got != fmt.Sprintf("session-%d", inits.Load()) {
					t.Errorf("session = %q", got)
				}
				if r.Header.Get("MCP-Protocol-Version") != "2025-11-25" {
					t.Errorf("missing negotiated version")
				}
				if n == 1 || persistent {
					w.WriteHeader(404)
					fmt.Fprint(w, `{"error":{"code":"session_not_found","message":"gone"}}`)
					return
				}
				relayResult(w, req["id"], map[string]any{"ok": true})
			})
			replies := relayOK(t, profileUser(t, server.URL, "relay-test-secret"), []string{original, relayCall(2, "vecnet_peers", map[string]any{})})
			relayIDs(t, replies, 1, 2)
			if inits.Load() != 2 || calls.Load() != 2 {
				t.Fatalf("posts: initializes=%d calls=%d", inits.Load(), calls.Load())
			}
			if persistent {
				relayError(t, replies[1], -32603, "vecnet relay: hub answered 404 session_not_found")
			} else if string(replies[1].Result) != `{"ok":true}` {
				t.Fatalf("retry result: %+v", replies[1])
			}
		})
	}
}

func TestMCPRelaySSEResponse(t *testing.T) {
	server := relayFake(t, func(w http.ResponseWriter, r *http.Request) {
		req := relayRequest(t, r)
		w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		fmt.Fprintf(w, ": heartbeat\r\nevent: message\r\ndata: {\"jsonrpc\":\"2.0\",\r\ndata: \"id\":%s,\"result\":{\"ok\":true}}\r\n\r\n", req["id"])
	})
	replies := relayOK(t, profileUser(t, server.URL, "relay-test-secret"), []string{relayCall(2, "vecnet_peers", map[string]any{})})
	relayIDs(t, replies, 2)
	if replies[0].Error != nil || string(replies[0].Result) != `{"ok":true}` {
		t.Fatalf("SSE response %+v", replies[0])
	}
}

func TestMCPRelayStdoutIsJSONOnly(t *testing.T) {
	closed := httptest.NewServer(http.NotFoundHandler())
	closed.Close()
	secret := "relay-test-secret"
	u := profileUser(t, strings.Replace(closed.URL, "http://", "http://url-user:url-password@", 1), secret)
	code, out, errs := relayRun(t, u, strings.Join([]string{relayLine(2, "ping", nil), `not json`, relayCall(3, "vecnet_peers", map[string]any{}), `{"jsonrpc":"2.0","method":"notifications/initialized"}`}, "\n"))
	if code != 0 || errs != "" {
		t.Fatalf("exit=%d stderr=%q", code, errs)
	}
	replies := relayReplies(t, out)
	if len(replies) != 3 {
		t.Fatalf("stdout %q", out)
	}
	relayError(t, replies[0], -32603, "vecnet relay: hub unreachable")
	relayError(t, replies[1], -32700, "Parse error")
	if string(replies[1].ID) != "null" {
		t.Fatalf("parse-error id=%s", replies[1].ID)
	}
	relayError(t, replies[2], -32603, "vecnet relay: hub unreachable")
	for _, value := range []string{secret, "Authorization", "url-user", "url-password"} {
		if strings.Contains(out+errs, value) {
			t.Fatalf("streams exposed %q", value)
		}
	}
	for _, status := range []int{401, 429} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			codeName := "unauthorized"
			if status == 429 {
				codeName = "client_rate_limited"
			}
			server := relayFake(t, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(status)
				fmt.Fprintf(w, `{"error":{"code":%q,"message":%q}}`, codeName, "Authorization: Bearer "+secret)
			})
			code, out, errs := relayRun(t, profileUser(t, server.URL, secret), relayLine(2, "ping", nil)+"\n"+`{"jsonrpc":"2.0","method":"notifications/initialized"}`+"\n")
			if code != 0 || errs != "" {
				t.Fatalf("exit=%d stderr=%q", code, errs)
			}
			replies := relayReplies(t, out)
			relayIDs(t, replies, 2)
			relayError(t, replies[0], -32603, fmt.Sprintf("vecnet relay: hub answered %d %s", status, codeName))
			if strings.Contains(out+errs, secret) || strings.Contains(out+errs, "Authorization") {
				t.Fatalf("credential leaked")
			}
		})
	}
}

type relayUnread struct{ t *testing.T }

func (r relayUnread) Read([]byte) (int, error) {
	r.t.Error("relay read stdin before refusing")
	return 0, io.EOF
}
func relayRefusal(t *testing.T, u *user, args []string, want string) {
	t.Helper()
	var out, errs bytes.Buffer
	code := cli.Run(args, relayUnread{t}, &out, &errs, u.getenv)
	if code != 1 || out.Len() != 0 || errs.String() != want+"\n" {
		t.Fatalf("refusal = %d stdout=%q stderr=%q, want %q", code, out.String(), errs.String(), want)
	}
}
func TestMCPRelayRefusesProfileMismatch(t *testing.T) {
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
	defer server.Close()
	u := profileUser(t, server.URL, "relay-test-secret")
	u.env["VECNET_PROFILE"] = "q"
	relayRefusal(t, u, []string{"--net", "p", "mcp"}, "vecnet mcp: this session is profile q; relay for profile p refuses")
	if hits.Load() != 0 {
		t.Fatalf("hub reached %d times", hits.Load())
	}
}
func TestMCPRelayRefusesOperator(t *testing.T) {
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
	defer server.Close()
	u := profileUser(t, server.URL, "vn_operator_test-secret")
	relayRefusal(t, u, []string{"--net", "p", "mcp"}, "vecnet mcp: operator credentials stay on the CLI; enrol a peer or principal profile for MCP")
	if hits.Load() != 0 {
		t.Fatalf("hub reached %d times", hits.Load())
	}
}
func TestMCPRelayRefusesBareAgent(t *testing.T) {
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
	defer server.Close()
	u := profileUser(t, server.URL, "relay-test-secret")
	delete(u.env, "VECNET_PROFILE")
	u.env["VECNET_AGENT"] = "1"
	var out, errs bytes.Buffer
	code := cli.Run([]string{"mcp"}, relayUnread{t}, &out, &errs, u.getenv)
	if code != 1 || out.Len() != 0 || !strings.Contains(errs.String(), "--net <profile>") {
		t.Fatalf("bare agent = %d %q %q", code, out.String(), errs.String())
	}
	if hits.Load() != 0 {
		t.Fatalf("hub reached %d times", hits.Load())
	}
}

func TestMCPRelayAsSchemaDriven(t *testing.T) {
	var lists, calls atomic.Int32
	server := relayFake(t, func(w http.ResponseWriter, r *http.Request) {
		req := relayRequest(t, r)
		switch string(req["method"]) {
		case `"tools/list"`:
			lists.Add(1)
			relayResult(w, req["id"], map[string]any{"tools": []any{
				map[string]any{"name": "future_tool", "inputSchema": map[string]any{"properties": map[string]any{"as": map[string]any{}, "peer": map[string]any{}}}},
				map[string]any{"name": "vecnet_threads", "inputSchema": map[string]any{"properties": map[string]any{"participant": map[string]any{}}}},
				map[string]any{"name": "vecnet_peers", "inputSchema": map[string]any{"properties": map[string]any{}}},
			}})
		case `"tools/call"`:
			calls.Add(1)
			var p struct {
				Name      string         `json:"name"`
				Arguments map[string]any `json:"arguments"`
			}
			if err := json.Unmarshal(req["params"], &p); err != nil {
				t.Error(err)
			}
			switch p.Name {
			case "future_tool":
				if p.Arguments["as"] != "P1" || p.Arguments["peer"] != "P1" || p.Arguments["text"] != "preserved" {
					t.Errorf("future binding: %+v", p)
				}
			case "vecnet_threads":
				if p.Arguments["participant"] != "P1" {
					t.Errorf("participant binding: %+v", p)
				}
			case "vecnet_peers":
				if len(p.Arguments) != 0 {
					t.Errorf("unbound tool changed: %+v", p)
				}
			}
			relayResult(w, req["id"], map[string]any{"ok": true})
		default:
			t.Errorf("unexpected method %s", req["method"])
		}
	})
	replies := relayOK(t, profileUser(t, server.URL, "relay-test-secret"), []string{
		relayLine(1, "tools/list", map[string]any{}),
		relayCall(2, "future_tool", map[string]any{"text": "preserved"}),
		relayCall(3, "future_tool", map[string]any{"as": "P2"}),
		relayCall(4, "future_tool", map[string]any{"peer": nil}),
		relayCall(5, "vecnet_threads", map[string]any{}),
		relayCall(6, "vecnet_threads", map[string]any{"participant": "me"}),
		relayCall(7, "vecnet_threads", map[string]any{"participant": "P2"}),
		relayCall(8, "vecnet_peers", map[string]any{}),
	}, "--as", "P1")
	relayIDs(t, replies, 1, 2, 3, 4, 5, 6, 7, 8)
	for _, i := range []int{2, 3, 6} {
		relayError(t, replies[i], -32602, "relay is bound to --as P1")
	}
	for _, i := range []int{1, 4, 5, 7} {
		if replies[i].Error != nil {
			t.Fatalf("permitted call: %+v", replies[i])
		}
	}
	if lists.Load() != 1 || calls.Load() != 4 {
		t.Fatalf("hub list=%d calls=%d", lists.Load(), calls.Load())
	}
}

func TestMCPRelayNoArguments(t *testing.T) {
	u := newUser(t)
	code, out, errs := u.run("mcp", "extra")
	if code != 1 || out != "" || !strings.Contains(errs, "usage: vecnet mcp") {
		t.Fatalf("arguments = %d %q %q", code, out, errs)
	}
}

// A restarted hub may have a new tool schema. Binding must be checked
// against that schema before a call is retried under the new session.
func TestMCPRelayRebindsAfterSessionLoss(t *testing.T) {
	for _, change := range []string{"add-conflict", "add-absent", "remove"} {
		t.Run(change, func(t *testing.T) {
			conflict, remove := change == "add-conflict", change == "remove"
			var inits, lists, calls atomic.Int32
			server := relayFake(t, func(w http.ResponseWriter, r *http.Request) {
				req := relayRequest(t, r)
				switch string(req["method"]) {
				case `"initialize"`:
					n := inits.Add(1)
					w.Header().Set("Mcp-Session-Id", fmt.Sprintf("session-%d", n))
					relayResult(w, req["id"], map[string]any{"protocolVersion": "2025-11-25"})
				case `"tools/list"`:
					lists.Add(1)
					properties := map[string]any{}
					if (!remove && inits.Load() > 1) || (remove && inits.Load() == 1) {
						properties["as"] = map[string]any{"type": "string"}
					}
					relayResult(w, req["id"], map[string]any{"tools": []any{map[string]any{"name": "future_tool", "inputSchema": map[string]any{"properties": properties}}}})
				case `"tools/call"`:
					if calls.Add(1) == 1 {
						w.WriteHeader(404)
						fmt.Fprint(w, `{"error":{"code":"session_not_found"}}`)
						return
					}
					var p struct {
						Arguments map[string]any `json:"arguments"`
					}
					if err := json.Unmarshal(req["params"], &p); err != nil {
						t.Error(err)
					}
					relayResult(w, req["id"], p.Arguments)
				default:
					t.Errorf("unexpected method %s", req["method"])
				}
			})
			args := map[string]any{}
			if conflict {
				args["as"] = "P2"
			}
			replies := relayOK(t, profileUser(t, server.URL, "relay-test-secret"), []string{relayInit("2025-11-25"), relayLine(2, "tools/list", map[string]any{}), relayCall(3, "future_tool", args)}, "--as", "P1")
			relayIDs(t, replies, 1, 2, 3)
			if conflict {
				relayError(t, replies[2], -32602, "relay is bound to --as P1")
				if calls.Load() != 1 {
					t.Fatalf("conflicting call retried %d times", calls.Load()-1)
				}
			} else {
				want := `{"as":"P1"}`
				if remove {
					want = `{}`
				}
				if replies[2].Error != nil || string(replies[2].Result) != want {
					t.Fatalf("new schema binding = %+v", replies[2])
				}
				if calls.Load() != 2 {
					t.Fatalf("call posts = %d", calls.Load())
				}
			}
			if inits.Load() != 2 || lists.Load() != 2 {
				t.Fatalf("recovery initializes=%d lists=%d", inits.Load(), lists.Load())
			}
		})
	}
}

func TestMCPRelayRejectsRoutingAliases(t *testing.T) {
	env := testhub.Start(t)
	prn, secret := env.NewPrincipal("relay principal")
	p1, _, err := env.Hub.AddPeer(context.Background(), "session", "P1", prn)
	if err != nil {
		t.Fatal(err)
	}
	p2, _, err := env.Hub.AddPeer(context.Background(), "session", "P2", prn)
	if err != nil {
		t.Fatal(err)
	}
	b, bSecret := env.NewPeer("session", "B")
	wrong := relaySend(b, "routing-alias")
	wrong["as"] = p2
	arguments, _ := json.Marshal(wrong)
	params := fmt.Sprintf(`{"name":"vecnet_send","arguments":%s}`, arguments)
	request := func(p string) string {
		return `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":` + p + `}`
	}
	cases := map[string]string{
		"name-alias-after":         request(fmt.Sprintf(`{"name":"vecnet_peers","Name":"vecnet_send","arguments":%s}`, arguments)),
		"name-alias-before":        request(fmt.Sprintf(`{"NAME":"vecnet_send","name":"vecnet_peers","arguments":%s}`, arguments)),
		"name-alias-only":          request(fmt.Sprintf(`{"Name":"vecnet_send","arguments":%s}`, arguments)),
		"name-duplicate":           request(fmt.Sprintf(`{"name":"vecnet_peers","name":"vecnet_send","arguments":%s}`, arguments)),
		"arguments-ascii":          request(fmt.Sprintf(`{"name":"vecnet_send","arguments":{},"Arguments":%s}`, arguments)),
		"arguments-long-s":         request(fmt.Sprintf(`{"name":"vecnet_send","arguments":{},"argumentſ":%s}`, arguments)),
		"arguments-escaped-long-s": request(fmt.Sprintf(`{"name":"vecnet_send","arguments":{},"argument\u017f":%s}`, arguments)),
		"arguments-alias-only":     request(fmt.Sprintf(`{"name":"vecnet_send","ARGUMENTS":%s}`, arguments)),
		"arguments-duplicate":      request(fmt.Sprintf(`{"name":"vecnet_send","arguments":{},"arguments":%s}`, arguments)),
		"method-alias":             `{"jsonrpc":"2.0","id":2,"method":"tools/list","Method":"tools/call","params":` + params + `}`,
		"method-duplicate":         `{"jsonrpc":"2.0","id":2,"method":"tools/list","method":"tools/call","params":` + params + `}`,
		"params-ascii":             `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{},"Params":` + params + `}`,
		"params-long-s":            `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{},"paramſ":` + params + `}`,
		"params-duplicate":         `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{},"params":` + params + `}`,
		"id-alias":                 `{"jsonrpc":"2.0","ID":2,"method":"tools/call","params":` + params + `}`,
		"id-duplicate":             `{"jsonrpc":"2.0","id":2,"id":2,"method":"tools/call","params":` + params + `}`,
		"jsonrpc-alias":            `{"JSONRPC":"2.0","id":2,"method":"tools/call","params":` + params + `}`,
		"jsonrpc-duplicate":        `{"jsonrpc":"2.0","jsonrpc":"2.0","id":2,"method":"tools/call","params":` + params + `}`,
	}
	for name, input := range cases {
		t.Run(name, func(t *testing.T) {
			replies := relayOK(t, profileUser(t, env.URL, secret), []string{relayInit("2025-11-25"), input}, "--as", p1)
			relayIDs(t, replies, 1, 2)
			if replies[1].Error == nil || replies[1].Error.Code != -32602 {
				t.Errorf("routing alias was not rejected: %+v", replies[1])
			}
		})
	}
	for _, event := range profileUser(t, env.URL, bSecret).inboxJSON() {
		if event.Type == "delivery" {
			t.Errorf("routing alias delivered a message from %s", event.Data.Message.Sender)
		}
	}
}

func TestMCPRelayRawFieldsAndBodyCap(t *testing.T) {
	// These are deliberately raw UTF-8 and HTML characters, rather than the
	// escaped representation produced by json.Marshal. Binding must preserve
	// each existing value, including whitespace inside nested raw objects.
	prefix := `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"future_tool","arguments":{"content":{ "text": "`
	suffix := `" },"metadata":{ "keep": "<>&` + "\u2028\u2029" + `" }}}}`
	for _, bound := range []bool{false, true} {
		t.Run(fmt.Sprintf("bound=%v", bound), func(t *testing.T) {
			growth := 0
			var options []string
			if bound {
				growth = len(`"as":"P1",`)
				options = []string{"--as", "P1"}
			}
			for _, tc := range []struct {
				name string
				text string
				over bool
			}{
				{"html", strings.Repeat("<", 50000), false},
				{"unicode", strings.Repeat("<>&\u2028\u2029", 6000), false},
				{"at-cap", strings.Repeat("<", httpapi.MaxBodyBytes-len(prefix+suffix)-growth), false},
				{"over-cap", strings.Repeat("<", httpapi.MaxBodyBytes-len(prefix+suffix)-growth+1), true},
			} {
				t.Run(tc.name, func(t *testing.T) {
					var calls atomic.Int32
					server := relayFake(t, func(w http.ResponseWriter, r *http.Request) {
						body, _ := io.ReadAll(r.Body)
						if len(body) > httpapi.MaxBodyBytes {
							t.Errorf("oversized request forwarded: %d bytes", len(body))
						}
						var req map[string]json.RawMessage
						if err := json.Unmarshal(body, &req); err != nil {
							t.Fatal(err)
						}
						if string(req["method"]) == `"tools/list"` {
							relayResult(w, req["id"], map[string]any{"tools": []any{map[string]any{"name": "future_tool", "inputSchema": map[string]any{"properties": map[string]any{"as": map[string]any{}}}}}})
							return
						}
						calls.Add(1)
						if tc.name == "at-cap" && len(body) != httpapi.MaxBodyBytes {
							t.Errorf("boundary request is %d bytes, want %d", len(body), httpapi.MaxBodyBytes)
						}
						var p struct {
							Arguments map[string]json.RawMessage `json:"arguments"`
						}
						if err := json.Unmarshal(req["params"], &p); err != nil {
							t.Error(err)
						}
						if string(p.Arguments["content"]) != `{ "text": "`+tc.text+`" }` || string(p.Arguments["metadata"]) != `{ "keep": "<>&`+"\u2028\u2029"+`" }` {
							t.Error("binding rewrote existing raw JSON values")
						}
						if bound && string(p.Arguments["as"]) != `"P1"` {
							t.Errorf("identity = %s", p.Arguments["as"])
						}
						if !bound && string(body) != prefix+tc.text+suffix {
							t.Error("unbound request changed")
						}
						relayResult(w, req["id"], map[string]any{"ok": true})
					})
					replies := relayOK(t, profileUser(t, server.URL, "relay-test-secret"), []string{prefix + tc.text + suffix}, options...)
					relayIDs(t, replies, 2)
					if tc.over {
						relayError(t, replies[0], -32602, "relay request exceeds 256 KiB")
						if calls.Load() != 0 {
							t.Errorf("oversized call reached hub %d times", calls.Load())
						}
					} else if replies[0].Error != nil || calls.Load() != 1 {
						t.Errorf("valid call: reply=%+v posts=%d", replies[0], calls.Load())
					}
				})
			}
		})
	}
}

func TestMCPRelayEscapableSend(t *testing.T) {
	for _, bound := range []bool{false, true} {
		t.Run(fmt.Sprintf("bound=%v", bound), func(t *testing.T) {
			env := testhub.Start(t)
			prn, secret := env.NewPrincipal("relay principal")
			p1, _, err := env.Hub.AddPeer(context.Background(), "session", "P1", prn)
			if err != nil {
				t.Fatal(err)
			}
			b, _ := env.NewPeer("session", "B")
			text := strings.Repeat("<", 50000)
			input := fmt.Sprintf(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"vecnet_send","arguments":{"as":%q,"recipients":[%q],"content":{"text":"%s"},"priority":"hold","idempotency_key":"escapable"}}}`, p1, b, text)
			options := []string{}
			if bound {
				options = []string{"--as", p1}
			}
			replies := relayOK(t, profileUser(t, env.URL, secret), []string{relayInit("2025-11-25"), input}, options...)
			relayIDs(t, replies, 1, 2)
			relayTool(t, replies[1], nil)
		})
	}
}

func TestMCPRelayClientResponses(t *testing.T) {
	for _, input := range []string{
		` {"jsonrpc":"2.0", "id":7, "result": {"ok": true}} `,
		`{"jsonrpc":"2.0","id":"server-request","error":{"code":-32601,"message":"unsupported"}}`,
		`{"jsonrpc":"2.0","id":null,"result":null}`,
	} {
		for _, status := range []int{http.StatusAccepted, http.StatusOK, http.StatusUnauthorized} {
			t.Run(fmt.Sprintf("%s/status=%d", input, status), func(t *testing.T) {
				var posts atomic.Int32
				server := relayFake(t, func(w http.ResponseWriter, r *http.Request) {
					posts.Add(1)
					body, err := io.ReadAll(r.Body)
					if err != nil || string(body) != input {
						t.Errorf("response POST changed: %q, error %v", body, err)
					}
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(status)
					if status == http.StatusOK {
						fmt.Fprint(w, `{"jsonrpc":"2.0","id":7,"result":{}}`)
					} else if status == http.StatusUnauthorized {
						fmt.Fprint(w, `{"error":{"code":"unauthorized"}}`)
					}
				})
				code, out, errs := relayRun(t, profileUser(t, server.URL, "relay-test-secret"), input+"\n", "--as", "P1")
				if code != 0 || out != "" || errs != "" || posts.Load() != 1 {
					t.Fatalf("exit=%d posts=%d stdout=%q stderr=%q", code, posts.Load(), out, errs)
				}
			})
		}
	}
}

func TestMCPRelayLargeInbox(t *testing.T) {
	env := testhub.Start(t)
	a, aSecret := env.NewPeer("session", "A")
	b, bSecret := env.NewPeer("session", "B")
	env.SetLimits(a, 1000, 1000)
	text := strings.Repeat("<", 4096)
	for i := range 200 {
		args := relaySend(b, fmt.Sprintf("large-inbox-%d", i))
		args["content"] = map[string]any{"text": text}
		resp, body := env.Do(aSecret, http.MethodPost, "/v1/messages", args)
		if resp.StatusCode != http.StatusAccepted {
			t.Fatalf("send %d: status=%d body=%s", i, resp.StatusCode, body)
		}
	}
	replies := relayOK(t, profileUser(t, env.URL, bSecret), []string{
		relayInit("2025-11-25"), relayCall(2, "vecnet_inbox", map[string]any{"limit": 200}),
	})
	relayIDs(t, replies, 1, 2)
	var inbox struct {
		Events []inboxEvent `json:"events"`
	}
	relayTool(t, replies[1], &inbox)
	if len(replies[1].Result) <= 4<<20 || len(inbox.Events) != 200 {
		t.Fatalf("inbox result: bytes=%d events=%d", len(replies[1].Result), len(inbox.Events))
	}
	for i, event := range inbox.Events {
		if event.Type != "delivery" || event.Data.Recipient != b || event.Data.Message.Content.Text != text {
			t.Fatalf("event %d incomplete: type=%q recipient=%q text bytes=%d", i, event.Type, event.Data.Recipient, len(event.Data.Message.Content.Text))
		}
	}
}

func TestMCPRelayLargeSSEFrame(t *testing.T) {
	text := strings.Repeat("x", 5<<20)
	payload := `{"jsonrpc":"2.0","id":2,"result":{"text":"` + text + `"}}`
	last := `{"jsonrpc":"2.0","id":2,"result":{"done":true}}`
	for _, ending := range []string{"\n\n", "\r\n\r\n", ""} {
		t.Run(fmt.Sprintf("ending=%q", ending), func(t *testing.T) {
			server := relayFake(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprintf(w, "data: %s\r\n\r\ndata: %s%s", payload, last, ending)
			})
			code, out, errs := relayRun(t, profileUser(t, server.URL, "relay-test-secret"), relayCall(2, "large", map[string]any{})+"\n")
			if code != 0 || errs != "" {
				t.Fatalf("exit=%d stderr=%q", code, errs)
			}
			if out != payload+"\n"+last+"\n" {
				t.Fatalf("incomplete SSE output: got %d bytes, want %d", len(out), len(payload)+len(last)+2)
			}
			relayIDs(t, relayReplies(t, out), 2, 2)
		})
	}
}
