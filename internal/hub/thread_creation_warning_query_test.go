package hub

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/XertroV/vecnet/internal/store"
)

// Exercise the production query itself: losing LIMIT must fail even when the
// warning verdict remains correct, and losing the index must fail the plan.
func TestRapidThreadWarningBoundedIndexedEvidence(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "groups.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	err = db.Tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.Exec(`INSERT INTO groups(grp_id,created_by,created_at,last_active_at) VALUES('grp_synthetic','peer_synthetic','2026-11-15T00:00:00.000Z','2026-11-15T00:00:00.000Z')`); err != nil {
			return err
		}
		for i := range 20 {
			thread := fmt.Sprint("thr_synthetic", i)
			if _, err := tx.Exec(`INSERT INTO threads(thr_id,created_by,created_at,last_message_at) VALUES(?,'peer_synthetic','2026-11-15T00:00:00.000Z','2026-11-15T00:00:00.000Z')`, thread); err != nil {
				return err
			}
			if _, err := tx.Exec(`INSERT INTO group_thread_creations(group_id,thread_id,created_at) VALUES('grp_synthetic',?,'2026-11-15T00:00:00.000Z')`, thread); err != nil {
				return err
			}
		}
		n, err := recentGroupThreadCreations(ctx, tx, "grp_synthetic", "2026-11-14T23:59:00.000Z", "2026-11-15T00:00:00.000Z")
		if err != nil {
			return err
		}
		if n != 3 {
			t.Errorf("production evidence query read %d rows from 20 recent creations, want bounded 3", n)
		}
		rows, err := tx.Query(`EXPLAIN QUERY PLAN `+recentGroupThreadCreationsSQL, "grp_synthetic", "2026-11-14T23:59:00.000Z", "2026-11-15T00:00:00.000Z")
		if err != nil {
			return err
		}
		defer rows.Close()
		var plan string
		for rows.Next() {
			var id, parent, unused int
			var detail string
			if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
				return err
			}
			plan += detail + "\n"
		}
		if err := rows.Err(); err != nil {
			return err
		}
		t.Log(plan)
		if !strings.Contains(plan, "SEARCH group_thread_creations USING COVERING INDEX group_thread_creations_recent (group_id=? AND created_at>? AND created_at<?)") {
			t.Errorf("production window missed covering group/time index: %s", plan)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
