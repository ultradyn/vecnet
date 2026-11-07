package hub

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/auth"
	"github.com/XertroV/vecnet/internal/blob"
	"github.com/XertroV/vecnet/internal/blobwire"
	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/ids"
	"github.com/XertroV/vecnet/internal/store"
)

// This transport can only reach the fixture's local TLS fake, including when
// production R2 bootstrap constructs the HEAD/completion eligibility adapters.
type oauthR2Transport struct {
	base   http.RoundTripper
	target *url.URL
}

func (r oauthR2Transport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Host != "00000000000000000000000000000000.r2.cloudflarestorage.com" {
		return nil, fmt.Errorf("unexpected test provider host")
	}
	clone := req.Clone(req.Context())
	u := *req.URL
	clone.URL = &u
	clone.URL.Scheme, clone.URL.Host = r.target.Scheme, r.target.Host
	return r.base.RoundTrip(clone)
}

func oauthTransferFixture(t *testing.T) (*transferFixture, auth.Caller) {
	t.Helper()
	f := newTransfers(t)
	h := f.env.Hub
	target, err := url.Parse(f.server.URL)
	if err != nil {
		t.Fatal(err)
	}
	h.Cfg.BlobBackend = "r2"
	h.Cfg.R2 = R2Config{AccountID: "00000000000000000000000000000000", Bucket: "test-bucket", ChecksumProfile: "sha256", StorageQuotaBytes: 100, AccessKeyID: "FAKEACCESS", SecretAccessKey: "fake-signing-secret", Client: &http.Client{Transport: oauthR2Transport{base: f.server.Client().Transport, target: target}}}
	if err := h.initR2(); err != nil {
		t.Fatal(err)
	}
	f.s = h.Blobs.Transfers
	now := envelope.FormatTime(h.Now())
	prn, otherPrn := ids.New(ids.Prn), ids.New(ids.Prn)
	peer, reboundPeer, otherPeer := ids.New(ids.Peer), ids.New(ids.Peer), ids.New(ids.Peer)
	for _, stmt := range []struct {
		query string
		args  []any
	}{
		{`INSERT INTO principals(prn_id,name,created_at) VALUES(?,'OAuth',?)`, []any{prn, now}},
		{`INSERT INTO principals(prn_id,name,created_at) VALUES(?,'Other',?)`, []any{otherPrn, now}},
		{`INSERT INTO peers(peer_id,prn_id,name,kind,enrolled_at) VALUES(?,?,'dot','dot',?)`, []any{peer, prn, now}},
		{`INSERT INTO peers(peer_id,prn_id,name,kind,enrolled_at) VALUES(?,?,'rebound','dot',?)`, []any{reboundPeer, prn, now}},
		{`INSERT INTO peers(peer_id,prn_id,name,kind,enrolled_at) VALUES(?,?,'other','dot',?)`, []any{otherPeer, otherPrn, now}},
		{`INSERT INTO oauth_principals(prn_id,subject,peer_id,created_at) VALUES(?,'oauth:test',?,?)`, []any{prn, peer, now}},
		{`INSERT INTO oauth_principals(prn_id,subject,peer_id,created_at) VALUES(?,'oauth:other',?,?)`, []any{otherPrn, otherPeer, now}},
	} {
		if _, err := h.DB.Exec(stmt.query, stmt.args...); err != nil {
			t.Fatal(err)
		}
	}
	binding := auth.OAuthCredential{TokenHash: auth.HashSecret("vat_test_only"), ClientID: "test-client", Resource: "https://hub.invalid/mcp", Scope: "vecnet events:read", RequiredScope: "vecnet", ExpiresAt: h.Now().Add(time.Hour)}
	if _, err := h.DB.Exec(`INSERT INTO oauth_tokens(token_hash,kind,client_id,principal_id,scope,resource,expires_at,created_at) VALUES(?,'access',?,?,?,?,?,?)`, binding.TokenHash, binding.ClientID, prn, binding.Scope, binding.Resource, envelope.FormatTime(binding.ExpiresAt), now); err != nil {
		t.Fatal(err)
	}
	// Fail at setup with the underlying query/parse error if the fixture does
	// not represent a token that real OAuth authentication would accept.
	token, err := store.ReadOAuthAccessToken(context.Background(), h.DB, binding.TokenHash)
	if err != nil {
		t.Fatalf("OAuth fixture access-token query: %v", err)
	}
	if _, err := envelope.ParseTime(token.ExpiresAt); err != nil {
		t.Fatalf("OAuth fixture token expiry: %v", err)
	}
	c := auth.Caller{Kind: "principal", PrincipalID: prn, PeerID: peer, PeerKind: "dot", OAuth: binding}
	if !auth.OAuthCallerActive(context.Background(), h.DB, c, h.Now()) {
		t.Fatal("OAuth fixture caller must be active before any provider wait")
	}
	return f, c
}

var oauthTransferChanges = []string{"revoke", "expire_clock", "expire_row", "kind", "audience", "scope", "scope_binding", "client", "principal", "retire_binding", "peer_binding", "peer_principal", "peer_kind", "disable", "invalid_expiry"}

func changeTransferOAuth(t *testing.T, f *transferFixture, c auth.Caller, change string) {
	t.Helper()
	if change == "expire_clock" {
		f.env.Now.Advance(time.Hour)
		return
	}
	q := ""
	var args []any
	switch change {
	case "revoke":
		q = `UPDATE oauth_tokens SET revoked_at=? WHERE token_hash=?`
		args = []any{envelope.FormatTime(f.env.Now.Now()), c.OAuth.TokenHash}
	case "expire_row":
		q = `UPDATE oauth_tokens SET expires_at=? WHERE token_hash=?`
		args = []any{envelope.FormatTime(f.env.Now.Now()), c.OAuth.TokenHash}
	case "invalid_expiry":
		q = `UPDATE oauth_tokens SET expires_at='invalid' WHERE token_hash=?`
	case "kind":
		q = `UPDATE oauth_tokens SET kind='refresh' WHERE token_hash=?`
	case "audience":
		q = `UPDATE oauth_tokens SET resource='https://other.invalid/mcp' WHERE token_hash=?`
	case "scope":
		q = `UPDATE oauth_tokens SET scope='events:read' WHERE token_hash=?`
	case "scope_binding":
		q = `UPDATE oauth_tokens SET scope='vecnet' WHERE token_hash=?`
	case "client":
		q = `UPDATE oauth_tokens SET client_id='other-client' WHERE token_hash=?`
	case "principal":
		q = `UPDATE oauth_tokens SET principal_id=(SELECT prn_id FROM oauth_principals WHERE subject='oauth:other') WHERE token_hash=?`
	case "retire_binding":
		q = `UPDATE oauth_principals SET peer_id=NULL WHERE prn_id=?`
		args = []any{c.PrincipalID}
	case "peer_binding":
		q = `UPDATE oauth_principals SET peer_id=(SELECT peer_id FROM peers WHERE prn_id=? AND name='rebound') WHERE prn_id=?`
		args = []any{c.PrincipalID, c.PrincipalID}
	case "peer_principal":
		q = `UPDATE peers SET prn_id=(SELECT prn_id FROM oauth_principals WHERE subject='oauth:other') WHERE peer_id=?`
		args = []any{c.PeerID}
	case "peer_kind":
		q = `UPDATE peers SET kind='session' WHERE peer_id=?`
		args = []any{c.PeerID}
	case "disable":
		q = `UPDATE peers SET disabled=1 WHERE peer_id=?`
		args = []any{c.PeerID}
	default:
		t.Fatalf("unknown mutation %s", change)
	}
	if args == nil {
		args = []any{c.OAuth.TokenHash}
	}
	if _, err := f.env.Hub.DB.Exec(q, args...); err != nil {
		t.Fatal(err)
	}
}

// Block a real operation, mutate authorization on another connection, and only
// then release it. Cleanup releases the wait even if a test assertion fails.
func blockTransferOAuth(t *testing.T, f *transferFixture, c auth.Caller, change string, install func(func()), run func() error) error {
	t.Helper()
	started, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	install(func() { close(started); <-release })
	done := make(chan error, 1)
	go func() { done <- run() }()
	select {
	case <-started:
	case err := <-done:
		t.Fatalf("operation did not wait: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("operation never reached provider wait")
	}
	changeTransferOAuth(t, f, c, change)
	unblock()
	select {
	case err := <-done:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("operation did not finish after wait")
		return nil
	}
}

func TestR2OAuthDownloadRevalidatesAfterProviderWait(t *testing.T) {
	for _, wait := range []string{"signing", "head"} {
		for _, change := range oauthTransferChanges {
			t.Run(wait+"/"+change, func(t *testing.T) {
				f, c := oauthTransferFixture(t)
				h := f.env.Hub
				g, err := h.CreateBlobUpload(context.Background(), c, "oauth-download", blobwire.UploadRequest{Size: 3, SHA256: hexSHA([]byte("abc")), MediaType: "text/plain"})
				if err != nil {
					t.Fatal(err)
				}
				f.upload(t, g)
				if _, err := h.CompleteBlobUpload(context.Background(), c, g.BlobID); err != nil {
					t.Fatal(err)
				}
				var got blobwire.DownloadGrant
				err = blockTransferOAuth(t, f, c, change, func(block func()) {
					if wait == "head" {
						f.mu.Lock()
						f.headWait = block
						f.mu.Unlock()
					} else {
						h.BlobSignDownload = func(ctx context.Context, o blob.R2Object) (blobwire.Request, error) {
							block()
							return blobwire.Request{Method: "GET", URL: "https://fake.invalid/signed-capability", ExpiresAt: h.Now().Add(time.Minute).Format(time.RFC3339Nano)}, nil
						}
					}
				}, func() error {
					var e error
					got, e = h.BlobDownload(context.Background(), &c, "", g.SHA256, g.BlobID)
					return e
				})
				requireBlobCode(t, err, "forbidden")
				if got.URL != "" {
					t.Fatal("OAuth authorization change leaked download capability")
				}
				if n := f.number(t, `SELECT count(*) FROM blob_usage_events WHERE kind='download_grant_issued'`); n != 0 {
					t.Fatal("denied OAuth mint recorded as issued")
				}
			})
		}
	}
}

func TestR2OAuthCompletionRevalidatesInsideTransactionAfterHEAD(t *testing.T) {
	for _, change := range oauthTransferChanges {
		t.Run(change, func(t *testing.T) {
			f, c := oauthTransferFixture(t)
			h := f.env.Hub
			g, err := h.CreateBlobUpload(context.Background(), c, "oauth-complete", blobwire.UploadRequest{Size: 3, SHA256: hexSHA([]byte("abc")), MediaType: "text/plain"})
			if err != nil {
				t.Fatal(err)
			}
			f.upload(t, g)
			var got blobwire.Ref
			err = blockTransferOAuth(t, f, c, change, func(block func()) { f.mu.Lock(); f.headWait = block; f.mu.Unlock() }, func() error {
				var e error
				got, e = h.CompleteBlobUpload(context.Background(), c, g.BlobID)
				return e
			})
			requireBlobCode(t, err, "forbidden")
			if got.BlobID != "" {
				t.Fatal("denied OAuth completion returned reference")
			}
			o := f.object(t, g.BlobID)
			if o.State != "pending" || o.SHA256Verified || o.VerifiedAt != nil {
				t.Fatal("OAuth authorization change committed completion")
			}
			f.charge(t, 3, 0)
		})
	}
}

func TestR2OAuthUploadRevalidatesAfterSigning(t *testing.T) {
	f, c := oauthTransferFixture(t)
	// PUT signing is local and runs under Reserve's writer transaction, so
	// another database writer cannot revoke or rebind a token during it. Time
	// can still advance; exercise expiry without inventing an external wait.
	sign := f.cfg.SignPUT
	f.cfg.SignPUT = func(ctx context.Context, o blob.R2Object) (blobwire.Request, error) {
		f.env.Now.Advance(time.Hour)
		return sign(ctx, o)
	}
	f.restart(t)
	got, err := f.env.Hub.CreateBlobUpload(context.Background(), c, "oauth-put-expiry", blobwire.UploadRequest{Size: 3, SHA256: hexSHA([]byte("abc")), MediaType: "text/plain"})
	requireBlobCode(t, err, "forbidden")
	if got.Upload.URL != "" {
		t.Fatal("expired OAuth credential leaked upload capability")
	}
}

func TestR2OAuthUnchangedAuthorizationCompletesAndDownloads(t *testing.T) {
	f, c := oauthTransferFixture(t)
	h := f.env.Hub
	g, err := h.CreateBlobUpload(context.Background(), c, "oauth-success", blobwire.UploadRequest{Size: 3, SHA256: hexSHA([]byte("abc")), MediaType: "text/plain"})
	if err != nil {
		t.Fatal(err)
	}
	f.upload(t, g)
	if _, err := h.CompleteBlobUpload(context.Background(), c, g.BlobID); err != nil {
		t.Fatal(err)
	}
	got, err := h.BlobDownload(context.Background(), &c, "", g.SHA256, g.BlobID)
	if err != nil {
		t.Fatal(err)
	}
	if got.URL == "" || got.BlobID != g.BlobID {
		t.Fatal("active OAuth caller lost download capability")
	}
	f.charge(t, 0, 3)
	if n := f.number(t, `SELECT count(*) FROM blob_usage_events WHERE kind='download_grant_issued' AND caller_identity=?`, c.PeerID); n != 1 {
		t.Fatal("successful OAuth download must record one grant for its bound peer")
	}
}
