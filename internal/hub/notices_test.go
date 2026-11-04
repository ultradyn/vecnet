package hub_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/auth"
	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/eventlog"
	"github.com/XertroV/vecnet/internal/hub"
	"github.com/XertroV/vecnet/internal/hubid"
	"github.com/XertroV/vecnet/internal/testhub"
)

func postNotice(t *testing.T, e *testhub.Env, r hub.NoticeRequest) envelope.HubNotice {
	t.Helper()
	n, err := e.Hub.PostNotice(context.Background(), auth.Caller{Kind: "operator", PrincipalID: "operator"}, r)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(n)
	if err != nil {
		t.Fatal(err)
	}
	if err := envelope.Validate("hubNotice", raw); err != nil {
		t.Fatal(err)
	}
	if err := hubid.VerifyNotice(e.Hub.IdentityKey().Pubkey(), n); err != nil {
		t.Fatal(err)
	}
	return n
}

func noticeEvents(t *testing.T, e *testhub.Env, owners ...string) []envelope.HubNoticeEvent {
	t.Helper()
	events, err := e.Hub.Log.Read(context.Background(), owners, eventlog.Cursor{Epoch: e.Hub.Log.Epoch()}, 1000)
	if err != nil {
		t.Fatal(err)
	}
	out := []envelope.HubNoticeEvent{}
	for _, ev := range events {
		if ev.Type == "hub_notice" {
			raw, err := json.Marshal(ev)
			if err != nil {
				t.Fatal(err)
			}
			if err := envelope.Validate("event", raw); err != nil {
				t.Fatal(err)
			}
			var n envelope.HubNoticeEvent
			if err := json.Unmarshal(ev.Data, &n); err != nil {
				t.Fatal(err)
			}
			if err := hubid.VerifyNotice(e.Hub.IdentityKey().Pubkey(), n.Notice); err != nil {
				t.Fatal(err)
			}
			out = append(out, n)
		}
	}
	return out
}

func TestNoticeServiceOperatorOnly(t *testing.T) {
	e := testhub.Start(t)
	ctx := context.Background()
	n := postNotice(t, e, hub.NoticeRequest{Kind: "maintenance", Message: "hello"})
	for _, kind := range []string{"peer", "principal", ""} {
		_, err := e.Hub.PostNotice(ctx, auth.Caller{Kind: kind}, hub.NoticeRequest{Kind: "maintenance", Message: "hello"})
		var api *hub.APIError
		if !errors.As(err, &api) || api.Status != 403 || api.Code != "forbidden" {
			t.Fatalf("post %s: %v", kind, err)
		}
		err = e.Hub.ClearNotice(ctx, auth.Caller{Kind: kind}, n.NoticeID)
		if !errors.As(err, &api) || api.Status != 403 || api.Code != "forbidden" {
			t.Fatalf("clear %s: %v", kind, err)
		}
	}
}

func TestNoticeFanOutEveryEnabledPeer(t *testing.T) {
	e := testhub.Start(t)
	ctx := context.Background()
	prn, principalSecret := e.NewPrincipal("notice owner")
	var peers []string
	for _, name := range []string{"one", "two"} {
		p, _, err := e.Hub.AddPeer(ctx, "session", name, prn)
		if err != nil {
			t.Fatal(err)
		}
		peers = append(peers, p)
	}
	disabled, _ := e.NewPeer("session", "disabled")
	if _, err := e.Hub.DB.Exec(`UPDATE peers SET disabled=1 WHERE peer_id=?`, disabled); err != nil {
		t.Fatal(err)
	}
	n := postNotice(t, e, hub.NoticeRequest{Kind: "maintenance", Message: "hello"})
	for _, peer := range append(peers, e.OperatorPeer) {
		events := noticeEvents(t, e, peer)
		if len(events) != 1 || events[0].Change != "posted" || events[0].Notice.NoticeID != n.NoticeID {
			t.Fatalf("%s: %+v", peer, events)
		}
	}
	if events := noticeEvents(t, e, disabled); len(events) != 0 {
		t.Fatalf("disabled: %+v", events)
	}
	events := noticeEvents(t, e, peers...)
	if len(events) != 2 || events[0].Notice.NoticeID != events[1].Notice.NoticeID {
		t.Fatalf("principal owners: %+v", events)
	}
	resp, raw := e.Do(principalSecret, "GET", "/v1/events?after="+(eventlog.Cursor{Epoch: e.Hub.Log.Epoch()}).String()+"&wait=0", nil)
	if resp.StatusCode != 200 {
		t.Fatalf("principal stream: %d %s", resp.StatusCode, raw)
	}
	var page struct {
		Events []envelope.Event `json:"events"`
	}
	if err := json.Unmarshal(raw, &page); err != nil {
		t.Fatal(err)
	}
	copies := 0
	for _, ev := range page.Events {
		if ev.Type == "hub_notice" {
			var data envelope.HubNoticeEvent
			if err := json.Unmarshal(ev.Data, &data); err != nil {
				t.Fatal(err)
			}
			if data.Notice.NoticeID != n.NoticeID || data.Change != "posted" {
				t.Fatalf("stream event: %+v", data)
			}
			copies++
		}
	}
	if copies != 2 {
		t.Fatalf("principal stream got %d copies, want 2", copies)
	}
}

func TestNoticeReplacesSameKind(t *testing.T) {
	e := testhub.Start(t)
	ctx := context.Background()
	first := postNotice(t, e, hub.NoticeRequest{Kind: "maintenance", Message: "first"})
	moving := postNotice(t, e, hub.NoticeRequest{Kind: "moving", Message: "move", NewURL: "https://next.example"})
	e.Now.Advance(time.Second)
	second := postNotice(t, e, hub.NoticeRequest{Kind: "maintenance", Message: "second"})
	active, err := e.Hub.ActiveNotices(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(active) != 2 || active[0].NoticeID != moving.NoticeID || active[1].NoticeID != second.NoticeID {
		t.Fatalf("active: %+v", active)
	}
	events := noticeEvents(t, e, e.OperatorPeer)
	if len(events) != 4 || events[2].Change != "cleared" || events[2].Notice.NoticeID != first.NoticeID || events[2].Notice.ClearedHow != "replaced" || events[2].Notice.ClearedAt == "" || events[3].Notice.NoticeID != second.NoticeID {
		t.Fatalf("events: %+v", events)
	}
}

func TestExpireNotices(t *testing.T) {
	e := testhub.Start(t)
	ctx := context.Background()
	peer, _ := e.NewPeer("session", "expiry")
	n := postNotice(t, e, hub.NoticeRequest{Kind: "maintenance", Message: "expiry", ExpiresAt: envelope.FormatTime(e.Now.Now().Add(time.Minute))})
	e.Now.Advance(time.Minute)
	e.Hub.Tick(ctx, false)
	active, err := e.Hub.ActiveNotices(ctx)
	if err != nil || len(active) != 0 {
		t.Fatalf("active: %+v %v", active, err)
	}
	for _, owner := range []string{peer, e.OperatorPeer} {
		events := noticeEvents(t, e, owner)
		if len(events) != 2 || events[1].Change != "cleared" || events[1].Notice.NoticeID != n.NoticeID || events[1].Notice.ClearedHow != "expired" {
			t.Fatalf("events: %+v", events)
		}
	}
	if count, err := e.Hub.ExpireNotices(ctx); err != nil || count != 0 {
		t.Fatalf("repeat: %d %v", count, err)
	}
}

func TestClearOnBoot(t *testing.T) {
	for _, own := range []bool{true, false} {
		t.Run(map[bool]string{true: "arrived", false: "elsewhere"}[own], func(t *testing.T) {
			e := testhub.Start(t)
			ctx := context.Background()
			peer, _ := e.NewPeer("session", "boot reader")
			restart := postNotice(t, e, hub.NoticeRequest{Kind: "restart", Message: "restart"})
			maintenance := postNotice(t, e, hub.NoticeRequest{Kind: "maintenance", Message: "maintenance"})
			moving := postNotice(t, e, hub.NoticeRequest{Kind: "moving", Message: "moving", NewURL: "https://next.example"})
			if own {
				e.Hub.Cfg.PublicURL = "HTTPS://NEXT.EXAMPLE:443/"
			}
			count, err := e.Hub.ClearOnBoot(ctx)
			want := 1
			if own {
				want = 2
			}
			if err != nil || count != want {
				t.Fatalf("boot: %d %v", count, err)
			}
			active, err := e.Hub.ActiveNotices(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if len(active) != 3-want {
				t.Fatalf("active: %+v", active)
			}
			found := false
			for _, n := range active {
				if n.NoticeID == maintenance.NoticeID {
					found = true
				}
				if own && n.NoticeID == moving.NoticeID {
					t.Fatal("moving not cleared")
				}
				if n.NoticeID == restart.NoticeID {
					t.Fatal("restart not cleared")
				}
			}
			if !found {
				t.Fatal("maintenance cleared")
			}
			for _, owner := range []string{e.OperatorPeer, peer} {
				cleared := map[string]bool{}
				for _, ev := range noticeEvents(t, e, owner) {
					if ev.Change == "cleared" {
						if ev.Notice.ClearedHow != "boot" || ev.Notice.ClearedAt == "" {
							t.Fatalf("boot event: %+v", ev)
						}
						cleared[ev.Notice.NoticeID] = true
					}
				}
				if len(cleared) != want || !cleared[restart.NoticeID] || cleared[moving.NoticeID] != own {
					t.Fatalf("boot fan-out to %s: %v", owner, cleared)
				}
			}
			if count, err := e.Hub.ClearOnBoot(ctx); err != nil || count != 0 {
				t.Fatalf("repeat: %d %v", count, err)
			}
		})
	}
}

func TestEnsureMovingNotice(t *testing.T) {
	e := testhub.Start(t)
	ctx := context.Background()
	n, err := e.Hub.EnsureMovingNotice(ctx, "HTTPS://NEXT.EXAMPLE:443/")
	if err != nil {
		t.Fatal(err)
	}
	if n.Kind != "moving" || n.At != envelope.FormatTime(e.Now.Now()) || n.NewURL != "https://next.example" {
		t.Fatalf("notice: %+v", n)
	}
	if err := hubid.VerifyNotice(e.Hub.IdentityKey().Pubkey(), n); err != nil {
		t.Fatal(err)
	}
	again, err := e.Hub.EnsureMovingNotice(ctx, "https://next.example")
	if err != nil || again.NoticeID != n.NoticeID {
		t.Fatalf("repeat: %+v %v", again, err)
	}
	var actor string
	var count int
	if err := e.Hub.DB.QueryRow(`SELECT actor FROM audit WHERE action='notice_post'`).Scan(&actor); err != nil {
		t.Fatal(err)
	}
	if err := e.Hub.DB.QueryRow(`SELECT count(*) FROM audit WHERE action='notice_post'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if actor != "hub" || count != 1 || len(noticeEvents(t, e, e.OperatorPeer)) != 1 {
		t.Fatalf("audit: %s %d", actor, count)
	}
	e.Now.Advance(31 * 24 * time.Hour)
	fresh, err := e.Hub.EnsureMovingNotice(ctx, n.NewURL)
	if err != nil || fresh.NoticeID == n.NoticeID {
		t.Fatalf("expired moving reused: %+v %v", fresh, err)
	}
}

func TestNoticeDefaults(t *testing.T) {
	for _, kind := range []string{"maintenance", "restart", "moving"} {
		t.Run(kind, func(t *testing.T) {
			e := testhub.Start(t)
			at := e.Now.Now().Add(time.Hour)
			r := hub.NoticeRequest{Kind: kind, Message: "notice", At: envelope.FormatTime(at)}
			duration := 24 * time.Hour
			if kind == "moving" {
				r.NewURL = "https://next.example"
				duration = 30 * 24 * time.Hour
			}
			n := postNotice(t, e, r)
			if n.ExpiresAt != envelope.FormatTime(at.Add(duration)) {
				t.Fatalf("expiry: %s", n.ExpiresAt)
			}
		})
	}
}

func TestNoticeTransactionRollback(t *testing.T) {
	e := testhub.Start(t)
	ctx := context.Background()
	first := postNotice(t, e, hub.NoticeRequest{Kind: "maintenance", Message: "first"})
	if _, err := e.Hub.DB.Exec(`CREATE TRIGGER reject_notice_post BEFORE INSERT ON audit WHEN NEW.action='notice_post' BEGIN SELECT RAISE(ABORT,'injected audit failure'); END`); err != nil {
		t.Fatal(err)
	}
	_, err := e.Hub.PostNotice(ctx, auth.Caller{Kind: "operator"}, hub.NoticeRequest{Kind: "maintenance", Message: "second"})
	if err == nil {
		t.Fatal("posting unexpectedly succeeded")
	}
	active, err := e.Hub.ActiveNotices(ctx)
	if err != nil || len(active) != 1 || active[0].NoticeID != first.NoticeID {
		t.Fatalf("rollback active %+v %v", active, err)
	}
	events := noticeEvents(t, e, e.OperatorPeer)
	if len(events) != 1 || events[0].Notice.NoticeID != first.NoticeID {
		t.Fatalf("events escaped rollback: %+v", events)
	}
	var count int
	if err := e.Hub.DB.QueryRow(`SELECT count(*) FROM audit WHERE action='notice_clear'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("clear audit escaped rollback")
	}
}

func TestNoticeConcurrentPosts(t *testing.T) {
	for _, ensure := range []bool{false, true} {
		t.Run(map[bool]string{false: "post", true: "ensure"}[ensure], func(t *testing.T) {
			e := testhub.Start(t)
			ctx := context.Background()
			start := make(chan struct{})
			results := make(chan error, 6)
			for i := 0; i < 6; i++ {
				go func() {
					<-start
					var err error
					if ensure {
						_, err = e.Hub.EnsureMovingNotice(ctx, "https://next.example")
					} else {
						_, err = e.Hub.PostNotice(ctx, auth.Caller{Kind: "operator"}, hub.NoticeRequest{Kind: "maintenance", Message: "hello"})
					}
					results <- err
				}()
			}
			close(start)
			for i := 0; i < 6; i++ {
				if err := <-results; err != nil {
					t.Fatal(err)
				}
			}
			active, err := e.Hub.ActiveNotices(ctx)
			if err != nil || len(active) != 1 {
				t.Fatalf("active: %+v %v", active, err)
			}
			events := noticeEvents(t, e, e.OperatorPeer)
			want := 11
			if ensure {
				want = 1
			}
			if len(events) != want {
				t.Fatalf("events: %d want %d", len(events), want)
			}
		})
	}
}
