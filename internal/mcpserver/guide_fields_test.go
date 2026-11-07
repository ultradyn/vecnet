package mcpserver_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/auth"
	"github.com/XertroV/vecnet/internal/mcpserver"
	"github.com/XertroV/vecnet/internal/testhub"
	"github.com/XertroV/vecnet/internal/webhook"
)

func TestGuideUsesActualResponseFieldNames(t *testing.T) {
	env := testhub.Start(t)
	peer, secret := env.NewPeer("session", "guide field names")
	front := eventsFront(t, env, guideAuth{mcpserver.Principal{
		Caller: auth.Caller{Kind: "peer", PeerID: peer, PeerKind: "dot"},
		OAuth:  &mcpserver.OAuthInfo{Expires: env.Now.Now().Add(time.Hour)},
	}}, nil)
	var guide guideWire
	result := callTool(t, front, "test-token", "vecnet_guide", nil)
	if result.IsError {
		t.Fatalf("guide: %s", result.StructuredContent)
	}
	if err := json.Unmarshal(result.StructuredContent, &guide); err != nil {
		t.Fatal(err)
	}
	for _, phrase := range []string{
		"vecnet_subscriptions returns refresh_before",
		"events/subscribe returns refreshBefore",
	} {
		if !strings.Contains(guide.Text, phrase) {
			t.Errorf("guide lacks response-specific field description %q", phrase)
		}
	}
	// These are deliberately decoded from wire responses, not Go struct tags.
	assertFields := func(label string, value map[string]any, fields ...string) {
		t.Helper()
		for _, field := range fields {
			if _, exists := value[field]; !exists {
				t.Errorf("%s lacks documented field %s", label, field)
			}
			if !strings.Contains(guide.Text, field) {
				t.Errorf("guide does not name %s field %s", label, field)
			}
		}
	}
	callback := testhub.NewCallback(t)
	sub := rpc2026(t, env, secret, "events/subscribe", subParams(callback.URL, webhook.NewSecret(), nil))
	if sub.Error != nil {
		t.Fatal(sub.Error)
	}
	var subscribed map[string]any
	if err := json.Unmarshal(sub.Result, &subscribed); err != nil {
		t.Fatal(err)
	}
	assertFields("events/subscribe", subscribed, "id", "refreshBefore", "cursor", "truncated")
	if _, exists := subscribed["refresh_before"]; exists {
		t.Error("events/subscribe unexpectedly uses refresh_before")
	}
	var status struct {
		Subscriptions []map[string]any `json:"subscriptions"`
	}
	toolOK(t, env, secret, "vecnet_subscriptions", nil, &status)
	if len(status.Subscriptions) != 1 {
		t.Fatalf("subscriptions = %v", status.Subscriptions)
	}
	assertFields("vecnet_subscriptions", status.Subscriptions[0], "refresh_before", "expires_at", "verified", "paused", "last_delivery_at", "last_status")
	if _, exists := status.Subscriptions[0]["refreshBefore"]; exists {
		t.Error("vecnet_subscriptions unexpectedly uses refreshBefore")
	}
	if status.Subscriptions[0]["refresh_before"] != subscribed["refreshBefore"] {
		t.Error("subscription deadline differs between response surfaces")
	}
	var identity map[string]any
	toolOK(t, env, secret, "vecnet_whoami", nil, &identity)
	assertFields("vecnet_whoami", identity, "id", "peer_id")

	var sent sendResult
	toolOK(t, env, secret, "vecnet_send", map[string]any{
		"idempotency_key": "guide-fields", "priority": "hold", "recipients": []string{peer},
		"content": map[string]any{"text": "field names"},
	}, &sent)
	var inbox map[string]any
	toolOK(t, env, secret, "vecnet_inbox", nil, &inbox)
	assertFields("vecnet_inbox", inbox, "events", "next_cursor")
	var history, threads map[string]any
	toolOK(t, env, secret, "vecnet_thread", map[string]any{"id": sent.ThreadID}, &history)
	assertFields("vecnet_thread", history, "messages", "next_after")
	toolOK(t, env, secret, "vecnet_threads", nil, &threads)
	assertFields("vecnet_threads", threads, "next")

	// Check the guide's inbox payload path against a real delivery.
	found := false
	for _, value := range inbox["events"].([]any) {
		event := value.(map[string]any)
		if event["type"] != "delivery" {
			continue
		}
		data := event["data"].(map[string]any)
		message := data["message"].(map[string]any)
		assertFields("delivery", data, "delivery_id")
		assertFields("message", message, "message_id", "content")
		if message["content"].(map[string]any)["text"] == "field names" {
			found = true
		}
	}
	if !found {
		t.Error("documented events[].data.message.content.text path did not resolve")
	}
}
