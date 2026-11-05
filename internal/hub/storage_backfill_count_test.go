package hub_test

import (
	"context"
	"database/sql/driver"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/auth"
	"github.com/XertroV/vecnet/internal/hub"
	"modernc.org/sqlite"
)

var exceptionProbeID atomic.Int64

func TestBackfillExceptionRescanBoundsExaminedKeysAcrossRestart(t *testing.T) {
	var visits atomic.Int64
	name := fmt.Sprintf("backfill_exception_probe_%d", exceptionProbeID.Add(1))
	if err := sqlite.RegisterScalarFunction(name, 1, func(*sqlite.FunctionContext, []driver.Value) (driver.Value, error) {
		visits.Add(1)
		return int64(1), nil
	}); err != nil {
		t.Fatal(err)
	}
	env, _ := backfillEnv(t)
	h := env.Hub
	forward := finishBackfill(t, h)
	// The first three keys refer to a converted delivery, a non-delivery,
	// and a pruned event. Most of the remaining keys have no event at all.
	guardExec(t, env, `UPDATE events SET type='presence',data='{}',payload_format='inline',msg_id=NULL,snapshot_id=NULL WHERE seq=2`)
	guardExec(t, env, `DELETE FROM events WHERE seq=3`)
	const historical = 100000
	guardExec(t, env, `WITH RECURSIVE n(i) AS (VALUES(1) UNION ALL SELECT i+1 FROM n WHERE i<?) INSERT INTO delivery_backfill_exceptions(seq,reason) SELECT i,'invalid_shape' FROM n`, historical)
	guardExec(t, env, `UPDATE meta SET value=json_set(value,'$.exception_count',?) WHERE key='delivery_backfill_v1'`, historical)
	// Count visits through the real slice queries, including their completion
	// checks, without relying on machine speed or duplicating the production SQL.
	guardExec(t, env, `ALTER TABLE delivery_backfill_exceptions RENAME TO backfill_exception_rows`)
	guardExec(t, env, `CREATE VIEW delivery_backfill_exceptions AS SELECT seq,reason FROM backfill_exception_rows WHERE `+name+`(seq)`)
	limits := hub.BackfillLimits{Rows: 1, RescanExceptions: true}
	for _, want := range []int64{1, 2, 3} {
		visits.Store(0)
		p := slice(t, h, limits)
		// Initial high-water capture may visit one key; selection and completion
		// may visit one each. No query may walk the unmatched suffix.
		maxVisits := int64(2)
		if want == 1 {
			maxVisits++
		}
		if n := visits.Load(); n > maxVisits {
			t.Errorf("Rows:1 rescan visited %d exceptions with %d retained", n, historical)
		}
		t.Logf("retained=%d rescan_last_seq=%d exception_visits=%d", historical, p.RescanLastSeq, visits.Load())
		if p.RescanLastSeq != want || p.RescanHighSeq != historical || !p.Rescanning || p.Done {
			t.Fatalf("rescan did not checkpoint examined key %d: %+v", want, p)
		}
		if p.Processed != 0 || p.SourceBytes != 0 || p.ExceptionCount != historical || p.LastSeq != forward.LastSeq || p.HighSeq != forward.HighSeq {
			t.Fatalf("ineligible exception changed counts or forward checkpoint: %+v", p)
		}
		restarted, err := hub.New(h.DB, h.Cfg, env.Now.Now)
		if err != nil {
			t.Fatal(err)
		}
		status, err := restarted.BackfillDeliveryStatus(context.Background(), auth.Caller{Kind: "operator"})
		if err != nil || status.RescanLastSeq != want || status.RescanHighSeq != historical || !status.Rescanning {
			t.Fatalf("rescan checkpoint lost on restart: %+v err=%v", status, err)
		}
		h = restarted
	}
	if backfillCount(t, h, `SELECT count(*) FROM backfill_exception_rows WHERE reason='invalid_shape'`) != historical {
		t.Fatal("rescan removed or changed retained diagnostics")
	}
}

func TestBackfillExceptionRescanBudgetsAndCompletion(t *testing.T) {
	env, fs := backfillEnv(t)
	h := env.Hub
	guardExec(t, env, `UPDATE deliveries SET body_sha256='bad' WHERE event_seq IN (2,3)`)
	forward := finishBackfill(t, h)
	guardExec(t, env, `UPDATE deliveries SET body_sha256=? WHERE event_seq=2`, fs[1].SHA256)
	guardExec(t, env, `UPDATE deliveries SET body_sha256=? WHERE event_seq=3`, fs[2].SHA256)
	guardExec(t, env, `DELETE FROM events WHERE seq=1`)
	guardExec(t, env, `INSERT INTO delivery_backfill_exceptions(seq,reason) VALUES (1,'invalid_shape'),(1000,'duplicate_key')`)
	guardExec(t, env, `UPDATE meta SET value=json_set(value,'$.exception_count',4) WHERE key='delivery_backfill_v1'`)
	var calls int
	h.Now = func() time.Time { calls++; return env.Now.Now().Add(time.Duration(calls) * time.Second) }
	p := slice(t, h, hub.BackfillLimits{Rows: 100, RescanExceptions: true, Target: time.Millisecond})
	if p.RescanLastSeq != 1 || p.Processed != 0 || p.Done || p.ExceptionCount != 4 {
		t.Fatalf("missing exception ignored time budget: %+v", p)
	}
	h.Now = env.Now.Now
	limits := hub.BackfillLimits{Rows: 100, RescanExceptions: true, Bytes: int64(len(fs[1].Data) + len(fs[2].Data)/2), DryRun: true}
	p = slice(t, h, limits)
	if p.Converted != 1 || p.RescanLastSeq != 2 || p.SourceBytes != int64(len(fs[1].Data)) || p.ExceptionCount != 3 || p.Done {
		t.Fatalf("rescan exceeded cumulative byte budget: %+v", p)
	}
	restarted, err := hub.New(h.DB, h.Cfg, env.Now.Now)
	if err != nil {
		t.Fatal(err)
	}
	h = restarted
	status, err := h.BackfillDeliveryStatus(context.Background(), auth.Caller{Kind: "operator"})
	if err != nil || status.RescanLastSeq != 1 || status.ExceptionCount != 4 || backfillCount(t, h, `SELECT count(*) FROM events WHERE seq IN (2,3) AND payload_format='inline'`) != 2 {
		t.Fatalf("dry rescan persisted progress: %+v err=%v", status, err)
	}
	limits.DryRun = false
	p = slice(t, h, limits)
	if p.Converted != 1 || p.RescanLastSeq != 2 || p.SourceBytes != int64(len(fs[1].Data)) || p.ExceptionCount != 3 || p.Done {
		t.Fatalf("rescan failed to resume deferred event: %+v", p)
	}
	limits.Bytes = 1
	p = slice(t, h, limits)
	if p.Converted != 1 || p.RescanLastSeq != 3 || p.SourceBytes != int64(len(fs[2].Data)) || p.ExceptionCount != 2 || p.Done {
		t.Fatalf("oversized rescan event lost its own slice or skipped unmatched suffix: %+v", p)
	}
	// An exception recorded after capture cannot extend this pass's high-water.
	guardExec(t, env, `INSERT INTO delivery_backfill_exceptions(seq,reason) VALUES (1001,'identity_mismatch')`)
	guardExec(t, env, `UPDATE meta SET value=json_set(value,'$.exception_count',3) WHERE key='delivery_backfill_v1'`)
	p = slice(t, h, limits)
	if !p.Done || p.Rescanning || p.RescanLastSeq != 1000 || p.RescanHighSeq != 1000 || p.Processed != 0 || p.ExceptionCount != 3 || p.HighSeq != forward.HighSeq || p.LastSeq != forward.LastSeq {
		t.Fatalf("rescan did not finish from remaining captured keys: %+v", p)
	}
	if backfillCount(t, h, `SELECT count(*) FROM delivery_backfill_exceptions WHERE (seq=1 AND reason='invalid_shape') OR (seq=1000 AND reason='duplicate_key') OR (seq=1001 AND reason='identity_mismatch')`) != 3 {
		t.Fatal("missing-event diagnostics changed")
	}
}

// Instrument actual exception-row visits rather than using a load-dependent
// latency assertion. The view preserves indexed seq lookups, while any full
// recount must evaluate the probe once per historical exception.
func TestBackfillHistoricalExceptionsDoNotEnterSlice(t *testing.T) {
	for _, candidate := range []bool{false, true} {
		t.Run(fmt.Sprintf("candidate_%t", candidate), func(t *testing.T) {
			var visits atomic.Int64
			name := fmt.Sprintf("backfill_exception_probe_%d", exceptionProbeID.Add(1))
			if err := sqlite.RegisterScalarFunction(name, 1, func(*sqlite.FunctionContext, []driver.Value) (driver.Value, error) {
				visits.Add(1)
				return int64(1), nil
			}); err != nil {
				t.Fatal(err)
			}
			env, fs := backfillEnv(t)
			h := env.Hub
			finishBackfill(t, h)
			const historical = 100000
			guardExec(t, env, `WITH RECURSIVE n(i) AS (VALUES(1) UNION ALL SELECT i+1 FROM n WHERE i<?) INSERT INTO delivery_backfill_exceptions(seq,reason) SELECT 1000000+i,'invalid_shape' FROM n`, historical)
			// Model an earlier binary's atomically committed checkpoint, which already
			// included the exact count. New slices must reuse it across a restart.
			guardExec(t, env, `UPDATE meta SET value=json_set(value,'$.exception_count',?) WHERE key='delivery_backfill_v1'`, historical)
			if candidate {
				guardExec(t, env, `INSERT INTO events(owner,type,at,data) VALUES (?,'delivery',?,?)`, guardB, guardAt, fs[0].Data)
				guardExec(t, env, `UPDATE meta SET value=json_set(value,'$.high_seq',(SELECT max(seq) FROM events)) WHERE key='delivery_backfill_v1'`)
			}
			guardExec(t, env, `ALTER TABLE delivery_backfill_exceptions RENAME TO backfill_exception_rows`)
			guardExec(t, env, `CREATE VIEW delivery_backfill_exceptions AS SELECT seq,reason FROM backfill_exception_rows WHERE `+name+`(seq)`)
			guardExec(t, env, `CREATE TRIGGER delete_exception INSTEAD OF DELETE ON delivery_backfill_exceptions BEGIN DELETE FROM backfill_exception_rows WHERE seq=OLD.seq; END`)
			restarted, err := hub.New(h.DB, h.Cfg, env.Now.Now)
			if err != nil {
				t.Fatal(err)
			}
			start := time.Now()
			p := slice(t, restarted, hub.BackfillLimits{Rows: 1})
			elapsed := time.Since(start)
			want := 0
			if candidate {
				want = 1
			}
			if !p.Done || p.Processed != want || p.Converted != want || p.ExceptionCount != historical {
				t.Fatalf("unexpected progress: %+v", p)
			}
			if n := visits.Load(); n > 1 {
				t.Errorf("slice visited %d historical exceptions for %d candidates", n, want)
			}
			visits.Store(0)
			status, err := restarted.BackfillDeliveryStatus(context.Background(), auth.Caller{Kind: "operator"})
			if err != nil || status.ExceptionCount != historical || len(status.Exceptions) != 100 {
				t.Fatalf("status count=%d details=%d err=%v", status.ExceptionCount, len(status.Exceptions), err)
			}
			if n := visits.Load(); n > 100 {
				t.Errorf("bounded status visited %d exceptions", n)
			}
			t.Logf("historical_exceptions=%d processed=%d slice_duration=%s required_headroom_bytes=%d", historical, want, elapsed, p.RequiredHeadroomBytes)
		})
	}
}

func TestBackfillSliceTimingIncludesCheckpointCommit(t *testing.T) {
	env, _ := backfillEnv(t)
	h := env.Hub
	h.Now = func() time.Time {
		// The clock advances only after the checkpoint is visible to another
		// connection, so a pre-commit timing sample cannot satisfy this assertion.
		if backfillCount(t, h, `SELECT count(*) FROM meta WHERE key='delivery_backfill_v1'`) > 0 {
			return env.Now.Now().Add(7 * time.Millisecond)
		}
		return env.Now.Now()
	}
	p := slice(t, h, hub.BackfillLimits{Rows: 1})
	if p.LastSliceNanos != int64(7*time.Millisecond) {
		t.Fatalf("timing omitted checkpoint/commit: %d ns", p.LastSliceNanos)
	}
	status, err := h.BackfillDeliveryStatus(context.Background(), auth.Caller{Kind: "operator"})
	if err != nil || status.LastSliceNanos != p.LastSliceNanos {
		t.Fatalf("status timing=%d err=%v", status.LastSliceNanos, err)
	}
}

func TestBackfillExceptionCounterAtomicMutations(t *testing.T) {
	env, fs := backfillEnv(t)
	h := env.Hub
	guardExec(t, env, `UPDATE deliveries SET body_sha256='bad' WHERE event_seq=1`)
	failCheckpoint := func() {
		guardExec(t, env, `CREATE TRIGGER fail_count_checkpoint BEFORE INSERT ON meta WHEN NEW.key='delivery_backfill_v1' BEGIN SELECT RAISE(ABORT,'synthetic_checkpoint_failure'); END`)
	}
	assertCount := func(want int64) {
		t.Helper()
		status, err := h.BackfillDeliveryStatus(context.Background(), auth.Caller{Kind: "operator"})
		actual := backfillCount(t, h, `SELECT count(*) FROM delivery_backfill_exceptions`)
		if err != nil || status.ExceptionCount != want || int64(actual) != want {
			t.Fatalf("counter=%d rows=%d want=%d err=%v", status.ExceptionCount, actual, want, err)
		}
	}
	failCheckpoint()
	if _, err := h.BackfillDeliverySlice(context.Background(), hub.BackfillLimits{Rows: 1}); err == nil {
		t.Fatal("failed checkpoint accepted exception insert")
	}
	assertCount(0)
	guardExec(t, env, `DROP TRIGGER fail_count_checkpoint`)
	if p := slice(t, h, hub.BackfillLimits{Rows: 1, DryRun: true}); p.ExceptionCount != 1 {
		t.Fatal("dry run missed exception")
	}
	assertCount(0)
	if p := slice(t, h, hub.BackfillLimits{Rows: 1}); p.ExceptionCount != 1 || p.Failed != 1 {
		t.Fatal("exception insert did not increment count")
	}
	assertCount(1)
	for range 2 {
		if p := slice(t, h, hub.BackfillLimits{Rows: 1, RescanExceptions: true}); p.ExceptionCount != 1 || p.Failed != 1 {
			t.Fatal("updating existing exception changed count")
		}
	}
	guardExec(t, env, `UPDATE deliveries SET body_sha256=? WHERE event_seq=1`, fs[0].SHA256)
	failCheckpoint()
	if _, err := h.BackfillDeliverySlice(context.Background(), hub.BackfillLimits{Rows: 1, RescanExceptions: true}); err == nil {
		t.Fatal("failed checkpoint accepted exception deletion")
	}
	assertCount(1)
	if backfillCount(t, h, `SELECT count(*) FROM events WHERE seq=1 AND payload_format='inline'`) != 1 {
		t.Fatal("failed deletion committed event conversion")
	}
	guardExec(t, env, `DROP TRIGGER fail_count_checkpoint`)
	if p := slice(t, h, hub.BackfillLimits{Rows: 1, RescanExceptions: true}); p.ExceptionCount != 0 || p.Converted != 1 {
		t.Fatal("exception deletion did not decrement count")
	}
	assertCount(0)
	if p := slice(t, h, hub.BackfillLimits{Rows: 1}); p.ExceptionCount != 0 || p.Converted != 1 {
		t.Fatal("deleting absent exception changed count")
	}
	restarted, err := hub.New(h.DB, h.Cfg, env.Now.Now)
	if err != nil {
		t.Fatal(err)
	}
	h = restarted
	assertCount(0)
}

func TestBackfillExceptionCounterReconciliationIsBounded(t *testing.T) {
	env, fs := backfillEnv(t)
	h := env.Hub
	guardExec(t, env, `WITH RECURSIVE n(i) AS (VALUES(1) UNION ALL SELECT i+1 FROM n WHERE i<7) INSERT INTO delivery_backfill_exceptions(seq,reason) SELECT 1000+i,'invalid_shape' FROM n`)
	status, err := h.BackfillDeliveryStatus(context.Background(), auth.Caller{Kind: "operator"})
	if err != nil || !status.ExceptionCountPending {
		t.Fatal("missing counter not marked for reconciliation")
	}
	limits := hub.BackfillLimits{Rows: 2, DryRun: true}
	p := slice(t, h, limits)
	if p.ExceptionCount != 2 || !p.ExceptionCountPending || p.ExceptionCountSeq != 1002 || p.Processed != 0 || p.Done {
		t.Fatalf("unbounded reconciliation: %+v", p)
	}
	if backfillCount(t, h, `SELECT count(*) FROM meta WHERE key='delivery_backfill_v1'`) != 0 {
		t.Fatal("dry reconciliation persisted")
	}
	limits.DryRun = false
	p = slice(t, h, limits)
	if p.ExceptionCount != 2 || p.ExceptionCountSeq != 1002 || p.HighSeq != int64(len(fs)) {
		t.Fatalf("first reconciliation: %+v", p)
	}
	// New events cannot extend the high-water captured before reconciliation.
	guardExec(t, env, `INSERT INTO events(owner,type,at,data) VALUES (?,'delivery',?,?)`, guardB, guardAt, fs[0].Data)
	restarted, err := hub.New(h.DB, h.Cfg, env.Now.Now)
	if err != nil {
		t.Fatal(err)
	}
	h = restarted
	var calls int
	h.Now = func() time.Time { calls++; return env.Now.Now().Add(time.Duration(calls) * time.Second) }
	p = slice(t, h, hub.BackfillLimits{Rows: 100, Target: time.Millisecond})
	if p.ExceptionCount != 3 || p.ExceptionCountSeq != 1003 || p.Processed != 0 {
		t.Fatalf("reconciliation missed clock budget or restart: %+v", p)
	}
	h.Now = env.Now.Now
	for _, want := range []int64{5, 7} {
		p = slice(t, h, hub.BackfillLimits{Rows: 2})
		if p.ExceptionCount != want || p.ExceptionCountPending != (want < 7) || p.Processed != 0 {
			t.Fatalf("reconciliation resume: %+v", p)
		}
	}
	p = finishBackfill(t, h)
	if p.ExceptionCount != 7 || p.HighSeq != int64(len(fs)) || p.TotalConverted != int64(backfillCount(t, h, fmt.Sprintf(`SELECT count(*) FROM events WHERE type='delivery' AND seq<=%d`, len(fs)))) {
		t.Fatalf("forward scan after reconciliation: %+v", p)
	}
	assertBackfillBytes(t, h, fs)
}
