package oauth

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/XertroV/vecnet/internal/auth"
	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/mcpserver"
)

// ResolveDotPeer names the dot peer principalID is bound to, "" when it has
// none. It is the seam mcpserver.Server and events.Worker read the binding
// through, and it applies the rule TokenAuth does: a principal retired by a
// later link code, or whose peer the operator has moved away, is bound to
// nothing, which stops its Events subscriptions (PROTOCOL §4.4, §4.5). A
// disabled peer is left to the caller; TokenAuth and the worker both refuse it.
func (s *Server) ResolveDotPeer(ctx context.Context, principalID string) (string, error) {
	var peer string
	err := s.DB.QueryRowContext(ctx,
		`SELECT o.peer_id FROM oauth_principals o
		 JOIN peers p ON p.peer_id = o.peer_id
		 WHERE o.prn_id = ? AND p.prn_id = o.prn_id`, principalID).Scan(&peer)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("oauth: resolve dot peer: %w", err)
	}
	return peer, nil
}

// TokenAuth is the resource server: mcpserver.Authenticator for OAuth access
// tokens on /mcp. Every call checks expiry, revocation, the audience
// (resource), the scope and the binding (PROTOCOL §4.5): a disabled peer, one
// the operator has moved away, or a later link code ends access at once.
type TokenAuth struct {
	S *Server
}

// Authenticate resolves "Authorization: Bearer vat_…" to the principal acting
// as its bound dot peer. Anything else is auth.ErrUnauthenticated, so a Chain
// can try its other authenticators.
func (a *TokenAuth) Authenticate(ctx context.Context, r *http.Request) (mcpserver.Principal, error) {
	tok := mcpserver.BearerToken(r)
	if !strings.HasPrefix(tok, accessPrefix) {
		return mcpserver.Principal{}, auth.ErrUnauthenticated
	}
	s := a.S
	var clientID, prn, peer, peerKind, scope, resource, expires string
	err := s.DB.QueryRowContext(ctx,
		`SELECT t.client_id, o.prn_id, o.peer_id, p.kind, t.scope, t.resource, t.expires_at
		 FROM oauth_tokens t
		 JOIN oauth_principals o ON o.prn_id = t.principal_id
		 JOIN peers p ON p.peer_id = o.peer_id
		 WHERE t.token_hash = ? AND t.kind = 'access' AND t.revoked_at IS NULL
		   AND p.disabled = 0 AND p.prn_id = o.prn_id`, auth.HashSecret(tok)).
		Scan(&clientID, &prn, &peer, &peerKind, &scope, &resource, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return mcpserver.Principal{}, auth.ErrUnauthenticated
	}
	if err != nil {
		return mcpserver.Principal{}, fmt.Errorf("oauth: find token: %w", err)
	}
	exp, err := envelope.ParseTime(expires)
	if err != nil {
		return mcpserver.Principal{}, fmt.Errorf("oauth: token expiry: %w", err)
	}
	scopes := strings.Fields(scope)
	if !s.now().Before(exp) || resource != s.resource() || !slices.Contains(scopes, Scope) {
		return mcpserver.Principal{}, auth.ErrUnauthenticated
	}
	return mcpserver.Principal{
		Caller: auth.Caller{Kind: "principal", PrincipalID: prn, PeerID: peer, PeerKind: peerKind},
		OAuth:  &mcpserver.OAuthInfo{PrincipalID: prn, ClientID: clientID, Scopes: scopes, Expires: exp},
	}, nil
}
