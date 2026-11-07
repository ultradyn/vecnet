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
// checks the body carries exactly {messages, next_after}, plus hosted_blobs
// for the operator (TestOperatorHistoryHostedBlobs).
func history(t *testing.T, env *testhub.Env, secret, thr, query string) historyResp {
	t.Helper()
	resp, raw := env.Do(secret, "GET", "/v1/threads/"+thr+"/messages"+query, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /v1/threads/%s/messages%s = %d; %s", thr, query, resp.StatusCode, raw)
	}
	var keys map[string]json.RawMessage
	want := 2
	if secret == env.OperatorSecret {
		want = 3
	}
	if err := json.Unmarshal(raw, &keys); err != nil || len(keys) != want || keys["messages"] == nil || keys["next_after"] == nil || want == 3 && keys["hosted_blobs"] == nil {
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

// TestThreadTitleQuery: q= is a case-insensitive substring of the thread
// title only. It composes with participant= and the existing keyset page,
// and an empty q leaves the list unfiltered.
func TestThreadTitleQuery(t *testing.T) {
	env := testhub.Start(t)
	_, aSec := env.NewPeer("session", "A")
	b, _ := env.NewPeer("session", "B")
	c, _ := env.NewPeer("session", "C")

	sendTitled := func(key, title, text string, to ...string) string {
		t.Helper()
		body := sendBody(key, to...)
		if title != "" {
			body["thread_title"] = title
		}
		if text != "" {
			body["content"] = map[string]any{"text": text}
		}
		id := postSend(t, env, aSec, body, http.StatusAccepted).ThreadID
		env.Now.Advance(time.Second)
		return id
	}

	// Oldest first. Activity order is the reverse.
	alphaOld := sendTitled("q1", "alpha old", "", b)
	noise := sendTitled("q2", "weekly sync", "", b)
	alphaMid := sendTitled("q3", "The ALPHA release", "", b)
	bodyOnly := sendTitled("q4", "plain notes", "please read alpha", b)
	alphaNew := sendTitled("q5", "xxALPHAyy", "", b)
	alphaC := sendTitled("q6", "alphabet soup", "", c)
	pct := sendTitled("q7", "100% done", "", b)
	under := sendTitled("q8", "a_b score", "", b)

	idsOf := func(tr threadsResp) []string {
		t.Helper()
		return threadIDs(tr.Threads)
	}
	query := func(vals url.Values) string {
		t.Helper()
		return "?" + vals.Encode()
	}

	// Case-insensitive title substring, and only threads B is also in.
	withB := url.Values{}
	withB.Set("q", "aLpHa")
	withB.Set("participant", b)
	if got := listThreads(t, env, aSec, query(withB)); !sameIDs(idsOf(got), []string{alphaNew, alphaMid, alphaOld}) || got.Next != nil {
		t.Fatalf("q=aLpHa participant=B = %v next %v, want [%s %s %s] null", idsOf(got), got.Next, alphaNew, alphaMid, alphaOld)
	}
	// Without participant, C's matching thread is included; the body hit is not.
	titleOnly := url.Values{}
	titleOnly.Set("q", "ALPHA")
	if got := listThreads(t, env, aSec, query(titleOnly)); !sameIDs(idsOf(got), []string{alphaC, alphaNew, alphaMid, alphaOld}) {
		t.Fatalf("q=ALPHA = %v, want [%s %s %s %s] (not %s or %s)", idsOf(got), alphaC, alphaNew, alphaMid, alphaOld, noise, bodyOnly)
	}

	// The filter is inside the keyset query: a non-match newer than a match
	// must not consume a page slot or hide the older match.
	page := url.Values{}
	page.Set("q", "alpha")
	page.Set("participant", b)
	page.Set("limit", "2")
	p1 := listThreads(t, env, aSec, query(page))
	if !sameIDs(idsOf(p1), []string{alphaNew, alphaMid}) || p1.Next == nil {
		t.Fatalf("filtered page 1 = %v next %v, want [%s %s] and a token", idsOf(p1), p1.Next, alphaNew, alphaMid)
	}
	page.Set("cursor", *p1.Next)
	p2 := listThreads(t, env, aSec, query(page))
	if !sameIDs(idsOf(p2), []string{alphaOld}) || p2.Next != nil {
		t.Fatalf("filtered page 2 = %v next %v, want [%s] null", idsOf(p2), p2.Next, alphaOld)
	}

	// Empty q is today's unfiltered list (A's threads; participant narrows).
	empty := url.Values{}
	empty.Set("q", "")
	empty.Set("participant", b)
	if got := listThreads(t, env, aSec, query(empty)); !sameIDs(idsOf(got), []string{under, pct, alphaNew, bodyOnly, alphaMid, noise, alphaOld}) {
		t.Fatalf("empty q participant=B = %v, want every thread B shares", idsOf(got))
	}

	// '%' and '_' are literal characters, not a match-all pattern.
	literal := url.Values{}
	literal.Set("q", "%")
	if got := listThreads(t, env, aSec, query(literal)); !sameIDs(idsOf(got), []string{pct}) {
		t.Fatalf("q=%% = %v, want only %s", idsOf(got), pct)
	}
	literal.Set("q", "_")
	if got := listThreads(t, env, aSec, query(literal)); !sameIDs(idsOf(got), []string{under}) {
		t.Fatalf("q=_ = %v, want only %s", idsOf(got), under)
	}
}

func TestThreadTitleQueryBound(t *testing.T) {
	env := testhub.Start(t)
	_, aSec := env.NewPeer("session", "A")
	b, _ := env.NewPeer("session", "B")
	postSend(t, env, aSec, sendBody("bound", b), http.StatusAccepted)

	over := strings.Repeat("z", 129)
	resp, raw := env.Do(aSec, "GET", "/v1/threads?q="+url.QueryEscape(over), nil)
	e := mustErr(t, resp, raw, http.StatusUnprocessableEntity, "invalid_value")
	if e.Error.Details["field"] != "q" {
		t.Fatalf("details.field = %v, want q", e.Error.Details["field"])
	}

	// 128 characters is accepted and simply matches nothing here. The cap
	// counts characters, so 128 non-ASCII characters are still in range.
	ok := url.Values{}
	ok.Set("q", strings.Repeat("z", 128))
	if got := listThreads(t, env, aSec, "?"+ok.Encode()); len(got.Threads) != 0 || got.Next != nil {
		t.Fatalf("q of 128 z's = %+v, want an empty page", got)
	}
	ok.Set("q", strings.Repeat("é", 128))
	if got := listThreads(t, env, aSec, "?"+ok.Encode()); len(got.Threads) != 0 || got.Next != nil {
		t.Fatalf("q of 128 é's = %+v, want an empty page", got)
	}
	resp, raw = env.Do(aSec, "GET", "/v1/threads?q="+url.QueryEscape(strings.Repeat("é", 129)), nil)
	e = mustErr(t, resp, raw, http.StatusUnprocessableEntity, "invalid_value")
	if e.Error.Details["field"] != "q" {
		t.Fatalf("details.field = %v, want q", e.Error.Details["field"])
	}
}

// TestThreadTitleQueryWithGroupAndSince: q stays inside the same list query
// as group and since, so both still narrow the page.
func TestThreadTitleQueryWithGroupAndSince(t *testing.T) {
	env := testhub.Start(t)
	_, aSec := env.NewPeer("session", "A")
	b, _ := env.NewPeer("session", "B")
	c, _ := env.NewPeer("session", "C")
	g := createGroup(t, env, aSec, b)
	other := createGroup(t, env, aSec, c)

	send := func(key, title, to string) string {
		t.Helper()
		body := sendBody(key, to)
		body["thread_title"] = title
		id := postSend(t, env, aSec, body, http.StatusAccepted).ThreadID
		env.Now.Advance(time.Second)
		return id
	}
	early := send("gs1", "alpha in group", g)
	cutoff := envelope.FormatTime(env.Now.Now())
	env.Now.Advance(time.Second)
	later := send("gs2", "ALPHA later", g)
	send("gs3", "alpha other group", other)
	send("gs4", "beta in group", g)

	q := url.Values{}
	q.Set("q", "alpha")
	q.Set("group", g)
	if got := listThreads(t, env, aSec, "?"+q.Encode()); !sameIDs(threadIDs(got.Threads), []string{later, early}) || got.Next != nil {
		t.Fatalf("q+group = %v next %v, want [%s %s] null", threadIDs(got.Threads), got.Next, later, early)
	}
	q.Set("since", cutoff)
	if got := listThreads(t, env, aSec, "?"+q.Encode()); !sameIDs(threadIDs(got.Threads), []string{later}) || got.Next != nil {
		t.Fatalf("q+group+since = %v next %v, want [%s] null", threadIDs(got.Threads), got.Next, later)
	}
}

func threadIDs(ts []envelope.ThreadSummary) []string {
	out := make([]string, len(ts))
	for i, s := range ts {
		out[i] = s.ThreadID
	}
	return out
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

// TestOperatorHistoryHostedBlobs: the operator's history page names, per
// message, the attachments whose bytes the hub hosts (a blob_refs row and
// unpurged bytes), so the UI can tell hosted bytes that also carry an
// informational origin_url from reference-only ones without a Delivery's
// blob_url. Other callers' pages are unchanged.
func TestOperatorHistoryHostedBlobs(t *testing.T) {
	env := testhub.Start(t)
	_, aSec := env.NewPeer("session", "A")
	b, _ := env.NewPeer("session", "B")
	hosted := putOK(t, env, aSec, "image/png", "png bytes").SHA256
	purged := putOK(t, env, aSec, "image/png", "soon gone").SHA256
	const refOnly = "0000000000000000000000000000000000000000000000000000000000000001"
	body := sendBody("hosted", b)
	body["attachments"] = []any{
		map[string]any{"sha256": hosted, "size": 9, "media_type": "image/png", "origin_url": "https://example.com/a.png"},
		map[string]any{"sha256": refOnly, "size": 5, "media_type": "image/png", "origin_url": "https://example.com/b.png"},
		map[string]any{"sha256": purged, "size": 9, "media_type": "image/png"},
	}
	sent := postSend(t, env, aSec, body, http.StatusAccepted)
	postSend(t, env, aSec, inThread("plain", sent.ThreadID), http.StatusAccepted)

	page := func(secret string) map[string]json.RawMessage {
		t.Helper()
		resp, raw := env.Do(secret, "GET", "/v1/threads/"+sent.ThreadID+"/messages", nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("history = %d %s", resp.StatusCode, raw)
		}
		var top map[string]json.RawMessage
		if err := json.Unmarshal(raw, &top); err != nil {
			t.Fatal(err)
		}
		return top
	}
	hostedOf := func() map[string][]string {
		t.Helper()
		raw, ok := page(env.OperatorSecret)["hosted_blobs"]
		if !ok {
			t.Fatal("the operator's history page has no hosted_blobs")
		}
		var got map[string][]string
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatal(err)
		}
		return got
	}
	got := hostedOf()
	if len(got) != 1 || strings.Join(got[sent.MessageID], ",") != hosted+","+purged {
		t.Fatalf("hosted_blobs = %v, want %s: [%s %s]", got, sent.MessageID, hosted, purged)
	}
	if err := env.Hub.PurgeBlob(t.Context(), purged); err != nil {
		t.Fatal(err)
	}
	if got := hostedOf(); strings.Join(got[sent.MessageID], ",") != hosted {
		t.Fatalf("after the purge hosted_blobs = %v, want only %s", got, hosted)
	}
	if _, ok := page(aSec)["hosted_blobs"]; ok {
		t.Fatal("a peer's history page carries hosted_blobs")
	}
}
