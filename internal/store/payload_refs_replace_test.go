package store_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/eventpayload"
	"github.com/XertroV/vecnet/internal/store"
)

// Exercise the real driver and default connection pragmas: REPLACE must be safe
// even when SQLite does not run the implicit deletion's DELETE triggers.
func TestPayloadReferenceReplacementGuards(t *testing.T) {
	for _, tc := range []struct {
		name, query string
		snapshot    bool
	}{
		{"message_primary", `INSERT OR REPLACE INTO messages(msg_id,thr_id,seq,sender,kind,created_at,body,job_id,approval_id) SELECT msg_id,thr_id,seq,sender,kind,created_at,'{}',job_id,approval_id FROM messages WHERE msg_id='msg_test'`, false},
		{"message_alternate", `INSERT OR REPLACE INTO messages(msg_id,thr_id,seq,sender,kind,created_at,body,job_id,approval_id) SELECT 'msg_other',thr_id,seq,sender,kind,created_at,body,job_id,approval_id FROM messages WHERE msg_id='msg_test'`, false},
		{"message_both_unique_keys", `INSERT OR REPLACE INTO messages(msg_id,thr_id,seq,sender,kind,created_at,body,job_id,approval_id) SELECT 'msg_other',thr_id,seq,sender,kind,created_at,'{}',job_id,approval_id FROM messages WHERE msg_id='msg_test'`, false},
		{"message_update_primary", `UPDATE OR REPLACE messages SET msg_id='msg_test' WHERE msg_id='msg_other'`, false},
		{"message_update_alternate", `UPDATE OR REPLACE messages SET seq=1 WHERE msg_id='msg_other'`, false},
		{"snapshot_primary_body", `INSERT OR REPLACE INTO event_message_snapshots SELECT snapshot_id,msg_id,body_sha256,'{}' FROM event_message_snapshots`, true},
		{"snapshot_primary_message", `INSERT OR REPLACE INTO event_message_snapshots SELECT snapshot_id,'msg_other',body_sha256,body FROM event_message_snapshots`, true},
		{"snapshot_primary_hash", `INSERT OR REPLACE INTO event_message_snapshots SELECT snapshot_id,msg_id,'other_hash',body FROM event_message_snapshots`, true},
		{"snapshot_alternate_identity", `INSERT OR REPLACE INTO event_message_snapshots SELECT 'snap_other',msg_id,body_sha256,body FROM event_message_snapshots`, true},
		{"snapshot_alternate_body", `INSERT OR REPLACE INTO event_message_snapshots SELECT 'snap_other',msg_id,body_sha256,'{}' FROM event_message_snapshots`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, err := store.Open(filepath.Join(t.TempDir(), "replace.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			ctx := context.Background()
			msgID := "msg_test"
			body := `{"message_id":"msg_test","attachments":[],"text":"synthetic-original"}`
			if tc.snapshot {
				msgID = "msg_00000000000000000000000001"
				body = `{"schema_version":1,"message_id":"msg_00000000000000000000000001","thread_id":"thr_00000000000000000000000001","in_reply_to":null,"origin":null,"kind":"message","sender":"hub","recipients":["peer_00000000000000000000000001"],"priority":"hold","created_at":"2026-11-05T00:00:00.000Z","content":{"text":"synthetic-original"},"attachments":[],"metadata":{},"loop":{"depth":0,"max_depth":8,"expires_at":"2026-11-06T00:00:00.000Z","sender_chain":[]}}`
				if envelope.Validate("message", []byte(body)) != nil {
					t.Fatal("snapshot fixture must satisfy Message schema")
				}
			}
			if !tc.snapshot {
				if _, err := db.Exec(`INSERT INTO messages(msg_id,thr_id,seq,sender,kind,created_at,body) VALUES ('msg_test','thr_test',1,'hub','message','now',?)`, body); err != nil {
					t.Fatal(err)
				}
				// A second conflict on the primary key must not hide the pinned
				// row encountered through the alternate (thread, sequence) key.
				if _, err := db.Exec(`INSERT INTO messages(msg_id,thr_id,seq,sender,kind,created_at,body) VALUES ('msg_other','thr_test',2,'hub','message','now','{}')`); err != nil {
					t.Fatal(err)
				}
			}
			wire := `{"message":` + body + `}`
			var p eventpayload.StoredPayload
			err = db.Tx(ctx, func(tx *sql.Tx) error {
				var err error
				p, err = eventpayload.EncodeDelivery(ctx, tx, msgID, []byte(wire))
				if err != nil {
					return err
				}
				_, err = tx.ExecContext(ctx, `INSERT INTO events(owner,type,at,data,payload_format,msg_id,snapshot_id) VALUES ('peer_test','delivery','now',?,?,?,?)`, string(p.Data), p.Format, p.MsgID, nullableSnapshot(p.SnapshotID))
				return err
			})
			if err != nil {
				t.Fatal(err)
			}
			if (p.SnapshotID != "") != tc.snapshot {
				t.Fatal("wrong fixture reference kind")
			}
			if _, err := db.Exec(tc.query); err == nil {
				t.Error("conflicting replacement accepted")
			}
			err = db.ReadSnapshot(ctx, func(q *sql.Conn) error {
				got, err := eventpayload.HydrateBatch(ctx, q, []eventpayload.StoredPayload{p})
				if err == nil && (len(got) != 1 || string(got[0]) != wire) {
					t.Error("replacement changed replay bytes")
				}
				return err
			})
			if err != nil {
				t.Fatal("original replay no longer hydratable", err)
			}
		})
	}
}

func nullableSnapshot(id string) any {
	if id == "" {
		return nil
	}
	return id
}

func TestMessageReplacementGuardCutoverAndIdenticalBytes(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "before_cutover", true: "after_cutover"}[enabled], func(t *testing.T) {
			db, err := store.Open(filepath.Join(t.TempDir(), "cutover.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			if _, err := db.Exec(`INSERT INTO messages(msg_id,thr_id,seq,sender,kind,created_at,body) VALUES ('msg_test','thr_test',1,'hub','message','now','{}')`); err != nil {
				t.Fatal(err)
			}
			if enabled {
				if err := db.SetMeta(context.Background(), "event_payload_refs_enabled", "1"); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := db.Exec(`INSERT OR REPLACE INTO messages SELECT * FROM messages`); err != nil {
				t.Fatal("identical replacement rejected", err)
			}
			_, err = db.Exec(`INSERT OR REPLACE INTO messages(msg_id,thr_id,seq,sender,kind,created_at,body,job_id,approval_id) SELECT msg_id,thr_id,seq,sender,kind,created_at,' { } ',job_id,approval_id FROM messages`)
			if (err != nil) != enabled {
				t.Fatal("replacement guard did not follow cutover flag")
			}
		})
	}
}
