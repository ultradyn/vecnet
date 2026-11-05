package events_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"testing"

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
	t.Run("reference_storage/TestReferenceGuardEventsCallbackAndOutbox", TestReferenceGuardEventsCallbackAndOutbox)
	t.Run("reference_storage/TestReferenceGuardExhaustedSuppression", TestReferenceGuardExhaustedSuppression)
	t.Run("reference_storage/TestReferenceGuardLegacyOutbox0018", TestReferenceGuardLegacyOutbox0018)
}

func assertPublicStorageKeys(t *testing.T, data []byte) {
	t.Helper()
	for _, key := range []string{"delivery_ref_v1", "wire_prefix", "wire_suffix", "snapshot_id", "payload_format"} {
		if strings.Contains(string(data), key) {
			t.Fatalf("storage key escaped public surface: key=%s", key)
		}
	}
}

func TestDeliveryReferenceFailureKeepsCursor(t *testing.T) {
	t.Run("reference_storage", func(t *testing.T) {
		env, _ := guardEnv(t)
		cb := testhub.NewCallback(t)
		sub := guardSubscription(t, env, cb)
		guardExec(t, env, `UPDATE events SET data=json_set(data,'$.body_sha256',?) WHERE seq=10`, strings.Repeat("0", 64))
		n, err := env.Events.Step(context.Background())
		var storage *eventpayload.StorageError
		if !errors.As(err, &storage) || n != 0 || len(cb.Posts()) != 0 {
			t.Fatal("hydration failure emitted a callback")
		}
		current := get(t, env, sub.ID)
		if current.Cursor != sub.Cursor {
			t.Fatal("hydration failure advanced subscription cursor")
		}
		var queued int
		if err := env.Hub.DB.QueryRow(`SELECT count(*) FROM event_outbox WHERE sub_id=?`, sub.ID).Scan(&queued); err != nil {
			t.Fatal(err)
		}
		if queued != 0 {
			t.Fatal("hydration failure queued a partial page")
		}
	})
}
