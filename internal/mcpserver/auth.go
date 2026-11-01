package mcpserver

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/XertroV/vecnet/internal/auth"
)

// OAuthInfo describes an OAuth access token behind a Principal. Task 6 (the
// OAuth resource server) fills it; bearer callers leave it nil.
type OAuthInfo struct {
	PrincipalID string
	ClientID    string
	Scopes      []string
	Expires     time.Time
}

// Principal is who an /mcp request acts as. Caller is the same identity the
// HTTP API uses, so tool handlers bind to the same hub methods.
type Principal struct {
	Caller auth.Caller
	OAuth  *OAuthInfo // nil for a vecnet bearer
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
	c, err := b.Resolver.Resolve(ctx, bearerToken(r))
	if err != nil {
		return Principal{}, err
	}
	return Principal{Caller: c}, nil
}

// Chain tries each Authenticator in order; the first success wins. When all
// fail the result is auth.ErrUnauthenticated, unless one failed for another
// reason (a store error), which is returned instead so it surfaces as 500.
type Chain []Authenticator

// Authenticate implements Authenticator.
func (c Chain) Authenticate(ctx context.Context, r *http.Request) (Principal, error) {
	var other error
	for _, a := range c {
		p, err := a.Authenticate(ctx, r)
		if err == nil {
			return p, nil
		}
		if !errors.Is(err, auth.ErrUnauthenticated) && other == nil {
			other = err
		}
	}
	if other != nil {
		return Principal{}, other
	}
	return Principal{}, auth.ErrUnauthenticated
}

// bearerToken extracts the token from "Authorization: Bearer <token>".
func bearerToken(r *http.Request) string {
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
