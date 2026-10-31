package httpapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/eventlog"
	"github.com/XertroV/vecnet/internal/ids"
	"github.com/XertroV/vecnet/internal/testhub"
)

// directoryKeys are the public directory fields (PROTOCOL §8); nothing else
// may appear in a GET /v1/peers entry.
var directoryKeys = []string{
	"capabilities", "enrolled_at", "kind", "last_seen", "location", "name",
	"peer_id", "presence", "quota", "reachability", "teams",
}

// getPeers GETs /v1/peers as secret and returns the raw entries keyed by
// peer_id plus the raw body.
func getPeers(t *testing.T, env *testhub.Env, secret string) (map[string]map[string]json.RawMessage, []byte) {
	t.Helper()
	resp, body := env.Do(secret, "GET", "/v1/peers", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /v1/peers = %d; %s", resp.StatusCode, body)
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(body, &top); err != nil {
		t.Fatalf("decode /v1/peers: %v; %s", err, body)
	}
	if len(top) != 1 || top["peers"] == nil {
		t.Fatalf("GET /v1/peers top-level keys = %v, want only peers", top)
	}
	var list []map[string]json.RawMessage
	if err := json.Unmarshal(top["peers"], &list); err != nil {
		t.Fatalf("decode peers: %v; %s", err, body)
	}
	out := map[string]map[string]json.RawMessage{}
	for _, e := range list {
		var id string
		if err := json.Unmarshal(e["peer_id"], &id); err != nil || id == "" {
			t.Fatalf("entry without peer_id: %v", e)
		}
		out[id] = e
	}
	return out, body
}

func str(t *testing.T, raw json.RawMessage) string {
	t.Helper()
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Fatalf("not a string: %s", raw)
	}
	return s
}

// reachability polls GET /v1/peers (as secret) until peer's reachability is
// want, failing after 10 s.
func waitReachability(t *testing.T, env *testhub.Env, secret, peer, want string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		dir, _ := getPeers(t, env, secret)
		got := str(t, dir[peer]["reachability"])
		if got == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("reachability of %s = %q, want %q", peer, got, want)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestDirectoryVisibleToAll(t *testing.T) {
	env := testhub.Start(t)
	a, aSec := env.NewPeer("grokbot", "alpha")
	b, bSec := env.NewPeer("dot", "bravo")
	c, cSec := env.NewPeer("session", "charlie")
	_, prnSec := env.NewPrincipal("pc")

	// A push route for A: its URL and whsec are operator-only.
	routeURL := "https://example.invalid/hook/alpha-route"
	route := "rte_" + ids.New(ids.Peer)[5:]
	now := envelope.FormatTime(env.Now.Now())
	if _, err := env.Hub.DB.Exec(`INSERT INTO routes(route_id, owner_kind, owner_id, url, created_at) VALUES (?, 'peer', ?, ?, ?)`,
		route, a, routeURL, now); err != nil {
		t.Fatal(err)
	}
	if _, err := env.Hub.DB.Exec(`INSERT INTO route_keys(route_id, whsec, created_at) VALUES (?, 'whsec_c2VjcmV0c2VjcmV0', ?)`, route, now); err != nil {
		t.Fatal(err)
	}
	if _, err := env.Hub.DB.Exec(`UPDATE peers SET route_id = ? WHERE peer_id = ?`, route, a); err != nil {
		t.Fatal(err)
	}
	team := ids.New(ids.Team)
	if _, err := env.Hub.DB.Exec(`INSERT INTO teams(team_id, name, created_at) VALUES (?, 'ab', ?)`, team, now); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{a, b} {
		if _, err := env.Hub.DB.Exec(`INSERT INTO team_members(team_id, peer_id) VALUES (?, ?)`, team, p); err != nil {
			t.Fatal(err)
		}
	}

	want := []string{env.OperatorPeer, a, b, c}
	slices.Sort(want)
	for name, secret := range map[string]string{"a": aSec, "c": cSec, "principal": prnSec, "operator": env.OperatorSecret} {
		dir, body := getPeers(t, env, secret)
		var gotIDs []string
		for id := range dir {
			gotIDs = append(gotIDs, id)
		}
		slices.Sort(gotIDs)
		if !slices.Equal(gotIDs, want) {
			t.Fatalf("%s sees peers %v, want %v", name, gotIDs, want)
		}
		for id, e := range dir {
			var keys []string
			for k := range e {
				keys = append(keys, k)
			}
			slices.Sort(keys)
			if !slices.Equal(keys, directoryKeys) {
				t.Fatalf("%s: entry %s keys = %v, want exactly %v", name, id, keys, directoryKeys)
			}
		}
		lower := strings.ToLower(string(body))
		for _, bad := range []string{"vn_", "whsec", "secret", "hash", "route", routeURL, "example.invalid",
			aSec, bSec, cSec, prnSec, env.OperatorSecret, "credential", "disabled", "subscribe"} {
			if strings.Contains(lower, strings.ToLower(bad)) {
				t.Fatalf("%s: GET /v1/peers leaks %q: %s", name, bad, body)
			}
		}
		if k := str(t, dir[env.OperatorPeer]["kind"]); k != "operator" {
			t.Fatalf("operator entry kind = %q", k)
		}
	}

	dir, _ := getPeers(t, env, aSec)
	if n := str(t, dir[a]["name"]); n != "alpha" {
		t.Fatalf("a name = %q, want alpha", n)
	}
	if k := str(t, dir[b]["kind"]); k != "dot" {
		t.Fatalf("b kind = %q, want dot", k)
	}
	if s := str(t, dir[a]["enrolled_at"]); s != now {
		t.Fatalf("a enrolled_at = %q, want %q", s, now)
	}
	if string(dir[a]["capabilities"]) != "[]" || string(dir[a]["quota"]) != "{}" {
		t.Fatalf("a capabilities/quota = %s / %s, want [] / {}", dir[a]["capabilities"], dir[a]["quota"])
	}
	if string(dir[a]["location"]) != "null" {
		t.Fatalf("a location = %s, want null", dir[a]["location"])
	}
	var teams []string
	if err := json.Unmarshal(dir[a]["teams"], &teams); err != nil || !slices.Equal(teams, []string{team}) {
		t.Fatalf("a teams = %s, want [%s]", dir[a]["teams"], team)
	}
	if string(dir[c]["teams"]) != "[]" {
		t.Fatalf("c teams = %s, want []", dir[c]["teams"])
	}
	if p := str(t, dir[b]["presence"]); p != "unknown" {
		t.Fatalf("b presence before any report = %q, want unknown", p)
	}
	// B has not called the API yet: no observation.
	if r := str(t, dir[b]["reachability"]); r != "unknown" {
		t.Fatalf("b reachability before any call = %q, want unknown", r)
	}
	if string(dir[b]["last_seen"]) != "null" {
		t.Fatalf("b last_seen before any call = %s, want null", dir[b]["last_seen"])
	}

	env.Now.Advance(time.Second)
	getPeers(t, env, bSec)
	dir, _ = getPeers(t, env, aSec)
	if r := str(t, dir[b]["reachability"]); r != "reachable" {
		t.Fatalf("b reachability after calling the API = %q, want reachable", r)
	}
	if s := str(t, dir[b]["last_seen"]); s != envelope.FormatTime(env.Now.Now()) {
		t.Fatalf("b last_seen = %q, want %q", s, envelope.FormatTime(env.Now.Now()))
	}

	// Self-reported fields show up for everyone.
	resp, body := env.Do(cSec, "POST", "/v1/presence", map[string]any{
		"state": "working", "at": envelope.FormatTime(env.Now.Now()),
		"capabilities": []string{"go", "review"}, "quota": map[string]any{"weekly_remaining_pct": 40},
		"location": "xsm",
	})
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("POST /v1/presence = %d; %s", resp.StatusCode, body)
	}
	dir, _ = getPeers(t, env, bSec)
	if p := str(t, dir[c]["presence"]); p != "working" {
		t.Fatalf("c presence = %q, want working", p)
	}
	if string(dir[c]["capabilities"]) != `["go","review"]` || string(dir[c]["quota"]) != `{"weekly_remaining_pct":40}` {
		t.Fatalf("c capabilities/quota = %s / %s", dir[c]["capabilities"], dir[c]["quota"])
	}
	if l := str(t, dir[c]["location"]); l != "xsm" {
		t.Fatalf("c location = %q, want xsm", l)
	}
	// A later report without the optional fields keeps them.
	resp, body = env.Do(cSec, "POST", "/v1/presence", map[string]any{"state": "idle", "at": envelope.FormatTime(env.Now.Now())})
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("POST /v1/presence = %d; %s", resp.StatusCode, body)
	}
	dir, _ = getPeers(t, env, bSec)
	if string(dir[c]["capabilities"]) != `["go","review"]` || str(t, dir[c]["presence"]) != "idle" {
		t.Fatalf("c after a bare report: presence %s capabilities %s", dir[c]["presence"], dir[c]["capabilities"])
	}

	// An open stream is connected; closing it falls back to reachable.
	s := openSSE(t, env, cSec, "", nil).start()
	s.expectPreamble(t, "")
	waitReachability(t, env, aSec, c, "connected")
	getPeers(t, env, cSec) // another call while the stream is open keeps it connected
	waitReachability(t, env, aSec, c, "connected")
	s.cancel()
	s.resp.Body.Close()
	waitStreams(t, 0)
	waitReachability(t, env, aSec, c, "reachable")
}

// directoryEvents returns the directory_updated Event data in peer's log.
func directoryEvents(t *testing.T, env *testhub.Env, peer string) []map[string]json.RawMessage {
	t.Helper()
	evs, err := env.Hub.Log.Read(context.Background(), []string{peer}, eventlog.Cursor{Epoch: env.Hub.Log.Epoch()}, 0)
	if err != nil {
		t.Fatal(err)
	}
	var out []map[string]json.RawMessage
	for _, ev := range evs {
		if ev.Type != "directory_updated" {
			continue
		}
		raw, _ := json.Marshal(ev)
		if err := envelope.Validate("event", raw); err != nil {
			t.Fatalf("directory_updated Event is not schema-valid: %v; %s", err, raw)
		}
		var d map[string]json.RawMessage
		if err := json.Unmarshal(ev.Data, &d); err != nil {
			t.Fatal(err)
		}
		out = append(out, d)
	}
	return out
}

func TestDirectoryUpdatedOnEnrol(t *testing.T) {
	env := testhub.Start(t)
	a, aSec := env.NewPeer("grokbot", "a")
	b, _ := env.NewPeer("dot", "b")
	beforeA := len(directoryEvents(t, env, a))
	beforeB := len(directoryEvents(t, env, b))
	beforeOp := len(directoryEvents(t, env, env.OperatorPeer))

	// A long-poll from the current head wakes on the enrolment.
	cur, err := env.Hub.Log.Current(context.Background(), []string{a})
	if err != nil {
		t.Fatal(err)
	}
	type result struct {
		pr  pollResp
		dur time.Duration
	}
	done := make(chan result, 1)
	go func() {
		resp, body, dur := getEvents(t, env, aSec, "?after="+cur.String()+"&wait=20", nil)
		var pr pollResp
		if resp != nil && resp.StatusCode == http.StatusOK {
			_ = json.Unmarshal(body, &pr)
		}
		done <- result{pr, dur}
	}()
	time.Sleep(100 * time.Millisecond)

	n, _ := env.NewPeer("session", "newbie")

	for name, p := range map[string]struct {
		peer   string
		before int
	}{"a": {a, beforeA}, "b": {b, beforeB}, "operator": {env.OperatorPeer, beforeOp}} {
		evs := directoryEvents(t, env, p.peer)
		if len(evs) != p.before+1 {
			t.Fatalf("%s gained %d directory_updated Events, want 1", name, len(evs)-p.before)
		}
		d := evs[len(evs)-1]
		var keys []string
		for k := range d {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		if !slices.Equal(keys, []string{"change", "fields", "peer_id"}) {
			t.Fatalf("%s directory_updated data keys = %v", name, keys)
		}
		if str(t, d["peer_id"]) != n || str(t, d["change"]) != "enrolled" {
			t.Fatalf("%s directory_updated = peer %s change %s, want %s enrolled", name, d["peer_id"], d["change"], n)
		}
		var fields []string
		if err := json.Unmarshal(d["fields"], &fields); err != nil {
			t.Fatalf("%s fields = %s, want an array", name, d["fields"])
		}
	}

	select {
	case r := <-done:
		found := false
		for _, ev := range r.pr.Events {
			if ev.Type == "directory_updated" {
				found = true
			}
		}
		if !found || r.dur > 10*time.Second {
			t.Fatalf("long-poll after enrolment returned %+v in %v, want a directory_updated Event at once", r.pr, r.dur)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("long-poll did not wake on enrolment")
	}
}
