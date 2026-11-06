package httpapi_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/blob"
	"github.com/XertroV/vecnet/internal/blobwire"
	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/eventlog"
)

func TestR2HTTPHashOnlyStatusRequiresIDWithoutExistenceLeak(t *testing.T) {
	f := newR2HTTP(t)
	_, owner := f.env.NewPeer("session", "owner")
	_, stranger := f.env.NewPeer("session", "stranger")
	g := f.upload(t, owner, "status-missing-id")
	for role, secret := range map[string]string{"owner": owner, "peer": stranger, "operator": f.env.OperatorSecret} {
		t.Run(role, func(t *testing.T) {
			for _, sha := range []string{g.SHA256, shaOf("unknown")} {
				for _, suffix := range []string{"/status", ""} {
					resp, raw := f.request(t, secret, "GET", "/v1/blobs/"+sha+suffix, nil, "")
					if resp.StatusCode != 422 {
						t.Errorf("hash-only %q = %d, want 422", suffix, resp.StatusCode)
					}
					requireCode(t, raw, "blob_id_required")
					for _, field := range []string{"size", "expires_at", "url", "retention_class", "state"} {
						if strings.Contains(string(raw), `"`+field+`"`) {
							t.Errorf("missing ID disclosed %s", field)
						}
					}
					if resp.Header.Get("Location") != "" {
						t.Error("missing ID redirected to a capability")
					}
				}
			}
		})
	}
	resp, _ := f.request(t, "", "GET", "/v1/blobs/"+g.SHA256+"/status", nil, "")
	if resp.StatusCode != 401 {
		t.Fatal("status must authenticate before requiring ID")
	}
	f.env.Hub.Cfg.BlobBackend = "disk"
	old := putOK(t, f.env, f.env.OperatorSecret, "text/plain", "grandfathered status")
	f.env.Hub.Cfg.BlobBackend = "r2"
	resp, raw := f.request(t, f.env.OperatorSecret, "GET", "/v1/blobs/"+old.SHA256+"/status", nil, "")
	if resp.StatusCode != 200 || !strings.Contains(string(raw), `"state":"available"`) {
		t.Fatal("grandfathered disk status changed")
	}
	resp, raw = f.request(t, f.env.OperatorSecret, "GET", "/v1/blobs/"+old.SHA256, nil, "")
	if resp.StatusCode != 200 || string(raw) != "grandfathered status" {
		t.Fatal("grandfathered disk legacy streaming changed")
	}
	// Auto-spilled durable message data also keeps its hash-only status.
	recipient, recipientSecret := f.env.NewPeer("session", "durable reader")
	resp, _ = f.request(t, owner, "POST", "/v1/messages", map[string]any{"idempotency_key": "durable-status", "priority": "hold", "recipients": []string{recipient}, "content": map[string]any{"text": strings.Repeat("x", 5000)}}, "")
	if resp.StatusCode != 202 {
		t.Fatal("durable spill send failed")
	}
	resp, raw = f.request(t, recipientSecret, "GET", "/v1/blobs/"+shaOf(strings.Repeat("x", 5000))+"/status", nil, "")
	if resp.StatusCode != 200 || !strings.Contains(string(raw), `"state":"available"`) {
		t.Fatal("durable status changed")
	}
	resp, raw = f.request(t, recipientSecret, "GET", "/v1/blobs/"+shaOf(strings.Repeat("x", 5000)), nil, "")
	if resp.StatusCode != 200 || string(raw) != strings.Repeat("x", 5000) {
		t.Fatal("durable legacy streaming changed")
	}
	resp, _ = f.request(t, "", "GET", "/v1/blobs/"+g.SHA256, nil, "")
	if resp.StatusCode != 401 {
		t.Error("legacy GET must authenticate before requiring ID")
	}
}

func TestR2HTTPUnverifiedGrantSendExpiry(t *testing.T) {
	for _, purged := range []bool{false, true} {
		t.Run(fmt.Sprintf("purged=%t", purged), func(t *testing.T) {
			f := newR2HTTP(t)
			e := f.env
			_, owner := e.NewPeer("session", "uploader")
			_, stranger := e.NewPeer("session", "stranger")
			recipient, _ := e.NewPeer("session", "recipient")
			resp, raw := f.request(t, owner, "POST", "/v1/blobs/uploads", blobwire.UploadRequest{Size: 3, SHA256: shaOf("abc"), MediaType: "text/plain"}, "unverified-expiry")
			if resp.StatusCode != 201 {
				t.Fatalf("reserve status = %d, want 201", resp.StatusCode)
			}
			var g blobwire.UploadGrant
			if err := json.Unmarshal(raw, &g); err != nil {
				t.Fatal(err)
			}
			if g.RetentionClass != "1d" || g.ExpiresAt == nil || g.SHA256Verified == nil || *g.SHA256Verified {
				t.Fatal("expected an unverified default one-day grant")
			}
			if purged {
				if err := e.Hub.PurgeBlob(context.Background(), g.SHA256); err != nil {
					t.Fatal(err)
				}
			}
			// No PUT, completion or cleanup: send must use the recorded deadline,
			// and an unverified claim must never disclose a hash purge tombstone.
			for _, phase := range []struct {
				name    string
				advance time.Duration
				status  int
				code    string
			}{
				{"before", 24*time.Hour - time.Millisecond, 409, "upload_incomplete"},
				{"at", time.Millisecond, 410, "blob_expired"},
				{"after", time.Millisecond, 410, "blob_expired"},
			} {
				e.Now.Advance(phase.advance)
				t.Run(phase.name, func(t *testing.T) {
					for _, caller := range []struct {
						name   string
						secret string
						status int
						code   string
					}{
						{"owner", owner, phase.status, phase.code},
						{"stranger", stranger, 422, "unknown_blob"},
					} {
						t.Run(caller.name, func(t *testing.T) {
							message := map[string]any{"idempotency_key": phase.name + "-" + caller.name, "priority": "hold", "recipients": []string{recipient}, "content": map[string]any{"text": "unverified attachment"}, "attachments": []any{map[string]any{"blob_id": g.BlobID, "sha256": g.SHA256, "size": 3, "media_type": "text/plain", "retention_class": g.RetentionClass, "expires_at": g.ExpiresAt}}}
							resp, raw := f.request(t, caller.secret, "POST", "/v1/messages", message, "")
							if resp.StatusCode != caller.status {
								t.Errorf("send status = %d, want %d", resp.StatusCode, caller.status)
							}
							for _, table := range []string{"messages", "blob_object_refs", "blob_refs", "durable_blob_refs"} {
								var count int
								if err := e.Hub.DB.QueryRow("SELECT count(*) FROM " + table).Scan(&count); err != nil {
									t.Fatal(err)
								}
								if count != 0 {
									t.Errorf("rejected send inserted %s rows: %d", table, count)
								}
							}
							requireCode(t, raw, caller.code)
						})
					}
				})
			}
		})
	}
}

func TestR2HTTPUnknownLengthJSONOverflow(t *testing.T) {
	f := newR2HTTP(t)
	secret := f.env.OperatorSecret
	g := f.upload(t, secret, "body-overflow")
	grantJSON := `{"size":3,"sha256":"` + g.SHA256 + `","media_type":"text/plain"}`
	for name, endpoint := range map[string]string{"grant": "/v1/blobs/uploads", "completion": g.CompleteURL} {
		t.Run(name, func(t *testing.T) {
			valid := grantJSON
			if name == "completion" {
				valid = `{}`
			}
			for _, position := range []string{"leading", "trailing"} {
				t.Run(position, func(t *testing.T) {
					body := strings.Repeat(" ", 256<<10) + valid
					if position == "trailing" {
						body = valid + strings.Repeat(" ", 256<<10)
					}
					req, _ := http.NewRequest("POST", f.env.URL+endpoint, io.NopCloser(strings.NewReader(body)))
					req.ContentLength = -1 // exercise MaxBytesReader, not the Content-Length precheck
					req.Header.Set("Authorization", "Bearer "+secret)
					req.Header.Set("Content-Type", "application/json")
					req.Header.Set("Idempotency-Key", "overflow")
					resp, err := http.DefaultClient.Do(req)
					if err != nil {
						t.Fatal("chunked request failed")
					}
					defer resp.Body.Close()
					raw, err := io.ReadAll(resp.Body)
					if err != nil {
						t.Fatal("response read failed")
					}
					if resp.StatusCode != 413 {
						t.Fatalf("overflow = %d, want 413", resp.StatusCode)
					}
					requireCode(t, raw, "payload_too_large")
				})
			}
		})
	}
}

func TestR2HTTPDiskRollbackPreservesRemoteDiscoveryAndRead(t *testing.T) {
	f := newR2HTTP(t)
	_, owner := f.env.NewPeer("session", "owner")
	recipient, reader := f.env.NewPeer("session", "reader")
	g := f.upload(t, owner, "rollback")
	resp, _ := f.request(t, owner, "POST", "/v1/messages", map[string]any{"idempotency_key": "retained", "priority": "hold", "recipients": []string{recipient}, "content": map[string]any{"text": "retained"}, "attachments": []any{map[string]any{"blob_id": g.BlobID, "sha256": g.SHA256, "size": 3, "media_type": "text/plain", "retention_class": g.RetentionClass, "expires_at": g.ExpiresAt}}}, "")
	if resp.StatusCode != 202 {
		t.Fatal("retained send failed")
	}
	// Durable message bytes survive the same restart and remain attachable.
	durableText := strings.Repeat("d", 5000)
	resp, _ = f.request(t, owner, "POST", "/v1/messages", map[string]any{"idempotency_key": "rollback-durable", "priority": "hold", "recipients": []string{recipient}, "content": map[string]any{"text": durableText}}, "")
	if resp.StatusCode != 202 {
		t.Fatal("pre-restart durable spill failed")
	}
	f.env.Hub.Cfg.BlobBackend = "disk"
	f.env = f.env.CloneDB(t) // restart in disk mode with retained remote credentials and references
	for role, secret := range map[string]string{"owner": owner, "peer": reader, "operator": f.env.OperatorSecret} {
		t.Run(role, func(t *testing.T) {
			for _, sha := range []string{g.SHA256, shaOf("unknown rollback object")} {
				for _, route := range []string{"status", "download", ""} {
					resp, raw := f.request(t, secret, "GET", "/v1/blobs/"+sha+strings.TrimSuffix("/"+route, "/"), nil, "")
					if resp.StatusCode != 422 {
						t.Errorf("rollback hash-only %s = %d, want 422", route, resp.StatusCode)
					}
					requireCode(t, raw, "blob_id_required")
					for _, field := range []string{"size", "expires_at", "url", "retention_class", "state"} {
						if strings.Contains(string(raw), `"`+field+`"`) {
							t.Errorf("missing ID disclosed %s", field)
						}
					}
				}
			}
		})
	}
	for i, sha := range []string{g.SHA256, shaOf("unknown rollback object")} {
		attachment := map[string]any{"sha256": sha, "size": 3, "media_type": "text/plain"}
		message := map[string]any{"idempotency_key": fmt.Sprintf("hash-only-rollback-%d", i), "priority": "hold", "recipients": []string{recipient}, "content": map[string]any{"text": "hash-only"}, "attachments": []any{attachment}}
		resp, raw := f.request(t, owner, "POST", "/v1/messages", message, "")
		if resp.StatusCode != 422 {
			t.Errorf("rollback hash-only send = %d, want 422", resp.StatusCode)
		}
		requireCode(t, raw, "blob_id_required")
		for _, field := range []string{"size", "expires_at", "url", "retention_class", "state"} {
			if strings.Contains(string(raw), `"`+field+`"`) {
				t.Errorf("hash-only send disclosed %s", field)
			}
		}
		attachment["origin_url"] = "https://never-fetch.invalid/rollback"
		resp, _ = f.request(t, owner, "POST", "/v1/messages", message, "")
		if resp.StatusCode != 202 {
			t.Error("disk rollback rejected reference-only origin")
		}
	}
	resp, _ = f.request(t, owner, "POST", "/v1/messages", map[string]any{"idempotency_key": "rollback-durable-reference", "priority": "hold", "recipients": []string{recipient}, "content": map[string]any{"text": "durable reference"}, "attachments": []any{map[string]any{"sha256": shaOf(durableText), "size": len(durableText), "media_type": "text/markdown"}}}, "")
	if resp.StatusCode != 202 {
		t.Fatal("disk rollback rejected successful durable attachment")
	}
	resp, raw := f.request(t, reader, "GET", "/v1/blobs/"+shaOf(durableText), nil, "")
	if resp.StatusCode != 200 || string(raw) != durableText {
		t.Fatal("disk rollback changed durable legacy streaming")
	}
	resp, raw = f.request(t, reader, "GET", "/v1/blob-capabilities", nil, "")
	var caps struct {
		Backend    string   `json:"backend"`
		UploadMode string   `json:"upload_mode"`
		Origins    []string `json:"download_origins"`
	}
	if json.Unmarshal(raw, &caps) != nil || resp.StatusCode != 200 || caps.Backend != "disk" || caps.UploadMode != "legacy" || len(caps.Origins) != 1 || caps.Origins[0] != fakeOrigin {
		t.Fatal("rollback discovery omitted trusted remote origin or disk upload mode")
	}
	for _, route := range []string{"status", "download", ""} {
		resp, _ := f.request(t, "", "GET", "/v1/blobs/"+g.SHA256+strings.TrimSuffix("/"+route, "/"), nil, "")
		if resp.StatusCode != 401 && resp.StatusCode != 403 {
			t.Error("missing ID must authenticate before disclosing an error")
		}
	}
	grant := f.download(t, reader, g)
	u, err := url.Parse(grant.URL)
	if err != nil || u.Scheme+"://"+u.Host != caps.Origins[0] {
		t.Fatal("retained download origin not authorized by discovery")
	}
	f.mu.Lock()
	calls := len(f.calls)
	puts := f.calls["PUT"]
	f.mu.Unlock()
	local := putOK(t, f.env, owner, "text/plain", "new local upload")
	resp, raw = f.request(t, f.env.OperatorSecret, "GET", "/v1/blobs/"+local.SHA256+"/download", nil, "")
	var localGrant struct {
		LocalPath string `json:"local_path"`
		URL       string `json:"url"`
	}
	if json.Unmarshal(raw, &localGrant) != nil || resp.StatusCode != 200 || localGrant.LocalPath == "" || localGrant.URL != "" {
		t.Fatal("new disk upload was not local")
	}
	resp, raw = f.request(t, f.env.OperatorSecret, "GET", "/v1/blobs/"+local.SHA256+"/status", nil, "")
	if resp.StatusCode != 200 || !strings.Contains(string(raw), `"state":"available"`) {
		t.Fatal("disk rollback changed successful hash-only status")
	}
	resp, raw = f.request(t, f.env.OperatorSecret, "GET", localGrant.LocalPath, nil, "")
	if resp.StatusCode != 200 || string(raw) != "new local upload" {
		t.Fatal("local read failed")
	}
	resp, _ = f.request(t, owner, "POST", "/v1/messages", map[string]any{"idempotency_key": "rollback-local-send", "priority": "hold", "recipients": []string{recipient}, "content": map[string]any{"text": "local"}, "attachments": []any{map[string]any{"sha256": local.SHA256, "size": local.Size, "media_type": "text/plain"}}}, "")
	if resp.StatusCode != 202 {
		t.Fatal("disk rollback rejected successful local attachment")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.calls) != calls || f.calls["PUT"] != puts {
		t.Fatal("disk upload/read contacted fake S3")
	}
}

func TestR2HTTPMixedSameSHADeliveryAndPublishedSendSchema(t *testing.T) {
	f := newR2HTTP(t)
	_, owner := f.env.NewPeer("session", "owner")
	recipient, _ := f.env.NewPeer("session", "recipient")
	g := f.upload(t, owner, "mixed")
	f.env.Hub.Cfg.BlobBackend = "disk"
	disk := putOK(t, f.env, owner, "text/plain", "abc")
	f.env.Hub.Cfg.BlobBackend = "r2"
	if disk.SHA256 != g.SHA256 {
		t.Fatal("fixture must share SHA across storage backends")
	}
	remote := map[string]any{"blob_id": g.BlobID, "sha256": g.SHA256, "size": 3, "media_type": "text/plain", "retention_class": g.RetentionClass, "expires_at": g.ExpiresAt}
	body := map[string]any{"idempotency_key": "mixed-send", "priority": "wake", "recipients": []string{recipient}, "content": map[string]any{"text": "mixed"}, "attachments": []any{map[string]any{"sha256": disk.SHA256, "size": 3, "media_type": "text/plain"}, remote}}
	resp, _ := f.request(t, owner, "POST", "/v1/messages", body, "")
	if resp.StatusCode != 202 {
		t.Fatal("mixed send failed")
	}
	raw, _ := json.Marshal(body)
	if err := envelope.Validate("sendRequest", raw); err != nil {
		t.Errorf("original accepted request violates published schema: %v", err)
	}
	events, err := f.env.Hub.Log.Read(context.Background(), []string{recipient}, eventlog.Cursor{Epoch: f.env.Hub.Log.Epoch()}, 0)
	if err != nil {
		t.Fatal(err)
	}
	deliveries := 0
	for _, event := range events {
		if event.Type != "delivery" {
			continue
		}
		deliveries++
		var delivery envelope.Delivery
		if json.Unmarshal(event.Data, &delivery) != nil {
			t.Fatal("delivery decode failed")
		}
		if len(delivery.Message.Attachments) != 2 {
			t.Fatal("mixed attachments missing from delivery")
		}
		local, remote := delivery.Message.Attachments[0], delivery.Message.Attachments[1]
		if local.BlobID != "" || local.BlobURL == "" {
			t.Error("disk delivery lost local URL")
		}
		if remote.BlobURL != "" || remote.BlobID != g.BlobID || remote.SHA256 != g.SHA256 || remote.RetentionClass != g.RetentionClass || remote.ExpiresAt == nil || g.ExpiresAt == nil || *remote.ExpiresAt != *g.ExpiresAt || remote.SHA256Verified == nil || !*remote.SHA256Verified || remote.Integrity == nil || !remote.Integrity.Verified || remote.Integrity.Algorithm != "sha256" || remote.Integrity.Value != g.SHA256 {
			t.Error("R2 delivery changed stable reference, proof or decorated a URL")
		}
		if err := envelope.Validate("delivery", event.Data); err != nil {
			t.Error("mixed delivery violates schema")
		}
	}
	if deliveries != 1 {
		t.Fatalf("deliveries = %d, want 1", deliveries)
	}
	for _, field := range []string{"sha256_verified", "integrity"} {
		if field == "sha256_verified" {
			remote[field] = true
		} else {
			remote[field] = map[string]any{"algorithm": "sha256", "value": g.SHA256, "verified": true}
		}
		raw, _ := json.Marshal(body)
		if envelope.Validate("sendRequest", raw) == nil {
			t.Error("schema accepted caller verification field")
		}
		resp, _ := f.request(t, owner, "POST", "/v1/messages", body, "")
		if resp.StatusCode != 400 {
			t.Error("HTTP accepted caller verification field")
		}
		delete(remote, field)
	}
}

func TestR2HTTPCapabilityRetentionExactWithAdvancingClock(t *testing.T) {
	f := newR2HTTP(t)
	now := f.env.Hub.Now
	var ticks atomic.Int64
	f.env.Hub.Now = func() time.Time { return now().Add(time.Duration(ticks.Add(1)) * time.Millisecond) }
	resp, raw := f.request(t, f.env.OperatorSecret, "GET", "/v1/blob-capabilities", nil, "")
	var caps struct {
		Retention struct {
			Seconds *int64 `json:"seconds"`
		} `json:"retention"`
	}
	if json.Unmarshal(raw, &caps) != nil || resp.StatusCode != 200 {
		t.Fatal("capability decode failed")
	}
	if caps.Retention.Seconds == nil || *caps.Retention.Seconds != 86400 {
		t.Fatal("1d retention seconds must equal 86400 with an advancing clock")
	}
}

// These are offline fake-provider regressions, not provider acceptance.
func TestR2HTTPDownloadProviderLossAndOutage(t *testing.T) {
	for _, failure := range []string{"missing", "outage"} {
		t.Run(failure, func(t *testing.T) {
			f := newR2HTTP(t)
			_, owner := f.env.NewPeer("session", "owner")
			_, stranger := f.env.NewPeer("session", "stranger")
			g := f.upload(t, owner, "provider-loss")
			f.mu.Lock()
			heads := f.calls["HEAD"]
			if failure == "missing" {
				clear(f.objects)
			} else {
				f.headFailure = true
			}
			f.mu.Unlock()
			for _, route := range []string{"/download?blob_id=", "?b="} {
				resp, raw := f.request(t, stranger, "GET", "/v1/blobs/"+g.SHA256+route+g.BlobID, nil, "")
				if resp.StatusCode != 403 {
					t.Fatal("provider failure leaked to unauthorized peer")
				}
				requireCode(t, raw, "forbidden")
			}
			f.mu.Lock()
			unauthorizedHeads := f.calls["HEAD"]
			f.mu.Unlock()
			if unauthorizedHeads != heads {
				t.Fatal("unauthorized download probed provider")
			}
			for _, route := range []string{"/download?blob_id=", "?b="} {
				resp, raw := f.request(t, owner, "GET", "/v1/blobs/"+g.SHA256+route+g.BlobID, nil, "")
				if resp.StatusCode != 503 {
					t.Errorf("provider %s grant = %d, want 503", failure, resp.StatusCode)
				}
				requireCode(t, raw, "blob_unavailable")
				if resp.Header.Get("Location") != "" {
					t.Error("provider failure returned redirect capability")
				}
				for _, marker := range []string{`"url"`, "X-Amz-", "FAKEACCESS", "fake-signing-secret", fakeAccount} {
					if strings.Contains(string(raw), marker) {
						t.Error("provider failure leaked capability or provider detail")
					}
				}
			}
			var issued int
			if err := f.env.Hub.DB.QueryRow(`SELECT count(*) FROM blob_usage_events WHERE blob_id=? AND kind='download_grant_issued'`, g.BlobID).Scan(&issued); err != nil {
				t.Fatal("usage lookup failed")
			}
			f.mu.Lock()
			gets, probed := f.calls["GET"], f.calls["HEAD"] > heads
			f.mu.Unlock()
			if issued != 0 || gets != 0 || !probed {
				t.Errorf("provider failure issued=%d byte GETs=%d probed=%t", issued, gets, probed)
			}
		})
	}
}

func TestR2HTTPDownloadRechecksEligibilityAfterProviderWait(t *testing.T) {
	for _, change := range []string{"disable", "revoke", "purge", "expire"} {
		t.Run(change, func(t *testing.T) {
			f := newR2HTTP(t)
			peer, owner := f.env.NewPeer("session", "owner")
			g := f.upload(t, owner, "download-wait")
			var changed atomic.Bool
			f.mu.Lock()
			f.headWait = func() {
				if changed.Swap(true) {
					return
				}
				var err error
				switch change {
				case "disable":
					_, err = f.env.Hub.DB.Exec(`UPDATE peers SET disabled=1 WHERE peer_id=?`, peer)
				case "revoke":
					_, err = f.env.Hub.DB.Exec(`UPDATE credentials SET revoked_at=? WHERE subject_id=?`, f.env.Now.Now().Format(time.RFC3339Nano), peer)
				case "purge":
					err = f.env.Hub.PurgeBlob(context.Background(), g.SHA256)
				case "expire":
					f.env.Now.Advance(24 * time.Hour)
				}
				if err != nil {
					t.Error("provider-wait state change failed")
				}
			}
			// A simultaneous outage must not hide the authoritative post-wait
			// authorization/expiry/purge result behind provider availability.
			f.headFailure = true
			f.mu.Unlock()
			resp, raw := f.request(t, owner, "GET", "/v1/blobs/"+g.SHA256+"/download?blob_id="+g.BlobID, nil, "")
			code, status := "forbidden", 403
			if change == "expire" {
				code, status = "blob_expired", 410
			} else if change == "purge" {
				code, status = "blob_purged", 410
			}
			if resp.StatusCode != status || !changed.Load() {
				t.Errorf("post-wait %s = %d, want %d; wait reached=%t", change, resp.StatusCode, status, changed.Load())
			}
			requireCode(t, raw, code)
			if strings.Contains(string(raw), `"url"`) || resp.Header.Get("Location") != "" {
				t.Error("eligibility change returned capability")
			}
			var issued int
			if err := f.env.Hub.DB.QueryRow(`SELECT count(*) FROM blob_usage_events WHERE blob_id=? AND kind='download_grant_issued'`, g.BlobID).Scan(&issued); err != nil {
				t.Fatal("usage lookup failed")
			}
			f.mu.Lock()
			gets := f.calls["GET"]
			f.mu.Unlock()
			if issued != 0 || gets != 0 {
				t.Errorf("eligibility change issued=%d, byte GETs=%d", issued, gets)
			}
		})
	}
}

// Fake-only HTTP quota coverage; this does not establish provider acceptance.
func TestR2HTTPUploadDailyQuotaRetryUntilUTCMidnight(t *testing.T) {
	f := newR2HTTP(t)
	f.env.Hub.Cfg.R2.StorageQuotaBytes = 1 << 30
	f.env = f.env.CloneDB(t)
	_, secret := f.env.NewPeer("session", "quota uploader")
	now := f.env.Now.Now().UTC()
	start := time.Date(now.Year(), now.Month(), now.Day()+1, 23, 54, 0, 250000000, time.UTC)
	f.env.Now.Advance(start.Sub(now))
	in := blobwire.UploadRequest{Size: blob.DefaultMaxSize, SHA256: shaOf("quota reservation"), MediaType: "text/plain"}
	// Twenty full reservations exhaust the default daily quota without moving
	// object bytes. Expire their PUT deadlines so the active-grant cap cannot
	// mask the daily-quota response.
	for i := 0; i < 20; i++ {
		resp, _ := f.request(t, secret, "POST", "/v1/blobs/uploads", in, fmt.Sprintf("daily-%d", i))
		if resp.StatusCode != 201 {
			t.Fatalf("reservation %d = %d, want 201", i, resp.StatusCode)
		}
	}
	f.env.Now.Advance(5*time.Minute + 17*time.Second)
	for i, retry := range []int{43, 13} {
		resp, raw := f.request(t, secret, "POST", "/v1/blobs/uploads", in, fmt.Sprintf("over-daily-%d", i))
		requireR2QuotaResponse(t, resp, raw, "upload_quota_exceeded", retry)
		if i == 0 {
			f.env.Now.Advance(30 * time.Second)
		}
	}
	f.env.Now.Advance(13 * time.Second)
	resp, _ := f.request(t, secret, "POST", "/v1/blobs/uploads", in, "next-utc-day")
	if resp.StatusCode != 201 {
		t.Fatalf("new UTC day reservation = %d, want 201", resp.StatusCode)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.calls) != 0 {
		t.Fatal("quota-only grant requests contacted fake S3")
	}
}

func requireR2QuotaResponse(t *testing.T, resp *http.Response, raw []byte, code string, retry int) {
	t.Helper()
	if resp.StatusCode != 429 {
		t.Fatalf("quota response = %d, want 429", resp.StatusCode)
	}
	requireCode(t, raw, code)
	var body struct {
		Error struct {
			Details struct {
				RetryAfterSeconds int `json:"retry_after_seconds"`
			} `json:"details"`
		} `json:"error"`
	}
	if json.Unmarshal(raw, &body) != nil || body.Error.Details.RetryAfterSeconds != retry || resp.Header.Get("Retry-After") != strconv.Itoa(retry) {
		t.Fatalf("quota retry header/detail must both equal %d", retry)
	}
	for _, field := range []string{"url", "upload", "size", "expires_at"} {
		if strings.Contains(string(raw), `"`+field+`"`) {
			t.Errorf("quota refusal disclosed %s", field)
		}
	}
	if resp.Header.Get("Location") != "" {
		t.Error("quota refusal redirected to a capability")
	}
}
