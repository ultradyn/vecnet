package events

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/XertroV/vecnet/internal/eventlog"
	"github.com/XertroV/vecnet/internal/store"
)

type replayLookupRecorder struct {
	*sql.Tx
	query string
	args  []any
}

func (q *replayLookupRecorder) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	q.query, q.args = query, args
	return q.Tx.QueryRowContext(ctx, query, args...)
}

func TestReplayAvailabilityLookupBounded(t *testing.T) {
	for _, pending := range []int{100, 10000} {
		t.Run(fmt.Sprint(pending), func(t *testing.T) {
			ctx := context.Background()
			db, err := store.Open(filepath.Join(t.TempDir(), "hub.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			if _, err := db.Exec(`INSERT INTO principals(prn_id,name,created_at)
 VALUES('prn_replay','synthetic','2026-11-01T00:00:00.000Z');
 INSERT INTO peers(peer_id,prn_id,name,kind,enrolled_at)
 VALUES('peer_replay','prn_replay','synthetic','session','2026-11-01T00:00:00.000Z');
 INSERT INTO event_subscriptions(sub_id,principal_kind,principal_id,peer_id,name,arguments,url,secret_enc,cursor_epoch,cursor_seq,types,created_at,updated_at)
 VALUES('sub_replay','vecnet','prn_replay','peer_replay','vecnet.message','{}','https://synthetic.invalid/callback','synthetic',1,0,'[]','2026-11-01T00:00:00.000Z','2026-11-01T00:00:00.000Z');
 WITH RECURSIVE n(i) AS (VALUES(1) UNION ALL SELECT i+1 FROM n WHERE i<?)
 INSERT INTO event_outbox(sub_id,event_id,seq,body,created_at)
 SELECT 'sub_replay','pending_'||i,i,'{}','2026-11-01T00:00:00.000Z' FROM n`, pending); err != nil {
				t.Fatal(err)
			}
			st := &Store{DB: db}
			cur := eventlog.Cursor{Epoch: 1}
			for _, analyze := range []bool{false, true} {
				t.Run(fmt.Sprintf("analyze=%v", analyze), func(t *testing.T) {
					if analyze {
						if _, err := db.Exec(`ANALYZE`); err != nil {
							t.Fatal(err)
						}
					}
					// Execute the real guard under the same writer transaction as
					// SetCursor. Explain its captured SQL, so the test cannot drift
					// to a different predicate from the production lookup.
					if err := db.Tx(ctx, func(tx *sql.Tx) error {
						q := &replayLookupRecorder{Tx: tx}
						if err := checkReplayAvailable(ctx, q, "sub_replay"); err != nil {
							return err
						}
						rows, err := tx.QueryContext(ctx, "EXPLAIN QUERY PLAN "+q.query, q.args...)
						if err != nil {
							return err
						}
						defer rows.Close()
						indexed := false
						for rows.Next() {
							var id, parent, unused int
							var detail string
							if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
								return err
							}
							t.Log(detail)
							if strings.Contains(detail, "SCAN event_outbox") {
								t.Errorf("replay lookup scans callback queue: %s", detail)
							}
							if strings.Contains(detail, "SEARCH event_outbox ") && strings.Contains(detail, "INDEX event_outbox_replay_unavailable (sub_id=?)") {
								indexed = true
							}
						}
						if !indexed {
							t.Error("replay lookup must seek event_outbox_replay_unavailable by sub_id")
						}
						return rows.Err()
					}); err != nil {
						t.Fatal(err)
					}
					if err := st.SetCursor(ctx, "sub_replay", cur, false); err != nil {
						t.Fatal(err)
					}
					if got, err := st.ReplayCursor(ctx, "sub_replay", cur); err != nil || got != cur {
						t.Fatalf("pending replay cursor=%v err=%v", got, err)
					}
					// Add the single unavailable copy only after the negative
					// lookup: unrelated queue growth must not mask a full scan.
					if _, err := db.Exec(`INSERT INTO event_outbox(sub_id,event_id,seq,body,created_at,done_at,terminal)
 VALUES('sub_replay','erased',?,'','2026-11-01T00:00:00.000Z','2026-11-01T00:00:00.000Z','exhausted')`, pending+1); err != nil {
						t.Fatal(err)
					}
					if err := st.SetCursor(ctx, "sub_replay", cur, false); !errors.Is(err, ErrReplayUnavailable) {
						t.Fatalf("erased exhausted SetCursor error=%v", err)
					}
					if _, err := st.ReplayCursor(ctx, "sub_replay", cur); !errors.Is(err, ErrReplayUnavailable) {
						t.Fatalf("erased exhausted ReplayCursor error=%v", err)
					}
					var count int
					if err := db.QueryRow(`SELECT count(*) FROM event_outbox WHERE sub_id='sub_replay' AND event_id='erased' AND body='' AND terminal='exhausted' AND done_at IS NOT NULL`).Scan(&count); err != nil || count != 1 {
						t.Fatalf("refusal lost exhausted copy: count=%d err=%v", count, err)
					}
					if _, err := db.Exec(`DELETE FROM event_outbox WHERE sub_id='sub_replay' AND event_id='erased'`); err != nil {
						t.Fatal(err)
					}
				})
			}
		})
	}
}
