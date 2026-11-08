package httpapi_test

// SOURCE DRAFT / UNRUN. All fixtures are synthetic; no production interface
// beyond the contracted future Hub.RouteStats is assumed.
import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/testhub"
)

func t2HTTPExec(t *testing.T, e *testhub.Env, q string, args ...any) {
	t.Helper()
	if _, err := e.Hub.DB.Exec(q, args...); err != nil {
		t.Fatal(err)
	}
}
func t2HTTPMap(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var v map[string]any
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal("response must be JSON")
	}
	return v
}
func t2HTTPKeys(t *testing.T, m map[string]any, keys ...string) {
	t.Helper()
	got := []string{}
	for k := range m {
		got = append(got, k)
	}
	sort.Strings(got)
	sort.Strings(keys)
	if !reflect.DeepEqual(got, keys) {
		t.Fatalf("wire keys=%v want=%v", got, keys)
	}
}
func t2HTTPNoCanary(t *testing.T, raw []byte, canaries ...string) {
	t.Helper()
	for _, c := range canaries {
		if c != "" && strings.Contains(string(raw), c) {
			t.Fatal("response leaked synthetic privacy canary")
		}
	}
}
func t2HTTPGet(t *testing.T, e *testhub.Env, secret, query string, status int, code, field string) []byte {
	t.Helper()
	resp, raw := e.Do(secret, http.MethodGet, "/v1/admin/route-stats"+query, nil)
	if resp.StatusCode != status {
		t.Fatalf("route stats HTTP status=%d want=%d", resp.StatusCode, status)
	}
	if !strings.HasPrefix(resp.Header.Get("Content-Type"), "application/json") {
		t.Fatal("route stats response must be JSON")
	}
	if code != "" {
		root := t2HTTPMap(t, raw)
		t2HTTPKeys(t, root, "error")
		errBody, ok := root["error"].(map[string]any)
		if !ok {
			t.Fatal("missing error object")
		}
		t2HTTPKeys(t, errBody, "code", "message", "details")
		if errBody["code"] != code {
			t.Fatalf("error code=%v want=%s", errBody["code"], code)
		}
		details, ok := errBody["details"].(map[string]any)
		if !ok {
			t.Fatal("error details must be object")
		}
		if field != "" {
			if details["field"] != field {
				t.Fatal("error must identify fixed field")
			}
		}
		if status == 500 && errBody["message"] != "internal" {
			t.Fatal("internal failure not sanitized")
		}
		if status == 401 && resp.Header.Get("WWW-Authenticate") == "" {
			t.Fatal("missing existing bearer challenge")
		}
	}
	return raw
}

func TestRouteStatsHTTPContractAndErrors(t *testing.T) {
	e := testhub.Start(t)
	e.Now.Advance(time.Date(2030, 1, 10, 12, 0, 0, 0, time.UTC).Sub(e.Now.Now()))
	peer, peerSecret := e.NewPeer("session", "stats-http-peer")
	_, principalSecret := e.NewPrincipal("stats-http-principal")
	route, _, err := e.Hub.AddRoute(context.Background(), "peer", peer, "https://stats-http.example:8443/private-path?private-query")
	if err != nil {
		t.Fatal(err)
	}
	sub := fmt.Sprintf("sub_%026d", 51)
	at := envelope.FormatTime(e.Now.Now())
	t2HTTPExec(t, e, "INSERT INTO event_subscriptions(sub_id,principal_kind,principal_id,peer_id,name,arguments,url,secret_enc,cursor_epoch,cursor_seq,types,created_at,updated_at) VALUES(?,'vecnet',?,?,'synthetic','{}','https://callback.example:9443/hook','synthetic-sealed',1,0,'[]',?,?)", sub, peer, peer, at, at)
	// In-storage URL may carry legacy userinfo; the reader must still return
	// only Hostname, even when today's AddRoute would reject this URL.
	t2HTTPExec(t, e, "UPDATE routes SET url=? WHERE route_id=?", "https://userinfo-canary:password-canary@stats-http.example:8443/private-path?private-query#private-fragment", route)
	t2HTTPExec(t, e, "INSERT INTO route_attempts(route_id,delivery_id,at,outcome,http_status,error_class,body_bytes,latency_ms) VALUES(?,'synthetic-dly',?,'fail',NULL,'request_failed',0,2)", route, at)
	canaries := []string{"userinfo-canary", "password-canary", ":8443", "private-path", "private-query", "private-fragment", "synthetic-sealed", "synthetic-dly"}
	for _, tc := range []struct{ id, kind string }{{peer, "peer"}, {route, "route"}, {sub, "subscription"}} {
		t.Run(tc.kind, func(t *testing.T) {
			raw := t2HTTPGet(t, e, e.OperatorSecret, "?id="+url.QueryEscape(tc.id), 200, "", "")
			t2HTTPNoCanary(t, raw, canaries...)
			got := t2HTTPMap(t, raw)
			t2HTTPKeys(t, got, "subject", "window", "retained_deliveries", "routes")
			subject := got["subject"].(map[string]any)
			if subject["kind"] != tc.kind || subject["id"] != tc.id {
				t.Fatal("wrong selector subject")
			}
			window := got["window"].(map[string]any)
			t2HTTPKeys(t, window, "since", "until", "max_rows", "max_age_hours")
			if window["since"] != envelope.FormatTime(e.Now.Now().Add(-168*time.Hour)) || window["until"] != at || window["max_rows"] != float64(1000) || window["max_age_hours"] != float64(168) {
				t.Fatal("wrong frozen sample window")
			}
			retained := got["retained_deliveries"].(map[string]any)
			t2HTTPKeys(t, retained, "peer_ids", "total", "with_push_evidence", "accepted_without_push_evidence")
			if _, ok := retained["peer_ids"].([]any); !ok {
				t.Fatal("peer_ids must be []")
			}
			caller, err := e.Hub.Auth.Resolve(context.Background(), e.OperatorSecret)
			if err != nil {
				t.Fatal(err)
			}
			service, err := e.Hub.RouteStats(context.Background(), caller, tc.id)
			if err != nil {
				t.Fatal(err)
			}
			encoded, err := json.Marshal(service)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, t2HTTPMap(t, encoded)) {
				t.Fatal("HTTP changed shared reader DTO")
			}
			rows, ok := got["routes"].([]any)
			if !ok || len(rows) == 0 {
				t.Fatal("fixture route omitted")
			}
			for _, value := range rows {
				row := value.(map[string]any)
				t2HTTPKeys(t, row, "route_id", "kind", "host", "owner_kind", "owner_id", "attempt_scope", "requests", "ok", "fail", "refused", "distinct_ids", "last_used_at", "last_success_at", "last_error", "last_error_at", "last_status", "body_bytes", "latency_ms")
				if row["attempt_scope"] != "route" || row["owner_kind"] != "peer" || row["owner_id"] != peer {
					t.Fatal("wrong owner/scope metadata")
				}
				for _, k := range []string{"body_bytes", "latency_ms"} {
					dist := row[k].(map[string]any)
					t2HTTPKeys(t, dist, "avg", "p95", "p99", "samples")
				}
				if row["route_id"] == route {
					if row["host"] != "stats-http.example" || row["requests"] != float64(1) || row["fail"] != float64(1) || row["last_error"] != "request_failed" || row["last_status"] != nil || row["last_success_at"] != nil {
						t.Fatal("failure/null DTO semantics changed")
					}
				}
				if row["route_id"] == sub {
					for _, k := range []string{"last_used_at", "last_success_at", "last_error", "last_error_at", "last_status"} {
						if row[k] != nil {
							t.Fatal("empty sample last fields must be null")
						}
					}
					for _, k := range []string{"body_bytes", "latency_ms"} {
						dist := row[k].(map[string]any)
						if dist["avg"] != nil || dist["p95"] != nil || dist["p99"] != nil || dist["samples"] != float64(0) {
							t.Fatal("empty distribution must contain nulls and zero samples")
						}
					}
				}
			}
		})
	}
	empty, _ := e.NewPeer("session", "empty-http")
	emptyRaw := t2HTTPGet(t, e, e.OperatorSecret, "?id="+url.QueryEscape(empty), 200, "", "")
	if rows, ok := t2HTTPMap(t, emptyRaw)["routes"].([]any); !ok || len(rows) != 0 {
		t.Fatal("empty route list must be []")
	}
	rejected := "rejected-selector-canary"
	for _, tc := range []struct{ query, field string }{
		{"", "id"}, {"?id=", "id"}, {"?id=" + peer + "&id=" + peer, "id"}, {"?id=" + peer + "&id=", "id"},
		{"?id=" + peer + "&limit=1", "query"}, {"?id=" + peer + "&as=" + peer, "query"},
		{"?id=" + peer + "&since=" + at, "query"}, {"?id=" + peer + "&until=" + at, "query"},
		{"?id=" + peer + "&window=168", "query"}, {"?id=" + peer + "&group=route", "query"},
		{"?unexpected-query-canary=1", "query"},
	} {
		raw := t2HTTPGet(t, e, e.OperatorSecret, tc.query, 422, "invalid_value", tc.field)
		t2HTTPNoCanary(t, raw, "unexpected-query-canary", peer)
	}
	for _, id := range []string{
		rejected, strings.Repeat("x", 81), " " + peer, peer + " ", peer + "," + route,
		"self", "default", "prn_00000000000000000000000001",
		"peer_80000000000000000000000000", "route_0000000000000000000000000I",
		"sub_0000000000000000000000000a", "peer_0000000000000000000000000",
	} {
		raw := t2HTTPGet(t, e, e.OperatorSecret, "?id="+url.QueryEscape(id), 422, "invalid_value", "id")
		t2HTTPNoCanary(t, raw, id)
	}
	for _, prefix := range []string{"peer", "route", "sub"} {
		id := fmt.Sprintf("%s_%026d", prefix, 999)
		raw := t2HTTPGet(t, e, e.OperatorSecret, "?id="+id, 404, "not_found", "")
		t2HTTPNoCanary(t, raw, id)
	}
	t2HTTPGet(t, e, "", "?id="+peer, 401, "unauthenticated", "")
	t2HTTPGet(t, e, "synthetic-invalid-bearer", "?id="+peer, 401, "unauthenticated", "")
	for _, secret := range []string{peerSecret, principalSecret} {
		for _, id := range []string{peer, rejected, fmt.Sprintf("route_%026d", 999)} {
			raw := t2HTTPGet(t, e, secret, "?id="+url.QueryEscape(id), 403, "forbidden", "")
			t2HTTPNoCanary(t, raw, rejected)
		}
	}
	t2HTTPExec(t, e, "UPDATE peers SET kind='operator' WHERE peer_id=?", peer)
	t2HTTPGet(t, e, peerSecret, "?id="+peer, 403, "forbidden", "")
	t2HTTPExec(t, e, "UPDATE peers SET disabled=1 WHERE peer_id=?", e.OperatorPeer)
	t2HTTPGet(t, e, e.OperatorSecret, "?id="+peer, 200, "", "")
	// Existing mux behavior: GET registration also serves HEAD, and unrelated
	// methods get 405. Neither response may create statistics.
	resp, raw := e.Do(e.OperatorSecret, http.MethodHead, "/v1/admin/route-stats?id="+peer, nil)
	if resp.StatusCode != 200 || len(raw) != 0 {
		t.Fatal("normal HEAD behavior changed")
	}
	resp, _ = e.Do(e.OperatorSecret, http.MethodPost, "/v1/admin/route-stats?id="+peer, nil)
	if resp.StatusCode != 405 {
		t.Fatal("route stats accepted a write method")
	}
	// Existing DB seam gives a deterministic query failure after authentication
	// and subject resolution; no new handler or production failpoint is needed.
	t2HTTPExec(t, e, "ALTER TABLE route_attempts RENAME TO route_attempts_fixture_hidden")
	raw = t2HTTPGet(t, e, e.OperatorSecret, "?id="+peer, 500, "internal", "")
	t2HTTPNoCanary(t, raw, append(canaries, "route_attempts_fixture_hidden", "SELECT", "no such table")...)
	if e.Hub.DB.Stats().InUse != 0 {
		t.Fatal("HTTP read failure leaked connection")
	}
	t2HTTPExec(t, e, "ALTER TABLE route_attempts_fixture_hidden RENAME TO route_attempts")
	t2HTTPGet(t, e, e.OperatorSecret, "?id="+peer, 200, "", "")
}
