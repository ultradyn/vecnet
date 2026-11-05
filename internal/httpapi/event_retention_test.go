package httpapi_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/XertroV/vecnet/internal/eventlog"
	"github.com/XertroV/vecnet/internal/testhub"
)

func TestOwnerRetentionHTTPMergedNarrowedAndDefaultAck(t *testing.T) {
	env := testhub.Start(t)
	ctx := context.Background()
	prn, secret := env.NewPrincipal("synthetic")
	a := adminAddPeer(t, env, map[string]any{"kind": "session", "name": "a", "principal_id": prn}).PeerID
	b := adminAddPeer(t, env, map[string]any{"kind": "session", "name": "b", "principal_id": prn}).PeerID
	// Existing admin helper returns the id string.
	appendEvent := func(owner string) eventlog.Cursor {
		c, err := env.Hub.Log.Append(ctx, nil, owner, "thread_updated", env.Hub.Now(), map[string]any{"thread_id": "thr_synthetic"})
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	appendEvent(b)
	floor := appendEvent(a)
	last := appendEvent(b)
	if _, err := env.Hub.DB.Exec(`DELETE FROM events WHERE owner=?`, a); err != nil {
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
	if len(page.Events) != 2 {
		t.Fatal("narrowed owner cannot recover retained events")
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
