package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/blobwire"
	"github.com/XertroV/vecnet/internal/envelope"
)

const reviewSHA = "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"
const reviewID = "blo_00000000000000000000000001"
const reviewPeer = "peer_01ARZ3NDEKTSV4RRFFQ69G5FAV"
const reviewMsg = "msg_01ARZ3NDEKTSV4RRFFQ69G5FAV"
const reviewThread = "thr_01ARZ3NDEKTSV4RRFFQ69G5FAV"

// All transfer capabilities are fake and accepted only by this in-process server.
func reviewBlobHub(t *testing.T, now func() time.Time) (*httptest.Server, *int, *int) {
	t.Helper()
	mints, sends := 0, 0
	deadlines := map[string]time.Time{}
	key := testIdentityKey(t, "1")
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v1/hub/identity":
			sig, _ := key.SignChallenge(srv.URL, r.URL.Query().Get("challenge"))
			json.NewEncoder(w).Encode(envelope.HubIdentity{Pubkey: key.Pubkey(), PublicURL: srv.URL, ChallengeSig: sig})
		case r.URL.Path == "/v1/blob-capabilities":
			json.NewEncoder(w).Encode(map[string]any{"backend": "r2", "max_size": 25 << 20, "checksum_profile": "sha256", "download_origins": []string{srv.URL}})
		case r.URL.Path == "/object" && r.Method == "PUT":
			b, _ := io.ReadAll(r.Body)
			if string(b) != "abc" {
				t.Error("bad upload body")
			}
		case r.URL.Path == "/object":
			token := r.URL.Query().Get("cap")
			end, ok := deadlines[token]
			if !ok || !now().Before(end) {
				w.WriteHeader(403)
				return
			}
			io.WriteString(w, "abc")
		case strings.HasSuffix(r.URL.Path, "/download"):
			mints++
			token := strconv.Itoa(mints)
			end := now().Add(600 * time.Second)
			deadlines[token] = end
			json.NewEncoder(w).Encode(blobwire.DownloadGrant{BlobID: reviewID, SHA256: reviewSHA, SHA256Verified: reviewBool(true), Integrity: &blobwire.Integrity{Algorithm: "sha256", Value: reviewSHA, Verified: true}, Size: 3, RetentionClass: "keep", Method: "GET", URL: srv.URL + "/object?cap=" + token, ExpiresAt: end.Format(time.RFC3339Nano)})
		case r.URL.Path == "/v1/blobs/uploads":
			ref := reviewRef()
			ref.SHA256Verified = reviewBool(false)
			ref.Integrity = &blobwire.Integrity{Algorithm: "sha256", Value: reviewSHA}
			json.NewEncoder(w).Encode(blobwire.UploadGrant{Ref: ref, Upload: blobwire.Request{Method: "PUT", URL: srv.URL + "/object", ExpiresAt: now().Add(299 * time.Second).Format(time.RFC3339Nano), Headers: map[string]string{"Content-Length": "3", "Content-Type": "application/octet-stream", "Content-Disposition": "attachment", "Cache-Control": "private, no-store", "If-None-Match": "*", "x-amz-checksum-sha256": "ungWv48Bz+pBQUDeXa4iI7ADYaOWF3qctBD/YfIAFa0=", "x-amz-meta-vecnet-checksum-profile": "sha256", "x-amz-meta-vecnet-checksum": "ungWv48Bz+pBQUDeXa4iI7ADYaOWF3qctBD/YfIAFa0="}}})
		case strings.HasSuffix(r.URL.Path, "/complete"):
			json.NewEncoder(w).Encode(reviewRef())
		case r.URL.Path == "/v1/messages":
			sends++
			raw, _ := io.ReadAll(r.Body)
			if err := envelope.Validate("sendRequest", raw); err != nil {
				t.Error("send failed published schema:", err)
				w.WriteHeader(400)
				return
			}
			var req envelope.SendRequest
			json.Unmarshal(raw, &req)
			if len(req.Attachments) != 1 || req.Attachments[0].BlobID != reviewID {
				t.Error("lost R2 reference")
			}
			json.NewEncoder(w).Encode(map[string]any{"message_id": reviewMsg, "thread_id": reviewThread, "deliveries": []any{}})
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &mints, &sends
}
func reviewBool(v bool) *bool { return &v }
func reviewRef() blobwire.Ref {
	return blobwire.Ref{BlobID: reviewID, SHA256: reviewSHA, Size: 3, RetentionClass: "keep", SHA256Verified: reviewBool(true), Integrity: &blobwire.Integrity{Algorithm: "sha256", Value: reviewSHA, Verified: true}}
}
func reviewArgs(t *testing.T, command string) []string {
	t.Helper()
	if command == "get" {
		return []string{"blob", "get", reviewSHA, "--blob-id", reviewID, "--out", filepath.Join(t.TempDir(), "saved"), "--json"}
	}
	path := filepath.Join(t.TempDir(), "attach.txt")
	if err := os.WriteFile(path, []byte("abc"), 0600); err != nil {
		t.Fatal(err)
	}
	if command == "reply" {
		return []string{"reply", reviewMsg, "hello", "--thread", reviewThread, "--hold", "--attach", path}
	}
	return []string{"send", reviewPeer, "hello", "--hold", "--attach", path}
}

func TestBlobTransfersResponseBodyFailures(t *testing.T) {
	for _, command := range []string{"get", "send", "reply"} {
		stages := []string{"discovery", "grant"}
		if command == "get" {
			stages = append(stages, "disk-get", "disk-local-get", "provider-get")
		} else {
			stages = append(stages, "completion", "disk-upload")
		}
		for _, stage := range stages {
			for _, mode := range []string{"disconnect", "malformed-json", "complete-json-disconnect"} {
				if strings.HasSuffix(stage, "get") && mode != "disconnect" {
					continue // Byte bodies have no JSON validation.
				}
				for _, jsonOutput := range []bool{false, true} {
					t.Run(command+"/"+stage+"/"+mode+"/json="+strconv.FormatBool(jsonOutput), func(t *testing.T) {
						srv, _, sends := reviewBlobHub(t, time.Now)
						original := srv.Config.Handler
						failures := 0
						srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
							path := r.URL.Path
							fail := stage == "discovery" && path == "/v1/blob-capabilities" ||
								stage == "grant" && (path == "/v1/blobs/uploads" || strings.HasSuffix(path, "/download")) ||
								stage == "completion" && strings.HasSuffix(path, "/complete") ||
								stage == "disk-upload" && path == "/v1/blobs" ||
								(stage == "disk-get" || stage == "disk-local-get") && path == "/v1/blobs/"+reviewSHA ||
								stage == "provider-get" && path == "/object"
							if fail {
								failures++
								body := "{"
								if mode == "complete-json-disconnect" {
									body = "{}"
								}
								if mode != "malformed-json" {
									// The server closes with fewer bytes than promised.
									w.Header().Set("Content-Length", "100")
								}
								if _, err := io.WriteString(w, body); err != nil {
									t.Errorf("write body fixture: %v", err)
								}
								return
							}
							if strings.HasPrefix(stage, "disk-") {
								if path == "/v1/blob-capabilities" || strings.HasSuffix(path, "/download") {
									if stage == "disk-local-get" {
										value := any(map[string]any{"backend": "disk", "max_size": 25 << 20})
										if strings.HasSuffix(path, "/download") {
											value = blobwire.DownloadGrant{SHA256: reviewSHA, Size: 3, LocalPath: "/v1/blobs/" + reviewSHA}
										}
										if err := json.NewEncoder(w).Encode(value); err != nil {
											t.Errorf("encode disk fixture: %v", err)
										}
									} else {
										w.WriteHeader(http.StatusNotFound) // Old-hub disk fallback.
									}
									return
								}
							}
							original.ServeHTTP(w, r)
						})
						const secret = "fake-private-hub-secret"
						_, env := pinTestEnv(t, fmt.Sprintf("[profiles.p]\nhub=%q\nsecret=%q\n", srv.URL, secret))
						args := reviewArgs(t, command)
						dir := t.TempDir()
						if command == "get" {
							args = []string{"blob", "get", reviewSHA, "--out", filepath.Join(dir, "saved")}
							if !strings.HasPrefix(stage, "disk-") {
								args = append(args, "--blob-id", reviewID)
							}
						}
						if jsonOutput {
							args = append(args, "--json")
						}
						t.Setenv("TMPDIR", dir)
						code, out, errs := pinRun(env, args...)
						wantExit, reason := 3, "blob_unavailable"
						if mode == "malformed-json" {
							wantExit, reason = 1, "bad_response"
						} else if stage == "provider-get" {
							wantExit = 1
						}
						wantErr := "vecnet: " + reason + "\n"
						if wantExit == 3 {
							wantErr = "vecnet: network: " + reason + "\n"
						}
						if code != wantExit || errs != wantErr {
							t.Errorf("exit=%d stderr=%q; want exit=%d stderr=%q", code, errs, wantExit, wantErr)
						}
						if command == "get" && jsonOutput {
							var wire struct {
								Error struct{ Code string } `json:"error"`
							}
							if json.Unmarshal([]byte(out), &wire) != nil || wire.Error.Code != reason {
								t.Error("missing sanitized machine-readable failure")
							}
						} else if out != "" {
							t.Error("failed transfer reported output")
						}
						for _, forbidden := range []string{srv.URL, secret, "cap=", "unexpected EOF", "Authorization"} {
							if strings.Contains(out+errs, forbidden) {
								t.Error("failure leaked transport details")
							}
						}
						if failures != 1 || *sends != 0 {
							t.Errorf("failed requests=%d sends=%d; want 1 and 0", failures, *sends)
						}
						if entries, err := os.ReadDir(dir); err != nil || len(entries) != 0 {
							t.Error("failure left output or spool")
						}
					})
				}
			}
		}
	}
}

func TestR2CLISendReferenceSchema(t *testing.T) {
	for _, cmd := range []string{"send", "reply"} {
		t.Run(cmd, func(t *testing.T) {
			srv, _, sends := reviewBlobHub(t, time.Now)
			_, env := pinTestEnv(t, fmt.Sprintf("[profiles.p]\nhub=%q\nsecret=\"fake\"\n", srv.URL))
			code, _, errs := pinRun(env, reviewArgs(t, cmd)...)
			if code != 0 || *sends != 1 {
				t.Fatalf("send exit=%d calls=%d error=%s", code, *sends, errs)
			}
		})
	}
}
func TestR2BlobFollowPinnedHub(t *testing.T) {
	for _, cmd := range []string{"get", "send", "reply"} {
		t.Run(cmd, func(t *testing.T) {
			dst, _, _ := reviewBlobHub(t, time.Now)
			key := testIdentityKey(t, "1")
			var notice envelope.HubNotice
			old := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/v1/blob-capabilities" {
					json.NewEncoder(w).Encode(map[string]any{"backend": "r2", "checksum_profile": "sha256"})
					return
				}
				w.WriteHeader(410)
				json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"code": "hub_moved", "details": map[string]any{"notice": notice, "private": "must not propagate"}}})
			}))
			defer old.Close()
			notice = envelope.HubNotice{NoticeID: "ntc_01ARZ3NDEKTSV4RRFFQ69G5FAV", Kind: "moving", Hub: old.URL, NewURL: dst.URL, Message: "move", IssuedAt: envelope.FormatTime(time.Now().Add(-time.Second)), ExpiresAt: envelope.FormatTime(time.Now().Add(time.Hour))}
			if err := key.SignNotice(&notice); err != nil {
				t.Fatal(err)
			}
			path, env := pinTestEnv(t, fmt.Sprintf("[profiles.p]\nhub=%q\nhub_pubkey=%q\nsecret=\"fake\"\n", old.URL, key.Pubkey()))
			code, out, errs := pinRun(env, reviewArgs(t, cmd)...)
			if code != 0 || strings.Contains(out, "hub_moved") || !strings.Contains(errs, "verified") {
				t.Fatalf("follow exit=%d err=%s", code, errs)
			}
			cfg, err := loadConfig(path)
			if err != nil || cfg.Profiles["p"].Hub != dst.URL {
				t.Fatal("profile did not follow")
			}
		})
	}
}
func TestBlobLabelsMatchPlanExpiryWording(t *testing.T) {
	name := "report.txt"
	cases := []struct{ state, class, want string }{
		{"expired", "1h", "Attachment expired (1-hour retention) report.txt"},
		{"expired", "6h", "Attachment expired (6-hour retention) report.txt"},
		{"expired", "12h", "Attachment expired (12-hour retention) report.txt"},
		{"expired", "1d", "Attachment expired (24-hour retention) report.txt"},
		{"expired", "7d", "Attachment expired (7-day retention) report.txt"},
		{"expired", "30d", "Attachment expired (30-day retention) report.txt"},
		{"expired", "keep", "Attachment expired report.txt"},
		{"purged", "1d", "[attachment purged: report.txt] (24-hour retention)"},
		{"available", "1d", "[attachment: report.txt] (24-hour retention)"},
		{"available", "keep", "[attachment: report.txt] (Kept while referenced)"},
	}
	for _, c := range cases {
		att := envelope.Attachment{BlobID: reviewID, Name: name, RetentionClass: c.class}
		got := blobLabels([]envelope.Attachment{att}, map[string]blobwire.State{reviewID: {State: c.state}})
		if len(got) != 1 || got[0] != c.want {
			t.Errorf("%s %s = %q, want %q", c.state, c.class, got, c.want)
		}
	}
}

func TestAttachmentNameStripsAndCaps(t *testing.T) {
	long := strings.Repeat("日", 256)
	cases := []struct{ path, want string }{
		{"report.txt", "report.txt"},
		{filepath.Join("dir", "report.txt"), "report.txt"},
		{`dir\secret.txt`, "secret.txt"},
		{`..\..\secret.txt`, "secret.txt"},
		{"a\u202ebidi.txt", "abidi.txt"},
		{"a\u2066b\u2069.txt", "ab.txt"},
		{"note\r\n.txt", "note .txt"},
		{"\x1b[31mred.txt", "red.txt"},
		{"\x1b[2J", "file"},
		{long, strings.Repeat("日", 255)},
	}
	for _, c := range cases {
		if got := attachmentName(c.path); got != c.want {
			t.Errorf("attachmentName(%q) = %q, want %q", c.path, got, c.want)
		}
	}
}

func TestBlobLabelsHonorAvailableProjection(t *testing.T) {
	past := "2000-01-01T00:00:00Z" // workstation is later; fresh hub projection wins
	att := envelope.Attachment{BlobID: reviewID, Name: "file", ExpiresAt: &past}
	labels := blobLabels([]envelope.Attachment{att}, map[string]blobwire.State{reviewID: {State: "available"}})
	if len(labels) != 1 || strings.Contains(labels[0], "expired") {
		t.Fatal("local time overrode hub projection")
	}
}

func TestR2CLIRepeatableGetAfterCapabilityExpiry(t *testing.T) {
	now := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	prev := blobNow
	blobNow = func() time.Time { return now }
	t.Cleanup(func() { blobNow = prev })
	srv, mints, _ := reviewBlobHub(t, blobNow)
	_, env := pinTestEnv(t, fmt.Sprintf("[profiles.p]\nhub=%q\nsecret=\"fake\"\n", srv.URL))
	for i := 0; i < 3; i++ {
		if i > 0 {
			// The prior capability is unusable, while the stable reference still works.
			resp, err := http.Get(srv.URL + "/object?cap=" + strconv.Itoa(i))
			if err != nil {
				t.Fatal("fake capability request failed")
			}
			resp.Body.Close()
			if resp.StatusCode != 403 {
				t.Fatal("old capability still works")
			}
		}
		args := reviewArgs(t, "get")
		code, out, errs := pinRun(env, args...)
		if code != 0 || strings.Contains(out, "url") || strings.Contains(out+errs, "cap=") {
			t.Fatalf("explicit get %d failed: exit=%d", i, code)
		}
		b, err := os.ReadFile(args[6])
		if err != nil || string(b) != "abc" {
			t.Fatal("missing verified file")
		}
		if *mints != i+1 {
			t.Fatal("explicit get did not mint exactly once")
		}
		now = now.Add(601 * time.Second)
	}
}
