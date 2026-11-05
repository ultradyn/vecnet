package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/XertroV/vecnet/internal/auth"
)

type legacyTestAuth struct{ p Principal }

func (a legacyTestAuth) Authenticate(context.Context, *http.Request) (Principal, error) {
	return a.p, nil
}

func TestLegacySessionsSwitch(t *testing.T) {
	for _, mode := range []string{"", "on", "off-for-oauth", "off"} {
		for _, oauth := range []bool{false, true} {
			t.Run(mode+map[bool]string{false: "/bearer", true: "/oauth"}[oauth], func(t *testing.T) {
				p := Principal{Caller: auth.Caller{Kind: "peer", PeerID: "peer_test"}}
				if oauth {
					p.OAuth = &OAuthInfo{Scopes: []string{"vecnet", "events:read", "events:write"}}
				}
				s := New(nil, legacyTestAuth{p}, "https://hub.example")
				s.LegacySessions = mode
				invoke := func(method string, params map[string]any, modern bool) *httptest.ResponseRecorder {
					body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
					r := httptest.NewRequest("POST", "https://hub.example/mcp", bytes.NewReader(body))
					r.Header.Set("Content-Type", "application/json")
					r.Header.Set("Accept", "application/json, text/event-stream")
					if modern {
						r.Header.Set("MCP-Protocol-Version", ProtoCurrent)
					}
					w := httptest.NewRecorder()
					s.ServeHTTP(w, r)
					return w
				}
				denied := mode == "off" || mode == "off-for-oauth" && oauth
				w := invoke("initialize", map[string]any{"protocolVersion": ProtoLegacy}, false)
				if denied {
					var reply struct{ Error RPCError }
					_ = json.Unmarshal(w.Body.Bytes(), &reply)
					if w.Code != 404 || reply.Error.Code != ErrMethodNotFound || w.Header().Get(hdrSessionID) != "" {
						t.Fatalf("initialize refusal %d %s", w.Code, w.Body.String())
					}
				} else if w.Code != 200 || w.Header().Get(hdrSessionID) == "" {
					t.Fatalf("initialize %d %s", w.Code, w.Body.String())
				}
				w = invoke("server/discover", map[string]any{"_meta": map[string]any{"io.modelcontextprotocol/protocolVersion": ProtoCurrent}}, true)
				if w.Code != 200 || w.Header().Get(hdrSessionID) != "" {
					t.Fatalf("discover %d %s", w.Code, w.Body.String())
				}
				w = invoke("unknown/method", nil, true)
				if w.Code != 404 {
					t.Fatalf("unknown current method status %d", w.Code)
				}
			})
		}
	}
}

func TestParseLegacySessions(t *testing.T) {
	for _, value := range []string{"", "on", "off-for-oauth", "off"} {
		if _, err := ParseLegacySessions(value); err != nil {
			t.Fatal(err)
		}
	}
	for _, value := range []string{"false", "OFF", " off", "on "} {
		if _, err := ParseLegacySessions(value); err == nil {
			t.Fatalf("accepted %q", value)
		}
	}
}
