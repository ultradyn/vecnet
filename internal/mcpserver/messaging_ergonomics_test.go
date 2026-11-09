package mcpserver_test

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/XertroV/vecnet/internal/testhub"
)

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
