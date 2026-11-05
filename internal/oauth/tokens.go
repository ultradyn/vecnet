package oauth

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/XertroV/vecnet/internal/audit"
	"github.com/XertroV/vecnet/internal/auth"
	"github.com/XertroV/vecnet/internal/envelope"
)

// Token prefixes: the opaque plaintext a client holds.
const (
	accessPrefix  = "vat_"
	refreshPrefix = "vrt_"
)

// newSecret mints prefix + 32 random bytes and returns it with its hash.
func newSecret(prefix string) (plain, hash string) {
	plain = randomID(prefix)
	return plain, auth.HashSecret(plain)
}

// tokenError is an RFC 6749 §5.2 refusal.
type tokenError struct{ code, description string }

func (e *tokenError) Error() string { return e.code }

func badGrant(description string) *tokenError { return &tokenError{"invalid_grant", description} }

// tokenResponse is the success body.
type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
	RefreshToken string `json:"refresh_token"`
	Scope        string `json:"scope"`
}

// token is the token endpoint. Parameters are read from the body only, so no
// secret is accepted from a URL, and a refused request changes nothing except
// that reuse of a spent code or refresh token revokes its family.
func (s *Server) token(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		writeOAuthError(w, http.StatusBadRequest, "invalid_request", "the body could not be read")
		return
	}
	form := r.PostForm
	if form.Get("grant_type") == "refresh_token" {
		oauthKind(w, "oauth.refresh")
	}
	if repeated(form) {
		writeOAuthError(w, http.StatusBadRequest, "invalid_request", "a parameter was given twice")
		return
	}
	// resource is optional (RFC 8707 §2): the token is bound to /mcp either way.
	if res := form.Get("resource"); res != "" && res != s.resource() {
		writeOAuthError(w, http.StatusBadRequest, "invalid_target", "resource is not this hub's /mcp")
		return
	}
	var resp *tokenResponse
	var err error
	switch form.Get("grant_type") {
	case "":
		err = &tokenError{"invalid_request", "grant_type is required"}
	case "authorization_code":
		resp, err = s.exchangeCode(r.Context(), form)
	case "refresh_token":
		resp, err = s.refresh(r.Context(), form)
	default: // no machine-to-machine grant exists (PROTOCOL §4.5)
		err = &tokenError{"unsupported_grant_type", "only authorization_code and refresh_token are supported"}
	}
	var te *tokenError
	switch {
	case errors.As(err, &te):
		writeOAuthError(w, http.StatusBadRequest, te.code, te.description)
	case err != nil:
		writeOAuthError(w, http.StatusInternalServerError, "server_error", "internal")
	default:
		oauthScopes(w, strings.Fields(resp.Scope))
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Pragma", "no-cache")
		writeJSON(w, http.StatusOK, resp)
	}
}

// exchangeCode spends an authorization code. Only a replay of a spent code
// changes anything when refused: it revokes the tokens that code issued (RFC
// 6749 §4.1.2), and that revocation must commit, so the refusal travels in
// refused rather than as the transaction's error. A wrong verifier leaves the
// code unspent: whoever tries one without it should not be able to burn it.
func (s *Server) exchangeCode(ctx context.Context, form url.Values) (*tokenResponse, error) {
	code, clientID, redirectURI, verifier := form.Get("code"), form.Get("client_id"), form.Get("redirect_uri"), form.Get("code_verifier")
	if code == "" || clientID == "" || redirectURI == "" || verifier == "" {
		return nil, &tokenError{"invalid_request", "code, client_id, redirect_uri and code_verifier are required"}
	}
	hash := auth.HashSecret(code)
	// The endpoint is public: an unknown code is refused by a read, so junk
	// never queues on the store's write lock. A known one, even spent, goes on
	// into the transaction, where reuse revokes its family.
	if known, err := s.known(ctx, `SELECT EXISTS (SELECT 1 FROM oauth_codes WHERE code_hash = ?)`, hash); err != nil {
		return nil, err
	} else if !known {
		return nil, badGrant("unknown code")
	}
	var resp *tokenResponse
	var refused *tokenError
	err := s.DB.Tx(ctx, func(tx *sql.Tx) error {
		resp, refused = nil, nil
		var wantClient, wantRedirect, scope, challenge, resource, prn, expires string
		var used sql.NullString
		err := tx.QueryRowContext(ctx,
			`SELECT client_id, redirect_uri, scope, code_challenge, resource, principal_id, expires_at, used_at
			 FROM oauth_codes WHERE code_hash = ?`, hash).
			Scan(&wantClient, &wantRedirect, &scope, &challenge, &resource, &prn, &expires, &used)
		if errors.Is(err, sql.ErrNoRows) {
			refused = badGrant("unknown code")
			return nil
		}
		if err != nil {
			return fmt.Errorf("oauth: find code: %w", err)
		}
		now := s.now()
		if used.Valid {
			refused = badGrant("this code was already used")
			return s.reuseDetected(ctx, tx, hash, prn, wantClient, now)
		}
		exp, err := envelope.ParseTime(expires)
		switch {
		case err != nil:
			return fmt.Errorf("oauth: code expiry: %w", err)
		case !now.Before(exp):
			refused = badGrant("this code has expired")
			return nil
		case clientID != wantClient, redirectURI != wantRedirect:
			refused = badGrant("this code was issued to another client or redirect_uri")
			return nil
		case !verifyPKCE(verifier, challenge):
			refused = badGrant("code_verifier does not match")
			return nil
		}
		res, err := tx.ExecContext(ctx, `UPDATE oauth_codes SET used_at = ? WHERE code_hash = ? AND used_at IS NULL`,
			envelope.FormatTime(now), hash)
		if err != nil {
			return fmt.Errorf("oauth: spend code: %w", err)
		}
		if n, _ := res.RowsAffected(); n != 1 {
			refused = badGrant("this code was already used")
			return nil
		}
		resp, err = s.mintPair(ctx, tx, clientID, prn, scope, resource, hash, now)
		return err
	})
	if err != nil {
		return nil, err
	}
	if refused != nil {
		return nil, refused
	}
	return resp, nil
}

// refresh rotates a refresh token: the old one is revoked, the new pair is
// linked to it. Presenting a revoked one means two parties hold the family,
// so all of it is revoked; that must commit, as in exchangeCode.
func (s *Server) refresh(ctx context.Context, form url.Values) (*tokenResponse, error) {
	presented := form.Get("refresh_token")
	if presented == "" {
		return nil, &tokenError{"invalid_request", "refresh_token is required"}
	}
	if !scopeOK(form.Get("scope")) {
		return nil, &tokenError{"invalid_scope", "only vecnet, events:read and events:write scopes are supported"}
	}
	hash := auth.HashSecret(presented)
	if known, err := s.known(ctx, `SELECT EXISTS (SELECT 1 FROM oauth_tokens WHERE token_hash = ? AND kind = 'refresh')`, hash); err != nil {
		return nil, err
	} else if !known {
		return nil, badGrant("unknown refresh token")
	}
	clientID := form.Get("client_id")
	var resp *tokenResponse
	var refused *tokenError
	err := s.DB.Tx(ctx, func(tx *sql.Tx) error {
		resp, refused = nil, nil
		var wantClient, prn, scope, resource, expires string
		var revoked sql.NullString
		err := tx.QueryRowContext(ctx,
			`SELECT client_id, principal_id, scope, resource, expires_at, revoked_at
			 FROM oauth_tokens WHERE token_hash = ? AND kind = 'refresh'`, hash).
			Scan(&wantClient, &prn, &scope, &resource, &expires, &revoked)
		if errors.Is(err, sql.ErrNoRows) {
			refused = badGrant("unknown refresh token")
			return nil
		}
		if err != nil {
			return fmt.Errorf("oauth: find refresh token: %w", err)
		}
		if clientID != "" && clientID != wantClient {
			refused = badGrant("this token was issued to another client")
			return nil
		}
		// RFC 6749 6: refresh may narrow the original grant, never elevate it.
		if requested := strings.Fields(form.Get("scope")); len(requested) > 0 {
			granted := strings.Fields(scope)
			if !slices.Contains(requested, Scope) {
				refused = &tokenError{"invalid_scope", "scope must retain vecnet"}
				return nil
			}
			for _, name := range requested {
				if !slices.Contains(granted, name) {
					refused = &tokenError{"invalid_scope", "scope exceeds the original grant; authorize again"}
					return nil
				}
			}
			scope = strings.Join(requested, " ")
		}
		now := s.now()
		if revoked.Valid {
			refused = badGrant("this refresh token was already used")
			return s.reuseDetected(ctx, tx, hash, prn, wantClient, now)
		}
		exp, err := envelope.ParseTime(expires)
		if err != nil {
			return fmt.Errorf("oauth: token expiry: %w", err)
		}
		if !now.Before(exp) {
			refused = badGrant("this refresh token has expired")
			return nil
		}
		if _, err := tx.ExecContext(ctx, `UPDATE oauth_tokens SET revoked_at = ? WHERE token_hash = ?`,
			envelope.FormatTime(now), hash); err != nil {
			return fmt.Errorf("oauth: revoke refresh token: %w", err)
		}
		resp, err = s.mintPair(ctx, tx, wantClient, prn, scope, resource, hash, now)
		return err
	})
	if err != nil {
		return nil, err
	}
	if refused != nil {
		return nil, refused
	}
	return resp, nil
}

// known runs an EXISTS query with hash, outside any transaction.
func (s *Server) known(ctx context.Context, query, hash string) (bool, error) {
	var ok bool
	if err := s.DB.QueryRowContext(ctx, query, hash).Scan(&ok); err != nil {
		return false, fmt.Errorf("oauth: look up grant: %w", err)
	}
	return ok, nil
}

// mintPair issues an access and a refresh token, returning the plaintext once.
// parent is the code or refresh token hash they descend from.
func (s *Server) mintPair(ctx context.Context, tx *sql.Tx, clientID, prn, scope, resource, parent string, now time.Time) (*tokenResponse, error) {
	access, accessHash := newSecret(accessPrefix)
	refresh, refreshHash := newSecret(refreshPrefix)
	insert := func(hash, kind, rotatedFrom string, ttl time.Duration) error {
		_, err := tx.ExecContext(ctx,
			`INSERT INTO oauth_tokens(token_hash, kind, client_id, principal_id, scope, resource, expires_at, rotated_from, created_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			hash, kind, clientID, prn, scope, resource, envelope.FormatTime(now.Add(ttl)), rotatedFrom, envelope.FormatTime(now))
		if err != nil {
			return fmt.Errorf("oauth: insert %s token: %w", kind, err)
		}
		return nil
	}
	if err := insert(refreshHash, "refresh", parent, s.RefreshTTL); err != nil {
		return nil, err
	}
	if err := insert(accessHash, "access", refreshHash, s.AccessTTL); err != nil {
		return nil, err
	}
	return &tokenResponse{
		AccessToken: access, TokenType: "Bearer", ExpiresIn: int(s.AccessTTL.Seconds()),
		RefreshToken: refresh, Scope: scope,
	}, nil
}

// reuseDetected answers a spent code or refresh token presented again: two
// parties hold the family, so all of it goes, and the operator hears of it. The
// audit row names who, never the token. Events subscriptions the family's
// access tokens made stop at the worker's next authorisation check.
func (s *Server) reuseDetected(ctx context.Context, tx *sql.Tx, hash, prn, clientID string, now time.Time) error {
	client := auditClientID(clientID)
	slog.Warn("oauth: token reuse: revoking the family", "principal", prn, "client", client)
	if err := audit.Record(ctx, tx, "hub", "oauth_token_reuse", []string{prn, client}, nil); err != nil {
		return err
	}
	return s.revokeFamily(ctx, tx, hash, now)
}

// revokeFamily revokes every live token sharing an ancestor with hash, a token
// or code hash: up the rotated_from chain to the family's first refresh token,
// then down to every descendant, access tokens included.
func (s *Server) revokeFamily(ctx context.Context, tx *sql.Tx, hash string, now time.Time) error {
	_, err := tx.ExecContext(ctx,
		`WITH RECURSIVE
		   up(h, parent) AS (
		     SELECT token_hash, rotated_from FROM oauth_tokens WHERE token_hash = ?1
		     UNION
		     SELECT t.token_hash, t.rotated_from FROM oauth_tokens t JOIN up ON t.token_hash = up.parent),
		   family(h) AS (
		     SELECT ?1
		     UNION SELECT h FROM up
		     UNION SELECT t.token_hash FROM oauth_tokens t JOIN family ON t.rotated_from = family.h)
		 UPDATE oauth_tokens SET revoked_at = ?2
		 WHERE revoked_at IS NULL AND token_hash IN (SELECT h FROM family)`,
		hash, envelope.FormatTime(now))
	if err != nil {
		return fmt.Errorf("oauth: revoke token family: %w", err)
	}
	return nil
}

// auditClientID names a client in logs and audit rows. A CIMD client_id is an
// attacker-chosen URL whose path or query may carry a credential, so it is
// reduced to its host plus a hash of the whole URL; other ids are hub-minted.
func auditClientID(clientID string) string {
	if !isCIMD(clientID) {
		return clientID
	}
	sum := sha256.Sum256([]byte(clientID))
	host := "invalid"
	if u, err := url.Parse(clientID); err == nil && u.Hostname() != "" {
		host = u.Hostname()
	}
	return "cimd:" + host + ":" + hex.EncodeToString(sum[:8])
}
