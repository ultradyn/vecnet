package hub_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/eventlog"
	"github.com/XertroV/vecnet/internal/hub"
	"github.com/XertroV/vecnet/internal/testhub"
)

// Onboarding's hub-authored setup must use the same immutable replay and
// retention dependencies as ordinary sends, including after a database reopen.
func TestGrokbotSetupReferenceRetention(t *testing.T) {
	for _, references := range []bool{false, true} {
		t.Run(fmt.Sprintf("references=%v", references), func(t *testing.T) {
			env := historyEnv(t, func(c *hub.Config) { c.DeliveryReferences = references })
			ctx := context.Background()
			// New setup work belongs to the finite policy cohort. A prospective
			// policy installed afterwards would correctly retain it forever.
			historyPolicy(t, env, 86400, false)
			res := mustOnboard(t, env, operatorCaller(t, env), grokReq("retained-setup", "Retained Bot", fakeRoutineURL(t), fakeRoutineKey(t), "", false))
			setup := res.Setup
			var seq int64
			var format, bodyHash string
			if err := env.Hub.DB.QueryRow(`SELECT e.seq,e.payload_format,d.body_sha256 FROM events e JOIN deliveries d ON d.event_seq=e.seq WHERE d.dly_id=?`, setup.DeliveryID).Scan(&seq, &format, &bodyHash); err != nil {
				t.Fatal(err)
			}
			if references == (format == "inline") {
				t.Fatal("setup event did not use configured payload storage")
			}
			cursor := eventlog.Cursor{Epoch: env.Hub.Log.Epoch(), Seq: seq}
			read := func(e *testhub.Env) []byte {
				t.Helper()
				floor, err := e.Hub.Log.OldestFor(ctx, []string{res.PeerID})
				if err != nil {
					t.Fatal(err)
				}
				events, err := e.Hub.Log.Read(ctx, []string{res.PeerID}, floor, 100)
				if err != nil {
					t.Fatal(err)
				}
				for _, ev := range events {
					if ev.Type != "delivery" {
						continue
					}
					var delivery envelope.Delivery
					if err := json.Unmarshal(ev.Data, &delivery); err != nil {
						t.Fatal(err)
					}
					if delivery.DeliveryID != setup.DeliveryID || delivery.Message.MessageID != setup.MessageID || delivery.Message.Sender != hub.HubSender || delivery.Ingress != "hub" {
						t.Fatal("hydrated setup delivery lost its hub message or identifiers")
					}
					if err := envelope.Validate("delivery", ev.Data); err != nil {
						t.Fatal(err)
					}
					sum := sha256.Sum256(ev.Data)
					if hex.EncodeToString(sum[:]) != bodyHash {
						t.Fatal("hydration changed frozen delivery bytes")
					}
					return ev.Data
				}
				t.Fatal("unacked setup delivery was pruned")
				return nil
			}
			before := read(env)
			env.Now.Advance(10 * 24 * time.Hour)
			retentionRun(t, env.Hub)
			historySweep(t, env)
			historySweep(t, env)
			if !bytes.Equal(before, read(env)) {
				t.Fatal("queued setup changed during retention")
			}
			// Settling push does not acknowledge inbox consumption. Once settled,
			// the unacked event must still pin the message body needed to hydrate it.
			historyExec(t, env, `UPDATE deliveries SET state='accepted',held=0,dispatching=0 WHERE dly_id=?`, setup.DeliveryID)
			restored := env.CloneDB(t)
			retentionRun(t, restored.Hub)
			historySweep(t, restored)
			historySweep(t, restored)
			if !bytes.Equal(before, read(restored)) {
				t.Fatal("unacked setup changed after restart and retention")
			}
			if countRows(t, restored, `SELECT count(*) FROM messages WHERE msg_id=?`, setup.MessageID) != 1 {
				t.Fatal("unacked setup lost its message body")
			}
			retentionAck(t, restored.Hub, res.PeerID, "*", cursor)
			retentionRun(t, restored.Hub)
			historySweep(t, restored)
			historySweep(t, restored)
			if countRows(t, restored, `SELECT count(*) FROM events WHERE seq=?`, seq) != 0 || countRows(t, restored, `SELECT count(*) FROM messages WHERE msg_id=?`, setup.MessageID) != 0 {
				t.Fatalf("settled acknowledged setup retained: events=%d messages=%d history_pins=%v", countRows(t, restored, `SELECT count(*) FROM events WHERE seq=?`, seq), countRows(t, restored, `SELECT count(*) FROM messages WHERE msg_id=?`, setup.MessageID), restored.Hub.HistoryRetentionStatus().Pins)
			}
		})
	}
}
