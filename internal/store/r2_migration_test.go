package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func r2MigrationDB(t *testing.T) (*sql.DB, migration) {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "r2.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	db.SetMaxOpenConns(1)
	if _, err = db.Exec(`PRAGMA foreign_keys=ON; CREATE TABLE schema_migrations(name TEXT PRIMARY KEY,sha256 TEXT NOT NULL,applied_at TEXT NOT NULL) STRICT`); err != nil {
		t.Fatal(err)
	}
	migrations, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	var r2 migration
	count := 0
	for _, m := range migrations {
		if strings.HasSuffix(m.name, "_r2_blobs.sql") {
			r2 = m
			count++
			continue
		}
		if _, err := applyOne(context.Background(), db, m); err != nil {
			t.Fatal(err)
		}
	}
	if count != 1 {
		t.Fatalf("want exactly one migration by _r2_blobs.sql suffix, got %d", count)
	}
	return db, r2
}

func r2Snapshot(t *testing.T, db *sql.DB, table string) []byte {
	t.Helper()
	rows, err := db.Query(`SELECT * FROM ` + table)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		t.Fatal(err)
	}
	var result [][]any
	for rows.Next() {
		values := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range values {
			ptrs[i] = &values[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			t.Fatal(err)
		}
		result = append(result, values)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	out, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

const r2InsertObject = `INSERT INTO blob_objects(blob_id,sha256,object_key,uploader,size,media_type,issued_at,upload_deadline,retention_class,expiry_pref_value,expiry_pref_source,expiry_pref_revision,expires_at,checksum_profile,expected_checksum,state) VALUES(?, ?, ?, 'synthetic-uploader',7,'text/plain','2026-11-05T00:00:00.000Z','2026-11-05T00:05:00.000Z',?,?,'default',0,?,'sha256',?,'pending')`

func TestR2MigrationKeepsDiskPins(t *testing.T) {
	db, m := r2MigrationDB(t)
	hash := strings.Repeat("a", 64)
	for _, stmt := range []string{`INSERT INTO blobs VALUES('` + hash + `',7,'text/plain','synthetic-uploader','2026-11-05T00:00:00.000Z',NULL,NULL)`, `INSERT INTO blob_uploaders VALUES('` + hash + `','synthetic-uploader')`, `INSERT INTO blob_refs VALUES('` + hash + `','synthetic-message')`, `INSERT INTO blob_quota VALUES('synthetic-uploader','2026-11-05',7)`} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	before := map[string][]byte{}
	for _, table := range []string{"blobs", "blob_uploaders", "blob_refs", "blob_quota"} {
		before[table] = r2Snapshot(t, db, table)
	}
	if _, err := applyOne(context.Background(), db, m); err != nil {
		t.Fatal(err)
	}
	for table, want := range before {
		if !reflect.DeepEqual(want, r2Snapshot(t, db, table)) {
			t.Errorf("migration changed existing %s bytes", table)
		}
	}
	for _, item := range []struct{ id, end, class string }{{"blo_00000000000000000000000001", "2026-11-06T00:00:00.000Z", "1d"}, {"blo_00000000000000000000000002", "2026-11-12T00:00:00.000Z", "7d"}} {
		_, err := db.Exec(r2InsertObject, item.id, hash, "attachments/v1/exp/"+item.class+"/0123456789abcdef0123456789abcdef/"+item.id, item.class, item.class, item.end, hash)
		if err != nil {
			t.Fatal("independent same-hash object rejected", err)
		}
	}
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM blob_objects WHERE sha256=?`, hash).Scan(&n); err != nil || n != 2 {
		t.Fatal("objects deduped by SHA")
	}
	if applied, err := Migrate(context.Background(), db); err != nil || len(applied) != 0 {
		t.Fatal("migration not idempotent")
	}
}

func TestR2MigrationConstraintsAndTables(t *testing.T) {
	db, m := r2MigrationDB(t)
	if _, err := applyOne(context.Background(), db, m); err != nil {
		t.Fatal(err)
	}
	tables := []string{"blob_objects", "blob_upload_requests", "blob_object_refs", "blob_r2_quota", "blob_r2_storage", "durable_blob_payloads", "durable_blob_refs", "blob_purge_tombstones", "blob_delete_jobs", "blob_usage_events"}
	for _, table := range tables {
		var n int
		if err := db.QueryRow(`SELECT count(*) FROM pragma_table_list WHERE name=? AND strict=1`, table).Scan(&n); err != nil || n != 1 {
			t.Errorf("missing STRICT %s", table)
		}
	}
	for _, column := range []string{"uploader, state", "state, expires_at"} {
		compact := strings.ReplaceAll(strings.ToLower(m.sql), " ", "")
		if !strings.Contains(compact, strings.ReplaceAll(column, " ", "")) {
			t.Errorf("missing composite index %s", column)
		}
	}
	for _, bad := range []string{"signed_url", "presigned_url", "CREATE TABLE preferences"} {
		if strings.Contains(strings.ToLower(m.sql), strings.ToLower(bad)) {
			t.Errorf("forbidden persisted field %s", bad)
		}
	}
	hash := strings.Repeat("b", 64)
	id := "blo_00000000000000000000000003"
	prefix := "attachments/v1/"
	for _, tt := range []struct {
		class, pref, key, hash string
		end                    any
		valid                  bool
	}{
		{"keep", "off", prefix + "keep/0123456789abcdef0123456789abcdef/" + id, hash, nil, true},
		{"keep", "off", prefix + "keep/0123456789abcdef0123456789abcdef/" + id, hash, "9999-01-01T00:00:00.000Z", false},
		{"1h", "1h", prefix + "exp/1h/0123456789abcdef0123456789abcdef/" + id, hash, "2026-11-05T01:00:00.000Z", true},
		{"1h", "1h", prefix + "exp/1h/0123456789abcdef0123456789abcdef/" + id, hash, nil, false},
		{"1h", "1h", prefix + "keep/0123456789abcdef0123456789abcdef/" + id, hash, "2026-11-05T01:00:00.000Z", false},
		{"1h", "off", prefix + "exp/1h/0123456789abcdef0123456789abcdef/" + id, hash, "2026-11-05T01:00:00.000Z", false},
		{"2h", "2h", prefix + "exp/2h/0123456789abcdef0123456789abcdef/" + id, hash, "2026-11-05T02:00:00.000Z", false},
		{"1h", "1h", prefix + "exp/1h/0123456789abcdef0123456789abcdef/" + id, strings.Repeat("B", 64), "2026-11-05T01:00:00.000Z", false},
	} {
		_, err := db.Exec(r2InsertObject, id, tt.hash, tt.key, tt.class, tt.pref, tt.end, tt.hash)
		if (err == nil) != tt.valid {
			t.Errorf("class/deadline/key/hash constraint mismatch %s valid=%v: %v", tt.class, tt.valid, err)
		}
		if _, err := db.Exec(`DELETE FROM blob_objects`); err != nil {
			t.Fatal(err)
		}
	}
	_, err := db.Exec(r2InsertObject, id, hash, prefix+"exp/1h/0123456789abcdef0123456789abcdef/"+id, "1h", "1h", "2026-11-05T01:00:00.000Z", hash)
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{`UPDATE blob_objects SET size=0`, `UPDATE blob_objects SET size=26214401`, `UPDATE blob_objects SET state='other'`, `UPDATE blob_objects SET checksum_profile='other'`, `UPDATE blob_objects SET sha256_verified=1`, `UPDATE blob_objects SET state='complete'`, `UPDATE blob_objects SET expected_checksum='bad'`, `INSERT INTO blob_r2_quota VALUES('u','2026-11-05',-1,0,0,0)`, `UPDATE blob_r2_storage SET retained_bytes=-1`, `INSERT INTO blob_object_refs VALUES('absent', 'absent', '` + hash + `')`} {
		if _, err := db.Exec(stmt); err == nil {
			t.Errorf("invalid row accepted: %s", stmt)
		}
	}
}

// r2EvidenceID names the one valid pending R2 row that r2EvidenceDB inserts;
// r2CompleteValid completes it with valid verification evidence.
const r2EvidenceID = "blo_00000000000000000000000004"

const r2CompleteValid = `UPDATE blob_objects SET state='complete', sha256_verified=1, verified_algorithm='sha256', verified_digest=sha256, verified_size=size, etag='"synthetic-etag"', verified_at='2026-11-05T00:01:00.000Z'`

func r2EvidenceDB(t *testing.T) (*sql.DB, string) {
	t.Helper()
	db, m := r2MigrationDB(t)
	if _, err := applyOne(context.Background(), db, m); err != nil {
		t.Fatal(err)
	}
	hash := strings.Repeat("c", 64)
	key := "attachments/v1/exp/1h/0123456789abcdef0123456789abcdef/" + r2EvidenceID
	if _, err := db.Exec(r2InsertObject, r2EvidenceID, hash, key, "1h", "1h", "2026-11-05T01:00:00.000Z", hash); err != nil {
		t.Fatal(err)
	}
	return db, hash
}

// TestR2MigrationVerificationEvidenceCheck: the evidence-consistency CHECK is
// exercised on its own. Each negative starts from a row whose state guards
// are already satisfied (completed and verified, or pending and unverified),
// so only the evidence CHECK can reject it.
func TestR2MigrationVerificationEvidenceCheck(t *testing.T) {
	t.Run("complete_evidence_fields", func(t *testing.T) {
		db, _ := r2EvidenceDB(t)
		if _, err := db.Exec(r2CompleteValid); err != nil {
			t.Fatalf("valid completion rejected: %v", err)
		}
		other := strings.Repeat("d", 64)
		for _, stmt := range []string{
			`UPDATE blob_objects SET verified_algorithm=NULL`,
			`UPDATE blob_objects SET verified_algorithm='md5'`,
			`UPDATE blob_objects SET verified_digest=NULL`,
			`UPDATE blob_objects SET verified_digest='` + other + `'`,
			`UPDATE blob_objects SET verified_size=NULL`,
			`UPDATE blob_objects SET verified_size=8`,
			`UPDATE blob_objects SET etag=NULL`,
			`UPDATE blob_objects SET etag=''`,
			`UPDATE blob_objects SET verified_at=NULL`,
			`UPDATE blob_objects SET verified_at='not-a-time'`,
		} {
			if _, err := db.Exec(stmt); err == nil {
				t.Errorf("corrupt verification evidence accepted: %s", stmt)
			}
		}
		var verified int
		var etag string
		if err := db.QueryRow(`SELECT sha256_verified, etag FROM blob_objects WHERE blob_id=?`, r2EvidenceID).Scan(&verified, &etag); err != nil || verified != 1 || etag != `"synthetic-etag"` {
			t.Fatal("rejected corruption changed the completed row")
		}
	})
	t.Run("complete_without_evidence", func(t *testing.T) {
		db, _ := r2EvidenceDB(t)
		if _, err := db.Exec(`UPDATE blob_objects SET state='complete', sha256_verified=1`); err == nil {
			t.Fatal("pending -> complete with sha256_verified=1 but no evidence accepted")
		}
	})
	t.Run("unverified_with_evidence", func(t *testing.T) {
		db, _ := r2EvidenceDB(t)
		for _, stmt := range []string{
			`UPDATE blob_objects SET verified_algorithm='sha256'`,
			`UPDATE blob_objects SET etag='"synthetic-etag"'`,
			`UPDATE blob_objects SET verified_at='2026-11-05T00:01:00.000Z'`,
		} {
			if _, err := db.Exec(stmt); err == nil {
				t.Errorf("unverified row accepted evidence: %s", stmt)
			}
		}
	})
}

// TestR2MigrationObjectRefForeignKeys: blob_object_refs has independent
// foreign keys to messages and blob_objects, and cascades on message delete.
func TestR2MigrationObjectRefForeignKeys(t *testing.T) {
	db, hash := r2EvidenceDB(t)
	if _, err := db.Exec(`INSERT INTO messages(msg_id,thr_id,seq,sender,kind,created_at,body) VALUES('msg_r2ref','thr_r2ref',1,'synthetic-sender','message','2026-11-05T00:00:00.000Z','{}')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO blob_object_refs VALUES('msg_r2ref', ?, ?)`, r2EvidenceID, hash); err != nil {
		t.Fatalf("valid reference rejected: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO blob_object_refs VALUES('msg_absent', ?, ?)`, r2EvidenceID, hash); err == nil || !strings.Contains(err.Error(), "FOREIGN KEY") {
		t.Errorf("missing message with a valid object: err = %v, want a foreign key error", err)
	}
	if _, err := db.Exec(`INSERT INTO blob_object_refs VALUES('msg_r2ref', 'blo_00000000000000000000000005', ?)`, hash); err == nil || !strings.Contains(err.Error(), "FOREIGN KEY") {
		t.Errorf("valid message with a missing object: err = %v, want a foreign key error", err)
	}
	if _, err := db.Exec(`DELETE FROM messages WHERE msg_id='msg_r2ref'`); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM blob_object_refs`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("blob_object_refs after message delete = %d, want 0 (cascade)", n)
	}
}
