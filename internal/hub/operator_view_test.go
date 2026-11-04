package hub_test

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/eventlog"
	"github.com/XertroV/vecnet/internal/events"
	"github.com/XertroV/vecnet/internal/ids"
	"github.com/XertroV/vecnet/internal/testhub"
)

type operatorView struct {
	Reason   string  `json:"reason"`
	ThreadID *string `json:"thread_id"`
}

func operatorViews(t *testing.T, env *testhub.Env, peer string) []operatorView {
	t.Helper()
	evs, err := env.Hub.Log.Read(context.Background(), []string{peer}, eventlog.Cursor{Epoch: env.Hub.Log.Epoch()}, 0)
	if err != nil {
		t.Fatal(err)
	}
	out := []operatorView{}
	for _, ev := range evs {
		if ev.Type != "operator_view" {
			continue
		}
		raw, err := json.Marshal(ev)
		if err != nil {
			t.Fatal(err)
		}
		if err := envelope.Validate("event", raw); err != nil {
			t.Fatalf("operator_view violates schema: %v", err)
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(ev.Data, &fields); err != nil {
			t.Fatal(err)
		}
		if len(fields) != 2 || fields["reason"] == nil || fields["thread_id"] == nil {
			t.Fatalf("data must have reason and explicit thread_id: %s", ev.Data)
		}
		var data operatorView
		if err := json.Unmarshal(ev.Data, &data); err != nil {
			t.Fatal(err)
		}
		out = append(out, data)
	}
	return out
}

func wantViews(t *testing.T, env *testhub.Env, want ...operatorView) {
	t.Helper()
	got := operatorViews(t, env, env.OperatorPeer)
	if want == nil {
		want = []operatorView{}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("operator_view events = %+v, want %+v", got, want)
	}
}

func operatorPost(t *testing.T, env *testhub.Env, path string, body any) []byte {
	t.Helper()
	resp, raw := env.Do(env.OperatorSecret, "POST", path, body)
	if resp.StatusCode/100 != 2 {
		t.Fatalf("POST %s = %d: %s", path, resp.StatusCode, raw)
	}
	return raw
}

func TestOperatorViewOnOperatorSend(t *testing.T) {
	env := testhub.Start(t)
	_, secret := env.NewPeer("session", "requester")
	apr := ids.New(ids.Apr)
	sent := mustSend(t, env, secret, approvalReq("ask", apr, env.Now.Now().Add(time.Hour), []string{"yes", "no"}, env.OperatorPeer))
	wantViews(t, env)
	body := approvalResp("answer", sent.ThreadID, apr, "yes")
	result := mustSend(t, env, env.OperatorSecret, body)
	wantViews(t, env, operatorView{"sent", &sent.ThreadID})
	status, replay, raw := sendHTTP(t, env, env.OperatorSecret, body)
	if status != http.StatusOK || replay.MessageID != result.MessageID {
		t.Fatalf("replay = %d %+v %s", status, replay, raw)
	}
	wantViews(t, env, operatorView{"sent", &sent.ThreadID})
}

func TestOperatorViewOnRead(t *testing.T) {
	env := testhub.Start(t)
	_, secret := env.NewPeer("session", "sender")
	first := mustSend(t, env, secret, req("one", env.OperatorPeer))
	second := req("two", env.OperatorPeer)
	second["thread_id"] = first.ThreadID
	mustSend(t, env, secret, second)
	for i, want := range []int{2, 0} {
		raw := operatorPost(t, env, "/v1/admin/threads/"+first.ThreadID+"/read", nil)
		var result struct {
			Marked int `json:"marked"`
		}
		if err := json.Unmarshal(raw, &result); err != nil || result.Marked != want {
			t.Fatalf("read %d = %s, want %d: %v", i, raw, want, err)
		}
		wantViews(t, env, operatorView{"read", &first.ThreadID}, operatorView{"read", &first.ThreadID})
	}
	// The direct receipts endpoint uses the same path; agent reads do not fire.
	third := mustSend(t, env, secret, req("three", env.OperatorPeer))
	for range 2 {
		operatorPost(t, env, "/v1/receipts", map[string]any{"delivery_id": third.Deliveries[0].DeliveryID, "state": "read"})
	}
	wantViews(t, env, operatorView{"read", &first.ThreadID}, operatorView{"read", &first.ThreadID}, operatorView{"read", &third.ThreadID})
}

func TestOperatorViewOnPresenceChangeOnly(t *testing.T) {
	env := testhub.Start(t)
	post := func(state string) {
		operatorPost(t, env, "/v1/presence", map[string]any{"state": state, "at": envelope.FormatTime(env.Now.Now())})
	}
	post("idle")
	before := operatorViews(t, env, env.OperatorPeer)
	post("working")
	got := operatorViews(t, env, env.OperatorPeer)
	if len(got) != len(before)+1 || !reflect.DeepEqual(got[len(got)-1], operatorView{"presence", nil}) {
		t.Fatalf("idle→working: %v -> %v", before, got)
	}
	env.Now.Advance(time.Minute)
	post("working")
	if fresh := operatorViews(t, env, env.OperatorPeer); !reflect.DeepEqual(fresh, got) {
		t.Fatalf("heartbeat emitted event: %v -> %v", got, fresh)
	}
	env.Now.Advance(2 * time.Hour)
	post("working")
	if fresh := operatorViews(t, env, env.OperatorPeer); len(fresh) != len(got)+1 {
		t.Fatalf("unknown→working should invalidate: %v", fresh)
	}
}

func operatorAdminChanges(t *testing.T, env *testhub.Env, peer, secret string) []operatorView {
	t.Helper()
	sent := mustSend(t, env, secret, req("dead", peer))
	dly := sent.Deliveries[0].DeliveryID
	// The operator is not in this thread, so the send itself is activity.
	want := []operatorView{{"activity", &sent.ThreadID}}
	// Seed the delivery failure as the push worker would.
	if _, err := env.Hub.DB.Exec(`UPDATE deliveries SET state = 'dead_letter' WHERE dly_id = ?`, dly); err != nil {
		t.Fatal(err)
	}
	operatorPost(t, env, "/v1/admin/dead-letters/"+dly+"/retry", nil)
	want = append(want, operatorView{"dead_letter", &sent.ThreadID})
	if _, err := env.Hub.DB.Exec(`UPDATE deliveries SET state = 'dead_letter' WHERE dly_id = ?`, dly); err != nil {
		t.Fatal(err)
	}
	operatorPost(t, env, "/v1/admin/dead-letters/"+dly+"/discard", nil)
	want = append(want, operatorView{"dead_letter", &sent.ThreadID})
	st := events.NewStore(env.Hub)
	sub, _, err := st.Upsert(context.Background(), events.Subscription{PrincipalKind: events.KindVecnet, PrincipalID: peer, PeerID: peer, Name: events.Name, URL: "http://127.0.0.1/callback"}, "whsec_test", time.Hour, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{"pause", "resume"} {
		operatorPost(t, env, "/v1/admin/subscriptions/"+sub.ID+"/"+action, nil)
		want = append(want, operatorView{"subscription", nil})
		before := operatorViews(t, env, env.OperatorPeer)
		operatorPost(t, env, "/v1/admin/subscriptions/"+sub.ID+"/"+action, nil)
		if after := operatorViews(t, env, env.OperatorPeer); !reflect.DeepEqual(before, after) {
			t.Fatalf("repeated %s emitted event: %v -> %v", action, before, after)
		}
	}
	return want
}

func TestOperatorViewDeadLetterAndSubscription(t *testing.T) {
	env := testhub.Start(t)
	peer, secret := env.NewPeer("session", "agent")
	wantViews(t, env, operatorAdminChanges(t, env, peer, secret)...)
}

func TestOperatorViewNeverReachesAgents(t *testing.T) {
	env := testhub.Start(t)
	agent, secret := env.NewPeer("session", "requester")
	other, _ := env.NewPeer("session", "other")
	apr := ids.New(ids.Apr)
	sent := mustSend(t, env, secret, approvalReq("ask", apr, env.Now.Now().Add(time.Hour), []string{"yes", "no"}, env.OperatorPeer))
	mustSend(t, env, env.OperatorSecret, approvalResp("answer", sent.ThreadID, apr, "yes"))
	operatorPost(t, env, "/v1/admin/threads/"+sent.ThreadID+"/read", nil)
	operatorPost(t, env, "/v1/presence", map[string]any{"state": "working", "at": envelope.FormatTime(env.Now.Now())})
	operatorAdminChanges(t, env, other, secret)
	if len(operatorViews(t, env, env.OperatorPeer)) == 0 {
		t.Fatal("no operator invalidations were generated")
	}
	for _, peer := range []string{agent, other} {
		if got := operatorViews(t, env, peer); len(got) != 0 {
			t.Fatalf("agent %s received operator_view: %v", peer, got)
		}
	}
}

func TestOperatorViewEnabledOperators(t *testing.T) {
	env := testhub.Start(t)
	var principal string
	if err := env.Hub.DB.QueryRow(`SELECT prn_id FROM peers WHERE peer_id = ?`, env.OperatorPeer).Scan(&principal); err != nil {
		t.Fatal(err)
	}
	other, _, err := env.Hub.AddPeer(context.Background(), "operator", "other operator", principal)
	if err != nil {
		t.Fatal(err)
	}
	disabled, _, err := env.Hub.AddPeer(context.Background(), "operator", "disabled operator", principal)
	if err != nil {
		t.Fatal(err)
	}
	// The public admin forbids disabling operators; seed a disabled row to
	// verify the helper's enabled-only contract independently of that guard.
	if _, err := env.Hub.DB.Exec(`UPDATE peers SET disabled = 1 WHERE peer_id = ?`, disabled); err != nil {
		t.Fatal(err)
	}
	agent, _ := env.NewPeer("session", "recipient")
	waitCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	wake := env.Hub.Log.Wait(waitCtx, []string{other})
	sent := mustSend(t, env, env.OperatorSecret, req("fanout", agent))
	// The other operator is not in the thread, so it also sees activity.
	wants := map[string][]operatorView{
		env.OperatorPeer: {{"sent", &sent.ThreadID}},
		other:            {{"sent", &sent.ThreadID}, {"activity", &sent.ThreadID}},
	}
	for peer, want := range wants {
		if got := operatorViews(t, env, peer); !reflect.DeepEqual(got, want) {
			t.Fatalf("operator %s: got %v, want %v", peer, got, want)
		}
	}
	if got := operatorViews(t, env, disabled); len(got) != 0 {
		t.Fatalf("disabled operator received events: %v", got)
	}
	select {
	case <-wake:
	default:
		t.Fatal("committed send did not wake the other operator's tail")
	}
}

// A message in a thread the operator is not in reaches it as one
// operator_view "activity" per message; a thread it is in, its own send, and
// an idempotent replay add none.
func TestOperatorViewActivity(t *testing.T) {
	env := testhub.Start(t)
	a, asec := env.NewPeer("session", "A")
	b, bsec := env.NewPeer("session", "B")
	first := mustSend(t, env, asec, req("one", b))
	wantViews(t, env, operatorView{"activity", &first.ThreadID})
	reply := req("two")
	reply["thread_id"] = first.ThreadID
	mustSend(t, env, bsec, reply)
	wantViews(t, env, operatorView{"activity", &first.ThreadID}, operatorView{"activity", &first.ThreadID})
	// Replaying the same key and body is answered from the idempotency store.
	status, _, raw := sendHTTP(t, env, bsec, reply)
	if status != http.StatusOK {
		t.Fatalf("replay = %d, want 200: %s", status, raw)
	}
	wantViews(t, env, operatorView{"activity", &first.ThreadID}, operatorView{"activity", &first.ThreadID})
	// A thread the operator is in: the operator gets a delivery, no activity.
	mine := mustSend(t, env, asec, req("mine", env.OperatorPeer, b))
	wantViews(t, env, operatorView{"activity", &first.ThreadID}, operatorView{"activity", &first.ThreadID})
	if len(deliveryEvents(t, env, env.OperatorPeer)) != 1 {
		t.Fatalf("operator should have one delivery for %s", mine.ThreadID)
	}
	// Joining by sending is "sent", not activity: the operator is now in it.
	join := req("join")
	join["thread_id"] = first.ThreadID
	mustSend(t, env, env.OperatorSecret, join)
	wantViews(t, env, operatorView{"activity", &first.ThreadID}, operatorView{"activity", &first.ThreadID}, operatorView{"sent", &first.ThreadID})
	again := req("after-join")
	again["thread_id"] = first.ThreadID
	mustSend(t, env, asec, again)
	wantViews(t, env, operatorView{"activity", &first.ThreadID}, operatorView{"activity", &first.ThreadID}, operatorView{"sent", &first.ThreadID})
	// Participants never receive operator_view.
	for _, peer := range []string{a, b} {
		if got := operatorViews(t, env, peer); len(got) != 0 {
			t.Fatalf("participant %s received operator_view: %v", peer, got)
		}
	}
}

// The hub's own expiry notice is a message too: a non-participant operator
// sees it as activity.
func TestOperatorViewActivityOnApprovalExpiry(t *testing.T) {
	env := testhub.Start(t)
	_, asec := env.NewPeer("session", "A")
	b, _ := env.NewPeer("session", "B")
	apr := ids.New(ids.Apr)
	sent := mustSend(t, env, asec, approvalReq("ask", apr, env.Now.Now().Add(time.Minute), []string{"yes", "no"}, b))
	env.Now.Advance(2 * time.Minute)
	if n, err := env.Hub.ExpireApprovals(context.Background(), env.Now.Now()); err != nil || n != 1 {
		t.Fatalf("expire = %d, %v", n, err)
	}
	wantViews(t, env, operatorView{"activity", &sent.ThreadID}, operatorView{"activity", &sent.ThreadID})
}

func TestOperatorViewAppendFailureRollsBackRead(t *testing.T) {
	env := testhub.Start(t)
	_, secret := env.NewPeer("session", "sender")
	sent := mustSend(t, env, secret, req("unread", env.OperatorPeer))
	// A durable-log failure must abort the read and must not wake a tail.
	if _, err := env.Hub.DB.Exec(`CREATE TRIGGER reject_operator_view BEFORE INSERT ON events
		WHEN NEW.type = 'operator_view' BEGIN SELECT RAISE(ABORT, 'test log failure'); END`); err != nil {
		t.Fatal(err)
	}
	waitCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	wake := env.Hub.Log.Wait(waitCtx, []string{env.OperatorPeer})
	resp, raw := env.Do(env.OperatorSecret, "POST", "/v1/receipts", map[string]any{
		"delivery_id": sent.Deliveries[0].DeliveryID, "state": "read",
	})
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("read despite log failure: %d %s", resp.StatusCode, raw)
	}
	wantViews(t, env)
	select {
	case <-wake:
		t.Fatal("failed transaction woke operator tail")
	default:
	}
	if _, err := env.Hub.DB.Exec(`DROP TRIGGER reject_operator_view`); err != nil {
		t.Fatal(err)
	}
	// The same read is still new after rollback, then becomes idempotent.
	for range 2 {
		operatorPost(t, env, "/v1/receipts", map[string]any{
			"delivery_id": sent.Deliveries[0].DeliveryID, "state": "read",
		})
	}
	wantViews(t, env, operatorView{"read", &sent.ThreadID})
}

// Two operator clients do not fight over presence: a heartbeat built before a
// manual change in the other client, and landing after it, leaves the manual
// state (Review Focus 4).
func TestOperatorPresenceStaleReportLoses(t *testing.T) {
	env := testhub.Start(t)
	t0 := env.Now.Now()
	post := func(state string, at time.Time) {
		operatorPost(t, env, "/v1/presence", map[string]any{"state": state, "at": envelope.FormatTime(at)})
	}
	stored := func() (string, string) {
		var state, at string
		if err := env.Hub.DB.QueryRow(`SELECT presence_state, presence_at FROM peers WHERE peer_id = ?`, env.OperatorPeer).Scan(&state, &at); err != nil {
			t.Fatal(err)
		}
		return state, at
	}
	post("working", t0)
	env.Now.Advance(2 * time.Second)
	post("idle", t0.Add(2*time.Second)) // the second client's manual change
	views := operatorViews(t, env, env.OperatorPeer)
	post("working", t0.Add(time.Second)) // the held heartbeat, read before it
	if state, at := stored(); state != "idle" || at != envelope.FormatTime(t0.Add(2*time.Second)) {
		t.Fatalf("stale heartbeat overwrote the manual change: %s at %s", state, at)
	}
	if after := operatorViews(t, env, env.OperatorPeer); !reflect.DeepEqual(after, views) {
		t.Fatalf("stale report emitted events: %v -> %v", views, after)
	}
	// A report at the same instant or later still lands.
	post("working", t0.Add(2*time.Second))
	if state, _ := stored(); state != "working" {
		t.Fatalf("equal at did not land: %s", state)
	}
}
