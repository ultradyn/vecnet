package mcpserver_test

import (
	"encoding/json"
	"fmt"
	"maps"
	"reflect"
	"strings"
	"testing"

	"github.com/XertroV/vecnet/internal/mcpdiag"
	"github.com/XertroV/vecnet/internal/mcpserver"
	"github.com/XertroV/vecnet/internal/testhub"
)

func TestToolArgumentMetadataIsDiscarded(t *testing.T) {
	env := testhub.Start(t)
	peer, secret := env.NewPeer("session", "metadata sender")
	capture, logs := captureDiag(t)
	metadata := map[string]any{"session": "private-session-canary", "window": "private-window-canary", "trace": []any{"private-trace-canary"}}
	for _, tool := range []string{"vecnet_whoami", "vecnet_inbox", "vecnet_threads", "vecnet_subscriptions"} {
		t.Run(tool, func(t *testing.T) {
			toolOK(t, env, secret, tool, map[string]any{"_meta": metadata}, nil)
		})
	}
	// Writes also discard protocol metadata before prechecks and handling.
	var sent sendResult
	toolOK(t, env, secret, "vecnet_send", map[string]any{
		"idempotency_key": "meta-send", "priority": "hold", "recipients": []string{peer},
		"content": map[string]any{"text": "hello"}, "_meta": metadata,
		"metadata": map[string]any{"_meta": "application-data-canary"},
	}, &sent)
	var history struct {
		Messages []map[string]any `json:"messages"`
	}
	toolOK(t, env, secret, "vecnet_thread", map[string]any{"id": sent.ThreadID, "_meta": metadata}, &history)
	if len(history.Messages) != 1 || history.Messages[0]["_meta"] != nil || history.Messages[0]["metadata"].(map[string]any)["_meta"] != "application-data-canary" {
		t.Fatalf("stored application metadata = %v", history.Messages)
	}
	// An unrelated refusal must not echo protocol metadata either.
	_, response := rpc(t, env, secret, "tools/call", map[string]any{
		"name": "vecnet_inbox", "arguments": map[string]any{"_meta": metadata, "limit": map[string]any{"private-field-canary": "private-value-canary"}},
	})
	if response.Error == nil || string(response.Error.Data) != `{"path":"/limit"}` {
		t.Fatalf("invalid limit = %+v, want path /limit only", response.Error)
	}
	raw, _ := json.Marshal(response)
	ring, _ := json.Marshal(mcpdiag.Snapshot(mcpdiag.Filter{Peer: peer}))
	for _, value := range []string{"private-session-canary", "private-window-canary", "private-trace-canary", "private-field-canary", "private-value-canary"} {
		if strings.Contains(string(raw), value) || strings.Contains(logs.String(), value) || strings.Contains(string(ring), value) {
			t.Errorf("response or diagnostics echoed %s", value)
		}
	}
	found := false
	for _, record := range capture.records {
		if fields := diagFields(record); fields["tool_arguments_meta_present"] == true {
			found = true
		}
	}
	if !found || !strings.Contains(string(ring), `"tool_arguments_meta_present":true`) {
		t.Error("missing presence-only metadata diagnostic")
	}
}

func TestToolValidationErrorsDoNotEchoStrings(t *testing.T) {
	env := testhub.Start(t)
	peer, secret := env.NewPeer("session", "invalid strings")
	_, logs := captureDiag(t)
	for _, tc := range []struct{ tool, field string }{
		{"vecnet_threads", "since"}, {"vecnet_thread", "id"},
	} {
		for _, padding := range []string{"", strings.Repeat("long-canary-", 12)} {
			value := "private-value-canary-" + padding + "private-tail-canary"
			response, raw := do(t, env, call{secret: secret, headers: modern(), body: map[string]any{
				"jsonrpc": "2.0", "id": 1, "method": "tools/call",
				"params": map[string]any{"name": tc.tool, "arguments": map[string]any{tc.field: value}},
			}})
			if response.StatusCode != 200 {
				t.Fatalf("%s: HTTP %d", tc.tool, response.StatusCode)
			}
			// Compare the whole response, including the message and all data.
			// Prefix/suffix checks below also catch attempted truncation to 64 bytes.
			var got, want any
			if err := json.Unmarshal(raw, &got); err != nil {
				t.Fatal(err)
			}
			expected := fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"error":{"code":-32602,"message":%q,"data":{"path":%q}}}`, "invalid arguments for "+tc.tool+": value does not satisfy the schema or hub constraint", "/"+tc.field)
			if err := json.Unmarshal([]byte(expected), &want); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("%s: unexpected complete error response: %s", tc.tool, raw)
			}
			ring, _ := json.Marshal(mcpdiag.Snapshot(mcpdiag.Filter{Peer: peer}))
			for _, canary := range []string{"private-value-canary", "long-canary", "private-tail-canary"} {
				if strings.Contains(string(raw), canary) || strings.Contains(logs.String(), canary) || strings.Contains(string(ring), canary) {
					t.Errorf("%s: response or diagnostics echoed %s", tc.tool, canary)
				}
			}
		}
	}
}

func TestToolValidationErrorsDoNotEchoCompositeValues(t *testing.T) {
	env := testhub.Start(t)
	_, secret := env.NewPeer("session", "invalid objects")
	for _, value := range []any{
		map[string]any{"private-field-canary": "private-object-canary"},
		[]any{"private-array-canary", map[string]any{"nested": "private-nested-canary"}},
	} {
		for _, tool := range []struct{ name, field string }{
			{"vecnet_inbox", "limit"}, {"vecnet_guide", "topic"}, {"vecnet_threads", "since"},
		} {
			_, response := rpc(t, env, secret, "tools/call", map[string]any{"name": tool.name, "arguments": map[string]any{tool.field: value}})
			if response.Error == nil {
				t.Fatalf("%s accepted composite %s", tool.name, tool.field)
			}
			raw, _ := json.Marshal(response.Error)
			for _, forbidden := range []string{"original_value", "private-field-canary", "private-object-canary", "private-array-canary", "private-nested-canary"} {
				if strings.Contains(string(raw), forbidden) {
					t.Errorf("%s error echoed %s", tool.name, forbidden)
				}
			}
		}
	}
}

// R2 adds conditional schemas and capability arguments. Refusals must keep
// rejected values out of the entire wire response and both diagnostic sinks.
func TestR2BlobValidationErrorsDoNotEchoCapabilities(t *testing.T) {
	env := testhub.Start(t)
	peer, secret := env.NewPeer("session", "private blob arguments")
	_, logs := captureDiag(t)
	const marker = "private-transfer-canary"
	const capability = "https://example.invalid/object?X-Amz-Signature=" + marker
	grant := map[string]any{
		"phase": "grant", "size": 1, "sha256": strings.Repeat("a", 64),
		"media_type": "text/plain", "idempotency_key": "private-grant",
	}
	cases := []struct {
		tool string
		args map[string]any
	}{
		{"vecnet_blob_put", map[string]any{"phase": capability}},
		{"vecnet_blob_get", map[string]any{"sha256": capability}},
		{"vecnet_blob_status", map[string]any{"sha256": capability}},
		{"vecnet_blob_get", map[string]any{"sha256": strings.Repeat("a", 64), "blob_id": map[string]any{"url": capability}}},
		{"vecnet_blob_status", map[string]any{"sha256": strings.Repeat("a", 64), "blob_id": []any{capability}}},
		{"vecnet_blob_put", map[string]any{"phase": "complete", "blob_id": capability}},
		{"vecnet_blob_put", map[string]any{capability: "unexpected"}},
	}
	for _, field := range []string{"size", "sha256"} {
		args := map[string]any{}
		for k, v := range grant {
			args[k] = v
		}
		args[field] = capability
		cases = append(cases, struct {
			tool string
			args map[string]any
		}{"vecnet_blob_put", args})
	}
	for i, tc := range cases {
		_, raw := do(t, env, call{secret: secret, headers: modern(), body: map[string]any{
			"jsonrpc": "2.0", "id": 1, "method": "tools/call",
			"params": map[string]any{"name": tc.tool, "arguments": tc.args},
		}})
		response := decodeRPC(t, raw)
		if response.Error == nil && !strings.Contains(string(response.Result), `"isError":true`) {
			t.Fatalf("case %d accepted invalid capability argument", i)
		}
		ring, err := json.Marshal(mcpdiag.Snapshot(mcpdiag.Filter{Peer: peer}))
		if err != nil {
			t.Fatal(err)
		}
		for _, forbidden := range []string{marker, "X-Amz-Signature", "example.invalid"} {
			if strings.Contains(string(raw), forbidden) || strings.Contains(logs.String(), forbidden) || strings.Contains(string(ring), forbidden) {
				t.Fatalf("case %d echoed capability data", i)
			}
		}
	}
}

func TestR2BlobUnknownPropertyNamesDoNotEchoCapabilities(t *testing.T) {
	env := testhub.Start(t)
	peer, secret := env.NewPeer("session", "private blob property names")
	_, logs := captureDiag(t)
	const capability = "https://private-property.invalid/object?X-Amz-Signature=property-canary&token=tail-canary"
	for _, tc := range []struct {
		name string
		args map[string]any
	}{
		{"vecnet_blob_get", map[string]any{"sha256": strings.Repeat("a", 64)}},
		{"vecnet_blob_status", map[string]any{"sha256": strings.Repeat("a", 64)}},
		{"vecnet_blob_put", map[string]any{
			"phase": "grant", "size": 1, "sha256": strings.Repeat("a", 64),
			"media_type": "text/plain", "idempotency_key": "unknown-property-grant",
		}},
		{"vecnet_blob_put", map[string]any{"phase": "complete", "blob_id": "blo_00000000000000000000000001"}},
	} {
		// Every surrounding request satisfies its schema. Only the unknown
		// property causes rejection; missing fields cannot mask its location.
		for _, key := range []string{capability, "private-property-canary", "0"} {
			args := maps.Clone(tc.args)
			args[key] = "unexpected"
			resp, raw := do(t, env, call{secret: secret, headers: modern(), body: map[string]any{
				"jsonrpc": "2.0", "id": 1, "method": "tools/call",
				"params": map[string]any{"name": tc.name, "arguments": args},
			}})
			response := decodeRPC(t, raw)
			if resp.StatusCode != 200 || response.Error == nil || response.Error.Code != mcpserver.ErrInvalidParams {
				t.Fatalf("%s did not reject unknown property with invalid params", tc.name)
			}
			var data struct {
				Path string `json:"path"`
			}
			if json.Unmarshal(response.Error.Data, &data) != nil || data.Path != "/<unknown>" {
				t.Fatalf("%s did not return the fixed unknown-property path", tc.name)
			}
			ring, err := json.Marshal(mcpdiag.Snapshot(mcpdiag.Filter{Peer: peer}))
			if err != nil {
				t.Fatal(err)
			}
			for _, sink := range []string{string(raw), logs.String(), string(ring)} {
				for _, forbidden := range []string{"private-property.invalid", "X-Amz-Signature", "property-canary", "tail-canary", "private-property-canary"} {
					if strings.Contains(sink, forbidden) {
						t.Fatalf("%s response or diagnostics retained private property name", tc.name)
					}
				}
			}
		}
	}
}
