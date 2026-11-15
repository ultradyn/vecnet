package mcpserver_test

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/testhub"
)

func TestMCPRecipientWarningSendReplyAndReplay(t *testing.T) {
	env := testhub.Start(t)
	_, sec := env.NewPeer("session", "Sender")
	b, bSec := env.NewPeer("session", "Recipient")
	env.Now.Advance(75 * time.Minute)
	args := map[string]any{"idempotency_key": "stale", "recipients": []string{b}, "priority": "hold", "content": map[string]any{"text": "Hello"}}
	result := callTool(t, env, sec, "vecnet_send", args)
	var receipt struct {
		MessageID string `json:"message_id"`
		Warnings  []struct {
			Code      string `json:"code"`
			Recipient string `json:"recipient"`
			Message   string `json:"message"`
		} `json:"warnings"`
	}
	check := func(result toolResult) {
		t.Helper()
		if result.IsError || json.Unmarshal(result.StructuredContent, &receipt) != nil || receipt.MessageID == "" || len(receipt.Warnings) != 1 || receipt.Warnings[0].Code != "recipient_not_seen" || receipt.Warnings[0].Recipient != b || !strings.Contains(receipt.Warnings[0].Message, "75 minutes ago") {
			t.Fatalf("missing recipient warning: %+v", result)
		}
		if len(result.Content) != 1 || !reflect.DeepEqual(ergMCPJSONValue(t, []byte(result.Content[0].Text)), ergMCPJSONValue(t, result.StructuredContent)) {
			t.Fatalf("warning text and structured output differ: %+v", result)
		}
	}
	check(result)
	check(callTool(t, env, sec, "vecnet_reply", map[string]any{"message_id": receipt.MessageID, "idempotency_key": "reply", "priority": "hold", "text": "Update"}))
	env.Do(bSec, "GET", "/v1/peers", nil)
	replay := callTool(t, env, sec, "vecnet_send", args)
	if replay.IsError || !reflect.DeepEqual(ergMCPJSONValue(t, result.StructuredContent), ergMCPJSONValue(t, replay.StructuredContent)) {
		t.Fatalf("replay changed warning: %+v", replay)
	}
}
