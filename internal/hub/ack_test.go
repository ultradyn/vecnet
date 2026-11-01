package hub_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/auth"
	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/eventlog"
	"github.com/XertroV/vecnet/internal/hub"
	"github.com/XertroV/vecnet/internal/ids"
	"github.com/XertroV/vecnet/internal/testhub"
)

// holdReq is a SendRequest to recipients with priority hold.
func holdReq(key string, recipients ...string) map[string]any {
	r := req(key, recipients...)
	r["priority"] = "hold"
	return r
}

// receipts returns the receipt Events in peer's log.
func receipts(t *testing.T, env *testhub.Env, peer string) []envelope.Receipt {
	t.Helper()
	evs, err := env.Hub.Log.Read(context.Background(), []string{peer}, eventlog.Cursor{Epoch: env.Hub.Log.Epoch()}, 0)
	if err != nil {
		t.Fatal(err)
	}
	var out []envelope.Receipt
	for _, ev := range evs {
		if ev.Type != "receipt" {
			continue
		}
		if err := envelope.Validate("receipt", ev.Data); err != nil {
			t.Fatalf("receipt Event data is not schema-valid: %v; %s", err, ev.Data)
		}
		var r envelope.Receipt
		if err := json.Unmarshal(ev.Data, &r); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	return out
}

func deliveryState(t *testing.T, env *testhub.Env, dly string) (state string, held int) {
	t.Helper()
	if err := env.Hub.DB.QueryRow(`SELECT state, held FROM deliveries WHERE dly_id = ?`, dly).Scan(&state, &held); err != nil {
		t.Fatal(err)
	}
	return state, held
}

func TestAckMarksHoldAccepted(t *testing.T) {
	env := testhub.Start(t)
	ctx := context.Background()
	a, aSec := env.NewPeer("grokbot", "a")
	b, bSec := env.NewPeer("session", "b")

	first := mustSend(t, env, aSec, holdReq("hold-1", b))
	second := mustSend(t, env, aSec, holdReq("hold-2", b))
	evs, err := env.Hub.Log.Read(ctx, []string{b}, eventlog.Cursor{Epoch: env.Hub.Log.Epoch()}, 0)
	if err != nil || len(evs) != 2 {
		t.Fatalf("b's log = %d events, %v", len(evs), err)
	}
	dly1, dly2 := first.Deliveries[0].DeliveryID, second.Deliveries[0].DeliveryID
	if s, _ := deliveryState(t, env, dly1); s != "queued" {
		t.Fatalf("hold delivery state before ack = %q, want queued", s)
	}

	resp, body := env.Do(bSec, "POST", "/v1/ack", map[string]string{"cursor": evs[0].Cursor})
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("POST /v1/ack = %d; %s", resp.StatusCode, body)
	}
	if s, held := deliveryState(t, env, dly1); s != "accepted" || held != 0 {
		t.Fatalf("acked delivery = %q held %d, want accepted held 0", s, held)
	}
	if s, _ := deliveryState(t, env, dly2); s != "queued" {
		t.Fatalf("delivery past the cursor = %q, want queued", s)
	}
	rs := receipts(t, env, a)
	if len(rs) != 1 {
		t.Fatalf("a's receipts = %+v, want 1", rs)
	}
	r := rs[0]
	if r.State != "accepted" || r.By != b || r.DeliveryID != dly1 || r.MessageID != first.MessageID || r.ThreadID != first.ThreadID {
		t.Fatalf("receipt = %+v, want accepted by %s for %s", r, b, dly1)
	}
	acked, ok, err := env.Hub.Log.Acked(ctx, b, "*")
	if err != nil || !ok || acked.String() != evs[0].Cursor {
		t.Fatalf("Acked(b, *) = %v %v %v, want %s", acked, ok, err, evs[0].Cursor)
	}
	if n := count(t, env, `SELECT count(*) FROM audit WHERE action = 'ack'`); n != 1 {
		t.Fatalf("ack audit rows = %d, want 1", n)
	}

	// A second identical ack is a no-op: no second receipt.
	resp, body = env.Do(bSec, "POST", "/v1/ack", map[string]string{"cursor": evs[0].Cursor})
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("second ack = %d; %s", resp.StatusCode, body)
	}
	if rs := receipts(t, env, a); len(rs) != 1 {
		t.Fatalf("a's receipts after a repeated ack = %d, want 1", len(rs))
	}

	// An ack past the newest event, a foreign epoch and a malformed body fail.
	resp, body = env.Do(bSec, "POST", "/v1/ack", map[string]string{"cursor": "e1.999999"})
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("ack past head = %d; %s", resp.StatusCode, body)
	}
	resp, body = env.Do(bSec, "POST", "/v1/ack", map[string]string{"cursor": "e9.1"})
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("ack of a foreign epoch = %d; %s", resp.StatusCode, body)
	}
	for _, bad := range []string{`{}`, `{"cursor":5}`, `{"cursor":"e1.1","x":1}`, `nope`} {
		resp, body = env.Do(bSec, "POST", "/v1/ack", bad)
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("ack %s = %d; %s", bad, resp.StatusCode, body)
		}
	}
	if s, _ := deliveryState(t, env, dly2); s != "queued" {
		t.Fatalf("rejected acks changed the second delivery to %q", s)
	}
}

func TestStreamOwners(t *testing.T) {
	env := testhub.Start(t)
	ctx := context.Background()
	a, _ := env.NewPeer("grokbot", "a")
	prn, _ := env.NewPrincipal("p")
	b, _, err := env.Hub.AddPeer(ctx, "session", "b", prn)
	if err != nil {
		t.Fatal(err)
	}
	c, _, err := env.Hub.AddPeer(ctx, "session", "c", prn)
	if err != nil {
		t.Fatal(err)
	}
	var aPrn string
	if err := env.Hub.DB.QueryRow(`SELECT prn_id FROM peers WHERE peer_id = ?`, a).Scan(&aPrn); err != nil {
		t.Fatal(err)
	}
	peerC := auth.Caller{Kind: "peer", PrincipalID: aPrn, PeerID: a}
	prnC := auth.Caller{Kind: "principal", PrincipalID: prn}

	owners, ao, as, err := env.Hub.StreamOwners(ctx, peerC, "")
	if err != nil || len(owners) != 1 || owners[0] != a || ao != a || as != "*" {
		t.Fatalf("peer stream = %v %s %s %v", owners, ao, as, err)
	}
	owners, ao, as, err = env.Hub.StreamOwners(ctx, prnC, "")
	if err != nil || len(owners) != 2 || ao != prn || as != "*" {
		t.Fatalf("principal stream = %v %s %s %v", owners, ao, as, err)
	}
	if m := map[string]bool{owners[0]: true, owners[1]: true}; !m[b] || !m[c] {
		t.Fatalf("principal owners = %v, want %s and %s", owners, b, c)
	}
	owners, ao, as, err = env.Hub.StreamOwners(ctx, prnC, c)
	if err != nil || len(owners) != 1 || owners[0] != c || ao != prn || as != c {
		t.Fatalf("narrowed stream = %v %s %s %v", owners, ao, as, err)
	}
	if _, _, _, err := env.Hub.StreamOwners(ctx, prnC, a); !isAPIError(err, http.StatusForbidden, "not_your_peer") {
		t.Fatalf("foreign peer filter: err = %v, want 403 not_your_peer", err)
	}
	if _, _, _, err := env.Hub.StreamOwners(ctx, peerC, b); !isAPIError(err, http.StatusForbidden, "not_your_peer") {
		t.Fatalf("peer credential naming another peer: err = %v, want 403 not_your_peer", err)
	}
}

func isAPIError(err error, status int, code string) bool {
	var e *hub.APIError
	return errors.As(err, &e) && e.Status == status && e.Code == code
}

// TestAckPastHubMessageWritesNoHubReceipt: acking past a hub-authored
// Delivery (the approval-expiry notice) accepts it but appends no receipt
// Event, since its sender "hub" is not a peer and owns no log reader
// (PROTOCOL §2.4, §9).
func TestAckPastHubMessageWritesNoHubReceipt(t *testing.T) {
	env := testhub.Start(t)
	ctx := context.Background()
	_, aSec := env.NewPeer("session", "A")
	b, bSec := env.NewPeer("session", "B")
	apr := ids.New(ids.Apr)
	mustSend(t, env, aSec, approvalReq("q1", apr, env.Now.Now().Add(time.Hour), []string{"yes", "no"}, b))
	env.Now.Advance(2 * time.Hour)
	if n, err := env.Hub.ExpireApprovals(ctx, env.Now.Now()); err != nil || n != 1 {
		t.Fatalf("ExpireApprovals = %d, %v; want 1", n, err)
	}
	cur, err := env.Hub.Log.Current(ctx, []string{b})
	if err != nil {
		t.Fatal(err)
	}
	bc, err := env.Hub.Auth.Resolve(ctx, bSec)
	if err != nil {
		t.Fatal(err)
	}
	if err := env.Hub.Ack(ctx, bc, "", cur); err != nil {
		t.Fatal(err)
	}
	if n := count(t, env, `SELECT count(*) FROM deliveries d JOIN messages m ON m.msg_id = d.msg_id
		WHERE m.sender = 'hub' AND d.recipient = ? AND d.state = 'accepted'`, b); n != 1 {
		t.Fatalf("accepted hub-message deliveries to B = %d, want 1", n)
	}
	if got := receipts(t, env, hub.HubSender); len(got) != 0 {
		t.Fatalf("receipt Events in the hub's log = %d, want 0", len(got))
	}
}
