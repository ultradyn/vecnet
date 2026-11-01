package hub_test

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/eventlog"
	"github.com/XertroV/vecnet/internal/hub"
	"github.com/XertroV/vecnet/internal/testhub"
)

// directoryEntry returns peer's entry as the Directory shows it.
func directoryEntry(t *testing.T, env *testhub.Env, peer string) hub.DirectoryEntry {
	t.Helper()
	dir, err := env.Hub.Directory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range dir {
		if e.PeerID == peer {
			return e
		}
	}
	t.Fatalf("peer %s missing from the directory", peer)
	return hub.DirectoryEntry{}
}

func wantReach(t *testing.T, env *testhub.Env, peer, want, when string) {
	t.Helper()
	if got := directoryEntry(t, env, peer).Reachability; got != want {
		t.Fatalf("%s: reachability = %q, want %q", when, got, want)
	}
}

// PROTOCOL §8: reachable = "last push accepted, or last inbound call within
// 24 h". Call-based reachability decays to unknown; push-based does not.
func TestReachabilityFromCallsDecaysAfter24h(t *testing.T) {
	env := testhub.Start(t)
	ctx := context.Background()
	b, bSec := env.NewPeer("session", "b")

	env.Do(bSec, "GET", "/v1/peers", nil)
	seen := envelope.FormatTime(env.Now.Now())
	wantReach(t, env, b, "reachable", "right after a call")

	env.Now.Advance(24 * time.Hour)
	wantReach(t, env, b, "reachable", "24 h after the call")

	env.Now.Advance(time.Millisecond)
	wantReach(t, env, b, "unknown", "24 h + 1 ms after the call")
	if ls := directoryEntry(t, env, b).LastSeen; ls == nil || *ls != seen {
		t.Fatalf("last_seen = %v, want it kept at %s", ls, seen)
	}

	env.Do(bSec, "GET", "/v1/peers", nil)
	wantReach(t, env, b, "reachable", "after a fresh call")

	// An accepted push has no window.
	env.Hub.ObservePush(ctx, b, true)
	env.Now.Advance(72 * time.Hour)
	wantReach(t, env, b, "reachable", "72 h after an accepted push")

	// A failed push is unreachable until the next observation.
	env.Hub.ObservePush(ctx, b, false)
	env.Now.Advance(72 * time.Hour)
	wantReach(t, env, b, "unreachable", "72 h after a failed push")

	// A closed stream is an inbound observation like a call.
	env.Hub.StreamOpened(ctx, b)
	wantReach(t, env, b, "connected", "with a stream open")
	env.Now.Advance(48 * time.Hour)
	wantReach(t, env, b, "connected", "48 h into an open stream")
	env.Hub.StreamClosed(ctx, b)
	wantReach(t, env, b, "reachable", "right after the stream closed")
	env.Now.Advance(24*time.Hour + time.Millisecond)
	wantReach(t, env, b, "unknown", "24 h + 1 ms after the stream closed")
}

// A principal acting for a peer with ?as= (or "as" on a send) is an inbound
// call by that peer (PROTOCOL §8, §13.6).
func TestPrincipalActingForPeerMarksItReachable(t *testing.T) {
	env := testhub.Start(t)
	a, _ := env.NewPeer("grokbot", "a")
	prn, prnSec := env.NewPrincipal("host")
	p, _ := peerUnder(t, env, "pagchat-bot", "p", prn)
	q, _ := peerUnder(t, env, "pagchat-bot", "q", prn)

	reportPresence(t, env, prnSec, "?as="+p, "working")
	wantReach(t, env, p, "reachable", "after the principal reported presence as p")
	if ls := directoryEntry(t, env, p).LastSeen; ls == nil || *ls != envelope.FormatTime(env.Now.Now()) {
		t.Fatalf("p last_seen = %v, want now", ls)
	}

	body := req("as-send", a)
	body["as"] = q
	mustSend(t, env, prnSec, body)
	wantReach(t, env, q, "reachable", "after the principal sent as q")

	// A rejected ?as= is no observation.
	_, otherSec := env.NewPrincipal("other")
	o, _ := peerUnder(t, env, "session", "o", prn)
	resp, _ := env.Do(otherSec, "POST", "/v1/presence?as="+o,
		map[string]any{"state": "idle", "at": envelope.FormatTime(env.Now.Now())})
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("foreign principal presence = %d, want 403", resp.StatusCode)
	}
	wantReach(t, env, o, "unknown", "after a rejected ?as=")
}

// A receipt that changes nothing (a terminal delivery, or a repeat) emits no
// Event; a sender that is not a peer gets none either.
func TestReceiptNoOpsEmitNothing(t *testing.T) {
	env := testhub.Start(t)
	a, aSec := env.NewPeer("grokbot", "a")
	b, bSec := env.NewPeer("session", "b")

	sr := mustSend(t, env, aSec, req("noop-1", b))
	dly := sr.Deliveries[0].DeliveryID
	if _, err := env.Hub.DB.Exec(`UPDATE deliveries SET state = 'expired' WHERE dly_id = ?`, dly); err != nil {
		t.Fatal(err)
	}
	for _, st := range []string{"processed", "failed"} {
		resp, body := postReceipt(t, env, bSec, map[string]any{"delivery_id": dly, "state": st})
		if resp.StatusCode != http.StatusNoContent {
			t.Fatalf("%s on an expired delivery = %d, want 204; %s", st, resp.StatusCode, body)
		}
		if n := len(findReceipt(t, env, a, dly, st)); n != 0 {
			t.Fatalf("%s on an expired delivery reached the sender (%d)", st, n)
		}
	}
	if s, _ := deliveryState(t, env, dly); s != "expired" {
		t.Fatalf("delivery state = %q, want expired", s)
	}

	// A repeated processed is one Event.
	sr2 := mustSend(t, env, aSec, req("noop-2", b))
	dly2 := sr2.Deliveries[0].DeliveryID
	for range 2 {
		if resp, body := postReceipt(t, env, bSec, map[string]any{"delivery_id": dly2, "state": "processed"}); resp.StatusCode != http.StatusNoContent {
			t.Fatalf("processed = %d; %s", resp.StatusCode, body)
		}
	}
	if n := len(findReceipt(t, env, a, dly2, "processed")); n != 1 {
		t.Fatalf("repeated processed gave %d Events, want 1", n)
	}

	// A hub-authored message: the state moves, no "hub" log row appears.
	sr3 := mustSend(t, env, aSec, req("noop-3", b))
	dly3 := sr3.Deliveries[0].DeliveryID
	if _, err := env.Hub.DB.Exec(`UPDATE messages SET sender = 'hub' WHERE msg_id = ?`, sr3.MessageID); err != nil {
		t.Fatal(err)
	}
	if resp, body := postReceipt(t, env, bSec, map[string]any{"delivery_id": dly3, "state": "processed"}); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("processed on a hub message = %d; %s", resp.StatusCode, body)
	}
	if s, _ := deliveryState(t, env, dly3); s != "processed" {
		t.Fatalf("hub message delivery state = %q, want processed", s)
	}
	if n := count(t, env, `SELECT count(*) FROM events WHERE owner = 'hub'`); n != 0 {
		t.Fatalf("receipt appended %d events to a 'hub' log", n)
	}
	evs, err := env.Hub.Log.Read(context.Background(), []string{"hub"}, eventlog.Cursor{Epoch: env.Hub.Log.Epoch()}, 0)
	if err != nil || len(evs) != 0 {
		t.Fatalf("hub log = %v, %v; want empty", evs, err)
	}
}

// An explicit "location": "" clears it; an absent location keeps it.
func TestPresenceLocationClears(t *testing.T) {
	env := testhub.Start(t)
	c, cSec := env.NewPeer("session", "c")
	post := func(body map[string]any) {
		t.Helper()
		body["at"] = envelope.FormatTime(env.Now.Now())
		if resp, raw := env.Do(cSec, "POST", "/v1/presence", body); resp.StatusCode != http.StatusNoContent {
			t.Fatalf("POST /v1/presence %v = %d; %s", body, resp.StatusCode, raw)
		}
	}
	post(map[string]any{"state": "working", "location": "xsm"})
	post(map[string]any{"state": "idle"})
	if l := directoryEntry(t, env, c).Location; l == nil || *l != "xsm" {
		t.Fatalf("location after a report without it = %v, want xsm", l)
	}
	post(map[string]any{"state": "idle", "location": ""})
	if l := directoryEntry(t, env, c).Location; l != nil {
		t.Fatalf("location after an explicit empty one = %q, want null", *l)
	}
}

// IdleHook is best-effort: its failure does not fail a presence report that
// has already taken effect.
func TestIdleHookFailureDoesNotFailReport(t *testing.T) {
	env := testhub.Start(t)
	c, cSec := env.NewPeer("session", "c")
	called := 0
	env.Hub.IdleHook = func(ctx context.Context, peer string) error {
		called++
		return errors.New("boom")
	}
	reportPresence(t, env, cSec, "", "idle")
	if called != 1 {
		t.Fatalf("IdleHook ran %d times, want 1", called)
	}
	if p := directoryPresence(t, env, c); p != "idle" {
		t.Fatalf("presence = %q, want idle", p)
	}
}
