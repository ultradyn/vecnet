package hub_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/hub"
	"modernc.org/sqlite"
)

// boundedPageSlack is how many more pages a slice may fetch when the
// unrelated history around it grows 100x. Deeper B-trees cost a page or two
// per lookup; a scan of the unrelated rows costs thousands.
const boundedPageSlack = 64

// slicePageReads runs one production slice on a single pooled connection and
// returns the pages SQLite fetched on it (page-cache hits plus misses). This
// measures the work of the statements BackfillDeliverySlice actually executes,
// whatever SQL or planner choice they use, without a wall-clock threshold.
func slicePageReads(t *testing.T, h *hub.Hub, limits hub.BackfillLimits) (hub.BackfillProgress, int) {
	t.Helper()
	prev := h.DB.Stats().MaxOpenConnections
	h.DB.SetMaxOpenConns(1)
	defer h.DB.SetMaxOpenConns(prev)
	status := func(reset bool) int {
		t.Helper()
		conn, err := h.DB.Conn(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		total := 0
		if err := conn.Raw(func(raw any) error {
			st := raw.(sqlite.DBStatus)
			for _, op := range []sqlite.DBStatusOp{sqlite.DBStatusCacheHit, sqlite.DBStatusCacheMiss} {
				cur, _, err := st.Status(op, reset)
				if err != nil {
					return err
				}
				total += cur
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		return total
	}
	status(true)
	p := slice(t, h, limits)
	return p, status(false)
}

// addUnrelatedHistory appends n presence events (never backfill candidates)
// and n deliveries whose event_seq links no candidate.
func addUnrelatedHistory(t *testing.T, h *hub.Hub, tag string, n int) {
	t.Helper()
	ctx := context.Background()
	tx, err := h.DB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`WITH RECURSIVE n(i) AS (VALUES(1) UNION ALL SELECT i+1 FROM n WHERE i<?)
		INSERT INTO events(owner,type,at,data) SELECT ?,'presence',?,'{}' FROM n`, n, guardB, guardAt); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`WITH RECURSIVE n(i) AS (VALUES(1) UNION ALL SELECT i+1 FROM n WHERE i<?)
		INSERT INTO deliveries(dly_id,msg_id,thr_id,recipient,also_via,priority,state,attempts_left,expires_at,created_at,updated_at,event_seq,body_sha256)
		SELECT 'dly_gap_'||?||'_'||i,'msg_gap','thr_gap',?,'[]','wake','acked',0,?,?,?,NULL,'unrelated' FROM n`, n, tag, guardB, guardAt, guardAt, guardAt); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

// assertBackfillQueryPlans keeps the named/covering-index contract alongside
// the production page-work checks. These statements mirror the candidate,
// completion and authoritative-hash queries in BackfillDeliverySlice; no
// INDEXED BY hint may force the planner to select the required access path.
func assertBackfillQueryPlans(t *testing.T, h *hub.Hub, last, high int64) {
	t.Helper()
	for _, q := range []struct {
		name string
		sql  string
		args []any
		want string
	}{
		{"candidate", `SELECT e.seq,e.data FROM events e WHERE e.seq>? AND e.seq<=? AND e.type='delivery' AND e.payload_format='inline' ORDER BY e.seq LIMIT ?`, []any{0, high, 1}, "SEARCH e USING INDEX events_backfill_inline_delivery_seq (seq>? AND seq<?)"},
		{"completion", `SELECT EXISTS(SELECT 1 FROM events WHERE seq>? AND seq<=? AND type='delivery' AND payload_format='inline')`, []any{last, high}, "SEARCH events USING COVERING INDEX events_backfill_inline_delivery_seq (seq>? AND seq<?)"},
		{"delivery_hash", `SELECT body_sha256 FROM deliveries WHERE event_seq=?`, []any{last}, "SEARCH deliveries USING COVERING INDEX deliveries_event_seq_hash (event_seq=?)"},
	} {
		rows, err := h.DB.Query("EXPLAIN QUERY PLAN "+q.sql, q.args...)
		if err != nil {
			t.Fatal(err)
		}
		indexed := false
		for rows.Next() {
			var id, parent, unused int
			var detail string
			if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
				rows.Close()
				t.Fatal(err)
			}
			t.Logf("%s: %s", q.name, detail)
			indexed = indexed || strings.Contains(detail, q.want)
		}
		err = rows.Err()
		closeErr := rows.Close()
		if err != nil || closeErr != nil {
			t.Fatal("query plan failed", err, closeErr)
		}
		if !indexed {
			t.Errorf("%s query plan must contain %q", q.name, q.want)
		}
	}
}

// Both forward queries (candidate and completion) must range-search the
// partial eligibility index, and a candidate's authoritative hash lookup must
// use the delivery event_seq index: unrelated presence events, converted
// deliveries and other queue rows cannot enter the slice's work, however long
// the intervening range or trailing suffix becomes. The check is the slice's
// own page work at two history sizes, so it fails if production bypasses
// either index (for example with NOT INDEXED) even though results match.
func TestBackfillSparseAndEmptyEligibilityUsesBoundedScan(t *testing.T) {
	for _, sparse := range []bool{false, true} {
		t.Run(fmt.Sprintf("sparse_%t", sparse), func(t *testing.T) {
			reads := map[int]int{}
			for _, unrelated := range []int{1000, 100000} {
				env, fs := backfillEnv(t)
				h := env.Hub
				// Existing reference deliveries must also be excluded from the
				// forward scan, without changing their frozen replay bytes.
				finishBackfill(t, h)
				guardExec(t, env, `DELETE FROM meta WHERE key='delivery_backfill_v1'`)
				addUnrelatedHistory(t, h, "a", unrelated)
				var candidateSeq int64
				if sparse {
					guardExec(t, env, `INSERT INTO events(owner,type,at,data) VALUES (?,'delivery',?,?)`, guardB, guardAt, fs[0].Data)
					if err := h.DB.QueryRow(`SELECT max(seq) FROM events`).Scan(&candidateSeq); err != nil {
						t.Fatal(err)
					}
				}
				addUnrelatedHistory(t, h, "b", unrelated)
				var high int64
				if err := h.DB.QueryRow(`SELECT max(seq) FROM events`).Scan(&high); err != nil {
					t.Fatal(err)
				}
				assertBackfillQueryPlans(t, h, candidateSeq, high)
				start := time.Now()
				p, pages := slicePageReads(t, h, hub.BackfillLimits{Rows: 1})
				elapsed := time.Since(start)
				want := 0
				if sparse {
					want = 1
				}
				if !p.Done || p.HighSeq != high || p.LastSeq != high || p.Processed != want || p.Converted != want || p.Skipped != 0 || p.Failed != 0 {
					t.Fatalf("sparse/empty slice did not finish at captured high-water: %+v", p)
				}
				if sparse && (len(p.Seqs) != 1 || p.Seqs[0] != candidateSeq) {
					t.Fatal("sparse candidate sequence changed")
				}
				assertBackfillBytes(t, h, fs)
				reads[unrelated] = pages
				t.Logf("unrelated_events=%d unrelated_deliveries=%d converted=%d slice_pages=%d worst_slice_duration=%s required_headroom_bytes=%d", 2*unrelated, 2*unrelated, p.Converted, pages, elapsed, p.RequiredHeadroomBytes)
			}
			if reads[100000] > reads[1000]+boundedPageSlack {
				t.Fatalf("slice page work grew with unrelated history: %d pages at 2k unrelated rows, %d at 200k; the slice scans ineligible rows instead of range-searching its indexes", reads[1000], reads[100000])
			}
		})
	}
}

// The authoritative hash lookup for a candidate reads deliveries by
// event_seq through its index: a larger delivery queue adds no page work.
func TestBackfillDeliveryHashLookupUsesEventSeqIndex(t *testing.T) {
	reads := map[int]int{}
	for _, unrelated := range []int{0, 50000} {
		env, fs := backfillEnv(t)
		h := env.Hub
		if unrelated > 0 {
			// Deliveries only: the events table is unchanged between runs.
			ctx := context.Background()
			if _, err := h.DB.ExecContext(ctx, `WITH RECURSIVE n(i) AS (VALUES(1) UNION ALL SELECT i+1 FROM n WHERE i<?)
				INSERT INTO deliveries(dly_id,msg_id,thr_id,recipient,also_via,priority,state,attempts_left,expires_at,created_at,updated_at,event_seq,body_sha256)
				SELECT 'dly_gap_'||i,'msg_gap','thr_gap',?,'[]','wake','acked',0,?,?,?,NULL,'unrelated' FROM n`, unrelated, guardB, guardAt, guardAt, guardAt); err != nil {
				t.Fatal(err)
			}
		}
		assertBackfillQueryPlans(t, h, 1, 1)
		p, pages := slicePageReads(t, h, hub.BackfillLimits{Rows: 1})
		if p.Converted != 1 || p.LastSeq != 1 {
			t.Fatalf("hash-verified conversion failed: %+v", p)
		}
		assertBackfillBytes(t, h, fs)
		reads[unrelated] = pages
		t.Logf("unrelated_deliveries=%d slice_pages=%d", unrelated, pages)
	}
	if reads[50000] > reads[0]+boundedPageSlack {
		t.Fatalf("hash lookup page work grew with the delivery queue: %d pages with no unrelated deliveries, %d with 50k; it scans the queue instead of using the event_seq index", reads[0], reads[50000])
	}
}
