package store

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"math"
	"os"
	"path/filepath"
	"time"
)

// MaintenanceOptions bounds a cooperative slice. ReclaimPages=0 performs only
// a PASSIVE checkpoint. Reclaim is independently opt-in and capped at 128 pages.
// Target=0 uses 50ms, including connection acquisition and checkpoint time.
// AvailableBytes overrides only the filesystem probe, for disposable fixtures.
// It must report available bytes excluding reserved blocks.
type MaintenanceOptions struct {
	ReclaimPages   int
	Target         time.Duration
	MinFreeBytes   int64
	AvailableBytes func(context.Context) (int64, error)
}

func ValidateMaintenanceOptions(o MaintenanceOptions) error {
	if o.ReclaimPages < 0 || o.ReclaimPages > 128 || o.Target < 0 || o.MinFreeBytes < 0 {
		return errors.New("maintenance: invalid_limits")
	}
	return nil
}

// StorageStats deliberately contains no path, SQL, content, URL or credential.
// LiveBytes is allocated pages minus freelist, not a claim that the file shrank.
type StorageStats struct {
	PageSize          int64 `json:"page_size"`
	PageCount         int64 `json:"page_count"`
	FreelistCount     int64 `json:"freelist_count"`
	AutoVacuum        int64 `json:"auto_vacuum"`
	Synchronous       int64 `json:"synchronous"`
	WALAutoCheckpoint int64 `json:"wal_autocheckpoint"`
	DBBytes           int64 `json:"db_bytes"`
	WALBytes          int64 `json:"wal_bytes"`
	LiveBytes         int64 `json:"live_bytes"`
	AvailableBytes    int64 `json:"available_bytes"`
}
type CheckpointProgress struct {
	Busy              int64 `json:"busy"`
	LogPages          int64 `json:"log_pages"`
	CheckpointedPages int64 `json:"checkpointed_pages"`
}
type MaintenanceResult struct {
	Before StorageStats `json:"before"`
	// After is zero if reclaim was attempted without a complete committed sample.
	After          StorageStats       `json:"after"`
	Checkpoint     CheckpointProgress `json:"checkpoint"`
	ReclaimedPages int64              `json:"reclaimed_pages"`
	ElapsedNanos   int64              `json:"elapsed_nanos"`
	PausedReason   string             `json:"paused_reason"`
}

func maintenanceStats(ctx context.Context, c *sql.Conn, path string, o MaintenanceOptions) (s StorageStats, err error) {
	for _, p := range []struct {
		name string
		dst  *int64
	}{
		{"page_size", &s.PageSize}, {"page_count", &s.PageCount}, {"freelist_count", &s.FreelistCount},
		{"auto_vacuum", &s.AutoVacuum}, {"synchronous", &s.Synchronous}, {"wal_autocheckpoint", &s.WALAutoCheckpoint},
	} {
		if err = c.QueryRowContext(ctx, "PRAGMA "+p.name).Scan(p.dst); err != nil {
			return s, err
		}
	}
	s.LiveBytes = (s.PageCount - s.FreelistCount) * s.PageSize
	if info, e := os.Stat(path); e == nil {
		s.DBBytes = info.Size()
	} else if !os.IsNotExist(e) {
		return s, errors.New("maintenance: db_probe")
	}
	if info, e := os.Stat(path + "-wal"); e == nil {
		s.WALBytes = info.Size()
	} else if !os.IsNotExist(e) {
		return s, errors.New("maintenance: wal_probe")
	}
	if o.AvailableBytes != nil {
		s.AvailableBytes, err = o.AvailableBytes(ctx)
	} else {
		s.AvailableBytes, err = availableDiskBytes(filepath.Dir(path))
	}
	if err != nil {
		return s, errors.New("maintenance: capacity_probe")
	}
	return s, nil
}
func lowHeadroom(s StorageStats, o MaintenanceOptions) bool {
	reserve := max(int64(64<<20), o.MinFreeBytes)
	return s.WALBytes > math.MaxInt64-reserve || s.AvailableBytes <= reserve+s.WALBytes
}

// Maintain never runs a full VACUUM, converts NONE, evicts readers or requests
// RESTART/TRUNCATE. A pinned reader or writer yields immediately. Vacuum runs
// one page at a time, checking capacity and time between statements. SQLite's
// statement execution itself is not a hard real-time latency guarantee.
func (db *DB) Maintain(ctx context.Context, o MaintenanceOptions) (r MaintenanceResult, err error) {
	if err = ValidateMaintenanceOptions(o); err != nil {
		return r, err
	}
	if o.Target == 0 {
		o.Target = 50 * time.Millisecond
	}
	start := time.Now()
	defer func() { r.ElapsedNanos = time.Since(start).Nanoseconds() }()
	slice, cancel := context.WithTimeout(ctx, o.Target)
	defer cancel()
	c, err := db.Conn(slice)
	if err != nil {
		return r, err
	}
	defer c.Close()
	// busy_timeout is connection-local. Always restore it before pool return,
	// even when the caller's context was canceled.
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), time.Second)
		defer stop()
		if _, e := c.ExecContext(cleanup, "PRAGMA busy_timeout=5000"); e != nil {
			_ = c.Raw(func(any) error { return driver.ErrBadConn })
		}
	}()
	if _, err = c.ExecContext(slice, "PRAGMA busy_timeout=0"); err != nil {
		return r, err
	}
	var seq int
	var name, path string
	if err = c.QueryRowContext(slice, "PRAGMA database_list").Scan(&seq, &name, &path); err != nil {
		return r, errors.New("maintenance: database_probe")
	}
	if path == "" {
		return r, errors.New("maintenance: on_disk_required")
	}
	r.Before, err = maintenanceStats(slice, c, path, o)
	if err != nil {
		return r, err
	}
	r.After = r.Before
	defer func() {
		if err != nil {
			var coded interface{ Code() int }
			switch {
			case errors.As(err, &coded) && coded.Code()&255 == 13:
				r.PausedReason = "sqlite_full"
				err = nil
			case errors.As(err, &coded) && (coded.Code()&255 == 5 || coded.Code()&255 == 6):
				r.PausedReason = "lock_pressure"
				err = nil
			case ctx.Err() == nil && slice.Err() != nil:
				r.PausedReason = "time_budget"
				err = nil
			default:
				err = errors.New("maintenance: slice_failed")
			}
		}
	}()
	if lowHeadroom(r.Before, o) {
		r.PausedReason = "low_headroom"
		return r, nil
	}
	checkpoint := func() error {
		return c.QueryRowContext(slice, "PRAGMA wal_checkpoint(PASSIVE)").Scan(&r.Checkpoint.Busy, &r.Checkpoint.LogPages, &r.Checkpoint.CheckpointedPages)
	}
	if err = checkpoint(); err != nil {
		return r, err
	}
	if r.Checkpoint.Busy != 0 || r.Checkpoint.LogPages > r.Checkpoint.CheckpointedPages {
		r.PausedReason = "reader_pinned"
		return r, nil
	}
	if r.Before.AutoVacuum == 2 && o.ReclaimPages > 0 {
		// Transactional probes are only for budgeting. Publish After once
		// reclaim commits and a complete post-transaction sample is available.
		r.After = StorageStats{}
		// Pin the writer lock across page accounting and reclaim. Concurrent
		// sends cannot grow the file between the budget check and vacuum.
		if _, err = c.ExecContext(slice, "BEGIN IMMEDIATE"); err != nil {
			// Cancellation can be reported after SQLite acquired the writer lock.
			// A failed BEGIN is ambiguous: never return this connection to the pool.
			_ = c.Raw(func(any) error { return driver.ErrBadConn })
			return r, err
		}
		active := true
		defer func() {
			if active {
				cleanup, stop := context.WithTimeout(context.Background(), time.Second)
				defer stop()
				if _, e := c.ExecContext(cleanup, "ROLLBACK"); e != nil {
					_ = c.Raw(func(any) error { return driver.ErrBadConn })
				}
			}
		}()
		var vacuumStart int64
		if err = c.QueryRowContext(slice, "PRAGMA page_count").Scan(&vacuumStart); err != nil {
			return r, err
		}
		reserve, e := sqliteReservedBytes(path)
		if e != nil {
			return r, e
		}
		for i := 0; i < o.ReclaimPages; i++ {
			if time.Since(start) >= o.Target {
				r.PausedReason = "time_budget"
				break
			}
			var s StorageStats
			s, err = maintenanceStats(slice, c, path, o)
			if err != nil {
				return r, err
			}
			if lowHeadroom(s, o) {
				r.PausedReason = "low_headroom"
				break
			}
			if s.FreelistCount == 0 {
				break
			}
			// Removing the last page may also remove the pointer-map page just
			// below it (e.g. page_count 823 -> 821 at 4096 bytes). Budget both
			// physical pages. SQLite files created by this driver reserve zero
			// bytes per page; query the header for existing reserved-byte files.
			mapStride := (s.PageSize-reserve)/5 + 1
			// The locking byte also reserves a page at the 1 GiB boundary.
			// SQLite skips it while truncating, just like a pointer-map page.
			pendingPage := int64(0x40000000)/s.PageSize + 1
			nextLast := s.PageCount - 1
			for nextLast > 1 {
				pointerMap := (nextLast-2)%mapStride == 0 && nextLast != pendingPage
				// A pointer map coinciding with the locking page shifts by one.
				pointerMap = pointerMap || nextLast == pendingPage+1 && (pendingPage-2)%mapStride == 0
				if nextLast != pendingPage && !pointerMap {
					break
				}
				nextLast--
			}
			nextPages := s.PageCount - nextLast
			if vacuumStart-s.PageCount+nextPages > int64(o.ReclaimPages) {
				break
			}
			if _, err = c.ExecContext(slice, "PRAGMA incremental_vacuum(1)"); err != nil {
				return r, err
			}
		}
		var vacuumEnd int64
		if err = c.QueryRowContext(slice, "PRAGMA page_count").Scan(&vacuumEnd); err != nil {
			return r, err
		}
		if _, err = c.ExecContext(slice, "COMMIT"); err != nil {
			return r, err
		}
		active = false
		r.ReclaimedPages = max(int64(0), vacuumStart-vacuumEnd)
	}
	if err = checkpoint(); err != nil {
		return r, err
	}
	var after StorageStats
	after, err = maintenanceStats(slice, c, path, o)
	if err == nil {
		r.After = after
	}
	return r, err
}

// The SQLite header's reserved-byte count defines pointer-map page spacing.
// Read only this one header byte, never any operational payload.
func sqliteReservedBytes(path string) (int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, errors.New("maintenance: header_probe")
	}
	defer f.Close()
	var b [1]byte
	if _, err = f.ReadAt(b[:], 20); err != nil {
		return 0, errors.New("maintenance: header_probe")
	}
	return int64(b[0]), nil
}
