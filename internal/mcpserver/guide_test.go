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
	for _, tc := range []struct {
		name, kind, secret string
		front              *testhub.Env
	}{
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
						} else if got.Subscribe != nil || strings.Contains(got.Text, "vecnet.message") {
							t.Fatalf("%s has dot subscription guidance: %s", tc.kind, got.Text)
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
					needles := []string{"vecnet_reply", "message_id", "wake_idle", "reply-", "delivery_id", "idempotency_conflict", "vecnet_receipt", "processed", "untrusted", "vecnet_thread"}
					if tc.kind == "dot" {
						needles = append(needles, "data.data.message.content.text", "data.preview", "vecnet_inbox", "refreshBefore", "id", "peer_id", "vecnet_whoami")
					} else {
						needles = append(needles, "vecnet_inbox", "next_cursor", "vecnet_ack", "notifications/resources/updated", "vecnet_presence_subscribe", "cursor_purged", "oldest_cursor")
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
		params := map[string]any{}
		if method == "initialize" {
			params["protocolVersion"] = mcpserver.ProtoLegacy
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
