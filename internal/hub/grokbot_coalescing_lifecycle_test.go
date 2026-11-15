package hub_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/auth"
	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/hub"
	"github.com/XertroV/vecnet/internal/push"
	"github.com/XertroV/vecnet/internal/testhub"
)

const t2Key = "crsr_00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff"

type t2Transport struct {
	entries atomic.Int64
	base    http.RoundTripper
}

func (c *t2Transport) RoundTrip(r *http.Request) (*http.Response, error) {
	c.entries.Add(1)
	return c.base.RoundTrip(r)
}

type t2Fixture struct {
	t                 *testing.T
	env               *testhub.Env
	ctx               context.Context
	a, b, bSec, route string
	serial            int
	w                 *push.Worker
	client            *t2Transport
	posts             atomic.Int64
	srv               *httptest.Server
	respond           func(http.ResponseWriter, *http.Request)
}

func t2Setup(t *testing.T) *t2Fixture {
	t.Helper()
	f := &t2Fixture{t: t, env: testhub.Start(t), ctx: context.Background()}
	f.a, _ = f.env.NewPeer("grokbot", "t2-sender")
	f.b, f.bSec = f.env.NewPeer("grokbot", "t2-recipient")
	f.env.SetLimits(f.a, 10000, 10000)
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		f.posts.Add(1)
		if f.respond != nil {
			f.respond(w, r)
			return
		}
		w.Write([]byte(`{"success":true,"runUuid":"t2"}`))
	}))
	t.Cleanup(f.srv.Close)
	old := hub.GrokbotURLPattern
	hub.GrokbotURLPattern = regexp.MustCompile(`^http://127\.0\.0\.1:[0-9]+(?:/other)?$`)
	t.Cleanup(func() { hub.GrokbotURLPattern = old })
	var err error
	f.route, err = f.env.Hub.AddGrokbotRouteWithTransport(f.ctx, "peer", f.b, f.srv.URL, t2Key, hub.GrokbotTransportRecipientDrainV1)
	t2Must(t, err)
	f.client = &t2Transport{base: http.DefaultTransport}
	f.w = &push.Worker{Hub: f.env.Hub, Now: f.env.Now.Now, Client: &http.Client{Transport: f.client, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	return f
}
func t2Must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
func (f *t2Fixture) send(priority string) string {
	f.t.Helper()
	f.serial++
	// Real public Send API, with explicit actor rather than HTTP auth setup.
	r := req(fmt.Sprintf("t2-%d", f.serial), f.b)
	r["priority"] = priority
	b, err := json.Marshal(r)
	t2Must(f.t, err)
	out, _, err := f.env.Hub.Send(f.ctx, f.caller(f.a), b, "http_send")
	t2Must(f.t, err)
	return out.Deliveries[0].DeliveryID
}
func (f *t2Fixture) presence(state string) {
	f.t.Helper()
	t2Must(f.t, f.env.Hub.SetPresence(f.ctx, f.caller(f.b), "", hub.PresenceReport{State: state}))
}
func (f *t2Fixture) claim() hub.DueDelivery {
	f.t.Helper()
	d, e := f.env.Hub.ClaimDue(f.ctx, f.env.Now.Now(), 16)
	t2Must(f.t, e)
	if len(d) != 1 || d[0].GrokWake == nil {
		f.t.Fatalf("claim=%+v", d)
	}
	return d[0]
}
func (f *t2Fixture) step() { f.t.Helper(); _, e := f.w.Step(f.ctx); t2Must(f.t, e) }
func (f *t2Fixture) requests(n int64) {
	f.t.Helper()
	if f.posts.Load() != n || f.client.entries.Load() != n {
		f.t.Fatalf("receiver POSTs=%d client entries=%d want %d", f.posts.Load(), f.client.entries.Load(), n)
	}
}
func (f *t2Fixture) scalar(q string, args ...any) int {
	f.t.Helper()
	var n int
	t2Must(f.t, f.env.Hub.DB.QueryRow(q, args...).Scan(&n))
	return n
}
func (f *t2Fixture) covers(id string) int {
	return f.scalar(`SELECT count(*) FROM grokbot_covers WHERE route_id=? AND delivery_id=?`, f.route, id)
}
func (f *t2Fixture) block(d hub.DueDelivery) func() {
	f.t.Helper()
	entered := make(chan struct{})
	release := make(chan struct{})
	done := make(chan struct{})
	var once sync.Once
	f.respond = func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-release
		w.Write([]byte(`{"success":true,"runUuid":"t2"}`))
	}
	go func() { f.w.Push(f.ctx, d); close(done) }()
	unblock := func() {
		once.Do(func() { close(release) })
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			f.t.Fatal("POST did not settle")
		}
	}
	f.t.Cleanup(unblock)
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		f.t.Fatal("receiver not entered")
	}
	return unblock
}
func (f *t2Fixture) lateInvalidation(mutate func()) {
	f.t.Helper()
	f.send("wake")
	f.send("wake")
	d := f.claim()
	deadline := f.env.Now.Now().Add(hub.PushLease)
	unblock := f.block(d)
	f.env.Now.Advance(time.Minute)
	mutate()
	f.step()
	f.requests(1)
	unblock()
	if n := f.scalar(`SELECT count(*) FROM grokbot_wakes WHERE route_id=? AND (cover_valid!=0 OR cover_until!='')`, f.route); n != 0 {
		f.t.Fatal("late 200 reopened invalid cover")
	}
	var retained string
	t2Must(f.t, f.env.Hub.DB.QueryRow(`SELECT lease_until FROM grokbot_wakes WHERE route_id=? AND token=?`, f.route, d.GrokWake.Token).Scan(&retained))
	if retained != envelope.FormatTime(deadline) {
		f.t.Fatalf("lease changed: %s want %s", retained, envelope.FormatTime(deadline))
	}
	f.step()
	f.requests(1)
	f.env.Now.Advance(deadline.Sub(f.env.Now.Now()) + time.Millisecond)
	f.respond = nil
	f.step()
	f.requests(2)
}
func TestIdleBeforeBeginAllowsNewCover(t *testing.T) {
	f := t2Setup(t)
	f.send("wake")
	f.send("wake")
	d := f.claim()
	f.presence("idle")
	f.w.Push(f.ctx, d)
	f.requests(1)
	if f.scalar(`SELECT count(*) FROM grokbot_wakes WHERE route_id=? AND cover_until> ?`, f.route, envelope.FormatTime(f.env.Now.Now())) != 1 {
		t.Fatal("idle before Begin prevented new cover")
	}
	f.step()
	f.requests(1)
}
func TestIdleAfterBeginRejectsLateCover(t *testing.T) {
	f := t2Setup(t)
	f.lateInvalidation(func() { f.presence("idle") })
}
func TestStaleOnIdleOnlyReleases(t *testing.T) {
	for _, ticker := range []bool{false, true} {
		t.Run(fmt.Sprint(ticker), func(t *testing.T) {
			f := t2Setup(t)
			at := f.env.Now.Now()
			if ticker {
				at = at.Add(-9 * time.Minute)
			}
			t2Must(t, f.env.Hub.SetPresence(f.ctx, f.caller(f.b), "", hub.PresenceReport{State: "working", At: at}))
			f.send("wake")
			d := f.claim()
			unblock := f.block(d)
			held := f.send("wake_idle")
			if ticker {
				f.env.Now.Advance(time.Minute + time.Millisecond)
				f.env.Hub.Tick(f.ctx, false)
			} else {
				t2Must(t, f.env.Hub.OnIdle(f.ctx, f.b))
			}
			if f.scalar(`SELECT held FROM deliveries WHERE dly_id=?`, held) != 0 {
				t.Fatal("hold not released")
			}
			if f.scalar(`SELECT idle_counter FROM grokbot_wakes WHERE route_id=?`, f.route) != 0 {
				t.Fatal("stale/direct release counted explicit idle")
			}
			unblock()
			f.requests(1)
			f.step()
			f.requests(1)
			if f.scalar(`SELECT count(*) FROM grokbot_wakes WHERE route_id=? AND cover_until!=''`, f.route) != 1 {
				t.Fatal("stale/direct release invalidated cover")
			}
		})
	}
}
func TestURLABAInvalidatesLateSuccess(t *testing.T) {
	f := t2Setup(t)
	f.lateInvalidation(func() {
		b := f.srv.URL + "/other"
		a := f.srv.URL
		t2Must(t, f.env.Hub.UpdateRoute(f.ctx, f.route, hub.RouteUpdate{URL: &b}))
		t2Must(t, f.env.Hub.UpdateRoute(f.ctx, f.route, hub.RouteUpdate{URL: &a}))
	})
}
func TestPrincipalMoveSamePeerRouteInvalidates(t *testing.T) {
	f := t2Setup(t)
	var old string
	t2Must(t, f.env.Hub.DB.QueryRow(`SELECT prn_id FROM peers WHERE peer_id=?`, f.b).Scan(&old))
	p, _ := f.env.NewPrincipal("new")
	f.lateInvalidation(func() { t2Must(t, f.env.Hub.MovePeer(f.ctx, f.b, p)); t2Must(t, f.env.Hub.MovePeer(f.ctx, f.b, old)) })
}
func TestDisableReenableAndRevokeRetainLease(t *testing.T) {
	for _, kind := range []string{"disable", "archive", "revoke", "rotate"} {
		t.Run(kind, func(t *testing.T) {
			f := t2Setup(t)
			f.lateInvalidation(func() {
				switch kind {
				case "disable":
					t2Must(t, f.env.Hub.SetPeerDisabled(f.ctx, f.b, true))
					t2Must(t, f.env.Hub.SetPeerDisabled(f.ctx, f.b, false))
				case "archive":
					t2Must(t, f.env.Hub.SetPeerArchived(f.ctx, f.b, true))
					t2Must(t, f.env.Hub.SetPeerArchived(f.ctx, f.b, false))
					t2Must(t, f.env.Hub.SetPeerDisabled(f.ctx, f.b, false))
				case "revoke":
					for _, mode := range []string{hub.GrokbotTransportDeliveryV1, hub.GrokbotTransportRecipientDrainV1} {
						t2Must(t, f.env.Hub.UpdateRoute(f.ctx, f.route, hub.RouteUpdate{GrokbotTransport: &mode}))
					}
				case "rotate":
					t2Must(t, f.env.Hub.RotateGrokbotKey(f.ctx, f.route, t2Key))
				}
			})
		})
	}
}
func TestRemovalAndRepairCannotTransferReservation(t *testing.T) {
	f := t2Setup(t)
	f.send("wake")
	f.send("wake")
	d := f.claim()
	unblock := f.block(d)
	t2Must(t, f.env.Hub.RemoveRoute(f.ctx, f.route))
	unblock()
	f.requests(1)
	if f.scalar(`SELECT count(*) FROM route_runs WHERE route_id=? AND grok_wake_token=?`, f.route, d.GrokWake.Token) != 1 {
		t.Fatal("removal lost original reservation")
	}
	if f.scalar(`SELECT count(*) FROM grokbot_wakes WHERE route_id=? AND cover_until!=''`, f.route) != 0 {
		t.Fatal("removed route got late cover")
	}
	f.step()
	f.requests(1)
	// A different route has a different reservation identity.
	f.respond = nil
	newRoute, e := f.env.Hub.AddGrokbotRouteWithTransport(f.ctx, "peer", f.b, f.srv.URL, t2Key, hub.GrokbotTransportRecipientDrainV1)
	t2Must(t, e)
	f.send("wake")
	f.step()
	f.requests(2)
	t2Must(t, f.env.Hub.GrokbotWakePushResult(f.ctx, d, 503, nil, 0, nil, f.env.Now.Now()))
	if f.scalar(`SELECT count(*) FROM route_runs WHERE route_id IN (?,?)`, f.route, newRoute) != 2 {
		t.Fatal("stale result refunded settled old or replacement reservation")
	}
}
func TestLifecycleBeforeBeginCompensates(t *testing.T) {
	for _, kind := range []string{"url", "move", "disable", "archive", "rotate", "remove"} {
		t.Run(kind, func(t *testing.T) {
			f := t2Setup(t)
			id := f.send("wake")
			d := f.claim()
			switch kind {
			case "url":
				b := f.srv.URL + "/other"
				a := f.srv.URL
				t2Must(t, f.env.Hub.UpdateRoute(f.ctx, f.route, hub.RouteUpdate{URL: &b}))
				t2Must(t, f.env.Hub.UpdateRoute(f.ctx, f.route, hub.RouteUpdate{URL: &a}))
			case "move":
				p, _ := f.env.NewPrincipal("new")
				t2Must(t, f.env.Hub.MovePeer(f.ctx, f.b, p))
			case "disable":
				t2Must(t, f.env.Hub.SetPeerDisabled(f.ctx, f.b, true))
				t2Must(t, f.env.Hub.SetPeerDisabled(f.ctx, f.b, false))
			case "archive":
				t2Must(t, f.env.Hub.SetPeerArchived(f.ctx, f.b, true))
			case "rotate":
				t2Must(t, f.env.Hub.RotateGrokbotKey(f.ctx, f.route, t2Key))
			case "remove":
				t2Must(t, f.env.Hub.RemoveRoute(f.ctx, f.route))
			}
			// Compensation is part of mutation, before a replaying worker can Begin.
			if f.scalar(`SELECT attempt FROM deliveries WHERE dly_id=?`, id) != 0 {
				t.Fatal("unarmed mutation did not compensate claim")
			}
			f.w.Push(f.ctx, d)
			f.requests(0)
		})
	}
}

func (f *t2Fixture) caller(peer string) auth.Caller {
	f.t.Helper()
	var prn string
	t2Must(f.t, f.env.Hub.DB.QueryRow(`SELECT prn_id FROM peers WHERE peer_id=?`, peer).Scan(&prn))
	return auth.Caller{Kind: "peer", PeerID: peer, PrincipalID: prn}
}

func TestStalePresenceReportDoesNotInvalidate(t *testing.T) {
	f := t2Setup(t)
	t2Must(t, f.env.Hub.RemoveRoute(f.ctx, f.route))
	f.b = f.env.OperatorPeer
	var err error
	f.route, err = f.env.Hub.AddGrokbotRouteWithTransport(f.ctx, "peer", f.b, f.srv.URL, t2Key, hub.GrokbotTransportRecipientDrainV1)
	t2Must(t, err)
	c := f.caller(f.b)
	t2Must(t, f.env.Hub.SetPresence(f.ctx, c, "", hub.PresenceReport{State: "working"}))
	f.send("wake")
	d := f.claim()
	unblock := f.block(d)
	t2Must(t, f.env.Hub.SetPresence(f.ctx, c, "", hub.PresenceReport{State: "idle", At: f.env.Now.Now().Add(-time.Minute)}))
	if f.scalar(`SELECT idle_counter FROM grokbot_wakes WHERE route_id=?`, f.route) != 0 {
		t.Fatal("stale idle report invalidated")
	}
	unblock()
	f.step()
	f.requests(1)
	if f.scalar(`SELECT count(*) FROM grokbot_wakes WHERE route_id=? AND cover_until!=''`, f.route) != 1 {
		t.Fatal("stale report closed cover")
	}
}
func TestIdleAfterSettledSuccessClosesCover(t *testing.T) {
	f := t2Setup(t)
	f.send("wake")
	f.send("wake")
	f.step()
	f.requests(1)
	f.env.Now.Advance(time.Second)
	f.presence("idle")
	f.step()
	f.requests(2)
}

func TestNoopLifecycleRetainsCover(t *testing.T) {
	for _, phase := range []string{"claimed", "armed", "covered"} {
		t.Run(phase, func(t *testing.T) {
			f := t2Setup(t)
			f.send("wake")
			f.send("wake")
			d := f.claim()
			var unblock func()
			if phase == "armed" {
				unblock = f.block(d)
			} else if phase == "covered" {
				f.w.Push(f.ctx, d)
			}
			c := f.caller(f.b)
			t2Must(t, f.env.Hub.MovePeer(f.ctx, f.b, c.PrincipalID))
			t2Must(t, f.env.Hub.SetPeerDisabled(f.ctx, f.b, false))
			t2Must(t, f.env.Hub.SetPeerArchived(f.ctx, f.b, false))
			u := f.srv.URL
			mode := hub.GrokbotTransportRecipientDrainV1
			t2Must(t, f.env.Hub.UpdateRoute(f.ctx, f.route, hub.RouteUpdate{URL: &u, GrokbotTransport: &mode}))
			if phase == "claimed" {
				f.w.Push(f.ctx, d)
			} else if phase == "armed" {
				unblock()
				f.respond = nil
			}
			f.requests(1)
			f.step()
			f.requests(1)
			if f.scalar(`SELECT count(*) FROM grokbot_wakes WHERE route_id=? AND cover_until!=''`, f.route) != 1 {
				t.Fatal("no-op lifecycle invalidated cover")
			}
		})
	}
}

func TestUnarmedRepairPreservesFrozenRetryBody(t *testing.T) {
	f := t2Setup(t)
	var bodies [][]byte
	f.client.base = t2RoundTripFunc(func(r *http.Request) (*http.Response, error) {
		body, e := io.ReadAll(r.Body)
		if e != nil {
			return nil, e
		}
		r.Body.Close()
		bodies = append(bodies, body)
		r.Body = io.NopCloser(bytes.NewReader(body))
		return http.DefaultTransport.RoundTrip(r)
	})
	f.respond = func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(503) }
	leader := f.send("wake")
	f.send("wake")
	f.step()
	f.requests(1)
	f.send("wake")
	var retry string
	t2Must(t, f.env.Hub.DB.QueryRow(`SELECT retry_at FROM grokbot_wakes WHERE route_id=?`, f.route).Scan(&retry))
	at, e := envelope.ParseTime(retry)
	t2Must(t, e)
	f.env.Now.Advance(at.Sub(f.env.Now.Now()) + time.Millisecond)
	d := f.claim()
	if d.DeliveryID != leader || d.Attempt != 2 {
		t.Fatalf("retry=%+v", d)
	}
	t2Must(t, f.env.Hub.RotateGrokbotKey(f.ctx, f.route, t2Key))
	f.w.Push(f.ctx, d)
	f.requests(1)
	f.respond = nil
	f.step()
	f.requests(2)
	if !bytes.Equal(bodies[0], bodies[1]) {
		t.Fatalf("unarmed repair rewrote frozen retry body:\nfirst=%s\nretry=%s", bodies[0], bodies[1])
	}
}
