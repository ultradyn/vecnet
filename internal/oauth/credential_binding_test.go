package oauth_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/XertroV/vecnet/internal/auth"
	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/oauth"
	"github.com/XertroV/vecnet/internal/testhub"
)

// Exercise the real authenticator with an access token minted by the OAuth
// flow: MCP handlers pass Caller alone into the hub transfer boundary.
func TestTokenAuthCarriesTypedCredentialBinding(t *testing.T) {
	env := testhub.Start(t)
	b, peer, tokens := login(t, env)
	req, err := http.NewRequest(http.MethodPost, env.URL+"/mcp", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+tokens.AccessToken)
	principal, err := (&oauth.TokenAuth{S: env.OAuth}).Authenticate(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	c := principal.Caller
	if c.CredentialID != "" || c.Kind != "principal" || c.PeerID != peer || c.PeerKind != "dot" || c.OAuth == (auth.OAuthCredential{}) {
		t.Fatal("TokenAuth did not carry a distinct OAuth credential into Caller")
	}
	binding := c.OAuth
	if binding.TokenHash != auth.HashSecret(tokens.AccessToken) || binding.TokenHash != principal.OAuth.TokenHash ||
		binding.ClientID != b.client || binding.Resource != env.OAuth.Issuer+"/mcp" ||
		binding.Scope != oauth.AuthorizationScopes || binding.RequiredScope != oauth.Scope ||
		!binding.ExpiresAt.Equal(principal.OAuth.Expires) || c.PrincipalID != principal.OAuth.PrincipalID {
		t.Fatal("Caller lost the authenticated token identity or authorization binding")
	}
	if !auth.OAuthCallerActive(context.Background(), env.Hub.DB, c, env.Now.Now()) {
		t.Fatal("fresh TokenAuth caller failed revalidation")
	}
	// MCP session lookup uses Caller equality on later HTTP requests, and must
	// accept a new authentication of this same token rather than compare pointer
	// allocations. The GET stream regression exercises that lookup end to end.
	repeated, err := (&oauth.TokenAuth{S: env.OAuth}).Authenticate(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if repeated.Caller != c {
		t.Fatal("repeated TokenAuth resolutions must have equal Caller values")
	}
	raw, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), binding.TokenHash) || strings.Contains(string(raw), tokens.AccessToken) {
		t.Fatal("serialized Caller exposed OAuth credential identity")
	}
	if _, err := env.Hub.DB.Exec(`UPDATE oauth_tokens SET revoked_at=? WHERE token_hash=?`, envelope.FormatTime(env.Now.Now()), binding.TokenHash); err != nil {
		t.Fatal(err)
	}
	if auth.OAuthCallerActive(context.Background(), env.Hub.DB, c, env.Now.Now()) {
		t.Fatal("Caller revalidation missed OAuth revocation")
	}
}
