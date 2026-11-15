package hub

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/store"
)

func TestGrokQueryPlansManyDomains(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "domains.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	now := time.Date(2026, 11, 15, 0, 0, 0, 0, time.UTC)
	at := envelope.FormatTime(now)
	h := &Hub{DB: db, Now: func() time.Time { return now }}
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO principals VALUES ('prn_synthetic','domains',?)`, at)
	exec(`INSERT INTO routes(route_id,owner_kind,owner_id,kind,url,verified_at,created_at,grokbot_transport) VALUES ('route_domains','principal','prn_synthetic','grokbot','https://example.test/routine',?1,?1,'recipient_drain_v1')`, at)
	// Many independent recipient domains; no Go slice of domains/followers.
	exec(`WITH RECURSIVE n(i) AS (VALUES(1) UNION ALL SELECT i+1 FROM n WHERE i<2049)
 INSERT INTO peers(peer_id,prn_id,name,kind,enrolled_at,route_id) SELECT 'peer_'||i,'prn_synthetic','synthetic '||i,'session',?1,'route_domains' FROM n`, at)
	exec(`WITH RECURSIVE n(i) AS (VALUES(1) UNION ALL SELECT i+1 FROM n WHERE i<2049)
 INSERT INTO events(seq,owner,type,at,data) SELECT i,'peer_'||i,'delivery',?1,json_object('delivery_id','dly_'||i) FROM n`, at)
	exec(`WITH RECURSIVE n(i) AS (VALUES(1) UNION ALL SELECT i+1 FROM n WHERE i<2049)
 INSERT INTO deliveries(dly_id,msg_id,thr_id,recipient,priority,state,attempts_left,route_id,expires_at,created_at,updated_at,event_seq,body_sha256)
 SELECT 'dly_'||i,'msg_'||i,'thr_'||i,'peer_'||i,'wake','queued',8,'route_domains','2026-12-01T00:00:00.000Z',?1,?1,i,'inline-fixture' FROM n`, at)
	exec(`INSERT INTO grokbot_wakes(route_id,recipient,representative_id,phase,cover_until) SELECT route_id,recipient,'old_'||recipient,'covered','2026-11-15T00:10:00.000Z' FROM deliveries WHERE event_seq<=2048`)
	exec(`INSERT INTO grokbot_covers SELECT route_id,dly_id,recipient,? FROM deliveries WHERE event_seq<=2048`, at)
	// The cursor/domain checks explain production queries; the due-range check
	// proves the additive index can serve a next-attempt range without sorting.
	checks := []struct {
		query  string
		args   []any
		want   []string
		noSort bool
	}{
		{grokCandidateQuery, []any{at, 0, "", grokCandidatePage}, []string{"deliveries_grok_cursor", "sqlite_autoindex_grokbot_wakes", "sqlite_autoindex_grokbot_covers"}, true},
		{`SELECT count(*) FROM deliveries d INDEXED BY deliveries_grok_domain WHERE ` + grokPendingPredicate, []any{"route_domains", "peer_2049", at}, []string{"deliveries_grok_domain"}, false},
		{`SELECT dly_id FROM deliveries INDEXED BY deliveries_grok_due WHERE state IN ('queued','pushed') AND priority IN ('wake','wake_idle') AND held=0 AND next_attempt_at<=? ORDER BY next_attempt_at,event_seq LIMIT 16`, []any{at}, []string{"deliveries_grok_due"}, true},
	}
	for _, check := range checks {
		rows, err := db.Query("EXPLAIN QUERY PLAN "+check.query, check.args...)
		if err != nil {
			t.Fatal(err)
		}
		var plans []string
		for rows.Next() {
			var id, parent, unused int
			var detail string
			if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
				t.Fatal(err)
			}
			plans = append(plans, detail)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		plan := strings.Join(plans, "\n")
		t.Log(plan)
		for _, want := range check.want {
			if !strings.Contains(plan, want) {
				t.Fatalf("query lost %s: %s", want, plan)
			}
		}
		if check.noSort && strings.Contains(plan, "TEMP B-TREE") {
			t.Fatalf("unbounded sort: %s", plan)
		}
	}
	due, err := h.ClaimDue(ctx, now, 16)
	if err != nil || len(due) != 1 || due[0].Recipient != "peer_2049" {
		t.Fatalf("many gated domains starved last eligible: %+v %v", due, err)
	}
	// The production candidate iterator really returns a bounded page even with
	// all gates inactive; caller-owned keyset continuation supplies completeness.
	exec(`UPDATE grokbot_wakes SET lease_until='',retry_at='',cover_until=''`)
	rows, err := db.Query(grokCandidateQuery, at, 0, "", grokCandidatePage)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for rows.Next() {
		n++
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if n != grokCandidatePage {
		t.Fatalf("materialized page=%d", n)
	}
}

func TestGrokEmptyGateCleanup(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "cleanup.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	now := "2026-11-15T00:00:00.000Z"
	if _, err := db.Exec(`INSERT INTO grokbot_wakes(route_id,recipient,phase,retry_at,failure_ordinal) VALUES ('removed_route','peer_old','waiting','2026-11-15T00:01:00.000Z',4)`); err != nil {
		t.Fatal(err)
	}
	cleanup := func(at string, want int) {
		t.Helper()
		if err := db.Tx(ctx, func(tx *sql.Tx) error { return cleanupGrokbotWakes(ctx, tx, at) }); err != nil {
			t.Fatal(err)
		}
		var n int
		if err := db.QueryRow(`SELECT count(*) FROM grokbot_wakes`).Scan(&n); err != nil || n != want {
			t.Fatalf("cleanup rows=%d want=%d %v", n, want, err)
		}
	}
	cleanup(now, 1)
	cleanup("2026-11-15T00:01:00.001Z", 0)
	// A retained held wake keeps its inactive coordinator until the wake expires.
	_, err = db.Exec(`INSERT INTO deliveries(dly_id,msg_id,thr_id,recipient,priority,state,attempts_left,route_id,held,expires_at,created_at,updated_at,body_sha256) VALUES ('held','msg','thr','peer_old','wake_idle','queued',8,'removed_route',1,'2026-12-01T00:00:00.000Z',?1,?1,'synthetic')`, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO grokbot_wakes(route_id,recipient,phase) VALUES ('removed_route','peer_old','waiting')`); err != nil {
		t.Fatal(err)
	}
	cleanup("2026-11-15T00:01:00.001Z", 1)
	cleanup("2026-12-02T00:00:00.000Z", 0)
}

func TestGrokGateRollbackPreservesReservation(t *testing.T) {
	// The same transaction contains both budget reservation and arm transition.
	db, err := store.Open(filepath.Join(t.TempDir(), "rollback.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`INSERT INTO routes(route_id,owner_kind,owner_id,kind,url,created_at) VALUES ('route','peer','peer','grokbot','https://example.test','synthetic')`); err != nil {
		t.Fatal(err)
	}
	err = db.Tx(context.Background(), func(tx *sql.Tx) error {
		ok, _, err := takeGrokbotRun(context.Background(), tx, "route", "token", time.Now())
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("no reservation")
		}
		return fmt.Errorf("synthetic rollback")
	})
	if err == nil {
		t.Fatal("missing rollback")
	}
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM route_runs`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("rolled back reservation=%d %v", n, err)
	}
}

func TestGrokLegacyImportTransactionAndCleanup(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "legacy-import.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	at := "2026-11-15T00:00:00.000Z"
	deadline := "2026-11-15T00:10:00.000Z"
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatal(err)
		}
	}
	count := func(table string, want int) {
		t.Helper()
		var n int
		if err := db.QueryRow(`SELECT count(*) FROM ` + table).Scan(&n); err != nil || n != want {
			t.Fatalf("%s count=%d want=%d err=%v", table, n, want, err)
		}
	}
	exec(`INSERT INTO routes(route_id,owner_kind,owner_id,kind,url,created_at) VALUES ('import','peer','peer','grokbot','https://example.test',?)`, at)
	// Many legacy leaders must be captured set-wise; include a sibling domain
	// and an opted token that must retain its existing coordinator identity.
	exec(`WITH RECURSIVE n(i) AS (VALUES(1) UNION ALL SELECT i+1 FROM n WHERE i<2049)
 INSERT INTO deliveries(dly_id,msg_id,thr_id,recipient,priority,state,attempt,attempts_left,route_id,expires_at,next_attempt_at,created_at,updated_at,body_sha256)
 SELECT 'legacy_'||i,'msg_'||i,'thr_'||i,CASE WHEN i=2049 THEN 'sibling' ELSE 'peer' END,'wake','pushed',1,8,'import','2026-12-01T00:00:00.000Z',?2,?1,?1,'synthetic' FROM n`, at, deadline)
	exec(`INSERT INTO grokbot_wakes(route_id,recipient,representative_id,token,phase,lease_until,failure_ordinal,retry_at) VALUES ('import','sibling','legacy_2049','opted-token','armed',?1,4,?1)`, deadline)
	// Rollback must include both the capability and every captured identity.
	err = db.Tx(ctx, func(tx *sql.Tx) error {
		if err := setGrokbotTransport(ctx, tx, "import", GrokbotTransportRecipientDrainV1, time.Date(2026, 11, 15, 0, 0, 0, 0, time.UTC)); err != nil {
			return err
		}
		return fmt.Errorf("synthetic capability rollback")
	})
	if err == nil || !strings.Contains(err.Error(), "synthetic capability rollback") {
		t.Fatalf("rollback=%v", err)
	}
	count("grokbot_legacy_claims", 0)
	var transport string
	if err := db.QueryRow(`SELECT grokbot_transport FROM routes WHERE route_id='import'`).Scan(&transport); err != nil || transport != GrokbotTransportDeliveryV1 {
		t.Fatalf("rollback transport=%q %v", transport, err)
	}
	if err := db.Tx(ctx, func(tx *sql.Tx) error {
		return setGrokbotTransport(ctx, tx, "import", GrokbotTransportRecipientDrainV1, time.Date(2026, 11, 15, 0, 0, 0, 0, time.UTC))
	}); err != nil {
		t.Fatal(err)
	}
	count("grokbot_legacy_claims", 2048)
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM grokbot_wakes WHERE route_id='import' AND recipient='sibling' AND token='opted-token' AND phase='armed' AND lease_until=?1 AND retry_at=?1 AND failure_ordinal=4`, deadline).Scan(&n); err != nil || n != 1 {
		t.Fatalf("existing token changed=%d %v", n, err)
	}
	// Recipient identity and finite deadline survive physical history collection.
	exec(`DELETE FROM deliveries`)
	exec(`DELETE FROM routes`)
	cleanup := func(now string, want int) {
		t.Helper()
		if err := db.Tx(ctx, func(tx *sql.Tx) error { return cleanupGrokbotWakes(ctx, tx, now) }); err != nil {
			t.Fatal(err)
		}
		count("grokbot_legacy_claims", want)
	}
	cleanup("2026-11-15T00:09:59.999Z", 2048)
	count("grokbot_wakes", 2)
	// Both scoped identity and expiration cleanup use bounded indexed searches.
	for _, probe := range []struct{ q, index string }{
		{`EXPLAIN QUERY PLAN SELECT 1 FROM grokbot_legacy_claims WHERE route_id='import' AND recipient='peer' AND delivery_id='legacy_1' AND lease_until>'2026-11-15'`, "INDEX"},
		{`EXPLAIN QUERY PLAN DELETE FROM grokbot_legacy_claims WHERE lease_until<='2026-11-15'`, "grokbot_legacy_claims_expiry"},
	} {
		rows, err := db.Query(probe.q)
		if err != nil {
			t.Fatal(err)
		}
		var plan strings.Builder
		for rows.Next() {
			var id, parent, unused int
			var detail string
			if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
				t.Fatal(err)
			}
			plan.WriteString(detail)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		rows.Close()
		if !strings.Contains(plan.String(), probe.index) {
			t.Fatalf("missing index %q: %s", probe.index, plan.String())
		}
	}
	cleanup(deadline, 0)
	count("grokbot_wakes", 0)
}
