package mcpserver_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/auth"
	"github.com/XertroV/vecnet/internal/logging"
	"github.com/XertroV/vecnet/internal/mcpserver"
	"github.com/XertroV/vecnet/internal/testhub"
	"github.com/XertroV/vecnet/internal/webhook"
)

// diagAuth keeps these transport tests independent of credentials and the store.
type diagAuth struct{ principal mcpserver.Principal }

type diagFailureAuth struct{}

func (diagFailureAuth) Authenticate(context.Context, *http.Request) (mcpserver.Principal, error) {
	return mcpserver.Principal{}, errors.New("auth-secret-canary")
}

func (a diagAuth) Authenticate(context.Context, *http.Request) (mcpserver.Principal, error) {
	return a.principal, nil
}

func TestMCPSubscribeDiagnostics(t *testing.T) {
	for _, tc := range []struct {
		name, verification, reason string
		code                       int64
	}{
		{"verified", "verified", "", 0}, {"cached", "cached", "", 0},
		{"challenge failed", "failed", "http_4xx", mcpserver.ErrCodeCallback},
		{"timeout", "failed", "timeout", mcpserver.ErrCodeCallback},
		{"egress refusal", "refused", "egress_refused", mcpserver.ErrCodeCallback},
		{"invalid arguments", "not_attempted", "invalid_params", mcpserver.ErrInvalidParams},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := testhub.Start(t)
			_, bearer := env.NewPeer("session", "diagnostic subscription")
			cb := testhub.NewCallback(t)
			if tc.name == "challenge failed" {
				cb.OnChallenge(func(_ string, w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusForbidden) })
			}
			if tc.name == "timeout" {
				release := make(chan struct{})
				t.Cleanup(func() { close(release) })
				cb.OnChallenge(func(_ string, _ http.ResponseWriter, r *http.Request) {
					select {
					case <-r.Context().Done():
					case <-release:
					}
				})
			}
			front := eventsFront(t, env, nil, func(s *mcpserver.Server) {
				s.EventsClient = env.Hub.EgressPolicy().Client(30 * time.Millisecond)
				if tc.name == "egress refusal" {
					s.Egress.AllowPrivate = false
				}
			})
			whsec := webhook.NewSecret()
			params := subParams(cb.URL+"?callback-query-canary", whsec, map[string]any{"arguments": map[string]any{"types": []string{"delivery"}}})
			if tc.name == "cached" {
				subscribeOK(t, front, bearer, params)
			}
			if tc.name == "invalid arguments" {
				params["arguments"].(map[string]any)["secret-key-canary"] = "argument-canary"
			}
			capture, output := captureDiag(t)
			rpc2026(t, front, bearer, "events/subscribe", params)
			var records []slog.Record
			for _, r := range capture.records {
				if r.Message == "mcp: request" {
					records = append(records, r)
				}
			}
			if len(records) != 1 {
				t.Fatalf("got %d request logs: %s", len(records), output)
			}
			f := diagFields(records[0])
			if f["event_name"] != "vecnet.message" || f["delivery_mode"] != "webhook" || f["callback_host"] != "127.0.0.1" ||
				f["verification"] != tc.verification || f["reason"] != tc.reason || f["code"] != tc.code || !reflect.DeepEqual(f["argument_keys"], []string{"types"}) {
				t.Errorf("subscribe diagnostics: %v", f)
			}
			for _, secret := range []string{bearer, whsec, "/hook/path-with-token", "callback-query-canary", "secret-key-canary", "argument-canary"} {
				if strings.Contains(output.String(), secret) {
					t.Errorf("diagnostics leaked canary: %s", output)
				}
			}
		})
	}
}

type diagCapture struct {
	slog.Handler
	records []slog.Record
}

func (c *diagCapture) Handle(ctx context.Context, r slog.Record) error {
	c.records = append(c.records, r.Clone())
	return c.Handler.Handle(ctx, r)
}

func captureDiag(t *testing.T) (*diagCapture, *bytes.Buffer) {
	t.Helper()
	var output bytes.Buffer
	capture := &diagCapture{Handler: logging.NewHandler(&output, slog.LevelInfo)}
	previous := slog.Default()
	slog.SetDefault(slog.New(capture))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return capture, &output
}

func diagFields(r slog.Record) map[string]any {
	fields := map[string]any{}
	r.Attrs(func(a slog.Attr) bool {
		fields[a.Key] = a.Value.Any()
		return true
	})
	return fields
}

func diagRequest(s *mcpserver.Server, method string, body any, headers map[string]string) *httptest.ResponseRecorder {
	raw, _ := json.Marshal(body)
	r := httptest.NewRequest(method, "https://hub.example/mcp", bytes.NewReader(raw))
	r.Header.Set("Content-Type", "application/json")
	// These opaque canaries do not match logging.Redact: omission must protect
	// cookies and session ids too, not just known vecnet secret prefixes.
	r.Header.Set("Authorization", "Bearer bearer-canary-do-not-log")
	r.Header.Set("Cookie", "login=cookie-canary-do-not-log")
	for name, value := range headers {
		r.Header.Set(name, value)
	}
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	return w
}

func TestMCPInitializeDiagnostics(t *testing.T) {
	for _, tc := range []struct {
		name string
		p    mcpserver.Principal
		kind string
	}{
		{"peer", mcpserver.Principal{Caller: auth.Caller{Kind: "peer", PeerID: "peer_diag"}}, "peer"},
		{"principal", mcpserver.Principal{Caller: auth.Caller{Kind: "principal"}}, "principal"},
		{"oauth", mcpserver.Principal{Caller: auth.Caller{Kind: "principal", PeerID: "peer_dot"}, OAuth: &mcpserver.OAuthInfo{}}, "oauth"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			capture, output := captureDiag(t)
			s := mcpserver.New(nil, diagAuth{tc.p}, "https://hub.example")
			body := initialize(1, "2024-11-05")
			body["params"].(map[string]any)["clientInfo"] = map[string]any{
				"name": strings.Repeat("界", 70), "version": strings.Repeat("版", 70),
				"private": "body-canary-do-not-log",
			}
			w := diagRequest(s, http.MethodPost, body, map[string]string{
				"Accept":         "application/json," + strings.Repeat("界", 110),
				"Origin":         "HTTPS://HUB.EXAMPLE:443",
				"Mcp-Session-Id": "session-canary-do-not-log",
			})
			if w.Code != http.StatusOK {
				t.Fatalf("initialize = %d: %s", w.Code, w.Body)
			}
			if len(capture.records) != 1 {
				t.Fatalf("initialize diagnostic records = %d, want 1; %s", len(capture.records), output)
			}
			r := capture.records[0]
			if r.Level != slog.LevelInfo {
				t.Errorf("initialize level = %s, want INFO", r.Level)
			}
			fields := diagFields(r)
			want := map[string]any{
				"method": "initialize", "result": "ok", "code": int64(0),
				"client_name": strings.Repeat("界", 64), "client_version": strings.Repeat("版", 64),
				"requested_protocol_version": "2024-11-05", "negotiated_protocol_version": mcpserver.ProtoLegacy,
				"origin_present": true, "accept": "application/json," + strings.Repeat("界", 83),
				"session_present": true, "caller_kind": tc.kind,
			}
			want["peer_id"] = tc.p.Caller.PeerID
			assertDiagFields(t, fields, want)
			assertDiagNoSecrets(t, output.String())
		})
	}
}

func assertDiagFields(t *testing.T, got, want map[string]any) {
	t.Helper()

	for name, value := range want {
		if got[name] != value {
			t.Errorf("diagnostic %s = %v, want %v", name, got[name], value)
		}
	}
}

func assertDiagNoSecrets(t *testing.T, output string) {
	t.Helper()
	for _, secret := range []string{"bearer-canary-do-not-log", "cookie-canary-do-not-log", "session-canary-do-not-log", "body-canary-do-not-log", "origin-user-canary", "origin-path-canary", "origin-query-canary"} {
		if strings.Contains(output, secret) {
			t.Errorf("diagnostics leaked %s: %s", secret, output)
		}
	}
}

func TestMCPRefusalDiagnostics(t *testing.T) {
	for _, tc := range []struct {
		name    string
		method  string
		body    any
		headers map[string]string
		status  int
		caller  string
		origin  string
	}{
		{"foreign origin", http.MethodPost, initialize(1, mcpserver.ProtoLegacy), map[string]string{"Origin": "HTTPS://origin-user-canary@CLIENT.EXAMPLE:443/origin-path-canary?origin-query-canary"}, 403, "unauthenticated", "https://client.example"},
		{"long origin", http.MethodPost, initialize(1, mcpserver.ProtoLegacy), map[string]string{"Origin": "https://" + strings.Repeat("a", 110) + ".example/path"}, 403, "unauthenticated", "https://" + strings.Repeat("a", 92)},
		{"invalid origin", http.MethodPost, initialize(1, mcpserver.ProtoLegacy), map[string]string{"Origin": "null"}, 403, "unauthenticated", ""},
		{"no version or session", http.MethodPost, map[string]any{"jsonrpc": "2.0", "id": 1, "method": "ping"}, nil, 400, "peer", ""},
		{"unsupported version", http.MethodPost, map[string]any{"jsonrpc": "2.0", "id": 1, "method": "ping"}, map[string]string{"MCP-Protocol-Version": "1900-01-01"}, 400, "peer", ""},
		{"unknown session", http.MethodPost, map[string]any{"jsonrpc": "2.0", "id": 1, "method": "ping"}, map[string]string{"Mcp-Session-Id": "session-canary-do-not-log"}, 404, "peer", ""},
		{"get unknown session", http.MethodGet, nil, map[string]string{"Mcp-Session-Id": "session-canary-do-not-log"}, 404, "peer", ""},
		{"delete unknown session", http.MethodDelete, nil, map[string]string{"Mcp-Session-Id": "session-canary-do-not-log"}, 404, "peer", ""},
		{"initialize accept", http.MethodPost, initialize(1, mcpserver.ProtoLegacy), map[string]string{"Accept": "text/plain"}, 406, "peer", ""},
		{"current accept", http.MethodPost, map[string]any{"jsonrpc": "2.0", "id": 1, "method": "ping"}, map[string]string{"MCP-Protocol-Version": mcpserver.ProtoCurrent, "Accept": "application/json"}, 406, "peer", ""},
		{"discover version mismatch", http.MethodPost, discover(1, "io.modelcontextprotocol/protocolVersion", mcpserver.ProtoCurrent), nil, 400, "peer", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			capture, output := captureDiag(t)
			s := mcpserver.New(nil, diagAuth{mcpserver.Principal{Caller: auth.Caller{Kind: "peer", PeerID: "peer_diag"}}}, "https://hub.example")
			w := diagRequest(s, tc.method, tc.body, tc.headers)
			if w.Code != tc.status {
				t.Fatalf("HTTP status = %d, want %d: %s", w.Code, tc.status, w.Body)
			}
			if len(capture.records) != 1 {
				t.Fatalf("refusal diagnostic records = %d, want 1; %s", len(capture.records), output)
			}
			r := capture.records[0]
			wantLevel := slog.LevelInfo
			if r.Level != wantLevel {
				t.Errorf("refusal level = %s, want %s", r.Level, wantLevel)
			}
			fields := diagFields(r)
			if fields["caller_kind"] != tc.caller {
				t.Errorf("caller_kind = %v, want %s", fields["caller_kind"], tc.caller)
			}
			if tc.caller == "unauthenticated" {
				if _, ok := fields["peer_id"]; ok {
					t.Error("Origin refusal must not authenticate just to log a peer id")
				}
			}
			if fields["origin"] != tc.origin && !(fields["origin"] == nil && tc.origin == "") {
				t.Errorf("origin = %v, want %s", fields["origin"], tc.origin)
			}
			assertDiagNoSecrets(t, output.String())
		})
	}
}

func TestMCPRefusalNegotiatedDiagnostics(t *testing.T) {
	capture, output := captureDiag(t)
	s := mcpserver.New(nil, diagAuth{mcpserver.Principal{Caller: auth.Caller{Kind: "peer", PeerID: "peer_diag"}}}, "https://hub.example")
	initialized := diagRequest(s, http.MethodPost, initialize(1, mcpserver.ProtoLegacy), nil)
	session := initialized.Header().Get("Mcp-Session-Id")
	for _, tc := range []struct {
		method     string
		headers    map[string]string
		status     int
		negotiated string
	}{
		{http.MethodPost, map[string]string{"MCP-Protocol-Version": mcpserver.ProtoCurrent, "Accept": "application/json"}, 406, mcpserver.ProtoCurrent},
		{http.MethodPost, map[string]string{"MCP-Protocol-Version": "1900-01-01", "Mcp-Session-Id": session}, 400, mcpserver.ProtoLegacy},
		{http.MethodPost, map[string]string{"Mcp-Session-Id": session, "Accept": "text/plain"}, 406, mcpserver.ProtoLegacy},
		{http.MethodGet, map[string]string{"Mcp-Session-Id": session, "Accept": "application/json"}, 406, mcpserver.ProtoLegacy},
	} {
		before := len(capture.records)
		w := diagRequest(s, tc.method, map[string]any{"jsonrpc": "2.0", "id": 2, "method": "ping"}, tc.headers)
		if w.Code != tc.status || len(capture.records) != before+1 {
			t.Fatalf("refusal: status=%d records=%d, want %d and %d", w.Code, len(capture.records), tc.status, before+1)
		}
		if got := diagFields(capture.records[before])["negotiated_protocol_version"]; got != tc.negotiated {
			t.Errorf("negotiated version = %v, want %s", got, tc.negotiated)
		}
	}
	if strings.Contains(output.String(), session) {
		t.Error("diagnostics leaked the real session id")
	}
}

func TestMCPAllowedForeignOriginDiagnostics(t *testing.T) {
	for _, method := range []string{"initialize", "ping"} {
		t.Run(method, func(t *testing.T) {
			capture, output := captureDiag(t)
			s := mcpserver.New(nil, diagAuth{mcpserver.Principal{Caller: auth.Caller{Kind: "peer", PeerID: "peer_diag"}}}, "https://hub.example")
			body := initialize(1, mcpserver.ProtoLegacy)
			if method == "ping" {
				body = map[string]any{"jsonrpc": "2.0", "id": 1, "method": "ping"}
			}
			w := diagRequest(s, http.MethodPost, body, map[string]string{
				"Origin": "https://chatgpt.com", "MCP-Protocol-Version": mcpserver.ProtoCurrent,
			})
			if w.Code != http.StatusOK {
				t.Fatalf("status = %d: %s", w.Code, w.Body)
			}
			if len(capture.records) != 1 {
				t.Fatalf("records = %d, want 1; %s", len(capture.records), output)
			}
			fields := diagFields(capture.records[0])
			if fields["origin"] != "https://chatgpt.com" || fields["origin_present"] != true {
				t.Errorf("foreign origin diagnostics: %v", fields)
			}
			assertDiagNoSecrets(t, output.String())
		})
	}
}

func TestMCPMethodDiagnostics(t *testing.T) {
	capture, output := captureDiag(t)
	s := mcpserver.New(nil, diagAuth{mcpserver.Principal{Caller: auth.Caller{Kind: "peer", PeerID: "peer_diag"}}}, "https://hub.example")
	s.Register("diag/fail", func(context.Context, *mcpserver.Session, *mcpserver.Request) (any, error) {
		return nil, errors.New("message-canary-do-not-log")
	})
	body := initialize(1, "2025-03-26")
	body["params"].(map[string]any)["clientInfo"] = map[string]any{"name": "ChatGPT", "version": "1"}
	initialized := diagRequest(s, http.MethodPost, body, nil)
	headers := map[string]string{"Mcp-Session-Id": initialized.Header().Get("Mcp-Session-Id")}
	for _, tc := range []struct {
		method, tool string
		code         int64
	}{
		{"events/list", "", 0}, {"tools/list", "", 0}, {"prompts/list", "", 0},
		{"tools/call", "unknown_tool", mcpserver.ErrInvalidParams}, {"missing/method", "", mcpserver.ErrMethodNotFound},
		{"diag/fail", "", mcpserver.ErrInternal},
	} {
		before := len(capture.records)
		diagRequest(s, http.MethodPost, map[string]any{"jsonrpc": "2.0", "id": 2, "method": tc.method,
			"params": map[string]any{"name": tc.tool, "arguments": map[string]any{"text": "message-canary-do-not-log", "secret": "argument-canary-do-not-log"}}}, headers)
		if len(capture.records) != before+1 {
			t.Fatalf("%s: got %d new records: %s", tc.method, len(capture.records)-before, output)
		}
		record := capture.records[before]
		f := diagFields(record)
		wantResult := "ok"
		if tc.code != 0 {
			wantResult = "error"
		}
		if record.Message != "mcp: request" || record.Level != slog.LevelInfo || f["method"] != tc.method || f["result"] != wantResult || f["code"] != tc.code ||
			f["caller_kind"] != "peer" || f["peer_id"] != "peer_diag" || f["client_name"] != "ChatGPT" || f["session_present"] != true {
			t.Errorf("%s diagnostics: %v (%s)", tc.method, f, record.Message)
		}
		if tc.tool != "" && f["tool_name"] != "unknown" {
			t.Errorf("tool_name = %v", f["tool_name"])
		}
	}
	before := len(capture.records)
	diagRequest(s, http.MethodPost, map[string]any{"jsonrpc": "2.0", "id": 3, "method": "ping"}, headers)
	if len(capture.records) != before+1 {
		t.Error("successful ping must log exactly once")
	}
	assertDiagNoSecrets(t, output.String())
	for _, secret := range []string{"message-canary-do-not-log", "argument-canary-do-not-log"} {
		if strings.Contains(output.String(), secret) {
			t.Errorf("diagnostics leaked %s", secret)
		}
	}
}

func TestMCPEarlyFailureDiagnostics(t *testing.T) {
	peer := diagAuth{mcpserver.Principal{Caller: auth.Caller{Kind: "peer", PeerID: "peer_diag"}}}
	for _, tc := range []struct {
		name, method string
		auth         mcpserver.Authenticator
		headers      map[string]string
		status       int64
	}{
		{"unauthenticated", http.MethodPost, mcpserver.Chain{}, nil, 401},
		{"authentication failure", http.MethodPost, diagFailureAuth{}, nil, 500},
		{"content type", http.MethodPost, peer, map[string]string{"Content-Type": "text/plain"}, 415},
		{"http method", http.MethodPut, peer, nil, 405},
	} {
		t.Run(tc.name, func(t *testing.T) {
			capture, output := captureDiag(t)
			s := mcpserver.New(nil, tc.auth, "https://hub.example")
			w := diagRequest(s, tc.method, map[string]any{"jsonrpc": "2.0", "id": 1, "method": "events/subscribe", "params": map[string]any{"secret": "body-canary-do-not-log"}}, tc.headers)
			if int64(w.Code) != tc.status || len(capture.records) != 1 {
				t.Fatalf("status=%d logs=%d: %s", w.Code, len(capture.records), output)
			}
			r := capture.records[0]
			f := diagFields(r)
			if r.Message != "mcp: request" || r.Level != slog.LevelInfo || f["result"] != "error" || f["code"] != tc.status || f["method"] != "(unparsed)" {
				t.Fatalf("diagnostics: %v", f)
			}
			assertDiagNoSecrets(t, output.String())
			if strings.Contains(output.String(), "auth-secret-canary") {
				t.Error("auth error leaked")
			}
		})
	}
}

// An early refusal cannot read even one byte from an unauthenticated client:
// reading a body for diagnostics would let slow clients delay the refusal.
type unreadableDiagBody struct{}

func (unreadableDiagBody) Read([]byte) (int, error) { panic("diagnostics read rejected request body") }
func (unreadableDiagBody) Close() error             { return nil }

func TestMCPAllHTTPMetadataAndNoEarlyBodyRead(t *testing.T) {
	capture, output := captureDiag(t)
	s := mcpserver.New(nil, mcpserver.Chain{}, "https://hub.example")
	r := httptest.NewRequest(http.MethodPost, "https://hub.example/mcp", nil)
	r.Body = unreadableDiagBody{}
	r.Header.Set("Mcp-Method", "events/subscribe")
	r.Header.Set("Mcp-Name", "vecnet.message")
	r.Header.Set("MCP-Protocol-Version", mcpserver.ProtoCurrent)
	r.Header.Set("User-Agent", strings.Repeat("A", 140))
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != 401 || len(capture.records) != 1 {
		t.Fatalf("status=%d records=%d", w.Code, len(capture.records))
	}
	rec := capture.records[0]
	fields := diagFields(rec)
	if rec.Level != slog.LevelInfo || fields["http_method"] != "POST" || fields["path"] != "/mcp" || fields["http_status"] != int64(401) || fields["mcp_method"] != "events/subscribe" || fields["mcp_name"] != "vecnet.message" || fields["user_agent"] != strings.Repeat("A", 120) || fields["id_type"] != "absent" || fields["auth_outcome"] != "none" {
		t.Fatalf("metadata: %v", fields)
	}
	if _, ok := fields["duration_ms"]; !ok {
		t.Error("missing duration")
	}
	assertDiagNoSecrets(t, output.String())
}
