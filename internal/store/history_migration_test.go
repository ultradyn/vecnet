package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
)

func TestHistoryMigrationIsAdditiveAndPreservesLegacyBytes(t *testing.T) {
	ctx := context.Background()
	migrations, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "hub.db")
	raw, err := sql.Open("sqlite", "file:"+path+"?"+dsnParams)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	if _, err := raw.Exec(`CREATE TABLE schema_migrations(name TEXT PRIMARY KEY,sha256 TEXT NOT NULL,applied_at TEXT NOT NULL) STRICT`); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, m := range migrations {
		if strings.HasSuffix(m.name, "_thread_seq_history_tombstones.sql") {
			found = true
			break
		}
		if _, err := applyOne(ctx, raw, m); err != nil {
			t.Fatal(err)
		}
	}
	if !found {
		t.Fatal("missing history migration")
	}
	if _, err := raw.Exec(`INSERT INTO threads(thr_id,created_by,created_at,last_message_at,message_count,budget_remaining) VALUES('thr_synthetic','peer_synthetic','2026-01-01T00:00:00.000Z','2026-01-01T00:00:00.000Z',7,493)`); err != nil {
		t.Fatal(err)
	}
	const body = `{ "synthetic": "exact legacy bytes" }`
	if _, err := raw.Exec(`INSERT INTO messages(msg_id,thr_id,seq,sender,kind,created_at,body) VALUES('msg_synthetic','thr_synthetic',12,'peer_synthetic','message','2026-01-01T00:00:00.000Z',?)`, body); err != nil {
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var got string
	var high, count, budget int
	var mark sql.NullString
	if err := db.QueryRow(`SELECT m.body,m.history_expired_at,t.last_allocated_seq,t.message_count,t.budget_remaining FROM messages m JOIN threads t USING(thr_id)`).Scan(&got, &mark, &high, &count, &budget); err != nil {
		t.Fatal(err)
	}
	if got != body || mark.Valid || high != 0 || count != 7 || budget != 493 {
		t.Fatal("migration rewrote existing message or lifetime metadata")
	}
	// The allocator's legacy max adoption is bounded by the existing thread/seq
	// index; it remains safe after the newest/all bodies are subsequently erased.
	if _, err := db.Exec(`UPDATE threads SET last_allocated_seq=max(last_allocated_seq,(SELECT max(seq) FROM messages WHERE messages.thr_id=threads.thr_id)) WHERE thr_id='thr_synthetic'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM messages`); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT last_allocated_seq FROM threads`).Scan(&high); err != nil || high != 12 {
		t.Fatal("legacy high water did not survive deletion")
	}
	if _, err := db.Exec(`UPDATE threads SET last_allocated_seq=1`); err == nil {
		t.Fatal("sequence could decrease")
	}
	var integrity string
	if err := db.QueryRow(`PRAGMA integrity_check`).Scan(&integrity); err != nil || integrity != "ok" {
		t.Fatal("migration integrity check failed")
	}
}

func TestHistoryMaintenanceQueriesUseKeptIndexes(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, tc := range []struct{ query, index string }{
		{`SELECT msg_id FROM messages WHERE created_at<'2026' AND (created_at,msg_id)>('2025','msg_synthetic') ORDER BY created_at,msg_id LIMIT 1`, "messages_history_age"},
		{`SELECT 1 FROM events WHERE payload_format='inline' AND json_extract(data,'$.message.message_id')='msg_synthetic'`, "events_history_inline_message"},
		{`SELECT 1 FROM events WHERE payload_format='inline' AND json_extract(data,'$.message_id')='msg_synthetic'`, "events_history_message"},
		{`SELECT 1 FROM events WHERE payload_format='inline' AND json_extract(data,'$.delivery_id')='dly_synthetic'`, "events_history_delivery"},
		{`SELECT 1 FROM event_outbox WHERE event_id='dly_synthetic'`, "event_outbox_history_delivery"},
		{`SELECT 1 FROM approvals WHERE msg_id='msg_synthetic'`, "approvals_history_message"},
		{`SELECT 1 FROM jobs WHERE thr_id='thr_synthetic'`, "jobs_history_thread"},
	} {
		rows, err := db.Query(`EXPLAIN QUERY PLAN ` + tc.query)
		if err != nil {
			t.Fatal(err)
		}
		plan := ""
		for rows.Next() {
			var id, parent, unused int
			var detail string
			if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
				rows.Close()
				t.Fatal(err)
			}
			plan += detail + "\n"
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("%s: %s", tc.index, strings.TrimSpace(plan))
		if !strings.Contains(plan, tc.index) {
			t.Errorf("query missed maintenance index %s", tc.index)
		}
	}
}
