package opview_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/hub"
	"github.com/XertroV/vecnet/internal/opapi"
	"github.com/XertroV/vecnet/internal/opview"
	"github.com/XertroV/vecnet/internal/testhub"
)

func dashExec(t *testing.T, env *testhub.Env, query string, args ...any) {
	t.Helper()
	if _, err := env.Hub.DB.ExecContext(context.Background(), query, args...); err != nil {
		t.Fatal(err)
	}
}

func peerRows(t *testing.T, env *testhub.Env) map[string]opapi.PeerRow {
	t.Helper()
	rows, err := opview.New(env.Hub).Peers(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]opapi.PeerRow{}
	for _, row := range rows {
		if _, ok := out[row.Peer.PeerID]; ok {
			t.Fatalf("duplicate peer %s", row.Peer.PeerID)
		}
		out[row.Peer.PeerID] = row
	}
	return out
}

func TestPeersOperatorView(t *testing.T) {
	env := testhub.Start(t)
	ctx := context.Background()
	active, _ := env.NewPeer("session", "active")
	disabled, _ := env.NewPeer("session", "disabled")
	dot, _ := env.NewPeer("dot", "push only")
	principal, _ := env.NewPrincipal("shared")
	inherited, _, err := env.Hub.AddPeer(ctx, "session", "inherited", principal)
	if err != nil {
		t.Fatal(err)
	}
	// A revoked peer credential cannot hide an active principal credential.
	if _, err := env.Hub.RotatePeer(ctx, inherited); err != nil {
		t.Fatal(err)
	}
	dashExec(t, env, `UPDATE credentials SET revoked_at = ? WHERE subject_id = ?`, envelope.FormatTime(env.Now.Now()), inherited)
	source, _, _, err := env.Hub.AddSource(ctx, "github", "source", active, nil, "wake")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := env.Hub.RotatePeer(ctx, disabled); err != nil {
		t.Fatal(err)
	}
	dashExec(t, env, `UPDATE credentials SET revoked_at = ? WHERE subject_id = ?`, envelope.FormatTime(env.Now.Now()), disabled)
	if err := env.Hub.SetPeerDisabled(ctx, disabled, true); err != nil {
		t.Fatal(err)
	}
	now := envelope.FormatTime(env.Now.Now())
	dashExec(t, env, `UPDATE peers SET capabilities = '["chat"]', quota = '{"left":42}', location = 'Sydney', presence_state = 'working', presence_at = ?, reachability = 'reachable', reach_via = 'call', last_seen = ? WHERE peer_id = ?`, now, now, active)
	dashExec(t, env, `INSERT INTO team_members(team_id, peer_id) VALUES ('team_z', ?), ('team_a', ?)`, active, active)
	env.Hub.ObservePush(ctx, dot, true)
	rawURL := "https://example.com:8443/private-callback?key=never-show-query"
	route, whsec, err := env.Hub.AddRoute(ctx, "principal", principal, rawURL)
	if err != nil {
		t.Fatal(err)
	}
	grokKey := "crsr_super-private-routine-key"
	grokRoute, err := env.Hub.AddGrokbotRoute(ctx, "peer", dot, "https://api2.cursor.sh/automations/webhook/12345678-1234-1234-1234-123456789abc", grokKey)
	if err != nil {
		t.Fatal(err)
	}
	var sealed string
	if err := env.Hub.DB.QueryRow(`SELECT key_enc FROM routes WHERE route_id = ?`, grokRoute).Scan(&sealed); err != nil {
		t.Fatal(err)
	}
	if sealed == "" || sealed == grokKey {
		t.Fatal("fixture must store a sealed routine key")
	}
	rows := peerRows(t, env)
	if rows[active].Credential != "active" || rows[inherited].Credential != "active" || rows[env.OperatorPeer].Credential != "active" {
		t.Fatalf("active credentials: %+v", rows)
	}
	if got := rows[disabled]; got.Credential != "revoked" || !got.Disabled || got.Peer.Name != "disabled" {
		t.Fatalf("disabled rotated/revoked peer: %+v", got)
	}
	if got := rows[dot]; got.Credential != "none" || got.Peer.Presence != "unknown" || got.Peer.Reachability != "reachable" || got.ReachVia == nil || *got.ReachVia != "push" {
		t.Fatalf("push-only peer: %+v", got)
	}
	if rows[active].PresenceAt == nil || *rows[active].PresenceAt != now || rows[active].ReachVia == nil || *rows[active].ReachVia != "call" {
		t.Fatalf("stored timestamps/transport missing: %+v", rows[active])
	}
	// Enabled entries remain byte-for-byte compatible with the public directory,
	// including source entries; operator metadata is additional.
	directory, err := env.Hub.Directory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range directory {
		want, err := json.Marshal(entry)
		if err != nil {
			t.Fatal(err)
		}
		got, err := json.Marshal(rows[entry.PeerID].Peer)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != string(want) {
			t.Fatalf("directory mismatch for %s: got %s want %s", entry.PeerID, got, want)
		}
	}
	if rows[source].Peer.Kind != "source" {
		t.Fatal("directory source omitted")
	}
	if got := rows[inherited]; got.PrincipalID != principal || len(got.Routes) != 1 || got.Routes[0].RouteID != route || got.Routes[0].Host != "example.com:8443" || got.Routes[0].Kind != "http" || got.Routes[0].VerifiedAt != nil {
		t.Fatalf("principal route: %+v", got)
	}
	if got := rows[dot].Routes; len(got) != 1 || got[0].RouteID != grokRoute || got[0].Host != "api2.cursor.sh" || got[0].Kind != "grokbot" || got[0].VerifiedAt == nil {
		t.Fatalf("grokbot route: %+v", got)
	}
	resp, wire := env.Do(env.OperatorSecret, "GET", "/v1/admin/peers", nil)
	if resp.StatusCode != 200 {
		t.Fatalf("peers HTTP %d: %s", resp.StatusCode, wire)
	}
	for _, forbidden := range []string{rawURL, "private-callback", "never-show-query", "automations/webhook", "12345678-1234", whsec, "whsec_", grokKey, "crsr_", sealed, "key_enc", "secret_hash"} {
		if strings.Contains(string(wire), forbidden) {
			t.Fatalf("peer JSON leaked %q", forbidden)
		}
	}
	env.Now.Advance(25 * time.Hour)
	rows = peerRows(t, env)
	if rows[active].Peer.Presence != "unknown" || rows[active].Peer.Reachability != "unknown" || rows[dot].Peer.Presence != "unknown" || rows[dot].Peer.Reachability != "reachable" {
		t.Fatal("presence/call decay must not expire push reachability")
	}
}

func TestLoopCutsWindow(t *testing.T) {
	env := testhub.Start(t)
	now := env.Now.Now()
	add := func(at time.Time, action, reason, thread string) {
		t.Helper()
		targets, _ := json.Marshal([]string{thread})
		detail, _ := json.Marshal(map[string]string{"reason": reason})
		dashExec(t, env, `INSERT INTO audit(at, actor, action, target_ids, detail) VALUES (?, 'peer_actor', ?, ?, ?)`, envelope.FormatTime(at), action, string(targets), string(detail))
	}
	add(now.Add(-25*time.Hour), "loop_cut", "expired", "thr_old")
	add(now.Add(-time.Hour), "rate_limited", "rate_limited", "thr_rate")
	add(now.Add(-24*time.Hour), "loop_cut", "expired", "thr_expired")
	add(now.Add(-2*time.Hour), "loop_cut", "thread_budget_exhausted", "thr_budget")
	add(now.Add(-time.Hour), "loop_cut", "max_depth_exceeded", "thr_depth")
	view := opview.New(env.Hub)
	got, err := view.LoopCuts(context.Background(), 24)
	if err != nil {
		t.Fatal(err)
	}
	if got.Hours != 24 || got.Since != envelope.FormatTime(now.Add(-24*time.Hour)) || got.Total != 3 || !reflect.DeepEqual(got.ByReason, map[string]int{"expired": 1, "thread_budget_exhausted": 1, "max_depth_exceeded": 1}) {
		t.Fatalf("cuts: %+v", got)
	}
	if len(got.Recent) != 3 || got.Recent[0].ThreadID != "thr_depth" || got.Recent[1].ThreadID != "thr_budget" || got.Recent[2].ThreadID != "thr_expired" || got.Recent[0].Actor != "peer_actor" || got.Recent[0].Reason != "max_depth_exceeded" || got.Recent[0].At != envelope.FormatTime(now.Add(-time.Hour)) {
		t.Fatalf("recent cuts: %+v", got.Recent)
	}
	for i := 0; i < 55; i++ {
		add(now.Add(time.Duration(i-55)*time.Second), "loop_cut", "expired", fmt.Sprintf("thr_%02d", i))
	}
	got, err = view.LoopCuts(context.Background(), 24)
	if err != nil {
		t.Fatal(err)
	}
	if got.Total != 58 || got.ByReason["expired"] != 56 || len(got.Recent) != 50 || got.Recent[0].ThreadID != "thr_54" || got.Recent[49].ThreadID != "thr_05" {
		t.Fatalf("cut limit/count/order: %+v", got)
	}
	env.Now.Advance(8 * 24 * time.Hour)
	got, err = view.LoopCuts(context.Background(), 24)
	if err != nil {
		t.Fatal(err)
	}
	if got.Total != 0 || len(got.Recent) != 0 || len(got.ByReason) != 0 {
		t.Fatalf("expired window: %+v", got)
	}
}

type dashFixture struct {
	env       *testhub.Env
	recipient string
	status    atomic.Int32
	serial    int
}

func newDashFixture(t *testing.T, status int) *dashFixture {
	t.Helper()
	f := &dashFixture{env: testhub.Start(t)}
	f.status.Store(int32(status))
	f.recipient, _ = f.env.NewPeer("session", "receiver")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var probe struct {
			Type      string `json:"type"`
			Challenge string `json:"challenge"`
		}
		if err := json.NewDecoder(r.Body).Decode(&probe); err != nil {
			http.Error(w, "bad JSON", 400)
			return
		}
		if probe.Type == "verification" {
			_ = json.NewEncoder(w).Encode(map[string]string{"challenge": probe.Challenge})
			return
		}
		w.WriteHeader(int(f.status.Load()))
	}))
	t.Cleanup(srv.Close)
	route, _, err := f.env.Hub.AddRoute(context.Background(), "peer", f.recipient, srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.env.Worker.Verify(context.Background(), route); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *dashFixture) send(t *testing.T, thread string) hub.SendResponse {
	t.Helper()
	f.serial++
	req := map[string]any{"idempotency_key": fmt.Sprintf("dash-%d", f.serial), "priority": "wake", "recipients": []string{f.recipient}, "content": map[string]string{"text": "hello"}}
	if thread != "" {
		req["thread_id"] = thread
	}
	resp, raw := f.env.Do(f.env.OperatorSecret, "POST", "/v1/messages", req)
	if resp.StatusCode != 202 {
		t.Fatalf("send HTTP %d: %s", resp.StatusCode, raw)
	}
	var result hub.SendResponse
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func (f *dashFixture) step(t *testing.T, want int) {
	t.Helper()
	n, err := f.env.Worker.Step(context.Background())
	if err != nil || n != want {
		t.Fatalf("worker pushes %d, %v; want %d", n, err, want)
	}
}

func health(t *testing.T, env *testhub.Env) opapi.DeliveryHealth {
	t.Helper()
	got, err := opview.New(env.Hub).DeliveryHealth(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestBlockedThread(t *testing.T) {
	t.Run("retry and independent thread", func(t *testing.T) {
		f := newDashFixture(t, 503)
		first := f.send(t, "")
		f.send(t, first.ThreadID)
		f.send(t, first.ThreadID)
		if got := health(t, f.env); len(got.Blocked) != 0 {
			t.Fatalf("unattempted queue blocked: %+v", got)
		}
		f.step(t, 1)
		got := health(t, f.env)
		if len(got.Blocked) != 1 {
			t.Fatalf("blocked: %+v", got)
		}
		b := got.Blocked[0]
		if b.Recipient != f.recipient || b.ThreadID != first.ThreadID || b.DeliveryID != first.Deliveries[0].DeliveryID || b.Waiting != 2 || b.Attempt != 1 || b.NextAttemptAt == nil || b.LastStatus == nil || *b.LastStatus != 503 || b.LastError == nil {
			t.Fatalf("retry detail: %+v", b)
		}
		f.status.Store(200)
		f.send(t, "")
		f.step(t, 1) // The other thread flows while the retry is still backed off.
		if b := health(t, f.env).Blocked; len(b) != 1 || b[0].Waiting != 2 || b[0].ThreadID != first.ThreadID {
			t.Fatalf("independent thread: %+v", b)
		}
		f.env.Now.Advance(3 * time.Second)
		f.step(t, 1)
		if b := health(t, f.env).Blocked; len(b) != 0 {
			t.Fatalf("successful retry still blocked: %+v", b)
		}
	})
	t.Run("matches serialization predicates", func(t *testing.T) {
		// Earlier nonpushable rows must not mask a retrying head, and later
		// nonpushable rows must not inflate its waiting count.
		for _, tc := range []struct{ name, update string }{
			{"hold", `priority = 'hold'`}, {"held", `held = 1`}, {"no route", `route_id = NULL`},
			{"accepted", `state = 'accepted'`}, {"failed", `state = 'failed'`}, {"expired state", `state = 'expired'`},
		} {
			t.Run(tc.name, func(t *testing.T) {
				f := newDashFixture(t, 503)
				earlier := f.send(t, "")
				head := f.send(t, earlier.ThreadID)
				later := f.send(t, earlier.ThreadID)
				dashExec(t, f.env, `UPDATE deliveries SET `+tc.update+` WHERE dly_id IN (?, ?)`, earlier.Deliveries[0].DeliveryID, later.Deliveries[0].DeliveryID)
				f.step(t, 1)
				got := health(t, f.env).Blocked
				if len(got) != 1 || got[0].DeliveryID != head.Deliveries[0].DeliveryID || got[0].Waiting != 0 {
					t.Fatalf("predicate %s: %+v", tc.name, got)
				}
			})
		}
	})
	t.Run("inflight and expired but unterminated rows serialize", func(t *testing.T) {
		f := newDashFixture(t, 503)
		first := f.send(t, "")
		f.send(t, first.ThreadID)
		claims, err := f.env.Hub.ClaimDue(context.Background(), f.env.Now.Now(), 10)
		if err != nil || len(claims) != 1 {
			t.Fatalf("claim: %+v, %v", claims, err)
		}
		dashExec(t, f.env, `UPDATE deliveries SET expires_at = ? WHERE thr_id = ?`, envelope.FormatTime(f.env.Now.Now().Add(-time.Second)), first.ThreadID)
		got := health(t, f.env).Blocked
		if len(got) != 1 || got[0].Attempt != 1 || got[0].Waiting != 1 || got[0].LastStatus != nil || got[0].LastError != nil {
			t.Fatalf("inflight head: %+v", got)
		}
	})
}

func TestFailedWindow(t *testing.T) {
	f := newDashFixture(t, 409)
	first := f.send(t, "")
	f.step(t, 1)
	got := health(t, f.env)
	if len(got.Failed) != 1 {
		t.Fatalf("409 missing: %+v", got)
	}
	failed := got.Failed[0]
	if failed.DeliveryID != first.Deliveries[0].DeliveryID || failed.Recipient != f.recipient || failed.ThreadID != first.ThreadID || failed.UpdatedAt != envelope.FormatTime(f.env.Now.Now()) || failed.LastStatus == nil || *failed.LastStatus != 409 || failed.LastError == nil || len(got.Blocked) != 0 {
		t.Fatalf("409 details: %+v", got)
	}
	// More than a page of actual failed-state rows tests ordering and cap;
	// a newer dead letter is deliberately not a failed delivery.
	for i := 0; i < 105; i++ {
		dashExec(t, f.env, `INSERT INTO deliveries(dly_id,msg_id,thr_id,recipient,priority,state,attempts_left,expires_at,created_at,updated_at,body_sha256) VALUES (?, 'msg_fixture', 'thr_fixture', ?, 'wake', 'failed', 0, ?, ?, ?, 'fixture')`, fmt.Sprintf("dly_fixture_%03d", i), f.recipient, envelope.FormatTime(f.env.Now.Now()), envelope.FormatTime(f.env.Now.Now()), envelope.FormatTime(f.env.Now.Now().Add(time.Duration(i-105)*time.Second)))
	}
	dashExec(t, f.env, `INSERT INTO deliveries(dly_id,msg_id,thr_id,recipient,priority,state,attempts_left,expires_at,created_at,updated_at,body_sha256) VALUES ('dly_dead_letter', 'msg_fixture', 'thr_fixture', ?, 'wake', 'dead_letter', 0, ?, ?, ?, 'fixture')`, f.recipient, envelope.FormatTime(f.env.Now.Now()), envelope.FormatTime(f.env.Now.Now()), envelope.FormatTime(f.env.Now.Now()))
	got = health(t, f.env)
	if len(got.Failed) != 100 || got.Failed[0].DeliveryID != first.Deliveries[0].DeliveryID || got.Failed[1].DeliveryID != "dly_fixture_104" || got.Failed[99].DeliveryID != "dly_fixture_006" {
		t.Fatalf("failed cap/order: %+v", got.Failed)
	}
	f.env.Now.Advance(8 * 24 * time.Hour)
	got = health(t, f.env)
	if len(got.Failed) != 0 {
		t.Fatalf("8-day failures still listed: %+v", got.Failed)
	}
}

func TestPeersRowCarriesCreatedBy(t *testing.T) {
	env := testhub.Start(t)
	ctx := context.Background()
	prn, sec := env.NewPrincipal("creator")
	if err := env.Hub.SetPrincipalLimits(ctx, prn, hub.PrincipalLimits{MaxPeers: 1, CreatesPerHour: 10, Kinds: []string{"pagchat-bot"}}); err != nil {
		t.Fatal(err)
	}
	caller, err := env.Hub.Auth.Resolve(ctx, sec)
	if err != nil {
		t.Fatal(err)
	}
	created, err := env.Hub.CreatePeer(ctx, caller, hub.CreatePeerRequest{Kind: "pagchat-bot", Name: "created", IdempotencyKey: "row"})
	if err != nil {
		t.Fatal(err)
	}
	enrolled, _ := env.NewPeer("session", "enrolled")
	rows := peerRows(t, env)
	if got := rows[created.Peer.PeerID].Peer.CreatedBy; got == nil || *got != prn {
		t.Fatalf("created_by: %v", got)
	}
	if rows[enrolled].Peer.CreatedBy != nil {
		t.Fatal("operator-enrolled peer has created_by")
	}
	if err := env.Hub.SetPeerDisabled(ctx, created.Peer.PeerID, true); err != nil {
		t.Fatal(err)
	}
	if got := peerRows(t, env)[created.Peer.PeerID].Peer.CreatedBy; got == nil || *got != prn {
		t.Fatalf("disabled created_by: %v", got)
	}
}

// PeerRow carries each peer's own scopes and send limits (PROTOCOL §2.13), so
// the web peer view needs no per-peer request. A source has neither.
func TestPeersRowCarriesScopesAndLimits(t *testing.T) {
	env := testhub.Start(t)
	ctx := context.Background()
	scoped, _ := env.NewPeer("session", "scoped")
	friend, _ := env.NewPeer("session", "friend")
	foe, _ := env.NewPeer("session", "foe")
	plain, _ := env.NewPeer("session", "plain")
	if err := env.Hub.SetScopes(ctx, scoped, []string{friend}, []string{foe}); err != nil {
		t.Fatal(err)
	}
	if err := env.Hub.SetPeerLimits(ctx, scoped, 7, 3); err != nil {
		t.Fatal(err)
	}
	source, _, _, err := env.Hub.AddSource(ctx, "github", "source", plain, nil, "wake")
	if err != nil {
		t.Fatal(err)
	}
	rows := peerRows(t, env)
	got := rows[scoped]
	if !reflect.DeepEqual(got.Scopes, opapi.PeerScopes{Allow: []string{friend}, Deny: []string{foe}}) {
		t.Fatalf("scopes: %+v", got.Scopes)
	}
	if got.Limits == nil || got.Limits.SendsPerMin != 7 || got.Limits.ThreadsPerHour != 3 {
		t.Fatalf("limits: %+v", got.Limits)
	}
	// Defaults: an unscoped peer allows all, and carries the column defaults.
	if p := rows[plain]; len(p.Scopes.Allow) != 0 || len(p.Scopes.Deny) != 0 || p.Limits == nil || p.Limits.SendsPerMin != 60 || p.Limits.ThreadsPerHour != 20 {
		t.Fatalf("plain: %+v %+v", p.Scopes, p.Limits)
	}
	if s := rows[source]; s.Limits != nil || len(s.Scopes.Allow) != 0 || len(s.Scopes.Deny) != 0 {
		t.Fatalf("source: %+v %+v", s.Scopes, s.Limits)
	}
	// On the wire: empty lists are [], a source's limits are null.
	resp, wire := env.Do(env.OperatorSecret, "GET", "/v1/admin/peers", nil)
	if resp.StatusCode != 200 {
		t.Fatalf("peers HTTP %d: %s", resp.StatusCode, wire)
	}
	var page struct {
		Peers []map[string]json.RawMessage `json:"peers"`
	}
	if err := json.Unmarshal(wire, &page); err != nil {
		t.Fatal(err)
	}
	seen := 0
	for _, row := range page.Peers {
		var peer struct {
			PeerID string `json:"peer_id"`
		}
		if err := json.Unmarshal(row["peer"], &peer); err != nil {
			t.Fatal(err)
		}
		switch peer.PeerID {
		case plain:
			seen++
			if string(row["scopes"]) != `{"allow":[],"deny":[]}` || string(row["limits"]) != `{"sends_per_min":60,"threads_per_hour":20}` {
				t.Fatalf("plain wire: scopes %s limits %s", row["scopes"], row["limits"])
			}
		case source:
			seen++
			if string(row["scopes"]) != `{"allow":[],"deny":[]}` || string(row["limits"]) != "null" {
				t.Fatalf("source wire: scopes %s limits %s", row["scopes"], row["limits"])
			}
		}
	}
	if seen != 2 {
		t.Fatalf("wire rows seen: %d", seen)
	}
}
