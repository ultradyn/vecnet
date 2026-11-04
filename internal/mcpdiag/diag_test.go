package mcpdiag

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"
)

func TestSanitizedRingAndFilter(t *testing.T) {
	var b Buffer
	for i := 0; i < Capacity+7; i++ {
		b.Emit("events.delivery", map[string]any{"peer_id": fmt.Sprintf("peer_%d", i%2), "method": "events/subscribe", "event_id": fmt.Sprintf("evt_%d", i), "http_status": 200})
	}
	all := b.Snapshot(Filter{})
	if len(all) != Capacity || all[0].Sequence != 8 || all[len(all)-1].Sequence != 1007 {
		t.Fatalf("ring bounds: len=%d first=%d last=%d", len(all), all[0].Sequence, all[len(all)-1].Sequence)
	}
	filtered := b.Snapshot(Filter{Peer: "peer_1", Method: "events/subscribe", Limit: 2, After: 1000})
	if len(filtered) != 2 || filtered[0].Sequence >= filtered[1].Sequence {
		t.Fatalf("filtered: %+v", filtered)
	}
	filtered[0].Fields["method"] = "mutated"
	if len(b.Snapshot(Filter{Method: "mutated"})) != 0 {
		t.Fatal("snapshot mutated ring")
	}
}

func TestSanitizationBeforeLogAndRing(t *testing.T) {
	var b Buffer
	b.Emit("events.subscribe_parsed", map[string]any{
		"peer_id": "peer_test", "method": "events/subscribe", "callback_host": "https://callback.example/private-callback-path?token=vat_private_token_canary",
		"argument_keys": []string{"types", "message-content-canary"}, "token_scopes": []string{"events:write", "whsec_secret_canary"},
		"reason": "message-content-canary", "secret": "whsec_secret_canary", "body": "message-content-canary", "authorization": "Bearer vat_private_token_canary",
		"user_agent": "Agent vat_private_token_canary https://callback.example/private-callback-path", "result": "ok",
	})
	records := b.Snapshot(Filter{})
	raw, _ := json.Marshal(records)
	for _, canary := range []string{"vat_private_token_canary", "whsec_secret_canary", "private-callback-path", "message-content-canary"} {
		if strings.Contains(string(raw), canary) {
			t.Fatalf("ring leaked %s", canary)
		}
	}
	if records[0].Fields["callback_host"] != "callback.example" || records[0].Fields["reason"] != "unknown" {
		t.Fatalf("sanitized metadata: %s", raw)
	}
	b.Emit("message-content-canary", map[string]any{"result": "ok"})
	if len(b.Snapshot(Filter{})) != 1 {
		t.Fatal("unallowlisted record kind admitted")
	}
}

func TestBodyNamesAreFixedVocabulary(t *testing.T) {
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	var b Buffer
	b.Emit("mcp.request", map[string]any{"event_name": "message-content-canary", "tool_name": "whsec_secret_canary", "delivery_mode": "/private-callback-path", "method": "unknown/custom-method"})
	rec := b.Snapshot(Filter{})[0]
	raw, _ := json.Marshal(rec)
	for _, canary := range []string{"message-content-canary", "whsec_secret_canary", "/private-callback-path"} {
		if strings.Contains(string(raw), canary) || strings.Contains(logs.String(), canary) {
			t.Fatalf("untrusted body name leaked %s", canary)
		}
	}
	for _, key := range []string{"event_name", "tool_name", "delivery_mode"} {
		if rec.Fields[key] != "unknown" {
			t.Errorf("%s was not scrubbed: %v", key, rec.Fields[key])
		}
	}
	if rec.Fields["method"] != "unknown/custom-method" {
		t.Error("unknown RPC method must remain identifiable")
	}
	b.Emit("mcp.request", map[string]any{"event_name": "vecnet.message", "tool_name": "vecnet_send", "delivery_mode": "webhook", "method": "vat_secret_method_canary"})
	known := b.Snapshot(Filter{Limit: 1})[0]
	if known.Fields["event_name"] != "vecnet.message" || known.Fields["tool_name"] != "vecnet_send" || known.Fields["delivery_mode"] != "webhook" {
		t.Fatalf("known vocabulary: %+v", known)
	}
	if strings.Contains(known.Fields["method"].(string), "vat_secret_method_canary") {
		t.Error("method token not redacted")
	}
}

func TestAfterPagesUnreadOldestFirst(t *testing.T) {
	var b Buffer
	for i := 0; i < 8; i++ {
		b.Emit("mcp.request", map[string]any{"method": "ping"})
	}
	initial := b.Snapshot(Filter{Limit: 2})
	if initial[0].Sequence != 7 || initial[1].Sequence != 8 {
		t.Fatalf("initial latest records: %+v", initial)
	}
	var sequences []uint64
	after := uint64(1)
	for {
		page := b.Snapshot(Filter{After: after, Limit: 2})
		if len(page) == 0 {
			break
		}
		for _, rec := range page {
			sequences = append(sequences, rec.Sequence)
			after = rec.Sequence
		}
	}
	if fmt.Sprint(sequences) != "[2 3 4 5 6 7 8]" {
		t.Fatalf("unread page gaps: %v", sequences)
	}
}

func TestCallbackHostKeepsOrdinaryLetters(t *testing.T) {
	for _, name := range []string{"chatgpt.com", "events.openai.com", "return.example"} {
		if got := host(name); got != name {
			t.Errorf("host(%q)=%q", name, got)
		}
	}
	for _, name := range []string{"host.example/private-path", "host.example\tsecret", "host.example\\private-path"} {
		if got := host(name); got != "" {
			t.Errorf("unsafe host(%q)=%q", name, got)
		}
	}
}

func TestStableSubscriptionFailureReasonsPreserved(t *testing.T) {
	for _, reason := range []string{"not_linked", "not_your_peer", "peer_required", "peer_disabled", "too_many"} {
		fields := sanitize(map[string]any{"reason": reason})
		if fields["reason"] != reason {
			t.Errorf("reason %q scrubbed to %v", reason, fields["reason"])
		}
	}
}

func TestExplicitZeroCursorReadsFirstUnreadPage(t *testing.T) {
	var b Buffer
	if len(b.Snapshot(Filter{Limit: 2})) != 0 {
		t.Fatal("new ring not empty")
	}
	for i := 0; i < 5; i++ {
		b.Emit("mcp.request", map[string]any{"method": "ping"})
	}
	page := b.Snapshot(Filter{After: 0, AfterSet: true, Limit: 2})
	if len(page) != 2 || page[0].Sequence != 1 || page[1].Sequence != 2 {
		t.Fatalf("zero cursor skipped unread records: %+v", page)
	}
	initial := b.Snapshot(Filter{Limit: 2})
	if initial[0].Sequence != 4 || initial[1].Sequence != 5 {
		t.Fatalf("initial snapshot not latest records: %+v", initial)
	}
}
