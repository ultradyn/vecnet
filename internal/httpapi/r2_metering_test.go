package httpapi_test

import (
	"context"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/blob"
)

// Production bootstrap (hub.New via testhub) must meter every provider HTTP
// attempt it makes, including adapter retries, without a test-installed hook.
// The in-process fake S3 counts what actually arrived. Fake-only evidence; not
// provider acceptance and not provider billing.
func TestR2HTTPProductionMetersProviderAttempts(t *testing.T) {
	f := newR2HTTP(t)
	ctx := context.Background()
	_, secret := f.env.NewPeer("session", "metered")
	usage := func() blob.TransferUsage {
		t.Helper()
		u, err := f.env.Hub.Blobs.Transfers.Usage(ctx)
		if err != nil {
			t.Fatal("usage read failed")
		}
		return u
	}
	if got := usage().Requests; got != (blob.R2RequestCounts{}) {
		t.Fatalf("fresh hub counted requests: %+v", got)
	}
	g := f.upload(t, secret, "metered")
	f.download(t, secret, g)
	// A retryable provider outage: the adapter makes three attempts per probe.
	f.mu.Lock()
	f.headFailure = true
	f.mu.Unlock()
	resp, _ := f.request(t, secret, "GET", "/v1/blobs/"+g.SHA256+"/download?blob_id="+g.BlobID, nil, "")
	if resp.StatusCode != 503 {
		t.Fatalf("outage download status %d", resp.StatusCode)
	}
	f.mu.Lock()
	f.headFailure = false
	f.mu.Unlock()
	if err := f.env.Hub.PurgeBlob(ctx, g.SHA256); err != nil {
		t.Fatal("purge failed")
	}
	f.env.Now.Advance(10 * time.Minute)
	if err := f.env.Hub.Blobs.Transfers.Tick(ctx); err != nil {
		t.Fatal("cleanup tick failed")
	}
	f.mu.Lock()
	heads, deletes := f.calls["HEAD"], f.calls["DELETE"]
	f.mu.Unlock()
	got := usage().Requests
	if heads != 5 || deletes != 1 {
		t.Fatalf("fake provider saw HEAD=%d DELETE=%d", heads, deletes)
	}
	want := blob.R2RequestCounts{Head: int64(heads), HeadFailed: 3, Delete: int64(deletes)}
	if got != want {
		t.Fatalf("production metering = %+v, want %+v", got, want)
	}
}
