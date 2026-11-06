package blobclient

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/blobwire"
)

const testID = "blo_00000000000000000000000001"
const abcSHA = "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type fixture struct {
	client                                       *Client
	now                                          time.Time
	direct                                       *httptest.Server
	grant                                        blobwire.DownloadGrant
	ref                                          blobwire.Ref
	putStatus, getStatus                         int
	mints, puts, completes, redirects, discovers int
	hubCode                                      string
	mutate                                       func()
	body                                         string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{now: time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC), body: "abc", putStatus: 200, getStatus: 200}
	verified := true
	f.ref = blobwire.Ref{BlobID: testID, SHA256: abcSHA, SHA256Verified: &verified, Integrity: &blobwire.Integrity{Algorithm: "sha256", Value: abcSHA, Verified: true}, Size: 3, RetentionClass: "keep"}
	f.direct = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
			t.Error("credential at direct endpoint")
		}
		if r.URL.Path == "/redirected" {
			f.redirects++
			w.WriteHeader(200)
			return
		}
		if r.Method == "PUT" {
			f.puts++
			sum := sha256.Sum256([]byte("abc"))
			if r.ContentLength != 3 || len(r.TransferEncoding) != 0 || r.Header.Get("X-Amz-Checksum-Sha256") != base64.StdEncoding.EncodeToString(sum[:]) || r.Header.Get("If-None-Match") != "*" || r.Header.Get("Content-Type") != "application/octet-stream" {
				t.Error("signed PUT contract changed")
			}
			b, _ := io.ReadAll(r.Body)
			if string(b) != "abc" {
				w.WriteHeader(400)
				return
			}
			if f.putStatus == 307 {
				w.Header().Set("Location", f.direct.URL+"/redirected")
			}
			w.WriteHeader(f.putStatus)
			return
		}
		if f.getStatus == 307 {
			w.Header().Set("Location", f.direct.URL+"/redirected")
		}
		w.WriteHeader(f.getStatus)
		if fl, ok := w.(http.Flusher); ok {
			fl.Flush()
		}
		io.WriteString(w, f.body)
	}))
	t.Cleanup(f.direct.Close)
	hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer fake-hub" {
			t.Error("missing hub auth")
		}
		switch {
		case r.URL.Path == "/v1/blob-capabilities":
			f.discovers++
			json.NewEncoder(w).Encode(map[string]any{"backend": "r2", "max_size": 25 << 20, "checksum_profile": "sha256", "download_origins": []string{f.direct.URL}})
		case r.URL.Path == "/v1/blobs/uploads":
			var req blobwire.UploadRequest
			if json.NewDecoder(r.Body).Decode(&req) != nil || req.SHA256 != abcSHA || req.Size != 3 || r.Header.Get("Idempotency-Key") != "idem" {
				t.Error("bad grant request")
			}
			if f.mutate != nil {
				f.mutate()
			}
			ref := f.ref
			no := false
			ref.SHA256Verified = &no
			ref.Integrity = &blobwire.Integrity{Algorithm: "sha256", Value: abcSHA}
			sum, _ := hex.DecodeString(abcSHA)
			json.NewEncoder(w).Encode(blobwire.UploadGrant{Ref: ref, Upload: blobwire.Request{Method: "PUT", URL: f.direct.URL + "/object?private=signature", ExpiresAt: f.now.Add(5 * time.Minute).Format(time.RFC3339), Headers: map[string]string{"Content-Length": "3", "Content-Type": "application/octet-stream", "Content-Disposition": "attachment", "Cache-Control": "private, no-store", "If-None-Match": "*", "x-amz-checksum-sha256": base64.StdEncoding.EncodeToString(sum), "x-amz-meta-vecnet-checksum-profile": "sha256", "x-amz-meta-vecnet-checksum": base64.StdEncoding.EncodeToString(sum)}}, CompleteURL: "/v1/blobs/uploads/" + testID + "/complete"})
		case strings.HasSuffix(r.URL.Path, "/complete"):
			f.completes++
			json.NewEncoder(w).Encode(f.ref)
		default:
			f.mints++
			if f.hubCode != "" {
				w.Header().Set("Retry-After", "17")
				w.WriteHeader(410)
				json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"code": f.hubCode, "message": "private=signature", "details": map[string]any{"retry_after_seconds": 17}}})
				return
			}
			if r.URL.Query().Get("blob_id") != testID {
				t.Error("missing stable ID")
			}
			g := f.grant
			if g.SHA256 == "" {
				g = blobwire.DownloadGrant{BlobID: testID, SHA256: abcSHA, SHA256Verified: f.ref.SHA256Verified, Integrity: f.ref.Integrity, Size: 3, MediaType: "text/plain", RetentionClass: "keep", Method: "GET", URL: f.direct.URL + "/object?private=signature", ExpiresAt: f.now.Add(600 * time.Second).Format(time.RFC3339)}
			}
			json.NewEncoder(w).Encode(g)
		}
	}))
	t.Cleanup(hub.Close)
	auth := transportFunc(func(r *http.Request) (*http.Response, error) {
		r.Header.Set("Authorization", "Bearer fake-hub")
		r.Header.Set("Cookie", "hub-only=1")
		return http.DefaultTransport.RoundTrip(r)
	})
	f.client = New(hub.URL, auth, nil, nil)
	f.client.Now = func() time.Time { return f.now }
	return f
}
func TestR2UploadTransport(t *testing.T) {
	f := newFixture(t)
	ref, err := f.client.Upload(context.Background(), strings.NewReader("abc"), 3, "text/plain", "idem")
	if err != nil || ref.BlobID != testID || ref.Integrity == nil || !ref.Integrity.Verified || f.puts != 1 || f.completes != 1 {
		t.Fatalf("upload failed: %v, puts=%d completes=%d", err, f.puts, f.completes)
	}
}
func TestR2UploadRedirectAndMutation(t *testing.T) {
	for _, kind := range []string{"redirect", "changed", "cancel"} {
		t.Run(kind, func(t *testing.T) {
			f := newFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			r := bytes.NewReader([]byte("abc"))
			switch kind {
			case "redirect":
				f.putStatus = 307
			case "changed":
				f.mutate = func() { r.Reset([]byte("xyz")) }
			case "cancel":
				f.mutate = cancel
			}
			_, err := f.client.Upload(ctx, r, 3, "text/plain", "idem")
			if err == nil || f.completes != 0 || f.redirects != 0 {
				t.Fatalf("unsafe completion: err=%v completes=%d redirects=%d", err, f.completes, f.redirects)
			}
		})
	}
}
func TestR2DownloadRepeatableAndBounded(t *testing.T) {
	f := newFixture(t)
	for i := 0; i < 3; i++ {
		b, g, err := f.client.Download(context.Background(), abcSHA, testID, 3)
		if err != nil || string(b) != "abc" || g.BlobID != testID {
			t.Fatalf("download %d: %v", i, err)
		}
		f.now = f.now.Add(601 * time.Second)
	}
	if f.mints != 3 || f.discovers != 3 {
		t.Fatalf("fresh exchanges=%d discovery=%d", f.mints, f.discovers)
	}
	for _, body := range []string{"abcd", "xyz", "ab"} {
		f.body = body
		b, _, err := f.client.Download(context.Background(), abcSHA, testID, 3)
		if err == nil || b != nil {
			t.Fatal("bad full object accepted")
		}
	}
	f.body = "abc"
	f.getStatus = 206
	if _, _, err := f.client.Download(context.Background(), abcSHA, testID, 3); err == nil {
		t.Fatal("range claimed full digest")
	}
	f.getStatus = 307
	if _, _, err := f.client.Download(context.Background(), abcSHA, testID, 3); err == nil || f.redirects != 0 {
		t.Fatal("followed redirect")
	}
}
func TestR2DownloadStateAndRetry(t *testing.T) {
	for _, code := range []string{"blob_expired", "blob_purged", "blob_url_rate_limited"} {
		t.Run(code, func(t *testing.T) {
			f := newFixture(t)
			f.hubCode = code
			b, g, err := f.client.Download(context.Background(), abcSHA, testID, 3)
			var e *Error
			if !errors.As(err, &e) || e.Code != code || e.RetryAfterSeconds != 17 || b != nil || g.URL != "" || strings.Contains(err.Error(), "signature") || f.mints != 1 {
				t.Fatal("state/retry contract lost")
			}
		})
	}
}
func TestR2DownloadRejectsUntrustedGrant(t *testing.T) {
	for _, kind := range []string{"origin", "deadline", "too-long", "unverified", "identity", "size", "object-expiry"} {
		t.Run(kind, func(t *testing.T) {
			f := newFixture(t)
			g := blobwire.DownloadGrant{BlobID: testID, SHA256: abcSHA, SHA256Verified: f.ref.SHA256Verified, Integrity: f.ref.Integrity, Size: 3, Method: "GET", URL: f.direct.URL + "/object", ExpiresAt: f.now.Add(600 * time.Second).Format(time.RFC3339)}
			switch kind {
			case "origin":
				g.URL = "https://untrusted.invalid/object"
			case "deadline":
				g.ExpiresAt = f.now.Format(time.RFC3339)
			case "too-long":
				g.ExpiresAt = f.now.Add(601 * time.Second).Format(time.RFC3339)
			case "unverified":
				no := false
				g.SHA256Verified = &no
				g.Integrity = &blobwire.Integrity{Algorithm: "sha256", Value: abcSHA}
			case "identity":
				g.BlobID = "other"
			case "size":
				g.Size = 4
			case "object-expiry":
				at := f.now.Add(time.Minute).Format(time.RFC3339)
				g.ObjectExpiresAt = &at
			}
			f.grant = g
			if _, _, err := f.client.Download(context.Background(), abcSHA, testID, 3); err == nil {
				t.Fatal("untrusted grant accepted")
			}
		})
	}
}
func TestR2DownloadExpiredCapabilityRefreshIsBounded(t *testing.T) {
	f := newFixture(t)
	f.getStatus = 403
	_, g, err := f.client.Download(context.Background(), abcSHA, testID, 3)
	if err == nil || g.URL != "" || f.mints != 2 {
		t.Fatalf("unbounded or missing refresh: mints=%d", f.mints)
	}
}

func TestR2DownloadRefreshSucceeds(t *testing.T) {
	f := newFixture(t)
	calls := 0
	f.client.transfer.Transport = transportFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			return &http.Response{StatusCode: 403, Body: io.NopCloser(strings.NewReader("private signature")), Header: make(http.Header)}, nil
		}
		return http.DefaultTransport.RoundTrip(r)
	})
	b, _, err := f.client.Download(context.Background(), abcSHA, testID, 3)
	if err != nil || string(b) != "abc" || f.mints != 2 {
		t.Fatalf("bounded fresh exchange failed: %v", err)
	}
}
func TestR2UploadCapsAndProgress(t *testing.T) {
	f := newFixture(t)
	var progress []int64
	f.client.Progress = func(n int64) { progress = append(progress, n) }
	if _, err := f.client.Upload(context.Background(), strings.NewReader("abc"), 3, "text/plain", "idem"); err != nil {
		t.Fatal(err)
	}
	if len(progress) == 0 || progress[len(progress)-1] != 3 {
		t.Fatal("missing transfer progress")
	}
	for _, n := range progress {
		if n > 3 {
			t.Fatal("digest pass counted as PUT bytes")
		}
	}
	for _, size := range []int64{-1, 0, MaxSize + 1} {
		if _, err := f.client.Upload(context.Background(), strings.NewReader("abc"), size, "text/plain", "idem"); err == nil {
			t.Fatal("upload bound accepted")
		}
	}
	if f.puts != 1 {
		t.Fatal("out-of-bounds upload reached provider")
	}
}
