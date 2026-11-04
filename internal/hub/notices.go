package hub

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"time"
	"unicode/utf8"

	"github.com/XertroV/vecnet/internal/audit"
	"github.com/XertroV/vecnet/internal/auth"
	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/hubid"
	"github.com/XertroV/vecnet/internal/ids"
)

const MaxNoticeMessage = 1024 // bytes, UTF-8

type NoticeRequest struct{ Kind, Message, At, ExpiresAt, NewURL string }

const noticeColumns = `notice_id, kind, message, hub, issued_at, COALESCE(at,''), expires_at, COALESCE(new_url,''), sig, COALESCE(cleared_at,''), COALESCE(cleared_how,'')`

type noticeScanner interface{ Scan(...any) error }

func scanNotice(row noticeScanner) (envelope.HubNotice, error) {
	var n envelope.HubNotice
	err := row.Scan(&n.NoticeID, &n.Kind, &n.Message, &n.Hub, &n.IssuedAt, &n.At, &n.ExpiresAt, &n.NewURL, &n.Sig, &n.ClearedAt, &n.ClearedHow)
	return n, err
}

// prepareNotice validates and signs exactly the structured notice record.
func (h *Hub) prepareNotice(r NoticeRequest) (envelope.HubNotice, error) {
	switch r.Kind {
	case "maintenance", "restart", "moving":
	default:
		return envelope.HubNotice{}, invalidValue("kind", "unknown notice kind")
	}
	if len(r.Message) < 1 || len(r.Message) > MaxNoticeMessage || !utf8.ValidString(r.Message) {
		return envelope.HubNotice{}, invalidValue("message", "message must be 1 to 1024 UTF-8 bytes")
	}
	own, err := hubid.NormalizeURL(h.Cfg.PublicURL)
	if err != nil {
		return envelope.HubNotice{}, fmt.Errorf("hub: notice public URL: %w", err)
	}
	if r.Kind == "moving" {
		next, err := hubid.NormalizeURL(r.NewURL)
		if err != nil || next == own {
			return envelope.HubNotice{}, invalidValue("new_url", "moving requires a different valid hub URL")
		}
		r.NewURL = next
	} else if r.NewURL != "" {
		return envelope.HubNotice{}, invalidValue("new_url", "new_url is only valid for moving notices")
	}
	now := h.Now()
	base := now
	if r.At != "" {
		at, err := envelope.ParseTime(r.At)
		if err != nil || envelope.FormatTime(at) != r.At {
			return envelope.HubNotice{}, invalidValue("at", "at must be a wire timestamp")
		}
		if at.After(base) {
			base = at
		}
	}
	if r.ExpiresAt == "" {
		ttl := 24 * time.Hour
		if r.Kind == "moving" {
			ttl = 30 * 24 * time.Hour
		}
		r.ExpiresAt = envelope.FormatTime(base.Add(ttl))
	} else {
		expiry, err := envelope.ParseTime(r.ExpiresAt)
		if err != nil || envelope.FormatTime(expiry) != r.ExpiresAt || !expiry.After(now) {
			return envelope.HubNotice{}, invalidValue("expires_at", "expires_at must be a future wire timestamp")
		}
	}
	n := envelope.HubNotice{NoticeID: ids.New(ids.Ntc), Kind: r.Kind, Message: r.Message, Hub: own, IssuedAt: envelope.FormatTime(now), At: r.At, ExpiresAt: r.ExpiresAt, NewURL: r.NewURL}
	key := h.IdentityKey()
	if err := key.SignNotice(&n); err != nil {
		return envelope.HubNotice{}, err
	}
	return n, nil
}

// PostNotice atomically replaces the same kind and appends each change to
// every enabled peer's durable log. Readers wake only after commit.
func (h *Hub) PostNotice(ctx context.Context, c auth.Caller, r NoticeRequest) (envelope.HubNotice, error) {
	if c.Kind != "operator" {
		return envelope.HubNotice{}, E(http.StatusForbidden, "forbidden", "operator credential required")
	}
	n, err := h.prepareNotice(r)
	if err != nil {
		return envelope.HubNotice{}, err
	}
	err = h.DB.Tx(ctx, func(tx *sql.Tx) error { return h.insertNotice(ctx, tx, n, "operator") })
	if err != nil {
		return envelope.HubNotice{}, err
	}
	h.notifyEnabled(ctx)
	return n, nil
}

func (h *Hub) insertNotice(ctx context.Context, tx *sql.Tx, n envelope.HubNotice, actor string) error {
	if _, err := h.clearNotices(ctx, tx, `cleared_at IS NULL AND kind = ?`, []any{n.Kind}, "replaced", actor, n.IssuedAt); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO hub_notices(notice_id,kind,message,hub,issued_at,at,expires_at,new_url,sig,created_by) VALUES (?,?,?,?,?,NULLIF(?,''),?,NULLIF(?,''),?,?)`, n.NoticeID, n.Kind, n.Message, n.Hub, n.IssuedAt, n.At, n.ExpiresAt, n.NewURL, n.Sig, actor); err != nil {
		return err
	}
	if err := h.noticeEvent(ctx, tx, "posted", n); err != nil {
		return err
	}
	return audit.Record(ctx, tx, actor, "notice_post", []string{n.NoticeID}, map[string]any{"notice_id": n.NoticeID, "kind": n.Kind})
}

// noticeEvent uses the same owner query as DirectoryUpdated. Notices never
// create a message, delivery, thread, or operator-view event.
func (h *Hub) noticeEvent(ctx context.Context, tx *sql.Tx, change string, n envelope.HubNotice) error {
	rows, err := tx.QueryContext(ctx, `SELECT peer_id FROM peers WHERE disabled = 0 ORDER BY peer_id`)
	if err != nil {
		return err
	}
	var owners []string
	for rows.Next() {
		var owner string
		if err := rows.Scan(&owner); err != nil {
			rows.Close()
			return err
		}
		owners = append(owners, owner)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, owner := range owners {
		if _, err := h.Log.Append(ctx, tx, owner, "hub_notice", h.Now(), envelope.HubNoticeEvent{Change: change, Notice: n}); err != nil {
			return err
		}
	}
	return nil
}

// clearNotices reads and closes its rows before writing on the same transaction.
// where is an internal predicate; all variable values are SQL parameters.
func (h *Hub) clearNotices(ctx context.Context, tx *sql.Tx, where string, args []any, how, actor, at string) (int, error) {
	rows, err := tx.QueryContext(ctx, `SELECT `+noticeColumns+` FROM hub_notices WHERE `+where+` ORDER BY issued_at, notice_id`, args...)
	if err != nil {
		return 0, err
	}
	var notices []envelope.HubNotice
	for rows.Next() {
		n, err := scanNotice(rows)
		if err != nil {
			rows.Close()
			return 0, err
		}
		notices = append(notices, n)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, err
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}
	for _, n := range notices {
		if _, err := tx.ExecContext(ctx, `UPDATE hub_notices SET cleared_at=?,cleared_how=? WHERE notice_id=?`, at, how, n.NoticeID); err != nil {
			return 0, err
		}
		n.ClearedAt, n.ClearedHow = at, how
		if err := h.noticeEvent(ctx, tx, "cleared", n); err != nil {
			return 0, err
		}
		if err := audit.Record(ctx, tx, actor, "notice_clear", []string{n.NoticeID}, map[string]any{"notice_id": n.NoticeID, "how": how}); err != nil {
			return 0, err
		}
	}
	return len(notices), nil
}

// ClearNotice is idempotent for a known notice, including one already cleared.
func (h *Hub) ClearNotice(ctx context.Context, c auth.Caller, id string) error {
	if c.Kind != "operator" {
		return E(http.StatusForbidden, "forbidden", "operator credential required")
	}
	count := 0
	err := h.DB.Tx(ctx, func(tx *sql.Tx) error {
		var one int
		err := tx.QueryRowContext(ctx, `SELECT 1 FROM hub_notices WHERE notice_id=?`, id).Scan(&one)
		if errors.Is(err, sql.ErrNoRows) {
			return E(http.StatusNotFound, "not_found", "unknown notice")
		}
		if err != nil {
			return err
		}
		count, err = h.clearNotices(ctx, tx, `cleared_at IS NULL AND notice_id=?`, []any{id}, "operator", "operator", h.nowStr())
		return err
	})
	if err == nil && count > 0 {
		h.notifyEnabled(ctx)
	}
	return err
}

// ActiveNotices returns unexpired, uncleared notices in issue order.
func (h *Hub) ActiveNotices(ctx context.Context) ([]envelope.HubNotice, error) {
	rows, err := h.DB.QueryContext(ctx, `SELECT `+noticeColumns+` FROM hub_notices WHERE cleared_at IS NULL AND expires_at > ? ORDER BY issued_at, notice_id`, h.nowStr())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	notices := []envelope.HubNotice{}
	for rows.Next() {
		n, err := scanNotice(rows)
		if err != nil {
			return nil, err
		}
		notices = append(notices, n)
	}
	return notices, rows.Err()
}

func (h *Hub) clearNoticeSet(ctx context.Context, where string, args []any, how string) (int, error) {
	count := 0
	err := h.DB.Tx(ctx, func(tx *sql.Tx) error {
		var err error
		count, err = h.clearNotices(ctx, tx, where, args, how, "hub", h.nowStr())
		return err
	})
	if err != nil {
		return 0, err
	}
	if count > 0 {
		h.notifyEnabled(ctx)
	}
	return count, nil
}

// ExpireNotices records the expiry once and fans the cleared state out.
func (h *Hub) ExpireNotices(ctx context.Context) (int, error) {
	return h.clearNoticeSet(ctx, `cleared_at IS NULL AND expires_at <= ?`, []any{h.nowStr()}, "expired")
}

// ClearOnBoot is called by the startup owner after the public URL is set.
func (h *Hub) ClearOnBoot(ctx context.Context) (int, error) {
	own, err := hubid.NormalizeURL(h.Cfg.PublicURL)
	if err != nil {
		return 0, err
	}
	return h.clearNoticeSet(ctx, `cleared_at IS NULL AND (kind='restart' OR (kind='moving' AND new_url=?))`, []any{own}, "boot")
}

// EnsureMovingNotice is atomic even when several startup callers race. An
// expired notice is replaced so relocated mode always has a live announcement.
func (h *Hub) EnsureMovingNotice(ctx context.Context, newURL string) (envelope.HubNotice, error) {
	n, err := h.prepareNotice(NoticeRequest{Kind: "moving", Message: "This hub has moved.", At: h.nowStr(), NewURL: newURL})
	if err != nil {
		return envelope.HubNotice{}, err
	}
	created := false
	err = h.DB.Tx(ctx, func(tx *sql.Tx) error {
		existing, err := scanNotice(tx.QueryRowContext(ctx, `SELECT `+noticeColumns+` FROM hub_notices WHERE cleared_at IS NULL AND expires_at > ? AND kind='moving' AND new_url=? ORDER BY issued_at LIMIT 1`, h.nowStr(), n.NewURL))
		if err == nil {
			n = existing
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if err := h.insertNotice(ctx, tx, n, "hub"); err != nil {
			return err
		}
		created = true
		return nil
	})
	if err != nil {
		return envelope.HubNotice{}, err
	}
	if created {
		h.notifyEnabled(ctx)
	}
	return n, nil
}
