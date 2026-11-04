package mcpserver_test

import (
	"encoding/json"
	"testing"

	venv "github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/testhub"
)

// TestSenderInfoInMCPReadTools: vecnet_inbox deliveries and vecnet_thread
// history carry the send-time sender_info (PROTOCOL §2.2 step 12, §4.3).
func TestSenderInfoInMCPReadTools(t *testing.T) {
	env := testhub.Start(t)
	_, aSecret := env.NewPeer("grokbot", "spawner")
	b, bSecret := env.NewPeer("session", "b1")

	var sent sendResult
	toolOK(t, env, aSecret, "vecnet_send", map[string]any{
		"idempotency_key": "k1", "recipients": []string{b}, "priority": "hold",
		"content": map[string]any{"text": "hello"},
	}, &sent)

	var inbox struct {
		Events []venv.Event `json:"events"`
	}
	toolOK(t, env, bSecret, "vecnet_inbox", map[string]any{}, &inbox)
	n := 0
	for _, ev := range inbox.Events {
		if ev.Type != "delivery" {
			continue
		}
		n++
		if err := venv.Validate("delivery", ev.Data); err != nil {
			t.Fatalf("inbox delivery violates the schema: %v", err)
		}
		var d venv.Delivery
		if err := json.Unmarshal(ev.Data, &d); err != nil {
			t.Fatal(err)
		}
		if si := d.Message.SenderInfo; si == nil || si.Name != "spawner" || si.Kind != "grokbot" {
			t.Fatalf("vecnet_inbox sender_info = %+v", si)
		}
	}
	if n != 1 {
		t.Fatalf("inbox has %d deliveries, want 1", n)
	}

	var hist struct {
		Messages []venv.Message `json:"messages"`
	}
	toolOK(t, env, bSecret, "vecnet_thread", map[string]any{"id": sent.ThreadID}, &hist)
	if len(hist.Messages) != 1 {
		t.Fatalf("history = %+v", hist)
	}
	if si := hist.Messages[0].SenderInfo; si == nil || si.Name != "spawner" || si.Kind != "grokbot" {
		t.Fatalf("vecnet_thread sender_info = %+v", si)
	}
}
