package hub_test

import (
	"context"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/testhub"
)

func accountingState(t *testing.T, env *testhub.Env, thr string) string {
	t.Helper()
	var state string
	if err := env.Hub.DB.QueryRow(`SELECT json_array(budget_remaining,message_count,last_message_at) FROM threads WHERE thr_id=?`, thr).Scan(&state); err != nil {
		t.Fatal(err)
	}
	return state
}

func TestSendAccountingGuard(t *testing.T) {
	env := idemEnv(t, nil)
	group := addGroup(t, env, guardA, guardA, guardB)
	first := mustSend(t, env, guardSecret+guardA, req("account-first", group))
	guardExec(t, env, `CREATE TABLE task06_updates(n INTEGER NOT NULL) STRICT`)
	guardExec(t, env, `INSERT INTO task06_updates VALUES (0)`)
	guardExec(t, env, `CREATE TRIGGER task06_count_updates AFTER UPDATE ON threads BEGIN UPDATE task06_updates SET n=n+1; END`)
	env.Now.Advance(time.Second)
	next := req("account-second", group)
	next["thread_id"] = first.ThreadID
	mustSend(t, env, guardSecret+guardA, next)
	if n := count(t, env, `SELECT n FROM task06_updates`); n != 1 {
		t.Fatalf("thread updates per existing-thread send = %d, want 1", n)
	}
	var budget, messages int
	var at, groupAt string
	if err := env.Hub.DB.QueryRow(`SELECT budget_remaining,message_count,last_message_at FROM threads WHERE thr_id=?`, first.ThreadID).Scan(&budget, &messages, &at); err != nil {
		t.Fatal(err)
	}
	if budget != 498 || messages != 2 || at != envelope.FormatTime(env.Now.Now()) {
		t.Fatal("ordinary send did not atomically advance budget/count/activity")
	}
	if err := env.Hub.DB.QueryRow(`SELECT last_active_at FROM groups WHERE grp_id=?`, group).Scan(&groupAt); err != nil {
		t.Fatal(err)
	}
	if groupAt != at {
		t.Fatal("successful send did not touch addressed group")
	}
	before := accountingState(t, env, first.ThreadID)
	env.Now.Advance(time.Second)
	bad := req("account-failed", "peer_00000000000000000000000099")
	bad["thread_id"] = first.ThreadID
	mustReject(t, env, guardSecret+guardA, bad, 404, "unknown_recipient")
	if after := accountingState(t, env, first.ThreadID); after != before {
		t.Fatal("failed send changed thread accounting")
	}
	guardExec(t, env, `UPDATE threads SET budget_remaining=0 WHERE thr_id=?`, first.ThreadID)
	before = accountingState(t, env, first.ThreadID)
	cut := req("account-cut", group)
	cut["thread_id"] = first.ThreadID
	mustReject(t, env, guardSecret+guardA, cut, 422, "thread_budget_exhausted")
	if after := accountingState(t, env, first.ThreadID); after != before {
		t.Fatal("loop cut changed budget/count/activity")
	}
	var afterGroup string
	if err := env.Hub.DB.QueryRow(`SELECT last_active_at FROM groups WHERE grp_id=?`, group).Scan(&afterGroup); err != nil {
		t.Fatal(err)
	}
	if afterGroup != groupAt {
		t.Fatal("failed send or loop cut touched group activity")
	}
	if n := count(t, env, `SELECT count(*) FROM audit WHERE action='loop_cut'`); n != 1 {
		t.Fatalf("loop cut audit count = %d", n)
	}
	if n := count(t, env, `SELECT count(*) FROM messages`); n != 2 {
		t.Fatalf("failed sends added messages: count %d", n)
	}
	if n := count(t, env, `SELECT count(*) FROM idempotency`); n != 2 {
		t.Fatalf("failed sends added keys: count %d", n)
	}
}

func TestReactionAccountingGuard(t *testing.T) {
	env := idemEnv(t, nil)
	first := mustSend(t, env, guardSecret+guardA, req("reaction-account", guardB))
	before := accountingState(t, env, first.ThreadID)
	env.Now.Advance(time.Second)
	response, _ := env.Do(guardSecret+guardB, "POST", "/v1/reactions", map[string]any{"message_id": first.MessageID, "emoji": "✅", "op": "add", "priority": "wake"})
	if response.StatusCode != 200 {
		t.Fatalf("reaction status = %d", response.StatusCode)
	}
	if n := count(t, env, `SELECT count(*) FROM messages WHERE kind='reaction'`); n != 1 {
		t.Fatalf("reaction messages = %d, want 1", n)
	}
	if after := accountingState(t, env, first.ThreadID); after != before {
		t.Fatal("reaction changed thread budget/count/activity")
	}
}

func TestHubAuthoredAccountingGuard(t *testing.T) {
	env := idemEnv(t, nil)
	first := mustSend(t, env, guardSecret+guardA, approvalReq("hub-account", "apr_00000000000000000000000001", env.Now.Now().Add(time.Hour), []string{"yes", "no"}, guardB))
	env.Now.Advance(2 * time.Hour)
	n, err := env.Hub.ExpireApprovals(context.Background(), env.Now.Now())
	if err != nil || n != 1 {
		t.Fatalf("ExpireApprovals = %d, %v", n, err)
	}
	var budget, messages int
	var at string
	if err := env.Hub.DB.QueryRow(`SELECT budget_remaining,message_count,last_message_at FROM threads WHERE thr_id=?`, first.ThreadID).Scan(&budget, &messages, &at); err != nil {
		t.Fatal(err)
	}
	if budget != 499 || messages != 2 || at != envelope.FormatTime(env.Now.Now()) {
		t.Fatal("hub-authored message changed budget or failed to advance count/activity")
	}
	if n := count(t, env, `SELECT count(*) FROM messages WHERE sender=?`, "hub"); n != 1 {
		t.Fatalf("hub-authored messages = %d", n)
	}
}
