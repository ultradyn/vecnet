package store_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/store"
)

func pragmaInt(t *testing.T, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, name string) int64 {
	t.Helper()
	var n int64
	if err := q.QueryRowContext(context.Background(), "PRAGMA "+name).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestCompactionNewDatabaseAndEveryReopenedConnection(t *testing.T) {
	t.Setenv("VECNET_SQLITE_SYNCHRONOUS", "full")
	path := filepath.Join(t.TempDir(), "hub.db")
	// An existing empty file is still a new SQLite database.
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	for pass := 0; pass < 2; pass++ {
		db, err := store.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		var connections []*sql.Conn
		for i := 0; i < 4; i++ {
			c, err := db.Conn(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			connections = append(connections, c)
			for name, want := range map[string]int64{"page_size": 4096, "auto_vacuum": 2, "synchronous": 2, "wal_autocheckpoint": 1000, "foreign_keys": 1} {
				if got := pragmaInt(t, c, name); got != want {
					t.Errorf("pass=%d connection=%d %s=%d want %d", pass, i, name, got, want)
				}
			}
			var mode string
			if err := c.QueryRowContext(context.Background(), "PRAGMA journal_mode").Scan(&mode); err != nil || mode != "wal" {
				t.Fatalf("journal_mode=%s: %v", mode, err)
			}
		}
		for _, c := range connections {
			c.Close()
		}
		db.Close()
	}
}

func TestCompactionExistingNoneAndBackupKeepPageSize(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	raw, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{"PRAGMA page_size=8192", "PRAGMA auto_vacuum=NONE", "CREATE TABLE legacy(value TEXT)", "INSERT INTO legacy VALUES ('preserved')"} {
		if _, err := raw.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	raw.Close()
	db, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := pragmaInt(t, db, "auto_vacuum"); got != 0 {
		t.Fatalf("existing auto_vacuum=%d", got)
	}
	if got := pragmaInt(t, db, "page_size"); got != 8192 {
		t.Fatalf("existing page_size=%d", got)
	}
	// Quiesced DB+WAL backup: closing the only handle checkpoints its WAL.
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	backup := filepath.Join(t.TempDir(), "restored.db")
	if err := os.WriteFile(backup, b, 0600); err != nil {
		t.Fatal(err)
	}
	restored, err := store.Open(backup)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	if got := pragmaInt(t, restored, "auto_vacuum"); got != 0 {
		t.Fatalf("restored auto_vacuum=%d", got)
	}
	if got := pragmaInt(t, restored, "page_size"); got != 8192 {
		t.Fatalf("restored page_size=%d", got)
	}
	var value string
	if err := restored.QueryRow("SELECT value FROM legacy").Scan(&value); err != nil || value != "preserved" {
		t.Fatalf("restored fixture: %v", err)
	}
}

// Free whole overflow pages in a private on-disk fixture; no production tables.
func freedPages(t *testing.T, db *store.DB) int64 {
	t.Helper()
	if _, err := db.Exec("CREATE TABLE reclaim_fixture(id INTEGER PRIMARY KEY, body BLOB)"); err != nil {
		t.Fatal(err)
	}
	if err := db.Tx(context.Background(), func(tx *sql.Tx) error {
		for i := 0; i < 400; i++ {
			if _, err := tx.Exec("INSERT INTO reclaim_fixture(body) VALUES (zeroblob(8192))"); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("DELETE FROM reclaim_fixture"); err != nil {
		t.Fatal(err)
	}
	n := pragmaInt(t, db, "freelist_count")
	if n < 128 {
		t.Fatalf("fixture freed only %d pages", n)
	}
	return n
}
func TestCompactionBoundedReclaimAndDiskPressure(t *testing.T) {
	db := openTemp(t)
	free := freedPages(t, db)
	pages := pragmaInt(t, db, "page_count")
	opts := store.MaintenanceOptions{ReclaimPages: 7, Target: time.Second, AvailableBytes: func(context.Context) (int64, error) { return 1 << 40, nil }}
	r, err := db.Maintain(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if r.ReclaimedPages < 1 || r.ReclaimedPages > 7 || pages-r.After.PageCount != r.ReclaimedPages || free-r.After.FreelistCount < 1 {
		t.Fatalf("bounded reclaim: %+v", r)
	}
	if r.Before.Synchronous != 2 || r.After.AutoVacuum != 2 || r.After.PageSize != 4096 {
		t.Fatalf("settings lost: %+v", r)
	}
	if r.After.DBBytes != r.After.PageCount*4096 {
		t.Fatalf("checkpointed physical bytes: %+v", r)
	}
	opts.AvailableBytes = func(context.Context) (int64, error) { return 0, nil }
	r, err = db.Maintain(context.Background(), opts)
	if err != nil || r.PausedReason != "low_headroom" || r.ReclaimedPages != 0 || r.Before.PageCount != r.After.PageCount {
		t.Fatalf("disk pressure: %+v %v", r, err)
	}
	// Probe between vacuum steps, not only at entry.
	probes := 0
	opts.AvailableBytes = func(context.Context) (int64, error) {
		probes++
		if probes > 2 {
			return 0, nil
		}
		return 1 << 40, nil
	}
	r, err = db.Maintain(context.Background(), opts)
	if err != nil || r.PausedReason != "low_headroom" || r.ReclaimedPages > 1 {
		t.Fatalf("mid-slice pressure: %+v %v", r, err)
	}
	b, err := json.Marshal(r)
	if err != nil || strings.Contains(string(b), "reclaim_fixture") || strings.Contains(string(b), "hub.db") {
		t.Fatalf("diagnostic exposes SQL or paths: %s %v", b, err)
	}
}
func TestCompactionPinnedReaderYieldsAndResumes(t *testing.T) {
	db := openTemp(t)
	freedPages(t, db)
	ready, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		done <- db.ReadSnapshot(context.Background(), func(c *sql.Conn) error {
			var n int
			if err := c.QueryRowContext(context.Background(), "SELECT count(*) FROM reclaim_fixture").Scan(&n); err != nil {
				return err
			}
			close(ready)
			<-release
			return nil
		})
	}()
	<-ready
	released := false
	defer func() {
		if !released {
			close(release)
			if err := <-done; err != nil {
				t.Error(err)
			}
		}
	}()
	if _, err := db.Exec("INSERT INTO reclaim_fixture(body) VALUES ('reader-pinned')"); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	r, err := db.Maintain(context.Background(), store.MaintenanceOptions{ReclaimPages: 128})
	if err != nil || r.PausedReason != "reader_pinned" || r.Checkpoint.LogPages <= r.Checkpoint.CheckpointedPages || r.ReclaimedPages != 0 {
		t.Fatalf("reader pin: %+v %v", r, err)
	}
	if time.Since(start) > time.Second {
		t.Fatal("PASSIVE checkpoint waited for pinned reader")
	}
	close(release)
	released = true
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	// Statistics and checkpoint work count against the cooperative target.
	// Reproduce a slow first probe exhausting a slice before reclaim starts.
	slowProbe := true
	opts := store.MaintenanceOptions{ReclaimPages: 7, Target: time.Second, AvailableBytes: func(context.Context) (int64, error) {
		if slowProbe {
			slowProbe = false
			time.Sleep(time.Second + 10*time.Millisecond)
		}
		return 1 << 40, nil
	}}
	pages := pragmaInt(t, db, "page_count")
	var resumed store.MaintenanceResult
	for slice := 0; slice < 3; slice++ {
		before := pragmaInt(t, db, "page_count")
		resumed, err = db.Maintain(context.Background(), opts)
		after := pragmaInt(t, db, "page_count")
		t.Logf("resume slice %d: %+v, err=%v, physical pages=%d -> %d", slice+1, resumed, err, before, after)
		if err != nil || resumed.ReclaimedPages < 0 || resumed.ReclaimedPages > 7 || before-after != resumed.ReclaimedPages || pages-after > 7 {
			t.Fatalf("bounded resume: %+v %v", resumed, err)
		}
		if slice == 0 && (resumed.PausedReason != "time_budget" || resumed.ReclaimedPages != 0) {
			t.Fatalf("slow probe did not exhaust the first slice: %+v", resumed)
		}
		if resumed.ReclaimedPages > 0 {
			break
		}
		// Only a cooperative expiration permits a later slice. A permanent
		// reader pin or checkpoint-only/no-op reclaim must still fail.
		if resumed.PausedReason != "time_budget" {
			t.Fatalf("reclaim did not resume after reader exit: %+v", resumed)
		}
	}
	if err != nil || resumed.ReclaimedPages < 1 || resumed.Checkpoint.LogPages != resumed.Checkpoint.CheckpointedPages {
		t.Fatalf("reclaim did not resume after reader exit: %+v %v", resumed, err)
	}
	// All four pool connections retain writer settings after maintenance.
	var conns []*sql.Conn
	for i := 0; i < 4; i++ {
		c, err := db.Conn(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		conns = append(conns, c)
		if got := pragmaInt(t, c, "busy_timeout"); got != 5000 {
			t.Errorf("busy_timeout leaked: %d", got)
		}
	}
	for _, c := range conns {
		c.Close()
	}
}
func TestCompactionNoneReusesPagesWithoutConversion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "none.db")
	raw, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec("CREATE TABLE legacy(id INTEGER PRIMARY KEY)"); err != nil {
		t.Fatal(err)
	}
	raw.Close()
	db, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	freedPages(t, db)
	r, err := db.Maintain(context.Background(), store.MaintenanceOptions{ReclaimPages: 128})
	if err != nil || r.ReclaimedPages != 0 || r.After.AutoVacuum != 0 || r.Before.PageCount != r.After.PageCount {
		t.Fatalf("NONE maintenance: %+v %v", r, err)
	}
	before := r.After.PageCount
	if err := db.Tx(context.Background(), func(tx *sql.Tx) error {
		for i := 0; i < 300; i++ {
			if _, err := tx.Exec("INSERT INTO reclaim_fixture(body) VALUES(zeroblob(8192))"); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if got := pragmaInt(t, db, "page_count"); got > before {
		t.Fatalf("freed pages not reused: %d -> %d", before, got)
	}
}
func TestCompactionRejectsUnboundedLimitsAndCancellation(t *testing.T) {
	db := openTemp(t)
	for _, o := range []store.MaintenanceOptions{{ReclaimPages: 129}, {ReclaimPages: -1}, {Target: -1}, {MinFreeBytes: -1}} {
		if _, err := db.Maintain(context.Background(), o); err == nil {
			t.Fatalf("invalid limits accepted: %+v", o)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := db.Maintain(ctx, store.MaintenanceOptions{}); err == nil {
		t.Fatal("canceled maintenance succeeded")
	}
}

func TestCompactionPhysicalPageCapAcrossPointerMaps(t *testing.T) {
	for _, expireSnapshot := range []bool{false, true} {
		name := "complete_snapshot"
		if expireSnapshot {
			name = "expired_snapshot"
		}
		t.Run(name, func(t *testing.T) {
			// This disposable disk fixture tests page geometry, not power-loss durability.
			path := filepath.Join(t.TempDir(), "hub.db")
			db, err := store.OpenWithSynchronous(path, "normal")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { db.Close() })
			freedPages(t, db)
			pageSize := pragmaInt(t, db, "page_size")
			pages := pragmaInt(t, db, "page_count")
			// This newly created SQLite fixture has no reserved bytes. Pointer maps
			// occupy page 2 and every pageSize/5+1 pages thereafter.
			stride := pageSize/5 + 1
			boundary := 2 + (pages-3)/stride*stride
			if boundary <= 2 || pages < boundary+2 || pragmaInt(t, db, "freelist_count") <= pages-boundary {
				t.Fatalf("fixture does not cross a pointer map: size=%d pages=%d map=%d", pageSize, pages, boundary)
			}
			expired := false
			maintain := func(limit int) (store.MaintenanceResult, int64, int64) {
				t.Helper()
				before := pragmaInt(t, db, "page_count")
				freeBefore := pragmaInt(t, db, "freelist_count")
				expiredThisSlice := false
				opts := store.MaintenanceOptions{ReclaimPages: limit, Target: time.Second}
				if expireSnapshot && !expired {
					opts.AvailableBytes = func(ctx context.Context) (int64, error) {
						// Another connection observes the old page count until reclaim
						// commits. Expire only the final committed statistics probe;
						// every vacuum statement and checkpoint still runs for real.
						if pragmaInt(t, db, "page_count") < before {
							expired = true
							expiredThisSlice = true
							<-ctx.Done()
							return 0, ctx.Err()
						}
						return 1 << 40, nil
					}
				}
				r, err := db.Maintain(context.Background(), opts)
				if err != nil {
					t.Fatal(err)
				}
				// Observe committed geometry independently, even when the slice
				// expired before it could publish a complete After snapshot.
				after := pragmaInt(t, db, "page_count")
				freeAfter := pragmaInt(t, db, "freelist_count")
				if r.ReclaimedPages < 0 || r.ReclaimedPages > int64(limit) || before-after != r.ReclaimedPages || r.Before.PageCount != before || r.Before.PageSize != pageSize || r.Before.FreelistCount != freeBefore {
					t.Fatalf("%d-page physical cap/accounting: before=%d after=%d result=%+v", limit, before, after, r)
				}
				info, err := os.Stat(path)
				if err != nil {
					t.Fatal(err)
				}
				freeReclaimed := freeBefore - freeAfter
				if pragmaInt(t, db, "page_size") != pageSize || info.Size() != after*pageSize || freeAfter < 0 || freeAfter > after || freeReclaimed < 0 || freeReclaimed > r.ReclaimedPages || r.ReclaimedPages > 0 && freeReclaimed == 0 {
					t.Fatalf("committed disk geometry: pages=%d->%d free=%d->%d bytes=%d result=%+v", before, after, freeBefore, freeAfter, info.Size(), r)
				}
				if r.After == (store.StorageStats{}) {
					if r.PausedReason != "time_budget" || r.ElapsedNanos < opts.Target.Nanoseconds() {
						t.Fatalf("missing committed snapshot without expired slice: %+v", r)
					}
				} else if r.After.PageSize != pageSize || r.After.PageCount != after || r.After.DBBytes != info.Size() || r.After.FreelistCount != freeAfter {
					t.Fatalf("committed snapshot disagrees with disk geometry: pages=%d free=%d bytes=%d result=%+v", after, freeAfter, info.Size(), r)
				}
				if expiredThisSlice {
					t.Logf("expired committed snapshot: pages=%d->%d free=%d->%d bytes=%d result=%+v", before, after, freeBefore, freeAfter, info.Size(), r)
					if r.ReclaimedPages != int64(limit) || r.After != (store.StorageStats{}) || r.PausedReason != "time_budget" {
						t.Fatalf("final statistics probe did not expire after bounded commit: %+v", r)
					}
				}
				return r, after, freeAfter
			}
			// Position immediately above the real structural edge using bounded slices.
			for i := 0; i < 20 && pragmaInt(t, db, "page_count") > boundary+2; i++ {
				remaining := pragmaInt(t, db, "page_count") - (boundary + 2)
				r, _, _ := maintain(int(min(int64(128), remaining)))
				if r.ReclaimedPages == 0 {
					t.Fatalf("could not approach pointer map %d: %+v", boundary, r)
				}
			}
			if got := pragmaInt(t, db, "page_count"); got != boundary+2 {
				t.Fatalf("pointer-map approach not encountered: pages=%d map=%d", got, boundary)
			}
			r, after, free := maintain(1)
			if r.ReclaimedPages != 1 || after != boundary+1 || free == 0 {
				t.Fatalf("one-page approach to pointer map %d not encountered: %+v", boundary, r)
			}
			r, after, free = maintain(1)
			if r.ReclaimedPages != 0 || after != boundary+1 || free == 0 || free != r.Before.FreelistCount {
				t.Fatalf("one-page budget did not stop at pointer map %d: %+v", boundary, r)
			}
			r, after, free = maintain(128)
			if r.ReclaimedPages < 2 || after >= boundary {
				t.Fatalf("128-page slice did not cross pointer map %d: %+v", boundary, r)
			}
			for i := 0; i < 20 && free != 0; i++ {
				r, after, free = maintain(128)
			}
			if free != 0 {
				t.Fatal("bounded slices did not finish reclaim")
			}
			if expireSnapshot && !expired {
				t.Fatal("committed snapshot deadline was not exercised")
			}
		})
	}
}
