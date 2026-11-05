package hub_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/auth"
	"github.com/XertroV/vecnet/internal/eventlog"
	"github.com/XertroV/vecnet/internal/eventpayload"
	"github.com/XertroV/vecnet/internal/hub"
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
	t.Run("reference_storage/TestReferenceGuardClaimDueFrozenRetry", TestReferenceGuardClaimDueFrozenRetry)
	t.Run("reference_storage/TestReferenceGuardWebhookAttempts", TestReferenceGuardWebhookAttempts)
	t.Run("reference_storage/TestReferenceGuardOperatorNotifications", TestReferenceGuardOperatorNotifications)
	t.Run("reference_storage/TestReferenceGuardFanOutSendBytes", TestReferenceGuardFanOutSendBytes)
}

func assertReferenceWrites(t *testing.T, env *testhub.Env, msgID string) {
	t.Helper()
	if !strings.Contains(t.Name(), "reference_storage") {
		return
	}
	var total, refs int
	if err := env.Hub.DB.QueryRow(`SELECT count(*),count(CASE WHEN payload_format='delivery_ref_v1' THEN 1 END) FROM events WHERE type='delivery' AND json_extract(data,'$.msg_id')=?`, msgID).Scan(&total, &refs); err != nil {
		t.Fatal("reference write metadata query failed")
	}
	if total == 0 || total != refs {
		t.Fatalf("reference writes missing: refs=%d total=%d", refs, total)
	}
}

func TestDeliveryReferenceSendRollback(t *testing.T) {
	for _, references := range []bool{false, true} {
		t.Run(fmt.Sprint(references), func(t *testing.T) {
			env, _ := guardEnv(t)
			env.Hub.Cfg.DeliveryReferences = references
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			tables := []string{"messages", "events", "deliveries", "threads", "thread_participants", "event_message_snapshots", "idempotency", "audit"}
			before := map[string]int{}
			for _, table := range tables {
				var beforeCount int
				if err := env.Hub.DB.QueryRow(`SELECT count(*) FROM ` + table).Scan(&beforeCount); err != nil {
					t.Fatal(err)
				}
				before[table] = beforeCount
			}
			// The second recipient aborts after the first event/delivery was inserted.
			guardExec(t, env, `CREATE TRIGGER reject_second_recipient BEFORE INSERT ON deliveries WHEN NEW.recipient='peer_00000000000000000000000003' BEGIN SELECT RAISE(ABORT,'synthetic_fanout_failure'); END`)
			wake := env.Hub.Log.Wait(ctx, []string{guardB, guardC})
			caller := auth.Caller{Kind: "peer", PeerID: guardA, PrincipalID: guardPrn}
			raw := []byte(`{"idempotency_key":"rollback-guard","recipients":["peer_00000000000000000000000002","peer_00000000000000000000000003"],"priority":"wake","content":{"text":"synthetic rollback"}}`)
			if _, _, err := env.Hub.Send(ctx, caller, raw, "http_send"); err == nil {
				t.Fatal("send did not roll back")
			}
			for _, table := range tables {
				var n int
				if err := env.Hub.DB.QueryRow(`SELECT count(*) FROM ` + table).Scan(&n); err != nil {
					t.Fatal(err)
				}
				if n != before[table] {
					t.Fatalf("partial transaction: table=%s before=%d after=%d", table, before[table], n)
				}
			}
			select {
			case <-wake:
				t.Fatal("rolled back send notified readers")
			default:
			}
			var marker int
			if err := env.Hub.DB.QueryRow(`SELECT count(*) FROM meta WHERE key='event_payload_refs_enabled'`).Scan(&marker); err != nil {
				t.Fatal(err)
			}
			if marker != 0 {
				t.Fatal("rolled back send enabled references")
			}
		})
	}
}

func TestDeliveryReferenceClaimFailureIsAtomic(t *testing.T) {
	for _, badHash := range []string{strings.Repeat("0", 64), ""} {
		t.Run(fmt.Sprintf("reference_storage/hash_bytes_%d", len(badHash)), func(t *testing.T) {
			env, fs := guardEnv(t)
			guardExec(t, env, `INSERT INTO routes(route_id,owner_kind,owner_id,kind,url,verified_at,created_at) VALUES ('guard-route','peer',?,'http','https://push.example/guard',?,?)`, guardB, guardAt, guardAt)
			guardExec(t, env, `UPDATE deliveries SET route_id='guard-route'`)
			guardExec(t, env, `UPDATE deliveries SET body_sha256=? WHERE event_seq=10`, badHash)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			env.Hub.DB.SetMaxOpenConns(1) // hydration must use the claim's transaction
			due, err := env.Hub.ClaimDue(ctx, env.Now.Now(), 100)
			var storage *eventpayload.StorageError
			if !errors.As(err, &storage) || len(due) != 0 {
				t.Fatal("corrupt batch returned a partial claim")
			}
			var attempts int
			if err := env.Hub.DB.QueryRow(`SELECT sum(attempt) FROM deliveries`).Scan(&attempts); err != nil {
				t.Fatal(err)
			}
			if attempts != 0 {
				t.Fatal("failed hydration advanced attempts")
			}
			guardExec(t, env, `UPDATE deliveries SET body_sha256=? WHERE event_seq=10`, fs[9].SHA256)
			due, err = env.Hub.ClaimDue(ctx, env.Now.Now(), 100)
			if err != nil || len(due) != 10 {
				t.Fatal("claim failed to recover using one connection")
			}
			for i, d := range due {
				if d.Attempt != 1 || !bytes.Equal(d.Body, []byte(fs[i].Data)) {
					t.Fatalf("claim recovery bytes changed: index=%d", i)
				}
			}
		})
	}
}

func TestDeliveryReferenceNotificationFailure(t *testing.T) {
	for _, badHash := range []string{strings.Repeat("0", 64), ""} {
		t.Run(fmt.Sprintf("reference_storage/hash_bytes_%d", len(badHash)), func(t *testing.T) {
			env, _ := guardEnv(t)
			guardExec(t, env, `UPDATE peers SET kind='operator' WHERE peer_id=?`, guardB)
			rcv := newNotifyReceiver(t)
			guardPutNotify(t, env, rcv.srv.URL)
			guardExec(t, env, `UPDATE deliveries SET body_sha256=? WHERE event_seq=1`, badHash)
			env.Hub.NotifyOperatorReferenceGuard(context.Background(), "msg_00000000000000000000000001")
			env.Hub.WaitNotifies()
			if len(rcv.Hits()) != 0 {
				t.Fatal("corrupt notification escaped")
			}
		})
	}
}

func TestDeliveryReferenceDefaultAndCutover(t *testing.T) {
	if hub.DefaultConfig().DeliveryReferences {
		t.Fatal("reference writes default on")
	}
	env, _ := guardEnv(t)
	caller := auth.Caller{Kind: "peer", PeerID: guardA, PrincipalID: guardPrn}
	raw := []byte(`{"idempotency_key":"cutover-guard","recipients":["peer_00000000000000000000000002","peer_00000000000000000000000003"],"priority":"wake","content":{"text":"synthetic cutover"}}`)
	env.Hub.Cfg.DeliveryReferences = true
	sent, _, err := env.Hub.Send(context.Background(), caller, raw, "http_send")
	if err != nil {
		t.Fatal("send failed")
	}
	var total, refs int
	if err := env.Hub.DB.QueryRow(`SELECT count(*),count(CASE WHEN payload_format='delivery_ref_v1' THEN 1 END) FROM events WHERE msg_id=?`, sent.MessageID).Scan(&total, &refs); err != nil {
		t.Fatal(err)
	}
	if total != 2 || refs != 2 {
		t.Fatalf("fanout not references: total=%d refs=%d", total, refs)
	}
	if _, err := env.Hub.DB.Exec(`UPDATE messages SET body=json_set(body,'$.content.text','changed') WHERE msg_id=?`, sent.MessageID); err == nil {
		t.Fatal("reference Message was mutable")
	}
	env.Hub.Cfg.DeliveryReferences = false
	// Dual reading and the cutover immutability guard survive disabling writes.
	evs, err := env.Hub.Log.Read(context.Background(), []string{guardB}, eventlog.Cursor{Epoch: 1, Seq: 12}, 100)
	if err != nil || len(evs) == 0 {
		t.Fatal("dual reading requires write switch")
	}
	for _, ev := range evs {
		if bytes.Contains(ev.Data, []byte(`"wire_prefix"`)) {
			t.Fatal("internal reference escaped")
		}
	}
	if _, err := env.Hub.DB.Exec(`UPDATE messages SET body=json_set(body,'$.content.text','changed') WHERE msg_id='msg_00000000000000000000000001'`); err == nil {
		t.Fatal("cutover did not freeze inline Messages")
	}
}

func assertPublicStorageKeys(t *testing.T, data []byte) {
	t.Helper()
	for _, key := range []string{"delivery_ref_v1", "wire_prefix", "wire_suffix", "snapshot_id", "payload_format"} {
		if strings.Contains(string(data), key) {
			t.Fatalf("storage key escaped public surface: key=%s", key)
		}
	}
}
