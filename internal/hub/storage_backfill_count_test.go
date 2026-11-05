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
