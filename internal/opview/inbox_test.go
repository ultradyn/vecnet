package opview_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/auth"
	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/eventlog"
	"github.com/XertroV/vecnet/internal/hub"
	"github.com/XertroV/vecnet/internal/ids"
	"github.com/XertroV/vecnet/internal/opapi"
	"github.com/XertroV/vecnet/internal/opview"
	"github.com/XertroV/vecnet/internal/testhub"
)

func caller(t *testing.T, env *testhub.Env) auth.Caller {
	t.Helper()
	c, err := env.Hub.Auth.Resolve(context.Background(), env.OperatorSecret)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func send(t *testing.T, env *testhub.Env, secret string, body map[string]any) hub.SendResponse {
	t.Helper()
	resp, raw := env.Do(secret, "POST", "/v1/messages", body)
	if resp.StatusCode != 202 {
		t.Fatalf("send = %d: %s", resp.StatusCode, raw)
	}
	var out hub.SendResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func body(key, priority string, peers ...string) map[string]any {
	return map[string]any{"idempotency_key": key, "priority": priority, "recipients": peers, "content": map[string]any{"text": "hello " + key}}
}

func inbox(t *testing.T, env *testhub.Env, q opapi.InboxQuery) opapi.Inbox {
	t.Helper()
	got, err := opview.New(env.Hub).Inbox(context.Background(), caller(t, env), q)
	if err != nil {
		t.Fatalf("Inbox: %v", err)
	}
	return got
}

func deliveryIDs(rows []opapi.InboxDelivery) []string {
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.DeliveryID
	}
	return out
}

func TestInboxSections(t *testing.T) {
	env := testhub.Start(t)
	sender, secret := env.NewPeer("session", "Sender")
	other, _ := env.NewPeer("session", "Other")
	var sent []hub.SendResponse
	for i, priority := range []string{"wake", "wake_idle", "hold"} {
		b := body(fmt.Sprint(i), priority, env.OperatorPeer)
		sent = append(sent, send(t, env, secret, b))
	}
	send(t, env, secret, body("other", "wake", other))
	if err := env.Hub.Receipt(context.Background(), caller(t, env), sent[0].Deliveries[0].DeliveryID, "read", ""); err != nil {
		t.Fatal(err)
	}
	got := inbox(t, env, opapi.InboxQuery{})
	want := []string{sent[2].Deliveries[0].DeliveryID, sent[1].Deliveries[0].DeliveryID, sent[0].Deliveries[0].DeliveryID}
	if !reflect.DeepEqual(deliveryIDs(got.Deliveries), want) {
		t.Fatalf("deliveries = %v, want %v", deliveryIDs(got.Deliveries), want)
	}
	if got.Unread != 2 {
		t.Fatalf("unread = %d, want 2", got.Unread)
	}
	for i, section := range []string{"held", "held", "woke"} {
		d := got.Deliveries[i]
		if d.Section != section || d.Sender != sender || d.Excerpt != fmt.Sprintf("hello %d", 2-i) || d.Kind != "message" || d.Priority != []string{"hold", "wake_idle", "wake"}[i] || d.State != "queued" || d.ThreadID != sent[2-i].ThreadID || d.MessageID != sent[2-i].MessageID || d.CreatedAt != envelope.FormatTime(env.Now.Now()) || d.Attachments != 0 {
			t.Errorf("delivery %d = %+v", i, d)
		}
	}
	if got.Deliveries[2].ReadAt == nil || *got.Deliveries[2].ReadAt != envelope.FormatTime(env.Now.Now()) || got.Deliveries[0].ReadAt != nil {
		t.Fatal("read_at does not reflect shared receipt state")
	}
	if got.NextBefore != nil {
		t.Fatalf("next_before = %v, want nil", got.NextBefore)
	}
	if got.Attention == nil || got.Approvals == nil || got.DeadLetters == nil {
		t.Fatal("empty collections must be non-nil")
	}
}

func approval(t *testing.T, env *testhub.Env, secret, key string, expiry time.Time, peers ...string) (string, hub.SendResponse) {
	t.Helper()
	apr := ids.New(ids.Apr)
	b := body(key, "wake", peers...)
	b["kind"] = "approval_request"
	b["approval_request"] = map[string]any{"approval_id": apr, "question": "ship it?", "options": []string{"yes", "no"}, "expires_at": envelope.FormatTime(expiry)}
	return apr, send(t, env, secret, b)
}

func TestInboxApprovalLifecycle(t *testing.T) {
	env := testhub.Start(t)
	sender, secret := env.NewPeer("session", "Requester")
	late, l := approval(t, env, secret, "late", env.Now.Now().Add(2*time.Hour), env.OperatorPeer)
	soon, s := approval(t, env, secret, "soon", env.Now.Now().Add(time.Hour), env.OperatorPeer)
	got := inbox(t, env, opapi.InboxQuery{})
	if len(got.Approvals) != 2 || got.Approvals[0].ApprovalID != soon || got.Approvals[1].ApprovalID != late || len(got.Deliveries) != 0 || got.Unread != 0 {
		t.Fatalf("open approvals = %+v; deliveries = %+v; unread = %d", got.Approvals, got.Deliveries, got.Unread)
	}
	a := got.Approvals[0]
	if a.MessageID != s.MessageID || a.ThreadID != s.ThreadID || a.Requester != sender || a.Question != "ship it?" || a.Text != "hello soon" || a.ExpiresAt != envelope.FormatTime(env.Now.Now().Add(time.Hour)) || !reflect.DeepEqual(a.Options, []string{"yes", "no"}) {
		t.Fatalf("approval details = %+v", a)
	}
	b := body("answer", "hold", sender)
	b["thread_id"], b["kind"] = l.ThreadID, "approval_response"
	b["approval_response"] = map[string]any{"approval_id": late, "decision": "yes"}
	send(t, env, env.OperatorSecret, b)
	got = inbox(t, env, opapi.InboxQuery{})
	if len(got.Approvals) != 1 || got.Approvals[0].ApprovalID != soon || !reflect.DeepEqual(deliveryIDs(got.Deliveries), []string{l.Deliveries[0].DeliveryID}) {
		t.Fatalf("after answer = %+v", got)
	}
	// An approval the operator answered was read: it never pads the unread
	// count, and its row is not unread in either client.
	if got.Unread != 0 || got.Deliveries[0].ReadAt == nil {
		t.Fatalf("answered approval is unread: unread %d, read_at %v", got.Unread, got.Deliveries[0].ReadAt)
	}
	if only := inbox(t, env, opapi.InboxQuery{UnreadOnly: true}); len(only.Deliveries) != 0 {
		t.Fatalf("unread filter lists the answered approval: %+v", only.Deliveries)
	}
	// Expiry must be respected even before a ticker has closed the row.
	env.Now.Advance(time.Hour)
	got = inbox(t, env, opapi.InboxQuery{})
	if len(got.Approvals) != 0 || !reflect.DeepEqual(deliveryIDs(got.Deliveries), []string{s.Deliveries[0].DeliveryID, l.Deliveries[0].DeliveryID}) || got.Unread != 1 {
		t.Fatalf("at expiry = %+v", got)
	}
	if n, err := env.Hub.ExpireApprovals(context.Background(), env.Now.Now()); err != nil || n != 1 {
		t.Fatalf("ExpireApprovals = %d, %v", n, err)
	}
	got = inbox(t, env, opapi.InboxQuery{})
	if len(got.Approvals) != 0 || len(got.Deliveries) != 3 {
		t.Fatalf("after expiry ticker = %+v", got)
	}
}

func TestInboxApprovalsOnlyMine(t *testing.T) {
	env := testhub.Start(t)
	_, secret := env.NewPeer("session", "Requester")
	other, _ := env.NewPeer("session", "Other")
	approval(t, env, secret, "other-approval", env.Now.Now().Add(time.Hour), other)
	got := inbox(t, env, opapi.InboxQuery{})
	if len(got.Approvals) != 0 || len(got.Deliveries) != 0 || got.Unread != 0 {
		t.Fatalf("another peer's approval leaked: %+v", got)
	}
}

func TestInboxDeadLetterEnriched(t *testing.T) {
	env := testhub.Start(t)
	sender, secret := env.NewPeer("session", "Sender")
	recipient, _ := env.NewPeer("session", "Recipient")
	cases := []struct {
		status int
		class  string
	}{{503, "retries_exhausted"}, {410, "terminal"}, {0, "network"}, {408, "retries_exhausted"}, {429, "retries_exhausted"}, {500, "retries_exhausted"}, {599, "retries_exhausted"}, {400, "terminal"}}
	wants := map[string]hub.SendResponse{}
	for i, tc := range cases {
		s := send(t, env, secret, body(fmt.Sprint(i), "wake", recipient))
		d := s.Deliveries[0].DeliveryID
		// Stage a final in-flight attempt, then record a real hub push outcome.
		if _, err := env.Hub.DB.Exec(`UPDATE deliveries SET state='pushed', attempt=3, attempts_left=1 WHERE dly_id=?`, d); err != nil {
			t.Fatal(err)
		}
		var netErr error
		if tc.status == 0 {
			netErr = errors.New("connection failed")
		}
		err := env.Hub.PushResult(context.Background(), hub.DueDelivery{DeliveryID: d, Recipient: recipient, ThreadID: s.ThreadID, Attempt: 3}, tc.status, 0, netErr, env.Now.Now())
		if err != nil {
			t.Fatal(err)
		}
		wants[d] = s
	}
	got := inbox(t, env, opapi.InboxQuery{})
	if len(got.DeadLetters) != len(cases) {
		t.Fatalf("dead letters = %+v", got.DeadLetters)
	}
	for i, d := range got.DeadLetters {
		s, ok := wants[d.DeliveryID]
		if !ok || d.Sender != sender || d.Recipient != recipient || d.MessageID != s.MessageID || d.ThreadID != s.ThreadID || d.Attempt != 3 || d.UpdatedAt != envelope.FormatTime(env.Now.Now()) || d.Excerpt != fmt.Sprintf("hello %d", i) || d.Class != cases[i].class || d.LastError == nil {
			t.Fatalf("enriched dead letter %d = %+v", i, d)
		}
		if cases[i].status == 0 {
			if d.LastStatus != nil {
				t.Errorf("network last_status = %v", d.LastStatus)
			}
		} else if d.LastStatus == nil || *d.LastStatus != cases[i].status {
			t.Errorf("last_status = %v, want %d", d.LastStatus, cases[i].status)
		}
	}
}

func TestInboxPaging(t *testing.T) {
	env := testhub.Start(t)
	_, secret := env.NewPeer("session", "Sender")
	var sent []hub.SendResponse
	for i := range 5 {
		sent = append(sent, send(t, env, secret, body(fmt.Sprint(i), "hold", env.OperatorPeer)))
	}
	if err := env.Hub.Receipt(context.Background(), caller(t, env), sent[3].Deliveries[0].DeliveryID, "read", ""); err != nil {
		t.Fatal(err)
	}
	first := inbox(t, env, opapi.InboxQuery{Limit: 2})
	if !reflect.DeepEqual(deliveryIDs(first.Deliveries), []string{sent[4].Deliveries[0].DeliveryID, sent[3].Deliveries[0].DeliveryID}) || first.NextBefore == nil || *first.NextBefore != sent[3].Deliveries[0].DeliveryID || first.Unread != 4 {
		t.Fatalf("first page = %+v", first)
	}
	second := inbox(t, env, opapi.InboxQuery{Limit: 2, Before: *first.NextBefore})
	if !reflect.DeepEqual(deliveryIDs(second.Deliveries), []string{sent[2].Deliveries[0].DeliveryID, sent[1].Deliveries[0].DeliveryID}) || second.NextBefore == nil || *second.NextBefore != sent[1].Deliveries[0].DeliveryID || second.Unread != 4 {
		t.Fatalf("second page = %+v", second)
	}
	last := inbox(t, env, opapi.InboxQuery{Limit: 2, Before: *second.NextBefore})
	if !reflect.DeepEqual(deliveryIDs(last.Deliveries), []string{sent[0].Deliveries[0].DeliveryID}) || last.NextBefore != nil || last.Unread != 4 {
		t.Fatalf("last page = %+v", last)
	}
	unread := inbox(t, env, opapi.InboxQuery{Limit: 2, UnreadOnly: true})
	if !reflect.DeepEqual(deliveryIDs(unread.Deliveries), []string{sent[4].Deliveries[0].DeliveryID, sent[2].Deliveries[0].DeliveryID}) || unread.NextBefore == nil {
		t.Fatalf("unread page = %+v", unread)
	}
	unreadLast := inbox(t, env, opapi.InboxQuery{Limit: 2, UnreadOnly: true, Before: *unread.NextBefore})
	if !reflect.DeepEqual(deliveryIDs(unreadLast.Deliveries), []string{sent[1].Deliveries[0].DeliveryID, sent[0].Deliveries[0].DeliveryID}) || unreadLast.NextBefore != nil {
		t.Fatalf("unread last page = %+v", unreadLast)
	}
	// Limit defaults and cap are reader contracts too, independently of HTTP.
	env.SetLimits(sentPeer(t, env, secret), 1000, 1000)
	for i := 5; i < 202; i++ {
		send(t, env, secret, body(fmt.Sprint(i), "hold", env.OperatorPeer))
	}
	for _, tc := range []struct{ limit, want int }{{0, 50}, {999, 200}, {200, 200}} {
		got := inbox(t, env, opapi.InboxQuery{Limit: tc.limit})
		if len(got.Deliveries) != tc.want || got.NextBefore == nil || *got.NextBefore != got.Deliveries[tc.want-1].DeliveryID || got.Unread != 201 {
			t.Fatalf("limit %d: count=%d next=%v unread=%d", tc.limit, len(got.Deliveries), got.NextBefore, got.Unread)
		}
	}
}

func sentPeer(t *testing.T, env *testhub.Env, secret string) string {
	t.Helper()
	c, err := env.Hub.Auth.Resolve(context.Background(), secret)
	if err != nil {
		t.Fatal(err)
	}
	return c.PeerID
}

func TestInboxNowIsHubClock(t *testing.T) {
	env := testhub.Start(t)
	for _, advance := range []time.Duration{0, 72 * time.Hour} {
		env.Now.Advance(advance)
		got := inbox(t, env, opapi.InboxQuery{})
		if got.Now != envelope.FormatTime(env.Now.Now()) {
			t.Fatalf("now = %q, want fake clock %q", got.Now, envelope.FormatTime(env.Now.Now()))
		}
	}
}

func TestInboxMe(t *testing.T) {
	env := testhub.Start(t)
	got := inbox(t, env, opapi.InboxQuery{})
	if got.Me.PeerID != env.OperatorPeer || got.Me.Name != "operator" || got.Me.Presence != "unknown" || got.Me.PresenceAt != nil {
		t.Fatalf("initial me = %+v", got.Me)
	}
	at := envelope.FormatTime(env.Now.Now())
	resp, raw := env.Do(env.OperatorSecret, "POST", "/v1/presence", map[string]any{"state": "working", "at": at})
	if resp.StatusCode != 204 {
		t.Fatalf("presence = %d: %s", resp.StatusCode, raw)
	}
	got = inbox(t, env, opapi.InboxQuery{})
	if got.Me.Presence != "working" || got.Me.PresenceAt == nil || *got.Me.PresenceAt != at {
		t.Fatalf("reported me = %+v", got.Me)
	}
	env.Now.Advance(hub.DefaultPresenceTTL + time.Millisecond)
	got = inbox(t, env, opapi.InboxQuery{})
	if got.Me.Presence != "unknown" || got.Me.PresenceAt == nil || *got.Me.PresenceAt != at {
		t.Fatalf("stale me = %+v", got.Me)
	}
}

func TestMarkThreadRead(t *testing.T) {
	env := testhub.Start(t)
	sender, secret := env.NewPeer("session", "Sender")
	other, _ := env.NewPeer("session", "Other")
	first := send(t, env, secret, body("first", "hold", env.OperatorPeer, other))
	b := body("second", "hold", env.OperatorPeer)
	b["thread_id"] = first.ThreadID
	second := send(t, env, secret, b)
	outside := send(t, env, secret, body("outside", "hold", env.OperatorPeer))
	readReceipts := func() []envelope.Receipt {
		t.Helper()
		events, err := env.Hub.Log.Read(context.Background(), []string{sender}, eventlog.Cursor{Epoch: env.Hub.Log.Epoch()}, 100)
		if err != nil {
			t.Fatal(err)
		}
		var out []envelope.Receipt
		for _, e := range events {
			if e.Type != "receipt" {
				continue
			}
			var r envelope.Receipt
			if err := json.Unmarshal(e.Data, &r); err != nil {
				t.Fatal(err)
			}
			if r.State == "read" {
				out = append(out, r)
			}
		}
		return out
	}
	r := opview.New(env.Hub)
	for _, want := range []int{2, 0} {
		got, err := r.MarkThreadRead(context.Background(), caller(t, env), first.ThreadID)
		if err != nil || got.Marked != want {
			t.Fatalf("MarkThreadRead = %+v, %v; want %d", got, err, want)
		}
		receipts := readReceipts()
		if len(receipts) != 2 {
			t.Fatalf("sender read receipts = %+v, want exactly 2", receipts)
		}
		wantIDs := map[string]bool{second.Deliveries[0].DeliveryID: true}
		for _, d := range first.Deliveries {
			if d.Recipient == env.OperatorPeer {
				wantIDs[d.DeliveryID] = true
			}
		}
		for _, receipt := range receipts {
			if !wantIDs[receipt.DeliveryID] || receipt.By != env.OperatorPeer || receipt.ThreadID != first.ThreadID || receipt.At != envelope.FormatTime(env.Now.Now()) || receipt.Note != "" {
				t.Fatalf("read receipt = %+v", receipt)
			}
			delete(wantIDs, receipt.DeliveryID)
		}
		snapshot := inbox(t, env, opapi.InboxQuery{UnreadOnly: true})
		if snapshot.Unread != 1 || !reflect.DeepEqual(deliveryIDs(snapshot.Deliveries), []string{outside.Deliveries[0].DeliveryID}) {
			t.Fatalf("shared read state = %+v", snapshot)
		}
	}
	// Use the HTTP seam to pin the promised status and reason code.
	resp, raw := env.Do(env.OperatorSecret, "POST", "/v1/admin/threads/"+ids.New(ids.Thr)+"/read", nil)
	if resp.StatusCode != 404 || !strings.Contains(string(raw), `"unknown_thread"`) {
		t.Fatalf("unknown thread = %d: %s", resp.StatusCode, raw)
	}
	// An existing thread with no operator delivery is valid and marks nothing.
	foreign := send(t, env, secret, body("foreign", "hold", other))
	got, err := r.MarkThreadRead(context.Background(), caller(t, env), foreign.ThreadID)
	if err != nil || got.Marked != 0 {
		t.Fatalf("foreign thread = %+v, %v", got, err)
	}
}

func TestMarkThreadReadConcurrent(t *testing.T) {
	env := testhub.Start(t)
	sender, secret := env.NewPeer("session", "Sender")
	const deliveries, clients = 32, 8
	wantIDs := make(map[string]bool, deliveries)
	var threadID string
	for i := range deliveries {
		b := body(fmt.Sprintf("concurrent-%d", i), "hold", env.OperatorPeer)
		if threadID != "" {
			b["thread_id"] = threadID
		}
		sent := send(t, env, secret, b)
		threadID = sent.ThreadID
		wantIDs[sent.Deliveries[0].DeliveryID] = true
	}

	c := caller(t, env)
	start := make(chan struct{})
	type result struct {
		marked int
		err    error
	}
	results := make(chan result, clients)
	for range clients {
		go func() {
			<-start
			got, err := opview.New(env.Hub).MarkThreadRead(context.Background(), c, threadID)
			results <- result{got.Marked, err}
		}()
	}
	close(start)
	total := 0
	for range clients {
		got := <-results
		if got.err != nil {
			t.Errorf("concurrent MarkThreadRead: %v", got.err)
		}
		total += got.marked
	}

	var read int
	if err := env.Hub.DB.QueryRow(`SELECT count(*) FROM deliveries WHERE thr_id = ? AND recipient = ? AND read_at IS NOT NULL`, threadID, env.OperatorPeer).Scan(&read); err != nil {
		t.Fatal(err)
	}
	if read != deliveries {
		t.Errorf("read deliveries = %d, want %d", read, deliveries)
	}
	events, err := env.Hub.Log.Read(context.Background(), []string{sender}, eventlog.Cursor{Epoch: env.Hub.Log.Epoch()}, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range events {
		if e.Type != "receipt" {
			continue
		}
		var receipt envelope.Receipt
		if err := json.Unmarshal(e.Data, &receipt); err != nil {
			t.Fatal(err)
		}
		if receipt.State != "read" {
			continue
		}
		if !wantIDs[receipt.DeliveryID] || receipt.By != env.OperatorPeer || receipt.ThreadID != threadID {
			t.Errorf("duplicate or unexpected read receipt: %+v", receipt)
		}
		delete(wantIDs, receipt.DeliveryID)
	}
	if len(wantIDs) != 0 {
		t.Errorf("missing read receipts: %v", wantIDs)
	}
	if total != deliveries {
		t.Errorf("total marked across %d overlapping calls = %d, want %d", clients, total, deliveries)
	}
	got, err := opview.New(env.Hub).MarkThreadRead(context.Background(), c, threadID)
	if err != nil || got.Marked != 0 {
		t.Errorf("repeat after concurrent reads = %+v, %v; want marked 0", got, err)
	}
}

func TestInboxHidesSuperseded(t *testing.T) {
	env := testhub.Start(t)
	sender, secret := env.NewPeer("session", "reactor")
	sent := send(t, env, secret, body("superseded", "wake", env.OperatorPeer))
	id := sent.Deliveries[0].DeliveryID
	// Seed a withdrawn wake independently of hub wake creation.
	threadExecFixture(t, env, `UPDATE messages SET kind='reaction',body=json_set(body,'$.kind','reaction','$.reaction',json_object('target',msg_id,'emoji','✅')) WHERE msg_id=?`, sent.MessageID)
	threadExecFixture(t, env, `UPDATE deliveries SET state='superseded' WHERE dly_id=?`, id)
	for _, q := range []opapi.InboxQuery{{}, {UnreadOnly: true}} {
		got := inbox(t, env, q)
		if len(got.Deliveries) != 0 || got.Unread != 0 {
			t.Errorf("superseded inbox=%+v", got)
		}
	}
	got, err := opview.New(env.Hub).MarkThreadRead(context.Background(), caller(t, env), sent.ThreadID)
	if err != nil || got.Marked != 0 {
		t.Errorf("mark read=%+v err=%v", got, err)
	}
	var readAt *string
	if err := env.Hub.DB.QueryRow(`SELECT read_at FROM deliveries WHERE dly_id=?`, id).Scan(&readAt); err != nil {
		t.Fatal(err)
	}
	if readAt != nil {
		t.Error("superseded marked read")
	}
	evs, err := env.Hub.Log.Read(context.Background(), []string{sender}, eventlog.Cursor{Epoch: env.Hub.Log.Epoch()}, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, ev := range evs {
		if ev.Type == "receipt" {
			t.Errorf("receipt for superseded: %s", ev.Data)
		}
	}
}
