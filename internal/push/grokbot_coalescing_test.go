package push_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/eventpayload"
	"github.com/XertroV/vecnet/internal/hub"
	"github.com/XertroV/vecnet/internal/push"
	"net/http"
	"reflect"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func sevenReleased(t *testing.T, f *fixture) []string {
	t.Helper()
	if _, err := f.env.Hub.DB.Exec(`UPDATE peers SET presence_state='working', presence_at=? WHERE peer_id=?`, f.env.Hub.Now().Format("2006-01-02T15:04:05.000Z"), f.b); err != nil {
		t.Fatal(err)
	}
	ids := make([]string, 0, 7)
	for range 7 {
		sr := f.send(f.aSec, f.msg("wake_idle"))
		ids = append(ids, sr.Deliveries[0].DeliveryID)
	}
	if err := f.env.Hub.OnIdle(f.ctx, f.b); err != nil {
		t.Fatal(err)
	}
	return ids
}

func TestMigratedLegacySevenReleaseRawBytes(t *testing.T) {
	f := setup(t, nil)
	f.addGrokbot(started)
	ids := sevenReleased(t, f)
	expected := map[string]bool{}
	for _, id := range ids {
		expected[string(f.eventData(f.b, id))] = true
	}
	claimed := f.step()
	if got := len(f.rcv.Hits()); got != 7 || claimed != 7 {
		t.Fatalf("POSTs=%d claims=%d, want 7/7", got, claimed)
	}
	for _, hit := range f.rcv.Hits() {
		if !expected[string(hit.Body)] {
			t.Fatalf("legacy body changed: %s", hit.Body)
		}
	}
}

func optIn(t *testing.T, f *fixture) {
	t.Helper()
	transport := hub.GrokbotTransportRecipientDrainV1
	if err := f.env.Hub.UpdateRoute(f.ctx, f.route, hub.RouteUpdate{GrokbotTransport: &transport}); err != nil {
		t.Fatal(err)
	}
}

func TestGrokDrainV1SevenReleaseOnePOST(t *testing.T) {
	f := setup(t, nil)
	f.addGrokbot(started)
	optIn(t, f)
	ids := sevenReleased(t, f)
	before := map[string]dlyRow{}
	for _, id := range ids {
		before[id] = f.row(id)
	}
	claimed := f.step()
	hits := f.rcv.Hits()
	if len(hits) != 1 || claimed != 1 {
		t.Fatalf("opted-in receiver POSTs=%d claims=%d, want 1/1", len(hits), claimed)
	}
	var wire struct {
		Type     string          `json:"type"`
		Version  int             `json:"version"`
		Delivery json.RawMessage `json:"delivery"`
		Count    int             `json:"pending_count"`
		Hint     string          `json:"drain_hint"`
	}
	if err := json.Unmarshal(hits[0].Body, &wire); err != nil {
		t.Fatal(err)
	}
	var inner struct {
		ID string `json:"delivery_id"`
	}
	json.Unmarshal(wire.Delivery, &inner)
	if wire.Type != "grokbot_wake" || wire.Version != 1 || wire.Count != 7 || wire.Hint != hub.GrokbotDrainHint {
		t.Fatalf("envelope=%s", hits[0].Body)
	}
	if !bytes.Equal(wire.Delivery, f.eventData(f.b, inner.ID)) {
		t.Fatal("inner Delivery bytes changed")
	}
	expected := []string{}
	for _, id := range ids {
		if id == inner.ID {
			continue
		}
		expected = append(expected, id)
		row := f.row(id)
		if row.State != "queued" || row.Attempt != 0 || row.AttemptsLeft != before[id].AttemptsLeft || len(f.receiptsFor(f.a, id)) != 0 {
			t.Fatalf("follower changed: %s %+v", id, row)
		}
		if len(f.eventData(f.b, id)) == 0 {
			t.Fatal("follower left inbox")
		}
	}
	rows, err := f.env.Hub.DB.Query(`SELECT delivery_id FROM grokbot_covers WHERE route_id=? ORDER BY delivery_id`, f.route)
	if err != nil {
		t.Fatal(err)
	}
	actual := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		actual = append(actual, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	sort.Strings(expected)
	if !reflect.DeepEqual(actual, expected) {
		t.Fatalf("covered=%v want exact followers %v", actual, expected)
	}
	if f.routeRuns() != 1 {
		t.Fatal("followers spent run budgets")
	}
	var bodyBytes, attempts int
	if err := f.env.Hub.DB.QueryRow(`SELECT count(*),coalesce(max(body_bytes),0) FROM route_attempts WHERE route_id=?`, f.route).Scan(&attempts, &bodyBytes); err != nil {
		t.Fatal(err)
	}
	if attempts != 1 || bodyBytes != len(hits[0].Body) {
		t.Fatalf("observation=%d/%d, body=%d", attempts, bodyBytes, len(hits[0].Body))
	}
	for _, priority := range []string{"wake", "wake_idle"} {
		f.send(f.aSec, f.msg(priority))
	}
	if n := f.step(); n != 0 || len(f.rcv.Hits()) != 1 {
		t.Fatal("recent arrivals bypass cover")
	}
	f.env.Now.Advance(hub.PushLease + time.Millisecond)
	if n := f.step(); n != 1 || len(f.rcv.Hits()) != 2 {
		t.Fatalf("expiry claims=%d POSTs=%d", n, len(f.rcv.Hits()))
	}
}

func TestMixedLegacyAndTokenReleaseSameRouteTime(t *testing.T) {
	f := setup(t, nil)
	f.addGrokbot(started)
	at := f.env.Now.Now()
	for _, token := range []any{"first-token", nil, "sentinel-token"} {
		if _, err := f.env.Hub.DB.Exec(`INSERT INTO route_runs(route_id,at,grok_wake_token) VALUES (?,?,?)`, f.route, at.Format("2006-01-02T15:04:05.000Z"), token); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.env.Hub.ReleaseGrokbotRun(f.ctx, f.route, at); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := f.env.Hub.DB.QueryRow(`SELECT count(*) FROM route_runs WHERE grok_wake_token='first-token'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatal("legacy release removed token-owned reservation")
	}
	// A repeated legacy release has no null row left to select.
	if err := f.env.Hub.ReleaseGrokbotRun(f.ctx, f.route, at); err != nil {
		t.Fatal(err)
	}
	wrong := hub.DueDelivery{RouteID: "other_route", Recipient: f.b, GrokWake: &hub.GrokWakeClaim{Token: "first-token"}}
	if err := f.env.Hub.GrokbotWakePushResult(f.ctx, wrong, 503, nil, 0, nil, at); err != nil {
		t.Fatal(err)
	}
	if f.routeRuns() != 2 {
		t.Fatal("wrong route or repeated legacy release consumed a token")
	}
	own := hub.DueDelivery{RouteID: f.route, Recipient: f.b, GrokWake: &hub.GrokWakeClaim{Token: "first-token"}}
	for range 2 {
		if err := f.env.Hub.GrokbotWakePushResult(f.ctx, own, 503, nil, 0, nil, at); err != nil {
			t.Fatal(err)
		}
	}
	if f.routeRuns() != 1 {
		t.Fatal("token release removed sentinel or was not idempotent")
	}
	var token string
	if err := f.env.Hub.DB.QueryRow(`SELECT grok_wake_token FROM route_runs WHERE route_id=?`, f.route).Scan(&token); err != nil || token != "sentinel-token" {
		t.Fatalf("sentinel=%q %v", token, err)
	}
}

func drainFixture(t *testing.T, respond func(int, hit) reply) *fixture {
	t.Helper()
	f := setup(t, nil)
	f.addGrokbot(respond)
	optIn(t, f)
	return f
}

func groupDeadline(t *testing.T, f *fixture) time.Time {
	t.Helper()
	var value string
	if err := f.env.Hub.DB.QueryRow(`SELECT max(lease_until,retry_at,cover_until) FROM grokbot_wakes WHERE route_id=? AND recipient=?`, f.route, f.b).Scan(&value); err != nil {
		t.Fatal(err)
	}
	at, err := envelope.ParseTime(value)
	if err != nil {
		t.Fatal(err)
	}
	return at
}
func advanceGroup(t *testing.T, f *fixture) {
	t.Helper()
	f.env.Now.Advance(groupDeadline(t, f).Sub(f.env.Now.Now()) + time.Millisecond)
}
func drainClaim(t *testing.T, f *fixture) hub.DueDelivery {
	t.Helper()
	due, err := f.env.Hub.ClaimDue(f.ctx, f.env.Now.Now(), 16)
	if err != nil || len(due) != 1 || due[0].GrokWake == nil {
		t.Fatalf("claim=%+v err=%v", due, err)
	}
	return due[0]
}

func TestGrokTerminalThenValidArrival(t *testing.T) {
	for _, status := range []int{400, 401, 200} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			f := drainFixture(t, func(n int, h hit) reply {
				if n == 0 {
					return reply{Status: status, Body: `{"success":false,"error":"bad payload"}`}
				}
				return started(n, h)
			})
			first := f.send(f.aSec, f.msg("wake")).Deliveries[0].DeliveryID
			if n := f.step(); n != 1 || f.row(first).State != "dead_letter" {
				t.Fatalf("terminal claim=%d row=%+v", n, f.row(first))
			}
			deadline := groupDeadline(t, f)
			f.send(f.aSec, f.msg("wake"))
			if n := f.step(); n != 0 || len(f.rcv.Hits()) != 1 {
				t.Fatal("valid arrival bypassed terminal cadence")
			}
			if !groupDeadline(t, f).Equal(deadline) {
				t.Fatal("arrival slid deadline")
			}
			advanceGroup(t, f)
			if n := f.step(); n != 1 || len(f.rcv.Hits()) != 2 {
				t.Fatal("domain permanently blocked")
			}
		})
	}
}

func TestGrokLeaderDiscardExpiryCollectionKeepsDeadline(t *testing.T) {
	for _, operation := range []string{"discard", "expiry", "collection"} {
		t.Run(operation, func(t *testing.T) {
			f := drainFixture(t, answer(429, rateLimited))
			ids := sevenReleased(t, f)
			f.step()
			deadline := groupDeadline(t, f)
			var leader string
			if err := f.env.Hub.DB.QueryRow(`SELECT representative_id FROM grokbot_wakes WHERE route_id=?`, f.route).Scan(&leader); err != nil {
				t.Fatal(err)
			}
			var q string
			switch operation {
			case "discard":
				q = `UPDATE deliveries SET state='discarded' WHERE dly_id=?`
			case "expiry":
				q = `UPDATE deliveries SET state='expired',expires_at='2000-01-01T00:00:00.000Z' WHERE dly_id=?`
			case "collection":
				q = `DELETE FROM deliveries WHERE dly_id=?`
			}
			if _, err := f.env.Hub.DB.Exec(q, leader); err != nil {
				t.Fatal(err)
			}
			if n := f.step(); n != 0 || len(f.rcv.Hits()) != 1 {
				t.Fatal("leader removal bypassed cadence")
			}
			if !groupDeadline(t, f).Equal(deadline) {
				t.Fatal("removal changed deadline")
			}
			advanceGroup(t, f)
			if n := f.step(); n != 1 || len(f.rcv.Hits()) != 2 {
				t.Fatalf("no follower rotation: claims=%d ids=%v", n, ids)
			}
			var ordinal int
			if err := f.env.Hub.DB.QueryRow(`SELECT failure_ordinal FROM grokbot_wakes WHERE route_id=?`, f.route).Scan(&ordinal); err != nil || ordinal != 2 {
				t.Fatalf("failure cadence reset: %d %v", ordinal, err)
			}
		})
	}
}

func TestGrok429FollowerCannotBypass(t *testing.T) {
	f := drainFixture(t, func(int, hit) reply {
		return reply{Status: 429, Body: rateLimited, Header: map[string]string{"Retry-After": "120"}}
	})
	sevenReleased(t, f)
	f.step()
	deadline := groupDeadline(t, f)
	if delay := deadline.Sub(f.env.Now.Now()); delay < 120*time.Second || delay > 132*time.Second {
		t.Fatalf("Retry-After=%v", delay)
	}
	for range 3 {
		f.send(f.aSec, f.msg("wake"))
		if n := f.step(); n != 0 {
			t.Fatal("follower bypassed Retry-After")
		}
	}
	if !groupDeadline(t, f).Equal(deadline) || f.routeRuns() != 0 {
		t.Fatal("429 changed cadence or retained refused run")
	}
	advanceGroup(t, f)
	if n := f.step(); n != 1 || len(f.rcv.Hits()) != 2 {
		t.Fatal("retry did not elect one representative")
	}
}

func TestGrokAmbiguousRestartLateToken(t *testing.T) {
	f := drainFixture(t, started)
	sevenReleased(t, f)
	first := drainClaim(t, f)
	var calls atomic.Int32
	f.w = &push.Worker{Hub: f.env.Hub, Now: f.env.Now.Now, Client: &http.Client{Transport: t1RoundTrip(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return nil, errors.New("synthetic ambiguous network")
	})}}
	f.w.Push(f.ctx, first)
	deadline := groupDeadline(t, f)
	if calls.Load() != 1 || f.routeRuns() != 1 {
		t.Fatal("ambiguity did not retain own reservation")
	}
	f.w = &push.Worker{Hub: f.env.Hub, Now: f.env.Now.Now}
	if n := f.step(); n != 0 {
		t.Fatal("restart bypassed original lease")
	}
	if deadline.Before(f.env.Now.Now().Add(hub.PushLease)) {
		t.Fatal("ambiguity shortened lease")
	}
	advanceGroup(t, f)
	second := drainClaim(t, f)
	if second.GrokWake.Token == first.GrokWake.Token {
		t.Fatal("reused fencing token")
	}
	f.w.Push(f.ctx, second)
	before := groupDeadline(t, f)
	for range 2 {
		if err := f.env.Hub.GrokbotWakePushResult(f.ctx, first, 200, []byte(`{"success":true}`), 0, nil, f.env.Now.Now()); err != nil {
			t.Fatal(err)
		}
	}
	if !groupDeadline(t, f).Equal(before) || f.routeRuns() != 2 || len(f.rcv.Hits()) != 1 {
		t.Fatal("late token mutated replacement")
	}
	// Once a definitive late HTTP 200 is observed, a contradictory duplicate
	// refusal cannot refund that reservation either.
	if err := f.env.Hub.GrokbotWakePushResult(f.ctx, first, 503, nil, 0, nil, f.env.Now.Now()); err != nil {
		t.Fatal(err)
	}
	if f.routeRuns() != 2 {
		t.Fatal("contradictory late refusal released a settled reservation")
	}
	if err := f.env.Hub.GrokbotWakePushResult(f.ctx, second, 200, []byte(`{"success":true}`), 0, nil, f.env.Now.Now()); err != nil {
		t.Fatal(err)
	}
	if !groupDeadline(t, f).Equal(before) {
		t.Fatal("double settlement extended cover")
	}
}

func TestGrokNoClientDeferCompensation(t *testing.T) {
	for _, failure := range []string{"key", "body", "request", "budget", "cancel"} {
		t.Run(failure, func(t *testing.T) {
			f := drainFixture(t, started)
			ids := sevenReleased(t, f)
			if failure == "key" {
				if _, err := f.env.Hub.DB.Exec(`UPDATE routes SET key_enc='v1:AAAA' WHERE route_id=?`, f.route); err != nil {
					t.Fatal(err)
				}
			}
			if failure == "budget" {
				f.setRunsPerHour(0)
			}
			d := drainClaim(t, f)
			ctx := f.ctx
			switch failure {
			case "body":
				d.Body = bytes.Repeat([]byte("x"), hub.MaxPushBody+1)
			case "request":
				d.URL = ":bad"
			case "cancel":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			f.w.Push(ctx, d)
			row := f.row(d.DeliveryID)
			if len(f.rcv.Hits()) != 0 || f.routeRuns() != 0 || row.State != "queued" || row.Attempt != 0 {
				t.Fatalf("noninvocation: POSTs=%d budget=%d row=%+v", len(f.rcv.Hits()), f.routeRuns(), row)
			}
			for _, id := range ids {
				if len(f.receiptsFor(f.a, id)) != 0 {
					t.Fatal("preflight invented receipt")
				}
			}
			if n := f.step(); n != 0 {
				t.Fatal("preflight rotated follower per poll")
			}
		})
	}
}

func TestGrokTwoWorkersOneDomain(t *testing.T) {
	entered, release := make(chan struct{}, 1), make(chan struct{})
	f := drainFixture(t, func(n int, h hit) reply { entered <- struct{}{}; <-release; return started(n, h) })
	sevenReleased(t, f)
	done := make(chan error, 1)
	go func() { _, err := f.w.Step(f.ctx); done <- err }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("no POST")
	}
	w2 := &push.Worker{Hub: f.env.Hub, Now: f.env.Now.Now}
	f.send(f.aSec, f.msg("wake"))
	n, err := w2.Step(f.ctx)
	close(release)
	if firstErr := <-done; firstErr != nil {
		t.Fatal(firstErr)
	}
	if err != nil || n != 0 || len(f.rcv.Hits()) != 1 || f.routeRuns() != 1 {
		t.Fatalf("overlap: claims=%d err=%v POSTs=%d", n, err, len(f.rcv.Hits()))
	}
}

func TestGrokOneConnectionHydrationRollback(t *testing.T) {
	f := drainFixture(t, started)
	f.env.Hub.Cfg.DeliveryReferences = true
	ids := sevenReleased(t, f)
	f.env.Hub.DB.SetMaxOpenConns(1)
	var original string
	if err := f.env.Hub.DB.QueryRow(`SELECT body_sha256 FROM deliveries WHERE dly_id=?`, ids[0]).Scan(&original); err != nil {
		t.Fatal(err)
	}
	if _, err := f.env.Hub.DB.Exec(`UPDATE deliveries SET body_sha256='' WHERE dly_id=?`, ids[0]); err != nil {
		t.Fatal(err)
	}
	due, err := f.env.Hub.ClaimDue(f.ctx, f.env.Now.Now(), 16)
	var storage *eventpayload.StorageError
	if !errors.As(err, &storage) || len(due) != 0 {
		t.Fatalf("hydration should fail atomically: %v %+v", err, due)
	}
	for _, table := range []string{"grokbot_wakes", "grokbot_covers"} {
		var count int
		if err := f.env.Hub.DB.QueryRow(`SELECT count(*) FROM ` + table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("rollback leaked %s=%d %v", table, count, err)
		}
	}
	if _, err := f.env.Hub.DB.Exec(`UPDATE deliveries SET body_sha256=? WHERE dly_id=?`, original, ids[0]); err != nil {
		t.Fatal(err)
	}
	if n := f.step(); n != 1 || len(f.rcv.Hits()) != 1 {
		t.Fatal("one-connection claim did not recover")
	}
}

func TestGrokOver16SameThreadContinuation(t *testing.T) {
	f := drainFixture(t, started)
	first := f.send(f.aSec, f.msg("wake"))
	ids := []string{first.Deliveries[0].DeliveryID}
	for range 40 {
		body := f.msg("wake")
		delete(body, "recipients")
		body["thread_id"] = first.ThreadID
		body["in_reply_to"] = first.MessageID
		ids = append(ids, f.send(f.aSec, body).Deliveries[0].DeliveryID)
	}
	if n := f.step(); n != 1 || len(f.rcv.Hits()) != 1 {
		t.Fatal("same-thread not coalesced")
	}
	var wire struct {
		Count int `json:"pending_count"`
	}
	json.Unmarshal(f.rcv.Hits()[0].Body, &wire)
	if wire.Count != 41 {
		t.Fatalf("count capped at page size: %d", wire.Count)
	}
	var n int
	if err := f.env.Hub.DB.QueryRow(`SELECT count(*) FROM grokbot_covers WHERE route_id=?`, f.route).Scan(&n); err != nil || n != 40 {
		t.Fatalf("coverage=%d %v", n, err)
	}
	for _, id := range ids[1:] {
		if f.row(id).Attempt != 0 {
			t.Fatal("same-thread follower attempted")
		}
	}
}

func TestGrokAttachmentPageDoesNotStarve(t *testing.T) {
	f := drainFixture(t, started)
	f.send(f.aSec, f.msg("wake"))
	f.step()
	for range 45 {
		f.send(f.aSec, f.msg("wake_idle"))
	}
	// A later domain on the same shared routine URL has independent authority.
	other, _ := f.env.NewPeer("session", "later-domain")
	route, err := f.env.Hub.AddGrokbotRouteWithTransport(f.ctx, "peer", other, f.rcv.srv.URL, grokKey, hub.GrokbotTransportRecipientDrainV1)
	if err != nil {
		t.Fatal(err)
	}
	body := f.msg("wake")
	body["recipients"] = []string{other}
	f.send(f.aSec, body)
	if n := f.step(); n != 1 || len(f.rcv.Hits()) != 2 {
		t.Fatalf("attachment-only pages starved later route %s: claims=%d POSTs=%d", route, n, len(f.rcv.Hits()))
	}
}

func TestGrokFrozenEnvelopeInlineReferenceRetry(t *testing.T) {
	for _, references := range []bool{false, true} {
		t.Run(fmt.Sprintf("reference=%v", references), func(t *testing.T) {
			f := drainFixture(t, func(n int, h hit) reply {
				if n == 0 {
					return reply{Status: 503}
				}
				return started(n, h)
			})
			body := f.msg("wake")
			body["content"] = map[string]any{"text": "frozen < & / escaped"}
			sent := f.send(f.aSec, body)
			id := sent.Deliveries[0].DeliveryID
			original := f.eventData(f.b, id)
			var spaced bytes.Buffer
			if err := json.Indent(&spaced, original, "", "  "); err != nil {
				t.Fatal(err)
			}
			wire := bytes.ReplaceAll(spaced.Bytes(), []byte(`\u003c`), []byte(`\u003C`))
			wire = bytes.ReplaceAll(wire, []byte("/ escaped"), []byte(`\/ escaped`))
			wire = append(append([]byte(" \n"), wire...), []byte("\n ")...)
			if bytes.Equal(wire, original) || !json.Valid(wire) {
				t.Fatal("whitespace/escape fixture not distinct valid JSON")
			}
			sum := sha256.Sum256(wire)
			err := f.env.Hub.DB.Tx(f.ctx, func(tx *sql.Tx) error {
				if _, err := tx.Exec(`UPDATE deliveries SET body_sha256=? WHERE dly_id=?`, hex.EncodeToString(sum[:]), id); err != nil {
					return err
				}
				p := eventpayload.StoredPayload{Format: eventpayload.Inline, Data: wire}
				if references {
					var err error
					p, err = eventpayload.EncodeDelivery(f.ctx, tx, sent.MessageID, wire)
					if err != nil {
						return err
					}
					if p.Format != eventpayload.DeliveryRefV1 {
						return errors.New("fixture did not encode reference")
					}
				}
				_, err := tx.Exec(`UPDATE events SET data=?,payload_format=?,msg_id=NULLIF(?,''),snapshot_id=NULLIF(?,'') WHERE seq=(SELECT event_seq FROM deliveries WHERE dly_id=?)`, string(p.Data), p.Format, p.MsgID, p.SnapshotID, id)
				return err
			})
			if err != nil {
				t.Fatal(err)
			}
			f.env.Hub.DB.SetMaxOpenConns(1)
			f.step()
			first := f.rcv.Hits()[0].Body
			var envelope struct {
				Delivery json.RawMessage `json:"delivery"`
				Count    int             `json:"pending_count"`
			}
			if err := json.Unmarshal(first, &envelope); err != nil {
				t.Fatal(err)
			}
			// RawMessage drops whitespace surrounding the JSON value. Independently
			// extract the physical nested byte region to verify all original bytes,
			// including root padding, instead of trusting its Go type.
			prefix := []byte(`{"type":"grokbot_wake","version":1,"delivery":`)
			end := bytes.LastIndex(first, []byte(`,"pending_count":`))
			if !bytes.HasPrefix(first, prefix) || end < len(prefix) {
				t.Fatal("invalid frozen frame")
			}
			extracted := first[len(prefix):end]
			if !bytes.Equal(extracted, wire) || sha256.Sum256(extracted) != sum || !bytes.Equal(envelope.Delivery, bytes.TrimSpace(wire)) || envelope.Count != 1 {
				t.Fatalf("inner byte/hash preservation failed: got=%q want=%q", extracted, wire)
			}
			f.send(f.aSec, f.msg("wake"))
			advanceGroup(t, f)
			// A fresh worker reclaims the same representative's durable snapshot.
			f.w = &push.Worker{Hub: f.env.Hub, Now: f.env.Now.Now}
			if n := f.step(); n != 1 || len(f.rcv.Hits()) != 2 {
				t.Fatal("retry did not POST once")
			}
			if !bytes.Equal(first, f.rcv.Hits()[1].Body) {
				t.Fatal("retry rewrote count or frozen inline/reference bytes")
			}
			if f.row(id).Attempt != 2 {
				t.Fatal("representative claim attempts changed")
			}
		})
	}
}

func TestGrokFullEnvelopeBodyCap(t *testing.T) {
	f := drainFixture(t, started)
	id := f.send(f.aSec, f.msg("wake")).Deliveries[0].DeliveryID
	// The old raw body fits exactly; adding the opted-in envelope must be
	// checked as a whole before reserving a run or entering the client.
	raw := []byte(`{"synthetic":"` + strings.Repeat("x", hub.MaxPushBody-len(`{"synthetic":""}`)) + `"}`)
	if len(raw) != hub.MaxPushBody {
		t.Fatalf("fixture length=%d", len(raw))
	}
	if _, err := f.env.Hub.DB.Exec(`UPDATE events SET data=? WHERE seq=(SELECT event_seq FROM deliveries WHERE dly_id=?)`, string(raw), id); err != nil {
		t.Fatal(err)
	}
	d := drainClaim(t, f)
	if len(d.Body) <= hub.MaxPushBody {
		t.Fatal("envelope did not exceed cap")
	}
	f.w.Push(f.ctx, d)
	if len(f.rcv.Hits()) != 0 || f.routeRuns() != 0 || len(t1PushRows(t, f)) != 0 || f.row(id).Attempt != 0 {
		t.Fatal("oversize envelope invoked or spent attempt/budget")
	}
}

func TestGrokSharedRouteEnableLegacyInFlight(t *testing.T) {
	f := setup(t, nil)
	f.addGrokbot(started)
	var prn string
	if err := f.env.Hub.DB.QueryRow(`SELECT prn_id FROM peers WHERE peer_id=?`, f.b).Scan(&prn); err != nil {
		t.Fatal(err)
	}
	other, _ := f.env.NewPeer("session", "shared-other")
	if _, err := f.env.Hub.DB.Exec(`UPDATE peers SET prn_id=?,route_id=? WHERE peer_id=?`, prn, f.route, other); err != nil {
		t.Fatal(err)
	}
	if _, err := f.env.Hub.DB.Exec(`UPDATE routes SET owner_kind='principal',owner_id=? WHERE route_id=?`, prn, f.route); err != nil {
		t.Fatal(err)
	}
	legacyID := f.send(f.aSec, f.msg("wake")).Deliveries[0].DeliveryID
	due, err := f.env.Hub.ClaimDue(f.ctx, f.env.Now.Now(), 16)
	if err != nil || len(due) != 1 || due[0].GrokWake != nil {
		t.Fatalf("legacy claim=%v %v", due, err)
	}
	legacy := due[0]
	optIn(t, f)
	f.send(f.aSec, f.msg("wake"))
	body := f.msg("wake")
	body["recipients"] = []string{other}
	f.send(f.aSec, body)
	opted := drainClaim(t, f)
	if opted.Recipient != other {
		t.Fatal("legacy domain barrier did not isolate recipients")
	}
	ok, _, err := f.env.Hub.BeginGrokbotPOST(f.ctx, opted, f.env.Now.Now())
	if err != nil || !ok {
		t.Fatalf("Begin=%v %v", ok, err)
	}
	// Token reservation precedes null reservation at identical route/time.
	ok, _ = f.env.Hub.GrokbotBudgetOK(f.ctx, f.route, f.env.Now.Now())
	if !ok {
		t.Fatal("shared budgets refused second population")
	}
	if f.routeRuns() != 2 {
		t.Fatal("budget omitted a reservation population")
	}
	if err := f.env.Hub.ReleaseGrokbotRun(f.ctx, f.route, f.env.Now.Now()); err != nil {
		t.Fatal(err)
	}
	if f.routeRuns() != 1 {
		t.Fatal("legacy release wrong population")
	}
	if err := f.env.Hub.GrokbotPushResult(f.ctx, legacy, 200, []byte(`{"success":true}`), 0, nil, f.env.Now.Now()); err != nil {
		t.Fatal(err)
	}
	if f.row(legacyID).State != "accepted" {
		t.Fatal("old legacy result stopped settling")
	}
	if err := f.env.Hub.GrokbotWakePushResult(f.ctx, opted, 503, nil, 0, nil, f.env.Now.Now()); err != nil {
		t.Fatal(err)
	}
	if f.routeRuns() != 0 {
		t.Fatal("opted exact-token release did not return its reservation")
	}
	// A legacy 200 cannot turn its imported lease into a successful drain cover.
	var cover string
	if err := f.env.Hub.DB.QueryRow(`SELECT cover_until FROM grokbot_wakes WHERE route_id=? AND recipient=?`, f.route, f.b).Scan(&cover); err != nil || cover != "" {
		t.Fatalf("legacy 200 inferred support: %q %v", cover, err)
	}
}

func TestGrokTransportRevocationClaimsAndLease(t *testing.T) {
	for _, phase := range []string{"claimed", "armed", "covered"} {
		t.Run(phase, func(t *testing.T) {
			f := drainFixture(t, started)
			sevenReleased(t, f)
			d := drainClaim(t, f)
			if phase != "claimed" {
				ok, _, err := f.env.Hub.BeginGrokbotPOST(f.ctx, d, f.env.Now.Now())
				if err != nil || !ok {
					t.Fatal(err)
				}
			}
			if phase == "covered" {
				if err := f.env.Hub.GrokbotWakePushResult(f.ctx, d, 200, []byte(`{"success":true}`), 0, nil, f.env.Now.Now()); err != nil {
					t.Fatal(err)
				}
			}
			legacy := hub.GrokbotTransportDeliveryV1
			if err := f.env.Hub.UpdateRoute(f.ctx, f.route, hub.RouteUpdate{GrokbotTransport: &legacy}); err != nil {
				t.Fatal(err)
			}
			if phase == "claimed" {
				f.w.Push(f.ctx, d)
				if f.row(d.DeliveryID).Attempt != 0 || len(f.rcv.Hits()) != 0 {
					t.Fatal("revoked unarmed claim POSTed or spent attempt")
				}
			}
			var cover string
			if err := f.env.Hub.DB.QueryRow(`SELECT cover_until FROM grokbot_wakes WHERE route_id=?`, f.route).Scan(&cover); err != nil || cover != "" {
				t.Fatal("revocation retained successful cover")
			}
			if phase == "armed" {
				if n := f.step(); n != 0 {
					t.Fatal("revocation withdrew armed exclusion")
				}
				// Restore before lease end; the invalid marker must remain invalid.
				optIn(t, f)
				if err := f.env.Hub.GrokbotWakePushResult(f.ctx, d, 200, []byte(`{"success":true}`), 0, nil, f.env.Now.Now()); err != nil {
					t.Fatal(err)
				}
				if err := f.env.Hub.DB.QueryRow(`SELECT cover_until FROM grokbot_wakes WHERE route_id=?`, f.route).Scan(&cover); err != nil || cover != "" {
					t.Fatal("re-enable reopened invalid cover")
				}
				advanceGroup(t, f)
				if n := f.step(); n != 1 || len(f.rcv.Hits()) != 1 {
					t.Fatal("post-lease recovery failed")
				}
			} else {
				if n := f.step(); n < 6 {
					t.Fatalf("revoked followers did not resume raw legacy scheduling: %d", n)
				}
				for _, h := range f.rcv.Hits() {
					if bytes.Contains(h.Body, []byte(`"grokbot_wake"`)) {
						t.Fatal("revoked route sent envelope")
					}
				}
			}
		})
	}
}

func TestGrokDuplicatePreflightCannotWithdrawArmedPOST(t *testing.T) {
	entered, release := make(chan struct{}, 1), make(chan struct{})
	f := drainFixture(t, func(n int, h hit) reply { entered <- struct{}{}; <-release; return started(n, h) })
	sevenReleased(t, f)
	d := drainClaim(t, f)
	done := make(chan struct{})
	go func() { f.w.Push(f.ctx, d); close(done) }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("no POST")
	}
	replay := d
	replay.Body = bytes.Repeat([]byte("x"), hub.MaxPushBody+1)
	f.w.Push(f.ctx, replay)
	var phase string
	err := f.env.Hub.DB.QueryRow(`SELECT phase FROM grokbot_wakes WHERE route_id=?`, f.route).Scan(&phase)
	runs := f.routeRuns()
	close(release)
	<-done
	if err != nil || phase != "armed" || runs != 1 {
		t.Fatalf("preflight replay withdrew active gate: phase=%q runs=%d %v", phase, runs, err)
	}
}

func TestGrokDuplicateResultCannotRefundSettled200(t *testing.T) {
	f := drainFixture(t, started)
	sevenReleased(t, f)
	d := drainClaim(t, f)
	f.w.Push(f.ctx, d)
	// Check the same token both while current and after its domain elects anew.
	for _, replacement := range []bool{false, true} {
		if replacement {
			advanceGroup(t, f)
			if n := f.step(); n != 1 {
				t.Fatalf("replacement claims=%d", n)
			}
		}
		runs := f.routeRuns()
		if err := f.env.Hub.GrokbotWakePushResult(f.ctx, d, 503, nil, 0, nil, f.env.Now.Now()); err != nil {
			t.Fatal(err)
		}
		if f.routeRuns() != runs {
			t.Fatal("duplicate/stale non-200 refunded already settled HTTP 200")
		}
	}
}

func TestGrokKnownNoninvocationAfterBeginAndShutdownAmbiguity(t *testing.T) {
	t.Run("known after Begin", func(t *testing.T) {
		f := drainFixture(t, started)
		sevenReleased(t, f)
		d := drainClaim(t, f)
		ok, _, err := f.env.Hub.BeginGrokbotPOST(f.ctx, d, f.env.Now.Now())
		if err != nil || !ok {
			t.Fatal(err)
		}
		for range 2 {
			if err := f.env.Hub.CancelUninvokedGrokbotPOST(f.ctx, d, f.env.Now.Now()); err != nil {
				t.Fatal(err)
			}
		}
		if f.routeRuns() != 0 || f.row(d.DeliveryID).Attempt != 0 || len(t1PushRows(t, f)) != 0 {
			t.Fatal("known noninvocation did not compensate exactly once")
		}
	})
	t.Run("shutdown after client entry", func(t *testing.T) {
		f := drainFixture(t, started)
		sevenReleased(t, f)
		d := drainClaim(t, f)
		ctx, cancel := context.WithCancel(f.ctx)
		defer cancel()
		var calls atomic.Int32
		f.w = &push.Worker{Hub: f.env.Hub, Now: f.env.Now.Now, Client: &http.Client{Transport: t1RoundTrip(func(*http.Request) (*http.Response, error) { calls.Add(1); cancel(); return nil, context.Canceled })}}
		f.w.Push(ctx, d)
		if calls.Load() != 1 || f.routeRuns() != 1 || f.row(d.DeliveryID).State != "pushed" || f.row(d.DeliveryID).Attempt != 1 {
			t.Fatal("shutdown ambiguity withdrew lease or reservation")
		}
		if n := f.step(); n != 0 {
			t.Fatal("shutdown ambiguity allowed another representative")
		}
	})
}

func TestGrokReplayCannotClearAmbiguousGate(t *testing.T) {
	f := drainFixture(t, started)
	sevenReleased(t, f)
	d := drainClaim(t, f)
	var calls atomic.Int32
	f.w = &push.Worker{Hub: f.env.Hub, Now: f.env.Now.Now, Client: &http.Client{Transport: t1RoundTrip(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return nil, errors.New("synthetic ambiguity")
	})}}
	f.w.Push(f.ctx, d)
	deadline := groupDeadline(t, f)
	f.w.Push(f.ctx, d)
	if calls.Load() != 1 || !groupDeadline(t, f).Equal(deadline) {
		t.Fatal("replayed Push cleared/changed an ambiguous armed exclusion")
	}
}

func TestGrokCrossRouteThreadOrder(t *testing.T) {
	f := setup(t, func(n int, h hit) reply {
		if n == 0 {
			return reply{Status: 503}
		}
		return reply{Status: 200}
	})
	oldReceiver := f.rcv
	first := f.send(f.aSec, f.msg("wake"))
	firstID := first.Deliveries[0].DeliveryID
	f.addGrokbot(started)
	optIn(t, f)
	body := f.msg("wake")
	delete(body, "recipients")
	body["thread_id"] = first.ThreadID
	f.send(f.aSec, body)
	if n := f.step(); n != 1 || len(oldReceiver.Hits()) != 1 || len(f.rcv.Hits()) != 0 {
		t.Fatal("new route overtook earlier thread work")
	}
	if n := f.step(); n != 0 || len(f.rcv.Hits()) != 0 {
		t.Fatal("new route bypassed earlier route's retry deadline")
	}
	f.env.Now.Advance(f.untilNext(firstID) + time.Millisecond)
	if n := f.step(); n != 1 || len(oldReceiver.Hits()) != 2 {
		t.Fatal("earlier route retry did not complete")
	}
	if n := f.step(); n != 1 || len(f.rcv.Hits()) != 1 {
		t.Fatal("later opted representative did not become eligible")
	}
}

func TestGrokSharedRouteCountsOnlyNamedRecipient(t *testing.T) {
	f := drainFixture(t, started)
	var principal string
	if err := f.env.Hub.DB.QueryRow(`SELECT prn_id FROM peers WHERE peer_id=?`, f.b).Scan(&principal); err != nil {
		t.Fatal(err)
	}
	other, _ := f.env.NewPeer("session", "recipient-domain")
	if _, err := f.env.Hub.DB.Exec(`UPDATE peers SET prn_id=?,route_id=? WHERE peer_id=?`, principal, f.route, other); err != nil {
		t.Fatal(err)
	}
	if _, err := f.env.Hub.DB.Exec(`UPDATE routes SET owner_kind='principal',owner_id=? WHERE route_id=?`, principal, f.route); err != nil {
		t.Fatal(err)
	}
	sevenReleased(t, f)
	for range 3 {
		body := f.msg("wake")
		body["recipients"] = []string{other}
		f.send(f.aSec, body)
	}
	held := f.send(f.aSec, f.msg("hold")).Deliveries[0].DeliveryID
	if n := f.step(); n != 2 || len(f.rcv.Hits()) != 2 || f.routeRuns() != 2 {
		t.Fatalf("shared route conflated recipients: claims=%d POSTs=%d", n, len(f.rcv.Hits()))
	}
	counts := map[string]int{}
	for _, hit := range f.rcv.Hits() {
		var e struct {
			Delivery struct {
				Recipient string `json:"recipient"`
			} `json:"delivery"`
			Count int `json:"pending_count"`
		}
		if err := json.Unmarshal(hit.Body, &e); err != nil {
			t.Fatal(err)
		}
		counts[e.Delivery.Recipient] = e.Count
	}
	if counts[f.b] != 7 || counts[other] != 3 || len(counts) != 2 {
		t.Fatalf("recipient counts=%v", counts)
	}
	if f.row(held).Attempt != 0 || len(f.receiptsFor(f.a, held)) != 0 {
		t.Fatal("hold was pushed or receipted")
	}
	var bad int
	if err := f.env.Hub.DB.QueryRow(`SELECT count(*) FROM grokbot_covers c JOIN deliveries d ON d.dly_id=c.delivery_id WHERE c.recipient<>d.recipient OR d.priority='hold'`).Scan(&bad); err != nil || bad != 0 {
		t.Fatalf("cross-recipient/hold coverage=%d %v", bad, err)
	}
}

func TestGrokBeginChecksCurrentAuthorityAndRoute(t *testing.T) {
	for _, change := range []string{"url", "key", "principal", "disabled", "owner", "capability", "removed"} {
		t.Run(change, func(t *testing.T) {
			f := drainFixture(t, started)
			sevenReleased(t, f)
			d := drainClaim(t, f)
			var q string
			var args []any
			switch change {
			case "url":
				q = `UPDATE routes SET url=url||'/changed' WHERE route_id=?`
				args = []any{f.route}
			case "key":
				sealed, err := f.env.Hub.Seal([]byte("crsr_new-synthetic-key"))
				if err != nil {
					t.Fatal(err)
				}
				q = `UPDATE routes SET key_enc=? WHERE route_id=?`
				args = []any{sealed, f.route}
			case "principal":
				prn, _ := f.env.NewPrincipal("new-authority")
				q = `UPDATE peers SET prn_id=? WHERE peer_id=?`
				args = []any{prn, f.b}
			case "disabled":
				q = `UPDATE peers SET disabled=1 WHERE peer_id=?`
				args = []any{f.b}
			case "owner":
				q = `UPDATE routes SET owner_id='peer_wrong' WHERE route_id=?`
				args = []any{f.route}
			case "capability":
				q = `UPDATE routes SET grokbot_transport='delivery_v1' WHERE route_id=?`
				args = []any{f.route}
			case "removed":
				q = `DELETE FROM routes WHERE route_id=?`
				args = []any{f.route}
			}
			if _, err := f.env.Hub.DB.Exec(q, args...); err != nil {
				t.Fatal(err)
			}
			f.w.Push(f.ctx, d)
			if len(f.rcv.Hits()) != 0 || f.routeRuns() != 0 || f.row(d.DeliveryID).Attempt != 0 {
				t.Fatalf("stale %s claim invoked or spent an attempt", change)
			}
		})
	}
}

func TestGrokSettlementRollbackIsAtomic(t *testing.T) {
	f := drainFixture(t, started)
	sevenReleased(t, f)
	d := drainClaim(t, f)
	if _, err := f.env.Hub.DB.Exec(`CREATE TRIGGER synthetic_settlement_failure BEFORE UPDATE OF state ON deliveries WHEN NEW.state='accepted' BEGIN SELECT RAISE(ABORT,'synthetic settlement rollback'); END`); err != nil {
		t.Fatal(err)
	}
	f.w.Push(f.ctx, d)
	var phase string
	if err := f.env.Hub.DB.QueryRow(`SELECT phase FROM grokbot_wakes WHERE route_id=?`, f.route).Scan(&phase); err != nil {
		t.Fatal(err)
	}
	if phase != "armed" || f.row(d.DeliveryID).State != "pushed" || len(f.receiptsFor(f.a, d.DeliveryID)) != 0 || f.routeRuns() != 1 || len(f.rcv.Hits()) != 1 {
		t.Fatal("failed settlement partially committed domain/delivery/receipt")
	}
	if _, err := f.env.Hub.DB.Exec(`DROP TRIGGER synthetic_settlement_failure`); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := f.env.Hub.GrokbotWakePushResult(f.ctx, d, 200, []byte(`{"success":true}`), 0, nil, f.env.Now.Now()); err != nil {
			t.Fatal(err)
		}
	}
	if f.row(d.DeliveryID).State != "accepted" || len(f.receiptsFor(f.a, d.DeliveryID)) != 1 || len(f.rcv.Hits()) != 1 {
		t.Fatal("settlement retry did not apply exactly once")
	}
}
