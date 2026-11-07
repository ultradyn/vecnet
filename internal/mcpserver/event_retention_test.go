package mcpserver_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/XertroV/vecnet/internal/eventlog"
	"github.com/XertroV/vecnet/internal/testhub"
	"github.com/XertroV/vecnet/internal/webhook"
)

func TestOwnerRetentionMCPResourceAndSubscribeFloors(t *testing.T) {
	env := testhub.Start(t)
	ctx := context.Background()
	prn, secret := env.NewPrincipal("principal")
	a, as := peerUnder(t, env, prn, "a")
	b, bs := peerUnder(t, env, prn, "b")
	appendEvent := func(owner string) eventlog.Cursor {
		c, err := env.Hub.Log.Append(ctx, nil, owner, "thread_updated", env.Hub.Now(), map[string]any{"thread_id": "thr_synthetic"})
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	appendEvent(b)
	floor := appendEvent(a)
	appendEvent(b)
	if _, err := env.Hub.DB.Exec(`DELETE FROM events WHERE owner=?`, a); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ secret, uri string }{{secret, prnURI(prn)}, {bs, peerURI(b)}} {
		r := rpc2026(t, env, c.secret, "resources/read", map[string]any{"uri": c.uri})
		if r.Error != nil {
			t.Fatal("resource default replay used global floor")
		}
	}
	// The MCP inbox default and resource tail must both resolve owner floors.
	r := rpc2026(t, env, as, "tools/call", map[string]any{"name": "vecnet_inbox", "arguments": map[string]any{}})
	if r.Error != nil {
		t.Fatal("MCP inbox default failed")
	}
	var tool struct {
		IsError bool `json:"isError"`
	}
	if err := json.Unmarshal(r.Result, &tool); err != nil {
		t.Fatal(err)
	}
	if tool.IsError {
		t.Fatal("MCP inbox default used another owner floor")
	}
	cb := testhub.NewCallback(t)
	one := subscribeOK(t, env, as, subParams(cb.URL, webhook.NewSecret(), map[string]any{"cursor": "e1.0"}))
	if !one.Truncated || one.Cursor != floor.String() {
		t.Fatalf("own purged cursor result=%+v", one)
	}
	two := subscribeOK(t, env, bs, subParams(cb.URL, webhook.NewSecret(), map[string]any{"cursor": "e1.0"}))
	if two.Truncated || two.Cursor != "e1.0" {
		t.Fatalf("unpurged owner cursor result=%+v", two)
	}
	exact := subscribeOK(t, env, as, subParams(cb.URL, webhook.NewSecret(), map[string]any{"cursor": floor.String()}))
	if exact.Truncated {
		t.Fatal("exact floor truncated")
	}
}
