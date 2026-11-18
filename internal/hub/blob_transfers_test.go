package hub

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/auth"
	"github.com/XertroV/vecnet/internal/blob"
	"github.com/XertroV/vecnet/internal/blobwire"
)

func transferCaller(f *transferFixture) auth.Caller {
	return auth.Caller{Kind: "operator", PeerID: f.env.OperatorPeer, PrincipalID: f.env.OperatorPrincipal}
}
func requireBlobCode(t *testing.T, err error, code string) {
	t.Helper()
	var e *APIError
	if !errors.As(err, &e) || e.Code != code {
		t.Fatalf("expected %s, got %v", code, err)
	}
}
func downloadFixture(t *testing.T) (*transferFixture, blobwire.UploadGrant, auth.Caller) {
	t.Helper()
	f := newTransfers(t)
	g := f.reserve(t, "download", 3)
	f.complete(t, g)
	f.env.Hub.BlobSignDownload = func(ctx context.Context, o blob.R2Object) (blobwire.Request, error) {
		ttl, err := blob.R2DownloadTTL(f.env.Now.Now(), o.ExpiresAt)
		if err != nil {
			return blobwire.Request{}, err
		}
		return blobwire.Request{Method: "GET", URL: "https://fake.invalid/object", ExpiresAt: f.env.Now.Now().Add(ttl).Format(time.RFC3339Nano)}, nil
	}
	return f, g, transferCaller(f)
}
func TestR2TokenCannotSelectAnotherIncarnation(t *testing.T) {
	f, g, c := downloadFixture(t)
	g2 := f.reserve(t, "second", 3)
	f.complete(t, g2)
	h := f.env.Hub
	tok := h.Blobs.TokenV2(c.PeerID, g.SHA256, g.BlobID, h.Now().Add(time.Hour))
	if _, err := h.BlobDownload(context.Background(), nil, tok, g.SHA256, g.BlobID); err != nil {
		t.Fatal(err)
	}
	_, err := h.BlobDownload(context.Background(), nil, tok, g.SHA256, g2.BlobID)
	requireBlobCode(t, err, "forbidden")
	for _, id := range []string{g.BlobID, g2.BlobID} {
		_, err = h.BlobDownload(context.Background(), nil, h.Blobs.Token(c.PeerID, g.SHA256, h.Now().Add(time.Hour)), g.SHA256, id)
		requireBlobCode(t, err, "forbidden")
	}
	if _, err = h.DB.Exec(`UPDATE peers SET disabled=1 WHERE peer_id=?`, c.PeerID); err != nil {
		t.Fatal(err)
	}
	_, err = h.BlobDownload(context.Background(), nil, tok, g.SHA256, g.BlobID)
	requireBlobCode(t, err, "forbidden")
}
func TestR2RepeatableDownloadAndStateBeforeLimit(t *testing.T) {
	f, g, c := downloadFixture(t)
	h := f.env.Hub
	ctx := context.Background()
	first, err := h.BlobDownload(ctx, &c, "", g.SHA256, g.BlobID)
	if err != nil {
		t.Fatal(err)
	}
	f.env.Now.Advance(601 * time.Second)
	next, err := h.BlobDownload(ctx, &c, "", g.SHA256, g.BlobID)
	if err != nil || next.ExpiresAt == first.ExpiresAt {
		t.Fatal("retained uploader cannot re-mint")
	}
	for i := 1; i < 30; i++ {
		if _, err = h.BlobDownload(ctx, &c, "", g.SHA256, g.BlobID); err != nil {
			t.Fatal(err)
		}
	}
	denied, err := h.BlobDownload(ctx, &c, "", g.SHA256, g.BlobID)
	requireBlobCode(t, err, "blob_url_rate_limited")
	if denied.URL != "" {
		t.Fatal("denial exposed URL")
	}
	var n int
	if err = h.DB.QueryRow(`SELECT count(*) FROM blob_usage_events WHERE kind='download_grant_issued' AND byte_count=0 AND caller_identity<>''`).Scan(&n); err != nil || n != 31 {
		t.Fatalf("issuance ledger count %d", n)
	}
	f.env.Now.Advance(24 * time.Hour)
	_, err = h.BlobDownload(ctx, &c, "", g.SHA256, g.BlobID)
	requireBlobCode(t, err, "blob_expired")
	_, err = h.BlobDownload(ctx, nil, "", g.SHA256, g.BlobID)
	requireBlobCode(t, err, "forbidden")
	if err = h.PurgeBlob(ctx, g.SHA256); err != nil {
		t.Fatal(err)
	}
	_, err = h.BlobDownload(ctx, &c, "", g.SHA256, g.BlobID)
	requireBlobCode(t, err, "blob_purged")
}
func TestR2ConcurrentMintLimit(t *testing.T) {
	// Exercise mint accounting independently of power-loss durability: slow
	// per-commit FULL syncs can exhaust SQLite's writer busy timeout. NORMAL
	// keeps the real concurrent pool and transactions on this disposable DB.
	t.Setenv("VECNET_SQLITE_SYNCHRONOUS", "normal")
	f, g, c := downloadFixture(t)
	var successes atomic.Int32
	var issued, denied atomic.Int32
	f.env.Hub.BlobGrantUsage = func(event BlobGrantEvent) {
		if event.BlobID != g.BlobID || event.CallerIdentity == "" || event.At.IsZero() {
			t.Error("invalid usage event")
		}
		switch event.Kind {
		case "download_grant_issued":
			issued.Add(1)
			if event.Status != 200 {
				t.Error("invalid issuance status")
			}
		case "download_grant_rate_limited":
			denied.Add(1)
			if event.Status != 429 {
				t.Error("invalid denial status")
			}
		default:
			t.Error("unexpected usage kind")
		}
	}
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Go(func() {
			started := time.Now()
			v, err := f.env.Hub.BlobDownload(context.Background(), &c, "", g.SHA256, g.BlobID)
			if err == nil && v.URL != "" {
				successes.Add(1)
			} else {
				var e *APIError
				if !errors.As(err, &e) || e.Status != http.StatusTooManyRequests {
					t.Errorf("unexpected concurrent refusal: error=%T %v api_error=%#v url_present=%t elapsed=%s pool=%+v", err, err, e, v.URL != "", time.Since(started), f.env.Hub.DB.Stats())
				}
			}
		})
	}
	wg.Wait()
	if successes.Load() != 30 || issued.Load() != 30 || denied.Load() != 20 {
		t.Fatalf("concurrency accounting: success=%d issued=%d denied=%d", successes.Load(), issued.Load(), denied.Load())
	}
	f.env.Now.Advance(60 * time.Second)
	if _, err := f.env.Hub.BlobDownload(context.Background(), &c, "", g.SHA256, g.BlobID); err != nil {
		t.Fatal(err)
	}
}
func TestR2FinalEligibilityAfterSigningWait(t *testing.T) {
	for _, change := range []string{"expire", "purge", "disable", "provider_error"} {
		t.Run(change, func(t *testing.T) {
			f, g, c := downloadFixture(t)
			h := f.env.Hub
			h.BlobSignDownload = func(ctx context.Context, o blob.R2Object) (blobwire.Request, error) {
				switch change {
				case "expire":
					f.env.Now.Advance(24 * time.Hour)
				case "purge":
					if err := h.PurgeBlob(ctx, g.SHA256); err != nil {
						t.Fatal(err)
					}
				case "disable":
					if _, err := h.DB.Exec(`UPDATE peers SET disabled=1 WHERE peer_id=?`, c.PeerID); err != nil {
						t.Fatal(err)
					}
				case "provider_error":
					return blobwire.Request{}, errors.New("untrusted provider text")
				}
				return blobwire.Request{Method: "GET", URL: "https://fake.invalid/object", ExpiresAt: h.Now().Add(600 * time.Second).Format(time.RFC3339Nano)}, nil
			}
			got, err := h.BlobDownload(context.Background(), &c, "", g.SHA256, g.BlobID)
			if err == nil || got.URL != "" {
				t.Fatal("capability escaped final check")
			}
			var n int
			if err = h.DB.QueryRow(`SELECT count(*) FROM blob_usage_events WHERE kind='download_grant_issued'`).Scan(&n); err != nil || n != 0 {
				t.Fatal("failed mint metered as success")
			}
		})
	}
}

func TestR2PendingHashCannotProbePurge(t *testing.T) {
	f := newTransfers(t)
	g := f.reserve(t, "pending-probe", 3)
	c := transferCaller(f)
	if err := f.env.Hub.PurgeBlob(context.Background(), g.SHA256); err != nil {
		t.Fatal(err)
	}
	_, err := f.env.Hub.BlobDownload(context.Background(), &c, "", g.SHA256, g.BlobID)
	requireBlobCode(t, err, "upload_incomplete")
}
func TestR2ExpiryAndPurgeWinAtMintLimit(t *testing.T) {
	for _, state := range []string{"expiry", "purge"} {
		t.Run(state, func(t *testing.T) {
			f, g, c := downloadFixture(t)
			h := f.env.Hub
			f.env.Now.Advance(24*time.Hour - 10*time.Second)
			for i := 0; i < 30; i++ {
				if _, err := h.BlobDownload(context.Background(), &c, "", g.SHA256, g.BlobID); err != nil {
					t.Fatal(err)
				}
			}
			code := "blob_expired"
			if state == "expiry" {
				f.env.Now.Advance(10 * time.Second)
			} else {
				code = "blob_purged"
				if err := h.PurgeBlob(context.Background(), g.SHA256); err != nil {
					t.Fatal(err)
				}
			}
			result, err := h.BlobDownload(context.Background(), &c, "", g.SHA256, g.BlobID)
			requireBlobCode(t, err, code)
			if result.URL != "" {
				t.Fatal("state error carried URL")
			}
		})
	}
}
func TestR2DeliveryTokenRedactionAndRevocationDuringSigning(t *testing.T) {
	f, g, c := downloadFixture(t)
	h := f.env.Hub
	if err := h.DB.QueryRow(`SELECT cred_id FROM credentials WHERE kind='operator'`).Scan(&c.CredentialID); err != nil {
		t.Fatal(err)
	}
	h.BlobSignDownload = func(ctx context.Context, o blob.R2Object) (blobwire.Request, error) {
		if _, err := h.DB.Exec(`UPDATE credentials SET revoked_at=? WHERE cred_id=?`, h.Now().Format(time.RFC3339Nano), c.CredentialID); err != nil {
			t.Fatal(err)
		}
		return blobwire.Request{Method: "GET", URL: "https://fake.invalid/object", ExpiresAt: h.Now().Add(time.Minute).Format(time.RFC3339Nano)}, nil
	}
	result, err := h.BlobDownload(context.Background(), &c, "", g.SHA256, g.BlobID)
	requireBlobCode(t, err, "forbidden")
	if result.URL != "" {
		t.Fatal("revoked credential received capability")
	}
}
