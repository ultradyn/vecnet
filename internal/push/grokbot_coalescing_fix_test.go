package push_test

import (
	"errors"
	"net/http"
	"reflect"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/hub"
)

func sharedDrainSibling(t *testing.T, f *fixture) string {
	t.Helper()
	var principal string
	if err := f.env.Hub.DB.QueryRow(`SELECT prn_id FROM peers WHERE peer_id=?`, f.b).Scan(&principal); err != nil {
		t.Fatal(err)
	}
	other, _ := f.env.NewPeer("session", "import-sibling")
	if _, err := f.env.Hub.DB.Exec(`UPDATE peers SET prn_id=?,route_id=? WHERE peer_id=?`, principal, f.route, other); err != nil {
		t.Fatal(err)
	}
	if _, err := f.env.Hub.DB.Exec(`UPDATE routes SET owner_kind='principal',owner_id=? WHERE route_id=?`, principal, f.route); err != nil {
		t.Fatal(err)
	}
	return other
}

func awaitImportSignal(t *testing.T, signal <-chan struct{}) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(10 * time.Second):
		t.Fatal("blocked client schedule timed out")
	}
}

func TestGrokEnableImportsLeaseBeforeLegacySettlement(t *testing.T) {
	for _, ambiguous := range []bool{false, true} {
		name := "started200"
		if ambiguous {
			name = "client-ambiguity"
		}
		t.Run(name, func(t *testing.T) {
			entered, release, done := make(chan struct{}, 1), make(chan struct{}), make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			f := setup(t, nil)
			f.addGrokbot(func(n int, h hit) reply {
				if n == 0 {
					entered <- struct{}{}
					<-release
				}
				return started(n, h)
			})
			// Cleanup runs before the receiver/server cleanup, even if an assertion fails.
			t.Cleanup(unblock)
			sibling := sharedDrainSibling(t, f)
			var calls atomic.Int32
			f.w.Client = &http.Client{Transport: t1RoundTrip(func(req *http.Request) (*http.Response, error) {
				n := calls.Add(1)
				resp, err := http.DefaultTransport.RoundTrip(req)
				if ambiguous && n == 1 && err == nil {
					resp.Body.Close()
					return nil, errors.New("synthetic ambiguity after actual receiver entry")
				}
				return resp, err
			})}
			id := f.send(f.aSec, f.msg("wake")).Deliveries[0].DeliveryID
			due, err := f.env.Hub.ClaimDue(f.ctx, f.env.Now.Now(), 16)
			if err != nil || len(due) != 1 || due[0].GrokWake != nil {
				t.Fatalf("legacy claim=%v %v", due, err)
			}
			deadline := f.env.Now.Now().Add(f.untilNext(id))
			go func() { f.w.Push(f.ctx, due[0]); close(done) }()
			awaitImportSignal(t, entered)
			optIn(t, f)
			unblock()
			awaitImportSignal(t, done) // Settlement wins before the first opted scan.
			wantState := "accepted"
			if ambiguous {
				wantState = "queued"
			}
			if f.row(id).State != wantState {
				t.Fatalf("legacy result=%+v", f.row(id))
			}
			f.send(f.aSec, f.msg("wake"))
			body := f.msg("wake")
			body["recipients"] = []string{sibling}
			f.send(f.aSec, body)
			next, err := f.env.Hub.ClaimDue(f.ctx, f.env.Now.Now(), 16)
			if err != nil {
				t.Fatal(err)
			}
			if len(next) != 1 || next[0].Recipient != sibling {
				t.Fatalf("settlement lost original barrier; claims=%+v", next)
			}
			f.w.Push(f.ctx, next[0])
			var cover string
			if err := f.env.Hub.DB.QueryRow(`SELECT cover_until FROM grokbot_wakes WHERE route_id=? AND recipient=?`, f.route, f.b).Scan(&cover); err != nil || cover != "" {
				t.Fatalf("legacy result granted cover=%q err=%v", cover, err)
			}
			f.env.Now.Advance(deadline.Sub(f.env.Now.Now()) - time.Millisecond)
			if n := f.step(); n != 0 || calls.Load() != 2 {
				t.Fatalf("pre-deadline claims=%d calls=%d", n, calls.Load())
			}
			f.env.Now.Advance(2 * time.Millisecond)
			if n := f.step(); n != 1 || calls.Load() != 3 || len(f.rcv.Hits()) != 3 {
				t.Fatalf("post-deadline claims=%d calls=%d hits=%d", n, calls.Load(), len(f.rcv.Hits()))
			}
		})
	}
}

func TestGrokImportedBarrierExcludesLegacyInFlight(t *testing.T) {
	for _, priorEvidence := range []bool{false, true} {
		name := "fresh-coverage"
		if priorEvidence {
			name = "preserve-earlier-evidence"
		}
		t.Run(name, func(t *testing.T) {
			entered, release := make(chan struct{}, 2), make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			f := setup(t, nil)
			f.addGrokbot(func(n int, h hit) reply {
				if n < 2 {
					entered <- struct{}{}
					<-release
				}
				return started(n, h)
			})
			t.Cleanup(unblock)
			ids := []string{f.send(f.aSec, f.msg("wake")).Deliveries[0].DeliveryID, f.send(f.aSec, f.msg("wake")).Deliveries[0].DeliveryID}
			due, err := f.env.Hub.ClaimDue(f.ctx, f.env.Now.Now(), 16)
			if err != nil || len(due) != 2 {
				t.Fatalf("legacy claims=%v %v", due, err)
			}
			// Genuine old first-cover evidence may predate this later legacy invocation.
			oldAt := envelope.FormatTime(f.env.Now.Now().Add(-time.Hour))
			if priorEvidence {
				if _, err := f.env.Hub.DB.Exec(`INSERT INTO grokbot_covers VALUES (?,?,?,?)`, f.route, ids[0], f.b, oldAt); err != nil {
					t.Fatal(err)
				}
			}
			var wg sync.WaitGroup
			for _, d := range due {
				wg.Add(1)
				go func(d hub.DueDelivery) { defer wg.Done(); f.w.Push(f.ctx, d) }(d)
			}
			awaitImportSignal(t, entered)
			awaitImportSignal(t, entered)
			optIn(t, f)
			followers := []string{f.send(f.aSec, f.msg("wake")).Deliveries[0].DeliveryID, f.send(f.aSec, f.msg("wake")).Deliveries[0].DeliveryID}
			before := map[string]dlyRow{}
			for _, id := range append(append([]string{}, ids...), followers...) {
				before[id] = f.row(id)
			}
			for range 2 {
				next, err := f.env.Hub.ClaimDue(f.ctx, f.env.Now.Now(), 16)
				if err != nil || len(next) != 0 {
					t.Fatalf("barrier claims=%v %v", next, err)
				}
			}
			rows, err := f.env.Hub.DB.Query(`SELECT delivery_id,first_covered_at FROM grokbot_covers WHERE route_id=? ORDER BY delivery_id`, f.route)
			if err != nil {
				t.Fatal(err)
			}
			got := []string{}
			var retained string
			for rows.Next() {
				var id, at string
				if err := rows.Scan(&id, &at); err != nil {
					t.Fatal(err)
				}
				got = append(got, id)
				if id == ids[0] {
					retained = at
				}
			}
			if err := rows.Err(); err != nil {
				t.Fatal(err)
			}
			rows.Close()
			want := append([]string{}, followers...)
			wantRetained := ""
			if priorEvidence {
				want = append(want, ids[0])
				wantRetained = oldAt
			}
			sort.Strings(want)
			if !reflect.DeepEqual(got, want) || retained != wantRetained {
				t.Errorf("covered=%v want only prior evidence plus queued followers=%v; prior=%q", got, want, retained)
			}
			for id, original := range before {
				if r := f.row(id); !reflect.DeepEqual(r, original) {
					t.Errorf("claim/follower row changed=%+v original=%+v", r, original)
				}
				if len(f.receiptsFor(f.a, id)) != 0 {
					t.Error("attachment fabricated receipt")
				}
			}
			if len(f.rcv.Hits()) != 2 || len(t1PushRows(t, f)) != 0 || f.routeRuns() != 2 {
				t.Error("attachment fabricated invocation/observation/reservation")
			}
			unblock()
			wg.Wait()
			// Positive control for the same sender-log oracle used while blocked:
			// normal legacy settlement now produces one accepted receipt per POST.
			for _, id := range ids {
				if rs := f.receiptsFor(f.a, id); len(rs) != 1 || rs[0].State != "accepted" || rs[0].By != "hub" {
					t.Errorf("legacy settlement sender receipts=%+v for %s", rs, id)
				}
			}
		})
	}
}

func TestGrokKnownNoninvocationExpiredLease(t *testing.T) {
	for _, name := range []string{"unreplaced", "replaced", "unreplaced-reservation-pruned"} {
		replaced := name == "replaced"
		t.Run(name, func(t *testing.T) {
			f := drainFixture(t, started)
			sevenReleased(t, f)
			d := drainClaim(t, f)
			ok, _, err := f.env.Hub.BeginGrokbotPOST(f.ctx, d, f.env.Now.Now())
			if err != nil || !ok {
				t.Fatalf("Begin=%v %v", ok, err)
			}
			at := envelope.FormatTime(f.env.Now.Now())
			for _, q := range []string{`INSERT INTO route_runs(route_id,at) VALUES (?1,?2)`, `INSERT INTO route_runs VALUES (?1,?2,'other-token',0)`, `INSERT INTO route_runs VALUES (?1,?2,'settled-token',1)`} {
				if _, err := f.env.Hub.DB.Exec(q, f.route, at); err != nil {
					t.Fatal(err)
				}
			}
			f.env.Now.Advance(hub.PushLease + time.Millisecond)
			if name == "unreplaced-reservation-pruned" {
				if _, err := f.env.Hub.DB.Exec(`DELETE FROM route_runs WHERE route_id=? AND grok_wake_token=?`, f.route, d.GrokWake.Token); err != nil {
					t.Fatal(err)
				}
			}
			var replacement hub.DueDelivery
			if replaced {
				replacement = drainClaim(t, f)
				ok, _, err = f.env.Hub.BeginGrokbotPOST(f.ctx, replacement, f.env.Now.Now())
				if err != nil || !ok {
					t.Fatalf("replacement Begin=%v %v", ok, err)
				}
			}
			snapshot := func() string {
				var s string
				if err := f.env.Hub.DB.QueryRow(`SELECT json_array(route_id,recipient,representative_id,token,phase,lease_until,retry_at,cover_until,pending_count,failure_ordinal,idle_counter,begin_idle_counter,cover_valid,expected_url,expected_principal,expected_key,hex(frozen_body)) FROM grokbot_wakes WHERE route_id=? AND recipient=?`, f.route, f.b).Scan(&s); err != nil {
					t.Fatal(err)
				}
				return s
			}
			before := snapshot()
			rowBefore := f.row(d.DeliveryID)
			for range 2 {
				if err := f.env.Hub.CancelUninvokedGrokbotPOST(f.ctx, d, f.env.Now.Now()); err != nil {
					t.Fatal(err)
				}
			}
			var own, sentinels int
			if err := f.env.Hub.DB.QueryRow(`SELECT count(*) FROM route_runs WHERE route_id=? AND grok_wake_token=?`, f.route, d.GrokWake.Token).Scan(&own); err != nil {
				t.Fatal(err)
			}
			if own != 0 {
				t.Errorf("proven noninvocation retained own expired reservation=%d", own)
			}
			if err := f.env.Hub.DB.QueryRow(`SELECT count(*) FROM route_runs WHERE route_id=?1 AND at=?2 AND (grok_wake_token IS NULL AND grok_wake_settled_200=0 OR grok_wake_token='other-token' AND grok_wake_settled_200=0 OR grok_wake_token='settled-token' AND grok_wake_settled_200=1)`, f.route, at).Scan(&sentinels); err != nil {
				t.Fatal(err)
			}
			if sentinels != 3 {
				t.Errorf("sentinels changed=%d", sentinels)
			}
			if replaced {
				if snapshot() != before || !reflect.DeepEqual(f.row(d.DeliveryID), rowBefore) {
					t.Error("old cancellation changed replacement gate/attempt")
				}
				var n int
				if err := f.env.Hub.DB.QueryRow(`SELECT count(*) FROM route_runs WHERE route_id=? AND grok_wake_token=? AND grok_wake_settled_200=0`, f.route, replacement.GrokWake.Token).Scan(&n); err != nil || n != 1 {
					t.Errorf("replacement reservation=%d %v", n, err)
				}
			} else {
				if r := f.row(d.DeliveryID); r.Attempt != 0 || r.State != "queued" {
					t.Errorf("current expired claim not compensated=%+v", r)
				}
			}
			// A settled token cannot be refunded even via the proof-bearing entrypoint.
			settled := d
			settled.GrokWake = &hub.GrokWakeClaim{Token: "settled-token"}
			if err := f.env.Hub.CancelUninvokedGrokbotPOST(f.ctx, settled, f.env.Now.Now()); err != nil {
				t.Fatal(err)
			}
			if len(f.receiptsFor(f.a, d.DeliveryID)) != 0 {
				t.Error("noninvocation fabricated receipt")
			}
			if len(f.rcv.Hits()) != 0 || len(t1PushRows(t, f)) != 0 {
				t.Error("known noninvocation entered payload client")
			}
			wantRuns := 3
			if replaced {
				wantRuns++
			}
			if f.routeRuns() != wantRuns {
				t.Errorf("exact reservation set count=%d want=%d", f.routeRuns(), wantRuns)
			}
		})
	}
}

// First-cover evidence outlives an inactive coordinator. Explicitly retrying
// that retained delivery must not require an unrelated arrival to reopen work.
func TestGrokCoveredRetryAfterCoordinatorCleanup(t *testing.T) {
	f := drainFixture(t, func(n int, h hit) reply {
		if n == 1 {
			return reply{Status: http.StatusBadRequest, Body: `{"success":false,"error":"synthetic terminal refusal"}`}
		}
		return started(n, h)
	})
	var calls atomic.Int32
	f.w.Client = &http.Client{Transport: t1RoundTrip(func(req *http.Request) (*http.Response, error) {
		calls.Add(1)
		return http.DefaultTransport.RoundTrip(req)
	})}
	first := f.send(f.aSec, f.msg("wake")).Deliveries[0].DeliveryID
	follower := f.send(f.aSec, f.msg("wake")).Deliveries[0].DeliveryID
	if n := f.step(); n != 1 || calls.Load() != 1 || len(f.rcv.Hits()) != 1 || f.row(first).State != "accepted" || f.row(follower).Attempt != 0 {
		t.Fatalf("initial representative claims=%d calls=%d hits=%d first=%+v follower=%+v", n, calls.Load(), len(f.rcv.Hits()), f.row(first), f.row(follower))
	}
	coverage := func() string {
		t.Helper()
		var value string
		if err := f.env.Hub.DB.QueryRow(`SELECT json_array(route_id,delivery_id,recipient,first_covered_at) FROM grokbot_covers WHERE route_id=? AND delivery_id=? AND recipient=?`, f.route, follower, f.b).Scan(&value); err != nil {
			t.Fatal(err)
		}
		var n int
		if err := f.env.Hub.DB.QueryRow(`SELECT count(*) FROM grokbot_covers WHERE route_id=?`, f.route).Scan(&n); err != nil || n != 1 {
			t.Fatalf("coverage population=%d err=%v", n, err)
		}
		return value
	}
	originalCover := coverage()
	advanceGroup(t, f) // The accepted representative's successful cover expires.
	if n := f.step(); n != 1 || calls.Load() != 2 || len(f.rcv.Hits()) != 2 || f.row(follower).State != "dead_letter" {
		t.Fatalf("covered follower terminal claims=%d calls=%d hits=%d row=%+v", n, calls.Load(), len(f.rcv.Hits()), f.row(follower))
	}
	if coverage() != originalCover {
		t.Fatal("later representative changed first-cover evidence")
	}
	advanceGroup(t, f) // Its finite failure deadline also expires.
	if n := f.step(); n != 0 || calls.Load() != 2 || len(f.rcv.Hits()) != 2 {
		t.Fatalf("cleanup claims=%d calls=%d hits=%d", n, calls.Load(), len(f.rcv.Hits()))
	}
	assertAbsent := func() {
		t.Helper()
		var n int
		if err := f.env.Hub.DB.QueryRow(`SELECT (SELECT count(*) FROM grokbot_wakes WHERE route_id=?1 AND recipient=?2)+(SELECT count(*) FROM grokbot_legacy_claims WHERE route_id=?1 AND recipient=?2)`, f.route, f.b).Scan(&n); err != nil || n != 0 {
			t.Fatalf("expected absent coordinator/imports, got %d err=%v", n, err)
		}
	}
	assertAbsent()
	if coverage() != originalCover {
		t.Fatal("cleanup erased historical coverage")
	}
	if err := f.env.Hub.RetryDeadLetter(f.ctx, follower); err != nil {
		t.Fatal(err)
	}
	assertAbsent() // Existing retry API requeues without creating a coordinator.
	if r := f.row(follower); r.State != "queued" || r.Attempt != 1 {
		t.Fatalf("explicit retry row=%+v", r)
	}
	if n := f.step(); n != 1 || calls.Load() != 3 || len(f.rcv.Hits()) != 3 {
		t.Fatalf("covered retry after cleanup claims=%d client entries=%d receiver POSTs=%d, want 1/3/3", n, calls.Load(), len(f.rcv.Hits()))
	}
	if r := f.row(follower); r.State != "accepted" || r.Attempt != 2 {
		t.Fatalf("retried follower result=%+v", r)
	}
	if coverage() != originalCover {
		t.Fatal("retry rewrote first-cover identity/time")
	}
	if n := f.step(); n != 0 || calls.Load() != 3 || len(f.rcv.Hits()) != 3 {
		t.Fatalf("post-retry duplicate claims=%d calls=%d hits=%d", n, calls.Load(), len(f.rcv.Hits()))
	}
}
