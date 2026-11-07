package httpapi_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/testhub"
)

// inviteStatus is GET/DELETE /v1/admin/peers/{id}/enrol-token: an invite's
// state, never its token (PROTOCOL §2.12).
type inviteStatus struct {
	PeerID    string `json:"peer_id"`
	State     string `json:"state"`
	ExpiresAt string `json:"expires_at"`
	RevokedAt string `json:"revoked_at"`
	Revoked   *bool  `json:"revoked"`
}

func decodeStatus(t *testing.T, raw []byte) inviteStatus {
	t.Helper()
	var s inviteStatus
	if err := json.Unmarshal(raw, &s); err != nil || s.PeerID == "" || s.State == "" {
		t.Fatalf("invite status %s: %v", raw, err)
	}
	var keys map[string]json.RawMessage
	_ = json.Unmarshal(raw, &keys)
	for k := range keys {
		if strings.Contains(k, "token") || k == "invite" || k == "secret" {
			t.Fatalf("invite status carries %q: %s", k, raw)
		}
	}
	return s
}

// noToken fails when any of tokens appears in raw.
func noToken(t *testing.T, what string, raw []byte, tokens ...string) {
	t.Helper()
	for _, tok := range tokens {
		if tok != "" && strings.Contains(string(raw), tok) {
			t.Fatalf("%s carries an enrol token", what)
		}
	}
}

// Revoke withdraws an unredeemed invite: the token stops enrolling, the
// status reads revoked, and the audit names enrol_revoke, never the token. A
// second revoke is a clean no-op, a fresh invite works again, and a redeemed
// credential is never touched (PROTOCOL §2.12).
func TestRevokeEnrolToken(t *testing.T) {
	env := testhub.Start(t)
	p := adminAddPeer(t, env, map[string]any{"kind": "session", "name": "pending"})
	path := "/v1/admin/peers/" + p.PeerID + "/enrol-token"

	raw := adminDo(t, env, "GET", path, nil, http.StatusOK)
	noToken(t, "status", raw, p.EnrolToken)
	st := decodeStatus(t, raw)
	if st.PeerID != p.PeerID || st.State != "pending" || st.ExpiresAt != envelope.FormatTime(env.Now.Now().Add(24*time.Hour)) || st.RevokedAt != "" {
		t.Fatalf("pending status = %+v", st)
	}

	env.Now.Advance(time.Minute)
	resp, raw := env.Do(env.OperatorSecret, "DELETE", path, nil)
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("DELETE = %d (Cache-Control %q); %s", resp.StatusCode, resp.Header.Get("Cache-Control"), raw)
	}
	noToken(t, "revoke answer", raw, p.EnrolToken)
	rv := decodeStatus(t, raw)
	if rv.Revoked == nil || !*rv.Revoked || rv.State != "revoked" || rv.RevokedAt != envelope.FormatTime(env.Now.Now()) || rv.ExpiresAt != "" {
		t.Fatalf("revoke = %s", raw)
	}

	// The revoked token no longer enrols.
	resp, raw = env.Do("", "POST", "/v1/enrol", map[string]string{"token": p.EnrolToken})
	mustErr(t, resp, raw, http.StatusUnauthorized, "unauthenticated")
	if st := decodeStatus(t, adminDo(t, env, "GET", path, nil, http.StatusOK)); st.State != "revoked" {
		t.Fatalf("status after revoke = %+v", st)
	}

	// Again: nothing live, so nothing changes and nothing is audited.
	again := decodeStatus(t, adminDo(t, env, "DELETE", path, nil, http.StatusOK))
	if again.Revoked == nil || *again.Revoked || again.State != "revoked" {
		t.Fatalf("second revoke = %+v", again)
	}

	// A new invite after a revoke works, and enrolling claims it.
	re := decodeInvite(t, adminDo(t, env, "POST", path, nil, http.StatusCreated))
	if st := decodeStatus(t, adminDo(t, env, "GET", path, nil, http.StatusOK)); st.State != "pending" || st.ExpiresAt != re.EnrolExpiresAt {
		t.Fatalf("status after reissue = %+v", st)
	}
	got := enrol(t, env, re.EnrolToken)
	if st := decodeStatus(t, adminDo(t, env, "GET", path, nil, http.StatusOK)); st.State != "claimed" || st.ExpiresAt != "" {
		t.Fatalf("status after enrol = %+v", st)
	}

	// Revoke never touches a redeemed credential: that is peer disable.
	noop := decodeStatus(t, adminDo(t, env, "DELETE", path, nil, http.StatusOK))
	if noop.Revoked == nil || *noop.Revoked || noop.State != "claimed" {
		t.Fatalf("revoke of an enrolled peer = %+v", noop)
	}
	resp, raw = env.Do(got.Secret, "GET", "/v1/peers/me", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("enrolled credential after revoke = %d %s", resp.StatusCode, raw)
	}

	// One enrol_revoke row, for the one revoke that spent a token; no audit
	// row anywhere carries a token.
	audit := adminDo(t, env, "GET", "/v1/admin/audit", nil, http.StatusOK)
	noToken(t, "audit", audit, p.EnrolToken, re.EnrolToken)
	if n := strings.Count(string(audit), `"enrol_revoke"`); n != 1 {
		t.Fatalf("enrol_revoke rows = %d; %s", n, audit)
	}
	var detail string
	if err := env.Hub.DB.QueryRow(`SELECT COALESCE(detail, '') FROM audit WHERE action = 'enrol_revoke'`).Scan(&detail); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(detail, "vn_enrol_") {
		t.Fatalf("enrol_revoke detail = %q", detail)
	}
}

// Every state the status read derives from the enrolment rows, with no
// stored state column: pending, expired, revoked, claimed, linked (an
// OAuth-bound dot or Grok Bot) and none (a dot that was never linked). A
// peer without a live invite revokes as a clean no-op.
func TestInviteStatusStates(t *testing.T) {
	env := testhub.Start(t)
	status := func(id string) inviteStatus {
		t.Helper()
		return decodeStatus(t, adminDo(t, env, "GET", "/v1/admin/peers/"+id+"/enrol-token", nil, http.StatusOK))
	}
	revoke := func(id string) inviteStatus {
		t.Helper()
		return decodeStatus(t, adminDo(t, env, "DELETE", "/v1/admin/peers/"+id+"/enrol-token", nil, http.StatusOK))
	}

	// Expired: past its TTL, still unspent. Revoke is a no-op; the token
	// still refuses as expired, not as revoked.
	short := adminAddPeer(t, env, map[string]any{"kind": "session", "name": "short", "enrol_ttl_seconds": 60})
	env.Now.Advance(2 * time.Minute)
	if st := status(short.PeerID); st.State != "expired" || st.ExpiresAt == "" {
		t.Fatalf("expired status = %+v", st)
	}
	if rv := revoke(short.PeerID); rv.Revoked == nil || *rv.Revoked || rv.State != "expired" {
		t.Fatalf("revoke of an expired invite = %+v", rv)
	}
	resp, raw := env.Do("", "POST", "/v1/enrol", map[string]string{"token": short.EnrolToken})
	mustErr(t, resp, raw, http.StatusGone, "enrol_token_expired")

	// A dot never holds an enrol token: none until linked, then linked.
	dot := adminDo(t, env, "POST", "/v1/admin/peers", map[string]any{"kind": "dot", "name": "d"}, http.StatusCreated)
	var d inviteResp
	_ = json.Unmarshal(dot, &d)
	if st := status(d.PeerID); st.State != "none" {
		t.Fatalf("dot status = %+v", st)
	}
	if rv := revoke(d.PeerID); rv.Revoked == nil || *rv.Revoked || rv.State != "none" {
		t.Fatalf("revoke of a dot = %+v", rv)
	}
	var prn string
	if err := env.Hub.DB.QueryRow(`SELECT prn_id FROM peers WHERE peer_id = ?`, d.PeerID).Scan(&prn); err != nil {
		t.Fatal(err)
	}
	if _, err := env.Hub.DB.Exec(`INSERT INTO oauth_principals VALUES(?,?,?,?)`, prn, "synthetic-subject", d.PeerID, envelope.FormatTime(env.Now.Now())); err != nil {
		t.Fatal(err)
	}
	if st := status(d.PeerID); st.State != "linked" {
		t.Fatalf("linked dot status = %+v", st)
	}

	// A peer rotated before it enrolled holds a credential: claimed.
	rot := adminAddPeer(t, env, map[string]any{"kind": "session", "name": "rotated"})
	adminDo(t, env, "POST", "/v1/admin/peers/"+rot.PeerID+"/rotate", nil, http.StatusOK)
	if st := status(rot.PeerID); st.State != "claimed" {
		t.Fatalf("rotated status = %+v", st)
	}

	// Unknown peers are 404; only the operator may ask.
	resp, raw = env.Do(env.OperatorSecret, "GET", "/v1/admin/peers/peer_nope/enrol-token", nil)
	mustErr(t, resp, raw, http.StatusNotFound, "not_found")
	resp, raw = env.Do(env.OperatorSecret, "DELETE", "/v1/admin/peers/peer_nope/enrol-token", nil)
	mustErr(t, resp, raw, http.StatusNotFound, "not_found")
	other := adminAddPeer(t, env, map[string]any{"kind": "session", "name": "other"})
	_, peerSecret := env.NewPeer("session", "nosy")
	for _, m := range []string{"GET", "DELETE"} {
		resp, raw = env.Do(peerSecret, m, "/v1/admin/peers/"+other.PeerID+"/enrol-token", nil)
		mustErr(t, resp, raw, http.StatusForbidden, "forbidden")
	}
	if st := status(other.PeerID); st.State != "pending" {
		t.Fatalf("a refused revoke changed the invite: %+v", st)
	}
	// DELETE takes no body.
	resp, raw = env.Do(env.OperatorSecret, "DELETE", "/v1/admin/peers/"+other.PeerID+"/enrol-token", map[string]any{"x": 1})
	mustErr(t, resp, raw, http.StatusBadRequest, "invalid_body")
}

// A new invite makes the previous unredeemed token unusable: at most one live
// invite per peer (PROTOCOL §2.12).
func TestReissueInvalidatesPriorToken(t *testing.T) {
	env := testhub.Start(t)
	p := adminAddPeer(t, env, map[string]any{"kind": "session", "name": "twice"})
	path := "/v1/admin/peers/" + p.PeerID + "/enrol-token"
	re := decodeInvite(t, adminDo(t, env, "POST", path, nil, http.StatusCreated))
	resp, raw := env.Do("", "POST", "/v1/enrol", map[string]string{"token": p.EnrolToken})
	mustErr(t, resp, raw, http.StatusUnauthorized, "unauthenticated")
	var live int
	if err := env.Hub.DB.QueryRow(`SELECT count(*) FROM enrol_tokens WHERE peer_id = ? AND used_at IS NULL`, p.PeerID).Scan(&live); err != nil || live != 1 {
		t.Fatalf("unspent tokens = %d, %v", live, err)
	}
	enrol(t, env, re.EnrolToken)
}

// reissueEnrol is one POST …/enrol-token from a racer. It returns the new
// token and never calls t (a test Fatalf from another goroutine does not
// fail the test). A non-201 is an error that names the status and code only.
func reissueEnrol(ctx context.Context, url, secret string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+secret)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	var body struct {
		EnrolToken string `json:"enrol_token"`
		Error      struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal(raw, &body)
	if resp.StatusCode != http.StatusCreated {
		return "", fmt.Errorf("status %d code %s", resp.StatusCode, body.Error.Code)
	}
	if body.EnrolToken == "" {
		return "", fmt.Errorf("status %d with no enrol token", resp.StatusCode)
	}
	return body.EnrolToken, nil
}

// Concurrent re-issues of one peer, released together, leave exactly one
// live token. The original and every superseded token are refused
// (PROTOCOL §2.12: at most one live invite per peer).
func TestConcurrentReissueOneLiveToken(t *testing.T) {
	env := testhub.Start(t)
	p := adminAddPeer(t, env, map[string]any{"kind": "session", "name": "racing"})
	path := env.URL + "/v1/admin/peers/" + p.PeerID + "/enrol-token"
	const racers = 8
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	release := make(chan struct{})
	arrived := make(chan struct{})
	tokens := make([]string, racers)
	errs := make([]error, racers)
	var wg sync.WaitGroup
	for i := range racers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			arrived <- struct{}{}
			<-release
			tokens[i], errs[i] = reissueEnrol(ctx, path, env.OperatorSecret)
		}(i)
	}
	for range racers {
		select {
		case <-arrived:
		case <-ctx.Done():
			t.Fatal("reissue did not reach the barrier")
		}
	}
	close(release)
	wg.Wait()

	seen := map[string]struct{}{}
	for i := range racers {
		if errs[i] != nil {
			t.Fatalf("reissue %d: %v", i, errs[i])
		}
		if tokens[i] == "" || tokens[i] == p.EnrolToken {
			t.Fatalf("reissue %d returned no new token", i)
		}
		seen[tokens[i]] = struct{}{}
	}
	if len(seen) != racers {
		t.Fatalf("distinct tokens = %d, want %d", len(seen), racers)
	}
	var unspent, rows int
	if err := env.Hub.DB.QueryRow(`SELECT count(*) FROM enrol_tokens WHERE peer_id = ? AND used_at IS NULL`, p.PeerID).Scan(&unspent); err != nil || unspent != 1 {
		t.Fatalf("unspent tokens = %d, %v", unspent, err)
	}
	if err := env.Hub.DB.QueryRow(`SELECT count(*) FROM enrol_tokens WHERE peer_id = ?`, p.PeerID).Scan(&rows); err != nil || rows != 1+racers {
		t.Fatalf("enrol token rows = %d, %v; want %d", rows, err, 1+racers)
	}

	resp, raw := env.Do("", "POST", "/v1/enrol", map[string]string{"token": p.EnrolToken})
	mustErr(t, resp, raw, http.StatusUnauthorized, "unauthenticated")
	live := 0
	for _, tok := range tokens {
		resp, raw = env.Do("", "POST", "/v1/enrol", map[string]string{"token": tok})
		if resp.StatusCode == http.StatusOK {
			live++
			var got enrolResp
			if json.Unmarshal(raw, &got) != nil || got.PeerID != p.PeerID || got.Secret == "" {
				t.Fatal("the live token enrolled as the wrong peer")
			}
			continue
		}
		mustErr(t, resp, raw, http.StatusUnauthorized, "unauthenticated")
	}
	if live != 1 {
		t.Fatalf("returned tokens that still enrol = %d, want 1", live)
	}
}
