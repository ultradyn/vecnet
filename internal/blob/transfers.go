package blob

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"mime"
	"net/http"
	"strconv"
	"time"

	"github.com/XertroV/vecnet/internal/blobwire"
	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/ids"
	"github.com/XertroV/vecnet/internal/prefs"
	"github.com/XertroV/vecnet/internal/store"
)

var (
	ErrR2StorageQuota = errors.New("blob_storage_quota_exceeded")
	ErrR2Idempotency  = errors.New("idempotency_conflict")
	ErrR2GrantExpired = errors.New("upload_grant_expired")
	ErrR2Busy         = errors.New("upload_busy")
	ErrR2Preferences  = errors.New("blob_preferences_unavailable")
	ErrR2Unavailable  = errors.New("blob_unavailable")
)

// TransferPreferences is the merged preference owner's transaction reader.
// There is no hub import or second preference store. The returned contract is
// the dependency-free prefs value type; adapters are installed by hub wiring.
type TransferPreferences interface {
	LoadSnapshot(context.Context, *sql.Tx) (prefs.Snapshot, error)
}
type TransferObjects interface {
	Head(context.Context, string) (ObjectProof, error)
	Delete(context.Context, string) error
}

// TransfersConfig injects trusted dependencies. SignPUT must sign the persisted
// object's original issue time and deadline, even on retries. Checkpoint is an
// optional fault-injection seam, never a logger. NewTransfers installs no hub
// routes, workers or preference mutation adapters.
type TransfersConfig struct {
	DB                                *store.DB
	Now                               func() time.Time
	Namespace                         string
	Preferences                       TransferPreferences
	Objects                           TransferObjects
	SignPUT                           func(context.Context, R2Object) (blobwire.Request, error)
	MaxSize, DailyQuota, StorageQuota int64
	CheckEligible                     func(context.Context, *sql.Tx, string) error
	Checkpoint                        func(string) error
	ObserveOperation                  func(TransferOperation)
}
type Transfers struct {
	cfg   TransfersConfig
	heads chan struct{}
}

func NewTransfers(cfg TransfersConfig) (*Transfers, error) {
	if cfg.DB == nil || cfg.Now == nil || cfg.Preferences == nil || cfg.Objects == nil || cfg.SignPUT == nil || cfg.StorageQuota <= 0 {
		return nil, ErrR2Metadata
	}
	if cfg.MaxSize == 0 {
		cfg.MaxSize = DefaultMaxSize
	}
	if cfg.DailyQuota == 0 {
		cfg.DailyQuota = DefaultDailyQuota
	}
	if cfg.MaxSize < 1 || cfg.MaxSize > DefaultMaxSize || cfg.DailyQuota < 1 {
		return nil, ErrR2Metadata
	}
	err := cfg.DB.Tx(context.Background(), func(tx *sql.Tx) error {
		var ns string
		err := tx.QueryRow(`SELECT value FROM meta WHERE key='r2_namespace'`).Scan(&ns)
		if errors.Is(err, sql.ErrNoRows) {
			ns = cfg.Namespace
			if ns == "" {
				var b [16]byte
				if _, err := rand.Read(b[:]); err != nil {
					return ErrR2Metadata
				}
				ns = hex.EncodeToString(b[:])
			}
			if !r2NamespacePattern.MatchString(ns) {
				return ErrR2Metadata
			}
			if _, err = tx.Exec(`INSERT INTO meta(key,value) VALUES('r2_namespace',?)`, ns); err != nil {
				return err
			}
		} else if err != nil {
			return err
		}
		if !r2NamespacePattern.MatchString(ns) {
			return ErrR2Metadata
		}
		if cfg.Namespace != "" && cfg.Namespace != ns {
			return ErrR2Metadata
		}
		cfg.Namespace = ns
		// Rebuild only storage totals, from durable per-object charge states. Daily
		// usage remains nonrefundable and is never reconstructed from live objects.
		_, err = tx.Exec(`UPDATE blob_r2_storage SET storage_reserved_bytes=(SELECT coalesce(sum(size),0) FROM blob_objects WHERE storage_charge_state='reserved'),retained_bytes=(SELECT coalesce(sum(size),0) FROM blob_objects WHERE storage_charge_state='retained') WHERE singleton=1`)
		return err
	})
	if err != nil {
		return nil, err
	}
	return &Transfers{cfg: cfg, heads: make(chan struct{}, 2)}, nil
}
func r2Time(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }
func (s *Transfers) checkpoint(point string) error {
	if s.cfg.Checkpoint != nil {
		return s.cfg.Checkpoint(point)
	}
	return nil
}
func (s *Transfers) eligible(ctx context.Context, tx *sql.Tx, uploader string) error {
	if s.cfg.CheckEligible != nil {
		if err := s.cfg.CheckEligible(ctx, tx, uploader); err != nil {
			return ErrNotFound
		}
		return nil
	}
	// Baseline enrollment eligibility is checked again in the completion CAS.
	// Principal-specific credential/scope checks may strengthen this via injection.
	var n int
	err := tx.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM peers WHERE peer_id=?1 AND disabled=0)+(SELECT count(*) FROM principals WHERE prn_id=?1)+(SELECT count(*) FROM sources WHERE src_id=?1 AND disabled=0)`, uploader).Scan(&n)
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}
func r2Tombstone(ctx context.Context, q querier, sha string) (bool, error) {
	var n int
	err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM blob_purge_tombstones WHERE sha256=?1) OR EXISTS(SELECT 1 FROM blobs WHERE sha256=?1 AND purged_at IS NOT NULL)`, sha).Scan(&n)
	return n != 0, err
}

func (s *Transfers) Reserve(ctx context.Context, uploader, idem string, in blobwire.UploadRequest) (blobwire.UploadGrant, error) {
	if uploader == "" || idem == "" || len(idem) > 256 || !r2SafeText(idem) || in.Size < 1 || !ValidSHA(in.SHA256) || len(in.MediaType) == 0 || len(in.MediaType) > 128 || !r2SafeText(in.MediaType) {
		return blobwire.UploadGrant{}, ErrR2Metadata
	}
	if in.Size > s.cfg.MaxSize {
		return blobwire.UploadGrant{}, ErrTooLarge
	}
	if _, _, err := mime.ParseMediaType(in.MediaType); err != nil {
		return blobwire.UploadGrant{}, ErrR2Metadata
	}
	encoded, _ := json.Marshal(in)
	digest := sha256.Sum256(encoded)
	bodySHA := hex.EncodeToString(digest[:])
	var grant blobwire.UploadGrant
	err := s.cfg.DB.Tx(ctx, func(tx *sql.Tx) error {
		if err := s.eligible(ctx, tx, uploader); err != nil {
			return err
		}
		var id, stored string
		err := tx.QueryRowContext(ctx, `SELECT blob_id,request_digest FROM blob_upload_requests WHERE uploader=? AND idempotency_key=?`, uploader, idem).Scan(&id, &stored)
		if err == nil {
			if stored != bodySHA {
				return ErrR2Idempotency
			}
			o, err := r2ReadObject(ctx, tx, `blob_id=?`, id)
			if err != nil {
				return err
			}
			if !s.cfg.Now().Before(o.UploadDeadline) {
				return ErrR2GrantExpired
			}
			if err := s.completionCheck(ctx, tx, o, false); err != nil {
				return err
			}
			grant, err = s.grant(ctx, o)
			return err
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		snapshot, err := s.cfg.Preferences.LoadSnapshot(ctx, tx)
		if err != nil {
			return ErrR2Preferences
		}
		effective, err := snapshot.Get("blobs.expiry")
		if err != nil {
			return ErrR2Preferences
		}
		def, err := prefs.Lookup("blobs.expiry")
		if err != nil || prefs.Validate(def, effective.Value) != nil {
			return ErrR2Preferences
		}
		value, ok := effective.Value.Enum()
		if !ok {
			return ErrR2Preferences
		}
		now := s.cfg.Now().UTC()
		class, expiry, err := R2Retention(value, now)
		if err != nil {
			return ErrR2Preferences
		}
		// A claimed hash is not proof of possession. Issue the same isolated,
		// quota-charged grant for purged and unknown hashes; completion enforces
		// the tombstone only after the checksum evidence has been checked.
		// Every unverified, still-usable presign holds its slot through its
		// original deadline, including purged/rejected grants. Terminal state
		// cannot reveal a concealed purge through subsequent admission.
		var active int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM blob_objects WHERE uploader=? AND sha256_verified=0 AND julianday(upload_deadline)>julianday(?)`, uploader, r2Time(now)).Scan(&active); err != nil {
			return err
		}
		if active >= 20 {
			return ErrR2Busy
		}
		day := quotaDay(now)
		if _, err := tx.ExecContext(ctx, `INSERT INTO blob_r2_quota(uploader,day) VALUES(?,?) ON CONFLICT DO NOTHING`, uploader, day); err != nil {
			return err
		}
		var used int64
		if err := tx.QueryRowContext(ctx, `SELECT reserved_bytes+accepted_bytes+abandoned_bytes+rejected_bytes+coalesce((SELECT bytes FROM blob_quota WHERE peer_id=?1 AND day=?2),0) FROM blob_r2_quota WHERE uploader=?1 AND day=?2`, uploader, day).Scan(&used); err != nil {
			return err
		}
		if in.Size > s.cfg.DailyQuota-used {
			return ErrQuota
		}
		var reserved, retained int64
		if err := tx.QueryRowContext(ctx, `SELECT storage_reserved_bytes,retained_bytes FROM blob_r2_storage WHERE singleton=1`).Scan(&reserved, &retained); err != nil {
			return err
		}
		if reserved > s.cfg.StorageQuota || retained > s.cfg.StorageQuota-reserved || in.Size > s.cfg.StorageQuota-reserved-retained {
			return ErrR2StorageQuota
		}
		id = ids.New(ids.Blo)
		key, err := R2ObjectKey(s.cfg.Namespace, id, class)
		if err != nil {
			return err
		}
		o := R2Object{BlobID: id, SHA256: in.SHA256, Backend: "r2", ObjectKey: key, Uploader: uploader, Size: in.Size, MediaType: in.MediaType, IssuedAt: now, UploadDeadline: now.Add(R2UploadTTL), RetentionClass: class, ExpiryPrefValue: value, ExpiryPrefSource: string(effective.Source), ExpiryPrefRevision: effective.Revision, ExpiresAt: expiry, ChecksumProfile: "sha256", ExpectedChecksum: in.SHA256, State: "pending", StorageChargeState: "reserved"}
		grant, err = s.grant(ctx, o)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO blob_objects(blob_id,sha256,object_key,uploader,size,media_type,issued_at,upload_deadline,retention_class,expiry_pref_value,expiry_pref_source,expiry_pref_revision,expires_at,checksum_profile,expected_checksum,state) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,'sha256',?,'pending')`, id, in.SHA256, key, uploader, in.Size, in.MediaType, r2Time(now), r2Time(o.UploadDeadline), class, value, o.ExpiryPrefSource, o.ExpiryPrefRevision, r2NullableTime(expiry), in.SHA256)
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO blob_upload_requests(uploader,idempotency_key,request_digest,blob_id,issue_day) VALUES(?,?,?,?,?)`, uploader, idem, bodySHA, id, day); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE blob_r2_quota SET reserved_bytes=reserved_bytes+? WHERE uploader=? AND day=?`, in.Size, uploader, day); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE blob_r2_storage SET storage_reserved_bytes=storage_reserved_bytes+? WHERE singleton=1`, in.Size); err != nil {
			return err
		}
		return r2Usage(ctx, tx, o, "reserve", now)
	})
	if err != nil {
		return blobwire.UploadGrant{}, err
	}
	return grant, nil
}
func r2NullableTime(t *time.Time) any {
	if t == nil {
		return nil
	}
	return r2Time(*t)
}
func r2Ref(o R2Object) blobwire.Ref {
	verified := o.SHA256Verified
	var exp *string
	if o.ExpiresAt != nil {
		v := envelope.FormatTime(*o.ExpiresAt)
		exp = &v
	}
	return blobwire.Ref{BlobID: o.BlobID, SHA256: o.SHA256, SHA256Verified: &verified, Integrity: &blobwire.Integrity{Algorithm: "sha256", Value: o.SHA256, Verified: verified}, Size: o.Size, RetentionClass: o.RetentionClass, ExpiresAt: exp}
}
func (s *Transfers) grant(ctx context.Context, o R2Object) (blobwire.UploadGrant, error) {
	request, err := s.cfg.SignPUT(ctx, o)
	if err != nil {
		return blobwire.UploadGrant{}, ErrR2Signing
	}
	end, err := time.Parse(time.RFC3339Nano, request.ExpiresAt)
	if err != nil || !end.Equal(o.UploadDeadline) || request.Method != "PUT" || request.URL == "" {
		return blobwire.UploadGrant{}, ErrR2Signing
	}
	request.ExpiresAt = envelope.FormatTime(end)
	return blobwire.UploadGrant{Ref: r2Ref(o), Upload: request, CompleteURL: "/v1/blobs/uploads/" + o.BlobID + "/complete"}, nil
}

const r2ObjectColumns = `blob_id,sha256,backend,object_key,uploader,size,media_type,issued_at,upload_deadline,retention_class,expiry_pref_value,expiry_pref_source,expiry_pref_revision,expires_at,first_attach_deadline,checksum_profile,expected_checksum,state,sha256_verified,verified_algorithm,verified_digest,verified_size,etag,verified_at,purged_at,deletion_confirmed_at,storage_charge_state`

func r2ReadObject(ctx context.Context, q querier, where string, args ...any) (R2Object, error) {
	var o R2Object
	var issued, deadline string
	var exp, first, algorithm, digest, etag, verified, purged, deleted sql.NullString
	var size sql.NullInt64
	err := q.QueryRowContext(ctx, `SELECT `+r2ObjectColumns+` FROM blob_objects WHERE `+where, args...).Scan(&o.BlobID, &o.SHA256, &o.Backend, &o.ObjectKey, &o.Uploader, &o.Size, &o.MediaType, &issued, &deadline, &o.RetentionClass, &o.ExpiryPrefValue, &o.ExpiryPrefSource, &o.ExpiryPrefRevision, &exp, &first, &o.ChecksumProfile, &o.ExpectedChecksum, &o.State, &o.SHA256Verified, &algorithm, &digest, &size, &etag, &verified, &purged, &deleted, &o.StorageChargeState)
	if errors.Is(err, sql.ErrNoRows) {
		return o, ErrNotFound
	}
	if err != nil {
		return o, err
	}
	o.IssuedAt, err = time.Parse(time.RFC3339Nano, issued)
	if err != nil {
		return o, ErrR2Metadata
	}
	o.UploadDeadline, err = time.Parse(time.RFC3339Nano, deadline)
	if err != nil {
		return o, ErrR2Metadata
	}
	for _, v := range []struct {
		raw sql.NullString
		out **time.Time
	}{{exp, &o.ExpiresAt}, {first, &o.FirstAttachDeadline}, {verified, &o.VerifiedAt}, {purged, &o.PurgedAt}, {deleted, &o.DeletionConfirmedAt}} {
		if v.raw.Valid {
			t, err := time.Parse(time.RFC3339Nano, v.raw.String)
			if err != nil {
				return o, ErrR2Metadata
			}
			*v.out = &t
		}
	}
	o.VerifiedAlgorithm, o.VerifiedDigest, o.ETag = algorithm.String, digest.String, etag.String
	if size.Valid {
		o.VerifiedSize = &size.Int64
	}
	return o, nil
}

// Lookup supplies the production metadata adapter's persisted-key reader. It
// contains no capability or bytes and reflects hash-level purge tombstones.
func (s *Transfers) Lookup(ctx context.Context, key string) (R2Object, error) {
	o, err := r2ReadObject(ctx, s.cfg.DB, `object_key=?`, key)
	if err != nil {
		return o, err
	}
	p, err := r2Tombstone(ctx, s.cfg.DB, o.SHA256)
	if err != nil {
		return o, err
	}
	if p {
		o.State = "purged"
	}
	return o, nil
}
func (s *Transfers) Complete(ctx context.Context, uploader, blobID string) (blobwire.Ref, error) {
	var o R2Object
	// Authorization precedes all state checks and is repeated under the final CAS.
	err := s.cfg.DB.Tx(ctx, func(tx *sql.Tx) error {
		var err error
		o, err = r2ReadObject(ctx, tx, `blob_id=? AND uploader=?`, blobID, uploader)
		if err != nil {
			return err
		}
		if err = s.eligible(ctx, tx, uploader); err != nil {
			return err
		}
		return s.completionCheck(ctx, tx, o, false)
	})
	if err != nil {
		return blobwire.Ref{}, err
	}
	if o.State == "complete" {
		return r2Ref(o), nil
	}
	select {
	case s.heads <- struct{}{}:
		defer func() { <-s.heads }()
	default:
		return blobwire.Ref{}, ErrR2Busy
	}
	if err = s.checkpoint("after_upload"); err != nil {
		return blobwire.Ref{}, err
	}
	proof, headErr := s.ProbeObject(ctx, o)
	if headErr != nil {
		headErr = r2TransferError(headErr)
	}
	if headErr == nil {
		// Revalidate evidence at the state-machine boundary, including adapters
		// supplied by later wiring. A boolean alone is never completion evidence.
		h := proof.Metadata.Clone()
		if h == nil {
			h = make(http.Header)
		}
		h.Set("Content-Length", strconv.FormatInt(proof.Length, 10))
		h.Set("ETag", proof.ETag)
		if proof.ProviderChecksum != "" {
			h.Set("X-Amz-Checksum-Sha256", proof.ProviderChecksum)
		}
		if !proof.SHA256Verified || proof.ChecksumProfile != "sha256" {
			headErr = ErrR2Proof
		} else {
			_, headErr = r2CheckProof(&http.Response{Header: h}, o)
		}
	}
	if headErr == nil {
		if err = s.checkpoint("after_head"); err != nil {
			return blobwire.Ref{}, err
		}
	}
	var result blobwire.Ref
	var outcome error
	err = s.cfg.DB.Tx(ctx, func(tx *sql.Tx) error {
		current, err := r2ReadObject(ctx, tx, `blob_id=? AND uploader=?`, blobID, uploader)
		if err != nil {
			return err
		}
		if err = s.eligible(ctx, tx, uploader); err != nil {
			return err
		}
		// The production HEAD adapter can report purge only after matching
		// immutable checksum evidence (or for a previously verified object).
		proofChecked := headErr == nil || errors.Is(headErr, ErrR2Purged)
		if err = s.completionCheck(ctx, tx, current, proofChecked); err != nil {
			return err
		}
		if current.State == "complete" {
			result = r2Ref(current)
			return nil
		}
		if headErr != nil {
			outcome = headErr
			if errors.Is(headErr, ErrR2Proof) && current.State == "pending" {
				return r2Terminal(ctx, tx, current, "rejected", s.cfg.Now())
			}
			return nil
		}
		now := s.cfg.Now()
		var first any
		if current.RetentionClass == "keep" {
			first = r2Time(now.Add(UnreferencedGrace))
		}
		res, err := tx.ExecContext(ctx, `UPDATE blob_objects SET state='complete',sha256_verified=1,verified_algorithm='sha256',verified_digest=sha256,verified_size=size,etag=?,verified_at=?,first_attach_deadline=?,storage_charge_state='retained' WHERE blob_id=? AND state='pending'`, proof.ETag, r2Time(now), first, blobID)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil || n != 1 {
			return ErrR2Incomplete
		}
		if _, err = tx.ExecContext(ctx, `UPDATE blob_r2_quota SET reserved_bytes=reserved_bytes-?,accepted_bytes=accepted_bytes+? WHERE uploader=? AND day=?`, current.Size, current.Size, uploader, quotaDay(current.IssuedAt)); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE blob_r2_storage SET storage_reserved_bytes=storage_reserved_bytes-?,retained_bytes=retained_bytes+? WHERE singleton=1`, current.Size, current.Size); err != nil {
			return err
		}
		current.State = "complete"
		current.SHA256Verified = true
		result = r2Ref(current)
		return r2Usage(ctx, tx, current, "verified", now)
	})
	if err != nil {
		return blobwire.Ref{}, err
	}
	if outcome != nil {
		return blobwire.Ref{}, outcome
	}
	if err = s.checkpoint("after_commit"); err != nil {
		return blobwire.Ref{}, err
	}
	return result, nil
}
func (s *Transfers) completable(ctx context.Context, tx *sql.Tx, o R2Object) error {
	return s.completionCheck(ctx, tx, o, true)
}

func (s *Transfers) completionCheck(ctx context.Context, tx *sql.Tx, o R2Object, proofChecked bool) error {
	if !o.SHA256Verified && !proofChecked {
		if err := r2CompletionAvailable(o, s.cfg.Now()); err != nil {
			return err
		}
		if !s.cfg.Now().Before(o.UploadDeadline) {
			return ErrR2GrantExpired
		}
		return nil
	}
	p, err := r2Tombstone(ctx, tx, o.SHA256)
	if err != nil {
		return err
	}
	if p {
		return ErrR2Purged
	}
	if err = r2ObjectAvailable(o, s.cfg.Now()); err != nil {
		return err
	}
	if o.State == "pending" && !s.cfg.Now().Before(o.UploadDeadline) {
		return ErrR2GrantExpired
	}
	return nil
}

// State projects metadata for an already-authorized hub caller. Its signature
// intentionally carries no credential; adapters must authorize before calling.
// An unverified grant conveys no possession of its claimed hash, so it cannot
// expose a hash tombstone through this projection either.
func (s *Transfers) State(ctx context.Context, blobID string) (blobwire.State, error) {
	return ReadR2State(ctx, s.cfg.DB, blobID, s.cfg.Now())
}

// ReadR2State projects persisted trust and logical deadlines without provider
// traffic. It also works after rollback when no transfer signer is installed.
func ReadR2State(ctx context.Context, q querier, blobID string, now time.Time) (blobwire.State, error) {
	o, err := r2ReadObject(ctx, q, `blob_id=?`, blobID)
	if err != nil {
		return blobwire.State{}, err
	}
	var p bool
	if o.SHA256Verified {
		p, err = r2Tombstone(ctx, q, o.SHA256)
		if err != nil {
			return blobwire.State{}, err
		}
	}
	state := "unavailable"
	switch {
	case o.SHA256Verified && (p || o.State == "purged"):
		state = "purged"
	case o.State == "expired" || o.ExpiresAt != nil && !now.Before(*o.ExpiresAt):
		state = "expired"
	case o.State == "complete":
		state = "available"
	}
	ref := r2Ref(o)
	return blobwire.State{State: state, RetentionClass: ref.RetentionClass, ExpiresAt: ref.ExpiresAt, SHA256Verified: ref.SHA256Verified, Integrity: ref.Integrity}, nil
}

// AddReference is called only after hub authorization and reference-field
// validation. Its transaction must also insert the Message. GC and attach use
// the same writer lock: a scheduled deletion can never be cancelled by attach.
func (s *Transfers) AddReference(ctx context.Context, tx *sql.Tx, blobID, sha, msgID string) error {
	if tx == nil {
		return ErrR2Metadata
	}
	o, err := r2ReadObject(ctx, tx, `blob_id=? AND sha256=?`, blobID, sha)
	if err != nil {
		return err
	}
	if !o.SHA256Verified {
		return ErrR2Incomplete
	}
	if err = s.completable(ctx, tx, o); err != nil {
		return err
	}
	if o.State != "complete" {
		return ErrR2Incomplete
	}
	var deleting int
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM blob_delete_jobs WHERE blob_id=?)`, blobID).Scan(&deleting); err != nil {
		return err
	}
	if deleting != 0 {
		return ErrR2Incomplete
	}
	var refs int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM blob_object_refs WHERE blob_id=?`, blobID).Scan(&refs); err != nil {
		return err
	}
	if o.RetentionClass == "keep" && o.FirstAttachDeadline != nil && refs == 0 && !s.cfg.Now().Before(*o.FirstAttachDeadline) {
		return ErrR2Expired
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO blob_object_refs(msg_id,blob_id,sha256) VALUES(?,?,?) ON CONFLICT DO NOTHING`, msgID, blobID, sha); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE blob_objects SET first_attach_deadline=NULL WHERE blob_id=?`, blobID)
	return err
}

// No untrusted adapter/transport error text reaches the hub API. Preserve only
// fixed semantic errors and the metadata adapter's finite provider reason set.
func r2TransferError(err error) error {
	for _, safe := range []error{ErrR2Incomplete, ErrR2Purged, ErrR2Proof, ErrR2Expired, context.Canceled, context.DeadlineExceeded} {
		if errors.Is(err, safe) {
			return safe
		}
	}
	var provider *R2ProviderError
	if errors.As(err, &provider) {
		switch provider.Code {
		case "provider_error", "redirect_refused", "access_denied", "not_found", "unavailable", "request_rejected", "transport_error", "response_too_large":
			return &R2ProviderError{Code: provider.Code, Status: provider.Status, Retryable: provider.Retryable}
		}
	}
	return ErrR2Unavailable
}

// ReadR2Object returns metadata for trusted hub authorization. The caller must
// authorize before exposing any field or state error to an external caller.
func ReadR2Object(ctx context.Context, q querier, id string) (R2Object, error) {
	return r2ReadObject(ctx, q, "blob_id=?", id)
}
