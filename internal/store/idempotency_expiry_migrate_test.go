package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
)

func TestIdempotencyAgeIndexAdditiveAndUsed(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "upgrade.db")+"?"+dsnParams)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	migrations, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE schema_migrations(name TEXT PRIMARY KEY,sha256 TEXT NOT NULL,applied_at TEXT NOT NULL) STRICT`); err != nil {
		t.Fatal(err)
	}
	var addition migration
	for _, m := range migrations {
		if strings.HasSuffix(m.name, "_idempotency_created_at_index.sql") {
			addition = m
			break
		}
		if _, err := applyOne(ctx, db, m); err != nil {
			t.Fatal(err)
		}
	}
	if addition.name == "" {
		t.Fatal("idempotency age migration not found by suffix")
	}
	if _, err := db.Exec(`INSERT INTO idempotency VALUES ('synthetic-sender','synthetic-key','synthetic-hash','{"synthetic":true}','2030-01-01T00:00:00.000Z')`); err != nil {
		t.Fatal(err)
	}
	indexNames := func() map[string]bool {
		rows, err := db.Query(`SELECT name FROM sqlite_master WHERE type='index' ORDER BY name`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		out := map[string]bool{}
		for rows.Next() {
			var name string
			if err := rows.Scan(&name); err != nil {
				t.Fatal(err)
			}
			out[name] = true
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		return out
	}
	before := indexNames()
	if _, err := applyOne(ctx, db, addition); err != nil {
		t.Fatal(err)
	}
	after := indexNames()
	if len(after) != len(before)+1 || !after["idempotency_created_at"] {
		t.Fatal("migration must add only the idempotency age index")
	}
	for name := range before {
		if !after[name] {
			t.Fatalf("migration dropped index %s", name)
		}
	}
	var response, hash string
	if err := db.QueryRow(`SELECT response,body_sha256 FROM idempotency`).Scan(&response, &hash); err != nil {
		t.Fatal(err)
	}
	if response != `{"synthetic":true}` || hash != "synthetic-hash" {
		t.Fatal("migration rewrote retained bytes")
	}
	rows, err := db.Query(`EXPLAIN QUERY PLAN DELETE FROM idempotency WHERE rowid IN (SELECT rowid FROM idempotency WHERE created_at < ? ORDER BY created_at LIMIT ?)`, "2030-01-02T00:00:00.000Z", 100)
	if err != nil {
		t.Fatal(err)
	}
	used := false
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		t.Logf("expiry_query_plan=%s", detail)
		used = used || strings.Contains(detail, "COVERING INDEX idempotency_created_at (created_at<?)")
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	rows.Close()
	if !used {
		t.Fatal("bounded expiry did not use the age index")
	}
	// Also verify normal fresh initialization, and enumerate EVERY merged index,
	// including implicit PK/UNIQUE indexes and parallel plan additions.
	fresh, err := Open(filepath.Join(t.TempDir(), "fresh.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer fresh.Close()
	inventory, err := fresh.Query(`SELECT tbl_name,name FROM sqlite_master WHERE type='index' ORDER BY tbl_name,name`)
	if err != nil {
		t.Fatal(err)
	}
	defer inventory.Close()
	for inventory.Next() {
		var table, name string
		if err := inventory.Scan(&table, &name); err != nil {
			t.Fatal(err)
		}
		t.Logf("index_verdict=KEEP table=%s index=%s", table, name)
	}
	if err := inventory.Err(); err != nil {
		t.Fatal(err)
	}
}
