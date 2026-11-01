package httpapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/hub"
	"github.com/XertroV/vecnet/internal/testhub"
)

type groupInfo struct {
	GroupID   string   `json:"group_id"`
	Members   []string `json:"members"`
	CreatedBy string   `json:"created_by"`
	Archived  bool     `json:"archived"`
}

func createGroup(t *testing.T, env *testhub.Env, secret string, members ...string) string {
	t.Helper()
	resp, raw := env.Do(secret, "POST", "/v1/groups", map[string]any{"members": members})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /v1/groups = %d; %s", resp.StatusCode, raw)
	}
	var keys map[string]string
	if err := json.Unmarshal(raw, &keys); err != nil || len(keys) != 1 || keys["group_id"] == "" {
		t.Fatalf("POST /v1/groups body %s, want exactly {group_id}", raw)
	}
	return keys["group_id"]
}

func listGroups(t *testing.T, env *testhub.Env, secret string) map[string]groupInfo {
	t.Helper()
	resp, raw := env.Do(secret, "GET", "/v1/groups", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /v1/groups = %d; %s", resp.StatusCode, raw)
	}
	var body struct {
		Groups []groupInfo `json:"groups"`
	}
	if err := json.Unmarshal(raw, &body); err != nil || body.Groups == nil {
		t.Fatalf("GET /v1/groups body %s, want {groups: [...]}", raw)
	}
	out := map[string]groupInfo{}
	for _, g := range body.Groups {
		out[g.GroupID] = g
	}
	return out
}

func sorted(ss ...string) []string {
	out := append([]string(nil), ss...)
	sort.Strings(out)
	return out
}

func TestGroupsLifecycle(t *testing.T) {
	env := testhub.Start(t)
	a, aSec := env.NewPeer("session", "A")
	b, bSec := env.NewPeer("session", "B")
	c, _ := env.NewPeer("session", "C")
	d, dSec := env.NewPeer("session", "D")

	g := createGroup(t, env, aSec, b)
	resp, raw := env.Do(bSec, "POST", "/v1/groups/"+g+"/members", map[string]any{"members": []string{c}})
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("B adds C = %d; %s", resp.StatusCode, raw)
	}
	resp, raw = env.Do(dSec, "POST", "/v1/groups/"+g+"/members", map[string]any{"members": []string{d}})
	mustErr(t, resp, raw, http.StatusForbidden, "not_member")
	resp, raw = env.Do(bSec, "POST", "/v1/groups/"+g+"/members", map[string]any{"members": []string{c}, "extra": 1})
	mustErr(t, resp, raw, http.StatusBadRequest, "invalid_body")

	// Members list it; a non-member does not; the operator sees every group.
	want := groupInfo{GroupID: g, Members: sorted(a, b, c), CreatedBy: a}
	for _, s := range []string{aSec, bSec, env.OperatorSecret} {
		got, ok := listGroups(t, env, s)[g]
		if !ok || got.CreatedBy != want.CreatedBy || got.Archived || !sameIDs(got.Members, want.Members) {
			t.Fatalf("GET /v1/groups[%s] = %+v, want %+v", g, got, want)
		}
	}
	if _, ok := listGroups(t, env, dSec)[g]; ok {
		t.Fatalf("D (non-member) lists group %s", g)
	}

	// A sends to the group: B and C get it, A (a member, but the sender) not.
	sr := postSend(t, env, aSec, sendBody("g1", g), http.StatusAccepted)
	var got []string
	for _, dl := range sr.Deliveries {
		got = append(got, dl.Recipient)
		if dl.Via == nil || *dl.Via != g {
			t.Fatalf("delivery %+v, want via %s", dl, g)
		}
	}
	if !sameIDs(sorted(got...), sorted(b, c)) {
		t.Fatalf("deliveries to %v, want %v", got, sorted(b, c))
	}

	// 31 idle days: the ticker archives it, and a send naming it is 410.
	env.Now.Advance(31 * 24 * time.Hour)
	n, err := env.Hub.ArchiveIdleGroups(context.Background(), env.Now.Now())
	if err != nil || n != 1 {
		t.Fatalf("ArchiveIdleGroups = %d, %v; want 1", n, err)
	}
	resp, raw = env.Do(aSec, "POST", "/v1/messages", sendBody("g2", g))
	mustErr(t, resp, raw, http.StatusGone, "group_archived")
	resp, raw = env.Do(bSec, "POST", "/v1/groups/"+g+"/members", map[string]any{"members": []string{d}})
	mustErr(t, resp, raw, http.StatusGone, "group_archived")
	if got := listGroups(t, env, aSec)[g]; !got.Archived {
		t.Fatalf("group after archive = %+v, want archived", got)
	}
	if n, err := env.Hub.ArchiveIdleGroups(context.Background(), env.Now.Now()); err != nil || n != 0 {
		t.Fatalf("second ArchiveIdleGroups = %d, %v; want 0", n, err)
	}
}

func TestGroupScopeDenied(t *testing.T) {
	env := testhub.Start(t)
	a, aSec := env.NewPeer("session", "A")
	b, _ := env.NewPeer("session", "B")
	c, _ := env.NewPeer("session", "C")
	if _, err := env.Hub.DB.Exec(`INSERT INTO scopes(peer_id, mode, target_id) VALUES (?, 'deny', ?)`, a, c); err != nil {
		t.Fatal(err)
	}
	resp, raw := env.Do(aSec, "POST", "/v1/groups", map[string]any{"members": []string{b, c}})
	mustErr(t, resp, raw, http.StatusForbidden, "scope_denied")
	if n := rowCount(t, env, `SELECT count(*) FROM groups`); n != 0 {
		t.Fatalf("a refused create left %d groups", n)
	}
	// Adding members runs under the same scopes.
	g := createGroup(t, env, aSec, b)
	resp, raw = env.Do(aSec, "POST", "/v1/groups/"+g+"/members", map[string]any{"members": []string{c}})
	mustErr(t, resp, raw, http.StatusForbidden, "scope_denied")
	if n := rowCount(t, env, `SELECT count(*) FROM group_members WHERE grp_id = ?`, g); n != 2 {
		t.Fatalf("group has %d members, want 2 (A, B)", n)
	}
}

func TestTeamsOperatorOnly(t *testing.T) {
	env := testhub.Start(t)
	ctx := context.Background()
	_, aSec := env.NewPeer("session", "A")
	b, _ := env.NewPeer("session", "B")
	c, _ := env.NewPeer("session", "C")
	peer, err := env.Hub.Auth.Resolve(ctx, aSec)
	if err != nil {
		t.Fatal(err)
	}
	op, err := env.Hub.Auth.Resolve(ctx, env.OperatorSecret)
	if err != nil {
		t.Fatal(err)
	}

	var apiErr *hub.APIError
	if _, err := env.Hub.CreateTeam(ctx, peer, "ops", []string{b}); !errors.As(err, &apiErr) || apiErr.Code != "forbidden" {
		t.Fatalf("peer CreateTeam = %v, want 403 forbidden", err)
	}
	team, err := env.Hub.CreateTeam(ctx, op, "ops", []string{b})
	if err != nil {
		t.Fatal(err)
	}
	if err := env.Hub.AddTeamMembers(ctx, peer, team, []string{c}); !errors.As(err, &apiErr) || apiErr.Code != "forbidden" {
		t.Fatalf("peer AddTeamMembers = %v, want 403 forbidden", err)
	}
	if err := env.Hub.AddTeamMembers(ctx, op, team, []string{c, b}); err != nil {
		t.Fatal(err)
	}
	if _, err := env.Hub.ListTeams(ctx, peer); !errors.As(err, &apiErr) || apiErr.Code != "forbidden" {
		t.Fatalf("peer ListTeams = %v, want 403 forbidden", err)
	}
	teams, err := env.Hub.ListTeams(ctx, op)
	if err != nil || len(teams) != 1 || teams[0].TeamID != team || teams[0].Name != "ops" ||
		!sameIDs(teams[0].Members, sorted(b, c)) {
		t.Fatalf("ListTeams = %+v, %v; want one team ops with B and C", teams, err)
	}

	// A team is addressable: a send fans out to its members.
	sr := postSend(t, env, aSec, sendBody("team1", team), http.StatusAccepted)
	var got []string
	for _, dl := range sr.Deliveries {
		got = append(got, dl.Recipient)
	}
	if !sameIDs(sorted(got...), sorted(b, c)) {
		t.Fatalf("team send to %v, want %v", got, sorted(b, c))
	}
}

// TestPrincipalGroupsWithAs: a principal creates and extends ad hoc groups
// for one of its peers via ?as= (PROTOCOL §2.1 "peer / principal", §13.6).
func TestPrincipalGroupsWithAs(t *testing.T) {
	env := testhub.Start(t)
	ctx := context.Background()
	b, _ := env.NewPeer("session", "b")
	c, _ := env.NewPeer("session", "c")
	other, _ := env.NewPeer("session", "other")
	prn, pSec := env.NewPrincipal("p")
	p1, _, err := env.Hub.AddPeer(ctx, "pagchat-bot", "p1", prn)
	if err != nil {
		t.Fatal(err)
	}

	resp, raw := env.Do(pSec, "POST", "/v1/groups", map[string]any{"members": []string{b}})
	mustErr(t, resp, raw, http.StatusUnprocessableEntity, "as_required")
	resp, raw = env.Do(pSec, "POST", "/v1/groups?as="+other, map[string]any{"members": []string{b}})
	mustErr(t, resp, raw, http.StatusForbidden, "forbidden")

	resp, raw = env.Do(pSec, "POST", "/v1/groups?as="+p1, map[string]any{"members": []string{b}})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("principal POST /v1/groups?as= = %d; %s", resp.StatusCode, raw)
	}
	var keys map[string]string
	if err := json.Unmarshal(raw, &keys); err != nil {
		t.Fatal(err)
	}
	grp := keys["group_id"]
	resp, raw = env.Do(pSec, "POST", "/v1/groups/"+grp+"/members?as="+p1, map[string]any{"members": []string{c}})
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("principal add members = %d; %s", resp.StatusCode, raw)
	}
	g := listGroups(t, env, pSec)[grp]
	if g.CreatedBy != p1 || !equalStrings(g.Members, sorted(p1, b, c)) {
		t.Fatalf("group = %+v, want created by %s with members %v", g, p1, sorted(p1, b, c))
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
