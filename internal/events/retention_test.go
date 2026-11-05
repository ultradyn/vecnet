package events_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/eventlog"
	"github.com/XertroV/vecnet/internal/hub"
	"github.com/XertroV/vecnet/internal/testhub"
)

func TestOwnerRetentionTerminalSuppressionAndHardTruncation(t *testing.T) {
	env := testhub.Start(t)
	ctx := context.Background()
	dot, _ := env.NewPeer("dot", "one")
	other, _ := env.NewPeer("session", "other")
	_, sender := env.NewPeer("session", "sender")
	cb := testhub.NewCallback(t)
	cb.OnPost(func(_ int, w http.ResponseWriter) { w.WriteHeader(http.StatusGone) })
	sub := subscribe(t, env, cb, dot, nil, time.Time{})
	dly := send(t, env, sender, dot, "synthetic", "wake", "synthetic")
	step(t, env)
	var seq int64
	if err := env.Hub.DB.QueryRow(`SELECT seq FROM event_outbox WHERE event_id=?`, dly).Scan(&seq); err != nil {
		t.Fatal(err)
	}
	c, err := env.Hub.Log.Append(ctx, nil, other, "thread_updated", env.Hub.Now(), map[string]any{"thread_id": "thr_synthetic"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := env.Hub.DB.Exec(`DELETE FROM events WHERE owner=?`, other); err != nil {
		t.Fatal(err)
	}
	// A later unrelated owner's floor must not remove this peer's suppression.
	env.Now.Advance(25 * time.Hour)
	step(t, env)
	count := func() int {
		var n int
		if err := env.Hub.DB.QueryRow(`SELECT count(*) FROM event_outbox WHERE event_id=?`, dly).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if count() != 1 {
		t.Fatal("unrelated owner's floor erased terminal suppression")
	}
	if _, err := env.Hub.DB.Exec(`DELETE FROM events WHERE owner=? AND seq<=?`, dot, seq); err != nil {
		t.Fatal(err)
	}
	step(t, env)
	if count() != 0 {
		t.Fatal("owning peer floor did not release terminal suppression")
	}
	// skipPurged reports the owning floor, never another owner's higher floor.
	refresh(t, env, sub, time.Time{})
	if err := env.Events.Store.SetCursor(ctx, sub.ID, eventlog.Cursor{Epoch: c.Epoch}, false); err != nil {
		t.Fatal(err)
	}
	step(t, env)
	got := get(t, env, sub.ID)
	if !got.Truncated || got.Cursor.Seq != seq {
		t.Fatalf("skipPurged cursor=%v truncated=%v want %d", got.Cursor, got.Truncated, seq)
	}
}

func TestRetentionHardExpiryResubscribeReportsTruncation(t *testing.T) {
	env := testhub.StartWith(t, func(cfg *hub.Config) { cfg.UnackedEventRetention = 30 * 24 * time.Hour })
	approveHardRetention(t, env.Hub)
	dot, _ := env.NewPeer("dot", "recipient")
	cb := testhub.NewCallback(t)
	sub := subscribe(t, env, cb, dot, nil, time.Time{})
	c, err := env.Hub.Log.Append(context.Background(), nil, dot, "thread_updated", env.Hub.Now(), map[string]any{"thread_id": "thr_synthetic"})
	if err != nil {
		t.Fatal(err)
	}
	// The subscription expires without scanning or accepting this event.
	env.Now.Advance(31 * 24 * time.Hour)
	if _, err := env.Hub.PruneEventSlice(context.Background()); err != nil {
		t.Fatal(err)
	}
	floor, err := env.Hub.Log.OldestFor(context.Background(), []string{dot})
	if err != nil || floor != c {
		t.Fatalf("hard expiry floor=%v err=%v", floor, err)
	}
	refresh(t, env, sub, time.Time{})
	step(t, env)
	got := get(t, env, sub.ID)
	if !got.Truncated || got.Cursor != c {
		t.Fatal("resubscribe did not report owner truncation")
	}
}

func TestOwnerRetentionReplayCursorDoesNotCrossFloor(t *testing.T) {
	env := testhub.Start(t)
	ctx := context.Background()
	dot, _ := env.NewPeer("dot", "recipient")
	_, sender := env.NewPeer("session", "sender")
	cb := testhub.NewCallback(t)
	cb.OnPost(func(_ int, w http.ResponseWriter) { w.WriteHeader(http.StatusServiceUnavailable) })
	sub := subscribe(t, env, cb, dot, nil, time.Time{})
	send(t, env, sender, dot, "synthetic", "wake", "synthetic")
	step(t, env)
	var seq int64
	if err := env.Hub.DB.QueryRow(`SELECT seq FROM event_outbox WHERE sub_id=?`, sub.ID).Scan(&seq); err != nil {
		t.Fatal(err)
	}
	if _, err := env.Hub.DB.Exec(`DELETE FROM events WHERE owner=?`, dot); err != nil {
		t.Fatal(err)
	}
	cur, err := env.Events.Store.ReplayCursor(ctx, sub.ID, eventlog.Cursor{Epoch: 1, Seq: seq})
	if err != nil || cur.Seq != seq {
		t.Fatalf("replay crossed owner floor: %v %v", cur, err)
	}
}
