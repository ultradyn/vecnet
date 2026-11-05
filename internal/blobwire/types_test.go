package blobwire

import (
	"encoding/json"
	"reflect"
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
}

// TestLegacyWireShapesPreserved: legacy disk values serialize to exactly their
// pre-R2 field sets, with every stable field preserved and no R2 field added.
func TestLegacyWireShapesPreserved(t *testing.T) {
	sha := strings.Repeat("a", 64)
	for _, tt := range []struct {
		name  string
		value any
		want  string
	}{
		{"ref", Ref{SHA256: sha, Size: 7}, `{"sha256":"` + sha + `","size":7}`},
		{"upload", UploadGrant{
			Ref:         Ref{SHA256: sha, Size: 7},
			Upload:      Request{Method: "PUT", URL: "/v1/blobs", Headers: map[string]string{"Content-Type": "text/plain"}, ExpiresAt: "2026-11-05T00:05:00.000Z"},
			CompleteURL: "/v1/blobs/" + sha + "/complete",
		}, `{"sha256":"` + sha + `","size":7,"upload":{"method":"PUT","url":"/v1/blobs","headers":{"Content-Type":"text/plain"},"expires_at":"2026-11-05T00:05:00.000Z"},"complete_url":"/v1/blobs/` + sha + `/complete"}`},
		{"download_url", DownloadGrant{SHA256: sha, Size: 7, URL: "/v1/blobs/" + sha, Method: "GET", MediaType: "text/plain", ExpiresAt: "2026-11-05T00:10:00.000Z"},
			`{"sha256":"` + sha + `","size":7,"url":"/v1/blobs/` + sha + `","method":"GET","media_type":"text/plain","expires_at":"2026-11-05T00:10:00.000Z"}`},
		{"download_local", DownloadGrant{SHA256: sha, Size: 7, LocalPath: "/tmp/file", MediaType: "text/plain"},
			`{"sha256":"` + sha + `","size":7,"local_path":"/tmp/file","media_type":"text/plain"}`},
		{"state", State{State: "available"}, `{"state":"available"}`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			b, err := json.Marshal(tt.value)
			if err != nil {
				t.Fatal("legacy marshal failed")
			}
			var got, want map[string]any
			if json.Unmarshal(b, &got) != nil || json.Unmarshal([]byte(tt.want), &want) != nil {
				t.Fatal("invalid JSON")
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("legacy wire = %s, want %s", b, tt.want)
			}
		})
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
	want := UploadRequest{Size: 7, SHA256: strings.Repeat("a", 64), MediaType: "text/plain"}
	if req != want {
		t.Fatalf("decoded upload request = %+v, want %+v", req, want)
	}
	// Decoding replaces a populated receiver; omitted fields do not survive.
	populated := UploadRequest{Size: 99, SHA256: strings.Repeat("b", 64), MediaType: "image/png"}
	if json.Unmarshal([]byte(`{"size":7,"sha256":"`+strings.Repeat("a", 64)+`"}`), &populated) != nil {
		t.Fatal("valid partial upload request rejected")
	}
	if populated != (UploadRequest{Size: 7, SHA256: strings.Repeat("a", 64)}) {
		t.Fatalf("populated receiver = %+v, want full replacement", populated)
	}
	// A rejected request leaves the receiver unchanged.
	kept := want
	if json.Unmarshal([]byte(base+`,"uploader":"other"}`), &kept) == nil || kept != want {
		t.Fatal("rejected upload request modified the receiver")
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
