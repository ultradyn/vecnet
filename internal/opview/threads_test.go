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
	"github.com/XertroV/vecnet/internal/hub"
	"github.com/XertroV/vecnet/internal/ids"
	"github.com/XertroV/vecnet/internal/opapi"
	"github.com/XertroV/vecnet/internal/opview"
	"github.com/XertroV/vecnet/internal/testhub"
)

var threadCtx = context.Background()

func threadSend(t *testing.T, env *testhub.Env, secret, thr string, recipients []string, extra map[string]any) hub.SendResponse {
	t.Helper()
	body := map[string]any{"idempotency_key": ids.New(ids.Msg), "priority": "hold", "content": map[string]any{"text": "hello"}}
	if thr != "" {
		body["thread_id"] = thr
	}
	if recipients != nil {
		body["recipients"] = recipients
	}
	for k, v := range extra {
		body[k] = v
	}
	resp, raw := env.Do(secret, "POST", "/v1/messages", body)
	if resp.StatusCode != 202 {
		t.Fatalf("send: %d %s", resp.StatusCode, raw)
	}
	var out hub.SendResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func threadDetail(t *testing.T, env *testhub.Env, id string) opapi.ThreadDetail {
	t.Helper()
	d, err := opview.New(env.Hub).Thread(threadCtx, auth.Caller{Kind: "operator", PeerID: env.OperatorPeer}, id)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func threadExecFixture(t *testing.T, env *testhub.Env, sql string, args ...any) {
	t.Helper()
	if _, err := env.Hub.DB.Exec(sql, args...); err != nil {
		t.Fatal(err)
	}
}

func assertThreadAPIError(t *testing.T, err error, status int, code string) {
	t.Helper()
	var api *hub.APIError
	if !errors.As(err, &api) || api.Status != status || api.Code != code {
		t.Fatalf("error = %v, want %d %s", err, status, code)
	}
}

func TestThreadsOrderAfterFutureApprovalExpiry(t *testing.T) {
	env := testhub.Start(t)
	_, secret := env.NewPeer("session", "sender")
	// The demo expires an approval one second ahead of its clock, then
	// posts another message at the current time in the same thread.
	first := threadSend(t, env, secret, "", []string{env.OperatorPeer}, map[string]any{
		"kind": "approval_request", "approval_request": map[string]any{
			"approval_id": ids.New(ids.Apr), "question": "Proceed?",
			"options":    []string{"yes", "no"},
			"expires_at": envelope.FormatTime(env.Now.Now().Add(time.Millisecond)),
		},
	})
	future := env.Now.Now().Add(time.Second)
	if n, err := env.Hub.ExpireApprovals(threadCtx, future); err != nil || n != 1 {
		t.Fatalf("ExpireApprovals = %d, %v", n, err)
	}
	if got := threadDetail(t, env, first.ThreadID).Row.LastMessageAt; got != envelope.FormatTime(future) {
		t.Fatalf("expiry activity = %s, want %s", got, envelope.FormatTime(future))
	}
	threadSend(t, env, secret, first.ThreadID, nil, nil)
	wantAt := envelope.FormatTime(env.Now.Now())
	if got := threadDetail(t, env, first.ThreadID).Row.LastMessageAt; got != wantAt {
		t.Errorf("activity after subsequent send = %s, want last inserted message time %s", got, wantAt)
	}
	// Start the journey before wall time catches up with the expiry notice.
	env.Now.Advance(500 * time.Millisecond)
	journey := threadSend(t, env, secret, "", []string{env.OperatorPeer}, map[string]any{"thread_title": "Attachment journey"})
	wantIDs := []string{journey.ThreadID, first.ThreadID}
	t.Run("threads endpoint", func(t *testing.T) {
		resp, raw := env.Do(env.OperatorSecret, "GET", "/v1/threads", nil)
		var page struct {
			Threads []envelope.ThreadSummary `json:"threads"`
		}
		if resp.StatusCode != 200 || json.Unmarshal(raw, &page) != nil {
			t.Fatalf("threads response: %d %s", resp.StatusCode, raw)
		}
		got := []string{}
		for _, row := range page.Threads {
			got = append(got, row.ThreadID)
		}
		if !reflect.DeepEqual(got, wantIDs) {
			t.Fatalf("thread order = %v, want journey first: %v", got, wantIDs)
		}
	})
	t.Run("operator threads endpoint", func(t *testing.T) {
		resp, raw := env.Do(env.OperatorSecret, "GET", "/v1/admin/threads?participant="+env.OperatorPeer, nil)
		var page opapi.ThreadPage
		if resp.StatusCode != 200 || json.Unmarshal(raw, &page) != nil {
			t.Fatalf("operator threads response: %d %s", resp.StatusCode, raw)
		}
		got := []string{}
		for _, row := range page.Threads {
			got = append(got, row.Thread.ThreadID)
		}
		if !reflect.DeepEqual(got, wantIDs) {
			t.Fatalf("operator thread order = %v, want journey first: %v", got, wantIDs)
		}
	})
}

func TestThreadsAllPeers(t *testing.T) {
	env := testhub.Start(t)
	a, asec := env.NewPeer("session", "A")
	b, _ := env.NewPeer("session", "B")
	_, csec := env.NewPeer("session", "C")
	env.SetLimits(a, 1000, 1000)
	first := threadSend(t, env, asec, "", []string{b}, nil)
	env.Now.Advance(time.Second)
	second := threadSend(t, env, csec, "", []string{env.OperatorPeer}, nil)
	r := opview.New(env.Hub)
	page, err := r.Threads(threadCtx, opapi.ThreadQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Threads) != 2 || page.Threads[0].Thread.ThreadID != second.ThreadID || page.Threads[1].Thread.ThreadID != first.ThreadID {
		t.Fatalf("all threads newest first: %+v", page)
	}
	if got := page.Threads[1]; got.CreatedBy != a || got.CreatedAt == "" || got.LastMessageAt == "" || got.Thread.MessageCount != 1 || got.Thread.BudgetRemaining == nil || *got.Thread.BudgetRemaining != 499 || !reflect.DeepEqual(got.Thread.Participants, []string{a, b}) {
		t.Fatalf("thread metadata: %+v", got)
	}
	filtered, err := r.Threads(threadCtx, opapi.ThreadQuery{Participant: b})
	if err != nil || len(filtered.Threads) != 1 || filtered.Threads[0].Thread.ThreadID != first.ThreadID {
		t.Fatalf("participant filter: %+v, %v", filtered, err)
	}
	empty, err := r.Threads(threadCtx, opapi.ThreadQuery{Participant: ids.New(ids.Peer)})
	if err != nil || len(empty.Threads) != 0 {
		t.Fatalf("unknown participant: %+v %v", empty, err)
	}
	// Editing activity moves an existing thread ahead of a newer-created one.
	env.Now.Advance(time.Second)
	threadSend(t, env, asec, first.ThreadID, []string{b}, nil)
	page, err = r.Threads(threadCtx, opapi.ThreadQuery{Limit: 1})
	if err != nil || len(page.Threads) != 1 || page.Threads[0].Thread.ThreadID != first.ThreadID || page.Next == nil {
		t.Fatalf("activity order: %+v %v", page, err)
	}
	next, err := r.Threads(threadCtx, opapi.ThreadQuery{Limit: 1, Cursor: *page.Next})
	if err != nil || len(next.Threads) != 1 || next.Threads[0].Thread.ThreadID != second.ThreadID || next.Next != nil {
		t.Fatalf("page 2: %+v %v", next, err)
	}
	for i := 0; i < 201; i++ {
		threadSend(t, env, asec, "", []string{b}, nil)
	}
	capped, err := r.Threads(threadCtx, opapi.ThreadQuery{Limit: 999})
	if err != nil || len(capped.Threads) != 200 || capped.Next == nil {
		t.Fatalf("cap: %d %+v %v", len(capped.Threads), capped.Next, err)
	}
	rest, err := r.Threads(threadCtx, opapi.ThreadQuery{Limit: 999, Cursor: *capped.Next})
	if err != nil || len(rest.Threads) != 3 || rest.Next != nil {
		t.Fatalf("rest: %+v %v", rest, err)
	}
	seen := map[string]bool{}
	for _, row := range capped.Threads {
		seen[row.Thread.ThreadID] = true
	}
	for _, row := range rest.Threads {
		if seen[row.Thread.ThreadID] {
			t.Fatalf("overlapping pages: %s", row.Thread.ThreadID)
		}
	}
	defaults, err := r.Threads(threadCtx, opapi.ThreadQuery{})
	if err != nil || len(defaults.Threads) != 50 {
		t.Fatalf("default page: %d %v", len(defaults.Threads), err)
	}
	_, err = r.Threads(threadCtx, opapi.ThreadQuery{Cursor: "bad"})
	assertThreadAPIError(t, err, 422, "invalid_value")
	_, err = r.Threads(threadCtx, opapi.ThreadQuery{Participant: "me"})
	assertThreadAPIError(t, err, 422, "invalid_value")
}

// ExcludeParticipant drops every thread a peer is in, before the page is
// cut, so a bounded page of "other" threads is never short because of them.
func TestThreadsExcludeParticipant(t *testing.T) {
	env := testhub.Start(t)
	a, asec := env.NewPeer("session", "A")
	b, _ := env.NewPeer("session", "B")
	env.SetLimits(a, 1000, 1000)
	others := []string{}
	for i := 0; i < 3; i++ {
		others = append(others, threadSend(t, env, asec, "", []string{b}, nil).ThreadID)
		env.Now.Advance(time.Second)
	}
	// Newer threads the operator is in would fill a page of 3 on their own.
	for i := 0; i < 3; i++ {
		threadSend(t, env, asec, "", []string{env.OperatorPeer}, nil)
		env.Now.Advance(time.Second)
	}
	r := opview.New(env.Hub)
	page, err := r.Threads(threadCtx, opapi.ThreadQuery{ExcludeParticipant: env.OperatorPeer, Limit: 3})
	if err != nil {
		t.Fatal(err)
	}
	got := []string{}
	for _, row := range page.Threads {
		got = append(got, row.Thread.ThreadID)
	}
	if want := []string{others[2], others[1], others[0]}; !reflect.DeepEqual(got, want) || page.Next != nil {
		t.Fatalf("excluded page = %v next %v, want %v", got, page.Next, want)
	}
	first, err := r.Threads(threadCtx, opapi.ThreadQuery{ExcludeParticipant: env.OperatorPeer, Limit: 2})
	if err != nil || len(first.Threads) != 2 || first.Next == nil {
		t.Fatalf("excluded page 1: %+v %v", first, err)
	}
	rest, err := r.Threads(threadCtx, opapi.ThreadQuery{ExcludeParticipant: env.OperatorPeer, Limit: 2, Cursor: *first.Next})
	if err != nil || len(rest.Threads) != 1 || rest.Threads[0].Thread.ThreadID != others[0] || rest.Next != nil {
		t.Fatalf("excluded page 2: %+v %v", rest, err)
	}
	// Combined with participant: B's threads the operator is not in.
	both, err := r.Threads(threadCtx, opapi.ThreadQuery{Participant: b, ExcludeParticipant: env.OperatorPeer})
	if err != nil || len(both.Threads) != 3 {
		t.Fatalf("participant and exclude: %+v %v", both, err)
	}
	none, err := r.Threads(threadCtx, opapi.ThreadQuery{Participant: env.OperatorPeer, ExcludeParticipant: env.OperatorPeer})
	if err != nil || len(none.Threads) != 0 {
		t.Fatalf("same peer both ways: %+v %v", none, err)
	}
	_, err = r.Threads(threadCtx, opapi.ThreadQuery{ExcludeParticipant: "me"})
	assertThreadAPIError(t, err, 422, "invalid_value")
}

func TestThreadCut(t *testing.T) {
	env := testhub.Start(t)
	_, asec := env.NewPeer("session", "A")
	b, bsec := env.NewPeer("session", "B")
	sr := threadSend(t, env, asec, "", []string{b}, nil)
	zero := 0
	if err := env.Hub.PatchThread(threadCtx, sr.ThreadID, hub.ThreadPatch{BudgetSet: true, Budget: &zero}); err != nil {
		t.Fatal(err)
	}
	d := threadDetail(t, env, sr.ThreadID)
	if d.Row.Cut == nil || d.Row.Cut.Reason != "thread_budget_exhausted" || d.Row.Cut.At != d.Row.LastMessageAt {
		t.Fatalf("budget cut: %+v", d.Row)
	}
	depth := threadSend(t, env, asec, "", []string{b}, map[string]any{"max_depth": 1})
	threadSend(t, env, bsec, depth.ThreadID, nil, nil)
	resp, raw := env.Do(asec, "POST", "/v1/messages", map[string]any{"idempotency_key": "cut", "thread_id": depth.ThreadID, "priority": "hold", "content": map[string]any{"text": "too deep"}})
	if resp.StatusCode != 422 || !strings.Contains(string(raw), "max_depth_exceeded") {
		t.Fatalf("depth rejection: %d %s", resp.StatusCode, raw)
	}
	d = threadDetail(t, env, depth.ThreadID)
	if d.Row.Cut == nil || d.Row.Cut.Reason != "max_depth_exceeded" || d.Row.Cut.At == "" {
		t.Fatalf("depth cut: %+v", d.Row.Cut)
	}
	page, err := opview.New(env.Hub).Threads(threadCtx, opapi.ThreadQuery{})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, row := range page.Threads {
		if row.Thread.ThreadID == depth.ThreadID {
			found = true
			if row.Cut == nil || row.Cut.Reason != "max_depth_exceeded" {
				t.Fatalf("list cut: %+v", row)
			}
		}
	}
	if !found {
		t.Fatal("cut thread missing from list")
	}
	env.Now.Advance(25 * time.Hour)
	if got := threadDetail(t, env, depth.ThreadID).Row.Cut; got != nil {
		t.Fatalf("stale cut: %+v", got)
	}
	if got := threadDetail(t, env, sr.ThreadID).Row.Cut; got == nil || got.Reason != "thread_budget_exhausted" {
		t.Fatalf("budget fallback expires: %+v", got)
	}
}

func TestThreadDetailFiles(t *testing.T) {
	env := testhub.Start(t)
	a, asec := env.NewPeer("session", "A")
	b, _ := env.NewPeer("session", "B")
	blob, err := env.Hub.PutBlob(threadCtx, auth.Caller{PeerID: a}, strings.NewReader("data"), "text/plain", 0)
	if err != nil {
		t.Fatal(err)
	}
	sr := threadSend(t, env, asec, "", []string{b}, map[string]any{"attachments": []envelope.Attachment{{SHA256: blob.SHA256, Name: "note.txt", Size: 4, MediaType: "text/plain"}}})
	env.Now.Advance(time.Second)
	long := strings.Repeat("x", 5000)
	spilled := threadSend(t, env, asec, sr.ThreadID, nil, map[string]any{"content": map[string]any{"text": long}})
	d := threadDetail(t, env, sr.ThreadID)
	if d.Now != envelope.FormatTime(env.Now.Now()) || d.Row.Thread.ThreadID != sr.ThreadID {
		t.Fatalf("snapshot: %+v", d)
	}
	if len(d.Files) != 2 {
		t.Fatalf("files: %+v", d.Files)
	}
	f := d.Files[0]
	if f.MessageID != spilled.MessageID || f.Name != "message.md" || f.Size != 5000 || f.MediaType != "text/markdown" || !f.Spilled || f.Purged || f.Sender != a || f.CreatedAt == "" {
		t.Fatalf("spill: %+v", f)
	}
	if f = d.Files[1]; f.MessageID != sr.MessageID || f.SHA256 != blob.SHA256 || f.Size != 4 || f.Name != "note.txt" || f.MediaType != "text/plain" || f.Spilled || f.Purged {
		t.Fatalf("attachment: %+v", f)
	}
	if err := env.Hub.PurgeBlob(threadCtx, blob.SHA256); err != nil {
		t.Fatal(err)
	}
	d = threadDetail(t, env, sr.ThreadID)
	if len(d.Files) != 2 || !d.Files[1].Purged || d.Files[1].Size != 4 {
		t.Fatalf("purged file retained: %+v", d.Files)
	}
	// External attachments need no blob_refs row; they still belong in the detail.
	external := threadSend(t, env, asec, sr.ThreadID, nil, map[string]any{"attachments": []envelope.Attachment{{SHA256: strings.Repeat("a", 64), Name: "external.txt", Size: 7, MediaType: "text/plain", OriginURL: "https://example.invalid/a"}}})
	d = threadDetail(t, env, sr.ThreadID)
	if len(d.Files) != 3 || d.Files[0].MessageID != external.MessageID || d.Files[0].Purged || d.Files[0].Size != 7 {
		t.Fatalf("external attachment: %+v", d.Files)
	}
}

func TestThreadDetailApprovals(t *testing.T) {
	env := testhub.Start(t)
	_, asec := env.NewPeer("session", "A")
	b, bsec := env.NewPeer("session", "B")
	ask := func(thr string, recipient string, expiry time.Time) (hub.SendResponse, string) {
		id := ids.New(ids.Apr)
		sr := threadSend(t, env, asec, thr, []string{recipient}, map[string]any{"kind": "approval_request", "approval_request": map[string]any{"approval_id": id, "question": "Proceed?", "options": []string{"go", "stop"}, "expires_at": envelope.FormatTime(expiry)}})
		return sr, id
	}
	sr, mine := ask("", env.OperatorPeer, env.Now.Now().Add(time.Hour))
	elsewhere, other := ask(sr.ThreadID, b, env.Now.Now().Add(time.Hour))
	_, expired := ask(sr.ThreadID, b, env.Now.Now().Add(time.Second))
	env.Now.Advance(2 * time.Second)
	d := threadDetail(t, env, sr.ThreadID)
	if len(d.OpenApprovals) != 2 {
		t.Fatalf("open unexpired: %+v", d.OpenApprovals)
	}
	found := map[string]opapi.ThreadApproval{}
	for _, a := range d.OpenApprovals {
		found[a.ApprovalID] = a
	}
	if a := found[mine]; !a.AddressedToMe || a.MessageID != sr.MessageID || !reflect.DeepEqual(a.Options, []string{"go", "stop"}) || a.ExpiresAt != envelope.FormatTime(env.Now.Now().Add(time.Hour-2*time.Second)) {
		t.Fatalf("mine: %+v", a)
	}
	if a := found[other]; a.AddressedToMe || a.MessageID != elsewhere.MessageID {
		t.Fatalf("elsewhere: %+v", a)
	}
	if _, ok := found[expired]; ok {
		t.Fatal("expired approval shown")
	}
	threadSend(t, env, bsec, sr.ThreadID, nil, map[string]any{"kind": "approval_response", "approval_response": map[string]any{"approval_id": other, "decision": "go"}})
	d = threadDetail(t, env, sr.ThreadID)
	if len(d.OpenApprovals) != 1 || d.OpenApprovals[0].ApprovalID != mine {
		t.Fatalf("closed approval shown: %+v", d.OpenApprovals)
	}
}

func TestMatrixCells(t *testing.T) {
	env := testhub.Start(t)
	a, asec := env.NewPeer("session", "A")
	b, bsec := env.NewPeer("session", "B")
	c, _ := env.NewPeer("session", "C")
	d, _ := env.NewPeer("session", "D")
	first := threadSend(t, env, asec, "", []string{b, c, d}, nil)
	second := threadSend(t, env, bsec, first.ThreadID, []string{a, c, d}, nil)
	threadExecFixture(t, env, `UPDATE deliveries SET state='accepted', attempt=2, last_status=202 WHERE msg_id=? AND recipient=?`, first.MessageID, b)
	threadExecFixture(t, env, `UPDATE deliveries SET state='dead_letter', attempt=8, last_status=500 WHERE msg_id=? AND recipient=?`, first.MessageID, c)
	threadExecFixture(t, env, `UPDATE deliveries SET state='pushed' WHERE msg_id=? AND recipient=?`, second.MessageID, a)
	r := opview.New(env.Hub)
	matrix, err := r.Matrix(threadCtx, first.ThreadID, "", 200)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(matrix.Recipients, []string{a, b, c, d}) || len(matrix.Rows) != 2 || matrix.NextAfter != nil {
		t.Fatalf("matrix: %+v", matrix)
	}
	for i, row := range matrix.Rows {
		wantID, wantSender := first.MessageID, a
		if i == 1 {
			wantID, wantSender = second.MessageID, b
		}
		if row.MessageID != wantID || row.Sender != wantSender || row.Excerpt != "hello" || row.CreatedAt == "" || len(row.Cells) != 3 {
			t.Fatalf("row: %+v", row)
		}
		for _, cell := range row.Cells {
			if cell.Recipient == row.Sender || cell.DeliveryID == "" || cell.UpdatedAt == "" {
				t.Fatalf("invalid cell: %+v", cell)
			}
		}
	}
	cells := map[string]opapi.MatrixCell{}
	for _, cell := range matrix.Rows[0].Cells {
		cells[cell.Recipient] = cell
	}
	if cell := cells[b]; cell.State != "accepted" || cell.Display != "accepted" || cell.Attempt != 2 || cell.LastStatus == nil || *cell.LastStatus != 202 || cell.ReadAt != nil {
		t.Fatalf("accepted: %+v", cell)
	}
	if cell := cells[c]; cell.State != "dead_letter" || cell.Display != "dead_letter" || cell.Attempt != 8 || cell.LastStatus == nil || *cell.LastStatus != 500 {
		t.Fatalf("dead letter: %+v", cell)
	}
	if cell := cells[d]; cell.State != "queued" || cell.Display != "queued" || cell.LastStatus != nil || cell.ReadAt != nil {
		t.Fatalf("queued: %+v", cell)
	}
	if err := env.Hub.Receipt(threadCtx, auth.Caller{Kind: "peer", PeerID: b}, cells[b].DeliveryID, "read", ""); err != nil {
		t.Fatal(err)
	}
	if err := env.Hub.Receipt(threadCtx, auth.Caller{Kind: "peer", PeerID: c}, cells[c].DeliveryID, "read", ""); err != nil {
		t.Fatal(err)
	}
	matrix, err = r.Matrix(threadCtx, first.ThreadID, "", 200)
	if err != nil {
		t.Fatal(err)
	}
	for _, cell := range matrix.Rows[0].Cells {
		if cell.Recipient == b && (cell.Display != "read" || cell.State != "accepted" || cell.ReadAt == nil) {
			t.Fatalf("read: %+v", cell)
		}
		if cell.Recipient == c && (cell.Display != "dead_letter" || cell.ReadAt == nil) {
			t.Fatalf("read must not hide dead letter: %+v", cell)
		}
	}
	// A read receipt overlays pushed just as it overlays accepted.
	pushed := matrix.Rows[1].Cells[0]
	if pushed.Recipient != a || pushed.State != "pushed" || pushed.Display != "pushed" {
		t.Fatalf("pushed: %+v", pushed)
	}
	if err := env.Hub.Receipt(threadCtx, auth.Caller{Kind: "peer", PeerID: a}, pushed.DeliveryID, "read", ""); err != nil {
		t.Fatal(err)
	}
	matrix, err = r.Matrix(threadCtx, first.ThreadID, "", 200)
	if err != nil || matrix.Rows[1].Cells[0].Display != "read" || matrix.Rows[1].Cells[0].ReadAt == nil {
		t.Fatalf("pushed read: %+v %v", matrix, err)
	}
	// Sender A is still part of the recipient union on a page containing only its own message.
	page, err := r.Matrix(threadCtx, first.ThreadID, "", 1)
	if err != nil || !reflect.DeepEqual(page.Recipients, []string{a, b, c, d}) {
		t.Fatalf("stable recipient union: %+v %v", page, err)
	}
	// Expiry notices are authored by the hub but have real deliveries. The
	// protocol's one-cell-per-delivery rule still exposes these deliveries.
	approval := ids.New(ids.Apr)
	threadSend(t, env, asec, first.ThreadID, []string{b}, map[string]any{
		"kind": "approval_request", "approval_request": map[string]any{
			"approval_id": approval, "question": "Expire?", "options": []string{"yes", "no"},
			"expires_at": envelope.FormatTime(env.Now.Now().Add(time.Second)),
		},
	})
	env.Now.Advance(2 * time.Second)
	if n, err := env.Hub.ExpireApprovals(threadCtx, env.Now.Now()); err != nil || n != 1 {
		t.Fatalf("expiry: %d %v", n, err)
	}
	matrix, err = r.Matrix(threadCtx, first.ThreadID, "", 200)
	if err != nil || len(matrix.Rows) != 4 {
		t.Fatalf("expiry matrix: %+v %v", matrix, err)
	}
	notice := matrix.Rows[3]
	if notice.Sender != "hub" || notice.Excerpt != "Decision: expired" || len(notice.Cells) != 4 {
		t.Fatalf("hub delivery cells: %+v", notice)
	}
}

// Stored-message fixtures exercise read-only states independently of send limits.
func fixtureMessage(t *testing.T, env *testhub.Env, thr, sender string, seq int) string {
	t.Helper()
	id := ids.New(ids.Msg)
	now := envelope.FormatTime(env.Now.Now())
	m := envelope.Message{MessageID: id, ThreadID: thr, Sender: sender, Kind: "message", CreatedAt: now, Content: envelope.Content{Text: fmt.Sprintf("message %d", seq)}}
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	threadExecFixture(t, env, `INSERT INTO messages(msg_id,thr_id,seq,sender,kind,created_at,body) VALUES(?,?,?,?,?,?,?)`, id, thr, seq, sender, "message", now, string(raw))
	return id
}

func TestMatrixPagingAndUnknown(t *testing.T) {
	env := testhub.Start(t)
	a, asec := env.NewPeer("session", "A")
	env.SetLimits(a, 1000, 1000)
	b, _ := env.NewPeer("session", "B")
	first := threadSend(t, env, asec, "", []string{b}, nil)
	want := []string{first.MessageID}
	for i := 2; i <= 202; i++ {
		sender := a
		if i == 2 {
			sender = "hub"
		}
		want = append(want, fixtureMessage(t, env, first.ThreadID, sender, i))
	}
	r := opview.New(env.Hub)
	p, err := r.Matrix(threadCtx, first.ThreadID, "", 1)
	if err != nil || len(p.Rows) != 1 || p.Rows[0].MessageID != want[0] || p.NextAfter == nil || *p.NextAfter != want[0] {
		t.Fatalf("first page: %+v %v", p, err)
	}
	p, err = r.Matrix(threadCtx, first.ThreadID, *p.NextAfter, 1)
	if err != nil || len(p.Rows) != 1 || p.Rows[0].MessageID != want[1] || p.Rows[0].Sender != "hub" || len(p.Rows[0].Cells) != 0 {
		t.Fatalf("hub row: %+v %v", p, err)
	}
	p, err = r.Matrix(threadCtx, first.ThreadID, "", 999)
	if err != nil || len(p.Rows) != 200 || p.NextAfter == nil || *p.NextAfter != want[199] {
		t.Fatalf("cap: %d %v %v", len(p.Rows), p.NextAfter, err)
	}
	tail, err := r.Matrix(threadCtx, first.ThreadID, *p.NextAfter, 200)
	if err != nil || len(tail.Rows) != 2 || tail.NextAfter != nil || tail.Rows[0].MessageID != want[200] || tail.Rows[1].MessageID != want[201] {
		t.Fatalf("tail: %+v %v", tail, err)
	}
	defaults, err := r.Matrix(threadCtx, first.ThreadID, "", 0)
	if err != nil || len(defaults.Rows) != 50 {
		t.Fatalf("protocol default: %d %v", len(defaults.Rows), err)
	}
	_, err = r.Matrix(threadCtx, first.ThreadID, "invalid", 1)
	assertThreadAPIError(t, err, 422, "invalid_value")
	_, err = r.Matrix(threadCtx, first.ThreadID, ids.New(ids.Msg), 1)
	assertThreadAPIError(t, err, 422, "not_in_thread")
	other := threadSend(t, env, asec, "", []string{b}, nil)
	_, err = r.Matrix(threadCtx, first.ThreadID, other.MessageID, 1)
	assertThreadAPIError(t, err, 422, "not_in_thread")
	unknown := ids.New(ids.Thr)
	_, err = r.Matrix(threadCtx, unknown, "", 1)
	assertThreadAPIError(t, err, 404, "unknown_thread")
	_, err = r.Thread(threadCtx, auth.Caller{Kind: "operator", PeerID: env.OperatorPeer}, unknown)
	assertThreadAPIError(t, err, 404, "unknown_thread")
	resp, raw := env.Do(env.OperatorSecret, "GET", "/v1/admin/threads/"+unknown+"/deliveries", nil)
	if resp.StatusCode != 404 || !strings.Contains(string(raw), `"code":"unknown_thread"`) {
		t.Fatalf("HTTP unknown: %d %s", resp.StatusCode, raw)
	}
}

func TestMatrix64Recipients(t *testing.T) {
	env := testhub.Start(t)
	a, asec := env.NewPeer("session", "A")
	b, _ := env.NewPeer("session", "B")
	sr := threadSend(t, env, asec, "", []string{b}, nil)
	// A non-peer source can address all 64 participants without itself joining.
	recipients := []string{a, b}
	for i := 2; i < 64; i++ {
		peer := ids.New(ids.Peer)
		recipients = append(recipients, peer)
		threadExecFixture(t, env, `INSERT INTO thread_participants(thr_id,peer_id,joined_at) VALUES(?,?,?)`, sr.ThreadID, peer, envelope.FormatTime(env.Now.Now()))
	}
	msg := fixtureMessage(t, env, sr.ThreadID, "source", 2)
	for _, peer := range recipients {
		threadExecFixture(t, env, `INSERT INTO deliveries(dly_id,msg_id,thr_id,recipient,priority,state,attempts_left,expires_at,created_at,updated_at,body_sha256) VALUES(?,?,?,?,'hold','queued',8,?,?,?,'fixture')`, ids.New(ids.Dly), msg, sr.ThreadID, peer, envelope.FormatTime(env.Now.Now().Add(time.Hour)), envelope.FormatTime(env.Now.Now()), envelope.FormatTime(env.Now.Now()))
	}
	m, err := opview.New(env.Hub).Matrix(threadCtx, sr.ThreadID, sr.MessageID, 200)
	if err != nil || len(m.Rows) != 1 || len(m.Rows[0].Cells) != 64 || !reflect.DeepEqual(m.Recipients, recipients) {
		t.Fatalf("64 recipients: %+v %v", m, err)
	}
	seen := map[string]bool{}
	for _, cell := range m.Rows[0].Cells {
		if seen[cell.Recipient] {
			t.Fatalf("duplicate recipient: %s", cell.Recipient)
		}
		seen[cell.Recipient] = true
	}
	for _, peer := range recipients {
		if !seen[peer] {
			t.Fatalf("missing recipient: %s", peer)
		}
	}
}

func TestMatrixHidesReactionMessages(t *testing.T) {
	env := testhub.Start(t)
	a, as := env.NewPeer("session", "A")
	b, bs := env.NewPeer("session", "B")
	target := threadSend(t, env, as, "", []string{b}, nil)
	// Give A only a reaction delivery, and keep B's original message column.
	reaction := threadSend(t, env, bs, target.ThreadID, []string{a}, nil)
	threadExecFixture(t, env, `UPDATE messages SET kind='reaction',body=json_set(body,'$.kind','reaction','$.reaction',json_object('target',?,'emoji','✅')) WHERE msg_id=?`, target.MessageID, reaction.MessageID)
	last := threadSend(t, env, as, target.ThreadID, []string{b}, nil)
	reader := opview.New(env.Hub)
	got, err := reader.Matrix(threadCtx, target.ThreadID, "", 2)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Recipients, []string{b}) {
		t.Errorf("columns=%v want only B", got.Recipients)
	}
	if len(got.Rows) != 2 || got.Rows[0].MessageID != target.MessageID || got.Rows[1].MessageID != last.MessageID || got.NextAfter != nil {
		t.Fatalf("matrix=%+v", got)
	}
}

// The projection preserves generation identity independently of content hash.
// Seed retained message JSON directly: transfer/provider acceptance is tested
// elsewhere, and this test exercises only the SQL read projection.
func TestThreadDetailFileGenerationIdentity(t *testing.T) {
	env := testhub.Start(t)
	_, secret := env.NewPeer("session", "A")
	peer, _ := env.NewPeer("session", "B")
	sr := threadSend(t, env, secret, "", []string{peer}, nil)
	atts := []envelope.Attachment{
		{BlobID: "blo_00000000000000000000000001", SHA256: strings.Repeat("a", 64), Name: "old.txt", Size: 3, MediaType: "text/plain", RetentionClass: "1h"},
		{BlobID: "blo_00000000000000000000000002", SHA256: strings.Repeat("a", 64), Name: "keep.txt", Size: 3, MediaType: "text/plain", RetentionClass: "keep"},
	}
	raw, err := json.Marshal(atts)
	if err != nil {
		t.Fatal(err)
	}
	threadExecFixture(t, env, `UPDATE messages SET body=json_set(body,'$.attachments',json(?)) WHERE msg_id=?`, string(raw), sr.MessageID)
	d := threadDetail(t, env, sr.ThreadID)
	if len(d.Files) != 2 {
		t.Fatal("missing generations")
	}
	for i, f := range d.Files {
		if f.BlobID != atts[i].BlobID || f.SHA256 != atts[i].SHA256 {
			t.Fatalf("file %d lost generation identity", i)
		}
	}
}
