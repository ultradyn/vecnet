package hub

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"net/http"
	"sync"
	"time"

	"github.com/XertroV/vecnet/internal/auth"
	"github.com/XertroV/vecnet/internal/blob"
	"github.com/XertroV/vecnet/internal/blobwire"
	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/ids"
)

// BlobGrantEvent is a capability event, never a downloaded-byte measurement.
type BlobGrantEvent struct {
	EventID        string
	Kind           string
	Status         int
	BlobID         string
	CallerIdentity string
	At             time.Time
}
type blobMintBudget struct {
	mu    sync.Mutex
	calls map[string][]time.Time
}

func (b *blobMintBudget) take(identity string, now time.Time) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.calls == nil {
		b.calls = map[string][]time.Time{}
	}
	// Enrollment bounds identities; remove idle entries without evicting live budgets.
	for key, times := range b.calls {
		if len(times) == 0 || !times[len(times)-1].After(now.Add(-time.Minute)) {
			delete(b.calls, key)
		}
	}
	times := b.calls[identity]
	i := 0
	for i < len(times) && !times[i].After(now.Add(-time.Minute)) {
		i++
	}
	times = times[i:]
	if len(times) >= 30 {
		b.calls[identity] = times
		return max(1, int(math.Ceil(times[0].Add(time.Minute).Sub(now).Seconds())))
	}
	b.calls[identity] = append(times, now)
	return 0
}

// callerActive rechecks current enrollment even for callers resolved before a
// provider wait. Credential resolution stays at the transport authentication seam.
func (h *Hub) callerActive(ctx context.Context, q blobQuery, c auth.Caller) bool {
	if c.OAuth != (auth.OAuthCredential{}) {
		return auth.OAuthCallerActive(ctx, q, c, h.Now())
	}
	var n int
	if c.CredentialID != "" {
		subject := c.PrincipalID
		if c.Kind == "peer" {
			subject = c.PeerID
		}
		if q.QueryRowContext(ctx, `SELECT count(*) FROM credentials WHERE cred_id=? AND kind=? AND subject_id=? AND revoked_at IS NULL`, c.CredentialID, c.Kind, subject).Scan(&n) != nil || n != 1 {
			return false
		}
	}
	if c.Kind == "peer" || c.PeerID != "" {
		if q.QueryRowContext(ctx, `SELECT count(*) FROM peers WHERE peer_id=? AND prn_id=? AND disabled=0`, c.PeerID, c.PrincipalID).Scan(&n) != nil || n != 1 {
			return false
		}
	}
	if c.Kind != "peer" && c.Kind != "principal" && c.Kind != "operator" {
		return false
	}
	if c.PrincipalID == "" {
		return false
	}
	return q.QueryRowContext(ctx, `SELECT count(*) FROM principals WHERE prn_id=?`, c.PrincipalID).Scan(&n) == nil && n == 1
}

type blobTransferCallerKey struct{}

type blobQuery interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func blobUploader(c auth.Caller) string {
	if c.PeerID != "" {
		return c.PeerID
	}
	return c.PrincipalID
}
func (h *Hub) CreateBlobUpload(ctx context.Context, c auth.Caller, idem string, in blobwire.UploadRequest) (blobwire.UploadGrant, error) {
	if !h.callerActive(ctx, h.DB, c) {
		return blobwire.UploadGrant{}, E(403, "forbidden", "not authorised")
	}
	if h.Cfg.BlobBackend != "r2" || h.Blobs.Transfers == nil {
		return blobwire.UploadGrant{}, E(409, "direct_upload_unavailable", "direct uploads are unavailable")
	}
	g, err := h.Blobs.Transfers.Reserve(context.WithValue(ctx, blobTransferCallerKey{}, c), blobUploader(c), idem, in)
	if err == nil && !h.callerActive(ctx, h.DB, c) {
		return blobwire.UploadGrant{}, E(403, "forbidden", "not authorised")
	}
	return g, h.blobTransferError(err)
}
func (h *Hub) CompleteBlobUpload(ctx context.Context, c auth.Caller, id string) (blobwire.Ref, error) {
	o, _, err := h.authorizeBlobObject(ctx, &c, "", "", id, true)
	if err != nil {
		return blobwire.Ref{}, err
	}
	if h.Blobs.Transfers == nil {
		return blobwire.Ref{}, E(503, "blob_unavailable", "blob unavailable")
	}
	ref, err := h.Blobs.Transfers.Complete(context.WithValue(ctx, blobTransferCallerKey{}, c), o.Uploader, id)
	if err != nil {
		return blobwire.Ref{}, h.blobTransferError(err)
	}
	if _, _, err = h.authorizeBlobObject(ctx, &c, "", o.SHA256, id, true); err != nil {
		return blobwire.Ref{}, err
	}
	return ref, nil
}

// authorizeBlobObject checks exact-incarnation ownership/history before any
// state error. Tokens grant only the named incarnation to an enabled recipient.
func (h *Hub) authorizeBlobObject(ctx context.Context, c *auth.Caller, token, sha, id string, ownerOnly bool) (blob.R2Object, string, error) {
	o, err := blob.ReadR2Object(ctx, h.DB, id)
	if err != nil {
		return blob.R2Object{}, "", E(403, "forbidden", "not authorised to read this blob")
	}
	if sha != "" && o.SHA256 != sha {
		return blob.R2Object{}, "", E(403, "forbidden", "not authorised to read this blob")
	}
	identity := ""
	allowed := false
	if !ownerOnly && token != "" {
		if peer, ok := h.Blobs.VerifyTokenV2(token, o.SHA256, id, h.Now()); ok {
			var n int
			if err = h.DB.QueryRowContext(ctx, `SELECT count(*) FROM peers WHERE peer_id=? AND disabled=0`, peer).Scan(&n); err != nil {
				return blob.R2Object{}, "", h.blobTransferError(err)
			}
			allowed = n == 1
			if allowed {
				identity = peer
			}
		}
	}
	if !allowed && c != nil && h.callerActive(ctx, h.DB, *c) {
		identity = blobUploader(*c)
		if c.Kind == "operator" {
			identity = c.PrincipalID
		}
		allowed = c.Kind == "operator" || o.Uploader == c.PeerID || o.Uploader == c.PrincipalID
		if !allowed && c.Kind == "principal" {
			var n int
			err = h.DB.QueryRowContext(ctx, `SELECT count(*) FROM peers WHERE peer_id=? AND prn_id=? AND disabled=0`, o.Uploader, c.PrincipalID).Scan(&n)
			if err != nil {
				return blob.R2Object{}, "", h.blobTransferError(err)
			}
			allowed = n > 0
		}
		if !allowed && !ownerOnly {
			allowed, err = h.r2BlobVisible(ctx, h.DB, c.PeerID, principalReadID(*c), o.SHA256, id)
			if err != nil {
				return blob.R2Object{}, "", h.blobTransferError(err)
			}
		}
	}
	if !allowed {
		return blob.R2Object{}, "", E(403, "forbidden", "not authorised to read this blob")
	}
	return o, identity, nil
}
func principalReadID(c auth.Caller) string {
	if c.Kind == "principal" {
		return c.PrincipalID
	}
	return ""
}
func (h *Hub) r2BlobVisible(ctx context.Context, q blobQuery, peer, prn, sha, id string) (bool, error) {
	var n int
	err := q.QueryRowContext(ctx, `SELECT count(*) FROM blob_object_refs br JOIN messages m ON m.msg_id=br.msg_id JOIN threads t ON t.thr_id=m.thr_id JOIN thread_participants tp ON tp.thr_id=m.thr_id JOIN peers p ON p.peer_id=tp.peer_id WHERE br.blob_id=?1 AND br.sha256=?2 AND p.disabled=0 AND ((?3<>'' AND p.peer_id=?3) OR (?4<>'' AND p.prn_id=?4)) AND `+visibleSQL, id, sha, peer, prn).Scan(&n)
	return n > 0, err
}
func (h *Hub) blobObjectReady(ctx context.Context, o blob.R2Object) error {
	// An unverified reservation proves no possession of its claimed hash.
	// Match Transfers.State/Complete: do not expose a global hash tombstone.
	if !o.SHA256Verified {
		if o.ExpiresAt != nil && !h.Now().Before(*o.ExpiresAt) {
			return h.blobTransferError(blob.ErrR2Expired)
		}
		return h.blobTransferError(blob.ErrR2Incomplete)
	}
	var n int
	if err := h.DB.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM blob_purge_tombstones WHERE sha256=?1) OR EXISTS(SELECT 1 FROM blobs WHERE sha256=?1 AND purged_at IS NOT NULL)`, o.SHA256).Scan(&n); err != nil {
		return h.blobTransferError(err)
	}
	if n != 0 || o.State == "purged" {
		return h.blobTransferError(blob.ErrR2Purged)
	}
	if o.State == "expired" || o.ExpiresAt != nil && !h.Now().Before(*o.ExpiresAt) {
		return h.blobTransferError(blob.ErrR2Expired)
	}
	if o.State != "complete" || !o.SHA256Verified {
		return h.blobTransferError(blob.ErrR2Incomplete)
	}
	return nil
}

// BlobDownload is the only URL mint path. Adapters never sign or meter grants.
// Each explicit call rechecks authority and state, including after signing waits.
func (h *Hub) BlobDownload(ctx context.Context, c *auth.Caller, token, sha, id string) (blobwire.DownloadGrant, error) {
	if id != "" && !blob.ValidSHA(sha) {
		return blobwire.DownloadGrant{}, E(403, "forbidden", "not authorised to read this blob")
	}
	if id == "" {
		f, m, err := h.ReadBlob(ctx, c, token, sha)
		if err != nil {
			return blobwire.DownloadGrant{}, h.BlobReadError(ctx, c, err)
		}
		f.Close()
		return blobwire.DownloadGrant{SHA256: sha, Size: m.Size, MediaType: m.MediaType, LocalPath: "/v1/blobs/" + sha}, nil
	}
	o, identity, err := h.authorizeBlobObject(ctx, c, token, sha, id, false)
	if err != nil {
		return blobwire.DownloadGrant{}, err
	}
	if err = h.blobObjectReady(ctx, o); err != nil {
		return blobwire.DownloadGrant{}, err
	}
	if retry := h.blobMints.take(identity, h.Now()); retry > 0 {
		if err = h.meterBlobMint(ctx, o, identity, "download_grant_rate_limited"); err != nil {
			return blobwire.DownloadGrant{}, err
		}
		e := E(429, "blob_url_rate_limited", "download grant rate limit exceeded")
		e.Details["retry_after_seconds"] = retry
		return blobwire.DownloadGrant{}, e
	}
	if h.BlobSignDownload == nil {
		return blobwire.DownloadGrant{}, E(503, "blob_unavailable", "blob unavailable")
	}
	request, signErr := h.BlobSignDownload(ctx, o)
	o, _, err = h.authorizeBlobObject(ctx, c, token, sha, id, false)
	if err != nil {
		return blobwire.DownloadGrant{}, err
	}
	if err = h.blobObjectReady(ctx, o); err != nil {
		return blobwire.DownloadGrant{}, err
	}
	if signErr != nil {
		return blobwire.DownloadGrant{}, h.blobTransferError(signErr)
	}
	deadline, err := time.Parse(time.RFC3339Nano, request.ExpiresAt)
	if err != nil || request.Method != "GET" || request.URL == "" || !h.Now().Before(deadline) || deadline.After(h.Now().Add(blob.R2DownloadMaxTTL)) || o.ExpiresAt != nil && deadline.After(*o.ExpiresAt) {
		return blobwire.DownloadGrant{}, h.blobTransferError(blob.ErrR2Unavailable)
	}
	if err = h.meterBlobMint(ctx, o, identity, "download_grant_issued"); err != nil {
		return blobwire.DownloadGrant{}, err
	}
	verified := o.SHA256Verified
	var expiry *string
	if o.ExpiresAt != nil {
		v := envelope.FormatTime(*o.ExpiresAt)
		expiry = &v
	}
	return blobwire.DownloadGrant{BlobID: id, SHA256: sha, SHA256Verified: &verified, Integrity: &blobwire.Integrity{Algorithm: "sha256", Value: sha, Verified: verified}, Size: o.Size, URL: request.URL, Method: request.Method, ExpiresAt: request.ExpiresAt, ObjectExpiresAt: expiry, MediaType: o.MediaType, RetentionClass: o.RetentionClass}, nil
}

// BlobReadError maps failed hash-only transfer lookups uniformly for authenticated
// callers while remote transfers remain configured. Successful local reads and
// the underlying ReadBlob authorization/error contract remain unchanged.
func (h *Hub) BlobReadError(ctx context.Context, c *auth.Caller, err error) error {
	var apiErr *APIError
	if h.Blobs.Transfers != nil && c != nil && errors.As(err, &apiErr) && (apiErr.Code == "forbidden" || apiErr.Code == "unknown_blob") && h.callerActive(ctx, h.DB, *c) {
		return E(422, "blob_id_required", "blob_id is required for remote reads")
	}
	return err
}

func (h *Hub) BlobStatus(ctx context.Context, c auth.Caller, sha, id string) (blobwire.State, error) {
	if id != "" && !blob.ValidSHA(sha) {
		return blobwire.State{}, E(403, "forbidden", "not authorised to read this blob")
	}
	if id == "" {
		f, _, err := h.ReadBlob(ctx, &c, "", sha)
		if err != nil {
			return blobwire.State{}, h.BlobReadError(ctx, &c, err)
		}
		f.Close()
		return blobwire.State{State: "available"}, nil
	}
	if _, _, err := h.authorizeBlobObject(ctx, &c, "", sha, id, false); err != nil {
		return blobwire.State{}, err
	}
	if h.Blobs.Transfers == nil {
		return blobwire.State{}, h.blobTransferError(blob.ErrR2Unavailable)
	}
	s, err := h.Blobs.Transfers.State(ctx, id)
	return s, h.blobTransferError(err)
}
func (h *Hub) meterBlobMint(ctx context.Context, o blob.R2Object, identity, kind string) error {
	now := h.Now()
	if h.Blobs.Transfers == nil {
		return h.blobTransferError(blob.ErrR2Unavailable)
	}
	event := blob.DownloadGrantEvent{EventID: ids.New(ids.Blo), BlobID: o.BlobID, CallerIdentity: identity, Kind: kind, At: now}
	inserted, err := h.Blobs.Transfers.RecordDownloadGrant(ctx, event)
	if err != nil {
		return h.blobTransferError(err)
	}
	if !inserted {
		return nil
	}

	if h.BlobGrantUsage != nil {
		status := 200
		if kind == "download_grant_rate_limited" {
			status = 429
		}
		h.BlobGrantUsage(BlobGrantEvent{EventID: event.EventID, Kind: kind, Status: status, BlobID: o.BlobID, CallerIdentity: identity, At: now})
	}
	return nil
}
func (h *Hub) blobTransferError(err error) error {
	if err == nil {
		return nil
	}
	code, status := "blob_unavailable", http.StatusServiceUnavailable
	for _, e := range []struct {
		err    error
		code   string
		status int
	}{{blob.ErrR2Expired, "blob_expired", 410}, {blob.ErrR2Purged, "blob_purged", 410}, {blob.ErrR2Incomplete, "upload_incomplete", 409}, {blob.ErrR2Idempotency, "idempotency_conflict", 409}, {blob.ErrR2GrantExpired, "upload_grant_expired", 410}, {blob.ErrR2StorageQuota, "blob_storage_quota_exceeded", 429}, {blob.ErrR2Busy, "upload_busy", 429}, {blob.ErrQuota, "upload_quota_exceeded", 429}, {blob.ErrTooLarge, "blob_too_large", 413}, {blob.ErrR2Metadata, "invalid_value", 422}, {blob.ErrNotFound, "forbidden", 403}, {blob.ErrR2Proof, "blob_proof_mismatch", 409}, {blob.ErrR2Preferences, "blob_preferences_unavailable", 503}} {
		if errors.Is(err, e.err) {
			code, status = e.code, e.status
			break
		}
	}
	result := E(status, code, code)
	if status == 429 {
		retry := 1
		if code == "upload_quota_exceeded" {
			retry = secondsToUTCMidnight(h.Now())
		}
		result.Details["retry_after_seconds"] = retry
	}
	return result
}
