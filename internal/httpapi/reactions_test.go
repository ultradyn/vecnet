package httpapi_test

import (
	"encoding/json"
	"sync/atomic"
	"testing"

	"github.com/XertroV/vecnet/internal/httpapi"
	"github.com/XertroV/vecnet/internal/opapi"
	"github.com/XertroV/vecnet/internal/testhub"
)

func TestPostReactionsShape(t *testing.T) {
	env := testhub.Start(t)
	_, as := env.NewPeer("session", "A")
	b, bs := env.NewPeer("session", "B")
	m := postSend(t, env, as, sendBody("reaction-shape", b), 202)
	for _, body := range []map[string]any{
		{"message_id": m.MessageID, "emoji": "✅", "op": "add", "extra": true},
		{"message_id": m.MessageID, "emoji": "✅"},
		{"message_id": m.MessageID, "emoji": 1, "op": "add"},
		{"message_id": m.MessageID, "emoji": "✅", "op": "add", "as": nil},
		{"message_id": m.MessageID, "emoji": "✅", "op": "add", "priority": false},
	} {
		r, raw := env.Do(bs, "POST", "/v1/reactions", body)
		mustErr(t, r, raw, 400, "invalid_body")
	}
	var kicks atomic.Int32
	env.Configure(func(d *httpapi.Deps) { d.Kick = func() { kicks.Add(1) } })
	r, raw := env.Do(bs, "POST", "/v1/reactions", map[string]any{"message_id": m.MessageID, "emoji": "✅", "op": "add"})
	if r.StatusCode != 200 {
		t.Fatalf("reaction=%d %s", r.StatusCode, raw)
	}
	var result opapi.ReactionResult
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	if !result.Changed || result.MessageID != m.MessageID || result.ThreadID != m.ThreadID || result.Superseded == nil || len(result.Superseded) != 0 || result.Reactions == nil || result.Wake != nil {
		t.Fatalf("result=%+v", result)
	}
	if kicks.Load() != 1 {
		t.Fatalf("kick=%d", kicks.Load())
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	if string(fields["superseded"]) != "[]" || string(fields["wake"]) != "null" {
		t.Fatalf("wire=%s", raw)
	}
}
func TestPostReactionsRetryAfter(t *testing.T) {
	env := testhub.Start(t)
	a, as := env.NewPeer("session", "A")
	b, _ := env.NewPeer("session", "B")
	env.SetLimits(a, 1, 20)
	m := postSend(t, env, as, sendBody("reaction-limit", b), 202)
	r, raw := env.Do(as, "POST", "/v1/reactions", map[string]any{"message_id": m.MessageID, "emoji": "✅", "op": "add"})
	mustErr(t, r, raw, 429, "rate_limited")
	if r.Header.Get("Retry-After") != "60" {
		t.Fatalf("Retry-After=%q", r.Header.Get("Retry-After"))
	}
}
