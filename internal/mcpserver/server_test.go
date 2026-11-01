package mcpserver_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptrace"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/XertroV/vecnet/internal/httpapi"
	"github.com/XertroV/vecnet/internal/mcpserver"
	"github.com/XertroV/vecnet/internal/testhub"
)

// rpcResp is a JSON-RPC 2.0 response as the hub writes it.
type rpcResp struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  json.RawMessage `json:"result"`
	Error   *struct {
		Code    int             `json:"code"`
		Message string          `json:"message"`
		Data    json.RawMessage `json:"data"`
	} `json:"error"`
}

// envelope is the hub's §2 HTTP error body.
type envelope struct {
	Error struct {
		Code    string         `json:"code"`
		Message string         `json:"message"`
		Details map[string]any `json:"details"`
	} `json:"error"`
}

// call is one raw HTTP request to /mcp.
type call struct {
	method  string // HTTP method; "" = POST
	secret  string // bearer; "" = none
	headers map[string]string
	body    any // nil, []byte, string or JSON-encoded value
	client  *http.Client
}

// modern returns the 2026-era request headers.
func modern() map[string]string {
	return map[string]string{
		"MCP-Protocol-Version": mcpserver.ProtoCurrent,
		"Accept":               "application/json, text/event-stream",
	}
}

func do(t *testing.T, env *testhub.Env, c call) (*http.Response, []byte) {
	t.Helper()
	var r io.Reader
	switch b := c.body.(type) {
	case nil:
	case []byte:
		r = bytes.NewReader(b)
	case string:
		r = strings.NewReader(b)
	default:
		j, err := json.Marshal(b)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		r = bytes.NewReader(j)
	}
	m := c.method
	if m == "" {
		m = http.MethodPost
	}
	req, err := http.NewRequest(m, env.URL+"/mcp", r)
	if err != nil {
		t.Fatal(err)
	}
	if c.body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.secret != "" {
		req.Header.Set("Authorization", "Bearer "+c.secret)
	}
	for k, v := range c.headers {
		req.Header.Set(k, v)
	}
	cl := c.client
	if cl == nil {
		cl = http.DefaultClient
	}
	resp, err := cl.Do(req)
	if err != nil {
		t.Fatalf("%s /mcp: %v", m, err)
	}
	defer resp.Body.Close()
	out, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	return resp, out
}

func decodeRPC(t *testing.T, body []byte) rpcResp {
	t.Helper()
	var r rpcResp
	if err := json.Unmarshal(body, &r); err != nil {
		t.Fatalf("decode JSON-RPC response %q: %v", body, err)
	}
	if r.JSONRPC != "2.0" {
		t.Fatalf("jsonrpc = %q, want 2.0 (body %s)", r.JSONRPC, body)
	}
	return r
}

func decodeEnvelope(t *testing.T, body []byte) envelope {
	t.Helper()
	var e envelope
	if err := json.Unmarshal(body, &e); err != nil || e.Error.Code == "" {
		t.Fatalf("body %q is not the hub error envelope (%v)", body, err)
	}
	if e.Error.Details == nil {
		t.Fatalf("envelope details missing: %s", body)
	}
	return e
}

func discover(id int, metaKey, version string) map[string]any {
	return map[string]any{
		"jsonrpc": "2.0", "id": id, "method": "server/discover",
		"params": map[string]any{"_meta": map[string]any{metaKey: version}},
	}
}

func initialize(id int, version string) map[string]any {
	return map[string]any{
		"jsonrpc": "2.0", "id": id, "method": "initialize",
		"params": map[string]any{
			"protocolVersion": version,
			"capabilities":    map[string]any{},
			"clientInfo":      map[string]any{"name": "test", "version": "1"},
		},
	}
}

// legacySession runs a raw 2025-11-25 initialize and returns the session id.
func legacySession(t *testing.T, env *testhub.Env, secret string) string {
	t.Helper()
	resp, body := do(t, env, call{secret: secret, body: initialize(1, mcpserver.ProtoLegacy),
		headers: map[string]string{"Accept": "application/json, text/event-stream"}})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("initialize: %d %s", resp.StatusCode, body)
	}
	r := decodeRPC(t, body)
	if r.Error != nil {
		t.Fatalf("initialize error: %+v", r.Error)
	}
	sid := resp.Header.Get("Mcp-Session-Id")
	if len(sid) != 26 {
		t.Fatalf("Mcp-Session-Id = %q, want 26 chars", sid)
	}
	return sid
}

func TestDiscover2026(t *testing.T) {
	env := testhub.Start(t)
	_, secret := env.NewPeer("session", "a1")

	for _, key := range []string{"protocolVersion", "io.modelcontextprotocol/protocolVersion"} {
		resp, body := do(t, env, call{secret: secret, headers: modern(), body: discover(7, key, mcpserver.ProtoCurrent)})
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("_meta %s: status %d %s", key, resp.StatusCode, body)
		}
		if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
			t.Fatalf("Content-Type = %q", ct)
		}
		if sid := resp.Header.Get("Mcp-Session-Id"); sid != "" {
			t.Fatalf("2026 discover minted a session id %q", sid)
		}
		r := decodeRPC(t, body)
		if r.Error != nil {
			t.Fatalf("discover error: %+v", r.Error)
		}
		if string(r.ID) != "7" {
			t.Fatalf("id = %s, want 7", r.ID)
		}
		var res struct {
			ProtocolVersion string         `json:"protocolVersion"`
			Capabilities    map[string]any `json:"capabilities"`
			ServerInfo      struct {
				Name string `json:"name"`
			} `json:"serverInfo"`
		}
		if err := json.Unmarshal(r.Result, &res); err != nil {
			t.Fatal(err)
		}
		if res.ProtocolVersion != "2026-07-28" {
			t.Fatalf("protocolVersion = %q", res.ProtocolVersion)
		}
		if res.ServerInfo.Name != "vecnet" {
			t.Fatalf("serverInfo.name = %q", res.ServerInfo.Name)
		}
		if _, ok := res.Capabilities["events"].(map[string]any); !ok {
			t.Fatalf("capabilities.events missing: %v", res.Capabilities)
		}
		ext, ok := res.Capabilities["extensions"].(map[string]any)
		if !ok {
			t.Fatalf("capabilities.extensions missing: %v", res.Capabilities)
		}
		if _, ok := ext["io.modelcontextprotocol/events"].(map[string]any); !ok {
			t.Fatalf(`capabilities.extensions["io.modelcontextprotocol/events"] missing: %v`, ext)
		}
	}
}

// bearerRT adds the vecnet bearer to every request of the SDK client.
type bearerRT struct{ secret string }

func (b bearerRT) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.secret)
	return http.DefaultTransport.RoundTrip(r)
}

func TestInitializeLegacyMintsSession(t *testing.T) {
	env := testhub.Start(t)
	_, secret := env.NewPeer("session", "a1")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	client := mcp.NewClient(&mcp.Implementation{Name: "vecnet-test", Version: "1"}, nil)
	tr := &mcp.StreamableClientTransport{
		Endpoint:   env.URL + "/mcp",
		HTTPClient: &http.Client{Transport: bearerRT{secret: secret}},
	}
	cs, err := client.Connect(ctx, tr, &mcp.ClientSessionOptions{ProtocolVersion: mcpserver.ProtoLegacy})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	sid := cs.ID()
	if len(sid) != 26 {
		t.Fatalf("session id %q, want 26 chars", sid)
	}
	if got := cs.InitializeResult().ProtocolVersion; got != mcpserver.ProtoLegacy {
		t.Fatalf("negotiated %q", got)
	}
	if got := cs.InitializeResult().ServerInfo; got == nil || got.Name != "vecnet" {
		t.Fatalf("serverInfo = %+v", got)
	}
	if err := cs.Ping(ctx, nil); err != nil {
		t.Fatalf("Ping: %v", err)
	}
	if err := cs.Close(); err != nil { // sends DELETE
		t.Fatalf("Close: %v", err)
	}

	ping := map[string]any{"jsonrpc": "2.0", "id": 2, "method": "ping"}
	resp, body := do(t, env, call{secret: secret, body: ping, headers: map[string]string{
		"Mcp-Session-Id": sid, "MCP-Protocol-Version": mcpserver.ProtoLegacy, "Accept": "application/json, text/event-stream",
	}})
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("POST after DELETE: %d %s, want 404", resp.StatusCode, body)
	}
	if e := decodeEnvelope(t, body); e.Error.Code != "session_not_found" {
		t.Fatalf("code = %q", e.Error.Code)
	}
}

func TestInitializeOldVersionRejected(t *testing.T) {
	env := testhub.Start(t)
	_, secret := env.NewPeer("session", "a1")

	var reused []bool
	trace := &httptrace.ClientTrace{GotConn: func(i httptrace.GotConnInfo) { reused = append(reused, i.Reused) }}
	cl := &http.Client{Transport: &http.Transport{}}
	defer cl.CloseIdleConnections()
	traced := &http.Client{Transport: traceRT{base: cl.Transport, trace: trace}}

	resp, body := do(t, env, call{secret: secret, client: traced, body: initialize(1, "2025-03-26"),
		headers: map[string]string{"Accept": "application/json, text/event-stream"}})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d %s, want 200 with a JSON-RPC error", resp.StatusCode, body)
	}
	if sid := resp.Header.Get("Mcp-Session-Id"); sid != "" {
		t.Fatalf("rejected initialize minted session %q", sid)
	}
	r := decodeRPC(t, body)
	if r.Error == nil || r.Error.Code != -32602 {
		t.Fatalf("error = %+v, want -32602", r.Error)
	}
	var data struct {
		SupportedVersions []string `json:"supportedVersions"`
	}
	if err := json.Unmarshal(r.Error.Data, &data); err != nil {
		t.Fatalf("data %s: %v", r.Error.Data, err)
	}
	if want := []string{"2026-07-28", "2025-11-25"}; !reflect.DeepEqual(data.SupportedVersions, want) {
		t.Fatalf("supportedVersions = %v, want %v", data.SupportedVersions, want)
	}

	// The client retries with 2026-07-28 server/discover on the same connection.
	resp, body = do(t, env, call{secret: secret, client: traced, headers: modern(),
		body: discover(2, "io.modelcontextprotocol/protocolVersion", mcpserver.ProtoCurrent)})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("discover: %d %s", resp.StatusCode, body)
	}
	r = decodeRPC(t, body)
	if r.Error != nil || !strings.Contains(string(r.Result), `"protocolVersion":"2026-07-28"`) {
		t.Fatalf("discover result %s err %+v", r.Result, r.Error)
	}
	if len(reused) != 2 || !reused[1] {
		t.Fatalf("connection reuse = %v, want the discover on the initialize's connection", reused)
	}
}

type traceRT struct {
	base  http.RoundTripper
	trace *httptrace.ClientTrace
}

func (t traceRT) RoundTrip(r *http.Request) (*http.Response, error) {
	return t.base.RoundTrip(r.WithContext(httptrace.WithClientTrace(r.Context(), t.trace)))
}

func TestOriginAndAuth(t *testing.T) {
	env := testhub.Start(t)
	_, peerSecret := env.NewPeer("session", "a1")
	_, prnSecret := env.NewPrincipal("max")
	body := discover(1, "protocolVersion", mcpserver.ProtoCurrent)

	h := modern()
	h["Origin"] = "https://evil.example"
	resp, out := do(t, env, call{secret: peerSecret, headers: h, body: body})
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("evil Origin: %d %s, want 403", resp.StatusCode, out)
	}
	decodeEnvelope(t, out)

	h = modern()
	h["Origin"] = env.URL // the hub's own public origin (Cfg.PublicURL)
	resp, out = do(t, env, call{secret: peerSecret, headers: h, body: body})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("own Origin: %d %s, want 200", resp.StatusCode, out)
	}

	resp, out = do(t, env, call{headers: modern(), body: body})
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no bearer: %d %s, want 401", resp.StatusCode, out)
	}
	wa := resp.Header.Get("WWW-Authenticate")
	wantMeta := `resource_metadata="` + env.URL + `/.well-known/oauth-protected-resource"`
	if !strings.Contains(wa, wantMeta) {
		t.Fatalf("WWW-Authenticate = %q, want it to contain %s", wa, wantMeta)
	}
	if e := decodeEnvelope(t, out); e.Error.Code != "unauthenticated" {
		t.Fatalf("code = %q", e.Error.Code)
	}

	resp, out = do(t, env, call{secret: "vn_peer_bogus", headers: modern(), body: body})
	if resp.StatusCode != http.StatusUnauthorized || !strings.Contains(resp.Header.Get("WWW-Authenticate"), "resource_metadata=") {
		t.Fatalf("bad bearer: %d %q %s", resp.StatusCode, resp.Header.Get("WWW-Authenticate"), out)
	}

	resp, out = do(t, env, call{secret: prnSecret, headers: modern(), body: body})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("principal credential: %d %s", resp.StatusCode, out)
	}
	if r := decodeRPC(t, out); r.Error != nil {
		t.Fatalf("principal discover error %+v", r.Error)
	}
}

// TestBodyLimit413: /mcp skips httpapi's bearer middleware but keeps the
// 256 KiB body cap and the hub error envelope.
func TestBodyLimit413(t *testing.T) {
	env := testhub.Start(t)
	_, secret := env.NewPeer("session", "a1")
	big := `{"jsonrpc":"2.0","id":1,"method":"ping","params":{"pad":"` + strings.Repeat("x", 300<<10) + `"}}`
	resp, out := do(t, env, call{secret: secret, headers: modern(), body: big})
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("300 KiB body: %d, want 413", resp.StatusCode)
	}
	if e := decodeEnvelope(t, out); e.Error.Code != "payload_too_large" {
		t.Fatalf("code = %q", e.Error.Code)
	}
	if httpapi.MaxBodyBytes != 256<<10 {
		t.Fatalf("MaxBodyBytes = %d", httpapi.MaxBodyBytes)
	}
}

func TestHeaderVersionMismatch400(t *testing.T) {
	env := testhub.Start(t)
	_, secret := env.NewPeer("session", "a1")
	h := modern()
	h["MCP-Protocol-Version"] = mcpserver.ProtoLegacy
	for _, key := range []string{"protocolVersion", "io.modelcontextprotocol/protocolVersion"} {
		resp, out := do(t, env, call{secret: secret, headers: h, body: discover(1, key, mcpserver.ProtoCurrent)})
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("_meta %s: %d %s, want 400", key, resp.StatusCode, out)
		}
		decodeEnvelope(t, out)
	}
	// Also on an ordinary 2026 method.
	h = modern()
	ping := map[string]any{"jsonrpc": "2.0", "id": 3, "method": "ping",
		"params": map[string]any{"_meta": map[string]any{"protocolVersion": mcpserver.ProtoLegacy}}}
	resp, out := do(t, env, call{secret: secret, headers: h, body: ping})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("ping mismatch: %d %s, want 400", resp.StatusCode, out)
	}
	decodeEnvelope(t, out)
}

func TestBatchRejected(t *testing.T) {
	env := testhub.Start(t)
	_, secret := env.NewPeer("session", "a1")
	batch := `[ {"jsonrpc":"2.0","id":1,"method":"ping"}, {"jsonrpc":"2.0","id":2,"method":"ping"} ]`
	resp, out := do(t, env, call{secret: secret, headers: modern(), body: batch})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("batch: %d %s", resp.StatusCode, out)
	}
	r := decodeRPC(t, out)
	if r.Error == nil || r.Error.Code != -32600 {
		t.Fatalf("batch error = %+v (body %s), want -32600", r.Error, out)
	}
	if string(r.ID) != "null" {
		t.Fatalf("batch error id = %s, want null", r.ID)
	}
	if r.Result != nil {
		t.Fatalf("batch produced a result %s", r.Result)
	}
}

func TestNotificationIs202(t *testing.T) {
	env := testhub.Start(t)
	_, secret := env.NewPeer("session", "a1")
	note := map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"}

	resp, out := do(t, env, call{secret: secret, headers: modern(), body: note})
	if resp.StatusCode != http.StatusAccepted || len(out) != 0 {
		t.Fatalf("2026 notification: %d %q, want 202 empty", resp.StatusCode, out)
	}

	sid := legacySession(t, env, secret)
	resp, out = do(t, env, call{secret: secret, body: note, headers: map[string]string{
		"Mcp-Session-Id": sid, "Accept": "application/json, text/event-stream",
	}})
	if resp.StatusCode != http.StatusAccepted || len(out) != 0 {
		t.Fatalf("legacy notification: %d %q, want 202 empty", resp.StatusCode, out)
	}
}

func TestGetStreamLegacyHeartbeat(t *testing.T) {
	env := testhub.Start(t)
	env.Configure(func(d *httpapi.Deps) { d.Heartbeat = 50 * time.Millisecond })
	_, secret := env.NewPeer("session", "a1")
	sid := legacySession(t, env, secret)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, env.URL+"/mcp", nil)
	req.Header.Set("Authorization", "Bearer "+secret)
	req.Header.Set("Mcp-Session-Id", sid)
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Last-Event-ID", "42") // ignored: no replay, no error
	start := time.Now()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("GET: %d %s", resp.StatusCode, b)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("Content-Type = %q", ct)
	}
	if cc := resp.Header.Get("Cache-Control"); cc != "no-store" {
		t.Fatalf("Cache-Control = %q", cc)
	}
	sc := bufio.NewScanner(resp.Body)
	gotHB := false
	for sc.Scan() {
		line := sc.Text()
		if line == ": hb" {
			gotHB = true
			break
		}
		if strings.HasPrefix(line, "data:") || strings.HasPrefix(line, "id:") || strings.HasPrefix(line, "event:") {
			t.Fatalf("stream replayed or sent an event before the heartbeat: %q", line)
		}
	}
	if !gotHB {
		t.Fatalf("no heartbeat: %v", sc.Err())
	}
	if el := time.Since(start); el > 500*time.Millisecond {
		t.Fatalf("heartbeat after %v, want within 500ms", el)
	}

	resp2, out := do(t, env, call{method: http.MethodGet, secret: secret, headers: map[string]string{
		"MCP-Protocol-Version": mcpserver.ProtoCurrent, "Accept": "text/event-stream",
	}})
	if resp2.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("2026 GET: %d %s, want 405", resp2.StatusCode, out)
	}
	decodeEnvelope(t, out)

	resp2, out = do(t, env, call{method: http.MethodDelete, secret: secret, headers: map[string]string{
		"MCP-Protocol-Version": mcpserver.ProtoCurrent,
	}})
	if resp2.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("2026 DELETE: %d %s, want 405", resp2.StatusCode, out)
	}

	// A second GET on the same session closes the first stream.
	ctx2, cancel2 := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel2()
	req2, _ := http.NewRequestWithContext(ctx2, http.MethodGet, env.URL+"/mcp", nil)
	req2.Header = req.Header.Clone()
	second, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Body.Close()
	if second.StatusCode != http.StatusOK {
		t.Fatalf("second GET: %d", second.StatusCode)
	}
	done := make(chan struct{})
	go func() {
		for sc.Scan() {
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("first stream still open after a second GET")
	}
}

func TestUnknownMethod(t *testing.T) {
	env := testhub.Start(t)
	_, secret := env.NewPeer("session", "a1")
	req := map[string]any{"jsonrpc": "2.0", "id": "x1", "method": "no/such/method"}
	resp, out := do(t, env, call{secret: secret, headers: modern(), body: req})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("%d %s", resp.StatusCode, out)
	}
	r := decodeRPC(t, out)
	if r.Error == nil || r.Error.Code != -32601 {
		t.Fatalf("error = %+v, want -32601", r.Error)
	}
	if string(r.ID) != `"x1"` {
		t.Fatalf("id = %s", r.ID)
	}

	sid := legacySession(t, env, secret)
	resp, out = do(t, env, call{secret: secret, body: req, headers: map[string]string{
		"Mcp-Session-Id": sid, "Accept": "application/json, text/event-stream",
	}})
	if r := decodeRPC(t, out); resp.StatusCode != http.StatusOK || r.Error == nil || r.Error.Code != -32601 {
		t.Fatalf("legacy unknown method: %d %s", resp.StatusCode, out)
	}
}
