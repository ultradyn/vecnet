package cli_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBlobGetExpired(t *testing.T) {
	for _, reason := range []string{"blob_expired", "blob_purged", "blob_url_rate_limited"} {
		t.Run(reason, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/v1/blob-capabilities" {
					json.NewEncoder(w).Encode(map[string]any{"backend": "r2", "checksum_profile": "sha256", "max_size": 25 << 20})
					return
				}
				w.WriteHeader(410)
				json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"code": reason, "message": "private-transfer-secret", "details": map[string]any{"retry_after_seconds": 17}}})
			}))
			defer srv.Close()
			u := profileUser(t, srv.URL, "fake")
			u.env["XDG_CONFIG_HOME"] = t.TempDir()
			path := filepath.Join(t.TempDir(), "result")
			code, out, errs := u.run("blob", "get", strings.Repeat("a", 64), "--blob-id", "blo_00000000000000000000000001", "--out", path, "--json")
			if code != 1 || !strings.Contains(out+errs, reason) || strings.Contains(out+errs, "private-transfer-secret") {
				t.Fatalf("state: exit=%d out=%q err=%q", code, out, errs)
			}
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Fatal("failure created output")
			}
			entries, _ := os.ReadDir(filepath.Dir(path))
			if len(entries) != 0 {
				t.Fatal("temporary output leaked")
			}
		})
	}
}
func TestBlobGetHelp(t *testing.T) {
	u := newUser(t)
	out := u.mustRun("--help")
	if !strings.Contains(out, "blob get") || !strings.Contains(out, "URLs are short-lived; call get again whenever you need the file, as long as it hasn't expired.") {
		t.Fatal("missing blob help")
	}
}

func TestBlobGetVerifiedOutputAndNoOverwrite(t *testing.T) {
	const sha = "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"
	const id = "blo_00000000000000000000000001"
	mints := 0
	body := "abc"
	direct := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
			t.Error("credential at object endpoint")
		}
		w.Write([]byte(body))
	}))
	defer direct.Close()
	hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/blob-capabilities" {
			json.NewEncoder(w).Encode(map[string]any{"backend": "r2", "max_size": 25 << 20, "checksum_profile": "sha256", "download_origins": []string{direct.URL}})
			return
		}
		if !strings.HasSuffix(r.URL.Path, "/download") {
			w.WriteHeader(404)
			return
		}
		mints++
		if r.URL.Query().Get("blob_id") != id {
			t.Error("missing stable identity")
		}
		json.NewEncoder(w).Encode(map[string]any{"method": "GET", "url": direct.URL + "/object?private-transfer-secret=1", "expires_at": time.Now().Add(599 * time.Second).UTC().Format(time.RFC3339Nano), "object_expires_at": nil, "blob_id": id, "sha256": sha, "sha256_verified": true, "integrity": map[string]any{"algorithm": "sha256", "value": sha, "verified": true}, "size": 3, "retention_class": "keep"})
	}))
	defer hub.Close()
	u := profileUser(t, hub.URL, "fake")
	for i := 0; i < 3; i++ {
		path := filepath.Join(t.TempDir(), "out")
		code, out, errs := u.run("blob", "get", sha, "--blob-id", id, "--out", path, "--json")
		if code != 0 || strings.Contains(out+errs, "private-transfer-secret") || strings.Contains(out, "url") {
			t.Fatalf("save failed or leaked capability: exit=%d", code)
		}
		b, err := os.ReadFile(path)
		if err != nil || string(b) != "abc" {
			t.Fatal("missing verified output")
		}
		st, _ := os.Stat(path)
		if st.Mode().Perm()&0o077 != 0 {
			t.Fatal("output is not private")
		}
		code, _, _ = u.run("blob", "get", sha, "--blob-id", id, "--out", path)
		if code != 1 {
			t.Fatal("overwrote output")
		}
	}
	body = "xyz"
	badDir := t.TempDir()
	badPath := filepath.Join(badDir, "bad")
	code, _, _ := u.run("blob", "get", sha, "--blob-id", id, "--out", badPath)
	if code != 1 {
		t.Fatal("checksum mismatch accepted")
	}
	entries, _ := os.ReadDir(badDir)
	if len(entries) != 0 {
		t.Fatal("failed verification left output")
	}
	if mints != 4 {
		t.Fatalf("fresh gets=%d", mints)
	}
}

func TestBlobGetDiskOldHub(t *testing.T) {
	const sha = "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"
	hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/blobs/"+sha {
			w.WriteHeader(404)
			return
		}
		w.Write([]byte("abc"))
	}))
	defer hub.Close()
	u := profileUser(t, hub.URL, "fake")
	path := filepath.Join(t.TempDir(), "disk")
	if code, _, _ := u.run("blob", "get", sha, "--out", path); code != 0 {
		t.Fatalf("old disk hub exit %d", code)
	}
}
