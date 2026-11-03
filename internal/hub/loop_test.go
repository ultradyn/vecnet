package hub_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/eventlog"
	"github.com/XertroV/vecnet/internal/testhub"
)

// storedLoop returns the loop block of the stored Message msgID.
func storedLoop(t *testing.T, env *testhub.Env, msgID string) envelope.Loop {
	t.Helper()
	var body string
	if err := env.Hub.DB.QueryRow(`SELECT body FROM messages WHERE msg_id = ?`, msgID).Scan(&body); err != nil {
		t.Fatalf("load message %s: %v", msgID, err)
	}
	var m envelope.Message
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		t.Fatalf("decode message %s: %v", msgID, err)
	}
	return m.Loop
}

// replyReq is a reply into thr, in reply to parent when parent != "".
func replyReq(key, thr, parent string) map[string]any {
	b := req(key)
	b["thread_id"] = thr
	if parent != "" {
		b["in_reply_to"] = parent
	}
	return b
}

// apiError decodes the §2 error envelope.
func apiError(t *testing.T, raw []byte) (code string, details map[string]any) {
	t.Helper()
	var body struct {
		Error struct {
			Code    string         `json:"code"`
			Details map[string]any `json:"details"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("decode error body: %v; %s", err, raw)
	}
	return body.Error.Code, body.Error.Details
}

// mustReject posts body and fails unless the answer is status with code.
func mustReject(t *testing.T, env *testhub.Env, secret string, body any, status int, code string) (*http.Response, map[string]any) {
	t.Helper()
	resp, raw := env.Do(secret, "POST", "/v1/messages", body)
	if resp.StatusCode != status {
		t.Fatalf("POST /v1/messages = %d, want %d %s; body %s", resp.StatusCode, status, code, raw)
	}
	got, details := apiError(t, raw)
	if got != code {
		t.Fatalf("error code = %q, want %q; body %s", got, code, raw)
	}
	return resp, details
}

func counter(t *testing.T, env *testhub.Env, name string) int {
	t.Helper()
	var n int
	err := env.Hub.DB.QueryRow(`SELECT coalesce((SELECT value FROM counters WHERE name = ?), 0)`, name).Scan(&n)
	if err != nil {
		t.Fatalf("counter %s: %v", name, err)
	}
	return n
}

func chainEq(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func TestDepthChainAndReset(t *testing.T) {
	env := testhub.Start(t)
	a, aSec := env.NewPeer("session", "A")
	b, bSec := env.NewPeer("session", "B")
	op := env.OperatorPeer

	m1 := mustSend(t, env, aSec, req("d1", b))
	if l := storedLoop(t, env, m1.MessageID); l.Depth != 0 || !chainEq(l.SenderChain, []string{a}) {
		t.Fatalf("A→B loop = %+v, want depth 0 chain [A]", l)
	}
	m2 := mustSend(t, env, bSec, replyReq("d2", m1.ThreadID, m1.MessageID))
	if l := storedLoop(t, env, m2.MessageID); l.Depth != 1 || !chainEq(l.SenderChain, []string{a, b}) {
		t.Fatalf("B reply loop = %+v, want depth 1 chain [A,B]", l)
	}
	m3 := mustSend(t, env, env.OperatorSecret, replyReq("d3", m1.ThreadID, m2.MessageID))
	if l := storedLoop(t, env, m3.MessageID); l.Depth != 0 || !chainEq(l.SenderChain, []string{a, b, op}) {
		t.Fatalf("operator reply loop = %+v, want depth 0 chain [A,B,Op]", l)
	}
	m4 := mustSend(t, env, aSec, replyReq("d4", m1.ThreadID, m3.MessageID))
	if l := storedLoop(t, env, m4.MessageID); l.Depth != 1 || !chainEq(l.SenderChain, []string{a, b, op, a}) {
		t.Fatalf("bot reply after operator loop = %+v, want depth 1 chain [A,B,Op,A]", l)
	}
}

func TestMaxDepthInheritedNeverRaised(t *testing.T) {
	env := testhub.Start(t)
	a, aSec := env.NewPeer("session", "A")
	b, bSec := env.NewPeer("session", "B")

	fresh := req("m0", b)
	fresh["max_depth"] = 2
	m0 := mustSend(t, env, aSec, fresh)
	if l := storedLoop(t, env, m0.MessageID); l.MaxDepth != 2 || l.Depth != 0 {
		t.Fatalf("fresh loop = %+v, want depth 0 max_depth 2", l)
	}

	r1 := replyReq("m1", m0.ThreadID, m0.MessageID)
	r1["max_depth"] = 10
	m1 := mustSend(t, env, bSec, r1)
	if l := storedLoop(t, env, m1.MessageID); l.MaxDepth != 2 || l.Depth != 1 {
		t.Fatalf("reply asking max_depth 10 stored %+v, want depth 1 max_depth 2 (never raised)", l)
	}
	m2 := mustSend(t, env, aSec, replyReq("m2", m0.ThreadID, m1.MessageID))
	if l := storedLoop(t, env, m2.MessageID); l.MaxDepth != 2 || l.Depth != 2 {
		t.Fatalf("second reply loop = %+v, want depth 2 max_depth 2", l)
	}

	_, details := mustReject(t, env, bSec, replyReq("m3", m0.ThreadID, m2.MessageID),
		http.StatusUnprocessableEntity, "max_depth_exceeded")
	if details["max_depth"] != float64(2) {
		t.Fatalf("details = %v, want max_depth 2", details)
	}
	if n := count(t, env, `SELECT count(*) FROM messages WHERE thr_id = ?`, m0.ThreadID); n != 3 {
		t.Fatalf("messages in thread = %d, want 3 (the cut reply is not stored)", n)
	}
	if n := counter(t, env, "loop_cut"); n != 1 {
		t.Fatalf("counter loop_cut = %d, want 1", n)
	}
	if n := count(t, env, `SELECT count(*) FROM audit WHERE action = 'loop_cut' AND actor = ?`, b); n != 1 {
		t.Fatalf("loop_cut audit rows = %d, want 1", n)
	}
	_ = a
}

func TestExpiresAtInheritedAndEnforced(t *testing.T) {
	env := testhub.Start(t)
	_, aSec := env.NewPeer("session", "A")
	b, bSec := env.NewPeer("session", "B")
	start := env.Now.Now()
	plus1h := envelope.FormatTime(start.Add(time.Hour))

	fresh := req("e0", b)
	fresh["expires_at"] = plus1h
	m0 := mustSend(t, env, aSec, fresh)
	if l := storedLoop(t, env, m0.MessageID); l.ExpiresAt != plus1h {
		t.Fatalf("fresh expires_at = %s, want %s", l.ExpiresAt, plus1h)
	}

	r1 := replyReq("e1", m0.ThreadID, m0.MessageID)
	r1["expires_at"] = envelope.FormatTime(start.Add(5 * time.Hour))
	m1 := mustSend(t, env, bSec, r1)
	if l := storedLoop(t, env, m1.MessageID); l.ExpiresAt != plus1h {
		t.Fatalf("reply asking +5h stored expires_at %s, want %s (never raised)", l.ExpiresAt, plus1h)
	}
	m2 := mustSend(t, env, aSec, replyReq("e2", m0.ThreadID, m1.MessageID))
	if l := storedLoop(t, env, m2.MessageID); l.ExpiresAt != plus1h {
		t.Fatalf("reply without expires_at stored %s, want inherited %s", l.ExpiresAt, plus1h)
	}

	env.Now.Advance(2 * time.Hour)
	mustReject(t, env, bSec, replyReq("e3", m0.ThreadID, m2.MessageID), http.StatusUnprocessableEntity, "expired")
	if n := count(t, env, `SELECT count(*) FROM messages WHERE thr_id = ?`, m0.ThreadID); n != 3 {
		t.Fatalf("messages in thread = %d, want 3", n)
	}
	if n := counter(t, env, "loop_cut"); n != 1 {
		t.Fatalf("counter loop_cut = %d, want 1", n)
	}
}

func TestSenderChainCapped32(t *testing.T) {
	env := testhub.Start(t)
	a, aSec := env.NewPeer("session", "A")
	b, bSec := env.NewPeer("session", "B")

	fresh := req("c0", b)
	fresh["max_depth"] = 64
	first := mustSend(t, env, aSec, fresh)
	want := []string{a}
	last := first.MessageID
	for i := 1; i <= 40; i++ {
		sec, who := aSec, a
		if i%2 == 1 {
			sec, who = bSec, b
		}
		sr := mustSend(t, env, sec, replyReq(fmt.Sprintf("c%d", i), first.ThreadID, last))
		want = append(want, who)
		last = sr.MessageID
	}
	l := storedLoop(t, env, last)
	if len(l.SenderChain) != 32 {
		t.Fatalf("sender_chain has %d entries, want 32", len(l.SenderChain))
	}
	if !chainEq(l.SenderChain, want[len(want)-32:]) {
		t.Fatalf("sender_chain = %v, want the last 32 originators %v (oldest dropped)", l.SenderChain, want[len(want)-32:])
	}
	if l.Depth != 40 {
		t.Fatalf("depth = %d, want 40", l.Depth)
	}
}

func TestBudgetExhaustedEmitsThreadUpdated(t *testing.T) {
	env := testhub.Start(t)
	a, aSec := env.NewPeer("session", "A")
	b, bSec := env.NewPeer("session", "B")
	m0 := mustSend(t, env, aSec, req("b0", b))

	// The operator budget API is Task 17; set the budget directly.
	if _, err := env.Hub.DB.Exec(`UPDATE threads SET budget_remaining = 1 WHERE thr_id = ?`, m0.ThreadID); err != nil {
		t.Fatal(err)
	}
	mustSend(t, env, bSec, replyReq("b1", m0.ThreadID, m0.MessageID))
	var budget int
	if err := env.Hub.DB.QueryRow(`SELECT budget_remaining FROM threads WHERE thr_id = ?`, m0.ThreadID).Scan(&budget); err != nil || budget != 0 {
		t.Fatalf("budget_remaining = %d, %v; want 0", budget, err)
	}

	mustReject(t, env, aSec, replyReq("b2", m0.ThreadID, ""), http.StatusUnprocessableEntity, "thread_budget_exhausted")
	if n := count(t, env, `SELECT count(*) FROM messages WHERE thr_id = ?`, m0.ThreadID); n != 2 {
		t.Fatalf("messages in thread = %d, want 2", n)
	}
	for _, p := range []string{a, b} {
		evs, err := env.Hub.Log.Read(context.Background(), []string{p},
			eventlog.Cursor{Epoch: env.Hub.Log.Epoch(), Seq: 0}, 0)
		if err != nil {
			t.Fatal(err)
		}
		var updates []envelope.ThreadSummary
		for _, ev := range evs {
			if ev.Type != "thread_updated" {
				continue
			}
			if err := envelope.Validate("threadSummary", ev.Data); err != nil {
				t.Fatalf("thread_updated data violates the schema: %v; %s", err, ev.Data)
			}
			var ts envelope.ThreadSummary
			if err := json.Unmarshal(ev.Data, &ts); err != nil {
				t.Fatal(err)
			}
			updates = append(updates, ts)
		}
		if len(updates) != 1 {
			t.Fatalf("%s's log has %d thread_updated events, want 1", p, len(updates))
		}
		ts := updates[0]
		if ts.ThreadID != m0.ThreadID || ts.BudgetRemaining == nil || *ts.BudgetRemaining != 0 || ts.MessageCount != 2 {
			t.Fatalf("%s's thread_updated = %+v, want thread %s budget_remaining 0 message_count 2", p, ts, m0.ThreadID)
		}
	}
	if n := counter(t, env, "loop_cut"); n != 1 {
		t.Fatalf("counter loop_cut = %d, want 1", n)
	}
	if n := count(t, env, `SELECT count(*) FROM audit WHERE action = 'loop_cut' AND actor = ?`, a); n != 1 {
		t.Fatalf("loop_cut audit rows = %d, want 1", n)
	}
}

func TestRateLimits(t *testing.T) {
	env := testhub.Start(t)
	a, aSec := env.NewPeer("session", "A")
	b, _ := env.NewPeer("session", "B")
	c, cSec := env.NewPeer("session", "C")

	// 60 sends a minute: one new thread plus 59 sends into it, all at one
	// instant. Each continues the thread's loop from its latest message
	// (PROTOCOL §6); max_depth 64 keeps the 60-deep chain clear of loop
	// control.
	s0 := req("s0", b)
	s0["max_depth"] = 64
	first := mustSend(t, env, aSec, s0)
	for i := 1; i < 60; i++ {
		mustSend(t, env, aSec, replyReq(fmt.Sprintf("s%d", i), first.ThreadID, ""))
	}
	resp, details := mustReject(t, env, aSec, replyReq("s60", first.ThreadID, ""), http.StatusTooManyRequests, "rate_limited")
	if ra := resp.Header.Get("Retry-After"); ra != "60" {
		t.Fatalf("Retry-After = %q, want 60", ra)
	}
	if details["retry_after_seconds"] != float64(60) {
		t.Fatalf("details = %v, want retry_after_seconds 60", details)
	}
	if n := count(t, env, `SELECT count(*) FROM messages WHERE sender = ?`, a); n != 60 {
		t.Fatalf("A's messages = %d, want 60", n)
	}
	env.Now.Advance(30 * time.Second)
	resp, _ = mustReject(t, env, aSec, replyReq("s61", first.ThreadID, ""), http.StatusTooManyRequests, "rate_limited")
	if ra := resp.Header.Get("Retry-After"); ra != "30" {
		t.Fatalf("Retry-After after 30 s = %q, want 30", ra)
	}
	env.Now.Advance(31 * time.Second)
	mustSend(t, env, aSec, replyReq("s62", first.ThreadID, ""))
	if n := counter(t, env, "rate_limited"); n != 2 {
		t.Fatalf("counter rate_limited = %d, want 2", n)
	}
	if n := count(t, env, `SELECT count(*) FROM audit WHERE action = 'rate_limited' AND actor = ?`, a); n != 2 {
		t.Fatalf("rate_limited audit rows = %d, want 2", n)
	}

	// 20 new threads an hour; replies into existing threads do not count.
	var thr string
	for i := 0; i < 20; i++ {
		thr = mustSend(t, env, cSec, req(fmt.Sprintf("t%d", i), b)).ThreadID
	}
	resp, details = mustReject(t, env, cSec, req("t20", b), http.StatusTooManyRequests, "rate_limited")
	ra, err := strconv.Atoi(resp.Header.Get("Retry-After"))
	if err != nil || ra != 3600 {
		t.Fatalf("Retry-After = %q, want 3600", resp.Header.Get("Retry-After"))
	}
	if details["retry_after_seconds"] != float64(3600) {
		t.Fatalf("details = %v, want retry_after_seconds 3600", details)
	}
	mustSend(t, env, cSec, replyReq("t-reply", thr, ""))

	// The per-peer limits are adjustable.
	env.SetLimits(c, 1000, 1000)
	mustSend(t, env, cSec, req("t21", b))
}

// TestThreadSendWithoutInReplyToContinuesLoop: a send into an existing
// thread that names no in_reply_to takes the thread's latest message as its
// loop parent (PROTOCOL §6), so leaving in_reply_to out never escapes
// max_depth, expires_at or sender_chain. The operator resets both depth and
// the TTL for its subtree (§16.1), so a human can always revive a thread.
func TestThreadSendWithoutInReplyToContinuesLoop(t *testing.T) {
	env := testhub.Start(t)
	a, aSec := env.NewPeer("session", "A")
	b, bSec := env.NewPeer("session", "B")
	op := env.OperatorPeer
	env.SetLimits(a, 10000, 10000)
	env.SetLimits(b, 10000, 10000)

	f0 := req("f0", b)
	f0["max_depth"] = 4
	first := mustSend(t, env, aSec, f0)
	thr := first.ThreadID
	firstExp := storedLoop(t, env, first.MessageID).ExpiresAt
	chain := []string{a}
	for i := 1; i <= 4; i++ {
		sec, who := aSec, a
		if i%2 == 1 {
			sec, who = bSec, b
		}
		m := mustSend(t, env, sec, replyReq(fmt.Sprintf("f%d", i), thr, ""))
		chain = append(chain, who)
		l := storedLoop(t, env, m.MessageID)
		if l.Depth != i || l.MaxDepth != 4 || l.ExpiresAt != firstExp || !chainEq(l.SenderChain, chain) {
			t.Fatalf("plain thread send %d loop = %+v, want depth %d max_depth 4 expires_at %s chain %v", i, l, i, firstExp, chain)
		}
	}
	// A larger max_depth on a plain send is clamped like any reply.
	over := replyReq("f5", thr, "")
	over["max_depth"] = 64
	mustReject(t, env, bSec, over, http.StatusUnprocessableEntity, "max_depth_exceeded")
	mustReject(t, env, bSec, replyReq("f5b", thr, ""), http.StatusUnprocessableEntity, "max_depth_exceeded")

	// The operator resets depth; bots continue below it.
	o1 := mustSend(t, env, env.OperatorSecret, replyReq("o1", thr, ""))
	chain = append(chain, op)
	if l := storedLoop(t, env, o1.MessageID); l.Depth != 0 || !chainEq(l.SenderChain, chain) {
		t.Fatalf("operator plain send loop = %+v, want depth 0 chain %v", l, chain)
	}
	b1 := mustSend(t, env, bSec, replyReq("b1", thr, ""))
	chain = append(chain, b)
	if l := storedLoop(t, env, b1.MessageID); l.Depth != 1 || !chainEq(l.SenderChain, chain) {
		t.Fatalf("bot send after operator loop = %+v, want depth 1 chain %v", l, chain)
	}

	// Past the thread's TTL, bots are cut with or without in_reply_to.
	env.Now.Advance(25 * time.Hour)
	mustReject(t, env, aSec, replyReq("late", thr, ""), http.StatusUnprocessableEntity, "expired")
	mustReject(t, env, aSec, replyReq("late-r", thr, b1.MessageID), http.StatusUnprocessableEntity, "expired")

	// The operator revives it: a fresh TTL that its subtree inherits.
	o2 := mustSend(t, env, env.OperatorSecret, replyReq("o2", thr, ""))
	l := storedLoop(t, env, o2.MessageID)
	exp, err := envelope.ParseTime(l.ExpiresAt)
	if err != nil {
		t.Fatal(err)
	}
	if want := env.Now.Now().Add(24 * time.Hour); !exp.Equal(want) || l.Depth != 0 {
		t.Fatalf("operator send into expired thread loop = %+v, want depth 0 expires_at %s", l, envelope.FormatTime(want))
	}
	a2 := mustSend(t, env, aSec, replyReq("a2", thr, ""))
	if la := storedLoop(t, env, a2.MessageID); la.Depth != 1 || la.ExpiresAt != l.ExpiresAt {
		t.Fatalf("bot send after operator revival loop = %+v, want depth 1 expires_at %s", la, l.ExpiresAt)
	}
	// An operator reply naming an expired message is not cut either.
	mustSend(t, env, env.OperatorSecret, replyReq("o3", thr, b1.MessageID))
	// The bot reply chain under the expired message stays closed.
	mustReject(t, env, bSec, replyReq("b-old", thr, b1.MessageID), http.StatusUnprocessableEntity, "expired")
}

func TestPlainSendAfterReactionContinuesLoop(t *testing.T) {
	f := newRX(t)
	needWake(t, rx(t, f.env, f.bs, f.target.MessageID, "✅", "add", "wake"))
	sent := mustSend(t, f.env, f.as, replyReq("plain", f.target.ThreadID, ""))
	l := storedLoop(t, f.env, sent.MessageID)
	if l.Depth != 1 || !chainEq(l.SenderChain, []string{f.a, f.a}) {
		t.Fatalf("plain loop=%+v; reaction became implicit parent", l)
	}
}
