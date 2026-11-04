package hub

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"time"

	"github.com/XertroV/vecnet/internal/audit"
	"github.com/XertroV/vecnet/internal/auth"
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
