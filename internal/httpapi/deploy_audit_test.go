package httpapi_test

// Public-deploy audit (docs/research/2026-11-04-public-deploy-audit.md).
// TestAuditGarbageEnrolSkipsWriteLock FAILS on master by design (finding S2).
// TestAuditPublicPathConfusion PASSES: it pins the verdict that no encoded or
// dotted path reaches a non-public handler without a bearer.

import (
	"bytes"
	"context"
	"database/sql"
	"net/http"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/testhub"
)

// Finding S2 (part): POST /v1/enrol with a token that does not exist opens a
// BEGIN IMMEDIATE transaction, so anonymous junk queues on the store's one
// write lock (and holds one of its four pooled connections meanwhile).
func TestAuditGarbageEnrolSkipsWriteLock(t *testing.T) {
	env := testhub.Start(t)

	held, done, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
	go func() {
		defer close(finished)
		_ = env.Hub.DB.Tx(context.Background(), func(*sql.Tx) error {
			close(held)
			<-done
			return nil
		})
	}()
	<-held
	defer func() { close(done); <-finished }()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, env.URL+"/v1/enrol",
		bytes.NewReader([]byte(`{"token":"vn_enrol_not-a-real-token"}`)))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	start := time.Now()
	resp, err := http.DefaultClient.Do(req)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("anonymous garbage /v1/enrol blocked on the store write lock for %s (%v)", elapsed.Round(time.Millisecond), err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("garbage enrol token: status %d after %s, want a quick 401", resp.StatusCode, elapsed.Round(time.Millisecond))
	}
}

// Guard (passes today): isPublic reads the decoded path while ServeMux
// matches escaped segments and redirects dotted paths; none of these
// spellings may reach an admin or peer handler without a bearer.
func TestAuditPublicPathConfusion(t *testing.T) {
	env := testhub.Start(t)
	for _, p := range []string{
		"/ui/../v1/admin/peers",
		"/ui/..%2fv1/admin/peers",
		"/ui%2f..%2fv1/admin/peers",
		"/v1/ingress/..%2f..%2fv1/admin/peers",
		"/v1/ingress%2f..%2fadmin/peers",
		"/v1/blobs/..%2fadmin%2fpeers?t=x",
		"/v1/blobs%2f..%2fadmin/peers?t=x",
		"/oauth/..%2fv1/admin/peers",
		"/.well-known/oauth-x/../../v1/admin/peers",
		"/v1/%61dmin/peers",
		"/mcp/../v1/admin/peers",
	} {
		req, err := http.NewRequest(http.MethodGet, env.URL+p, nil)
		if err != nil {
			t.Fatal(err)
		}
		noRedirect := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		resp, err := noRedirect.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		switch resp.StatusCode {
		case http.StatusUnauthorized, http.StatusNotFound, http.StatusForbidden, http.StatusBadRequest,
			http.StatusMovedPermanently, http.StatusTemporaryRedirect, http.StatusPermanentRedirect: // a redirect to the cleaned path, which is authenticated afresh
		default:
			t.Errorf("GET %s with no bearer: status %d", p, resp.StatusCode)
		}
	}
	// The same admin path with no bearer is 401, and with an anonymous
	// junk bearer too.
	for _, secret := range []string{"", "vn_operator_junk"} {
		resp, _ := env.Do(secret, http.MethodGet, "/v1/admin/peers", nil)
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("GET /v1/admin/peers bearer %q: %d", secret, resp.StatusCode)
		}
	}
}
