package oauth_test

// Public-deploy audit (docs/research/2026-11-04-public-deploy-audit.md).
// Each test was red on master (9b62751) and pins the fix of one finding.

import (
	"context"
	"crypto/rand"
	"database/sql"
	"math/big"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/hub"
	"github.com/XertroV/vecnet/internal/testhub"
)

const auditLinkAlphabet = "ABCDEFGHJKMNPQRSTUVWXYZ23456789"

// auditGuess is a well-formed link code other than not.
func auditGuess(t *testing.T, not string) string {
	t.Helper()
	norm := strings.ReplaceAll(not, "-", "")
	for {
		b := make([]byte, hub.DotLinkLen)
		for i := range b {
			n, err := rand.Int(rand.Reader, big.NewInt(int64(len(auditLinkAlphabet))))
			if err != nil {
				t.Fatal(err)
			}
			b[i] = auditLinkAlphabet[n.Int64()]
		}
		if g := string(b); g != norm {
			return g
		}
	}
}

// auditCount counts audit rows of action, waiting up to 2 s for want: the
// link-code guard writes its rows after it answers.
func auditCount(t *testing.T, env *testhub.Env, action string, want int) int {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		var n int
		if err := env.Hub.DB.QueryRow(`SELECT count(*) FROM audit WHERE action = ?`, action).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n >= want || time.Now().After(deadline) {
			return n
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// Finding S1: link codes had no attempt limit and no lockout. Now the
// DotLinkFailureBudget-th wrong code across all clients burns every live
// code, audits each refusal and the lockout, and refuses link codes with 429
// and Retry-After until the operator issues a new code.
func TestAuditLinkCodeGuessesAreLimited(t *testing.T) {
	env := testhub.Start(t)
	attacker := newBrowser(t, env)
	peer := newDot(t, env, "victim-dot")
	link := linkCode(t, env, peer)

	resp, page := attacker.authorize(attacker.query(verifierA))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("authorize: %d %s", resp.StatusCode, page)
	}
	request := sealedRequest(t, page)

	for i := 1; i <= hub.DotLinkFailureBudget; i++ {
		resp, _ := attacker.consent(request, auditGuess(t, link))
		want := http.StatusBadRequest
		if i == hub.DotLinkFailureBudget {
			want = http.StatusTooManyRequests
		}
		if resp.StatusCode != want {
			t.Fatalf("wrong guess %d: status %d, want %d", i, resp.StatusCode, want)
		}
	}
	// Locked: even the real code is refused before it is looked at.
	resp, body := attacker.consent(request, link)
	if resp.StatusCode != http.StatusTooManyRequests || resp.Header.Get("Retry-After") == "" {
		t.Fatalf("during lockout: %d Retry-After=%q", resp.StatusCode, resp.Header.Get("Retry-After"))
	}
	if !strings.Contains(body, "Too many wrong link codes") || !strings.Contains(body, `name="link_code"`) {
		t.Fatalf("lockout page:\n%s", body)
	}
	if n := auditCount(t, env, "dot_link_refused", hub.DotLinkFailureBudget); n != hub.DotLinkFailureBudget {
		t.Fatalf("dot_link_refused rows = %d, want %d", n, hub.DotLinkFailureBudget)
	}
	if n := auditCount(t, env, "dot_link_lockout", 1); n != 1 {
		t.Fatalf("dot_link_lockout rows = %d, want 1", n)
	}

	// A new code from the operator ends the lockout; the old one was burned.
	fresh := linkCode(t, env, peer)
	if resp, _ := attacker.consent(request, link); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("burned code after lockout: %d, want 400", resp.StatusCode)
	}
	// People type it: lower case, no hyphens, stray spaces.
	typed := " " + strings.ToLower(strings.ReplaceAll(fresh, "-", "")) + " "
	if resp, _ := attacker.consent(request, typed); resp.StatusCode != http.StatusFound {
		t.Fatalf("fresh code typed loosely: %d, want 302", resp.StatusCode)
	}
}

// The lockout also expires by itself after DotLinkLockout.
func TestLinkCodeLockoutExpires(t *testing.T) {
	env := testhub.Start(t)
	b := newBrowser(t, env)
	peer := newDot(t, env, "dot")
	_, page := b.authorize(b.query(verifierA))
	request := sealedRequest(t, page)
	for range hub.DotLinkFailureBudget {
		b.consent(request, auditGuess(t, ""))
	}
	if env.Hub.DotLinkLocked() <= 0 {
		t.Fatal("not locked after the budget")
	}
	auditCount(t, env, "dot_link_lockout", 1)
	env.Now.Advance(hub.DotLinkLockout)
	if d := env.Hub.DotLinkLocked(); d != 0 {
		t.Fatalf("still locked for %s after DotLinkLockout", d)
	}
	// A fresh sealed form (the first expired with the clock) and a code issued
	// after the lockout work.
	_, page = b.authorize(b.query(verifierA))
	if resp, _ := b.consent(sealedRequest(t, page), linkCode(t, env, peer)); resp.StatusCode != http.StatusFound {
		t.Fatalf("after lockout expiry: %d", resp.StatusCode)
	}
}

// Finding S2 (part): POST /oauth/token with a code that does not exist opens
// a BEGIN IMMEDIATE transaction, so anonymous junk queues on the store's one
// write lock. A garbage code must be refused by a read alone, without
// waiting for the writer.
func TestAuditGarbageTokenRequestSkipsWriteLock(t *testing.T) {
	env := testhub.Start(t)
	b := newBrowser(t, env)

	release := auditHoldWriteLock(t, env)
	defer release()

	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	form := url.Values{
		"grant_type": {"authorization_code"}, "code": {"vac_not-a-real-code"}, "redirect_uri": {b.redirect},
		"client_id": {b.client}, "code_verifier": {verifierA},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, env.URL+"/oauth/token", strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := http.DefaultClient.Do(req)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("anonymous garbage /oauth/token blocked on the store write lock for %s (%v): anonymous requests can starve every writer", elapsed.Round(time.Millisecond), err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("garbage code: status %d after %s, want a quick 400 invalid_grant", resp.StatusCode, elapsed.Round(time.Millisecond))
	}
}

// auditHoldWriteLock takes the SQLite write lock (BEGIN IMMEDIATE via
// store.DB.Tx) on a background connection until release is called.
func auditHoldWriteLock(t *testing.T, env *testhub.Env) (release func()) {
	t.Helper()
	held, done := make(chan struct{}), make(chan struct{})
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		_ = env.Hub.DB.Tx(context.Background(), func(*sql.Tx) error {
			close(held)
			<-done
			return nil
		})
	}()
	select {
	case <-held:
	case <-time.After(5 * time.Second):
		t.Fatal("could not take the write lock")
	}
	return func() {
		close(done)
		<-finished
	}
}
