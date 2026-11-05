package blob

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

const r2TestNamespace = "0123456789abcdef0123456789abcdef"

func r2TestObject(t *testing.T, class string) R2Object {
	t.Helper()
	key, err := R2ObjectKey(r2TestNamespace, "blo_00000000000000000000000001", class)
	if err != nil {
		t.Fatal("fixture key invalid")
	}
	digest := sha256.Sum256([]byte("fixture"))
	_, expiry, err := R2Retention(class, r2VectorNow)
	if class == "keep" {
		expiry = nil
		err = nil
	}
	if err != nil {
		t.Fatal("fixture retention invalid")
	}
	return R2Object{BlobID: "blo_00000000000000000000000001", Backend: "r2", ObjectKey: key, SHA256: hex.EncodeToString(digest[:]), Size: 7, IssuedAt: r2VectorNow, UploadDeadline: r2VectorNow.Add(R2UploadTTL), RetentionClass: class, ExpiresAt: expiry, ChecksumProfile: "sha256", ExpectedChecksum: base64.StdEncoding.EncodeToString(digest[:]), State: "pending"}
}

func r2ProofHeaders() http.Header {
	h := r2VectorHeaders()
	h.Del("If-None-Match")
	h.Set("ETag", `"opaque-single-put-etag"`)
	return h
}

func r2TestAdapter(t *testing.T, object *R2Object, now *time.Time, handler http.HandlerFunc) (*R2Objects, *httptest.Server) {
	t.Helper()
	var mu sync.Mutex
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { mu.Lock(); defer mu.Unlock(); handler(w, r) }))
	t.Cleanup(server.Close)
	adapter, err := NewR2Objects(R2ObjectsConfig{Endpoint: server.URL, Bucket: "vecnet-blobs-test", Namespace: r2TestNamespace, AccessID: "VECTORACCESS", Secret: "vector-secret-not-live", Client: server.Client(), Now: func() time.Time { mu.Lock(); defer mu.Unlock(); return *now }, Lookup: func(ctx context.Context, key string) (R2Object, error) {
		mu.Lock()
		defer mu.Unlock()
		if key != object.ObjectKey {
			return R2Object{}, ErrR2Metadata
		}
		return *object, nil
	}})
	if err != nil {
		t.Fatal("adapter construction failed")
	}
	return adapter, server
}

func TestR2HeadProof(t *testing.T) {
	// This fake models the Task 0 accepted SHA-enforced immutable PUT contract;
	// fake-only success is not provider acceptance.
	for _, provider := range []bool{true, false} {
		t.Run(strconv.FormatBool(provider), func(t *testing.T) {
			object := r2TestObject(t, "1h")
			now := r2VectorNow
			heads, deletes, gets := 0, 0, 0
			adapter, _ := r2TestAdapter(t, &object, &now, func(w http.ResponseWriter, r *http.Request) {
				switch r.Method {
				case "HEAD":
					heads++
				case "DELETE":
					deletes++
				case "GET":
					gets++
				}
				if r.URL.Path != "/vecnet-blobs-test/"+object.ObjectKey || r.URL.RawQuery != "" || r.Header.Get("Authorization") == "" || r.Header.Get("X-Amz-Date") != "20261105T030405Z" || r.Header.Get("X-Amz-Content-Sha256") != "UNSIGNED-PAYLOAD" || (r.Method == "HEAD" && r.Header.Get("X-Amz-Checksum-Mode") != "ENABLED") {
					t.Error("private request scope/header mismatch; values suppressed")
				}
				if r.Method == "DELETE" {
					w.WriteHeader(204)
					return
				}
				for k, vs := range r2ProofHeaders() {
					for _, v := range vs {
						w.Header().Add(k, v)
					}
				}
				if !provider {
					w.Header().Del("X-Amz-Checksum-Sha256")
				}
			})
			if object.SHA256Verified {
				t.Fatal("pending grant already verified")
			}
			proof, err := adapter.Head(context.Background(), object.ObjectKey)
			if err != nil || !proof.SHA256Verified || proof.Length != 7 || proof.ETag != `"opaque-single-put-etag"` || proof.ChecksumProfile != "sha256" || proof.Metadata.Get("X-Amz-Meta-Vecnet-Checksum") != object.ExpectedChecksum {
				t.Fatal("matching completion evidence rejected")
			}
			if (proof.ProviderChecksum != "") != provider {
				t.Fatal("optional provider checksum not preserved")
			}
			if object.SHA256Verified {
				t.Fatal("adapter mutated persisted completion state before caller CAS")
			}
			if err := adapter.Delete(context.Background(), object.ObjectKey); err != nil {
				t.Fatal("delete failed")
			}
			if heads != 1 || deletes != 1 || gets != 0 {
				t.Fatal("hub must only HEAD/DELETE; no verification GET")
			}
		})
	}
}

func TestR2HeadProofRejectsMismatch(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(http.Header)
	}{
		{"provider_sha", func(h http.Header) { h.Set("X-Amz-Checksum-Sha256", strings.Repeat("A", 43)+"=") }},
		{"size", func(h http.Header) { h.Set("Content-Length", "8") }},
		{"type", func(h http.Header) { h.Set("Content-Type", "text/html") }},
		{"disposition", func(h http.Header) { h.Set("Content-Disposition", "inline") }},
		{"cache", func(h http.Header) { h.Set("Cache-Control", "public") }},
		{"metadata_sha", func(h http.Header) { h.Set("X-Amz-Meta-Vecnet-Checksum", "wrong") }},
		{"profile", func(h http.Header) { h.Set("X-Amz-Meta-Vecnet-Checksum-Profile", "unknown") }},
		{"missing_metadata", func(h http.Header) { h.Del("X-Amz-Meta-Vecnet-Checksum") }},
		{"missing_etag", func(h http.Header) { h.Del("ETag") }},
		{"duplicate_type", func(h http.Header) { h.Add("Content-Type", "text/html") }},
		{"duplicate_checksum", func(h http.Header) { h.Add("X-Amz-Checksum-Sha256", h.Get("X-Amz-Checksum-Sha256")) }},
		{"oversized_metadata", func(h http.Header) { h.Set("X-Amz-Meta-Untrusted", strings.Repeat("x", 20000)) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			object := r2TestObject(t, "1h")
			now := r2VectorNow
			adapter, _ := r2TestAdapter(t, &object, &now, func(w http.ResponseWriter, r *http.Request) {
				h := r2ProofHeaders()
				tc.mutate(h)
				for k, vs := range h {
					for _, v := range vs {
						w.Header().Add(k, v)
					}
				}
			})
			proof, err := adapter.Head(context.Background(), object.ObjectKey)
			if err == nil || proof.SHA256Verified || object.SHA256Verified {
				t.Fatal("invalid metadata established verification")
			}
		})
	}
}

func TestR2HeadDownloadStoredGrantOnly(t *testing.T) {
	for _, name := range []string{"foreign_namespace", "prefix_class", "key_blob_id", "traversal", "request_key", "expired", "purged", "class_deadline", "unsupported_profile", "digest_binding"} {
		t.Run(name, func(t *testing.T) {
			object := r2TestObject(t, "1h")
			now := r2VectorNow
			calls := 0
			key := object.ObjectKey
			switch name {
			case "foreign_namespace":
				object.ObjectKey = strings.Replace(key, r2TestNamespace, strings.Repeat("f", 32), 1)
				key = object.ObjectKey
			case "prefix_class":
				object.RetentionClass = "keep"
			case "key_blob_id":
				object.BlobID = "blo_00000000000000000000000002"
			case "traversal":
				object.ObjectKey = key + "/../suffix"
				key = object.ObjectKey
			case "request_key":
				key += "/caller-input"
			case "expired":
				now = *object.ExpiresAt
			case "purged":
				object.PurgedAt = &now
			case "class_deadline":
				object.ExpiresAt = nil
			case "unsupported_profile":
				object.ChecksumProfile = "unknown"
			case "digest_binding":
				object.SHA256 = strings.Repeat("0", 64)
			}
			adapter, _ := r2TestAdapter(t, &object, &now, func(w http.ResponseWriter, r *http.Request) { calls++; w.WriteHeader(500) })
			proof, err := adapter.Head(context.Background(), key)
			if err == nil || proof.SHA256Verified {
				t.Fatal("invalid persisted grant permitted completion")
			}
			request, err := adapter.Download(context.Background(), key, now, object.ExpiresAt)
			if err == nil || request.URL != "" {
				t.Fatal("invalid persisted grant yielded URL; suppressed")
			}
			if calls != 0 {
				t.Fatal("invalid grant reached provider")
			}
		})
	}
}

func TestR2HeadMissingRetryAndDelete(t *testing.T) {
	object := r2TestObject(t, "1h")
	now := r2VectorNow
	heads, deletes := 0, 0
	adapter, _ := r2TestAdapter(t, &object, &now, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "DELETE" {
			deletes++
			w.WriteHeader(404)
			return
		}
		heads++
		if heads == 1 {
			w.WriteHeader(503)
			return
		}
		w.WriteHeader(404)
	})
	proof, err := adapter.Head(context.Background(), object.ObjectKey)
	if !errors.Is(err, ErrR2Incomplete) || proof.SHA256Verified || heads != 2 {
		t.Fatal("HEAD transient retry/missing-object mapping failed")
	}
	object.PurgedAt = &now
	now = now.Add(2 * time.Hour)
	for i := 0; i < 2; i++ {
		if err := adapter.Delete(context.Background(), object.ObjectKey); err != nil {
			t.Fatal("delete of absent purged/expired grant must be idempotent")
		}
	}
	if deletes != 2 {
		t.Fatal("delete not sent for cleanup")
	}
}

func TestR2HeadProviderErrorsAndRedirects(t *testing.T) {
	for _, method := range []string{"HEAD", "DELETE"} {
		for _, status := range []int{301, 302, 307, 308, 400, 403, 429, 500} {
			t.Run(fmt.Sprintf("%s_%d", method, status), func(t *testing.T) {
				object := r2TestObject(t, "1h")
				now := r2VectorNow
				redirects := 0
				calls := 0
				target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { redirects++ }))
				defer target.Close()
				adapter, _ := r2TestAdapter(t, &object, &now, func(w http.ResponseWriter, r *http.Request) {
					calls++
					w.Header().Set("Location", target.URL+"/sensitive-synthetic-marker")
					w.WriteHeader(status)
					_, _ = io.WriteString(w, `<Error><Code>AccessDenied</Code><Message>https://invalid.test/?X-Amz-Signature=sensitive-synthetic-marker credential-looking-value</Message></Error>`)
				})
				var err error
				if method == "HEAD" {
					_, err = adapter.Head(context.Background(), object.ObjectKey)
				} else {
					err = adapter.Delete(context.Background(), object.ObjectKey)
				}
				var typed *R2ProviderError
				if err == nil || !errors.As(err, &typed) || strings.Contains(err.Error(), "sensitive-synthetic-marker") || strings.Contains(err.Error(), "credential-looking-value") || strings.Contains(err.Error(), "https:") || strings.Contains(err.Error(), "X-Amz-") {
					t.Fatal("provider error missing typed sanitized mapping")
				}
				if status >= 300 && status < 400 && typed.Code != "redirect_refused" {
					t.Fatal("redirect did not return the sanitized refusal reason")
				}
				if status == 403 && typed.Code != "access_denied" {
					t.Fatal("provider access error code was not mapped")
				}
				if redirects != 0 || calls > 3 {
					t.Fatal("redirect followed or retries unbounded")
				}
			})
		}
	}
}

func TestR2DownloadTTLAndRemint(t *testing.T) {
	for _, class := range []string{"1h", "6h", "12h", "1d", "7d", "30d", "keep"} {
		t.Run(class, func(t *testing.T) {
			object := r2TestObject(t, class)
			object.State = "complete"
			object.SHA256Verified = true
			now := r2VectorNow
			calls := 0
			adapter, _ := r2TestAdapter(t, &object, &now, func(w http.ResponseWriter, r *http.Request) { calls++; w.WriteHeader(500) })
			first, err := adapter.Download(context.Background(), object.ObjectKey, now, object.ExpiresAt)
			if err != nil {
				t.Fatal("first mint failed")
			}
			u, err := url.Parse(first.URL)
			if err != nil {
				t.Fatal("capability parse failed; suppressed")
			}
			deadline, err := time.Parse(time.RFC3339Nano, first.ExpiresAt)
			if err != nil || !deadline.Equal(now.Add(600*time.Second)) || first.Method != "GET" || len(first.Headers) != 0 || u.Query().Get("X-Amz-Expires") != "600" || u.Query().Get("X-Amz-SignedHeaders") != "host" || u.Path != "/vecnet-blobs-test/"+object.ObjectKey {
				t.Fatal("download scope/deadline mismatch; capability suppressed")
			}
			now = now.Add(601 * time.Second)
			second, err := adapter.Download(context.Background(), object.ObjectKey, now, object.ExpiresAt)
			if err != nil || second.URL == first.URL {
				t.Fatal("fresh URL unavailable after earlier deadline; suppressed")
			}
			u, err = url.Parse(second.URL)
			if err != nil || u.Query().Get("X-Amz-Date") != "20261105T031406Z" {
				t.Fatal("new mint did not use supplied clock")
			}
			if class == "keep" {
				if object.ExpiresAt != nil {
					t.Fatal("keep object acquired time expiry")
				}
				if u.Query().Get("X-Amz-Expires") != "600" {
					t.Fatal("keep capability TTL changed")
				}
			} else {
				for _, remaining := range []time.Duration{600*time.Second + 900*time.Millisecond, 1900 * time.Millisecond, time.Second, 999 * time.Millisecond, 0, -time.Second} {
					now = object.ExpiresAt.Add(-remaining)
					req, err := adapter.Download(context.Background(), object.ObjectKey, now, object.ExpiresAt)
					if remaining < time.Second {
						if !errors.Is(err, ErrR2Expired) || req.URL != "" {
							t.Fatal("subsecond or expired object minted capability")
						}
						continue
					}
					u, e := url.Parse(req.URL)
					want := "1"
					if remaining > 600*time.Second {
						want = "600"
					}
					if err != nil || e != nil || u.Query().Get("X-Amz-Expires") != want {
						t.Fatal("finite deadline not floored/capped")
					}
					end, e := time.Parse(time.RFC3339Nano, req.ExpiresAt)
					if e != nil || end.After(*object.ExpiresAt) {
						t.Fatal("capability outlived object")
					}
				}
			}
			if calls != 0 {
				t.Fatal("download mint performed hub GET/HEAD")
			}
		})
	}
}

func TestR2DownloadStateAndDeadlineBinding(t *testing.T) {
	for _, name := range []string{"pending", "unverified", "purged", "wrong_expiry", "cancelled"} {
		t.Run(name, func(t *testing.T) {
			object := r2TestObject(t, "1h")
			object.State = "complete"
			object.SHA256Verified = true
			now := r2VectorNow
			deadline := object.ExpiresAt
			switch name {
			case "pending":
				object.State = "pending"
			case "unverified":
				object.SHA256Verified = false
			case "purged":
				object.PurgedAt = &now
			case "wrong_expiry":
				deadline = nil
			case "cancelled":
				object.State = "cancelled"
			}
			adapter, _ := r2TestAdapter(t, &object, &now, func(w http.ResponseWriter, r *http.Request) { t.Error("mint must not contact provider") })
			req, err := adapter.Download(context.Background(), object.ObjectKey, now, deadline)
			if err == nil || req.URL != "" {
				t.Fatal("unavailable object/deadline override minted URL")
			}
		})
	}
}

func TestR2HeadRechecksStateAfterProviderWait(t *testing.T) {
	for _, purge := range []bool{true, false} {
		t.Run(strconv.FormatBool(purge), func(t *testing.T) {
			object := r2TestObject(t, "1h")
			now := r2VectorNow
			adapter, _ := r2TestAdapter(t, &object, &now, func(w http.ResponseWriter, r *http.Request) {
				for k, vs := range r2ProofHeaders() {
					for _, v := range vs {
						w.Header().Add(k, v)
					}
				}
				if purge {
					object.PurgedAt = &now
				} else {
					now = *object.ExpiresAt
				}
			})
			proof, err := adapter.Head(context.Background(), object.ObjectKey)
			if err == nil || proof.SHA256Verified {
				t.Fatal("late completion revived expired/purged object")
			}
		})
	}
}

type r2TestRoundTripper func(*http.Request) (*http.Response, error)

func (f r2TestRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type r2TestReader struct {
	bytes.Reader
	read int
}

func (r *r2TestReader) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	r.read += n
	return n, err
}

func TestR2HeadDownloadTransportBounds(t *testing.T) {
	for _, name := range []string{"large_error", "malformed_xml", "unknown_code", "transport_error", "large_header", "success_body"} {
		t.Run(name, func(t *testing.T) {
			object := r2TestObject(t, "1h")
			now := r2VectorNow
			reader := &r2TestReader{Reader: *bytes.NewReader([]byte(strings.Repeat("x", 100000)))}
			source := &http.Client{Transport: r2TestRoundTripper(func(r *http.Request) (*http.Response, error) {
				if r.Method != "HEAD" && r.Method != "DELETE" {
					t.Error("hub requested object body")
				}
				status := 400
				headers := http.Header{}
				switch name {
				case "transport_error":
					return nil, errors.New("https://invalid.test/?X-Amz-Signature=sensitive-synthetic-marker credential-looking-value")
				case "large_header":
					headers.Set("X-Amz-Meta-Big", strings.Repeat("x", 20000))
				case "success_body":
					status = 200
					headers = r2ProofHeaders()
				case "malformed_xml":
					reader.Reader = *bytes.NewReader([]byte(`<Error><Code>AccessDenied`))
				case "unknown_code":
					reader.Reader = *bytes.NewReader([]byte(`<Error><Code>sensitive-synthetic-marker</Code></Error>`))
				}
				return &http.Response{StatusCode: status, Header: headers, Body: io.NopCloser(reader), Request: r}, nil
			})}
			jar, _ := cookiejar.New(nil)
			source.Jar = jar
			adapter, err := NewR2Objects(R2ObjectsConfig{Endpoint: r2VectorEndpoint, Bucket: "vecnet-blobs-test", Namespace: r2TestNamespace, AccessID: "VECTORACCESS", Secret: "vector-secret-not-live", Now: func() time.Time { return now }, Lookup: func(context.Context, string) (R2Object, error) { return object, nil }, Client: source})
			if err != nil {
				t.Fatal("injected transport rejected")
			}
			if adapter.client.Jar != nil || source.Jar == nil || source.CheckRedirect != nil {
				t.Fatal("private client leaked cookies or mutated injected client")
			}
			if name == "success_body" {
				proof, err := adapter.Head(context.Background(), object.ObjectKey)
				if err != nil || !proof.SHA256Verified || reader.read != 0 {
					t.Fatal("HEAD read body or valid proof failed")
				}
			} else {
				err := adapter.Delete(context.Background(), object.ObjectKey)
				var typed *R2ProviderError
				if err == nil || !errors.As(err, &typed) || strings.Contains(err.Error(), "sensitive-synthetic-marker") || strings.Contains(err.Error(), "https:") || strings.Contains(err.Error(), "X-Amz-") {
					t.Fatal("untrusted error escaped sanitization")
				}
				if reader.read > 16385 {
					t.Fatal("provider error read exceeded bound")
				}
			}
		})
	}
}

// This verifier independently reconstructs the fake S3 request signature from
// the received method/path/headers; it does not call production signer helpers.
func r2TestPrivateValid(r *http.Request) bool {
	date := "20261105"
	stamp := "20261105T030405Z"
	scope := date + "/auto/s3/aws4_request"
	signedHeaders := "host;x-amz-content-sha256;x-amz-date"
	headers := "host:" + r.Host + "\nx-amz-content-sha256:" + r.Header.Get("X-Amz-Content-Sha256") + "\nx-amz-date:" + r.Header.Get("X-Amz-Date") + "\n"
	if r.Method == "HEAD" {
		signedHeaders = "host;x-amz-checksum-mode;x-amz-content-sha256;x-amz-date"
		headers = "host:" + r.Host + "\nx-amz-checksum-mode:" + r.Header.Get("X-Amz-Checksum-Mode") + "\nx-amz-content-sha256:" + r.Header.Get("X-Amz-Content-Sha256") + "\nx-amz-date:" + r.Header.Get("X-Amz-Date") + "\n"
	}
	canonical := r.Method + "\n" + r.URL.EscapedPath() + "\n\n" + headers + "\n" + signedHeaders + "\nUNSIGNED-PAYLOAD"
	digest := sha256.Sum256([]byte(canonical))
	sign := func(key []byte, value string) []byte {
		hash := hmac.New(sha256.New, key)
		hash.Write([]byte(value))
		return hash.Sum(nil)
	}
	key := []byte("AWS4vector-secret-not-live")
	for _, part := range []string{date, "auto", "s3", "aws4_request"} {
		key = sign(key, part)
	}
	signature := hex.EncodeToString(sign(key, "AWS4-HMAC-SHA256\n"+stamp+"\n"+scope+"\n"+hex.EncodeToString(digest[:])))
	expected := "AWS4-HMAC-SHA256 Credential=VECTORACCESS/" + scope + ", SignedHeaders=" + signedHeaders + ", Signature=" + signature
	return hmac.Equal([]byte(expected), []byte(r.Header.Get("Authorization"))) && r.URL.RawQuery == ""
}

func TestR2HeadProofPrivateSignatureTampering(t *testing.T) {
	for _, name := range []string{"valid", "method", "path", "checksum_mode", "date", "payload_hash"} {
		t.Run(name, func(t *testing.T) {
			object := r2TestObject(t, "1h")
			now := r2VectorNow
			calls := 0
			adapter, server := r2TestAdapter(t, &object, &now, func(w http.ResponseWriter, r *http.Request) {
				calls++
				if !r2TestPrivateValid(r) {
					w.WriteHeader(403)
					return
				}
				for k, vs := range r2ProofHeaders() {
					for _, v := range vs {
						w.Header().Add(k, v)
					}
				}
			})
			underlying := server.Client().Transport
			adapter.client.Transport = r2TestRoundTripper(func(r *http.Request) (*http.Response, error) {
				request := r.Clone(r.Context())
				switch name {
				case "method":
					request.Method = "DELETE"
				case "path":
					request.URL.Path += "-changed"
				case "checksum_mode":
					request.Header.Set("X-Amz-Checksum-Mode", "DISABLED")
				case "date":
					request.Header.Set("X-Amz-Date", "20261105T030406Z")
				case "payload_hash":
					request.Header.Set("X-Amz-Content-Sha256", strings.Repeat("0", 64))
				}
				return underlying.RoundTrip(request)
			})
			proof, err := adapter.Head(context.Background(), object.ObjectKey)
			if name == "valid" {
				if err != nil || !proof.SHA256Verified {
					t.Fatal("independently verified signature rejected")
				}
			} else if err == nil || proof.SHA256Verified {
				t.Fatal("tampered private request accepted")
			}
			if calls != 1 {
				t.Fatal("private signature test did not reach fake exactly once")
			}
		})
	}
}
