package hub_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/eventlog"
	"github.com/XertroV/vecnet/internal/events"
	"github.com/XertroV/vecnet/internal/hub"
	"github.com/XertroV/vecnet/internal/opapi"
	"github.com/XertroV/vecnet/internal/testhub"
	"github.com/XertroV/vecnet/internal/webhook"
)

func reactBody(msg, emoji, op string) map[string]any {
	return map[string]any{"message_id": msg, "emoji": emoji, "op": op}
}
func react(t *testing.T, env *testhub.Env, secret, msg, emoji, op string) opapi.ReactionResult {
	t.Helper()
	r, raw := env.Do(secret, "POST", "/v1/reactions", reactBody(msg, emoji, op))
	if r.StatusCode != 200 {
		t.Fatalf("react = %d: %s", r.StatusCode, raw)
	}
	var out opapi.ReactionResult
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	if out.MessageID != msg || out.Op != op || out.Reactions == nil || out.Superseded == nil || len(out.Superseded) != 0 || out.Wake != nil {
		t.Fatalf("reaction response: %+v", out)
	}
	return out
}
func rejectReact(t *testing.T, env *testhub.Env, secret string, body any, status int, code string) map[string]any {
	t.Helper()
	r, raw := env.Do(secret, "POST", "/v1/reactions", body)
	got, details := apiError(t, raw)
	if r.StatusCode != status || got != code {
		t.Fatalf("react = %d %s, want %d %s", r.StatusCode, raw, status, code)
	}
	return details
}
func reactionEvents(t *testing.T, env *testhub.Env, peer, typ string) []envelope.Event {
	t.Helper()
	evs, err := env.Hub.Log.Read(context.Background(), []string{peer}, eventlog.Cursor{Epoch: env.Hub.Log.Epoch()}, 0)
	if err != nil {
		t.Fatal(err)
	}
	var out []envelope.Event
	for _, ev := range evs {
		if ev.Type == typ {
			out = append(out, ev)
		}
	}
	return out
}
func TestReactAddRemoveIdempotent(t *testing.T) {
	env := testhub.Start(t)
	a, as := env.NewPeer("session", "A")
	b, bs := env.NewPeer("session", "B")
	m := mustSend(t, env, as, req("start", b))
	for i, op := range []string{"add", "add", "remove", "remove"} {
		out := react(t, env, bs, m.MessageID, "✅", op)
		if out.Changed != (i%2 == 0) || out.ThreadID != m.ThreadID || out.Emoji != "✅" {
			t.Fatalf("step %d: %+v", i, out)
		}
		want := 1
		if i >= 2 {
			want = 0
		}
		if n := count(t, env, `SELECT count(*) FROM reactions WHERE msg_id=?`, m.MessageID); n != want {
			t.Fatalf("rows=%d want %d", n, want)
		}
		if len(out.Reactions) != want {
			t.Fatalf("aggregate=%+v", out.Reactions)
		}
		evs := 1
		if i >= 2 {
			evs = 2
		}
		for _, p := range []string{a, b} {
			if n := len(reactionEvents(t, env, p, "reaction")); n != evs {
				t.Fatalf("events=%d want %d", n, evs)
			}
		}
	}
	if n := count(t, env, `SELECT count(*) FROM reaction_ops`); n != 2 {
		t.Fatalf("ops=%d", n)
	}
	if n := count(t, env, `SELECT count(*) FROM audit WHERE action='react' AND actor=?`, b); n != 2 {
		t.Fatalf("audit=%d", n)
	}
}
func TestReactVisibility(t *testing.T) {
	env := testhub.Start(t)
	_, as := env.NewPeer("session", "A")
	b, _ := env.NewPeer("session", "B")
	late, ls := env.NewPeer("session", "late")
	_, os := env.NewPeer("session", "outsider")
	m := mustSend(t, env, as, req("early", b))
	rejectReact(t, env, os, reactBody(m.MessageID, "✅", "add"), 404, "not_found")
	body := req("later", late)
	body["thread_id"] = m.ThreadID
	later := mustSend(t, env, as, body)
	rejectReact(t, env, ls, reactBody(m.MessageID, "✅", "add"), 404, "not_found")
	if n := count(t, env, `SELECT count(*) FROM reactions`); n != 0 {
		t.Fatalf("invisible writes=%d", n)
	}
	if !react(t, env, ls, later.MessageID, "✅", "add").Changed {
		t.Fatal("visible add unchanged")
	}
	if _, err := env.Hub.DB.Exec(`UPDATE threads SET history_access='full' WHERE thr_id=?`, m.ThreadID); err != nil {
		t.Fatal(err)
	}
	if !react(t, env, ls, m.MessageID, "✅", "add").Changed {
		t.Fatal("full history unchanged")
	}
	if !react(t, env, env.OperatorSecret, m.MessageID, "✅", "add").Changed {
		t.Fatal("operator unchanged")
	}
	if participants(t, env, m.ThreadID)[env.OperatorPeer] {
		t.Fatal("react added operator participant")
	}
	d := rejectReact(t, env, as, reactBody("msg_00000000000000000000000000", "✅", "add"), 404, "not_found")
	if d["message_id"] != "msg_00000000000000000000000000" {
		t.Fatalf("details=%v", d)
	}
}
func TestReactEmojiNotAllowed(t *testing.T) {
	env := testhub.Start(t)
	_, as := env.NewPeer("session", "A")
	b, bs := env.NewPeer("session", "B")
	m := mustSend(t, env, as, req("emoji", b))
	for _, s := range []string{"🙂", "👍🏽", "check", "✅́", "\u202e✅"} {
		d := rejectReact(t, env, bs, reactBody(m.MessageID, s, "add"), 422, "emoji_not_allowed")
		if d["emoji"] != s {
			t.Fatalf("details=%v", d)
		}
	}
	out := react(t, env, bs, m.MessageID, "❤", "add")
	if out.Emoji != "❤️" || len(out.Reactions) != 1 || out.Reactions[0].Emoji != "❤️" {
		t.Fatalf("canonical=%+v", out)
	}
	if n := count(t, env, `SELECT count(*) FROM reactions WHERE emoji=?`, "❤️"); n != 1 {
		t.Fatalf("canonical rows=%d", n)
	}
}
func TestReactBadOpAndPriority(t *testing.T) {
	env := testhub.Start(t)
	_, as := env.NewPeer("session", "A")
	b, bs := env.NewPeer("session", "B")
	m := mustSend(t, env, as, req("bad", b))
	c, err := env.Hub.Auth.Resolve(context.Background(), bs)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"op", "priority"} {
		q := envelope.ReactionRequest{MessageID: m.MessageID, Emoji: "✅", Op: "add"}
		body := reactBody(m.MessageID, "✅", "add")
		if field == "op" {
			q.Op = "toggle"
			body[field] = "toggle"
		} else {
			q.Priority = "now"
			body[field] = "now"
		}
		_, err := env.Hub.React(context.Background(), c, q, "http_send")
		if err == nil {
			t.Fatalf("direct %s accepted", field)
		}
		d := rejectReact(t, env, bs, body, 422, "invalid_value")
		if d["field"] != field {
			t.Fatalf("details=%v", d)
		}
		if e, ok := err.(*hub.APIError); !ok || e.Status != 422 || e.Code != "invalid_value" || e.Details["field"] != field {
			t.Fatalf("direct error=%+v", err)
		}
	}
}
func TestReactAsPrincipal(t *testing.T) {
	env := testhub.Start(t)
	_, as := env.NewPeer("session", "A")
	b, _ := env.NewPeer("session", "B")
	_, ps := env.NewPrincipal("P")
	m := mustSend(t, env, as, req("principal", b))
	rejectReact(t, env, ps, reactBody(m.MessageID, "✅", "add"), 422, "as_required")
	body := reactBody(m.MessageID, "✅", "add")
	body["as"] = b
	rejectReact(t, env, ps, body, 403, "forbidden")
}
func TestReactSharesSendLimit(t *testing.T) {
	env := testhub.Start(t)
	a, as := env.NewPeer("session", "A")
	b, _ := env.NewPeer("session", "B")
	env.SetLimits(a, 3, 20)
	m := mustSend(t, env, as, req("one", b))
	mustSend(t, env, as, req("two", b))
	react(t, env, as, m.MessageID, "✅", "add")
	before := count(t, env, `SELECT count(*) FROM events`)
	d := rejectReact(t, env, as, reactBody(m.MessageID, "❓", "add"), 429, "rate_limited")
	if d["limit"] != "sends_per_min" {
		t.Fatalf("details=%v", d)
	}
	mustReject(t, env, as, req("three", b), 429, "rate_limited")
	if react(t, env, as, m.MessageID, "✅", "add").Changed {
		t.Fatal("no-op changed")
	}
	if n := count(t, env, `SELECT count(*) FROM reaction_ops`); n != 1 {
		t.Fatalf("ops=%d", n)
	}
	if n := count(t, env, `SELECT count(*) FROM reactions`); n != 1 {
		t.Fatalf("reactions=%d", n)
	}
	if n := count(t, env, `SELECT count(*) FROM events`); n != before {
		t.Fatalf("cut/no-op appended events: %d -> %d", before, n)
	}
	if n := count(t, env, `SELECT count(*) FROM audit WHERE action='rate_limited'`); n != 2 {
		t.Fatalf("rate audits=%d", n)
	}
}
func TestReactionEventFanOut(t *testing.T) {
	env := testhub.Start(t)
	a, as := env.NewPeer("session", "A")
	b, bs := env.NewPeer("session", "B")
	late, _ := env.NewPeer("session", "late")
	outside, _ := env.NewPeer("session", "outside")
	m := mustSend(t, env, as, req("early", b))
	body := req("late", late)
	body["thread_id"] = m.ThreadID
	mustSend(t, env, as, body)
	react(t, env, bs, m.MessageID, "✅", "add")
	for _, p := range []string{a, b, late, outside} {
		evs := reactionEvents(t, env, p, "reaction")
		want := 0
		if p == a || p == b {
			want = 1
		}
		if len(evs) != want {
			t.Fatalf("peer %s got %d, want %d", p, len(evs), want)
		}
		for _, ev := range evs {
			if err := envelope.Validate("reactionEvent", ev.Data); err != nil {
				t.Fatal(err)
			}
			var data envelope.ReactionEvent
			if err := json.Unmarshal(ev.Data, &data); err != nil {
				t.Fatal(err)
			}
			if data != (envelope.ReactionEvent{MessageID: m.MessageID, ThreadID: m.ThreadID, PeerID: b, Emoji: "✅", Op: "add", At: envelope.FormatTime(env.Now.Now())}) {
				t.Fatalf("event=%+v", data)
			}
		}
	}
	// A non-participant operator reactor receives the event itself exactly once.
	react(t, env, env.OperatorSecret, m.MessageID, "❓", "add")
	if n := len(reactionEvents(t, env, env.OperatorPeer, "reaction")); n != 1 {
		t.Fatalf("reactor events=%d", n)
	}
}
func TestReactionWakesLongPoll(t *testing.T) {
	env := testhub.Start(t)
	a, as := env.NewPeer("session", "A")
	b, bs := env.NewPeer("session", "B")
	m := mustSend(t, env, as, req("poll", b))
	cur, err := env.Hub.Log.Current(context.Background(), []string{a})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	r, err := http.NewRequestWithContext(ctx, "GET", env.URL+"/v1/events?wait=25&after="+url.QueryEscape(cur.String()), nil)
	if err != nil {
		t.Fatal(err)
	}
	r.Header.Set("Authorization", "Bearer "+as)
	r.Header.Set("Accept", "application/json")
	type result struct {
		raw    []byte
		err    error
		status int
	}
	ch := make(chan result, 1)
	go func() {
		resp, err := http.DefaultClient.Do(r)
		if err != nil {
			ch <- result{err: err}
			return
		}
		defer resp.Body.Close()
		raw, err := io.ReadAll(resp.Body)
		ch <- result{raw, err, resp.StatusCode}
	}()
	// Wait for the HTTP handler to register before appending the reaction.
	ready := time.NewTimer(time.Second)
	defer ready.Stop()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for env.Hub.Log.Waiters(a) == 0 {
		select {
		case got := <-ch:
			t.Fatalf("poll did not park: %+v", got)
		case <-ready.C:
			t.Fatal("long poll did not register a log waiter")
		case <-tick.C:
		}
	}
	// The handler re-reads after registering, so it can observe the reaction
	// even without Notify. This separate waiter proves Notify itself ran;
	// its context cannot close the channel until test cleanup.
	waitCtx, cancelWait := context.WithCancel(context.Background())
	t.Cleanup(cancelWait)
	notified := env.Hub.Log.Wait(waitCtx, []string{a})
	start := time.Now()
	react(t, env, bs, m.MessageID, "✅", "add")
	select {
	case <-notified:
	default:
		t.Fatal("reaction did not notify the registered log waiter")
	}
	select {
	case got := <-ch:
		var p struct {
			Events []envelope.Event `json:"events"`
		}
		if got.err != nil || got.status != 200 || json.Unmarshal(got.raw, &p) != nil || len(p.Events) != 1 || p.Events[0].Type != "reaction" {
			t.Fatalf("poll=%+v %s", got, got.raw)
		}
		if time.Since(start) >= time.Second {
			t.Fatal("reaction notification took >=1s")
		}
	case <-time.After(time.Second):
		t.Fatal("reaction did not notify long poll")
	}
}
func TestReactionOperatorView(t *testing.T) {
	env := testhub.Start(t)
	_, as := env.NewPeer("session", "A")
	b, bs := env.NewPeer("session", "B")
	m := mustSend(t, env, as, req("op", b))
	before := len(reactionEvents(t, env, env.OperatorPeer, "operator_view"))
	react(t, env, bs, m.MessageID, "✅", "add")
	evs := reactionEvents(t, env, env.OperatorPeer, "operator_view")
	if len(evs) != before+1 {
		t.Fatalf("views=%d", len(evs))
	}
	var data struct{ Reason, ThreadID string }
	var top map[string]string
	if err := json.Unmarshal(evs[len(evs)-1].Data, &top); err != nil {
		t.Fatal(err)
	}
	data.Reason = top["reason"]
	data.ThreadID = top["thread_id"]
	if data.Reason != "reaction" || data.ThreadID != m.ThreadID {
		t.Fatalf("view=%v", top)
	}
	body := req("with-op", env.OperatorPeer)
	m2 := mustSend(t, env, as, body)
	before = len(reactionEvents(t, env, env.OperatorPeer, "operator_view"))
	react(t, env, as, m2.MessageID, "✅", "add")
	if len(reactionEvents(t, env, env.OperatorPeer, "operator_view")) != before || len(reactionEvents(t, env, env.OperatorPeer, "reaction")) != 1 {
		t.Fatal("participant operator did not get only reaction")
	}
}
func TestReactionEventNeverPushed(t *testing.T) {
	env := testhub.Start(t)
	ctx := context.Background()
	_, as := env.NewPeer("session", "A")
	b, bs := env.NewPeer("session", "B")
	dot, _ := env.NewPeer("dot", "dot")
	cb := testhub.NewCallback(t)
	dots := testhub.NewCallback(t)
	route, _, err := env.Hub.AddRoute(ctx, "peer", b, cb.URL)
	if err != nil {
		t.Fatal(err)
	}
	if err = env.Worker.Verify(ctx, route); err != nil {
		t.Fatal(err)
	}
	m := mustSend(t, env, as, req("push", b, dot))
	if _, err = env.Worker.Step(ctx); err != nil {
		t.Fatal(err)
	}
	if len(cb.Posts()) != 1 {
		t.Fatalf("baseline pushes=%d", len(cb.Posts()))
	}
	cur, err := env.Hub.Log.Current(ctx, []string{dot})
	if err != nil {
		t.Fatal(err)
	}
	sub, _, err := env.Events.Store.Upsert(ctx, events.Subscription{PrincipalKind: "vecnet", PrincipalID: dot, PeerID: dot, Name: events.Name, URL: dots.URL, Cursor: cur}, webhook.NewSecret(), 0, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if err = env.Events.Store.MarkVerified(ctx, sub.ID); err != nil {
		t.Fatal(err)
	}
	react(t, env, bs, m.MessageID, "✅", "add")
	if _, err = env.Worker.Step(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = env.Events.Step(ctx); err != nil {
		t.Fatal(err)
	}
	if len(cb.Posts()) != 1 || len(dots.Posts()) != 0 {
		t.Fatalf("reaction pushed: route=%d dots=%d", len(cb.Posts()), len(dots.Posts()))
	}
	if len(reactionEvents(t, env, dot, "reaction")) != 1 {
		t.Fatal("dot had no reaction to filter")
	}
}
func TestHistoryAggregatesReactions(t *testing.T) {
	env := testhub.Start(t)
	a, as := env.NewPeer("session", "A")
	b, bs := env.NewPeer("session", "B")
	m := mustSend(t, env, as, req("history", b))
	body := req("unreacted", b)
	body["thread_id"] = m.ThreadID
	mustSend(t, env, as, body)
	react(t, env, bs, m.MessageID, "✅", "add")
	env.Now.Advance(time.Millisecond)
	react(t, env, as, m.MessageID, "✅", "add")
	react(t, env, as, m.MessageID, "👍", "add")
	react(t, env, bs, m.MessageID, "❗", "add")
	r, raw := env.Do(bs, "GET", "/v1/threads/"+m.ThreadID+"/messages", nil)
	if r.StatusCode != 200 {
		t.Fatalf("history=%d %s", r.StatusCode, raw)
	}
	var page struct {
		Messages []json.RawMessage `json:"messages"`
	}
	if err := json.Unmarshal(raw, &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Messages) != 2 {
		t.Fatalf("history=%s", raw)
	}
	var first envelope.Message
	if err := json.Unmarshal(page.Messages[0], &first); err != nil {
		t.Fatal(err)
	}
	want := []envelope.ReactionGroup{{Emoji: "👍", Peers: []string{a}}, {Emoji: "✅", Peers: []string{b, a}}, {Emoji: "❗", Peers: []string{b}}}
	if !reflect.DeepEqual(first.Reactions, want) {
		t.Fatalf("groups=%+v want %+v", first.Reactions, want)
	}
	var second map[string]any
	if err := json.Unmarshal(page.Messages[1], &second); err != nil {
		t.Fatal(err)
	}
	if _, ok := second["reactions"]; ok {
		t.Fatal("empty reaction field present")
	}
	for _, d := range deliveryEvents(t, env, b) {
		var wire struct {
			Message map[string]any `json:"message"`
		}
		if err := json.Unmarshal(d, &wire); err != nil {
			t.Fatal(err)
		}
		if _, ok := wire.Message["reactions"]; ok {
			t.Fatal("delivery carries reactions")
		}
	}
}
func TestReactionOpsPruned(t *testing.T) {
	env := testhub.Start(t)
	_, as := env.NewPeer("session", "A")
	b, bs := env.NewPeer("session", "B")
	m := mustSend(t, env, as, req("prune", b))
	react(t, env, bs, m.MessageID, "✅", "add")
	env.Now.Advance(time.Hour)
	react(t, env, bs, m.MessageID, "❓", "add")
	env.Hub.Tick(context.Background(), true)
	if n := count(t, env, `SELECT count(*) FROM reaction_ops`); n != 2 {
		t.Fatalf("one-hour boundary count=%d", n)
	}
	env.Now.Advance(time.Millisecond)
	env.Hub.Tick(context.Background(), false)
	if n := count(t, env, `SELECT count(*) FROM reaction_ops`); n != 2 {
		t.Fatalf("fast tick count=%d", n)
	}
	env.Hub.Tick(context.Background(), true)
	if n := count(t, env, `SELECT count(*) FROM reaction_ops`); n != 1 {
		t.Fatalf("slow tick count=%d", n)
	}
}
func TestSendRefusesReactionKind(t *testing.T) {
	env := testhub.Start(t)
	_, as := env.NewPeer("session", "A")
	b, _ := env.NewPeer("session", "B")
	body := req("kind", b)
	body["kind"] = "reaction"
	_, d := mustReject(t, env, as, body, 400, "invalid_body")
	if d["path"] != "/kind" {
		t.Fatalf("details=%v", d)
	}
}

// rxFixture exercises the HTTP reaction boundary with a three-peer thread.
type rxFixture struct {
	env                 *testhub.Env
	a, as, b, bs, c, cs string
	target              hub.SendResponse
}

func newRX(t *testing.T) rxFixture {
	t.Helper()
	f := rxFixture{env: testhub.Start(t)}
	f.a, f.as = f.env.NewPeer("session", "A")
	f.b, f.bs = f.env.NewPeer("session", "B")
	f.c, f.cs = f.env.NewPeer("session", "C")
	f.target = mustSend(t, f.env, f.as, holdReq("target", f.b, f.c))
	return f
}
func rxBody(msg, emoji, op, priority string) map[string]any {
	b := reactBody(msg, emoji, op)
	if priority != "" {
		b["priority"] = priority
	}
	return b
}
func rx(t *testing.T, env *testhub.Env, secret, msg, emoji, op, priority string) hub.ReactResponse {
	t.Helper()
	r, raw := env.Do(secret, "POST", "/v1/reactions", rxBody(msg, emoji, op, priority))
	if r.StatusCode != 200 {
		t.Fatalf("reaction = %d: %s", r.StatusCode, raw)
	}
	var out hub.ReactResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	if out.Reactions == nil || out.Superseded == nil {
		t.Fatalf("null arrays: %s", raw)
	}
	return out
}
func needWake(t *testing.T, out hub.ReactResponse) *hub.ReactWake {
	t.Helper()
	if !out.Changed || out.Wake == nil {
		t.Fatalf("expected changed reaction with wake: %+v", out)
	}
	if out.Wake.DeliveryID == "" || out.Wake.MessageID == "" || out.Wake.State != "queued" {
		t.Fatalf("wake: %+v", out.Wake)
	}
	return out.Wake
}
func rxCount(t *testing.T, env *testhub.Env, want int) {
	t.Helper()
	if n := count(t, env, `SELECT count(*) FROM deliveries d JOIN messages m ON m.msg_id=d.msg_id WHERE m.kind='reaction'`); n != want {
		t.Fatalf("reaction deliveries=%d want %d", n, want)
	}
}
func rxRoute(t *testing.T, f rxFixture) (*testhub.Callback, string) {
	t.Helper()
	cb := testhub.NewCallback(t)
	route, secret, err := f.env.Hub.AddRoute(context.Background(), "peer", f.a, cb.URL)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.env.Worker.Verify(context.Background(), route); err != nil {
		t.Fatal(err)
	}
	return cb, secret
}
func rxStep(t *testing.T, env *testhub.Env) {
	t.Helper()
	if _, err := env.Worker.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
}
func rxAccept(t *testing.T, f rxFixture, w *hub.ReactWake) {
	t.Helper()
	cur, err := f.env.Hub.Log.Current(context.Background(), []string{f.a})
	if err != nil {
		t.Fatal(err)
	}
	r, raw := f.env.Do(f.as, "POST", "/v1/ack", map[string]string{"cursor": cur.String()})
	if r.StatusCode != 204 {
		t.Fatalf("ack=%d %s", r.StatusCode, raw)
	}
	if state, _ := deliveryState(t, f.env, w.DeliveryID); state != "accepted" {
		t.Fatalf("state=%s", state)
	}
}
func rxStored(t *testing.T, env *testhub.Env, id string) envelope.Message {
	t.Helper()
	var raw string
	if err := env.Hub.DB.QueryRow(`SELECT body FROM messages WHERE msg_id=?`, id).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var m envelope.Message
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatal(err)
	}
	return m
}
func TestReactionHoldCreatesNoDelivery(t *testing.T) {
	for _, p := range []string{"", "hold"} {
		t.Run(p, func(t *testing.T) {
			f := newRX(t)
			out := rx(t, f.env, f.bs, f.target.MessageID, "✅", "add", p)
			if !out.Changed || out.Wake != nil {
				t.Fatalf("hold=%+v", out)
			}
			for _, q := range []string{`SELECT count(*) FROM messages WHERE kind='reaction'`, `SELECT count(*) FROM reaction_wakes`, `SELECT count(*) FROM deliveries WHERE recipient=?`} {
				var args []any
				if strings.Contains(q, "recipient") {
					args = []any{f.a}
				}
				if n := count(t, f.env, q, args...); n != 0 {
					t.Fatalf("%s=%d", q, n)
				}
			}
			rxCount(t, f.env, 0)
		})
	}
}
func TestReactionWakeOneDeliveryToSender(t *testing.T) {
	f := newRX(t)
	w := needWake(t, rx(t, f.env, f.bs, f.target.MessageID, "✅", "add", "wake"))
	if w.Recipient != f.a {
		t.Fatalf("recipient=%s", w.Recipient)
	}
	rxCount(t, f.env, 1)
	evs := deliveryEvents(t, f.env, f.a)
	if len(evs) != 1 {
		t.Fatalf("deliveries to A=%d", len(evs))
	}
	if err := envelope.Validate("delivery", evs[0]); err != nil {
		t.Fatal(err)
	}
	var d envelope.Delivery
	if err := json.Unmarshal(evs[0], &d); err != nil {
		t.Fatal(err)
	}
	m := d.Message
	if d.DeliveryID != w.DeliveryID || m.MessageID != w.MessageID || m.Kind != "reaction" || m.InReplyTo == nil || *m.InReplyTo != f.target.MessageID || m.Reaction == nil || *m.Reaction != (envelope.ReactionRef{Target: f.target.MessageID, Emoji: "✅"}) || !reflect.DeepEqual(m.Recipients, []string{f.a}) || m.Sender != f.b || m.Content.Text != "✅" || m.Priority != "wake" || len(m.Attachments) != 0 || len(m.Metadata) != 0 {
		t.Fatalf("delivery=%+v message=%+v", d, m)
	}
	for _, p := range []string{f.b, f.c} {
		if n := count(t, f.env, `SELECT count(*) FROM deliveries WHERE msg_id=? AND recipient=?`, w.MessageID, p); n != 0 {
			t.Fatalf("delivery to %s", p)
		}
	}
}
func TestReactionWakePushedPerRoute(t *testing.T) {
	f := newRX(t)
	cb, secret := rxRoute(t, f)
	w := needWake(t, rx(t, f.env, f.bs, f.target.MessageID, "✅", "add", "wake"))
	rxStep(t, f.env)
	rxStep(t, f.env)
	posts := cb.Posts()
	if len(posts) != 1 {
		t.Fatalf("pushes=%d", len(posts))
	}
	p := posts[0]
	if err := webhook.Verify([]string{secret}, p.ID, p.Timestamp, p.Signature, p.Body, f.env.Now.Now()); err != nil {
		t.Fatal(err)
	}
	var d envelope.Delivery
	if err := json.Unmarshal(p.Body, &d); err != nil {
		t.Fatal(err)
	}
	if d.DeliveryID != w.DeliveryID || d.Message.MessageID != w.MessageID {
		t.Fatalf("push=%s", p.Body)
	}
	if !reflect.DeepEqual(p.Body, []byte(deliveryEvents(t, f.env, f.a)[0])) {
		t.Fatal("push differs from logged Delivery")
	}
}
func TestReactionWakeIdleHeldThenReleased(t *testing.T) {
	f := newRX(t)
	cb, _ := rxRoute(t, f)
	reportPresence(t, f.env, f.as, "", "working")
	w := needWake(t, rx(t, f.env, f.bs, f.target.MessageID, "✅", "add", "wake_idle"))
	if s, held := deliveryState(t, f.env, w.DeliveryID); s != "queued" || held != 1 {
		t.Fatalf("held=%s/%d", s, held)
	}
	rxStep(t, f.env)
	if len(cb.Posts()) != 0 {
		t.Fatal("pushed while working")
	}
	reportPresence(t, f.env, f.as, "", "idle")
	rxStep(t, f.env)
	rxStep(t, f.env)
	if len(cb.Posts()) != 1 {
		t.Fatalf("released pushes=%d", len(cb.Posts()))
	}
}
func TestReactionLoopFields(t *testing.T) {
	f := newRX(t)
	m := mustSend(t, f.env, f.as, replyReq("second", f.target.ThreadID, f.target.MessageID))
	w := needWake(t, rx(t, f.env, f.bs, m.MessageID, "✅", "add", "wake"))
	parent, got := storedLoop(t, f.env, m.MessageID), storedLoop(t, f.env, w.MessageID)
	if got.Depth != parent.Depth+1 || !reflect.DeepEqual(got.SenderChain, append(parent.SenderChain, f.b)) || got.MaxDepth != parent.MaxDepth || got.ExpiresAt != parent.ExpiresAt {
		t.Fatalf("loop=%+v parent=%+v", got, parent)
	}
}
func TestReactionRefusedPastMaxDepth(t *testing.T) {
	f := newRX(t)
	b := replyReq("max", f.target.ThreadID, f.target.MessageID)
	b["max_depth"] = 1
	target := mustSend(t, f.env, f.as, b)
	before := count(t, f.env, `SELECT count(*) FROM events`)
	rejectReact(t, f.env, f.bs, rxBody(target.MessageID, "✅", "add", "wake"), 422, "max_depth_exceeded")
	if count(t, f.env, `SELECT count(*) FROM reactions`) != 0 || count(t, f.env, `SELECT count(*) FROM reaction_ops`) != 0 || count(t, f.env, `SELECT count(*) FROM events`) != before || counter(t, f.env, "loop_cut") != 1 {
		t.Fatal("cut did not commit only its counter and audit")
	}
	if n := count(t, f.env, `SELECT count(*) FROM audit WHERE action='loop_cut' AND actor=? AND json_extract(detail,'$.via')='reaction' AND json_extract(detail,'$.reason')='max_depth_exceeded'`, f.b); n != 1 {
		t.Fatalf("cut audit=%d", n)
	}
	rxCount(t, f.env, 0)
}
func TestReactionRefusedAfterExpiry(t *testing.T) {
	f := newRX(t)
	f.env.Now.Advance(25 * time.Hour)
	rejectReact(t, f.env, f.bs, rxBody(f.target.MessageID, "✅", "add", "wake"), 422, "expired")
	if count(t, f.env, `SELECT count(*) FROM reactions`) != 0 {
		t.Fatal("expired changed reaction")
	}
	if !rx(t, f.env, f.bs, f.target.MessageID, "✅", "add", "hold").Changed {
		t.Fatal("hold refused")
	}
}
func TestNoopRetryAfterExpiryIs200(t *testing.T) {
	f := newRX(t)
	needWake(t, rx(t, f.env, f.bs, f.target.MessageID, "✅", "add", "wake"))
	f.env.Now.Advance(25 * time.Hour)
	out := rx(t, f.env, f.bs, f.target.MessageID, "✅", "add", "wake")
	if out.Changed || out.Wake != nil || len(out.Superseded) != 0 {
		t.Fatalf("retry=%+v", out)
	}
	rxCount(t, f.env, 1)
}
func TestOperatorReactionNeverCut(t *testing.T) {
	f := newRX(t)
	b := replyReq("max", f.target.ThreadID, f.target.MessageID)
	b["max_depth"] = 1
	m := mustSend(t, f.env, f.as, b)
	f.env.Now.Advance(25 * time.Hour)
	w := needWake(t, rx(t, f.env, f.env.OperatorSecret, m.MessageID, "✅", "add", "wake"))
	l := storedLoop(t, f.env, w.MessageID)
	exp, err := envelope.ParseTime(l.ExpiresAt)
	if err != nil || l.Depth != 0 || !exp.Equal(f.env.Now.Now().Add(24*time.Hour)) || !reflect.DeepEqual(l.SenderChain, []string{f.a, f.a, f.env.OperatorPeer}) {
		t.Fatalf("operator loop=%+v err=%v", l, err)
	}
	if participants(t, f.env, m.ThreadID)[f.env.OperatorPeer] {
		t.Fatal("operator became participant")
	}
	rxCount(t, f.env, 1)
}
func TestReactionNoBudgetCost(t *testing.T) {
	f := newRX(t)
	if _, err := f.env.Hub.DB.Exec(`UPDATE threads SET budget_remaining=0 WHERE thr_id=?`, f.target.ThreadID); err != nil {
		t.Fatal(err)
	}
	var before, after string
	snapshot := func(out *string) {
		t.Helper()
		if err := f.env.Hub.DB.QueryRow(`SELECT json_array(budget_remaining,message_count,last_message_at) FROM threads WHERE thr_id=?`, f.target.ThreadID).Scan(out); err != nil {
			t.Fatal(err)
		}
	}
	snapshot(&before)
	f.env.Now.Advance(time.Second)
	needWake(t, rx(t, f.env, f.bs, f.target.MessageID, "✅", "add", "wake"))
	snapshot(&after)
	if before != after {
		t.Fatalf("thread changed: %s -> %s", before, after)
	}
}
func TestReplyToReactionRefused(t *testing.T) {
	f := newRX(t)
	w := needWake(t, rx(t, f.env, f.bs, f.target.MessageID, "✅", "add", "wake"))
	_, d := mustReject(t, f.env, f.as, replyReq("reply", f.target.ThreadID, w.MessageID), 422, "not_replyable")
	if d["in_reply_to"] != w.MessageID {
		t.Fatalf("details=%v", d)
	}
	c, err := f.env.Hub.Auth.Resolve(context.Background(), f.as)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.env.Hub.ReplyThread(context.Background(), c, "", w.MessageID)
	e, ok := err.(*hub.APIError)
	if !ok || e.Code != "not_replyable" || e.Status != 422 || e.Details["in_reply_to"] != w.MessageID {
		t.Fatalf("ReplyThread=%v", err)
	}
}
func TestReactToReactionRefused(t *testing.T) {
	f := newRX(t)
	w := needWake(t, rx(t, f.env, f.bs, f.target.MessageID, "✅", "add", "wake"))
	rejectReact(t, f.env, f.as, rxBody(w.MessageID, "❗", "add", "wake"), 422, "not_replyable")
}
func TestRepeatedAddDoesNotWakeTwice(t *testing.T) {
	f := newRX(t)
	needWake(t, rx(t, f.env, f.bs, f.target.MessageID, "✅", "add", "wake"))
	out := rx(t, f.env, f.bs, f.target.MessageID, "✅", "add", "wake")
	if out.Changed || out.Wake != nil {
		t.Fatalf("repeat=%+v", out)
	}
	rxCount(t, f.env, 1)
}
func TestReaddAfterRemoveDoesNotWakeAgain(t *testing.T) {
	f := newRX(t)
	w := needWake(t, rx(t, f.env, f.bs, f.target.MessageID, "✅", "add", "wake"))
	rxAccept(t, f, w)
	rx(t, f.env, f.bs, f.target.MessageID, "✅", "remove", "wake")
	before := len(reactionEvents(t, f.env, f.a, "reaction"))
	out := rx(t, f.env, f.bs, f.target.MessageID, "✅", "add", "wake")
	if !out.Changed || out.Wake != nil || len(reactionEvents(t, f.env, f.a, "reaction")) != before+1 {
		t.Fatalf("readd=%+v", out)
	}
	rxCount(t, f.env, 1)
}
func TestEmojiRotationWakesAtMostThree(t *testing.T) {
	f := newRX(t)
	for i, emoji := range []string{"✅", "❓", "❗", "✅", "❓", "❗"} {
		out := rx(t, f.env, f.bs, f.target.MessageID, emoji, "add", "wake")
		if !out.Changed {
			t.Fatal("rotation unchanged")
		}
		if i < 3 {
			rxAccept(t, f, needWake(t, out))
		} else if out.Wake != nil {
			t.Fatalf("repeat emoji woke: %+v", out)
		}
		rx(t, f.env, f.bs, f.target.MessageID, emoji, "remove", "wake")
	}
	rxCount(t, f.env, 3)
	if n := count(t, f.env, `SELECT count(*) FROM reaction_wakes WHERE peer_id=? AND msg_id=?`, f.b, f.target.MessageID); n != 3 {
		t.Fatalf("wake ledger=%d", n)
	}
}
func TestOperatorReaddDoesWake(t *testing.T) {
	f := newRX(t)
	needWake(t, rx(t, f.env, f.env.OperatorSecret, f.target.MessageID, "✅", "add", "wake"))
	rx(t, f.env, f.env.OperatorSecret, f.target.MessageID, "✅", "remove", "wake")
	needWake(t, rx(t, f.env, f.env.OperatorSecret, f.target.MessageID, "✅", "add", "wake"))
	rxCount(t, f.env, 2)
	if n := count(t, f.env, `SELECT count(*) FROM reaction_wakes WHERE peer_id=?`, f.env.OperatorPeer); n != 0 {
		t.Fatalf("operator ledger=%d", n)
	}
}
func TestRemoveNeverWakes(t *testing.T) {
	f := newRX(t)
	rx(t, f.env, f.bs, f.target.MessageID, "✅", "add", "hold")
	out := rx(t, f.env, f.bs, f.target.MessageID, "✅", "remove", "wake")
	if !out.Changed || out.Wake != nil {
		t.Fatalf("remove=%+v", out)
	}
	rxCount(t, f.env, 0)
}
func TestRemoveWithdrawsQueuedWake(t *testing.T) {
	f := newRX(t)
	cb, _ := rxRoute(t, f)
	reportPresence(t, f.env, f.as, "", "working")
	w := needWake(t, rx(t, f.env, f.bs, f.target.MessageID, "✅", "add", "wake_idle"))
	out := rx(t, f.env, f.bs, f.target.MessageID, "✅", "remove", "wake")
	if out.Wake != nil || !reflect.DeepEqual(out.Superseded, []string{w.DeliveryID}) {
		t.Fatalf("withdraw=%+v", out)
	}
	if s, held := deliveryState(t, f.env, w.DeliveryID); s != "superseded" || held != 0 {
		t.Fatalf("state=%s held=%d", s, held)
	}
	reportPresence(t, f.env, f.as, "", "idle")
	rxStep(t, f.env)
	if len(cb.Posts()) != 0 {
		t.Fatal("withdrawn wake pushed")
	}
}
func TestCoalesceSupersedesQueuedWake(t *testing.T) {
	f := newRX(t)
	cb, _ := rxRoute(t, f)
	reportPresence(t, f.env, f.as, "", "working")
	w := needWake(t, rx(t, f.env, f.bs, f.target.MessageID, "✅", "add", "wake_idle"))
	m := mustSend(t, f.env, f.as, replyReq("other-target", f.target.ThreadID, f.target.MessageID))
	out := rx(t, f.env, f.bs, m.MessageID, "❗", "add", "wake_idle")
	w2 := needWake(t, out)
	if !reflect.DeepEqual(out.Superseded, []string{w.DeliveryID}) {
		t.Fatalf("superseded=%v", out.Superseded)
	}
	if s, _ := deliveryState(t, f.env, w.DeliveryID); s != "superseded" {
		t.Fatalf("first=%s", s)
	}
	if s, _ := deliveryState(t, f.env, w2.DeliveryID); s != "queued" {
		t.Fatalf("second=%s", s)
	}
	reportPresence(t, f.env, f.as, "", "idle")
	rxStep(t, f.env)
	rxStep(t, f.env)
	if len(cb.Posts()) != 1 {
		t.Fatalf("pushes=%d", len(cb.Posts()))
	}
	var d envelope.Delivery
	if err := json.Unmarshal(cb.Posts()[0].Body, &d); err != nil || d.DeliveryID != w2.DeliveryID {
		t.Fatalf("wrong push: %+v %v", d, err)
	}
}
func TestCoalesceLeavesAcceptedWake(t *testing.T) {
	f := newRX(t)
	w := needWake(t, rx(t, f.env, f.bs, f.target.MessageID, "✅", "add", "wake"))
	rxAccept(t, f, w)
	out := rx(t, f.env, f.bs, f.target.MessageID, "❗", "add", "wake")
	needWake(t, out)
	if len(out.Superseded) != 0 {
		t.Fatalf("superseded=%v", out.Superseded)
	}
	if s, _ := deliveryState(t, f.env, w.DeliveryID); s != "accepted" {
		t.Fatalf("first=%s", s)
	}
	rxCount(t, f.env, 2)
}
func TestCoalesceIsPerThreadAndReactor(t *testing.T) {
	f := newRX(t)
	x := needWake(t, rx(t, f.env, f.bs, f.target.MessageID, "✅", "add", "wake"))
	other := mustSend(t, f.env, f.as, holdReq("Y", f.b))
	y := needWake(t, rx(t, f.env, f.bs, other.MessageID, "✅", "add", "wake"))
	c := needWake(t, rx(t, f.env, f.cs, f.target.MessageID, "✅", "add", "wake"))
	out := rx(t, f.env, f.bs, f.target.MessageID, "❗", "add", "wake")
	needWake(t, out)
	if !reflect.DeepEqual(out.Superseded, []string{x.DeliveryID}) {
		t.Fatalf("superseded=%v", out.Superseded)
	}
	for _, w := range []*hub.ReactWake{y, c} {
		if s, _ := deliveryState(t, f.env, w.DeliveryID); s != "queued" {
			t.Fatalf("other wake=%s", s)
		}
	}
}
func TestReactionWakeSkipsSelfAndSources(t *testing.T) {
	f := newRX(t)
	out := rx(t, f.env, f.as, f.target.MessageID, "✅", "add", "wake")
	if !out.Changed || out.Wake != nil {
		t.Fatalf("self=%+v", out)
	}
	src, _, _, err := f.env.Hub.AddSource(context.Background(), "github", "source", f.b, []string{"issues"}, "hold")
	if err != nil {
		t.Fatal(err)
	}
	msg, dropped, err := f.env.Hub.IngestSource(context.Background(), hub.SourceMessage{SrcID: src, ProviderDeliveryID: "delivery", OriginThreadID: "issue/1", OriginEventID: "event/1", EventName: "issues.opened", Summary: "source", Raw: []byte(`{}`), MediaType: "application/json"})
	if err != nil || dropped {
		t.Fatalf("source=%s %v %v", msg, dropped, err)
	}
	out = rx(t, f.env, f.bs, msg, "✅", "add", "wake")
	if !out.Changed || out.Wake != nil {
		t.Fatalf("source reaction=%+v", out)
	}
	rxCount(t, f.env, 0)
}
func TestReactionWakeRespectsScopes(t *testing.T) {
	f := newRX(t)
	if err := f.env.Hub.SetScopes(context.Background(), f.b, nil, []string{f.a}); err != nil {
		t.Fatal(err)
	}
	before := count(t, f.env, `SELECT count(*) FROM events`)
	rejectReact(t, f.env, f.bs, rxBody(f.target.MessageID, "✅", "add", "wake"), 403, "scope_denied")
	if count(t, f.env, `SELECT count(*) FROM reactions`) != 0 || count(t, f.env, `SELECT count(*) FROM reaction_ops`) != 0 || count(t, f.env, `SELECT count(*) FROM events`) != before {
		t.Fatal("scope refusal wrote reaction state")
	}
	rxCount(t, f.env, 0)
}
func TestHistoryHidesReactionMessages(t *testing.T) {
	f := newRX(t)
	needWake(t, rx(t, f.env, f.bs, f.target.MessageID, "✅", "add", "wake"))
	m2 := mustSend(t, f.env, f.as, replyReq("next", f.target.ThreadID, f.target.MessageID))
	needWake(t, rx(t, f.env, f.bs, m2.MessageID, "❗", "add", "wake"))
	m3 := mustSend(t, f.env, f.as, replyReq("last", f.target.ThreadID, m2.MessageID))
	c, err := f.env.Hub.Auth.Resolve(context.Background(), f.bs)
	if err != nil {
		t.Fatal(err)
	}
	page, next, err := f.env.Hub.ThreadMessages(context.Background(), c, f.target.ThreadID, "", 2)
	if err != nil || len(page) != 2 || page[0].MessageID != f.target.MessageID || page[1].MessageID != m2.MessageID || next == nil || *next != m2.MessageID {
		t.Fatalf("page=%+v next=%v err=%v", page, next, err)
	}
	page, next, err = f.env.Hub.ThreadMessages(context.Background(), c, f.target.ThreadID, *next, 2)
	if err != nil || len(page) != 1 || page[0].MessageID != m3.MessageID || next != nil {
		t.Fatalf("last page=%+v next=%v err=%v", page, next, err)
	}
}
func TestSupersededNeverPushed(t *testing.T) {
	f := newRX(t)
	cb, _ := rxRoute(t, f)
	// Seed a queued delivery in the terminal state so the gate fails red
	// independently of whether wake creation has been implemented yet.
	m := mustSend(t, f.env, f.bs, req("terminal", f.a))
	id := m.Deliveries[0].DeliveryID
	if _, err := f.env.Hub.DB.Exec(`UPDATE messages SET kind='reaction',body=json_set(body,'$.kind','reaction','$.reaction',json_object('target',?,'emoji','✅')) WHERE msg_id=?`, f.target.MessageID, m.MessageID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.env.Hub.DB.Exec(`UPDATE deliveries SET state='superseded',held=0,next_attempt_at=NULL WHERE dly_id=?`, id); err != nil {
		t.Fatal(err)
	}
	gate, err := f.env.Hub.DeliveryPushGate(context.Background(), id)
	if err != nil || gate != hub.GateNever {
		t.Errorf("gate=%v err=%v want GateNever", gate, err)
	}
	rxStep(t, f.env)
	if len(cb.Posts()) != 0 {
		t.Fatal("superseded claimed/pushed")
	}
	cur, err := f.env.Hub.Log.Current(context.Background(), []string{f.a})
	if err != nil {
		t.Fatal(err)
	}
	r, raw := f.env.Do(f.as, "POST", "/v1/ack", map[string]string{"cursor": cur.String()})
	if r.StatusCode != 204 {
		t.Fatalf("ack=%d %s", r.StatusCode, raw)
	}
	f.env.Now.Advance(25 * time.Hour)
	if _, err := f.env.Hub.ExpireDeliveries(context.Background(), f.env.Now.Now()); err != nil {
		t.Fatal(err)
	}
	if s, _ := deliveryState(t, f.env, id); s != "superseded" {
		t.Fatalf("terminal state=%s", s)
	}
	for _, rc := range receipts(t, f.env, f.b) {
		if rc.DeliveryID == id {
			t.Fatalf("receipt for superseded=%+v", rc)
		}
	}
}
func TestReactionWakeNotifiesOperator(t *testing.T) {
	f := newRX(t)
	target := mustSend(t, f.env, f.env.OperatorSecret, holdReq("operator", f.b))
	rcv := newNotifyReceiver(t)
	routes := putNotify(t, f.env, map[string]string{"url": rcv.srv.URL, "filter": "wake"})
	w := needWake(t, rx(t, f.env, f.bs, target.MessageID, "✅", "add", "wake"))
	f.env.Hub.WaitNotifies()
	hits := rcv.Hits()
	if len(hits) != 1 {
		t.Fatalf("notifications=%d", len(hits))
	}
	ev := verifyNotify(t, hits[0], routes[rcv.srv.URL].Whsec, f.env.Now.Now())
	var d envelope.Delivery
	if err := json.Unmarshal(ev.Data, &d); err != nil || ev.Type != "delivery" || d.DeliveryID != w.DeliveryID || d.Message.Kind != "reaction" {
		t.Fatalf("notification=%+v delivery=%+v err=%v", ev, d, err)
	}
}
