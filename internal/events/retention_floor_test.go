package events_test

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/events"
	"github.com/XertroV/vecnet/internal/hub"
	"github.com/XertroV/vecnet/internal/testhub"
)

func TestOwnerRetentionFloorEchoPreservesCallback(t *testing.T) {
	for _, name := range []string{"pending_at_floor", "pending_below_floor", "exhausted_at_floor", "exhausted_below_floor"} {
		exhausted := strings.HasPrefix(name, "exhausted")
		t.Run(name, func(t *testing.T) {
			env := testhub.Start(t)
			ctx := context.Background()
			peer, _ := env.NewPeer("dot", "recipient")
			cb := testhub.NewCallback(t)
			cb.OnPost(func(_ int, w http.ResponseWriter) { w.WriteHeader(http.StatusServiceUnavailable) })
			sub := subscribe(t, env, cb, peer, map[string]any{"types": []string{"thread_updated"}}, time.Time{})
			if _, err := env.Hub.Log.Append(ctx, nil, peer, "thread_updated", env.Hub.Now(), map[string]any{"thread_id": "thr_frozen"}); err != nil {
				t.Fatal(err)
			}
			step(t, env)
			first := wantPosts(t, cb, 1)
			if strings.Contains(name, "below") {
				if _, err := env.Hub.Log.Append(ctx, nil, peer, "presence", env.Hub.Now(), map[string]any{}); err != nil {
					t.Fatal(err)
				}
			}
			if exhausted {
				for i := 1; i < hub.PushAttempts; i++ {
					env.Now.Advance(5 * time.Minute)
					step(t, env)
				}
				for _, p := range first {
					_, done, term := outbox(t, env, sub.ID, p.ID)
					if !done || term != "exhausted" {
						t.Fatal("callback did not exhaust")
					}
				}
			}
			// A legacy/external purge has left exact durable callback copies both
			// below and at the owning peer's floor.
			if _, err := env.Hub.DB.Exec(`DELETE FROM events WHERE owner=?`, peer); err != nil {
				t.Fatal(err)
			}
			floor, err := env.Hub.Log.OldestFor(ctx, []string{peer})
			if err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 2; i++ {
				reported, err := env.Events.Store.ReplayCursor(ctx, sub.ID, get(t, env, sub.ID).Cursor)
				if err != nil || reported != floor {
					t.Fatalf("reported=%v floor=%v err=%v", reported, floor, err)
				}
				refresh(t, env, sub, time.Time{})
				if err := env.Events.Store.SetCursor(ctx, sub.ID, reported, true); err != nil {
					t.Fatal(err)
				}
				for _, p := range first {
					var body string
					var done bool
					if err := env.Hub.DB.QueryRow(`SELECT body,done_at IS NOT NULL FROM event_outbox WHERE sub_id=? AND event_id=?`, sub.ID, p.ID).Scan(&body, &done); err != nil {
						t.Fatal("floor echo lost durable callback: ", err)
					}
					if done || !bytes.Equal([]byte(body), p.Body) {
						t.Fatal("floor echo failed to preserve/rearm frozen callback")
					}
				}
			}
			before := len(cb.Posts())
			cb.OnPost(nil)
			env.Now.Advance(5 * time.Minute)
			if sent := step(t, env); sent != 1 {
				t.Fatalf("successful replays=%d want 1", sent)
			}
			posts := wantPosts(t, cb, before+1)
			for i, p := range posts[before:] {
				if p.ID != first[i].ID || !bytes.Equal(p.Body, first[i].Body) {
					t.Fatal("replay changed callback bytes or id")
				}
			}
			if step(t, env) != 0 {
				t.Fatal("settled callbacks replayed again")
			}
		})
	}
}

func TestOwnerRetentionEnqueueRechecksPausedAndPurgedRead(t *testing.T) {
	for _, resume := range []bool{false, true} {
		name := "paused"
		if resume {
			name = "resumed_before_enqueue"
		}
		t.Run(name, func(t *testing.T) {
			env := testhub.StartWith(t, func(cfg *hub.Config) { cfg.UnackedEventRetention = 30 * 24 * time.Hour })
			approveHardRetention(t, env.Hub)
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			peer, _ := env.NewPeer("dot", "recipient")
			cb := testhub.NewCallback(t)
			sub := subscribe(t, env, cb, peer, map[string]any{"types": []string{"thread_updated"}}, time.Time{})
			c, err := env.Hub.Log.Append(ctx, nil, peer, "thread_updated", env.Hub.Now().Add(-40*24*time.Hour), map[string]any{"thread_id": "thr_buffered"})
			if err != nil {
				t.Fatal(err)
			}
			read, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
			var stepErr error
			var calls atomic.Int32
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			// Step samples Now first for housekeeping, then tail samples it after
			// Log.Read/body materialization, immediately before the enqueue writer.
			worker := &events.Worker{Hub: env.Hub, Store: env.Events.Store, Now: func() time.Time {
				if calls.Add(1) == 2 {
					close(read)
					select {
					case <-release:
					case <-ctx.Done():
					}
				}
				return env.Now.Now()
			}}
			go func() { defer close(done); _, stepErr = worker.Step(ctx) }()
			defer func() { unblock(); <-done }()
			select {
			case <-read:
			case <-done:
				t.Fatalf("worker ended before read barrier: %v", stepErr)
			case <-ctx.Done():
				t.Fatal("read barrier timed out")
			}
			if err := env.Events.Store.Pause(ctx, sub.ID); err != nil {
				t.Fatal(err)
			}
			if _, err := env.Hub.PruneEventSlice(ctx); err != nil {
				t.Fatal(err)
			}
			floor, err := env.Hub.Log.OldestFor(ctx, []string{peer})
			if err != nil || floor != c {
				t.Fatalf("pause/prune floor=%v want=%v err=%v", floor, c, err)
			}
			if resume {
				if err := env.Events.Store.Resume(ctx, sub.ID); err != nil {
					t.Fatal(err)
				}
			}
			unblock()
			select {
			case <-done:
				if stepErr != nil {
					t.Fatal(stepErr)
				}
			case <-ctx.Done():
				t.Fatal("enqueue did not finish")
			}
			var n int
			if err := env.Hub.DB.QueryRow(`SELECT count(*) FROM event_outbox WHERE sub_id=?`, sub.ID).Scan(&n); err != nil {
				t.Fatal(err)
			}
			if n != 0 || get(t, env, sub.ID).Cursor != sub.Cursor {
				t.Fatal("stale read enqueued or advanced after pause/purge")
			}
			wantPosts(t, cb, 0)
		})
	}
}
