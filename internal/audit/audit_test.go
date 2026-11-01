package audit_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/XertroV/vecnet/internal/audit"
	"github.com/XertroV/vecnet/internal/store"
)

func openTemp(t *testing.T) *store.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "hub.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func count(t *testing.T, db *store.DB) int {
	t.Helper()
	var n int
	if err := db.QueryRowContext(context.Background(), `SELECT count(*) FROM audit`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	return n
}

func record(db *store.DB, actor, action string, targets []string, detail map[string]any) error {
	return db.Tx(context.Background(), func(tx *sql.Tx) error {
		return audit.Record(context.Background(), tx, actor, action, targets, detail)
	})
}

func TestAuditRejectsBodies(t *testing.T) {
	db := openTemp(t)
	for _, detail := range []map[string]any{
		{"text": "hi"},
		{"Body": "hi"},
		{"SECRET": "vn_peer_x"},
		{"token": "vn_enrol_x"},
		{"ok": map[string]any{"text": "nested"}},
	} {
		if err := record(db, "peer_x", "send", []string{"msg_x"}, detail); err == nil {
			t.Fatalf("Record(%v) = nil, want error", detail)
		}
	}
	if n := count(t, db); n != 0 {
		t.Fatalf("rejected records wrote %d rows", n)
	}
}

func TestAuditRecordsRow(t *testing.T) {
	db := openTemp(t)
	if err := record(db, "peer_a", "send", []string{"msg_1", "thr_1"}, map[string]any{"bytes": 12}); err != nil {
		t.Fatalf("Record: %v", err)
	}
	if err := record(db, "peer_a", "ack", nil, nil); err != nil {
		t.Fatalf("Record(nil, nil): %v", err)
	}
	rows, err := db.QueryContext(context.Background(), `SELECT at, actor, action, target_ids, detail FROM audit ORDER BY id`)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()
	type row struct{ at, actor, action, targets, detail string }
	var got []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.at, &r.actor, &r.action, &r.targets, &r.detail); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got = append(got, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("rows = %d, want 2", len(got))
	}
	var targets []string
	if err := json.Unmarshal([]byte(got[0].targets), &targets); err != nil || len(targets) != 2 || targets[0] != "msg_1" || targets[1] != "thr_1" {
		t.Fatalf("targets = %s (%v)", got[0].targets, err)
	}
	if got[0].actor != "peer_a" || got[0].action != "send" || got[0].detail != `{"bytes":12}` || len(got[0].at) != 24 {
		t.Fatalf("row 0 = %+v", got[0])
	}
	if got[1].targets != "[]" || got[1].detail != "{}" {
		t.Fatalf("row 1 = %+v", got[1])
	}
}
