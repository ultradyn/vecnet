package httpapi_test

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/store"
	"github.com/XertroV/vecnet/internal/testhub"
)

func runtimeBody() map[string]any {
	return map[string]any{"runtime": "codex", "state": "idle", "delivery": "ready", "version": "1.2.3-dev.1+build.5", "ttl_seconds": 60}
}

func postRuntime(t *testing.T, env *testhub.Env, secret, query string, body any) {
	t.Helper()
	resp, raw := env.Do(secret, "POST", "/v1/runtime-status"+query, body)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("runtime report = %d: %s", resp.StatusCode, raw)
	}
}

// Both endpoints must expose the same safe projection, including for disabled
// admin rows. Checking exact keys catches accidentally publishing binding data.
func runtimeFromPeers(t *testing.T, env *testhub.Env, path, peer string) map[string]any {
	t.Helper()
	resp, raw := env.Do(env.OperatorSecret, "GET", path, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("%s = %d: %s", path, resp.StatusCode, raw)
	}
	var body struct {
		Peers []map[string]json.RawMessage `json:"peers"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	for _, row := range body.Peers {
		if path == "/v1/admin/peers" {
			var p map[string]json.RawMessage
			if err := json.Unmarshal(row["peer"], &p); err != nil {
				t.Fatal(err)
			}
			row = p
		}
		var id string
		if err := json.Unmarshal(row["peer_id"], &id); err != nil {
			t.Fatal(err)
		}
		if id != peer {
			continue
		}
		var status map[string]any
		if raw := row["runtime_status"]; len(raw) > 0 {
			if err := json.Unmarshal(raw, &status); err != nil {
				t.Fatal(err)
			}
		}
		return status
	}
	t.Fatalf("peer %s missing from %s", peer, path)
	return nil
}

func TestRuntimeStatusProjectionAndExpiry(t *testing.T) {
	env := testhub.Start(t)
	peer, secret := env.NewPeer("session", "runtime peer")
	never, _ := env.NewPeer("session", "never reported")
	source, _, _, err := env.Hub.AddSource(t.Context(), "github", "runtime source", peer, nil, "hold")
	if err != nil {
		t.Fatal(err)
	}
	paths := []string{"/v1/peers", "/v1/admin/peers"}
	for _, path := range paths {
		if got := runtimeFromPeers(t, env, path, never); got != nil {
			t.Fatalf("never reported = %v", got)
		}
		if got := runtimeFromPeers(t, env, path, source); got != nil {
			t.Fatalf("source status = %v", got)
		}
	}
	now := env.Now.Now()
	body := runtimeBody()
	body["state"] = "blocked"
	body["delivery"] = "unavailable"
	body["reason"] = "waiting_approval"
	postRuntime(t, env, secret, "", body)
	want := map[string]any{"runtime": "codex", "state": "blocked", "delivery": "unavailable", "reason": "waiting_approval", "version": "1.2.3-dev.1+build.5", "updated_at": envelope.FormatTime(now), "expires_at": envelope.FormatTime(now.Add(time.Minute))}
	for _, path := range paths {
		if got := runtimeFromPeers(t, env, path, peer); !reflect.DeepEqual(got, want) {
			t.Fatalf("%s status = %v, want %v", path, got, want)
		}
	}
	env.Now.Advance(time.Minute - time.Millisecond)
	if got := runtimeFromPeers(t, env, paths[0], peer); got["state"] != "blocked" {
		t.Fatalf("expired early: %v", got)
	}
	env.Now.Advance(time.Millisecond)
	want["state"], want["delivery"], want["reason"] = "unknown", "unknown", "stale"
	for _, path := range paths {
		if got := runtimeFromPeers(t, env, path, peer); !reflect.DeepEqual(got, want) {
			t.Fatalf("%s at expiry = %v, want %v", path, got, want)
		}
	}
	// Persistence, not in-memory freshness: another hub sees the same expired row.
	clone := env.CloneDB(t)
	clone.Now.Advance(now.Add(time.Minute).Sub(clone.Now.Now()))
	if got := runtimeFromPeers(t, clone, paths[0], peer); !reflect.DeepEqual(got, want) {
		t.Fatalf("reopened status = %v", got)
	}
}

func TestRuntimeStatusAuthenticationAndTargetIsolation(t *testing.T) {
	env := testhub.Start(t)
	own, sec := env.NewPeer("session", "own runtime")
	foreign, otherSec := env.NewPeer("session", "foreign runtime")
	prn, prnSec := env.NewPrincipal("runtime principal")
	owned, _, err := env.Hub.AddPeer(t.Context(), "session", "principal runtime", prn)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name, secret, query string
		status              int
	}{
		{"missing bearer", "", "", 401}, {"invalid bearer", "invalid", "", 401},
		{"operator", env.OperatorSecret, "", 403},
		{"peer self", sec, "", 204}, {"peer explicit self", sec, "?as=" + own, 204},
		{"peer foreign", sec, "?as=" + foreign, 403}, {"other peer owned", otherSec, "?as=" + own, 403},
		{"principal missing target", prnSec, "", 422}, {"principal owned", prnSec, "?as=" + owned, 204},
		{"principal foreign", prnSec, "?as=" + own, 403}, {"principal unknown", prnSec, "?as=peer_unknown", 403},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := runtimeBody()
			body["state"] = "working"
			resp, raw := env.Do(tc.secret, "POST", "/v1/runtime-status"+tc.query, body)
			if resp.StatusCode != tc.status {
				t.Fatalf("got %d: %s; want %d", resp.StatusCode, raw, tc.status)
			}
		})
	}
	if got := runtimeFromPeers(t, env, "/v1/peers", foreign); got != nil {
		t.Fatalf("foreign peer was modified: %v", got)
	}
	if _, err := env.Hub.DB.Exec(`UPDATE peers SET disabled=1 WHERE peer_id=?`, owned); err != nil {
		t.Fatal(err)
	}
	resp, raw := env.Do(prnSec, "POST", "/v1/runtime-status?as="+owned, runtimeBody())
	if resp.StatusCode != 403 {
		t.Fatalf("disabled target = %d: %s", resp.StatusCode, raw)
	}
	// Disabled peers still have their last snapshot in admin reads.
	if got := runtimeFromPeers(t, env, "/v1/admin/peers", owned); got["state"] != "working" {
		t.Fatalf("disabled snapshot=%v", got)
	}
}

func TestRuntimeStatusRejectsUnsafeAndInvalidReports(t *testing.T) {
	env := testhub.Start(t)
	peer, sec := env.NewPeer("session", "validation runtime")
	tests := []struct {
		name, field string
		value       any
	}{
		{"runtime", "runtime", "other"}, {"runtime type", "runtime", 7}, {"runtime null", "runtime", nil},
		{"state", "state", "unknown"}, {"state type", "state", true}, {"delivery", "delivery", "unknown"},
		{"ttl zero", "ttl_seconds", 0}, {"ttl high", "ttl_seconds", 91}, {"ttl fractional", "ttl_seconds", 1.5},
		{"ttl string", "ttl_seconds", "60"}, {"ttl null", "ttl_seconds", nil}, {"ttl boolean", "ttl_seconds", true},
		{"reason text", "reason", "private error /home/operator/thread-123"}, {"stale reserved", "reason", "stale"},
		{"reason empty", "reason", ""}, {"reason null", "reason", nil}, {"version null", "version", nil},
		{"version empty", "version", ""}, {"version prefix", "version", "v1.2.3"}, {"version short", "version", "1.2"},
		{"version leading zero", "version", "01.2.3"}, {"version prerelease zero", "version", "1.2.3-01"},
		{"version overlong", "version", "1.2.3+" + strings.Repeat("x", 123)},
		{"private field", "thread_id", "private-thread-marker"}, {"clock field", "updated_at", "2026-01-01T00:00:00.000Z"},
		{"offline ready", "state", "offline"}, {"blocked ready", "state", "blocked"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			body := runtimeBody()
			body[tc.field] = tc.value
			resp, raw := env.Do(sec, "POST", "/v1/runtime-status", body)
			if resp.StatusCode != 400 {
				t.Fatalf("got %d: %s, want 400", resp.StatusCode, raw)
			}
			if strings.Contains(string(raw), "private") || strings.Contains(string(raw), "thread_id") || strings.Contains(string(raw), "/home/") {
				t.Fatalf("rejected content leaked: %s", raw)
			}
		})
	}
	for _, field := range []string{"runtime", "state", "delivery", "ttl_seconds"} {
		t.Run("missing "+field, func(t *testing.T) {
			body := runtimeBody()
			delete(body, field)
			resp, raw := env.Do(sec, "POST", "/v1/runtime-status", body)
			if resp.StatusCode != 400 {
				t.Fatalf("got %d: %s", resp.StatusCode, raw)
			}
		})
	}
	for _, body := range []string{"null", "[]", "{} {}", `{"runtime":"codex"`} {
		resp, raw := env.Do(sec, "POST", "/v1/runtime-status", body)
		if resp.StatusCode != 400 {
			t.Fatalf("malformed report=%d: %s", resp.StatusCode, raw)
		}
	}
	// offline also excludes unsupported; ready is restricted to idle/working.
	body := runtimeBody()
	body["state"] = "offline"
	body["delivery"] = "unsupported"
	resp, raw := env.Do(sec, "POST", "/v1/runtime-status", body)
	if resp.StatusCode != 400 {
		t.Fatalf("offline unsupported=%d: %s", resp.StatusCode, raw)
	}
	if got := runtimeFromPeers(t, env, "/v1/peers", peer); got != nil {
		t.Fatalf("rejection created status: %v", got)
	}
}

func TestRuntimeStatusAllowsBoundedReasonsVersionsAndTTLs(t *testing.T) {
	env := testhub.Start(t)
	_, sec := env.NewPeer("session", "allowed runtime")
	reasons := []string{"connecting", "unreachable", "refused_version", "stopped", "expired", "archived", "not_loaded", "idle_stale", "approval_policy", "waiting_approval", "waiting_input", "over_limit", "identity_changed", "ownership_changed", "unverified_thread", "identity_unverified", "watch_stopped", "delivery_unresolved"}
	for _, reason := range reasons {
		body := runtimeBody()
		body["state"] = "blocked"
		body["delivery"] = "unsupported"
		body["reason"] = reason
		postRuntime(t, env, sec, "", body)
	}
	for _, version := range []string{"0.0.0", "1.2.3", "1.2.3-alpha.0+001", "1.2.3+" + strings.Repeat("x", 122)} {
		body := runtimeBody()
		body["version"] = version
		postRuntime(t, env, sec, "", body)
	}
	for _, ttl := range []int{1, 90} {
		body := runtimeBody()
		body["ttl_seconds"] = ttl
		delete(body, "version")
		postRuntime(t, env, sec, "", body)
	}
	for _, state := range []string{"idle", "working", "blocked", "offline"} {
		body := runtimeBody()
		body["state"] = state
		body["delivery"] = "unavailable"
		postRuntime(t, env, sec, "", body)
	}
}

func TestRuntimeStatusSmallBodyLimit(t *testing.T) {
	env := testhub.Start(t)
	_, sec := env.NewPeer("session", "body runtime")
	body := fmt.Sprintf(`{"runtime":"codex","state":"idle","delivery":"ready","ttl_seconds":60,"version":%q}`, strings.Repeat("x", 4096))
	for _, chunked := range []bool{false, true} {
		req, err := http.NewRequest("POST", env.URL+"/v1/runtime-status", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+sec)
		if chunked {
			req.ContentLength = -1
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != 413 {
			t.Fatalf("chunked=%v got %d, want 413", chunked, resp.StatusCode)
		}
	}
}

// The engine's version refusal is a live blocked runtime with unsupported
// delivery; it must not be collapsed into offline or rejected as a combination.
func TestRuntimeStatusRefusedVersion(t *testing.T) {
	env := testhub.Start(t)
	peer, sec := env.NewPeer("session", "refused version runtime")
	body := runtimeBody()
	body["state"] = "blocked"
	body["delivery"] = "unsupported"
	body["reason"] = "refused_version"
	postRuntime(t, env, sec, "", body)
	for _, path := range []string{"/v1/peers", "/v1/admin/peers"} {
		got := runtimeFromPeers(t, env, path, peer)
		if got["state"] != "blocked" || got["delivery"] != "unsupported" || got["reason"] != "refused_version" {
			t.Fatalf("%s version refusal=%v", path, got)
		}
	}
}

// Interpose just the real SQLite query boundary; the endpoints still execute
// real auth, peer reads, JSON projection and runtime expiry. A per-peer lookup
// would increase this count as the directory grows.
type runtimeQueryConnector struct {
	driver driver.Driver
	dsn    string
	calls  *atomic.Int32
}

func (c runtimeQueryConnector) Driver() driver.Driver { return c.driver }
func (c runtimeQueryConnector) Connect(context.Context) (driver.Conn, error) {
	conn, err := c.driver.Open(c.dsn)
	if err != nil {
		return nil, err
	}
	return runtimeQueryConn{Conn: conn, calls: c.calls}, nil
}

type runtimeQueryConn struct {
	driver.Conn
	calls *atomic.Int32
}

func (c runtimeQueryConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if strings.Contains(query, "peer_runtime_status") {
		c.calls.Add(1)
	}
	return c.Conn.(driver.QueryerContext).QueryContext(ctx, query, args)
}
func TestRuntimeStatusBatchProjectionQueries(t *testing.T) {
	env := testhub.Start(t)
	peer, sec := env.NewPeer("session", "batch status")
	postRuntime(t, env, sec, "", runtimeBody())
	for i := 0; i < 12; i++ {
		env.NewPeer("session", fmt.Sprintf("batch peer %d", i))
	}
	var calls atomic.Int32
	wrapped := sql.OpenDB(runtimeQueryConnector{driver: env.Hub.DB.Driver(), dsn: filepath.Join(env.Hub.Cfg.DataDir, "hub.db"), calls: &calls})
	t.Cleanup(func() { wrapped.Close() })
	env.Hub.DB = &store.DB{DB: wrapped}
	for _, path := range []string{"/v1/peers", "/v1/admin/peers"} {
		calls.Store(0)
		if got := runtimeFromPeers(t, env, path, peer); got["state"] != "idle" {
			t.Fatalf("%s missing projection: %v", path, got)
		}
		if got := calls.Load(); got != 1 {
			t.Fatalf("%s runtime queries=%d for 14 peers, want one batch", path, got)
		}
	}
}
