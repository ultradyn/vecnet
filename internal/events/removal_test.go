package events_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/events"
	"github.com/XertroV/vecnet/internal/opapi"
	"github.com/XertroV/vecnet/internal/testhub"
)

func removalCount(t *testing.T, env *testhub.Env, query string, args ...any) int {
	t.Helper()
	var n int
	if err := env.Hub.DB.QueryRowContext(context.Background(), query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func removalExec(t *testing.T, env *testhub.Env, query string, args ...any) {
	t.Helper()
	if _, err := env.Hub.DB.ExecContext(context.Background(), query, args...); err != nil {
		t.Fatal(err)
	}
}

func wantRemoved(t *testing.T, env *testhub.Env, id string) {
	t.Helper()
	if _, err := env.Events.Store.Get(context.Background(), id); !errors.Is(err, events.ErrNotFound) {
		t.Fatalf("removed subscription lookup: %v", err)
	}
	for _, query := range []string{
		`SELECT count(*) FROM event_outbox WHERE sub_id = ?`,
		`SELECT count(*) FROM meta WHERE key = 'events_refresh_schedule:' || ?`,
	} {
		if n := removalCount(t, env, query, id); n != 0 {
			t.Fatalf("removal left %d dependent rows", n)
		}
	}
}

func TestRemoveSubscriptionExactID(t *testing.T) {
	for _, broken := range []bool{false, true} {
		t.Run(fmt.Sprintf("broken=%v", broken), func(t *testing.T) {
			env := testhub.Start(t)
			peer, _ := env.NewPeer("dot", "receiver")
			_, sender := env.NewPeer("session", "sender")
			cb := testhub.NewCallback(t)
			target := subscribe(t, env, cb, peer, nil, time.Time{})
			other := subscribe(t, env, cb, peer, map[string]any{"thread_id": "unmatched"}, time.Time{})
			if broken {
				cb.OnPost(func(_ int, w http.ResponseWriter) { w.WriteHeader(503) })
			}
			send(t, env, sender, peer, "remove-message", "wake", "retained history")
			step(t, env)
			if broken {
				removalExec(t, env, `UPDATE event_subscriptions SET paused_reason='delivery_failed' WHERE sub_id=?`, target.ID)
			}
			if removalCount(t, env, `SELECT count(*) FROM event_outbox WHERE sub_id=?`, target.ID) == 0 {
				t.Fatal("missing outbox fixture")
			}
			// Keep a sibling's queued callback: deleting by peer or URL
			// instead of exact ID would incorrectly erase this row too.
			removalExec(t, env, `INSERT INTO event_outbox(sub_id,event_id,seq,body,parked,created_at) VALUES(?,'sibling',1,'{"synthetic":"retained"}',1,'2026-11-15T00:00:00Z')`, other.ID)
			for _, state := range []struct {
				id             string
				parked         int
				done, terminal any
			}{
				{"pending", 0, nil, nil}, {"parked", 1, nil, nil},
				{"settled", 0, "2026-11-15T00:00:00Z", nil},
				{"exhausted", 0, "2026-11-15T00:00:00Z", "exhausted"},
			} {
				removalExec(t, env, `INSERT INTO event_outbox(sub_id,event_id,seq,body,parked,created_at,done_at,terminal) VALUES(?,?,1,'{}',?,'2026-11-15T00:00:00Z',?,?)`, target.ID, state.id, state.parked, state.done, state.terminal)
			}
			otherBefore := get(t, env, other.ID)
			scheduleBefore, err := env.Hub.DB.Meta(context.Background(), "events_refresh_schedule:"+other.ID)
			if err != nil {
				t.Fatal(err)
			}
			history := map[string]int{}
			for _, table := range []string{"messages", "deliveries", "peers", "routes", "credentials"} {
				history[table] = removalCount(t, env, `SELECT count(*) FROM `+table)
			}
			peerBefore, err := env.Hub.Log.Current(context.Background(), []string{peer})
			if err != nil {
				t.Fatal(err)
			}
			env.Events.Store.RememberVerification(target, "synthetic-proof", env.Now.Now())
			waitCtx, cancel := context.WithCancel(context.Background())
			defer cancel()
			wake := env.Hub.Log.Wait(waitCtx, []string{env.OperatorPeer})
			before, err := env.Hub.Log.Current(context.Background(), []string{env.OperatorPeer})
			if err != nil {
				t.Fatal(err)
			}
			if err := env.Events.Store.Remove(context.Background(), target.ID); err != nil {
				t.Fatal(err)
			}
			wantRemoved(t, env, target.ID)
			if !reflect.DeepEqual(get(t, env, other.ID), otherBefore) {
				t.Fatal("sibling subscription changed")
			}
			if removalCount(t, env, `SELECT count(*) FROM event_outbox WHERE sub_id=? AND event_id='sibling' AND parked=1 AND body='{"synthetic":"retained"}'`, other.ID) != 1 {
				t.Fatal("sibling callback queue changed")
			}
			scheduleAfter, err := env.Hub.DB.Meta(context.Background(), "events_refresh_schedule:"+other.ID)
			if err != nil || scheduleAfter != scheduleBefore {
				t.Fatal("sibling refresh schedule changed")
			}
			for table, count := range history {
				if got := removalCount(t, env, `SELECT count(*) FROM `+table); got != count {
					t.Fatalf("%s changed: %d -> %d", table, count, got)
				}
			}
			peerAfter, err := env.Hub.Log.Current(context.Background(), []string{peer})
			if err != nil || peerAfter != peerBefore {
				t.Fatal("peer event history changed")
			}
			resp, _ := env.Do(sender, "GET", "/v1/peers/me", nil)
			if resp.StatusCode != http.StatusOK {
				t.Fatal("peer credential no longer works")
			}
			if _, ok := env.Events.Store.CachedVerification(target, "synthetic-proof"); !ok {
				t.Fatal("verification cache changed")
			}
			select {
			case <-wake:
			default:
				t.Fatal("committed removal did not wake operator")
			}
			evs, err := env.Hub.Log.Read(context.Background(), []string{env.OperatorPeer}, before, 0)
			if err != nil {
				t.Fatal(err)
			}
			if len(evs) != 1 || evs[0].Type != "operator_view" || string(evs[0].Data) != `{"reason":"subscription","thread_id":null}` {
				t.Fatal("missing subscription invalidation")
			}
			if got := opapi.Tags(evs[0]); !reflect.DeepEqual(got, []string{"inbox", "threads", "peers", "subs"}) {
				t.Fatalf("real subscription invalidation tags: %v", got)
			}
			var actor, targets, detail string
			err = env.Hub.DB.QueryRowContext(context.Background(), `SELECT actor,target_ids,detail FROM audit WHERE action='subscription_remove'`).Scan(&actor, &targets, &detail)
			if err != nil {
				t.Fatal(err)
			}
			var ids []string
			if err := json.Unmarshal([]byte(targets), &ids); err != nil {
				t.Fatal(err)
			}
			if actor != "operator" || !reflect.DeepEqual(ids, []string{target.ID}) || detail != "{}" {
				t.Fatal("removal audit must contain only target ID")
			}
			repeatWake := env.Hub.Log.Wait(waitCtx, []string{env.OperatorPeer})
			for _, id := range []string{target.ID, "sub_unknown", ""} {
				if err := env.Events.Store.Remove(context.Background(), id); !errors.Is(err, events.ErrNotFound) {
					t.Fatalf("unknown removal: %v", err)
				}
			}
			if n := removalCount(t, env, `SELECT count(*) FROM audit WHERE action='subscription_remove'`); n != 1 {
				t.Fatalf("repeated removal wrote %d audits", n)
			}
			select {
			case <-repeatWake:
				t.Fatal("missing removal woke operator")
			default:
			}
			posts := len(cb.Posts())
			env.Now.Advance(time.Minute)
			step(t, env)
			afterPosts := cb.Posts()
			if len(afterPosts) != posts+1 || afterPosts[posts].Subscription != other.ID {
				t.Fatal("expected only the surviving sibling callback to be sent")
			}
			for _, post := range afterPosts[posts:] {
				if post.Subscription == target.ID {
					t.Fatal("removed callback retried")
				}
			}
			wantRemoved(t, env, target.ID)
			fresh, created, err := env.Events.Store.Upsert(context.Background(), target, "synthetic-secret", time.Hour, time.Time{})
			if err != nil || !created || fresh.ID == target.ID {
				t.Fatal("client cannot resubscribe with a fresh ID")
			}
		})
	}
}

func TestRemoveSubscriptionRollback(t *testing.T) {
	for name, trigger := range map[string]string{
		"schedule":     `CREATE TRIGGER reject_remove BEFORE DELETE ON meta WHEN OLD.key LIKE 'events_refresh_schedule:%' BEGIN SELECT RAISE(ABORT,'test failure'); END`,
		"outbox":       `CREATE TRIGGER reject_remove BEFORE DELETE ON event_outbox BEGIN SELECT RAISE(ABORT,'test failure'); END`,
		"subscription": `CREATE TRIGGER reject_remove BEFORE DELETE ON event_subscriptions BEGIN SELECT RAISE(ABORT,'test failure'); END`,
		"audit":        `CREATE TRIGGER reject_remove BEFORE INSERT ON audit WHEN NEW.action='subscription_remove' BEGIN SELECT RAISE(ABORT,'test failure'); END`,
		"notification": `CREATE TRIGGER reject_remove BEFORE INSERT ON events WHEN NEW.type='operator_view' BEGIN SELECT RAISE(ABORT,'test failure'); END`,
	} {
		t.Run(name, func(t *testing.T) {
			env := testhub.Start(t)
			peer, _ := env.NewPeer("dot", "receiver")
			sub := subscribe(t, env, testhub.NewCallback(t), peer, nil, time.Time{})
			removalExec(t, env, `INSERT INTO event_outbox(sub_id,event_id,seq,body,created_at) VALUES(?,'synthetic',1,'{}','2026-11-15T00:00:00Z')`, sub.ID)
			removalExec(t, env, trigger)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			wake := env.Hub.Log.Wait(ctx, []string{env.OperatorPeer})
			before, err := env.Hub.Log.Current(ctx, []string{env.OperatorPeer})
			if err != nil {
				t.Fatal(err)
			}
			if err := env.Events.Store.Remove(context.Background(), sub.ID); err == nil {
				t.Fatal("removal succeeded despite injected failure")
			}
			get(t, env, sub.ID)
			if removalCount(t, env, `SELECT count(*) FROM event_outbox WHERE sub_id=?`, sub.ID) != 1 || removalCount(t, env, `SELECT count(*) FROM meta WHERE key='events_refresh_schedule:' || ?`, sub.ID) != 1 {
				t.Fatal("cleanup was not rolled back")
			}
			if removalCount(t, env, `SELECT count(*) FROM audit WHERE action='subscription_remove'`) != 0 {
				t.Fatal("failed removal left audit")
			}
			after, err := env.Hub.Log.Current(ctx, []string{env.OperatorPeer})
			if err != nil || after != before {
				t.Fatal("failed removal left operator event")
			}
			select {
			case <-wake:
				t.Fatal("failed removal woke operator")
			default:
			}
			removalExec(t, env, `DROP TRIGGER reject_remove`)
			if err := env.Events.Store.Remove(context.Background(), sub.ID); err != nil {
				t.Fatal(err)
			}
			wantRemoved(t, env, sub.ID)
		})
	}
}

func TestRemoveSubscriptionMidDrain(t *testing.T) {
	for _, status := range []int{200, 503} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			env := testhub.Start(t)
			peer, _ := env.NewPeer("dot", "receiver")
			_, sender := env.NewPeer("session", "sender")
			cb := testhub.NewCallback(t)
			sub := subscribe(t, env, cb, peer, nil, time.Time{})
			result := make(chan error, 1)
			cb.OnPost(func(n int, w http.ResponseWriter) {
				if n == 1 {
					result <- env.Events.Store.Remove(context.Background(), sub.ID)
				}
				w.WriteHeader(status)
			})
			send(t, env, sender, peer, "remove-first", "wake", "first")
			send(t, env, sender, peer, "remove-second", "wake", "second")
			step(t, env)
			if err := <-result; err != nil {
				t.Fatal(err)
			}
			wantPosts(t, cb, 1)
			wantRemoved(t, env, sub.ID)
			env.Now.Advance(time.Minute)
			send(t, env, sender, peer, "remove-third", "wake", "third")
			step(t, env)
			wantPosts(t, cb, 1)
			wantRemoved(t, env, sub.ID)
		})
	}
}

// Operator recovery must work even when decoding subscription health or
// unsealing a secret would fail. Removal needs only the routing ID.
func TestRemoveSubscriptionUnreadableRow(t *testing.T) {
	env := testhub.Start(t)
	peer, _ := env.NewPeer("dot", "receiver")
	cb := testhub.NewCallback(t)
	sub := subscribe(t, env, cb, peer, nil, time.Time{})
	removalExec(t, env, `UPDATE event_subscriptions SET secret_enc='invalid-sealed-data',expires_at='invalid-time',types='{}' WHERE sub_id=?`, sub.ID)
	removalExec(t, env, `UPDATE meta SET value='invalid-schedule' WHERE key='events_refresh_schedule:' || ?`, sub.ID)
	if _, err := env.Events.Store.Get(context.Background(), sub.ID); err == nil {
		t.Fatal("fixture must fail subscription decoding")
	}
	if err := env.Events.Store.Remove(context.Background(), sub.ID); err != nil {
		t.Fatal(err)
	}
	wantRemoved(t, env, sub.ID)
	if cb.Requests() != 0 {
		t.Fatal("removal contacted callback")
	}
}
