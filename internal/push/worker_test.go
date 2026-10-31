package push_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/eventlog"
	"github.com/XertroV/vecnet/internal/hub"
	"github.com/XertroV/vecnet/internal/push"
	"github.com/XertroV/vecnet/internal/testhub"
	"github.com/XertroV/vecnet/internal/webhook"
)

// hit is one request a receiver got.
type hit struct {
	Header http.Header
	Body   []byte
}

// reply is a scripted receiver answer.
type reply struct {
	Status int
	Header map[string]string
	Body   string
}

// receiver is an httptest push receiver. It answers verification
// challenges (echoing them unless wrongChallenge is set) and records every
// other request, answering it with respond (200 when nil).
type receiver struct {
	srv *httptest.Server

	mu             sync.Mutex
	hits           []hit
	verifications  []hit
	wrongChallenge bool
	verifyStatus   int                      // non-zero: answer challenges with this status
	respond        func(n int, h hit) reply // n: 0-based index of the push
}

func newReceiver(t *testing.T, respond func(n int, h hit) reply) *receiver {
	t.Helper()
	r := &receiver{respond: respond}
	r.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		body, _ := io.ReadAll(req.Body)
		h := hit{Header: req.Header.Clone(), Body: body}
		var probe struct {
			Type      string `json:"type"`
			Challenge string `json:"challenge"`
		}
		if json.Unmarshal(body, &probe) == nil && probe.Type == "verification" {
			r.mu.Lock()
			r.verifications = append(r.verifications, h)
			wrong, status := r.wrongChallenge, r.verifyStatus
			r.mu.Unlock()
			if status != 0 {
				w.WriteHeader(status)
				return
			}
			c := probe.Challenge
			if wrong {
				c = "not-" + c
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]string{"challenge": c})
			return
		}
		r.mu.Lock()
		n := len(r.hits)
		r.hits = append(r.hits, h)
		respond := r.respond
		r.mu.Unlock()
		rep := reply{Status: http.StatusOK}
		if respond != nil {
			rep = respond(n, h)
		}
		for k, v := range rep.Header {
			w.Header().Set(k, v)
		}
		w.WriteHeader(rep.Status)
		io.WriteString(w, rep.Body)
	}))
	t.Cleanup(r.srv.Close)
	return r
}

func (r *receiver) Hits() []hit {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]hit(nil), r.hits...)
}

func (r *receiver) Verifications() []hit {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]hit(nil), r.verifications...)
}

// always answers every push with status.
func always(status int) func(int, hit) reply {
	return func(int, hit) reply { return reply{Status: status} }
}

// fixture is a hub with peers A (sender) and B (recipient) and a worker on
// the hub's fake clock.
type fixture struct {
	env        *testhub.Env
	a, aSec    string
	b, bSec    string
	w          *push.Worker
	rcv        *receiver
	route      string
	whsec      string
	ctx        context.Context
	t          *testing.T
	sendSerial int
}

// setup starts a hub; when respond is non-nil it also gives B a verified
// route to a receiver answering with respond.
func setup(t *testing.T, respond func(int, hit) reply) *fixture {
	t.Helper()
	env := testhub.Start(t)
	f := &fixture{env: env, ctx: context.Background(), t: t}
	f.a, f.aSec = env.NewPeer("grokbot", "a")
	f.b, f.bSec = env.NewPeer("dot", "b")
	env.SetLimits(f.a, 10000, 10000)
	f.w = &push.Worker{Hub: env.Hub, Now: env.Now.Now}
	if respond != nil {
		f.addRoute(respond)
	}
	return f
}

// addRoute gives B a route to a fresh receiver and verifies it.
func (f *fixture) addRoute(respond func(int, hit) reply) {
	f.t.Helper()
	f.rcv = newReceiver(f.t, respond)
	var err error
	f.route, f.whsec, err = f.env.Hub.AddRoute(f.ctx, "peer", f.b, f.rcv.srv.URL)
	if err != nil {
		f.t.Fatalf("AddRoute: %v", err)
	}
	if !strings.HasPrefix(f.whsec, "whsec_") {
		f.t.Fatalf("AddRoute whsec = %q, want a whsec_ secret", f.whsec)
	}
	if err := f.w.Verify(f.ctx, f.route); err != nil {
		f.t.Fatalf("Verify: %v", err)
	}
}

// send posts a SendRequest as secret and returns the 202 response.
func (f *fixture) send(secret string, body map[string]any) hub.SendResponse {
	f.t.Helper()
	resp, raw := f.env.Do(secret, "POST", "/v1/messages", body)
	if resp.StatusCode != http.StatusAccepted {
		f.t.Fatalf("POST /v1/messages = %d; %s", resp.StatusCode, raw)
	}
	var sr hub.SendResponse
	if err := json.Unmarshal(raw, &sr); err != nil {
		f.t.Fatalf("decode send response: %v; %s", err, raw)
	}
	return sr
}

// msg is a fresh SendRequest from A to B with priority.
func (f *fixture) msg(priority string) map[string]any {
	f.sendSerial++
	return map[string]any{
		"idempotency_key": fmt.Sprintf("k-%d", f.sendSerial),
		"priority":        priority,
		"recipients":      []string{f.b},
		"content":         map[string]any{"text": fmt.Sprintf("hello %d", f.sendSerial)},
	}
}

// step runs one synchronous worker step and returns how many pushes it made.
func (f *fixture) step() int {
	f.t.Helper()
	n, err := f.w.Step(f.ctx)
	if err != nil {
		f.t.Fatalf("Step: %v", err)
	}
	return n
}

type dlyRow struct {
	State        string
	Attempt      int
	AttemptsLeft int
	Next         *string
	Held         int
	LastStatus   *int
}

func (f *fixture) row(dly string) dlyRow {
	f.t.Helper()
	var r dlyRow
	if err := f.env.Hub.DB.QueryRow(
		`SELECT state, attempt, attempts_left, next_attempt_at, held, last_status FROM deliveries WHERE dly_id = ?`, dly).
		Scan(&r.State, &r.Attempt, &r.AttemptsLeft, &r.Next, &r.Held, &r.LastStatus); err != nil {
		f.t.Fatalf("delivery %s: %v", dly, err)
	}
	return r
}

// untilNext is how long from the fake now until dly's next_attempt_at.
func (f *fixture) untilNext(dly string) time.Duration {
	f.t.Helper()
	r := f.row(dly)
	if r.Next == nil {
		f.t.Fatalf("delivery %s has no next_attempt_at (state %s)", dly, r.State)
	}
	at, err := envelope.ParseTime(*r.Next)
	if err != nil {
		f.t.Fatal(err)
	}
	return at.Sub(f.env.Now.Now())
}

func (f *fixture) counter(name string) int {
	f.t.Helper()
	var n int
	if err := f.env.Hub.DB.QueryRow(`SELECT coalesce((SELECT value FROM counters WHERE name = ?), 0)`, name).Scan(&n); err != nil {
		f.t.Fatal(err)
	}
	return n
}

func (f *fixture) reachability(peer string) string {
	f.t.Helper()
	var r string
	if err := f.env.Hub.DB.QueryRow(`SELECT reachability FROM peers WHERE peer_id = ?`, peer).Scan(&r); err != nil {
		f.t.Fatal(err)
	}
	return r
}

// events returns peer's log events of type typ.
func (f *fixture) events(peer, typ string) []envelope.Event {
	f.t.Helper()
	evs, err := f.env.Hub.Log.Read(f.ctx, []string{peer}, eventlog.Cursor{Epoch: f.env.Hub.Log.Epoch()}, 0)
	if err != nil {
		f.t.Fatal(err)
	}
	var out []envelope.Event
	for _, ev := range evs {
		if ev.Type == typ {
			out = append(out, ev)
		}
	}
	return out
}

// receiptsFor returns the receipts in peer's log for dly.
func (f *fixture) receiptsFor(peer, dly string) []envelope.Receipt {
	f.t.Helper()
	var out []envelope.Receipt
	for _, ev := range f.events(peer, "receipt") {
		if err := envelope.Validate("receipt", ev.Data); err != nil {
			f.t.Fatalf("receipt Event is not schema-valid: %v; %s", err, ev.Data)
		}
		var r envelope.Receipt
		if err := json.Unmarshal(ev.Data, &r); err != nil {
			f.t.Fatal(err)
		}
		if r.DeliveryID == dly {
			out = append(out, r)
		}
	}
	return out
}

// eventData returns the data bytes of the delivery Event for dly in peer's log.
func (f *fixture) eventData(peer, dly string) []byte {
	f.t.Helper()
	for _, ev := range f.events(peer, "delivery") {
		var d struct {
			DeliveryID string `json:"delivery_id"`
		}
		if err := json.Unmarshal(ev.Data, &d); err != nil {
			f.t.Fatal(err)
		}
		if d.DeliveryID == dly {
			return ev.Data
		}
	}
	f.t.Fatalf("no delivery Event for %s in %s's log", dly, peer)
	return nil
}

// verifyHit checks h's signature against keys at the fake now.
func (f *fixture) verifyHit(h hit, keys ...string) {
	f.t.Helper()
	if err := webhook.Verify(keys, h.Header.Get("webhook-id"), h.Header.Get("webhook-timestamp"),
		h.Header.Get("webhook-signature"), h.Body, f.env.Now.Now()); err != nil {
		f.t.Fatalf("push signature does not verify: %v (headers %v)", err, h.Header)
	}
}

func TestPushHappyPath(t *testing.T) {
	f := setup(t, nil)
	f.addRoute(always(http.StatusOK))
	sr := f.send(f.aSec, f.msg("wake"))
	dly := sr.Deliveries[0].DeliveryID

	if n := f.step(); n != 1 {
		t.Fatalf("Step pushed %d, want 1", n)
	}
	hits := f.rcv.Hits()
	if len(hits) != 1 {
		t.Fatalf("receiver got %d pushes, want 1", len(hits))
	}
	h := hits[0]
	if got := h.Header.Get("webhook-id"); got != dly {
		t.Fatalf("webhook-id = %q, want delivery_id %q", got, dly)
	}
	f.verifyHit(h, f.whsec)
	if got := h.Header.Get("X-Vecnet-Attempt"); got != "1" {
		t.Fatalf("X-Vecnet-Attempt = %q, want 1", got)
	}
	if ct := h.Header.Get("Content-Type"); ct != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", ct)
	}
	if ts, err := strconv.ParseInt(h.Header.Get("webhook-timestamp"), 10, 64); err != nil || ts != f.env.Now.Now().Unix() {
		t.Fatalf("webhook-timestamp = %q, want the hub clock %d", h.Header.Get("webhook-timestamp"), f.env.Now.Now().Unix())
	}
	if err := envelope.Validate("delivery", h.Body); err != nil {
		t.Fatalf("pushed body is not a schema-valid delivery: %v; %s", err, h.Body)
	}
	if want := f.eventData(f.b, dly); !bytes.Equal(h.Body, want) {
		t.Fatalf("pushed body differs from the delivery Event data:\n got %s\nwant %s", h.Body, want)
	}
	if r := f.row(dly); r.State != "accepted" || r.Attempt != 1 {
		t.Fatalf("delivery = %+v, want accepted at attempt 1", r)
	}
	rs := f.receiptsFor(f.a, dly)
	if len(rs) != 1 || rs[0].State != "accepted" || rs[0].By != "hub" || rs[0].MessageID != sr.MessageID || rs[0].ThreadID != sr.ThreadID {
		t.Fatalf("A's receipts for %s = %+v, want one accepted by hub", dly, rs)
	}
	if r := f.reachability(f.b); r != "reachable" {
		t.Fatalf("B reachability = %q, want reachable", r)
	}
	// Accepted is final for the push worker.
	f.env.Now.Advance(time.Hour)
	if n := f.step(); n != 0 || len(f.rcv.Hits()) != 1 {
		t.Fatalf("an accepted delivery was pushed again (%d, %d hits)", n, len(f.rcv.Hits()))
	}
}

// A route added after sends picks up the queued deliveries (core-hub plan
// self-review); an unverified route is not live yet (PROTOCOL §3.2); a hold
// delivery is never pushed.
func TestRouteAddedLaterPicksUpQueued(t *testing.T) {
	f := setup(t, nil)
	sr := f.send(f.aSec, f.msg("wake"))
	dly := sr.Deliveries[0].DeliveryID
	hold := f.send(f.aSec, f.msg("hold")).Deliveries[0].DeliveryID
	if n := f.step(); n != 0 {
		t.Fatalf("Step pushed %d for a peer without a route, want 0", n)
	}
	if r := f.row(dly); r.State != "queued" || r.Attempt != 0 {
		t.Fatalf("pull-only delivery = %+v, want queued at attempt 0", r)
	}

	rcv := newReceiver(t, always(http.StatusOK))
	route, whsec, err := f.env.Hub.AddRoute(f.ctx, "peer", f.b, rcv.srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if n := f.step(); n != 0 || len(rcv.Hits()) != 0 {
		t.Fatalf("an unverified route was pushed to (%d)", n)
	}
	if err := f.w.Verify(f.ctx, route); err != nil {
		t.Fatal(err)
	}
	if n := f.step(); n != 1 {
		t.Fatalf("Step after the route was verified pushed %d, want 1", n)
	}
	hits := rcv.Hits()
	if len(hits) != 1 || hits[0].Header.Get("webhook-id") != dly {
		t.Fatalf("receiver hits = %d, want the queued delivery %s", len(hits), dly)
	}
	f.verifyHit(hits[0], whsec)
	if r := f.row(dly); r.State != "accepted" {
		t.Fatalf("delivery = %+v, want accepted", r)
	}
	if r := f.row(hold); r.State != "queued" || r.Attempt != 0 {
		t.Fatalf("hold delivery = %+v, want queued and never pushed", r)
	}
}

func TestRetryThenDeadLetter(t *testing.T) {
	f := setup(t, always(http.StatusServiceUnavailable))
	sr := f.send(f.aSec, f.msg("wake"))
	dly := sr.Deliveries[0].DeliveryID

	var bodies [][]byte
	var stamps, sigs []string
	for n := 1; n <= hub.PushAttempts; n++ {
		if got := f.step(); got != 1 {
			t.Fatalf("attempt %d: Step pushed %d, want 1", n, got)
		}
		hits := f.rcv.Hits()
		if len(hits) != n {
			t.Fatalf("attempt %d: receiver has %d pushes", n, len(hits))
		}
		h := hits[n-1]
		if got := h.Header.Get("X-Vecnet-Attempt"); got != strconv.Itoa(n) {
			t.Fatalf("attempt %d: X-Vecnet-Attempt = %q", n, got)
		}
		if got := h.Header.Get("webhook-id"); got != dly {
			t.Fatalf("attempt %d: webhook-id = %q, want %q", n, got, dly)
		}
		f.verifyHit(h, f.whsec)
		bodies = append(bodies, h.Body)
		stamps = append(stamps, h.Header.Get("webhook-timestamp"))
		sigs = append(sigs, h.Header.Get("webhook-signature"))

		r := f.row(dly)
		if n == hub.PushAttempts {
			if r.State != "dead_letter" || r.AttemptsLeft != 0 {
				t.Fatalf("after %d attempts: %+v, want dead_letter with no attempts left", n, r)
			}
			break
		}
		if r.State != "queued" || r.Attempt != n || r.AttemptsLeft != hub.PushAttempts-n || r.LastStatus == nil || *r.LastStatus != 503 {
			t.Fatalf("after attempt %d: %+v, want queued, attempt %d, %d left, last 503", n, r, n, hub.PushAttempts-n)
		}
		base := min(time.Hour, time.Duration(1<<n)*time.Second)
		wait := f.untilNext(dly)
		if wait < base || wait > base+base/10 {
			t.Fatalf("after attempt %d: next attempt in %v, want within [%v, %v]", n, wait, base, base+base/10)
		}
		// Not due yet: nothing is pushed.
		f.env.Now.Advance(wait - time.Millisecond)
		if got := f.step(); got != 0 {
			t.Fatalf("after attempt %d: Step before next_attempt_at pushed %d", n, got)
		}
		f.env.Now.Advance(time.Millisecond)
	}
	for i := 1; i < len(bodies); i++ {
		if !bytes.Equal(bodies[i], bodies[0]) {
			t.Fatalf("attempt %d body differs from attempt 1", i+1)
		}
		if stamps[i] == stamps[i-1] || sigs[i] == sigs[i-1] {
			t.Fatalf("attempt %d reused the timestamp/signature of attempt %d", i+1, i)
		}
	}
	rs := f.receiptsFor(f.a, dly)
	if len(rs) != 1 || rs[0].State != "dead_letter" || rs[0].By != "hub" {
		t.Fatalf("A's receipts = %+v, want one dead_letter by hub", rs)
	}
	if got := f.counter("dead_letters"); got != 1 {
		t.Fatalf("dead_letters counter = %d, want 1", got)
	}
	if r := f.reachability(f.b); r != "unreachable" {
		t.Fatalf("B reachability = %q, want unreachable", r)
	}
	f.env.Now.Advance(2 * time.Hour)
	if got := f.step(); got != 0 || len(f.rcv.Hits()) != hub.PushAttempts {
		t.Fatalf("a dead letter was pushed again")
	}
}

func TestTerminalStatuses(t *testing.T) {
	t.Run("404 dead letters after one attempt", func(t *testing.T) {
		f := setup(t, always(http.StatusNotFound))
		dly := f.send(f.aSec, f.msg("wake")).Deliveries[0].DeliveryID
		if n := f.step(); n != 1 {
			t.Fatalf("Step pushed %d, want 1", n)
		}
		if r := f.row(dly); r.State != "dead_letter" || r.Attempt != 1 || r.LastStatus == nil || *r.LastStatus != 404 {
			t.Fatalf("delivery = %+v, want dead_letter at attempt 1, last 404", r)
		}
		if rs := f.receiptsFor(f.a, dly); len(rs) != 1 || rs[0].State != "dead_letter" || rs[0].By != "hub" {
			t.Fatalf("A's receipts = %+v, want one dead_letter by hub", rs)
		}
		if got := f.counter("dead_letters"); got != 1 {
			t.Fatalf("dead_letters = %d, want 1", got)
		}
		if r := f.reachability(f.b); r != "unreachable" {
			t.Fatalf("B reachability = %q, want unreachable", r)
		}
		f.env.Now.Advance(2 * time.Hour)
		if n := f.step(); n != 0 || len(f.rcv.Hits()) != 1 {
			t.Fatalf("404 was retried (%d, %d hits)", n, len(f.rcv.Hits()))
		}
	})
	t.Run("410 and 413 dead letter", func(t *testing.T) {
		for _, status := range []int{http.StatusGone, http.StatusRequestEntityTooLarge, http.StatusBadRequest} {
			f := setup(t, always(status))
			dly := f.send(f.aSec, f.msg("wake")).Deliveries[0].DeliveryID
			f.step()
			if r := f.row(dly); r.State != "dead_letter" || r.Attempt != 1 {
				t.Fatalf("%d: delivery = %+v, want dead_letter at attempt 1", status, r)
			}
		}
	})
	t.Run("409 fails, never retried, counted", func(t *testing.T) {
		f := setup(t, always(http.StatusConflict))
		dly := f.send(f.aSec, f.msg("wake")).Deliveries[0].DeliveryID
		if n := f.step(); n != 1 {
			t.Fatalf("Step pushed %d, want 1", n)
		}
		if r := f.row(dly); r.State != "failed" || r.Attempt != 1 {
			t.Fatalf("delivery = %+v, want failed at attempt 1", r)
		}
		if got := f.counter("push_conflicts"); got != 1 {
			t.Fatalf("push_conflicts = %d, want 1", got)
		}
		if got := f.counter("dead_letters"); got != 0 {
			t.Fatalf("dead_letters = %d, want 0 for a conflict", got)
		}
		var audits int
		if err := f.env.Hub.DB.QueryRow(`SELECT count(*) FROM audit WHERE action = 'push_conflict' AND target_ids LIKE ?`,
			"%"+dly+"%").Scan(&audits); err != nil || audits != 1 {
			t.Fatalf("push_conflict audit rows = %d, %v; want 1", audits, err)
		}
		if rs := f.receiptsFor(f.a, dly); len(rs) != 1 || rs[0].State != "failed" || rs[0].By != "hub" {
			t.Fatalf("A's receipts = %+v, want one failed by hub", rs)
		}
		for range 3 {
			f.env.Now.Advance(2 * time.Hour)
			if n := f.step(); n != 0 {
				t.Fatalf("a 409 was retried")
			}
		}
		if len(f.rcv.Hits()) != 1 {
			t.Fatalf("receiver got %d pushes, want 1", len(f.rcv.Hits()))
		}
	})
	t.Run("302 is not followed and dead letters", func(t *testing.T) {
		var mu sync.Mutex
		elsewhere := 0
		other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			elsewhere++
			mu.Unlock()
		}))
		t.Cleanup(other.Close)
		f := setup(t, func(int, hit) reply {
			return reply{Status: http.StatusFound, Header: map[string]string{"Location": other.URL + "/x"}}
		})
		dly := f.send(f.aSec, f.msg("wake")).Deliveries[0].DeliveryID
		f.step()
		mu.Lock()
		n := elsewhere
		mu.Unlock()
		if n != 0 {
			t.Fatalf("the redirect was followed (%d requests elsewhere)", n)
		}
		if r := f.row(dly); r.State != "dead_letter" || r.LastStatus == nil || *r.LastStatus != 302 {
			t.Fatalf("delivery = %+v, want dead_letter, last 302", r)
		}
		f.env.Now.Advance(2 * time.Hour)
		if f.step() != 0 {
			t.Fatalf("a 302 was retried")
		}
	})
}

func TestRetryAfterHonoured(t *testing.T) {
	var date string
	script := []reply{
		{Status: 429, Header: map[string]string{"Retry-After": "120"}},
		{Status: 429, Header: map[string]string{"Retry-After": "99999"}},
		{Status: 503, Header: map[string]string{"Retry-After": "garbage"}},
		{Status: 503}, // Retry-After: HTTP-date, set below
		{Status: 200},
	}
	f := setup(t, func(n int, h hit) reply {
		r := script[min(n, len(script)-1)]
		if n == 3 {
			r.Header = map[string]string{"Retry-After": date}
		}
		return r
	})
	dly := f.send(f.aSec, f.msg("wake")).Deliveries[0].DeliveryID
	within := func(label string, base time.Duration) {
		t.Helper()
		got := f.untilNext(dly)
		if got < base || got > base+base/10 {
			t.Fatalf("%s: next attempt in %v, want within [%v, %v]", label, got, base, base+base/10)
		}
		f.env.Now.Advance(got)
	}
	f.step()
	within("Retry-After: 120", 120*time.Second)
	f.step()
	within("Retry-After: 99999", time.Hour)
	f.step()
	within("Retry-After: garbage", 8*time.Second) // computed: 2^3 s
	date = f.env.Now.Now().Add(300 * time.Second).UTC().Format(http.TimeFormat)
	f.step()
	within("Retry-After: HTTP-date", 300*time.Second)
	f.step()
	if r := f.row(dly); r.State != "accepted" || r.Attempt != 5 {
		t.Fatalf("delivery = %+v, want accepted at attempt 5", r)
	}
}

func TestSerialisationPerPeerThread(t *testing.T) {
	var mu sync.Mutex
	var first string
	seen := map[string]int{}
	f := setup(t, func(n int, h hit) reply {
		mu.Lock()
		defer mu.Unlock()
		id := h.Header.Get("webhook-id")
		seen[id]++
		if id == first && seen[id] == 1 {
			return reply{Status: http.StatusServiceUnavailable}
		}
		return reply{Status: http.StatusOK}
	})
	s1 := f.send(f.aSec, f.msg("wake"))
	r2 := f.msg("wake")
	delete(r2, "recipients")
	r2["thread_id"] = s1.ThreadID
	s2 := f.send(f.aSec, r2)
	s3 := f.send(f.aSec, f.msg("wake")) // a new thread
	if s2.ThreadID != s1.ThreadID || s3.ThreadID == s1.ThreadID {
		t.Fatalf("threads: %s %s %s", s1.ThreadID, s2.ThreadID, s3.ThreadID)
	}
	d1, d2, d3 := s1.Deliveries[0].DeliveryID, s2.Deliveries[0].DeliveryID, s3.Deliveries[0].DeliveryID
	mu.Lock()
	first = d1
	mu.Unlock()

	ids := func() []string {
		var out []string
		for _, h := range f.rcv.Hits() {
			out = append(out, h.Header.Get("webhook-id"))
		}
		return out
	}
	if n := f.step(); n != 2 {
		t.Fatalf("first Step pushed %d, want 2 (one per thread)", n)
	}
	got := ids()
	if len(got) != 2 || !((got[0] == d1 && got[1] == d3) || (got[0] == d3 && got[1] == d1)) {
		t.Fatalf("first pushes = %v, want %s and %s (not %s)", got, d1, d3, d2)
	}
	if r := f.row(d2); r.State != "queued" || r.Attempt != 0 {
		t.Fatalf("second delivery in the thread = %+v, want queued at attempt 0", r)
	}
	if r := f.row(d3); r.State != "accepted" {
		t.Fatalf("other thread's delivery = %+v, want accepted", r)
	}
	// d1 is pending a retry: d2 stays blocked behind it.
	if n := f.step(); n != 0 {
		t.Fatalf("Step while %s awaits retry pushed %d, want 0", d1, n)
	}
	f.env.Now.Advance(f.untilNext(d1))
	if n := f.step(); n != 1 {
		t.Fatalf("retry Step pushed %d, want 1", n)
	}
	if got := ids(); len(got) != 3 || got[2] != d1 {
		t.Fatalf("pushes = %v, want %s retried third", got, d1)
	}
	if n := f.step(); n != 1 {
		t.Fatalf("Step after %s was accepted pushed %d, want 1", d1, n)
	}
	if got := ids(); len(got) != 4 || got[3] != d2 {
		t.Fatalf("pushes = %v, want %s last", got, d2)
	}
	if r := f.row(d2); r.State != "accepted" {
		t.Fatalf("second delivery = %+v, want accepted", r)
	}
}

func TestWakeIdleGating(t *testing.T) {
	f := setup(t, always(http.StatusOK))
	presence := func(state string) {
		t.Helper()
		resp, raw := f.env.Do(f.bSec, "POST", "/v1/presence",
			map[string]any{"state": state, "at": envelope.FormatTime(f.env.Now.Now())})
		if resp.StatusCode != http.StatusNoContent {
			t.Fatalf("POST /v1/presence %s = %d; %s", state, resp.StatusCode, raw)
		}
	}

	// No presence yet is unknown, which counts as idle: pushed at once.
	d0 := f.send(f.aSec, f.msg("wake_idle")).Deliveries[0].DeliveryID
	if r := f.row(d0); r.Held != 0 {
		t.Fatalf("wake_idle to an unknown-presence peer held: %+v", r)
	}
	if n := f.step(); n != 1 {
		t.Fatalf("Step pushed %d, want 1", n)
	}

	presence("working")
	d1 := f.send(f.aSec, f.msg("wake_idle")).Deliveries[0].DeliveryID
	if r := f.row(d1); r.Held != 1 || r.State != "queued" {
		t.Fatalf("wake_idle to a working peer = %+v, want held", r)
	}
	if n := f.step(); n != 0 || len(f.rcv.Hits()) != 1 {
		t.Fatalf("a held delivery was pushed")
	}
	presence("idle")
	if r := f.row(d1); r.Held != 0 {
		t.Fatalf("after idle the delivery is still held: %+v", r)
	}
	if n := f.step(); n != 1 {
		t.Fatalf("Step after idle pushed %d, want 1", n)
	}
	hits := f.rcv.Hits()
	if len(hits) != 2 || hits[1].Header.Get("webhook-id") != d1 {
		t.Fatalf("pushes = %d, want the released delivery %s", len(hits), d1)
	}
	if n := f.step(); n != 0 {
		t.Fatalf("the released delivery was pushed twice")
	}

	// hold: never pushed, visible on B's long-poll.
	d2 := f.send(f.aSec, f.msg("hold")).Deliveries[0].DeliveryID
	presence("idle")
	f.env.Now.Advance(time.Hour)
	if n := f.step(); n != 0 || len(f.rcv.Hits()) != 2 {
		t.Fatalf("a hold delivery was pushed")
	}
	resp, raw := f.env.Do(f.bSec, "GET", fmt.Sprintf("/v1/events?after=e%d.0&wait=0", f.env.Hub.Log.Epoch()), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("long-poll = %d; %s", resp.StatusCode, raw)
	}
	var poll struct {
		Events []envelope.Event `json:"events"`
	}
	if err := json.Unmarshal(raw, &poll); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, ev := range poll.Events {
		if ev.Type == "delivery" && strings.Contains(string(ev.Data), d2) {
			found = true
		}
	}
	if !found {
		t.Fatalf("hold delivery %s not on B's long-poll: %s", d2, raw)
	}
}

func TestExpiry(t *testing.T) {
	f := setup(t, nil)
	m := f.msg("hold")
	m["expires_at"] = envelope.FormatTime(f.env.Now.Now().Add(time.Hour))
	sr := f.send(f.aSec, m)
	dly := sr.Deliveries[0].DeliveryID

	if n, err := f.env.Hub.ExpireDeliveries(f.ctx, f.env.Now.Now()); err != nil || n != 0 {
		t.Fatalf("ExpireDeliveries before expiry = %d, %v; want 0", n, err)
	}
	f.env.Now.Advance(2 * time.Hour)
	n, err := f.env.Hub.ExpireDeliveries(f.ctx, f.env.Now.Now())
	if err != nil || n != 1 {
		t.Fatalf("ExpireDeliveries = %d, %v; want 1", n, err)
	}
	if r := f.row(dly); r.State != "expired" {
		t.Fatalf("delivery = %+v, want expired", r)
	}
	rs := f.receiptsFor(f.a, dly)
	if len(rs) != 1 || rs[0].State != "expired" || rs[0].By != "hub" {
		t.Fatalf("A's receipts = %+v, want one expired by hub", rs)
	}
	if n, err := f.env.Hub.ExpireDeliveries(f.ctx, f.env.Now.Now()); err != nil || n != 0 {
		t.Fatalf("second ExpireDeliveries = %d, %v; want 0", n, err)
	}

	// B acks past the entry: the expired delivery stays expired.
	evs := f.events(f.b, "delivery")
	resp, raw := f.env.Do(f.bSec, "POST", "/v1/ack", map[string]string{"cursor": evs[len(evs)-1].Cursor})
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("ack = %d; %s", resp.StatusCode, raw)
	}
	if r := f.row(dly); r.State != "expired" {
		t.Fatalf("delivery after ack = %+v, want expired", r)
	}
	if rs := f.receiptsFor(f.a, dly); len(rs) != 1 {
		t.Fatalf("A's receipts after ack = %+v, want only the expired one", rs)
	}
}

// A routed wake delivery past its expires_at is not pushed.
func TestExpiredNotPushed(t *testing.T) {
	f := setup(t, always(http.StatusOK))
	m := f.msg("wake")
	m["expires_at"] = envelope.FormatTime(f.env.Now.Now().Add(time.Minute))
	dly := f.send(f.aSec, m).Deliveries[0].DeliveryID
	f.env.Now.Advance(2 * time.Minute)
	if n := f.step(); n != 0 || len(f.rcv.Hits()) != 0 {
		t.Fatalf("an expired delivery was pushed")
	}
	if n, err := f.env.Hub.ExpireDeliveries(f.ctx, f.env.Now.Now()); err != nil || n != 1 {
		t.Fatalf("ExpireDeliveries = %d, %v; want 1", n, err)
	}
	if r := f.row(dly); r.State != "expired" {
		t.Fatalf("delivery = %+v, want expired", r)
	}
}

func TestVerificationChallenge(t *testing.T) {
	f := setup(t, nil)
	routeVerified := func(route string) bool {
		t.Helper()
		var v *string
		if err := f.env.Hub.DB.QueryRow(`SELECT verified_at FROM routes WHERE route_id = ?`, route).Scan(&v); err != nil {
			t.Fatal(err)
		}
		return v != nil
	}

	good := newReceiver(t, nil)
	route, whsec, err := f.env.Hub.AddRoute(f.ctx, "peer", f.b, good.srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if routeVerified(route) {
		t.Fatalf("a new route is verified before its challenge")
	}
	if err := f.w.Verify(f.ctx, route); err != nil {
		t.Fatalf("Verify with an echoing receiver: %v", err)
	}
	if !routeVerified(route) {
		t.Fatalf("route not verified after an echoed challenge")
	}
	vs := good.Verifications()
	if len(vs) != 1 {
		t.Fatalf("receiver got %d challenges, want 1", len(vs))
	}
	f.verifyHit(vs[0], whsec)
	var body map[string]string
	if err := json.Unmarshal(vs[0].Body, &body); err != nil {
		t.Fatal(err)
	}
	c := body["challenge"]
	if len(body) != 2 || body["type"] != "verification" || len(c) != 43 {
		t.Fatalf("challenge body = %s, want {type: verification, challenge: 43 chars}", vs[0].Body)
	}
	if _, err := base64.RawURLEncoding.DecodeString(c); err != nil {
		t.Fatalf("challenge %q is not base64url: %v", c, err)
	}

	bad := newReceiver(t, nil)
	bad.mu.Lock()
	bad.wrongChallenge = true
	bad.mu.Unlock()
	route2, _, err := f.env.Hub.AddRoute(f.ctx, "peer", f.a, bad.srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.w.Verify(f.ctx, route2); err == nil {
		t.Fatalf("Verify with a wrong echo succeeded")
	}
	if routeVerified(route2) {
		t.Fatalf("route verified after a wrong echo")
	}

	failing := newReceiver(t, nil)
	failing.mu.Lock()
	failing.verifyStatus = http.StatusInternalServerError
	failing.mu.Unlock()
	prn, _ := f.env.NewPrincipal("p")
	route3, _, err := f.env.Hub.AddRoute(f.ctx, "principal", prn, failing.srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.w.Verify(f.ctx, route3); err == nil || routeVerified(route3) {
		t.Fatalf("Verify against a 500 = %v, verified %v; want an error and not verified", err, routeVerified(route3))
	}
}

// Rotation keeps the old key for 24 h and pushes carry both signatures
// (PROTOCOL §3.5).
func TestRouteRotationOverlap(t *testing.T) {
	f := setup(t, always(http.StatusOK))
	newSec, err := f.env.Hub.RotateRoute(f.ctx, f.route)
	if err != nil {
		t.Fatal(err)
	}
	if newSec == f.whsec || !strings.HasPrefix(newSec, "whsec_") {
		t.Fatalf("RotateRoute = %q", newSec)
	}
	keys, err := f.env.Hub.RouteKeys(f.ctx, f.route, f.env.Now.Now())
	if err != nil || len(keys) != 2 {
		t.Fatalf("RouteKeys during overlap = %v, %v; want 2", keys, err)
	}
	f.send(f.aSec, f.msg("wake"))
	f.step()
	h := f.rcv.Hits()[0]
	if n := len(strings.Fields(h.Header.Get("webhook-signature"))); n != 2 {
		t.Fatalf("push during overlap carries %d signatures, want 2", n)
	}
	f.verifyHit(h, f.whsec)
	f.verifyHit(h, newSec)

	f.env.Now.Advance(24*time.Hour + time.Second)
	keys, err = f.env.Hub.RouteKeys(f.ctx, f.route, f.env.Now.Now())
	if err != nil || len(keys) != 1 || keys[0] != newSec {
		t.Fatalf("RouteKeys after 24 h = %v, %v; want only the new key", keys, err)
	}
	f.send(f.aSec, f.msg("wake"))
	f.step()
	h = f.rcv.Hits()[1]
	if n := len(strings.Fields(h.Header.Get("webhook-signature"))); n != 1 {
		t.Fatalf("push after the overlap carries %d signatures, want 1", n)
	}
	f.verifyHit(h, newSec)
	if webhook.Verify([]string{f.whsec}, h.Header.Get("webhook-id"), h.Header.Get("webhook-timestamp"),
		h.Header.Get("webhook-signature"), h.Body, f.env.Now.Now()) == nil {
		t.Fatalf("the retired key still verifies after 24 h")
	}
}

// A principal route serves every peer of the principal, including one
// enrolled after the route was added.
func TestPrincipalRoute(t *testing.T) {
	f := setup(t, nil)
	prn, _ := f.env.NewPrincipal("host")
	ctx := f.ctx
	addPeer := func(name string) string {
		id, tok, err := f.env.Hub.AddPeer(ctx, "pagchat-bot", name, prn)
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := f.env.Hub.Auth.Enrol(ctx, tok); err != nil {
			t.Fatal(err)
		}
		return id
	}
	p1 := addPeer("p1")
	rcv := newReceiver(t, nil)
	route, _, err := f.env.Hub.AddRoute(ctx, "principal", prn, rcv.srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.w.Verify(ctx, route); err != nil {
		t.Fatal(err)
	}
	p2 := addPeer("p2")
	m := f.msg("wake")
	m["recipients"] = []string{p1, p2}
	f.send(f.aSec, m)
	if n := f.step(); n != 2 {
		t.Fatalf("Step pushed %d, want 2", n)
	}
	got := map[string]bool{}
	for _, h := range rcv.Hits() {
		var d struct {
			Recipient string `json:"recipient"`
		}
		json.Unmarshal(h.Body, &d)
		got[d.Recipient] = true
	}
	if !got[p1] || !got[p2] {
		t.Fatalf("principal route pushes for %v, want %s and %s", got, p1, p2)
	}
}

// Run pushes without explicit steps and stops when its context ends.
func TestWorkerRun(t *testing.T) {
	f := setup(t, always(http.StatusOK))
	f.w.Interval = 10 * time.Millisecond
	ctx, cancel := context.WithCancel(f.ctx)
	done := make(chan error, 1)
	go func() { done <- f.w.Run(ctx) }()
	dly := f.send(f.aSec, f.msg("wake")).Deliveries[0].DeliveryID
	f.w.Kick()
	deadline := time.Now().Add(5 * time.Second)
	for f.row(dly).State != "accepted" {
		if time.Now().After(deadline) {
			t.Fatalf("Run did not push within 5 s: %+v", f.row(dly))
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("Run did not return after cancel")
	}
}
