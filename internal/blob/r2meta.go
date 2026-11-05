package blob

import (
	"errors"
	"regexp"
	"time"

	"github.com/XertroV/vecnet/internal/ids"
)

// ErrR2Metadata reports invalid retention or key components without echoing inputs.
var ErrR2Metadata = errors.New("blob: invalid R2 metadata")

// ErrR2Expired refuses a capability with less than one second remaining.
var ErrR2Expired = errors.New("blob_expired")

const (
	R2UploadTTL      = 300 * time.Second
	R2DownloadMaxTTL = 600 * time.Second
)

var r2NamespacePattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

// R2Object is persisted metadata, never a presigned URL or a preference-store object.
// A nil ExpiresAt denotes keep; FirstAttachDeadline is set on keep completion.
type R2Object struct {
	BlobID, SHA256, Backend, ObjectKey, Uploader      string
	Size                                              int64
	MediaType                                         string
	IssuedAt, UploadDeadline                          time.Time
	RetentionClass, ExpiryPrefValue, ExpiryPrefSource string
	ExpiryPrefRevision                                uint64
	ExpiresAt, FirstAttachDeadline                    *time.Time
	ChecksumProfile, ExpectedChecksum, State          string
	SHA256Verified                                    bool
	VerifiedAlgorithm, VerifiedDigest                 string
	VerifiedSize                                      *int64
	ETag                                              string
	VerifiedAt, PurgedAt, DeletionConfirmedAt         *time.Time
	StorageChargeState                                string
}

// R2Retention snapshots a supported preference as a class and logical deadline.
func R2Retention(value string, issued time.Time) (string, *time.Time, error) {
	var duration time.Duration
	switch value {
	case "off":
		return "keep", nil, nil
	case "1h":
		duration = time.Hour
	case "6h":
		duration = 6 * time.Hour
	case "12h":
		duration = 12 * time.Hour
	case "1d":
		duration = 24 * time.Hour
	case "7d":
		duration = 7 * 24 * time.Hour
	case "30d":
		duration = 30 * 24 * time.Hour
	default:
		return "", nil, ErrR2Metadata
	}
	end := issued.UTC().Add(duration)
	return value, &end, nil
}

// R2ObjectKey isolates each grant beneath a class-first lifecycle prefix.
func R2ObjectKey(namespace, blobID, class string) (string, error) {
	if !r2NamespacePattern.MatchString(namespace) || !ids.Is(ids.Blo, blobID) {
		return "", ErrR2Metadata
	}
	prefix := "attachments/v1/keep/"
	if class != "keep" {
		if _, _, err := R2Retention(class, time.Time{}); err != nil || class == "off" {
			return "", ErrR2Metadata
		}
		prefix = "attachments/v1/exp/" + class + "/"
	}
	return prefix + namespace + "/" + blobID, nil
}

// R2DownloadTTL floors seconds so a capability cannot outlive a finite object.
func R2DownloadTTL(now time.Time, expiresAt *time.Time) (time.Duration, error) {
	ttl := R2DownloadMaxTTL
	if expiresAt != nil && expiresAt.Sub(now) < ttl {
		ttl = expiresAt.Sub(now)
	}
	if ttl < time.Second {
		return 0, ErrR2Expired
	}
	return ttl / time.Second * time.Second, nil
}
