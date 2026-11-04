package hub_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/hub"
	"github.com/XertroV/vecnet/internal/ids"
	"github.com/XertroV/vecnet/internal/testhub"
)

func nextActivity(t *testing.T, s *hub.ActivitySub) hub.ActivityEvent {
	t.Helper()
	select {
	case ev := <-s.C:
		return ev
	case <-time.After(5 * time.Second):
		t.Fatal("no activity event")
	}
	return hub.ActivityEvent{}
}

func noActivity(t *testing.T, s *hub.ActivitySub) {
	t.Helper()
	select {
	case ev := <-s.C:
		t.Fatalf("unexpected activity %+v", ev)
	default:
	}
}

func TestActivityFeedFanOut(t *testing.T) {
	var f hub.ActivityFeed
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a, b := f.Subscribe(ctx), f.Subscribe(ctx)
	ev := hub.ActivityEvent{Type: "message", ThreadID: "thr_1", MessageID: "msg_1", Sender: "peer_1", Kind: "message", At: "2026-11-04T00:00:00.000Z"}
	f.Publish(ev)
	for _, s := range []*hub.ActivitySub{a, b} {
		if got := nextActivity(t, s); got != ev {
			t.Fatalf("got %+v, want %+v", got, ev)
		}
		if s.TakeLost() {
			t.Fatal("a reading subscriber was marked lost")
		}
	}
}

// A subscriber that stops reading loses events and is marked for resync;
// the publisher never waits on it and the other subscriber gets everything.
func TestActivityFeedSlowSubscriber(t *testing.T) {
	var f hub.ActivityFeed
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	slow, fast := f.Subscribe(ctx), f.Subscribe(ctx)
	got := 0
	done := make(chan struct{})
	go func() {
		defer close(done)
		for range hub.ActivityBuffer * 3 {
			<-fast.C
			got++
		}
	}()
	published := make(chan struct{})
	go func() {
		for i := range hub.ActivityBuffer * 3 {
			f.Publish(hub.ActivityEvent{Type: "message", ThreadID: "thr_1", MessageID: ids.New(ids.Msg), At: time.Unix(int64(i), 0).UTC().Format(time.RFC3339)})
			if i%hub.ActivityBuffer == hub.ActivityBuffer-1 {
				// Let the fast reader drain; the slow one never does.
				for len(fast.C) > 0 {
					time.Sleep(time.Millisecond)
				}
			}
		}
		close(published)
	}()
	select {
	case <-published:
	case <-time.After(10 * time.Second):
		t.Fatal("Publish blocked on a full subscriber")
	}
	<-done
	if got != hub.ActivityBuffer*3 {
		t.Fatalf("fast subscriber got %d events", got)
	}
	if len(slow.C) != hub.ActivityBuffer || !slow.TakeLost() {
		t.Fatalf("slow subscriber: %d queued, lost not marked", len(slow.C))
	}
	if slow.TakeLost() {
		t.Fatal("TakeLost did not clear the mark")
	}
}

func TestActivityFeedUnsubscribeOnCancel(t *testing.T) {
	var f hub.ActivityFeed
	ctx, cancel := context.WithCancel(context.Background())
	keep, stop := context.WithCancel(context.Background())
	defer stop()
	f.Subscribe(ctx)
	f.Subscribe(keep)
	if f.Subscribers() != 2 {
		t.Fatalf("subscribers = %d", f.Subscribers())
	}
	cancel()
	deadline := time.Now().Add(5 * time.Second)
	for f.Subscribers() != 1 {
		if time.Now().After(deadline) {
			t.Fatalf("subscribers = %d after cancel", f.Subscribers())
		}
		time.Sleep(time.Millisecond)
	}
}

// Only a committed message is published: a send whose transaction rolls back,
// a rate-limit cut and an idempotent replay publish nothing.
func TestActivityPublishedAfterCommit(t *testing.T) {
	env := testhub.Start(t)
	a, asec := env.NewPeer("session", "A")
	b, _ := env.NewPeer("session", "B")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sub := env.Hub.Activity.Subscribe(ctx)

	if _, err := env.Hub.DB.Exec(`CREATE TRIGGER reject_send BEFORE INSERT ON audit
		WHEN NEW.action = 'send' BEGIN SELECT RAISE(ABORT, 'test send failure'); END`); err != nil {
		t.Fatal(err)
	}
	if status, _, raw := sendHTTP(t, env, asec, req("rolled-back", b)); status != http.StatusInternalServerError {
		t.Fatalf("send despite failure: %d %s", status, raw)
	}
	noActivity(t, sub)
	if _, err := env.Hub.DB.Exec(`DROP TRIGGER reject_send`); err != nil {
		t.Fatal(err)
	}

	body := req("committed", b)
	sent := mustSend(t, env, asec, body)
	got := nextActivity(t, sub)
	if got.Type != "message" || got.ThreadID != sent.ThreadID || got.MessageID != sent.MessageID || got.Sender != a || got.Kind != "message" || got.At == "" {
		t.Fatalf("activity %+v for %+v", got, sent)
	}
	if status, _, raw := sendHTTP(t, env, asec, body); status != http.StatusOK {
		t.Fatalf("replay = %d %s", status, raw)
	}
	noActivity(t, sub)

	env.SetLimits(a, 1, 1000)
	if status, _, raw := sendHTTP(t, env, asec, req("over-limit", b)); status != http.StatusTooManyRequests {
		t.Fatalf("rate cut = %d %s", status, raw)
	}
	noActivity(t, sub)
}

// The hub's approval-expiry notice is a stored message too; a rename
// publishes a thread entry with no title.
func TestActivityExpiryAndRename(t *testing.T) {
	env := testhub.Start(t)
	_, asec := env.NewPeer("session", "A")
	b, _ := env.NewPeer("session", "B")
	apr := ids.New(ids.Apr)
	sent := mustSend(t, env, asec, approvalReq("ask", apr, env.Now.Now().Add(time.Minute), []string{"yes", "no"}, b))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sub := env.Hub.Activity.Subscribe(ctx)
	env.Now.Advance(2 * time.Minute)
	if n, err := env.Hub.ExpireApprovals(context.Background(), env.Now.Now()); err != nil || n != 1 {
		t.Fatalf("expire = %d, %v", n, err)
	}
	got := nextActivity(t, sub)
	if got.Type != "message" || got.ThreadID != sent.ThreadID || got.Sender != hub.HubSender || got.Kind != "approval_response" || got.MessageID == "" {
		t.Fatalf("expiry activity %+v", got)
	}
	title := "renamed"
	if err := env.Hub.PatchThread(context.Background(), sent.ThreadID, hub.ThreadPatch{Title: &title, TitleSet: true}); err != nil {
		t.Fatal(err)
	}
	got = nextActivity(t, sub)
	if got != (hub.ActivityEvent{Type: "thread", ThreadID: sent.ThreadID, At: got.At}) || got.At == "" {
		t.Fatalf("rename activity %+v", got)
	}
}
