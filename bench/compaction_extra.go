//go:build linux

package main

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/XertroV/vecnet/internal/eventpayload"
	"github.com/XertroV/vecnet/internal/store"
)

func addCompactionPlans(queries map[string]struct {
	sql  string
	args []any
}, recipient, thread, at string) {
	visible := `d.recipient = ? AND d.state != 'superseded' AND NOT EXISTS (SELECT 1 FROM approvals a WHERE a.msg_id=d.msg_id AND a.closed_at IS NULL AND a.expires_at>?)`
	unread := ` AND d.read_at IS NULL AND NOT EXISTS (SELECT 1 FROM approvals a WHERE a.msg_id=d.msg_id AND a.decided_by=d.recipient)`
	queries["opview_inbox_full"] = struct {
		sql  string
		args []any
	}{`SELECT d.dly_id,d.msg_id,d.thr_id,coalesce(t.title,''),m.sender,m.kind,d.priority,d.state,m.created_at,coalesce(d.read_at,(SELECT a.closed_at FROM approvals a WHERE a.msg_id=d.msg_id AND a.decided_by=d.recipient)),m.body FROM deliveries d JOIN messages m ON m.msg_id=d.msg_id JOIN threads t ON t.thr_id=d.thr_id WHERE ` + visible + ` AND d.dly_id<? ORDER BY d.dly_id DESC LIMIT ?`, []any{recipient, at, "dly_zzzzzzzzzzzzzzzzzzzzzzzzzz", 51}}
	queries["opview_unread_full"] = struct {
		sql  string
		args []any
	}{`SELECT d.thr_id,count(*) FROM deliveries d WHERE ` + visible + unread + ` GROUP BY d.thr_id`, []any{recipient, at}}
	queries["history_full_forever_policy"] = struct {
		sql  string
		args []any
	}{`SELECT m.body FROM messages m WHERE m.thr_id=? AND m.seq>? AND m.kind!='reaction' AND 1 ORDER BY m.seq LIMIT ?`, []any{thread, 0, 101}}
	queries["history_locator"] = struct {
		sql  string
		args []any
	}{`SELECT seq FROM messages WHERE msg_id=?1 AND thr_id=?2 UNION ALL SELECT seq FROM message_history_locators WHERE msg_id=?1 AND thr_id=?2 LIMIT 1`, []any{"msg_synthetic", thread}}
	queries["purge_prefix_full"] = struct {
		sql  string
		args []any
	}{`SELECT seq,at,length(CAST(data AS BLOB)) FROM events WHERE owner=? ORDER BY seq LIMIT ?`, []any{recipient, 100}}
	queries["purge_pins_full"] = struct {
		sql  string
		args []any
	}{`WITH scopes(kind,id) AS (VALUES ('vecnet',?),('vecnet',(SELECT prn_id FROM peers WHERE peer_id=?)),('oauth',(SELECT o.prn_id FROM oauth_principals o JOIN peers p ON p.peer_id=o.peer_id AND p.prn_id=o.prn_id WHERE o.peer_id=?))) SELECT EXISTS(SELECT 1 FROM deliveries d WHERE d.event_seq=? AND (d.state IN ('queued','pushed','dead_letter') OR d.held=1 OR d.dispatching>0)) OR EXISTS(SELECT 1 FROM event_outbox o INDEXED BY event_outbox_retention_candidate WHERE o.seq=? AND (o.done_at IS NULL OR o.terminal='exhausted')) OR EXISTS(SELECT 1 FROM scopes CROSS JOIN event_subscriptions s INDEXED BY event_subscriptions_retention_identity WHERE s.peer_id=? AND s.principal_kind=scopes.kind AND s.principal_id=scopes.id AND s.verified_at IS NOT NULL AND s.paused_reason IS NULL AND min(coalesce(s.expires_at,'9999'),coalesce(s.token_expires_at,'9999'))>? AND (s.cursor_epoch!=? OR s.cursor_seq<?) AND NOT EXISTS(SELECT 1 FROM event_outbox o WHERE o.sub_id=s.sub_id AND o.seq=?))`, []any{recipient, recipient, recipient, 1, 1, recipient, at, 1, 1, 1}}
}

type snapshotCost struct {
	Fanout        int
	References    bool
	Messages      int
	Before, After compactionStage
}

// Snapshot cohorts deliberately differ only in legacy Message whitespace, which
// requires a deduplicated replay snapshot. The canonical body is still retained.
// No public serializer is changed to create these historical-storage fixtures.
func measureSnapshotCosts(n int) (out []snapshotCost, err error) {
	for _, f := range []int{1, 2, 5, 20} {
		for _, refs := range []bool{false, true} {
			dir, e := os.MkdirTemp("", "vecnet-snapshot-cost-")
			if e != nil {
				return nil, e
			}
			run := func() (r snapshotCost, err error) {
				r = snapshotCost{Fanout: f, References: refs, Messages: n}
				db, e := store.Open(filepath.Join(dir, "hub.db"))
				if e != nil {
					return r, e
				}
				defer db.Close()
				r.Before, e = compactionCapture(db, dir, "snapshot_empty")
				if e != nil {
					return r, e
				}
				e = db.Tx(context.Background(), func(tx *sql.Tx) error {
					for i := 0; i < n; i++ {
						id := fmt.Sprintf("msg_%026d", i)
						body := []byte(fmt.Sprintf(`{"schema_version":1,"message_id":%q,"thread_id":"thr_00000000000000000000000001","in_reply_to":null,"origin":null,"kind":"message","sender":"hub","recipients":["peer_00000000000000000000000001"],"priority":"hold","created_at":"2030-01-02T03:04:05.000Z","content":{"text":%q},"attachments":[],"metadata":{},"loop":{"depth":0,"max_depth":8,"expires_at":"2030-01-03T03:04:05.000Z","sender_chain":[]}}`, id, strings.Repeat("x", 2048)))
						if _, err := tx.Exec("INSERT INTO messages(msg_id,thr_id,seq,sender,kind,created_at,body) VALUES(?,'thr_00000000000000000000000001',?,'hub','message','2030-01-02T03:04:05.000Z',?)", id, i+1, string(body)); err != nil {
							return err
						}
						legacy := append([]byte("{\n"), body[1:]...)
						for j := 0; j < f; j++ {
							owner := fmt.Sprintf("peer_%026d", j)
							wire := []byte(fmt.Sprintf(`{"delivery_id":"dly_%026d","recipient":%q,"message":%s}`, i*f+j, owner, legacy))
							p := eventpayload.StoredPayload{Format: eventpayload.Inline, Data: wire}
							if refs {
								p, e = eventpayload.EncodeDelivery(context.Background(), tx, id, wire)
								if e != nil {
									return e
								}
								if p.SnapshotID == "" {
									return fmt.Errorf("legacy fixture failed to create snapshot")
								}
								hydrated, err := eventpayload.HydrateBatch(context.Background(), tx, []eventpayload.StoredPayload{p})
								if err != nil {
									return err
								}
								if !bytes.Equal(hydrated[0], wire) {
									return fmt.Errorf("legacy snapshot byte mismatch")
								}
							}
							var msg, snapshot any
							if p.MsgID != "" {
								msg = p.MsgID
							}
							if p.SnapshotID != "" {
								snapshot = p.SnapshotID
							}
							if _, err := tx.Exec("INSERT INTO events(owner,type,at,data,payload_format,msg_id,snapshot_id) VALUES(?,'delivery','2030-01-02T03:04:05.000Z',?,?,?,?)", owner, string(p.Data), p.Format, msg, snapshot); err != nil {
								return err
							}
						}
					}
					return nil
				})
				if e != nil {
					return r, e
				}
				r.After, e = compactionCapture(db, dir, "snapshot_populated")
				if e != nil {
					return r, e
				}
				if refs && r.After.Storage.Rows["event_message_snapshots"] != int64(n) {
					return r, fmt.Errorf("snapshot dedup count changed")
				}
				return r, nil
			}
			r, e := run()
			os.RemoveAll(dir)
			if e != nil {
				return nil, e
			}
			out = append(out, r)
		}
	}
	return out, nil
}
