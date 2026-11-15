package mcpserver_test

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/auth"
	"github.com/XertroV/vecnet/internal/testhub"
)

func TestMCPRapidThreadCreationWarningIsSoftAndPreservesReplay(t *testing.T) {
	env := testhub.Start(t)
	a, secret := env.NewPeer("session", "Sender")
	b, _ := env.NewPeer("session", "Receiver")
	group, err := env.Hub.CreateTeam(context.Background(), auth.Caller{Kind: "operator"}, "Warning team", []string{a, b})
	if err != nil {
		t.Fatal(err)
	}
	var third toolResult
	var args map[string]any
	for n := 1; n <= 3; n++ {
		args = map[string]any{"idempotency_key": fmt.Sprintf("rapid-%d", n), "recipients": []string{group}, "priority": "hold", "content": map[string]any{"text": "Separate topic"}}
		result := callTool(t, env, secret, "vecnet_send", args)
		var receipt struct {
			MessageID string `json:"message_id"`
			Warnings  []struct {
				Code      string `json:"code"`
				Message   string `json:"message"`
				Group     string `json:"group_id"`
				Window    int    `json:"window_seconds"`
				Threshold int    `json:"threshold"`
			} `json:"warnings"`
		}
		if result.IsError || json.Unmarshal(result.StructuredContent, &receipt) != nil || receipt.MessageID == "" || len(result.Content) != 1 || !reflect.DeepEqual(ergMCPJSONValue(t, []byte(result.Content[0].Text)), ergMCPJSONValue(t, result.StructuredContent)) {
			t.Fatalf("send %d failed or text and structured output diverged: %+v", n, result)
		}
		if n < 3 && len(receipt.Warnings) != 0 {
			t.Fatalf("send %d warned before threshold: %s", n, result.StructuredContent)
		}
		if n == 3 {
			if len(receipt.Warnings) != 1 || receipt.Warnings[0].Code != "rapid_thread_creation" || receipt.Warnings[0].Group != group || receipt.Warnings[0].Window != 60 || receipt.Warnings[0].Threshold != 2 || !strings.Contains(strings.ToLower(receipt.Warnings[0].Message), "repl") {
				t.Fatalf("successful third send lacks advisory to reply: %s", result.StructuredContent)
			}
			third = result
			var reply map[string]json.RawMessage
			toolOK(t, env, secret, "vecnet_reply", map[string]any{"message_id": receipt.MessageID, "idempotency_key": "rapid-reply", "priority": "hold", "text": "Related update"}, &reply)
			if len(reply["warnings"]) > 0 && string(reply["warnings"]) != "[]" && string(reply["warnings"]) != "null" {
				t.Fatalf("reply incorrectly warned about new threads: %s", reply["warnings"])
			}
		}
	}
	env.Now.Advance(61 * time.Second)
	replayed := callTool(t, env, secret, "vecnet_send", args)
	if replayed.IsError || !reflect.DeepEqual(ergMCPJSONValue(t, replayed.StructuredContent), ergMCPJSONValue(t, third.StructuredContent)) || len(replayed.Content) != 1 || !reflect.DeepEqual(ergMCPJSONValue(t, []byte(replayed.Content[0].Text)), ergMCPJSONValue(t, third.StructuredContent)) {
		t.Fatalf("replay lost original warning snapshot: %+v", replayed)
	}
}
