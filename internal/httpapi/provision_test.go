package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/XertroV/vecnet/internal/hub"
	"github.com/XertroV/vecnet/internal/opapi"
	"github.com/XertroV/vecnet/internal/testhub"
)

func provisionPrincipal(t *testing.T, env *testhub.Env, cap, rate int) (string, string) {
	t.Helper()
	prn, sec := env.NewPrincipal("creator")
	if err := env.Hub.SetPrincipalLimits(context.Background(), prn, hub.PrincipalLimits{MaxPeers: cap, CreatesPerHour: rate, Kinds: []string{"pagchat-bot"}}); err != nil {
		t.Fatal(err)
	}
	return prn, sec
}

func createHTTPPeer(t *testing.T, env *testhub.Env, sec, key string) (hub.ProvisionedPeer, []byte) {
	t.Helper()
	resp, raw := env.Do(sec, "POST", "/v1/peers", map[string]string{"kind": "pagchat-bot", "name": "bot", "idempotency_key": key})
	if resp.StatusCode != 201 {
		t.Fatalf("create = %d: %s", resp.StatusCode, raw)
	}
	if resp.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("create cache: %v", resp.Header)
	}
	var result hub.CreatePeerResult
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	return result.Peer, raw
}

func exactKeys(t *testing.T, raw []byte, want ...string) map[string]json.RawMessage {
	t.Helper()
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		t.Fatal(err)
	}
	keys := []string{}
	for k := range obj {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	slices.Sort(want)
	if !slices.Equal(keys, want) {
		t.Fatalf("keys = %v, want %v; %s", keys, want, raw)
	}
	return obj
}

func TestPostPeersShape(t *testing.T) {
	env := testhub.Start(t)
	prn, sec := provisionPrincipal(t, env, 1, 1)
	peer, raw := createHTTPPeer(t, env, sec, "shape")
	top := exactKeys(t, raw, "peer", "idempotency_key")
	exactKeys(t, top["peer"], "peer_id", "kind", "name", "created_by", "disabled")
	if !strings.HasPrefix(peer.PeerID, "peer_") || peer.Kind != "pagchat-bot" || peer.Name != "bot" || peer.CreatedBy != prn || peer.Disabled || string(top["idempotency_key"]) != `"shape"` {
		t.Fatalf("response: %s", raw)
	}
	resp, replay := env.Do(sec, "POST", "/v1/peers", map[string]string{"kind": "pagchat-bot", "name": "bot", "idempotency_key": "shape"})
	if resp.StatusCode != 200 || !bytes.Equal(raw, replay) || resp.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("replay = %d %s", resp.StatusCode, replay)
	}
	for _, body := range []string{`{"kind":"pagchat-bot","name":"bot","idempotency_key":"x","extra":true}`, `{"kind":"pagchat-bot","name":"bot"}`, `{"kind":"pagchat-bot","name":5,"idempotency_key":"x"}`, `{"kind":null,"name":"bot","idempotency_key":"x"}`} {
		resp, raw := env.Do(sec, "POST", "/v1/peers", json.RawMessage(body))
		mustErr(t, resp, raw, 400, "invalid_body")
	}
}

func TestPostPeersRefusals(t *testing.T) {
	env := testhub.Start(t)
	_, sec := provisionPrincipal(t, env, 1, 1)
	resp, raw := env.Do(sec, "POST", "/v1/peers", map[string]string{"kind": "session", "name": "bot", "idempotency_key": "forbidden"})
	e := mustErr(t, resp, raw, 403, "kind_not_allowed")
	if e.Error.Details["kind"] != "session" {
		t.Fatalf("kind detail: %+v", e)
	}
	peer, _ := createHTTPPeer(t, env, sec, "first")
	resp, raw = env.Do(sec, "POST", "/v1/peers", map[string]string{"kind": "pagchat-bot", "name": "bot", "idempotency_key": "second"})
	e = mustErr(t, resp, raw, 429, "peer_cap_reached")
	if !reflect.DeepEqual(e.Error.Details, map[string]any{"limit": float64(1), "active": float64(1), "disabled": float64(0)}) {
		t.Fatalf("cap details: %+v", e.Error.Details)
	}
	resp, raw = env.Do(sec, "PATCH", "/v1/peers/"+peer.PeerID, map[string]bool{"disabled": true})
	if resp.StatusCode != 200 {
		t.Fatalf("disable = %d %s", resp.StatusCode, raw)
	}
	resp, raw = env.Do(sec, "POST", "/v1/peers", map[string]string{"kind": "pagchat-bot", "name": "bot", "idempotency_key": "second"})
	e = mustErr(t, resp, raw, 429, "peer_create_rate_limited")
	retry, err := strconv.Atoi(resp.Header.Get("Retry-After"))
	if err != nil || retry != 3600 || e.Error.Details["retry_after_seconds"] != float64(retry) {
		t.Fatalf("retry header %v, details %+v", resp.Header, e.Error.Details)
	}
	resp, raw = env.Do(sec, "POST", "/v1/peers", map[string]string{"kind": "pagchat-bot", "name": "changed", "idempotency_key": "first"})
	e = mustErr(t, resp, raw, 409, "idempotency_conflict")
	if e.Error.Details["idempotency_key"] != "first" {
		t.Fatalf("conflict: %+v", e)
	}
	for _, secret := range []string{env.OperatorSecret, func() string { _, s := env.NewPeer("session", "peer"); return s }()} {
		resp, raw = env.Do(secret, "POST", "/v1/peers", map[string]string{"kind": "pagchat-bot", "name": "bot", "idempotency_key": "nonprincipal"})
		mustErr(t, resp, raw, 403, "forbidden")
	}
}

func TestPatchPeerOwnOnly(t *testing.T) {
	env := testhub.Start(t)
	prn, sec := provisionPrincipal(t, env, 1, 10)
	peer, _ := createHTTPPeer(t, env, sec, "own")
	resp, raw := env.Do(sec, "PATCH", "/v1/peers/"+peer.PeerID, map[string]bool{"disabled": true})
	if resp.StatusCode != 200 {
		t.Fatalf("patch = %d %s", resp.StatusCode, raw)
	}
	top := exactKeys(t, raw, "peer")
	exactKeys(t, top["peer"], "peer_id", "kind", "name", "created_by", "disabled")
	var got struct {
		Peer hub.ProvisionedPeer `json:"peer"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	peer.Disabled = true
	if got.Peer != peer || got.Peer.CreatedBy != prn {
		t.Fatalf("patched peer: %+v", got)
	}
	resp, noop := env.Do(sec, "PATCH", "/v1/peers/"+peer.PeerID, map[string]bool{"disabled": true})
	if resp.StatusCode != 200 || !bytes.Equal(raw, noop) {
		t.Fatalf("no-op: %d %s", resp.StatusCode, noop)
	}
	for _, body := range []string{`{"disabled":"yes"}`, `{"disabled":null}`, `{}`, `{"disabled":true,"extra":1}`} {
		resp, raw = env.Do(sec, "PATCH", "/v1/peers/"+peer.PeerID, json.RawMessage(body))
		mustErr(t, resp, raw, 400, "invalid_body")
	}
	foreign, _ := env.NewPeer("session", "foreign")
	enrolled, _, err := env.Hub.AddPeer(context.Background(), "pagchat-bot", "operator enrolled", prn)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{foreign, enrolled, "peer_unknown"} {
		resp, raw = env.Do(sec, "PATCH", "/v1/peers/"+id, map[string]bool{"disabled": true})
		mustErr(t, resp, raw, 404, "not_found")
	}
	createHTTPPeer(t, env, sec, "replacement")
	resp, raw = env.Do(sec, "PATCH", "/v1/peers/"+peer.PeerID, map[string]bool{"disabled": false})
	mustErr(t, resp, raw, 429, "peer_cap_reached")
}

func TestPatchPeersMeStillWorks(t *testing.T) {
	env := testhub.Start(t)
	_, sec := env.NewPeer("session", "self")
	resp, raw := env.Do(sec, "PATCH", "/v1/peers/me", map[string]string{"subscribe_presence": "all"})
	if resp.StatusCode != 204 {
		t.Fatalf("me = %d %s", resp.StatusCode, raw)
	}
}

func TestCreatedByInDirectory(t *testing.T) {
	env := testhub.Start(t)
	prn, sec := provisionPrincipal(t, env, 2, 10)
	peer, _ := createHTTPPeer(t, env, sec, "directory")
	enrolled, _ := env.NewPeer("session", "operator enrolled")
	resp, raw := env.Do(sec, "GET", "/v1/peers", nil)
	if resp.StatusCode != 200 {
		t.Fatalf("directory: %d %s", resp.StatusCode, raw)
	}
	var body struct {
		Peers []map[string]json.RawMessage `json:"peers"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, p := range body.Peers {
		var id string
		if err := json.Unmarshal(p["peer_id"], &id); err != nil {
			t.Fatal(err)
		}
		if id == peer.PeerID {
			seen[id] = true
			if string(p["created_by"]) != strconv.Quote(prn) {
				t.Fatalf("provenance: %s", raw)
			}
		}
		if id == enrolled {
			seen[id] = true
			if _, ok := p["created_by"]; ok {
				t.Fatalf("enrolled has provenance: %s", raw)
			}
		}
	}
	if len(seen) != 2 {
		t.Fatalf("directory missing peers: %s", raw)
	}
}

func TestOperatorPowersOnCreatedPeers(t *testing.T) {
	env := testhub.Start(t)
	_, sec := provisionPrincipal(t, env, 2, 10)
	peer, _ := createHTTPPeer(t, env, sec, "powers")
	raw := adminDo(t, env, "POST", "/v1/admin/peers/"+peer.PeerID+"/rotate", nil, 200)
	var cred struct {
		Secret string `json:"secret"`
	}
	if err := json.Unmarshal(raw, &cred); err != nil {
		t.Fatal(err)
	}
	if cred.Secret == "" {
		t.Fatal("rotate returned no secret")
	}
	resp, raw := env.Do(cred.Secret, "GET", "/v1/peers", nil)
	if resp.StatusCode != 200 {
		t.Fatalf("rotated credential: %d %s", resp.StatusCode, raw)
	}
	adminDo(t, env, "PUT", "/v1/admin/peers/"+peer.PeerID+"/scopes", map[string][]string{"allow": {env.OperatorPeer}}, 204)
	outsider, _ := env.NewPeer("session", "outside scope")
	resp, raw = env.Do(cred.Secret, "POST", "/v1/messages", map[string]any{"idempotency_key": "scope-check", "priority": "hold", "recipients": []string{outsider}, "content": map[string]string{"text": "scoped"}})
	mustErr(t, resp, raw, 403, "scope_denied")
	dest, destSec := env.NewPrincipal("new owner")
	adminDo(t, env, "POST", "/v1/admin/peers/"+peer.PeerID+"/move", map[string]string{"principal_id": dest}, 204)
	for _, secret := range []string{sec, destSec} {
		resp, raw = env.Do(secret, "PATCH", "/v1/peers/"+peer.PeerID, map[string]bool{"disabled": true})
		mustErr(t, resp, raw, 404, "not_found")
	}
	adminDo(t, env, "POST", "/v1/admin/peers/"+peer.PeerID+"/disable", nil, 204)
	raw = adminDo(t, env, "GET", "/v1/admin/peers", nil, 200)
	var listed struct {
		Peers []opapi.PeerRow `json:"peers"`
	}
	if err := json.Unmarshal(raw, &listed); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, row := range listed.Peers {
		if row.Peer.PeerID == peer.PeerID {
			found = true
			if row.PrincipalID != dest || !row.Disabled || row.Peer.CreatedBy == nil || *row.Peer.CreatedBy != peer.CreatedBy {
				t.Fatalf("moved/disabled peer: %+v", row)
			}
		}
	}
	if !found {
		t.Fatal("created peer missing from operator view")
	}

	resp, raw = env.Do(cred.Secret, "GET", "/v1/peers", nil)
	mustErr(t, resp, raw, 401, "unauthenticated")
}

func principalRow(t *testing.T, env *testhub.Env, prn string) map[string]json.RawMessage {
	t.Helper()
	raw := adminDo(t, env, "GET", "/v1/admin/principals", nil, 200)
	var body struct {
		Principals []map[string]json.RawMessage `json:"principals"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	for _, p := range body.Principals {
		if string(p["principal_id"]) == strconv.Quote(prn) {
			return p
		}
	}
	t.Fatalf("principal missing: %s", raw)
	return nil
}

func TestAdminPrincipalLimitsAndList(t *testing.T) {
	env := testhub.Start(t)
	prn, sec := env.NewPrincipal("limited")
	peer, _, err := env.Hub.AddPeer(context.Background(), "pagchat-bot", "scoped", prn)
	if err != nil {
		t.Fatal(err)
	}
	if err := env.Hub.SetScopes(context.Background(), peer, []string{env.OperatorPeer}, nil); err != nil {
		t.Fatal(err)
	}
	body := map[string]any{"max_peers": 3, "creates_per_hour": 12, "kinds": []string{"pagchat-bot", "launcher"}}
	adminDo(t, env, "PUT", "/v1/admin/principals/"+prn+"/limits", body, 204)
	row := principalRow(t, env, prn)
	for k, want := range map[string]string{"principal_id": strconv.Quote(prn), "name": `"limited"`, "max_peers": "3", "creates_per_hour": "12", "kinds": `["pagchat-bot","launcher"]`, "scoped_peers": "1", "active": "0", "disabled": "0", "creates_last_hour": "0", "default_allow": "[]", "default_deny": "[]"} {
		if string(row[k]) != want {
			t.Errorf("%s = %s, want %s", k, row[k], want)
		}
	}
	created, _ := createHTTPPeer(t, env, sec, "counts-disabled")
	createHTTPPeer(t, env, sec, "counts-active")
	resp, raw := env.Do(sec, "PATCH", "/v1/peers/"+created.PeerID, map[string]bool{"disabled": true})
	if resp.StatusCode != 200 {
		t.Fatalf("disable: %d %s", resp.StatusCode, raw)
	}
	row = principalRow(t, env, prn)
	for key, want := range map[string]string{"active": "1", "disabled": "1", "creates_last_hour": "2", "scoped_peers": "1"} {
		if string(row[key]) != want {
			t.Errorf("%s = %s, want %s", key, row[key], want)
		}
	}
	body["kinds"] = []string{"operator"}
	resp, raw = env.Do(env.OperatorSecret, "PUT", "/v1/admin/principals/"+prn+"/limits", body)
	e := mustErr(t, resp, raw, 422, "invalid_value")
	if e.Error.Details["field"] != "kinds" {
		t.Fatalf("field: %+v", e)
	}
	for _, path := range []string{"/v1/admin/principals", "/v1/admin/principals/" + prn + "/limits", "/v1/admin/principals/" + prn + "/default-scopes"} {
		method := "PUT"
		if path == "/v1/admin/principals" {
			method = "GET"
		}
		resp, raw = env.Do(sec, method, path, body)
		mustErr(t, resp, raw, 403, "forbidden")
	}
}

func TestAdminDefaultScopes(t *testing.T) {
	env := testhub.Start(t)
	prn, _ := env.NewPrincipal("defaults")
	deny, _ := env.NewPeer("session", "denied")
	adminDo(t, env, "PUT", "/v1/admin/principals/"+prn+"/default-scopes", map[string][]string{"allow": {env.OperatorPeer}, "deny": {deny}}, 204)
	row := principalRow(t, env, prn)
	if string(row["default_allow"]) != `["`+env.OperatorPeer+`"]` || string(row["default_deny"]) != `["`+deny+`"]` {
		t.Fatalf("defaults: %v", row)
	}
	adminDo(t, env, "PUT", "/v1/admin/principals/"+prn+"/default-scopes", map[string]any{}, 204)
	row = principalRow(t, env, prn)
	if string(row["default_allow"]) != "[]" || string(row["default_deny"]) != "[]" {
		t.Fatalf("cleared defaults: %v", row)
	}
}
