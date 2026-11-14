package hub

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/XertroV/vecnet/internal/audit"
	"github.com/XertroV/vecnet/internal/auth"
	"github.com/XertroV/vecnet/internal/avatar"
	"github.com/XertroV/vecnet/internal/egress"
	"github.com/XertroV/vecnet/internal/envelope"
)

// Peer avatars (PROTOCOL §8.1). A peer sets or clears its own; the operator
// sets or clears any peer's. Every image is normalised by internal/avatar to
// a 128×128 PNG and stored in the avatars table under its SHA-256, which the
// directory carries as `avatar`.

const (
	// DefaultAvatarChangesPerHour is a peer's self-service avatar changes
	// (sets and clears) allowed per rolling hour. Operator changes are not
	// counted.
	DefaultAvatarChangesPerHour = 10
	// AvatarFetchTimeout bounds a hub-side fetch of an avatar URL, body
	// included.
	AvatarFetchTimeout = 10 * time.Second
	// avatarWindow is the rate-limit window.
	avatarWindow = time.Hour
	// maxAvatarDecodes bounds concurrent decodes: each holds up to
	// avatar.MaxDecodedBytes (64 MiB) for a moment, and a Fly machine has
	// 512 MB.
	maxAvatarDecodes = 2
)

var avatarDecodes = make(chan struct{}, maxAvatarDecodes)

// avatarFields is the fields list of an avatar's directory_updated Event.
var avatarFields = []string{"avatar"}

// avatarTarget is the peer an avatar change applies to and how it was
// authorised.
type avatarTarget struct {
	peer    string
	actor   string // audit actor: the peer itself, or "operator"
	limited bool   // counts against the per-peer rate limit
}

// selfTarget resolves the peer a self-service call acts for, as a send's
// sender is resolved (PROTOCOL §2.2 step 1): a peer credential is its own
// peer, a principal names one of its peers with as, the operator credential
// is the operator peer (or as, under the operator principal).
func (h *Hub) selfTarget(c auth.Caller, as string) func(ctx context.Context, tx *sql.Tx) (avatarTarget, error) {
	return func(ctx context.Context, tx *sql.Tx) (avatarTarget, error) {
		s, err := h.resolveSender(ctx, tx, c, as)
		if err != nil {
			return avatarTarget{}, err
		}
		return avatarTarget{peer: s.peer, actor: s.peer, limited: c.Kind != "operator"}, nil
	}
}

// operatorTarget is any existing peer, disabled ones included, for the
// operator (404 not_found otherwise).
func operatorTarget(peerID string) func(ctx context.Context, tx *sql.Tx) (avatarTarget, error) {
	return func(ctx context.Context, tx *sql.Tx) (avatarTarget, error) {
		if _, _, _, err := peerRow(ctx, tx, peerID); err != nil {
			return avatarTarget{}, err
		}
		return avatarTarget{peer: peerID, actor: "operator"}, nil
	}
}

// SetAvatar sets the caller's own avatar from image bytes (PROTOCOL §8.1)
// and returns its sha256. as is the principal's acting peer, as on
// POST /v1/presence.
func (h *Hub) SetAvatar(ctx context.Context, c auth.Caller, as string, data []byte) (string, error) {
	return h.setAvatar(ctx, h.selfTarget(c, as), func() ([]byte, error) { return data, nil })
}

// SetAvatarURL is SetAvatar from an https URL the hub fetches through the
// egress guard: public addresses only, no redirects, AvatarFetchTimeout,
// avatar.MaxBytes. The URL is never stored or passed on.
func (h *Hub) SetAvatarURL(ctx context.Context, c auth.Caller, as, rawURL string) (string, error) {
	u, err := h.EgressPolicy().CheckURL(rawURL)
	if err != nil {
		return "", invalidURL("url", err)
	}
	return h.setAvatar(ctx, h.selfTarget(c, as), func() ([]byte, error) { return h.fetchAvatar(ctx, u) })
}

// ClearAvatar removes the caller's own avatar; clearing none is a no-op.
func (h *Hub) ClearAvatar(ctx context.Context, c auth.Caller, as string) error {
	return h.clearAvatar(ctx, h.selfTarget(c, as))
}

// AdminSetAvatar sets peerID's avatar for the operator (no rate limit).
func (h *Hub) AdminSetAvatar(ctx context.Context, peerID string, data []byte) (string, error) {
	return h.setAvatar(ctx, operatorTarget(peerID), func() ([]byte, error) { return data, nil })
}

// AdminSetAvatarURL is AdminSetAvatar from a URL, fetched as SetAvatarURL does.
func (h *Hub) AdminSetAvatarURL(ctx context.Context, peerID, rawURL string) (string, error) {
	u, err := h.EgressPolicy().CheckURL(rawURL)
	if err != nil {
		return "", invalidURL("url", err)
	}
	return h.setAvatar(ctx, operatorTarget(peerID), func() ([]byte, error) { return h.fetchAvatar(ctx, u) })
}

// AdminClearAvatar removes peerID's avatar for the operator.
func (h *Hub) AdminClearAvatar(ctx context.Context, peerID string) error {
	return h.clearAvatar(ctx, operatorTarget(peerID))
}

// setAvatar authorises and rate-checks first, so a refused caller costs no
// fetch or decode; then normalises the image outside any transaction; then
// re-checks and writes. Setting the avatar a peer already has changes
// nothing and is not counted.
func (h *Hub) setAvatar(ctx context.Context, target func(context.Context, *sql.Tx) (avatarTarget, error), load func() ([]byte, error)) (string, error) {
	if err := h.DB.Tx(ctx, func(tx *sql.Tx) error {
		t, err := target(ctx, tx)
		if err != nil {
			return err
		}
		return h.avatarRate(ctx, tx, t)
	}); err != nil {
		return "", err
	}
	data, err := load()
	if err != nil {
		return "", err
	}
	res, err := normalizeAvatar(ctx, data)
	if err != nil {
		return "", err
	}
	changed := false
	err = h.DB.Tx(ctx, func(tx *sql.Tx) error {
		t, err := target(ctx, tx)
		if err != nil {
			return err
		}
		old, err := currentAvatar(ctx, tx, t.peer)
		if err != nil || (old != nil && *old == res.SHA256) {
			return err
		}
		if err := h.avatarRate(ctx, tx, t); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO avatars(sha256, png, created_at) VALUES (?, ?, ?) ON CONFLICT(sha256) DO NOTHING`,
			res.SHA256, res.PNG, h.nowStr()); err != nil {
			return fmt.Errorf("hub: store avatar: %w", err)
		}
		changed = true
		return h.writeAvatar(ctx, tx, t, old, &res.SHA256, "avatar_set")
	})
	if err != nil {
		return "", err
	}
	if changed {
		h.notifyEnabled(ctx)
	}
	return res.SHA256, nil
}

func (h *Hub) clearAvatar(ctx context.Context, target func(context.Context, *sql.Tx) (avatarTarget, error)) error {
	changed := false
	err := h.DB.Tx(ctx, func(tx *sql.Tx) error {
		t, err := target(ctx, tx)
		if err != nil {
			return err
		}
		old, err := currentAvatar(ctx, tx, t.peer)
		if err != nil || old == nil {
			return err
		}
		if err := h.avatarRate(ctx, tx, t); err != nil {
			return err
		}
		changed = true
		return h.writeAvatar(ctx, tx, t, old, nil, "avatar_clear")
	})
	if err == nil && changed {
		h.notifyEnabled(ctx)
	}
	return err
}

// writeAvatar points t.peer at sha (nil clears), counts a limited change,
// deletes the old image when no peer names it any more, tells every peer
// (directory_updated, fields ["avatar"]) and audits action.
func (h *Hub) writeAvatar(ctx context.Context, tx *sql.Tx, t avatarTarget, old, sha *string, action string) error {
	if _, err := tx.ExecContext(ctx, `UPDATE peers SET avatar = ? WHERE peer_id = ?`, sha, t.peer); err != nil {
		return fmt.Errorf("hub: set avatar: %w", err)
	}
	if t.limited {
		now := h.Now()
		if _, err := tx.ExecContext(ctx, `DELETE FROM avatar_ops WHERE at <= ?`, envelope.FormatTime(now.Add(-avatarWindow))); err != nil {
			return fmt.Errorf("hub: prune avatar ops: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO avatar_ops(peer_id, at) VALUES (?, ?)`, t.peer, envelope.FormatTime(now)); err != nil {
			return fmt.Errorf("hub: count avatar op: %w", err)
		}
	}
	if old != nil {
		if _, err := tx.ExecContext(ctx, `DELETE FROM avatars WHERE sha256 = ? AND NOT EXISTS (SELECT 1 FROM peers WHERE avatar = ?)`, *old, *old); err != nil {
			return fmt.Errorf("hub: drop old avatar: %w", err)
		}
	}
	if err := h.DirectoryUpdated(ctx, tx, t.peer, "updated", avatarFields); err != nil {
		return err
	}
	detail := map[string]any{}
	if sha != nil {
		detail["sha256"] = *sha
	}
	return audit.Record(ctx, tx, t.actor, action, []string{t.peer}, detail)
}

// avatarRate is 429 rate_limited when t is limited and its peer has made
// Cfg.AvatarChangesPerHour changes in the last hour (PROTOCOL §8.1).
func (h *Hub) avatarRate(ctx context.Context, tx *sql.Tx, t avatarTarget) error {
	if !t.limited {
		return nil
	}
	limit := h.Cfg.AvatarChangesPerHour
	if limit <= 0 {
		limit = DefaultAvatarChangesPerHour
	}
	retry, err := windowRetry(ctx, tx, `SELECT at FROM avatar_ops WHERE peer_id = ? AND at > ? ORDER BY at`,
		t.peer, limit, avatarWindow, h.Now())
	if err != nil || retry == 0 {
		return err
	}
	e := E(http.StatusTooManyRequests, "rate_limited", "avatar change rate limit exceeded")
	e.Details["limit"] = "avatar_changes_per_hour"
	e.Details["max"] = limit
	e.Details["retry_after_seconds"] = retry
	return e
}

func currentAvatar(ctx context.Context, tx *sql.Tx, peerID string) (*string, error) {
	var cur sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT avatar FROM peers WHERE peer_id = ?`, peerID).Scan(&cur); err != nil {
		return nil, fmt.Errorf("hub: read avatar: %w", err)
	}
	return nullable(cur), nil
}

// normalizeAvatar runs avatar.Normalize under the decode bound and maps its
// errors onto the wire (PROTOCOL §8.1).
func normalizeAvatar(ctx context.Context, data []byte) (avatar.Result, error) {
	if len(data) > avatar.MaxBytes {
		return avatar.Result{}, avatarTooLarge()
	}
	select {
	case avatarDecodes <- struct{}{}:
	case <-ctx.Done():
		return avatar.Result{}, ctx.Err()
	}
	res, err := avatar.Normalize(data)
	<-avatarDecodes
	switch {
	case err == nil:
		return res, nil
	case errors.Is(err, avatar.ErrTooLarge):
		return res, avatarTooLarge()
	case errors.Is(err, avatar.ErrUnsupported):
		e := E(http.StatusUnsupportedMediaType, "unsupported_media_type", "an avatar must be a PNG, JPEG, GIF or WebP image")
		e.Details["accepted"] = []string{"image/png", "image/jpeg", "image/gif", "image/webp"}
		return res, e
	case errors.Is(err, avatar.ErrDimensions):
		e := E(http.StatusUnprocessableEntity, "invalid_image", "image dimensions exceed the avatar pixel cap")
		e.Details["reason"] = "too_many_pixels"
		e.Details["max_pixels"] = avatar.MaxPixels
		e.Details["max_side"] = avatar.MaxSide
		return res, e
	case errors.Is(err, avatar.ErrInvalid):
		e := E(http.StatusUnprocessableEntity, "invalid_image", "the image could not be decoded")
		e.Details["reason"] = "undecodable"
		return res, e
	}
	return res, err
}

// avatarTooLarge is the 413 for an image over avatar.MaxBytes.
func avatarTooLarge() *APIError {
	e := E(http.StatusRequestEntityTooLarge, "payload_too_large", "avatar image exceeds 2 MiB")
	e.Details["limit"] = avatar.MaxBytes
	return e
}

// fetchAvatar GETs u through the egress guard and returns at most
// avatar.MaxBytes of body. A refused address is 422 invalid_url; any other
// failure is 422 fetch_failed with a reason that never quotes the URL.
func (h *Hub) fetchAvatar(ctx context.Context, u *url.URL) ([]byte, error) {
	client := h.AvatarClient
	if client == nil {
		client = h.EgressPolicy().Client(AvatarFetchTimeout)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, invalidURL("url", egress.ErrInvalidURL)
	}
	req.Header.Set("Accept", "image/png, image/jpeg, image/gif, image/webp")
	req.Header.Set("User-Agent", "vecnet-hub (avatar fetch)")
	resp, err := client.Do(req)
	if err != nil {
		for _, refused := range []error{egress.ErrPrivateAddress, egress.ErrScheme, egress.ErrUserinfo, egress.ErrInvalidURL} {
			if errors.Is(err, refused) {
				return nil, invalidURL("url", refused)
			}
		}
		reason := EgressErr(err)
		if errors.Is(err, egress.ErrRedirect) {
			reason = "redirect refused"
		}
		return nil, fetchFailed(reason)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fetchFailed(fmt.Sprintf("status %d", resp.StatusCode))
	}
	if resp.ContentLength > avatar.MaxBytes {
		return nil, avatarTooLarge()
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, avatar.MaxBytes+1))
	if err != nil {
		return nil, fetchFailed(EgressErr(err))
	}
	if len(data) > avatar.MaxBytes {
		return nil, avatarTooLarge()
	}
	return data, nil
}

func fetchFailed(reason string) *APIError {
	e := E(http.StatusUnprocessableEntity, "fetch_failed", "the avatar URL could not be fetched")
	e.Details["field"] = "url"
	e.Details["reason"] = reason
	return e
}

// Avatar returns peerID's avatar PNG and its sha256 for c (PROTOCOL §8.1):
// any authenticated caller may read an enabled peer's, as it may read the
// directory; a disabled peer's only the operator. "me" is the caller's own
// peer. No such peer, or no avatar, is 404 not_found.
func (h *Hub) Avatar(ctx context.Context, c auth.Caller, peerID string) ([]byte, string, error) {
	if peerID == "me" {
		peerID = c.PeerID
	}
	none := E(http.StatusNotFound, "not_found", "no avatar")
	if peerID == "" {
		return nil, "", none
	}
	var (
		disabled int
		sha      sql.NullString
		png      []byte
	)
	err := h.DB.QueryRowContext(ctx,
		`SELECT p.disabled, a.sha256, a.png FROM peers p LEFT JOIN avatars a ON a.sha256 = p.avatar WHERE p.peer_id = ?`,
		peerID).Scan(&disabled, &sha, &png)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, "", none
	}
	if err != nil {
		return nil, "", fmt.Errorf("hub: read avatar: %w", err)
	}
	if !sha.Valid || (disabled != 0 && c.Kind != "operator") {
		return nil, "", none
	}
	return png, sha.String, nil
}
