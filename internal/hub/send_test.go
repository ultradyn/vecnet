package hub_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"testing"

	"github.com/XertroV/vecnet/internal/auth"
	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/eventlog"
	"github.com/XertroV/vecnet/internal/hub"
	"github.com/XertroV/vecnet/internal/ids"
	"github.com/XertroV/vecnet/internal/testhub"
)

// sendHTTP posts body to /v1/messages as secret and returns the status, the
// decoded SendResponse (zero on error) and the raw body.
func sendHTTP(t *testing.T, env *testhub.Env, secret string, body any) (int, hub.SendResponse, []byte) {
	t.Helper()
	resp, raw := env.Do(secret, "POST", "/v1/messages", body)
	var sr hub.SendResponse
	if resp.StatusCode/100 == 2 {
		if err := json.Unmarshal(raw, &sr); err != nil {
			t.Fatalf("decode SendResponse: %v; %s", err, raw)
		}
	}
	return resp.StatusCode, sr, raw
}

// mustSend posts body and fails unless the answer is 202.
func mustSend(t *testing.T, env *testhub.Env, secret string, body any) hub.SendResponse {
	t.Helper()
	status, sr, raw := sendHTTP(t, env, secret, body)
	if status != http.StatusAccepted {
		t.Fatalf("POST /v1/messages = %d, want 202; body %s", status, raw)
	}
	return sr
}

// deliveryEvents returns the raw data of every "delivery" Event in peer's log.
func deliveryEvents(t *testing.T, env *testhub.Env, peer string) []json.RawMessage {
	t.Helper()
	evs, err := env.Hub.Log.Read(context.Background(), []string{peer},
		eventlog.Cursor{Epoch: env.Hub.Log.Epoch(), Seq: 0}, 0)
	if err != nil {
		t.Fatalf("Log.Read(%s): %v", peer, err)
	}
	var out []json.RawMessage
	for _, ev := range evs {
		if ev.Type == "delivery" {
			out = append(out, ev.Data)
		}
	}
	return out
}

// deliveryData is the part of a delivery Event's data the tests inspect.
type deliveryData struct {
	DeliveryID string          `json:"delivery_id"`
	Recipient  string          `json:"recipient"`
	Via        *string         `json:"via"`
	AlsoVia    []string        `json:"also_via"`
	Ingress    string          `json:"ingress"`
	Thread     json.RawMessage `json:"thread"`
	Message    json.RawMessage `json:"message"`
}

func decodeDelivery(t *testing.T, raw json.RawMessage) (deliveryData, envelope.ThreadSummary, envelope.Message) {
	t.Helper()
	if err := envelope.Validate("delivery", raw); err != nil {
		t.Fatalf("delivery event data violates the schema: %v; %s", err, raw)
	}
	var d deliveryData
	if err := json.Unmarshal(raw, &d); err != nil {
		t.Fatalf("decode delivery: %v", err)
	}
	var ts envelope.ThreadSummary
	if err := json.Unmarshal(d.Thread, &ts); err != nil {
		t.Fatalf("decode thread: %v", err)
	}
	var m envelope.Message
	if err := json.Unmarshal(d.Message, &m); err != nil {
		t.Fatalf("decode message: %v", err)
	}
	return d, ts, m
}

func participants(t *testing.T, env *testhub.Env, thr string) map[string]bool {
	t.Helper()
	rows, err := env.Hub.DB.Query(`SELECT peer_id FROM thread_participants WHERE thr_id = ?`, thr)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			t.Fatal(err)
		}
		out[p] = true
	}
	return out
}

func count(t *testing.T, env *testhub.Env, q string, args ...any) int {
	t.Helper()
	var n int
	if err := env.Hub.DB.QueryRow(q, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return n
}

// addGroup inserts an ad hoc group with members directly (the groups API is
// Task 15).
func addGroup(t *testing.T, env *testhub.Env, creator string, members ...string) string {
	t.Helper()
	g := ids.New(ids.Grp)
	now := envelope.FormatTime(env.Now.Now())
	if _, err := env.Hub.DB.Exec(`INSERT INTO groups(grp_id, created_by, created_at, last_active_at) VALUES (?, ?, ?, ?)`,
		g, creator, now, now); err != nil {
		t.Fatal(err)
	}
	for _, m := range members {
		if _, err := env.Hub.DB.Exec(`INSERT INTO group_members(grp_id, peer_id) VALUES (?, ?)`, g, m); err != nil {
			t.Fatal(err)
		}
	}
	return g
}

func req(key string, recipients ...string) map[string]any {
	b := map[string]any{
		"idempotency_key": key,
		"priority":        "wake",
		"content":         map[string]any{"text": "hello " + key},
	}
	if len(recipients) > 0 {
		b["recipients"] = recipients
	}
	return b
}

func TestSendCreatesThreadAndDeliveries(t *testing.T) {
	env := testhub.Start(t)
	a, aSec := env.NewPeer("session", "A")
	b, _ := env.NewPeer("session", "B")
	c, _ := env.NewPeer("session", "C")

	sr := mustSend(t, env, aSec, req("k1", b, c))
	if !ids.Is(ids.Msg, sr.MessageID) || !ids.Is(ids.Thr, sr.ThreadID) {
		t.Fatalf("ids: message %q thread %q", sr.MessageID, sr.ThreadID)
	}
	if sr.Spilled {
		t.Fatalf("spilled = true for a short text")
	}
	if got := participants(t, env, sr.ThreadID); len(got) != 3 || !got[a] || !got[b] || !got[c] {
		t.Fatalf("participants = %v, want {A,B,C}", got)
	}
	if len(sr.Deliveries) != 2 {
		t.Fatalf("deliveries = %+v, want 2", sr.Deliveries)
	}
	seen := map[string]bool{}
	for _, d := range sr.Deliveries {
		if d.State != "queued" || d.Via != nil || !ids.Is(ids.Dly, d.DeliveryID) {
			t.Fatalf("delivery ref %+v, want queued, via nil, dly_ id", d)
		}
		seen[d.Recipient] = true
	}
	if !seen[b] || !seen[c] {
		t.Fatalf("delivery recipients = %v, want B and C", seen)
	}
	if n := count(t, env, `SELECT count(*) FROM deliveries WHERE msg_id = ? AND state = 'queued'`, sr.MessageID); n != 2 {
		t.Fatalf("queued delivery rows = %d, want 2", n)
	}

	evs := deliveryEvents(t, env, b)
	if len(evs) != 1 {
		t.Fatalf("B's log has %d delivery events, want 1", len(evs))
	}
	d, ts, m := decodeDelivery(t, evs[0])
	if m.Sender != a {
		t.Fatalf("data.message.sender = %q, want A %q", m.Sender, a)
	}
	if ts.BudgetRemaining == nil || *ts.BudgetRemaining != 499 {
		t.Fatalf("data.thread.budget_remaining = %v, want 499", ts.BudgetRemaining)
	}
	if ts.MessageCount != 1 || ts.HistoryAccess != "from_join" || ts.ThreadID != sr.ThreadID {
		t.Fatalf("thread summary = %+v", ts)
	}
	if d.Recipient != b || d.Ingress != "http_send" || m.MessageID != sr.MessageID {
		t.Fatalf("delivery = %+v message id %q", d, m.MessageID)
	}
	if n := count(t, env, `SELECT count(*) FROM audit WHERE action = 'send' AND actor = ?`, a); n != 1 {
		t.Fatalf("send audit rows = %d, want 1", n)
	}
	var evSeq int64
	if err := env.Hub.DB.QueryRow(`SELECT event_seq FROM deliveries WHERE recipient = ?`, b).Scan(&evSeq); err != nil || evSeq <= 0 {
		t.Fatalf("deliveries.event_seq = %d, %v; want > 0", evSeq, err)
	}
}

func TestMessageBytesIdenticalAcrossDeliveries(t *testing.T) {
	env := testhub.Start(t)
	_, aSec := env.NewPeer("session", "A")
	b, _ := env.NewPeer("session", "B")
	c, _ := env.NewPeer("session", "C")
	body := req("k1", b, c)
	body["metadata"] = map[string]any{"z": 1, "a": []any{"x", 2.5}, "m": map[string]any{"q": true}}
	sr := mustSend(t, env, aSec, body)

	eb, ec := deliveryEvents(t, env, b), deliveryEvents(t, env, c)
	if len(eb) != 1 || len(ec) != 1 {
		t.Fatalf("events: B %d, C %d; want 1 each", len(eb), len(ec))
	}
	db, _, _ := decodeDelivery(t, eb[0])
	dc, _, _ := decodeDelivery(t, ec[0])
	if !bytes.Equal(db.Message, dc.Message) {
		t.Fatalf("message bytes differ:\nB %s\nC %s", db.Message, dc.Message)
	}
	var stored string
	if err := env.Hub.DB.QueryRow(`SELECT body FROM messages WHERE msg_id = ?`, sr.MessageID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal([]byte(stored), db.Message) {
		t.Fatalf("stored message bytes differ from the delivered ones:\nstored %s\nB      %s", stored, db.Message)
	}
}

func TestReplyDefaultsRecipientsToParticipants(t *testing.T) {
	env := testhub.Start(t)
	a, aSec := env.NewPeer("session", "A")
	b, bSec := env.NewPeer("session", "B")
	c, _ := env.NewPeer("session", "C")
	first := mustSend(t, env, aSec, req("k1", b, c))

	reply := req("r1")
	reply["thread_id"] = first.ThreadID
	reply["in_reply_to"] = first.MessageID
	sr := mustSend(t, env, bSec, reply)
	if sr.ThreadID != first.ThreadID {
		t.Fatalf("reply thread = %s, want %s", sr.ThreadID, first.ThreadID)
	}
	got := map[string]bool{}
	for _, d := range sr.Deliveries {
		got[d.Recipient] = true
	}
	if len(sr.Deliveries) != 2 || !got[a] || !got[c] || got[b] {
		t.Fatalf("reply deliveries = %+v, want A and C only", sr.Deliveries)
	}
	if n := len(deliveryEvents(t, env, b)); n != 1 {
		t.Fatalf("B's log has %d deliveries, want 1 (only the first message)", n)
	}
	evs := deliveryEvents(t, env, a)
	if len(evs) != 1 {
		t.Fatalf("A's log has %d deliveries, want 1", len(evs))
	}
	_, ts, m := decodeDelivery(t, evs[0])
	if m.InReplyTo == nil || *m.InReplyTo != first.MessageID {
		t.Fatalf("in_reply_to = %v, want %s", m.InReplyTo, first.MessageID)
	}
	if len(m.Recipients) != 2 {
		t.Fatalf("message.recipients = %v, want the two other participants", m.Recipients)
	}
	if ts.MessageCount != 2 || ts.BudgetRemaining == nil || *ts.BudgetRemaining != 498 {
		t.Fatalf("thread after reply = %+v", ts)
	}
}

func TestGroupFanOutViaAndAlsoVia(t *testing.T) {
	env := testhub.Start(t)
	a, aSec := env.NewPeer("session", "A")
	b, _ := env.NewPeer("session", "B")
	c, _ := env.NewPeer("session", "C")
	g1 := addGroup(t, env, a, a, b, c)
	g2 := addGroup(t, env, a, c)

	sr := mustSend(t, env, aSec, req("k1", g1, g2))
	if len(sr.Deliveries) != 2 {
		t.Fatalf("deliveries = %+v, want one per peer (B, C; not the sender)", sr.Deliveries)
	}
	for _, d := range sr.Deliveries {
		if d.Via == nil || *d.Via != g1 {
			t.Fatalf("delivery %+v: via = %v, want %s", d, d.Via, g1)
		}
		if d.Recipient == a {
			t.Fatalf("the sender got a delivery through its own group")
		}
	}
	if n := count(t, env, `SELECT count(*) FROM deliveries WHERE msg_id = ?`, sr.MessageID); n != 2 {
		t.Fatalf("delivery rows = %d, want 2", n)
	}

	ec := deliveryEvents(t, env, c)
	if len(ec) != 1 {
		t.Fatalf("C has %d deliveries, want 1", len(ec))
	}
	dc, ts, _ := decodeDelivery(t, ec[0])
	if dc.Via == nil || *dc.Via != g1 || len(dc.AlsoVia) != 1 || dc.AlsoVia[0] != g2 {
		t.Fatalf("C: via %v also_via %v, want %s / [%s]", dc.Via, dc.AlsoVia, g1, g2)
	}
	if ts.Group == nil || *ts.Group != g1 {
		t.Fatalf("thread.group = %v, want first recipient group %s", ts.Group, g1)
	}
	eb := deliveryEvents(t, env, b)
	if len(eb) != 1 {
		t.Fatalf("B has %d deliveries, want 1", len(eb))
	}
	db, _, _ := decodeDelivery(t, eb[0])
	if db.Via == nil || *db.Via != g1 || len(db.AlsoVia) != 0 {
		t.Fatalf("B: via %v also_via %v, want %s / none", db.Via, db.AlsoVia, g1)
	}
	if got := participants(t, env, sr.ThreadID); len(got) != 3 || !got[a] || !got[b] || !got[c] {
		t.Fatalf("participants = %v, want {A,B,C}", got)
	}
}

func TestIdempotencyRace(t *testing.T) {
	env := testhub.Start(t)
	a, aSec := env.NewPeer("session", "A")
	b, _ := env.NewPeer("session", "B")
	body, err := json.Marshal(req("race-1", b))
	if err != nil {
		t.Fatal(err)
	}

	const n = 8
	var wg sync.WaitGroup
	type result struct {
		status int
		sr     hub.SendResponse
		raw    []byte
	}
	results := make([]result, n)
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			s, sr, raw := sendHTTP(t, env, aSec, body)
			results[i] = result{s, sr, raw}
		}(i)
	}
	close(start)
	wg.Wait()

	accepted := 0
	for i, r := range results {
		if r.status != http.StatusAccepted && r.status != http.StatusOK {
			t.Fatalf("send %d = %d, want 2xx; %s", i, r.status, r.raw)
		}
		if r.status == http.StatusAccepted {
			accepted++
		}
		if r.sr.MessageID != results[0].sr.MessageID || r.sr.MessageID == "" {
			t.Fatalf("send %d message_id %q, send 0 %q: want identical", i, r.sr.MessageID, results[0].sr.MessageID)
		}
	}
	if accepted != 1 {
		t.Fatalf("%d sends answered 202, want exactly 1 (the rest 200 replays)", accepted)
	}
	if m := count(t, env, `SELECT count(*) FROM messages WHERE sender = ?`, a); m != 1 {
		t.Fatalf("message rows = %d, want 1", m)
	}
	if d := count(t, env, `SELECT count(*) FROM deliveries`); d != 1 {
		t.Fatalf("delivery rows = %d, want 1", d)
	}
	if e := len(deliveryEvents(t, env, b)); e != 1 {
		t.Fatalf("B's delivery events = %d, want 1", e)
	}
}

// TestSendDirectIngress exercises Hub.Send without HTTP: a connector's origin
// is kept and the replay flag is reported.
func TestSendDirectIngress(t *testing.T) {
	env := testhub.Start(t)
	_, aSec := env.NewPeer("openclaw", "A")
	b, _ := env.NewPeer("session", "B")
	c, err := env.Hub.Auth.Resolve(context.Background(), aSec)
	if err != nil {
		t.Fatal(err)
	}
	body := req("conn-1", b)
	body["origin"] = map[string]any{"system": "github", "thread_id": "o/r#1"}
	raw, _ := json.Marshal(body)
	sr, replayed, err := env.Hub.Send(context.Background(), c, raw, "connector")
	if err != nil || replayed {
		t.Fatalf("Send = %v, replayed %v", err, replayed)
	}
	evs := deliveryEvents(t, env, b)
	if len(evs) != 1 {
		t.Fatalf("B has %d deliveries", len(evs))
	}
	d, _, m := decodeDelivery(t, evs[0])
	if m.Origin == nil || m.Origin.System != "github" || m.Origin.ThreadID != "o/r#1" || d.Ingress != "connector" {
		t.Fatalf("connector origin/ingress lost: origin %+v ingress %q", m.Origin, d.Ingress)
	}
	sr2, replayed, err := env.Hub.Send(context.Background(), c, raw, "connector")
	if err != nil || !replayed || sr2.MessageID != sr.MessageID {
		t.Fatalf("replay = %+v, %v, %v; want same message, replayed", sr2, replayed, err)
	}
}

func TestThreadFullAcrossSends(t *testing.T) {
	env := testhub.Start(t)
	_, aSec := env.NewPeer("session", "A")
	var many []string
	for i := 0; i < 62; i++ {
		p, _ := env.NewPeer("session", fmt.Sprintf("P%02d", i))
		many = append(many, p)
	}
	b, _ := env.NewPeer("session", "B")
	extra, _ := env.NewPeer("session", "X")
	g := addGroup(t, env, b, many...)

	// A + B + 62 group members = 64 participants: at the cap, accepted.
	sr := mustSend(t, env, aSec, req("k1", b, g))
	if got := len(participants(t, env, sr.ThreadID)); got != 64 {
		t.Fatalf("participants = %d, want 64", got)
	}
	add := req("k2", extra)
	add["thread_id"] = sr.ThreadID
	status, _, raw := sendHTTP(t, env, aSec, add)
	if status != http.StatusUnprocessableEntity || !bytes.Contains(raw, []byte(`"thread_full"`)) {
		t.Fatalf("65th participant = %d %s, want 422 thread_full", status, raw)
	}
}

// TestCreatorSetsHistoryAccessAndLock: a send that creates a thread may set
// history_access and locked_participants (PROTOCOL §16.3); a later send
// into the thread cannot change them.
func TestCreatorSetsHistoryAccessAndLock(t *testing.T) {
	env := testhub.Start(t)
	_, aSec := env.NewPeer("session", "A")
	b, bSec := env.NewPeer("session", "B")
	c, _ := env.NewPeer("session", "C")

	threadFlags := func(thr string) (string, int) {
		t.Helper()
		var ha string
		var locked int
		if err := env.Hub.DB.QueryRow(`SELECT history_access, locked_participants FROM threads WHERE thr_id = ?`, thr).
			Scan(&ha, &locked); err != nil {
			t.Fatal(err)
		}
		return ha, locked
	}

	plain := mustSend(t, env, aSec, req("h0", b))
	if ha, locked := threadFlags(plain.ThreadID); ha != "from_join" || locked != 0 {
		t.Fatalf("default thread = %s locked %d, want from_join unlocked", ha, locked)
	}

	body := req("h1", b)
	body["history_access"] = "full"
	body["locked_participants"] = true
	sr := mustSend(t, env, aSec, body)
	if ha, locked := threadFlags(sr.ThreadID); ha != "full" || locked != 1 {
		t.Fatalf("created thread = %s locked %d, want full locked", ha, locked)
	}

	// Ignored on a send into an existing thread.
	again := req("h2")
	again["thread_id"] = plain.ThreadID
	again["history_access"] = "full"
	again["locked_participants"] = true
	mustSend(t, env, bSec, again)
	if ha, locked := threadFlags(plain.ThreadID); ha != "from_join" || locked != 0 {
		t.Fatalf("existing thread after send = %s locked %d, want unchanged", ha, locked)
	}

	// The lock holds: B, not the creator, cannot add C.
	add := req("h3", c)
	add["thread_id"] = sr.ThreadID
	if status, _, raw := sendHTTP(t, env, bSec, add); status != http.StatusForbidden || !bytes.Contains(raw, []byte("participants_locked")) {
		t.Fatalf("non-creator add to a locked thread = %d %s, want 403 participants_locked", status, raw)
	}
}

// TestDefaultedRecipientsCappedAt32: a reply-all in a thread of more than
// 33 participants delivers to every participant, but Message.recipients
// stays within $defs/message's 32, so every Delivery is schema-valid.
func TestDefaultedRecipientsCappedAt32(t *testing.T) {
	env := testhub.Start(t)
	_, aSec := env.NewPeer("session", "A")
	var peers []string
	for i := 0; i < 39; i++ {
		p, _ := env.NewPeer("session", fmt.Sprintf("p%d", i))
		peers = append(peers, p)
	}
	first := mustSend(t, env, aSec, req("cap0", peers[:32]...))
	more := req("cap1", peers[32:]...)
	more["thread_id"] = first.ThreadID
	mustSend(t, env, aSec, more)

	// A reply-all from A defaults to the 39 other participants.
	all := req("cap2")
	all["thread_id"] = first.ThreadID
	sr := mustSend(t, env, aSec, all)
	if len(sr.Deliveries) != 39 {
		t.Fatalf("reply-all deliveries = %d, want 39", len(sr.Deliveries))
	}
	for _, p := range peers {
		evs := deliveryEvents(t, env, p)
		d := evs[len(evs)-1]
		if err := envelope.Validate("delivery", d); err != nil {
			t.Fatalf("delivery to %s is schema-invalid: %v", p, err)
		}
	}
}

// TestGroupFanOutAppliesMemberScopes: a group or team address is checked
// per member as if each were addressed directly (PROTOCOL §7, §12): a deny
// on a member refuses the send (403 scope_denied naming the member), and
// allow-list scopes admit a group whose members are all allowed, or a
// group or team that is itself allowed.
func TestGroupFanOutAppliesMemberScopes(t *testing.T) {
	env := testhub.Start(t)
	ctx := context.Background()
	a, aSec := env.NewPeer("session", "A")
	b, _ := env.NewPeer("session", "B")
	x, _ := env.NewPeer("session", "X")
	y, _ := env.NewPeer("session", "Y")
	opCaller := auth.Caller{Kind: "operator", PeerID: env.OperatorPeer}
	if err := env.Hub.DB.QueryRow(`SELECT prn_id FROM peers WHERE peer_id = ?`, env.OperatorPeer).Scan(&opCaller.PrincipalID); err != nil {
		t.Fatal(err)
	}

	// The operator builds a group {A, X, B}; A is denied X.
	grp, err := env.Hub.CreateGroup(ctx, opCaller, "", []string{a, x, b})
	if err != nil {
		t.Fatal(err)
	}
	if err := env.Hub.SetScopes(ctx, a, nil, []string{x}); err != nil {
		t.Fatal(err)
	}
	status, _, raw := sendHTTP(t, env, aSec, req("gs1", grp))
	if status != http.StatusForbidden || !bytes.Contains(raw, []byte("scope_denied")) || !bytes.Contains(raw, []byte(x)) {
		t.Fatalf("send to a group holding a denied peer = %d %s, want 403 scope_denied naming %s", status, raw, x)
	}
	if n := count(t, env, `SELECT count(*) FROM deliveries WHERE recipient = ?`, x); n != 0 {
		t.Fatalf("deliveries to the denied peer = %d, want 0", n)
	}

	// Allow-list scopes: A may message X and Y only. A group of exactly
	// those is sendable; adding B (not allowed) makes it refused.
	if err := env.Hub.SetScopes(ctx, a, []string{x, y}, nil); err != nil {
		t.Fatal(err)
	}
	aCaller := auth.Caller{Kind: "peer", PeerID: a}
	if err := env.Hub.DB.QueryRow(`SELECT prn_id FROM peers WHERE peer_id = ?`, a).Scan(&aCaller.PrincipalID); err != nil {
		t.Fatal(err)
	}
	g2, err := env.Hub.CreateGroup(ctx, aCaller, "", []string{x, y})
	if err != nil {
		t.Fatal(err)
	}
	if sr := mustSend(t, env, aSec, req("gs2", g2)); len(sr.Deliveries) != 2 {
		t.Fatalf("send to an all-allowed group = %d deliveries, want 2", len(sr.Deliveries))
	}
	if err := env.Hub.AddGroupMembers(ctx, opCaller, "", g2, []string{b}); err != nil {
		t.Fatal(err)
	}
	status, _, raw = sendHTTP(t, env, aSec, req("gs3", g2))
	if status != http.StatusForbidden || !bytes.Contains(raw, []byte(b)) {
		t.Fatalf("send to a group with a non-allowed member = %d %s, want 403 scope_denied naming %s", status, raw, b)
	}
	// Allowing the group itself admits its members (except explicit denies).
	if err := env.Hub.SetScopes(ctx, a, []string{x, y, g2}, nil); err != nil {
		t.Fatal(err)
	}
	if sr := mustSend(t, env, aSec, req("gs4", g2)); len(sr.Deliveries) != 3 {
		t.Fatalf("send to an allowed group = %d deliveries, want 3", len(sr.Deliveries))
	}
}
