package hub_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/XertroV/vecnet/internal/auth"
	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/testhub"
)

// postReceipt posts a receipt body as secret and returns the response.
func postReceipt(t *testing.T, env *testhub.Env, secret string, body any) (*http.Response, []byte) {
	t.Helper()
	return env.Do(secret, "POST", "/v1/receipts", body)
}

// peerUnder creates a peer of kind under the existing principal prn and
// enrols it, returning the peer id and its secret.
func peerUnder(t *testing.T, env *testhub.Env, kind, name, prn string) (string, string) {
	t.Helper()
	ctx := context.Background()
	id, tok, err := env.Hub.AddPeer(ctx, kind, name, prn)
	if err != nil {
		t.Fatalf("AddPeer: %v", err)
	}
	_, secret, err := env.Hub.Auth.Enrol(ctx, tok)
	if err != nil {
		t.Fatalf("Enrol: %v", err)
	}
	return id, secret
}

// findReceipt returns the receipts in peer's log for dly in state.
func findReceipt(t *testing.T, env *testhub.Env, peer, dly, state string) []envelope.Receipt {
	t.Helper()
	var out []envelope.Receipt
	for _, r := range receipts(t, env, peer) {
		if r.DeliveryID == dly && r.State == state {
			out = append(out, r)
		}
	}
	return out
}

func TestReceiptProcessedReachesSender(t *testing.T) {
	env := testhub.Start(t)
	a, aSec := env.NewPeer("grokbot", "a")
	b, bSec := env.NewPeer("dot", "b")

	sr := mustSend(t, env, aSec, req("rcpt-1", b))
	dly := sr.Deliveries[0].DeliveryID

	resp, body := postReceipt(t, env, bSec, map[string]any{"delivery_id": dly, "state": "processed", "note": "done"})
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("POST /v1/receipts processed = %d; %s", resp.StatusCode, body)
	}
	got := findReceipt(t, env, a, dly, "processed")
	if len(got) != 1 {
		t.Fatalf("sender's log has %d processed receipts for %s, want 1", len(got), dly)
	}
	r := got[0]
	if r.By != b {
		t.Fatalf("receipt by = %q, want recipient %q", r.By, b)
	}
	if r.ThreadID != sr.ThreadID || r.ThreadID == "" {
		t.Fatalf("receipt thread_id = %q, want %q", r.ThreadID, sr.ThreadID)
	}
	if r.MessageID != sr.MessageID {
		t.Fatalf("receipt message_id = %q, want %q", r.MessageID, sr.MessageID)
	}
	if r.Note != "done" {
		t.Fatalf("receipt note = %q, want done", r.Note)
	}
	if r.At != envelope.FormatTime(env.Now.Now()) {
		t.Fatalf("receipt at = %q, want hub now %q", r.At, envelope.FormatTime(env.Now.Now()))
	}
	if s, _ := deliveryState(t, env, dly); s != "processed" {
		t.Fatalf("delivery state = %q, want processed", s)
	}
	if n := count(t, env, `SELECT count(*) FROM audit WHERE action = 'receipt' AND actor = ?`, b); n != 1 {
		t.Fatalf("receipt audit rows = %d, want 1", n)
	}
	// The receipt never reaches the recipient's own log.
	if n := len(receipts(t, env, b)); n != 0 {
		t.Fatalf("recipient's log has %d receipts, want 0", n)
	}

	// read is recorded and reaches the sender but leaves the delivery state.
	resp, body = postReceipt(t, env, bSec, map[string]any{"delivery_id": dly, "state": "read"})
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("POST /v1/receipts read = %d; %s", resp.StatusCode, body)
	}
	if got := findReceipt(t, env, a, dly, "read"); len(got) != 1 || got[0].By != b {
		t.Fatalf("read receipts in sender's log = %+v, want one by %s", got, b)
	}
	if s, _ := deliveryState(t, env, dly); s != "processed" {
		t.Fatalf("delivery state after read = %q, want processed", s)
	}

	// failed moves the state.
	sr2 := mustSend(t, env, aSec, req("rcpt-2", b))
	dly2 := sr2.Deliveries[0].DeliveryID
	if err := env.Hub.Receipt(context.Background(), auth.Caller{Kind: "peer", PeerID: b}, dly2, "failed", ""); err != nil {
		t.Fatalf("Hub.Receipt failed: %v", err)
	}
	if s, _ := deliveryState(t, env, dly2); s != "failed" {
		t.Fatalf("delivery state = %q, want failed", s)
	}
	if got := findReceipt(t, env, a, dly2, "failed"); len(got) != 1 || got[0].ThreadID != sr2.ThreadID {
		t.Fatalf("failed receipts in sender's log = %+v", got)
	}
}

func TestReceiptAuthz(t *testing.T) {
	env := testhub.Start(t)
	a, aSec := env.NewPeer("grokbot", "a")
	prn, prnSec := env.NewPrincipal("bob")
	b, bSec := peerUnder(t, env, "session", "b", prn)
	_, cSec := env.NewPeer("dot", "c")

	sr := mustSend(t, env, aSec, req("authz-1", b))
	dly := sr.Deliveries[0].DeliveryID

	// C is not the recipient.
	resp, body := postReceipt(t, env, cSec, map[string]any{"delivery_id": dly, "state": "processed"})
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("non-recipient receipt = %d, want 403; %s", resp.StatusCode, body)
	}
	if code, _ := apiError(t, body); code != "not_recipient" {
		t.Fatalf("non-recipient code = %q, want not_recipient", code)
	}
	// The sender is not the recipient either.
	resp, body = postReceipt(t, env, aSec, map[string]any{"delivery_id": dly, "state": "processed"})
	if code, _ := apiError(t, body); resp.StatusCode != http.StatusForbidden || code != "not_recipient" {
		t.Fatalf("sender receipt = %d %q, want 403 not_recipient", resp.StatusCode, code)
	}
	if s, _ := deliveryState(t, env, dly); s != "queued" {
		t.Fatalf("delivery state after rejected receipts = %q, want queued", s)
	}
	if n := len(findReceipt(t, env, a, dly, "processed")); n != 0 {
		t.Fatalf("rejected receipts reached the sender: %d", n)
	}

	// clients never post accepted (or anything outside processed|read|failed).
	for _, st := range []string{"accepted", "expired", "dead_letter", "bogus"} {
		resp, body = postReceipt(t, env, bSec, map[string]any{"delivery_id": dly, "state": st})
		if resp.StatusCode != http.StatusUnprocessableEntity {
			t.Fatalf("state %q = %d, want 422; %s", st, resp.StatusCode, body)
		}
		if code, _ := apiError(t, body); code != "invalid_state" {
			t.Fatalf("state %q code = %q, want invalid_state", st, code)
		}
	}

	// B's principal may post for B; the receipt is by B.
	resp, body = postReceipt(t, env, prnSec, map[string]any{"delivery_id": dly, "state": "processed"})
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("principal receipt = %d, want 204; %s", resp.StatusCode, body)
	}
	got := findReceipt(t, env, a, dly, "processed")
	if len(got) != 1 || got[0].By != b {
		t.Fatalf("principal's receipt = %+v, want one by %s", got, b)
	}

	// Another principal may not.
	_, otherSec := env.NewPrincipal("eve")
	resp, body = postReceipt(t, env, otherSec, map[string]any{"delivery_id": dly, "state": "processed"})
	if code, _ := apiError(t, body); resp.StatusCode != http.StatusForbidden || code != "not_recipient" {
		t.Fatalf("foreign principal receipt = %d %q, want 403 not_recipient", resp.StatusCode, code)
	}
	// An unknown delivery is not the caller's either.
	resp, body = postReceipt(t, env, bSec, map[string]any{"delivery_id": "dly_01JD3Q2K8M4N6P8R0T2V4X6Z8Z", "state": "processed"})
	if code, _ := apiError(t, body); resp.StatusCode != http.StatusForbidden || code != "not_recipient" {
		t.Fatalf("unknown delivery receipt = %d %q, want 403 not_recipient", resp.StatusCode, code)
	}
	// Malformed bodies are 400 invalid_body.
	for _, bad := range []any{
		map[string]any{"delivery_id": dly, "state": "processed", "extra": 1},
		map[string]any{"state": "processed"},
		map[string]any{"delivery_id": dly, "state": 5},
		"not json",
	} {
		resp, body = postReceipt(t, env, bSec, bad)
		if code, _ := apiError(t, body); resp.StatusCode != http.StatusBadRequest || code != "invalid_body" {
			t.Fatalf("bad body %v = %d %q, want 400 invalid_body; %s", bad, resp.StatusCode, code, body)
		}
	}
}
