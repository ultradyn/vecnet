package hub

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/XertroV/vecnet/internal/audit"
	"github.com/XertroV/vecnet/internal/auth"
	"github.com/XertroV/vecnet/internal/blob"
	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/ids"
)

// Operator powers behind /v1/admin/... (PROTOCOL §2.12, §5.5, §12). The HTTP
// layer admits only the operator credential to these paths; the methods
// themselves trust their caller and audit with actor "operator".

func unknownPeer(peerID string) *APIError {
	e := E(http.StatusNotFound, "not_found", "no such peer")
	e.Details["peer_id"] = peerID
	return e
}

// peerRow reads peerID's principal and kind inside tx; a missing peer is
// 404 not_found.
func peerRow(ctx context.Context, tx *sql.Tx, peerID string) (prn, kind string, disabled bool, err error) {
	var d int
	err = tx.QueryRowContext(ctx, `SELECT prn_id, kind, disabled FROM peers WHERE peer_id = ?`, peerID).Scan(&prn, &kind, &d)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", false, unknownPeer(peerID)
	}
	if err != nil {
		return "", "", false, fmt.Errorf("hub: find peer: %w", err)
	}
	return prn, kind, d != 0, nil
}

// PeerPrincipal returns the principal peerID belongs to; 404 not_found
// when there is no such peer.
func (h *Hub) PeerPrincipal(ctx context.Context, peerID string) (string, error) {
	var prn string
	err := h.DB.Tx(ctx, func(tx *sql.Tx) error {
		var err error
		prn, _, _, err = peerRow(ctx, tx, peerID)
		return err
	})
	return prn, err
}

// MovePeer reassigns peerID to principal prnID (PROTOCOL §2.12 "peer
// move", R10): from now on the new principal's credential acts for it and
// its stream carries the peer's events. A peer served by its old
// principal's route switches to the new principal's route, or to none.
// The operator peer cannot move; an unknown principal is 422
// invalid_value; an unknown peer 404 not_found.
func (h *Hub) MovePeer(ctx context.Context, peerID, prnID string) error {
	err := h.DB.Tx(ctx, func(tx *sql.Tx) error {
		return h.movePeer(ctx, tx, peerID, prnID, "operator")
	})
	if err != nil {
		return err
	}
	// Wake whoever now reads this peer's log, and whoever follows the new
	// principal's peer set: nothing else tells them it has grown.
	h.Log.Notify(peerID, prnID)
	return nil
}

// movePeer is MovePeer's work inside tx, audited with actor.
func (h *Hub) movePeer(ctx context.Context, tx *sql.Tx, peerID, prnID, actor string) error {
	oldPrn, kind, _, err := peerRow(ctx, tx, peerID)
	if err != nil {
		return err
	}
	if kind == "operator" {
		return invalidValue("peer_id", "the operator peer cannot move")
	}
	var one int
	err = tx.QueryRowContext(ctx, `SELECT 1 FROM principals WHERE prn_id = ?`, prnID).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return invalidValue("principal_id", "unknown principal")
	}
	if err != nil {
		return fmt.Errorf("hub: find principal: %w", err)
	}
	if oldPrn == prnID {
		return nil
	}
	now := h.nowStr()
	if _, err := tx.ExecContext(ctx, `UPDATE peers SET prn_id = ? WHERE peer_id = ?`, prnID, peerID); err != nil {
		return fmt.Errorf("hub: move peer: %w", err)
	}
	// A route inherited from the old principal follows the principal.
	var viaOld bool
	if err := tx.QueryRowContext(ctx,
		`SELECT EXISTS (SELECT 1 FROM peers p JOIN routes r ON r.route_id = p.route_id
		                WHERE p.peer_id = ? AND r.owner_kind = 'principal' AND r.owner_id = ?)`,
		peerID, oldPrn).Scan(&viaOld); err != nil {
		return fmt.Errorf("hub: move peer route: %w", err)
	}
	if viaOld {
		var newRoute any
		var r string
		err := tx.QueryRowContext(ctx,
			`SELECT route_id FROM routes WHERE owner_kind = 'principal' AND owner_id = ? ORDER BY created_at DESC, route_id DESC LIMIT 1`,
			prnID).Scan(&r)
		switch {
		case err == nil:
			newRoute = r
		case !errors.Is(err, sql.ErrNoRows):
			return fmt.Errorf("hub: move peer route: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE peers SET route_id = ? WHERE peer_id = ?`, newRoute, peerID); err != nil {
			return fmt.Errorf("hub: move peer route: %w", err)
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE deliveries SET route_id = ?, updated_at = ? WHERE recipient = ? AND state = 'queued'`,
			newRoute, now, peerID); err != nil {
			return fmt.Errorf("hub: move peer deliveries: %w", err)
		}
	}
	return audit.Record(ctx, tx, actor, "peer_move", []string{peerID, oldPrn, prnID}, nil)
}

// DotLinkTTL is how long a link code stays valid (PROTOCOL §4.5, R15).
const DotLinkTTL = 10 * time.Minute

const (
	dotLinkAlphabet = "ABCDEFGHJKMNPQRSTUVWXYZ23456789" // no I, L, O, 0 or 1
	dotLinkLen      = 8
)

// ErrDotLink is every refused link code: unknown, used, expired, or naming a
// peer that is no longer an enabled dot. The cases are not told apart.
var ErrDotLink = errors.New("hub: invalid link code")

// newDotLinkCode draws dotLinkLen uniform characters from the alphabet.
func newDotLinkCode() (string, error) {
	limit := big.NewInt(int64(len(dotLinkAlphabet)))
	b := make([]byte, dotLinkLen)
	for i := range b {
		n, err := rand.Int(rand.Reader, limit)
		if err != nil {
			return "", fmt.Errorf("hub: link code: %w", err)
		}
		b[i] = dotLinkAlphabet[n.Int64()]
	}
	return string(b), nil
}

// DotLink mints the one-time code that binds a ChatGPT principal to dot peer
// peerID on first login (PROTOCOL §4.5, R15). The code is returned once and
// only its hash is stored; it lasts DotLinkTTL. A peer of another kind, or a
// disabled one, is 422 invalid_value; an unknown peer 404 not_found. Audited
// as "dot_link".
func (h *Hub) DotLink(ctx context.Context, peerID string) (string, error) {
	code, err := newDotLinkCode()
	if err != nil {
		return "", err
	}
	err = h.DB.Tx(ctx, func(tx *sql.Tx) error {
		_, kind, disabled, err := peerRow(ctx, tx, peerID)
		if err != nil {
			return err
		}
		if kind != "dot" {
			return invalidValue("peer_id", "only a dot peer can be linked")
		}
		if disabled {
			return invalidValue("peer_id", "the peer is disabled")
		}
		now := h.Now()
		if _, err := tx.ExecContext(ctx, `DELETE FROM dot_links WHERE expires_at <= ?`, envelope.FormatTime(now)); err != nil {
			return fmt.Errorf("hub: prune link codes: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO dot_links(code_hash, peer_id, expires_at) VALUES (?, ?, ?)`,
			auth.HashSecret(code), peerID, envelope.FormatTime(now.Add(DotLinkTTL))); err != nil {
			return fmt.Errorf("hub: insert link code: %w", err)
		}
		return audit.Record(ctx, tx, "operator", "dot_link", []string{peerID}, nil)
	})
	if err != nil {
		return "", err
	}
	return code, nil
}

// ConsumeDotLink spends code and returns the peer it names. Any code that
// cannot be spent is ErrDotLink.
func (h *Hub) ConsumeDotLink(ctx context.Context, code string) (string, error) {
	var peerID string
	err := h.DB.Tx(ctx, func(tx *sql.Tx) error {
		var err error
		peerID, err = h.ConsumeDotLinkTx(ctx, tx, code)
		return err
	})
	if err != nil {
		return "", err
	}
	return peerID, nil
}

// ConsumeDotLinkTx is ConsumeDotLink inside tx, so a failed bind leaves the
// code unspent. People type these codes, so case and surrounding space are
// forgiven. Audited as "dot_link_consume" by "hub".
func (h *Hub) ConsumeDotLinkTx(ctx context.Context, tx *sql.Tx, code string) (string, error) {
	code = strings.ToUpper(strings.TrimSpace(code))
	if len(code) != dotLinkLen {
		return "", ErrDotLink
	}
	hash := auth.HashSecret(code)
	var peerID, expires string
	var used sql.NullString
	err := tx.QueryRowContext(ctx, `SELECT peer_id, expires_at, used_at FROM dot_links WHERE code_hash = ?`, hash).
		Scan(&peerID, &expires, &used)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrDotLink
	}
	if err != nil {
		return "", fmt.Errorf("hub: find link code: %w", err)
	}
	exp, err := envelope.ParseTime(expires)
	if err != nil || used.Valid || !h.Now().Before(exp) {
		return "", ErrDotLink
	}
	_, kind, disabled, err := peerRow(ctx, tx, peerID)
	if err != nil {
		return "", err
	}
	if kind != "dot" || disabled {
		return "", ErrDotLink
	}
	res, err := tx.ExecContext(ctx, `UPDATE dot_links SET used_at = ? WHERE code_hash = ? AND used_at IS NULL`, h.nowStr(), hash)
	if err != nil {
		return "", fmt.Errorf("hub: spend link code: %w", err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return "", ErrDotLink
	}
	if err := audit.Record(ctx, tx, "hub", "dot_link_consume", []string{peerID}, nil); err != nil {
		return "", err
	}
	return peerID, nil
}

// BindDotPrincipalTx moves dot peer peerID under a new principal named "dot:"
// + the peer's name, inside tx, on the authority of the operator's link code
// (PROTOCOL §2.12, §4.5). It returns the principal. After commit, wake readers
// with Log.Notify(peerID, prnID).
func (h *Hub) BindDotPrincipalTx(ctx context.Context, tx *sql.Tx, peerID string) (string, error) {
	var name string
	err := tx.QueryRowContext(ctx, `SELECT name FROM peers WHERE peer_id = ?`, peerID).Scan(&name)
	if errors.Is(err, sql.ErrNoRows) {
		return "", unknownPeer(peerID)
	}
	if err != nil {
		return "", fmt.Errorf("hub: find peer: %w", err)
	}
	prnID := ids.New(ids.Prn)
	if err := insertPrincipal(ctx, tx, prnID, "dot:"+name, h.nowStr()); err != nil {
		return "", err
	}
	if err := h.movePeer(ctx, tx, peerID, prnID, "hub"); err != nil {
		return "", err
	}
	return prnID, nil
}

// RotatePeer revokes peerID's peer credentials and returns a new secret,
// once (PROTOCOL §2.12, §12). 404 not_found for an unknown peer.
func (h *Hub) RotatePeer(ctx context.Context, peerID string) (string, error) {
	s, err := h.Auth.Rotate(ctx, "peer", peerID)
	if errors.Is(err, auth.ErrUnknownSubject) {
		return "", unknownPeer(peerID)
	}
	return s, err
}

// RotatePrincipal revokes principal prnID's credential and returns a new
// one, once. The operator principal's credential stays an operator
// credential. 404 not_found for an unknown principal.
func (h *Hub) RotatePrincipal(ctx context.Context, prnID string) (string, error) {
	kind := "principal"
	var isOp bool
	if err := h.DB.QueryRowContext(ctx,
		`SELECT EXISTS (SELECT 1 FROM credentials WHERE subject_id = ? AND kind = 'operator')`, prnID).Scan(&isOp); err != nil {
		return "", fmt.Errorf("hub: rotate principal: %w", err)
	}
	if isOp {
		kind = "operator"
	}
	s, err := h.Auth.Rotate(ctx, kind, prnID)
	if errors.Is(err, auth.ErrUnknownSubject) {
		e := E(http.StatusNotFound, "not_found", "no such principal")
		e.Details["principal_id"] = prnID
		return "", e
	}
	return s, err
}

// SetScopes replaces peerID's send scopes with allow and deny lists of
// peer, team or group ids (PROTOCOL §12): empty lists mean allow all. An
// unknown target, or one in both lists, is 422 invalid_value. Audited as
// "scope_change".
func (h *Hub) SetScopes(ctx context.Context, peerID string, allow, deny []string) error {
	return h.DB.Tx(ctx, func(tx *sql.Tx) error {
		if _, _, _, err := peerRow(ctx, tx, peerID); err != nil {
			return err
		}
		inAllow := map[string]bool{}
		for _, id := range allow {
			inAllow[id] = true
		}
		for _, list := range []struct {
			field string
			ids   []string
		}{{"allow", allow}, {"deny", deny}} {
			for _, id := range list.ids {
				if list.field == "deny" && inAllow[id] {
					return invalidValue("deny", "an id is in both allow and deny")
				}
				var ok bool
				if err := tx.QueryRowContext(ctx,
					`SELECT EXISTS (SELECT 1 FROM peers WHERE peer_id = ?1) OR EXISTS (SELECT 1 FROM teams WHERE team_id = ?1)
					     OR EXISTS (SELECT 1 FROM groups WHERE grp_id = ?1)`, id).Scan(&ok); err != nil {
					return fmt.Errorf("hub: scope target: %w", err)
				}
				if !ok {
					e := invalidValue(list.field, "unknown peer, team or group")
					e.Details["id"] = id
					return e
				}
			}
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM scopes WHERE peer_id = ?`, peerID); err != nil {
			return fmt.Errorf("hub: clear scopes: %w", err)
		}
		for mode, list := range map[string][]string{"allow": allow, "deny": deny} {
			for _, id := range list {
				if _, err := tx.ExecContext(ctx,
					`INSERT OR IGNORE INTO scopes(peer_id, mode, target_id) VALUES (?, ?, ?)`, peerID, mode, id); err != nil {
					return fmt.Errorf("hub: insert scope: %w", err)
				}
			}
		}
		return audit.Record(ctx, tx, "operator", "scope_change", []string{peerID},
			map[string]any{"allow": len(allow), "deny": len(deny)})
	})
}

// SetPeerLimits sets peerID's per-peer rate limits (PROTOCOL §16.2); each
// must be at least 1 (422 invalid_value). Audited as "limits_change".
func (h *Hub) SetPeerLimits(ctx context.Context, peerID string, sendsPerMin, threadsPerHour int) error {
	if sendsPerMin < 1 {
		return invalidValue("sends_per_min", "sends_per_min is at least 1")
	}
	if threadsPerHour < 1 {
		return invalidValue("threads_per_hour", "threads_per_hour is at least 1")
	}
	return h.DB.Tx(ctx, func(tx *sql.Tx) error {
		if _, _, _, err := peerRow(ctx, tx, peerID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE peers SET sends_per_min = ?, threads_per_hour = ? WHERE peer_id = ?`,
			sendsPerMin, threadsPerHour, peerID); err != nil {
			return fmt.Errorf("hub: set limits: %w", err)
		}
		return audit.Record(ctx, tx, "operator", "limits_change", []string{peerID},
			map[string]any{"sends_per_min": sendsPerMin, "threads_per_hour": threadsPerHour})
	})
}

// SetPeerDisabled disables or re-enables peerID. A disabled peer's
// credential is refused (401; GET /v1/events answers 204) and it leaves the
// directory ("removed"); re-enabling brings it back ("enrolled"). The
// operator peer cannot be disabled (422 invalid_value). Audited.
func (h *Hub) SetPeerDisabled(ctx context.Context, peerID string, disabled bool) error {
	changed := false
	err := h.DB.Tx(ctx, func(tx *sql.Tx) error {
		changed = false
		_, kind, was, err := peerRow(ctx, tx, peerID)
		if err != nil {
			return err
		}
		if kind == "operator" && disabled {
			return invalidValue("peer_id", "the operator peer cannot be disabled")
		}
		if was == disabled {
			return nil
		}
		changed = true
		v, action, change := 0, "peer_enable", "enrolled"
		if disabled {
			v, action, change = 1, "peer_disable", "removed"
		}
		if _, err := tx.ExecContext(ctx, `UPDATE peers SET disabled = ? WHERE peer_id = ?`, v, peerID); err != nil {
			return fmt.Errorf("hub: disable peer: %w", err)
		}
		fields := enrolledFields
		if disabled {
			fields = nil
		}
		if err := h.DirectoryUpdated(ctx, tx, peerID, change, fields); err != nil {
			return err
		}
		return audit.Record(ctx, tx, "operator", action, []string{peerID}, nil)
	})
	if err != nil {
		return err
	}
	if changed {
		h.notifyEnabled(ctx)
		h.Log.Notify(peerID) // a disabled peer's open stream notices
	}
	return nil
}

// UnarchiveGroup unarchives ad hoc group grpID (PROTOCOL §7, R21) and marks
// it active now, so the next idle sweep does not archive it again. An
// unknown group is 404 not_found; an unarchived one is left as is.
func (h *Hub) UnarchiveGroup(ctx context.Context, grpID string) error {
	return h.DB.Tx(ctx, func(tx *sql.Tx) error {
		var archived sql.NullString
		err := tx.QueryRowContext(ctx, `SELECT archived_at FROM groups WHERE grp_id = ?`, grpID).Scan(&archived)
		if errors.Is(err, sql.ErrNoRows) {
			e := E(http.StatusNotFound, "not_found", "no such group")
			e.Details["group_id"] = grpID
			return e
		}
		if err != nil {
			return fmt.Errorf("hub: find group: %w", err)
		}
		if !archived.Valid {
			return nil
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE groups SET archived_at = NULL, last_active_at = ? WHERE grp_id = ?`, h.nowStr(), grpID); err != nil {
			return fmt.Errorf("hub: unarchive group: %w", err)
		}
		return audit.Record(ctx, tx, "operator", "group_unarchive", []string{grpID}, nil)
	})
}

// DeadLetter is one entry of GET /v1/admin/dead-letters (PROTOCOL §5.5).
type DeadLetter struct {
	DeliveryID string  `json:"delivery_id"`
	Recipient  string  `json:"recipient"`
	ThreadID   string  `json:"thread_id"`
	Attempt    int     `json:"attempt"`
	LastStatus *int    `json:"last_status"`
	LastError  *string `json:"last_error"`
	UpdatedAt  string  `json:"updated_at"`
}

// DeadLetters lists every delivery in state dead_letter, oldest first.
// Nothing is auto-deleted (§5.5); discarded ones are no longer listed.
func (h *Hub) DeadLetters(ctx context.Context) ([]DeadLetter, error) {
	rows, err := h.DB.QueryContext(ctx,
		`SELECT dly_id, recipient, thr_id, attempt, last_status, last_error, updated_at
		 FROM deliveries WHERE state = 'dead_letter' ORDER BY updated_at, dly_id`)
	if err != nil {
		return nil, fmt.Errorf("hub: dead letters: %w", err)
	}
	defer rows.Close()
	out := []DeadLetter{}
	for rows.Next() {
		var d DeadLetter
		var st sql.NullInt64
		var le sql.NullString
		if err := rows.Scan(&d.DeliveryID, &d.Recipient, &d.ThreadID, &d.Attempt, &st, &le, &d.UpdatedAt); err != nil {
			return nil, fmt.Errorf("hub: dead letters: %w", err)
		}
		if st.Valid {
			v := int(st.Int64)
			d.LastStatus = &v
		}
		d.LastError = nullable(le)
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("hub: dead letters: %w", err)
	}
	return out, nil
}

func unknownDeadLetter(dly string) *APIError {
	e := E(http.StatusNotFound, "not_found", "no such dead letter")
	e.Details["delivery_id"] = dly
	return e
}

// RetryDeadLetter re-queues dead letter dly for push now (PROTOCOL §5.5,
// R18): same delivery_id, the attempt counter continues, and a fresh budget
// of PushAttempts attempts. A delivery that is not a dead letter is 404
// not_found. Callers kick the push worker.
func (h *Hub) RetryDeadLetter(ctx context.Context, dly string) error {
	return h.DB.Tx(ctx, func(tx *sql.Tx) error {
		now := h.nowStr()
		res, err := tx.ExecContext(ctx,
			`UPDATE deliveries SET state = 'queued', attempts_left = ?, next_attempt_at = ?, held = 0, updated_at = ?
			 WHERE dly_id = ? AND state = 'dead_letter'`, PushAttempts, now, now, dly)
		if err != nil {
			return fmt.Errorf("hub: retry dead letter: %w", err)
		}
		if n, _ := res.RowsAffected(); n != 1 {
			return unknownDeadLetter(dly)
		}
		return audit.Record(ctx, tx, "operator", "dead_letter_retry", []string{dly}, nil)
	})
}

// DiscardDeadLetter moves dead letter dly to the terminal state discarded;
// it is no longer listed. Not a dead letter: 404 not_found. Audited.
func (h *Hub) DiscardDeadLetter(ctx context.Context, dly string) error {
	return h.DB.Tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			`UPDATE deliveries SET state = 'discarded', next_attempt_at = NULL, updated_at = ?
			 WHERE dly_id = ? AND state = 'dead_letter'`, h.nowStr(), dly)
		if err != nil {
			return fmt.Errorf("hub: discard dead letter: %w", err)
		}
		if n, _ := res.RowsAffected(); n != 1 {
			return unknownDeadLetter(dly)
		}
		return audit.Record(ctx, tx, "operator", "dead_letter_discard", []string{dly}, nil)
	})
}

// ThreadPatch is a PATCH /v1/admin/threads/{id} body. A *Set flag means
// the field was present; a nil Budget then means unlimited and a nil Title
// clears the title.
type ThreadPatch struct {
	BudgetSet     bool
	Budget        *int
	TitleSet      bool
	Title         *string
	HistoryAccess *string // "from_join" | "full"
	Locked        *bool
}

// PatchThread applies p to thread thrID (PROTOCOL §2.12: raise the budget,
// rename; §16.3: history_access, locked_participants) and appends a
// thread_updated Event with the new ThreadSummary to every participant. A
// negative budget or unknown history_access is 422 invalid_value; an
// unknown thread 404 unknown_thread. Audited with the changed field names.
func (h *Hub) PatchThread(ctx context.Context, thrID string, p ThreadPatch) error {
	if p.BudgetSet && p.Budget != nil && *p.Budget < 0 {
		return invalidValue("budget_remaining", "budget_remaining is a non-negative integer or null")
	}
	if p.HistoryAccess != nil && *p.HistoryAccess != "from_join" && *p.HistoryAccess != "full" {
		return invalidValue("history_access", "history_access is from_join or full")
	}
	var notify []string
	err := h.DB.Tx(ctx, func(tx *sql.Tx) error {
		thr, err := loadThread(ctx, tx, thrID)
		if err != nil {
			return err
		}
		var fields []string
		if p.BudgetSet {
			thr.Budget = p.Budget
			fields = append(fields, "budget_remaining")
		}
		if p.TitleSet {
			thr.Title = p.Title
			fields = append(fields, "title")
		}
		if p.HistoryAccess != nil {
			thr.HistoryAccess = *p.HistoryAccess
			fields = append(fields, "history_access")
		}
		if p.Locked != nil {
			thr.Locked = *p.Locked
			fields = append(fields, "locked_participants")
		}
		var budget any
		if thr.Budget != nil {
			budget = *thr.Budget
		}
		locked := 0
		if thr.Locked {
			locked = 1
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE threads SET budget_remaining = ?, title = ?, history_access = ?, locked_participants = ? WHERE thr_id = ?`,
			budget, thr.Title, thr.HistoryAccess, locked, thrID); err != nil {
			return fmt.Errorf("hub: patch thread: %w", err)
		}
		sum := thr.summary()
		now := h.Now()
		for _, pt := range thr.participants {
			if _, err := h.Log.Append(ctx, tx, pt, "thread_updated", now, sum); err != nil {
				return err
			}
		}
		notify = thr.participants
		return audit.Record(ctx, tx, "operator", "thread_patch", []string{thrID}, map[string]any{"fields": fields})
	})
	if err != nil {
		return err
	}
	if len(notify) > 0 {
		h.Log.Notify(notify...)
	}
	return nil
}

// PurgeBlob removes sha's bytes and keeps its tombstone (PROTOCOL §11);
// later reads are 410 blob_purged. An unknown hash is 404 unknown_blob.
// Audited as "purge".
func (h *Hub) PurgeBlob(ctx context.Context, sha string) error {
	if err := h.Blobs.Purge(ctx, sha); err != nil {
		if errors.Is(err, blob.ErrNotFound) {
			e := E(http.StatusNotFound, "unknown_blob", "no such blob")
			e.Details["sha256"] = sha
			return e
		}
		return err
	}
	return h.DB.Tx(ctx, func(tx *sql.Tx) error {
		return audit.Record(ctx, tx, "operator", "purge", []string{sha}, nil)
	})
}

// AuditEntry is one audit row as GET /v1/admin/audit returns it.
type AuditEntry struct {
	ID      int64          `json:"id"`
	At      string         `json:"at"`
	Actor   string         `json:"actor"`
	Action  string         `json:"action"`
	Targets []string       `json:"target_ids"`
	Detail  map[string]any `json:"detail"`
}

// AuditPage returns up to limit audit rows (≤ 0 means DefaultPageLimit,
// capped at MaxPageLimit) with id below before (0 means from the newest),
// newest first, and next: the before of the following page, or nil on the
// last page.
func (h *Hub) AuditPage(ctx context.Context, limit int, before int64) ([]AuditEntry, *string, error) {
	limit = pageLimit(limit)
	q := `SELECT id, at, actor, action, target_ids, detail FROM audit`
	args := []any{}
	if before > 0 {
		q += ` WHERE id < ?`
		args = append(args, before)
	}
	q += ` ORDER BY id DESC LIMIT ?`
	args = append(args, limit+1)
	rows, err := h.DB.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, nil, fmt.Errorf("hub: audit: %w", err)
	}
	defer rows.Close()
	out := []AuditEntry{}
	for rows.Next() {
		var e AuditEntry
		var targets, detail string
		if err := rows.Scan(&e.ID, &e.At, &e.Actor, &e.Action, &targets, &detail); err != nil {
			return nil, nil, fmt.Errorf("hub: audit: %w", err)
		}
		if err := json.Unmarshal([]byte(targets), &e.Targets); err != nil {
			return nil, nil, fmt.Errorf("hub: audit %d targets: %w", e.ID, err)
		}
		if err := json.Unmarshal([]byte(detail), &e.Detail); err != nil {
			return nil, nil, fmt.Errorf("hub: audit %d detail: %w", e.ID, err)
		}
		if e.Targets == nil {
			e.Targets = []string{}
		}
		if e.Detail == nil {
			e.Detail = map[string]any{}
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("hub: audit: %w", err)
	}
	if len(out) <= limit {
		return out, nil, nil
	}
	out = out[:limit]
	next := strconv.FormatInt(out[limit-1].ID, 10)
	return out, &next, nil
}

// AdminCounters are the dashboard counters of GET /v1/admin/counters.
var AdminCounters = []string{CounterLoopCut, CounterRateLimited, CounterDeadLetters, CounterPushConflicts, CounterPushAuthFailed}

// Counters returns every AdminCounters value (0 when never bumped).
func (h *Hub) Counters(ctx context.Context) (map[string]int, error) {
	out := make(map[string]int, len(AdminCounters))
	for _, name := range AdminCounters {
		var v int
		if err := h.DB.QueryRowContext(ctx,
			`SELECT coalesce((SELECT value FROM counters WHERE name = ?), 0)`, name).Scan(&v); err != nil {
			return nil, fmt.Errorf("hub: counter %s: %w", name, err)
		}
		out[name] = v
	}
	return out, nil
}
