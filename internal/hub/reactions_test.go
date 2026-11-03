package hub_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"reflect"
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
