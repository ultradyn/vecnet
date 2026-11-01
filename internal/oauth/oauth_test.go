package oauth_test

import (
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/egress"
	"github.com/XertroV/vecnet/internal/mcpserver"
	"github.com/XertroV/vecnet/internal/oauth"
	"github.com/XertroV/vecnet/internal/testhub"
)

const (
	chatgptRedirect = "https://chatgpt.com/connector_platform_oauth_redirect"
	verifierA       = "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"
	verifierB       = "Vz3-9xQp2Lm7Tn0Rk4Yb8Hc6Fd1Gs5Ja.Wq~Ue_Xi2Oo"
	cookieName      = "vecnet_oauth_sub"
)

// noFollow keeps a 302 visible to the test.
var noFollow = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
	return http.ErrUseLastResponse
}}

func challenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// browser is one ChatGPT user: a registered client plus the consent cookie.
type browser struct {
	t        *testing.T
	env      *testhub.Env
	client   string
	redirect string
	cookie   *http.Cookie
}

// tokens is a /oauth/token response, success or error.
type tokens struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
	RefreshToken string `json:"refresh_token"`
	Scope        string `json:"scope"`
	Error        string `json:"error"`
	status       int
	header       http.Header
}

func postForm(t *testing.T, env *testhub.Env, path string, form url.Values, cookie *http.Cookie) (*http.Response, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, env.URL+path, strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if cookie != nil {
		req.AddCookie(cookie)
	}
	resp, err := noFollow.Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp, string(body)
}

func get(t *testing.T, rawURL string, cookie *http.Cookie) (*http.Response, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	if cookie != nil {
		req.AddCookie(cookie)
	}
	resp, err := noFollow.Do(req)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp, string(body)
}

// register runs RFC 7591 registration for redirects, requiring success.
func register(t *testing.T, env *testhub.Env, redirects ...string) string {
	t.Helper()
	resp, body := env.Do("", "POST", "/oauth/register", map[string]any{
		"client_name": "ChatGPT", "redirect_uris": redirects,
		"grant_types": []string{"authorization_code", "refresh_token"}, "response_types": []string{"code"},
		"token_endpoint_auth_method": "none",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("register: %d %s", resp.StatusCode, body)
	}
	var out struct {
		ClientID     string   `json:"client_id"`
		RedirectURIs []string `json:"redirect_uris"`
		AuthMethod   string   `json:"token_endpoint_auth_method"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("register body %s: %v", body, err)
	}
	if out.ClientID == "" || !slices.Equal(out.RedirectURIs, redirects) || out.AuthMethod != "none" {
		t.Fatalf("register returned %s", body)
	}
	return out.ClientID
}

func newBrowser(t *testing.T, env *testhub.Env) *browser {
	t.Helper()
	return &browser{t: t, env: env, client: register(t, env, chatgptRedirect), redirect: chatgptRedirect}
}

func (b *browser) query(verifier string) url.Values {
	return url.Values{
		"response_type": {"code"}, "client_id": {b.client}, "redirect_uri": {b.redirect},
		"code_challenge": {challenge(verifier)}, "code_challenge_method": {"S256"},
		"state": {"st-123"}, "resource": {b.env.URL + "/mcp"}, "scope": {"vecnet"},
	}
}

func (b *browser) authorize(q url.Values) (*http.Response, string) {
	b.t.Helper()
	return get(b.t, b.env.URL+"/oauth/authorize?"+q.Encode(), b.cookie)
}

var requestField = regexp.MustCompile(`name="request"\s+value="([^"]*)"`)

// sealedRequest pulls the consent form's hidden request field out of a page.
func sealedRequest(t *testing.T, page string) string {
	t.Helper()
	m := requestField.FindStringSubmatch(page)
	if m == nil {
		t.Fatalf("no request field in page:\n%s", page)
	}
	return html.UnescapeString(m[1])
}

// consent posts the consent form, returning the response. link "" sends no
// link_code field.
func (b *browser) consent(request, link string) (*http.Response, string) {
	b.t.Helper()
	form := url.Values{"request": {request}, "approve": {"1"}}
	if link != "" {
		form.Set("link_code", link)
	}
	return postForm(b.t, b.env, "/oauth/authorize", form, b.cookie)
}

// code runs authorize and consent, keeps the cookie and returns the code.
func (b *browser) code(verifier, link string) string {
	b.t.Helper()
	resp, page := b.authorize(b.query(verifier))
	if resp.StatusCode != http.StatusOK {
		b.t.Fatalf("authorize: %d %s", resp.StatusCode, page)
	}
	resp, body := b.consent(sealedRequest(b.t, page), link)
	if resp.StatusCode != http.StatusFound {
		b.t.Fatalf("consent: %d %s", resp.StatusCode, body)
	}
	for _, c := range resp.Cookies() {
		if c.Name == cookieName {
			b.cookie = c
		}
	}
	loc, err := url.Parse(resp.Header.Get("Location"))
	if err != nil {
		b.t.Fatal(err)
	}
	if got := loc.Query().Get("state"); got != "st-123" {
		b.t.Fatalf("state = %q in %s", got, loc)
	}
	code := loc.Query().Get("code")
	if code == "" {
		b.t.Fatalf("no code in %s", loc)
	}
	return code
}

func token(t *testing.T, env *testhub.Env, form url.Values) tokens {
	t.Helper()
	resp, body := postForm(t, env, "/oauth/token", form, nil)
	var out tokens
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("token body %q: %v", body, err)
	}
	out.status, out.header = resp.StatusCode, resp.Header
	return out
}

func (b *browser) exchange(code, verifier string) tokens {
	b.t.Helper()
	return token(b.t, b.env, url.Values{
		"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {b.redirect},
		"client_id": {b.client}, "code_verifier": {verifier}, "resource": {b.env.URL + "/mcp"},
	})
}

func (b *browser) refresh(rt string) tokens {
	b.t.Helper()
	return token(b.t, b.env, url.Values{
		"grant_type": {"refresh_token"}, "refresh_token": {rt}, "client_id": {b.client},
		"resource": {b.env.URL + "/mcp"},
	})
}

func wantError(t *testing.T, got tokens, code string) {
	t.Helper()
	if got.status != http.StatusBadRequest || got.Error != code {
		t.Fatalf("token: %d error=%q, want 400 %s", got.status, got.Error, code)
	}
	if got.AccessToken != "" || got.RefreshToken != "" {
		t.Fatalf("an error carried tokens: %+v", got)
	}
}

// newDot pre-creates a dot peer through the operator API.
func newDot(t *testing.T, env *testhub.Env, name string) string {
	t.Helper()
	resp, body := env.Do(env.OperatorSecret, "POST", "/v1/admin/peers", map[string]string{"kind": "dot", "name": name})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("add peer: %d %s", resp.StatusCode, body)
	}
	var out struct {
		PeerID string `json:"peer_id"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatal(err)
	}
	return out.PeerID
}

// linkCode asks the operator API for a link code.
func linkCode(t *testing.T, env *testhub.Env, peer string) string {
	t.Helper()
	resp, body := env.Do(env.OperatorSecret, "POST", "/v1/admin/dot-links", map[string]string{"peer_id": peer})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("dot link: %d %s", resp.StatusCode, body)
	}
	var out struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatal(err)
	}
	return out.Code
}

// login runs the whole first-login flow and returns the browser, its peer
// and a fresh token pair.
func login(t *testing.T, env *testhub.Env) (*browser, string, tokens) {
	t.Helper()
	b := newBrowser(t, env)
	peer := newDot(t, env, "my-dot")
	got := b.exchange(b.code(verifierA, linkCode(t, env, peer)), verifierA)
	if got.status != http.StatusOK {
		t.Fatalf("exchange: %d %s", got.status, got.Error)
	}
	return b, peer, got
}

// mcp posts one 2026-era JSON-RPC request with the access token.
func mcp(t *testing.T, env *testhub.Env, access, method string, params any) (*http.Response, string) {
	t.Helper()
	body := map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, env.URL+"/mcp", strings.NewReader(string(raw)))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("MCP-Protocol-Version", mcpserver.ProtoCurrent)
	req.Header.Set("Authorization", "Bearer "+access)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	out, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp, string(out)
}

func discover(t *testing.T, env *testhub.Env, access string) (*http.Response, string) {
	t.Helper()
	return mcp(t, env, access, "server/discover", map[string]any{
		"_meta": map[string]any{"protocolVersion": mcpserver.ProtoCurrent},
	})
}

// tool calls a tool with the access token, requiring a non-error result, and
// returns its structured JSON text.
func tool(t *testing.T, env *testhub.Env, access, name string, args any) map[string]any {
	t.Helper()
	resp, body := mcp(t, env, access, "tools/call", map[string]any{"name": name, "arguments": args})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("%s: %d %s", name, resp.StatusCode, body)
	}
	var r struct {
		Error  *json.RawMessage `json:"error"`
		Result struct {
			IsError           bool           `json:"isError"`
			StructuredContent map[string]any `json:"structuredContent"`
			Content           []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(body), &r); err != nil {
		t.Fatalf("%s: %q: %v", name, body, err)
	}
	if r.Error != nil || r.Result.IsError {
		t.Fatalf("%s failed: %s", name, body)
	}
	if r.Result.StructuredContent != nil {
		return r.Result.StructuredContent
	}
	var out map[string]any
	if len(r.Result.Content) == 0 || json.Unmarshal([]byte(r.Result.Content[0].Text), &out) != nil {
		t.Fatalf("%s: no JSON result in %s", name, body)
	}
	return out
}

func sendAs(t *testing.T, env *testhub.Env, access, key, to string) string {
	t.Helper()
	res := tool(t, env, access, "vecnet_send", map[string]any{
		"idempotency_key": key, "recipients": []string{to}, "priority": "hold",
		"content": map[string]any{"text": "hello"},
	})
	id, _ := res["message_id"].(string)
	if id == "" {
		t.Fatalf("no message_id in %v", res)
	}
	return id
}

func senderOf(t *testing.T, env *testhub.Env, msg string) string {
	t.Helper()
	var sender string
	if err := env.Hub.DB.QueryRow(`SELECT sender FROM messages WHERE msg_id = ?`, msg).Scan(&sender); err != nil {
		t.Fatalf("message %s: %v", msg, err)
	}
	return sender
}

func TestMetadataDocuments(t *testing.T) {
	env := testhub.Start(t)

	resp, body := get(t, env.URL+"/.well-known/oauth-authorization-server", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("AS metadata: %d %s", resp.StatusCode, body)
	}
	var as struct {
		Issuer                string   `json:"issuer"`
		AuthorizationEndpoint string   `json:"authorization_endpoint"`
		TokenEndpoint         string   `json:"token_endpoint"`
		RegistrationEndpoint  string   `json:"registration_endpoint"`
		ResponseTypes         []string `json:"response_types_supported"`
		GrantTypes            []string `json:"grant_types_supported"`
		ChallengeMethods      []string `json:"code_challenge_methods_supported"`
		AuthMethods           []string `json:"token_endpoint_auth_methods_supported"`
		Scopes                []string `json:"scopes_supported"`
		CIMD                  bool     `json:"client_id_metadata_document_supported"`
	}
	if err := json.Unmarshal([]byte(body), &as); err != nil {
		t.Fatalf("AS metadata %s: %v", body, err)
	}
	if as.Issuer != env.URL || as.AuthorizationEndpoint != env.URL+"/oauth/authorize" ||
		as.TokenEndpoint != env.URL+"/oauth/token" || as.RegistrationEndpoint != env.URL+"/oauth/register" {
		t.Fatalf("AS endpoints: %s", body)
	}
	if !slices.Equal(as.ResponseTypes, []string{"code"}) ||
		!slices.Equal(as.GrantTypes, []string{"authorization_code", "refresh_token"}) ||
		!slices.Equal(as.ChallengeMethods, []string{"S256"}) ||
		!slices.Equal(as.AuthMethods, []string{"none"}) ||
		!slices.Equal(as.Scopes, []string{"vecnet"}) || !as.CIMD {
		t.Fatalf("AS capabilities: %s", body)
	}

	// The root path is PROTOCOL §4.5; the /mcp suffix is what RFC 9728 §3.1
	// derives from the resource URL.
	for _, path := range []string{"/.well-known/oauth-protected-resource", "/.well-known/oauth-protected-resource/mcp"} {
		resp, body := get(t, env.URL+path, nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s: %d %s", path, resp.StatusCode, body)
		}
		var rs struct {
			Resource string   `json:"resource"`
			AS       []string `json:"authorization_servers"`
			Scopes   []string `json:"scopes_supported"`
			Bearer   []string `json:"bearer_methods_supported"`
		}
		if err := json.Unmarshal([]byte(body), &rs); err != nil {
			t.Fatalf("resource metadata %s: %v", body, err)
		}
		if rs.Resource != env.URL+"/mcp" || !slices.Equal(rs.AS, []string{env.URL}) ||
			!slices.Equal(rs.Scopes, []string{"vecnet"}) || !slices.Equal(rs.Bearer, []string{"header"}) {
			t.Fatalf("resource metadata: %s", body)
		}
	}
}

func TestDCRAndFullCodeFlow(t *testing.T) {
	env := testhub.Start(t)
	b := newBrowser(t, env)

	resp, page := b.authorize(b.query(verifierA))
	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/html") {
		t.Fatalf("authorize: %d %s\n%s", resp.StatusCode, resp.Header.Get("Content-Type"), page)
	}
	if !strings.Contains(page, `name="link_code"`) {
		t.Fatalf("no link_code field:\n%s", page)
	}
	request := sealedRequest(t, page)

	resp, bad := b.consent(request, "ZZZZZZZZ")
	if resp.StatusCode != http.StatusBadRequest || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/html") {
		t.Fatalf("wrong code: %d %s", resp.StatusCode, bad)
	}
	if resp.Header.Get("Location") != "" || len(resp.Cookies()) != 0 {
		t.Fatalf("wrong code redirected or set a cookie: %v", resp.Header)
	}
	if !strings.Contains(bad, `name="link_code"`) {
		t.Fatalf("400 page has no retry field:\n%s", bad)
	}

	peer := newDot(t, env, "my-dot")
	link := linkCode(t, env, peer)
	code := b.code(verifierA, link)

	if b.cookie == nil || !b.cookie.HttpOnly || b.cookie.MaxAge != 30*24*3600 {
		t.Fatalf("consent cookie = %+v", b.cookie)
	}
	if b.cookie.SameSite != http.SameSiteLaxMode {
		t.Fatalf("cookie SameSite = %v, want Lax", b.cookie.SameSite)
	}

	// The link code is single use, even from a fresh browser.
	other := newBrowser(t, env)
	r2, p2 := other.authorize(other.query(verifierA))
	if r2.StatusCode != http.StatusOK {
		t.Fatalf("authorize: %d", r2.StatusCode)
	}
	if r3, body := other.consent(sealedRequest(t, p2), link); r3.StatusCode != http.StatusBadRequest {
		t.Fatalf("second use of a link code: %d %s", r3.StatusCode, body)
	}

	wantError(t, b.exchange(code, verifierB), "invalid_grant") // wrong verifier

	got := b.exchange(code, verifierA)
	if got.status != http.StatusOK {
		t.Fatalf("exchange: %d %s", got.status, got.Error)
	}
	if !strings.HasPrefix(got.AccessToken, "vat_") || !strings.HasPrefix(got.RefreshToken, "vrt_") ||
		got.TokenType != "Bearer" || got.ExpiresIn != 3600 || got.Scope != "vecnet" {
		t.Fatalf("token response: %+v", got)
	}
	if got.header.Get("Cache-Control") != "no-store" {
		t.Fatalf("token response Cache-Control = %q", got.header.Get("Cache-Control"))
	}

	wantError(t, b.exchange(code, verifierB), "invalid_grant") // wrong verifier, spent code
	wantError(t, b.exchange(code, verifierA), "invalid_grant") // code reuse

	// RFC 6749 §4.1.2: tokens issued from a replayed code are revoked.
	if resp, body := discover(t, env, got.AccessToken); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("access token survived code replay: %d %s", resp.StatusCode, body)
	}
	wantError(t, b.refresh(got.RefreshToken), "invalid_grant")

	// A code is bound to its redirect_uri and client.
	peer2 := newDot(t, env, "dot-two")
	c2 := newBrowser(t, env)
	code2 := c2.code(verifierA, linkCode(t, env, peer2))
	wantError(t, token(t, env, url.Values{
		"grant_type": {"authorization_code"}, "code": {code2}, "client_id": {c2.client},
		"redirect_uri": {"https://chatgpt.com/connector/oauth/other"}, "code_verifier": {verifierA},
	}), "invalid_grant")
	wantError(t, token(t, env, url.Values{
		"grant_type": {"authorization_code"}, "code": {code2}, "client_id": {b.client},
		"redirect_uri": {chatgptRedirect}, "code_verifier": {verifierA},
	}), "invalid_grant")

	// A code lives 10 minutes.
	env.Now.Advance(10*time.Minute + time.Second)
	wantError(t, c2.exchange(code2, verifierA), "invalid_grant")
}

func TestTokenAuthOnMcp(t *testing.T) {
	env := testhub.Start(t)
	_, peer, got := login(t, env)
	other, _ := env.NewPeer("session", "other")

	resp, body := discover(t, env, got.AccessToken)
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, `"result"`) {
		t.Fatalf("discover: %d %s", resp.StatusCode, body)
	}

	msg := sendAs(t, env, got.AccessToken, "k1", other)
	if s := senderOf(t, env, msg); s != peer {
		t.Fatalf("sender = %s, want bound dot peer %s", s, peer)
	}
	// Reading tools act as the bound peer without `as` too.
	threads := tool(t, env, got.AccessToken, "vecnet_threads", map[string]any{})
	if list, _ := threads["threads"].([]any); len(list) != 1 {
		t.Fatalf("vecnet_threads = %v, want the one thread", threads)
	}

	env.Now.Advance(2 * time.Hour)
	resp, body = discover(t, env, got.AccessToken)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expired token: %d %s", resp.StatusCode, body)
	}
	want := `resource_metadata="` + env.URL + `/.well-known/oauth-protected-resource"`
	if h := resp.Header.Get("WWW-Authenticate"); !strings.Contains(h, want) {
		t.Fatalf("WWW-Authenticate = %q, want %s", h, want)
	}

	// A vecnet bearer still works on the same endpoint.
	_, peerSecret := env.NewPeer("session", "bearer")
	if resp, body := discover(t, env, peerSecret); resp.StatusCode != http.StatusOK {
		t.Fatalf("bearer on /mcp: %d %s", resp.StatusCode, body)
	}
}

func TestRefreshRotation(t *testing.T) {
	env := testhub.Start(t)
	b, _, first := login(t, env)

	second := b.refresh(first.RefreshToken)
	if second.status != http.StatusOK {
		t.Fatalf("refresh: %d %s", second.status, second.Error)
	}
	if second.AccessToken == first.AccessToken || second.RefreshToken == first.RefreshToken ||
		!strings.HasPrefix(second.AccessToken, "vat_") || !strings.HasPrefix(second.RefreshToken, "vrt_") ||
		second.ExpiresIn != 3600 || second.Scope != "vecnet" {
		t.Fatalf("refresh response: %+v (first %+v)", second, first)
	}
	if second.header.Get("Cache-Control") != "no-store" {
		t.Fatalf("refresh Cache-Control = %q", second.header.Get("Cache-Control"))
	}
	var rotatedFrom, oldRevoked *string
	db := env.Hub.DB
	if err := db.QueryRow(`SELECT rotated_from FROM oauth_tokens WHERE token_hash = ?`, hash(second.RefreshToken)).Scan(&rotatedFrom); err != nil {
		t.Fatal(err)
	}
	if rotatedFrom == nil || *rotatedFrom != hash(first.RefreshToken) {
		t.Fatalf("rotated_from = %v", rotatedFrom)
	}
	if err := db.QueryRow(`SELECT revoked_at FROM oauth_tokens WHERE token_hash = ?`, hash(first.RefreshToken)).Scan(&oldRevoked); err != nil {
		t.Fatal(err)
	}
	if oldRevoked == nil {
		t.Fatal("old refresh token not revoked")
	}
	if resp, body := discover(t, env, second.AccessToken); resp.StatusCode != http.StatusOK {
		t.Fatalf("new access token: %d %s", resp.StatusCode, body)
	}

	third := b.refresh(second.RefreshToken)
	if third.status != http.StatusOK {
		t.Fatalf("refresh: %d %s", third.status, third.Error)
	}

	// A second login of the same principal is another family.
	apart := b.exchange(b.code(verifierB, ""), verifierB)
	if apart.status != http.StatusOK {
		t.Fatalf("exchange: %d %s", apart.status, apart.Error)
	}

	// Reuse of a mid-chain token revokes every descendant and sibling.
	wantError(t, b.refresh(first.RefreshToken), "invalid_grant")
	wantError(t, b.refresh(third.RefreshToken), "invalid_grant")
	for name, tok := range map[string]string{"first": first.AccessToken, "second": second.AccessToken, "third": third.AccessToken} {
		if resp, body := discover(t, env, tok); resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("%s access token after reuse: %d %s", name, resp.StatusCode, body)
		}
	}
	if resp, body := discover(t, env, apart.AccessToken); resp.StatusCode != http.StatusOK {
		t.Fatalf("another family's access token after reuse: %d %s", resp.StatusCode, body)
	}
	if again := b.refresh(apart.RefreshToken); again.status != http.StatusOK {
		t.Fatalf("another family's refresh token after reuse: %d %s", again.status, again.Error)
	}

	// A refresh token lives 30 days, and a stranger's client_id cannot use it.
	fresh := b.exchange(b.code(verifierB, ""), verifierB)
	if fresh.status != http.StatusOK {
		t.Fatalf("exchange: %d %s", fresh.status, fresh.Error)
	}
	wantError(t, token(t, env, url.Values{
		"grant_type": {"refresh_token"}, "refresh_token": {fresh.RefreshToken}, "client_id": {"someone-else"},
	}), "invalid_grant")
	env.Now.Advance(30*24*time.Hour + time.Minute)
	wantError(t, b.refresh(fresh.RefreshToken), "invalid_grant")
}

func hash(plain string) string {
	sum := sha256.Sum256([]byte(plain))
	return fmt.Sprintf("%x", sum)
}

func TestReturningPrincipalSkipsLinkCode(t *testing.T) {
	env := testhub.Start(t)
	b, peer, first := login(t, env)
	other, _ := env.NewPeer("session", "other")

	var principals int
	if err := env.Hub.DB.QueryRow(`SELECT count(*) FROM oauth_principals`).Scan(&principals); err != nil || principals != 1 {
		t.Fatalf("oauth_principals = %d (%v)", principals, err)
	}

	resp, page := b.authorize(b.query(verifierB))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("authorize: %d %s", resp.StatusCode, page)
	}
	if strings.Contains(page, "link_code") || !strings.Contains(page, "Approve") {
		t.Fatalf("returning page:\n%s", page)
	}
	second := b.exchange(b.code(verifierB, ""), verifierB)
	if second.status != http.StatusOK {
		t.Fatalf("exchange: %d %s", second.status, second.Error)
	}
	if s := senderOf(t, env, sendAs(t, env, second.AccessToken, "k2", other)); s != peer {
		t.Fatalf("sender = %s, want %s", s, peer)
	}
	if err := env.Hub.DB.QueryRow(`SELECT count(*) FROM oauth_principals`).Scan(&principals); err != nil || principals != 1 {
		t.Fatalf("oauth_principals after return = %d (%v)", principals, err)
	}
	if first.AccessToken == second.AccessToken {
		t.Fatal("same access token twice")
	}

	// The same cookie works from a newly registered client: same peer.
	c := &browser{t: t, env: env, client: register(t, env, chatgptRedirect), redirect: chatgptRedirect, cookie: b.cookie}
	third := c.exchange(c.code(verifierA, ""), verifierA)
	if third.status != http.StatusOK {
		t.Fatalf("exchange: %d %s", third.status, third.Error)
	}
	if s := senderOf(t, env, sendAs(t, env, third.AccessToken, "k3", other)); s != peer {
		t.Fatalf("sender = %s, want %s", s, peer)
	}

	// A tampered cookie, or none, asks for a link code again and POST refuses.
	flipped := []byte(b.cookie.Value)
	flipped[len(flipped)-4] ^= 1
	for name, ck := range map[string]*http.Cookie{"tampered": {Name: cookieName, Value: string(flipped)}, "none": nil} {
		x := &browser{t: t, env: env, client: b.client, redirect: b.redirect, cookie: ck}
		resp, page := x.authorize(x.query(verifierA))
		if resp.StatusCode != http.StatusOK || !strings.Contains(page, `name="link_code"`) {
			t.Fatalf("%s cookie: %d\n%s", name, resp.StatusCode, page)
		}
		if resp, body := x.consent(sealedRequest(t, page), ""); resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("%s cookie: consent without link code: %d %s", name, resp.StatusCode, body)
		}
	}

	// A new link code for the bound peer loads the same principal (a lost
	// cookie, another browser), and the old cookie expires after 30 days.
	var prn string
	if err := env.Hub.DB.QueryRow(`SELECT prn_id FROM oauth_principals`).Scan(&prn); err != nil {
		t.Fatal(err)
	}
	fresh := newBrowser(t, env)
	if got := fresh.exchange(fresh.code(verifierA, linkCode(t, env, peer)), verifierA); got.status != http.StatusOK {
		t.Fatalf("relink exchange: %d %s", got.status, got.Error)
	}
	var again string
	if err := env.Hub.DB.QueryRow(`SELECT prn_id FROM peers WHERE peer_id = ?`, peer).Scan(&again); err != nil || again != prn {
		t.Fatalf("relink moved the peer to %s (%v), want %s", again, err, prn)
	}
	if err := env.Hub.DB.QueryRow(`SELECT count(*) FROM oauth_principals`).Scan(&principals); err != nil || principals != 1 {
		t.Fatalf("oauth_principals after relink = %d (%v)", principals, err)
	}
	env.Now.Advance(30*24*time.Hour + time.Minute)
	if _, page := b.authorize(b.query(verifierA)); !strings.Contains(page, `name="link_code"`) {
		t.Fatalf("expired cookie still recognised:\n%s", page)
	}
}

// cimdServer serves client metadata documents over TLS on loopback and points
// the hub's CIMD egress at it. docs maps a path to a document, in which
// "{self}" stands for the URL it is served at.
func cimdServer(t *testing.T, env *testhub.Env, docs map[string]string) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	var srv *httptest.Server
	srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		doc, ok := docs[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, strings.ReplaceAll(doc, "{self}", srv.URL+r.URL.Path))
	}))
	t.Cleanup(srv.Close)
	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())
	env.OAuth.Egress = egress.Policy{AllowPrivate: true, RootCAs: pool}
	return srv, &hits
}

func TestCIMD(t *testing.T) {
	env := testhub.Start(t)
	doc := func(redirect string) string {
		return `{"client_id":"{self}","client_name":"ChatGPT <cimd>","redirect_uris":["` + redirect + `"],` +
			`"grant_types":["authorization_code","refresh_token"],"token_endpoint_auth_method":"none"}`
	}
	srv, hits := cimdServer(t, env, map[string]string{
		"/good.json":   doc(chatgptRedirect),
		"/other.json":  doc("https://chatgpt.com/connector/oauth/abc"),
		"/liar.json":   `{"client_id":"https://elsewhere.example/x.json","redirect_uris":["` + chatgptRedirect + `"]}`,
		"/secret.json": `{"client_id":"{self}","token_endpoint_auth_method":"client_secret_basic","redirect_uris":["` + chatgptRedirect + `"]}`,
		"/notjson":     `<html>`,
	})

	b := &browser{t: t, env: env, client: srv.URL + "/good.json", redirect: chatgptRedirect}
	resp, page := b.authorize(b.query(verifierA))
	if resp.StatusCode != http.StatusOK || !strings.Contains(page, `name="link_code"`) {
		t.Fatalf("authorize with CIMD client: %d\n%s", resp.StatusCode, page)
	}
	if !strings.Contains(page, "ChatGPT &lt;cimd&gt;") {
		t.Fatalf("client_name not shown escaped:\n%s", page)
	}
	if hits.Load() != 1 {
		t.Fatalf("fetches = %d, want 1", hits.Load())
	}

	// Cached for an hour, then fetched again.
	b.authorize(b.query(verifierA))
	if hits.Load() != 1 {
		t.Fatalf("fetches after a cached authorize = %d, want 1", hits.Load())
	}
	env.Now.Advance(time.Hour + time.Second)
	b.authorize(b.query(verifierA))
	if hits.Load() != 2 {
		t.Fatalf("fetches after an hour = %d, want 2", hits.Load())
	}

	// The whole flow works.
	peer := newDot(t, env, "cimd-dot")
	if got := b.exchange(b.code(verifierA, linkCode(t, env, peer)), verifierA); got.status != http.StatusOK {
		t.Fatalf("CIMD exchange: %d %s", got.status, got.Error)
	}

	for _, path := range []string{"/other.json", "/liar.json", "/secret.json", "/notjson", "/missing.json"} {
		x := &browser{t: t, env: env, client: srv.URL + path, redirect: chatgptRedirect}
		resp, page := x.authorize(x.query(verifierA))
		if resp.StatusCode != http.StatusBadRequest || !strings.Contains(page, "invalid_request") {
			t.Fatalf("%s: %d\n%s", path, resp.StatusCode, page)
		}
		if resp.Header.Get("Location") != "" {
			t.Fatalf("%s: redirected to an unvalidated URI: %v", path, resp.Header)
		}
	}

	// A document naming another client caches nothing under that name.
	var stored int
	if err := env.Hub.DB.QueryRow(`SELECT count(*) FROM oauth_clients WHERE client_id = 'https://elsewhere.example/x.json'`).Scan(&stored); err != nil || stored != 0 {
		t.Fatalf("stored a client it did not fetch: %d (%v)", stored, err)
	}

	// The production policy never reaches a loopback address.
	before := hits.Load()
	env.OAuth.Egress = egress.Policy{}
	x := &browser{t: t, env: env, client: srv.URL + "/fresh.json", redirect: chatgptRedirect}
	if resp, page := x.authorize(x.query(verifierA)); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("production egress: %d\n%s", resp.StatusCode, page)
	}
	if hits.Load() != before {
		t.Fatal("production policy connected to a private address")
	}
}

// A CIMD document must not redirect the hub anywhere.
func TestCIMDRedirectRefused(t *testing.T) {
	env := testhub.Start(t)
	var target atomic.Int32
	inner := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { target.Add(1) }))
	t.Cleanup(inner.Close)
	srv := cimdServerRedirecting(t, env, inner.URL)
	x := &browser{t: t, env: env, client: srv.URL + "/c.json", redirect: chatgptRedirect}
	if resp, page := x.authorize(x.query(verifierA)); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("redirecting CIMD: %d\n%s", resp.StatusCode, page)
	}
	if target.Load() != 0 {
		t.Fatal("the hub followed a redirect")
	}
}

// cimdServerRedirecting is a TLS server trusted by the hub's CIMD egress that
// answers everything with a redirect to target.
func cimdServerRedirecting(t *testing.T, env *testhub.Env, target string) *httptest.Server {
	t.Helper()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target, http.StatusFound)
	}))
	t.Cleanup(srv.Close)
	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())
	env.OAuth.Egress = egress.Policy{AllowPrivate: true, RootCAs: pool}
	return srv
}

func TestResourceMismatch(t *testing.T) {
	env := testhub.Start(t)
	b := newBrowser(t, env)
	peer := newDot(t, env, "res-dot")
	code := b.code(verifierA, linkCode(t, env, peer))

	wantError(t, token(t, env, url.Values{
		"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {b.redirect},
		"client_id": {b.client}, "code_verifier": {verifierA}, "resource": {"https://other/mcp"},
	}), "invalid_target")

	// The refused request did not spend the code.
	got := b.exchange(code, verifierA)
	if got.status != http.StatusOK {
		t.Fatalf("exchange: %d %s", got.status, got.Error)
	}

	// Refresh checks it too, without burning the token.
	wantError(t, token(t, env, url.Values{
		"grant_type": {"refresh_token"}, "refresh_token": {got.RefreshToken}, "resource": {"https://other/mcp"},
	}), "invalid_target")
	if again := b.refresh(got.RefreshToken); again.status != http.StatusOK {
		t.Fatalf("refresh after a refused resource: %d %s", again.status, again.Error)
	}

	// authorize refuses it through the validated redirect.
	q := b.query(verifierA)
	q.Set("resource", "https://other/mcp")
	resp, body := b.authorize(q)
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("authorize resource: %d %s", resp.StatusCode, body)
	}
	loc, err := url.Parse(resp.Header.Get("Location"))
	if err != nil || loc.Query().Get("error") != "invalid_target" || loc.Query().Get("state") != "st-123" {
		t.Fatalf("Location = %v", resp.Header.Get("Location"))
	}

	// An access token bound to another resource is refused on /mcp.
	if _, err := env.Hub.DB.Exec(`UPDATE oauth_tokens SET resource = 'https://other/mcp' WHERE kind = 'access'`); err != nil {
		t.Fatal(err)
	}
	if resp, body := discover(t, env, got.AccessToken); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("token for another resource: %d %s", resp.StatusCode, body)
	}
}

func TestPKCERequired(t *testing.T) {
	env := testhub.Start(t)
	b := newBrowser(t, env)
	for name, edit := range map[string]func(url.Values){
		"no challenge": func(q url.Values) { q.Del("code_challenge") },
		"no method":    func(q url.Values) { q.Del("code_challenge_method") },
		"plain":        func(q url.Values) { q.Set("code_challenge_method", "plain") },
		"short":        func(q url.Values) { q.Set("code_challenge", "abc") },
		"not base64":   func(q url.Values) { q.Set("code_challenge", strings.Repeat("!", 43)) },
	} {
		q := b.query(verifierA)
		edit(q)
		resp, body := b.authorize(q)
		if resp.StatusCode != http.StatusFound {
			t.Fatalf("%s: %d %s", name, resp.StatusCode, body)
		}
		loc, err := url.Parse(resp.Header.Get("Location"))
		if err != nil || loc.Query().Get("error") != "invalid_request" {
			t.Fatalf("%s: Location = %v", name, resp.Header.Get("Location"))
		}
	}
	q := b.query(verifierA)
	q.Set("response_type", "token")
	resp, _ := b.authorize(q)
	loc, _ := url.Parse(resp.Header.Get("Location"))
	if loc == nil || loc.Query().Get("error") != "unsupported_response_type" {
		t.Fatalf("response_type=token: Location = %v", resp.Header.Get("Location"))
	}
	q = b.query(verifierA)
	q.Set("scope", "vecnet admin")
	resp, _ = b.authorize(q)
	loc, _ = url.Parse(resp.Header.Get("Location"))
	if loc == nil || loc.Query().Get("error") != "invalid_scope" {
		t.Fatalf("scope: Location = %v", resp.Header.Get("Location"))
	}

	// A malformed verifier never matches, even when its hash does.
	peer := newDot(t, env, "pkce-dot")
	short := "short"
	b2 := newBrowser(t, env)
	resp, page := b2.authorize(b2.query(short))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("authorize: %d %s", resp.StatusCode, page)
	}
	cresp, body := b2.consent(sealedRequest(t, page), linkCode(t, env, peer))
	if cresp.StatusCode != http.StatusFound {
		t.Fatalf("consent: %d %s", cresp.StatusCode, body)
	}
	cloc, _ := url.Parse(cresp.Header.Get("Location"))
	wantError(t, b2.exchange(cloc.Query().Get("code"), short), "invalid_grant")
}

func TestRedirectURIRules(t *testing.T) {
	env := testhub.Start(t)

	// testhub adds the loopback pattern; these all stay refused.
	for _, uri := range []string{
		"https://evil.example/cb",
		"https://chatgpt.com/connector_platform_oauth_redirect/extra",
		"https://chatgpt.com/connector/oauth/a/b",
		"https://chatgpt.com/connector/oauth/",
		"https://chatgpt.com/connector/oauth/..",
		"https://chatgpt.com/connector/oauth/a%2fb",
		"https://chatgpt.com.evil.example/connector/oauth/abc",
		"http://chatgpt.com/connector_platform_oauth_redirect",
		"http://127.0.0.1:8080@evil.example/cb",
		"http://user@127.0.0.1:8080/cb",
		"http://127.0.0.1/cb",
		"http://127.0.0.1:/cb",
		"http://127.0.0.1:99999/cb",
		"http://127.0.0.1:0x50/cb",
		"http://127.0.0.1.evil.example:80/cb",
		"http://localhost:8080/cb",
		"https://127.0.0.1:8080/cb",
		"http://127.0.0.1:8080/cb#frag",
	} {
		resp, body := env.Do("", "POST", "/oauth/register", map[string]any{"redirect_uris": []string{chatgptRedirect, uri}})
		if resp.StatusCode != http.StatusBadRequest || !strings.Contains(string(body), "invalid_redirect_uri") {
			t.Errorf("register %s: %d %s", uri, resp.StatusCode, body)
		}
	}
	for _, uri := range []string{
		chatgptRedirect,
		"https://chatgpt.com/connector/oauth/abc_DEF-123",
		"http://127.0.0.1:54321/callback",
	} {
		register(t, env, uri)
	}

	// The production list has no loopback pattern.
	env.OAuth.AllowedRedirects = oauth.DefaultRedirects
	if resp, body := env.Do("", "POST", "/oauth/register", map[string]any{"redirect_uris": []string{"http://127.0.0.1:54321/callback"}}); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("production loopback: %d %s", resp.StatusCode, body)
	}

	// An unregistered redirect_uri never redirects, even one on the allow list.
	b := newBrowser(t, env)
	q := b.query(verifierA)
	q.Set("redirect_uri", "https://chatgpt.com/connector/oauth/other")
	resp, page := b.authorize(q)
	if resp.StatusCode != http.StatusBadRequest || resp.Header.Get("Location") != "" {
		t.Fatalf("unregistered redirect: %d %v", resp.StatusCode, resp.Header)
	}
	if !strings.Contains(page, "invalid_request") {
		t.Fatalf("page:\n%s", page)
	}

	// Registration refuses machine-to-machine grants (PROTOCOL §4.5).
	dresp, dbody := env.Do("", "POST", "/oauth/register", map[string]any{
		"redirect_uris": []string{chatgptRedirect}, "grant_types": []string{"client_credentials"},
	})
	if dresp.StatusCode != http.StatusBadRequest || !strings.Contains(string(dbody), "invalid_client_metadata") {
		t.Fatalf("client_credentials: %d %s", dresp.StatusCode, dbody)
	}
	dresp, dbody = env.Do("", "POST", "/oauth/register", map[string]any{"redirect_uris": []string{}})
	if dresp.StatusCode != http.StatusBadRequest {
		t.Fatalf("no redirects: %d %s", dresp.StatusCode, dbody)
	}

	// The token endpoint offers no other grant.
	got := token(t, env, url.Values{"grant_type": {"client_credentials"}, "client_id": {b.client}})
	if got.status != http.StatusBadRequest || got.Error != "unsupported_grant_type" {
		t.Fatalf("client_credentials token: %d %s", got.status, got.Error)
	}
}

func TestDeniedConsent(t *testing.T) {
	env := testhub.Start(t)
	b := newBrowser(t, env)
	resp, page := b.authorize(b.query(verifierA))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("authorize: %d", resp.StatusCode)
	}
	rresp, body := postForm(t, env, "/oauth/authorize", url.Values{"request": {sealedRequest(t, page)}}, nil)
	if rresp.StatusCode != http.StatusFound {
		t.Fatalf("deny: %d %s", rresp.StatusCode, body)
	}
	loc, err := url.Parse(rresp.Header.Get("Location"))
	if err != nil || loc.Query().Get("error") != "access_denied" || loc.Query().Get("state") != "st-123" ||
		!strings.HasPrefix(rresp.Header.Get("Location"), chatgptRedirect+"?") {
		t.Fatalf("Location = %v", rresp.Header.Get("Location"))
	}

	// The form is sealed: no field of it can be forged, and it expires.
	if resp, body := postForm(t, env, "/oauth/authorize", url.Values{
		"request": {"v1:AAAA"}, "approve": {"1"}, "link_code": {"ABCDEFGH"},
	}, nil); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("forged request: %d %s", resp.StatusCode, body)
	}

	// A consent cookie is sealed with the same key; it is no request.
	x := newBrowser(t, env)
	x.code(verifierA, linkCode(t, env, newDot(t, env, "purpose-dot")))
	if resp, body := postForm(t, env, "/oauth/authorize", url.Values{
		"request": {x.cookie.Value}, "approve": {"1"},
	}, nil); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("sealed value of another purpose: %d %s", resp.StatusCode, body)
	}

	// An expired form is refused even with a good link code, which stays
	// unspent.
	env.Now.Advance(16 * time.Minute)
	good := linkCode(t, env, newDot(t, env, "late-dot"))
	if resp, body := b.consent(sealedRequest(t, page), good); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expired request: %d %s", resp.StatusCode, body)
	}
	fresh := newBrowser(t, env)
	fresh.code(verifierA, good)
}

func TestConsentPageHardening(t *testing.T) {
	env := testhub.Start(t)
	b := newBrowser(t, env)
	resp, page := b.authorize(b.query(verifierA))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("authorize: %d", resp.StatusCode)
	}
	csp := resp.Header.Get("Content-Security-Policy")
	for _, want := range []string{"default-src 'self'", "frame-ancestors 'none'", "base-uri 'none'", "form-action 'self' https://chatgpt.com"} {
		if !strings.Contains(csp, want) {
			t.Errorf("CSP %q lacks %q", csp, want)
		}
	}
	if resp.Header.Get("Cache-Control") != "no-store" || resp.Header.Get("Referrer-Policy") != "no-referrer" {
		t.Errorf("headers: %v", resp.Header)
	}
	low := strings.ToLower(page)
	for _, bad := range []string{"<script", "<style", " style=", "onclick=", "onsubmit="} {
		if strings.Contains(low, bad) {
			t.Errorf("page contains %q:\n%s", bad, page)
		}
	}
	if !strings.Contains(low, `method="post"`) {
		t.Errorf("no POST form:\n%s", page)
	}
	if !strings.Contains(page, "formnovalidate") {
		t.Errorf("Deny would be blocked by the required link code:\n%s", page)
	}
	if !strings.Contains(page, `href="/oauth/consent.css"`) {
		t.Errorf("no stylesheet link:\n%s", page)
	}
	css, cssBody := get(t, env.URL+"/oauth/consent.css", nil)
	if css.StatusCode != http.StatusOK || !strings.HasPrefix(css.Header.Get("Content-Type"), "text/css") || cssBody == "" {
		t.Fatalf("stylesheet: %d %s", css.StatusCode, css.Header.Get("Content-Type"))
	}

	// Page text is escaped.
	r2, body := env.Do("", "POST", "/oauth/register", map[string]any{
		"client_name": `<img src=x onerror=alert(1)>`, "redirect_uris": []string{chatgptRedirect},
	})
	if r2.StatusCode != http.StatusCreated {
		t.Fatalf("register: %d %s", r2.StatusCode, body)
	}
	var reg struct {
		ClientID string `json:"client_id"`
	}
	if err := json.Unmarshal(body, &reg); err != nil {
		t.Fatal(err)
	}
	x := &browser{t: t, env: env, client: reg.ClientID, redirect: chatgptRedirect}
	if _, p := x.authorize(x.query(verifierA)); strings.Contains(p, "<img") {
		t.Fatalf("client_name not escaped:\n%s", p)
	}
}

func TestSecretsStoredHashed(t *testing.T) {
	env := testhub.Start(t)
	b := newBrowser(t, env)
	peer := newDot(t, env, "hash-dot")
	link := linkCode(t, env, peer)
	code := b.code(verifierA, link)
	got := b.exchange(code, verifierA)
	if got.status != http.StatusOK {
		t.Fatalf("exchange: %d %s", got.status, got.Error)
	}

	var n int
	check := func(what, q string, hashes ...string) {
		t.Helper()
		for _, h := range hashes {
			if err := env.Hub.DB.QueryRow(q, h).Scan(&n); err != nil || n != 1 {
				t.Fatalf("%s: no row for its hash (%d, %v)", what, n, err)
			}
		}
	}
	check("code", `SELECT count(*) FROM oauth_codes WHERE code_hash = ?`, hash(code))
	check("tokens", `SELECT count(*) FROM oauth_tokens WHERE token_hash = ?`, hash(got.AccessToken), hash(got.RefreshToken))
	check("link", `SELECT count(*) FROM dot_links WHERE code_hash = ?`, hash(link))

	// No plaintext anywhere, audit included.
	for _, table := range []string{"oauth_codes", "oauth_tokens", "dot_links", "audit"} {
		rows, err := env.Hub.DB.Query(`SELECT * FROM ` + table)
		if err != nil {
			t.Fatal(err)
		}
		cols, _ := rows.Columns()
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		for rows.Next() {
			if err := rows.Scan(ptrs...); err != nil {
				t.Fatal(err)
			}
			line := fmt.Sprint(vals...)
			for _, secret := range []string{code, link, got.AccessToken, got.RefreshToken} {
				if strings.Contains(line, secret) {
					t.Fatalf("%s holds a plaintext secret: %s", table, line)
				}
			}
		}
		rows.Close()
	}
}

func TestPeerStateEndsAccess(t *testing.T) {
	env := testhub.Start(t)
	b, peer, got := login(t, env)
	ctx := context.Background()

	if err := env.Hub.SetPeerDisabled(ctx, peer, true); err != nil {
		t.Fatal(err)
	}
	if resp, body := discover(t, env, got.AccessToken); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("disabled peer: %d %s", resp.StatusCode, body)
	}
	if err := env.Hub.SetPeerDisabled(ctx, peer, false); err != nil {
		t.Fatal(err)
	}
	if resp, body := discover(t, env, got.AccessToken); resp.StatusCode != http.StatusOK {
		t.Fatalf("re-enabled peer: %d %s", resp.StatusCode, body)
	}

	// The consent cookie stops counting too: the page asks for a link code
	// and Approve alone is refused.
	wantLinkCode := func(when string) {
		t.Helper()
		resp, page := b.authorize(b.query(verifierB))
		if resp.StatusCode != http.StatusOK || !strings.Contains(page, `name="link_code"`) {
			t.Fatalf("%s: %d\n%s", when, resp.StatusCode, page)
		}
		if resp, body := b.consent(sealedRequest(t, page), ""); resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("%s: approve without link code: %d %s", when, resp.StatusCode, body)
		}
	}
	if err := env.Hub.SetPeerDisabled(ctx, peer, true); err != nil {
		t.Fatal(err)
	}
	wantLinkCode("disabled peer")
	if err := env.Hub.SetPeerDisabled(ctx, peer, false); err != nil {
		t.Fatal(err)
	}

	// The operator moving the peer away ends the binding.
	prn, _ := env.NewPrincipal("elsewhere")
	if err := env.Hub.MovePeer(ctx, peer, prn); err != nil {
		t.Fatal(err)
	}
	if resp, body := discover(t, env, got.AccessToken); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("moved peer: %d %s", resp.StatusCode, body)
	}
	wantLinkCode("moved peer")

	// A fresh link code is the operator binding it back, to the same principal.
	again := b.exchange(b.code(verifierB, linkCode(t, env, peer)), verifierB)
	if again.status != http.StatusOK {
		t.Fatalf("relink exchange: %d %s", again.status, again.Error)
	}
	if resp, body := discover(t, env, again.AccessToken); resp.StatusCode != http.StatusOK {
		t.Fatalf("relinked peer: %d %s", resp.StatusCode, body)
	}
}

func TestBoundPeerMovesToDotPrincipal(t *testing.T) {
	env := testhub.Start(t)
	_, peer, _ := login(t, env)
	var name, oauthPrn, subject string
	if err := env.Hub.DB.QueryRow(
		`SELECT pr.name, o.prn_id, o.subject FROM oauth_principals o
		 JOIN peers p ON p.peer_id = o.peer_id JOIN principals pr ON pr.prn_id = p.prn_id
		 WHERE o.peer_id = ?`, peer).Scan(&name, &oauthPrn, &subject); err != nil {
		t.Fatal(err)
	}
	if name != "dot:my-dot" {
		t.Fatalf("principal name = %q", name)
	}
	if !strings.HasPrefix(subject, "oauth:") {
		t.Fatalf("subject = %q", subject)
	}
	var got string
	if err := env.Hub.DB.QueryRow(`SELECT prn_id FROM peers WHERE peer_id = ?`, peer).Scan(&got); err != nil || got != oauthPrn {
		t.Fatalf("peer principal = %s (%v), want %s", got, err, oauthPrn)
	}
}

func TestTokenEndpointRejectsMalformed(t *testing.T) {
	env := testhub.Start(t)
	for name, form := range map[string]url.Values{
		"no grant":      {},
		"repeated code": {"grant_type": {"authorization_code"}, "code": {"a", "b"}, "client_id": {"c"}, "redirect_uri": {"r"}, "code_verifier": {verifierA}},
		"no verifier":   {"grant_type": {"authorization_code"}, "code": {"a"}, "client_id": {"c"}, "redirect_uri": {"r"}},
		"no refresh":    {"grant_type": {"refresh_token"}},
	} {
		t.Run(name, func(t *testing.T) { wantError(t, token(t, env, form), "invalid_request") })
	}
	// Parameters in the query are not parameters.
	resp, body := postForm(t, env, "/oauth/token?grant_type=refresh_token&refresh_token=x", url.Values{}, nil)
	if resp.StatusCode != http.StatusBadRequest || !strings.Contains(body, "invalid_request") {
		t.Fatalf("query params: %d %s", resp.StatusCode, body)
	}
}
