package events_test

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
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/events"
	"github.com/XertroV/vecnet/internal/hub"
	"github.com/XertroV/vecnet/internal/testhub"
	"github.com/XertroV/vecnet/internal/webhook"
	"modernc.org/sqlite"
)

// Source-only A3 draft; uncompiled and unrun. All receivers, keys and payloads
// are synthetic. Telemetry faults target only route_attempts; the separately
// labelled outcome-error case deliberately aborts Events settlement.

type t1EventAttempt struct {
	id                 int64
	event, at, outcome string
	status             sql.NullInt64
	class              sql.NullString
	body, latency      int
}

type t1EventRoundTrip func(*http.Request) (*http.Response, error)

func (fn t1EventRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) {
	return fn(r)
}

type t1EventDrain struct {
	io.ReadCloser
	clock  *testhub.FakeClock
	once   sync.Once
	read   int
	closed bool
}

func (r *t1EventDrain) Read(p []byte) (int, error) {
	r.once.Do(func() { time.Sleep(15 * time.Millisecond); r.clock.Advance(2 * time.Hour) })
	n, err := r.ReadCloser.Read(p)
	r.read += n
	return n, err
}

func (r *t1EventDrain) Close() error {
	r.closed = true
	return r.ReadCloser.Close()
}

func t1EventRows(t *testing.T, env *testhub.Env, sub string) []t1EventAttempt {
	t.Helper()
	rows, err := env.Hub.DB.Query(`SELECT id,delivery_id,at,outcome,http_status,error_class,body_bytes,latency_ms FROM route_attempts WHERE route_id=? ORDER BY id`, sub)
	if err != nil {
		t.Fatalf("query callback observations: %v", err)
	}
	defer rows.Close()
	out := []t1EventAttempt{}
	for rows.Next() {
		var a t1EventAttempt
		if err := rows.Scan(&a.id, &a.event, &a.at, &a.outcome, &a.status, &a.class, &a.body, &a.latency); err != nil {
			t.Fatal(err)
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func t1EventBody(t *testing.T, env *testhub.Env, sub, event string) []byte {
	t.Helper()
	var body string
	if err := env.Hub.DB.QueryRow(`SELECT body FROM event_outbox WHERE sub_id=? AND event_id=?`, sub, event).Scan(&body); err != nil {
		t.Fatal(err)
	}
	return []byte(body)
}

func t1EventState(t *testing.T, env *testhub.Env, dly string) string {
	t.Helper()
	var state string
	var attempt, left, dispatching, receipts int
	var next sql.NullString
	if err := env.Hub.DB.QueryRow(`SELECT state,attempt,attempts_left,dispatching,next_attempt_at FROM deliveries WHERE dly_id=?`, dly).Scan(&state, &attempt, &left, &dispatching, &next); err != nil {
		t.Fatal(err)
	}
	if attempt != 0 || left != hub.PushAttempts || dispatching != 0 || next.Valid {
		t.Fatal("Events changed the Delivery retry budget or retained a dispatch lease")
	}
	if err := env.Hub.DB.QueryRow(`SELECT count(*) FROM events WHERE type='receipt' AND json_extract(data,'$.delivery_id')=? AND json_extract(data,'$.state')='accepted'`, dly).Scan(&receipts); err != nil {
		t.Fatal(err)
	}
	if (state == "accepted" && receipts != 1) || (state != "accepted" && receipts != 0) {
		t.Fatal("Events acceptance receipt changed independently of Delivery state")
	}
	return state
}

func TestRouteAttemptEventsOutcomePopulation(t *testing.T) {
	for _, kind := range []string{"delivery", "receipt"} {
		for _, tc := range []struct {
			status         int
			outcome, class string
		}{
			{200, "ok", ""}, {204, "ok", ""},
			{401, "fail", "http_4xx"}, {409, "fail", "http_409"}, {307, "fail", "http_3xx"},
			{410, "refused", "http_410"}, {413, "refused", "http_413"}, {503, "fail", "http_5xx"},
			{0, "fail", "connection_closed"},
		} {
			t.Run(fmt.Sprintf("%s/%d", kind, tc.status), func(t *testing.T) {
				env := testhub.Start(t)
				peer, _ := env.NewPeer("session", "callback-recipient")
				_, sender := env.NewPeer("session", "sender")
				cb := testhub.NewCallback(t)
				sub := subscribe(t, env, cb, peer, map[string]any{"types": []string{kind}}, time.Time{})
				cb.OnPost(func(_ int, w http.ResponseWriter) {
					if tc.status != 0 {
						if tc.status == 307 {
							w.Header().Set("Location", cb.URL+"/must-not-follow")
						}
						w.WriteHeader(tc.status)
						if tc.status != 307 {
							_, _ = io.WriteString(w, strings.Repeat("r", 4113))
						}
						return
					}
					conn, _, err := w.(http.Hijacker).Hijack()
					if err != nil {
						t.Error(err)
						return
					}
					_ = conn.Close()
				})
				var event string
				if kind == "delivery" {
					event = send(t, env, sender, peer, "attempt-population", "wake", "synthetic callback body")
				} else {
					cur, err := env.Hub.Log.Append(context.Background(), nil, peer, "receipt", env.Now.Now(), map[string]any{"delivery_id": "dly_unrelated", "state": "processed"})
					if err != nil {
						t.Fatal(err)
					}
					event = cur.String()
					if !strings.HasPrefix(event, "e") {
						t.Fatal("non-delivery fixture lacks an event cursor")
					}
				}
				client := env.Hub.EgressPolicy().Client(events.ClientTimeout)
				t.Cleanup(client.CloseIdleConnections)
				var drained *t1EventDrain
				env.Events.Client = &http.Client{Timeout: client.Timeout, CheckRedirect: client.CheckRedirect, Transport: t1EventRoundTrip(func(req *http.Request) (*http.Response, error) {
					resp, err := client.Transport.RoundTrip(req)
					if err == nil && tc.status != 307 {
						drained = &t1EventDrain{ReadCloser: resp.Body, clock: env.Now}
						resp.Body = drained
					}
					return resp, err
				})}
				start := time.Now()
				n := step(t, env)
				elapsed := time.Since(start)
				posts := wantPosts(t, cb, 1)
				a := t1EventRows(t, env, sub.ID)
				if len(a) != 1 {
					t.Fatalf("one real callback produced %d observations", len(a))
				}
				r := a[0]
				if posts[0].ID != event || posts[0].Subscription != sub.ID {
					t.Fatal("observation identity differs from actual callback headers")
				}
				if r.event != event || r.outcome != tc.outcome || r.class.String != tc.class || r.class.Valid != (tc.class != "") || r.status.Valid != (tc.status != 0) || (r.status.Valid && r.status.Int64 != int64(tc.status)) {
					t.Fatalf("Events classifier = %+v; want %s/%s/%d", r, tc.outcome, tc.class, tc.status)
				}
				if r.body != len(posts[0].Body) || r.at != envelope.FormatTime(env.Now.Now()) || r.latency < 0 || r.latency > int(elapsed.Milliseconds())+2 {
					t.Fatal("callback byte/time measurement differs from actual request", r)
				}
				if tc.status != 0 && tc.status != 307 {
					wantRead := 4096
					if tc.status == 204 {
						wantRead = 0
					}
					if drained == nil || !drained.closed || drained.read != wantRead || r.latency < 14 {
						t.Fatal("callback completion omitted the bounded drain or close")
					}
				}
				attempts, done, terminal := outbox(t, env, sub.ID, event)
				wantAttempts, wantSent, wantFailures := 1, 0, 1
				wantDone, wantTerminal := false, ""
				if tc.outcome == "ok" {
					wantAttempts, wantSent, wantFailures, wantDone = 0, 1, 0, true
				} else if tc.outcome == "refused" {
					wantAttempts, wantFailures, wantDone, wantTerminal = 0, 0, true, fmt.Sprintf("http_%d", tc.status)
				}
				if n != wantSent || attempts != wantAttempts || done != wantDone || terminal != wantTerminal {
					t.Fatalf("Events settlement = sent %d/attempts %d/done %v/terminal %q", n, attempts, done, terminal)
				}
				wantEventsCounters(t, env, wantFailures, 0)
				if kind == "delivery" {
					wantState := "queued"
					if tc.outcome == "ok" {
						wantState = "accepted"
					}
					if state := t1EventState(t, env, event); state != wantState {
						t.Fatalf("Delivery state = %s, want %s", state, wantState)
					}
				}
				step(t, env)
				wantPosts(t, cb, 1)
				if len(t1EventRows(t, env, sub.ID)) != 1 {
					t.Fatal("no-Do step invented a row")
				}
			})
		}
	}
	t.Run("exhaustion_does_not_reclassify_the_request", func(t *testing.T) {
		env := testhub.Start(t)
		peer, _ := env.NewPeer("session", "exhaustion-recipient")
		_, sender := env.NewPeer("session", "sender")
		cb := testhub.NewCallback(t)
		sub := subscribe(t, env, cb, peer, nil, time.Time{})
		cb.OnPost(func(_ int, w http.ResponseWriter) { w.WriteHeader(503) })
		dly := send(t, env, sender, peer, "exhaustion", "wake", "retry bytes")
		step(t, env)
		if _, err := env.Hub.DB.Exec(`UPDATE event_outbox SET attempts=?,next_attempt_at=NULL WHERE sub_id=? AND event_id=?`, hub.PushAttempts-1, sub.ID, dly); err != nil {
			t.Fatal(err)
		}
		if _, err := env.Hub.DB.Exec(`UPDATE event_subscriptions SET next_attempt_at=NULL WHERE sub_id=?`, sub.ID); err != nil {
			t.Fatal(err)
		}
		step(t, env)
		posts := wantPosts(t, cb, 2)
		a := t1EventRows(t, env, sub.ID)
		if len(a) != 2 || a[1].outcome != "fail" || a[1].class.String != "http_5xx" || !a[1].status.Valid || a[1].status.Int64 != 503 {
			t.Fatal("exhaustion reclassified a retryable request as refusal", a)
		}
		if attempts, done, terminal := outbox(t, env, sub.ID, dly); attempts != hub.PushAttempts-1 || !done || terminal != "exhausted" || !bytes.Equal(posts[0].Body, posts[1].Body) || !bytes.Equal(posts[0].Body, t1EventBody(t, env, sub.ID, dly)) {
			t.Fatal("exhaustion changed request counting/frozen callback bytes")
		}
		wantEventsCounters(t, env, 2, 1)
	})
	t.Run("challenge_and_gating", func(t *testing.T) {
		for _, gate := range []string{"challenge", "hold", "held", "paused", "unverified", "expired", "unauthorized", "withdrawn"} {
			t.Run(gate, func(t *testing.T) {
				env := testhub.Start(t)
				peer, secret := env.NewPeer("session", "recipient")
				_, sender := env.NewPeer("session", "sender")
				cb := testhub.NewCallback(t)
				sub := subscribe(t, env, cb, peer, nil, time.Time{})
				priority := "wake"
				if gate == "hold" {
					priority = "hold"
				} else if gate == "held" {
					priority = "wake_idle"
					report(t, env, secret, "working")
				}
				dly := send(t, env, sender, peer, "gated", priority, "no callback")
				switch gate {
				case "challenge":
					if err := events.Challenge(context.Background(), env.Hub.EgressPolicy().Client(time.Second), sub, webhook.NewSecret(), env.Now.Now()); err != nil {
						t.Fatal(err)
					}
					if len(cb.Challenges()) != 1 || len(t1EventRows(t, env, sub.ID)) != 0 {
						t.Fatal("challenge created a payload observation")
					}
					return
				case "paused":
					if _, err := env.Hub.DB.Exec(`UPDATE event_subscriptions SET paused_reason='operator' WHERE sub_id=?`, sub.ID); err != nil {
						t.Fatal(err)
					}
				case "unverified":
					if _, err := env.Hub.DB.Exec(`UPDATE event_subscriptions SET verified_at=NULL WHERE sub_id=?`, sub.ID); err != nil {
						t.Fatal(err)
					}
				case "expired":
					if _, err := env.Hub.DB.Exec(`UPDATE event_subscriptions SET expires_at=? WHERE sub_id=?`, envelope.FormatTime(env.Now.Now().Add(-time.Second)), sub.ID); err != nil {
						t.Fatal(err)
					}
				case "unauthorized":
					if _, err := env.Hub.DB.Exec(`UPDATE peers SET disabled=1 WHERE peer_id=?`, peer); err != nil {
						t.Fatal(err)
					}
				case "withdrawn":
					if _, err := env.Hub.DB.Exec(`UPDATE deliveries SET state='expired' WHERE dly_id=?`, dly); err != nil {
						t.Fatal(err)
					}
				}
				step(t, env)
				wantPosts(t, cb, 0)
				if len(t1EventRows(t, env, sub.ID)) != 0 {
					t.Fatal("gated callback was measured as an actual request")
				}
			})
		}
	})
	t.Run("completed_canceled_client", func(t *testing.T) {
		env := testhub.Start(t)
		peer, _ := env.NewPeer("session", "cancel-recipient")
		_, sender := env.NewPeer("session", "sender")
		cb := testhub.NewCallback(t)
		sub := subscribe(t, env, cb, peer, nil, time.Time{})
		dly := send(t, env, sender, peer, "cancel", "wake", "completed canceled invocation")
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		calls := 0
		client := env.Hub.EgressPolicy().Client(events.ClientTimeout)
		t.Cleanup(client.CloseIdleConnections)
		env.Events.Client = &http.Client{Transport: t1EventRoundTrip(func(req *http.Request) (*http.Response, error) {
			calls++
			resp, err := client.Transport.RoundTrip(req)
			if err != nil {
				return nil, err
			}
			cancel()
			resp.Body.Close()
			return nil, context.Canceled
		})}
		if n, err := env.Events.Step(ctx); n != 0 || (err != nil && !errors.Is(err, context.Canceled)) {
			t.Fatalf("shutdown result = %d, %v", n, err)
		}
		a := t1EventRows(t, env, sub.ID)
		post := wantPosts(t, cb, 1)[0]
		if calls != 1 || len(a) != 1 || a[0].event != dly || a[0].outcome != "fail" || a[0].class.String != "canceled" || a[0].status.Valid {
			t.Fatal("completed canceled callback was lost or misclassified", a)
		}
		if attempts, done, terminal := outbox(t, env, sub.ID, dly); attempts != 0 || done || terminal != "" || len(t1EventBody(t, env, sub.ID, dly)) == 0 {
			t.Fatal("shutdown spent a retry or cleared frozen callback bytes")
		}
		if t1EventState(t, env, dly) != "queued" || a[0].body != len(post.Body) || a[0].at != envelope.FormatTime(env.Now.Now()) {
			t.Fatal("canceled completed Do lost measurement or changed authoritative Delivery")
		}
		wantEventsCounters(t, env, 0, 0)
	})
	t.Run("outcome_error_still_observes_actual_do", func(t *testing.T) {
		env := testhub.Start(t)
		peer, _ := env.NewPeer("session", "record-error-recipient")
		_, sender := env.NewPeer("session", "sender")
		cb := testhub.NewCallback(t)
		sub := subscribe(t, env, cb, peer, nil, time.Time{})
		dly := send(t, env, sender, peer, "record-error", "wake", "actual callback, failed settlement")
		// This is an explicit outcome-error fixture, distinct from the
		// telemetry-only faults in TelemetryFailurePreservesReplay below.
		if _, err := env.Hub.DB.Exec(`CREATE TRIGGER t1_record_error BEFORE UPDATE ON event_outbox BEGIN SELECT RAISE(ROLLBACK,'synthetic outcome error'); END`); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _, _ = env.Hub.DB.Exec(`DROP TRIGGER IF EXISTS t1_record_error`) })
		if _, err := env.Events.Step(context.Background()); err == nil {
			t.Fatal("outcome-error fault was not reached")
		}
		post := wantPosts(t, cb, 1)[0]
		a := t1EventRows(t, env, sub.ID)
		if len(a) != 1 || a[0].event != dly || a[0].outcome != "ok" || a[0].body != len(post.Body) {
			t.Fatal("actual callback observation was lost on record error", a)
		}
		if !bytes.Equal(t1EventBody(t, env, sub.ID, dly), post.Body) || t1EventState(t, env, dly) != "queued" {
			t.Fatal("observation reapplied failed settlement")
		}
	})
	t.Run("preview_measures_frozen_callback", func(t *testing.T) {
		env := testhub.Start(t)
		peer, _ := env.NewPeer("session", "preview-recipient")
		_, sender := env.NewPeer("session", "sender")
		cb := testhub.NewCallback(t)
		sub := subscribe(t, env, cb, peer, nil, time.Time{})
		dly := send(t, env, sender, peer, "preview", "wake", "é"+strings.Repeat("preview text ", 1000))
		var seq int64
		var raw string
		if err := env.Hub.DB.QueryRow(`SELECT event_seq FROM deliveries WHERE dly_id=?`, dly).Scan(&seq); err != nil {
			t.Fatal(err)
		}
		if err := env.Hub.DB.QueryRow(`SELECT data FROM events WHERE seq=?`, seq).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		var event map[string]any
		if err := json.Unmarshal([]byte(raw), &event); err != nil {
			t.Fatal(err)
		}
		event["message"].(map[string]any)["metadata"] = map[string]any{"pad": strings.Repeat("x", 300<<10)}
		grown, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := env.Hub.DB.Exec(`UPDATE events SET data=? WHERE seq=?`, string(grown), seq); err != nil {
			t.Fatal(err)
		}
		step(t, env)
		post := wantPosts(t, cb, 1)[0]
		a := t1EventRows(t, env, sub.ID)
		if len(a) != 1 || a[0].body != len(post.Body) || a[0].body >= len(grown) || a[0].body > hub.MaxPushBody {
			t.Fatal("preview measured source payload instead of actual callback", a)
		}
		var preview struct {
			Data struct {
				Preview string `json:"preview"`
			} `json:"data"`
		}
		if err := json.Unmarshal(post.Body, &preview); err != nil || len(preview.Data.Preview) == 0 || len(preview.Data.Preview) > 1024 {
			t.Fatal("preview fixture did not produce a bounded preview", err)
		}
	})
}

var t1EventFaultSerial atomic.Int64

func t1EventFault(t *testing.T, env *testhub.Env, operation string) *atomic.Int64 {
	t.Helper()
	hits := &atomic.Int64{}
	fn := fmt.Sprintf("t1_event_fault_%d", t1EventFaultSerial.Add(1))
	if err := sqlite.RegisterScalarFunction(fn, 0, func(*sqlite.FunctionContext, []driver.Value) (driver.Value, error) {
		hits.Add(1)
		return int64(1), nil
	}); err != nil {
		t.Fatal(err)
	}
	// modernc installs registered functions when a connection opens. Close
	// idle fixture connections so the trigger cannot miss its scalar hook.
	env.Hub.DB.SetMaxIdleConns(0)
	env.Hub.DB.SetMaxIdleConns(4)
	// The external scalar counter proves the failing telemetry statement was
	// reached even though RAISE(ROLLBACK) undoes every database write.
	if _, err := env.Hub.DB.Exec(`CREATE TRIGGER t1_event_failure BEFORE ` + operation + ` ON route_attempts BEGIN SELECT ` + fn + `(); SELECT RAISE(ROLLBACK,'synthetic telemetry failure'); END`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = env.Hub.DB.Exec(`DROP TRIGGER IF EXISTS t1_event_failure`) })
	return hits
}

func TestRouteAttemptEventsTelemetryFailurePreservesReplay(t *testing.T) {
	for _, operation := range []string{"INSERT", "DELETE"} {
		for _, outcome := range []string{"retry", "success", "permanent", "exhausted"} {
			t.Run(operation+"/"+outcome, func(t *testing.T) {
				env := testhub.Start(t)
				peer, _ := env.NewPeer("session", "replay-recipient")
				_, sender := env.NewPeer("session", "sender")
				cb := testhub.NewCallback(t)
				sub := subscribe(t, env, cb, peer, nil, time.Time{})
				cb.OnPost(func(_ int, w http.ResponseWriter) { w.WriteHeader(503) })
				dly := send(t, env, sender, peer, "frozen-replay", "wake", "frozen replay canary")
				step(t, env) // create the real frozen outbox bytes and one retry
				frozen := append([]byte(nil), wantPosts(t, cb, 1)[0].Body...)
				if !bytes.Equal(frozen, t1EventBody(t, env, sub.ID, dly)) || len(t1EventRows(t, env, sub.ID)) != 1 {
					t.Fatal("initial retry lost frozen bytes/observation")
				}
				// Controls remain outside the active subscription: pending and
				// parked bytes must survive; permanent settlement stays cleared.
				for _, control := range []struct {
					id, body string
					parked   int
					done     any
					terminal any
				}{
					{"e1.9001", ` { "pending":1.50 } `, 0, nil, nil},
					{"e1.9002", ` { "parked":2.50 } `, 1, nil, nil},
					{"e1.9003", "", 0, envelope.FormatTime(env.Now.Now()), "http_410"},
				} {
					if _, err := env.Hub.DB.Exec(`INSERT INTO event_outbox(sub_id,event_id,seq,body,parked,created_at,done_at,terminal) VALUES('sub_t1_control',?,?,?,?,?,?,?)`, control.id, 9000, control.body, control.parked, envelope.FormatTime(env.Now.Now()), control.done, control.terminal); err != nil {
						t.Fatal(err)
					}
				}
				controlSnapshot := func() string {
					var value string
					if err := env.Hub.DB.QueryRow(`SELECT json_group_array(json_array(event_id,seq,body,parked,created_at,done_at,terminal,attempts,next_attempt_at)) FROM (SELECT * FROM event_outbox WHERE sub_id='sub_t1_control' ORDER BY event_id)`).Scan(&value); err != nil {
						t.Fatal(err)
					}
					return value
				}
				controlsBefore := controlSnapshot()
				if _, err := env.Hub.DB.Exec(`INSERT INTO route_attempts(route_id,delivery_id,at,outcome,body_bytes,latency_ms) VALUES(?,'e1.1','2020-01-01T00:00:00.000Z','ok',0,0)`, sub.ID); err != nil {
					t.Fatal(err)
				}
				before := t1EventRows(t, env, sub.ID)
				if outcome == "exhausted" {
					if _, err := env.Hub.DB.Exec(`UPDATE event_outbox SET attempts=? WHERE sub_id=? AND event_id=?`, hub.PushAttempts-1, sub.ID, dly); err != nil {
						t.Fatal(err)
					}
				}
				for _, query := range []string{`UPDATE event_outbox SET next_attempt_at=NULL WHERE sub_id=?`, `UPDATE event_subscriptions SET next_attempt_at=NULL WHERE sub_id=?`} {
					if _, err := env.Hub.DB.Exec(query, sub.ID); err != nil {
						t.Fatal(err)
					}
				}
				status := 503
				if outcome == "success" {
					status = 200
				} else if outcome == "permanent" {
					status = 410
				}
				cb.OnPost(func(_ int, w http.ResponseWriter) { w.WriteHeader(status) })
				hits := t1EventFault(t, env, operation)
				sent := step(t, env)
				posts := wantPosts(t, cb, 2)
				if hits.Load() != 1 || !bytes.Equal(frozen, posts[1].Body) || !reflect.DeepEqual(before, t1EventRows(t, env, sub.ID)) {
					t.Fatal("fault not reached, request changed, or telemetry rollback failed", hits.Load())
				}
				attempts, done, terminal := outbox(t, env, sub.ID, dly)
				wantAttempts := 1
				if outcome == "retry" {
					wantAttempts = 2
				} else if outcome == "exhausted" {
					wantAttempts = hub.PushAttempts - 1
				}
				if attempts != wantAttempts {
					t.Fatal("telemetry changed outbox retry expenditure", attempts)
				}
				body := t1EventBody(t, env, sub.ID, dly)
				wantFailed, wantPaused := 1, 0 // initial 503 always counted
				switch outcome {
				case "success":
					if sent != 1 || !done || terminal != "" || len(body) != 0 || t1EventState(t, env, dly) != "accepted" {
						t.Fatal("telemetry undid successful Events acceptance")
					}
					var receipts int
					if err := env.Hub.DB.QueryRow(`SELECT count(*) FROM events WHERE owner=(SELECT sender FROM messages WHERE msg_id=(SELECT msg_id FROM deliveries WHERE dly_id=?)) AND type='receipt' AND json_extract(data,'$.delivery_id')=? AND json_extract(data,'$.state')='accepted'`, dly, dly).Scan(&receipts); err != nil || receipts != 1 {
						t.Fatalf("accepted receipt count = %d, %v", receipts, err)
					}
				case "permanent":
					if sent != 0 || !done || terminal != "http_410" || len(body) != 0 {
						t.Fatal("permanent settlement was changed")
					}
				case "retry":
					wantFailed = 2
					if sent != 0 || done || terminal != "" || !bytes.Equal(body, frozen) {
						t.Fatal("retry lost exact replay bytes")
					}
				case "exhausted":
					wantFailed, wantPaused = 2, 1
					if sent != 0 || !done || terminal != "exhausted" || !bytes.Equal(body, frozen) || get(t, env, sub.ID).PausedReason != events.PausedDeliveryFailed {
						t.Fatal("exhaustion lost exact replay bytes or pause")
					}
				}
				wantEventsCounters(t, env, wantFailed, wantPaused)
				if outcome != "success" && t1EventState(t, env, dly) != "queued" {
					t.Fatal("Events failure changed the authoritative Delivery")
				}
				for _, control := range []struct{ id, body string }{{"e1.9001", ` { "pending":1.50 } `}, {"e1.9002", ` { "parked":2.50 } `}, {"e1.9003", ""}} {
					if !bytes.Equal(t1EventBody(t, env, "sub_t1_control", control.id), []byte(control.body)) {
						t.Fatal("telemetry changed pending/parked/permanent control bytes")
					}
				}
				if controlSnapshot() != controlsBefore {
					t.Fatal("telemetry changed pending/parked/permanent control state")
				}
				if _, err := env.Hub.DB.Exec(`DROP TRIGGER t1_event_failure`); err != nil {
					t.Fatal(err)
				}
				cb.OnPost(nil)
				if outcome == "retry" || outcome == "exhausted" {
					if outcome == "exhausted" {
						// Change the source log to prove replay uses its retained
						// callback body rather than rebuilding it from current data.
						if _, err := env.Hub.DB.Exec(`UPDATE events SET data=json_set(data,'$.telemetry_test_changed',1) WHERE seq=(SELECT event_seq FROM deliveries WHERE dly_id=?)`, dly); err != nil {
							t.Fatal(err)
						}
						live := refresh(t, env, sub, time.Time{})
						cur, err := env.Events.Store.ReplayCursor(context.Background(), live.ID, live.Cursor)
						if err != nil {
							t.Fatal(err)
						}
						if err := env.Events.Store.SetCursor(context.Background(), live.ID, cur, false); err != nil {
							t.Fatal(err)
						}
					} else {
						env.Now.Advance(5 * time.Second)
					}
					if step(t, env) != 1 || !bytes.Equal(wantPosts(t, cb, 3)[2].Body, frozen) {
						t.Fatal("retry/explicit replay did not resend exact frozen bytes")
					}
					observed := t1EventRows(t, env, sub.ID)
					if len(observed) != 2 || observed[1].event != dly || observed[1].outcome != "ok" {
						t.Fatal("replay did not add exactly one observation for the same ID", observed)
					}
				} else {
					step(t, env)
					wantPosts(t, cb, 2)
					fresh := send(t, env, sender, peer, "after-telemetry-failure", "wake", "positive observation control")
					step(t, env)
					observed := t1EventRows(t, env, sub.ID)
					if len(observed) != 2 || observed[1].event != fresh || observed[1].outcome != "ok" {
						t.Fatal("observation did not recover after removing telemetry fault", observed)
					}
				}
			})
		}
	}
}
