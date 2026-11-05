package eventlog_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/eventlog"
	"github.com/XertroV/vecnet/internal/eventpayload"
	"github.com/XertroV/vecnet/internal/testhub"
)

// Reuse the frozen Task 1 assertions in a second storage mode. Only storage
// setup changes; fixtures, public renderers and expected bytes stay untouched.
func prepareReferenceStorage(t *testing.T, env *testhub.Env) {
	t.Helper()
	// Simulate 0018 before converting its historical event bytes.
	if strings.Contains(t.Name(), "Legacy") {
		return
	}
	hydrateReferenceStorage(t, env)
}

func hydrateReferenceStorage(t *testing.T, env *testhub.Env) {
	t.Helper()
	if !strings.Contains(t.Name(), "reference_storage") {
		return
	}
	env.Hub.Cfg.DeliveryReferences = true
	ctx := context.Background()
	err := env.Hub.DB.Tx(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT seq,data FROM events WHERE type='delivery' AND payload_format='inline' ORDER BY seq`)
		if err != nil {
			return err
		}
		type item struct {
			seq  int64
			data []byte
		}
		var items []item
		for rows.Next() {
			var r item
			if err := rows.Scan(&r.seq, &r.data); err != nil {
				rows.Close()
				return err
			}
			items = append(items, r)
		}
		if err := rows.Close(); err != nil {
			return err
		}
		if err := rows.Err(); err != nil {
			return err
		}
		for _, r := range items {
			var wire struct {
				Message struct {
					ID string `json:"message_id"`
				} `json:"message"`
			}
			if err := json.Unmarshal(r.data, &wire); err != nil {
				return err
			}
			p, err := eventpayload.EncodeDelivery(ctx, tx, wire.Message.ID, r.data)
			if err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `UPDATE events SET data=?,payload_format=?,msg_id=?,snapshot_id=NULLIF(?,'') WHERE seq=?`, string(p.Data), p.Format, p.MsgID, p.SnapshotID, r.seq); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal("reference fixture setup failed")
	}
}

func TestDeliveryReferenceGuards(t *testing.T) {
	t.Run("reference_storage/TestReferenceGuardLogReadBytes", TestReferenceGuardLogReadBytes)
	t.Run("reference_storage/TestReferenceGuardCursorOrdering", TestReferenceGuardCursorOrdering)
	t.Run("reference_storage/TestReferenceGuardLegacy0018", TestReferenceGuardLegacy0018)
}

func TestDeliveryReferenceReadFailureIsAtomic(t *testing.T) {
	t.Run("reference_storage", func(t *testing.T) {
		env, _ := guardEnv(t)
		guardExec(t, env, `UPDATE events SET data=json_set(data,'$.body_sha256',?) WHERE seq=10`, strings.Repeat("0", 64))
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		env.Hub.DB.SetMaxOpenConns(1)
		evs, err := env.Hub.Log.Read(ctx, []string{guardB}, eventlog.Cursor{Epoch: 1}, 100)
		var storage *eventpayload.StorageError
		if !errors.As(err, &storage) || len(evs) != 0 {
			t.Fatal("corrupt read returned a partial page")
		}
	})
}

func assertPublicStorageKeys(t *testing.T, data []byte) {
	t.Helper()
	for _, key := range []string{"delivery_ref_v1", "wire_prefix", "wire_suffix", "snapshot_id", "payload_format"} {
		if strings.Contains(string(data), key) {
			t.Fatalf("storage key escaped public surface: key=%s", key)
		}
	}
}

func TestDeliveryReferenceEmptyDeliveryHash(t *testing.T) {
	t.Run("reference_storage", func(t *testing.T) {
		env, _ := guardEnv(t)
		guardExec(t, env, `UPDATE deliveries SET body_sha256='' WHERE event_seq=10`)
		evs, err := env.Hub.Log.Read(context.Background(), []string{guardB}, eventlog.Cursor{Epoch: 1}, 100)
		var storage *eventpayload.StorageError
		if !errors.As(err, &storage) || len(evs) != 0 {
			t.Fatal("empty stored delivery hash bypassed validation")
		}
	})
}

func TestDeliveryReferenceDualReaderOneConnection(t *testing.T) {
	t.Run("reference_storage", func(t *testing.T) {
		env, fs := guardEnv(t)
		env.Hub.DB.SetMaxOpenConns(1)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		for _, enabled := range []bool{false, true} {
			env.Hub.Cfg.DeliveryReferences = enabled
			evs, err := env.Hub.Log.Read(ctx, []string{guardB, guardC}, eventlog.Cursor{Epoch: 1}, 100)
			if err != nil || len(evs) != len(fs) {
				t.Fatal("mixed dual reader failed with one connection")
			}
			for i, ev := range evs {
				if string(ev.Data) != fs[i].Data {
					t.Fatalf("dual reader changed bytes: seq=%d", i+1)
				}
			}
		}
	})
}
