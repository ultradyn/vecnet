package hub_test

import (
	"context"
	"encoding/json"
	"reflect"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/auth"
	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/hub"
	"github.com/XertroV/vecnet/internal/testhub"
)

func runtimeReport(t *testing.T, env *testhub.Env, sec string, body map[string]any) {
	t.Helper()
	resp, raw := env.Do(sec, "POST", "/v1/runtime-status", body)
	if resp.StatusCode != 204 {
		t.Fatalf("runtime report=%d: %s", resp.StatusCode, raw)
	}
}

// A heartbeat must renew timestamps without durable event growth or waiter
// wakeups; content transitions and stale->fresh must do both, atomically.
func TestRuntimeStatusMaterialChangesNotifyAndHeartbeatsDoNot(t *testing.T) {
	env := testhub.Start(t)
	peer, sec := env.NewPeer("session", "material runtime")
	observer, _ := env.NewPeer("session", "runtime observer")
	body := map[string]any{"runtime": "codex", "state": "idle", "delivery": "ready", "ttl_seconds": 60, "version": "1.0.0"}
	wantEvents := 0
	check := func(material bool) {
		t.Helper()
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		wake := env.Hub.Log.Wait(ctx, []string{observer})
		eventsBefore := count(t, env, `SELECT count(*) FROM events`)
		runtimeReport(t, env, sec, body)
		select {
		case <-wake:
			if !material {
				t.Fatal("heartbeat woke directory reader")
			}
		default:
			if material {
				t.Fatal("material change did not wake directory reader")
			}
		}
		if material {
			wantEvents++
		} else if got := count(t, env, `SELECT count(*) FROM events`); got != eventsBefore {
			t.Fatalf("heartbeat grew durable events from %d to %d", eventsBefore, got)
		}
		for _, owner := range []string{peer, observer, env.OperatorPeer} {
			n := count(t, env, `SELECT count(*) FROM events WHERE owner=? AND type='directory_updated' AND json_extract(data,'$.peer_id')=? AND json_extract(data,'$.fields')='["runtime_status"]'`, owner, peer)
			if n != wantEvents {
				t.Fatalf("owner %s events=%d, want %d", owner, n, wantEvents)
			}
		}
	}
	check(true)
	env.Now.Advance(30 * time.Second)
	body["ttl_seconds"] = 90
	check(false)
	entry := directoryEntry(t, env, peer)
	raw, _ := json.Marshal(entry)
	var wire map[string]any
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatal(err)
	}
	status, ok := wire["runtime_status"].(map[string]any)
	if !ok {
		t.Fatalf("missing status: %s", raw)
	}
	if status["updated_at"] != envelope.FormatTime(env.Now.Now()) || status["expires_at"] != envelope.FormatTime(env.Now.Now().Add(90*time.Second)) {
		t.Fatalf("heartbeat failed to renew: %v", status)
	}
	body["state"] = "working"
	check(true)
	body["delivery"] = "unavailable"
	check(true)
	body["reason"] = "waiting_input"
	check(true)
	body["version"] = "1.0.1"
	check(true)
	delete(body, "reason")
	check(true)
	delete(body, "version")
	check(true)
	// Equality is stale, even when the report's content is unchanged.
	env.Now.Advance(90 * time.Second)
	check(true)
	check(false)
	var records int
	if err := env.Hub.DB.QueryRow(`SELECT count(*) FROM peer_runtime_status WHERE peer_id=?`, peer).Scan(&records); err != nil || records != 1 {
		t.Fatalf("snapshot rows=%d err=%v", records, err)
	}
}

func TestRuntimeStatusIndependentOfPresenceAndQuota(t *testing.T) {
	env := testhub.Start(t)
	peer, sec := env.NewPeer("session", "independent runtime")
	resp, raw := env.Do(sec, "POST", "/v1/presence", map[string]any{"state": "working", "at": envelope.FormatTime(env.Now.Now()), "capabilities": []string{"manual-capability"}, "quota": map[string]any{"weekly_remaining_pct": 42}, "location": "manual location", "note": "manual note"})
	if resp.StatusCode != 204 {
		t.Fatalf("presence=%d: %s", resp.StatusCode, raw)
	}
	before := directoryEntry(t, env, peer)
	hookCalls := 0
	env.Hub.IdleHook = func(_ context.Context, _ string) error { hookCalls++; return nil }
	runtimeReport(t, env, sec, map[string]any{"runtime": "codex", "state": "idle", "delivery": "ready", "ttl_seconds": 1})
	after := directoryEntry(t, env, peer)
	if before.Presence != after.Presence || !reflect.DeepEqual(before.Quota, after.Quota) || !reflect.DeepEqual(before.Capabilities, after.Capabilities) || !reflect.DeepEqual(before.Location, after.Location) || hookCalls != 0 {
		t.Fatalf("runtime altered manual state: before=%+v after=%+v hook=%d", before, after, hookCalls)
	}
	env.Now.Advance(time.Second)
	after = directoryEntry(t, env, peer)
	if after.Presence != "working" {
		t.Fatalf("runtime expiry changed presence: %+v", after)
	}
	// A subsequent presence update must preserve the independent runtime row.
	resp, raw = env.Do(sec, "POST", "/v1/presence", map[string]any{"state": "blocked", "at": envelope.FormatTime(env.Now.Now()), "quota": map[string]any{"weekly_remaining_pct": 1}})
	if resp.StatusCode != 204 {
		t.Fatalf("presence=%d: %s", resp.StatusCode, raw)
	}
	var state string
	if err := env.Hub.DB.QueryRow(`SELECT state FROM peer_runtime_status WHERE peer_id=?`, peer).Scan(&state); err != nil || state != "idle" {
		t.Fatalf("presence clobbered runtime: %q err=%v", state, err)
	}
}

// Even an OAuth-style principal caller carrying its bound peer must explicitly
// select the reporting target. Runtime reporting uses the frozen ?as= contract.
func TestRuntimeStatusPrincipalRequiresExplicitTarget(t *testing.T) {
	env := testhub.Start(t)
	prn, sec := env.NewPrincipal("explicit runtime principal")
	peer, _, err := env.Hub.AddPeer(t.Context(), "session", "explicit runtime target", prn)
	if err != nil {
		t.Fatal(err)
	}
	c := provisionCaller(t, env, sec)
	c.PeerID = peer
	p := hub.RuntimeStatusReport{Runtime: "codex", State: "idle", Delivery: "ready", TTLSeconds: 60}
	provisionError(t, env.Hub.SetRuntimeStatus(t.Context(), c, "", p), 422, "as_required")
	if got := count(t, env, `SELECT count(*) FROM peer_runtime_status WHERE peer_id=?`, peer); got != 0 {
		t.Fatalf("implicit target wrote %d snapshots", got)
	}
	if err := env.Hub.SetRuntimeStatus(t.Context(), c, peer, p); err != nil {
		t.Fatalf("explicit owned target: %v", err)
	}
}

// The snapshot and refresh event commit together. A failed event append must
// not leave an updated status (or wake readers) that the caller was told failed.
func TestRuntimeStatusFailedEventRollsBackSnapshot(t *testing.T) {
	env := testhub.Start(t)
	peer, sec := env.NewPeer("session", "rollback runtime")
	observer, _ := env.NewPeer("session", "rollback observer")
	body := map[string]any{"runtime": "codex", "state": "idle", "delivery": "ready", "ttl_seconds": 60}
	runtimeReport(t, env, sec, body)
	before := directoryEntry(t, env, peer)
	n := count(t, env, `SELECT count(*) FROM events`)
	if _, err := env.Hub.DB.Exec(`CREATE TRIGGER fail_runtime_directory BEFORE INSERT ON events WHEN NEW.type='directory_updated' BEGIN SELECT RAISE(ABORT,'runtime event fault'); END`); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	wake := env.Hub.Log.Wait(ctx, []string{observer})
	env.Now.Advance(time.Second)
	body["state"] = "working"
	resp, raw := env.Do(sec, "POST", "/v1/runtime-status", body)
	if resp.StatusCode != 500 {
		t.Fatalf("failed append=%d: %s", resp.StatusCode, raw)
	}
	select {
	case <-wake:
		t.Fatal("rolled back report woke directory readers")
	default:
	}
	after := directoryEntry(t, env, peer)
	if !reflect.DeepEqual(before.RuntimeStatus, after.RuntimeStatus) {
		t.Fatalf("failed report changed snapshot: before=%+v after=%+v", before.RuntimeStatus, after.RuntimeStatus)
	}
	if got := count(t, env, `SELECT count(*) FROM events`); got != n {
		t.Fatalf("failed report changed event count=%d want %d", got, n)
	}
}

// Runtime cannot release wake_idle work or become a delivery gate. The real
// presence policy remains the authority in both directions.
func TestRuntimeStatusDoesNotGateOrReleaseDelivery(t *testing.T) {
	env := testhub.Start(t)
	_, sender := env.NewPeer("session", "runtime sender")
	peer, sec := env.NewPeer("session", "runtime recipient")
	reportPresence(t, env, sec, "", "working")
	b := req("runtime-held", peer)
	b["priority"] = "wake_idle"
	sent := mustSend(t, env, sender, b)
	dly := sent.Deliveries[0].DeliveryID
	if _, held := deliveryState(t, env, dly); held != 1 {
		t.Fatal("fixture must start held")
	}
	runtimeReport(t, env, sec, map[string]any{"runtime": "codex", "state": "idle", "delivery": "ready", "ttl_seconds": 60})
	if _, held := deliveryState(t, env, dly); held != 1 {
		t.Fatal("runtime idle released held delivery")
	}
	runtimeReport(t, env, sec, map[string]any{"runtime": "codex", "state": "offline", "delivery": "unavailable", "ttl_seconds": 60})
	reportPresence(t, env, sec, "", "idle")
	if _, held := deliveryState(t, env, dly); held != 0 {
		t.Fatal("runtime offline prevented manual presence release")
	}
	b = req("runtime-offline-send", peer)
	b["priority"] = "wake_idle"
	sent = mustSend(t, env, sender, b)
	if _, held := deliveryState(t, env, sent.Deliveries[0].DeliveryID); held != 0 {
		t.Fatal("runtime offline gated a new delivery")
	}
}

func TestRuntimeStatusPrincipalReportRacesIdleSweep(t *testing.T) {
	env := testhub.Start(t)
	ctx := context.Background()
	p, _ := env.NewPeer("session", "race-runtime")
	old := envelope.FormatTime(env.Now.Now().Add(-72 * time.Hour))
	archiveExec(t, env, `UPDATE peers SET enrolled_at=?,last_seen=? WHERE peer_id=?`, old, old, p)
	var prn string
	if err := env.Hub.DB.QueryRow(`SELECT prn_id FROM peers WHERE peer_id=?`, p).Scan(&prn); err != nil {
		t.Fatal(err)
	}
	env.Hub.DB.SetMaxOpenConns(1)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	env.Hub.Now = func() time.Time { once.Do(func() { close(entered); <-release }); return env.Now.Now() }
	reportDone := make(chan error, 1)
	go func() {
		reportDone <- env.Hub.SetRuntimeStatus(ctx, auth.Caller{Kind: "principal", PrincipalID: prn}, p, hub.RuntimeStatusReport{Runtime: "codex", State: "idle", Delivery: "ready", TTLSeconds: 60})
	}()
	<-entered // The report holds the writer before committing fresh activity.
	waits := env.Hub.DB.Stats().WaitCount
	sweepDone := make(chan error, 1)
	go func() { _, err := env.Hub.ArchiveIdlePeers(ctx, env.Now.Now()); sweepDone <- err }()
	deadline := time.Now().Add(5 * time.Second)
	for env.Hub.DB.Stats().WaitCount == waits && time.Now().Before(deadline) {
		runtime.Gosched()
	}
	if env.Hub.DB.Stats().WaitCount == waits {
		close(release)
		t.Fatal("sweep never queued")
	}
	close(release) // the sweep is queued for the DB writer; the report must record last_seen in its own transaction.
	if err := <-reportDone; err != nil {
		t.Fatal(err)
	}
	if err := <-sweepDone; err != nil {
		t.Fatal(err)
	}
	if n := archiveCount(t, env, `SELECT count(*) FROM peers WHERE peer_id=? AND (archived_at IS NOT NULL OR disabled=1)`, p); n != 0 {
		t.Fatal("peer was archived or disabled despite a committed fresh runtime-status report")
	}
}
