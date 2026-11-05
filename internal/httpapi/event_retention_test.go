package httpapi_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/eventlog"
	"github.com/XertroV/vecnet/internal/testhub"
)

func TestOwnerRetentionHTTPMergedNarrowedAndDefaultAck(t *testing.T) {
	env := testhub.Start(t)
	ctx := context.Background()
	prn, secret := env.NewPrincipal("synthetic")
	a := adminAddPeer(t, env, map[string]any{"kind": "session", "name": "a", "principal_id": prn}).PeerID
	b := adminAddPeer(t, env, map[string]any{"kind": "session", "name": "b", "principal_id": prn}).PeerID
	_, sender := env.NewPeer("session", "sender")
	first := postSend(t, env, sender, sendBody("retained-before-floor", b), http.StatusAccepted)
	pruned := postSend(t, env, sender, sendBody("pruned-owner", a), http.StatusAccepted)
	lastMessage := postSend(t, env, sender, sendBody("retained-after-floor", b), http.StatusAccepted)
	cursorFor := func(id string) eventlog.Cursor {
		var seq int64
		if err := env.Hub.DB.QueryRow(`SELECT event_seq FROM deliveries WHERE msg_id=?`, id).Scan(&seq); err != nil {
			t.Fatal(err)
		}
		return eventlog.Cursor{Epoch: env.Hub.Log.Epoch(), Seq: seq}
	}
	before := cursorFor(first.MessageID)
	floor := cursorFor(pruned.MessageID)
	last := cursorFor(lastMessage.MessageID)
	if before.Seq >= floor.Seq {
		t.Fatal("history fixture must predate the merged floor")
	}
	// Ack and settle only A. B's older queued delivery remains pinned, so
	// pruning A advances the merged floor beyond B's retained history.
	if _, err := env.Hub.DB.Exec(`UPDATE deliveries SET state='accepted' WHERE msg_id=?`, pruned.MessageID); err != nil {
		t.Fatal(err)
	}
	if err := env.Hub.Log.Ack(ctx, nil, a, "*", floor); err != nil {
		t.Fatal(err)
	}
	env.Now.Advance(8 * 24 * time.Hour)
	if _, err := env.Hub.PruneEventSlice(ctx); err != nil {
		t.Fatal(err)
	}

	resp, body, _ := getEvents(t, env, secret, "?after=e1.0&wait=0", nil)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("gap status=%d", resp.StatusCode)
	}
	var out struct {
		Error struct {
			Code    string `json:"code"`
			Details struct {
				Oldest string `json:"oldest_cursor"`
			} `json:"details"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatal(err)
	}
	if out.Error.Code != "cursor_purged" || out.Error.Details.Oldest != floor.String() {
		t.Fatal("changed cursor_purged shape or floor")
	}
	page := mustPoll(t, env, secret, "?wait=0")
	if len(page.Events) != 1 || page.NextCursor != last.String() {
		t.Fatal("fresh merged stream did not start at safe floor")
	}
	page = mustPoll(t, env, secret, fmt.Sprintf("?peer=%s&after=e1.0&wait=0", b))
	foundBefore, foundLast := false, false
	for _, event := range page.Events {
		foundBefore = foundBefore || event.Cursor == before.String()
		foundLast = foundLast || event.Cursor == last.String()
	}
	if !foundBefore || !foundLast {
		t.Fatal("narrowed owner cannot recover retained delivery events")
	}
	// Protocol 2.3 recovery explicitly resyncs retained Message history,
	// including messages whose delivery cursor is below the combined floor.
	recovered := history(t, env, secret, first.ThreadID, "")
	if len(recovered.Messages) != 1 || recovered.Messages[0].MessageID != first.MessageID || recovered.Messages[0].Content.Text != "hi retained-before-floor" {
		t.Fatal("history resync lost the retained Message below the merged floor")
	}
	recovered = history(t, env, secret, pruned.ThreadID, "")
	if len(recovered.Messages) != 1 || recovered.Messages[0].MessageID != pruned.MessageID {
		t.Fatal("event pruning removed Message history")
	}
	if err := env.Hub.Log.Ack(ctx, nil, prn, "*", eventlog.Cursor{Epoch: 1}); err != nil {
		t.Fatal(err)
	}
	resp, _, _ = getEvents(t, env, secret, "?wait=0", nil)
	if resp.StatusCode != http.StatusConflict {
		t.Fatal("stale saved ack silently rewound")
	}
	page = mustPoll(t, env, secret, "?after=head&wait=0")
	if len(page.Events) != 0 || page.NextCursor != last.String() {
		t.Fatal("head changed")
	}
}

func TestOwnerRetentionDefaultAckFromOldEpochIsPurged(t *testing.T) {
	env := testhub.Start(t)
	p, secret := env.NewPeer("session", "recipient")
	if _, err := env.Hub.DB.Exec(`INSERT INTO acks(owner,scope,seq,acked_at,epoch) VALUES(?,'*',0,'2026-01-01T00:00:00.000Z',999)`, p); err != nil {
		t.Fatal(err)
	}
	resp, _, _ := getEvents(t, env, secret, "?wait=0", nil)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("stale epoch ack silently rewound: HTTP %d", resp.StatusCode)
	}
}
