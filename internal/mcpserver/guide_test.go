package mcpserver_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/XertroV/vecnet/internal/auth"
	venv "github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/mcpserver"
	"github.com/XertroV/vecnet/internal/testhub"
)

type guideWire struct {
	CallerKind string `json:"caller_kind"`
	Topic      string `json:"topic"`
	Subscribe  *struct {
		Event        string         `json:"event"`
		Args         map[string]any `json:"args"`
		AllowedTypes []string       `json:"allowed_types"`
	} `json:"subscribe"`
	Steps []string `json:"steps"`
	Text  string   `json:"text"`
}

// guideAuth exercises both OAuth caller shapes without token plumbing.
type guideAuth struct{ principal mcpserver.Principal }

func (a guideAuth) Authenticate(context.Context, *http.Request) (mcpserver.Principal, error) {
	return a.principal, nil
}

func TestGuide(t *testing.T) {
	env := testhub.Start(t)
	_, peerSecret := env.NewPeer("session", "guide peer")
	_, principalSecret := env.NewPrincipal("guide principal")
	dotID, _ := env.NewPeer("dot", "guide dot")
	owner, err := env.Hub.PeerPrincipal(context.Background(), dotID)
	if err != nil {
		t.Fatal(err)
	}
	oauthPrincipal := mcpserver.Principal{
		Caller: auth.Caller{Kind: "principal", PrincipalID: owner, PeerID: dotID, PeerKind: "dot"},
		OAuth:  &mcpserver.OAuthInfo{PrincipalID: owner, Expires: env.Now.Now().Add(time.Hour)},
	}
	dot := eventsFront(t, env, guideAuth{oauthPrincipal}, nil)
	oauthPeer := oauthPrincipal
	oauthPeer.Caller.Kind = "peer"
	dotPeer := eventsFront(t, env, guideAuth{oauthPeer}, nil)
	grokPrincipal := oauthPrincipal
	grokPrincipal.Caller.PeerKind = "grokbot"
	grok := eventsFront(t, env, guideAuth{grokPrincipal}, nil)
	for _, tc := range []struct {
		name, kind, secret string
		front              *testhub.Env
	}{
		{"OAuth grokbot", "grokbot", "oauth-token", grok},
		{"OAuth principal", "dot", "oauth-token", dot}, {"OAuth peer", "dot", "oauth-token", dotPeer},
		{"peer", "peer", peerSecret, env}, {"principal", "principal", principalSecret, env}, {"operator", "operator", env.OperatorSecret, env},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, era := range []string{mcpserver.ProtoCurrent, mcpserver.ProtoLegacy} {
				t.Run(era, func(t *testing.T) {
					headers := modern()
					if era == mcpserver.ProtoLegacy {
						headers = map[string]string{"Mcp-Session-Id": legacySession(t, tc.front, tc.secret), "Accept": "application/json"}
					}
					run := func(method string, params any) rpcResp {
						t.Helper()
						resp, body := do(t, tc.front, call{secret: tc.secret, headers: headers,
							body: map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params}})
						r := decodeRPC(t, body)
						if resp.StatusCode != http.StatusOK || r.Error != nil {
							t.Fatalf("%s: HTTP %d error %+v", method, resp.StatusCode, r.Error)
						}
						return r
					}
					var catalogue struct {
						Tools []map[string]any `json:"tools"`
					}
					r := run("tools/list", nil)
					if err := json.Unmarshal(r.Result, &catalogue); err != nil {
						t.Fatal(err)
					}
					var entry map[string]any
					for _, tool := range catalogue.Tools {
						if tool["name"] == "vecnet_guide" {
							entry = tool
						}
					}
					if entry == nil {
						t.Fatal("tools/list missing vecnet_guide")
					}
					ann, _ := entry["annotations"].(map[string]any)
					if ann["readOnlyHint"] != true || ann["openWorldHint"] != false || ann["destructiveHint"] != false {
						t.Fatalf("guide annotations: %v", ann)
					}
					if !strings.HasPrefix(entry["description"].(string), "Call this first:") {
						t.Fatalf("guide description: %v", entry["description"])
					}
					output, ok := entry["outputSchema"]
					if !ok {
						t.Fatal("guide missing outputSchema")
					}
					compiler := jsonschema.NewCompiler()
					if err := compiler.AddResource("urn:guide-output", output); err != nil {
						t.Fatal(err)
					}
					outputSchema, err := compiler.Compile("urn:guide-output")
					if err != nil {
						t.Fatal(err)
					}
					var start guideWire
					for _, topic := range []string{"", "start", "events", "replying", "etiquette"} {
						args := map[string]any{}
						if topic != "" {
							args["topic"] = topic
						}
						r := run("tools/call", map[string]any{"name": "vecnet_guide", "arguments": args})
						var result toolResult
						if err := json.Unmarshal(r.Result, &result); err != nil {
							t.Fatal(err)
						}
						var got guideWire
						if err := json.Unmarshal(result.StructuredContent, &got); err != nil {
							t.Fatal(err)
						}
						value, err := jsonschema.UnmarshalJSON(bytes.NewReader(result.StructuredContent))
						if err != nil {
							t.Fatal(err)
						}
						if err := venv.ValidateValue(outputSchema, value); err != nil {
							t.Fatalf("guide output schema: %v", err)
						}
						wantTopic := topic
						if wantTopic == "" {
							wantTopic = "start"
						}
						if result.IsError || got.CallerKind != tc.kind || got.Topic != wantTopic || len(got.Steps) == 0 || len(result.Content) != 1 || result.Content[0].Text != got.Text {
							t.Fatalf("guide result: %s", r.Result)
						}
						for _, step := range got.Steps {
							if !strings.Contains(got.Text, step) {
								t.Fatalf("text lacks step %q", step)
							}
						}
						if tc.kind == "dot" {
							if got.Subscribe == nil || got.Subscribe.Event != "vecnet.message" || len(got.Subscribe.Args) != 0 || !reflect.DeepEqual(got.Subscribe.AllowedTypes, []string{"delivery", "receipt", "thread_updated", "approval_expired"}) {
								t.Fatalf("dot subscribe: %+v", got.Subscribe)
							}
							if wantTopic == "start" || wantTopic == "events" {
								for _, needle := range []string{
									"You, the model, cannot subscribe. There is no model tool for it. ChatGPT's client does it when the user asks.",
									"no client subscribe action is exposed to the model",
									"confirm the vecnet.message event appears beside the tools; if it does not, Rescan",
									"Work chat (web, or desktop with Work + Cloud) or a dot",
									"Subscribe to the vecnet.message event from the vecnet plugin with no arguments, and when an event arrives, read it and reply via vecnet.",
									"Workspace controls for plugins and event-triggered tasks must allow it",
									"Call vecnet_subscriptions with no arguments",
									"If its subscriptions list is empty, the subscription is not active",
									"tell the user to follow the plugin-page, Rescan and Work chat or dot steps above",
									"use vecnet_inbox meanwhile",
									"Never claim you subscribed",
									"Handle every event on the page",
								} {
									if !strings.Contains(got.Text, needle) {
										t.Errorf("dot %s guide lacks %q: %s", wantTopic, needle, got.Text)
									}
								}
							}
						} else if got.Subscribe != nil || strings.Contains(got.Text, "vecnet.message") {
							t.Fatalf("%s has dot subscription guidance: %s", tc.kind, got.Text)
						}
						if tc.kind == "grokbot" {
							for _, forbidden := range []string{"ChatGPT", "Work chat", "Cloud", "Rescan", "plugin page"} {
								if strings.Contains(got.Text, forbidden) {
									t.Errorf("grokbot guide contains %q", forbidden)
								}
							}
						}
						if topic == "" {
							start = got
						}
						if topic == "start" && !reflect.DeepEqual(got, start) {
							t.Fatal("default topic differs from start")
						}
						prompt := run("prompts/get", map[string]any{"name": "vecnet_setup", "arguments": args})
						var p struct {
							Messages []struct {
								Role    string                      `json:"role"`
								Content struct{ Type, Text string } `json:"content"`
							} `json:"messages"`
						}
						if err := json.Unmarshal(prompt.Result, &p); err != nil {
							t.Fatal(err)
						}
						if len(p.Messages) != 1 || p.Messages[0].Role != "user" || p.Messages[0].Content.Type != "text" || p.Messages[0].Content.Text != got.Text {
							t.Fatalf("prompt differs: %s", prompt.Result)
						}
					}
					needles := []string{"vecnet_reply", "message_id", "wake_idle", "reply-", "delivery_id", "idempotency_conflict", "vecnet_receipt", "processed", "untrusted", "vecnet_thread", "recipients", "content", "vecnet_ack", "accepted", "human", "read", "failed", "at-least-once", "expires_at", "durably", "hold", "working", "unknown"}
					if tc.kind == "dot" {
						needles = append(needles, "data.data.message.content.text", "data.preview", "vecnet_inbox", "refreshBefore", "id", "peer_id", "vecnet_whoami", "not a model tool", "Work chat", "Cloud", "Rescan", "plugin page", "natural language", "events/subscribe", "vecnet_subscriptions", "Never claim you subscribed")
					} else {
						needles = append(needles, "vecnet_inbox", "next_cursor", "vecnet_ack", "notifications/resources/updated", "vecnet_presence_subscribe", "cursor_purged", "oldest_cursor", "cursor for other inbox events")
					}
					if tc.kind == "grokbot" {
						needles = append(needles, "webhook-triggered Grok Bot routine", "wake hint", "drain every page")
					}
					for _, needle := range needles {
						if !strings.Contains(start.Text, needle) {
							t.Errorf("guide lacks %q: %s", needle, start.Text)
						}
					}
					if tc.kind == "principal" && !strings.Contains(start.Text, "as") {
						t.Error("principal guide lacks acting peer hint")
					}
					if tc.kind == "dot" && era == mcpserver.ProtoCurrent {
						t.Logf("sample_dot_base64=%s", base64.StdEncoding.EncodeToString([]byte(start.Text)))
					}
				})
			}
		})
	}
}

func TestGuideDiscoveryAndValidation(t *testing.T) {
	env := testhub.Start(t)
	_, secret := env.NewPeer("session", "guide")
	if !strings.Contains(mcpserver.Instructions, "vecnet_guide") {
		t.Error("Instructions must point to vecnet_guide")
	}
	for _, method := range []string{"server/discover", "initialize"} {
		params := map[string]any{"_meta": map[string]any{"protocolVersion": mcpserver.ProtoCurrent}}
		if method == "initialize" {
			params = map[string]any{"protocolVersion": mcpserver.ProtoLegacy}
		}
		_, r := rpc(t, env, secret, method, params)
		var result struct {
			Capabilities map[string]any `json:"capabilities"`
			Instructions string         `json:"instructions"`
		}
		if r.Error != nil || json.Unmarshal(r.Result, &result) != nil {
			t.Fatalf("%s: %s %+v", method, r.Result, r.Error)
		}
		if result.Capabilities["prompts"] == nil || !strings.Contains(result.Instructions, "vecnet_guide") {
			t.Fatalf("%s: %s", method, r.Result)
		}
	}
	_, r := rpc(t, env, secret, "prompts/list", nil)
	var listed struct {
		Prompts []struct {
			Name      string `json:"name"`
			Arguments []struct {
				Name     string `json:"name"`
				Required bool   `json:"required"`
			} `json:"arguments"`
		} `json:"prompts"`
	}
	if r.Error != nil || json.Unmarshal(r.Result, &listed) != nil || len(listed.Prompts) != 1 {
		t.Fatalf("prompts/list: %s %+v", r.Result, r.Error)
	}
	if listed.Prompts[0].Name != "vecnet_setup" || len(listed.Prompts[0].Arguments) != 1 || listed.Prompts[0].Arguments[0].Name != "topic" || listed.Prompts[0].Arguments[0].Required {
		t.Fatalf("prompts/list: %s", r.Result)
	}
	for _, args := range []any{map[string]any{"topic": "unknown"}, map[string]any{"topic": ""}, map[string]any{"caller_kind": "dot"}, map[string]any{"topic": 1}, map[string]any{"topic": nil}, map[string]any{"extra": true}, []any{}} {
		for _, method := range []string{"tools/call", "prompts/get"} {
			name := "vecnet_guide"
			if method == "prompts/get" {
				name = "vecnet_setup"
			}
			_, r := rpc(t, env, secret, method, map[string]any{"name": name, "arguments": args})
			if r.Error == nil || r.Error.Code != mcpserver.ErrInvalidParams {
				t.Errorf("%s %v: want -32602, got %s %+v", method, args, r.Result, r.Error)
			}
		}
	}
	for _, method := range []string{"tools/call", "prompts/get"} {
		name := "vecnet_guide"
		if method == "prompts/get" {
			name = "vecnet_setup"
		}
		_, r := rpc(t, env, secret, method, map[string]any{"name": name})
		if r.Error != nil {
			t.Errorf("%s missing optional arguments: %+v", method, r.Error)
		}
	}
	_, r = rpc(t, env, secret, "prompts/get", map[string]any{"name": "unknown"})
	if r.Error == nil || r.Error.Code != mcpserver.ErrInvalidParams {
		t.Errorf("unknown prompt: %+v", r.Error)
	}
}

// Run the published examples through the real MCP tools, replacing only the
// explicitly marked ids. This catches examples that look plausible but cannot
// actually start a thread, post to it, or answer its message.
func TestGuideMessagingExamples(t *testing.T) {
	env := testhub.Start(t)
	_, sender := env.NewPeer("session", "sender")
	peer, recipient := env.NewPeer("session", "recipient")
	var guide guideWire
	_, r := rpc(t, env, sender, "tools/call", map[string]any{"name": "vecnet_guide", "arguments": map[string]any{"topic": "replying"}})
	var guideTool toolResult
	if r.Error != nil || json.Unmarshal(r.Result, &guideTool) != nil || json.Unmarshal(guideTool.StructuredContent, &guide) != nil {
		t.Fatalf("guide: %s %+v", r.Result, r.Error)
	}
	var first sendResult
	count := 0
	for _, part := range strings.Split(guide.Text, "```json\n")[1:] {
		raw := strings.SplitN(part, "```", 2)[0]
		raw = strings.NewReplacer("<peer_id>", peer, "<thread_id>", first.ThreadID,
			"<message_id>", first.MessageID, "<delivery_id>", firstDeliveryID(first)).Replace(raw)
		var params struct {
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		}
		if err := json.Unmarshal([]byte(raw), &params); err != nil {
			t.Fatalf("invalid guide example: %v", err)
		}
		var result sendResult
		secret := sender
		if params.Name == "vecnet_reply" {
			secret = recipient
		}
		toolOK(t, env, secret, params.Name, params.Arguments, &result)
		if count == 0 {
			if params.Name != "vecnet_send" || len(result.Deliveries) != 1 || result.Deliveries[0].Recipient != peer {
				t.Fatalf("new-thread example: %+v", result)
			}
			first = result
		} else if result.ThreadID != first.ThreadID {
			t.Fatalf("example %d left the thread: %+v", count, result)
		}
		count++
	}
	if count != 3 {
		t.Fatalf("want new-thread send, existing-thread send and reply examples, got %d", count)
	}
}

func firstDeliveryID(result sendResult) string {
	if len(result.Deliveries) == 0 {
		return ""
	}
	return result.Deliveries[0].DeliveryID
}

func TestGuideSchemaErrorRecovery(t *testing.T) {
	env := testhub.Start(t)
	_, secret := env.NewPeer("session", "sender")
	for _, name := range []string{"vecnet_send", "vecnet_reply"} {
		for _, args := range []any{
			map[string]any{"to": "peer_00000000000000000000000000", "text": "private-message-canary", "priority": "hold", "idempotency_key": "bad-shape"},
			map[string]any{"priority": "hold", "idempotency_key": "missing-address", "content": map[string]any{"text": "hello"}},
		} {
			_, r := rpc(t, env, secret, "tools/call", map[string]any{"name": name, "arguments": args})
			if r.Error == nil || r.Error.Code != mcpserver.ErrInvalidParams {
				t.Fatalf("%s must still reject bad arguments: %+v", name, r)
			}
			if !strings.Contains(r.Error.Message, "vecnet_guide") || !strings.Contains(r.Error.Message, "replying") || !strings.Contains(r.Error.Message, "examples") {
				t.Errorf("%s lacks actionable schema feedback: %s", name, r.Error.Message)
			}
			if strings.Contains(r.Error.Message, "private-message-canary") {
				t.Error("schema hint exposed message content")
			}
			var data map[string]any
			if json.Unmarshal(r.Error.Data, &data) != nil || data["path"] == nil {
				t.Fatalf("schema feedback lost data.path: %s", r.Error.Data)
			}
		}
	}
}
