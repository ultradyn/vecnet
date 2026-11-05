package httpapi_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/auth"
	"github.com/XertroV/vecnet/internal/blobwire"
	"github.com/XertroV/vecnet/internal/hub"
	"github.com/XertroV/vecnet/internal/logging"
	"github.com/XertroV/vecnet/internal/testhub"
)

const fakeAccount = "00000000000000000000000000000000"
const fakeOrigin = "https://" + fakeAccount + ".r2.cloudflarestorage.com"

type r2Transport struct {
	base   http.RoundTripper
	target *url.URL
}

func (t r2Transport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Scheme != "https" || r.URL.Host != strings.TrimPrefix(fakeOrigin, "https://") {
		return nil, fmt.Errorf("unexpected fake provider destination")
	}
	clone := r.Clone(r.Context())
	u := *r.URL
	clone.URL = &u
	clone.Host = r.URL.Host
	clone.URL.Scheme = t.target.Scheme
	clone.URL.Host = t.target.Host
	return t.base.RoundTrip(clone)
}

type fakeR2Object struct {
	body    []byte
	headers http.Header
}
type r2HTTPFixture struct {
	env         *testhub.Env
	client      *http.Client
	mu          sync.Mutex
	objects     map[string]fakeR2Object
	calls       map[string]int
	headFailure bool
	headWait    func()
}

func newR2HTTP(t *testing.T) *r2HTTPFixture {
	t.Helper()
	f := &r2HTTPFixture{objects: map[string]fakeR2Object{}, calls: map[string]int{}}
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.calls[r.Method]++
		fail, wait := f.headFailure, f.headWait
		f.mu.Unlock()
		if !verifyFakeSig(r, f.env.Now.Now()) {
			w.WriteHeader(403)
			return
		}
		if r.Header.Get("Cookie") != "" || strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
			t.Error("hub credential reached fake S3")
			w.WriteHeader(403)
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" && origin != f.env.URL {
			w.WriteHeader(403)
			return
		}
		if r.Method == "HEAD" && wait != nil {
			wait()
		}
		if r.Method == "HEAD" && fail {
			w.WriteHeader(503)
			return
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		object, exists := f.objects[r.URL.Path]
		switch r.Method {
		case "PUT":
			data, err := io.ReadAll(io.LimitReader(r.Body, 26<<20))
			if err != nil {
				w.WriteHeader(400)
				return
			}
			sum := sha256.Sum256(data)
			if r.ContentLength != int64(len(data)) || base64.StdEncoding.EncodeToString(sum[:]) != r.Header.Get("X-Amz-Checksum-Sha256") {
				w.WriteHeader(400)
				return
			}
			if r.Header.Get("If-None-Match") != "*" || exists {
				w.WriteHeader(412)
				return
			}
			h := r.Header.Clone()
			h.Set("Content-Length", strconv.Itoa(len(data)))
			h.Set("ETag", `"test-etag"`)
			f.objects[r.URL.Path] = fakeR2Object{data, h}
			w.WriteHeader(200)
		case "HEAD", "GET":
			if !exists {
				w.WriteHeader(404)
				return
			}
			for _, key := range []string{"Content-Length", "Content-Type", "Content-Disposition", "Cache-Control", "ETag", "X-Amz-Checksum-Sha256", "X-Amz-Meta-Vecnet-Checksum", "X-Amz-Meta-Vecnet-Checksum-Profile"} {
				w.Header().Set(key, object.headers.Get(key))
			}
			w.WriteHeader(200)
			if r.Method == "GET" {
				_, _ = w.Write(object.body)
			}
		case "DELETE":
			delete(f.objects, r.URL.Path)
			w.WriteHeader(204)
		default:
			w.WriteHeader(405)
		}
	}))
	t.Cleanup(srv.Close)
	target, _ := url.Parse(srv.URL)
	client := srv.Client()
	client.Transport = r2Transport{base: client.Transport, target: target}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	f.client = client
	f.env = testhub.StartWith(t, func(c *hub.Config) {
		c.BlobBackend = "r2"
		c.R2 = hub.R2Config{AccountID: fakeAccount, Bucket: "test-bucket", ChecksumProfile: "sha256", StorageQuotaBytes: 100, AccessKeyID: "FAKEACCESS", SecretAccessKey: "fake-signing-secret", Client: client}
	})
	if len(f.calls) != 0 {
		t.Fatal("startup contacted fake S3")
	}
	return f
}
func fakeMAC(key []byte, value string) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(value))
	return m.Sum(nil)
}

// Independent SigV4 verifier: shared test fake, not provider acceptance. It
// reconstructs canonical requests from actual HTTP headers, never hub helpers.
func verifyFakeSig(r *http.Request, now time.Time) bool {
	q := r.URL.Query()
	stamp := q.Get("X-Amz-Date")
	credential := q.Get("X-Amz-Credential")
	names := q.Get("X-Amz-SignedHeaders")
	signature := q.Get("X-Amz-Signature")
	presigned := signature != ""
	canonicalQuery := ""
	if presigned {
		seconds, err := strconv.Atoi(q.Get("X-Amz-Expires"))
		issued, e := time.Parse("20060102T150405Z", stamp)
		if err != nil || e != nil || now.Before(issued) || !now.Before(issued.Add(time.Duration(seconds)*time.Second)) {
			return false
		}
		q.Del("X-Amz-Signature")
		canonicalQuery = strings.ReplaceAll(q.Encode(), "+", "%20")
	} else {
		stamp = r.Header.Get("X-Amz-Date")
		authorization := strings.TrimPrefix(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256 ")
		for _, field := range strings.Split(authorization, ", ") {
			key, value, _ := strings.Cut(field, "=")
			switch key {
			case "Credential":
				credential = value
			case "SignedHeaders":
				names = value
			case "Signature":
				signature = value
			}
		}
	}
	access, scope, ok := strings.Cut(credential, "/")
	if !ok || access != "FAKEACCESS" || len(stamp) < 8 || scope != stamp[:8]+"/auto/s3/aws4_request" {
		return false
	}
	var headers strings.Builder
	for _, key := range strings.Split(names, ";") {
		value := r.Header.Get(key)
		if key == "host" {
			value = r.Host
		}
		if key == "content-length" {
			value = strconv.FormatInt(r.ContentLength, 10)
		}
		headers.WriteString(key + ":" + strings.Join(strings.Fields(value), " ") + "\n")
	}
	canonical := strings.Join([]string{r.Method, r.URL.EscapedPath(), canonicalQuery, headers.String(), names, "UNSIGNED-PAYLOAD"}, "\n")
	digest := sha256.Sum256([]byte(canonical))
	toSign := "AWS4-HMAC-SHA256\n" + stamp + "\n" + scope + "\n" + hex.EncodeToString(digest[:])
	key := fakeMAC([]byte("AWS4fake-signing-secret"), stamp[:8])
	key = fakeMAC(key, "auto")
	key = fakeMAC(key, "s3")
	key = fakeMAC(key, "aws4_request")
	return hmac.Equal([]byte(signature), []byte(hex.EncodeToString(fakeMAC(key, toSign))))
}
func (f *r2HTTPFixture) request(t *testing.T, secret, method, path string, body any, idem string) (*http.Response, []byte) {
	t.Helper()
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal("request JSON failed")
		}
		reader = strings.NewReader(string(raw))
	}
	req, _ := http.NewRequest(method, f.env.URL+path, reader)
	if secret != "" {
		req.Header.Set("Authorization", "Bearer "+secret)
	}
	req.Header.Set("Content-Type", "application/json")
	if idem != "" {
		req.Header.Set("Idempotency-Key", idem)
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal("hub request failed")
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal("hub response failed")
	}
	return resp, raw
}
func (f *r2HTTPFixture) upload(t *testing.T, secret, key string, contents ...string) blobwire.UploadGrant {
	t.Helper()
	data := "abc"
	if len(contents) > 0 {
		data = contents[0]
	}
	in := blobwire.UploadRequest{Size: int64(len(data)), SHA256: shaOf(data), MediaType: "text/plain"}
	resp, raw := f.request(t, secret, "POST", "/v1/blobs/uploads", in, key)
	if resp.StatusCode != 201 {
		t.Fatalf("upload grant status %d", resp.StatusCode)
	}
	var g blobwire.UploadGrant
	if json.Unmarshal(raw, &g) != nil {
		t.Fatal("invalid grant")
	}
	if g.SHA256Verified == nil || *g.SHA256Verified || g.Integrity == nil || g.Integrity.Verified {
		t.Fatal("pending upload marked verified")
	}
	req, err := http.NewRequest("PUT", g.Upload.URL, strings.NewReader(data))
	if err != nil {
		t.Fatal("invalid upload URL")
	}
	for k, v := range g.Upload.Headers {
		if k != "Content-Length" {
			req.Header.Set(k, v)
		}
	}
	resp, err = f.client.Do(req)
	if err != nil {
		t.Fatal("direct fake S3 upload failed")
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("fake S3 rejected signed upload: %d", resp.StatusCode)
	}
	resp, raw = f.request(t, secret, "POST", g.CompleteURL, map[string]any{}, "")
	if resp.StatusCode != 200 {
		t.Fatalf("completion status %d", resp.StatusCode)
	}
	var ref blobwire.Ref
	if json.Unmarshal(raw, &ref) != nil || ref.SHA256Verified == nil || !*ref.SHA256Verified || ref.Integrity == nil || !ref.Integrity.Verified {
		t.Fatal("completion lacks SHA proof")
	}
	return g
}
func (f *r2HTTPFixture) download(t *testing.T, secret string, g blobwire.UploadGrant) blobwire.DownloadGrant {
	t.Helper()
	f.mu.Lock()
	before := f.calls["GET"]
	f.mu.Unlock()
	resp, raw := f.request(t, secret, "GET", "/v1/blobs/"+g.SHA256+"/download?blob_id="+g.BlobID, nil, "")
	f.mu.Lock()
	after := f.calls["GET"]
	f.mu.Unlock()
	if after != before {
		t.Fatal("hub fetched object bytes while minting")
	}

	if resp.StatusCode != 200 {
		t.Fatalf("download grant status %d", resp.StatusCode)
	}
	var grant blobwire.DownloadGrant
	if json.Unmarshal(raw, &grant) != nil || grant.URL == "" || resp.Header.Get("Cache-Control") != "no-store" {
		t.Fatal("bad download grant")
	}
	req, err := http.NewRequest("GET", grant.URL, nil)
	if err != nil {
		t.Fatal("bad direct GET URL")
	}
	resp, err = f.client.Do(req)
	if err != nil {
		t.Fatal("direct GET failed")
	}
	data, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || string(data) != "abc" {
		t.Fatal("direct fake S3 GET mismatch")
	}
	return grant
}
func TestR2HTTPGrantFlowAuthorizationAndRemint(t *testing.T) {
	f := newR2HTTP(t)
	env := f.env
	a, secret := env.NewPeer("session", "uploader")
	b, bSecret := env.NewPeer("session", "recipient")
	_, stranger := env.NewPeer("session", "stranger")
	g := f.upload(t, secret, "flow")
	first := f.download(t, secret, g) // uploader before first attachment
	path := "/v1/blobs/" + g.SHA256 + "/download?blob_id=" + g.BlobID
	resp, raw := f.request(t, stranger, "GET", path, nil, "")
	if resp.StatusCode != 403 {
		t.Fatal("hash guessing disclosed object")
	}
	requireCode(t, raw, "forbidden")
	if strings.Contains(string(raw), "size") || strings.Contains(string(raw), "expires_at") {
		t.Fatal("unauthorised metadata leak")
	}
	att := map[string]any{"blob_id": g.BlobID, "sha256": g.SHA256, "size": 3, "media_type": "text/plain", "retention_class": g.RetentionClass, "expires_at": g.ExpiresAt}
	body := map[string]any{"idempotency_key": "attach", "priority": "hold", "recipients": []string{b}, "content": map[string]any{"text": "file"}, "attachments": []any{att}}
	resp, raw = f.request(t, stranger, "POST", "/v1/messages", body, "")
	if resp.StatusCode != 422 {
		t.Fatal("unowned send accepted")
	}
	requireCode(t, raw, "unknown_blob")
	resp, raw = f.request(t, secret, "POST", "/v1/messages", body, "")
	if resp.StatusCode != 202 {
		t.Fatalf("owned send status %d", resp.StatusCode)
	}
	f.download(t, bSecret, g)
	env.Now.Advance(601 * time.Second)
	next := f.download(t, secret, g)
	f.download(t, bSecret, g)
	if next.ExpiresAt == first.ExpiresAt {
		t.Fatal("re-mint reused old expiry")
	}
	req, _ := http.NewRequest("GET", first.URL, nil)
	expired, err := f.client.Do(req)
	if err != nil {
		t.Fatal("fake request failed")
	}
	expired.Body.Close()
	if expired.StatusCode != 403 {
		t.Fatal("old capability still usable")
	}
	req, _ = http.NewRequest("GET", next.URL, nil)
	req.Header.Set("Origin", "https://foreign.invalid")
	foreign, err := f.client.Do(req)
	if err != nil {
		t.Fatal("fake request failed")
	}
	foreign.Body.Close()
	if foreign.StatusCode != 403 {
		t.Fatal("fake accepted foreign origin")
	}
	var count int
	if err = env.Hub.DB.QueryRow(`SELECT count(*) FROM blob_refs WHERE sha256=?`, g.SHA256).Scan(&count); err != nil || count != 0 {
		t.Fatal("remote bytes pinned as disk")
	}
	var stored string
	if err = env.Hub.DB.QueryRow(`SELECT body FROM messages WHERE sender=?`, a).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stored, "X-Amz-") || strings.Contains(stored, "blob_url") {
		t.Fatal("message stored capability")
	}
	env.Now.Advance(24 * time.Hour)
	resp, raw = f.request(t, bSecret, "GET", path, nil, "")
	if resp.StatusCode != 410 {
		t.Fatal("logical expiry ignored")
	}
	requireCode(t, raw, "blob_expired")
	att["origin_url"] = "https://unreachable.invalid/fallback"
	body["idempotency_key"] = "expired"
	resp, raw = f.request(t, secret, "POST", "/v1/messages", body, "")
	if resp.StatusCode != 410 {
		t.Fatal("expired ref used origin fallback")
	}
	requireCode(t, raw, "blob_expired")
}
func TestR2HTTPMintBudgetSharedAcrossSurfacesAndCredentials(t *testing.T) {
	f := newR2HTTP(t)
	e := f.env
	peer, secret := e.NewPeer("session", "recipient and uploader")
	_, other := e.NewPeer("session", "other identity")
	grants := []blobwire.UploadGrant{f.upload(t, secret, "limits-a"), f.upload(t, secret, "limits-b", "xyz")}
	if grants[0].SHA256 == grants[1].SHA256 || grants[0].BlobID == grants[1].BlobID {
		t.Fatal("budget fixtures must have distinct identities and hashes")
	}
	// A second live bearer and v2 delivery tokens resolve to the same peer.
	second, hash := auth.NewSecret("peer")
	if _, err := e.Hub.DB.Exec(`INSERT INTO credentials(cred_id,kind,subject_id,secret_hash,created_at) VALUES('second','peer',?,?,?)`, peer, hash, e.Now.Now().Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	caller, err := e.Hub.Auth.Resolve(context.Background(), second)
	if err != nil {
		t.Fatal(err)
	}
	mint := func(i int) {
		t.Helper()
		g := grants[i%2]
		path := "/v1/blobs/" + g.SHA256
		switch i % 4 {
		case 0:
			resp, _ := f.request(t, secret, "GET", path+"/download?blob_id="+g.BlobID, nil, "")
			if resp.StatusCode != 200 {
				t.Fatalf("HTTP mint = %d", resp.StatusCode)
			}
		case 1:
			resp, _ := f.request(t, second, "GET", path+"?b="+g.BlobID, nil, "")
			if resp.StatusCode != 307 {
				t.Fatal("legacy mint failed")
			}
		case 2:
			if _, err := e.Hub.BlobDownload(context.Background(), &caller, "", g.SHA256, g.BlobID); err != nil {
				t.Fatal("common wrapper mint failed")
			}
		case 3:
			token := e.Hub.Blobs.TokenV2(peer, g.SHA256, g.BlobID, e.Now.Now().Add(time.Hour))
			resp, _ := f.request(t, "", "GET", path+"/download?blob_id="+g.BlobID+"&t="+url.QueryEscape(token), nil, "")
			if resp.StatusCode != 200 {
				t.Fatal("recipient-token mint failed")
			}
		}
	}
	denied := func(secret string, g blobwire.UploadGrant, token string, retry int) {
		t.Helper()
		path := "/v1/blobs/" + g.SHA256 + "/download?blob_id=" + g.BlobID
		if token != "" {
			path += "&t=" + url.QueryEscape(token)
		}
		resp, raw := f.request(t, secret, "GET", path, nil, "")
		var result struct {
			Error struct {
				Details struct {
					Retry int `json:"retry_after_seconds"`
				} `json:"details"`
			} `json:"error"`
		}
		if json.Unmarshal(raw, &result) != nil {
			t.Fatal("limit error decode failed")
		}
		if resp.StatusCode != 429 || resp.Header.Get("Retry-After") != strconv.Itoa(retry) || result.Error.Details.Retry != retry || strings.Contains(string(raw), `"url"`) || strings.Contains(string(raw), "X-Amz-") || resp.Header.Get("Location") != "" {
			t.Fatalf("unsafe or incorrect limit response: status=%d retry=%s detail=%d", resp.StatusCode, resp.Header.Get("Retry-After"), result.Error.Details.Retry)
		}
		requireCode(t, raw, "blob_url_rate_limited")
	}
	for i := 0; i < 15; i++ {
		mint(i)
	}
	e.Now.Advance(30 * time.Second)
	for i := 15; i < 30; i++ {
		mint(i)
	}
	token := e.Hub.Blobs.TokenV2(peer, grants[1].SHA256, grants[1].BlobID, e.Now.Now().Add(time.Hour))
	denied(second, grants[0], "", 30)
	denied("", grants[1], token, 30)
	// Another peer has its own budget even for the same content hash.
	otherGrant := f.upload(t, other, "other")
	f.download(t, other, otherGrant)
	e.Now.Advance(29 * time.Second)
	denied(secret, grants[1], "", 1)
	e.Now.Advance(time.Second)
	// Only the 15 t=0 calls expire at t=60. A fixed-window reset would
	// wrongly permit the next call; a per-object bucket would never deny it.
	for i := 30; i < 45; i++ {
		mint(i)
	}
	denied(second, grants[0], "", 30)
	denied("", grants[1], token, 30)
	e.Now.Advance(30 * time.Second)
	for i := 45; i < 60; i++ {
		mint(i)
	}
	denied(secret, grants[0], "", 30)
	var issued, refused int
	if err := e.Hub.DB.QueryRow(`SELECT count(*) FROM blob_usage_events WHERE caller_identity=? AND kind='download_grant_issued'`, peer).Scan(&issued); err != nil {
		t.Fatal(err)
	}
	if err := e.Hub.DB.QueryRow(`SELECT count(*) FROM blob_usage_events WHERE caller_identity=? AND kind='download_grant_rate_limited'`, peer).Scan(&refused); err != nil {
		t.Fatal(err)
	}
	if issued != 60 || refused != 6 {
		t.Fatalf("mint metering = %d issued, %d refused", issued, refused)
	}
}
func TestR2HTTPCompletionWaitRevocationAndProviderErrors(t *testing.T) {
	for _, change := range []string{"disable", "revoke", "purge", "expire", "provider"} {
		t.Run(change, func(t *testing.T) {
			f := newR2HTTP(t)
			e := f.env
			peer, secret := e.NewPeer("session", "uploader")
			// Reserve and upload through the same helper, then inject another HEAD by
			// starting a fresh pending grant (completion may not reuse verified state).
			resp, raw := f.request(t, secret, "POST", "/v1/blobs/uploads", blobwire.UploadRequest{Size: 3, SHA256: shaOf("abc"), MediaType: "text/plain"}, "pending")
			if resp.StatusCode != 201 {
				t.Fatal("reserve failed")
			}
			var g blobwire.UploadGrant
			if json.Unmarshal(raw, &g) != nil {
				t.Fatal("decode failed")
			}
			req, _ := http.NewRequest("PUT", g.Upload.URL, strings.NewReader("abc"))
			for k, v := range g.Upload.Headers {
				if k != "Content-Length" {
					req.Header.Set(k, v)
				}
			}
			put, err := f.client.Do(req)
			if err != nil {
				t.Fatal("PUT failed")
			}
			put.Body.Close()
			if put.StatusCode != 200 {
				t.Fatal("signed PUT rejected")
			}
			f.mu.Lock()
			f.headWait = func() {
				switch change {
				case "disable":
					_, err = e.Hub.DB.Exec(`UPDATE peers SET disabled=1 WHERE peer_id=?`, peer)
				case "revoke":
					_, err = e.Hub.DB.Exec(`UPDATE credentials SET revoked_at=? WHERE subject_id=?`, e.Now.Now().Format(time.RFC3339Nano), peer)
				case "purge":
					err = e.Hub.PurgeBlob(context.Background(), g.SHA256)
				case "expire":
					e.Now.Advance(24 * time.Hour)
				}
			}
			f.headFailure = change == "provider"
			f.mu.Unlock()
			resp, raw = f.request(t, secret, "POST", g.CompleteURL, map[string]any{}, "")
			if err != nil {
				t.Fatal("state change failed")
			}
			if resp.StatusCode == 200 || strings.Contains(string(raw), "X-Amz-") || strings.Contains(string(raw), "url\":") {
				t.Fatalf("completion leaked or accepted changed eligibility (%s)", change)
			}
			if change == "provider" {
				requireCode(t, raw, "blob_unavailable")
			}
			f.mu.Lock()
			gets := f.calls["GET"]
			f.mu.Unlock()
			if gets != 0 {
				t.Fatal("hub fetched R2 bytes during completion")
			}
		})
	}
}

func TestR2HTTPVisibilityPrincipalChargingAndState(t *testing.T) {
	f := newR2HTTP(t)
	e := f.env
	_, aSecret := e.NewPeer("session", "uploader")
	b, bSecret := e.NewPeer("session", "recipient")
	late, lateSecret := e.NewPeer("session", "late")
	g := f.upload(t, aSecret, "visibility")
	att := map[string]any{"blob_id": g.BlobID, "sha256": g.SHA256, "size": 3, "media_type": "text/plain", "retention_class": g.RetentionClass, "expires_at": g.ExpiresAt}
	body := map[string]any{"idempotency_key": "first", "priority": "hold", "recipients": []string{b}, "content": map[string]any{"text": "file"}, "attachments": []any{att}}
	resp, raw := f.request(t, aSecret, "POST", "/v1/messages", body, "")
	if resp.StatusCode != 202 {
		t.Fatal("send failed")
	}
	var sent hub.SendResponse
	if json.Unmarshal(raw, &sent) != nil {
		t.Fatal("decode send failed")
	}
	join := map[string]any{"idempotency_key": "join", "thread_id": sent.ThreadID, "priority": "hold", "recipients": []string{b, late}, "content": map[string]any{"text": "late join"}}
	resp, _ = f.request(t, aSecret, "POST", "/v1/messages", join, "")
	if resp.StatusCode != 202 {
		t.Fatal("late join failed")
	}
	path := "/v1/blobs/" + g.SHA256 + "/download?blob_id=" + g.BlobID
	resp, raw = f.request(t, lateSecret, "GET", path, nil, "")
	if resp.StatusCode != 403 {
		t.Fatal("from_join read old attachment")
	}
	requireCode(t, raw, "forbidden")
	f.download(t, bSecret, g)
	// Full history grants the same late participant access to the old reference.
	if _, err := e.Hub.DB.Exec(`UPDATE threads SET history_access='full' WHERE thr_id=?`, sent.ThreadID); err != nil {
		t.Fatal(err)
	}
	f.download(t, lateSecret, g)
	prn, prnSecret := e.NewPrincipal("principal uploader")
	owned := f.upload(t, prnSecret, "principal")
	var uploader string
	if err := e.Hub.DB.QueryRow(`SELECT uploader FROM blob_objects WHERE blob_id=?`, owned.BlobID).Scan(&uploader); err != nil || uploader != prn {
		t.Fatal("principal upload charged wrong identity")
	}
	f.download(t, prnSecret, owned)
	// A principal can explicitly read its enabled peer's original upload.
	var ownerPrn string
	owner, err := e.Hub.Auth.Resolve(context.Background(), aSecret)
	if err != nil {
		t.Fatal(err)
	}
	ownerPrn = owner.PrincipalID
	principalSecret, hash := auth.NewSecret("principal")
	if _, err = e.Hub.DB.Exec(`INSERT INTO credentials(cred_id,kind,subject_id,secret_hash,created_at) VALUES('owner-principal','principal',?,?,?)`, ownerPrn, hash, e.Now.Now().Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	f.download(t, principalSecret, g)
	resp, _ = f.request(t, bSecret, "GET", "/v1/admin/blobs/storage", nil, "")
	if resp.StatusCode != 403 {
		t.Fatal("peer saw aggregate storage")
	}
	resp, raw = f.request(t, e.OperatorSecret, "GET", "/v1/admin/blobs/storage", nil, "")
	if resp.StatusCode != 200 || !strings.Contains(string(raw), `"retained_bytes":6`) {
		t.Fatal("operator storage accounting missing")
	}
	if err = e.Hub.PurgeBlob(context.Background(), g.SHA256); err != nil {
		t.Fatal(err)
	}
	resp, raw = f.request(t, bSecret, "GET", path, nil, "")
	if resp.StatusCode != 410 {
		t.Fatal("purged reference accessible")
	}
	requireCode(t, raw, "blob_purged")
	resp, raw = f.request(t, bSecret, "GET", "/v1/blobs/"+g.SHA256+"/status?blob_id="+g.BlobID, nil, "")
	if resp.StatusCode != 200 || !strings.Contains(string(raw), `"state":"purged"`) {
		t.Fatal("status omitted purge")
	}
}
func TestR2HTTPQuotaRestartStrictBodyAndCapabilities(t *testing.T) {
	f := newR2HTTP(t)
	e := f.env
	_, secret := e.NewPeer("session", "uploader")
	g := f.upload(t, secret, "retained")
	for _, bad := range []any{map[string]any{"size": 3, "sha256": g.SHA256, "media_type": "text/plain", "content_md5": "parked"}, map[string]any{"size": 3, "sha256": g.SHA256, "media_type": "text/plain", "uploader": "other"}, map[string]any{"size": 3.5, "sha256": g.SHA256, "media_type": "text/plain"}} {
		resp, raw := f.request(t, secret, "POST", "/v1/blobs/uploads", bad, "strict")
		if resp.StatusCode != 400 {
			t.Fatal("invalid grant body accepted")
		}
		requireCode(t, raw, "invalid_body")
	}
	resp, raw := f.request(t, secret, "GET", "/v1/blob-capabilities", nil, "")
	if resp.StatusCode != 200 || !strings.Contains(string(raw), `"checksum_profile":"sha256"`) || !strings.Contains(string(raw), `"value":"1d"`) {
		t.Fatal("R2 discovery incorrect")
	}
	recipient, recipientSecret := e.NewPeer("session", "retained recipient")
	attachment := map[string]any{"blob_id": g.BlobID, "sha256": g.SHA256, "size": 3, "media_type": "text/plain", "retention_class": g.RetentionClass, "expires_at": g.ExpiresAt}
	message := map[string]any{"idempotency_key": "before-restart", "priority": "hold", "recipients": []string{recipient}, "content": map[string]any{"text": "retained file"}, "attachments": []any{attachment}}
	sent, _ := f.request(t, secret, "POST", "/v1/messages", message, "")
	if sent.StatusCode != 202 {
		t.Fatal("pre-restart attachment failed")
	}
	// Restart below existing usage must preserve object metadata and references,
	// reject new grants, and still permit downloads without provider writes.
	cfg := e.Hub.Cfg
	cfg.R2.StorageQuotaBytes = 2
	restarted, err := hub.New(e.Hub.DB, cfg, e.Now.Now)
	if err != nil {
		t.Fatal(err)
	}
	c, err := restarted.Auth.Resolve(context.Background(), secret)
	if err != nil {
		t.Fatal(err)
	}
	_, err = restarted.CreateBlobUpload(context.Background(), c, "over-limit", blobwire.UploadRequest{Size: 3, SHA256: g.SHA256, MediaType: "text/plain"})
	var apiErr *hub.APIError
	if !errors.As(err, &apiErr) || apiErr.Code != "blob_storage_quota_exceeded" || apiErr.Status != 429 || apiErr.Details["retry_after_seconds"] == nil {
		t.Fatal("over-limit restart accepted grant")
	}
	if _, err = restarted.BlobDownload(context.Background(), &c, "", g.SHA256, g.BlobID); err != nil {
		t.Fatal("over-limit restart lost read")
	}
	reader, err := restarted.Auth.Resolve(context.Background(), recipientSecret)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = restarted.BlobDownload(context.Background(), &reader, "", g.SHA256, g.BlobID); err != nil {
		t.Fatal("over-limit restart deleted reference access")
	}

	resp, raw = f.request(t, secret, "POST", g.CompleteURL, map[string]any{"url": "https://foreign.invalid"}, "")
	if resp.StatusCode != 400 {
		t.Fatal("completion accepted untrusted receipt")
	}
	requireCode(t, raw, "invalid_body")
	req, _ := http.NewRequest("POST", e.URL+"/v1/blobs/uploads", strings.NewReader(`{}`))
	req.Header.Set("Origin", "https://foreign.invalid")
	req.Header.Set("Authorization", "Bearer "+secret)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal("origin request failed")
	}
	resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Fatal("foreign hub origin accepted")
	}
}
func TestR2HTTPExpectContinueRejectsWithoutStreaming(t *testing.T) {
	f := newR2HTTP(t)
	req, _ := http.NewRequest("PUT", f.env.URL+"/v1/blobs", unreadR2Body{t})
	req.Header.Set("Authorization", "Bearer "+f.env.OperatorSecret)
	req.Header.Set("Expect", "100-continue")
	req.ContentLength = 1 << 20
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.ExpectContinueTimeout = time.Second
	defer transport.CloseIdleConnections()
	resp, err := (&http.Client{Transport: transport}).Do(req)
	if err != nil {
		t.Fatal("expect continue request failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != 409 {
		t.Fatal("legacy streaming upload not refused")
	}
	raw, _ := io.ReadAll(resp.Body)
	requireCode(t, raw, "direct_upload_required")
}

func TestR2PurgeWhileUploadGrantIsLive(t *testing.T) {
	f := newR2HTTP(t)
	e := f.env
	_, secret := e.NewPeer("session", "uploader")
	resp, raw := f.request(t, secret, "POST", "/v1/blobs/uploads", blobwire.UploadRequest{Size: 3, SHA256: shaOf("abc"), MediaType: "text/plain"}, "purge-live")
	if resp.StatusCode != 201 {
		t.Fatal("reserve failed")
	}
	var g blobwire.UploadGrant
	if json.Unmarshal(raw, &g) != nil {
		t.Fatal("grant decode failed")
	}
	if err := e.Hub.PurgeBlob(context.Background(), g.SHA256); err != nil {
		t.Fatal(err)
	}
	// S3 may accept an outstanding presign after purge. Hub tombstones must
	// prevent completion and attachment regardless of that provider replay.
	req, _ := http.NewRequest("PUT", g.Upload.URL, strings.NewReader("abc"))
	for k, v := range g.Upload.Headers {
		if k != "Content-Length" {
			req.Header.Set(k, v)
		}
	}
	put, err := f.client.Do(req)
	if err != nil {
		t.Fatal("direct PUT failed")
	}
	put.Body.Close()
	if put.StatusCode != 200 {
		t.Fatal("fake failed outstanding PUT")
	}
	resp, raw = f.request(t, secret, "POST", g.CompleteURL, map[string]any{}, "")
	if resp.StatusCode != 410 {
		t.Fatal("purged upload completed")
	}
	requireCode(t, raw, "blob_purged")
}
func TestR2GrandfatheredDiskReadStaysLocal(t *testing.T) {
	f := newR2HTTP(t)
	e := f.env
	e.Hub.Cfg.BlobBackend = "disk"
	old := putOK(t, e, e.OperatorSecret, "text/plain", "grandfathered bytes")
	e.Hub.Cfg.BlobBackend = "r2"
	c, err := e.Hub.Auth.Resolve(context.Background(), e.OperatorSecret)
	if err != nil {
		t.Fatal(err)
	}
	grant, err := e.Hub.BlobDownload(context.Background(), &c, "", old.SHA256, "")
	if err != nil || grant.URL != "" || grant.LocalPath != "/v1/blobs/"+old.SHA256 {
		t.Fatal("grandfathered blob did not remain local")
	}
	resp, raw := e.Do(e.OperatorSecret, "GET", grant.LocalPath, nil)
	if resp.StatusCode != 200 || string(raw) != "grandfathered bytes" {
		t.Fatal("grandfathered bytes changed")
	}
	token := e.Hub.Blobs.Token(e.OperatorPeer, old.SHA256, e.Now.Now().Add(time.Hour))
	resp, raw = e.Do("", "HEAD", grant.LocalPath+"?t="+url.QueryEscape(token), nil)
	if resp.StatusCode != 200 || len(raw) != 0 {
		t.Fatal("legacy disk HEAD token compatibility changed")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.calls) != 0 {
		t.Fatal("disk read reached S3")
	}
}
func TestR2TokenQueryRedacted(t *testing.T) {
	f := newR2HTTP(t)
	g := f.upload(t, f.env.OperatorSecret, "token-redaction")
	token := f.env.Hub.Blobs.TokenV2(f.env.OperatorPeer, g.SHA256, g.BlobID, f.env.Now.Now().Add(time.Hour))
	if redacted := logging.Redact("GET /v1/blobs/" + g.SHA256 + "?b=" + g.BlobID + "&t=" + token); strings.Contains(redacted, token) || !strings.Contains(redacted, "rt_[redacted]") {
		t.Fatal("delivery token query not redacted")
	}
}

func TestR2SettingsStorageAndHashOnlyDoNotDisclosePresence(t *testing.T) {
	f := newR2HTTP(t)
	e := f.env
	_, owner := e.NewPeer("session", "owner")
	_, stranger := e.NewPeer("session", "stranger")
	g := f.upload(t, owner, "hash-presence")
	resp, raw := e.Do(e.OperatorSecret, "GET", "/v1/admin/prefs", nil)
	if resp.StatusCode != 200 || !strings.Contains(string(raw), `"storage"`) || !strings.Contains(string(raw), `"retained_bytes":3`) {
		t.Fatal("shared settings omitted storage")
	}
	for _, sha := range []string{g.SHA256, shaOf("unknown")} {
		request := map[string]any{"idempotency_key": sha, "priority": "hold", "recipients": []string{e.OperatorPeer}, "content": map[string]any{"text": "hash guess"}, "attachments": []any{map[string]any{"sha256": sha, "size": 3, "media_type": "text/plain"}}}
		resp, raw = f.request(t, stranger, "POST", "/v1/messages", request, "")
		if resp.StatusCode != 422 {
			t.Fatal("hash-only R2 send disclosed presence")
		}
		requireCode(t, raw, "blob_id_required")
	}
}

func TestR2HTTPHashOnlyDownloadRequiresIDWithoutExistenceLeak(t *testing.T) {
	f := newR2HTTP(t)
	e := f.env
	_, owner := e.NewPeer("session", "owner")
	_, stranger := e.NewPeer("session", "stranger")
	g := f.upload(t, owner, "missing-id")
	for _, sha := range []string{g.SHA256, shaOf("unknown")} {
		resp, raw := f.request(t, stranger, "GET", "/v1/blobs/"+sha+"/download", nil, "")
		if resp.StatusCode != 422 {
			t.Fatal("hash-only download must require an ID")
		}
		requireCode(t, raw, "blob_id_required")
	}
}
func TestR2TokenCannotSelectAnotherIncarnation(t *testing.T) {
	f := newR2HTTP(t)
	e := f.env
	_, first := e.NewPeer("session", "first")
	_, second := e.NewPeer("session", "second")
	recipient, _ := e.NewPeer("session", "recipient")
	a, b := f.upload(t, first, "first"), f.upload(t, second, "second")
	token := e.Hub.Blobs.TokenV2(recipient, a.SHA256, a.BlobID, e.Now.Now().Add(time.Hour))
	path := func(g blobwire.UploadGrant, tok string) string {
		return "/v1/blobs/" + g.SHA256 + "/download?blob_id=" + g.BlobID + "&t=" + url.QueryEscape(tok)
	}
	resp, _ := f.request(t, "", "GET", path(a, token), nil, "")
	if resp.StatusCode != 200 {
		t.Fatal("v2 token refused own incarnation")
	}
	resp, raw := f.request(t, "", "GET", path(b, token), nil, "")
	if resp.StatusCode != 403 {
		t.Fatal("v2 token selected another incarnation")
	}
	requireCode(t, raw, "forbidden")
	v1 := e.Hub.Blobs.Token(recipient, a.SHA256, e.Now.Now().Add(time.Hour))
	for _, g := range []blobwire.UploadGrant{a, b} {
		resp, raw = f.request(t, "", "GET", path(g, v1), nil, "")
		if resp.StatusCode != 403 {
			t.Fatal("disk token authorized R2")
		}
		requireCode(t, raw, "forbidden")
	}
	if _, err := e.Hub.DB.Exec(`UPDATE peers SET disabled=1 WHERE peer_id=?`, recipient); err != nil {
		t.Fatal(err)
	}
	resp, raw = f.request(t, "", "GET", path(a, token), nil, "")
	if resp.StatusCode != 403 {
		t.Fatal("disabled token recipient authorized")
	}
	requireCode(t, raw, "forbidden")
}

func TestR2HTTPReferenceOnlyOriginKeepsProtocolSemantics(t *testing.T) {
	f := newR2HTTP(t)
	e := f.env
	_, owner := e.NewPeer("session", "owner")
	_, stranger := e.NewPeer("session", "stranger")
	g := f.upload(t, owner, "reference-only")
	// PROTOCOL section 11 permits reference-only origins. They confer no R2
	// authority, including when their hash happens to name a hosted object.
	for _, sha := range []string{g.SHA256, shaOf("unknown")} {
		message := map[string]any{"idempotency_key": sha, "priority": "hold", "recipients": []string{e.OperatorPeer}, "content": map[string]any{"text": "external reference"}, "attachments": []any{map[string]any{"sha256": sha, "size": 3, "media_type": "text/plain", "origin_url": "https://never-fetch.invalid/file"}}}
		resp, _ := f.request(t, stranger, "POST", "/v1/messages", message, "")
		if resp.StatusCode != 202 {
			t.Fatal("reference-only origin rejected contrary to protocol")
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.calls["GET"] != 0 {
		t.Fatal("reference-only origin caused byte fetch")
	}
}
