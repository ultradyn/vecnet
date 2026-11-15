package opview_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/hub"
	"github.com/XertroV/vecnet/internal/opapi"
	"github.com/XertroV/vecnet/internal/opview"
	"github.com/XertroV/vecnet/internal/testhub"
)

// MarkAllRead clears thread spam: every unread delivery from one sender (or
// from everyone), as the inbox counts them, with the sender seeing read
// receipts; other senders, open approvals and later arrivals stay unread.
func TestMarkAllReadBySender(t *testing.T) {
	env := testhub.Start(t)
	spammer, spamSecret := env.NewPeer("session", "Spammer")
	_, otherSecret := env.NewPeer("session", "Other")
	for i := range 5 {
		send(t, env, spamSecret, body(fmt.Sprintf("spam-%d", i), "hold", env.OperatorPeer))
	}
	other := send(t, env, otherSecret, body("other", "hold", env.OperatorPeer))
	apr, _ := approval(t, env, spamSecret, "ask", env.Now.Now().Add(time.Hour), env.OperatorPeer)
	if got := inbox(t, env, opapi.InboxQuery{}); got.Unread != 6 {
		t.Fatalf("unread before = %d, want 6 (the open approval is listed apart)", got.Unread)
	}
	r := opview.New(env.Hub)
	c := caller(t, env)
	got, err := r.MarkAllRead(context.Background(), c, opapi.ReadAllQuery{Sender: spammer})
	if err != nil || got.Marked != 5 || got.More || got.Until != envelope.FormatTime(env.Now.Now()) {
		t.Fatalf("MarkAllRead(sender) = %+v, %v; want 5, no more", got, err)
	}
	after := inbox(t, env, opapi.InboxQuery{UnreadOnly: true})
	if after.Unread != 1 || len(after.Deliveries) != 1 || after.Deliveries[0].DeliveryID != other.Deliveries[0].DeliveryID {
		t.Fatalf("after sender read: unread %d, %+v", after.Unread, deliveryIDs(after.Deliveries))
	}
	if len(after.Approvals) != 1 || after.Approvals[0].ApprovalID != apr {
		t.Fatalf("the open approval was touched: %+v", after.Approvals)
	}
	// Each read went through the receipt path: the spammer saw five reads.
	var receipts int
	if err := env.Hub.DB.QueryRow(`SELECT count(*) FROM deliveries d JOIN messages m ON m.msg_id = d.msg_id WHERE m.sender = ? AND d.recipient = ? AND d.read_at IS NOT NULL`, spammer, env.OperatorPeer).Scan(&receipts); err != nil || receipts != 5 {
		t.Fatalf("spam deliveries read = %d, %v", receipts, err)
	}
	// Again: nothing left from that sender.
	if again, err := r.MarkAllRead(context.Background(), c, opapi.ReadAllQuery{Sender: spammer}); err != nil || again.Marked != 0 || again.More {
		t.Fatalf("second MarkAllRead = %+v, %v", again, err)
	}
	// Everyone: the other sender's too; the badge reaches zero.
	if all, err := r.MarkAllRead(context.Background(), c, opapi.ReadAllQuery{}); err != nil || all.Marked != 1 {
		t.Fatalf("MarkAllRead(all) = %+v, %v", all, err)
	}
	if final := inbox(t, env, opapi.InboxQuery{}); final.Unread != 0 || len(final.UnreadByThread) != 0 {
		t.Fatalf("unread after all = %d %v", final.Unread, final.UnreadByThread)
	}
}

// Until bounds what is marked, so paging through More never marks a message
// that arrived after the first call; batches stop at ReadAllBatch.
func TestMarkAllReadUntilAndBatches(t *testing.T) {
	saved := opview.ReadAllBatch
	opview.ReadAllBatch = 4
	t.Cleanup(func() { opview.ReadAllBatch = saved })
	env := testhub.Start(t)
	_, secret := env.NewPeer("session", "Bulk")
	// Thread spam: each message its own new thread.
	for i := range opview.ReadAllBatch + 3 {
		send(t, env, secret, body(fmt.Sprintf("bulk-%d", i), "hold", env.OperatorPeer))
	}
	r := opview.New(env.Hub)
	c := caller(t, env)
	first, err := r.MarkAllRead(context.Background(), c, opapi.ReadAllQuery{})
	if err != nil || first.Marked != opview.ReadAllBatch || !first.More {
		t.Fatalf("first batch = %+v, %v; want %d and more", first, err, opview.ReadAllBatch)
	}
	// A new delivery with the same timestamp is outside the event high-water.
	tied := send(t, env, secret, body("same-millisecond", "hold", env.OperatorPeer))
	// A later timestamp is outside Until as well.
	env.Now.Advance(time.Second)
	late := send(t, env, secret, body("late", "hold", env.OperatorPeer))
	rest, err := r.MarkAllRead(context.Background(), c, opapi.ReadAllQuery{Until: first.Until, BeforeSeq: first.BeforeSeq})
	if err != nil || rest.Marked != 3 || rest.More || rest.Until != first.Until || rest.BeforeSeq != first.BeforeSeq {
		t.Fatalf("second batch = %+v, %v; want 3, no more, same until", rest, err)
	}
	left := inbox(t, env, opapi.InboxQuery{UnreadOnly: true})
	remaining := map[string]bool{}
	for _, d := range left.Deliveries {
		remaining[d.DeliveryID] = true
	}
	if left.Unread != 2 || len(left.Deliveries) != 2 || !remaining[tied.Deliveries[0].DeliveryID] || !remaining[late.Deliveries[0].DeliveryID] {
		t.Fatalf("after batches: unread %d %v; want tied %s and late %s", left.Unread, deliveryIDs(left.Deliveries), tied.Deliveries[0].DeliveryID, late.Deliveries[0].DeliveryID)
	}
}

func TestMarkAllReadRefusals(t *testing.T) {
	env := testhub.Start(t)
	r := opview.New(env.Hub)
	c := caller(t, env)
	var he *hub.APIError
	if _, err := r.MarkAllRead(context.Background(), c, opapi.ReadAllQuery{Sender: "peer_01JD0000000000000000000000"}); !errors.As(err, &he) || he.Status != http.StatusNotFound {
		t.Fatalf("unknown sender = %v, want 404", err)
	}
	if _, err := r.MarkAllRead(context.Background(), c, opapi.ReadAllQuery{Until: "yesterday"}); !errors.As(err, &he) || he.Code != "invalid_value" || he.Details["field"] != "until" {
		t.Fatalf("bad until = %v, want 422 invalid_value until", err)
	}
	for _, bad := range []string{"-1", "+1", " 1", "1.0", "wat", "9223372036854775808"} {
		if _, err := r.MarkAllRead(context.Background(), c, opapi.ReadAllQuery{BeforeSeq: bad}); !errors.As(err, &he) || he.Code != "invalid_value" || he.Details["field"] != "before_seq" {
			t.Fatalf("bad before_seq %q = %v, want 422 invalid_value before_seq", bad, err)
		}
	}
	// HTTP: body shapes and the sender's form.
	for _, tc := range []struct {
		body   any
		status int
	}{
		{nil, http.StatusOK},
		{map[string]any{}, http.StatusOK},
		{map[string]any{"sender": "thr_x"}, http.StatusUnprocessableEntity},
		{map[string]any{"sender": 7}, http.StatusBadRequest},
		{map[string]any{"other": 1}, http.StatusBadRequest},
		{map[string]any{"before_seq": "0"}, http.StatusOK},
		{map[string]any{"before_seq": "-1"}, http.StatusUnprocessableEntity},
		{map[string]any{"before_seq": "9223372036854775808"}, http.StatusUnprocessableEntity},
		{map[string]any{"before_seq": ""}, http.StatusUnprocessableEntity},
		{map[string]any{"before_seq": 123}, http.StatusBadRequest},
	} {
		resp, raw := env.Do(env.OperatorSecret, "POST", "/v1/admin/inbox/read", tc.body)
		if resp.StatusCode != tc.status {
			t.Fatalf("POST inbox/read %v = %d %s, want %d", tc.body, resp.StatusCode, raw, tc.status)
		}
	}
	// A peer credential cannot use the operator route.
	_, peerSecret := env.NewPeer("session", "Nosy")
	if resp, _ := env.Do(peerSecret, "POST", "/v1/admin/inbox/read", nil); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("peer POST inbox/read = %d, want 403", resp.StatusCode)
	}
}

// unread_by_sender splits unread by sender from the same query, so it sums
// to unread and is exactly what MarkAllRead(sender) clears.
func TestInboxUnreadBySender(t *testing.T) {
	env := testhub.Start(t)
	a, aSec := env.NewPeer("session", "A")
	b, bSec := env.NewPeer("session", "B")
	for i := range 3 {
		send(t, env, aSec, body(fmt.Sprintf("a-%d", i), "hold", env.OperatorPeer))
	}
	send(t, env, bSec, body("b-0", "hold", env.OperatorPeer))
	got := inbox(t, env, opapi.InboxQuery{})
	if got.UnreadBySender[a] != 3 || got.UnreadBySender[b] != 1 || len(got.UnreadBySender) != 2 || got.Unread != 4 {
		t.Fatalf("unread_by_sender = %v (unread %d)", got.UnreadBySender, got.Unread)
	}
	if _, err := opview.New(env.Hub).MarkAllRead(context.Background(), caller(t, env), opapi.ReadAllQuery{Sender: a}); err != nil {
		t.Fatal(err)
	}
	got = inbox(t, env, opapi.InboxQuery{})
	if _, has := got.UnreadBySender[a]; has || got.UnreadBySender[b] != 1 || got.Unread != 1 {
		t.Fatalf("after reading A: %v (unread %d)", got.UnreadBySender, got.Unread)
	}
}
