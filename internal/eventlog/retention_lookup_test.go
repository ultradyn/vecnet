package eventlog

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/store"
)

type retentionLookupRecorder struct {
	*store.DB
	queries []struct {
		sql  string
		args []any
	}
}

func (q *retentionLookupRecorder) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	q.queries = append(q.queries, struct {
		sql  string
		args []any
	}{query, args})
	return q.DB.QueryRowContext(ctx, query, args...)
}

func TestRetentionPopulatedLookupPlans(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`INSERT INTO principals(prn_id,name,created_at) VALUES('prn_target','synthetic','2026-11-01T00:00:00.000Z');
		INSERT INTO peers(peer_id,prn_id,name,kind,enrolled_at) VALUES('peer_target','prn_target','synthetic','session','2026-11-01T00:00:00.000Z');
		WITH RECURSIVE n(i) AS (VALUES(1) UNION ALL SELECT i+1 FROM n WHERE i<3)
		INSERT INTO events(owner,type,at,data) SELECT 'peer_target','presence','2026-11-01T00:00:00.000Z','{}' FROM n;
		INSERT INTO acks(owner,scope,seq,epoch,acked_at) VALUES
		('peer_target','*',1,1,'2026-11-01T00:00:00.000Z'),
		('prn_target','*',2,1,'2026-11-01T00:00:00.000Z'),
		('prn_target','peer_target',3,1,'2026-11-01T00:00:00.000Z');
		WITH RECURSIVE n(i) AS (VALUES(1) UNION ALL SELECT i+1 FROM n WHERE i<10000)
		INSERT INTO acks(owner,scope,seq,epoch,acked_at) SELECT 'prn_unrelated_'||i,'*',3,1,'2026-11-01T00:00:00.000Z' FROM n;
		INSERT INTO event_subscriptions(sub_id,principal_kind,principal_id,peer_id,name,arguments,url,secret_enc,cursor_epoch,cursor_seq,types,verified_at,created_at,updated_at)
		VALUES('sub_target','vecnet','prn_target','peer_target','vecnet.message','{}','https://synthetic.invalid/callback','synthetic',1,0,'[]','2026-11-01T00:00:00.000Z','2026-11-01T00:00:00.000Z','2026-11-01T00:00:00.000Z');
		WITH RECURSIVE n(i) AS (VALUES(1) UNION ALL SELECT i+1 FROM n WHERE i<10000)
		INSERT INTO event_outbox(sub_id,event_id,seq,body,created_at,done_at,terminal)
		SELECT 'sub_target','event_'||i,i+100,'{}','2026-11-01T00:00:00.000Z',
		CASE WHEN i%3!=0 THEN '2026-11-01T00:00:00.000Z' END,
		CASE i%3 WHEN 1 THEN 'exhausted' WHEN 2 THEN 'http_410' END FROM n`); err != nil {
		t.Fatal(err)
	}
	l, err := New(db)
	if err != nil {
		t.Fatal(err)
	}
	for _, analyze := range []bool{false, true} {
		if analyze {
			if _, err := db.Exec(`ANALYZE`); err != nil {
				t.Fatal(err)
			}
		}
		q := &retentionLookupRecorder{DB: db}
		ack, err := l.effectiveAck(context.Background(), q, "peer_target")
		if err != nil || ack != 3 {
			t.Fatalf("ack=%d err=%v", ack, err)
		}
		pinned, err := l.retentionPinned(context.Background(), q, "peer_target", 3, time.Date(2026, 11, 6, 0, 0, 0, 0, time.UTC))
		if err != nil || !pinned {
			t.Fatalf("lagging subscription without exact copy: pinned=%v err=%v", pinned, err)
		}
		ackSeeks, copySeeks, candidateSeeks, liveSeeks := 0, 0, 0, 0
		// Explain the statements the real helpers executed, rather than SQL
		// mirrored in a test that could drift away from production.
		for _, query := range q.queries {
			rows, err := db.Query("EXPLAIN QUERY PLAN "+query.sql, query.args...)
			if err != nil {
				t.Fatal(err)
			}
			for rows.Next() {
				var id, parent, unused int
				var detail string
				if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
					rows.Close()
					t.Fatal(err)
				}
				t.Logf("analyze=%v %s", analyze, detail)
				if strings.Contains(detail, "sqlite_autoindex_acks_1 (owner=? AND scope=?)") {
					ackSeeks++
				}
				if strings.Contains(detail, "event_outbox_retention_candidate (seq=?)") {
					candidateSeeks++
				}
				if strings.Contains(detail, "event_subscriptions_retention_identity (peer_id=? AND principal_kind=? AND principal_id=? AND <expr>>?)") {
					liveSeeks++
				}
				if strings.Contains(detail, "event_outbox_retention_seq (sub_id=? AND seq=?)") {
					copySeeks++
				}
			}
			err = rows.Err()
			rows.Close()
			if err != nil {
				t.Fatal(err)
			}
		}
		if ackSeeks != 3 || copySeeks != 1 || candidateSeeks != 1 || liveSeeks != 1 {
			t.Errorf("analyze=%v need three full ack PK seeks and candidate, live-range and full exact-copy seeks: ack=%d copy=%d candidate=%d live=%d", analyze, ackSeeks, copySeeks, candidateSeeks, liveSeeks)
		}
	}
}
