package httpapi_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/eventlog"
	"github.com/XertroV/vecnet/internal/testhub"
)

// adminDo sends an operator request and fails unless the status is want.
func adminDo(t *testing.T, env *testhub.Env, method, path string, body any, want int) []byte {
	t.Helper()
	resp, raw := env.Do(env.OperatorSecret, method, path, body)
	if resp.StatusCode != want {
		t.Fatalf("%s %s = %d, want %d; %s", method, path, resp.StatusCode, want, raw)
	}
	return raw
}

type addPeerResp struct {
	PeerID      string `json:"peer_id"`
	EnrolToken  string `json:"enrol_token"`
	PrincipalID string `json:"principal_id"`
}

// adminAddPeer is POST /v1/admin/peers expecting 201.
func adminAddPeer(t *testing.T, env *testhub.Env, body map[string]any) addPeerResp {
	t.Helper()
	raw := adminDo(t, env, "POST", "/v1/admin/peers", body, http.StatusCreated)
	var r addPeerResp
	if err := json.Unmarshal(raw, &r); err != nil {
		t.Fatalf("decode: %v; %s", err, raw)
	}
	if !strings.HasPrefix(r.PeerID, "peer_") || r.EnrolToken == "" || !strings.HasPrefix(r.PrincipalID, "prn_") {
		t.Fatalf("POST /v1/admin/peers body %s, want peer_id, enrol_token, principal_id", raw)
	}
	return r
}

type enrolResp struct {
	PeerID string `json:"peer_id"`
	Secret string `json:"secret"`
}

// enrol is POST /v1/enrol {"token"} expecting 200.
func enrol(t *testing.T, env *testhub.Env, token string) enrolResp {
	t.Helper()
	resp, raw := env.Do("", "POST", "/v1/enrol", map[string]string{"token": token})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /v1/enrol = %d; %s", resp.StatusCode, raw)
	}
	var r enrolResp
	if err := json.Unmarshal(raw, &r); err != nil {
		t.Fatalf("decode: %v; %s", err, raw)
	}
	return r
}

func TestEnrolFlow(t *testing.T) {
	env := testhub.Start(t)
	p := adminAddPeer(t, env, map[string]any{"kind": "dot", "name": "alice"})

	got := enrol(t, env, p.EnrolToken)
	if got.PeerID != p.PeerID || got.Secret == "" {
		t.Fatalf("enrol = %+v, want peer %s and a secret", got, p.PeerID)
	}
	resp, raw := env.Do(got.Secret, "GET", "/v1/peers", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /v1/peers with the enrolled secret = %d; %s", resp.StatusCode, raw)
	}

	// The token is one-time.
	resp, raw = env.Do("", "POST", "/v1/enrol", map[string]string{"token": p.EnrolToken})
	mustErr(t, resp, raw, http.StatusUnauthorized, "unauthenticated")
	// An unknown token likewise.
	resp, raw = env.Do("", "POST", "/v1/enrol", map[string]string{"token": "vn_enrol_nope"})
	mustErr(t, resp, raw, http.StatusUnauthorized, "unauthenticated")

	// A peer credential is not the operator.
	resp, raw = env.Do(got.Secret, "POST", "/v1/admin/peers", map[string]any{"kind": "dot", "name": "mallory"})
	mustErr(t, resp, raw, http.StatusForbidden, "forbidden")
	resp, raw = env.Do(got.Secret, "GET", "/v1/admin/audit", nil)
	mustErr(t, resp, raw, http.StatusForbidden, "forbidden")
}

func TestImplicitAndExplicitPrincipal(t *testing.T) {
	env := testhub.Start(t)
	_, aSec := env.NewPeer("grokbot", "sender")

	// Without principal_id: an implicit principal named like the peer.
	imp := adminAddPeer(t, env, map[string]any{"kind": "dot", "name": "bob"})
	var name string
	if err := env.Hub.DB.QueryRow(`SELECT name FROM principals WHERE prn_id = ?`, imp.PrincipalID).Scan(&name); err != nil {
		t.Fatal(err)
	}
	if name != "bob" {
		t.Fatalf("implicit principal name = %q, want bob", name)
	}
	if n := rowCount(t, env, `SELECT count(*) FROM peers WHERE peer_id = ? AND prn_id = ?`, imp.PeerID, imp.PrincipalID); n != 1 {
		t.Fatalf("peer %s is not under its implicit principal %s", imp.PeerID, imp.PrincipalID)
	}

	// An explicit principal.
	raw := adminDo(t, env, "POST", "/v1/admin/principals", map[string]any{"name": "household"}, http.StatusCreated)
	var prn struct {
		PrincipalID string `json:"principal_id"`
		Secret      string `json:"secret"`
	}
	if err := json.Unmarshal(raw, &prn); err != nil || !strings.HasPrefix(prn.PrincipalID, "prn_") || prn.Secret == "" {
		t.Fatalf("POST /v1/admin/principals body %s", raw)
	}
	exp := adminAddPeer(t, env, map[string]any{"kind": "openclaw", "name": "claw", "principal_id": prn.PrincipalID})
	if exp.PrincipalID != prn.PrincipalID {
		t.Fatalf("explicit peer principal = %s, want %s", exp.PrincipalID, prn.PrincipalID)
	}
	enrol(t, env, imp.EnrolToken)
	enrol(t, env, exp.EnrolToken)

	// Move bob under the explicit principal: its deliveries reach the new
	// principal's stream.
	adminDo(t, env, "POST", "/v1/admin/peers/"+imp.PeerID+"/move", map[string]any{"principal_id": prn.PrincipalID}, http.StatusNoContent)
	if n := rowCount(t, env, `SELECT count(*) FROM peers WHERE peer_id = ? AND prn_id = ?`, imp.PeerID, prn.PrincipalID); n != 1 {
		t.Fatalf("peer %s was not moved to %s", imp.PeerID, prn.PrincipalID)
	}
	sr := postSend(t, env, aSec, sendBody("after-move", imp.PeerID), http.StatusAccepted)
	pr := mustPoll(t, env, prn.Secret, "?after="+zeroCursor(env))
	found := false
	for _, ev := range pr.Events {
		if ev.Type == "delivery" && recipientOf(t, ev) == imp.PeerID && strings.Contains(string(ev.Data), sr.MessageID) {
			found = true
		}
	}
	if !found {
		t.Fatalf("the new principal's stream lacks the moved peer's delivery of %s: %+v", sr.MessageID, pr.Events)
	}

	// Moving to an unknown principal is rejected.
	resp, body := env.Do(env.OperatorSecret, "POST", "/v1/admin/peers/"+imp.PeerID+"/move", map[string]any{"principal_id": "prn_01ARZ3NDEKTSV4RRFFQ69G5FAV"})
	mustErr(t, resp, body, http.StatusUnprocessableEntity, "invalid_value")
}

func TestRotateInvalidatesOld(t *testing.T) {
	env := testhub.Start(t)
	peer, old := env.NewPeer("dot", "carol")
	if resp, raw := env.Do(old, "GET", "/v1/peers", nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /v1/peers before rotate = %d; %s", resp.StatusCode, raw)
	}
	raw := adminDo(t, env, "POST", "/v1/admin/peers/"+peer+"/rotate", nil, http.StatusOK)
	var r struct {
		Secret string `json:"secret"`
	}
	if err := json.Unmarshal(raw, &r); err != nil || r.Secret == "" || r.Secret == old {
		t.Fatalf("rotate body %s, want a new secret", raw)
	}
	resp, body := env.Do(old, "GET", "/v1/peers", nil)
	mustErr(t, resp, body, http.StatusUnauthorized, "unauthenticated")
	if resp, raw := env.Do(r.Secret, "GET", "/v1/peers", nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /v1/peers with the rotated secret = %d; %s", resp.StatusCode, raw)
	}

	// A principal's credential rotates through /v1/admin/principals/{id}/rotate.
	prnID, prnOld := env.NewPrincipal("p")
	raw = adminDo(t, env, "POST", "/v1/admin/principals/"+prnID+"/rotate", nil, http.StatusOK)
	if err := json.Unmarshal(raw, &r); err != nil || r.Secret == "" || r.Secret == prnOld {
		t.Fatalf("principal rotate body %s, want a new secret", raw)
	}
	resp, body = env.Do(prnOld, "GET", "/v1/peers", nil)
	mustErr(t, resp, body, http.StatusUnauthorized, "unauthenticated")
}

// scriptedReceiver is a push route receiver: it echoes verification
// challenges and answers the n-th push (0-based) with status(n).
type scriptedReceiver struct {
	srv    *httptest.Server
	status func(n int) int

	mu   sync.Mutex
	hits []http.Header
}

func newScriptedReceiver(t *testing.T, status func(n int) int) *scriptedReceiver {
	t.Helper()
	r := &scriptedReceiver{status: status}
	r.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		body, _ := io.ReadAll(req.Body)
		var probe struct {
			Type      string `json:"type"`
			Challenge string `json:"challenge"`
		}
		if json.Unmarshal(body, &probe) == nil && probe.Type == "verification" {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]string{"challenge": probe.Challenge})
			return
		}
		r.mu.Lock()
		n := len(r.hits)
		r.hits = append(r.hits, req.Header.Clone())
		r.mu.Unlock()
		w.WriteHeader(r.status(n))
	}))
	t.Cleanup(r.srv.Close)
	return r
}

func (r *scriptedReceiver) Hits() []http.Header {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]http.Header(nil), r.hits...)
}

type deadLetter struct {
	DeliveryID string  `json:"delivery_id"`
	Recipient  string  `json:"recipient"`
	ThreadID   string  `json:"thread_id"`
	Attempt    int     `json:"attempt"`
	LastStatus *int    `json:"last_status"`
	LastError  *string `json:"last_error"`
	UpdatedAt  string  `json:"updated_at"`
}

func listDeadLetters(t *testing.T, env *testhub.Env) map[string]deadLetter {
	t.Helper()
	raw := adminDo(t, env, "GET", "/v1/admin/dead-letters", nil, http.StatusOK)
	var body struct {
		DeadLetters []deadLetter `json:"dead_letters"`
	}
	if err := json.Unmarshal(raw, &body); err != nil || body.DeadLetters == nil {
		t.Fatalf("GET /v1/admin/dead-letters body %s", raw)
	}
	out := map[string]deadLetter{}
	for _, d := range body.DeadLetters {
		out[d.DeliveryID] = d
	}
	return out
}

func dlyState(t *testing.T, env *testhub.Env, dly string) (state string, attempt, left int) {
	t.Helper()
	if err := env.Hub.DB.QueryRow(`SELECT state, attempt, attempts_left FROM deliveries WHERE dly_id = ?`, dly).
		Scan(&state, &attempt, &left); err != nil {
		t.Fatal(err)
	}
	return state, attempt, left
}

func TestDeadLetterRetryKeepsIdContinuesAttempt(t *testing.T) {
	env := testhub.Start(t)
	ctx := context.Background()
	a, aSec := env.NewPeer("grokbot", "a")
	b, _ := env.NewPeer("dot", "b")
	env.SetLimits(a, 1000, 1000)

	// Pushes 1–8 fail with 500, push 9 (the retry) succeeds, push 10 (a
	// second message) is rejected with 410.
	rcv := newScriptedReceiver(t, func(n int) int {
		switch {
		case n < 8:
			return http.StatusInternalServerError
		case n == 8:
			return http.StatusOK
		default:
			return http.StatusGone
		}
	})
	raw := adminDo(t, env, "POST", "/v1/admin/routes", map[string]any{"owner": b, "url": rcv.srv.URL}, http.StatusCreated)
	var route struct {
		RouteID string `json:"route_id"`
		Whsec   string `json:"whsec"`
	}
	if err := json.Unmarshal(raw, &route); err != nil || route.RouteID == "" || !strings.HasPrefix(route.Whsec, "whsec_") {
		t.Fatalf("POST /v1/admin/routes body %s", raw)
	}
	adminDo(t, env, "POST", "/v1/admin/routes/"+route.RouteID+"/verify", nil, http.StatusNoContent)

	sr := postSend(t, env, aSec, sendBody("dl-1", b), http.StatusAccepted)
	dly := sr.Deliveries[0].DeliveryID
	for i := 0; i < 8; i++ {
		if _, err := env.Worker.Step(ctx); err != nil {
			t.Fatal(err)
		}
		env.Now.Advance(2 * time.Hour)
	}
	if st, att, _ := dlyState(t, env, dly); st != "dead_letter" || att != 8 {
		t.Fatalf("after 8 failed pushes: state %s attempt %d, want dead_letter at 8", st, att)
	}
	dl, ok := listDeadLetters(t, env)[dly]
	if !ok || dl.Attempt != 8 || dl.Recipient != b || dl.ThreadID != sr.ThreadID || dl.LastStatus == nil || *dl.LastStatus != 500 {
		t.Fatalf("dead-letters entry for %s = %+v (listed %v)", dly, dl, ok)
	}

	adminDo(t, env, "POST", "/v1/admin/dead-letters/"+dly+"/retry", nil, http.StatusNoContent)
	if st, att, left := dlyState(t, env, dly); st != "queued" || att != 8 || left != 8 {
		t.Fatalf("after retry: state %s attempt %d attempts_left %d, want queued, 8, 8", st, att, left)
	}
	if _, err := env.Worker.Step(ctx); err != nil {
		t.Fatal(err)
	}
	hits := rcv.Hits()
	if len(hits) != 9 {
		t.Fatalf("receiver got %d pushes, want 9", len(hits))
	}
	if got := hits[8].Get("X-Vecnet-Attempt"); got != "9" {
		t.Fatalf("retry X-Vecnet-Attempt = %q, want 9", got)
	}
	if got := hits[8].Get("webhook-id"); got != dly || hits[0].Get("webhook-id") != dly {
		t.Fatalf("retry webhook-id = %q (first %q), want %s", got, hits[0].Get("webhook-id"), dly)
	}
	if st, _, _ := dlyState(t, env, dly); st != "accepted" {
		t.Fatalf("after successful retry: state %s, want accepted", st)
	}
	if _, listed := listDeadLetters(t, env)[dly]; listed {
		t.Fatalf("accepted delivery %s still listed as a dead letter", dly)
	}

	// Discard.
	sr2 := postSend(t, env, aSec, sendBody("dl-2", b), http.StatusAccepted)
	dly2 := sr2.Deliveries[0].DeliveryID
	if _, err := env.Worker.Step(ctx); err != nil {
		t.Fatal(err)
	}
	if _, listed := listDeadLetters(t, env)[dly2]; !listed {
		t.Fatalf("410-rejected delivery %s is not a listed dead letter", dly2)
	}
	adminDo(t, env, "POST", "/v1/admin/dead-letters/"+dly2+"/discard", nil, http.StatusNoContent)
	if st, _, _ := dlyState(t, env, dly2); st != "discarded" {
		t.Fatalf("after discard: state %s, want discarded", st)
	}
	if _, listed := listDeadLetters(t, env)[dly2]; listed {
		t.Fatalf("discarded delivery %s still listed", dly2)
	}
	if n := rowCount(t, env, `SELECT count(*) FROM audit WHERE action = 'dead_letter_discard' AND target_ids LIKE ?`, "%"+dly2+"%"); n != 1 {
		t.Fatalf("discard audit rows = %d, want 1", n)
	}
	// A discarded delivery is no longer a dead letter to retry.
	resp, body := env.Do(env.OperatorSecret, "POST", "/v1/admin/dead-letters/"+dly2+"/retry", nil)
	mustErr(t, resp, body, http.StatusNotFound, "not_found")
}

func TestThreadPatchEmitsUpdate(t *testing.T) {
	env := testhub.Start(t)
	a, aSec := env.NewPeer("grokbot", "a")
	b, _ := env.NewPeer("dot", "b")
	sr := postSend(t, env, aSec, sendBody("t-1", b), http.StatusAccepted)
	thr := sr.ThreadID

	adminDo(t, env, "PATCH", "/v1/admin/threads/"+thr, map[string]any{"budget_remaining": 0}, http.StatusNoContent)
	resp, raw := env.Do(aSec, "POST", "/v1/messages", inThread("t-2", thr))
	mustErr(t, resp, raw, http.StatusUnprocessableEntity, "thread_budget_exhausted")

	adminDo(t, env, "PATCH", "/v1/admin/threads/"+thr, map[string]any{"budget_remaining": 10, "title": "renamed"}, http.StatusNoContent)
	postSend(t, env, aSec, inThread("t-3", thr), http.StatusAccepted)

	for _, p := range []string{a, b} {
		evs, err := env.Hub.Log.Read(context.Background(), []string{p}, mustCursor(t, zeroCursor(env)), 0)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, ev := range evs {
			if ev.Type != "thread_updated" {
				continue
			}
			var s envelope.ThreadSummary
			if err := json.Unmarshal(ev.Data, &s); err != nil {
				t.Fatal(err)
			}
			if s.ThreadID == thr && s.Title != nil && *s.Title == "renamed" && s.BudgetRemaining != nil && *s.BudgetRemaining == 10 {
				found = true
			}
		}
		if !found {
			t.Fatalf("participant %s got no thread_updated with title renamed and budget 10", p)
		}
	}
	var title string
	var budget int
	if err := env.Hub.DB.QueryRow(`SELECT title, budget_remaining FROM threads WHERE thr_id = ?`, thr).Scan(&title, &budget); err != nil {
		t.Fatal(err)
	}
	if title != "renamed" || budget != 9 {
		t.Fatalf("thread row title %q budget %d, want renamed and 9", title, budget)
	}

	// Unknown fields and threads are rejected.
	resp, raw = env.Do(env.OperatorSecret, "PATCH", "/v1/admin/threads/"+thr, map[string]any{"colour": "red"})
	mustErr(t, resp, raw, http.StatusBadRequest, "invalid_body")
	resp, raw = env.Do(env.OperatorSecret, "PATCH", "/v1/admin/threads/thr_01ARZ3NDEKTSV4RRFFQ69G5FAV", map[string]any{"title": "x"})
	mustErr(t, resp, raw, http.StatusNotFound, "unknown_thread")
}

func TestAuditHasNoBodies(t *testing.T) {
	env := testhub.Start(t)
	_, aSec := env.NewPeer("grokbot", "a")
	b, bSec := env.NewPeer("dot", "b")
	const secretText = "zebra-crossing-7731 body text"
	body := sendBody("audit-1", b)
	body["content"] = map[string]any{"text": secretText}
	postSend(t, env, aSec, body, http.StatusAccepted)
	pr := mustPoll(t, env, bSec, "?after="+zeroCursor(env))
	if len(pr.Events) == 0 {
		t.Fatal("recipient has no events to ack")
	}
	resp, raw := env.Do(bSec, "POST", "/v1/ack", map[string]string{"cursor": pr.Events[len(pr.Events)-1].Cursor})
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("POST /v1/ack = %d; %s", resp.StatusCode, raw)
	}

	// Raw rows: no detail carries the text.
	rows, err := env.Hub.DB.Query(`SELECT action, target_ids, detail FROM audit`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	actions := map[string]bool{}
	for rows.Next() {
		var action, targets, detail string
		if err := rows.Scan(&action, &targets, &detail); err != nil {
			t.Fatal(err)
		}
		actions[action] = true
		if strings.Contains(detail, "zebra-crossing") || strings.Contains(targets, "zebra-crossing") {
			t.Fatalf("audit row %s carries the message text: %s %s", action, targets, detail)
		}
	}
	for _, want := range []string{"send", "ack", "enrol"} {
		if !actions[want] {
			t.Fatalf("audit actions %v lack %q", actions, want)
		}
	}

	// The admin API pages the same rows, newest first, without bodies.
	raw = adminDo(t, env, "GET", "/v1/admin/audit?limit=2", nil, http.StatusOK)
	var page struct {
		Entries []struct {
			ID      int64           `json:"id"`
			At      string          `json:"at"`
			Actor   string          `json:"actor"`
			Action  string          `json:"action"`
			Targets []string        `json:"target_ids"`
			Detail  json.RawMessage `json:"detail"`
		} `json:"entries"`
		Next *string `json:"next"`
	}
	if err := json.Unmarshal(raw, &page); err != nil {
		t.Fatalf("decode audit page: %v; %s", err, raw)
	}
	if len(page.Entries) != 2 || page.Next == nil || page.Entries[0].ID <= page.Entries[1].ID {
		t.Fatalf("audit page %s, want 2 entries newest first and a next token", raw)
	}
	seen := map[string]bool{}
	before := ""
	for i := 0; i < 100; i++ {
		raw = adminDo(t, env, "GET", "/v1/admin/audit?limit=50"+before, nil, http.StatusOK)
		if strings.Contains(string(raw), "zebra-crossing") {
			t.Fatalf("GET /v1/admin/audit carries the message text: %s", raw)
		}
		page.Next = nil
		page.Entries = nil
		if err := json.Unmarshal(raw, &page); err != nil {
			t.Fatal(err)
		}
		for _, e := range page.Entries {
			seen[e.Action] = true
		}
		if page.Next == nil {
			break
		}
		before = "&before=" + *page.Next
	}
	for _, want := range []string{"send", "ack", "enrol"} {
		if !seen[want] {
			t.Fatalf("GET /v1/admin/audit actions %v lack %q", seen, want)
		}
	}

	// Counters are readable.
	raw = adminDo(t, env, "GET", "/v1/admin/counters", nil, http.StatusOK)
	var ctr map[string]int
	if err := json.Unmarshal(raw, &ctr); err != nil {
		t.Fatalf("decode counters: %v; %s", err, raw)
	}
	for _, k := range []string{"loop_cut", "rate_limited", "dead_letters", "push_conflicts"} {
		if _, ok := ctr[k]; !ok {
			t.Fatalf("counters %s lack %q", raw, k)
		}
	}
}

func TestTickersExpire(t *testing.T) {
	env := testhub.Start(t)
	env.StartWorkers()
	_, aSec := env.NewPeer("grokbot", "a")
	b, _ := env.NewPeer("dot", "b")
	body := sendBody("tick-1", b)
	body["priority"] = "hold"
	body["expires_at"] = envelope.FormatTime(env.Now.Now().Add(time.Hour))
	sr := postSend(t, env, aSec, body, http.StatusAccepted)
	dly := sr.Deliveries[0].DeliveryID

	env.Now.Advance(2 * time.Hour)
	deadline := time.Now().Add(2 * time.Second)
	for {
		st, _, _ := dlyState(t, env, dly)
		if st == "expired" {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("delivery %s state %s 2 s after its expiry passed, want expired", dly, st)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func mustCursor(t *testing.T, s string) eventlog.Cursor {
	t.Helper()
	c, err := eventlog.ParseCursor(s)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// TestOperatorKindReserved: operator powers key on peers.kind, so kind
// "operator" is only accepted under the operator principal; any other
// principal (or an implicit one) is 422 invalid_value (PROTOCOL §16.1,
// §14 inv. 3).
func TestOperatorKindReserved(t *testing.T) {
	env := testhub.Start(t)
	prn, _ := env.NewPrincipal("p")
	for _, body := range []map[string]any{
		{"kind": "operator", "name": "x"},
		{"kind": "operator", "name": "y", "principal_id": prn},
	} {
		resp, raw := env.Do(env.OperatorSecret, "POST", "/v1/admin/peers", body)
		mustErr(t, resp, raw, http.StatusUnprocessableEntity, "invalid_value")
	}
	var opPrn string
	if err := env.Hub.DB.QueryRow(`SELECT prn_id FROM peers WHERE peer_id = ?`, env.OperatorPeer).Scan(&opPrn); err != nil {
		t.Fatal(err)
	}
	adminAddPeer(t, env, map[string]any{"kind": "operator", "name": "op2", "principal_id": opPrn})
}

// TestEnrolTokenExpiresOnHubClock: the one-time enrolment token expires 24 h
// after issue on the hub's clock (PROTOCOL §12), so the fake clock drives it.
func TestEnrolTokenExpiresOnHubClock(t *testing.T) {
	env := testhub.Start(t)
	ok := adminAddPeer(t, env, map[string]any{"kind": "session", "name": "fresh"})
	late := adminAddPeer(t, env, map[string]any{"kind": "session", "name": "late"})
	env.Now.Advance(23 * time.Hour)
	enrol(t, env, ok.EnrolToken)
	env.Now.Advance(2 * time.Hour)
	resp, raw := env.Do("", "POST", "/v1/enrol", map[string]string{"token": late.EnrolToken})
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("enrol 25 h after issue = %d %s, want 401", resp.StatusCode, raw)
	}
}
