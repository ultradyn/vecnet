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
	"github.com/XertroV/vecnet/internal/testhub"
)

// addTeam inserts an operator-managed team with members directly (the admin
// teams API is Task 17).
func addTeam(t *testing.T, env *testhub.Env, members ...string) string {
	t.Helper()
	id := ids.New(ids.Team)
	if _, err := env.Hub.DB.Exec(`INSERT INTO teams(team_id, name, created_at) VALUES (?, ?, ?)`,
		id, "t", envelope.FormatTime(env.Now.Now())); err != nil {
		t.Fatal(err)
	}
	for _, m := range members {
		if _, err := env.Hub.DB.Exec(`INSERT INTO team_members(team_id, peer_id) VALUES (?, ?)`, id, m); err != nil {
			t.Fatal(err)
		}
	}
	return id
}

// presenceEvents returns the presence Events in peer's log, schema-checked.
func presenceEvents(t *testing.T, env *testhub.Env, peer string) []envelope.Presence {
	t.Helper()
	evs, err := env.Hub.Log.Read(context.Background(), []string{peer}, eventlog.Cursor{Epoch: env.Hub.Log.Epoch()}, 0)
	if err != nil {
		t.Fatal(err)
	}
	var out []envelope.Presence
	for _, ev := range evs {
		if ev.Type != "presence" {
			continue
		}
		if err := envelope.Validate("presence", ev.Data); err != nil {
			t.Fatalf("presence Event data is not schema-valid: %v; %s", err, ev.Data)
		}
		var p envelope.Presence
		if err := json.Unmarshal(ev.Data, &p); err != nil {
			t.Fatal(err)
		}
		out = append(out, p)
	}
	return out
}

// setSubscribe PATCHes /v1/peers/me and fails unless the answer is 204.
func setSubscribe(t *testing.T, env *testhub.Env, secret, v string) {
	t.Helper()
	resp, body := env.Do(secret, "PATCH", "/v1/peers/me", map[string]string{"subscribe_presence": v})
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("PATCH /v1/peers/me %s = %d; %s", v, resp.StatusCode, body)
	}
}

// reportPresence POSTs /v1/presence with state at the hub's now and fails
// unless the answer is 204.
func reportPresence(t *testing.T, env *testhub.Env, secret, query, state string) {
	t.Helper()
	resp, body := env.Do(secret, "POST", "/v1/presence"+query,
		map[string]any{"state": state, "at": envelope.FormatTime(env.Now.Now())})
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("POST /v1/presence%s %s = %d; %s", query, state, resp.StatusCode, body)
	}
}

// directoryPresence returns peer's presence as the Directory shows it.
func directoryPresence(t *testing.T, env *testhub.Env, peer string) string {
	t.Helper()
	dir, err := env.Hub.Directory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range dir {
		if e.PeerID == peer {
			return e.Presence
		}
	}
	t.Fatalf("peer %s missing from the directory", peer)
	return ""
}

func TestPresenceFanOutRespectsSubscription(t *testing.T) {
	env := testhub.Start(t)
	a, _ := env.NewPeer("grokbot", "a")
	b, bSec := env.NewPeer("session", "b")
	c, cSec := env.NewPeer("dot", "c")
	d, dSec := env.NewPeer("openclaw", "d")
	addTeam(t, env, a, b)
	setSubscribe(t, env, cSec, "all")
	setSubscribe(t, env, dSec, "none")
	if n := count(t, env, `SELECT count(*) FROM peers WHERE peer_id = ? AND subscribe_presence = 'all'`, c); n != 1 {
		t.Fatalf("c subscribe_presence not stored as all")
	}

	resp, body := env.Do(cSec, "PATCH", "/v1/peers/me", map[string]string{"subscribe_presence": "some"})
	if code, _ := apiError(t, body); resp.StatusCode != http.StatusUnprocessableEntity || code != "invalid_value" {
		t.Fatalf("PATCH bad subscribe_presence = %d %q, want 422 invalid_value", resp.StatusCode, code)
	}

	reportPresence(t, env, bSec, "", "working")

	for _, p := range []string{a, c} {
		got := presenceEvents(t, env, p)
		if len(got) != 1 {
			t.Fatalf("%s got %d presence Events, want 1", p, len(got))
		}
		if got[0].PeerID != b || got[0].State != "working" {
			t.Fatalf("%s presence Event = %+v, want b working", p, got[0])
		}
	}
	for name, p := range map[string]string{"d (none)": d, "b (self)": b, "operator (teams, no teammate)": env.OperatorPeer} {
		if got := presenceEvents(t, env, p); len(got) != 0 {
			t.Fatalf("%s got %d presence Events, want 0", name, len(got))
		}
	}
	if s := directoryPresence(t, env, b); s != "working" {
		t.Fatalf("directory presence of b = %q, want working", s)
	}

	// A principal reports for its peer with ?as=; without it the hub cannot tell which peer.
	prn, prnSec := env.NewPrincipal("pc")
	e, _ := peerUnder(t, env, "pagchat-bot", "e", prn)
	addTeam(t, env, a, e)
	reportPresence(t, env, prnSec, "?as="+e, "blocked")
	if s := directoryPresence(t, env, e); s != "blocked" {
		t.Fatalf("directory presence of e = %q, want blocked", s)
	}
	if got := presenceEvents(t, env, a); len(got) != 2 || got[1].PeerID != e || got[1].State != "blocked" {
		t.Fatalf("a's presence Events = %+v, want b working then e blocked", got)
	}
	resp, body = env.Do(prnSec, "POST", "/v1/presence", map[string]any{"state": "idle", "at": envelope.FormatTime(env.Now.Now())})
	if code, _ := apiError(t, body); resp.StatusCode != http.StatusUnprocessableEntity || code != "as_required" {
		t.Fatalf("principal presence without as = %d %q, want 422 as_required", resp.StatusCode, code)
	}
	resp, body = env.Do(prnSec, "POST", "/v1/presence?as="+b, map[string]any{"state": "idle", "at": envelope.FormatTime(env.Now.Now())})
	if code, _ := apiError(t, body); resp.StatusCode != http.StatusForbidden || code != "forbidden" {
		t.Fatalf("principal presence for a foreign peer = %d %q, want 403 forbidden", resp.StatusCode, code)
	}
	// Schema violations are 400 invalid_body.
	for _, bad := range []map[string]any{
		{"state": "asleep", "at": envelope.FormatTime(env.Now.Now())},
		{"state": "idle"},
		{"state": "idle", "at": "yesterday"},
		{"state": "idle", "at": envelope.FormatTime(env.Now.Now()), "mood": "ok"},
		{"state": "idle", "at": envelope.FormatTime(env.Now.Now()), "quota": map[string]any{"weekly_remaining_pct": 101}},
	} {
		resp, body = env.Do(bSec, "POST", "/v1/presence", bad)
		if code, _ := apiError(t, body); resp.StatusCode != http.StatusBadRequest || code != "invalid_body" {
			t.Fatalf("presence %v = %d %q, want 400 invalid_body; %s", bad, resp.StatusCode, code, body)
		}
	}
}

func TestPresenceGoesStale(t *testing.T) {
	env := testhub.Start(t)
	s, sSec := env.NewPeer("session", "s")
	g, gSec := env.NewPeer("grokbot", "g")
	n, _ := env.NewPeer("dot", "n")

	if p := directoryPresence(t, env, n); p != "unknown" {
		t.Fatalf("never-reported presence = %q, want unknown", p)
	}
	reportPresence(t, env, sSec, "", "idle")
	reportPresence(t, env, gSec, "", "idle")
	if p := directoryPresence(t, env, s); p != "idle" {
		t.Fatalf("fresh session presence = %q, want idle", p)
	}

	env.Now.Advance(16 * time.Minute)
	if p := directoryPresence(t, env, s); p != "unknown" {
		t.Fatalf("session presence after 16m = %q, want unknown", p)
	}
	if p := directoryPresence(t, env, g); p != "idle" {
		t.Fatalf("grokbot presence after 16m = %q, want idle", p)
	}

	env.Now.Advance(43 * time.Minute) // 59m since the report
	if p := directoryPresence(t, env, g); p != "idle" {
		t.Fatalf("grokbot presence after 59m = %q, want idle", p)
	}
	env.Now.Advance(2 * time.Minute) // 61m
	if p := directoryPresence(t, env, g); p != "unknown" {
		t.Fatalf("grokbot presence after 61m = %q, want unknown", p)
	}

	// EffectivePresence is the same rule over a bare row.
	at := envelope.FormatTime(env.Now.Now().Add(-14 * time.Minute))
	idle := "idle"
	now := env.Now.Now()
	if got := env.Hub.EffectivePresence(hub.PresenceRow{Kind: "session", State: &idle, At: &at}, now); got != "idle" {
		t.Fatalf("EffectivePresence(session, 14m) = %q, want idle", got)
	}
	if got := env.Hub.EffectivePresence(hub.PresenceRow{Kind: "session"}, now); got != "unknown" {
		t.Fatalf("EffectivePresence(no report) = %q, want unknown", got)
	}
}
