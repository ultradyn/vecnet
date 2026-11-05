package hub

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"time"

	"github.com/XertroV/vecnet/internal/auth"
	"github.com/XertroV/vecnet/internal/blob"
	"github.com/XertroV/vecnet/internal/blobwire"
)

// R2Config is bootstrap-only. Credentials have no flags or JSON representation.
// Client is an injected transport for in-process tests; production uses HTTPS.
type R2Config struct {
	AccountID         string
	Bucket            string
	Endpoint          string
	ChecksumProfile   string
	StorageQuotaBytes int64
	AccessKeyID       string       `json:"-"`
	SecretAccessKey   string       `json:"-"`
	Client            *http.Client `json:"-"`
}

func (R2Config) String() string   { return "R2Config{redacted}" }
func (R2Config) GoString() string { return "R2Config{redacted}" }

var r2AccountID = regexp.MustCompile(`^[0-9a-f]{32}$`)

func ValidateBlobConfig(cfg Config) error {
	if cfg.BlobBackend != "" && cfg.BlobBackend != "disk" && cfg.BlobBackend != "r2" {
		return errors.New("invalid blob backend")
	}
	r := cfg.R2
	if cfg.BlobBackend != "r2" && r.AccountID == "" {
		return nil
	}
	endpoint := "https://" + r.AccountID + ".r2.cloudflarestorage.com"
	if !r2AccountID.MatchString(r.AccountID) || r.Bucket == "" || r.Endpoint != "" && r.Endpoint != endpoint || r.ChecksumProfile != "sha256" || r.StorageQuotaBytes <= 0 || r.AccessKeyID == "" || r.SecretAccessKey == "" {
		return errors.New("invalid R2 configuration")
	}
	if _, err := blob.PresignGET(endpoint, r.Bucket, "validate", r.AccessKeyID, r.SecretAccessKey, time.Unix(1, 0), time.Second); err != nil {
		return errors.New("invalid R2 configuration")
	}
	return nil
}
func (h *Hub) initR2() error {
	if h.Cfg.BlobBackend != "r2" && h.Cfg.R2.AccountID == "" {
		var n int
		if err := h.DB.QueryRow(`SELECT count(*) FROM blob_objects WHERE deletion_confirmed_at IS NULL`).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return errors.New("R2 credentials required for retained remote objects")
		}
		return nil
	}
	r := h.Cfg.R2
	if r.Endpoint == "" {
		r.Endpoint = "https://" + r.AccountID + ".r2.cloudflarestorage.com"
	}
	var ns string
	err := h.DB.Tx(context.Background(), func(tx *sql.Tx) error {
		err := tx.QueryRow(`SELECT value FROM meta WHERE key='r2_namespace'`).Scan(&ns)
		if errors.Is(err, sql.ErrNoRows) {
			var random [16]byte
			if _, err = rand.Read(random[:]); err != nil {
				return err
			}
			ns = hex.EncodeToString(random[:])
			_, err = tx.Exec(`INSERT INTO meta(key,value) VALUES('r2_namespace',?)`, ns)
		}
		return err
	})
	if err != nil {
		return errors.New("R2 metadata initialization failed")
	}
	var transfers *blob.Transfers
	client := r.Client
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	objects, err := blob.NewR2Objects(blob.R2ObjectsConfig{Endpoint: r.Endpoint, Bucket: r.Bucket, Namespace: ns, AccessID: r.AccessKeyID, Secret: r.SecretAccessKey, Client: client, Now: h.Now, Lookup: func(ctx context.Context, key string) (blob.R2Object, error) { return transfers.Lookup(ctx, key) }})
	if err != nil {
		return errors.New("invalid R2 configuration")
	}
	transfers, err = blob.NewTransfers(blob.TransfersConfig{DB: h.DB, Now: h.Now, Namespace: ns, Preferences: h.Preferences, Objects: objects, StorageQuota: r.StorageQuotaBytes, CheckEligible: func(ctx context.Context, tx *sql.Tx, uploader string) error {
		if c, ok := ctx.Value(blobTransferCallerKey{}).(auth.Caller); ok && !h.callerActive(ctx, tx, c) {
			return blob.ErrNotFound
		}
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM peers WHERE peer_id=?1 AND disabled=0)+(SELECT count(*) FROM principals WHERE prn_id=?1)+(SELECT count(*) FROM sources WHERE src_id=?1 AND disabled=0)`, uploader).Scan(&n); err != nil || n == 0 {
			return blob.ErrNotFound
		}
		return nil
	}, SignPUT: func(ctx context.Context, o blob.R2Object) (blobwire.Request, error) {
		digest, err := hex.DecodeString(o.SHA256)
		if err != nil {
			return blobwire.Request{}, blob.ErrR2Signing
		}
		checksum := base64.StdEncoding.EncodeToString(digest)
		headers := http.Header{"Content-Length": {fmt.Sprint(o.Size)}, "Content-Type": {"application/octet-stream"}, "Content-Disposition": {"attachment"}, "Cache-Control": {"private, no-store"}, "If-None-Match": {"*"}, "X-Amz-Checksum-Sha256": {checksum}, "X-Amz-Meta-Vecnet-Checksum-Profile": {"sha256"}, "X-Amz-Meta-Vecnet-Checksum": {checksum}}
		signed, err := blob.PresignPUT(r.Endpoint, r.Bucket, o.ObjectKey, r.AccessKeyID, r.SecretAccessKey, o.IssuedAt, blob.R2UploadTTL, headers)
		if err != nil {
			return blobwire.Request{}, blob.ErrR2Signing
		}
		values := map[string]string{}
		for key := range headers {
			values[key] = headers.Get(key)
		}
		return blobwire.Request{Method: "PUT", URL: signed, Headers: values, ExpiresAt: o.UploadDeadline.Format(time.RFC3339Nano)}, nil
	}})
	if err != nil {
		return errors.New("R2 transfer initialization failed")
	}
	h.Blobs.Transfers = transfers
	h.BlobSignDownload = func(ctx context.Context, o blob.R2Object) (blobwire.Request, error) {
		return objects.Download(ctx, o.ObjectKey, h.Now(), o.ExpiresAt)
	}
	return nil
}
