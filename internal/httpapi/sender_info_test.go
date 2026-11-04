package httpapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/opapi"
	"github.com/XertroV/vecnet/internal/testhub"
)

func deliverySenderInfo(t *testing.T, where string, data []byte) *envelope.SenderInfo {
	t.Helper()
	if err := envelope.Validate("delivery", data); err != nil {
		t.Fatalf("%s: delivery violates the schema: %v; %s", where, err, data)
	}
	var d envelope.Delivery
	if err := json.Unmarshal(data, &d); err != nil {
		t.Fatalf("%s: %v", where, err)
	}
	return d.Message.SenderInfo
}

func wantInfo(t *testing.T, where string, got *envelope.SenderInfo, name, kind string) {
	t.Helper()
	if got == nil || got.Name != name || got.Kind != kind {
		t.Fatalf("%s: sender_info = %+v, want {%s %s}", where, got, name, kind)
	}
}

// TestSenderInfoOnEveryRecipientSurface: the long poll, the SSE stream,
// thread history and the operator inbox all carry the send-time
// sender_info, so a consumer never needs GET /v1/peers to name the sender
// (PROTOCOL §2.2 step 12, §14).
func TestSenderInfoOnEveryRecipientSurface(t *testing.T) {
	env := testhub.Start(t)
	_, aSec := env.NewPeer("pagchat-bot", "pag0")
	b, bSec := env.NewPeer("session", "b")

	cur, err := env.Hub.Log.Oldest(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	s := openSSE(t, env, bSec, "", nil).start()
	s.expectPreamble(t, cur.String())
	sr := postSend(t, env, aSec, sendBody("si-1", b, env.OperatorPeer), http.StatusAccepted)

	f := s.nextEvent(t, 10*time.Second)
	var ev envelope.Event
	if err := json.Unmarshal([]byte(f.Data), &ev); err != nil || ev.Type != "delivery" {
		t.Fatalf("SSE frame %+v: %v", f, err)
	}
	wantInfo(t, "SSE", deliverySenderInfo(t, "SSE", ev.Data), "pag0", "pagchat-bot")

	pr := mustPoll(t, env, bSec, "?after="+zeroCursor(env)+"&wait=0")
	n := 0
	for _, ev := range pr.Events {
		if ev.Type == "delivery" {
			n++
			wantInfo(t, "long poll", deliverySenderInfo(t, "long poll", ev.Data), "pag0", "pagchat-bot")
		}
	}
	if n != 1 {
		t.Fatalf("long poll has %d deliveries, want 1", n)
	}

	h := history(t, env, bSec, sr.ThreadID, "")
	if len(h.Messages) != 1 {
		t.Fatalf("history = %+v", h)
	}
	wantInfo(t, "history", h.Messages[0].SenderInfo, "pag0", "pagchat-bot")
	opHist := history(t, env, env.OperatorSecret, sr.ThreadID, "")
	wantInfo(t, "operator history", opHist.Messages[0].SenderInfo, "pag0", "pagchat-bot")

	resp, raw := env.Do(env.OperatorSecret, "GET", "/v1/admin/inbox", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("operator inbox = %d: %s", resp.StatusCode, raw)
	}
	var inbox opapi.Inbox
	if err := json.Unmarshal(raw, &inbox); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, d := range inbox.Deliveries {
		if d.MessageID == sr.MessageID {
			found = true
			wantInfo(t, "operator inbox", d.SenderInfo, "pag0", "pagchat-bot")
		}
	}
	if !found {
		t.Fatalf("operator inbox lacks %s: %s", sr.MessageID, raw)
	}
}
