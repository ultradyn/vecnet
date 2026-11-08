package opview

import (
	"path/filepath"
	"strings"
	"testing"
)

import "github.com/XertroV/vecnet/internal/store"

// Explain the actual production aggregate, not a similar stand-in query.
// Wrapping created_at in the WHERE predicate would turn this bounded index
// lookup into a scan and must fail this test.
func TestActivityStatsQueryUsesTimeIndex(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.Query(`EXPLAIN QUERY PLAN `+activityStatsQuery, int64(1794267045123), "2026-11-09T23:30:45.123Z", "2026-11-10T00:30:45.123Z")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var plan string
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		plan += detail + "\n"
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	t.Log(plan)
	if !strings.Contains(plan, "SEARCH messages USING INDEX messages_history_age (created_at>? AND created_at<?)") {
		t.Fatalf("activity aggregate must use bounded time index: %s", plan)
	}
}
