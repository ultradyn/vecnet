package hub

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"math"
	"mime"
	"net/http"
	"time"

	"github.com/XertroV/vecnet/internal/auth"
	"github.com/XertroV/vecnet/internal/blob"
)

// PutBlob stores an upload from c (PROTOCOL §2.7). The quota is charged to
// c's peer, or to its principal for a principal credential. mediaType is the
// request Content-Type ("" → application/octet-stream); an unparsable one is
// 422 invalid_value. Over the cap → 413 blob_too_large; over the daily quota
// → 429 upload_quota_exceeded with retry_after_seconds until the next UTC
// midnight; re-uploading purged bytes → 410 blob_purged.
func (h *Hub) PutBlob(ctx context.Context, c auth.Caller, r io.Reader, mediaType string, ttl time.Duration) (blob.Meta, error) {
	if h.Cfg.BlobBackend == "r2" {
		return blob.Meta{}, E(409, "direct_upload_required", "use the upload grant API")
	}
	uploader := c.PeerID
	if uploader == "" {
		uploader = c.PrincipalID
	}
	if uploader == "" {
		return blob.Meta{}, E(http.StatusForbidden, "forbidden", "no peer or principal to charge the upload to")
	}
	return h.putBlobAs(ctx, uploader, r, mediaType, ttl)
}

// putBlobAs is PutBlob for an uploader the caller has already derived: a
// peer, a principal, or a source (IngestSource).
func (h *Hub) putBlobAs(ctx context.Context, uploader string, r io.Reader, mediaType string, ttl time.Duration) (blob.Meta, error) {
	if mediaType != "" {
		mt, params, err := mime.ParseMediaType(mediaType)
		if err == nil {
			mediaType = mime.FormatMediaType(mt, params)
		}
		if err != nil || mediaType == "" {
			e := E(http.StatusUnprocessableEntity, "invalid_value", "Content-Type is not a media type")
			e.Details["field"] = "content_type"
			return blob.Meta{}, e
		}
	}
	m, err := h.Blobs.Put(ctx, uploader, r, mediaType, ttl)
	switch {
	case err == nil:
		return m, nil
	case errors.Is(err, blob.ErrTooLarge):
		e := E(http.StatusRequestEntityTooLarge, "blob_too_large", "blob exceeds the per-blob cap")
		e.Details["limit"] = h.Blobs.MaxSize
		return blob.Meta{}, e
	case errors.Is(err, blob.ErrQuota):
		e := E(http.StatusTooManyRequests, "upload_quota_exceeded", "daily upload quota exceeded")
		e.Details["limit"] = h.Blobs.DailyQuota
		e.Details["retry_after_seconds"] = secondsToUTCMidnight(h.Now())
		return blob.Meta{}, e
	case errors.Is(err, blob.ErrPurged):
		return blob.Meta{}, E(http.StatusGone, "blob_purged", "this blob was purged by the operator")
	}
	return blob.Meta{}, err
}

// secondsToUTCMidnight is the whole seconds (at least 1) until the next UTC
// day, when the daily upload quota resets.
func secondsToUTCMidnight(now time.Time) int {
	now = now.UTC()
	midnight := time.Date(now.Year(), now.Month(), now.Day()+1, 0, 0, 0, 0, time.UTC)
	return max(1, int(math.Ceil(midnight.Sub(now).Seconds())))
}

// ReadBlob authorises and opens sha (PROTOCOL §2.7, §11, §16.3). Access is
// granted by a token valid for sha whose recipient peer still exists and is
// enabled, by the operator credential, or by c being
// (a peer of a principal) a participant of a thread holding a message that
// references sha and that the participant may see under the thread's
// history_access. c nil means no bearer was presented. A malformed hash is
// 404 unknown_blob before anything else; a refused read is 403 forbidden;
// then 404 unknown_blob or 410 blob_purged by the blob's state.
func (h *Hub) ReadBlob(ctx context.Context, c *auth.Caller, token, sha string) (io.ReadSeekCloser, blob.Meta, error) {
	if !blob.ValidSHA(sha) {
		return nil, blob.Meta{}, E(http.StatusNotFound, "unknown_blob", "no such blob")
	}
	allowed := false
	if token != "" {
		var recipient string
		if recipient, allowed = h.Blobs.VerifyToken(token, sha, h.Now()); allowed {
			// The token dies with its recipient: a disabled or removed
			// peer's links are revoked (PROTOCOL §14 inv. 6).
			var disabled int
			err := h.DB.QueryRowContext(ctx, `SELECT disabled FROM peers WHERE peer_id = ?`, recipient).Scan(&disabled)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return nil, blob.Meta{}, fmt.Errorf("hub: blob token recipient: %w", err)
			}
			allowed = err == nil && disabled == 0
		}
	}
	if !allowed && c != nil {
		var err error
		if allowed, err = h.canSeeBlob(ctx, *c, sha); err != nil {
			return nil, blob.Meta{}, err
		}
	}
	if !allowed {
		return nil, blob.Meta{}, E(http.StatusForbidden, "forbidden", "not authorised to read this blob")
	}
	f, m, err := h.Blobs.Open(ctx, sha)
	switch {
	case errors.Is(err, blob.ErrNotFound):
		return nil, blob.Meta{}, E(http.StatusNotFound, "unknown_blob", "no such blob")
	case errors.Is(err, blob.ErrPurged):
		return nil, blob.Meta{}, E(http.StatusGone, "blob_purged", "this blob was purged by the operator")
	case err != nil:
		return nil, blob.Meta{}, err
	}
	return f, m, nil
}

// canSeeBlob reports bearer read access: the operator always; a peer
// credential through CanSeeBlob; a principal credential when any of its
// peers passes the same check.
func (h *Hub) canSeeBlob(ctx context.Context, c auth.Caller, sha string) (bool, error) {
	switch c.Kind {
	case "operator":
		return true, nil
	case "principal":
		return h.blobVisible(ctx, c.PeerID, c.PrincipalID, sha)
	}
	if c.PeerID == "" {
		return false, nil
	}
	return h.CanSeeBlob(ctx, c.PeerID, sha)
}
