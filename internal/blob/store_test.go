package blob_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/blob"
	"github.com/XertroV/vecnet/internal/store"
	"github.com/XertroV/vecnet/internal/testhub"
)

// clock is a settable fake clock.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) Set(t time.Time) {
	c.mu.Lock()
	c.t = t
	c.mu.Unlock()
}
func (c *clock) Advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func newStore(t *testing.T) (*blob.Store, *clock) {
	t.Helper()
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	c := &clock{t: time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)}
	s, err := blob.Open(filepath.Join(dir, "blobs"), db, c.Now)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return s, c
}

func hexSHA(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func put(t *testing.T, s *blob.Store, uploader, body string, ttl time.Duration) blob.Meta {
	t.Helper()
	m, err := s.Put(context.Background(), uploader, strings.NewReader(body), "text/plain", ttl)
	if err != nil {
		t.Fatalf("Put(%q): %v", body, err)
	}
	return m
}

func rows(t *testing.T, s *blob.Store, q string, args ...any) int {
	t.Helper()
	var n int
	if err := s.DB.QueryRow(q, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return n
}

// files lists the regular files under dir (recursively); a missing dir is empty.
func files(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			out = append(out, p)
		}
		return nil
	})
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	return out
}

func mustNoTmp(t *testing.T, s *blob.Store) {
	t.Helper()
	if f := files(t, filepath.Join(s.Root, "tmp")); len(f) != 0 {
		t.Fatalf("tmp files remain: %v", f)
	}
}

func blobPath(s *blob.Store, sha string) string {
	return filepath.Join(s.Root, "sha256", sha[:2], sha[2:4], sha)
}

func TestPutHashesAndLaysOut(t *testing.T) {
	s, _ := newStore(t)
	if s.MaxSize != 25<<20 || s.DailyQuota != 500<<20 {
		t.Fatalf("defaults MaxSize=%d DailyQuota=%d, want 25 MiB and 500 MiB", s.MaxSize, s.DailyQuota)
	}
	m := put(t, s, "peer_A", "hello", 0)
	const want = "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824"
	if m.SHA256 != want || m.Size != 5 || m.MediaType != "text/plain" {
		t.Fatalf("meta = %+v, want sha %s size 5 text/plain", m, want)
	}
	got, err := os.ReadFile(filepath.Join(s.Root, "sha256", "2c", "f2", want))
	if err != nil {
		t.Fatalf("blob file: %v", err)
	}
	if string(got) != "hello" {
		t.Fatalf("file content %q", got)
	}
	mustNoTmp(t, s)
	if n := rows(t, s, `SELECT count(*) FROM blobs WHERE sha256 = ? AND size = 5 AND uploader = 'peer_A'
		AND media_type = 'text/plain' AND ttl_until IS NULL AND purged_at IS NULL`, want); n != 1 {
		t.Fatalf("blobs rows = %d, want 1", n)
	}
	rc, meta, err := s.Open(context.Background(), want)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer rc.Close()
	b, _ := io.ReadAll(rc)
	if string(b) != "hello" || meta != m {
		t.Fatalf("Open = %q %+v", b, meta)
	}
}

func TestPutDedupes(t *testing.T) {
	s, _ := newStore(t)
	a := put(t, s, "peer_A", "same bytes", 0)
	b := put(t, s, "peer_A", "same bytes", 0)
	if a != b {
		t.Fatalf("metas differ: %+v %+v", a, b)
	}
	if f := files(t, filepath.Join(s.Root, "sha256")); len(f) != 1 {
		t.Fatalf("blob files = %v, want exactly one", f)
	}
	if n := rows(t, s, `SELECT count(*) FROM blobs`); n != 1 {
		t.Fatalf("blobs rows = %d, want 1", n)
	}
	mustNoTmp(t, s)
	// The dedupe skips the rewrite but each upload still counts against quota.
	if n := rows(t, s, `SELECT bytes FROM blob_quota WHERE peer_id = 'peer_A'`); n != 20 {
		t.Fatalf("quota bytes = %d, want 20", n)
	}
}

// zeros is an endless reader of zero bytes.
type zeros struct{}

func (zeros) Read(p []byte) (int, error) { clear(p); return len(p), nil }

func TestPutTooLargeAbortsCleanly(t *testing.T) {
	s, _ := newStore(t)
	_, err := s.Put(context.Background(), "peer_A", io.LimitReader(zeros{}, 25<<20+1), "application/octet-stream", 0)
	if !errors.Is(err, blob.ErrTooLarge) {
		t.Fatalf("Put(25 MiB + 1) = %v, want ErrTooLarge", err)
	}
	mustNoTmp(t, s)
	if f := files(t, filepath.Join(s.Root, "sha256")); len(f) != 0 {
		t.Fatalf("blob files = %v", f)
	}
	if n := rows(t, s, `SELECT count(*) FROM blobs`); n != 0 {
		t.Fatalf("blobs rows = %d, want 0", n)
	}
	if n := rows(t, s, `SELECT count(*) FROM blob_quota WHERE bytes > 0`); n != 0 {
		t.Fatal("an aborted upload was charged to quota")
	}
	// Exactly MaxSize is accepted; one byte more is not.
	s.MaxSize = 10
	if _, err := s.Put(context.Background(), "peer_A", strings.NewReader("0123456789"), "text/plain", 0); err != nil {
		t.Fatalf("Put(MaxSize bytes) = %v", err)
	}
	if _, err := s.Put(context.Background(), "peer_A", strings.NewReader("0123456789a"), "text/plain", 0); !errors.Is(err, blob.ErrTooLarge) {
		t.Fatalf("Put(MaxSize+1) = %v, want ErrTooLarge", err)
	}
	mustNoTmp(t, s)
}

// cancelReader yields one chunk, then cancels the context and fails like a
// request body whose client went away.
type cancelReader struct {
	cancel func()
	n      int
}

func (r *cancelReader) Read(p []byte) (int, error) {
	if r.n == 0 {
		r.n++
		return copy(p, "partial"), nil
	}
	r.cancel()
	return 0, context.Canceled
}

func TestPutCancelledBodyRemovesTemp(t *testing.T) {
	s, _ := newStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if _, err := s.Put(ctx, "peer_A", &cancelReader{cancel: cancel}, "text/plain", 0); err == nil {
		t.Fatal("Put over a failing body succeeded")
	}
	mustNoTmp(t, s)
	if n := rows(t, s, `SELECT count(*) FROM blobs`); n != 0 {
		t.Fatalf("blobs rows = %d, want 0", n)
	}
	if n := rows(t, s, `SELECT count(*) FROM blob_quota WHERE bytes > 0`); n != 0 {
		t.Fatal("a cancelled upload was charged to quota")
	}
	// A context cancelled before the body ends aborts too, even if reads succeed.
	ctx2, cancel2 := context.WithCancel(context.Background())
	cancel2()
	if _, err := s.Put(ctx2, "peer_A", strings.NewReader("fine bytes"), "text/plain", 0); err == nil {
		t.Fatal("Put under a cancelled context succeeded")
	}
	mustNoTmp(t, s)
	if n := rows(t, s, `SELECT count(*) FROM blobs`); n != 0 {
		t.Fatalf("blobs rows = %d, want 0", n)
	}
}

func TestDailyQuota(t *testing.T) {
	s, c := newStore(t)
	s.DailyQuota = 10
	// 23:00 UTC on 1 Nov, already 2 Nov in the clock's own zone: the quota
	// day must follow UTC, not the clock's location.
	c.Set(time.Date(2026, 11, 1, 23, 0, 0, 0, time.UTC).In(time.FixedZone("ACDT", 10*3600+1800)))
	put(t, s, "peer_A", "aaaaaa", 0)
	if _, err := s.Put(context.Background(), "peer_A", strings.NewReader("bbbbbb"), "text/plain", 0); !errors.Is(err, blob.ErrQuota) {
		t.Fatalf("second Put = %v, want ErrQuota", err)
	}
	mustNoTmp(t, s)
	if n := rows(t, s, `SELECT count(*) FROM blobs WHERE sha256 = ?`, hexSHA([]byte("bbbbbb"))); n != 0 {
		t.Fatal("an over-quota upload left a row")
	}
	// The quota is per uploader.
	put(t, s, "peer_B", "bbbbbb", 0)
	// Same UTC day: still refused.
	c.Advance(30 * time.Minute)
	if _, err := s.Put(context.Background(), "peer_A", strings.NewReader("cccccc"), "text/plain", 0); !errors.Is(err, blob.ErrQuota) {
		t.Fatalf("Put later the same UTC day = %v, want ErrQuota", err)
	}
	// Past UTC midnight: a fresh day.
	c.Advance(31 * time.Minute)
	put(t, s, "peer_A", "cccccc", 0)
	// FakeClock + 24 h: fresh again.
	c.Advance(24 * time.Hour)
	put(t, s, "peer_A", "dddddd", 0)
}

func TestTokenScoped(t *testing.T) {
	s, c := newStore(t)
	sha := hexSHA([]byte("x"))
	other := hexSHA([]byte("y"))
	now := c.Now()
	exp := now.Add(time.Hour)
	tok := s.Token("peer_B", sha, exp)
	if !strings.HasPrefix(tok, "rt_") {
		t.Fatalf("token %q lacks rt_ prefix", tok)
	}
	if strings.ContainsAny(tok[3:], "+/=") {
		t.Fatalf("token %q is not unpadded base64url", tok)
	}
	if r, ok := s.VerifyToken(tok, sha, now); !ok || r != "peer_B" {
		t.Fatalf("VerifyToken = %q %v, want peer_B true", r, ok)
	}
	if _, ok := s.VerifyToken(tok, other, now); ok {
		t.Fatal("token verified for another sha")
	}
	if _, ok := s.VerifyToken(tok, sha, exp); ok {
		t.Fatal("token verified at its expiry")
	}
	if _, ok := s.VerifyToken(tok, sha, exp.Add(time.Second)); ok {
		t.Fatal("expired token verified")
	}
	if tc := s.Token("peer_C", sha, exp); tc == tok {
		t.Fatal("tokens for different recipients are equal")
	} else if r, ok := s.VerifyToken(tc, sha, now); !ok || r != "peer_C" {
		t.Fatalf("C's token verified as %q %v", r, ok)
	}
	// Change each character in turn: no tampered token may verify.
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"
	for i := 3; i < len(tok); i++ {
		b := []byte(tok)
		j := strings.IndexByte(alphabet, b[i])
		b[i] = alphabet[(j+1)%len(alphabet)]
		if r, ok := s.VerifyToken(string(b), sha, now); ok {
			t.Fatalf("tampered token (pos %d) verified as %q", i, r)
		}
	}
	for _, bad := range []string{"", "rt_", "rt_!!!", tok[3:], "xx" + tok[2:], tok + "A", tok + "AAAA"} {
		if _, ok := s.VerifyToken(bad, sha, now); ok {
			t.Fatalf("garbage token %q verified", bad)
		}
	}
	// Another hub's key rejects the token.
	s2, _ := newStore(t)
	if _, ok := s2.VerifyToken(tok, sha, now); ok {
		t.Fatal("token verified under another hub's key")
	}
	if u := s.URL("https://hub.example", "peer_B", sha, exp); u != "https://hub.example/v1/blobs/"+sha+"?t="+tok {
		t.Fatalf("URL = %q", u)
	}
}

func TestGCAndPinning(t *testing.T) {
	s, c := newStore(t)
	ctx := context.Background()
	a := put(t, s, "peer_A", "unreferenced", 0)
	b := put(t, s, "peer_A", "pinned", 0)
	ttl := put(t, s, "peer_A", "ttl one hour", time.Hour)
	short := put(t, s, "peer_A", "ttl clamped", 30*time.Minute) // minimum 1 h
	pinnedTTL := put(t, s, "peer_A", "pinned with ttl", time.Hour)
	err := s.DB.Tx(ctx, func(tx *sql.Tx) error {
		if err := s.Pin(ctx, tx, b.SHA256, "msg_1"); err != nil {
			return err
		}
		if err := s.Pin(ctx, tx, b.SHA256, "msg_1"); err != nil { // idempotent
			return err
		}
		return s.Pin(ctx, tx, pinnedTTL.SHA256, "msg_2")
	})
	if err != nil {
		t.Fatalf("Pin: %v", err)
	}
	if n := rows(t, s, `SELECT count(*) FROM blob_refs`); n != 2 {
		t.Fatalf("blob_refs = %d, want 2", n)
	}
	exists := func(sha string) bool {
		t.Helper()
		var ok bool
		if err := s.DB.Tx(ctx, func(tx *sql.Tx) error {
			var err error
			ok, err = s.Exists(ctx, tx, sha)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		_, statErr := os.Stat(blobPath(s, sha))
		if ok != (statErr == nil) {
			t.Fatalf("Exists(%s) = %v but file stat err = %v", sha[:8], ok, statErr)
		}
		return ok
	}
	gc := func(want int) {
		t.Helper()
		if n, err := s.GC(ctx); err != nil || n != want {
			t.Fatalf("GC = %d, %v; want %d", n, err, want)
		}
	}

	gc(0)
	c.Advance(45 * time.Minute)
	gc(0) // the 30 min TTL was raised to 1 h
	c.Advance(15*time.Minute + time.Millisecond)
	gc(2) // ttl and short: unreferenced past their TTL
	if exists(ttl.SHA256) || exists(short.SHA256) {
		t.Fatal("TTL blobs survived GC")
	}
	if !exists(a.SHA256) || !exists(b.SHA256) || !exists(pinnedTTL.SHA256) {
		t.Fatal("GC removed a blob it should keep")
	}
	c.Advance(23 * time.Hour)
	gc(1) // a: unreferenced for more than 24 h
	if exists(a.SHA256) {
		t.Fatal("unreferenced blob survived 24 h")
	}
	if !exists(b.SHA256) || !exists(pinnedTTL.SHA256) {
		t.Fatal("pinned blob was collected")
	}
	c.Advance(30 * 24 * time.Hour)
	gc(0)
	if !exists(b.SHA256) || !exists(pinnedTTL.SHA256) {
		t.Fatal("pinned blob was collected later")
	}
	if _, _, err := s.Open(ctx, a.SHA256); !errors.Is(err, blob.ErrNotFound) {
		t.Fatalf("Open(collected) = %v, want ErrNotFound", err)
	}
	if n := rows(t, s, `SELECT count(*) FROM blobs`); n != 2 {
		t.Fatalf("blobs rows = %d, want 2", n)
	}
}

func TestRejectsMalformedSHA(t *testing.T) {
	s, _ := newStore(t)
	ctx := context.Background()
	// A file outside the store that a path traversal would reach.
	outside := filepath.Join(filepath.Dir(s.Root), "secret")
	if err := os.WriteFile(outside, []byte("s"), 0o600); err != nil {
		t.Fatal(err)
	}
	good := hexSHA([]byte("x"))
	for _, sha := range []string{
		"", "abc", "../secret", "../../secret", strings.ToUpper(good), good[:63], good + "0",
		good[:62] + "/.", good[:63] + "g", "sha256:" + good,
	} {
		if _, _, err := s.Open(ctx, sha); !errors.Is(err, blob.ErrNotFound) {
			t.Fatalf("Open(%q) = %v, want ErrNotFound", sha, err)
		}
		if err := s.Purge(ctx, sha); !errors.Is(err, blob.ErrNotFound) {
			t.Fatalf("Purge(%q) = %v, want ErrNotFound", sha, err)
		}
		if err := s.DB.Tx(ctx, func(tx *sql.Tx) error {
			ok, err := s.Exists(ctx, tx, sha)
			if ok {
				t.Fatalf("Exists(%q) = true", sha)
			}
			return err
		}); err != nil {
			t.Fatal(err)
		}
		if err := s.DB.Tx(ctx, func(tx *sql.Tx) error { return s.Pin(ctx, tx, sha, "msg_1") }); err == nil {
			t.Fatalf("Pin(%q) succeeded", sha)
		}
		if _, ok := s.VerifyToken(s.Token("peer_B", sha, time.Now().Add(time.Hour)), sha, time.Now()); ok {
			t.Fatalf("token verified for malformed sha %q", sha)
		}
	}
	if _, err := os.Stat(outside); err != nil {
		t.Fatalf("outside file touched: %v", err)
	}
}

func TestPurgeTombstone(t *testing.T) {
	env := testhub.Start(t)
	s := env.Hub.Blobs
	ctx := context.Background()
	m, err := s.Put(ctx, "peer_A", strings.NewReader("purge me"), "text/plain", 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.DB.Tx(ctx, func(tx *sql.Tx) error { return s.Pin(ctx, tx, m.SHA256, "msg_1") }); err != nil {
		t.Fatal(err)
	}
	if err := s.Purge(ctx, m.SHA256); err != nil {
		t.Fatalf("Purge: %v", err)
	}
	if _, err := os.Stat(blobPath(s, m.SHA256)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("purged bytes still on disk: %v", err)
	}
	if n := rows(t, s, `SELECT count(*) FROM blobs WHERE sha256 = ? AND purged_at IS NOT NULL`, m.SHA256); n != 1 {
		t.Fatalf("tombstone rows = %d, want 1", n)
	}
	if _, _, err := s.Open(ctx, m.SHA256); !errors.Is(err, blob.ErrPurged) {
		t.Fatalf("Open(purged) = %v, want ErrPurged", err)
	}
	if err := s.DB.Tx(ctx, func(tx *sql.Tx) error {
		ok, err := s.Exists(ctx, tx, m.SHA256)
		if ok {
			t.Fatal("Exists(purged) = true")
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	// Purge is idempotent; an unknown hash is ErrNotFound.
	if err := s.Purge(ctx, m.SHA256); err != nil {
		t.Fatalf("second Purge: %v", err)
	}
	if err := s.Purge(ctx, hexSHA([]byte("never uploaded"))); !errors.Is(err, blob.ErrNotFound) {
		t.Fatalf("Purge(unknown) = %v, want ErrNotFound", err)
	}
	// Re-uploading the same bytes does not resurrect a purged blob.
	if _, err := s.Put(ctx, "peer_A", strings.NewReader("purge me"), "text/plain", 0); !errors.Is(err, blob.ErrPurged) {
		t.Fatalf("Put(purged bytes) = %v, want ErrPurged", err)
	}
	mustNoTmp(t, s)

	tok := s.Token("peer_B", m.SHA256, env.Now.Now().Add(time.Hour))
	resp, body := env.Do("", "GET", "/v1/blobs/"+m.SHA256+"?t="+tok, nil)
	mustCode(t, resp, body, http.StatusGone, "blob_purged")
	resp, body = env.Do(env.OperatorSecret, "GET", "/v1/blobs/"+m.SHA256, nil)
	mustCode(t, resp, body, http.StatusGone, "blob_purged")

	// The tombstone outlives GC.
	env.Now.Advance(48 * time.Hour)
	if _, err := s.GC(ctx); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Open(ctx, m.SHA256); !errors.Is(err, blob.ErrPurged) {
		t.Fatalf("Open(purged) after GC = %v, want ErrPurged", err)
	}
}

func mustCode(t *testing.T, resp *http.Response, body []byte, status int, code string) {
	t.Helper()
	var e struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if resp.StatusCode != status || json.Unmarshal(body, &e) != nil || e.Error.Code != code {
		t.Fatalf("= %d %s, want %d %s", resp.StatusCode, bytes.TrimSpace(body), status, code)
	}
}
