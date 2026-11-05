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

func TestOAuthRedirectMetadataSanitizedBeforeBothDestinations(t *testing.T) {
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	var b Buffer
	b.Emit("oauth.redirect_refused", map[string]any{
		"redirect_uri": "https://user:USERINFO_CANARY@refused.example:8443/callback/%76at_path_canary?QUERY_CANARY=one#FRAGMENT_CANARY",
		"client_name":  "Connector\n\twhsec_name_canary https://user:pass@example.com/private-name-path?NAME_QUERY_CANARY=one",
		"endpoint":     "/oauth/authorize", "code_verifier": "VERIFIER_CANARY", "state": "STATE_CANARY", "client_secret": "CLIENT_SECRET_CANARY",
	})
	b.Emit("oauth.client_registered", map[string]any{
		"client_id_prefix": "dcr_12345678private-client-id-tail", "redirect_hosts": []string{"accepted.example:8443", "bad.example/private-host-path", "vat_host_canary"},
		"client_name": strings.Repeat("界", 100), "endpoint": "/oauth/register",
	})
	records := b.Snapshot(Filter{})
	if len(records) != 2 {
		t.Fatalf("got %d records, want 2", len(records))
	}
	raw, _ := json.Marshal(records)
	for i, canary := range []string{"USERINFO_CANARY", "QUERY_CANARY", "FRAGMENT_CANARY", "vat_path_canary", "whsec_name_canary", "private-name-path", "NAME_QUERY_CANARY", "VERIFIER_CANARY", "STATE_CANARY", "CLIENT_SECRET_CANARY", "private-client-id-tail", "private-host-path", "vat_host_canary"} {
		if strings.Contains(string(raw)+logs.String(), canary) {
			t.Errorf("OAuth metadata leaked canary %d", i)
		}
	}
	refused := records[0].Fields
	if refused["scheme"] != "https" || refused["host"] != "refused.example:8443" || !strings.HasPrefix(refused["path"].(string), "/callback/") || refused["endpoint"] != "/oauth/authorize" {
		t.Error("refused redirect lost required components")
	}
	if strings.ContainsAny(refused["client_name"].(string), "\n\t") {
		t.Error("client name retained controls")
	}
	if records[1].Fields["client_id_prefix"] != "dcr_12345678" || len([]rune(records[1].Fields["client_name"].(string))) != 80 {
		t.Error("registration metadata was not bounded")
	}
	lines := strings.Split(strings.TrimSpace(logs.String()), "\n")
	if len(lines) != len(records) {
		t.Fatalf("got %d INFO lines, want %d", len(lines), len(records))
	}
	for i, line := range lines {
		var logged map[string]any
		if err := json.Unmarshal([]byte(line), &logged); err != nil {
			t.Fatal(err)
		}
		if logged["level"] != "INFO" || logged["msg"] != records[i].Kind {
			t.Error("wrong INFO level or record kind")
		}
		delete(logged, "time")
		delete(logged, "level")
		delete(logged, "msg")
		logFields, _ := json.Marshal(logged)
		ringFields, _ := json.Marshal(records[i].Fields)
		if string(logFields) != string(ringFields) {
			t.Error("ring and INFO fields differ")
		}
	}
	// Callback paths remain private for all other record kinds.
	b.Emit("events.delivery", map[string]any{"redirect_uri": "https://example.com/private-callback", "path": "/private-callback", "redirect_hosts": []string{"example.com"}, "endpoint": "/oauth/register"})
	if len(b.Snapshot(Filter{Limit: 1})[0].Fields) != 0 {
		t.Error("OAuth metadata exception leaked into Events")
	}
}

func TestOAuthClientNameDropsMalformedURLs(t *testing.T) {
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	var b Buffer
	for _, kind := range []string{"oauth.redirect_refused", "oauth.client_registered"} {
		for _, name := range []string{
			"Connector https://u:PASS_CANARY@x.example/%zz?QUERY_CANARY=1",
			"Connector https://x.example:bad/private-path",
			"Connector https:///missing-host?MISSING_HOST_CANARY=1",
		} {
			b.Emit(kind, map[string]any{"client_name": name})
		}
	}
	raw, _ := json.Marshal(b.Snapshot(Filter{}))
	for i, canary := range []string{"PASS_CANARY", "QUERY_CANARY", "private-path", "MISSING_HOST_CANARY"} {
		if strings.Contains(string(raw)+logs.String(), canary) {
			t.Errorf("malformed client-name URL leaked canary %d", i)
		}
	}
}
