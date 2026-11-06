package events_test

import (
	"bytes"
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/eventlog"
	"github.com/XertroV/vecnet/internal/events"
	"github.com/XertroV/vecnet/internal/hub"
	"github.com/XertroV/vecnet/internal/testhub"
)

func TestRetentionSettledHeldCallbacksReleasePrefix(t *testing.T) {
	for _, outcome := range []string{"success", "ack"} {
		t.Run(outcome, func(t *testing.T) {
			ctx := context.Background()
			env := testhub.StartWith(t, func(cfg *hub.Config) {
				// This checks settled callback pins, not wall-clock slice limits.
				cfg.EventRetentionLimits = eventlog.RetentionLimits{Target: time.Minute}
			})
			peer, secret := env.NewPeer("session", "recipient")
			_, sender := env.NewPeer("session", "sender")
			cb := testhub.NewCallback(t)
			sub := subscribe(t, env, cb, peer, nil, time.Time{})
			report(t, env, secret, "working")
			dly := send(t, env, sender, peer, "held", "wake_idle", "synthetic held callback")
			step(t, env)
			var parked bool
			var seq int64
			if err := env.Hub.DB.QueryRow(`SELECT parked,seq FROM event_outbox WHERE sub_id=? AND event_id=?`, sub.ID, dly).Scan(&parked, &seq); err != nil {
				t.Fatal(err)
			}
			if !parked {
				t.Fatal("held callback was not parked")
			}
			if outcome == "success" {
				report(t, env, secret, "idle")
			} else {
				resp, _ := env.Do(secret, "POST", "/v1/ack", map[string]string{"cursor": (eventlog.Cursor{Epoch: sub.Cursor.Epoch, Seq: seq}).String()})
				if resp.StatusCode != http.StatusNoContent {
					t.Fatalf("ack status=%d", resp.StatusCode)
				}
			}
			step(t, env)
			want := 0
			if outcome == "success" {
				want = 1
			}
			wantPosts(t, cb, want)
			_, done, term := outbox(t, env, sub.ID, dly)
			if !done || outcome == "ack" && term != "gated" {
				t.Fatal("held callback did not settle")
			}
			// A real pull ack makes the settled event eligible, independently of push acceptance.
			cur, err := env.Hub.Log.Current(ctx, []string{peer})
			if err != nil {
				t.Fatal(err)
			}
			if err := env.Hub.Log.Ack(ctx, nil, peer, "*", cur); err != nil {
				t.Fatal(err)
			}
			env.Now.Advance(8 * 24 * time.Hour)
			step(t, env) // Aged outbox cleanup uses the real worker path.
			var count int
			if err := env.Hub.DB.QueryRow(`SELECT count(*) FROM event_outbox WHERE sub_id=? AND event_id=?`, sub.ID, dly).Scan(&count); err != nil {
				t.Fatal(err)
			}
			if count != 0 {
				t.Error("settled held callback survived aged cleanup")
			}
			if _, err := env.Hub.PruneEventSlice(ctx); err != nil {
				t.Fatal(err)
			}
			floor, err := env.Hub.Log.OldestFor(ctx, []string{peer})
			if err != nil || floor.Seq < seq {
				t.Fatalf("settled held callback pinned prefix: floor=%v err=%v", floor, err)
			}
		})
	}
}

func TestRetentionExhaustedLegacyCallbackSurvivesReplay(t *testing.T) {
	for _, storage := range []string{"inline", "reference_storage"} {
		t.Run(storage, retentionExhaustedLegacyCallbackSurvivesReplay)
	}
}

func retentionExhaustedLegacyCallbackSurvivesReplay(t *testing.T) {
	env, _ := guardEnv(t)
	ctx := context.Background()
	guardExec(t, env, `UPDATE events SET data=json_remove(data,'$.message.sender_info') WHERE seq=9`)
	cb := testhub.NewCallback(t)
	sub := guardSubscription(t, env, cb)
	if err := env.Events.Store.SetCursor(ctx, sub.ID, eventlog.Cursor{Epoch: 1, Seq: 8}, false); err != nil {
		t.Fatal(err)
	}
	cb.OnPost(func(_ int, w http.ResponseWriter) { w.WriteHeader(http.StatusServiceUnavailable) })
	step(t, env)
	first := wantPosts(t, cb, 1)[0]
	guardBytes(t, "legacy-outbox-before-0018.json", first.Body)
	guardExec(t, env, `UPDATE events SET data=json_set(events.data,'$.message.sender_info',json(json_extract(m.body,'$.sender_info'))) FROM messages m WHERE events.seq=9 AND m.msg_id=json_extract(events.data,'$.message.message_id')`)
	hydrateReferenceStorage(t, env)
	for i := 1; i < hub.PushAttempts; i++ {
		env.Now.Advance(5 * time.Minute)
		step(t, env)
	}
	posts := wantPosts(t, cb, hub.PushAttempts)
	for _, post := range posts {
		if !bytes.Equal(post.Body, first.Body) {
			t.Fatal("retry changed frozen bytes")
		}
	}
	_, done, term := outbox(t, env, sub.ID, first.ID)
	if !done || term != "exhausted" {
		t.Fatal("callback did not exhaust")
	}
	var body, created string
	if err := env.Hub.DB.QueryRow(`SELECT body,created_at FROM event_outbox WHERE sub_id=? AND event_id=?`, sub.ID, first.ID).Scan(&body, &created); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal([]byte(body), first.Body) {
		t.Error("exhaustion erased exact callback body")
	}
	// Put the exhausted callback itself at the owner's retention prefix; no
	// earlier queued delivery may mask a missing exhausted-row safety pin.
	guardExec(t, env, `DELETE FROM events WHERE owner=? AND seq<9`, guardB)
	base, err := env.Hub.Log.OldestFor(ctx, []string{guardB})
	if err != nil {
		t.Fatal(err)
	}
	var oldest int64
	if err := env.Hub.DB.QueryRow(`SELECT min(seq) FROM events WHERE owner=?`, guardB).Scan(&oldest); err != nil || oldest != 9 {
		t.Fatalf("exhausted callback is not first: seq=%d err=%v", oldest, err)
	}
	// Settle the delivery independently so only the retryable callback pins it.
	guardExec(t, env, `UPDATE deliveries SET state='accepted',held=0 WHERE dly_id=?`, first.ID)
	if err := env.Hub.Log.Ack(ctx, nil, guardB, "*", eventlog.Cursor{Epoch: 1, Seq: 12}); err != nil {
		t.Fatal(err)
	}
	env.Now.Advance(8 * 24 * time.Hour)
	step(t, env)
	if _, err := env.Hub.PruneEventSlice(ctx); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := env.Hub.DB.QueryRow(`SELECT count(*) FROM events WHERE seq=9`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatal("retryable exhaustion lost its event")
	}
	floor, err := env.Hub.Log.OldestFor(ctx, []string{guardB})
	if err != nil || floor != base {
		t.Fatalf("exhausted callback lost its prefix pin: floor=%v err=%v", floor, err)
	}
	// Restart the hub/worker on the same disposable DB; no materialization cache survives.
	restarted, err := hub.New(env.Hub.DB, env.Hub.Cfg, env.Now.Now)
	if err != nil {
		t.Fatal(err)
	}
	env.Hub = restarted
	env.Events = &events.Worker{Hub: restarted, Store: events.NewStore(restarted), Now: env.Now.Now}
	refresh(t, env, sub, time.Time{})
	if err := env.Events.Store.SetCursor(ctx, sub.ID, eventlog.Cursor{Epoch: 1, Seq: 8}, false); err != nil {
		t.Fatal(err)
	}
	// A second rewind must also preserve the rearmed (now pending) record.
	if err := env.Events.Store.SetCursor(ctx, sub.ID, eventlog.Cursor{Epoch: 1, Seq: 8}, false); err != nil {
		t.Fatal(err)
	}
	var replayBody, replayCreated string
	var attempts int
	var replayDone bool
	if err := env.Hub.DB.QueryRow(`SELECT body,created_at,attempts,done_at IS NOT NULL FROM event_outbox WHERE sub_id=? AND event_id=?`, sub.ID, first.ID).Scan(&replayBody, &replayCreated, &attempts, &replayDone); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal([]byte(replayBody), first.Body) || replayCreated != created || attempts != 0 || replayDone {
		t.Fatal("rewind did not reuse frozen row with a renewed budget")
	}
	cb.OnPost(nil)
	step(t, env)
	posts = cb.Posts()
	if len(posts) <= hub.PushAttempts {
		t.Fatal("rewound callback was not retried")
	}
	if posts[hub.PushAttempts].ID != first.ID || !bytes.Equal(posts[hub.PushAttempts].Body, first.Body) {
		t.Fatal("replay rebuilt legacy callback instead of retrying frozen bytes")
	}
	var terminal *string
	if err := env.Hub.DB.QueryRow(`SELECT terminal FROM event_outbox WHERE sub_id=? AND event_id=?`, sub.ID, first.ID).Scan(&terminal); err != nil {
		t.Fatal(err)
	}
	if terminal != nil {
		t.Fatal("rearmed callback did not settle successfully")
	}
	if _, err := env.Hub.PruneEventSlice(ctx); err != nil {
		t.Fatal(err)
	}
	floor, err = env.Hub.Log.OldestFor(ctx, []string{guardB})
	if err != nil || floor.Seq < 9 {
		t.Fatalf("successful replay did not release prefix: floor=%v err=%v", floor, err)
	}
}
