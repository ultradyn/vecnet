package hub_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/auth"
	"github.com/XertroV/vecnet/internal/eventlog"
	"github.com/XertroV/vecnet/internal/eventpayload"
	"github.com/XertroV/vecnet/internal/hub"
	"github.com/XertroV/vecnet/internal/store"
	"github.com/XertroV/vecnet/internal/testhub"
)

func backfillEnv(t *testing.T) (*testhub.Env, []guardFixture) {
	t.Helper()
	env, fs := guardEnv(t)
	env.Hub.Cfg.BackfillEnabled = true
	env.Hub.Cfg.BackfillCapacity = func(context.Context) (hub.BackfillCapacity, error) {
		return hub.BackfillCapacity{AvailableBytes: 1 << 40}, nil
	}
	env.Hub.Cfg.BackfillCheckpoint = func(context.Context) (hub.BackfillCheckpoint, error) { return hub.BackfillCheckpoint{}, nil }
	return env, fs
}
func slice(t *testing.T, h *hub.Hub, limits hub.BackfillLimits) hub.BackfillProgress {
	t.Helper()
	p, err := h.BackfillDeliverySlice(context.Background(), limits)
	if err != nil {
		t.Fatal(err)
	}
	if p.Processed != p.Converted+p.Skipped+p.Failed {
		t.Fatal("slice row counts do not balance")
	}
	return p
}
func backfillCount(t *testing.T, h *hub.Hub, q string) int {
	t.Helper()
	var n int
	if err := h.DB.QueryRow(q).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
func assertBackfillBytes(t *testing.T, h *hub.Hub, fs []guardFixture) {
	t.Helper()
	err := h.DB.ReadSnapshot(context.Background(), func(q *sql.Conn) error {
		for i, f := range fs {
			var p eventpayload.StoredPayload
			var msg, snap sql.NullString
			if err := q.QueryRowContext(context.Background(), `SELECT payload_format,data,msg_id,snapshot_id FROM events WHERE seq=?`, i+1).Scan(&p.Format, &p.Data, &msg, &snap); err != nil {
				return err
			}
			p.MsgID, p.SnapshotID = msg.String, snap.String
			got, err := eventpayload.HydrateBatch(context.Background(), q, []eventpayload.StoredPayload{p})
			if err != nil {
				return err
			}
			if !bytes.Equal(got[0], []byte(f.Data)) {
				return fmt.Errorf("wire bytes changed at seq %d", i+1)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
func finishBackfill(t *testing.T, h *hub.Hub) hub.BackfillProgress {
	t.Helper()
	for range 100 {
		p := slice(t, h, hub.BackfillLimits{Rows: 2})
		if p.Done {
			return p
		}
	}
	t.Fatal("backfill never completed")
	return hub.BackfillProgress{}
}
func TestBackfillRepeatResumeCapturedHighWater(t *testing.T) {
	env, fs := backfillEnv(t)
	h := env.Hub
	p := slice(t, h, hub.BackfillLimits{Rows: 1})
	if p.Converted != 1 || p.LastSeq != 1 || p.HighSeq != int64(len(fs)) || p.Done {
		t.Fatalf("unexpected first progress: %+v", p)
	}
	guardExec(t, env, `INSERT INTO events(owner,type,at,data) VALUES (?,'delivery',?,?)`, guardB, guardAt, fs[0].Data)
	resumed, err := hub.New(h.DB, h.Cfg, env.Now.Now)
	if err != nil {
		t.Fatal(err)
	}
	p = finishBackfill(t, resumed)
	if p.HighSeq != int64(len(fs)) || p.LastSeq != p.HighSeq {
		t.Fatalf("high-water moved: %+v", p)
	}
	if backfillCount(t, h, `SELECT count(*) FROM events WHERE type='delivery' AND payload_format='inline'`) != 1 {
		t.Fatal("post-capture row was converted")
	}
	if again := slice(t, resumed, hub.BackfillLimits{}); !again.Done || again.Converted != 0 {
		t.Fatalf("repeat not idempotent: %+v", again)
	}
	assertBackfillBytes(t, h, fs)
}
func TestBackfillCrashBeforeAfterCommit(t *testing.T) {
	env, fs := backfillEnv(t)
	h := env.Hub
	guardExec(t, env, `CREATE TRIGGER fail_checkpoint BEFORE INSERT ON meta WHEN NEW.key='delivery_backfill_v1' BEGIN SELECT RAISE(ABORT,'synthetic_checkpoint_failure'); END`)
	if _, err := h.BackfillDeliverySlice(context.Background(), hub.BackfillLimits{Rows: 2}); err == nil {
		t.Fatal("checkpoint failure accepted")
	}
	if backfillCount(t, h, `SELECT count(*) FROM events WHERE payload_format!='inline'`) != 0 || backfillCount(t, h, `SELECT count(*) FROM event_message_snapshots`) != 0 {
		t.Fatal("uncommitted conversion escaped")
	}
	guardExec(t, env, `DROP TRIGGER fail_checkpoint`)
	p := slice(t, h, hub.BackfillLimits{Rows: 2})
	if p.Converted != 2 {
		t.Fatal("retry did not convert")
	}
	restarted, err := hub.New(h.DB, h.Cfg, env.Now.Now)
	if err != nil {
		t.Fatal(err)
	}
	next := slice(t, restarted, hub.BackfillLimits{Rows: 1})
	if next.LastSeq <= p.LastSeq {
		t.Fatal("committed checkpoint lost")
	}
	finishBackfill(t, restarted)
	assertBackfillBytes(t, restarted, fs)
}

// Exit a real process with an open SQLite writer, then exit a second process
// just after commit. The parent verifies actual WAL recovery, not a retry mock.
func TestBackfillProcessCrash(t *testing.T) {
	if mode := os.Getenv("VECNET_BACKFILL_CRASH"); mode != "" {
		dir := os.Getenv("VECNET_BACKFILL_DIR")
		db, err := store.Open(filepath.Join(dir, "hub.db"))
		if err != nil {
			t.Fatal("child store open failed")
		}
		cfg := hub.DefaultConfig()
		cfg.DataDir = dir
		cfg.BackfillEnabled = true
		cfg.BackfillCapacity = func(context.Context) (hub.BackfillCapacity, error) {
			return hub.BackfillCapacity{AvailableBytes: 1 << 40}, nil
		}
		cfg.BackfillCheckpoint = func(context.Context) (hub.BackfillCheckpoint, error) { return hub.BackfillCheckpoint{}, nil }
		h, err := hub.New(db, cfg, func() time.Time { return time.Unix(1800000000, 0) })
		if err != nil {
			t.Fatal("child hub open failed")
		}
		calls := 0
		h.Now = func() time.Time {
			calls++
			if mode == "before" && calls == 2 {
				os.Exit(27)
			}
			return time.Unix(1800000000, 0)
		}
		if p, err := h.BackfillDeliverySlice(context.Background(), hub.BackfillLimits{Rows: 2}); err != nil || p.Converted != 2 {
			t.Fatal("child slice failed")
		}
		os.Exit(27)
	}
	env, fs := backfillEnv(t)
	h := env.Hub
	guardExec(t, env, `DELETE FROM messages WHERE msg_id='msg_00000000000000000000000001'`)
	for _, mode := range []string{"before", "after"} {
		cmd := exec.Command(os.Args[0], "-test.run=^TestBackfillProcessCrash$")
		dir := h.Cfg.DataDir
		cmd.Env = []string{"VECNET_BACKFILL_CRASH=" + mode, "VECNET_BACKFILL_DIR=" + dir, "HOME=" + dir, "XDG_CONFIG_HOME=" + dir, "XDG_CACHE_HOME=" + dir, "VECNET_CONFIG=" + dir}
		err := cmd.Run()
		var exited *exec.ExitError
		if !errors.As(err, &exited) || exited.ExitCode() != 27 {
			t.Fatalf("crash process exit=%v", err)
		}
		if mode == "before" {
			if backfillCount(t, h, `SELECT count(*) FROM events WHERE payload_format!='inline'`) != 0 || backfillCount(t, h, `SELECT count(*) FROM event_message_snapshots`) != 0 || backfillCount(t, h, `SELECT count(*) FROM meta WHERE key='delivery_backfill_v1'`) != 0 {
				t.Fatal("process crash leaked an uncommitted slice")
			}
		} else {
			if backfillCount(t, h, `SELECT count(*) FROM events WHERE payload_format!='inline'`) != 2 {
				t.Fatal("process crash lost committed slice")
			}
			p := slice(t, h, hub.BackfillLimits{Rows: 1})
			if p.LastSeq <= 2 {
				t.Fatal("process recovery lost checkpoint")
			}
		}
	}
	finishBackfill(t, h)
	assertBackfillBytes(t, h, fs)
}

func TestBackfillCancellationRollsBackWholeSlice(t *testing.T) {
	env, _ := backfillEnv(t)
	h := env.Hub
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	h.Now = func() time.Time {
		calls++
		if calls == 2 {
			cancel()
		}
		return env.Now.Now()
	}
	if _, err := h.BackfillDeliverySlice(ctx, hub.BackfillLimits{Rows: 2}); !errors.Is(err, context.Canceled) {
		t.Fatalf("mid-slice cancel=%v", err)
	}
	if backfillCount(t, h, `SELECT count(*) FROM events WHERE payload_format!='inline'`) != 0 || backfillCount(t, h, `SELECT count(*) FROM meta WHERE key='delivery_backfill_v1'`) != 0 {
		t.Fatal("cancel committed partial work")
	}
	h.Now = env.Now.Now
	if p := slice(t, h, hub.BackfillLimits{Rows: 2}); p.Converted != 2 {
		t.Fatal("cancelled slice did not resume")
	}
}

func TestBackfillRealReaderCheckpointPressure(t *testing.T) {
	env, _ := backfillEnv(t)
	h := env.Hub
	h.Cfg.BackfillCheckpoint = nil
	h.Cfg.BackfillCapacity = nil
	conn, err := h.DB.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err = conn.ExecContext(context.Background(), `BEGIN DEFERRED`); err != nil {
		t.Fatal(err)
	}
	var n int
	if err = conn.QueryRowContext(context.Background(), `SELECT count(*) FROM events`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	guardExec(t, env, `INSERT INTO events(owner,type,at,data) VALUES (?,'presence',?,'{}')`, guardB, guardAt)
	p := slice(t, h, hub.BackfillLimits{Rows: 1})
	if p.PausedReason != "wal_pressure" || p.Converted != 0 {
		t.Fatalf("reader did not pause: %+v", p)
	}
	if _, err = conn.ExecContext(context.Background(), `ROLLBACK`); err != nil {
		t.Fatal(err)
	}
	if p = slice(t, h, hub.BackfillLimits{Rows: 1}); p.Converted != 1 {
		t.Fatalf("reader relief did not resume: %+v", p)
	}
}

func TestBackfillPoolPressureYields(t *testing.T) {
	env, _ := backfillEnv(t)
	h := env.Hub
	for range 4 {
		conn, err := h.DB.Conn(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	p, err := h.BackfillDeliverySlice(ctx, hub.BackfillLimits{Target: 5 * time.Millisecond})
	if err != nil || p.PausedReason != "lock_pressure" || ctx.Err() != nil {
		t.Fatalf("pool pressure failed to yield: reason=%s err=%v", p.PausedReason, err)
	}
}
func TestBackfillLegacySnapshotsAndDurableExceptions(t *testing.T) {
	env, fs := backfillEnv(t)
	h := env.Hub
	// Missing canonical bytes and legacy key order require immutable snapshots.
	guardExec(t, env, `DELETE FROM messages WHERE msg_id='msg_00000000000000000000000001'`)
	guardExec(t, env, `UPDATE messages SET body=json_set(body,'$.sender_info.name','changed') WHERE msg_id='msg_00000000000000000000000002'`)
	guardExec(t, env, `UPDATE messages SET body=json_set(body,'$.attachments',json('[]')) WHERE msg_id='msg_00000000000000000000000008'`)
	guardExec(t, env, `UPDATE deliveries SET body_sha256='bad' WHERE event_seq=3`)
	unknown := `{"message":{"message_id":"msg_unknown","future":true},"future":42}`
	guardExec(t, env, `INSERT INTO events(owner,type,at,data) VALUES (?,'delivery',?,?)`, guardB, guardAt, unknown)
	finishBackfill(t, h)
	if backfillCount(t, h, `SELECT count(*) FROM delivery_backfill_exceptions`) != 2 {
		t.Fatal("exceptions not durable")
	}
	if backfillCount(t, h, `SELECT count(*) FROM delivery_backfill_exceptions WHERE seq=3 AND reason='delivery_hash_mismatch'`) != 1 {
		t.Fatal("hash mismatch not recorded")
	}
	if backfillCount(t, h, `SELECT count(*) FROM deliveries WHERE event_seq=3 AND body_sha256='bad'`) != 1 {
		t.Fatal("historical hash rewritten")
	}
	if backfillCount(t, h, `SELECT count(*) FROM events WHERE seq=3 AND payload_format='inline'`) != 1 {
		t.Fatal("invalid hash converted")
	}
	if backfillCount(t, h, `SELECT count(*) FROM event_message_snapshots`) < 2 {
		t.Fatal("legacy snapshots absent")
	}
	assertBackfillBytes(t, h, fs)
	guardExec(t, env, `UPDATE deliveries SET body_sha256=? WHERE event_seq=3`, fs[2].SHA256)
	if p := slice(t, h, hub.BackfillLimits{}); p.Converted != 0 {
		t.Fatal("exceptions automatically retried")
	}
	p := slice(t, h, hub.BackfillLimits{RescanExceptions: true})
	if p.Converted != 1 || backfillCount(t, h, `SELECT count(*) FROM delivery_backfill_exceptions`) != 1 {
		t.Fatalf("explicit rescan failed: %+v", p)
	}
	assertBackfillBytes(t, h, fs)
}
func TestBackfillBudgetsDryRunAndCancellation(t *testing.T) {
	t.Run("rows", func(t *testing.T) {
		env, _ := backfillEnv(t)
		p := slice(t, env.Hub, hub.BackfillLimits{Rows: 2})
		if p.Processed != 2 {
			t.Fatalf("row budget: %+v", p)
		}
	})
	t.Run("bytes", func(t *testing.T) {
		env, fs := backfillEnv(t)
		p := slice(t, env.Hub, hub.BackfillLimits{Bytes: int64(len(fs[0].Data))})
		if p.Processed != 1 || p.SourceBytes != int64(len(fs[0].Data)) {
			t.Fatalf("byte budget: %+v", p)
		}
	})
	t.Run("oversize", func(t *testing.T) {
		env, _ := backfillEnv(t)
		p := slice(t, env.Hub, hub.BackfillLimits{Bytes: 1})
		if p.Processed != 1 {
			t.Fatal("single oversize event did not get its own slice")
		}
	})
	t.Run("time", func(t *testing.T) {
		env, _ := backfillEnv(t)
		var calls int
		env.Hub.Now = func() time.Time { calls++; return env.Now.Now().Add(time.Duration(calls) * time.Second) }
		p := slice(t, env.Hub, hub.BackfillLimits{Target: time.Millisecond})
		if p.Processed != 1 {
			t.Fatalf("time budget: %+v", p)
		}
	})
	t.Run("dry_run", func(t *testing.T) {
		env, fs := backfillEnv(t)
		p := slice(t, env.Hub, hub.BackfillLimits{Rows: 2, DryRun: true})
		if p.Processed != 2 || p.SourceBytes == 0 || len(p.Seqs) != 2 {
			t.Fatal("dry-run missing ids/counts/bytes")
		}
		if backfillCount(t, env.Hub, `SELECT count(*) FROM events WHERE payload_format!='inline'`) != 0 || backfillCount(t, env.Hub, `SELECT count(*) FROM meta WHERE key='delivery_backfill_v1'`) != 0 {
			t.Fatal("dry run persisted work")
		}
		assertBackfillBytes(t, env.Hub, fs)
	})
	t.Run("cancel", func(t *testing.T) {
		env, _ := backfillEnv(t)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := env.Hub.BackfillDeliverySlice(ctx, hub.BackfillLimits{}); !errors.Is(err, context.Canceled) {
			t.Fatalf("cancel: %v", err)
		}
	})
}

func TestBackfillUnknownFieldsAndDuplicateKeys(t *testing.T) {
	env, fs := backfillEnv(t)
	h := env.Hub
	var f map[string]json.RawMessage
	if err := json.Unmarshal([]byte(fs[0].Data), &f); err != nil {
		t.Fatal(err)
	}
	// Preserve an unknown legacy field and whitespace without normalizing keys.
	embedded := bytes.TrimSpace(f["message"])
	original := ` { "future_delivery": {"n":1.50}, "message": ` + string(embedded[:len(embedded)-1]) + `,"future_message":{"n":1.50}} } `
	duplicate := `{"message":` + string(f["message"]) + `,"message":` + string(f["message"]) + `}`
	for _, data := range []string{original, duplicate} {
		guardExec(t, env, `INSERT INTO events(owner,type,at,data) VALUES (?,'delivery',?,?)`, guardB, guardAt, data)
	}
	p := finishBackfill(t, h)
	if p.ExceptionCount != 1 || backfillCount(t, h, `SELECT count(*) FROM delivery_backfill_exceptions WHERE reason='duplicate_key'`) != 1 {
		t.Fatal("duplicate critical key was not quarantined")
	}
	var stored eventpayload.StoredPayload
	if err := h.DB.QueryRow(`SELECT payload_format,data,msg_id,coalesce(snapshot_id,'') FROM events WHERE seq=?`, len(fs)+1).Scan(&stored.Format, &stored.Data, &stored.MsgID, &stored.SnapshotID); err != nil {
		t.Fatal(err)
	}
	if stored.Format != eventpayload.DeliveryRefV1 {
		t.Fatal("recoverable unknown field remained inline")
	}
	got, err := eventpayload.HydrateBatch(context.Background(), h.DB, []eventpayload.StoredPayload{stored})
	if err != nil || !bytes.Equal(got[0], []byte(original)) {
		t.Fatal("unknown-field wire bytes changed")
	}
	var unchanged string
	if err := h.DB.QueryRow(`SELECT data FROM events WHERE seq=? AND payload_format='inline'`, len(fs)+2).Scan(&unchanged); err != nil || unchanged != duplicate {
		t.Fatal("duplicate-key original changed")
	}
}
func TestBackfillCapacityWALLocksAndFull(t *testing.T) {
	for _, tc := range []struct {
		name, reason   string
		available, wal int64
		busy           bool
	}{
		{"low_capacity", "low_headroom", 64 << 20, 0, false},
		{"wal_growth", "wal_pressure", 65 << 20, 2 << 20, false},
		{"busy_reader", "wal_pressure", 1 << 40, 1 << 20, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env, _ := backfillEnv(t)
			h := env.Hub
			h.Cfg.BackfillCapacity = func(context.Context) (hub.BackfillCapacity, error) {
				return hub.BackfillCapacity{AvailableBytes: tc.available, WALBytes: tc.wal}, nil
			}
			h.Cfg.BackfillCheckpoint = func(context.Context) (hub.BackfillCheckpoint, error) {
				return hub.BackfillCheckpoint{Busy: tc.busy}, nil
			}
			p := slice(t, h, hub.BackfillLimits{})
			if p.PausedReason != tc.reason || p.Processed != 0 {
				t.Fatalf("unsafe gate: %+v", p)
			}
			if backfillCount(t, h, `SELECT count(*) FROM meta WHERE key='delivery_backfill_v1'`) != 0 {
				t.Fatal("pause advanced checkpoint")
			}
		})
	}
	t.Run("writer_lock", func(t *testing.T) {
		env, _ := backfillEnv(t)
		tx, err := env.Hub.DB.BeginTx(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		p := slice(t, env.Hub, hub.BackfillLimits{})
		if p.PausedReason != "lock_pressure" {
			t.Fatalf("lock pressure: %+v", p)
		}
	})
	t.Run("sqlite_full", func(t *testing.T) {
		env, _ := backfillEnv(t)
		h := env.Hub
		h.DB.SetMaxOpenConns(1)
		// Make conversion require a large legacy snapshot, then cap actual SQLite allocation.
		guardExec(t, env, `UPDATE messages SET body=json_set(body,'$.sender_info.name','canonical')`)
		guardExec(t, env, `UPDATE events SET data=json_set(data,'$.message.metadata.synthetic',?) WHERE seq=1`, strings.Repeat("x", 512<<10))
		guardExec(t, env, `DELETE FROM deliveries WHERE event_seq=1`)
		var pages int
		if err := h.DB.QueryRow(`PRAGMA page_count`).Scan(&pages); err != nil {
			t.Fatal(err)
		}
		guardExec(t, env, fmt.Sprintf(`PRAGMA max_page_count=%d`, pages))
		p := slice(t, h, hub.BackfillLimits{Rows: 1})
		if p.PausedReason != "sqlite_full" || p.Converted != 0 {
			t.Fatalf("SQLITE_FULL not paused: %+v", p)
		}
		if backfillCount(t, h, `SELECT count(*) FROM events WHERE payload_format!='inline'`) != 0 || backfillCount(t, h, `SELECT count(*) FROM event_message_snapshots`) != 0 || backfillCount(t, h, `SELECT count(*) FROM meta WHERE key='delivery_backfill_v1'`) != 0 {
			t.Fatal("SQLITE_FULL committed partial slice")
		}
		guardExec(t, env, `PRAGMA max_page_count=1073741823`)
		if p := slice(t, h, hub.BackfillLimits{Rows: 1}); p.Converted != 1 {
			t.Fatalf("capacity relief did not resume: %+v", p)
		}
	})
}
func TestBackfillBackupRestoreAndConcurrentTraffic(t *testing.T) {
	env, fs := backfillEnv(t)
	h := env.Hub
	first := slice(t, h, hub.BackfillLimits{Rows: 1})
	backup := filepath.Join(t.TempDir(), "backup.db")
	// Quiesce writers, checkpoint, and copy a consistent disposable database.
	if _, err := h.DB.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(h.Cfg.DataDir, "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(backup, raw, 0600); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(backup)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	cfg := h.Cfg
	cfg.DataDir = filepath.Dir(backup)
	restored, err := hub.New(db, cfg, env.Now.Now)
	if err != nil {
		t.Fatal(err)
	}
	assertBackfillBytes(t, restored, fs)
	p := finishBackfill(t, restored)
	if p.HighSeq != first.HighSeq {
		t.Fatal("backup high-water changed")
	}
	assertBackfillBytes(t, restored, fs)
	h.Cfg.DeliveryReferences = true
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	wg.Add(2)
	go func() {
		defer wg.Done()
		for range 10 {
			if _, err := h.Log.Read(context.Background(), []string{guardB}, eventlog.Cursor{Epoch: 1}, 100); err != nil {
				errs <- err
				return
			}
		}
	}()
	go func() {
		defer wg.Done()
		for i := range 10 {
			if _, _, err := h.Send(context.Background(), auth.Caller{Kind: "peer", PeerID: guardA, PrincipalID: guardPrn}, []byte(fmt.Sprintf(`{"idempotency_key":"backfill-concurrent-%d","recipients":["%s"],"priority":"hold","content":{"text":"synthetic concurrent send"}}`, i, guardB)), "http_send"); err != nil {
				errs <- err
				return
			}
		}
	}()
	var worst time.Duration
	for range 100 {
		start := time.Now()
		p = slice(t, h, hub.BackfillLimits{Rows: 2})
		if d := time.Since(start); d > worst {
			worst = d
		}
		if p.Done {
			break
		}
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	if !p.Done || p.HighSeq != first.HighSeq {
		t.Fatal("concurrent writes moved high-water")
	}
	assertBackfillBytes(t, h, fs)
	var integrity string
	if err := h.DB.QueryRow(`PRAGMA integrity_check`).Scan(&integrity); err != nil || integrity != "ok" {
		t.Fatal("integrity failed")
	}
	rows, err := h.DB.Query(`PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if rows.Next() || rows.Err() != nil {
		t.Fatal("foreign key violation")
	}
	t.Logf("worst_slice_duration=%s required_headroom_bytes=%d", worst, p.RequiredHeadroomBytes)
}
func TestBackfillOperatorStatusAndTicker(t *testing.T) {
	env, _ := backfillEnv(t)
	h := env.Hub
	for _, tc := range []struct {
		secret string
		want   int
	}{{"", 401}, {guardSecret + guardB, 403}, {env.OperatorSecret, 200}} {
		resp, raw := env.Do(tc.secret, "GET", "/v1/admin/storage/backfill", nil)
		if resp.StatusCode != tc.want {
			t.Fatalf("status auth=%d want=%d", resp.StatusCode, tc.want)
		}
		if tc.want == 200 {
			var p hub.BackfillProgress
			if json.Unmarshal(raw, &p) != nil {
				t.Fatal("status not JSON")
			}
			if strings.Contains(string(raw), "message_id") || strings.Contains(string(raw), "blob_url") {
				t.Fatal("status leaked payload")
			}
		}
	}
	h.Cfg.BackfillEnabled = false
	h.Tick(context.Background(), true)
	if backfillCount(t, h, `SELECT count(*) FROM events WHERE payload_format!='inline'`) != 0 {
		t.Fatal("disabled ticker backfilled")
	}
	h.Cfg.BackfillEnabled = true
	h.Tick(context.Background(), false)
	if backfillCount(t, h, `SELECT count(*) FROM events WHERE payload_format!='inline'`) != 0 {
		t.Fatal("fast ticker backfilled")
	}
	h.Tick(context.Background(), true)
	if backfillCount(t, h, `SELECT count(*) FROM events WHERE payload_format!='inline'`) == 0 {
		t.Fatal("slow ticker omitted slice")
	}
}
