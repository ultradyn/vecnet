package mcpserver_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	venv "github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/testhub"
)

func TestMCPTimestampsNormalizeBeforeValidation(t *testing.T) {
	env := testhub.Start(t)
	_, secret := env.NewPeer("session", "timestamps")
	peer, _ := env.NewPeer("session", "timestamp recipient")
	for _, value := range []string{
		"2030-01-02T03:04:05Z", "2030-01-02T03:04:05.1Z",
		"2030-01-02T03:04:05.123456789Z", "2030-01-02T13:04:05+10:00",
		"2030-01-01T21:34:05.123456-05:30", "2030-01-02t03:04:05z",
	} {
		t.Run(value, func(t *testing.T) {
			toolOK(t, env, secret, "vecnet_threads", map[string]any{"since": value}, nil)
		})
	}
	for _, value := range []any{
		"yesterday", "2030-02-30T03:04:05Z", "2030-01-02", "2030-01-02T03:04:05",
		"2030-01-02T03:04:05,123Z", "2030-01-02T03:04:05+24:00", "2030-01-02T03:04:05+10:60",
		"2030-01-02T03:04:05Z trailing", 42, map[string]any{"private": "value"},
	} {
		if path := invalidPath(t, env, secret, "vecnet_threads", map[string]any{"since": value}); path != "/since" {
			t.Errorf("invalid timestamp %v: path %q", value, path)
		}
	}

	// Presence must still satisfy the hub's freshness checks, after conversion.
	now := env.Now.Now().Truncate(time.Second)
	toolOK(t, env, secret, "vecnet_presence", map[string]any{
		"state": "idle", "at": now.In(time.FixedZone("offset", 10*60*60)).Format(time.RFC3339),
	}, nil)
	expires := now.Add(time.Hour).Add(123456789 * time.Nanosecond)
	offsetExpiry := expires.In(time.FixedZone("offset", -5*60*60-30*60)).Format(time.RFC3339Nano)
	var sent sendResult
	toolOK(t, env, secret, "vecnet_send", map[string]any{
		"idempotency_key": "timestamp-send", "priority": "hold", "recipients": []string{peer},
		"content": map[string]any{"text": "timestamp"}, "expires_at": offsetExpiry,
		"metadata": map[string]any{"expires_at": offsetExpiry},
	}, &sent)
	toolOK(t, env, secret, "vecnet_reply", map[string]any{
		"idempotency_key": "timestamp-reply", "priority": "hold", "message_id": sent.MessageID,
		"text": "reply", "expires_at": offsetExpiry,
	}, nil)
	var history struct {
		Messages []struct {
			Loop struct {
				ExpiresAt string `json:"expires_at"`
			} `json:"loop"`
			Metadata map[string]any `json:"metadata"`
		} `json:"messages"`
	}
	toolOK(t, env, secret, "vecnet_thread", map[string]any{"id": sent.ThreadID}, &history)
	if len(history.Messages) != 2 {
		t.Fatalf("history length = %d", len(history.Messages))
	}
	for _, message := range history.Messages {
		if message.Loop.ExpiresAt != venv.FormatTime(expires) {
			t.Errorf("expiry = %q, want canonical UTC milliseconds %q", message.Loop.ExpiresAt, venv.FormatTime(expires))
		}
	}
	if history.Messages[0].Metadata["expires_at"] != offsetExpiry {
		t.Error("application metadata timestamp was changed")
	}

	// HTTP continues to require the hub's canonical timestamp spelling.
	response, _ := env.Do(secret, "POST", "/v1/messages", map[string]any{
		"idempotency_key": "timestamp-http", "priority": "hold", "recipients": []string{peer},
		"content": map[string]any{"text": "http"}, "expires_at": offsetExpiry,
	})
	if response.StatusCode != 400 {
		t.Errorf("HTTP noncanonical timestamp: status %d, want 400", response.StatusCode)
	}

	// Retention references reach hub authorization only after the MCP adapter
	// validates their timestamps. No real blob or backend is needed here.
	for _, tool := range []string{"vecnet_send", "vecnet_reply"} {
		args := map[string]any{
			"idempotency_key": "timestamp-attachment-" + tool, "priority": "hold",
			"attachments": []any{map[string]any{
				"sha256": strings.Repeat("a", 64), "size": 1, "media_type": "text/plain",
				"blob_id": "blo_00000000000000000000000001", "retention_class": "1h", "expires_at": offsetExpiry,
			}},
		}
		if tool == "vecnet_send" {
			args["recipients"], args["content"] = []string{peer}, map[string]any{"text": "attachment"}
		} else {
			args["message_id"], args["text"] = sent.MessageID, "attachment"
		}
		_, response := rpc(t, env, secret, "tools/call", map[string]any{"name": tool, "arguments": args})
		if response.Error == nil || response.Error.Code != -32602 || !strings.Contains(response.Error.Message, "value does not satisfy the schema or hub constraint") || string(response.Error.Data) != `{"path":"/attachments/0/blob_id"}` {
			t.Errorf("%s: want hub's unavailable-backend refusal after timestamp validation, got %+v", tool, response.Error)
		}
		args["attachments"].([]any)[0].(map[string]any)["expires_at"] = "malformed"
		if path := invalidPath(t, env, secret, tool, args); path != "/attachments/0/expires_at" {
			t.Errorf("%s malformed attachment timestamp path = %q", tool, path)
		}
	}

	// Approval deadlines are the other timestamp-valued MCP input.
	toolOK(t, env, secret, "vecnet_send", map[string]any{
		"idempotency_key": "timestamp-approval", "priority": "hold", "recipients": []string{peer},
		"kind": "approval_request", "content": map[string]any{"text": "approve"},
		"approval_request": map[string]any{
			"approval_id": "apr_00000000000000000000000001", "question": "Proceed?",
			"options": []string{"yes", "no"}, "expires_at": offsetExpiry,
		},
	}, nil)

	// JSON Schema integer spellings retain their existing semantics when
	// timestamp arguments are rewritten.
	args := json.RawMessage(fmt.Sprintf(`{"since":%q,"limit":5.0}`, offsetExpiry))
	toolOK(t, env, secret, "vecnet_threads", args, nil)
}

func TestMCPTimestampNormalizationPreservesDuplicateHandling(t *testing.T) {
	env := testhub.Start(t)
	_, secret := env.NewPeer("session", "duplicate sender")
	peer, _ := env.NewPeer("session", "duplicate recipient")
	for i, timestamp := range []string{
		`2030-01-02T03:04:05.000Z`,
		`2030-01-02T03:04:05.\u0030\u0030\u0030Z`,
		`2030-01-02T13:04:05+10:00`,
	} {
		args := fmt.Sprintf(`{"idempotency_key":"duplicate-%d","priority":"hold","recipients":[%q],"kind":"approval_request","content":{"text":"approve"},"approval_request":{"approval_id":"apr_00000000000000000000000001","question":"first","question":"last","options":["yes","no"],"expires_at":"%s"}}`, i, peer, timestamp)
		status, response := rpcVerbatim(t, env, secret, "vecnet_send", args)
		if status != 200 || response.Error == nil || response.Error.Code != -32602 || string(response.Error.Data) != `{"path":"/approval_request/question"}` {
			t.Errorf("timestamp %d: nested duplicate must be refused: HTTP %d, %+v", i, status, response)
		}
	}
	// Top-level duplicates intentionally keep the last value, even if the
	// earlier value is invalid. Timestamp conversion must not change that.
	args := fmt.Sprintf(`{"idempotency_key":"top-duplicate","priority":"invalid","priority":"hold","recipients":[%q],"content":{"text":"ok"},"expires_at":"invalid","expires_at":%q}`, peer, env.Now.Now().Add(time.Hour).Format(time.RFC3339))
	if result := verbatimResult(t, env, secret, "vecnet_send", args); result.IsError {
		t.Fatalf("top-level duplicates lost last-value behavior: %s", result.StructuredContent)
	}
}
