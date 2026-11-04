package httpapi_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/testhub"
)

type inviteResp struct {
	PeerID         string `json:"peer_id"`
	EnrolToken     string `json:"enrol_token"`
	EnrolExpiresAt string `json:"enrol_expires_at"`
	HubURL         string `json:"hub_url"`
	HubOwner       string `json:"hub_owner"`
}

func decodeInvite(t *testing.T, raw []byte) inviteResp {
	t.Helper()
	var r inviteResp
	if err := json.Unmarshal(raw, &r); err != nil || r.EnrolToken == "" {
		t.Fatalf("invite body %s: %v", raw, err)
	}
	return r
}

// An operator-chosen TTL sets enrol_expires_at, within [60 s, 7 d]; the
// answer carries the hub's public URL for the invite (PROTOCOL §2.12).
func TestAddPeerEnrolTTL(t *testing.T) {
	env := testhub.Start(t)
	raw := adminDo(t, env, "POST", "/v1/admin/peers", map[string]any{"kind": "session", "name": "a", "enrol_ttl_seconds": 3600}, http.StatusCreated)
	p := decodeInvite(t, raw)
	if want := envelope.FormatTime(env.Now.Now().Add(time.Hour)); p.EnrolExpiresAt != want {
		t.Fatalf("enrol_expires_at = %q, want %q", p.EnrolExpiresAt, want)
	}
	if p.HubOwner == "" {
		t.Fatalf("hub_owner is empty: %s", raw)
	}
	if p.HubURL != env.Hub.Cfg.PublicURL || p.HubURL == "" {
		t.Fatalf("hub_url = %q, want %q", p.HubURL, env.Hub.Cfg.PublicURL)
	}
	def := decodeInvite(t, adminDo(t, env, "POST", "/v1/admin/peers", map[string]any{"kind": "session", "name": "b"}, http.StatusCreated))
	if want := envelope.FormatTime(env.Now.Now().Add(24 * time.Hour)); def.EnrolExpiresAt != want {
		t.Fatalf("default enrol_expires_at = %q, want %q", def.EnrolExpiresAt, want)
	}
	adminDo(t, env, "POST", "/v1/admin/peers", map[string]any{"kind": "session", "name": "c", "enrol_ttl_seconds": 7 * 86400}, http.StatusCreated)
	for _, bad := range []any{0, 59, 7*86400 + 1, -5} {
		resp, raw := env.Do(env.OperatorSecret, "POST", "/v1/admin/peers", map[string]any{"kind": "session", "name": "x", "enrol_ttl_seconds": bad})
		mustErr(t, resp, raw, http.StatusUnprocessableEntity, "invalid_value")
	}
	resp, raw := env.Do(env.OperatorSecret, "POST", "/v1/admin/peers", map[string]any{"kind": "session", "name": "x", "enrol_ttl_seconds": "1h"})
	mustErr(t, resp, raw, http.StatusBadRequest, "invalid_body")

	env.Now.Advance(time.Hour)
	resp, raw = env.Do("", "POST", "/v1/enrol", map[string]string{"token": p.EnrolToken})
	mustErr(t, resp, raw, http.StatusGone, "enrol_token_expired")
	enrol(t, env, def.EnrolToken)
}

// Re-issue replaces an un-enrolled peer's token (the old one stops working);
// an enrolled peer is 409 already_enrolled and a dot 422.
func TestReissueEnrolToken(t *testing.T) {
	env := testhub.Start(t)
	p := adminAddPeer(t, env, map[string]any{"kind": "session", "name": "late"})
	env.Now.Advance(25 * time.Hour)
	path := "/v1/admin/peers/" + p.PeerID + "/enrol-token"
	re := decodeInvite(t, adminDo(t, env, "POST", path, map[string]any{"enrol_ttl_seconds": 600}, http.StatusCreated))
	if re.PeerID != p.PeerID || re.EnrolToken == p.EnrolToken || re.EnrolExpiresAt != envelope.FormatTime(env.Now.Now().Add(10*time.Minute)) {
		t.Fatalf("reissue = %+v", re)
	}
	second := decodeInvite(t, adminDo(t, env, "POST", path, nil, http.StatusCreated))
	resp, raw := env.Do("", "POST", "/v1/enrol", map[string]string{"token": re.EnrolToken})
	mustErr(t, resp, raw, http.StatusUnauthorized, "unauthenticated")
	if got := enrol(t, env, second.EnrolToken); got.PeerID != p.PeerID {
		t.Fatalf("enrol after reissue = %+v", got)
	}
	resp, raw = env.Do(env.OperatorSecret, "POST", path, nil)
	mustErr(t, resp, raw, http.StatusConflict, "already_enrolled")

	resp, raw = env.Do(env.OperatorSecret, "POST", "/v1/admin/peers/peer_nope/enrol-token", nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown peer reissue = %d %s", resp.StatusCode, raw)
	}
	dot := adminDo(t, env, "POST", "/v1/admin/peers", map[string]any{"kind": "dot", "name": "d"}, http.StatusCreated)
	var d inviteResp
	_ = json.Unmarshal(dot, &d)
	if d.EnrolExpiresAt != "" {
		t.Fatalf("dot enrol_expires_at = %q, want empty", d.EnrolExpiresAt)
	}
	resp, raw = env.Do(env.OperatorSecret, "POST", "/v1/admin/peers/"+d.PeerID+"/enrol-token", nil)
	mustErr(t, resp, raw, http.StatusUnprocessableEntity, "invalid_value")
	resp, raw = env.Do(env.OperatorSecret, "POST", path, map[string]any{"enrol_ttl_seconds": 1})
	mustErr(t, resp, raw, http.StatusUnprocessableEntity, "invalid_value")

	// The audit names the re-issue, never the token.
	audit := string(adminDo(t, env, "GET", "/v1/admin/audit", nil, http.StatusOK))
	if !strings.Contains(audit, `"enrol_reissue"`) || strings.Contains(audit, second.EnrolToken) {
		t.Fatalf("audit: %s", audit)
	}
}

// Rotating a peer that never enrolled spends its invite: a leaked token is
// what a rotation answers.
func TestRotateSpendsEnrolToken(t *testing.T) {
	env := testhub.Start(t)
	p := adminAddPeer(t, env, map[string]any{"kind": "session", "name": "pending"})
	adminDo(t, env, "POST", "/v1/admin/peers/"+p.PeerID+"/rotate", nil, http.StatusOK)
	resp, raw := env.Do("", "POST", "/v1/enrol", map[string]string{"token": p.EnrolToken})
	mustErr(t, resp, raw, http.StatusUnauthorized, "unauthenticated")
}

// An open invite: the agent picks its display name once, at enrol. Refusals
// (missing, malformed, reserved, a lookalike of another peer or of the
// operator) spend nothing; the chosen name reaches the directory, the
// directory_updated log and the enrol audit row (PROTOCOL §2.12).
func TestEnrolChoosesName(t *testing.T) {
	env := testhub.Start(t)
	alice := adminAddPeer(t, env, map[string]any{"kind": "session", "name": "alice"})
	_, watcherSecret := env.NewPeer("session", "watcher")
	var opName string
	if err := env.Hub.DB.QueryRow(`SELECT name FROM peers WHERE peer_id = ?`, env.OperatorPeer).Scan(&opName); err != nil {
		t.Fatal(err)
	}

	raw := adminDo(t, env, "POST", "/v1/admin/peers", map[string]any{"kind": "session", "name_open": true}, http.StatusCreated)
	var p struct {
		inviteResp
		Name     string `json:"name"`
		NameOpen bool   `json:"name_open"`
	}
	if err := json.Unmarshal(raw, &p); err != nil || !p.NameOpen || !strings.HasPrefix(p.Name, "invited-") || p.EnrolToken == "" {
		t.Fatalf("open create = %s", raw)
	}
	try := func(name string) (*http.Response, []byte) {
		body := map[string]string{"token": p.EnrolToken}
		if name != "" {
			body["name"] = name
		}
		return env.Do("", "POST", "/v1/enrol", body)
	}
	resp, raw := try("")
	mustErr(t, resp, raw, http.StatusUnprocessableEntity, "name_required")
	for _, bad := range []string{"x", "it's me", "аlice", "a;b", "$(id)", strings.Repeat("a", 33)} {
		resp, raw = try(bad)
		mustErr(t, resp, raw, http.StatusUnprocessableEntity, "invalid_value")
	}
	for _, reserved := range []string{"Operator", "ADMIN", "rnax", "v e c n e t", "System", opName} {
		resp, raw = try(reserved)
		mustErr(t, resp, raw, http.StatusUnprocessableEntity, "name_reserved")
	}
	for _, taken := range []string{"ALICE", "al1ce", "a.l.i.c.e", "watcher", "Watcher"} {
		resp, raw = try(taken)
		mustErr(t, resp, raw, http.StatusConflict, "name_taken")
	}
	resp, raw = try("Scout-7")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("enrol with a good name = %d %s", resp.StatusCode, raw)
	}
	var got struct{ PeerID, Secret, Name string }
	if err := json.Unmarshal(raw, &got); err != nil || got.Name != "Scout-7" {
		t.Fatalf("enrol answer %s", raw)
	}
	// Once only: the token is spent and the name is no longer open.
	resp, raw = try("Other")
	mustErr(t, resp, raw, http.StatusUnauthorized, "unauthenticated")
	dir := string(adminDo(t, env, "GET", "/v1/peers", nil, http.StatusOK))
	if !strings.Contains(dir, `"Scout-7"`) || strings.Contains(dir, p.Name) {
		t.Fatalf("directory after naming: %s", dir)
	}
	var open int
	if err := env.Hub.DB.QueryRow(`SELECT name_open FROM peers WHERE peer_id = ?`, p.PeerID).Scan(&open); err != nil || open != 0 {
		t.Fatalf("name_open = %d, %v", open, err)
	}
	var detail string
	if err := env.Hub.DB.QueryRow(`SELECT detail FROM audit WHERE action = 'enrol' AND actor = ?`, p.PeerID).Scan(&detail); err != nil || !strings.Contains(detail, "Scout-7") {
		t.Fatalf("enrol audit detail = %q, %v", detail, err)
	}
	_, evRaw, _ := getEvents(t, env, watcherSecret, "?wait=0", nil)
	ev := string(evRaw)
	if !strings.Contains(ev, `"change":"updated"`) || !strings.Contains(ev, `"fields":["name"]`) || !strings.Contains(ev, p.PeerID) {
		t.Fatalf("watcher's log lacks the rename: %s", ev)
	}

	// A fixed invite keeps its name: another is refused, the same is fine.
	fixed := adminAddPeer(t, env, map[string]any{"kind": "session", "name": "bob"})
	resp, raw = env.Do("", "POST", "/v1/enrol", map[string]string{"token": fixed.EnrolToken, "name": "robert"})
	mustErr(t, resp, raw, http.StatusUnprocessableEntity, "name_not_open")
	resp, raw = env.Do("", "POST", "/v1/enrol", map[string]string{"token": fixed.EnrolToken, "name": "bob"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("fixed enrol with its own name = %d %s", resp.StatusCode, raw)
	}
	_ = alice

	// A suggestion has the agent-name shape; dots and operators never get
	// an open name.
	resp, raw = env.Do(env.OperatorSecret, "POST", "/v1/admin/peers", map[string]any{"kind": "session", "name": "it's", "name_open": true})
	mustErr(t, resp, raw, http.StatusUnprocessableEntity, "invalid_value")
	resp, raw = env.Do(env.OperatorSecret, "POST", "/v1/admin/peers", map[string]any{"kind": "dot", "name_open": true})
	mustErr(t, resp, raw, http.StatusUnprocessableEntity, "invalid_value")
	resp, raw = env.Do(env.OperatorSecret, "POST", "/v1/admin/peers", map[string]any{"kind": "session"})
	mustErr(t, resp, raw, http.StatusBadRequest, "invalid_body")
	sug := adminDo(t, env, "POST", "/v1/admin/peers", map[string]any{"kind": "session", "name": "helper", "name_open": true}, http.StatusCreated)
	if !strings.Contains(string(sug), `"name":"helper"`) || !strings.Contains(string(sug), `"name_open":true`) {
		t.Fatalf("suggested create = %s", sug)
	}
}
