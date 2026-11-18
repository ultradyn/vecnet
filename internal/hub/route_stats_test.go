package hub_test

// Synthetic fixtures exercise shared route statistics, retained evidence and
// operator-only snapshot behavior.
import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/auth"
	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/hub"
	"github.com/XertroV/vecnet/internal/opapi"
	"github.com/XertroV/vecnet/internal/testhub"
)

func t2ID(prefix string, n int) string { return fmt.Sprintf("%s_%026d", prefix, n) }

func t2Env(t *testing.T) *testhub.Env {
	t.Helper()
	e := testhub.Start(t)
	e.Now.Advance(time.Date(2030, 1, 10, 12, 0, 0, 0, time.UTC).Sub(e.Now.Now()))
	return e
}

func t2Exec(t *testing.T, e *testhub.Env, q string, args ...any) {
	t.Helper()
	if _, err := e.Hub.DB.Exec(q, args...); err != nil {
		t.Fatalf("synthetic fixture SQL failed: %v", err)
	}
}

// t2Seed commits a raw reader fixture as one transaction. It preserves
// insert order and triggers; production writes and all reader assertions stay
// outside the setup transaction.
func t2Seed(t *testing.T, e *testhub.Env, seed func(*sql.Tx)) {
	t.Helper()
	tx, err := e.Hub.DB.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	seed(tx)
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

type t2FixtureWriter interface {
	Exec(string, ...any) (sql.Result, error)
}

func t2Caller(t *testing.T, e *testhub.Env, secret string) auth.Caller {
	t.Helper()
	c, err := e.Hub.Auth.Resolve(context.Background(), secret)
	if err != nil {
		t.Fatal("synthetic credential resolution failed")
	}
	return c
}

func t2Route(t *testing.T, e *testhub.Env, id, ownerKind, owner, kind, rawURL string) {
	t.Helper()
	t2Exec(t, e, "INSERT INTO routes(route_id,owner_kind,owner_id,kind,url,created_at) VALUES(?,?,?,?,?,?)",
		id, ownerKind, owner, kind, rawURL, envelope.FormatTime(e.Now.Now()))
}

func t2Sub(t *testing.T, e *testhub.Env, id, peer string) {
	t.Helper()
	at := envelope.FormatTime(e.Now.Now())
	t2Exec(t, e, "INSERT INTO event_subscriptions(sub_id,principal_kind,principal_id,peer_id,name,arguments,url,secret_enc,cursor_epoch,cursor_seq,types,created_at,updated_at,expires_at,paused_reason) VALUES(?,'vecnet',?,?,'synthetic','{}',?,'synthetic-sealed',1,0,'[]',?,?,?,'exhausted')",
		id, peer, peer, "https://callback.example/"+id, at, at, envelope.FormatTime(e.Now.Now().Add(-time.Hour)))
}

// Raw inserts deliberately retain stale/future/over-cap rows. Reader tests
// cannot rely on the insert/prune writer to pre-correct their populations.
func t2Attempt(t *testing.T, e *testhub.Env, route, delivery string, at time.Time, outcome string, status, class any, body, latency int) {
	t.Helper()
	t2AttemptOn(t, e.Hub.DB, route, delivery, at, outcome, status, class, body, latency)
}

func t2AttemptOn(t *testing.T, q t2FixtureWriter, route, delivery string, at time.Time, outcome string, status, class any, body, latency int) {
	t.Helper()
	_, err := q.Exec("INSERT INTO route_attempts(route_id,delivery_id,at,outcome,http_status,error_class,body_bytes,latency_ms) VALUES(?,?,?,?,?,?,?,?)",
		route, delivery, envelope.FormatTime(at), outcome, status, class, body, latency)
	t2Must(t, err)
}

func t2JSON(t *testing.T, v any) map[string]any {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func t2Stats(t *testing.T, e *testhub.Env, id string) map[string]any {
	t.Helper()
	got, err := e.Hub.RouteStats(context.Background(), t2Caller(t, e, e.OperatorSecret), id)
	if err != nil {
		t.Fatalf("RouteStats returned an error for synthetic subject: %v", err)
	}
	m := t2JSON(t, got)
	t2Keys(t, m, "subject", "window", "retained_deliveries", "routes")
	t2Equal(t, m["window"], map[string]any{
		"since": envelope.FormatTime(e.Now.Now().Add(-168 * time.Hour)),
		"until": envelope.FormatTime(e.Now.Now()), "max_rows": 1000, "max_age_hours": 168,
	})
	if _, ok := m["routes"].([]any); !ok {
		t.Fatal("routes must be a nonnull array")
	}
	return m
}

func t2Keys(t *testing.T, m map[string]any, names ...string) {
	t.Helper()
	got := make([]string, 0, len(m))
	for k := range m {
		got = append(got, k)
	}
	sort.Strings(got)
	sort.Strings(names)
	if !reflect.DeepEqual(got, names) {
		t.Fatalf("wire keys=%v want %v", got, names)
	}
}

func t2Equal(t *testing.T, got, want any) {
	t.Helper()
	// Normalize integer literals and decoded JSON numbers in both directions.
	a, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	var x, y any
	if json.Unmarshal(a, &x) != nil || json.Unmarshal(b, &y) != nil {
		t.Fatal("invalid oracle JSON")
	}
	if !reflect.DeepEqual(x, y) {
		t.Fatalf("semantic JSON mismatch:\ngot %s\nwant %s", a, b)
	}
}

func t2Rows(t *testing.T, m map[string]any) []map[string]any {
	t.Helper()
	raw, ok := m["routes"].([]any)
	if !ok {
		t.Fatal("routes must be []")
	}
	rows := make([]map[string]any, 0, len(raw))
	for _, v := range raw {
		row, ok := v.(map[string]any)
		if !ok {
			t.Fatal("route must be object")
		}
		t2Keys(t, row, "route_id", "kind", "host", "owner_kind", "owner_id", "attempt_scope",
			"requests", "coalesced", "ok", "fail", "refused", "distinct_ids", "last_used_at",
			"last_success_at", "last_error", "last_error_at", "last_status", "body_bytes", "latency_ms")
		requests, ok := row["requests"].(float64)
		if !ok || requests < 0 || requests > 1000 {
			t.Fatal("invalid sample request count")
		}
		sum := float64(0)
		for _, key := range []string{"ok", "fail", "refused"} {
			n, ok := row[key].(float64)
			if !ok || n < 0 || n != float64(int(n)) {
				t.Fatal("count must be a nonnegative integer")
			}
			sum += n
		}
		distinct, ok := row["distinct_ids"].(float64)
		if !ok || distinct < 0 || distinct > requests || distinct != float64(int(distinct)) || sum != requests {
			t.Fatal("sample population invariant failed")
		}
		for _, key := range []string{"body_bytes", "latency_ms"} {
			dist, ok := row[key].(map[string]any)
			if !ok {
				t.Fatal("distribution must be an object")
			}
			t2Keys(t, dist, "avg", "p95", "p99", "samples")
			if dist["samples"] != requests {
				t.Fatal("distribution used a different population")
			}
		}
		rows = append(rows, row)
	}
	return rows
}

func t2One(t *testing.T, m map[string]any) map[string]any {
	t.Helper()
	rows := t2Rows(t, m)
	if len(rows) != 1 {
		t.Fatalf("route count=%d want 1", len(rows))
	}
	return rows[0]
}

func t2Dist(avg, p95, p99 any, n int) map[string]any {
	return map[string]any{"avg": avg, "p95": p95, "p99": p99, "samples": n}
}

func t2Empty(t *testing.T, row map[string]any) {
	t.Helper()
	for _, key := range []string{"requests", "coalesced", "ok", "fail", "refused", "distinct_ids"} {
		t2Equal(t, row[key], 0)
	}
	for _, key := range []string{"last_used_at", "last_success_at", "last_error", "last_error_at", "last_status"} {
		if value, exists := row[key]; !exists || value != nil {
			t.Fatalf("%s must be explicit null", key)
		}
	}
	t2Equal(t, row["body_bytes"], t2Dist(nil, nil, nil, 0))
	t2Equal(t, row["latency_ms"], t2Dist(nil, nil, nil, 0))
}

func t2Failure(t *testing.T, e *testhub.Env, c auth.Caller, id string, status int, code string) {
	t.Helper()
	got, err := e.Hub.RouteStats(context.Background(), c, id)
	var api *hub.APIError
	if !errors.As(err, &api) || api.Status != status || api.Code != code {
		t.Fatalf("expected sanitized %d/%s failure", status, code)
	}
	if !reflect.ValueOf(got).IsZero() {
		t.Fatal("failure returned a partial report")
	}
	if code == "invalid_value" && api.Details["field"] != "id" {
		t.Fatal("invalid selector must identify fixed field id")
	}
	if strings.Contains(api.Message, id) && id != "" {
		t.Fatal("failure echoed rejected selector")
	}
}

func TestRouteStatsPercentilesNullsAndLastFields(t *testing.T) {
	e := t2Env(t)
	peer, _ := e.NewPeer("session", "distribution")
	route := t2ID("route", 1)
	t2Route(t, e, route, "peer", peer, "http", "https://stats.example:8443/hook")
	now := e.Now.Now()
	t2Seed(t, e, func(tx *sql.Tx) {
		for i := 1; i <= 300; i++ {
			t2AttemptOn(t, tx, route, t2ID("dly", i), now.Add(-time.Minute), "ok", 200, nil, i, i)
		}
	})
	row := t2One(t, t2Stats(t, e, route))
	t2Equal(t, row["requests"], 300)
	t2Equal(t, row["ok"], 300)
	t2Equal(t, row["distinct_ids"], 300)
	for _, key := range []string{"body_bytes", "latency_ms"} {
		t2Equal(t, row[key], t2Dist(150.5, 285, 297, 300))
	}
	t2Equal(t, row["host"], "stats.example")
	t2Equal(t, row["attempt_scope"], "route")
	t2Equal(t, row["last_status"], 200)

	t2Exec(t, e, "DELETE FROM route_attempts WHERE route_id=?", route)
	t2Empty(t, t2One(t, t2Stats(t, e, route)))
	t2Attempt(t, e, route, "e1.1", now, "ok", 204, nil, 0, 0)
	row = t2One(t, t2Stats(t, e, route))
	for _, key := range []string{"body_bytes", "latency_ms"} {
		t2Equal(t, row[key], t2Dist(0, 0, 0, 1))
	}
	// Same completion timestamp: inserted ID, not status or delivery ID,
	// determines which failure is newest. A subsequent success preserves it.
	t2Attempt(t, e, route, "e1.2", now, "fail", 503, "http_5xx", 1, 1)
	t2Attempt(t, e, route, "e1.3", now, "fail", nil, "timeout", 2, 2)
	row = t2One(t, t2Stats(t, e, route))
	t2Equal(t, row["last_status"], nil)
	t2Equal(t, row["last_error"], "timeout")
	t2Equal(t, row["last_error_at"], envelope.FormatTime(now))
	t2Attempt(t, e, route, "e1.4", now, "ok", 202, nil, 3, 3)
	row = t2One(t, t2Stats(t, e, route))
	t2Equal(t, row["last_status"], 202)
	t2Equal(t, row["last_success_at"], envelope.FormatTime(now))
	t2Equal(t, row["last_used_at"], envelope.FormatTime(now))
	t2Equal(t, row["last_error"], "timeout")
	t2Equal(t, row["body_bytes"], t2Dist(1.5, 3, 3, 4))
	e.Now.Advance(time.Millisecond)
	now = e.Now.Now()
	t2Attempt(t, e, route, "newer-success", now, "ok", 204, nil, 4, 4)
	row = t2One(t, t2Stats(t, e, route))
	t2Equal(t, row["last_error"], "timeout")
	t2Equal(t, row["last_error_at"], envelope.FormatTime(now.Add(-time.Millisecond)))
	t2Equal(t, row["last_success_at"], envelope.FormatTime(now))
	// Round3, rather than truncation or an interpolated percentile.
	t2Exec(t, e, "DELETE FROM route_attempts WHERE route_id=?", route)
	for i, n := range []int{0, 0, 1} {
		t2Attempt(t, e, route, fmt.Sprint(i), now, "ok", 200, nil, n, n)
	}
	t2Equal(t, t2One(t, t2Stats(t, e, route))["latency_ms"], t2Dist(0.333, 1, 1, 3))

	t2Exec(t, e, "DELETE FROM route_attempts WHERE route_id=?", route)
	// A success expires while a later failure stays selected: last_success
	// cannot be read from the uncapped or out-of-window history.
	agedSuccess := now.Add(-168 * time.Hour)
	retainedFailure := now.Add(-time.Hour)
	t2Attempt(t, e, route, "aging-ok", agedSuccess, "ok", 200, nil, 8, 8)
	t2Attempt(t, e, route, "aging-fail", retainedFailure, "fail", 503, "http_5xx", 9, 9)
	row = t2One(t, t2Stats(t, e, route))
	t2Equal(t, row["requests"], 2)
	t2Equal(t, row["last_success_at"], envelope.FormatTime(agedSuccess))
	e.Now.Advance(time.Millisecond)
	now = e.Now.Now()
	row = t2One(t, t2Stats(t, e, route))
	for key, want := range map[string]any{
		"requests": 1, "ok": 0, "fail": 1, "refused": 0, "distinct_ids": 1,
		"last_success_at": nil, "last_used_at": envelope.FormatTime(retainedFailure),
		"last_error_at": envelope.FormatTime(retainedFailure), "last_error": "http_5xx", "last_status": 503,
	} {
		t2Equal(t, row[key], want)
	}
	t2Exec(t, e, "DELETE FROM route_attempts WHERE route_id=?", route)
	// Cutoff equality is included; an out-of-window failure cannot supply
	// last_error. Future rows must be excluded BEFORE applying the cap.
	cutoff := now.Add(-168 * time.Hour)
	t2Attempt(t, e, route, "old", cutoff.Add(-time.Millisecond), "fail", 503, "http_5xx", 999, 999)
	t2Attempt(t, e, route, "boundary", cutoff, "ok", 200, nil, 7, 7)
	t2Equal(t, t2One(t, t2Stats(t, e, route))["requests"], 1)
	t2Seed(t, e, func(tx *sql.Tx) {
		for i := 0; i < 1005; i++ {
			t2AttemptOn(t, tx, route, fmt.Sprint("cap-", i), now, "ok", 201, nil, 2, 2)
		}
	})
	t2Seed(t, e, func(tx *sql.Tx) {
		for i := 0; i < 1001; i++ {
			t2AttemptOn(t, tx, route, fmt.Sprint("future-", i), now.Add(time.Millisecond), "fail", 500, "http_5xx", 900, 900)
		}
	})
	row = t2One(t, t2Stats(t, e, route))
	for key, want := range map[string]any{"requests": 1000, "ok": 1000, "fail": 0, "distinct_ids": 1000, "last_error": nil, "last_status": 201} {
		t2Equal(t, row[key], want)
	}
	t2Equal(t, row["body_bytes"], t2Dist(2, 2, 2, 1000))
	// A second saturated route has its own cap; a peer report must not share
	// a global 1000-row budget between the two routes.
	second := t2ID("route", 2)
	t2Route(t, e, second, "peer", peer, "http", "https://second.example")
	t2Seed(t, e, func(tx *sql.Tx) {
		for i := 0; i < 1001; i++ {
			t2AttemptOn(t, tx, second, fmt.Sprint("second-", i), now, "ok", 200, nil, 4, 4)
		}
	})
	rows := t2Rows(t, t2Stats(t, e, peer))
	if len(rows) != 2 {
		t.Fatal("peer lost a saturated route")
	}
	for _, r := range rows {
		t2Equal(t, r["requests"], 1000)
		want := 2
		if r["route_id"] == second {
			want = 4
		}
		t2Equal(t, r["body_bytes"], t2Dist(want, want, want, 1000))
	}
}

func TestRouteStatsSelectorsAndSharedPrincipalScope(t *testing.T) {
	e := t2Env(t)
	a, _ := e.NewPeer("session", "shared-a")
	b, _ := e.NewPeer("session", "shared-b")
	prn, _ := e.NewPrincipal("shared-owner")
	t2Exec(t, e, "UPDATE peers SET prn_id=? WHERE peer_id IN (?,?)", prn, a, b)
	r1, r2, sub := t2ID("route", 11), t2ID("route", 12), t2ID("sub", 11)
	t2Route(t, e, r1, "principal", prn, "http", "https://shared.example:443/old")
	t2Route(t, e, r2, "peer", a, "grokbot", "https://routine.example/run/private")
	t2Sub(t, e, sub, a)
	t2Exec(t, e, "UPDATE peers SET route_id=? WHERE peer_id IN (?,?)", r1, a, b)
	for _, d := range []string{"dly-a", "dly-b", "dly-b"} {
		t2Attempt(t, e, r1, d, e.Now.Now(), "ok", 200, nil, 3, 4)
	}
	for _, p := range []string{a, b} {
		m := t2Stats(t, e, p)
		t2Equal(t, m["subject"], map[string]any{"kind": "peer", "id": p})
		rows := t2Rows(t, m)
		want := []string{r1}
		if p == a {
			want = []string{r1, r2, sub}
		}
		ids := []string{}
		for _, row := range rows {
			ids = append(ids, row["route_id"].(string))
			if row["route_id"] == r1 {
				t2Equal(t, row["requests"], 3)
				t2Equal(t, row["distinct_ids"], 2)
				t2Equal(t, row["owner_kind"], "principal")
				t2Equal(t, row["owner_id"], prn)
			}
		}
		sort.Strings(want)
		t2Equal(t, ids, want)
	}
	t2Equal(t, t2Stats(t, e, r1)["subject"], map[string]any{"kind": "route", "id": r1})
	sm := t2Stats(t, e, sub)
	t2Equal(t, sm["subject"], map[string]any{"kind": "subscription", "id": sub})
	sr := t2One(t, sm)
	for key, want := range map[string]any{"kind": "events", "owner_kind": "peer", "owner_id": a, "host": "callback.example"} {
		t2Equal(t, sr[key], want)
	}
	// Current ownership and host change; observations stay attached to r1.
	t2Exec(t, e, "UPDATE routes SET owner_kind='peer',owner_id=?,url='https://moved.example:9443/new' WHERE route_id=?", b, r1)
	row := t2One(t, t2Stats(t, e, r1))
	t2Equal(t, row["owner_kind"], "peer")
	t2Equal(t, row["owner_id"], b)
	t2Equal(t, row["host"], "moved.example")
	t2Equal(t, row["requests"], 3)
	// A peer move removes its former principal route from the peer report.
	t2Exec(t, e, "UPDATE routes SET owner_kind='principal',owner_id=? WHERE route_id=?", prn, r1)
	newPrn, _ := e.NewPrincipal("new-owner")
	t2Exec(t, e, "UPDATE peers SET prn_id=?,route_id=NULL,disabled=1,archived_at=? WHERE peer_id=?", newPrn, envelope.FormatTime(e.Now.Now()), b)
	t2Equal(t, t2Stats(t, e, b)["routes"], []any{})
	// Existing route/sub lookups accept the maximum valid ULID and uppercase
	// Crockford letters; strict validation must not become digits-only.
	maxRoute := "route_7ZZZZZZZZZZZZZZZZZZZZZZZZZ"
	t2Route(t, e, maxRoute, "peer", a, "http", "https://max.example")
	t2Equal(t, t2Stats(t, e, maxRoute)["subject"], map[string]any{"kind": "route", "id": maxRoute})
	t2Exec(t, e, "DELETE FROM routes WHERE route_id=?", maxRoute)
	t2Exec(t, e, "DELETE FROM routes WHERE route_id=?", r1)
	op := t2Caller(t, e, e.OperatorSecret)
	t2Failure(t, e, op, r1, 404, "not_found")
	for _, row := range t2Rows(t, t2Stats(t, e, a)) {
		if row["route_id"] == r1 {
			t.Fatal("orphan observations synthesized deleted route")
		}
	}
	for _, prefix := range []string{"peer", "route", "sub"} {
		t2Failure(t, e, op, t2ID(prefix, 999), 404, "not_found")
	}
	for _, bad := range []string{
		"", "self", "default", "shared-a", prn, " " + a, a + " ", a + "," + b,
		"peer_0000000000000000000000000", "peer_000000000000000000000000000",
		"peer_80000000000000000000000000", "route_Z0000000000000000000000000",
		"sub_0000000000000000000000000I", "route_0000000000000000000000000a", strings.Repeat("x", 81),
	} {
		t2Failure(t, e, op, bad, 422, "invalid_value")
	}
}

func t2Delivery(t *testing.T, e *testhub.Env, id, recipient, route, state string, attempt int, at time.Time) {
	t.Helper()
	t2DeliveryOn(t, e.Hub.DB, id, recipient, route, state, attempt, at)
}

func t2DeliveryOn(t *testing.T, q t2FixtureWriter, id, recipient, route, state string, attempt int, at time.Time) {
	t.Helper()
	// No message body is necessary for this read population. Real Send and
	// retention fixtures below independently exercise retained body deletion.
	stamp := envelope.FormatTime(at)
	_, err := q.Exec("INSERT INTO deliveries(dly_id,msg_id,thr_id,recipient,priority,state,attempt,attempts_left,route_id,expires_at,created_at,updated_at,body_sha256) VALUES(?,?,?,?,'wake',?,?,3,?,?,?,?,?)",
		id, t2ID("msg", 80), t2ID("thr", 80), recipient, state, attempt, route, envelope.FormatTime(at.Add(24*time.Hour)), stamp, stamp, strings.Repeat("a", 64))
	t2Must(t, err)
}

func TestRouteStatsDistinctIDsAndRetainedDeliveryEvidence(t *testing.T) {
	e := t2Env(t)
	a, _ := e.NewPeer("session", "evidence-a")
	b, _ := e.NewPeer("session", "evidence-b")
	outsider, _ := e.NewPeer("session", "outside")
	prn, _ := e.NewPrincipal("evidence-owner")
	r, s1, s2 := t2ID("route", 21), t2ID("sub", 21), t2ID("sub", 22)
	t2Route(t, e, r, "principal", prn, "http", "https://evidence.example")
	t2Exec(t, e, "UPDATE peers SET route_id=? WHERE peer_id IN (?,?)", r, a, b)
	t2Sub(t, e, s1, a)
	t2Sub(t, e, s2, a)
	now := e.Now.Now()
	// Repeated actual requests across three handles; one retained delivery.
	for _, route := range []string{r, s1, s2} {
		t2Attempt(t, e, route, "ack-after-fail", now, "fail", 503, "http_5xx", 3, 1)
		t2Attempt(t, e, route, "ack-after-fail", now, "ok", 200, nil, 3, 2)
	}
	t2Attempt(t, e, s2, "e1.101", now, "ok", 204, nil, 4, 1)
	t2Delivery(t, e, "ack-after-fail", a, r, "accepted", 0, now)
	t2Delivery(t, e, "claim-only", a, r, "accepted", 1, now)
	t2Delivery(t, e, "accepted-no-evidence", a, r, "accepted", 0, now)
	t2Delivery(t, e, "pending-no-evidence", a, r, "queued", 0, now)
	t2Delivery(t, e, "other-peer", b, r, "accepted", 0, now)
	t2Delivery(t, e, "outside", outsider, r, "accepted", 9, now)
	t2Delivery(t, e, "too-old", a, r, "accepted", 9, now.Add(-168*time.Hour-time.Millisecond))
	t2Delivery(t, e, "future", a, r, "accepted", 9, now.Add(time.Millisecond))
	// Stored in-window evidence outside the SELECTED cap is still evidence.
	// This accepted delivery has exactly one observation, only on r, older
	// than every cap-filling row. It cannot gain evidence via s1 or s2.
	t2Delivery(t, e, "outside-all-selected", a, r, "accepted", 0, now)
	t2Attempt(t, e, r, "outside-all-selected", now.Add(-time.Millisecond), "ok", 200, nil, 9, 9)
	t2Seed(t, e, func(tx *sql.Tx) {
		for i := 0; i < 1001; i++ {
			t2AttemptOn(t, tx, r, "retry-only", now, "ok", 200, nil, 1, 1)
		}
	})
	t2Delivery(t, e, "retry-only", a, r, "accepted", 0, now)
	wantA := map[string]any{"peer_ids": []string{a}, "total": 6, "with_push_evidence": 4, "accepted_without_push_evidence": 1}
	t2Equal(t, t2Stats(t, e, a)["retained_deliveries"], wantA)
	row := t2One(t, t2Stats(t, e, r))
	t2Equal(t, row["requests"], 1000)
	t2Equal(t, row["distinct_ids"], 1)
	t2Equal(t, row["body_bytes"], t2Dist(1, 1, 1, 1000))
	if count(t, e, "SELECT count(*) FROM route_attempts WHERE delivery_id=?", "outside-all-selected") != 1 {
		t.Fatal("uncapped evidence fixture must have exactly one stored observation")
	}
	for _, pair := range []struct {
		id                 string
		requests, distinct int
	}{{s1, 2, 1}, {s2, 3, 2}} {
		row := t2One(t, t2Stats(t, e, pair.id))
		t2Equal(t, row["requests"], pair.requests)
		t2Equal(t, row["distinct_ids"], pair.distinct)
		t2Equal(t, t2Stats(t, e, pair.id)["retained_deliveries"], wantA)
	}
	peers := []string{a, b}
	sort.Strings(peers)
	t2Equal(t, t2Stats(t, e, r)["retained_deliveries"], map[string]any{
		"peer_ids": peers, "total": 7, "with_push_evidence": 4, "accepted_without_push_evidence": 2,
	})
	// A peer-owned route includes both its owner and every current member;
	// owner a is already a member, so it must occur only once.
	t2Exec(t, e, "UPDATE routes SET owner_kind='peer',owner_id=? WHERE route_id=?", a, r)
	t2Equal(t, t2Stats(t, e, r)["retained_deliveries"], map[string]any{
		"peer_ids": peers, "total": 7, "with_push_evidence": 4, "accepted_without_push_evidence": 2,
	})
	// Restore principal ownership for the existing empty-membership oracle.
	t2Exec(t, e, "UPDATE routes SET owner_kind='principal',owner_id=? WHERE route_id=?", prn, r)
	// No 1000-row cap applies to delivery context.
	t2Seed(t, e, func(tx *sql.Tx) {
		for i := 0; i < 1001; i++ {
			t2DeliveryOn(t, tx, fmt.Sprint("context-", i), b, r, "queued", 0, now)
		}
	})
	t2Equal(t, t2Stats(t, e, r)["retained_deliveries"].(map[string]any)["total"], 1008)
	t2Exec(t, e, "UPDATE peers SET route_id=NULL WHERE route_id=?", r)
	t2Equal(t, t2Stats(t, e, r)["retained_deliveries"], map[string]any{"peer_ids": []any{}, "total": 0, "with_push_evidence": 0, "accepted_without_push_evidence": 0})
	// A peer-owned route includes its owner even if no current membership.
	t2Exec(t, e, "UPDATE routes SET owner_kind='peer',owner_id=? WHERE route_id=?", a, r)
	t2Equal(t, t2Stats(t, e, r)["retained_deliveries"], wantA)
	// Inclusive lower/upper delivery time bounds, independent of attempts.
	t2Delivery(t, e, "cutoff-equality", a, r, "accepted", 0, now.Add(-168*time.Hour))
	t2Equal(t, t2Stats(t, e, r)["retained_deliveries"].(map[string]any)["total"], 7)
	// In-window retained rows with only stale/future telemetry have NO
	// in-window evidence. Updated_at is independent of observation completion.
	t2Delivery(t, e, "stale-only", a, r, "accepted", 0, now)
	t2Delivery(t, e, "future-only", a, r, "accepted", 0, now)
	t2Attempt(t, e, s1, "stale-only", now.Add(-168*time.Hour-time.Millisecond), "ok", 200, nil, 1, 1)
	t2Attempt(t, e, s1, "future-only", now.Add(time.Millisecond), "ok", 200, nil, 1, 1)
	t2Equal(t, t2Stats(t, e, r)["retained_deliveries"], map[string]any{
		"peer_ids": []string{a}, "total": 9, "with_push_evidence": 4, "accepted_without_push_evidence": 4,
	})
}

func TestRouteStatsHistoryRetentionAndIdleRows(t *testing.T) {
	// Existing authorized history fixture uses the real preference confirmation,
	// mark/not_before/sweep path. No new retention policy or direct GC delete.
	e := historyEnv(t, nil)
	_, sender := historyPeer(t, e, "a")
	recipient, _ := historyPeer(t, e, "b")
	sent := historySend(t, e, sender, "route-stats-history", "", recipient)
	historySettle(t, e, sent.MessageID)
	e.Now.Advance(10 * 24 * time.Hour)
	// An ack can move an old retained delivery into the updated_at cohort.
	t2Exec(t, e, "UPDATE deliveries SET updated_at=? WHERE msg_id=?", envelope.FormatTime(e.Now.Now()), sent.MessageID)
	r := t2ID("route", 31)
	t2Route(t, e, r, "peer", recipient, "http", "https://history.example")
	t2Attempt(t, e, r, sent.Deliveries[0].DeliveryID, e.Now.Now(), "ok", 200, nil, 10, 3)
	t2Equal(t, t2Stats(t, e, r)["retained_deliveries"].(map[string]any)["total"], 1)
	historyPolicy(t, e, 86400, true)
	historyNight(t, e)
	historySweep(t, e)
	historySweep(t, e)
	if count(t, e, "SELECT count(*) FROM deliveries WHERE msg_id=?", sent.MessageID) != 0 {
		t.Fatal("real history sweep did not remove eligible delivery")
	}
	m := t2Stats(t, e, r)
	t2Equal(t, m["retained_deliveries"].(map[string]any)["total"], 0)
	t2Equal(t, t2One(t, m)["requests"], 1)
	e.Now.Advance(168 * time.Hour)
	t2Empty(t, t2One(t, t2Stats(t, e, r)))
	if count(t, e, "SELECT count(*) FROM route_attempts WHERE route_id=?", r) != 1 {
		t.Fatal("read performed an unauthorized physical age sweep")
	}
}

// Driver-only instrumentation uses the same existing seam as Task1's
// t1HubInterpose, with no production failpoints. SQL still runs against SQLite.
type t2ReadProbe struct {
	nextID                                                                  int
	snapshotConn                                                            int
	queries, openRows, violations, writeAttempts, faultHits, readOnlyProbes int
	failTable                                                               string
	mutate                                                                  func()
	mutated                                                                 bool
}
type t2ReadConnector struct {
	base driver.Driver
	dsn  string
	p    *t2ReadProbe
}

func (c t2ReadConnector) Driver() driver.Driver { return c.base }
func (c t2ReadConnector) Connect(context.Context) (driver.Conn, error) {
	raw, err := c.base.Open(c.dsn)
	if err != nil {
		return nil, err
	}
	c.p.nextID++
	return &t2ReadConn{Conn: raw, id: c.p.nextID, p: c.p}, nil
}

type t2ReadConn struct {
	driver.Conn
	id                  int
	p                   *t2ReadProbe
	queryOnly, snapshot bool
}

func (c *t2ReadConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	// Even a successful writer BeginTx is a violation: read statistics must
	// not acquire the _txlock=immediate writer reservation.
	c.p.writeAttempts++
	return c.Conn.(driver.ConnBeginTx).BeginTx(ctx, opts)
}
func (c *t2ReadConn) ExecContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Result, error) {
	n := strings.ToUpper(strings.TrimSpace(q))
	result, err := c.Conn.(driver.ExecerContext).ExecContext(ctx, q, args)
	if err == nil {
		switch n {
		case "PRAGMA QUERY_ONLY=ON":
			c.queryOnly = true
		case "PRAGMA QUERY_ONLY=OFF":
			c.queryOnly = false
		case "BEGIN DEFERRED":
			c.snapshot = true
			if c.p.snapshotConn != 0 && c.p.snapshotConn != c.id {
				c.p.violations++
			}
			c.p.snapshotConn = c.id
		case "ROLLBACK":
			c.snapshot = false
		default:
			if strings.HasPrefix(n, "INSERT") || strings.HasPrefix(n, "UPDATE") || strings.HasPrefix(n, "DELETE") || strings.HasPrefix(n, "BEGIN") {
				c.p.writeAttempts++
			}
		}
	}
	return result, err
}
func t2DomainQuery(q string) bool {
	q = strings.ToLower(q)
	for _, name := range []string{"peers", "routes", "event_subscriptions", "route_attempts", "deliveries", "grokbot_covers"} {
		if strings.Contains(q, name) {
			return true
		}
	}
	return false
}
func (c *t2ReadConn) QueryContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	domain := t2DomainQuery(q)
	if domain {
		c.p.queries++
		if !c.queryOnly || !c.snapshot || c.id != c.p.snapshotConn || c.p.openRows != 0 {
			c.p.violations++
		}
		// Actually ask SQLite to write, rather than trusting a counted pragma.
		// This no-op would succeed on a connection lacking query_only.
		_, err := c.Conn.(driver.ExecerContext).ExecContext(ctx, "UPDATE meta SET value=value WHERE 0", nil)
		if err == nil {
			c.p.violations++
		}
		c.p.readOnlyProbes++
		if c.p.failTable != "" && strings.Contains(strings.ToLower(q), c.p.failTable) {
			c.p.faultHits++
			return nil, errors.New("synthetic_sql_canary_private")
		}
	}
	rows, err := c.Conn.(driver.QueryerContext).QueryContext(ctx, q, args)
	if err != nil {
		return nil, err
	}
	if !domain {
		return rows, nil
	}
	c.p.openRows++
	return &t2ReadRows{Rows: rows, p: c.p}, nil
}

type t2ReadRows struct {
	driver.Rows
	p      *t2ReadProbe
	closed bool
}

func (r *t2ReadRows) Close() error {
	if !r.closed {
		r.closed = true
		r.p.openRows--
	}
	return r.Rows.Close()
}
func (r *t2ReadRows) Next(dest []driver.Value) error {
	err := r.Rows.Next(dest)
	if err != nil && err != io.EOF {
		return err
	}
	// Next pins SQLite's read snapshot even when that query returns no rows.
	if !r.p.mutated && r.p.mutate != nil {
		r.p.mutated = true
		r.p.mutate()
	}
	return err
}

func t2Interpose(t *testing.T, e *testhub.Env, p *t2ReadProbe) *sql.DB {
	t.Helper()
	original := e.Hub.DB.DB
	dsn := "file:" + filepath.Join(e.Hub.Cfg.DataDir, "hub.db") + "?_txlock=immediate&_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(ON)"
	wrapped := sql.OpenDB(t2ReadConnector{base: original.Driver(), dsn: dsn, p: p})
	wrapped.SetMaxOpenConns(4)
	e.Hub.DB.DB = wrapped
	t.Cleanup(func() { wrapped.Close(); e.Hub.DB.DB = original })
	return original
}

func t2Released(t *testing.T, db *sql.DB, p *t2ReadProbe) {
	t.Helper()
	if db.Stats().InUse != 0 || p.openRows != 0 {
		t.Fatal("read leaked a pinned connection or result rows")
	}
	// Check EVERY available connection after success/failure, including the
	// connection actually used by ReadSnapshot. Holding them avoids reacquiring
	// only one clean pool member and missing a contaminated one.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	conns := []*sql.Conn{}
	defer func() {
		for _, c := range conns {
			c.Close()
		}
	}()
	for i := 0; i < 4; i++ {
		c, err := db.Conn(ctx)
		if err != nil {
			t.Fatal("connection leak exhausted pool")
		}
		conns = append(conns, c)
		var qo int
		if err := c.QueryRowContext(ctx, "PRAGMA query_only").Scan(&qo); err != nil || qo != 0 {
			t.Fatal("query_only leaked into pool")
		}
		if _, err := c.ExecContext(ctx, "UPDATE meta SET value=value WHERE 0"); err != nil {
			t.Fatal("pooled connection still read-only or transactional")
		}
	}
}

func TestRouteStatsSnapshotConsistencyAndReadFailure(t *testing.T) {
	t.Run("concurrent-insert-prune-reassignment", func(t *testing.T) {
		e := t2Env(t)
		a, _ := e.NewPeer("session", "snapshot-a")
		b, _ := e.NewPeer("session", "snapshot-b")
		r := t2ID("route", 41)
		t2Route(t, e, r, "peer", a, "http", "https://before.example")
		t2Exec(t, e, "UPDATE peers SET route_id=? WHERE peer_id=?", r, a)
		t2Delivery(t, e, "before", a, r, "accepted", 0, e.Now.Now())
		t2Attempt(t, e, r, "before", e.Now.Now(), "ok", 200, nil, 11, 13)
		op := t2Caller(t, e, e.OperatorSecret)
		before := t2Stats(t, e, r)
		original := e.Hub.DB.DB
		p := &t2ReadProbe{}
		p.mutate = func() {
			tx, err := original.BeginTx(context.Background(), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			for _, q := range []struct {
				sql  string
				args []any
			}{
				{"UPDATE routes SET owner_id=?,url='https://after.example' WHERE route_id=?", []any{b, r}},
				{"UPDATE peers SET route_id=NULL WHERE peer_id=?", []any{a}},
				{"UPDATE peers SET route_id=? WHERE peer_id=?", []any{r, b}},
				{"DELETE FROM route_attempts WHERE route_id=?", []any{r}},
				{"INSERT INTO route_attempts(route_id,delivery_id,at,outcome,http_status,error_class,body_bytes,latency_ms) VALUES(?,'after',?,'fail',503,'http_5xx',101,103)", []any{r, envelope.FormatTime(e.Now.Now())}},
			} {
				if _, err := tx.Exec(q.sql, q.args...); err != nil {
					t.Fatal(err)
				}
			}
			if err := tx.Commit(); err != nil {
				t.Fatal(err)
			}
			// Time must have been frozen before the first query.
			e.Now.Advance(time.Hour)
		}
		t2Interpose(t, e, p)
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		got, err := e.Hub.RouteStats(ctx, op, r)
		if err != nil {
			t.Fatal(err)
		}
		t2Equal(t, got, before)
		if !p.mutated || p.queries == 0 || p.readOnlyProbes == 0 || p.violations != 0 || p.writeAttempts != 0 {
			t.Fatalf("snapshot/query_only violation: %+v", p)
		}
		t2Released(t, e.Hub.DB.DB, p)
		// Verify that the competing transaction really committed distinct data.
		var host string
		if err := original.QueryRow("SELECT url FROM routes WHERE route_id=?", r).Scan(&host); err != nil || host != "https://after.example" {
			t.Fatal("interleaving fixture did not commit")
		}
	})
	for _, table := range []string{"routes", "route_attempts", "deliveries"} {
		t.Run("failure-"+table, func(t *testing.T) {
			e := t2Env(t)
			a, _ := e.NewPeer("session", "failure-peer")
			r := t2ID("route", 42)
			t2Route(t, e, r, "peer", a, "http", "https://failure.example")
			t2Attempt(t, e, r, "failure-dly", e.Now.Now(), "ok", 200, nil, 7, 9)
			t2Delivery(t, e, "failure-dly", a, r, "accepted", 0, e.Now.Now())
			op := t2Caller(t, e, e.OperatorSecret)
			p := &t2ReadProbe{failTable: table}
			t2Interpose(t, e, p)
			got, err := e.Hub.RouteStats(context.Background(), op, r)
			if err == nil || !reflect.ValueOf(got).IsZero() {
				t.Fatal("read failure returned success or partial data")
			}
			if p.faultHits == 0 || p.violations != 0 || p.writeAttempts != 0 {
				t.Fatal("fault did not exercise the pinned read-only query path")
			}
			// Failure must not delete or rewrite existing durable samples/context.
			p.failTable = ""
			var n int
			if err := e.Hub.DB.QueryRow("SELECT count(*) FROM route_attempts WHERE route_id=?", r).Scan(&n); err != nil || n != 1 {
				t.Fatal("failed read changed attempt storage")
			}
			t2Released(t, e.Hub.DB.DB, p)
			p.failTable = ""
			// Reuse the pool to expose lingering query_only/transaction state.
			t2Equal(t, t2One(t, t2Stats(t, e, r))["requests"], 1)
		})
	}
	t.Run("canceled-read", func(t *testing.T) {
		e := t2Env(t)
		op := t2Caller(t, e, e.OperatorSecret)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		got, err := e.Hub.RouteStats(ctx, op, e.OperatorPeer)
		if err == nil || !reflect.ValueOf(got).IsZero() {
			t.Fatal("canceled read returned report")
		}
		if e.Hub.DB.Stats().InUse != 0 {
			t.Fatal("canceled read leaked connection")
		}
	})
}

func TestRouteStatsHubOperatorOnly(t *testing.T) {
	e := t2Env(t)
	peer, secret := e.NewPeer("session", "authority-peer")
	prn, principalSecret := e.NewPrincipal("authority-principal")
	grok, grokSecret := e.NewPeer("grokbot", "authority-grok")
	t2Exec(t, e, "UPDATE peers SET kind='operator' WHERE peer_id=?", peer)
	peerCaller := t2Caller(t, e, secret)
	if peerCaller.Kind != "peer" || peerCaller.PeerKind != "operator" {
		t.Fatal("fixture lost credential-kind distinction")
	}
	callers := []auth.Caller{peerCaller, t2Caller(t, e, principalSecret), t2Caller(t, e, grokSecret)}
	// OAuth-shaped principal identities use the existing authenticated Caller
	// seam; bearer grants themselves are tested through MCP's authenticator.
	callers = append(callers, auth.Caller{Kind: "principal", PrincipalID: prn, PeerID: peer, PeerKind: "dot"},
		auth.Caller{Kind: "principal", PrincipalID: prn, PeerID: grok, PeerKind: "grokbot"})
	for _, c := range callers {
		for _, id := range []string{peer, e.OperatorPeer, t2ID("route", 999), "invalid-selector-canary"} {
			t2Failure(t, e, c, id, 403, "forbidden")
		}
	}
	op := t2Caller(t, e, e.OperatorSecret)
	if _, err := e.Hub.RouteStats(context.Background(), op, peer); err != nil {
		t.Fatal(err)
	}
	t2Exec(t, e, "UPDATE peers SET disabled=1 WHERE peer_id=?", e.OperatorPeer)
	op = t2Caller(t, e, e.OperatorSecret)
	if _, err := e.Hub.RouteStats(context.Background(), op, peer); err != nil {
		t.Fatal("operator credential lost read authority without enabled peer")
	}
	if op.Kind != "operator" {
		t.Fatal("operator fixture changed credential kind")
	}
}

func TestRouteStatsLegacyGrokRunsRemainIndependent(t *testing.T) {
	e := t2Env(t)
	ctx := context.Background()
	peer, _ := e.NewPeer("grokbot", "legacy-runs")
	r, d := t2ID("route", 51), t2ID("dly", 51)
	now := e.Now.Now()
	t2Route(t, e, r, "peer", peer, "grokbot", "https://legacy.example/routine")
	t2Exec(t, e, "UPDATE peers SET route_id=? WHERE peer_id=?", r, peer)
	for _, age := range []time.Duration{2 * time.Minute, time.Minute} {
		if ok, wait := e.Hub.GrokbotBudgetOK(ctx, r, now.Add(-age)); !ok || wait != 0 {
			t.Fatal("legacy run reservation failed")
		}
	}
	t2Exec(t, e, "INSERT INTO counters(name,value) VALUES('push_auth_failed',17),('push_conflicts',29)")
	t2Delivery(t, e, d, peer, r, "accepted", 0, now)
	original := e.Hub.DB.DB
	// Include every run's row identity and every counter, through the original
	// pool so preservation checks cannot affect the reader's fault probe.
	state := func() [][4]string {
		t.Helper()
		rows, err := original.Query("SELECT 'run',CAST(rowid AS TEXT),route_id,at FROM route_runs UNION ALL SELECT 'counter',name,CAST(value AS TEXT),'' FROM counters ORDER BY 1,2,3,4")
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		out := [][4]string{}
		for rows.Next() {
			var row [4]string
			if err := rows.Scan(&row[0], &row[1], &row[2], &row[3]); err != nil {
				t.Fatal(err)
			}
			out = append(out, row)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		return out
	}
	before := state()
	if count(t, e, "SELECT count(*) FROM route_runs WHERE route_id=?", r) != 2 ||
		count(t, e, "SELECT count(*) FROM route_attempts WHERE route_id=?", r) != 0 {
		t.Fatal("fixture needs legacy runs and no captured attempts")
	}
	want := map[string]any{
		"route_id": r, "kind": "grokbot", "host": "legacy.example",
		"owner_kind": "peer", "owner_id": peer, "attempt_scope": "route",
		"requests": 0, "coalesced": 0, "ok": 0, "fail": 0, "refused": 0, "distinct_ids": 0,
		"last_used_at": nil, "last_success_at": nil, "last_error": nil,
		"last_error_at": nil, "last_status": nil,
		"body_bytes": t2Dist(nil, nil, nil, 0), "latency_ms": t2Dist(nil, nil, nil, 0),
	}
	contextWant := map[string]any{"peer_ids": []string{peer}, "total": 1, "with_push_evidence": 0, "accepted_without_push_evidence": 1}
	check := func() {
		t.Helper()
		for _, subject := range []struct{ kind, id string }{{"route", r}, {"peer", peer}} {
			t2Equal(t, state(), before)
			m := t2Stats(t, e, subject.id)
			t2Equal(t, t2One(t, m), want)
			t2Equal(t, m["subject"], map[string]any{"kind": subject.kind, "id": subject.id})
			t2Equal(t, m["retained_deliveries"], contextWant)
			t2Equal(t, state(), before)
		}
	}
	check()
	if count(t, e, "SELECT count(*) FROM route_attempts WHERE route_id=?", r) != 0 {
		t.Fatal("reader backfilled attempts from legacy runs")
	}
	// This is one actual observation-writer call, not a reconstructed run.
	e.Hub.NoteRouteAttempt(ctx, hub.RouteAttemptObservation{
		RouteID: r, DeliveryID: d, Kind: "grokbot", Attempted: true,
		HTTPStatus: 200, GrokClassification: hub.GrokbotStarted,
		BodyBytes: 11, Elapsed: 13 * time.Millisecond, CompletedAt: now,
	})
	if count(t, e, "SELECT count(*) FROM route_attempts WHERE route_id=? AND delivery_id=?", r, d) != 1 ||
		count(t, e, "SELECT count(*) FROM route_attempts") != 1 {
		t.Fatal("fixture needs exactly one captured attempt")
	}
	t2Equal(t, state(), before)
	for _, key := range []string{"requests", "ok", "distinct_ids"} {
		want[key] = 1
	}
	want["last_used_at"], want["last_success_at"] = envelope.FormatTime(now), envelope.FormatTime(now)
	want["last_status"] = 200
	want["body_bytes"], want["latency_ms"] = t2Dist(11, 11, 11, 1), t2Dist(13, 13, 13, 1)
	contextWant["with_push_evidence"], contextWant["accepted_without_push_evidence"] = 1, 0
	check()
	op := t2Caller(t, e, e.OperatorSecret)
	p := &t2ReadProbe{failTable: "route_attempts"}
	t2Interpose(t, e, p)
	t2Equal(t, state(), before)
	got, err := e.Hub.RouteStats(ctx, op, r)
	if err == nil || !reflect.ValueOf(got).IsZero() {
		t.Fatal("injected sample read failure returned success or partial report")
	}
	if p.faultHits == 0 || p.readOnlyProbes == 0 || p.queries == 0 || p.violations != 0 || p.writeAttempts != 0 {
		t.Fatal("failure did not reach the pinned read-only sample query")
	}
	t2Equal(t, state(), before)
	t2Released(t, e.Hub.DB.DB, p)
	p.failTable = ""
	check()
	t2Equal(t, state(), before)
}

// These fixtures catch counting pending_count (including the leader), counting
// attempt samples, and reattributing retained coverage to current membership.
func t4StatsRow(t *testing.T, e *testhub.Env, route string) opapi.RouteStatsRow {
	t.Helper()
	s, err := e.Hub.RouteStats(context.Background(), t2Caller(t, e, e.OperatorSecret), route)
	t2Must(t, err)
	if len(s.Routes) != 1 {
		t.Fatalf("route rows=%d want1", len(s.Routes))
	}
	return s.Routes[0]
}

// Capture the actual outgoing client body without replacing its transport or
// the fixture's independent client-entry and receiver-POST counters.
type t4Capture struct {
	base http.RoundTripper
	body []byte
}

func (c *t4Capture) RoundTrip(r *http.Request) (*http.Response, error) {
	var err error
	c.body, err = io.ReadAll(r.Body)
	if err != nil {
		return nil, err
	}
	r.Body = io.NopCloser(bytes.NewReader(c.body))
	return c.base.RoundTrip(r)
}

func TestRouteStatsSixCoveredOneRequest(t *testing.T) {
	f := t2Setup(t)
	f.presence("working")
	ids := make([]string, 7)
	prior := map[string]t2HandledRow{}
	for i := range ids {
		ids[i] = f.send("wake_idle")
		prior[ids[i]] = f.handledRow(ids[i])
	}
	f.step()
	f.requests(0)
	f.presence("idle")
	d := f.claim()
	if d.GrokWake.PendingCount != 7 {
		t.Fatalf("pending_count=%d want7", d.GrokWake.PendingCount)
	}
	want := []string{}
	for _, id := range ids {
		if id != d.DeliveryID {
			want = append(want, id)
		}
	}
	sort.Strings(want)
	rows, err := f.env.Hub.DB.Query(`SELECT delivery_id FROM grokbot_covers WHERE route_id=? ORDER BY delivery_id`, f.route)
	t2Must(t, err)
	got := []string{}
	for rows.Next() {
		var id string
		t2Must(t, rows.Scan(&id))
		got = append(got, id)
	}
	t2Must(t, rows.Err())
	t2Must(t, rows.Close())
	if len(want) != 6 || !reflect.DeepEqual(got, want) {
		t.Fatalf("covered IDs=%v want exact followers=%v", got, want)
	}
	capture := &t4Capture{base: f.client}
	f.w.Client.Transport = capture
	f.w.Push(f.ctx, d)
	f.step()
	f.requests(1)
	var body struct {
		Type     string `json:"type"`
		Version  int    `json:"version"`
		Pending  int    `json:"pending_count"`
		Delivery struct {
			ID string `json:"delivery_id"`
		} `json:"delivery"`
	}
	t2Must(t, json.Unmarshal(capture.body, &body))
	if body.Type != "grokbot_wake" || body.Version != 1 || body.Pending != 7 || body.Delivery.ID != d.DeliveryID {
		t.Fatalf("actual POST envelope=%+v", body)
	}
	for _, id := range want {
		after := f.handledRow(id)
		if after.State != "queued" || after.Attempt != 0 || after.Left != prior[id].Left {
			t.Fatalf("follower accounting=%+v prior=%+v", after, prior[id])
		}
		for _, receipt := range receipts(t, f.env, f.a) {
			if receipt.DeliveryID == id {
				t.Fatalf("fabricated follower receipt=%+v", receipt)
			}
		}
		if f.scalar(`SELECT count(*) FROM route_attempts WHERE delivery_id=?`, id) != 0 {
			t.Fatal("fabricated follower attempt")
		}
	}
	if f.covers(d.DeliveryID) != 0 || f.scalar(`SELECT count(*) FROM route_runs WHERE route_id=?`, f.route) != 1 {
		t.Fatal("leader coverage or follower run reservation")
	}
	row := t4StatsRow(t, f.env, f.route)
	if row.Requests != 1 || row.Coalesced != 6 {
		t.Fatalf("Requests=%d Coalesced=%d want1/6 (7 pending, 6 followers, 1 POST)", row.Requests, row.Coalesced)
	}
}

func TestCoalescedFirstCoverageDistinctWindowAndMove(t *testing.T) {
	e := t2Env(t)
	a, _ := e.NewPeer("grokbot", "cover-old-owner")
	b, _ := e.NewPeer("grokbot", "cover-new-owner")
	r, other, httpRoute, sub := t2ID("route", 701), t2ID("route", 702), t2ID("route", 703), t2ID("sub", 701)
	t2Route(t, e, r, "peer", a, "grokbot", "https://historical.example")
	t2Route(t, e, other, "peer", b, "grokbot", "https://current.example")
	t2Route(t, e, httpRoute, "peer", b, "http", "https://http.example")
	t2Sub(t, e, sub, b)
	now := e.Now.Now()
	since := now.Add(-168 * time.Hour)
	cover := func(id, route string, first time.Time) {
		t.Helper()
		t2Exec(t, e, `INSERT INTO grokbot_covers(route_id,delivery_id,recipient,first_covered_at) VALUES(?,?,?,?) ON CONFLICT DO NOTHING`, route, id, a, envelope.FormatTime(first))
	}
	for i, at := range []time.Time{since.Add(-time.Millisecond), since, now, now.Add(time.Millisecond)} {
		id := t2ID("dly", 710+i)
		t2Delivery(t, e, id, a, r, "queued", 0, now)
		cover(id, r, at)
	}
	// More first-cover rows than the sample cap; no attempt evidence required.
	t2Seed(t, e, func(tx *sql.Tx) {
		for i := 0; i < 1001; i++ {
			id := t2ID("dly", 800+i)
			t2DeliveryOn(t, tx, id, a, r, "queued", 0, now)
			_, err := tx.Exec(`INSERT INTO grokbot_covers(route_id,delivery_id,recipient,first_covered_at) VALUES(?,?,?,?) ON CONFLICT DO NOTHING`, r, id, a, envelope.FormatTime(now.Add(-time.Hour)))
			t2Must(t, err)
		}
	})
	// Re-cover cannot slide an old first time into this window.
	cover(t2ID("dly", 710), r, now)
	// A covered ID may also genuinely have attempted evidence; it counts once
	// in coverage independently of repeated attempts and their sample cap.
	t2Seed(t, e, func(tx *sql.Tx) {
		for i := 0; i < 1001; i++ {
			t2AttemptOn(t, tx, r, t2ID("dly", 800), now, "ok", 200, nil, 1, 1)
		}
	})
	// Current owner, peer membership and delivery routing no longer match the
	// historical first-covered route. Attribution must remain historical.
	t2Exec(t, e, `UPDATE routes SET owner_id=? WHERE route_id=?`, b, r)
	t2Exec(t, e, `UPDATE peers SET route_id=? WHERE peer_id=?`, other, a)
	t2Exec(t, e, `UPDATE deliveries SET route_id=?,recipient=?`, other, b)
	for _, id := range []string{httpRoute, sub} {
		cover(t2ID("dly", 800), id, now)
	}
	check := func(want int64) {
		t.Helper()
		row := t4StatsRow(t, e, r)
		if row.Coalesced != want || row.Requests != 1000 || row.DistinctIDs != 1 {
			t.Fatalf("historical row=%+v want coalesced=%d requests1000 distinct1", row, want)
		}
	}
	check(1003)
	for _, id := range []string{other, httpRoute, sub} {
		if row := t4StatsRow(t, e, id); row.Coalesced != 0 {
			t.Fatalf("other kind/route/subscription counted coverage: %+v", row)
		}
	}
	t2Exec(t, e, `DELETE FROM deliveries WHERE dly_id=?`, t2ID("dly", 800))
	check(1002)
	e.Now.Advance(time.Millisecond)
	// Lower bound leaves and future upper row enters, keeping the total equal.
	check(1002)
}

func TestCoalescedRecoversAndHandlesWithoutRewritingFirstEvidence(t *testing.T) {
	t.Run("later-post-recovery-recover-and-handle", func(t *testing.T) {
		f := t2Setup(t)
		f.send("wake")
		follower := f.send("wake")
		f.step()
		f.requests(1)
		first := f.firstCover(follower)
		f.env.Now.Advance(hub.PushLease + time.Millisecond)
		f.respond = func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(400)
			fmt.Fprint(w, `{"success":false,"error":"invalid payload"}`)
		}
		f.step()
		f.requests(2)
		if f.handledRow(follower).State != "dead_letter" {
			t.Fatal("fixture needs terminal follower POST")
		}
		var retry string
		t2Must(t, f.env.Hub.DB.QueryRow(`SELECT retry_at FROM grokbot_wakes WHERE route_id=?`, f.route).Scan(&retry))
		at, err := envelope.ParseTime(retry)
		t2Must(t, err)
		f.env.Now.Advance(at.Sub(f.env.Now.Now()) + time.Millisecond)
		f.step()
		f.requests(2)
		if f.scalar(`SELECT count(*) FROM grokbot_wakes WHERE route_id=?`, f.route) != 0 {
			t.Fatal("finite coordinator cleanup not exercised")
		}
		t2Must(t, f.env.Hub.RetryDeadLetter(f.ctx, follower))
		f.respond = nil
		f.step()
		f.requests(3)
		f.processed(f.caller(f.b), follower)
		f.step()
		f.requests(3)
		if f.firstCover(follower) != first {
			t.Fatal("later POST/recovery/handling rewrote first evidence")
		}
		row := t4StatsRow(t, f.env, f.route)
		if row.Requests != 3 || row.Coalesced != 1 || row.DistinctIDs != 2 {
			t.Fatalf("overlapping populations=%+v want requests3/coalesced1/distinct2", row)
		}
	})
	t.Run("handled-before-begin-and-replacement", func(t *testing.T) {
		f := t2Setup(t)
		leader := f.send("wake")
		follower := f.send("wake")
		d := f.claim()
		if f.covers(leader) != 0 {
			t.Fatal("election falsely covered leader")
		}
		first := f.firstCover(follower)
		f.env.Now.Advance(time.Second)
		f.processed(f.caller(f.b), leader)
		f.processed(f.caller(f.b), leader)
		f.w.Push(f.ctx, d)
		f.requests(0)
		if f.covers(leader) != 1 {
			t.Fatal("handling must separately cover suppressed leader once")
		}
		f.step()
		f.requests(1)
		f.processed(f.caller(f.b), follower)
		if f.firstCover(follower) != first {
			t.Fatal("replacement or handling rewrote prior follower coverage")
		}
		row := t4StatsRow(t, f.env, f.route)
		if row.Requests != 1 || row.Coalesced != 2 {
			t.Fatalf("handled/replacement stats=%+v want1/2", row)
		}
	})
}

func TestCoalescedOperatorOnlySnapshot(t *testing.T) {
	t.Run("operator-only-consistent-snapshot", func(t *testing.T) {
		e := t2Env(t)
		a, secret := e.NewPeer("grokbot", "cover-snapshot")
		b, _ := e.NewPeer("grokbot", "cover-next")
		r := t2ID("route", 704)
		id := t2ID("dly", 704)
		t2Route(t, e, r, "peer", a, "grokbot", "https://before.example")
		t2Delivery(t, e, id, a, r, "queued", 0, e.Now.Now())
		t2Exec(t, e, `INSERT INTO grokbot_covers VALUES(?,?,?,?)`, r, id, a, envelope.FormatTime(e.Now.Now()))
		t2Failure(t, e, t2Caller(t, e, secret), r, 403, "forbidden")
		op := t2Caller(t, e, e.OperatorSecret)
		original := e.Hub.DB.DB
		p := &t2ReadProbe{}
		p.mutate = func() {
			_, err := original.Exec(`DELETE FROM grokbot_covers WHERE route_id=?`, r)
			t2Must(t, err)
			_, err = original.Exec(`UPDATE routes SET owner_id=? WHERE route_id=?`, b, r)
			t2Must(t, err)
			e.Now.Advance(time.Hour)
		}
		t2Interpose(t, e, p)
		got, err := e.Hub.RouteStats(context.Background(), op, r)
		t2Must(t, err)
		if len(got.Routes) != 1 || got.Routes[0].Coalesced != 1 || got.Routes[0].OwnerID != a {
			t.Fatalf("coverage escaped pinned snapshot=%+v", got)
		}
		if !p.mutated || p.violations != 0 || p.writeAttempts != 0 {
			t.Fatalf("snapshot probe=%+v", p)
		}
		t2Released(t, e.Hub.DB.DB, p)
		if row := t4StatsRow(t, e, r); row.Coalesced != 0 || row.OwnerID != b {
			t.Fatalf("committed mutation invisible=%+v", row)
		}
	})
	t.Run("coverage-read-failure-is-sanitized", func(t *testing.T) {
		e := t2Env(t)
		a, _ := e.NewPeer("grokbot", "cover-failure")
		r := t2ID("route", 705)
		t2Route(t, e, r, "peer", a, "grokbot", "https://failure.example")
		p := &t2ReadProbe{failTable: "grokbot_covers"}
		t2Interpose(t, e, p)
		got, err := e.Hub.RouteStats(context.Background(), t2Caller(t, e, e.OperatorSecret), r)
		if err == nil || strings.Contains(err.Error(), "synthetic_sql_canary_private") || !reflect.ValueOf(got).IsZero() || p.faultHits != 1 {
			t.Fatalf("coverage read not atomic/sanitized: report=%+v err=%v hits=%d", got, err, p.faultHits)
		}
		t2Released(t, e.Hub.DB.DB, p)
	})
}
