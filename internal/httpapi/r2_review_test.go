package httpapi_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

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
				resp, raw := f.request(t, secret, "GET", "/v1/blobs/"+sha+"/status", nil, "")
				if resp.StatusCode != 422 {
					t.Fatalf("hash-only status = %d, want 422", resp.StatusCode)
				}
				requireCode(t, raw, "blob_id_required")
				if strings.Contains(string(raw), "size") || strings.Contains(string(raw), "expires_at") {
					t.Fatal("missing ID disclosed state")
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
	f.env.Hub.Cfg.BlobBackend = "disk"
	f.env = f.env.CloneDB(t) // restart in disk mode with retained remote credentials and references
	for role, secret := range map[string]string{"owner": owner, "peer": reader, "operator": f.env.OperatorSecret} {
		t.Run(role, func(t *testing.T) {
			for _, sha := range []string{g.SHA256, shaOf("unknown rollback object")} {
				for _, route := range []string{"status", "download"} {
					resp, raw := f.request(t, secret, "GET", "/v1/blobs/"+sha+"/"+route, nil, "")
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
	resp, raw := f.request(t, reader, "GET", "/v1/blob-capabilities", nil, "")
	var caps struct {
		Backend    string   `json:"backend"`
		UploadMode string   `json:"upload_mode"`
		Origins    []string `json:"download_origins"`
	}
	if json.Unmarshal(raw, &caps) != nil || resp.StatusCode != 200 || caps.Backend != "disk" || caps.UploadMode != "legacy" || len(caps.Origins) != 1 || caps.Origins[0] != fakeOrigin {
		t.Fatal("rollback discovery omitted trusted remote origin or disk upload mode")
	}
	for _, route := range []string{"status", "download"} {
		resp, _ := f.request(t, "", "GET", "/v1/blobs/"+g.SHA256+"/"+route, nil, "")
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
