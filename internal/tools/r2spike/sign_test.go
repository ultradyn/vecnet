//go:build spike

package main

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func vectorCredential() credential {
	return credential{"00000000000000000000000000000000", "vecnet-blobs-test", "https://00000000000000000000000000000000.r2.cloudflarestorage.com", "VECTORACCESS", "vector-secret-not-live"}
}
func TestCanonicalVector(t *testing.T) {
	// Expected canonical bytes are literal; signature independently computed with
	// Python hmac/hashlib, never derived by calling this implementation.
	want := `GET
/vecnet-blobs-test/folder/a%20b%2Bc/%CE%BB
X-Amz-Algorithm=AWS4-HMAC-SHA256&X-Amz-Credential=VECTORACCESS%2F20261105%2Fauto%2Fs3%2Faws4_request&X-Amz-Date=20261105T030405Z&X-Amz-Expires=600&X-Amz-SignedHeaders=host&response-content-disposition=attachment%3B%20filename%3Da%2Bb.txt
host:00000000000000000000000000000000.r2.cloudflarestorage.com

host
UNSIGNED-PAYLOAD`
	now := time.Date(2026, 11, 5, 3, 4, 5, 0, time.UTC)
	signed, canonical, err := presign(vectorCredential(), "GET", "folder/a b+c/λ", nil, now, 600, url.Values{"response-content-disposition": []string{"attachment; filename=a+b.txt"}})
	if err != nil || canonical != want {
		t.Fatal("canonical vector mismatch (values suppressed)")
	}
	u, err := url.Parse(signed)
	if err != nil {
		t.Fatal("vector URL parse failed")
	}
	if u.Query().Get("X-Amz-Signature") != "0dc205a362d63f6181c919440f33d7ecb398ab7819c205c6514d7054c78ec530" {
		t.Fatal("independent signature vector mismatch")
	}
}
func TestPUTBindingsAndHeaderCanonicalization(t *testing.T) {
	c := vectorCredential()
	now := time.Date(2026, 11, 5, 3, 4, 5, 0, time.UTC)
	b := []byte("fixture")
	h := putHeaders("sha256", b)
	original, canonical, err := presign(c, "PUT", "key", h, now, 300, nil)
	if err != nil {
		t.Fatal("presign failed")
	}
	for _, v := range []string{"content-length:7\n", "if-none-match:*\n", "x-amz-checksum-sha256:" + checksum("sha256", b) + "\n"} {
		if !strings.Contains(canonical, v) {
			t.Fatal("required binding absent")
		}
	}
	u, _ := url.Parse(original)
	if u.Query().Get("x-amz-checksum-sha256") != "" {
		t.Fatal("checksum hoisted")
	}
	for _, field := range []string{"Content-Length", "If-None-Match", "Content-Type", "Content-Disposition", "Cache-Control", "x-amz-checksum-sha256", "x-amz-meta-vecnet-checksum", "x-amz-meta-vecnet-checksum-profile"} {
		mutated := h.Clone()
		mutated.Set(field, "changed")
		other, _, _ := presign(c, "PUT", "key", mutated, now, 300, nil)
		if other == original {
			t.Fatal("header mutation not bound")
		}
		mutated = h.Clone()
		mutated.Del(field)
		other, _, _ = presign(c, "PUT", "key", mutated, now, 300, nil)
		if other == original {
			t.Fatal("header removal not bound")
		}
	}
	h = http.Header{"X-Test": []string{"  a\t b  "}}
	_, canonical, _ = presign(c, "PUT", "key", h, now, 300, nil)
	if !strings.Contains(canonical, "x-test:a b\n") {
		t.Fatal("header whitespace not normalized")
	}
	if canonicalQuery(url.Values{"a": []string{"z", "+", " "}}) != "a=%20&a=%2B&a=z" {
		t.Fatal("encoded query sorting mismatch")
	}
	for _, ttl := range []int{0, -1, 604801} {
		_, _, err = presign(c, "GET", "key", nil, now, ttl, nil)
		if err == nil {
			t.Fatal("invalid TTL accepted")
		}
	}
}
func TestDownloadTTL(t *testing.T) {
	now := time.Date(2026, 11, 5, 3, 4, 5, 0, time.UTC)
	if getTTL(now, nil) != 600 {
		t.Fatal("keep TTL")
	}
	for _, tt := range []struct {
		d    time.Duration
		want int
	}{{time.Hour, 600}, {600 * time.Second, 600}, {time.Second + 900*time.Millisecond, 1}, {900 * time.Millisecond, 0}, {-time.Second, -1}} {
		end := now.Add(tt.d)
		if getTTL(now, &end) != tt.want {
			t.Fatal("finite expiry floor/cap mismatch")
		}
	}
}
func envFixture() string {
	c := vectorCredential()
	return "R2_ACCOUNT_ID=" + c.account + "\nR2_BUCKET=" + c.bucket + "\nR2_ENDPOINT=" + c.endpoint + "\nR2_ACCESS_KEY_ID=" + c.access + "\nR2_SECRET_ACCESS_KEY='" + c.secret + "'\n"
}
func TestProtectedInput(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "input.env")
	if os.WriteFile(p, []byte(envFixture()), 0600) != nil {
		t.Fatal("fixture failed")
	}
	if _, err := loadCredential(p); err != nil {
		t.Fatal("valid env fixture rejected")
	}
	link := filepath.Join(dir, "link")
	if os.Symlink(p, link) != nil {
		t.Fatal("symlink fixture failed")
	}
	if _, err := loadCredential(link); err == nil {
		t.Fatal("symlink accepted")
	}
	for _, mode := range []os.FileMode{0644, 0400, 0660, 0700} {
		if os.Chmod(p, mode) != nil {
			t.Fatal("chmod fixture failed")
		}
		if _, err := loadCredential(p); err == nil {
			t.Fatal("wrong mode accepted")
		}
	}
	os.Chmod(p, 0600)
	for _, text := range []string{envFixture() + "R2_ACCESS_KEY_ID=duplicate\n", envFixture() + "EXTRA=unexpected\n", strings.ReplaceAll(envFixture(), "vecnet-blobs-test", "production"), strings.ReplaceAll(envFixture(), "https://", "http://"), strings.ReplaceAll(envFixture(), "r2.cloudflarestorage.com", "r2.cloudflarestorage.com.evil.invalid"), strings.ReplaceAll(envFixture(), "R2_SECRET_ACCESS_KEY=", "MISSING=")} {
		os.WriteFile(p, []byte(text), 0600)
		if _, err := loadCredential(p); err == nil {
			t.Fatal("invalid credential fixture accepted")
		}
	}
	// Test ownership where privileges permit it; no chmod/chown outside t.TempDir.
	os.WriteFile(p, []byte(envFixture()), 0600)
	if os.Chown(p, os.Getuid()+1, -1) == nil {
		if _, err := loadCredential(p); err == nil {
			t.Fatal("wrong owner accepted")
		}
	}
	if _, err := loadCredential(dir); err == nil {
		t.Fatal("directory accepted")
	}
}
func TestFailureRedaction(t *testing.T) {
	marker := "sensitive-synthetic-marker"
	x := readResponse(nil, &url.Error{Op: "Get", URL: "https://invalid.test/?signature=" + marker, Err: errors.New(marker)})
	if x.code != "TransportFailure" || strings.Contains(x.code, marker) {
		t.Fatal("transport error not sanitized")
	}
	for _, body := range []string{"<Error><Code>" + marker + "</Code><Message>" + marker + "</Message></Error>", marker} {
		if strings.Contains(safeCode([]byte(body)), marker) {
			t.Fatal("provider error not sanitized")
		}
	}
	if safeCode([]byte("<Error><Code>BadDigest</Code><Message>"+marker+"</Message></Error>")) != "BadDigest" {
		t.Fatal("provider code lost")
	}
}

type fakeTransport func(*http.Request) (*http.Response, error)

func (f fakeTransport) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }
func TestPreviousFixtureDeletionScope(t *testing.T) {
	t.Chdir(t.TempDir())
	os.MkdirAll(filepath.Dir(reportPath), 0755)
	runID := "20261105T000000Z-0123456789abcdef"
	exp := "attachments/v1/exp/1h/spike-" + runID + "/lifecycle-exp-1h"
	keep := "attachments/v1/keep/spike-" + runID + "/lifecycle-keep"
	calls := 0
	r := runner{c: vectorCredential(), report: report{Gates: map[string]string{}}, client: &http.Client{Transport: fakeTransport(func(req *http.Request) (*http.Response, error) {
		calls++
		if req.URL.Path != "/vecnet-blobs-test/"+exp && req.URL.Path != "/vecnet-blobs-test/"+keep {
			t.Fatal("deletion escaped prior fixture scope")
		}
		status := 204
		if req.Method == "HEAD" {
			status = 404
		} else if req.Method != "DELETE" {
			t.Fatal("unexpected fixture cleanup method")
		}
		return &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(""))}, nil
	})}}
	for _, old := range []report{
		{RunID: "../outside", Finished: stamp(), Fixtures: []string{exp, keep}},
		{RunID: runID, Fixtures: []string{exp, keep}},
		{RunID: runID, Finished: stamp(), Fixtures: []string{exp, "attachments/v1/keep/foreign/object"}},
		{RunID: runID, Finished: time.Now().Add(time.Hour).UTC().Format(time.RFC3339), Fixtures: []string{exp, keep}},
	} {
		data, _ := json.Marshal(old)
		os.WriteFile(reportPath, data, 0644)
		if r.replacePreviousFixtures() || calls != 0 {
			t.Fatal("unsafe previous receipt accepted")
		}
	}
	data, _ := json.Marshal(report{RunID: runID, Finished: time.Now().Add(-time.Hour).UTC().Format(time.RFC3339), Fixtures: []string{exp, keep}})
	os.WriteFile(reportPath, data, 0644)
	if !r.replacePreviousFixtures() || calls != 4 {
		t.Fatal("exact fixture cleanup failed")
	}
}

func TestCanonicalPUTVector(t *testing.T) {
	// Literal PUT canonical bytes and signature independently calculated by Python.
	want := `PUT
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
	now := time.Date(2026, 11, 5, 3, 4, 5, 0, time.UTC)
	signed, canonical, err := presign(vectorCredential(), "PUT", "attachments/v1/exp/1h/vector/blob", putHeaders("sha256", []byte("fixture")), now, 300, nil)
	if err != nil || canonical != want {
		t.Fatal("PUT canonical vector mismatch (values suppressed)")
	}
	u, err := url.Parse(signed)
	if err != nil {
		t.Fatal("PUT vector URL parse failed")
	}
	if u.Query().Get("X-Amz-Signature") != "e6ca8ef5f955a2a6ae9e339c3ea31f8a1d88a99a208aab0d52a9ea093c77e9d1" {
		t.Fatal("independent PUT signature vector mismatch")
	}
}
