package hub_test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/hub"
)

// A wall-clock deadline would make this regression load-dependent. Instead,
// require both production queries to range-search the partial eligibility index:
// unrelated presence and already-converted delivery rows cannot enter that scan,
// regardless of how long the intervening range or trailing suffix becomes.
func TestBackfillSparseAndEmptyEligibilityUsesBoundedScan(t *testing.T) {
	for _, unrelated := range []int{1000, 100000} {
		for _, sparse := range []bool{false, true} {
			t.Run(fmt.Sprintf("unrelated_%d/sparse_%t", unrelated, sparse), func(t *testing.T) {
				env, fs := backfillEnv(t)
				h := env.Hub
				// Existing reference deliveries must also be excluded from the
				// forward scan, without changing their frozen replay bytes.
				finishBackfill(t, h)
				guardExec(t, env, `DELETE FROM meta WHERE key='delivery_backfill_v1'`)
				addGap := func() {
					guardExec(t, env, `WITH RECURSIVE n(i) AS (VALUES(1) UNION ALL SELECT i+1 FROM n WHERE i<?)
						INSERT INTO events(owner,type,at,data) SELECT ?,'presence',?,'{}' FROM n`, unrelated, guardB, guardAt)
				}
				addGap()
				var candidateSeq int64
				if sparse {
					guardExec(t, env, `INSERT INTO events(owner,type,at,data) VALUES (?,'delivery',?,?)`, guardB, guardAt, fs[0].Data)
					if err := h.DB.QueryRow(`SELECT max(seq) FROM events`).Scan(&candidateSeq); err != nil {
						t.Fatal(err)
					}
				}
				addGap()
				var high int64
				if err := h.DB.QueryRow(`SELECT max(seq) FROM events`).Scan(&high); err != nil {
					t.Fatal(err)
				}
				// These are the forward candidate and completion queries from
				// BackfillDeliverySlice, without INDEXED BY or planner hints.
				for _, q := range []struct {
					name  string
					sql   string
					args  []any
					alias string
				}{
					{"candidate", `SELECT e.seq,e.data FROM events e WHERE e.seq>? AND e.seq<=? AND e.type='delivery' AND e.payload_format='inline' ORDER BY e.seq LIMIT ?`, []any{0, high, 1}, "e"},
					{"completion", `SELECT EXISTS(SELECT 1 FROM events WHERE seq>? AND seq<=? AND type='delivery' AND payload_format='inline')`, []any{candidateSeq, high}, "events"},
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
						indexed = indexed || (strings.Contains(detail, "SEARCH "+q.alias+" USING ") && strings.Contains(detail, "INDEX events_backfill_inline_delivery_seq (seq>? AND seq<?)"))
					}
					err = rows.Err()
					closeErr := rows.Close()
					if err != nil || closeErr != nil {
						t.Fatal("query plan failed", err, closeErr)
					}
					if !indexed {
						t.Errorf("%s scans ineligible events instead of range-searching the partial eligibility index", q.name)
					}
				}
				start := time.Now()
				p := slice(t, h, hub.BackfillLimits{Rows: 1})
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
				t.Logf("unrelated_events=%d converted=%d worst_slice_duration=%s required_headroom_bytes=%d", 2*unrelated, p.Converted, elapsed, p.RequiredHeadroomBytes)
			})
		}
	}
}
