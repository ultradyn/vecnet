package hub_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/auth"
	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/hub"
	"github.com/XertroV/vecnet/internal/testhub"
)

func provisionCaller(t *testing.T, env *testhub.Env, secret string) auth.Caller {
	t.Helper()
	c, err := env.Hub.Auth.Resolve(context.Background(), secret)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func provisionSetup(t *testing.T, max, rate int) (*testhub.Env, auth.Caller, string) {
	t.Helper()
	env := testhub.Start(t)
	prn, secret := env.NewPrincipal("host")
	c := provisionCaller(t, env, secret)
	if err := env.Hub.SetPrincipalLimits(context.Background(), prn, hub.PrincipalLimits{MaxPeers: max, CreatesPerHour: rate, Kinds: []string{"pagchat-bot"}}); err != nil {
		t.Fatal(err)
	}
	return env, c, secret
}

func createProvisioned(t *testing.T, env *testhub.Env, c auth.Caller, key string) *hub.CreatePeerResult {
	t.Helper()
	r, err := env.Hub.CreatePeer(context.Background(), c, hub.CreatePeerRequest{Kind: "pagchat-bot", Name: "bot " + key, IdempotencyKey: key})
	if err != nil {
		t.Fatal(err)
	}
	if r == nil || r.Replayed {
		t.Fatalf("fresh create = %+v", r)
	}
	return r
}

func provisionError(t *testing.T, err error, status int, code string) *hub.APIError {
	t.Helper()
	var e *hub.APIError
	if !errors.As(err, &e) || e.Status != status || e.Code != code {
		t.Fatalf("error = %v, want %d %s", err, status, code)
	}
	return e
}

func provisionAudit(t *testing.T, env *testhub.Env, action, actor, reason string, want int) {
	t.Helper()
	rows, err := env.Hub.DB.Query(`SELECT actor, detail FROM audit WHERE action = ? ORDER BY id`, action)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		n++
		var gotActor, raw string
		if err := rows.Scan(&gotActor, &raw); err != nil {
			t.Fatal(err)
		}
		var detail map[string]any
		if err := json.Unmarshal([]byte(raw), &detail); err != nil {
			t.Fatal(err)
		}
		if actor != "" && gotActor != actor {
			t.Fatalf("actor = %q, want %q", gotActor, actor)
		}
		if reason != "" && detail["reason"] != reason {
			t.Fatalf("detail = %s, want reason %s", raw, reason)
		}
		if _, ok := detail["name"]; ok {
			t.Fatalf("name leaked into audit: %s", raw)
		}
		if _, ok := detail["body"]; ok {
			t.Fatalf("body leaked into audit: %s", raw)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if n != want {
		t.Fatalf("%s audit rows = %d, want %d", action, n, want)
	}
}

func provisionDirectoryEvent(t *testing.T, env *testhub.Env, owner, peer, change string, want int) {
	t.Helper()
	n := count(t, env, `SELECT count(*) FROM events WHERE owner = ? AND type = 'directory_updated' AND json_extract(data, '$.peer_id') = ? AND json_extract(data, '$.change') = ?`, owner, peer, change)
	if n != want {
		t.Fatalf("%s directory %s for %s = %d, want %d", owner, change, peer, n, want)
	}
}

func TestCreatePeerDefaultOff(t *testing.T) {
	env := testhub.Start(t)
	_, sec := env.NewPrincipal("host")
	c := provisionCaller(t, env, sec)
	_, err := env.Hub.CreatePeer(context.Background(), c, hub.CreatePeerRequest{Kind: "pagchat-bot", Name: "bot", IdempotencyKey: "off"})
	e := provisionError(t, err, 403, "kind_not_allowed")
	if e.Details["kind"] != "pagchat-bot" {
		t.Fatalf("details = %v", e.Details)
	}
	provisionAudit(t, env, "peer_create_refused", c.PrincipalID, "kind_not_allowed", 1)
}

func TestCreatePeerHappyPath(t *testing.T) {
	env, c, _ := provisionSetup(t, 2, 60)
	observer, _ := env.NewPeer("session", "observer")
	r := createProvisioned(t, env, c, "happy")
	raw, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]any
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatal(err)
	}
	expected := map[string]any{"peer": map[string]any{"peer_id": r.Peer.PeerID, "kind": "pagchat-bot", "name": "bot happy", "created_by": c.PrincipalID, "disabled": false}, "idempotency_key": "happy"}
	if !reflect.DeepEqual(wire, expected) {
		t.Fatalf("create wire = %s, want %v", raw, expected)
	}
	if !strings.HasPrefix(r.Peer.PeerID, "peer_") {
		t.Fatalf("peer id = %s", r.Peer.PeerID)
	}
	var prn, creator string
	if err := env.Hub.DB.QueryRow(`SELECT prn_id, created_by FROM peers WHERE peer_id = ?`, r.Peer.PeerID).Scan(&prn, &creator); err != nil {
		t.Fatal(err)
	}
	if prn != c.PrincipalID || creator != prn {
		t.Fatalf("ownership = %s / %s", prn, creator)
	}
	for _, owner := range []string{env.OperatorPeer, observer, r.Peer.PeerID} {
		provisionDirectoryEvent(t, env, owner, r.Peer.PeerID, "enrolled", 1)
	}
	provisionAudit(t, env, "peer_create", c.PrincipalID, "", 1)
	if n := count(t, env, `SELECT count(*) FROM audit WHERE action = 'peer_create' AND json_extract(target_ids,'$[0]') = ? AND json_extract(target_ids,'$[1]') = ? AND json_extract(detail,'$.kind') = 'pagchat-bot'`, r.Peer.PeerID, c.PrincipalID); n != 1 {
		t.Fatalf("create audit targets/kind = %d", n)
	}
	for _, owner := range []string{env.OperatorPeer, observer, r.Peer.PeerID} {
		if n := count(t, env, `SELECT count(*) FROM events WHERE owner = ? AND type = 'directory_updated' AND json_extract(data,'$.peer_id') = ? AND json_extract(data,'$.fields') = '["name","kind","enrolled_at"]'`, owner, r.Peer.PeerID); n != 1 {
			t.Fatalf("enrolled fields = %d", n)
		}
	}
	entry := directoryEntry(t, env, r.Peer.PeerID)
	if entry.CreatedBy == nil || *entry.CreatedBy != c.PrincipalID {
		t.Fatalf("created_by = %v", entry.CreatedBy)
	}
	raw, err = json.Marshal(directoryEntry(t, env, observer))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "created_by") {
		t.Fatalf("operator-enrolled entry leaks created_by: %s", raw)
	}
}

func TestCreatePeerFirstPeerStream(t *testing.T) {
	env, c, sec := provisionSetup(t, 1, 60)
	r := createProvisioned(t, env, c, "first")
	for _, path := range []string{"/v1/events?wait=0", "/v1/events?peer=" + r.Peer.PeerID + "&wait=0"} {
		resp, raw := env.Do(sec, "GET", path, nil)
		if resp.StatusCode != 200 {
			t.Fatalf("principal stream %s = %d %s", path, resp.StatusCode, raw)
		}
		var poll struct {
			Events []envelope.Event `json:"events"`
		}
		if err := json.Unmarshal(raw, &poll); err != nil {
			t.Fatal(err)
		}
		matched := 0
		for _, event := range poll.Events {
			if event.Type != "directory_updated" {
				continue
			}
			var data struct {
				PeerID string   `json:"peer_id"`
				Change string   `json:"change"`
				Fields []string `json:"fields"`
			}
			if err := json.Unmarshal(event.Data, &data); err != nil {
				t.Fatal(err)
			}
			if data.PeerID == r.Peer.PeerID && data.Change == "enrolled" {
				matched++
				if !reflect.DeepEqual(data.Fields, []string{"name", "kind", "enrolled_at"}) {
					t.Fatalf("enrolled fields = %v", data.Fields)
				}
			}
		}
		if matched != 1 {
			t.Fatalf("principal stream %s has %d first-peer enrollment events, want 1: %s", path, matched, raw)
		}
	}
}

func TestCreatePeerIdempotentReplay(t *testing.T) {
	env, c, _ := provisionSetup(t, 2, 60)
	first := createProvisioned(t, env, c, "same")
	before := count(t, env, `SELECT count(*) FROM events`)
	r, err := env.Hub.CreatePeer(context.Background(), c, hub.CreatePeerRequest{Kind: "pagchat-bot", Name: "bot same", IdempotencyKey: "same"})
	if err != nil || r == nil || !r.Replayed || r.Peer != first.Peer {
		t.Fatalf("replay = %+v, %v", r, err)
	}
	if after := count(t, env, `SELECT count(*) FROM events`); after != before {
		t.Fatalf("replay appended %d events, want 0", after-before)
	}
	provisionDirectoryEvent(t, env, first.Peer.PeerID, first.Peer.PeerID, "enrolled", 1)
	if n := count(t, env, `SELECT count(*) FROM peers WHERE created_by = ?`, c.PrincipalID); n != 1 {
		t.Fatalf("created %d peers", n)
	}
	provisionAudit(t, env, "peer_create", c.PrincipalID, "", 1)
	provisionAudit(t, env, "peer_create_refused", c.PrincipalID, "", 0)
	_, err = env.Hub.CreatePeer(context.Background(), c, hub.CreatePeerRequest{Kind: "pagchat-bot", Name: "changed", IdempotencyKey: "same"})
	e := provisionError(t, err, 409, "idempotency_conflict")
	if e.Details["idempotency_key"] != "same" {
		t.Fatalf("details = %v", e.Details)
	}
	provisionAudit(t, env, "peer_create_refused", c.PrincipalID, "idempotency_conflict", 1)
}

func TestReplayBeatsLimits(t *testing.T) {
	env, c, _ := provisionSetup(t, 2, 2)
	first := createProvisioned(t, env, c, "first")
	createProvisioned(t, env, c, "second")
	// Replay must even survive operator revocation of the allowed kinds.
	if err := env.Hub.SetPrincipalLimits(context.Background(), c.PrincipalID, hub.PrincipalLimits{MaxPeers: 0, CreatesPerHour: 1, Kinds: []string{}}); err != nil {
		t.Fatal(err)
	}
	r, err := env.Hub.CreatePeer(context.Background(), c, hub.CreatePeerRequest{Kind: "pagchat-bot", Name: "bot first", IdempotencyKey: "first"})
	if err != nil || r == nil || !r.Replayed || r.Peer != first.Peer {
		t.Fatalf("replay at cap = %+v, %v", r, err)
	}
	provisionAudit(t, env, "peer_create_refused", c.PrincipalID, "", 0)
}

func TestCreatePeerKeyExpires(t *testing.T) {
	env, c, _ := provisionSetup(t, 3, 60)
	first := createProvisioned(t, env, c, "expires")
	env.Now.Advance(24*time.Hour + time.Minute)
	second := createProvisioned(t, env, c, "expires")
	if second.Peer.PeerID == first.Peer.PeerID {
		t.Fatal("expired key reused old peer")
	}
	if n := count(t, env, `SELECT count(*) FROM peer_create_keys WHERE prn_id = ?`, c.PrincipalID); n != 1 {
		t.Fatalf("key rows = %d", n)
	}
}

func TestCreatePeerNeverProvisionable(t *testing.T) {
	for _, kind := range []string{"operator", "source", "session", "dot"} {
		t.Run(kind, func(t *testing.T) {
			env, c, _ := provisionSetup(t, 2, 60)
			err := env.Hub.SetPrincipalLimits(context.Background(), c.PrincipalID, hub.PrincipalLimits{MaxPeers: 2, CreatesPerHour: 60, Kinds: []string{kind}})
			if e := provisionError(t, err, 422, "invalid_value"); e.Details["field"] != "kinds" {
				t.Fatalf("details = %v", e.Details)
			}
			// Defense in depth: even a corrupt/operator-written limits row cannot grant this kind.
			if _, err := env.Hub.DB.Exec(`UPDATE principal_limits SET kinds = ? WHERE prn_id = ?`, `["`+kind+`"]`, c.PrincipalID); err != nil {
				t.Fatal(err)
			}
			_, err = env.Hub.CreatePeer(context.Background(), c, hub.CreatePeerRequest{Kind: kind, Name: "bot", IdempotencyKey: "never"})
			provisionError(t, err, 403, "kind_not_allowed")
			provisionAudit(t, env, "peer_create_refused", c.PrincipalID, "kind_not_allowed", 1)
		})
	}
}

func TestCreatePeerCapCountsActiveOnly(t *testing.T) {
	env, c, _ := provisionSetup(t, 2, 60)
	a := createProvisioned(t, env, c, "a")
	createProvisioned(t, env, c, "b")
	check := func(disabled int) {
		t.Helper()
		_, err := env.Hub.CreatePeer(context.Background(), c, hub.CreatePeerRequest{Kind: "pagchat-bot", Name: "third", IdempotencyKey: "third"})
		e := provisionError(t, err, 429, "peer_cap_reached")
		if !reflect.DeepEqual(e.Details, map[string]any{"limit": 2, "active": 2, "disabled": disabled}) {
			t.Fatalf("cap details = %v", e.Details)
		}
	}
	check(0)
	if _, err := env.Hub.SetOwnPeerDisabled(context.Background(), c, a.Peer.PeerID, true); err != nil {
		t.Fatal(err)
	}
	createProvisioned(t, env, c, "c")
	check(1)
	provisionAudit(t, env, "peer_create_refused", c.PrincipalID, "peer_cap_reached", 2)
}

func TestReenableCountsTowardCap(t *testing.T) {
	env, c, _ := provisionSetup(t, 1, 60)
	a := createProvisioned(t, env, c, "a")
	if _, err := env.Hub.SetOwnPeerDisabled(context.Background(), c, a.Peer.PeerID, true); err != nil {
		t.Fatal(err)
	}
	createProvisioned(t, env, c, "b")
	_, err := env.Hub.SetOwnPeerDisabled(context.Background(), c, a.Peer.PeerID, false)
	e := provisionError(t, err, 429, "peer_cap_reached")
	if !reflect.DeepEqual(e.Details, map[string]any{"limit": 1, "active": 1, "disabled": 1}) {
		t.Fatalf("cap details = %v", e.Details)
	}
	provisionAudit(t, env, "peer_patch_refused", c.PrincipalID, "peer_cap_reached", 1)
}

func TestCreatePeerRateLimited(t *testing.T) {
	env, c, _ := provisionSetup(t, 100, 3)
	for i := 0; i < 3; i++ {
		r := createProvisioned(t, env, c, fmt.Sprintf("rate-%d", i))
		if _, err := env.Hub.SetOwnPeerDisabled(context.Background(), c, r.Peer.PeerID, true); err != nil {
			t.Fatal(err)
		}
	}
	_, err := env.Hub.CreatePeer(context.Background(), c, hub.CreatePeerRequest{Kind: "pagchat-bot", Name: "four", IdempotencyKey: "four"})
	e := provisionError(t, err, 429, "peer_create_rate_limited")
	retry, ok := e.Details["retry_after_seconds"].(int)
	if !ok || retry <= 0 || retry > 3600 {
		t.Fatalf("retry = %v", e.Details)
	}
	provisionAudit(t, env, "peer_create_refused", c.PrincipalID, "peer_create_rate_limited", 1)
	env.Now.Advance(time.Hour)
	createProvisioned(t, env, c, "four")
}

func TestCreatePeerRefusalsAudited(t *testing.T) {
	cases := []struct {
		name        string
		mutate      func(*hub.CreatePeerRequest)
		peer        bool
		field, code string
		status      int
	}{
		{name: "peer", peer: true, code: "forbidden", status: 403},
		{name: "empty name", mutate: func(r *hub.CreatePeerRequest) { r.Name = "" }, field: "name", code: "invalid_value", status: 422},
		{name: "long name", mutate: func(r *hub.CreatePeerRequest) { r.Name = strings.Repeat("界", 129) }, field: "name", code: "invalid_value", status: 422},
		{name: "bad key", mutate: func(r *hub.CreatePeerRequest) { r.IdempotencyKey = "!bad" }, field: "idempotency_key", code: "invalid_value", status: 422},
		{name: "long key", mutate: func(r *hub.CreatePeerRequest) { r.IdempotencyKey = strings.Repeat("a", 129) }, field: "idempotency_key", code: "invalid_value", status: 422},
		{name: "unknown kind", mutate: func(r *hub.CreatePeerRequest) { r.Kind = "unknown" }, field: "kind", code: "invalid_value", status: 422},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env, c, _ := provisionSetup(t, 2, 60)
			actor := c.PrincipalID
			if tc.peer {
				_, sec := env.NewPeer("session", "peer")
				c = provisionCaller(t, env, sec)
				actor = c.PeerID
			}
			r := hub.CreatePeerRequest{Kind: "pagchat-bot", Name: "private name", IdempotencyKey: "valid"}
			if tc.mutate != nil {
				tc.mutate(&r)
			}
			_, err := env.Hub.CreatePeer(context.Background(), c, r)
			e := provisionError(t, err, tc.status, tc.code)
			if tc.field != "" && e.Details["field"] != tc.field {
				t.Fatalf("details = %v", e.Details)
			}
			provisionAudit(t, env, "peer_create_refused", actor, tc.code, 1)
			want := r.Kind
			if !hub.PeerKinds[r.Kind] {
				want = hub.AuditUnrecognizedKind
			}
			if n := count(t, env, `SELECT count(*) FROM audit WHERE action = 'peer_create_refused' AND json_extract(detail,'$.kind') = ?`, want); n != 1 {
				t.Fatalf("kind %q missing from refusal audit: %d", want, n)
			}
		})
	}
}

// A credential pasted as kind is refused and must not land in append-only
// audit (PROTOCOL §14 invariant 6), whoever sent it and at whatever step.
func TestCreatePeerRefusalNeverAuditsRawKind(t *testing.T) {
	secrets := []string{
		"vn_peer_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
		"vn_enrol_BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB",
		"whsec_CCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCC",
	}
	for _, from := range []string{"principal", "peer"} {
		for i, secret := range secrets {
			t.Run(fmt.Sprintf("%s/%d", from, i), func(t *testing.T) {
				env, c, _ := provisionSetup(t, 2, 60)
				if from == "peer" {
					_, sec := env.NewPeer("session", "peer")
					c = provisionCaller(t, env, sec)
				}
				reqs := []hub.CreatePeerRequest{
					{Kind: secret, Name: "x", IdempotencyKey: "audit-probe"},
					{Kind: secret, Name: "", IdempotencyKey: "audit-probe"},
					{Kind: secret, Name: "x", IdempotencyKey: "!bad"},
				}
				for _, r := range reqs {
					if _, err := env.Hub.CreatePeer(context.Background(), c, r); err == nil {
						t.Fatalf("create with kind %q succeeded", secret)
					}
				}
				if n := count(t, env, `SELECT count(*) FROM audit WHERE action = 'peer_create_refused'`); n != len(reqs) {
					t.Fatalf("refusals audited = %d, want %d", n, len(reqs))
				}
				if n := count(t, env, `SELECT count(*) FROM audit WHERE instr(detail, ?) > 0`, secret); n != 0 {
					t.Fatalf("%d audit rows carry the raw kind", n)
				}
			})
		}
	}
}

func TestCreatePeerOnlyPrincipals(t *testing.T) {
	env := testhub.Start(t)
	_, sec := env.NewPeer("session", "peer")
	for _, secret := range []string{sec, env.OperatorSecret} {
		c := provisionCaller(t, env, secret)
		_, err := env.Hub.CreatePeer(context.Background(), c, hub.CreatePeerRequest{Kind: "pagchat-bot", Name: "bot", IdempotencyKey: "only"})
		provisionError(t, err, 403, "forbidden")
		_, err = env.Hub.SetOwnPeerDisabled(context.Background(), c, env.OperatorPeer, true)
		provisionError(t, err, 403, "forbidden")
	}
	if n := count(t, env, `SELECT count(*) FROM audit WHERE action = 'peer_patch_refused' AND json_extract(detail,'$.reason') = 'forbidden'`); n != 2 {
		t.Fatalf("patch refusals = %d", n)
	}
}

func TestCreatedPeerUsableAsAtOnce(t *testing.T) {
	env, c, sec := provisionSetup(t, 2, 60)
	target, targetSec := env.NewPeer("session", "target")
	r := createProvisioned(t, env, c, "usable")
	body := req("out", target)
	body["as"] = r.Peer.PeerID
	mustSend(t, env, sec, body)
	mustSend(t, env, targetSec, req("in", r.Peer.PeerID))
	resp, raw := env.Do(sec, "GET", "/v1/events?wait=0", nil)
	if resp.StatusCode != 200 || !strings.Contains(string(raw), `"recipient":"`+r.Peer.PeerID+`"`) {
		t.Fatalf("principal stream = %d %s", resp.StatusCode, raw)
	}
}

func TestOwnPeerDisableScope(t *testing.T) {
	env, c, sec := provisionSetup(t, 3, 60)
	prn2, sec2 := env.NewPrincipal("other")
	if err := env.Hub.SetPrincipalLimits(context.Background(), prn2, hub.PrincipalLimits{MaxPeers: 2, CreatesPerHour: 60, Kinds: []string{"pagchat-bot"}}); err != nil {
		t.Fatal(err)
	}
	other := createProvisioned(t, env, provisionCaller(t, env, sec2), "other")
	enrolled, _ := env.NewPeer("session", "enrolled")
	if err := env.Hub.MovePeer(context.Background(), enrolled, c.PrincipalID); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{other.Peer.PeerID, enrolled, "peer_missing"} {
		_, err := env.Hub.SetOwnPeerDisabled(context.Background(), c, id, true)
		provisionError(t, err, 404, "not_found")
	}
	provisionAudit(t, env, "peer_patch_refused", c.PrincipalID, "not_found", 3)
	own := createProvisioned(t, env, c, "own")
	for i := range 2 {
		before := count(t, env, `SELECT count(*) FROM events`)
		r, err := env.Hub.SetOwnPeerDisabled(context.Background(), c, own.Peer.PeerID, true)
		if err != nil || r == nil || !r.Disabled || r.CreatedBy != c.PrincipalID {
			t.Fatalf("disable = %+v, %v", r, err)
		}
		if after := count(t, env, `SELECT count(*) FROM events`); i == 1 && after != before {
			t.Fatalf("disable no-op appended %d events, want 0", after-before)
		}
	}
	provisionDirectoryEvent(t, env, env.OperatorPeer, own.Peer.PeerID, "removed", 1)
	provisionAudit(t, env, "peer_disable", c.PrincipalID, "", 1)
	resp, raw := env.Do(sec, "GET", "/v1/events?peer="+own.Peer.PeerID+"&wait=0", nil)
	if resp.StatusCode != 204 {
		t.Fatalf("disabled stream = %d %s", resp.StatusCode, raw)
	}
	for i := range 2 {
		before := count(t, env, `SELECT count(*) FROM events`)
		r, err := env.Hub.SetOwnPeerDisabled(context.Background(), c, own.Peer.PeerID, false)
		if err != nil || r == nil || r.Disabled {
			t.Fatalf("reenable = %+v, %v", r, err)
		}
		if after := count(t, env, `SELECT count(*) FROM events`); i == 1 && after != before {
			t.Fatalf("enable no-op appended %d events, want 0", after-before)
		}
		provisionDirectoryEvent(t, env, own.Peer.PeerID, own.Peer.PeerID, "enrolled", 2)
	}
	provisionDirectoryEvent(t, env, env.OperatorPeer, own.Peer.PeerID, "enrolled", 2)
	provisionAudit(t, env, "peer_enable", c.PrincipalID, "", 1)
}

func TestCreatedPeerInheritsDefaultScopes(t *testing.T) {
	env, c, sec := provisionSetup(t, 3, 60)
	target, _ := env.NewPeer("session", "X")
	before := createProvisioned(t, env, c, "before")
	if err := env.Hub.SetPrincipalDefaultScopes(context.Background(), c.PrincipalID, nil, []string{target}); err != nil {
		t.Fatal(err)
	}
	after := createProvisioned(t, env, c, "after")
	body := req("before-send", target)
	body["as"] = before.Peer.PeerID
	mustSend(t, env, sec, body)
	body = req("after-send", target)
	body["as"] = after.Peer.PeerID
	status, _, raw := sendHTTP(t, env, sec, body)
	if status != 403 || !strings.Contains(string(raw), `"scope_denied"`) {
		t.Fatalf("scoped send = %d %s", status, raw)
	}
	if err := env.Hub.SetPrincipalDefaultScopes(context.Background(), c.PrincipalID, []string{target}, nil); err != nil {
		t.Fatal(err)
	}
	if n := count(t, env, `SELECT count(*) FROM principal_default_scopes WHERE prn_id = ? AND mode = 'deny'`, c.PrincipalID); n != 0 {
		t.Fatalf("old defaults = %d", n)
	}
	scope, err := env.Hub.Auth.LoadScopes(context.Background(), nil, after.Peer.PeerID)
	if err != nil || !scope.Denied(target) {
		t.Fatalf("existing scope changed: %+v %v", scope, err)
	}
	provisionAudit(t, env, "principal_default_scopes", "operator", "", 2)
}

func TestCreatedByImmutable(t *testing.T) {
	env, c, _ := provisionSetup(t, 2, 60)
	other, sec := env.NewPrincipal("other")
	r := createProvisioned(t, env, c, "move")
	if err := env.Hub.MovePeer(context.Background(), r.Peer.PeerID, other); err != nil {
		t.Fatal(err)
	}
	entry := directoryEntry(t, env, r.Peer.PeerID)
	if entry.CreatedBy == nil || *entry.CreatedBy != c.PrincipalID {
		t.Fatalf("created_by changed: %v", entry.CreatedBy)
	}
	for _, caller := range []auth.Caller{c, provisionCaller(t, env, sec)} {
		_, err := env.Hub.SetOwnPeerDisabled(context.Background(), caller, r.Peer.PeerID, true)
		provisionError(t, err, 404, "not_found")
	}
}

func TestPrincipalsCounts(t *testing.T) {
	env, c, _ := provisionSetup(t, 5, 60)
	target, _ := env.NewPeer("session", "target")
	a := createProvisioned(t, env, c, "active")
	b := createProvisioned(t, env, c, "disabled")
	moved := createProvisioned(t, env, c, "moved")
	if _, err := env.Hub.SetOwnPeerDisabled(context.Background(), c, b.Peer.PeerID, true); err != nil {
		t.Fatal(err)
	}
	other, _ := env.NewPrincipal("other")
	if err := env.Hub.MovePeer(context.Background(), moved.Peer.PeerID, other); err != nil {
		t.Fatal(err)
	}
	// ScopedPeers includes operator-enrolled peers, as well as provisioned ones.
	enrolled, _ := peerUnder(t, env, "session", "enrolled", c.PrincipalID)
	for _, peer := range []string{a.Peer.PeerID, enrolled} {
		if err := env.Hub.SetScopes(context.Background(), peer, nil, []string{target}); err != nil {
			t.Fatal(err)
		}
	}
	if err := env.Hub.SetPrincipalDefaultScopes(context.Background(), c.PrincipalID, []string{target}, []string{env.OperatorPeer}); err != nil {
		t.Fatal(err)
	}
	infos, err := env.Hub.Principals(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for i, p := range infos {
		if i > 0 && infos[i-1].PrincipalID >= p.PrincipalID {
			t.Fatal("principals not ordered")
		}
		if p.Limits.Kinds == nil || p.DefaultAllow == nil || p.DefaultDeny == nil {
			t.Fatalf("nil slices: %+v", p)
		}
		if p.PrincipalID == other && (p.Active != 0 || p.Disabled != 0 || p.CreatesLastHour != 0 || p.Limits.MaxPeers != 0 || p.Limits.CreatesPerHour != 60) {
			t.Fatalf("other info = %+v", p)
		}
		if p.PrincipalID == c.PrincipalID {
			found = true
			if p.Name != "host" || p.Active != 1 || p.Disabled != 1 || p.CreatesLastHour != 3 || p.ScopedPeers != 2 || p.Limits.MaxPeers != 5 || p.Limits.CreatesPerHour != 60 || !reflect.DeepEqual(p.Limits.Kinds, []string{"pagchat-bot"}) || !reflect.DeepEqual(p.DefaultAllow, []string{target}) || !reflect.DeepEqual(p.DefaultDeny, []string{env.OperatorPeer}) {
				t.Fatalf("principal info = %+v", p)
			}
		}
	}
	if !found {
		t.Fatal("principal missing")
	}
	if len(infos) != count(t, env, `SELECT count(*) FROM principals`) {
		t.Fatalf("missing principals: %d", len(infos))
	}
	env.Now.Advance(time.Hour)
	infos, err = env.Hub.Principals(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range infos {
		if p.PrincipalID == c.PrincipalID && p.CreatesLastHour != 0 {
			t.Fatalf("expired rate count = %d", p.CreatesLastHour)
		}
	}
}

func TestSetPeerDisabledOperatorUnchanged(t *testing.T) {
	env := testhub.Start(t)
	peer, sec := env.NewPeer("session", "peer")
	for _, disabled := range []bool{true, true, false, false} {
		if err := env.Hub.SetPeerDisabled(context.Background(), peer, disabled); err != nil {
			t.Fatal(err)
		}
	}
	provisionDirectoryEvent(t, env, env.OperatorPeer, peer, "removed", 1)
	provisionDirectoryEvent(t, env, env.OperatorPeer, peer, "enrolled", 2)
	provisionDirectoryEvent(t, env, peer, peer, "enrolled", 0)
	provisionAudit(t, env, "peer_disable", "operator", "", 1)
	provisionAudit(t, env, "peer_enable", "operator", "", 1)
	if _, err := env.Hub.Auth.Resolve(context.Background(), sec); err != nil {
		t.Fatal(err)
	}
	provisionError(t, env.Hub.SetPeerDisabled(context.Background(), env.OperatorPeer, true), 422, "invalid_value")
}

func TestPrincipalLimitsValidation(t *testing.T) {
	env, c, _ := provisionSetup(t, 2, 60)
	for _, tc := range []struct {
		limits hub.PrincipalLimits
		field  string
	}{
		{hub.PrincipalLimits{MaxPeers: -1, CreatesPerHour: 60}, "max_peers"},
		{hub.PrincipalLimits{MaxPeers: 2, CreatesPerHour: 0}, "creates_per_hour"},
		{hub.PrincipalLimits{MaxPeers: 2, CreatesPerHour: 60, Kinds: []string{"unknown"}}, "kinds"},
	} {
		err := env.Hub.SetPrincipalLimits(context.Background(), c.PrincipalID, tc.limits)
		e := provisionError(t, err, 422, "invalid_value")
		if e.Details["field"] != tc.field {
			t.Fatalf("details = %v", e.Details)
		}
	}
	provisionError(t, env.Hub.SetPrincipalLimits(context.Background(), "prn_missing", hub.PrincipalLimits{MaxPeers: 1, CreatesPerHour: 60}), 404, "not_found")
	infos, err := env.Hub.Principals(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range infos {
		if p.PrincipalID == c.PrincipalID && (p.Limits.MaxPeers != 2 || p.Limits.CreatesPerHour != 60 || !reflect.DeepEqual(p.Limits.Kinds, []string{"pagchat-bot"})) {
			t.Fatalf("validation changed limits: %+v", p.Limits)
		}
	}
	provisionAudit(t, env, "principal_limits", "operator", "", 1)
}

func TestPrincipalDefaultScopesValidation(t *testing.T) {
	env, c, _ := provisionSetup(t, 2, 60)
	target, _ := env.NewPeer("session", "target")
	if err := env.Hub.SetPrincipalDefaultScopes(context.Background(), c.PrincipalID, nil, []string{target}); err != nil {
		t.Fatal(err)
	}
	provisionError(t, env.Hub.SetPrincipalDefaultScopes(context.Background(), "prn_missing", nil, nil), 404, "not_found")
	for _, tc := range []struct {
		allow, deny []string
		field       string
	}{
		{[]string{target}, []string{target}, "deny"},
		{[]string{"peer_missing"}, nil, "allow"},
		{nil, []string{"peer_missing"}, "deny"},
	} {
		e := provisionError(t, env.Hub.SetPrincipalDefaultScopes(context.Background(), c.PrincipalID, tc.allow, tc.deny), 422, "invalid_value")
		if e.Details["field"] != tc.field {
			t.Fatalf("details = %v", e.Details)
		}
	}
	if n := count(t, env, `SELECT count(*) FROM principal_default_scopes WHERE prn_id = ? AND mode = 'deny' AND target_id = ?`, c.PrincipalID, target); n != 1 {
		t.Fatalf("failed replacement changed defaults: %d", n)
	}
	provisionAudit(t, env, "principal_default_scopes", "operator", "", 1)
}

func TestCreatePeerConcurrentCapAndReplay(t *testing.T) {
	for _, sameKey := range []bool{false, true} {
		t.Run(fmt.Sprintf("same_key_%t", sameKey), func(t *testing.T) {
			env, c, _ := provisionSetup(t, 1, 60)
			type outcome struct {
				result *hub.CreatePeerResult
				err    error
			}
			outcomes := make(chan outcome, 2)
			start := make(chan struct{})
			for i := 0; i < 2; i++ {
				go func(i int) {
					<-start
					key := "race"
					if !sameKey {
						key = fmt.Sprintf("race-%d", i)
					}
					r, err := env.Hub.CreatePeer(context.Background(), c, hub.CreatePeerRequest{Kind: "pagchat-bot", Name: "race", IdempotencyKey: key})
					outcomes <- outcome{r, err}
				}(i)
			}
			close(start)
			fresh, replays, refused := 0, 0, 0
			peer := ""
			for range 2 {
				out := <-outcomes
				if out.err != nil {
					provisionError(t, out.err, 429, "peer_cap_reached")
					refused++
					continue
				}
				if out.result == nil {
					t.Fatal("nil successful result")
				}
				if peer != "" && peer != out.result.Peer.PeerID {
					t.Fatal("replay created another identity")
				}
				peer = out.result.Peer.PeerID
				if out.result.Replayed {
					replays++
				} else {
					fresh++
				}
			}
			wantReplay, wantRefused := 0, 1
			if sameKey {
				wantReplay, wantRefused = 1, 0
			}
			if fresh != 1 || replays != wantReplay || refused != wantRefused {
				t.Fatalf("fresh/replays/refused = %d/%d/%d", fresh, replays, refused)
			}
			if n := count(t, env, `SELECT count(*) FROM peers WHERE created_by = ?`, c.PrincipalID); n != 1 {
				t.Fatalf("race created %d peers", n)
			}
			provisionAudit(t, env, "peer_create", c.PrincipalID, "", 1)
			provisionAudit(t, env, "peer_create_refused", c.PrincipalID, "peer_cap_reached", wantRefused)
		})
	}
}
