package mcpserver_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"

	venv "github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/mcpserver"
	"github.com/XertroV/vecnet/internal/testhub"
)

func ergMCPRequest(t *testing.T, env *testhub.Env, secret, version string) func(*testing.T, string, any) rpcResp {
	t.Helper()
	headers := modern()
	if version == mcpserver.ProtoLegacy {
		headers = map[string]string{
			"MCP-Protocol-Version": version,
			"Mcp-Session-Id":       legacySession(t, env, secret),
			"Accept":               "application/json, text/event-stream",
		}
	}
	return func(t *testing.T, method string, params any) rpcResp {
		t.Helper()
		resp, raw := do(t, env, call{secret: secret, headers: headers, body: map[string]any{"jsonrpc": "2.0", "id": 7, "method": method, "params": params}})
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s: status=%d body=%s", method, resp.StatusCode, raw)
		}
		return decodeRPC(t, raw)
	}
}

func ergMCPToolPage(t *testing.T, response rpcResp) []byte {
	t.Helper()
	if response.Error != nil {
		t.Fatalf("inbox tool RPC error: %+v", response.Error)
	}
	var result toolResult
	if err := json.Unmarshal(response.Result, &result); err != nil {
		t.Fatal(err)
	}
	if result.IsError || len(result.Content) != 1 || result.Content[0].Type != "text" || !reflect.DeepEqual(ergMCPJSONValue(t, []byte(result.Content[0].Text)), ergMCPJSONValue(t, result.StructuredContent)) {
		t.Fatalf("text/structured inbox result diverged or errored: %s", response.Result)
	}
	return result.StructuredContent
}

func ergMCPLoop(t *testing.T, raw []byte, messageID string) map[string]json.RawMessage {
	t.Helper()
	var page struct {
		Events     []venv.Event `json:"events"`
		NextCursor string       `json:"next_cursor"`
	}
	if err := json.Unmarshal(raw, &page); err != nil || page.NextCursor == "" {
		t.Fatalf("bad inbox page: %s %v", raw, err)
	}
	for _, event := range page.Events {
		if event.Type != "delivery" {
			continue
		}
		var data struct {
			Message struct {
				ID   string                     `json:"message_id"`
				Loop map[string]json.RawMessage `json:"loop"`
			} `json:"message"`
		}
		if err := json.Unmarshal(event.Data, &data); err != nil {
			t.Fatal(err)
		}
		if data.Message.ID == messageID {
			return data.Message.Loop
		}
	}
	t.Fatalf("inbox omitted message %s", messageID)
	return nil
}

func TestMCPInboxSenderChainProjection(t *testing.T) {
	env := testhub.Start(t)
	a, aSecret := env.NewPeer("session", "Alpha")
	b, bSecret := env.NewPeer("session", "Bravo")
	hosted := []byte("ergonomics attachment")
	sum := sha256.Sum256(hosted)
	sha := hex.EncodeToString(sum[:])
	if resp, raw := env.Do(aSecret, http.MethodPut, "/v1/blobs", hosted); resp.StatusCode/100 != 2 {
		t.Fatalf("fixture blob upload: %d %s", resp.StatusCode, raw)
	}
	var sent sendResult
	toolOK(t, env, aSecret, "vecnet_send", map[string]any{
		"idempotency_key": "erg-mcp", "recipients": []string{b}, "priority": "hold",
		"content":     map[string]any{"text": "compact MCP inbox"},
		"metadata":    json.RawMessage(`{"large":9007199254740993,"sender_chain":["keep"]}`),
		"attachments": []any{map[string]any{"sha256": sha, "size": len(hosted), "media_type": "text/plain"}},
	}, &sent)
	resp, canonicalBefore := env.Do(bSecret, http.MethodGet, "/v1/events?after=e1.0&wait=0", nil)
	if resp.StatusCode != http.StatusOK || !bytes.Contains(canonicalBefore, []byte(`"sender_chain"`)) || !bytes.Contains(canonicalBefore, []byte("?t=")) {
		t.Fatal("fixture lacks canonical chain/blob token")
	}
	for _, version := range []string{mcpserver.ProtoCurrent, mcpserver.ProtoLegacy} {
		t.Run(version, func(t *testing.T) {
			request := ergMCPRequest(t, env, bSecret, version)
			for _, mode := range []string{"default", "false", "true"} {
				t.Run(mode, func(t *testing.T) {
					args := map[string]any{"after": "e1.0"}
					if mode != "default" {
						args["include_sender_chain"] = mode == "true"
					}
					raw := ergMCPToolPage(t, request(t, "tools/call", map[string]any{"name": "vecnet_inbox", "arguments": args}))
					loop := ergMCPLoop(t, raw, sent.MessageID)
					chainRaw, present := loop["sender_chain"]
					if present != (mode == "true") {
						t.Errorf("mode=%s sender_chain presence=%t", mode, present)
					}
					if mode == "true" {
						var chain []string
						if err := json.Unmarshal(chainRaw, &chain); err != nil || !reflect.DeepEqual(chain, []string{a}) {
							t.Fatalf("opt-in lost ancestry: %s %v", chainRaw, err)
						}
					}
					if len(loop["depth"]) == 0 || len(loop["max_depth"]) == 0 || len(loop["expires_at"]) == 0 || bytes.Contains(raw, []byte("blob_url")) || bytes.Contains(raw, []byte("?t=")) || !bytes.Contains(raw, []byte(sha)) || !bytes.Contains(raw, []byte("9007199254740993")) || !bytes.Contains(raw, []byte(`"sender_chain":["keep"]`)) {
						t.Fatalf("view altered loop/metadata/blob protection: %s", raw)
					}
				})
			}
		})
	}
	_, canonicalAfter := env.Do(bSecret, http.MethodGet, "/v1/events?after=e1.0&wait=0", nil)
	if !bytes.Equal(canonicalBefore, canonicalAfter) {
		t.Fatal("MCP reads changed canonical HTTP delivery bytes or acknowledged work")
	}
}

func TestMCPInboxSchemaAdvertisesIncludeSenderChain(t *testing.T) {
	env := testhub.Start(t)
	_, secret := env.NewPeer("session", "Reader")
	_, response := rpc(t, env, secret, "tools/list", nil)
	if response.Error != nil {
		t.Fatal(response.Error)
	}
	var listed struct {
		Tools []listedTool `json:"tools"`
	}
	if err := json.Unmarshal(response.Result, &listed); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, tool := range listed.Tools {
		if tool.Name != "vecnet_inbox" {
			continue
		}
		found = true
		properties, ok := tool.InputSchema["properties"].(map[string]any)
		if !ok {
			t.Fatal("inbox discovery has no properties")
		}
		field, ok := properties["include_sender_chain"].(map[string]any)
		if !ok || field["type"] != "boolean" || field["default"] != false || strings.TrimSpace(fmtMCPString(field["description"])) == "" {
			t.Errorf("discovery omits include_sender_chain boolean/default/help: %#v", field)
		}
	}
	if !found {
		t.Fatal("vecnet_inbox missing from discovery")
	}
	for _, invalid := range []any{"true", 1, nil} {
		if path := invalidPath(t, env, secret, "vecnet_inbox", map[string]any{"include_sender_chain": invalid}); path != "/include_sender_chain" {
			t.Errorf("bad include type path=%q", path)
		}
	}
	// Positive validation lives in the behavior test for omitted/false/true.
}

func fmtMCPString(value any) string {
	text, _ := value.(string)
	return text
}

func TestMCPInboxResourceOmitsSenderChain(t *testing.T) {
	env := testhub.Start(t)
	_, senderSecret := env.NewPeer("session", "Sender")
	principal, principalSecret := env.NewPrincipal("Readers")
	p1, p1Secret := peerUnder(t, env, principal, "First")
	p2, _ := peerUnder(t, env, principal, "Second")
	foreign, _ := env.NewPeer("session", "Foreign")
	var first, second sendResult
	for key, target := range map[string]string{"first": p1, "second": p2} {
		out := &first
		if key == "second" {
			out = &second
		}
		toolOK(t, env, senderSecret, "vecnet_send", map[string]any{"idempotency_key": "erg-resource-" + key, "recipients": []string{target}, "priority": "hold", "content": map[string]any{"text": key}}, out)
	}
	for _, scope := range []struct {
		name, secret, uri string
		messages          []string
	}{
		{"peer", p1Secret, peerURI(p1), []string{first.MessageID}},
		{"principal", principalSecret, prnURI(principal), []string{first.MessageID, second.MessageID}},
	} {
		t.Run(scope.name, func(t *testing.T) {
			raw := []byte(inboxText(t, env, scope.secret, scope.uri))
			for _, messageID := range scope.messages {
				if _, present := ergMCPLoop(t, raw, messageID)["sender_chain"]; present {
					t.Error("resource inbox still exposes sender_chain by default")
				}
			}
			if scope.name == "peer" && bytes.Contains(raw, []byte(second.MessageID)) {
				t.Fatal("peer resource widened to another inbox")
			}
		})
	}
	// Full-chain migration uses the existing tool, keeping the same peer scope.
	raw := ergMCPToolPage(t, rpc2026(t, env, principalSecret, "tools/call", map[string]any{"name": "vecnet_inbox", "arguments": map[string]any{"peer": p1, "include_sender_chain": true}}))
	if _, present := ergMCPLoop(t, raw, first.MessageID)["sender_chain"]; !present || bytes.Contains(raw, []byte(second.MessageID)) {
		t.Fatal("resource full-chain migration lost chain or widened peer scope")
	}
	for _, secret := range []string{p1Secret, principalSecret} {
		response := rpc2026(t, env, secret, "resources/read", map[string]any{"uri": peerURI(foreign)})
		if response.Error == nil || response.Error.Code != mcpserver.ErrInvalidParams || errReason(t, response) != "forbidden" {
			t.Fatal("compact resource bypassed ownership authorization")
		}
	}
}

func TestMCPSendAndReplyReceiptNames(t *testing.T) {
	env := testhub.Start(t)
	a, aSecret := env.NewPeer("session", "Alpha")
	b, bSecret := env.NewPeer("session", "Bravo")
	var sent sendResult
	send := callTool(t, env, aSecret, "vecnet_send", map[string]any{"idempotency_key": "erg-receipt", "recipients": []string{b}, "priority": "hold", "content": map[string]any{"text": "question"}})
	if err := json.Unmarshal(send.StructuredContent, &sent); err != nil || sent.MessageID == "" {
		t.Fatalf("send failed: %s %v", send.StructuredContent, err)
	}
	reply := callTool(t, env, bSecret, "vecnet_reply", map[string]any{"message_id": sent.MessageID, "idempotency_key": "erg-reply", "priority": "hold", "text": "answer"})
	for _, item := range []struct {
		result   toolResult
		id, name string
	}{{send, b, "Bravo"}, {reply, a, "Alpha"}} {
		var receipt struct {
			Deliveries []struct {
				Recipient string `json:"recipient"`
				Name      string `json:"recipient_name"`
			} `json:"deliveries"`
		}
		if item.result.IsError || json.Unmarshal(item.result.StructuredContent, &receipt) != nil || len(receipt.Deliveries) != 1 || receipt.Deliveries[0].Recipient != item.id || receipt.Deliveries[0].Name != item.name || len(item.result.Content) != 1 || !reflect.DeepEqual(ergMCPJSONValue(t, []byte(item.result.Content[0].Text)), ergMCPJSONValue(t, item.result.StructuredContent)) {
			t.Errorf("MCP receipt must name destination and canonical ID in both representations: %s", item.result.StructuredContent)
		}
	}
}

func ergMCPJSONValue(t *testing.T, raw []byte) any {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		t.Fatal(err)
	}
	return value
}
