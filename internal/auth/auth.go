// Package auth owns credentials (PROTOCOL §12): bearer secrets, one-time
// enrolment tokens, rotation, and per-peer send scopes. Plaintext secrets are
// returned exactly once and never stored or logged; the database holds only
// their SHA-256 hex.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/XertroV/vecnet/internal/audit"
	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/ids"
	"github.com/XertroV/vecnet/internal/store"
)

// Caller is the authenticated identity behind a request.
//
// Kind "peer": PeerID is the credential's peer, PrincipalID its principal.
// Kind "principal": PrincipalID; the request names a peer with `as`. An OAuth
// principal (PROTOCOL §4.5) also has PeerID, its bound dot peer, used when
// `as` is absent.
// Kind "operator": PrincipalID is the operator principal and PeerID its
// operator peer (peers.kind = 'operator'), so the operator is addressable
// like any peer; PeerID is "" when no operator peer exists yet.
type Caller struct {
	Kind        string // "peer" | "principal" | "operator"
	PrincipalID string
	PeerID      string
	PeerKind    string // peers.kind of PeerID, "" when PeerID is ""
}

// ErrUnauthenticated reports an unknown, revoked, expired or used credential,
// or one whose peer is disabled.
var ErrUnauthenticated = errors.New("unauthenticated")

// ErrPeerDisabled is the ErrUnauthenticated that Resolve returns for a valid
// peer credential whose peer is disabled; errors.Is(err, ErrUnauthenticated)
// holds for it. GET /v1/events answers it with 204 (PROTOCOL §2.3).
var ErrPeerDisabled = fmt.Errorf("%w: peer disabled", ErrUnauthenticated)

// ErrDotBearer reports an attempt to enrol a dot: its token buys no secret.
var ErrDotBearer = errors.New("auth: a dot holds no vecnet secret")

// ErrUnknownSubject reports a Rotate of a peer or principal that does not exist.
var ErrUnknownSubject = errors.New("auth: unknown subject")

// ErrEnrolTokenExpired is the ErrUnauthenticated that Enrol returns for an
// unused token past its expires_at; errors.Is(err, ErrUnauthenticated) holds
// for it. POST /v1/enrol answers it with 410 enrol_token_expired.
var ErrEnrolTokenExpired = fmt.Errorf("%w: enrolment token expired", ErrUnauthenticated)

// DefaultEnrolTTL is the lifetime of an enrolment token when the caller passes
// a ttl <= 0; MaxEnrolTTL is the longest an operator may ask for.
const (
	DefaultEnrolTTL = 24 * time.Hour
	MaxEnrolTTL     = 7 * 24 * time.Hour
)

// credKinds are the credentials.kind values.
var credKinds = map[string]bool{"peer": true, "principal": true, "operator": true}

// NewSecret mints "vn_<kind>_" + base64url (unpadded) of 32 random bytes and
// returns it with its SHA-256 lower hex. Store only the hash.
func NewSecret(kind string) (plain, hash string) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("auth: crypto/rand failed: " + err.Error())
	}
	plain = "vn_" + kind + "_" + base64.RawURLEncoding.EncodeToString(b[:])
	return plain, HashSecret(plain)
}

// HashSecret returns the SHA-256 lower hex of plain.
func HashSecret(plain string) string {
	sum := sha256.Sum256([]byte(plain))
	return hex.EncodeToString(sum[:])
}

// Resolver turns bearers into Callers and manages credentials.
type Resolver struct {
	DB *store.DB
	// Now is the clock for enrolment-token expiry and credential
	// timestamps; nil means time.Now. The hub wires its own clock, so a
	// test's fake clock drives token expiry.
	Now func() time.Time
}

func (r *Resolver) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

// querier is the subset of *sql.DB / *sql.Tx the helpers need.
type querier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// Resolve maps a bearer secret to its Caller. An empty, unknown or revoked
// secret, a disabled peer, or a missing principal is ErrUnauthenticated.
func (r *Resolver) Resolve(ctx context.Context, bearer string) (Caller, error) {
	if bearer == "" {
		return Caller{}, ErrUnauthenticated
	}
	var kind, subject string
	err := r.DB.QueryRowContext(ctx,
		`SELECT kind, subject_id FROM credentials WHERE secret_hash = ? AND revoked_at IS NULL`,
		HashSecret(bearer)).Scan(&kind, &subject)
	if errors.Is(err, sql.ErrNoRows) {
		return Caller{}, ErrUnauthenticated
	}
	if err != nil {
		return Caller{}, fmt.Errorf("auth: resolve: %w", err)
	}

	switch kind {
	case "peer":
		var prn, peerKind string
		var disabled int
		err := r.DB.QueryRowContext(ctx,
			`SELECT prn_id, kind, disabled FROM peers WHERE peer_id = ?`, subject).Scan(&prn, &peerKind, &disabled)
		if errors.Is(err, sql.ErrNoRows) {
			return Caller{}, ErrUnauthenticated
		}
		if err != nil {
			return Caller{}, fmt.Errorf("auth: resolve peer: %w", err)
		}
		if disabled != 0 {
			return Caller{}, ErrPeerDisabled
		}
		return Caller{Kind: "peer", PrincipalID: prn, PeerID: subject, PeerKind: peerKind}, nil

	case "principal", "operator":
		var one int
		err := r.DB.QueryRowContext(ctx, `SELECT 1 FROM principals WHERE prn_id = ?`, subject).Scan(&one)
		if errors.Is(err, sql.ErrNoRows) {
			return Caller{}, ErrUnauthenticated
		}
		if err != nil {
			return Caller{}, fmt.Errorf("auth: resolve principal: %w", err)
		}
		c := Caller{Kind: kind, PrincipalID: subject}
		if kind == "operator" {
			var peer string
			err := r.DB.QueryRowContext(ctx,
				`SELECT peer_id FROM peers WHERE prn_id = ? AND kind = 'operator' AND disabled = 0 ORDER BY peer_id LIMIT 1`,
				subject).Scan(&peer)
			switch {
			case err == nil:
				c.PeerID, c.PeerKind = peer, "operator"
			case !errors.Is(err, sql.ErrNoRows):
				return Caller{}, fmt.Errorf("auth: resolve operator peer: %w", err)
			}
		}
		return c, nil
	}
	return Caller{}, ErrUnauthenticated
}

// IssueEnrolToken stores a one-time enrolment token for peerID inside tx and
// returns its plaintext ("vn_enrol_…"), once, with its expiry. ttl <= 0
// means DefaultEnrolTTL; the caller bounds it by MaxEnrolTTL.
func (r *Resolver) IssueEnrolToken(ctx context.Context, tx *sql.Tx, peerID string, ttl time.Duration) (string, time.Time, error) {
	if ttl <= 0 {
		ttl = DefaultEnrolTTL
	}
	plain, hash := NewSecret("enrol")
	exp := r.now().Add(ttl)
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO enrol_tokens(token_hash, peer_id, expires_at) VALUES (?, ?, ?)`,
		hash, peerID, envelope.FormatTime(exp)); err != nil {
		return "", time.Time{}, fmt.Errorf("auth: issue enrol token: %w", err)
	}
	return plain, exp, nil
}

// SpendEnrolTokensTx marks every unspent enrolment token of peerID used,
// inside tx, so none of them can enrol any more.
func (r *Resolver) SpendEnrolTokensTx(ctx context.Context, tx *sql.Tx, peerID string) error {
	if _, err := tx.ExecContext(ctx,
		`UPDATE enrol_tokens SET used_at = ? WHERE peer_id = ? AND used_at IS NULL`, envelope.FormatTime(r.now()), peerID); err != nil {
		return fmt.Errorf("auth: spend enrol tokens: %w", err)
	}
	return nil
}

// Enrol exchanges a one-time token for the peer's long-lived secret
// ("vn_peer_…"), returned once. The token is marked used in the same
// transaction; an unknown or used token, or a missing or disabled peer, is
// ErrUnauthenticated, and an unused token past its expiry is
// ErrEnrolTokenExpired. A dot's token is ErrDotBearer: dots never hold
// a vecnet secret (PROTOCOL §12). The exchange is audited as "enrol".
func (r *Resolver) Enrol(ctx context.Context, token string) (peerID, secret string, err error) {
	if token == "" {
		return "", "", ErrUnauthenticated
	}
	// The route is public: a token that is unknown or spent is refused by a
	// read, so a stream of guesses never queues on the store's write lock.
	var spendable bool
	err = r.DB.QueryRowContext(ctx, `SELECT used_at IS NULL FROM enrol_tokens WHERE token_hash = ?`, HashSecret(token)).Scan(&spendable)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && !spendable) {
		return "", "", ErrUnauthenticated
	}
	if err != nil {
		return "", "", fmt.Errorf("auth: enrol: %w", err)
	}
	err = r.DB.Tx(ctx, func(tx *sql.Tx) error {
		var expires string
		var used sql.NullString
		hash := HashSecret(token)
		err := tx.QueryRowContext(ctx,
			`SELECT peer_id, expires_at, used_at FROM enrol_tokens WHERE token_hash = ?`, hash).
			Scan(&peerID, &expires, &used)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrUnauthenticated
		}
		if err != nil {
			return fmt.Errorf("auth: enrol: %w", err)
		}
		if used.Valid {
			return ErrUnauthenticated
		}
		exp, err := envelope.ParseTime(expires)
		if err != nil {
			return ErrUnauthenticated
		}
		if !r.now().Before(exp) {
			return ErrEnrolTokenExpired
		}
		var disabled int
		var kind string
		err = tx.QueryRowContext(ctx, `SELECT disabled, kind FROM peers WHERE peer_id = ?`, peerID).Scan(&disabled, &kind)
		if errors.Is(err, sql.ErrNoRows) || (err == nil && disabled != 0) {
			return ErrUnauthenticated
		}
		if err != nil {
			return fmt.Errorf("auth: enrol peer: %w", err)
		}
		if kind == "dot" {
			return ErrDotBearer
		}
		now := envelope.FormatTime(r.now())
		res, err := tx.ExecContext(ctx,
			`UPDATE enrol_tokens SET used_at = ? WHERE token_hash = ? AND used_at IS NULL`, now, hash)
		if err != nil {
			return fmt.Errorf("auth: enrol mark used: %w", err)
		}
		if n, _ := res.RowsAffected(); n != 1 {
			return ErrUnauthenticated
		}
		secret, err = insertCredential(ctx, tx, "peer", peerID, now)
		if err != nil {
			return err
		}
		return audit.Record(ctx, tx, peerID, "enrol", []string{peerID}, nil)
	})
	if err != nil {
		return "", "", err
	}
	return peerID, secret, nil
}

// Rotate revokes every live credential of kind for subjectID (a peer id for
// "peer", a prn id for "principal" and "operator") and returns a new secret,
// once. Rotating a peer also spends its unused enrolment tokens, since a
// rotation answers a leak. The rotation is audited as "rotate" with actor
// "operator": only the operator rotates credentials (PROTOCOL §2.12).
func (r *Resolver) Rotate(ctx context.Context, kind, subjectID string) (secret string, err error) {
	err = r.DB.Tx(ctx, func(tx *sql.Tx) error {
		var err error
		secret, err = r.RotateTx(ctx, tx, kind, subjectID)
		return err
	})
	if err != nil {
		return "", err
	}
	return secret, nil
}

// RotateTx is Rotate inside tx, so what follows a rotation commits with it.
func (r *Resolver) RotateTx(ctx context.Context, tx *sql.Tx, kind, subjectID string) (string, error) {
	if !credKinds[kind] {
		return "", fmt.Errorf("auth: rotate: unknown credential kind %q", kind)
	}
	q := `SELECT 1 FROM principals WHERE prn_id = ?`
	if kind == "peer" {
		q = `SELECT 1 FROM peers WHERE peer_id = ?`
	}
	var one int
	err := tx.QueryRowContext(ctx, q, subjectID).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("%w: %s %s", ErrUnknownSubject, kind, subjectID)
	}
	if err != nil {
		return "", fmt.Errorf("auth: rotate: %w", err)
	}
	now := envelope.FormatTime(r.now())
	if _, err := tx.ExecContext(ctx,
		`UPDATE credentials SET revoked_at = ? WHERE kind = ? AND subject_id = ? AND revoked_at IS NULL`,
		now, kind, subjectID); err != nil {
		return "", fmt.Errorf("auth: rotate revoke: %w", err)
	}
	if kind == "peer" {
		if err := r.SpendEnrolTokensTx(ctx, tx, subjectID); err != nil {
			return "", err
		}
	}
	secret, err := insertCredential(ctx, tx, kind, subjectID, now)
	if err != nil {
		return "", err
	}
	return secret, audit.Record(ctx, tx, "operator", "rotate", []string{subjectID}, map[string]any{"kind": kind})
}

// RevokePeerCredentialsTx ends every bearer and unspent enrolment token of
// peerID, inside tx. A dot holds neither (PROTOCOL §12); this clears any left
// from before that was enforced.
func (r *Resolver) RevokePeerCredentialsTx(ctx context.Context, tx *sql.Tx, peerID string) error {
	now := envelope.FormatTime(r.now())
	if _, err := tx.ExecContext(ctx,
		`UPDATE credentials SET revoked_at = ? WHERE kind = 'peer' AND subject_id = ? AND revoked_at IS NULL`, now, peerID); err != nil {
		return fmt.Errorf("auth: revoke peer credentials: %w", err)
	}
	return r.SpendEnrolTokensTx(ctx, tx, peerID)
}

// insertCredential mints a secret of kind for subject, stores its hash and
// returns the plaintext.
func insertCredential(ctx context.Context, tx *sql.Tx, kind, subject, now string) (string, error) {
	plain, hash := NewSecret(kind)
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO credentials(cred_id, kind, subject_id, secret_hash, created_at) VALUES (?, ?, ?, ?, ?)`,
		ids.New(ids.Prefix("cred")), kind, subject, hash, now); err != nil {
		return "", fmt.Errorf("auth: insert credential: %w", err)
	}
	return plain, nil
}

// Scopes is one peer's send scopes: explicit deny and allow targets (peer,
// team or group ids).
type Scopes struct {
	deny, allow map[string]bool
}

// Denied reports an explicit deny row for id.
func (s Scopes) Denied(id string) bool { return s.deny[id] }

// AllowListed reports whether any allow row exists (allow-list mode).
func (s Scopes) AllowListed() bool { return len(s.allow) > 0 }

// Allows applies the scopes to id: a deny row for id → false; otherwise, if
// any allow rows exist, true only when one names id; no rows → true.
func (s Scopes) Allows(id string) bool {
	if s.deny[id] {
		return false
	}
	return len(s.allow) == 0 || s.allow[id]
}

// LoadScopes reads fromPeer's send scopes. A nil tx reads from r.DB.
func (r *Resolver) LoadScopes(ctx context.Context, tx *sql.Tx, fromPeer string) (Scopes, error) {
	var q querier = r.DB
	if tx != nil {
		q = tx
	}
	rows, err := q.QueryContext(ctx, `SELECT mode, target_id FROM scopes WHERE peer_id = ?`, fromPeer)
	if err != nil {
		return Scopes{}, fmt.Errorf("auth: scopes: %w", err)
	}
	defer rows.Close()
	s := Scopes{deny: map[string]bool{}, allow: map[string]bool{}}
	for rows.Next() {
		var mode, target string
		if err := rows.Scan(&mode, &target); err != nil {
			return Scopes{}, fmt.Errorf("auth: scopes: %w", err)
		}
		switch mode {
		case "deny":
			s.deny[target] = true
		case "allow":
			s.allow[target] = true
		}
	}
	if err := rows.Err(); err != nil {
		return Scopes{}, fmt.Errorf("auth: scopes: %w", err)
	}
	return s, nil
}

// MaySend applies fromPeer's send scopes to toID (a peer, team or group id):
// no scope rows → true; a deny row for toID → false; otherwise, if any allow
// rows exist, true only when one names toID. A nil tx reads from r.DB.
func (r *Resolver) MaySend(ctx context.Context, tx *sql.Tx, fromPeer, toID string) (bool, error) {
	s, err := r.LoadScopes(ctx, tx, fromPeer)
	if err != nil {
		return false, err
	}
	return s.Allows(toID), nil
}
