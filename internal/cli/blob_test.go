package cli_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestBlobGetExpired(t *testing.T) {
	for _, reason := range []string{"blob_expired", "blob_purged"} {
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
func TestBlobGetHubErrors(t *testing.T) {
	for _, endpoint := range []string{"capabilities", "download"} {
		for _, status := range []int{401, 403, 429, 503} {
			t.Run(endpoint+"/"+strconv.Itoa(status), func(t *testing.T) {
				const reason = "blob_unavailable"
				codeName := reason
				if status == 429 {
					codeName = "blob_url_rate_limited"
				}
				requests := 0
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if endpoint == "download" && r.URL.Path == "/v1/blob-capabilities" {
						json.NewEncoder(w).Encode(map[string]any{"backend": "r2", "checksum_profile": "sha256", "max_size": 25 << 20})
						return
					}
					if r.URL.Path != "/v1/blob-capabilities" && !strings.HasSuffix(r.URL.Path, "/download") {
						w.WriteHeader(http.StatusNotFound)
						return
					}
					requests++
					w.Header().Set("Retry-After", "17")
					w.WriteHeader(status)
					json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"code": codeName, "message": "private-transfer-secret", "details": map[string]any{"retry_after_seconds": 17, "private": "private-transfer-secret"}}})
				}))
				defer srv.Close()
				u := profileUser(t, srv.URL, "fake")
				path := filepath.Join(t.TempDir(), "result")
				code, out, errs := u.run("blob", "get", strings.Repeat("a", 64), "--blob-id", "blo_00000000000000000000000001", "--out", path, "--json")
				var wire struct {
					Error struct {
						Code  string `json:"code"`
						Retry int    `json:"retry_after_seconds"`
					} `json:"error"`
				}
				if json.Unmarshal([]byte(out), &wire) != nil || wire.Error.Code != codeName || wire.Error.Retry != 17 || strings.Contains(out+errs, "private-transfer-secret") {
					t.Error("hub error lost sanitized machine-readable code/retry metadata")
				}
				if code != 2 {
					t.Errorf("hub status %d: exit=%d, want 2", status, code)
				}
				if requests != 1 {
					t.Errorf("failure retried %d times", requests)
				}
				entries, err := os.ReadDir(filepath.Dir(path))
				if err != nil || len(entries) != 0 {
					t.Fatal("hub error left output or private spool")
				}
			})
		}
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
