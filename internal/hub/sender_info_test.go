package hub_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/eventlog"
	"github.com/XertroV/vecnet/internal/hub"
	"github.com/XertroV/vecnet/internal/ids"
	"github.com/XertroV/vecnet/internal/store"
	"github.com/XertroV/vecnet/internal/testhub"
)

func wantSenderInfo(t *testing.T, where string, got *envelope.SenderInfo, name, kind string) {
	t.Helper()
	if got == nil || got.Name != name || got.Kind != kind {
		t.Fatalf("%s: sender_info = %+v, want {%s %s}", where, got, name, kind)
	}
}

// historyMessage returns msgID from GET /v1/threads/{thr}/messages as secret.
func historyMessage(t *testing.T, env *testhub.Env, secret, thr, msgID string) envelope.Message {
	t.Helper()
	resp, raw := env.Do(secret, "GET", "/v1/threads/"+thr+"/messages", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("history = %d: %s", resp.StatusCode, raw)
	}
	var body struct {
		Messages []envelope.Message `json:"messages"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	for _, m := range body.Messages {
		if m.MessageID == msgID {
			return m
		}
	}
	t.Fatalf("history of %s lacks %s: %s", thr, msgID, raw)
	return envelope.Message{}
}

func storedMessage(t *testing.T, env *testhub.Env, msgID string) envelope.Message {
	t.Helper()
	var body string
	if err := env.Hub.DB.QueryRow(`SELECT body FROM messages WHERE msg_id = ?`, msgID).Scan(&body); err != nil {
		t.Fatal(err)
	}
	var m envelope.Message
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		t.Fatal(err)
	}
	return m
}

// TestSenderInfoSnapshotAtSend: the hub stores the sender's directory name
// and kind with the Message at send time; renaming the peer afterwards
// leaves that Message's sender_info alone, in the event log and in thread
// history, while the next send carries the new name (PROTOCOL §2.2 step 12).
func TestSenderInfoSnapshotAtSend(t *testing.T) {
	env := testhub.Start(t)
	a, aSec := env.NewPeer("pagchat-bot", "Alice")
	b, bSec := env.NewPeer("session", "B")

	sr := mustSend(t, env, aSec, req("k1", b))
	evs := deliveryEvents(t, env, b)
	if len(evs) != 1 {
		t.Fatalf("B has %d deliveries, want 1", len(evs))
	}
	_, _, m := decodeDelivery(t, evs[0])
	wantSenderInfo(t, "delivery", m.SenderInfo, "Alice", "pagchat-bot")
	wantSenderInfo(t, "stored", storedMessage(t, env, sr.MessageID).SenderInfo, "Alice", "pagchat-bot")
	wantSenderInfo(t, "history", historyMessage(t, env, bSec, sr.ThreadID, sr.MessageID).SenderInfo, "Alice", "pagchat-bot")

	if _, err := env.Hub.DB.Exec(`UPDATE peers SET name = 'Renamed' WHERE peer_id = ?`, a); err != nil {
		t.Fatal(err)
	}
	_, _, m = decodeDelivery(t, deliveryEvents(t, env, b)[0])
	wantSenderInfo(t, "delivery after rename", m.SenderInfo, "Alice", "pagchat-bot")
	wantSenderInfo(t, "history after rename", historyMessage(t, env, bSec, sr.ThreadID, sr.MessageID).SenderInfo, "Alice", "pagchat-bot")

	next := req("k2")
	next["thread_id"] = sr.ThreadID
	sr2 := mustSend(t, env, aSec, next)
	wantSenderInfo(t, "next send", historyMessage(t, env, bSec, sr.ThreadID, sr2.MessageID).SenderInfo, "Renamed", "pagchat-bot")
	wantSenderInfo(t, "old message", historyMessage(t, env, bSec, sr.ThreadID, sr.MessageID).SenderInfo, "Alice", "pagchat-bot")
}

// TestSenderInfoNamesThePeerNotThePrincipal: a principal sending as one of
// its peers gets that peer's entry; the operator gets its operator peer's.
func TestSenderInfoNamesThePeerNotThePrincipal(t *testing.T) {
	env := testhub.Start(t)
	b, bSec := env.NewPeer("session", "B")
	prn, prnSec := env.NewPrincipal("pagd-host")
	p, _ := peerUnder(t, env, "pagchat-bot", "pag0", prn)

	body := req("as-send", b)
	body["as"] = p
	sr := mustSend(t, env, prnSec, body)
	wantSenderInfo(t, "principal as peer", historyMessage(t, env, bSec, sr.ThreadID, sr.MessageID).SenderInfo, "pag0", "pagchat-bot")

	op := mustSend(t, env, env.OperatorSecret, req("op-send", b))
	var opName string
	if err := env.Hub.DB.QueryRow(`SELECT name FROM peers WHERE kind = 'operator'`).Scan(&opName); err != nil {
		t.Fatal(err)
	}
	wantSenderInfo(t, "operator", historyMessage(t, env, bSec, op.ThreadID, op.MessageID).SenderInfo, opName, "operator")
}

// TestSenderInfoSourceHubAndReaction covers the other Message authors: an
// inbound webhook source ({source name, "source"}), the hub's own expiry
// response ({"hub", "hub"}) and a reaction wake (the reactor's entry).
func TestSenderInfoSourceHubAndReaction(t *testing.T) {
	env := testhub.Start(t)
	ctx := context.Background()
	a, aSec := env.NewPeer("session", "A")
	b, bSec := env.NewPeer("grokbot", "Bee")

	src, _, _, err := env.Hub.AddSource(ctx, "github", "hark-github", b, nil, "hold")
	if err != nil {
		t.Fatal(err)
	}
	msgID, dropped, err := env.Hub.IngestSource(ctx, hub.SourceMessage{SrcID: src, ProviderDeliveryID: "d1",
		OriginThreadID: "issue/1", OriginEventID: "event/1", EventName: "issues.opened", Summary: "opened",
		Raw: []byte(`{}`), MediaType: "application/json"})
	if err != nil || dropped {
		t.Fatalf("IngestSource = %s %v %v", msgID, dropped, err)
	}
	wantSenderInfo(t, "source", storedMessage(t, env, msgID).SenderInfo, "hark-github", "source")

	sent := mustSend(t, env, aSec, req("rx", b))
	wake := needWake(t, rx(t, env, bSec, sent.MessageID, "✅", "add", "wake"))
	wantSenderInfo(t, "reaction wake", storedMessage(t, env, wake.MessageID).SenderInfo, "Bee", "grokbot")

	apr := ids.New(ids.Apr)
	ar := mustSend(t, env, aSec, approvalReq("q1", apr, env.Now.Now().Add(time.Hour), []string{"yes", "no"}, b))
	env.Now.Advance(2 * time.Hour)
	if n, err := env.Hub.ExpireApprovals(ctx, env.Now.Now()); err != nil || n != 1 {
		t.Fatalf("ExpireApprovals = %d, %v", n, err)
	}
	var hubMsg string
	if err := env.Hub.DB.QueryRow(`SELECT msg_id FROM messages WHERE thr_id = ? AND sender = 'hub'`, ar.ThreadID).Scan(&hubMsg); err != nil {
		t.Fatal(err)
	}
	wantSenderInfo(t, "hub expiry", storedMessage(t, env, hubMsg).SenderInfo, "hub", "hub")
	for _, raw := range deliveryEvents(t, env, a) {
		if _, _, m := decodeDelivery(t, raw); m.MessageID == hubMsg {
			wantSenderInfo(t, "hub expiry delivery", m.SenderInfo, "hub", "hub")
		}
	}
}

// TestSenderInfoBackfillForLegacyRows: a Message stored before migration
// 0018 has no sender_info; the migration fills it, in the stored body and in
// every delivery Event, from the sender's directory entry as of the
// migration (not a send-time snapshot), and every read surface then serves
// it. A row that already has sender_info is not touched.
func TestSenderInfoBackfillForLegacyRows(t *testing.T) {
	env := testhub.Start(t)
	ctx := context.Background()
	a, aSec := env.NewPeer("session", "Old")
	b, bSec := env.NewPeer("session", "B")
	legacy := mustSend(t, env, aSec, req("legacy", b))
	kept := mustSend(t, env, bSec, req("kept", a))

	// Make the first send a legacy row, then rename its sender.
	for _, q := range []string{
		`UPDATE messages SET body = json_remove(body, '$.sender_info') WHERE msg_id = '` + legacy.MessageID + `'`,
		`UPDATE events SET data = json_remove(data, '$.message.sender_info') WHERE type = 'delivery' AND json_extract(data, '$.message.message_id') = '` + legacy.MessageID + `'`,
		`UPDATE peers SET name = 'New' WHERE peer_id = '` + a + `'`,
		`UPDATE peers SET name = 'B2' WHERE peer_id = '` + b + `'`,
		`DELETE FROM schema_migrations WHERE name = '0018_sender_info.sql'`,
	} {
		if _, err := env.Hub.DB.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	if m := storedMessage(t, env, legacy.MessageID); m.SenderInfo != nil {
		t.Fatalf("legacy setup kept sender_info %+v", m.SenderInfo)
	}
	applied, err := store.Migrate(ctx, env.Hub.DB.DB)
	if err != nil || len(applied) != 1 || applied[0] != "0018_sender_info.sql" {
		t.Fatalf("Migrate = %v, %v", applied, err)
	}

	wantSenderInfo(t, "legacy stored", storedMessage(t, env, legacy.MessageID).SenderInfo, "New", "session")
	wantSenderInfo(t, "legacy history", historyMessage(t, env, bSec, legacy.ThreadID, legacy.MessageID).SenderInfo, "New", "session")
	evs, err := env.Hub.Log.Read(ctx, []string{b}, eventlog.Cursor{Epoch: env.Hub.Log.Epoch()}, 0)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, ev := range evs {
		if ev.Type != "delivery" {
			continue
		}
		if _, _, m := decodeDelivery(t, ev.Data); m.MessageID == legacy.MessageID {
			found = true
			wantSenderInfo(t, "legacy delivery", m.SenderInfo, "New", "session")
		}
	}
	if !found {
		t.Fatal("legacy delivery event missing")
	}
	// The row sent with a snapshot keeps it: B was renamed to B2 after.
	wantSenderInfo(t, "snapshot row", storedMessage(t, env, kept.MessageID).SenderInfo, "B", "session")
}
