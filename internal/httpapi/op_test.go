package httpapi_test

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/hub"
	"github.com/XertroV/vecnet/internal/opapi"
	"github.com/XertroV/vecnet/internal/testhub"
)

func TestOpRoutesOperatorOnly(t *testing.T) {
	env := testhub.Start(t)
	peer, secret := env.NewPeer("session", "reader")
	_, principal := env.NewPrincipal("reader principal")
	msg := postSend(t, env, env.OperatorSecret, sendBody("operator-contract-thread", peer), 202)
	paths := []struct{ method, path string }{
		{"GET", "/v1/admin/inbox"}, {"POST", "/v1/admin/threads/" + msg.ThreadID + "/read"},
		{"GET", "/v1/admin/threads"}, {"GET", "/v1/admin/threads/" + msg.ThreadID},
		{"GET", "/v1/admin/threads/" + msg.ThreadID + "/deliveries"}, {"GET", "/v1/admin/peers"},
		{"GET", "/v1/admin/loop-cuts"}, {"GET", "/v1/admin/delivery-health"},
	}
	for _, path := range paths {
		t.Run(path.method+path.path, func(t *testing.T) {
			resp, body := env.Do("", path.method, path.path, nil)
			mustErr(t, resp, body, 401, "unauthenticated")
			for _, bearer := range []string{secret, principal} {
				resp, body = env.Do(bearer, path.method, path.path, nil)
				mustErr(t, resp, body, 403, "forbidden")
			}
			resp, body = env.Do(env.OperatorSecret, path.method, path.path, nil)
			if resp.StatusCode != 200 {
				t.Fatalf("operator route = %d, want 200: %s", resp.StatusCode, body)
			}
		})
	}
	adminDo(t, env, "PATCH", "/v1/admin/threads/"+msg.ThreadID, map[string]any{"title": "renamed"}, 204)
}

func TestOpParamValidation(t *testing.T) {
	env := testhub.Start(t)
	for _, tc := range []struct{ path, field string }{
		{"/v1/admin/inbox?limit=0", "limit"}, {"/v1/admin/threads?limit=0", "limit"}, {"/v1/admin/threads/thr_example/deliveries?limit=0", "limit"},
		{"/v1/admin/loop-cuts?hours=0", "hours"}, {"/v1/admin/loop-cuts?hours=169", "hours"}, {"/v1/admin/loop-cuts?hours=no", "hours"},
		{"/v1/admin/inbox?before=xyz", "before"}, {"/v1/admin/inbox?before=dly_nope", "before"}, {"/v1/admin/inbox?before=", "before"},
		{"/v1/admin/threads?participant=xyz", "participant"}, {"/v1/admin/threads?participant=peer_nope", "participant"}, {"/v1/admin/threads?participant=", "participant"},
		{"/v1/admin/threads?exclude_participant=xyz", "exclude_participant"}, {"/v1/admin/threads?exclude_participant=peer_nope", "exclude_participant"}, {"/v1/admin/threads?exclude_participant=", "exclude_participant"},
	} {
		t.Run(tc.path, func(t *testing.T) {
			resp, raw := env.Do(env.OperatorSecret, "GET", tc.path, nil)
			e := mustErr(t, resp, raw, 422, "invalid_value")
			if e.Error.Details["field"] != tc.field {
				t.Fatalf("details.field = %v, want %s", e.Error.Details["field"], tc.field)
			}
		})
	}
	for _, path := range []string{"/v1/admin/loop-cuts?hours=1", "/v1/admin/loop-cuts?hours=168", "/v1/admin/inbox?before=dly_01J00000000000000000000000", "/v1/admin/threads?participant=" + env.OperatorPeer, "/v1/admin/threads?exclude_participant=" + env.OperatorPeer} {
		resp, raw := env.Do(env.OperatorSecret, "GET", path, nil)
		if resp.StatusCode != 200 {
			t.Fatalf("valid parameters on %s = %d: %s", path, resp.StatusCode, raw)
		}
	}
}

// exclude_participant drops the operator's own threads on the wire, so the
// web sidebar's Other Threads page is the newest threads it is not in.
func TestOpThreadsExcludeParticipant(t *testing.T) {
	env := testhub.Start(t)
	a, asec := env.NewPeer("session", "A")
	b, _ := env.NewPeer("session", "B")
	other := postSend(t, env, asec, sendBody("exclude-other", b), 202)
	env.Now.Advance(time.Second)
	mine := postSend(t, env, asec, sendBody("exclude-mine", env.OperatorPeer), 202)
	resp, raw := env.Do(env.OperatorSecret, "GET", "/v1/admin/threads?exclude_participant="+env.OperatorPeer, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("exclude = %d: %s", resp.StatusCode, raw)
	}
	var page opapi.ThreadPage
	if err := json.Unmarshal(raw, &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Threads) != 1 || page.Threads[0].Thread.ThreadID != other.ThreadID {
		t.Fatalf("excluded list = %s, want only %s (not %s)", raw, other.ThreadID, mine.ThreadID)
	}
	row := page.Threads[0]
	if row.Thread.MessageCount != 1 || row.LastMessageAt == "" || !reflect.DeepEqual(row.Thread.Participants, []string{a, b}) {
		t.Fatalf("row lacks what the sidebar shows: %+v", row)
	}
}

// These wire copies deliberately avoid a dependency from opapi to hub.
// Check their field sets as well as JSON so a newly added hub field cannot
// silently be omitted by an explicit conversion in this test. RuntimeStatus
// is an HTTP-only extension to the operator peer DTO; its live public/admin
// projection is checked by TestRuntimeStatusProjectionAndExpiry.
func TestOpHubJSONParity(t *testing.T) {
	at, location, via := "2026-11-02T00:00:00.000Z", "Sydney", "team_example"
	values := []struct {
		name           string
		original, copy any
	}{
		{"peer", hub.DirectoryEntry{PeerID: "peer_example", Name: "Operator", Kind: "operator", Capabilities: []string{"chat"}, Quota: map[string]any{"left": 42}, Location: &location, Presence: "working", Reachability: "connected", LastSeen: &at, EnrolledAt: &at, Teams: []string{"team_example"}, Avatar: &at}, opapi.Peer{}},
		{"audit", hub.AuditEntry{ID: 42, At: at, Actor: "peer_example", Action: "thread_renamed", Targets: []string{"thr_example"}, Detail: map[string]any{"title": "New"}}, opapi.AuditEntry{}},
		{"send", hub.SendResponse{MessageID: "msg_example", ThreadID: "thr_example", JobID: "job_example", Spilled: true, Deliveries: []hub.DeliveryRef{{DeliveryID: "dly_example", Recipient: "peer_example", Via: &via, State: "queued"}}}, opapi.SendResult{}},
		{"send no job", hub.SendResponse{MessageID: "msg_example", ThreadID: "thr_example", Deliveries: []hub.DeliveryRef{}}, opapi.SendResult{}},
	}
	for _, tc := range values {
		t.Run(tc.name, func(t *testing.T) {
			a, b := reflect.TypeOf(tc.original), reflect.TypeOf(tc.copy)
			wantFields := a.NumField()
			if tc.name == "peer" {
				wantFields-- // RuntimeStatus is added by opPeers, outside opapi.Peer.
			}
			if wantFields != b.NumField() {
				t.Fatalf("field counts differ: %s/%s", a, b)
			}
			for i := 0; i < a.NumField(); i++ {
				af := a.Field(i)
				if tc.name == "peer" && af.Name == "RuntimeStatus" {
					continue
				}
				bf, ok := b.FieldByName(af.Name)
				if !ok || af.Tag.Get("json") != bf.Tag.Get("json") {
					t.Errorf("field mismatch: %s", af.Name)
				}
			}
			raw, err := json.Marshal(tc.original)
			if err != nil {
				t.Fatal(err)
			}
			dst := reflect.New(b).Interface()
			if err := json.Unmarshal(raw, dst); err != nil {
				t.Fatal(err)
			}
			copied, err := json.Marshal(dst)
			if err != nil {
				t.Fatal(err)
			}
			var want, got any
			if err := json.Unmarshal(raw, &want); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(copied, &got); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(want, got) {
				t.Fatalf("hub %s != opapi %s", raw, copied)
			}
		})
	}
}
