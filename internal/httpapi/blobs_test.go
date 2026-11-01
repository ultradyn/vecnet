package httpapi_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/testhub"
)

type putResp struct {
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

// onlyReader hides bytes.Reader's type so the client cannot learn the length
// and sends the body chunked.
type onlyReader struct{ io.Reader }

// putBlob sends PUT /v1/blobs<query> with ctype ("" sends no Content-Type).
func putBlob(t *testing.T, env *testhub.Env, secret, query, ctype string, body io.Reader) (*http.Response, []byte) {
	t.Helper()
	req, err := http.NewRequest("PUT", env.URL+"/v1/blobs"+query, body)
	if err != nil {
		t.Fatal(err)
	}
	if ctype != "" {
		req.Header.Set("Content-Type", ctype)
	}
	if secret != "" {
		req.Header.Set("Authorization", "Bearer "+secret)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT /v1/blobs: %v", err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp, out
}

func putOK(t *testing.T, env *testhub.Env, secret, ctype, body string) putResp {
	t.Helper()
	resp, raw := putBlob(t, env, secret, "", ctype, strings.NewReader(body))
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT = %d; %s", resp.StatusCode, raw)
	}
	var pr putResp
	if err := json.Unmarshal(raw, &pr); err != nil {
		t.Fatalf("decode: %v; %s", err, raw)
	}
	var keys map[string]any
	_ = json.Unmarshal(raw, &keys)
	if len(keys) != 2 {
		t.Fatalf("PUT body %s, want exactly {sha256,size}", raw)
	}
	return pr
}

func shaOf(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

func TestHTTPPutGet(t *testing.T) {
	env := testhub.Start(t)
	_, aSecret := env.NewPeer("session", "A")
	b, _ := env.NewPeer("session", "B")

	const content = "hello, blob world"
	pr := putOK(t, env, aSecret, "text/plain; charset=utf-8", content)
	if pr.SHA256 != shaOf(content) || pr.Size != int64(len(content)) {
		t.Fatalf("PUT = %+v, want sha %s size %d", pr, shaOf(content), len(content))
	}

	tok := env.Hub.Blobs.Token(b, pr.SHA256, env.Now.Now().Add(time.Hour))
	resp, body := env.Do("", "GET", "/v1/blobs/"+pr.SHA256+"?t="+tok, nil)
	if resp.StatusCode != http.StatusOK || string(body) != content {
		t.Fatalf("GET ?t= = %d %q, want 200 %q", resp.StatusCode, body, content)
	}
	if got := resp.Header.Get("Content-Type"); got != "text/plain; charset=utf-8" {
		t.Fatalf("Content-Type = %q", got)
	}
	if got := resp.Header.Get("Content-Length"); got != strconv.Itoa(len(content)) {
		t.Fatalf("Content-Length = %q", got)
	}
	if got := resp.Header.Get("ETag"); got != `"`+pr.SHA256+`"` {
		t.Fatalf("ETag = %q", got)
	}
	// Uploaded bytes are untrusted: never sniffed or run as the hub's origin.
	if got := resp.Header.Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("X-Content-Type-Options = %q", got)
	}
	if got := resp.Header.Get("Content-Security-Policy"); !strings.Contains(got, "sandbox") {
		t.Fatalf("Content-Security-Policy = %q, want sandbox", got)
	}

	resp, body = env.Do("", "GET", "/v1/blobs/"+pr.SHA256, nil)
	mustErr(t, resp, body, http.StatusUnauthorized, "unauthenticated")
	resp, body = putBlob(t, env, "", "", "text/plain", strings.NewReader("x"))
	mustErr(t, resp, body, http.StatusUnauthorized, "unauthenticated")

	// No Content-Type → application/octet-stream.
	pr2 := putOK(t, env, aSecret, "", "raw bytes")
	tok2 := env.Hub.Blobs.Token(b, pr2.SHA256, env.Now.Now().Add(time.Hour))
	resp, _ = env.Do("", "GET", "/v1/blobs/"+pr2.SHA256+"?t="+tok2, nil)
	if got := resp.Header.Get("Content-Type"); got != "application/octet-stream" {
		t.Fatalf("default Content-Type = %q", got)
	}
}

func TestHTTPBlobTokenRules(t *testing.T) {
	env := testhub.Start(t)
	_, aSecret := env.NewPeer("session", "A")
	b, _ := env.NewPeer("session", "B")
	pr := putOK(t, env, aSecret, "text/plain", "token rules")
	other := putOK(t, env, aSecret, "text/plain", "other blob")
	exp := env.Now.Now().Add(time.Hour)

	// A token for another hash does not open this one.
	resp, body := env.Do("", "GET", "/v1/blobs/"+pr.SHA256+"?t="+env.Hub.Blobs.Token(b, other.SHA256, exp), nil)
	mustErr(t, resp, body, http.StatusForbidden, "forbidden")
	// Garbage and expired tokens.
	resp, body = env.Do("", "GET", "/v1/blobs/"+pr.SHA256+"?t=rt_garbage", nil)
	mustErr(t, resp, body, http.StatusForbidden, "forbidden")
	tok := env.Hub.Blobs.Token(b, pr.SHA256, exp)
	env.Now.Advance(time.Hour)
	resp, body = env.Do("", "GET", "/v1/blobs/"+pr.SHA256+"?t="+tok, nil)
	mustErr(t, resp, body, http.StatusForbidden, "forbidden")
	// A malformed hash is unknown, never a filesystem path.
	resp, body = env.Do("", "GET", "/v1/blobs/abc?t=rt_x", nil)
	mustErr(t, resp, body, http.StatusNotFound, "unknown_blob")
	resp, body = env.Do(env.OperatorSecret, "GET", "/v1/blobs/..%2F..%2Fhub.db", nil)
	mustErr(t, resp, body, http.StatusNotFound, "unknown_blob")
	resp, body = env.Do(env.OperatorSecret, "GET", "/v1/blobs/"+strings.ToUpper(pr.SHA256), nil)
	mustErr(t, resp, body, http.StatusNotFound, "unknown_blob")
	// A valid token for a hash the hub never stored.
	missing := shaOf("never uploaded")
	resp, body = env.Do("", "GET", "/v1/blobs/"+missing+"?t="+env.Hub.Blobs.Token(b, missing, env.Now.Now().Add(time.Hour)), nil)
	mustErr(t, resp, body, http.StatusNotFound, "unknown_blob")
}

func TestHTTPBlobBearerVisibility(t *testing.T) {
	env := testhub.Start(t)
	a, aSecret := env.NewPeer("session", "A")
	b, bSecret := env.NewPeer("session", "B")
	_, cSecret := env.NewPeer("session", "C")
	pr := putOK(t, env, aSecret, "text/plain", "thread attachment")

	// Unreferenced: only the operator reads it with a bearer.
	resp, body := env.Do(bSecret, "GET", "/v1/blobs/"+pr.SHA256, nil)
	mustErr(t, resp, body, http.StatusForbidden, "forbidden")
	resp, body = env.Do(env.OperatorSecret, "GET", "/v1/blobs/"+pr.SHA256, nil)
	if resp.StatusCode != http.StatusOK || string(body) != "thread attachment" {
		t.Fatalf("operator GET = %d %q", resp.StatusCode, body)
	}
	resp, body = env.Do(env.OperatorSecret, "GET", "/v1/blobs/"+shaOf("never uploaded"), nil)
	mustErr(t, resp, body, http.StatusNotFound, "unknown_blob")

	// A message in an A–B thread references it: A and B read it, C does not.
	sr := postSend(t, env, aSecret, sendBody("k1", b), http.StatusAccepted)
	ctx := context.Background()
	if err := env.Hub.DB.Tx(ctx, func(tx *sql.Tx) error {
		return env.Hub.Blobs.Pin(ctx, tx, pr.SHA256, sr.MessageID)
	}); err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{aSecret, bSecret} {
		resp, body = env.Do(s, "GET", "/v1/blobs/"+pr.SHA256, nil)
		if resp.StatusCode != http.StatusOK || string(body) != "thread attachment" {
			t.Fatalf("participant GET = %d %q", resp.StatusCode, body)
		}
	}
	resp, body = env.Do(cSecret, "GET", "/v1/blobs/"+pr.SHA256, nil)
	mustErr(t, resp, body, http.StatusForbidden, "forbidden")
	// A bad token does not shadow a good bearer.
	resp, body = env.Do(bSecret, "GET", "/v1/blobs/"+pr.SHA256+"?t=rt_garbage", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("participant GET with bad token = %d %s", resp.StatusCode, body)
	}
	resp, body = env.Do("vn_peer_bogus", "GET", "/v1/blobs/"+pr.SHA256+"?t=rt_garbage", nil)
	mustErr(t, resp, body, http.StatusUnauthorized, "unauthenticated")

	// §16.3 from_join: a participant added after the message cannot read it
	// through its bearer.
	_ = a
	late, lateSecret := env.NewPeer("session", "Late")
	if _, err := env.Hub.DB.Exec(`INSERT INTO thread_participants(thr_id, peer_id, joined_at, joined_after_seq)
		VALUES (?, ?, ?, (SELECT max(seq) FROM messages WHERE thr_id = ?))`,
		sr.ThreadID, late, "2026-11-01T00:00:00.000Z", sr.ThreadID); err != nil {
		t.Fatal(err)
	}
	resp, body = env.Do(lateSecret, "GET", "/v1/blobs/"+pr.SHA256, nil)
	mustErr(t, resp, body, http.StatusForbidden, "forbidden")
	if _, err := env.Hub.DB.Exec(`UPDATE threads SET history_access = 'full' WHERE thr_id = ?`, sr.ThreadID); err != nil {
		t.Fatal(err)
	}
	resp, body = env.Do(lateSecret, "GET", "/v1/blobs/"+pr.SHA256, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("late participant under full history = %d %s", resp.StatusCode, body)
	}
}

func TestHTTPBlobLimits(t *testing.T) {
	env := testhub.Start(t)
	a, aSecret := env.NewPeer("session", "A")

	// The 256 KiB request cap does not apply to blob uploads.
	big := bytes.Repeat([]byte("z"), 300<<10)
	resp, raw := putBlob(t, env, aSecret, "", "application/octet-stream", bytes.NewReader(big))
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT 300 KiB = %d %s", resp.StatusCode, raw)
	}

	env.Hub.Blobs.MaxSize = 1024
	resp, raw = putBlob(t, env, aSecret, "", "text/plain", bytes.NewReader(bytes.Repeat([]byte("y"), 1025)))
	mustErr(t, resp, raw, http.StatusRequestEntityTooLarge, "blob_too_large")
	resp, raw = putBlob(t, env, aSecret, "", "text/plain", onlyReader{bytes.NewReader(bytes.Repeat([]byte("x"), 4096))})
	mustErr(t, resp, raw, http.StatusRequestEntityTooLarge, "blob_too_large")

	// ttl: seconds, non-negative integer; below 1 h is raised to 1 h.
	resp, raw = putBlob(t, env, aSecret, "?ttl=abc", "text/plain", strings.NewReader("t1"))
	mustErr(t, resp, raw, http.StatusUnprocessableEntity, "invalid_value")
	resp, raw = putBlob(t, env, aSecret, "?ttl=-5", "text/plain", strings.NewReader("t1"))
	mustErr(t, resp, raw, http.StatusUnprocessableEntity, "invalid_value")
	resp, raw = putBlob(t, env, aSecret, "?ttl=7200", "text/plain", strings.NewReader("t1"))
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT ?ttl=7200 = %d %s", resp.StatusCode, raw)
	}
	var ttlUntil string
	if err := env.Hub.DB.QueryRow(`SELECT ttl_until FROM blobs WHERE sha256 = ?`, shaOf("t1")).Scan(&ttlUntil); err != nil {
		t.Fatal(err)
	}
	if want := env.Now.Now().Add(2 * time.Hour).UTC().Format("2006-01-02T15:04:05.000Z"); ttlUntil != want {
		t.Fatalf("ttl_until = %s, want %s", ttlUntil, want)
	}
	resp, raw = putBlob(t, env, aSecret, "?ttl=60", "text/plain", strings.NewReader("t2"))
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT ?ttl=60 = %d %s", resp.StatusCode, raw)
	}
	if err := env.Hub.DB.QueryRow(`SELECT ttl_until FROM blobs WHERE sha256 = ?`, shaOf("t2")).Scan(&ttlUntil); err != nil {
		t.Fatal(err)
	}
	if want := env.Now.Now().Add(time.Hour).UTC().Format("2006-01-02T15:04:05.000Z"); ttlUntil != want {
		t.Fatalf("ttl_until = %s, want %s (1 h minimum)", ttlUntil, want)
	}
	resp, raw = putBlob(t, env, aSecret, "", "not a media type;;", strings.NewReader("t3"))
	mustErr(t, resp, raw, http.StatusUnprocessableEntity, "invalid_value")

	// Daily quota → 429 with Retry-After = seconds to the next UTC midnight.
	var used int64
	if err := env.Hub.DB.QueryRow(`SELECT bytes FROM blob_quota WHERE peer_id = ?`, a).Scan(&used); err != nil {
		t.Fatal(err)
	}
	env.Hub.Blobs.DailyQuota = used + 6
	putOK(t, env, aSecret, "text/plain", "qqqqqq")
	resp, raw = putBlob(t, env, aSecret, "", "text/plain", strings.NewReader("rrrrrr"))
	mustErr(t, resp, raw, http.StatusTooManyRequests, "upload_quota_exceeded")
	now := env.Now.Now().UTC()
	midnight := time.Date(now.Year(), now.Month(), now.Day()+1, 0, 0, 0, 0, time.UTC)
	want := int(math.Ceil(midnight.Sub(now).Seconds()))
	if got := resp.Header.Get("Retry-After"); got != strconv.Itoa(want) {
		t.Fatalf("Retry-After = %q, want %d", got, want)
	}
}
