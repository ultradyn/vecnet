package hub_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/blobwire"
	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/hub"
	"github.com/XertroV/vecnet/internal/ids"
	"github.com/XertroV/vecnet/internal/testhub"
)

const stalledR2Account = "00000000000000000000000000000000"

// stalledR2Transport sends the production R2 origin to an in-process TLS fake;
// any other destination fails without network access.
type stalledR2Transport struct {
	base   http.RoundTripper
	target *url.URL
}

func (s stalledR2Transport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Host != stalledR2Account+".r2.cloudflarestorage.com" {
		return nil, http.ErrNotSupported
	}
	clone := r.Clone(r.Context())
	u := *r.URL
	u.Scheme, u.Host = s.target.Scheme, s.target.Host
	clone.URL = &u
	return s.base.RoundTrip(clone)
}

// A provider DELETE that never answers must not hold the hub's housekeeping
// ticker: approval and notice expiry keep running while R2 cleanup is stalled.
// Fake-only evidence (in-process S3 fake), not provider acceptance.
func TestR2StalledCleanupDoesNotBlockHubTimers(t *testing.T) {
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		select {
		case entered <- struct{}{}:
		default:
		}
		select {
		case <-release:
		case <-r.Context().Done():
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(srv.Close)
	target, _ := url.Parse(srv.URL)
	client := srv.Client()
	client.Transport = stalledR2Transport{base: client.Transport, target: target}
	env := testhub.StartWith(t, func(c *hub.Config) {
		c.BlobBackend = "r2"
		c.R2 = hub.R2Config{AccountID: stalledR2Account, Bucket: "test-bucket", ChecksumProfile: "sha256", StorageQuotaBytes: 100, AccessKeyID: "FAKEACCESS", SecretAccessKey: "fake-signing-secret", Client: client}
	})
	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	t.Cleanup(func() { cancel(); <-stopped })
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(release) }) })

	if _, err := env.Hub.Blobs.Transfers.Reserve(ctx, env.OperatorPeer, "stall", blobwire.UploadRequest{Size: 3, SHA256: shaHex("abc"), MediaType: "text/plain"}); err != nil {
		t.Fatal("reservation failed")
	}
	env.Now.Advance(7 * time.Minute) // abandoned, and past upload_deadline+60s
	go func() {
		defer close(stopped)
		env.Hub.RunTickers(ctx, 5*time.Millisecond)
	}()
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("cleanup never reached the provider DELETE")
	}

	// With DELETE stalled, new deadlines must still be honoured promptly.
	_, aSec := env.NewPeer("session", "A")
	b, _ := env.NewPeer("session", "B")
	apr := ids.New(ids.Apr)
	mustSend(t, env, aSec, approvalReq("stalled-r2", apr, env.Now.Now().Add(time.Hour), []string{"yes", "no"}, b))
	notice := postNotice(t, env, hub.NoticeRequest{Kind: "maintenance", Message: "stalled", ExpiresAt: envelope.FormatTime(env.Now.Now().Add(time.Hour))})
	env.Now.Advance(2 * time.Hour)
	deadline := time.Now().Add(10 * time.Second)
	for {
		row := loadApproval(t, env, apr)
		active, err := env.Hub.ActiveNotices(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		noticeLive := false
		for _, n := range active {
			noticeLive = noticeLive || n.NoticeID == notice.NoticeID
		}
		if row.decision != nil && *row.decision == "expired" && !noticeLive {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("stalled R2 DELETE blocked approval/notice expiry")
		}
		time.Sleep(5 * time.Millisecond)
	}
	once.Do(func() { close(release) })
}
