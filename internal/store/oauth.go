package store

import (
	"context"
	"database/sql"
)

// OAuthAccessToken is the current authorization and enrollment of an access
// token. Token hashes identify rows; plaintext bearer secrets never enter here.
type OAuthAccessToken struct {
	ClientID, PrincipalID, PeerID, PeerKind string
	Scope, Resource, ExpiresAt              string
}

// OAuthTokenQuery allows the same authorization read on a DB or inside the
// transaction that commits a sensitive operation.
type OAuthTokenQuery interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func ReadOAuthAccessToken(ctx context.Context, q OAuthTokenQuery, hash string) (OAuthAccessToken, error) {
	var token OAuthAccessToken
	err := q.QueryRowContext(ctx, `SELECT t.client_id, o.prn_id, o.peer_id, p.kind, t.scope, t.resource, t.expires_at
		FROM oauth_tokens t
		JOIN oauth_principals o ON o.prn_id = t.principal_id
		JOIN principals principal ON principal.prn_id = o.prn_id
		JOIN peers p ON p.peer_id = o.peer_id
		WHERE t.token_hash = ? AND t.kind = 'access' AND t.revoked_at IS NULL
		  AND p.disabled = 0 AND p.prn_id = o.prn_id`, hash).
		Scan(&token.ClientID, &token.PrincipalID, &token.PeerID, &token.PeerKind, &token.Scope, &token.Resource, &token.ExpiresAt)
	return token, err
}
