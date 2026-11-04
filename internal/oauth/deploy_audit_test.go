package oauth_test

// Public-deploy audit (docs/research/2026-11-04-public-deploy-audit.md).
// These tests FAIL on master by design: each demonstrates a finding that must
// be fixed before (or soon after) the hub is reachable from the internet.

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

	"github.com/XertroV/vecnet/internal/testhub"
)

const auditLinkAlphabet = "ABCDEFGHJKMNPQRSTUVWXYZ23456789"

func auditGuess(t *testing.T, not string) string {
	t.Helper()
	for {
		b := make([]byte, 8)
		for i := range b {
			n, err := rand.Int(rand.Reader, big.NewInt(int64(len(auditLinkAlphabet))))
			if err != nil {
				t.Fatal(err)
			}
			b[i] = auditLinkAlphabet[n.Int64()]
		}
		if g := string(b); g != not {
			return g
		}
	}
}

// Finding S1: link codes (~39.6 bits, 10 min) have no attempt limit and no
// lockout. An anonymous client registers a DCR client, opens the consent form
// once and may then post guesses as fast as the hub answers. After 200 wrong
// guesses the live code must be burned (or the guesser throttled); today the
// guesses are all answered with the same retry page and the real code still
// binds, i.e. a guesser who eventually hits it takes over the dot.
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

	throttled := false
	const guesses = 200
	for range guesses {
		resp, _ := attacker.consent(request, auditGuess(t, link))
		if resp.StatusCode == http.StatusTooManyRequests {
			throttled = true
			break
		}
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("wrong guess: status %d", resp.StatusCode)
		}
	}
	if throttled {
		return
	}
	resp, _ = attacker.consent(request, link)
	if resp.StatusCode == http.StatusFound {
		t.Fatalf("after %d wrong link-code guesses from one client, none was throttled and the live code still bound the dot "+
			"(no attempt limit or lockout on POST /oauth/authorize)", guesses)
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
