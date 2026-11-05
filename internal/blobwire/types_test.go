package blobwire

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestDownloadGrantHasDistinctDeadlines(t *testing.T) {
	deadline := "2026-11-06T00:00:00Z"
	b, err := json.Marshal(DownloadGrant{
		ExpiresAt:       "2026-11-05T00:10:00Z",
		ObjectExpiresAt: &deadline,
		RetentionClass:  "1d",
	})
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if got["expires_at"] != "2026-11-05T00:10:00Z" ||
		got["object_expires_at"] != "2026-11-06T00:00:00Z" {
		t.Fatalf("lost or shadowed expiry fields: %s", b)
	}
}

func TestR2WireDeadlinesAndLegacyOmission(t *testing.T) {
	verified := false
	integrity := &Integrity{Algorithm: "sha256", Value: strings.Repeat("a", 64), Verified: false}
	ref := Ref{BlobID: "blo_00000000000000000000000001", SHA256: integrity.Value, Size: 7, RetentionClass: "keep", SHA256Verified: &verified, Integrity: integrity}
	for _, tt := range []struct {
		name       string
		value      any
		field      string
		capability string
	}{
		{"ref", ref, "expires_at", ""},
		{"upload", UploadGrant{Ref: ref, Upload: Request{Method: "PUT", ExpiresAt: "2026-11-05T00:05:00Z"}, CompleteURL: "/complete"}, "expires_at", "upload"},
		{"download", DownloadGrant{BlobID: ref.BlobID, SHA256: ref.SHA256, Size: 7, RetentionClass: "keep", SHA256Verified: &verified, Integrity: integrity, ExpiresAt: "2026-11-05T00:10:00Z"}, "object_expires_at", "expires_at"},
		{"state", State{State: "available", RetentionClass: "keep", SHA256Verified: &verified, Integrity: integrity}, "expires_at", ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			b, err := json.Marshal(tt.value)
			if err != nil {
				t.Fatal("wire marshal failed")
			}
			var m map[string]any
			if json.Unmarshal(b, &m) != nil {
				t.Fatal("invalid wire JSON")
			}
			if v, ok := m[tt.field]; !ok || v != nil {
				t.Fatal("keep deadline must be explicit null")
			}
			if m["sha256_verified"] != false || m["integrity"] == nil {
				t.Fatal("pending verification omitted or claimed true")
			}
			if tt.capability == "upload" {
				u, ok := m["upload"].(map[string]any)
				if !ok || u["expires_at"] != "2026-11-05T00:05:00Z" || m["complete_url"] != "/complete" {
					t.Fatal("embedded marshaler lost grant fields")
				}
			}
			if tt.capability == "expires_at" && m["expires_at"] != "2026-11-05T00:10:00Z" {
				t.Fatal("GET capability lost")
			}
		})
	}
	for _, value := range []any{Ref{SHA256: ref.SHA256, Size: 7}, UploadGrant{Ref: Ref{SHA256: ref.SHA256, Size: 7}}, DownloadGrant{SHA256: ref.SHA256, LocalPath: "/tmp/file"}, State{State: "available"}} {
		b, err := json.Marshal(value)
		if err != nil {
			t.Fatal("legacy marshal failed")
		}
		var m map[string]any
		if json.Unmarshal(b, &m) != nil {
			t.Fatal("invalid JSON")
		}
		for _, field := range []string{"blob_id", "retention_class", "expires_at", "object_expires_at", "sha256_verified", "integrity"} {
			if _, ok := m[field]; ok {
				t.Errorf("legacy added %s", field)
			}
		}
	}
}

func TestR2UploadRequestRejectsUnknownAndRecordedFields(t *testing.T) {
	base := `{"size":7,"sha256":"` + strings.Repeat("a", 64) + `","media_type":"text/plain"`
	for _, extra := range []string{`,"content_md5":"fake"`, `,"sha256_verified":true`, `,"integrity":{}`, `,"url":"https://invalid.test"`, `,"uploader":"other"`} {
		var req UploadRequest
		if json.Unmarshal([]byte(base+extra+`}`), &req) == nil {
			t.Fatal("accepted unknown upload assertion")
		}
	}
	var req UploadRequest
	if json.Unmarshal([]byte(base+`}`), &req) != nil {
		t.Fatal("valid upload request rejected")
	}
}

func TestR2WireRequiresRecordedVerification(t *testing.T) {
	for _, value := range []any{
		Ref{BlobID: "blo_00000000000000000000000001", RetentionClass: "keep"},
		UploadGrant{Ref: Ref{BlobID: "blo_00000000000000000000000001", RetentionClass: "keep"}},
		DownloadGrant{BlobID: "blo_00000000000000000000000001", RetentionClass: "keep"},
		State{State: "available", RetentionClass: "keep"},
	} {
		if _, err := json.Marshal(value); err == nil {
			t.Fatal("R2 serialized absent verification as omitted/null")
		}
	}
}
