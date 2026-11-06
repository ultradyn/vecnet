package mcpserver_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/blob"
	"github.com/XertroV/vecnet/internal/hub"
	"github.com/XertroV/vecnet/internal/logging"
	"github.com/XertroV/vecnet/internal/testhub"
)

const (
	r2GuideSentence = "URLs are short-lived; call get again whenever you need the file, as long as it hasn't expired."
	r2FakeAccount   = "00000000000000000000000000000000"
	r2FakeOrigin    = "https://" + r2FakeAccount + ".r2.cloudflarestorage.com"
)

func TestR2BlobToolDescriptions(t *testing.T) {
	env := testhub.Start(t)
	_, secret := env.NewPeer("session", "reader")
	status, r := rpc(t, env, secret, "tools/list", nil)
	if status != http.StatusOK || r.Error != nil {
		t.Fatalf("tools/list: %d %+v", status, r.Error)
	}
	var listed struct {
		Tools []listedTool `json:"tools"`
	}
	if json.Unmarshal(r.Result, &listed) != nil {
		t.Fatal("tools")
	}
	byName := map[string]listedTool{}
	for _, tool := range listed.Tools {
		byName[tool.Name] = tool
	}
	for _, name := range []string{"vecnet_blob_put", "vecnet_blob_get", "vecnet_blob_status"} {
		tool, ok := byName[name]
		if !ok {
			t.Fatalf("missing %s", name)
		}
		if !strings.Contains(tool.Description, r2GuideSentence) || !strings.Contains(tool.Description, "blob_url_rate_limited") || !strings.Contains(tool.Description, "30") || !strings.Contains(strings.ToLower(tool.Description), "sensitive") {
			t.Fatalf("%s description missing repeatable-get contract: %s", name, tool.Description)
		}
		if strings.Contains(tool.Description, "X-Amz-Signature=") || strings.Contains(tool.Description, "r2.cloudflarestorage.com/") {
			t.Fatalf("%s description contains a live-looking URL", name)
		}
	}
	put := byName["vecnet_blob_put"]
	if !strings.Contains(put.Description, "phase") || !strings.Contains(put.Description, "grant") || !strings.Contains(put.Description, "complete") || !strings.Contains(put.Description, "https://example.invalid/") {
		t.Fatalf("put description: %s", put.Description)
	}
	get := byName["vecnet_blob_get"]
	if !strings.Contains(get.Description, "Range") || !strings.Contains(get.Description, "blob_expired") || !strings.Contains(get.Description, "blob_purged") {
		t.Fatalf("get description: %s", get.Description)
	}
	names := map[string]bool{}
	propertyNames(byName["vecnet_blob_status"].OutputSchema, names)
	for key := range names {
		if strings.Contains(strings.ToLower(key), "url") {
			t.Fatalf("status schema exposes %s", key)
		}
	}
	guide := callTool(t, env, secret, "vecnet_guide", map[string]any{"topic": "start"})
	if guide.IsError || !strings.Contains(guide.Content[0].Text, r2GuideSentence) || !strings.Contains(guide.Content[0].Text, "blob_url_rate_limited") {
		t.Fatal("guide omits the repeatable-get sentence")
	}
}

func TestR2MCPHostTransfersWithoutFlyFileProxy(t *testing.T) {
	f := newR2MCP(t)
	env := f.env
	_, sender := env.NewPeer("session", "sender")
	recipient, reader := env.NewPeer("session", "recipient")
	_, outsider := env.NewPeer("session", "late")
	payload := []byte("abc")
	sum := sha256.Sum256(payload)
	sha := fmt.Sprintf("%x", sum[:])
	logs := bytes.Buffer{}
	previous := slog.Default()
	slog.SetDefault(slog.New(logging.NewHandler(&logs, slog.LevelDebug)))
	t.Cleanup(func() { slog.SetDefault(previous) })

	e := toolFails(t, env, sender, "vecnet_blob_put", map[string]any{"data_base64": "YQ==", "media_type": "text/plain"}, "direct_upload_required")
	if strings.Contains(string(mustJSON(t, e)), "http") || f.hubFileBytes() != 0 {
		t.Fatal("one-byte data_base64 was decoded or proxied")
	}
	if _, r := rpc(t, env, sender, "tools/call", map[string]any{"name": "vecnet_blob_put", "arguments": map[string]any{"data_base64": "%", "media_type": "text/plain"}}); r.Error != nil {
		t.Fatal("invalid base64 was decoded instead of refused before decoding")
	}

	var grant map[string]any
	toolOK(t, env, sender, "vecnet_blob_put", map[string]any{
		"phase": "grant", "size": len(payload), "sha256": sha, "media_type": "application/octet-stream", "idempotency_key": "grant-1",
	}, &grant)
	assertNoSecretLeak(t, mustJSON(t, grant))
	upload := grant["upload"].(map[string]any)
	headers := upload["headers"].(map[string]any)
	if upload["method"] != "PUT" || headers["If-None-Match"] != "*" || headers["Content-Length"] != "3" || headers["X-Amz-Checksum-Sha256"] == "" {
		t.Fatalf("grant headers: %v", upload)
	}
	putURL := upload["url"].(string)
	if secs := queryExpiry(t, putURL); secs != 300 {
		t.Fatalf("PUT TTL %d, want 300", secs)
	}
	if grant["sha256_verified"] != false || grant["retention_class"] != "1d" || grant["expires_at"] == nil {
		t.Fatalf("pending grant trust: %v", grant)
	}
	deadline, err := time.Parse(time.RFC3339Nano, upload["expires_at"].(string))
	if err != nil || deadline.Sub(env.Now.Now()) > 300*time.Second || deadline.Before(env.Now.Now()) {
		t.Fatalf("PUT deadline %v", upload["expires_at"])
	}
	var pending map[string]any
	toolOK(t, env, sender, "vecnet_blob_status", map[string]any{"sha256": sha, "blob_id": grant["blob_id"]}, &pending)
	if pending["sha256_verified"] != false || pending["url"] != nil || strings.Contains(mustJSON(t, pending), "http") {
		t.Fatalf("pending status leaked a capability: %v", pending)
	}
	toolFails(t, env, reader, "vecnet_blob_put", map[string]any{"phase": "complete", "blob_id": grant["blob_id"]}, "forbidden")

	f.hostPut(t, putURL, headers, payload)
	if f.hubFileBytes() != 0 {
		t.Fatalf("hub proxied %d file bytes", f.hubFileBytes())
	}
	var done map[string]any
	toolOK(t, env, sender, "vecnet_blob_put", map[string]any{"phase": "complete", "blob_id": grant["blob_id"]}, &done)
	if done["sha256_verified"] != true || done["url"] != nil || done["integrity"].(map[string]any)["verified"] != true {
		t.Fatalf("complete trust: %v", done)
	}
	toolOK(t, env, sender, "vecnet_send", map[string]any{
		"idempotency_key": "send-r2", "recipients": []string{recipient}, "priority": "hold",
		"content":     map[string]any{"text": "file"},
		"attachments": []any{map[string]any{"blob_id": done["blob_id"], "sha256": sha, "size": len(payload), "media_type": "application/octet-stream", "name": "note.bin", "retention_class": done["retention_class"], "expires_at": done["expires_at"]}},
	}, nil)

	var got map[string]any
	toolOK(t, env, reader, "vecnet_blob_get", map[string]any{"sha256": sha, "blob_id": done["blob_id"]}, &got)
	if got["method"] != "GET" || got["retention_class"] != "1d" || got["sha256_verified"] != true || got["object_expires_at"] == nil {
		t.Fatalf("get grant: %v", got)
	}
	getURL := got["url"].(string)
	if secs := queryExpiry(t, getURL); secs <= 0 || secs > 600 {
		t.Fatalf("GET TTL %d", secs)
	}
	getDeadline, err := time.Parse(time.RFC3339Nano, got["expires_at"].(string))
	objectDeadline, err2 := time.Parse(time.RFC3339Nano, got["object_expires_at"].(string))
	if err != nil || err2 != nil || getDeadline.After(objectDeadline) || getDeadline.Sub(env.Now.Now()) > 600*time.Second {
		t.Fatalf("GET deadlines url=%s object=%s", got["expires_at"], got["object_expires_at"])
	}
	if _, r := rpc(t, env, reader, "tools/call", map[string]any{"name": "vecnet_blob_get", "arguments": map[string]any{"sha256": sha, "blob_id": done["blob_id"], "offset": 0}}); r.Error == nil || !strings.Contains(r.Error.Message, "Range") {
		t.Fatalf("pagination fallback: %+v", r.Error)
	}
	if !bytes.Equal(f.hostGet(t, getURL), payload) || f.hubFileBytes() != 0 {
		t.Fatal("host GET did not see the object, or the hub proxied it")
	}
	env.Now.Advance(601 * time.Second)
	if f.hostGetStatus(t, getURL) == http.StatusOK {
		t.Fatal("expired URL still transferred")
	}
	var again map[string]any
	toolOK(t, env, sender, "vecnet_blob_get", map[string]any{"sha256": sha, "blob_id": done["blob_id"]}, &again)
	if again["url"] == getURL || f.hostGetStatus(t, again["url"].(string)) != http.StatusOK {
		t.Fatal("retained uploader did not receive a fresh URL")
	}
	toolOK(t, env, reader, "vecnet_blob_get", map[string]any{"sha256": sha, "blob_id": done["blob_id"]}, &again)

	f.retarget(t, done["blob_id"].(string), "keep", nil)
	var kept map[string]any
	toolOK(t, env, reader, "vecnet_blob_get", map[string]any{"sha256": sha, "blob_id": done["blob_id"]}, &kept)
	if kept["object_expires_at"] != nil || kept["retention_class"] != "keep" || queryExpiry(t, kept["url"].(string)) != 600 {
		t.Fatalf("keep get: %v", kept)
	}
	for _, class := range []string{"1h", "6h", "12h", "7d", "30d"} {
		f.retarget(t, done["blob_id"].(string), class, nil)
		var view map[string]any
		toolOK(t, env, reader, "vecnet_blob_status", map[string]any{"sha256": sha, "blob_id": done["blob_id"]}, &view)
		if view["retention_class"] != class || view["expires_at"] == nil || strings.Contains(mustJSON(t, view), "http") {
			t.Fatalf("class %s status: %v", class, view)
		}
	}
	f.retarget(t, done["blob_id"].(string), "1h", nil)
	issued := f.issuedAt(t, done["blob_id"].(string))
	env.Now.Advance(issued.Add(time.Hour).Sub(env.Now.Now()) - 2*time.Second)
	var near map[string]any
	toolOK(t, env, reader, "vecnet_blob_get", map[string]any{"sha256": sha, "blob_id": done["blob_id"]}, &near)
	if secs := queryExpiry(t, near["url"].(string)); secs < 1 || secs > 2 {
		t.Fatalf("near-deadline TTL %d", secs)
	}
	env.Now.Advance(3 * time.Second)
	expired := toolFails(t, env, reader, "vecnet_blob_get", map[string]any{"sha256": sha, "blob_id": done["blob_id"]}, "blob_expired")
	if strings.Contains(mustJSON(t, expired), "http") || strings.Contains(mustJSON(t, expired), "url") {
		t.Fatal("expiry returned a URL")
	}
	var state map[string]any
	toolOK(t, env, reader, "vecnet_blob_status", map[string]any{"sha256": sha, "blob_id": done["blob_id"]}, &state)
	if state["state"] != "expired" || strings.Contains(mustJSON(t, state), "http") {
		t.Fatalf("expired status: %v", state)
	}

	f.retarget(t, done["blob_id"].(string), "1d", nil)
	if err := env.Hub.PurgeBlob(context.Background(), sha); err != nil {
		t.Fatal(err)
	}
	purged := toolFails(t, env, reader, "vecnet_blob_get", map[string]any{"sha256": sha, "blob_id": done["blob_id"]}, "blob_purged")
	if strings.Contains(mustJSON(t, purged), "http") {
		t.Fatal("purge returned a URL")
	}
	denied := toolFails(t, env, outsider, "vecnet_blob_get", map[string]any{"sha256": sha, "blob_id": done["blob_id"]}, "forbidden")
	if strings.Contains(mustJSON(t, denied), "http") || strings.Contains(denied.Error.Message, "url") {
		t.Fatal("unauthorized get returned a URL")
	}
	if strings.Contains(logs.String(), "X-Amz-") || strings.Contains(logs.String(), putURL) {
		t.Fatal("logs retained a transfer URL")
	}
}

func TestR2SharedMintLimitAndMetadataSurfaces(t *testing.T) {
	f := newR2MCP(t)
	env := f.env
	_, sender := env.NewPeer("session", "sender")
	recipient, reader := env.NewPeer("session", "recipient")
	payload := []byte("abc")
	sum := sha256.Sum256(payload)
	sha := fmt.Sprintf("%x", sum[:])
	var grant map[string]any
	toolOK(t, env, sender, "vecnet_blob_put", map[string]any{"phase": "grant", "size": 3, "sha256": sha, "media_type": "text/plain", "idempotency_key": "mint"}, &grant)
	f.hostPut(t, grant["upload"].(map[string]any)["url"].(string), grant["upload"].(map[string]any)["headers"].(map[string]any), payload)
	var done map[string]any
	toolOK(t, env, sender, "vecnet_blob_put", map[string]any{"phase": "complete", "blob_id": grant["blob_id"]}, &done)
	id := done["blob_id"].(string)
	toolOK(t, env, sender, "vecnet_send", map[string]any{
		"idempotency_key": "mint-send", "recipients": []string{recipient}, "priority": "hold", "content": map[string]any{"text": "file"},
		"attachments": []any{map[string]any{"blob_id": id, "sha256": sha, "size": 3, "media_type": "text/plain", "retention_class": done["retention_class"], "expires_at": done["expires_at"]}},
	}, nil)
	var threadID string
	if err := env.Hub.DB.QueryRow(`SELECT thr_id FROM messages ORDER BY rowid DESC LIMIT 1`).Scan(&threadID); err != nil {
		t.Fatal(err)
	}
	latePeer, lateSecret := env.NewPeer("session", "from-join")
	if _, err := env.Hub.DB.Exec(`INSERT INTO thread_participants(thr_id,peer_id,joined_at,joined_after_seq) VALUES(?,?,?,5)`, threadID, latePeer, env.Now.Now().Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	late := toolFails(t, env, lateSecret, "vecnet_blob_get", map[string]any{"sha256": sha, "blob_id": id}, "forbidden")
	if strings.Contains(mustJSON(t, late), "http") {
		t.Fatal("from_join late reader received a URL")
	}
	for i := 0; i < 30; i++ {
		secret := reader
		if i%2 == 0 {
			resp, body := env.Do(secret, "GET", "/v1/blobs/"+sha+"/download?blob_id="+id, nil)
			if resp.StatusCode != http.StatusOK || !bytes.Contains(body, []byte(`"url"`)) {
				t.Fatalf("HTTP mint %d: %d %s", i, resp.StatusCode, body)
			}
			continue
		}
		toolOK(t, env, secret, "vecnet_blob_get", map[string]any{"sha256": sha, "blob_id": id}, nil)
	}
	limited := toolFails(t, env, reader, "vecnet_blob_get", map[string]any{"sha256": sha, "blob_id": id}, "blob_url_rate_limited")
	if limited.Error.Details["retry_after_seconds"] == nil || strings.Contains(mustJSON(t, limited), "http") || strings.Contains(mustJSON(t, limited), "X-Amz-") {
		t.Fatalf("rate limit: %s", mustJSON(t, limited))
	}
	resp, body := env.Do(reader, "GET", "/v1/blobs/"+sha+"/download?blob_id="+id, nil)
	if resp.StatusCode != http.StatusTooManyRequests || bytes.Contains(body, []byte("http")) || !bytes.Contains(body, []byte("blob_url_rate_limited")) {
		t.Fatalf("shared HTTP denial: %d %s", resp.StatusCode, body)
	}
	var inbox struct {
		Events []json.RawMessage `json:"events"`
	}
	toolOK(t, env, reader, "vecnet_inbox", map[string]any{}, &inbox)
	var hist map[string]any
	toolOK(t, env, reader, "vecnet_thread", map[string]any{"id": threadID}, &hist)
	_, resource := rpc(t, env, reader, "resources/read", map[string]any{"uri": "vecnet://peer/" + recipient + "/inbox"})
	for _, raw := range []string{mustJSON(t, inbox), mustJSON(t, hist), string(resource.Result)} {
		if strings.Contains(raw, "X-Amz-") || strings.Contains(raw, "blob_url") || strings.Contains(raw, r2FakeOrigin) {
			t.Fatalf("metadata surface carried a transfer URL: %s", raw)
		}
	}
}

func TestR2QuotaExhaustionReturnsNoCapability(t *testing.T) {
	f := newR2MCPQuota(t, 2)
	_, sender := f.env.NewPeer("session", "sender")
	sum := sha256.Sum256([]byte("abc"))
	e := toolFails(t, f.env, sender, "vecnet_blob_put", map[string]any{
		"phase": "grant", "size": 3, "sha256": fmt.Sprintf("%x", sum[:]), "media_type": "text/plain", "idempotency_key": "over",
	}, "blob_storage_quota_exceeded")
	if strings.Contains(mustJSON(t, e), "http") || f.hubFileBytes() != 0 || f.objectCount() != 0 {
		t.Fatal("quota refusal returned a capability")
	}
}

func TestR2DurableSpillRemainsBase64After25h(t *testing.T) {
	f := newR2MCP(t)
	env := f.env
	_, sender := env.NewPeer("session", "sender")
	recipient, reader := env.NewPeer("session", "recipient")
	full := strings.Repeat("x", 5000)
	toolOK(t, env, sender, "vecnet_send", map[string]any{
		"idempotency_key": "spill", "recipients": []string{recipient}, "priority": "hold", "content": map[string]any{"text": full},
	}, nil)
	env.Now.Advance(25 * time.Hour)
	if err := env.Hub.Blobs.Transfers.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(full))
	var page struct {
		Encoding string `json:"encoding"`
		Data     string `json:"data"`
		URL      string `json:"url"`
	}
	toolOK(t, env, reader, "vecnet_blob_get", map[string]any{"sha256": fmt.Sprintf("%x", sum[:])}, &page)
	got, err := base64.StdEncoding.DecodeString(page.Data)
	if err != nil || page.Encoding != "base64" || string(got) != full || page.URL != "" {
		t.Fatal("durable spill lost its base64 read")
	}
}

type r2MCP struct {
	env    *testhub.Env
	host   *http.Client
	mu     sync.Mutex
	bodies map[string][]byte
	heads  map[string]http.Header
	files  int64
}

type countingTransport struct {
	base http.RoundTripper
	n    *int64
}

func (t countingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.Method == http.MethodPut || r.Method == http.MethodGet {
		*t.n += r.ContentLength
		if *t.n < 0 {
			*t.n = 0
		}
	}
	return t.base.RoundTrip(r)
}

type r2Rewrite struct {
	base   http.RoundTripper
	target *url.URL
}

func (t r2Rewrite) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Host != strings.TrimPrefix(r2FakeOrigin, "https://") {
		return nil, fmt.Errorf("unexpected provider host")
	}
	clone := r.Clone(r.Context())
	u := *r.URL
	clone.URL = &u
	clone.URL.Scheme = t.target.Scheme
	clone.URL.Host = t.target.Host
	clone.Host = r.Host
	return t.base.RoundTrip(clone)
}

func newR2MCP(t *testing.T) *r2MCP { return newR2MCPQuota(t, 5<<30) }

func newR2MCPQuota(t *testing.T, quota int64) *r2MCP {
	t.Helper()
	f := &r2MCP{bodies: map[string][]byte{}, heads: map[string]http.Header{}}
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") || r.Header.Get("Cookie") != "" {
			t.Error("hub credential reached fake S3")
			w.WriteHeader(http.StatusForbidden)
			return
		}
		if sig := r.URL.Query().Get("X-Amz-Signature"); sig != "" && f.env != nil {
			issued, err := time.Parse("20060102T150405Z", r.URL.Query().Get("X-Amz-Date"))
			secs, conv := strconv.Atoi(r.URL.Query().Get("X-Amz-Expires"))
			now := f.env.Now.Now()
			if err != nil || conv != nil || !now.Before(issued.Add(time.Duration(secs)*time.Second)) {
				w.WriteHeader(http.StatusForbidden)
				return
			}
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		switch r.Method {
		case http.MethodPut:
			body, err := io.ReadAll(io.LimitReader(r.Body, 26<<20))
			if err != nil || r.Header.Get("If-None-Match") != "*" || r.Header.Get("Content-Length") != strconv.Itoa(len(body)) {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			sum := sha256.Sum256(body)
			if r.Header.Get("X-Amz-Checksum-Sha256") != base64.StdEncoding.EncodeToString(sum[:]) {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			if _, exists := f.bodies[r.URL.Path]; exists {
				w.WriteHeader(http.StatusPreconditionFailed)
				return
			}
			h := http.Header{}
			h.Set("Content-Length", strconv.Itoa(len(body)))
			h.Set("Content-Type", "application/octet-stream")
			h.Set("Content-Disposition", "attachment")
			h.Set("Cache-Control", "private, no-store")
			h.Set("X-Amz-Checksum-Sha256", r.Header.Get("X-Amz-Checksum-Sha256"))
			h.Set("X-Amz-Meta-Vecnet-Checksum-Profile", r.Header.Get("X-Amz-Meta-Vecnet-Checksum-Profile"))
			h.Set("X-Amz-Meta-Vecnet-Checksum", r.Header.Get("X-Amz-Meta-Vecnet-Checksum"))
			h.Set("ETag", `"fake-etag"`)
			f.bodies[r.URL.Path] = append([]byte(nil), body...)
			f.heads[r.URL.Path] = h
			w.WriteHeader(http.StatusOK)
		case http.MethodHead, http.MethodGet:
			h, ok := f.heads[r.URL.Path]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			for key, values := range h {
				w.Header()[key] = values
			}
			w.WriteHeader(http.StatusOK)
			if r.Method == http.MethodGet {
				_, _ = w.Write(f.bodies[r.URL.Path])
			}
		case http.MethodDelete:
			delete(f.bodies, r.URL.Path)
			delete(f.heads, r.URL.Path)
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	t.Cleanup(srv.Close)
	target, _ := url.Parse(srv.URL)
	tlsTransport := srv.Client().Transport
	rewrite := r2Rewrite{base: tlsTransport, target: target}
	hubClient := &http.Client{Transport: countingTransport{base: rewrite, n: &f.files}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	f.host = &http.Client{Transport: rewrite, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	f.env = testhub.StartWith(t, func(c *hub.Config) {
		c.BlobBackend = "r2"
		c.R2 = hub.R2Config{AccountID: r2FakeAccount, Bucket: "test-bucket", ChecksumProfile: "sha256", StorageQuotaBytes: quota, AccessKeyID: "FAKEACCESS", SecretAccessKey: "fake-signing-secret", Client: hubClient}
	})
	return f
}

func (f *r2MCP) hubFileBytes() int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.files
}
func (f *r2MCP) objectCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.bodies)
}
func (f *r2MCP) hostPut(t *testing.T, raw string, headers map[string]any, body []byte) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPut, raw, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	for key, value := range headers {
		if key != "Content-Length" {
			req.Header.Set(key, fmt.Sprint(value))
		}
	}
	req.ContentLength = int64(len(body))
	req.Header.Set("X-Test-Host", "1")
	resp, err := f.host.Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		status := 0
		if resp != nil {
			status = resp.StatusCode
		}
		t.Fatalf("host PUT status %d (%v)", status, err)
	}
	resp.Body.Close()
}
func (f *r2MCP) hostGet(t *testing.T, raw string) []byte {
	t.Helper()
	resp, err := f.host.Do(mustRequest(t, http.MethodGet, raw))
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("host GET failed: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return body
}
func (f *r2MCP) hostGetStatus(t *testing.T, raw string) int {
	t.Helper()
	resp, err := f.host.Do(mustRequest(t, http.MethodGet, raw))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}
func mustRequest(t *testing.T, method, raw string) *http.Request {
	t.Helper()
	req, err := http.NewRequest(method, raw, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Test-Host", "1")
	return req
}
func (f *r2MCP) retarget(t *testing.T, id, class string, expiry *time.Time) {
	t.Helper()
	var key, issuedRaw string
	if err := f.env.Hub.DB.QueryRow(`SELECT object_key,issued_at FROM blob_objects WHERE blob_id=?`, id).Scan(&key, &issuedRaw); err != nil {
		t.Fatal(err)
	}
	issued, err := time.Parse(time.RFC3339Nano, issuedRaw)
	if err != nil {
		t.Fatal(err)
	}
	ns := strings.Split(key, "/")
	namespace := ns[len(ns)-2]
	next, err := blob.R2ObjectKey(namespace, id, class)
	if err != nil {
		t.Fatal(err)
	}
	var expires any
	pref := class
	if class == "keep" {
		pref = "off"
		expires = nil
	} else if expiry != nil {
		expires = expiry.UTC().Format(time.RFC3339Nano)
	} else {
		duration := map[string]time.Duration{"1h": time.Hour, "6h": 6 * time.Hour, "12h": 12 * time.Hour, "1d": 24 * time.Hour, "7d": 7 * 24 * time.Hour, "30d": 30 * 24 * time.Hour}[class]
		expires = issued.Add(duration).UTC().Format(time.RFC3339Nano)
	}
	if _, err = f.env.Hub.DB.Exec(`UPDATE blob_objects SET retention_class=?, expiry_pref_value=?, expires_at=?, object_key=? WHERE blob_id=?`, class, pref, expires, next, id); err != nil {
		t.Fatal(err)
	}
	oldPath, newPath := "/test-bucket/"+key, "/test-bucket/"+next
	f.mu.Lock()
	f.bodies[newPath] = f.bodies[oldPath]
	f.heads[newPath] = f.heads[oldPath]
	f.mu.Unlock()
}
func (f *r2MCP) issuedAt(t *testing.T, id string) time.Time {
	t.Helper()
	var raw string
	if err := f.env.Hub.DB.QueryRow(`SELECT issued_at FROM blob_objects WHERE blob_id=?`, id).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	issued, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		t.Fatal(err)
	}
	return issued
}

func queryExpiry(t *testing.T, raw string) int {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil || u.Host != strings.TrimPrefix(r2FakeOrigin, "https://") || u.Query().Get("X-Amz-Signature") == "" {
		t.Fatalf("transfer URL is not a signed fake-S3 capability")
	}
	secs, err := strconv.Atoi(u.Query().Get("X-Amz-Expires"))
	if err != nil {
		t.Fatal(err)
	}
	return secs
}
func assertNoSecretLeak(t *testing.T, raw string) {
	t.Helper()
	if strings.Contains(raw, "fake-signing-secret") {
		t.Fatal("result exposed the signing secret")
	}
}
