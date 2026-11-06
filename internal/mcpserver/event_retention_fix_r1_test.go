package mcpserver_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/eventlog"
	"github.com/XertroV/vecnet/internal/events"
	"github.com/XertroV/vecnet/internal/hub"
	"github.com/XertroV/vecnet/internal/testhub"
	"github.com/XertroV/vecnet/internal/webhook"
)

func TestRetentionLegacyErasedExhaustionBlocksReplay(t *testing.T) {
	for _, mode := range []string{"rewind", "refresh", "head"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			env := testhub.Start(t)
			peer, secret := env.NewPeer("session", "recipient")
			_, sender := env.NewPeer("session", "sender")
			cb := testhub.NewCallback(t)
			key := webhook.NewSecret()
			sub := subscribeOK(t, env, secret, subParams(cb.URL, key, nil))
			cb.OnPost(func(_ int, w http.ResponseWriter) { w.WriteHeader(http.StatusServiceUnavailable) })
			dly := sendPriority(t, env, sender, peer, "legacy-erased", "wake")
			for range hub.PushAttempts {
				stepEvents(t, env)
				env.Now.Advance(5 * time.Minute)
			}
			// The previous binary cleared bodies even for terminal=exhausted.
			// Model that shipped on-disk state, rather than new-binary exhaustion.
			res, err := env.Hub.DB.Exec(`UPDATE event_outbox SET body='' WHERE sub_id=? AND event_id=? AND terminal='exhausted' AND done_at IS NOT NULL`, sub.ID, dly)
			if err != nil {
				t.Fatal(err)
			}
			if n, _ := res.RowsAffected(); n != 1 {
				t.Fatal("fixture did not reach legacy exhaustion")
			}
			before, err := env.Events.Store.Get(ctx, sub.ID)
			if err != nil {
				t.Fatal(err)
			}
			cur, err := eventlog.ParseCursor(sub.Cursor)
			if err != nil {
				t.Fatal(err)
			}
			if err := env.Events.Store.SetCursor(ctx, sub.ID, cur, false); !errors.Is(err, events.ErrReplayUnavailable) {
				t.Errorf("Store.SetCursor did not report unavailable replay: %v", err)
			}
			if _, err := env.Events.Store.ReplayCursor(ctx, sub.ID, before.Cursor); !errors.Is(err, events.ErrReplayUnavailable) {
				t.Errorf("Store.ReplayCursor did not report unavailable replay: %v", err)
			}
			extra := map[string]any{}
			if mode == "rewind" {
				extra["cursor"] = sub.Cursor
			} else if mode == "head" {
				extra["cursor"] = "head"
			}
			r := rpc2026(t, env, secret, "events/subscribe", subParams(cb.URL, key, extra))
			var detail struct {
				Reason string `json:"reason"`
			}
			if r.Error != nil {
				_ = json.Unmarshal(r.Error.Data, &detail)
			}
			if r.Error == nil || detail.Reason != "replay_unavailable" {
				t.Errorf("subscribe did not expose replay_unavailable: error=%+v", r.Error)
			}
			var seq int64
			var body, terminal string
			var done bool
			if err := env.Hub.DB.QueryRow(`SELECT seq,body,terminal,done_at IS NOT NULL FROM event_outbox WHERE sub_id=? AND event_id=?`, sub.ID, dly).Scan(&seq, &body, &terminal, &done); err != nil {
				t.Fatal(err)
			}
			if body != "" || terminal != "exhausted" || !done {
				t.Fatal("blocked replay changed the legacy row")
			}
			after, err := env.Events.Store.Get(ctx, sub.ID)
			if err != nil || after.Cursor != before.Cursor {
				t.Fatal("blocked replay moved the durable cursor")
			}
			// Isolate the exhausted callback pin from delivery retry pins.
			if _, err := env.Hub.DB.Exec(`UPDATE deliveries SET state='accepted',held=0 WHERE dly_id=?`, dly); err != nil {
				t.Fatal(err)
			}
			if err := env.Hub.Log.Ack(ctx, nil, peer, "*", eventlog.Cursor{Epoch: cur.Epoch, Seq: seq}); err != nil {
				t.Fatal(err)
			}
			env.Now.Advance(8 * 24 * time.Hour)
			stepEvents(t, env)
			if _, err := env.Hub.PruneEventSlice(ctx); err != nil {
				t.Fatal(err)
			}
			var count int
			if err := env.Hub.DB.QueryRow(`SELECT count(*) FROM events WHERE seq=?`, seq).Scan(&count); err != nil || count != 1 {
				t.Fatalf("unrecoverable callback lost its event pin: count=%d err=%v", count, err)
			}
		})
	}
}
