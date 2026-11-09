package push_test

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/hub"
	"github.com/XertroV/vecnet/internal/push"
)

// Source-only A2 draft; uncompiled and unrun. Existing helpers remain unchanged.
// Injected SQL failures target telemetry only; payload responses and settlement
// timing controls use the existing worker with real loopback HTTP and SQLite.

type t1PushAttempt struct {
	id                    int64
	delivery, at, outcome string
	status                sql.NullInt64
	class                 sql.NullString
	body, latency         int
}

func t1PushRows(t *testing.T, f *fixture) []t1PushAttempt {
	t.Helper()
	rows, err := f.env.Hub.DB.Query(`SELECT id,delivery_id,at,outcome,http_status,error_class,body_bytes,latency_ms FROM route_attempts WHERE route_id=? ORDER BY id`, f.route)
	if err != nil {
		t.Fatalf("query payload observations: %v", err)
	}
	defer rows.Close()
	out := []t1PushAttempt{}
	for rows.Next() {
		var a t1PushAttempt
		if err := rows.Scan(&a.id, &a.delivery, &a.at, &a.outcome, &a.status, &a.class, &a.body, &a.latency); err != nil {
			t.Fatal(err)
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

type t1RoundTrip func(*http.Request) (*http.Response, error)

func (fn t1RoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return fn(r) }

type t1SlowBody struct {
	io.ReadCloser
	once   sync.Once
	read   int
	closed bool
}

func (r *t1SlowBody) Read(b []byte) (int, error) {
	r.once.Do(func() { time.Sleep(15 * time.Millisecond) })
	n, err := r.ReadCloser.Read(b)
	r.read += n
	return n, err
}

func (r *t1SlowBody) Close() error {
	r.closed = true
	return r.ReadCloser.Close()
}

func t1Claim(t *testing.T, f *fixture) hub.DueDelivery {
	t.Helper()
	due, err := f.env.Hub.ClaimDue(f.ctx, f.env.Now.Now(), 1)
	if err != nil || len(due) != 1 {
		t.Fatalf("claim fixture: %d, %v", len(due), err)
	}
	return due[0]
}

func t1PushOutcome(t *testing.T, f *fixture, dly, state string, spent int) {
	t.Helper()
	r := f.row(dly)
	if r.State != state || r.Attempt != 1 || r.AttemptsLeft != hub.PushAttempts-spent {
		t.Fatalf("authoritative delivery = %+v, want %s, one claim, %d spent", r, state, spent)
	}
	rs := f.receiptsFor(f.a, dly)
	wantReceipts := 1
	if state == "queued" {
		wantReceipts = 0
		if r.Next == nil || f.untilNext(dly) <= 0 {
			t.Fatal("retry lost its backoff")
		}
	}
	if len(rs) != wantReceipts || (len(rs) == 1 && (rs[0].State != state || rs[0].By != "hub")) {
		t.Fatalf("receipts = %+v, want state %s/count %d", rs, state, wantReceipts)
	}
	wantDead, wantConflict := 0, 0
	if state == "dead_letter" {
		wantDead = 1
	}
	if state == "failed" {
		wantConflict = 1
	}
	if f.counter("dead_letters") != wantDead || f.counter("push_conflicts") != wantConflict {
		t.Fatal("authoritative counters changed")
	}
}

func TestRouteAttemptHTTPActualRequests(t *testing.T) {
	for _, tc := range []struct {
		name, outcome, class, state string
		status, spent               int
		netErr                      error
	}{
		{"200", "ok", "", "accepted", 200, 0, nil},
		{"204", "ok", "", "accepted", 204, 0, nil},
		{"408", "fail", "http_408", "queued", 408, 1, nil},
		{"429", "fail", "http_429", "queued", 429, 1, nil},
		{"503", "fail", "http_5xx", "queued", 503, 1, nil},
		{"409", "refused", "http_409", "failed", 409, 1, nil},
		{"410", "refused", "http_410", "dead_letter", 410, 1, nil},
		{"413", "refused", "http_413", "dead_letter", 413, 1, nil},
		{"302", "refused", "http_3xx", "dead_letter", 302, 1, nil},
		{"connection_closed", "fail", "connection_closed", "queued", 0, 1, io.ErrUnexpectedEOF},
		{"unknown_safe_reason", "fail", "request_failed", "queued", 0, 1, &hub.OutboundError{Reason: "secretcanary", Err: errors.New("https://user:key@fixture.invalid/private?token=canary")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := setup(t, func(int, hit) reply {
				return reply{Status: max(tc.status, 200), Body: strings.Repeat("r", 4113)}
			})
			dly := f.send(f.aSec, f.msg("wake")).Deliveries[0].DeliveryID
			d := t1Claim(t, f)
			calls := 0
			var sent []byte
			var responseBody *t1SlowBody
			transport := http.DefaultTransport.(*http.Transport).Clone()
			t.Cleanup(transport.CloseIdleConnections)
			f.w = &push.Worker{Hub: f.env.Hub, Now: f.env.Now.Now, Client: &http.Client{
				CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
				Transport: t1RoundTrip(func(req *http.Request) (*http.Response, error) {
					calls++
					var err error
					sent, err = io.ReadAll(req.Body)
					if err != nil {
						return nil, err
					}
					req.Body = io.NopCloser(bytes.NewReader(sent))
					if tc.netErr != nil {
						return nil, tc.netErr
					}
					resp, err := transport.RoundTrip(req)
					if err == nil {
						responseBody = &t1SlowBody{ReadCloser: resp.Body}
						resp.Body = responseBody
					}
					// Wall time changes cannot become a two-hour latency.
					f.env.Now.Advance(2 * time.Hour)
					return resp, err
				}),
			}}
			start := time.Now()
			f.w.Push(f.ctx, d)
			elapsed := time.Since(start)
			a := t1PushRows(t, f)
			if calls != 1 || len(a) != 1 || !bytes.Equal(sent, d.Body) {
				t.Fatalf("client calls/rows/body = %d/%d/%v", calls, len(a), bytes.Equal(sent, d.Body))
			}
			row := a[0]
			if row.delivery != dly || row.outcome != tc.outcome || row.class.Valid != (tc.class != "") || row.class.String != tc.class || row.status.Valid != (tc.status != 0) || (row.status.Valid && row.status.Int64 != int64(tc.status)) {
				t.Fatalf("observation = %+v, want %s/%s/status %d", row, tc.outcome, tc.class, tc.status)
			}
			if row.body != len(d.Body) || row.at != envelope.FormatTime(f.env.Now.Now()) || row.latency < 0 || row.latency > int(elapsed.Milliseconds())+2 {
				t.Fatalf("body/completion/monotonic measurement = %+v; call %v", row, elapsed)
			}
			if tc.netErr == nil && row.latency < 14 {
				t.Fatal("latency omitted the bounded response drain")
			}
			if tc.netErr == nil {
				wantRead := 4096
				if tc.status == 204 {
					wantRead = 0
				}
				if responseBody == nil || !responseBody.closed || responseBody.read != wantRead {
					t.Fatal("response was not closed after its exact bounded drain")
				}
			}
			t1PushOutcome(t, f, dly, tc.state, tc.spent)
			if f.step() != 0 || calls != 1 || len(t1PushRows(t, f)) != 1 {
				t.Fatal("outcome/telemetry introduced another immediate request")
			}
		})
	}
	t.Run("retry_reuses_delivery_id", func(t *testing.T) {
		f := setup(t, func(n int, _ hit) reply {
			if n == 0 {
				return reply{Status: 503}
			}
			return reply{Status: 204}
		})
		dly := f.send(f.aSec, f.msg("wake")).Deliveries[0].DeliveryID
		if f.step() != 1 {
			t.Fatal("first payload was not invoked")
		}
		t1PushOutcome(t, f, dly, "queued", 1)
		firstAt := envelope.FormatTime(f.env.Now.Now())
		f.env.Now.Advance(f.untilNext(dly))
		if f.step() != 1 {
			t.Fatal("due retry was not invoked")
		}
		a, hits := t1PushRows(t, f), f.rcv.Hits()
		if len(a) != 2 || len(hits) != 2 || a[0].id >= a[1].id || !bytes.Equal(hits[0].Body, hits[1].Body) {
			t.Fatal("two payload invocations lost rows, insertion order or frozen retry bytes")
		}
		for i, status := range []int64{503, 204} {
			if a[i].delivery != dly || a[i].status != (sql.NullInt64{Int64: status, Valid: true}) ||
				a[i].body != len(hits[i].Body) || a[i].outcome != []string{"fail", "ok"}[i] ||
				a[i].class.String != []string{"http_5xx", ""}[i] || a[i].class.Valid != (i == 0) ||
				a[i].at != []string{firstAt, envelope.FormatTime(f.env.Now.Now())}[i] || a[i].latency < 0 {
				t.Fatal("retry observations changed delivery ID or classification")
			}
		}
		if r := f.row(dly); r.State != "accepted" || r.Attempt != 2 || r.AttemptsLeft != hub.PushAttempts-1 || len(f.receiptsFor(f.a, dly)) != 1 {
			t.Fatal("retry telemetry changed authoritative settlement")
		}
	})
	t.Run("refused_redirect_keeps_response_status", func(t *testing.T) {
		var followed atomic.Int64
		target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			followed.Add(1)
			w.WriteHeader(200)
		}))
		t.Cleanup(target.Close)
		f := setup(t, func(int, hit) reply { return reply{Status: 302, Header: map[string]string{"Location": target.URL}} })
		f.send(f.aSec, f.msg("wake"))
		f.step()
		a := t1PushRows(t, f)
		if followed.Load() != 0 || len(f.rcv.Hits()) != 1 || len(a) != 1 || a[0].outcome != "refused" || !a[0].status.Valid || a[0].status.Int64 != 302 || a[0].class.String != "http_3xx" {
			t.Fatal("redirect followed or response status lost", a)
		}
	})
	t.Run("latency_excludes_settlement", func(t *testing.T) {
		f := setup(t, always(200))
		f.send(f.aSec, f.msg("wake"))
		d := t1Claim(t, f)
		fault := t1InterposePushSQL(t, f, "")
		fault.settlementDelay = 80 * time.Millisecond
		start := time.Now()
		f.w.Push(f.ctx, d)
		elapsed := time.Since(start)
		a := t1PushRows(t, f)
		if len(a) != 1 || elapsed < 80*time.Millisecond || a[0].latency > int(elapsed.Milliseconds())-60 {
			t.Fatalf("latency included settlement/database time: rows %+v, elapsed %v", a, elapsed)
		}
	})
	t.Run("concurrent_invocations_keep_their_measurements", func(t *testing.T) {
		f := setup(t, func(_ int, h hit) reply {
			if bytes.Contains(h.Body, []byte("different-body")) {
				return reply{Status: 503}
			}
			return reply{Status: 200}
		})
		f.send(f.aSec, f.msg("wake"))
		second := f.msg("wake")
		second["content"] = map[string]any{"text": strings.Repeat("different-body", 100)}
		f.send(f.aSec, second)
		if f.step() != 2 {
			t.Fatal("fixture did not claim both independent invocations")
		}
		a, hits := t1PushRows(t, f), f.rcv.Hits()
		if len(a) != 2 || len(hits) != 2 {
			t.Fatalf("concurrent request/row count = %d/%d", len(hits), len(a))
		}
		for _, h := range hits {
			matched := false
			for _, row := range a {
				if row.delivery != h.Header.Get("webhook-id") {
					continue
				}
				matched = true
				wantStatus := int64(200)
				if bytes.Contains(h.Body, []byte("different-body")) {
					wantStatus = 503
				}
				if row.body != len(h.Body) || !row.status.Valid || row.status.Int64 != wantStatus {
					t.Fatal("measurements leaked between invocations", row)
				}
			}
			if !matched {
				t.Fatal("a payload invocation lost its observation")
			}
		}
	})
}

func TestRouteAttemptHTTPInformationalRetryI1(t *testing.T) {
	f := setup(t, always(200))
	var serverCalls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		serverCalls.Add(1)
		if _, err := io.Copy(io.Discard, req.Body); err != nil {
			t.Error(err)
			return
		}
		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.Close()
		if _, err := io.WriteString(conn, "HTTP/1.1 101 Switching Protocols\r\nConnection: close\r\n\r\n"); err != nil {
			t.Error(err)
		}
	}))
	t.Cleanup(server.Close)
	dly := f.send(f.aSec, f.msg("wake")).Deliveries[0].DeliveryID
	d := t1Claim(t, f)
	d.URL = server.URL
	calls := 0
	var sent []byte
	transport := http.DefaultTransport.(*http.Transport).Clone()
	t.Cleanup(transport.CloseIdleConnections)
	f.w = &push.Worker{Hub: f.env.Hub, Now: f.env.Now.Now, Client: &http.Client{
		Transport: t1RoundTrip(func(req *http.Request) (*http.Response, error) {
			calls++
			var err error
			sent, err = io.ReadAll(req.Body)
			if err != nil {
				return nil, err
			}
			req.Body = io.NopCloser(bytes.NewReader(sent))
			return transport.RoundTrip(req)
		}),
	}}
	f.w.Push(f.ctx, d)
	// The existing classifier must durably retry before telemetry is checked:
	// one spent attempt, positive backoff, no receipt or terminal counter.
	t1PushOutcome(t, f, dly, "queued", 1)
	if calls != 1 || serverCalls.Load() != 1 || !bytes.Equal(sent, d.Body) {
		t.Fatalf("HTTP 101 payload calls/body = %d/%v", calls, bytes.Equal(sent, d.Body))
	}
	a := t1PushRows(t, f)
	if len(a) != 1 {
		t.Fatalf("HTTP 101 payload produced %d observations, want one", len(a))
	}
	row := a[0]
	if row.delivery != dly || row.outcome != "fail" || !row.class.Valid || row.class.String != "http_status" || !row.status.Valid || row.status.Int64 != 101 {
		t.Fatalf("HTTP 101 retry telemetry = %+v, want fail/http_status/status 101", row)
	}
	if row.body != len(d.Body) || row.at != envelope.FormatTime(f.env.Now.Now()) {
		t.Fatalf("HTTP 101 body/completion = %+v, want %d bytes", row, len(d.Body))
	}
	if f.step() != 0 || calls != 1 || len(t1PushRows(t, f)) != 1 {
		t.Fatal("HTTP 101 retry lost backoff or introduced another request/observation")
	}
}

func TestRouteAttemptExcludesPreflightAndChallenges(t *testing.T) {
	t.Run("challenge_hold_and_held_wake_idle", func(t *testing.T) {
		f := setup(t, always(200)) // setup performs a real signed challenge
		if len(f.rcv.Verifications()) != 1 || len(t1PushRows(t, f)) != 0 {
			t.Fatal("verification created a payload observation")
		}
		f.send(f.aSec, f.msg("hold"))
		resp, _ := f.env.Do(f.bSec, "POST", "/v1/presence", map[string]any{"state": "working", "at": envelope.FormatTime(f.env.Now.Now())})
		if resp.StatusCode != http.StatusNoContent {
			t.Fatal("working presence fixture failed")
		}
		held := f.send(f.aSec, f.msg("wake_idle")).Deliveries[0].DeliveryID
		if f.row(held).Held != 1 || f.step() != 0 || len(f.rcv.Hits()) != 0 || len(t1PushRows(t, f)) != 0 {
			t.Fatal("gated work was measured as a request")
		}
	})
	for _, preflight := range []string{"body_cap", "no_key", "invalid_key", "invalid_request", "claim_only"} {
		t.Run(preflight, func(t *testing.T) {
			f := setup(t, always(200))
			f.send(f.aSec, f.msg("wake"))
			d := t1Claim(t, f)
			switch preflight {
			case "body_cap":
				d.Body = bytes.Repeat([]byte("x"), hub.MaxPushBody+1)
			case "no_key":
				d.Keys = nil
			case "invalid_key":
				d.Keys = []string{"whsec_!not-base64!"}
			case "invalid_request":
				d.URL = "://invalid\nrequest"
			}
			if preflight != "claim_only" {
				f.w.Push(f.ctx, d)
			}
			if len(f.rcv.Hits()) != 0 || len(t1PushRows(t, f)) != 0 {
				t.Fatal("pre-Do failure/claim created an observation")
			}
		})
	}
	for _, reason := range []string{"budget", "grok_key", "grok_body", "grok_request"} {
		t.Run(reason, func(t *testing.T) {
			f := setup(t, nil)
			f.addGrokbot(started)
			f.send(f.aSec, f.msg("wake"))
			d := t1Claim(t, f)
			switch reason {
			case "budget":
				f.setRunsPerHour(1)
				f.seedRuns(300, 30*time.Minute) // account-wide budget, regardless of route
			case "grok_key":
				if _, err := f.env.Hub.DB.Exec(`UPDATE routes SET key_enc=NULL WHERE route_id=?`, f.route); err != nil {
					t.Fatal(err)
				}
			case "grok_body":
				d.Body = bytes.Repeat([]byte("x"), hub.MaxPushBody+1)
			case "grok_request":
				d.URL = "://invalid\nrequest"
			}
			f.w.Push(f.ctx, d)
			wantRuns := 0
			if reason == "grok_request" {
				// Preserve M's status-based rule: an error returned by
				// postGrokbot retains the reserved run even before Do.
				wantRuns = 1
			}
			if len(f.rcv.Hits()) != 0 || len(t1PushRows(t, f)) != 0 || f.routeRuns() != wantRuns {
				t.Fatal("Grok preflight consumed a run or recorded a request")
			}
			if reason == "budget" {
				if r := f.row(d.DeliveryID); r.State != "queued" || r.Attempt != 0 || r.AttemptsLeft != hub.PushAttempts {
					t.Fatal("budget deferral spent an attempt", r)
				}
			}
		})
	}
}

func TestRouteAttemptGrokClassificationAndBudget(t *testing.T) {
	for _, tc := range []struct {
		name, body, outcome, class, state string
		status, runs, auth                int
	}{
		{"started", `{"success":true}`, "ok", "", "accepted", 200, 1, 0},
		{"not_started", `{"success":false}`, "refused", "routine_not_started", "dead_letter", 200, 1, 0},
		{"rate_limited", rateLimited, "fail", "routine_rate_limited", "queued", 400, 0, 0},
		{"disabled", disabledMsg, "refused", "routine_auth", "dead_letter", 400, 0, 1},
		{"unauthorized", invalidKey, "refused", "routine_auth", "dead_letter", 401, 0, 1},
		{"404", "", "refused", "http_4xx", "dead_letter", 404, 0, 0},
		{"429", "", "fail", "http_429", "queued", 429, 0, 0},
		{"503", "", "fail", "http_5xx", "queued", 503, 0, 0},
		{"network", "", "fail", "connection_closed", "queued", 0, 1, 0},
		{"capped_unparseable_200", strings.Repeat("x", hub.PushResponseCap+1), "ok", "", "accepted", 200, 1, 0},
		{"key_echo", grokKey + " private-response-canary", "refused", "routine_auth", "dead_letter", 401, 0, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			logs := captureSlog(t)
			f := setup(t, nil)
			f.addGrokbot(answer(max(tc.status, 200), tc.body))
			if tc.status == 0 {
				f.w = &push.Worker{Hub: f.env.Hub, Now: f.env.Now.Now, Client: &http.Client{Transport: t1RoundTrip(func(*http.Request) (*http.Response, error) { return nil, io.ErrUnexpectedEOF })}}
			}
			dly := f.send(f.aSec, f.msg("wake")).Deliveries[0].DeliveryID
			f.step()
			a := t1PushRows(t, f)
			if len(a) != 1 || a[0].delivery != dly || a[0].at != envelope.FormatTime(f.env.Now.Now()) ||
				a[0].outcome != tc.outcome || a[0].class.String != tc.class ||
				a[0].class.Valid != (tc.class != "") || a[0].status.Valid != (tc.status != 0) ||
				(a[0].status.Valid && a[0].status.Int64 != int64(tc.status)) {
				t.Fatalf("Grok observation = %+v", a)
			}
			spent := 1
			if tc.state == "accepted" {
				spent = 0
			}
			t1PushOutcome(t, f, dly, tc.state, spent)
			if f.routeRuns() != tc.runs || f.counter("push_auth_failed") != tc.auth {
				t.Fatal("Grok status-based reservation/release or auth counter changed")
			}
			var lastError sql.NullString
			if err := f.env.Hub.DB.QueryRow(`SELECT last_error FROM deliveries WHERE dly_id=?`, dly).Scan(&lastError); err != nil {
				t.Fatal(err)
			}
			diagnostics := fmt.Sprintf("%+v", a) + lastError.String + logs.String()
			for _, secret := range []string{grokKey, strings.TrimPrefix(grokKey, "crsr_"), "private-response-canary"} {
				if strings.Contains(diagnostics, secret) {
					t.Fatal("Grok observation, stored error or log exposed key/response content")
				}
			}
			if tc.auth != 0 && !strings.Contains(logs.String(), "push: grokbot") {
				t.Fatal("auth warning log capture was not live")
			}
			if tc.status != 0 {
				hits := f.rcv.Hits()
				if len(hits) != 1 || a[0].body != len(hits[0].Body) {
					t.Fatal("Grok body measurement differs from frozen request")
				}
			}
		})
	}
	t.Run("shared_account_budget", func(t *testing.T) {
		f := setup(t, nil)
		f.addGrokbot(started)
		f.seedRuns(300, 30*time.Minute)
		dly := f.send(f.aSec, f.msg("wake")).Deliveries[0].DeliveryID
		f.step()
		if len(f.rcv.Hits()) != 0 || len(t1PushRows(t, f)) != 0 || f.routeRuns() != 0 {
			t.Fatal("account deferral was observed or reserved")
		}
		if r := f.row(dly); r.State != "queued" || r.Attempt != 0 || r.AttemptsLeft != hub.PushAttempts {
			t.Fatal("account deferral changed attempt budget", r)
		}
	})
	t.Run("per_route_budget", func(t *testing.T) {
		f := setup(t, nil)
		f.addGrokbot(started)
		f.setRunsPerHour(1)
		f.send(f.aSec, f.msg("wake"))
		f.step()
		dly := f.send(f.aSec, f.msg("wake")).Deliveries[0].DeliveryID
		f.step()
		if len(f.rcv.Hits()) != 1 || len(t1PushRows(t, f)) != 1 || f.routeRuns() != 1 {
			t.Fatal("route budget deferral created another request/observation/run")
		}
		if r := f.row(dly); r.State != "queued" || r.Attempt != 0 || r.AttemptsLeft != hub.PushAttempts {
			t.Fatal("route budget deferral spent an attempt", r)
		}
	})
}

// A connector wraps the real SQLite driver. A telemetry Begin is identified by
// its detached <=1s context; insert/prune/commit faults require route_attempts
// SQL. No trigger or fault targets a settlement table.
type t1SQLFault struct {
	mu                        sync.Mutex
	stage                     string
	begins, inserts, hits     int
	deadline, mixedSettlement bool
	settlementDelay           time.Duration
}

type t1SQLConnector struct {
	driver driver.Driver
	dsn    string
	fault  *t1SQLFault
}

func (c t1SQLConnector) Driver() driver.Driver { return c.driver }
func (c t1SQLConnector) Connect(context.Context) (driver.Conn, error) {
	conn, err := c.driver.Open(c.dsn)
	if err != nil {
		return nil, err
	}
	return &t1SQLConn{Conn: conn, fault: c.fault}, nil
}

type t1SQLConn struct {
	driver.Conn
	fault     *t1SQLFault
	telemetry bool
}

func (c *t1SQLConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	c.telemetry = false
	if deadline, ok := ctx.Deadline(); ok && time.Until(deadline) <= time.Second {
		c.telemetry = true
		c.fault.mu.Lock()
		c.fault.begins++
		c.fault.deadline = ctx.Err() == nil && time.Until(deadline) > 0
		fail := c.fault.stage == "begin"
		if fail {
			c.fault.hits++
		}
		c.fault.mu.Unlock()
		if fail {
			c.telemetry = false
			return nil, errors.New("synthetic begin failure with private SQL canary")
		}
	}
	tx, err := c.Conn.(driver.ConnBeginTx).BeginTx(ctx, opts)
	if err != nil {
		c.telemetry = false
		return nil, err
	}
	return &t1SQLTx{Tx: tx, conn: c}, nil
}

func (c *t1SQLConn) QueryContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	c.checkSettlement(q)
	return c.Conn.(driver.QueryerContext).QueryContext(ctx, q, args)
}

func (c *t1SQLConn) checkSettlement(q string) {
	if c.telemetry && (strings.Contains(strings.ToLower(q), "deliveries") || strings.Contains(strings.ToLower(q), "event_outbox")) {
		c.fault.mu.Lock()
		c.fault.mixedSettlement = true
		c.fault.mu.Unlock()
	}
}

func (c *t1SQLConn) ExecContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Result, error) {
	c.checkSettlement(q)
	upper := strings.ToUpper(strings.TrimSpace(q))
	if strings.HasPrefix(upper, "UPDATE DELIVERIES") && c.fault.settlementDelay > 0 {
		time.Sleep(c.fault.settlementDelay)
	}
	if strings.Contains(upper, "ROUTE_ATTEMPTS") {
		c.fault.mu.Lock()
		insert := strings.HasPrefix(upper, "INSERT")
		prune := strings.HasPrefix(upper, "DELETE")
		if insert {
			c.fault.inserts++
		}
		fail := (insert && c.fault.stage == "insert") || (prune && c.fault.stage == "prune")
		if fail {
			c.fault.hits++
		}
		c.fault.mu.Unlock()
		if fail {
			return nil, errors.New("synthetic telemetry SQL failure")
		}
	}
	return c.Conn.(driver.ExecerContext).ExecContext(ctx, q, args)
}

type t1SQLTx struct {
	driver.Tx
	conn *t1SQLConn
}

func (tx *t1SQLTx) Commit() error {
	defer func() { tx.conn.telemetry = false }()
	if tx.conn.telemetry && tx.conn.fault.stage == "commit" {
		tx.conn.fault.mu.Lock()
		tx.conn.fault.hits++
		tx.conn.fault.mu.Unlock()
		// database/sql marks a driver commit error done: roll the real
		// transaction back before returning the injected failure.
		if err := tx.Tx.Rollback(); err != nil {
			return err
		}
		return errors.New("synthetic telemetry commit failure")
	}
	return tx.Tx.Commit()
}

func (tx *t1SQLTx) Rollback() error {
	tx.conn.telemetry = false
	return tx.Tx.Rollback()
}

func t1InterposePushSQL(t *testing.T, f *fixture, stage string) *t1SQLFault {
	t.Helper()
	original := f.env.Hub.DB.DB
	fault := &t1SQLFault{stage: stage}
	dsn := "file:" + filepath.Join(f.env.Hub.Cfg.DataDir, "hub.db") + "?_txlock=immediate&_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(ON)"
	wrapped := sql.OpenDB(t1SQLConnector{driver: original.Driver(), dsn: dsn, fault: fault})
	wrapped.SetMaxOpenConns(4)
	f.env.Hub.DB.DB = wrapped
	t.Cleanup(func() { wrapped.Close(); f.env.Hub.DB.DB = original })
	return fault
}

func TestRouteAttemptFailureCannotRollbackPushOutcome(t *testing.T) {
	for _, transport := range []string{"http", "grokbot"} {
		for _, status := range []int{200, 503, 410} {
			for _, stage := range []string{"begin", "insert", "prune", "commit"} {
				t.Run(fmt.Sprintf("%s/%d/%s", transport, status, stage), func(t *testing.T) {
					logs := captureSlog(t)
					f := setup(t, nil)
					if transport == "grokbot" {
						f.addGrokbot(answer(status, `{"success":true}`))
					} else {
						f.addRoute(always(status))
					}
					dly := f.send(f.aSec, f.msg("wake")).Deliveries[0].DeliveryID
					d := t1Claim(t, f)
					// Force pruning after an insert; rollback must preserve this row.
					if _, err := f.env.Hub.DB.Exec(`INSERT INTO route_attempts(route_id,delivery_id,at,outcome,body_bytes,latency_ms) VALUES(?, 'e1.1','2020-01-01T00:00:00.000Z','ok',0,0)`, f.route); err != nil {
						t.Fatal(err)
					}
					before := t1PushRows(t, f)
					fault := t1InterposePushSQL(t, f, stage)
					f.w.Push(f.ctx, d)
					state, spent := "accepted", 0
					if status == 503 {
						state, spent = "queued", 1
					} else if status == 410 {
						state, spent = "dead_letter", 1
					}
					t1PushOutcome(t, f, dly, state, spent)
					if len(f.rcv.Hits()) != 1 || !reflect.DeepEqual(before, t1PushRows(t, f)) {
						t.Fatal("telemetry failure retried payload or published partial telemetry")
					}
					fault.mu.Lock()
					hits, begins, deadline, mixed := fault.hits, fault.begins, fault.deadline, fault.mixedSettlement
					fault.mu.Unlock()
					if hits != 1 || begins != 1 || !deadline || mixed {
						t.Fatalf("fault reached/isolation = hits %d, begins %d, deadline %v, mixed %v", hits, begins, deadline, mixed)
					}
					if !strings.Contains(logs.String(), "route attempt telemetry failed") || strings.Contains(logs.String(), "synthetic") {
						t.Fatal("telemetry failure log missing or exposed raw driver text")
					}
					if transport == "grokbot" {
						wantRuns := 0
						if status == 200 {
							wantRuns = 1
						}
						if f.routeRuns() != wantRuns {
							t.Fatal("telemetry changed Grok reservation/release")
						}
					}
					if f.step() != 0 || len(f.rcv.Hits()) != 1 {
						t.Fatal("telemetry failure caused an immediate retry")
					}
				})
			}
		}
	}
}

func TestRouteAttemptStaleAndShutdownObservation(t *testing.T) {
	for _, transport := range []string{"http", "grokbot"} {
		for _, scenario := range []string{"stale", "shutdown", "claim_only"} {
			t.Run(transport+"/"+scenario, func(t *testing.T) {
				f := setup(t, nil)
				if transport == "grokbot" {
					f.addGrokbot(started)
				} else {
					f.addRoute(always(200))
				}
				dly := f.send(f.aSec, f.msg("wake")).Deliveries[0].DeliveryID
				d := t1Claim(t, f)
				before := f.row(dly)
				ctx, cancel := context.WithCancel(f.ctx)
				defer cancel()
				calls := 0
				transport := http.DefaultTransport.(*http.Transport).Clone()
				t.Cleanup(transport.CloseIdleConnections)
				f.w = &push.Worker{Hub: f.env.Hub, Now: f.env.Now.Now, Client: &http.Client{Transport: t1RoundTrip(func(req *http.Request) (*http.Response, error) {
					calls++
					resp, err := transport.RoundTrip(req)
					if err != nil {
						return nil, err
					}
					if scenario == "shutdown" {
						cancel()
						resp.Body.Close()
						return nil, context.Canceled
					}
					// Simulate an outcome that moved on while the request was in flight.
					if _, err := f.env.Hub.DB.Exec(`UPDATE deliveries SET state='accepted',attempt=attempt+1,last_status=202 WHERE dly_id=?`, dly); err != nil {
						t.Error(err)
					}
					return resp, nil
				})}}
				if scenario != "claim_only" {
					f.w.Push(ctx, d)
				}
				a := t1PushRows(t, f)
				want := 1
				if scenario == "claim_only" {
					want = 0
				}
				if calls != want || len(a) != want {
					t.Fatalf("actual Do/observation = %d/%d, want %d", calls, len(a), want)
				}
				if want == 1 && (a[0].delivery != dly || a[0].body != len(d.Body) || a[0].at != envelope.FormatTime(f.env.Now.Now())) {
					t.Fatal("stale/shutdown observation lost its payload identity or completion")
				}
				if scenario == "stale" {
					r := f.row(dly)
					if r.State != "accepted" || r.Attempt != d.Attempt+1 || r.LastStatus == nil ||
						*r.LastStatus != 202 || a[0].outcome != "ok" || !a[0].status.Valid ||
						a[0].status.Int64 != 200 || a[0].class.Valid {
						t.Fatal("stale observation reapplied outcome", r, a)
					}
				} else if !reflect.DeepEqual(before, f.row(dly)) {
					t.Fatal("shutdown/claim changed lease or spent another retry")
				}
				if scenario == "shutdown" && (a[0].outcome != "fail" || a[0].class.String != "canceled" || a[0].status.Valid) {
					t.Fatal("completed canceled Do was not observed", a)
				}
				if len(f.receiptsFor(f.a, dly)) != 0 || f.counter("dead_letters") != 0 || f.counter("push_conflicts") != 0 {
					t.Fatal("observation invented settlement effects")
				}
			})
		}
	}
}
