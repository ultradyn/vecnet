package hub_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/auth"
	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/eventlog"
	"github.com/XertroV/vecnet/internal/eventpayload"
	"github.com/XertroV/vecnet/internal/hub"
	"github.com/XertroV/vecnet/internal/store"
	"github.com/XertroV/vecnet/internal/testhub"
	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
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
	// Only the checkpoint probe is real: the injected 1 TiB capacity probe
	// stays, so low host disk can never produce this pause instead of the
	// reader holding the WAL.
	h.Cfg.BackfillCheckpoint = nil
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
	if p.PausedReason != "wal_pressure" || p.Converted != 0 || p.AvailableBytes != 1<<40 || p.WALBytes != 0 {
		t.Fatalf("reader did not pause the checkpoint probe: %+v", p)
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

// Neither embedded id is authoritative for locating an existing delivery.
func TestBackfillHashMismatchWithDisagreeingEmbeddedIDs(t *testing.T) {
	env, fs := backfillEnv(t)
	h := env.Hub
	original := strings.ReplaceAll(fs[0].Data, "msg_00000000000000000000000001", "msg_00000000000000000000000099")
	original = strings.ReplaceAll(original, "dly_00000000000000000000000001", "dly_00000000000000000000000099")
	if original == fs[0].Data || strings.Contains(original, "msg_00000000000000000000000001") || strings.Contains(original, "dly_00000000000000000000000001") {
		t.Fatal("synthetic embedded ids did not change")
	}
	guardExec(t, env, `UPDATE events SET data=? WHERE seq=1`, original)
	p := slice(t, h, hub.BackfillLimits{Rows: 1})
	if p.Converted != 0 || p.Failed != 1 || p.LastSeq != 1 {
		t.Fatalf("linked hash mismatch bypassed: converted=%d failed=%d last_seq=%d", p.Converted, p.Failed, p.LastSeq)
	}
	var data, format, hash string
	if err := h.DB.QueryRow(`SELECT data,payload_format FROM events WHERE seq=1`).Scan(&data, &format); err != nil {
		t.Fatal(err)
	}
	if err := h.DB.QueryRow(`SELECT body_sha256 FROM deliveries WHERE event_seq=1`).Scan(&hash); err != nil {
		t.Fatal(err)
	}
	if data != original || format != eventpayload.Inline || hash != fs[0].SHA256 {
		t.Fatal("exception changed original bytes, format or delivery hash")
	}
	if backfillCount(t, h, `SELECT count(*) FROM delivery_backfill_exceptions WHERE seq=1 AND reason='delivery_hash_mismatch'`) != 1 || backfillCount(t, h, `SELECT count(*) FROM event_message_snapshots`) != 0 {
		t.Fatal("hash mismatch lacks durable exception or created a snapshot")
	}
	restarted, err := hub.New(h.DB, h.Cfg, env.Now.Now)
	if err != nil {
		t.Fatal(err)
	}
	status, err := restarted.BackfillDeliveryStatus(context.Background(), auth.Caller{Kind: "operator"})
	if err != nil || status.LastSeq != 1 || status.TotalFailed != 1 || len(status.Exceptions) != 1 || status.Exceptions[0].Reason != "delivery_hash_mismatch" {
		t.Fatal("hash exception did not survive restart")
	}
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
		// Leave space after the first row, but too little for the second.
		limit := int64(len(fs[0].Data) + len(fs[1].Data)/2)
		p := slice(t, env.Hub, hub.BackfillLimits{Bytes: limit})
		if p.Processed != 1 || p.SourceBytes != int64(len(fs[0].Data)) || p.LastSeq != 1 || p.Done {
			t.Fatalf("cumulative byte budget: %+v", p)
		}
		if backfillCount(t, env.Hub, `SELECT count(*) FROM events WHERE seq=2 AND payload_format='inline'`) != 1 {
			t.Fatal("overflow row converted in first slice")
		}
		next := slice(t, env.Hub, hub.BackfillLimits{Rows: 1, Bytes: limit})
		if next.Converted != 1 || len(next.Seqs) != 1 || next.Seqs[0] != 2 || next.SourceBytes != int64(len(fs[1].Data)) {
			t.Fatal("deferred overflow row was not converted next")
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
	t.Run("configured_byte_reserve", func(t *testing.T) {
		env, _ := backfillEnv(t)
		h := env.Hub
		const wal = int64(3 << 20)
		const required = int64(128<<20) + wal
		available := required - 1
		h.Cfg.BackfillCapacity = func(context.Context) (hub.BackfillCapacity, error) {
			return hub.BackfillCapacity{AvailableBytes: available, WALBytes: wal}, nil
		}
		limits := hub.BackfillLimits{Rows: 1, Bytes: 32 << 20}
		for _, capacity := range []int64{required - 1, required} {
			available = capacity
			p := slice(t, h, limits)
			if p.RequiredHeadroomBytes != required || p.PausedReason != "wal_pressure" || p.Processed != 0 {
				t.Fatalf("configured reserve did not pause at capacity=%d: %+v", capacity, p)
			}
			if backfillCount(t, h, `SELECT count(*) FROM meta WHERE key='delivery_backfill_v1'`) != 0 || backfillCount(t, h, `SELECT count(*) FROM events WHERE payload_format!='inline'`) != 0 {
				t.Fatal("capacity pause persisted work")
			}
		}
		available = required + 1
		p := slice(t, h, limits)
		if p.RequiredHeadroomBytes != required || p.PausedReason != "" || p.Converted != 1 || p.LastSeq != 1 {
			t.Fatalf("capacity relief did not resume above threshold: %+v", p)
		}
	})
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
		// max_page_count and the pre-update hook are per connection.
		h.DB.SetMaxOpenConns(1)
		// seq 1 converts with a legacy snapshot, seq 2 becomes a durable
		// exception, and only seq 3 needs a snapshot too large for the page cap:
		// SQLITE_FULL arrives after earlier work in the same slice.
		guardExec(t, env, `UPDATE messages SET body=json_set(body,'$.sender_info.name','canonical')`)
		guardExec(t, env, `UPDATE deliveries SET body_sha256='bad' WHERE event_seq=2`)
		guardExec(t, env, `UPDATE events SET data=json_set(data,'$.message.metadata.synthetic',?) WHERE seq=3`, strings.Repeat("x", 512<<10))
		guardExec(t, env, `DELETE FROM deliveries WHERE event_seq=3`)
		before := backfillState(t, h)
		var pages int
		if err := h.DB.QueryRow(`PRAGMA page_count`).Scan(&pages); err != nil {
			t.Fatal(err)
		}
		guardExec(t, env, fmt.Sprintf(`PRAGMA max_page_count=%d`, pages+2))
		type write struct {
			op    int32
			table string
			rowid int64
		}
		var writes []write
		hook := func(fn sqlite.PreUpdateHookFn) {
			t.Helper()
			conn, err := h.DB.Conn(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			if err := conn.Raw(func(raw any) error {
				raw.(sqlite.HookRegisterer).RegisterPreUpdateHook(fn)
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		}
		hook(func(d sqlite.SQLitePreUpdateData) {
			writes = append(writes, write{d.Op, d.TableName, d.NewRowID})
		})
		p := slice(t, h, hub.BackfillLimits{Rows: 3})
		hook(nil)
		if p.PausedReason != "sqlite_full" || p.Converted != 0 || p.Failed != 0 || len(p.Seqs) != 0 {
			t.Fatalf("SQLITE_FULL not paused: %+v", p)
		}
		var converted1, exception2, snapshot, touched3 bool
		for _, w := range writes {
			converted1 = converted1 || (w.op == sqlite3.SQLITE_UPDATE && w.table == "events" && w.rowid == 1)
			exception2 = exception2 || (w.op == sqlite3.SQLITE_INSERT && w.table == "delivery_backfill_exceptions" && w.rowid == 2)
			snapshot = snapshot || (w.op == sqlite3.SQLITE_INSERT && w.table == "event_message_snapshots")
			touched3 = touched3 || (w.table == "events" && w.rowid == 3)
		}
		if !converted1 || !exception2 || !snapshot || touched3 {
			t.Fatalf("the failing slice did not convert seq 1, record seq 2 and write a snapshot before seq 3 failed: %+v", writes)
		}
		// Reopen: the committed state is exactly the pre-slice state.
		db, err := store.Open(filepath.Join(h.Cfg.DataDir, "hub.db"))
		if err != nil {
			t.Fatal(err)
		}
		after := backfillStateDB(t, db)
		db.Close()
		if after != before {
			t.Fatalf("SQLITE_FULL committed partial slice work:\nbefore %s\nafter  %s", before, after)
		}
		guardExec(t, env, `PRAGMA max_page_count=1073741823`)
		p = slice(t, h, hub.BackfillLimits{Rows: 3})
		if p.Converted != 2 || p.Failed != 1 || p.LastSeq != 3 || fmt.Sprint(p.Seqs) != "[1 2 3]" || p.PausedReason != "" {
			t.Fatalf("capacity relief did not resume exactly where the failed slice began: %+v", p)
		}
		if backfillCount(t, h, `SELECT count(*) FROM delivery_backfill_exceptions WHERE seq=2 AND reason='delivery_hash_mismatch'`) != 1 ||
			backfillCount(t, h, `SELECT count(*) FROM events WHERE seq IN (1,3) AND payload_format='delivery_ref_v1'`) != 2 {
			t.Fatal("resumed slice did not redo the rolled-back work")
		}
	})
}

// backfillState is a digest of every row a slice may write: event formats and
// bytes, snapshots, durable exceptions and the committed checkpoint.
func backfillState(t *testing.T, h *hub.Hub) string {
	t.Helper()
	return backfillStateDB(t, h.DB)
}

func backfillStateDB(t *testing.T, db *store.DB) string {
	t.Helper()
	var out strings.Builder
	for _, q := range []string{
		`SELECT seq||':'||payload_format||':'||coalesce(msg_id,'')||':'||coalesce(snapshot_id,'')||':'||length(data)||':'||hex(substr(data,1,64)) FROM events ORDER BY seq`,
		`SELECT snapshot_id||':'||msg_id||':'||body_sha256 FROM event_message_snapshots ORDER BY snapshot_id`,
		`SELECT seq||':'||reason FROM delivery_backfill_exceptions ORDER BY seq`,
		`SELECT key||':'||value FROM meta WHERE key IN ('delivery_backfill_v1','event_payload_refs_enabled') ORDER BY key`,
	} {
		rows, err := db.Query(q)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var v string
			if err := rows.Scan(&v); err != nil {
				rows.Close()
				t.Fatal(err)
			}
			out.WriteString(v)
			out.WriteByte(';')
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		rows.Close()
		out.WriteByte('|')
	}
	return fmt.Sprintf("%x", sha256.Sum256([]byte(out.String())))
}

func TestBackfillBackupRestoresSnapshotsAndCheckpoint(t *testing.T) {
	env, fs := backfillEnv(t)
	h := env.Hub
	// Force the first committed conversion to depend on a legacy snapshot.
	guardExec(t, env, `UPDATE messages SET body=json_set(body,'$.sender_info.name','canonical changed') WHERE msg_id='msg_00000000000000000000000001'`)
	first := slice(t, h, hub.BackfillLimits{Rows: 1})
	if first.Converted != 1 || first.LastSeq != 1 || first.Done {
		t.Fatal("backup setup did not commit one unfinished slice")
	}
	var snapshotID, snapshotBody, checkpoint string
	if err := h.DB.QueryRow(`SELECT s.snapshot_id,s.body FROM events e JOIN event_message_snapshots s ON s.snapshot_id=e.snapshot_id WHERE e.seq=1`).Scan(&snapshotID, &snapshotBody); err != nil {
		t.Fatal("first conversion did not require a snapshot")
	}
	var wire struct{ Message json.RawMessage }
	if err := json.Unmarshal([]byte(fs[0].Data), &wire); err != nil || !bytes.Equal(wire.Message, []byte(snapshotBody)) {
		t.Fatal("snapshot does not contain the exact legacy Message bytes")
	}
	if err := h.DB.QueryRow(`SELECT value FROM meta WHERE key='delivery_backfill_v1'`).Scan(&checkpoint); err != nil {
		t.Fatal(err)
	}
	// This row must survive restore but remain outside the captured scan. A
	// lost checkpoint would recapture a different high-water and convert it.
	guardExec(t, env, `INSERT INTO events(owner,type,at,data) VALUES (?,'delivery',?,?)`, guardB, guardAt, fs[0].Data)
	backup := filepath.Join(t.TempDir(), "backup.db")
	// There are no concurrent writers in this test: checkpoint then copy the
	// quiesced main database, including committed snapshots and metadata.
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
	var restoredCheckpoint, restoredSnapshot string
	if err := db.QueryRow(`SELECT value FROM meta WHERE key='delivery_backfill_v1'`).Scan(&restoredCheckpoint); err != nil || restoredCheckpoint != checkpoint {
		t.Fatal("backup did not restore the exact committed checkpoint")
	}
	status, err := restored.BackfillDeliveryStatus(context.Background(), auth.Caller{Kind: "operator"})
	if err != nil || status.HighSeq != first.HighSeq || status.LastSeq != first.LastSeq || status.TotalConverted != 1 {
		t.Fatal("restored progress differs before resumption")
	}
	if err := db.QueryRow(`SELECT body FROM event_message_snapshots WHERE snapshot_id=?`, snapshotID).Scan(&restoredSnapshot); err != nil || restoredSnapshot != snapshotBody {
		t.Fatal("backup did not restore snapshot bytes")
	}
	assertBackfillBytes(t, restored, fs)
	remaining := backfillCount(t, restored, `SELECT count(*) FROM events WHERE type='delivery' AND payload_format='inline' AND seq<=`+fmt.Sprint(first.HighSeq))
	converted := 0
	for range 100 {
		p := slice(t, restored, hub.BackfillLimits{Rows: 2})
		for _, seq := range p.Seqs {
			if seq <= first.LastSeq || seq > first.HighSeq {
				t.Fatal("resume processed a row outside the persisted checkpoint")
			}
		}
		converted += p.Converted
		if p.HighSeq != first.HighSeq || p.Failed != 0 || p.Skipped != 0 {
			t.Fatal("restore changed high-water or introduced exceptions")
		}
		if p.Done {
			break
		}
	}
	status, err = restored.BackfillDeliveryStatus(context.Background(), auth.Caller{Kind: "operator"})
	if err != nil || !status.Done || status.LastSeq != first.HighSeq || converted != remaining || status.TotalConverted != int64(remaining+1) {
		t.Fatal("resume did not convert exactly the remaining captured rows")
	}
	var postCapture string
	if err := db.QueryRow(`SELECT data FROM events WHERE seq=? AND payload_format='inline'`, len(fs)+1).Scan(&postCapture); err != nil || postCapture != fs[0].Data {
		t.Fatal("restore/resume changed post-capture row")
	}
	assertBackfillBytes(t, restored, fs)
}

func TestBackfillConcurrentTrafficBetweenUnfinishedSlices(t *testing.T) {
	env, fs := backfillEnv(t)
	h := env.Hub
	h.Cfg.DeliveryReferences = true
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	expected := make(map[string]envelope.Event)
	owners := []string{}
	seenOwner := make(map[string]bool)
	for i, f := range fs {
		cursor := fmt.Sprintf("e1.%d", i+1)
		expected[cursor] = envelope.Event{Cursor: cursor, Type: f.Type, At: f.At, Data: json.RawMessage(f.Data)}
		if !seenOwner[f.Owner] {
			owners = append(owners, f.Owner)
			seenOwner[f.Owner] = true
		}
	}
	type sentDelivery struct {
		seq  int64
		hash string
	}
	var sent []sentDelivery
	var worst time.Duration
	runSlice := func() hub.BackfillProgress {
		start := time.Now()
		p := slice(t, h, hub.BackfillLimits{Rows: 1})
		worst = max(worst, time.Since(start))
		return p
	}
	first := runSlice()
	p := first
	trafficRounds := 0
	for !p.Done && trafficRounds < 100 {
		// Acknowledge sends and a complete read before allowing the next
		// conversion slice. Scheduling cannot defer traffic until completion.
		type sendResult struct {
			response *hub.SendResponse
			err      error
		}
		sendDone := make(chan sendResult, 1)
		go func(round int) {
			response, _, err := h.Send(ctx, auth.Caller{Kind: "peer", PeerID: guardA, PrincipalID: guardPrn}, []byte(fmt.Sprintf(`{"idempotency_key":"backfill-concurrent-%d","recipients":["%s"],"priority":"hold","content":{"text":"synthetic concurrent send"}}`, round, guardB)), "http_send")
			sendDone <- sendResult{response, err}
		}(trafficRounds)
		result := <-sendDone
		if result.err != nil {
			t.Fatal(result.err)
		}
		if result.response == nil || len(result.response.Deliveries) != 1 {
			t.Fatal("send returned no delivery")
		}
		delivery := result.response.Deliveries[0]
		var seq int64
		var hash, at string
		var stored eventpayload.StoredPayload
		if err := h.DB.QueryRow(`SELECT e.seq,e.at,e.payload_format,e.data,coalesce(e.msg_id,''),coalesce(e.snapshot_id,''),d.body_sha256 FROM deliveries d JOIN events e ON e.seq=d.event_seq WHERE d.dly_id=?`, delivery.DeliveryID).Scan(&seq, &at, &stored.Format, &stored.Data, &stored.MsgID, &stored.SnapshotID, &hash); err != nil {
			t.Fatal(err)
		}
		if seq <= first.HighSeq || stored.Format != eventpayload.DeliveryRefV1 || stored.MsgID != result.response.MessageID || delivery.Recipient != guardB {
			t.Fatal("concurrent send did not create a reference above captured high-water")
		}
		body, err := eventpayload.HydrateBatch(ctx, h.DB, []eventpayload.StoredPayload{stored})
		if err != nil || fmt.Sprintf("%x", sha256.Sum256(body[0])) != hash {
			t.Fatal("concurrent send hash differs from exact delivery bytes")
		}
		cursor := fmt.Sprintf("e1.%d", seq)
		expected[cursor] = envelope.Event{Cursor: cursor, Type: "delivery", At: at, Data: body[0]}
		sent = append(sent, sentDelivery{seq, hash})
		type readResult struct {
			events []envelope.Event
			err    error
		}
		readDone := make(chan readResult, 1)
		go func() {
			events, err := h.Log.Read(ctx, owners, eventlog.Cursor{Epoch: 1}, 100)
			readDone <- readResult{events, err}
		}()
		page := <-readDone
		if page.err != nil {
			t.Fatal(page.err)
		}
		if len(page.events) != len(expected) {
			t.Fatalf("incomplete traffic read: events=%d expected=%d", len(page.events), len(expected))
		}
		var previous int64
		for _, event := range page.events {
			want, ok := expected[event.Cursor]
			cur, err := eventlog.ParseCursor(event.Cursor)
			if err != nil || cur.Seq <= previous || !ok || event.Type != want.Type || event.At != want.At || !bytes.Equal(event.Data, want.Data) {
				t.Fatal("traffic read changed sequence, metadata or exact bytes")
			}
			previous = cur.Seq
		}
		trafficRounds++
		p = runSlice()
		if p.HighSeq != first.HighSeq {
			t.Fatal("traffic extended captured high-water")
		}
	}
	if !p.Done || trafficRounds < 2 {
		t.Fatal("traffic did not interleave with unfinished slices")
	}
	assertBackfillBytes(t, h, fs)
	for i, f := range fs {
		if f.Type != "delivery" {
			continue
		}
		var hash string
		if err := h.DB.QueryRow(`SELECT body_sha256 FROM deliveries WHERE event_seq=?`, i+1).Scan(&hash); err != nil || hash != f.SHA256 {
			t.Fatalf("original delivery sequence/hash changed at seq=%d", i+1)
		}
	}
	for _, d := range sent {
		var hash, format string
		if err := h.DB.QueryRow(`SELECT d.body_sha256,e.payload_format FROM deliveries d JOIN events e ON e.seq=d.event_seq WHERE e.seq=?`, d.seq).Scan(&hash, &format); err != nil || hash != d.hash || format != eventpayload.DeliveryRefV1 {
			t.Fatalf("concurrent delivery sequence/hash changed at seq=%d", d.seq)
		}
	}
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
	t.Logf("traffic_rounds=%d worst_slice_duration=%s required_headroom_bytes=%d", trafficRounds, worst, p.RequiredHeadroomBytes)
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
