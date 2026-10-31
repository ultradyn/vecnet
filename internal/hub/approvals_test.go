package hub_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/eventlog"
	"github.com/XertroV/vecnet/internal/ids"
	"github.com/XertroV/vecnet/internal/testhub"
)

// approvalReq is an approval_request asking options, expiring at expires.
func approvalReq(key, apr string, expires time.Time, options []string, recipients ...string) map[string]any {
	b := req(key, recipients...)
	b["kind"] = "approval_request"
	b["approval_request"] = map[string]any{
		"approval_id": apr,
		"question":    "ship it?",
		"options":     options,
		"expires_at":  envelope.FormatTime(expires),
	}
	return b
}

// approvalResp answers apr with decision inside thr.
func approvalResp(key, thr, apr, decision string) map[string]any {
	b := req(key)
	b["thread_id"] = thr
	b["kind"] = "approval_response"
	b["approval_response"] = map[string]any{"approval_id": apr, "decision": decision}
	return b
}

type approvalRow struct {
	closed, decision, decidedBy *string
	options                     []string
}

func loadApproval(t *testing.T, env *testhub.Env, apr string) approvalRow {
	t.Helper()
	var r approvalRow
	var opts string
	if err := env.Hub.DB.QueryRow(`SELECT options, closed_at, decision, decided_by FROM approvals WHERE apr_id = ?`, apr).
		Scan(&opts, &r.closed, &r.decision, &r.decidedBy); err != nil {
		t.Fatalf("load approval %s: %v", apr, err)
	}
	if err := json.Unmarshal([]byte(opts), &r.options); err != nil {
		t.Fatalf("decode options: %v", err)
	}
	return r
}

func threadBudget(t *testing.T, env *testhub.Env, thr string) int {
	t.Helper()
	return count(t, env, `SELECT budget_remaining FROM threads WHERE thr_id = ?`, thr)
}

func TestApprovalFirstValidResponseCloses(t *testing.T) {
	env := testhub.Start(t)
	_, aSec := env.NewPeer("session", "A")
	b, bSec := env.NewPeer("session", "B")
	c, cSec := env.NewPeer("session", "C")
	d, dSec := env.NewPeer("session", "D")

	// kind approval_request without approval_request is a schema violation.
	bad := req("bad", b, c)
	bad["kind"] = "approval_request"
	_, det := mustReject(t, env, aSec, bad, http.StatusBadRequest, "invalid_body")
	if det["path"] != "/approval_request" {
		t.Fatalf("details.path = %v, want /approval_request", det["path"])
	}

	apr := ids.New(ids.Apr)
	sr := mustSend(t, env, aSec, approvalReq("q1", apr, env.Now.Now().Add(time.Hour), []string{"yes", "no"}, b, c))
	if row := loadApproval(t, env, apr); row.closed != nil || len(row.options) != 2 || row.options[0] != "yes" || row.options[1] != "no" {
		t.Fatalf("approval row = %+v, want open with options [yes no]", row)
	}
	// D joins the thread but is not addressed by the request.
	join := req("join", d)
	join["thread_id"] = sr.ThreadID
	mustSend(t, env, aSec, join)

	mustReject(t, env, cSec, approvalResp("r0", sr.ThreadID, ids.New(ids.Apr), "yes"), http.StatusNotFound, "unknown_approval")
	_, det = mustReject(t, env, cSec, approvalResp("r1", sr.ThreadID, apr, "maybe"), http.StatusUnprocessableEntity, "invalid_option")
	if row := loadApproval(t, env, apr); row.closed != nil {
		t.Fatalf("invalid option closed the approval: %+v", row)
	}
	mustSend(t, env, cSec, approvalResp("r2", sr.ThreadID, apr, "no"))
	row := loadApproval(t, env, apr)
	if row.closed == nil || row.decision == nil || *row.decision != "no" || row.decidedBy == nil || *row.decidedBy != c {
		t.Fatalf("approval after C's no = closed %v decision %v by %v; want closed, no, C", row.closed, row.decision, row.decidedBy)
	}
	mustReject(t, env, bSec, approvalResp("r3", sr.ThreadID, apr, "yes"), http.StatusUnprocessableEntity, "approval_closed")
	mustReject(t, env, dSec, approvalResp("r4", sr.ThreadID, apr, "yes"), http.StatusForbidden, "not_addressed")
	if row := loadApproval(t, env, apr); *row.decision != "no" || *row.decidedBy != c {
		t.Fatalf("later responses changed the decision: %v by %v", *row.decision, *row.decidedBy)
	}
}

func TestApprovalExpiryHubMessage(t *testing.T) {
	env := testhub.Start(t)
	a, aSec := env.NewPeer("session", "A")
	b, bSec := env.NewPeer("session", "B")
	c, _ := env.NewPeer("session", "C")
	ctx := context.Background()

	apr := ids.New(ids.Apr)
	sr := mustSend(t, env, aSec, approvalReq("q1", apr, env.Now.Now().Add(time.Hour), []string{"yes", "no"}, b, c))
	reqLoop := storedLoop(t, env, sr.MessageID)
	budget := threadBudget(t, env, sr.ThreadID)

	if n, err := env.Hub.ExpireApprovals(ctx, env.Now.Now()); err != nil || n != 0 {
		t.Fatalf("ExpireApprovals before expiry = %d, %v; want 0", n, err)
	}
	env.Now.Advance(2 * time.Hour)
	n, err := env.Hub.ExpireApprovals(ctx, env.Now.Now())
	if err != nil || n != 1 {
		t.Fatalf("ExpireApprovals = %d, %v; want 1", n, err)
	}
	row := loadApproval(t, env, apr)
	if row.closed == nil || row.decision == nil || *row.decision != "expired" {
		t.Fatalf("approval after expiry = closed %v decision %v; want closed, expired", row.closed, row.decision)
	}
	if got := threadBudget(t, env, sr.ThreadID); got != budget {
		t.Fatalf("budget_remaining = %d after the hub message, want unchanged %d", got, budget)
	}

	var hubMsgID string
	for _, p := range []string{a, b, c} {
		evs, err := env.Hub.Log.Read(ctx, []string{p}, eventlog.Cursor{Epoch: env.Hub.Log.Epoch(), Seq: 0}, 0)
		if err != nil {
			t.Fatal(err)
		}
		idx := -1
		for i, ev := range evs {
			if ev.Type == "approval_expired" {
				idx = i
				break
			}
		}
		if idx < 0 || idx+1 >= len(evs) {
			t.Fatalf("%s's log %+v: want approval_expired followed by a delivery", p, evs)
		}
		evJSON, _ := json.Marshal(evs[idx])
		if err := envelope.Validate("event", evJSON); err != nil {
			t.Fatalf("approval_expired event violates the schema: %v; %s", err, evJSON)
		}
		var ex map[string]any
		if err := json.Unmarshal(evs[idx].Data, &ex); err != nil {
			t.Fatal(err)
		}
		if len(ex) != 2 || ex["approval_id"] != apr || ex["thread_id"] != sr.ThreadID {
			t.Fatalf("approval_expired data = %v, want {approval_id, thread_id}", ex)
		}
		next := evs[idx+1]
		if next.Type != "delivery" {
			t.Fatalf("%s: event after approval_expired is %q, want delivery", p, next.Type)
		}
		dl, ts, m := decodeDelivery(t, next.Data) // validates against the schema
		if m.Sender != "hub" || m.Kind != "approval_response" || m.ApprovalResponse == nil ||
			m.ApprovalResponse.ApprovalID != apr || m.ApprovalResponse.Decision != "expired" {
			t.Fatalf("%s: hub message = %+v", p, m)
		}
		if dl.Ingress != "hub" || m.Priority != "hold" || dl.Recipient != p {
			t.Fatalf("%s: delivery ingress %q priority %q recipient %q; want hub, hold, %s", p, dl.Ingress, m.Priority, dl.Recipient, p)
		}
		if m.InReplyTo == nil || *m.InReplyTo != sr.MessageID {
			t.Fatalf("%s: hub message in_reply_to = %v, want the request %s", p, m.InReplyTo, sr.MessageID)
		}
		if m.Loop.Depth != reqLoop.Depth || m.Loop.MaxDepth != reqLoop.MaxDepth || !chainEq(m.Loop.SenderChain, reqLoop.SenderChain) {
			t.Fatalf("%s: hub message loop = %+v, want depth/chain of the request %+v", p, m.Loop, reqLoop)
		}
		if ts.BudgetRemaining == nil || *ts.BudgetRemaining != budget {
			t.Fatalf("%s: delivery thread budget_remaining = %v, want %d", p, ts.BudgetRemaining, budget)
		}
		if hubMsgID == "" {
			hubMsgID = m.MessageID
		} else if m.MessageID != hubMsgID {
			t.Fatalf("hub message ids differ across deliveries: %s vs %s", m.MessageID, hubMsgID)
		}
	}
	if n := count(t, env, `SELECT count(*) FROM messages WHERE thr_id = ? AND sender = 'hub' AND kind = 'approval_response'`, sr.ThreadID); n != 1 {
		t.Fatalf("hub messages stored = %d, want 1", n)
	}

	// A response after expiry is to a closed approval; a second sweep is a no-op.
	mustReject(t, env, bSec, approvalResp("late", sr.ThreadID, apr, "yes"), http.StatusUnprocessableEntity, "approval_closed")
	if n, err := env.Hub.ExpireApprovals(ctx, env.Now.Now()); err != nil || n != 0 {
		t.Fatalf("second ExpireApprovals = %d, %v; want 0", n, err)
	}
}

func TestApprovalCountsAgainstBudgetAndDepth(t *testing.T) {
	env := testhub.Start(t)
	a, aSec := env.NewPeer("session", "A")
	b, bSec := env.NewPeer("session", "B")

	m1 := mustSend(t, env, aSec, req("m1", b))
	parent := storedLoop(t, env, m1.MessageID)
	budget := threadBudget(t, env, m1.ThreadID)

	apr := ids.New(ids.Apr)
	q := approvalReq("q1", apr, env.Now.Now().Add(time.Hour), []string{"go", "stop", "wait"})
	q["thread_id"], q["in_reply_to"], q["recipients"] = m1.ThreadID, m1.MessageID, []string{a}
	qr := mustSend(t, env, bSec, q)
	if l := storedLoop(t, env, qr.MessageID); l.Depth != parent.Depth+1 {
		t.Fatalf("approval_request depth = %d, want parent+1 = %d", l.Depth, parent.Depth+1)
	}
	if got := threadBudget(t, env, m1.ThreadID); got != budget-1 {
		t.Fatalf("budget after approval_request = %d, want %d", got, budget-1)
	}
	r := approvalResp("r1", m1.ThreadID, apr, "wait")
	r["in_reply_to"] = qr.MessageID
	rr := mustSend(t, env, aSec, r)
	if l := storedLoop(t, env, rr.MessageID); l.Depth != parent.Depth+2 {
		t.Fatalf("approval_response depth = %d, want %d", l.Depth, parent.Depth+2)
	}
	if got := threadBudget(t, env, m1.ThreadID); got != budget-2 {
		t.Fatalf("budget after approval_response = %d, want %d", got, budget-2)
	}
}

// The hub expiry message must not raise the thread's inherited
// loop.expires_at (PROTOCOL §6): a thread whose loop TTL passed stays
// closed after the sweep, yet the notice itself is still delivered.
func TestApprovalExpiryDoesNotReviveExpiredThread(t *testing.T) {
	env := testhub.Start(t)
	_, aSec := env.NewPeer("session", "A")
	b, bSec := env.NewPeer("session", "B")
	ctx := context.Background()

	start := env.Now.Now()
	apr := ids.New(ids.Apr)
	q := approvalReq("q1", apr, start.Add(3*time.Hour), []string{"yes", "no"}, b)
	loopExp := envelope.FormatTime(start.Add(time.Hour))
	q["expires_at"] = loopExp
	sr := mustSend(t, env, aSec, q)

	reply := func(key string) map[string]any {
		r := req(key)
		r["thread_id"] = sr.ThreadID
		return r
	}
	env.Now.Advance(2 * time.Hour)
	mustReject(t, env, bSec, reply("before"), http.StatusUnprocessableEntity, "expired")

	env.Now.Advance(2 * time.Hour)
	if n, err := env.Hub.ExpireApprovals(ctx, env.Now.Now()); err != nil || n != 1 {
		t.Fatalf("ExpireApprovals = %d, %v; want 1", n, err)
	}
	var hubMsg string
	if err := env.Hub.DB.QueryRow(`SELECT msg_id FROM messages WHERE thr_id = ? AND sender = 'hub'`, sr.ThreadID).Scan(&hubMsg); err != nil {
		t.Fatalf("hub message: %v", err)
	}
	if l := storedLoop(t, env, hubMsg); l.ExpiresAt != loopExp {
		t.Fatalf("hub message loop.expires_at = %s, want the request's %s (never raised)", l.ExpiresAt, loopExp)
	}
	mustReject(t, env, bSec, reply("after"), http.StatusUnprocessableEntity, "expired")

	// The notice is still deliverable: its deliveries outlive the sweep.
	nowS := envelope.FormatTime(env.Now.Now())
	if n := count(t, env, `SELECT count(*) FROM deliveries WHERE msg_id = ? AND expires_at > ?`, hubMsg, nowS); n != 2 {
		t.Fatalf("live hub-message deliveries = %d, want 2", n)
	}
	if n, err := env.Hub.ExpireDeliveries(ctx, env.Now.Now()); err != nil {
		t.Fatal(err)
	} else if m := count(t, env, `SELECT count(*) FROM deliveries WHERE msg_id = ? AND state = 'expired'`, hubMsg); m != 0 {
		t.Fatalf("ExpireDeliveries (%d) expired %d hub-message deliveries, want 0", n, m)
	}
}
