package events_test

import (
	"bytes"
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/auth"
	"github.com/XertroV/vecnet/internal/hub"
	"github.com/XertroV/vecnet/internal/prefs"
	"github.com/XertroV/vecnet/internal/testhub"
)

func TestRetentionExhaustedParkedResubscribeAtReportedCursor(t *testing.T) {
	for _, outcome := range []string{"success", "ack", "expiry"} {
		t.Run(outcome, func(t *testing.T) {
			ctx := context.Background()
			env := testhub.Start(t)
			peer, secret := env.NewPeer("session", "recipient")
			_, sender := env.NewPeer("session", "sender")
			cb := testhub.NewCallback(t)
			sub := subscribe(t, env, cb, peer, nil, time.Time{})
			report(t, env, secret, "working")
			dly := send(t, env, sender, peer, "held-exhaustion", "wake_idle", "synthetic frozen callback")
			step(t, env)
			var seq int64
			var parked bool
			if err := env.Hub.DB.QueryRow(`SELECT seq,parked FROM event_outbox WHERE sub_id=? AND event_id=?`, sub.ID, dly).Scan(&seq, &parked); err != nil {
				t.Fatal(err)
			}
			if !parked {
				t.Fatal("held row was not parked")
			}
			report(t, env, secret, "idle")
			cb.OnPost(func(_ int, w http.ResponseWriter) { w.WriteHeader(http.StatusServiceUnavailable) })
			step(t, env)
			first := wantPosts(t, cb, 1)[0]
			for i := 1; i < hub.PushAttempts; i++ {
				env.Now.Advance(5 * time.Minute)
				step(t, env)
			}
			_, done, term := outbox(t, env, sub.ID, dly)
			if !done || term != "exhausted" {
				t.Fatal("parked row did not exhaust")
			}
			// Return exactly the hub's reported cursor, which may pass parked rows.
			current := get(t, env, sub.ID).Cursor
			reported, err := env.Events.Store.ReplayCursor(ctx, sub.ID, current)
			if err != nil {
				t.Fatal(err)
			}
			if reported.Seq < seq {
				t.Fatal("fixture did not pass parked row")
			}
			if outcome == "ack" {
				resp, _ := env.Do(secret, "POST", "/v1/ack", map[string]string{"cursor": reported.String()})
				if resp.StatusCode != http.StatusNoContent {
					t.Fatalf("ack status=%d", resp.StatusCode)
				}
			} else if outcome == "expiry" {
				env.Now.Advance(2 * 24 * time.Hour)
				if _, err := env.Hub.ExpireDeliveries(ctx, env.Now.Now()); err != nil {
					t.Fatal(err)
				}
			}
			refresh(t, env, sub, time.Time{})
			if err := env.Events.Store.SetCursor(ctx, sub.ID, reported, true); err != nil {
				t.Fatal(err)
			}
			var body string
			var attempts int
			if err := env.Hub.DB.QueryRow(`SELECT body,parked,attempts,done_at IS NOT NULL FROM event_outbox WHERE sub_id=? AND event_id=?`, sub.ID, dly).Scan(&body, &parked, &attempts, &done); err != nil {
				t.Fatal(err)
			}
			if done || attempts != 0 || !parked || !bytes.Equal([]byte(body), first.Body) {
				t.Fatal("resubscribe did not rearm the preserved parked row")
			}
			cb.OnPost(nil)
			step(t, env)
			posts := cb.Posts()
			if outcome == "success" {
				if len(posts) != hub.PushAttempts+1 || posts[hub.PushAttempts].ID != first.ID || !bytes.Equal(posts[hub.PushAttempts].Body, first.Body) {
					t.Fatal("retry did not preserve callback bytes and id")
				}
			} else {
				if len(posts) != hub.PushAttempts {
					t.Fatal("withdrawn callback retried")
				}
				_, done, term = outbox(t, env, sub.ID, dly)
				if !done || term != "gated" {
					t.Fatal("rearmed parked row was not withdrawn by its delivery gate")
				}
			}
			// Once no callback retry remains, an ack releases the aged owner prefix.
			current, err = env.Hub.Log.Current(ctx, []string{peer})
			if err != nil {
				t.Fatal(err)
			}
			if err := env.Hub.Log.Ack(ctx, nil, peer, "*", current); err != nil {
				t.Fatal(err)
			}
			env.Now.Advance(8 * 24 * time.Hour)
			step(t, env)
			if _, err := env.Hub.PruneEventSlice(ctx); err != nil {
				t.Fatal(err)
			}
			floor, err := env.Hub.Log.OldestFor(ctx, []string{peer})
			if err != nil || floor.Seq < seq {
				t.Fatalf("completed callback pinned prefix: floor=%v err=%v", floor, err)
			}
		})
	}
}

// Exercise hard-expiry consumers only after the shared service authorizes the class.
func approveHardRetention(t *testing.T, h *hub.Hub) {
	t.Helper()
	ctx := context.Background()
	operator := auth.Caller{Kind: "operator", PeerKind: "operator"}
	s, err := h.Preferences.ReadPreferences(ctx, operator)
	if err != nil {
		t.Fatal(err)
	}
	patch := prefs.Patch{Set: map[string]prefs.Value{"retention.archive_mode": prefs.Bool(false)}, ApplyExisting: true}
	preview, err := h.Preferences.PreviewPreferences(ctx, operator, s.ETag, patch)
	if err != nil {
		t.Fatal(err)
	}
	patch.Confirm = []string{preview.ConfirmToken}
	if _, err := h.Preferences.PatchPreferences(ctx, operator, s.ETag, patch); err != nil {
		t.Fatal(err)
	}
}
