package blob

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/XertroV/vecnet/internal/blobwire"
)

var (
	ErrR2Incomplete = errors.New("upload_incomplete")
	ErrR2Purged     = errors.New("blob_purged")
	ErrR2Proof      = errors.New("blob_proof_mismatch")
)

const r2ResponseLimit = 16 << 10

// R2ProviderError exposes only an allowlisted reason and HTTP status. It never
// wraps transport errors, provider XML, request URLs or authentication headers.
type R2ProviderError struct {
	Code      string
	Status    int
	Retryable bool
}

func (e *R2ProviderError) Error() string { return "blob: R2 " + e.Code }

// ObjectProof is completion evidence for one persisted grant. SHA256Verified is
// true only after all stored metadata, size and checksum checks match. The caller
// must still CAS pending -> complete and recheck authorization/purge in its DB
// transaction. ETag is opaque; it is never interpreted as a content checksum.
type ObjectProof struct {
	Length           int64
	ETag             string
	Metadata         http.Header
	ProviderChecksum string
	ChecksumProfile  string
	SHA256Verified   bool
}

// R2ObjectsConfig contains trusted hub configuration, not request input. Lookup
// reads the current persisted grant by its exact key; it must also reflect purge
// tombstones. It is deliberately independent of store and prefs packages.
// Client is copied; its redirect policy and cookie jar are never inherited.
// Endpoint is the operator-selected HTTPS S3 origin (validated by hub config in
// Task 4); an injected TLS httptest client allows fully offline adapter tests.
type R2ObjectsConfig struct {
	Endpoint, Bucket, Namespace, AccessID, Secret string
	Client                                        *http.Client
	Now                                           func() time.Time
	Lookup                                        func(context.Context, string) (R2Object, error)
}

// R2Objects only sends HEAD and DELETE. Download signs GET without fetching or
// caching bytes/URLs. Authorization and mint metering belong to the hub wrapper.
type R2Objects struct {
	endpoint, bucket, namespace, accessID, secret string
	client                                        *http.Client
	now                                           func() time.Time
	lookup                                        func(context.Context, string) (R2Object, error)
}

func NewR2Objects(config R2ObjectsConfig) (*R2Objects, error) {
	if config.Now == nil || config.Lookup == nil || config.Client == nil || !r2NamespacePattern.MatchString(config.Namespace) {
		return nil, ErrR2Metadata
	}
	// Reuse the signer's strict origin/credential/bucket validation without HTTP.
	if _, err := PresignGET(config.Endpoint, config.Bucket, "validate", config.AccessID, config.Secret, config.Now(), time.Second); err != nil {
		return nil, err
	}
	client := *config.Client
	client.Jar = nil
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	if client.Timeout <= 0 || client.Timeout > 30*time.Second {
		client.Timeout = 30 * time.Second
	}
	transport := client.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	if standard, ok := transport.(*http.Transport); ok {
		clone := standard.Clone()
		clone.MaxResponseHeaderBytes = r2ResponseLimit
		if clone.ResponseHeaderTimeout <= 0 || clone.ResponseHeaderTimeout > 30*time.Second {
			clone.ResponseHeaderTimeout = 30 * time.Second
		}
		client.Transport = clone
	}
	return &R2Objects{endpoint: config.Endpoint, bucket: config.Bucket, namespace: config.Namespace, accessID: config.AccessID, secret: config.Secret, client: &client, now: config.Now, lookup: config.Lookup}, nil
}

func (s *R2Objects) stored(ctx context.Context, key string) (R2Object, error) {
	if err := ctx.Err(); err != nil {
		return R2Object{}, err
	}
	// Reject malformed/foreign input even before a caller-supplied lookup runs.
	parts := strings.Split(key, "/")
	var class, namespace, id string
	switch {
	case len(parts) == 6 && parts[0] == "attachments" && parts[1] == "v1" && parts[2] == "exp":
		class, namespace, id = parts[3], parts[4], parts[5]
	case len(parts) == 5 && parts[0] == "attachments" && parts[1] == "v1" && parts[2] == "keep":
		class, namespace, id = "keep", parts[3], parts[4]
	default:
		return R2Object{}, ErrR2Metadata
	}
	expected, err := R2ObjectKey(s.namespace, id, class)
	if err != nil || namespace != s.namespace || key != expected {
		return R2Object{}, ErrR2Metadata
	}
	object, err := s.lookup(ctx, key)
	if err != nil {
		return R2Object{}, ErrR2Metadata
	} // never echo untrusted storage errors
	expected, err = R2ObjectKey(s.namespace, object.BlobID, object.RetentionClass)
	if err != nil || object.Backend != "r2" || object.ObjectKey != key || expected != key || object.Size < 1 || object.Size > DefaultMaxSize || object.ChecksumProfile != "sha256" {
		return R2Object{}, ErrR2Metadata
	}
	if object.RetentionClass == "keep" {
		if object.ExpiresAt != nil {
			return R2Object{}, ErrR2Metadata
		}
	} else {
		_, expiry, err := R2Retention(object.RetentionClass, object.IssuedAt)
		if err != nil || object.IssuedAt.IsZero() || object.ExpiresAt == nil || !expiry.Equal(*object.ExpiresAt) {
			return R2Object{}, ErrR2Metadata
		}
	}
	// Persisted expected_checksum is the same lowercase hex digest as sha256.
	digest, err := hex.DecodeString(object.SHA256)
	if err != nil || len(digest) != sha256.Size || hex.EncodeToString(digest) != object.SHA256 || object.ExpectedChecksum != object.SHA256 {
		return R2Object{}, ErrR2Metadata
	}
	return object, nil
}

func r2ObjectAvailable(object R2Object, now time.Time) error {
	if object.PurgedAt != nil || object.State == "purged" {
		return ErrR2Purged
	}
	if object.State == "expired" || (object.ExpiresAt != nil && !now.Before(*object.ExpiresAt)) {
		return ErrR2Expired
	}
	if object.State != "pending" && object.State != "complete" {
		return ErrR2Incomplete
	}
	return nil
}

// An unverified uploader has only claimed a hash, so its completion probe must
// not reveal a tombstone before HEAD establishes possession. This exception is
// only for the pre-proof check; downloads and the post-proof check stay strict.
func r2CompletionAvailable(object R2Object, now time.Time) error {
	if !object.SHA256Verified {
		object.PurgedAt = nil
		if object.State == "purged" {
			object.State = "pending"
		}
	}
	return r2ObjectAvailable(object, now)
}

func (s *R2Objects) Head(ctx context.Context, key string) (ObjectProof, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	object, err := s.stored(ctx, key)
	if err != nil {
		return ObjectProof{}, err
	}
	if err = r2CompletionAvailable(object, s.now()); err != nil {
		return ObjectProof{}, err
	}
	response, err := s.private(ctx, http.MethodHead, key)
	if err != nil {
		return ObjectProof{}, err
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotFound {
		return ObjectProof{}, ErrR2Incomplete
	}
	if response.StatusCode != http.StatusOK {
		return ObjectProof{}, r2ProviderFailure(response)
	}
	proof, err := r2CheckProof(response, object)
	if err != nil {
		return ObjectProof{}, err
	}
	current, err := s.stored(ctx, key)
	if err != nil {
		return ObjectProof{}, err
	}
	if err = r2ObjectAvailable(current, s.now()); err != nil {
		return ObjectProof{}, err
	}
	// Immutable grant fields must still describe the evidence just checked.
	if current.SHA256 != object.SHA256 || current.Size != object.Size || current.ExpectedChecksum != object.ExpectedChecksum {
		return ObjectProof{}, ErrR2Proof
	}
	return proof, nil
}

func r2CheckProof(response *http.Response, object R2Object) (ObjectProof, error) {
	digest, err := hex.DecodeString(object.ExpectedChecksum)
	if err != nil || len(digest) != sha256.Size {
		return ObjectProof{}, ErrR2Proof
	}
	// S3 checksum headers and signed metadata use base64, independently of the
	// validated persisted hex representation. Never rewrite the stored grant.
	checksum := base64.StdEncoding.EncodeToString(digest)
	if !r2BoundedHeaders(response.Header) {
		return ObjectProof{}, ErrR2Proof
	}
	h := response.Header
	single := func(name string) string {
		values := h.Values(name)
		if len(values) != 1 {
			return ""
		}
		return values[0]
	}
	if single("Content-Length") != strconv.FormatInt(object.Size, 10) || single("Content-Type") != "application/octet-stream" || single("Content-Disposition") != "attachment" || single("Cache-Control") != "private, no-store" || single("X-Amz-Meta-Vecnet-Checksum-Profile") != "sha256" || single("X-Amz-Meta-Vecnet-Checksum") != checksum {
		return ObjectProof{}, ErrR2Proof
	}
	etag := single("ETag")
	if len(etag) < 3 || len(etag) > 256 || etag[0] != '"' || etag[len(etag)-1] != '"' || !r2SafeText(etag) || strings.Contains(etag[1:len(etag)-1], "\"") {
		return ObjectProof{}, ErrR2Proof
	}
	provider := single("X-Amz-Checksum-Sha256")
	if values, present := h[http.CanonicalHeaderKey("X-Amz-Checksum-Sha256")]; present && (len(values) != 1 || provider != checksum) {
		return ObjectProof{}, ErrR2Proof
	}
	// Provider checksum is preferred. Its absence falls back to signed immutable
	// metadata ONLY because Task 0 proved R2 enforces the signed SHA256/length and
	// If-None-Match single PUT. Privileged bucket writes are outside that model.
	metadata := http.Header{}
	for _, name := range []string{"Content-Type", "Content-Disposition", "Cache-Control", "X-Amz-Meta-Vecnet-Checksum", "X-Amz-Meta-Vecnet-Checksum-Profile"} {
		metadata.Set(name, single(name))
	}
	return ObjectProof{Length: object.Size, ETag: etag, Metadata: metadata, ProviderChecksum: provider, ChecksumProfile: "sha256", SHA256Verified: true}, nil
}

func (s *R2Objects) Delete(ctx context.Context, key string) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if _, err := s.stored(ctx, key); err != nil {
		return err
	}
	response, err := s.private(ctx, http.MethodDelete, key)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	switch response.StatusCode {
	case http.StatusOK, http.StatusNoContent, http.StatusNotFound:
		return nil
	}
	return r2ProviderFailure(response)
}

// Download's objectExpiry is *time.Time: nil for keep, otherwise the persisted
// deadline. Passing a different deadline fails closed. Every call uses now and
// needs no earlier capability; same-second identical inputs may sign identically.
func (s *R2Objects) Download(ctx context.Context, key string, now time.Time, objectExpiry *time.Time) (blobwire.Request, error) {
	fail := func(err error) (blobwire.Request, error) { return blobwire.Request{}, err }
	object, err := s.stored(ctx, key)
	if err != nil {
		return fail(err)
	}
	if now.IsZero() {
		return fail(ErrR2Metadata)
	}
	if err = r2ObjectAvailable(object, now); err != nil {
		return fail(err)
	}
	if object.State != "complete" || !object.SHA256Verified {
		return fail(ErrR2Incomplete)
	}
	if (object.ExpiresAt == nil) != (objectExpiry == nil) || (objectExpiry != nil && !objectExpiry.Equal(*object.ExpiresAt)) {
		return fail(ErrR2Metadata)
	}
	ttl, err := R2DownloadTTL(now, objectExpiry)
	if err != nil {
		return fail(err)
	}
	signed, err := PresignGET(s.endpoint, s.bucket, key, s.accessID, s.secret, now, ttl)
	if err != nil {
		return fail(err)
	}
	if err = ctx.Err(); err != nil {
		return fail(err)
	}
	// SigV4 timestamps have whole-second resolution. Report that actual deadline,
	// which may be earlier than now+ttl when the injected clock has fractions.
	// The wire timestamp still requires fixed millisecond UTC precision.
	return blobwire.Request{Method: http.MethodGet, URL: signed, Headers: map[string]string{}, ExpiresAt: now.UTC().Truncate(time.Second).Add(ttl).Format("2006-01-02T15:04:05.000Z")}, nil
}

func (s *R2Objects) private(ctx context.Context, method, key string) (*http.Response, error) {
	for attempt := 0; attempt < 3; attempt++ {
		request, err := r2PrivateRequest(method, s.endpoint, s.bucket, key, s.accessID, s.secret, s.now())
		if err != nil {
			return nil, err
		}
		response, err := s.client.Do(request.WithContext(ctx))
		if err != nil {
			if response != nil && response.Body != nil {
				response.Body.Close()
			}
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			// net/http errors often embed URLs; never wrap or expose them.
			return nil, &R2ProviderError{Code: "transport_error", Retryable: true}
		}
		if !r2BoundedHeaders(response.Header) {
			response.Body.Close()
			return nil, &R2ProviderError{Code: "response_too_large", Status: response.StatusCode}
		}
		if (response.StatusCode == 429 || response.StatusCode == 500 || response.StatusCode == 502 || response.StatusCode == 503 || response.StatusCode == 504) && attempt < 2 {
			// Read only bounded error XML, never an object body. HEAD success bodies
			// are never read; successful DELETE bodies are simply closed by the caller.
			_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, r2ResponseLimit))
			response.Body.Close()
			continue
		}
		return response, nil
	}
	return nil, &R2ProviderError{Code: "unavailable", Retryable: true}
}

func r2BoundedHeaders(headers http.Header) bool {
	total := 0
	for name, values := range headers {
		if !r2SafeText(name) {
			return false
		}
		for _, value := range values {
			total += len(name) + len(value) + 4
			if total > r2ResponseLimit || !r2SafeText(value) {
				return false
			}
		}
	}
	return true
}

func r2ProviderFailure(response *http.Response) *R2ProviderError {
	code := "provider_error"
	switch response.StatusCode {
	case 301, 302, 303, 307, 308:
		code = "redirect_refused"
	case 401, 403:
		code = "access_denied"
	case 404:
		code = "not_found"
	case 429, 500, 502, 503, 504:
		code = "unavailable"
	}
	var body struct {
		XMLName xml.Name `xml:"Error"`
		Code    string   `xml:"Code"`
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, r2ResponseLimit+1))
	if err == nil && len(data) <= r2ResponseLimit && xml.Unmarshal(data, &body) == nil && code != "redirect_refused" {
		switch body.Code {
		case "AccessDenied", "Unauthorized", "SignatureDoesNotMatch", "ExpiredRequest":
			code = "access_denied"
		case "NoSuchKey", "NotFound":
			code = "not_found"
		case "SlowDown", "InternalError", "ServiceUnavailable":
			code = "unavailable"
		case "BadDigest", "InvalidDigest", "InvalidArgument", "PreconditionFailed":
			code = "request_rejected"
		}
	}
	return &R2ProviderError{Code: code, Status: response.StatusCode, Retryable: response.StatusCode == 429 || response.StatusCode >= 500}
}

// r2PrivateRequest uses header SigV4 so hub credentials never enter query strings.
// Only the two metadata/cleanup methods exist here; GET always uses PresignGET.
func r2PrivateRequest(method, endpoint, bucket, key, accessID, secret string, now time.Time) (*http.Request, error) {
	if method != http.MethodHead && method != http.MethodDelete {
		return nil, ErrR2Signing
	}
	// The shared signer validates and preserves exact origin/path encoding. Drop
	// its ephemeral query immediately; the actual request uses an independent
	// method-bound Authorization signature over the headers below.
	signed, err := PresignGET(endpoint, bucket, key, accessID, secret, now, time.Second)
	if err != nil {
		return nil, ErrR2Signing
	}
	originPath := strings.SplitN(signed, "?", 2)[0]
	request, err := http.NewRequest(method, originPath, nil)
	if err != nil {
		return nil, ErrR2Signing
	}
	stamp := now.UTC().Format("20060102T150405Z")
	date := now.UTC().Format("20060102")
	values := map[string]string{"host": request.URL.Host, "x-amz-date": stamp, "x-amz-content-sha256": "UNSIGNED-PAYLOAD"}
	if method == http.MethodHead {
		values["x-amz-checksum-mode"] = "ENABLED"
	}
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	var canonicalHeaders strings.Builder
	for _, name := range names {
		canonicalHeaders.WriteString(name + ":" + values[name] + "\n")
		if name != "host" {
			request.Header.Set(name, values[name])
		}
	}
	signedHeaders := strings.Join(names, ";")
	canonical := method + "\n" + request.URL.EscapedPath() + "\n\n" + canonicalHeaders.String() + "\n" + signedHeaders + "\nUNSIGNED-PAYLOAD"
	digest := sha256.Sum256([]byte(canonical))
	scope := date + "/auto/s3/aws4_request"
	toSign := "AWS4-HMAC-SHA256\n" + stamp + "\n" + scope + "\n" + hex.EncodeToString(digest[:])
	signingKey := r2HMAC([]byte("AWS4"+secret), date)
	signingKey = r2HMAC(signingKey, "auto")
	signingKey = r2HMAC(signingKey, "s3")
	signingKey = r2HMAC(signingKey, "aws4_request")
	request.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential="+accessID+"/"+scope+", SignedHeaders="+signedHeaders+", Signature="+hex.EncodeToString(r2HMAC(signingKey, toSign)))
	return request, nil
}
