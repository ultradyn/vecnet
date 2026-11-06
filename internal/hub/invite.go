package hub

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/XertroV/vecnet/internal/audit"
	"github.com/XertroV/vecnet/internal/auth"
	"github.com/XertroV/vecnet/internal/envelope"
)

// MinEnrolTTL is the shortest enrolment-token lifetime an operator may ask
// for; auth.MaxEnrolTTL is the longest (PROTOCOL §2.12).
const MinEnrolTTL = time.Minute

// checkEnrolTTL accepts 0 (the default) or a ttl within [MinEnrolTTL,
// auth.MaxEnrolTTL]; anything else is 422 invalid_value on enrol_ttl_seconds.
func checkEnrolTTL(ttl time.Duration) error {
	if ttl == 0 || (ttl >= MinEnrolTTL && ttl <= auth.MaxEnrolTTL) {
		return nil
	}
	return invalidValue("enrol_ttl_seconds", fmt.Sprintf("enrol_ttl_seconds must be between %d and %d",
		int(MinEnrolTTL/time.Second), int(auth.MaxEnrolTTL/time.Second)))
}

// ReissueEnrolToken replaces the enrolment token of a peer that has not
// enrolled yet: its unused tokens are spent and a new one living ttl (0 means
// the default) is returned, once, with its expiry. A peer holding a live
// credential is 409 already_enrolled (rotate it instead); a dot is 422
// invalid_value, since it never holds a vecnet secret. Audited as
// "enrol_reissue".
func (h *Hub) ReissueEnrolToken(ctx context.Context, peerID string, ttl time.Duration) (token string, expires time.Time, err error) {
	if err := checkEnrolTTL(ttl); err != nil {
		return "", time.Time{}, err
	}
	err = h.DB.Tx(ctx, func(tx *sql.Tx) error {
		_, kind, _, err := peerRow(ctx, tx, peerID)
		if err != nil {
			return err
		}
		if kind == "dot" {
			return invalidValue("peer_id", "a dot holds no vecnet secret; issue a link code instead")
		}
		var live int
		if err := tx.QueryRowContext(ctx,
			`SELECT count(*) FROM credentials WHERE kind = 'peer' AND subject_id = ? AND revoked_at IS NULL`, peerID).Scan(&live); err != nil {
			return fmt.Errorf("hub: find peer credential: %w", err)
		}
		if live > 0 {
			return E(http.StatusConflict, "already_enrolled", "the peer has already enrolled; rotate its credential instead")
		}
		if err := h.Auth.SpendEnrolTokensTx(ctx, tx, peerID); err != nil {
			return err
		}
		if token, expires, err = h.Auth.IssueEnrolToken(ctx, tx, peerID, ttl); err != nil {
			return err
		}
		return audit.Record(ctx, tx, "operator", "enrol_reissue", []string{peerID}, nil)
	})
	if err != nil {
		return "", time.Time{}, err
	}
	return token, expires, nil
}

// InviteStatus is what the operator may read about a peer's invite: its
// state, never its token (PROTOCOL §2.12). There is no state column: the
// state is derived from the enrolment rows, so a peer has at most one live
// invite (a re-issue spends the previous token).
//
//   - pending: an unspent token that has not expired; ExpiresAt is its expiry
//   - expired: an unspent token past its expiry; ExpiresAt is when
//   - revoked: every token spent and the peer never held a credential, so
//     none was redeemed; RevokedAt is when the last one was spent
//   - claimed: the peer holds (or held) a peer credential: it enrolled
//   - linked: an OAuth login is bound to the peer (a dot or a Grok Bot)
//   - none: no enrolment token was ever issued (an unlinked dot)
type InviteStatus struct {
	PeerID    string `json:"peer_id"`
	State     string `json:"state"`
	ExpiresAt string `json:"expires_at"`
	RevokedAt string `json:"revoked_at"`
}

// rowQuerier is a *sql.DB or a *sql.Tx.
type rowQuerier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// inviteStatus derives peerID's InviteStatus, which must exist, at now.
func inviteStatus(ctx context.Context, q rowQuerier, peerID string, now time.Time) (InviteStatus, error) {
	st := InviteStatus{PeerID: peerID}
	var live, ever, linked int
	if err := q.QueryRowContext(ctx, `SELECT
		(SELECT count(*) FROM credentials WHERE kind = 'peer' AND subject_id = ? AND revoked_at IS NULL),
		(SELECT count(*) FROM credentials WHERE kind = 'peer' AND subject_id = ?),
		(SELECT count(*) FROM oauth_principals WHERE peer_id = ?)`, peerID, peerID, peerID).Scan(&live, &ever, &linked); err != nil {
		return st, fmt.Errorf("hub: invite status: %w", err)
	}
	var unspent, spent sql.NullString
	if err := q.QueryRowContext(ctx, `SELECT
		(SELECT max(expires_at) FROM enrol_tokens WHERE peer_id = ? AND used_at IS NULL),
		(SELECT max(used_at) FROM enrol_tokens WHERE peer_id = ? AND used_at IS NOT NULL)`, peerID, peerID).Scan(&unspent, &spent); err != nil {
		return st, fmt.Errorf("hub: invite status: %w", err)
	}
	switch {
	case live > 0:
		st.State = "claimed"
	case linked > 0:
		st.State = "linked"
	case unspent.Valid:
		st.State, st.ExpiresAt = "pending", unspent.String
		if unspent.String <= envelope.FormatTime(now) { // fixed-width UTC: text order is time order
			st.State = "expired"
		}
	case ever > 0:
		st.State = "claimed"
	case spent.Valid:
		st.State, st.RevokedAt = "revoked", spent.String
	default:
		st.State = "none"
	}
	return st, nil
}

// InviteStatus reads peerID's invite state; 404 not_found for an unknown
// peer. It never returns a token.
func (h *Hub) InviteStatus(ctx context.Context, peerID string) (InviteStatus, error) {
	// A read alone: the peer page asks every 30 s, so it never queues on
	// the store's write lock.
	var one int
	err := h.DB.QueryRowContext(ctx, `SELECT 1 FROM peers WHERE peer_id = ?`, peerID).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return InviteStatus{}, unknownPeer(peerID)
	}
	if err != nil {
		return InviteStatus{}, fmt.Errorf("hub: invite status: %w", err)
	}
	return inviteStatus(ctx, h.DB, peerID, h.Now())
}

// RevokeEnrolToken withdraws peerID's live invite: its unspent, unexpired
// enrolment token is spent, so it can no longer enrol. revoked says whether
// there was one; a peer with no live invite (expired, revoked, claimed, a
// dot) is a no-op that changes and audits nothing. Credentials are never
// touched: stopping an enrolled peer is disable or rotate. Audited as
// "enrol_revoke", with no detail and never the token. 404 not_found for an
// unknown peer.
func (h *Hub) RevokeEnrolToken(ctx context.Context, peerID string) (st InviteStatus, revoked bool, err error) {
	err = h.DB.Tx(ctx, func(tx *sql.Tx) error {
		if _, _, _, err := peerRow(ctx, tx, peerID); err != nil {
			return err
		}
		now := envelope.FormatTime(h.Now())
		res, err := tx.ExecContext(ctx,
			`UPDATE enrol_tokens SET used_at = ? WHERE peer_id = ? AND used_at IS NULL AND expires_at > ?`, now, peerID, now)
		if err != nil {
			return fmt.Errorf("hub: revoke enrol token: %w", err)
		}
		if n, _ := res.RowsAffected(); n > 0 {
			revoked = true
			if err := audit.Record(ctx, tx, "operator", "enrol_revoke", []string{peerID}, nil); err != nil {
				return err
			}
		}
		st, err = inviteStatus(ctx, tx, peerID, h.Now())
		return err
	})
	if err != nil {
		return InviteStatus{}, false, err
	}
	return st, revoked, nil
}

// InviteInfo is what an invite says about its peer besides the token.
type InviteInfo struct {
	Name, PrincipalID string
	NameOpen          bool   // the agent picks its name at enrol
	Owner             string // the operator principal's name, for "<owner>'s hub"
}

// InviteInfo reads peerID's name, principal and whether its name is still
// open, and the operator's name; 404 not_found for an unknown peer.
func (h *Hub) InviteInfo(ctx context.Context, peerID string) (InviteInfo, error) {
	var in InviteInfo
	var open int
	err := h.DB.QueryRowContext(ctx, `SELECT name, prn_id, name_open FROM peers WHERE peer_id = ?`, peerID).Scan(&in.Name, &in.PrincipalID, &open)
	if errors.Is(err, sql.ErrNoRows) {
		return in, unknownPeer(peerID)
	}
	if err != nil {
		return in, fmt.Errorf("hub: invite info: %w", err)
	}
	in.NameOpen = open != 0
	err = h.DB.QueryRowContext(ctx, `SELECT p.name FROM principals p JOIN credentials c ON c.subject_id = p.prn_id
		WHERE c.kind = 'operator' AND c.revoked_at IS NULL ORDER BY c.created_at LIMIT 1`).Scan(&in.Owner)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return in, fmt.Errorf("hub: invite owner: %w", err)
	}
	return in, nil
}
