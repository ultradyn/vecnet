package hub_test

import (
	"context"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/eventlog"
	"github.com/XertroV/vecnet/internal/hub"
	"modernc.org/sqlite"
)

// Count pages fetched by the actual production slice, including policy and
// owner discovery, on one connection. Cache hits count too, so warm caches
// cannot hide a scan. B-tree depth may grow; scanning unrelated rows may not.
func retentionSlicePageReads(t *testing.T, h *hub.Hub) (hub.EventRetentionStatus, int) {
	t.Helper()
	previous := h.DB.Stats().MaxOpenConnections
	h.DB.SetMaxOpenConns(1)
	defer h.DB.SetMaxOpenConns(previous)
	pages := func(reset bool) int {
		t.Helper()
		conn, err := h.DB.Conn(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		total := 0
		if err := conn.Raw(func(raw any) error {
			for _, op := range []sqlite.DBStatusOp{sqlite.DBStatusCacheHit, sqlite.DBStatusCacheMiss} {
				cur, _, err := raw.(sqlite.DBStatus).Status(op, reset)
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
	pages(true)
	stat, err := h.PruneEventSlice(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return stat, pages(false)
}

func TestRetentionOneRowSliceHasBoundedDiscoveryAndProbes(t *testing.T) {
	for _, kind := range []string{"owners", "acks", "outbox"} {
		t.Run(kind, func(t *testing.T) {
			var small int
			for _, n := range []int{100, 30000} {
				cfg := hub.DefaultConfig()
				cfg.EventRetentionLimits = eventlog.RetentionLimits{Rows: 1, Target: time.Hour}
				h, _, clock := preferencesHubClock(t, cfg)
				c := retentionEvent(t, h, "peer_target", clock.Now().Add(-8*24*time.Hour))
				if kind != "acks" {
					retentionAck(t, h, "peer_target", "*", c)
				}
				var query string
				switch kind {
				case "owners":
					// One heavily populated owner must be skipped by a single
					// owner-key seek, rather than DISTINCT over its event rows.
					query = `WITH RECURSIVE n(i) AS (VALUES(1) UNION ALL SELECT i+1 FROM n WHERE i<?)
					INSERT INTO events(owner,type,at,data) SELECT 'peer_unrelated','presence','2026-11-01T00:00:00.000Z','{}' FROM n`
				case "acks":
					query = `WITH RECURSIVE n(i) AS (VALUES(1) UNION ALL SELECT i+1 FROM n WHERE i<?)
					INSERT INTO acks(owner,scope,seq,epoch,acked_at) SELECT 'peer_unrelated_'||i,'*',1,1,'2026-11-01T00:00:00.000Z' FROM n`
				case "outbox":
					retentionSubscription(t, h, "peer_target", eventlog.Cursor{Epoch: c.Epoch}, "live")
					// The absent candidate copy makes BOTH exact-copy probes
					// run. Include pending, exhausted and terminal rows so a
					// partial pending-only index cannot satisfy this contract.
					query = `WITH RECURSIVE n(i) AS (VALUES(1) UNION ALL SELECT i+1 FROM n WHERE i<?)
					INSERT INTO event_outbox(sub_id,event_id,seq,body,created_at,done_at,terminal)
					SELECT 'sub_synthetic','event_'||i,i+100,'{}','2026-11-01T00:00:00.000Z',
					CASE WHEN i%3!=0 THEN '2026-11-01T00:00:00.000Z' END,
					CASE i%3 WHEN 1 THEN 'exhausted' WHEN 2 THEN 'http_410' END FROM n`
				}
				if _, err := h.DB.Exec(query, n); err != nil {
					t.Fatal(err)
				}
				stat, reads := retentionSlicePageReads(t, h)
				t.Logf("kind=%s unrelated_rows=%d page_reads=%d owners=%d scanned=%d deleted=%d pinned=%d", kind, n, reads, stat.Owners, stat.Scanned, stat.Deleted, stat.Pinned)
				if stat.Owners != 1 || stat.Scanned != 1 {
					t.Fatalf("one-row budget lost: %+v", stat)
				}
				if kind == "owners" && stat.Deleted != 1 || kind == "acks" && stat.Deleted != 0 || kind == "outbox" && (stat.Deleted != 0 || stat.Pinned != 1) {
					t.Fatalf("eligibility changed: %+v", stat)
				}
				if n == 100 {
					small = reads
				} else if reads > small+64 {
					t.Errorf("writer work grew with unrelated %s: small=%d large=%d slack=64", kind, small, reads)
				}
			}
		})
	}
}

func TestRetentionOwnerSeeksRotateAndWrapOnce(t *testing.T) {
	cfg := hub.DefaultConfig()
	cfg.EventRetentionLimits = eventlog.RetentionLimits{Rows: 1, Target: time.Hour}
	h, _, clock := preferencesHubClock(t, cfg)
	// The first owner is pinned by its unacknowledged prefix. The cursor must
	// still reach and delete the second owner, then wrap to the first.
	retentionEvent(t, h, "peer_a", clock.Now().Add(-8*24*time.Hour))
	b := retentionEvent(t, h, "peer_b", clock.Now().Add(-8*24*time.Hour))
	retentionAck(t, h, "peer_b", "*", b)
	for _, want := range []string{"peer_a", "peer_b", "peer_a"} {
		stat, err := h.PruneEventSlice(context.Background())
		if err != nil || stat.Owners != 1 || stat.Scanned != 1 {
			t.Fatalf("rotation: %+v err=%v", stat, err)
		}
		var got string
		if err := h.DB.QueryRow(`SELECT value FROM meta WHERE key='event_retention_owner_cursor'`).Scan(&got); err != nil || got != want {
			t.Fatalf("cursor=%s want=%s err=%v", got, want, err)
		}
	}
	retentionFloor(t, h, "peer_b", b.Seq)
	// A larger row budget must not visit the only remaining pinned owner twice.
	h.Cfg.EventRetentionLimits.Rows = 10
	stat, err := h.PruneEventSlice(context.Background())
	if err != nil || stat.Owners != 1 || stat.Scanned != 1 {
		t.Fatalf("wrapped more than once: %+v err=%v", stat, err)
	}
}
