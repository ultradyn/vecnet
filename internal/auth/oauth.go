package auth

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/store"
)

// OAuthCredential binds an authenticated caller to one OAuth access token and
// its authorization snapshot. It must only be constructed by authentication.
type OAuthCredential struct {
	TokenHash, ClientID, Resource, Scope, RequiredScope string
	ExpiresAt                                           time.Time
}

// OAuthCallerActive rechecks both token authorization and its current peer
// binding. q may be the transaction that is about to commit an operation.
func OAuthCallerActive(ctx context.Context, q store.OAuthTokenQuery, c Caller, now time.Time) bool {
	b := c.OAuth
	if c.CredentialID != "" || c.Kind != "principal" || b.TokenHash == "" ||
		b.ClientID == "" || b.Resource == "" || b.RequiredScope == "" || !now.Before(b.ExpiresAt) {
		return false
	}
	t, err := store.ReadOAuthAccessToken(ctx, q, b.TokenHash)
	if err != nil || t.ClientID != b.ClientID || t.PrincipalID != c.PrincipalID ||
		t.PeerID != c.PeerID || t.PeerKind != c.PeerKind || t.Resource != b.Resource ||
		t.Scope != b.Scope || !slices.Contains(strings.Fields(t.Scope), b.RequiredScope) {
		return false
	}
	exp, err := envelope.ParseTime(t.ExpiresAt)
	return err == nil && now.Before(exp)
}
