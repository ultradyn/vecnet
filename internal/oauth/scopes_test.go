package oauth_test

import (
	"encoding/json"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/XertroV/vecnet/internal/oauth"
	"github.com/XertroV/vecnet/internal/testhub"
)

// New consent upgrades the grant even if an older client asks only for vecnet;
// already-issued credentials must never gain that permission through refresh.
func TestEventScopeGrantMigration(t *testing.T) {
	env := testhub.Start(t)
	b, _, fresh := login(t, env)
	if fresh.Scope != oauth.AuthorizationScopes {
		t.Fatalf("new grant scope = %q", fresh.Scope)
	}
	// Model an access/refresh pair issued before Events scopes existed.
	if _, err := env.Hub.DB.Exec(`UPDATE oauth_tokens SET scope = 'vecnet'`); err != nil {
		t.Fatal(err)
	}
	for _, method := range []string{"events/list", "tools/list"} {
		resp, _ := mcp(t, env, fresh.AccessToken, method, map[string]any{})
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("legacy grant %s status = %d", method, resp.StatusCode)
		}
	}
	for _, method := range []string{"events/subscribe", "events/unsubscribe"} {
		resp, body := mcp(t, env, fresh.AccessToken, method, map[string]any{})
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("legacy grant %s status = %d", method, resp.StatusCode)
		}
		challenge := resp.Header.Get("WWW-Authenticate")
		for _, part := range []string{`error="insufficient_scope"`, `scope="events:write"`, env.URL + "/.well-known/oauth-protected-resource/mcp"} {
			if !strings.Contains(challenge, part) {
				t.Fatalf("scope challenge missing %q", part)
			}
		}
		var out struct {
			Error struct {
				Code int `json:"code"`
			} `json:"error"`
		}
		if err := json.Unmarshal([]byte(body), &out); err != nil || out.Error.Code != -32003 {
			t.Fatal("missing insufficient-scope RPC error")
		}
	}
	elevated := token(t, env, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {fresh.RefreshToken}, "scope": {oauth.AuthorizationScopes}})
	wantError(t, elevated, "invalid_scope")
	rotated := b.refresh(fresh.RefreshToken)
	if rotated.status != http.StatusOK || rotated.Scope != oauth.Scope {
		t.Fatalf("legacy refresh status=%d scope=%q", rotated.status, rotated.Scope)
	}
	resp, _ := mcp(t, env, rotated.AccessToken, "events/subscribe", map[string]any{})
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("rotated legacy grant status = %d", resp.StatusCode)
	}
}

func TestRefreshScopeNarrowingCannotRegainEvents(t *testing.T) {
	env := testhub.Start(t)
	b, _, fresh := login(t, env)
	narrow := token(t, env, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {fresh.RefreshToken}, "scope": {oauth.Scope}})
	if narrow.status != http.StatusOK || narrow.Scope != oauth.Scope {
		t.Fatalf("narrowed grant status=%d scope=%q", narrow.status, narrow.Scope)
	}
	wantError(t, token(t, env, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {narrow.RefreshToken}, "scope": {oauth.AuthorizationScopes}}), "invalid_scope")
	if rotated := b.refresh(narrow.RefreshToken); rotated.status != http.StatusOK || rotated.Scope != oauth.Scope {
		t.Fatal("scope refusal spent or elevated the narrow refresh grant")
	}
}

func TestEveryToolDeclaresOAuthSecurity(t *testing.T) {
	env := testhub.Start(t)
	_, _, tok := login(t, env)
	resp, body := mcp(t, env, tok.AccessToken, "tools/list", map[string]any{})
	if resp.StatusCode != http.StatusOK {
		t.Fatal("tools/list failed")
	}
	var out struct {
		Result struct {
			Tools []struct {
				Name     string `json:"name"`
				Security []struct {
					Type   string   `json:"type"`
					Scopes []string `json:"scopes"`
				} `json:"securitySchemes"`
			} `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Result.Tools) == 0 {
		t.Fatal("no tools discovered")
	}
	for _, tool := range out.Result.Tools {
		if len(tool.Security) != 1 || tool.Security[0].Type != "oauth2" || !slices.Equal(tool.Security[0].Scopes, []string{oauth.Scope}) {
			t.Errorf("tool %s lacks its OAuth scope", tool.Name)
		}
	}
}

func TestEventOnlyAuthorizationIncludesBaseScope(t *testing.T) {
	env := testhub.Start(t)
	b := newBrowser(t, env)
	q := b.query(verifierA)
	q.Set("scope", oauth.ScopeEventsRead+" "+oauth.ScopeEventsWrite)
	resp, page := b.authorize(q)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("event scope authorization status = %d", resp.StatusCode)
	}
	for _, scope := range strings.Fields(oauth.AuthorizationScopes) {
		if !strings.Contains(page, "<code>"+scope+"</code>") {
			t.Errorf("consent page omits scope %s", scope)
		}
	}
	peer := newDot(t, env, "event-scoped-dot")
	resp, _ = b.consent(sealedRequest(t, page), linkCode(t, env, peer))
	loc, err := url.Parse(resp.Header.Get("Location"))
	if err != nil || loc.Query().Get("code") == "" {
		t.Fatal("consent did not issue an authorization code")
	}
	tok := b.exchange(loc.Query().Get("code"), verifierA)
	if tok.status != http.StatusOK || tok.Scope != oauth.AuthorizationScopes {
		t.Fatalf("event grant status=%d scope=%q", tok.status, tok.Scope)
	}
}

func TestNativeBearerEventScopesUnchanged(t *testing.T) {
	env := testhub.Start(t)
	_, secret := env.NewPeer("session", "native-event-peer")
	cb := testhub.NewCallback(t)
	subscribeDot(t, env, secret, cb)
	resp, body := mcp(t, env, secret, "events/unsubscribe", map[string]any{
		"name": "vecnet.message", "arguments": map[string]any{}, "delivery": map[string]any{"mode": "webhook", "url": cb.URL},
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("native unsubscribe status = %d", resp.StatusCode)
	}
	var out struct {
		Error json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal([]byte(body), &out); err != nil || len(out.Error) != 0 {
		t.Fatal("native bearer unsubscribe failed")
	}
}

// Every successful refresh must still issue a token the resource server can
// authenticate. A supported subset without vecnet is not a usable grant.
func TestRefreshRejectsDroppingBaseScope(t *testing.T) {
	env := testhub.Start(t)
	b, _, fresh := login(t, env)
	for _, scope := range []string{oauth.ScopeEventsRead, oauth.ScopeEventsWrite, oauth.ScopeEventsRead + " " + oauth.ScopeEventsWrite} {
		refused := token(t, env, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {fresh.RefreshToken}, "scope": {scope}})
		wantError(t, refused, "invalid_scope")
	}
	rotated := b.refresh(fresh.RefreshToken)
	if rotated.status != http.StatusOK || rotated.Scope != oauth.AuthorizationScopes {
		t.Fatal("dropping-base-scope refusal consumed or changed the grant")
	}
	resp, _ := mcp(t, env, rotated.AccessToken, "tools/list", map[string]any{})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("refresh returned unusable access token, status=%d", resp.StatusCode)
	}
}
