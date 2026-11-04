package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/XertroV/vecnet/internal/auth"
)

const (
	ScopeVecnet      = "vecnet"
	ScopeEventsRead  = "events:read"
	ScopeEventsWrite = "events:write"
)

// ErrExpiredToken preserves the 401 authentication contract while letting
// request diagnostics distinguish expiry from unknown credentials.
var ErrExpiredToken = fmt.Errorf("oauth token expired: %w", auth.ErrUnauthenticated)

// OAuthInfo describes an OAuth access token behind a Principal. Task 6 (the
// OAuth resource server) fills it; bearer callers leave it nil.
type OAuthInfo struct {
	PrincipalID string
	ClientID    string
	Scopes      []string
	Expires     time.Time
	TokenHash   string // of this access token, never the token: revoking it stops what it subscribed
}

// Principal is who an /mcp request acts as. Caller is the same identity the
// HTTP API uses, so tool handlers bind to the same hub methods.
type Principal struct {
	Caller auth.Caller
	OAuth  *OAuthInfo // nil for a vecnet bearer
}

// HasScope checks only OAuth grants. Native vecnet bearer peers retain their
// existing authorization behavior; their hub permissions still apply.
func (p Principal) HasScope(scope string) bool {
	return p.OAuth == nil || slices.Contains(p.OAuth.Scopes, scope)
}

// RequireScope reports an OAuth authorization failure explicitly so a client
// can reauthorize an older grant. It never turns an old valid token into 401.
func (s *Server) RequireScope(w http.ResponseWriter, r *http.Request, p Principal, scope string, id json.RawMessage) bool {
	if p.HasScope(scope) {
		return true
	}
	noteDiagAuth(r, "insufficient_scope")
	w.Header().Set("WWW-Authenticate", `Bearer error="insufficient_scope", scope="`+scope+`", resource_metadata="`+s.PublicOrigin+`/.well-known/oauth-protected-resource/mcp"`)
	if len(id) == 0 {
		id = nullID
	}
	writeRPCStatus(w, http.StatusForbidden, id, &RPCError{Code: -32003, Message: "Insufficient scope"})
	return false
}

// Authenticator resolves the credential on an /mcp request. An error
// wrapping auth.ErrUnauthenticated is a 401 carrying
// WWW-Authenticate: Bearer resource_metadata="<public>/.well-known/oauth-protected-resource";
// any other error is a 500.
type Authenticator interface {
	Authenticate(ctx context.Context, r *http.Request) (Principal, error)
}

// BearerAuth accepts vecnet peer, principal and operator secrets.
type BearerAuth struct {
	Resolver *auth.Resolver
}

// Authenticate resolves "Authorization: Bearer <secret>".
func (b *BearerAuth) Authenticate(ctx context.Context, r *http.Request) (Principal, error) {
	c, err := b.Resolver.Resolve(ctx, BearerToken(r))
	if err != nil {
		return Principal{}, err
	}
	return Principal{Caller: c}, nil
}

// Chain tries each Authenticator in order; the first success wins. When all
// fail the result is auth.ErrUnauthenticated (auth.ErrPeerDisabled if that is
// what one said, which still means 401), unless one failed for another reason
// (a store error), which is returned instead so it surfaces as 500.
type Chain []Authenticator

// Authenticate implements Authenticator.
func (c Chain) Authenticate(ctx context.Context, r *http.Request) (Principal, error) {
	var other error
	refused := auth.ErrUnauthenticated
	var failed Principal
	for _, a := range c {
		p, err := a.Authenticate(ctx, r)
		if err == nil {
			return p, nil
		}
		if errors.Is(err, auth.ErrPeerDisabled) || errors.Is(err, ErrExpiredToken) {
			refused = err
			failed = p
		}
		if !errors.Is(err, auth.ErrUnauthenticated) && other == nil {
			other = err
		}
	}
	if other != nil {
		return Principal{}, other
	}
	return failed, refused
}

// BearerToken extracts the token from "Authorization: Bearer <token>".
func BearerToken(r *http.Request) string {
	scheme, tok, ok := strings.Cut(r.Header.Get("Authorization"), " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return ""
	}
	return strings.TrimSpace(tok)
}

type principalKey struct{}

// PrincipalFromContext returns the Principal ServeHTTP authenticated for
// this request; the zero Principal outside a dispatched handler.
func PrincipalFromContext(ctx context.Context) Principal {
	p, _ := ctx.Value(principalKey{}).(Principal)
	return p
}

func withPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, principalKey{}, p)
}
