package blob

import (
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

const r2VectorEndpoint = "https://00000000000000000000000000000000.r2.cloudflarestorage.com"

var r2VectorNow = time.Date(2026, 11, 5, 3, 4, 5, 0, time.UTC)

func r2VectorHeaders() http.Header {
	digest := sha256.Sum256([]byte("fixture"))
	checksum := base64.StdEncoding.EncodeToString(digest[:])
	h := http.Header{}
	for k, v := range map[string]string{"Content-Length": "7", "Content-Type": "application/octet-stream", "If-None-Match": "*", "Content-Disposition": "attachment", "Cache-Control": "private, no-store", "X-Amz-Checksum-Sha256": checksum, "X-Amz-Meta-Vecnet-Checksum": checksum, "X-Amz-Meta-Vecnet-Checksum-Profile": "sha256"} {
		h.Set(k, v)
	}
	return h
}

func TestR2CanonicalPUTVector(t *testing.T) {
	// Literal accepted spike vector, independently calculated with Python hmac/hashlib.
	const wantCanonical = `PUT
/vecnet-blobs-test/attachments/v1/exp/1h/vector/blob
X-Amz-Algorithm=AWS4-HMAC-SHA256&X-Amz-Credential=VECTORACCESS%2F20261105%2Fauto%2Fs3%2Faws4_request&X-Amz-Date=20261105T030405Z&X-Amz-Expires=300&X-Amz-SignedHeaders=cache-control%3Bcontent-disposition%3Bcontent-length%3Bcontent-type%3Bhost%3Bif-none-match%3Bx-amz-checksum-sha256%3Bx-amz-meta-vecnet-checksum%3Bx-amz-meta-vecnet-checksum-profile
cache-control:private, no-store
content-disposition:attachment
content-length:7
content-type:application/octet-stream
host:00000000000000000000000000000000.r2.cloudflarestorage.com
if-none-match:*
x-amz-checksum-sha256:8W0F7GspJI0sYa2x6SY/eOT3us4blVAUotF4cs/kBk0=
x-amz-meta-vecnet-checksum:8W0F7GspJI0sYa2x6SY/eOT3us4blVAUotF4cs/kBk0=
x-amz-meta-vecnet-checksum-profile:sha256

cache-control;content-disposition;content-length;content-type;host;if-none-match;x-amz-checksum-sha256;x-amz-meta-vecnet-checksum;x-amz-meta-vecnet-checksum-profile
UNSIGNED-PAYLOAD`
	const wantStringToSign = `AWS4-HMAC-SHA256
20261105T030405Z
20261105/auto/s3/aws4_request
b2f7c4ce90a2ffb4b313d01ae44322eef9fb52523745c6e09a7129c8bbce74b3`
	signed, canonical, stringToSign, err := r2Presign("PUT", r2VectorEndpoint, "vecnet-blobs-test", "attachments/v1/exp/1h/vector/blob", "VECTORACCESS", "vector-secret-not-live", r2VectorNow, 300*time.Second, r2VectorHeaders())
	if err != nil || canonical != wantCanonical || stringToSign != wantStringToSign {
		t.Fatal("literal canonical/string-to-sign vector mismatch; values suppressed")
	}
	u, err := url.Parse(signed)
	if err != nil {
		t.Fatal("vector URL parse failed")
	}
	if u.Query().Get("X-Amz-Signature") != "e6ca8ef5f955a2a6ae9e339c3ea31f8a1d88a99a208aab0d52a9ea093c77e9d1" {
		t.Fatal("literal independent signature mismatch")
	}
	public, err := PresignPUT(r2VectorEndpoint, "vecnet-blobs-test", "attachments/v1/exp/1h/vector/blob", "VECTORACCESS", "vector-secret-not-live", r2VectorNow, 300*time.Second, r2VectorHeaders())
	if err != nil || public != signed {
		t.Fatal("public signer differs from accepted vector")
	}
}

func TestR2CanonicalGETVector(t *testing.T) {
	// Offline vector independently calculated with Python hmac/hashlib. These
	// literals pin GET and the exact object key; no provider call is involved.
	const wantCanonical = `GET
/vecnet-blobs-test/folder/a%20b%2Bc/%CE%BB//d
X-Amz-Algorithm=AWS4-HMAC-SHA256&X-Amz-Credential=VECTORACCESS%2F20261105%2Fauto%2Fs3%2Faws4_request&X-Amz-Date=20261105T030405Z&X-Amz-Expires=600&X-Amz-SignedHeaders=host
host:00000000000000000000000000000000.r2.cloudflarestorage.com

host
UNSIGNED-PAYLOAD`
	const wantStringToSign = `AWS4-HMAC-SHA256
20261105T030405Z
20261105/auto/s3/aws4_request
6e8752ab2c9245808be31b0e4085a1ab830b1f335dd1c9801a13596ade97c4f7`
	const wantSignature = "44e77479a75cf80d84b995c5364977333fb4e5cf9adf18ffed5b2c24b076a489"
	signed, canonical, stringToSign, err := r2Presign("GET", r2VectorEndpoint, "vecnet-blobs-test", "folder/a b+c/λ//d", "VECTORACCESS", "vector-secret-not-live", r2VectorNow, 600*time.Second, nil)
	if err != nil || canonical != wantCanonical || stringToSign != wantStringToSign {
		t.Fatal("literal GET canonical/string-to-sign vector mismatch; values suppressed")
	}
	public, err := PresignGET(r2VectorEndpoint, "vecnet-blobs-test", "folder/a b+c/λ//d", "VECTORACCESS", "vector-secret-not-live", r2VectorNow, 600*time.Second)
	if err != nil || public != signed {
		t.Fatal("public GET signer differs from independent vector; values suppressed")
	}
	u, err := url.Parse(public)
	if err != nil {
		t.Fatal("GET vector URL parse failed")
	}
	if u.Scheme != "https" || "https://"+u.Host != r2VectorEndpoint || u.EscapedPath() != "/vecnet-blobs-test/folder/a%20b%2Bc/%CE%BB//d" || u.Query().Get("X-Amz-Signature") != wantSignature {
		t.Fatal("public GET object/signature vector mismatch; values suppressed")
	}
}

func TestR2SignerBindingsEscapingAndTTL(t *testing.T) {
	h := r2VectorHeaders()
	original, err := PresignPUT(r2VectorEndpoint, "vecnet-blobs-test", "folder/a b+c/λ//d", "VECTORACCESS", "vector-secret-not-live", r2VectorNow, 300*time.Second, h)
	if err != nil {
		t.Fatal("valid PUT rejected")
	}
	u, err := url.Parse(original)
	if err != nil {
		t.Fatal("URL parse failed")
	}
	if u.EscapedPath() != "/vecnet-blobs-test/folder/a%20b%2Bc/%CE%BB//d" {
		t.Fatal("space/plus/slash/Unicode escaping mismatch")
	}
	if u.Query().Get("X-Amz-Expires") != "300" {
		t.Fatal("PUT TTL changed")
	}
	if u.Query().Has("x-amz-checksum-sha256") {
		t.Fatal("checksum hoisted into query")
	}
	const signedHeaders = "cache-control;content-disposition;content-length;content-type;host;if-none-match;x-amz-checksum-sha256;x-amz-meta-vecnet-checksum;x-amz-meta-vecnet-checksum-profile"
	if u.Query().Get("X-Amz-SignedHeaders") != signedHeaders {
		t.Fatal("missing required signed header")
	}
	if strings.Contains(u.RawQuery, "+") || !strings.HasPrefix(u.RawQuery, "X-Amz-Algorithm=") {
		t.Fatal("query not AWS encoded/sorted")
	}
	for field := range h {
		missing := h.Clone()
		missing.Del(field)
		if _, err := PresignPUT(r2VectorEndpoint, "vecnet-blobs-test", "key", "VECTORACCESS", "vector-secret-not-live", r2VectorNow, 300*time.Second, missing); err == nil {
			t.Errorf("missing signed header %s accepted", field)
		}
	}
	for _, length := range []string{"0", "-1", "7.0", "+7", "07", "26214401"} {
		bad := h.Clone()
		bad.Set("Content-Length", length)
		if _, err := PresignPUT(r2VectorEndpoint, "vecnet-blobs-test", "key", "VECTORACCESS", "vector-secret-not-live", r2VectorNow, 300*time.Second, bad); err == nil {
			t.Fatal("invalid exact size accepted")
		}
	}
	changed := h.Clone()
	changed.Set("Content-Length", "8")
	other, err := PresignPUT(r2VectorEndpoint, "vecnet-blobs-test", "folder/a b+c/λ//d", "VECTORACCESS", "vector-secret-not-live", r2VectorNow, 300*time.Second, changed)
	if err != nil || other == original {
		t.Fatal("valid changed length did not change capability")
	}
	changed = h.Clone()
	d := sha256.Sum256([]byte("changed"))
	c := base64.StdEncoding.EncodeToString(d[:])
	changed.Set("X-Amz-Checksum-Sha256", c)
	changed.Set("X-Amz-Meta-Vecnet-Checksum", c)
	other, err = PresignPUT(r2VectorEndpoint, "vecnet-blobs-test", "folder/a b+c/λ//d", "VECTORACCESS", "vector-secret-not-live", r2VectorNow, 300*time.Second, changed)
	if err != nil || other == original {
		t.Fatal("valid changed body checksum did not change capability")
	}
	for _, ttl := range []time.Duration{0, time.Second, 299 * time.Second, 301 * time.Second, 300*time.Second + time.Nanosecond} {
		if _, err := PresignPUT(r2VectorEndpoint, "vecnet-blobs-test", "key", "VECTORACCESS", "vector-secret-not-live", r2VectorNow, ttl, h); err == nil {
			t.Fatal("non-300 PUT TTL accepted")
		}
	}
	for _, tt := range []struct {
		ttl  time.Duration
		want string
	}{{time.Hour, "600"}, {600 * time.Second, "600"}, {1900 * time.Millisecond, "1"}, {time.Second, "1"}} {
		value, err := PresignGET(r2VectorEndpoint, "vecnet-blobs-test", "key", "VECTORACCESS", "vector-secret-not-live", r2VectorNow, tt.ttl)
		if err != nil {
			t.Fatal("valid GET TTL rejected")
		}
		u, err := url.Parse(value)
		if err != nil || u.Query().Get("X-Amz-Expires") != tt.want || u.Query().Get("X-Amz-SignedHeaders") != "host" {
			t.Fatal("GET floor/cap mismatch")
		}
	}
	for _, ttl := range []time.Duration{0, -time.Second, 999 * time.Millisecond} {
		if _, err := PresignGET(r2VectorEndpoint, "vecnet-blobs-test", "key", "VECTORACCESS", "vector-secret-not-live", r2VectorNow, ttl); err == nil {
			t.Fatal("subsecond GET allowed")
		}
	}
}

func TestR2SignerRejectsUnsafePUTHeaders(t *testing.T) {
	for _, tt := range []struct {
		name, field, value string
	}{
		{"html", "Content-Type", "text/html"},
		{"inline", "Content-Disposition", "inline"},
		{"public_cache", "Cache-Control", "public"},
		{"conditional_etag", "If-None-Match", `"existing-etag"`},
		{"conditional_empty", "If-None-Match", ""},
		{"conditional_arbitrary", "If-None-Match", "changed"},
		{"unsupported_profile", "X-Amz-Meta-Vecnet-Checksum-Profile", "sha1"},
		{"parked_profile", "X-Amz-Meta-Vecnet-Checksum-Profile", "md5"},
		{"empty_profile", "X-Amz-Meta-Vecnet-Checksum-Profile", ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			h := r2VectorHeaders()
			h.Set(tt.field, tt.value)
			value, err := PresignPUT(r2VectorEndpoint, "vecnet-blobs-test", "key", "VECTORACCESS", "vector-secret-not-live", r2VectorNow, R2UploadTTL, h)
			if err == nil || value != "" {
				t.Fatal("unsafe PUT header accepted; capability suppressed")
			}
		})
	}
}

func TestR2SignerEndpointAndErrorRedaction(t *testing.T) {
	marker := "sensitive-synthetic-marker"
	for _, endpoint := range []string{"http://invalid.test", "https://user:" + marker + "@invalid.test", "https://invalid.test/", "https://invalid.test/path", "https://invalid.test?X-Amz-Signature=" + marker, "https://invalid.test#" + marker, "https://invalid.test?", "https://", "%" + marker} {
		value, err := PresignGET(endpoint, "vecnet-blobs-test", "key", marker, marker, r2VectorNow, 600*time.Second)
		if err == nil || value != "" {
			t.Fatal("invalid endpoint accepted")
		}
		if strings.Contains(err.Error(), marker) || strings.Contains(err.Error(), endpoint) || strings.Contains(err.Error(), "X-Amz-") {
			t.Fatal("signing error leaks sensitive values")
		}
	}
	for _, tt := range []struct{ bucket, key, access, secret string }{{"../bucket", "key", marker, marker}, {"bucket", "", marker, marker}, {"bucket", "key", "", marker}, {"bucket", "key", marker, ""}} {
		if value, err := PresignGET(r2VectorEndpoint, tt.bucket, tt.key, tt.access, tt.secret, r2VectorNow, time.Second); err == nil || value != "" || strings.Contains(err.Error(), marker) {
			t.Fatal("invalid signing input accepted or leaked")
		}
	}
	h := r2VectorHeaders()
	h["X-Amz-Checksum-Sha256"] = []string{marker}
	if _, err := PresignPUT(r2VectorEndpoint, "bucket", "key", marker, marker, r2VectorNow, 300*time.Second, h); err == nil || strings.Contains(err.Error(), marker) {
		t.Fatal("invalid checksum accepted or leaked")
	}
}

func TestR2SignerDoesNotContactOrFollowRedirects(t *testing.T) {
	// The signer is pure: it performs no HTTP, so even a configured 301 endpoint
	// is never contacted. Task 2 owns the metadata client's redirect refusal.
	calls := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		http.Redirect(w, r, "https://invalid.test", http.StatusMovedPermanently)
	}))
	defer server.Close()
	if _, err := PresignGET(server.URL, "bucket", "key", "VECTORACCESS", "vector-secret-not-live", r2VectorNow, time.Second); err != nil {
		t.Fatal("valid HTTPS fake endpoint rejected")
	}
	if calls != 0 {
		t.Fatal("signer contacted endpoint or followed 301")
	}
}

func TestR2SignerRejectsAmbiguousHeadersAndSortsQuery(t *testing.T) {
	for _, mutate := range []func(http.Header){
		func(h http.Header) { h["content-length"] = []string{"7"} },
		func(h http.Header) { h["Content-Length"] = []string{"7", "8"} },
		func(h http.Header) { h.Set("Content-Type", "application/octet-stream\r\nInjected: value") },
		func(h http.Header) { h.Set("Host", "other.invalid") },
		func(h http.Header) { h.Set("Content-Md5", "unsupported") },
		func(h http.Header) { h.Set("Authorization", "sensitive-synthetic-marker") },
	} {
		h := r2VectorHeaders()
		mutate(h)
		value, err := PresignPUT(r2VectorEndpoint, "bucket", "key", "VECTORACCESS", "vector-secret-not-live", r2VectorNow, R2UploadTTL, h)
		if err == nil || value != "" || strings.Contains(err.Error(), "sensitive-synthetic-marker") {
			t.Fatal("ambiguous/unsafe header accepted or leaked")
		}
	}
	if r2CanonicalQuery(url.Values{"a": {"z", "+", " "}, "a b": {"/"}}) != "a=%20&a=%2B&a=z&a%20b=%2F" {
		t.Fatal("AWS query order or encoding mismatch")
	}
}
