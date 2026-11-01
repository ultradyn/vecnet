package httpapi_test

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/testhub"
)

type threadsResp struct {
	Threads []envelope.ThreadSummary `json:"threads"`
	Next    *string                  `json:"next"`
}

type historyResp struct {
	Messages  []envelope.Message `json:"messages"`
	NextAfter *string            `json:"next_after"`
}

// listThreads is GET /v1/threads<query> expecting 200; it also checks the
// body carries exactly {threads, next}.
func listThreads(t *testing.T, env *testhub.Env, secret, query string) threadsResp {
	t.Helper()
	resp, raw := env.Do(secret, "GET", "/v1/threads"+query, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /v1/threads%s = %d; %s", query, resp.StatusCode, raw)
	}
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(raw, &keys); err != nil || len(keys) != 2 || keys["threads"] == nil || keys["next"] == nil {
		t.Fatalf("GET /v1/threads body %s, want exactly {threads, next}", raw)
	}
	var tr threadsResp
	if err := json.Unmarshal(raw, &tr); err != nil {
		t.Fatalf("decode: %v; %s", err, raw)
	}
	return tr
}

// history is GET /v1/threads/{thr}/messages<query> expecting 200; it also
// checks the body carries exactly {messages, next_after}.
func history(t *testing.T, env *testhub.Env, secret, thr, query string) historyResp {
	t.Helper()
	resp, raw := env.Do(secret, "GET", "/v1/threads/"+thr+"/messages"+query, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /v1/threads/%s/messages%s = %d; %s", thr, query, resp.StatusCode, raw)
	}
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(raw, &keys); err != nil || len(keys) != 2 || keys["messages"] == nil || keys["next_after"] == nil {
		t.Fatalf("history body %s, want exactly {messages, next_after}", raw)
	}
	var hr historyResp
	if err := json.Unmarshal(raw, &hr); err != nil {
		t.Fatalf("decode: %v; %s", err, raw)
	}
	return hr
}

func msgIDs(ms []envelope.Message) []string {
	out := make([]string, len(ms))
	for i, m := range ms {
		out[i] = m.MessageID
	}
	return out
}

func sameIDs(got, want []string) bool {
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

// inThread is a send body continuing thr; recipients nil defaults to the
// thread's participants.
func inThread(key, thr string, recipients ...string) map[string]any {
	b := sendBody(key, recipients...)
	b["thread_id"] = thr
	return b
}

func TestThreadListPagination(t *testing.T) {
	env := testhub.Start(t)
	_, aSec := env.NewPeer("session", "A")
	b, bSec := env.NewPeer("session", "B")
	_, dSec := env.NewPeer("session", "D")

	var thrs []string
	for _, k := range []string{"t1", "t2", "t3"} {
		thrs = append(thrs, postSend(t, env, aSec, sendBody(k, b), http.StatusAccepted).ThreadID)
		env.Now.Advance(time.Second)
	}

	// Newest activity first.
	p1 := listThreads(t, env, aSec, "?limit=2")
	if len(p1.Threads) != 2 || p1.Threads[0].ThreadID != thrs[2] || p1.Threads[1].ThreadID != thrs[1] {
		t.Fatalf("page 1 = %+v, want [%s %s]", p1.Threads, thrs[2], thrs[1])
	}
	if p1.Next == nil || *p1.Next == "" {
		t.Fatalf("page 1 next = %v, want a token", p1.Next)
	}
	if s := p1.Threads[0]; s.MessageCount != 1 || len(s.Participants) != 2 || s.HistoryAccess != "from_join" {
		t.Fatalf("summary %+v, want 1 message, 2 participants, from_join", s)
	}
	p2 := listThreads(t, env, aSec, "?limit=2&cursor="+url.QueryEscape(*p1.Next))
	if len(p2.Threads) != 1 || p2.Threads[0].ThreadID != thrs[0] {
		t.Fatalf("page 2 = %+v, want only %s", p2.Threads, thrs[0])
	}
	if p2.Next != nil {
		t.Fatalf("page 2 next = %q, want null", *p2.Next)
	}

	// The other participant sees them too (default limit, participant=me).
	if all := listThreads(t, env, bSec, "?participant=me"); len(all.Threads) != 3 || all.Next != nil {
		t.Fatalf("B's list = %d threads next %v, want 3 and null", len(all.Threads), all.Next)
	}
	// A non-participant's list excludes them.
	if d := listThreads(t, env, dSec, ""); len(d.Threads) != 0 || d.Next != nil {
		t.Fatalf("D's list = %+v, want empty", d)
	}

	// Malformed paging parameters are 422 invalid_value.
	resp, raw := env.Do(aSec, "GET", "/v1/threads?cursor=garbage", nil)
	mustErr(t, resp, raw, http.StatusUnprocessableEntity, "invalid_value")
	resp, raw = env.Do(aSec, "GET", "/v1/threads?limit=0", nil)
	mustErr(t, resp, raw, http.StatusUnprocessableEntity, "invalid_value")
}

func TestHistoryFromJoin(t *testing.T) {
	env := testhub.Start(t)
	_, aSec := env.NewPeer("session", "A")
	b, bSec := env.NewPeer("session", "B")
	c, cSec := env.NewPeer("session", "C")

	m1 := postSend(t, env, aSec, sendBody("h1", b), http.StatusAccepted)
	thr := m1.ThreadID
	m2 := postSend(t, env, aSec, inThread("h2", thr), http.StatusAccepted)
	m3 := postSend(t, env, aSec, inThread("h3", thr, b, c), http.StatusAccepted)

	// C joined at message 3: under from_join it reads only message 3.
	hc := history(t, env, cSec, thr, "")
	if !sameIDs(msgIDs(hc.Messages), []string{m3.MessageID}) || hc.NextAfter != nil {
		t.Fatalf("C's history = %v next %v, want [%s] null", msgIDs(hc.Messages), hc.NextAfter, m3.MessageID)
	}
	if hc.Messages[0].Content.Text != "hi h3" || hc.Messages[0].ThreadID != thr {
		t.Fatalf("C's message 3 = %+v", hc.Messages[0])
	}
	// after is exclusive; at the end next_after is null.
	if h := history(t, env, cSec, thr, "?after="+m3.MessageID); len(h.Messages) != 0 || h.NextAfter != nil {
		t.Fatalf("after=msg3 = %v next %v, want empty and null", msgIDs(h.Messages), h.NextAfter)
	}
	// after a message C cannot see still yields only what C can see.
	if h := history(t, env, cSec, thr, "?after="+m1.MessageID); !sameIDs(msgIDs(h.Messages), []string{m3.MessageID}) {
		t.Fatalf("C after=msg1 = %v, want [%s]", msgIDs(h.Messages), m3.MessageID)
	}

	// B was there from the start: all three, and paging walks them.
	all := []string{m1.MessageID, m2.MessageID, m3.MessageID}
	if h := history(t, env, bSec, thr, ""); !sameIDs(msgIDs(h.Messages), all) || h.NextAfter != nil {
		t.Fatalf("B's history = %v next %v, want %v null", msgIDs(h.Messages), h.NextAfter, all)
	}
	p1 := history(t, env, bSec, thr, "?limit=2")
	if !sameIDs(msgIDs(p1.Messages), all[:2]) || p1.NextAfter == nil || *p1.NextAfter != m2.MessageID {
		t.Fatalf("B limit=2 = %v next %v, want %v next %s", msgIDs(p1.Messages), p1.NextAfter, all[:2], m2.MessageID)
	}
	p2 := history(t, env, bSec, thr, "?limit=2&after="+*p1.NextAfter)
	if !sameIDs(msgIDs(p2.Messages), all[2:]) || p2.NextAfter != nil {
		t.Fatalf("B page 2 = %v next %v, want %v null", msgIDs(p2.Messages), p2.NextAfter, all[2:])
	}
	// The operator reads the whole thread.
	if h := history(t, env, env.OperatorSecret, thr, ""); !sameIDs(msgIDs(h.Messages), all) {
		t.Fatalf("operator's history = %v, want %v", msgIDs(h.Messages), all)
	}

	// A thread with history_access full (the creator/operator setting; the
	// admin PATCH is Task 17, so set it in SQL): C sees all three.
	f1 := postSend(t, env, aSec, sendBody("f1", b), http.StatusAccepted)
	full := f1.ThreadID
	if _, err := env.Hub.DB.Exec(`UPDATE threads SET history_access = 'full' WHERE thr_id = ?`, full); err != nil {
		t.Fatal(err)
	}
	f2 := postSend(t, env, aSec, inThread("f2", full), http.StatusAccepted)
	f3 := postSend(t, env, aSec, inThread("f3", full, b, c), http.StatusAccepted)
	want := []string{f1.MessageID, f2.MessageID, f3.MessageID}
	if h := history(t, env, cSec, full, ""); !sameIDs(msgIDs(h.Messages), want) || h.NextAfter != nil {
		t.Fatalf("C's full history = %v next %v, want %v null", msgIDs(h.Messages), h.NextAfter, want)
	}

	// after must be a message of this thread.
	resp, raw := env.Do(bSec, "GET", "/v1/threads/"+thr+"/messages?after="+f1.MessageID, nil)
	mustErr(t, resp, raw, http.StatusUnprocessableEntity, "not_in_thread")
}

func TestHistoryBlobVisibility(t *testing.T) {
	env := testhub.Start(t)
	_, aSec := env.NewPeer("session", "A")
	b, bSec := env.NewPeer("session", "B")
	c, cSec := env.NewPeer("session", "C")
	const early, late = "attached to message one", "attached to message three"
	sha1 := putOK(t, env, aSec, "text/plain", early).SHA256
	sha3 := putOK(t, env, aSec, "text/plain", late).SHA256

	b1 := sendBody("v1", b)
	b1["attachments"] = []any{map[string]any{"sha256": sha1, "size": len(early), "media_type": "text/plain"}}
	thr := postSend(t, env, aSec, b1, http.StatusAccepted).ThreadID
	postSend(t, env, aSec, inThread("v2", thr), http.StatusAccepted)
	b3 := inThread("v3", thr, b, c)
	b3["attachments"] = []any{map[string]any{"sha256": sha3, "size": len(late), "media_type": "text/plain"}}
	postSend(t, env, aSec, b3, http.StatusAccepted)

	// C joined at message 3 (from_join): message 1's blob is not visible
	// through its bearer; B's is.
	resp, raw := env.Do(cSec, "GET", "/v1/blobs/"+sha1, nil)
	mustErr(t, resp, raw, http.StatusForbidden, "forbidden")
	resp, raw = env.Do(bSec, "GET", "/v1/blobs/"+sha1, nil)
	if resp.StatusCode != http.StatusOK || string(raw) != early {
		t.Fatalf("B GET message 1 blob = %d %q, want 200 %q", resp.StatusCode, raw, early)
	}
	if ok, err := env.Hub.CanSeeBlob(t.Context(), c, sha1); err != nil || ok {
		t.Fatalf("CanSeeBlob(C, sha1) = %v, %v; want false", ok, err)
	}
	if ok, err := env.Hub.CanSeeBlob(t.Context(), b, sha1); err != nil || !ok {
		t.Fatalf("CanSeeBlob(B, sha1) = %v, %v; want true", ok, err)
	}

	// C's own Delivery of message 3 carries a working blob_url, and the
	// message is visible to C, so its bearer reads that blob too.
	m, _ := lastMessage(t, env, c)
	if len(m.Attachments) != 1 || m.Attachments[0].SHA256 != sha3 || m.Attachments[0].BlobURL == "" {
		t.Fatalf("C's delivery attachments = %+v", m.Attachments)
	}
	path := strings.TrimPrefix(m.Attachments[0].BlobURL, env.URL)
	resp, raw = env.Do("", "GET", path, nil)
	if resp.StatusCode != http.StatusOK || string(raw) != late {
		t.Fatalf("C GET blob_url = %d %q, want 200 %q", resp.StatusCode, raw, late)
	}
	resp, raw = env.Do(cSec, "GET", "/v1/blobs/"+sha3, nil)
	if resp.StatusCode != http.StatusOK || string(raw) != late {
		t.Fatalf("C bearer GET message 3 blob = %d %q", resp.StatusCode, raw)
	}
}

func TestNotParticipant403(t *testing.T) {
	env := testhub.Start(t)
	_, aSec := env.NewPeer("session", "A")
	b, _ := env.NewPeer("session", "B")
	_, dSec := env.NewPeer("session", "D")
	thr := postSend(t, env, aSec, sendBody("np", b), http.StatusAccepted).ThreadID

	resp, raw := env.Do(dSec, "GET", "/v1/threads/"+thr+"/messages", nil)
	mustErr(t, resp, raw, http.StatusForbidden, "not_participant")
	resp, raw = env.Do(dSec, "GET", "/v1/threads/thr_01ARZ3NDEKTSV4RRFFQ69G5FAV/messages", nil)
	mustErr(t, resp, raw, http.StatusNotFound, "unknown_thread")
}
