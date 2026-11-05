// Package blobwire defines direct-transfer wire values without hub or database dependencies.
package blobwire

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
)

// Integrity records server-established checksum evidence, false until completion.
type Integrity struct {
	Algorithm string `json:"algorithm"`
	Value     string `json:"value"`
	Verified  bool   `json:"verified"`
}

// Ref is a stable object reference; BlobID distinguishes R2 from legacy disk.
type Ref struct {
	BlobID         string     `json:"blob_id,omitempty"`
	SHA256         string     `json:"sha256"`
	SHA256Verified *bool      `json:"sha256_verified,omitempty"`
	Integrity      *Integrity `json:"integrity,omitempty"`
	Size           int64      `json:"size"`
	RetentionClass string     `json:"retention_class,omitempty"`
	ExpiresAt      *string    `json:"expires_at"`
}

type UploadRequest struct {
	Size      int64  `json:"size"`
	SHA256    string `json:"sha256"`
	MediaType string `json:"media_type"`
}

// UnmarshalJSON rejects caller-supplied ownership, verification and transfer fields.
func (r *UploadRequest) UnmarshalJSON(data []byte) error {
	type plain UploadRequest
	var value plain
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(&value); err != nil {
		return errors.New("blobwire: invalid upload request")
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return errors.New("blobwire: invalid upload request")
	}
	*r = UploadRequest(value)
	return nil
}

// Request is an ephemeral capability, never stored in messages or logs.
type Request struct {
	Method    string            `json:"method"`
	URL       string            `json:"url"`
	Headers   map[string]string `json:"headers"`
	ExpiresAt string            `json:"expires_at"`
}

type UploadGrant struct {
	Ref
	Upload      Request `json:"upload"`
	CompleteURL string  `json:"complete_url"`
}

type DownloadGrant struct {
	BlobID          string     `json:"blob_id,omitempty"`
	SHA256          string     `json:"sha256"`
	SHA256Verified  *bool      `json:"sha256_verified,omitempty"`
	Integrity       *Integrity `json:"integrity,omitempty"`
	Size            int64      `json:"size"`
	URL             string     `json:"url,omitempty"`
	Method          string     `json:"method,omitempty"`
	LocalPath       string     `json:"local_path,omitempty"`
	MediaType       string     `json:"media_type"`
	ExpiresAt       string     `json:"expires_at,omitempty"`
	ObjectExpiresAt *string    `json:"object_expires_at"`
	RetentionClass  string     `json:"retention_class,omitempty"`
}

type State struct {
	State          string     `json:"state"`
	RetentionClass string     `json:"retention_class,omitempty"`
	ExpiresAt      *string    `json:"expires_at"`
	SHA256Verified *bool      `json:"sha256_verified,omitempty"`
	Integrity      *Integrity `json:"integrity,omitempty"`
}

// The private plain types prevent recursive marshaling. An explicit outer field
// overrides the alias field, allowing disk omission and R2's required null.
func (r Ref) MarshalJSON() ([]byte, error) {
	type plain Ref
	if r.BlobID != "" {
		if !recordedVerification(r.SHA256Verified, r.Integrity) {
			return nil, errRecordedVerification
		}
		return json.Marshal(plain(r))
	}
	return json.Marshal(struct {
		plain
		ExpiresAt *string `json:"expires_at,omitempty"`
	}{plain: plain(r)})
}

// Ref's embedded marshaler would swallow sibling fields without this override.
func (g UploadGrant) MarshalJSON() ([]byte, error) {
	type plainRef Ref
	value := struct {
		plainRef
		Upload      Request `json:"upload"`
		CompleteURL string  `json:"complete_url"`
	}{plainRef(g.Ref), g.Upload, g.CompleteURL}
	if g.BlobID != "" {
		if !recordedVerification(g.SHA256Verified, g.Integrity) {
			return nil, errRecordedVerification
		}
		return json.Marshal(value)
	}
	return json.Marshal(struct {
		plainRef
		Upload      Request `json:"upload"`
		CompleteURL string  `json:"complete_url"`
		ExpiresAt   *string `json:"expires_at,omitempty"`
	}{plainRef: plainRef(g.Ref), Upload: g.Upload, CompleteURL: g.CompleteURL})
}

func (g DownloadGrant) MarshalJSON() ([]byte, error) {
	type plain DownloadGrant
	if g.BlobID != "" || g.RetentionClass != "" {
		if g.BlobID != "" && !recordedVerification(g.SHA256Verified, g.Integrity) {
			return nil, errRecordedVerification
		}
		return json.Marshal(plain(g))
	}
	return json.Marshal(struct {
		plain
		ObjectExpiresAt *string `json:"object_expires_at,omitempty"`
	}{plain: plain(g)})
}

func (s State) MarshalJSON() ([]byte, error) {
	type plain State
	if s.RetentionClass != "" {
		if !recordedVerification(s.SHA256Verified, s.Integrity) {
			return nil, errRecordedVerification
		}
		return json.Marshal(plain(s))
	}
	return json.Marshal(struct {
		plain
		ExpiresAt *string `json:"expires_at,omitempty"`
	}{plain: plain(s)})
}

var errRecordedVerification = errors.New("blobwire: missing or inconsistent recorded verification")

func recordedVerification(verified *bool, integrity *Integrity) bool {
	return verified != nil && integrity != nil && integrity.Algorithm == "sha256" && *verified == integrity.Verified
}
