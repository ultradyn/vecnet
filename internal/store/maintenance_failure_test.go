package store_test

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/store"
)

// Wrap the real SQLite driver so a cancellation can be injected precisely
// after BEGIN takes effect, including the driver's ambiguous error outcome.
// All statements still execute against the private on-disk database.
type maintenanceConnector struct {
	driver driver.Driver
	dsn    string
	before func(context.Context, string) error
	after  func(context.Context, string) error
}

func (c maintenanceConnector) Driver() driver.Driver { return c.driver }

func (c maintenanceConnector) Connect(context.Context) (driver.Conn, error) {
	conn, err := c.driver.Open(c.dsn)
	if err != nil {
		return nil, err
	}
	return &maintenanceConn{
		Conn: conn, ExecerContext: conn.(driver.ExecerContext),
		QueryerContext: conn.(driver.QueryerContext), ConnBeginTx: conn.(driver.ConnBeginTx),
		before: c.before, after: c.after,
	}, nil
}

type maintenanceConn struct {
	driver.Conn
	driver.ExecerContext
	driver.QueryerContext
	driver.ConnBeginTx
	before func(context.Context, string) error
	after  func(context.Context, string) error
}

func (c *maintenanceConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if c.before != nil {
		if err := c.before(ctx, query); err != nil {
			return nil, err
		}
	}
	result, err := c.ExecerContext.ExecContext(ctx, query, args)
	if err == nil && c.after != nil {
		err = c.after(ctx, query)
	}
	return result, err
}

// maintenanceUntimed is a cooperative slice budget for tests whose subject is
// page accounting or fault handling, not the time budget. Each slice still
// ends after its bounded page work; this only stops wall-clock pressure on a
// slow runner from pausing the slice before the behaviour under test.
const maintenanceUntimed = time.Minute

func TestCompactionRollbackDoesNotPublishUncommittedStats(t *testing.T) {
	for _, failure := range []string{"capacity_probe", "commit"} {
		t.Run(failure, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "hub.db")
			independent, err := store.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer independent.Close()
			free := freedPages(t, independent)
			pages := pragmaInt(t, independent, "page_count")
			vacuumSteps, probes := 0, 0
			injected := false
			pool := sql.OpenDB(maintenanceConnector{
				driver: independent.Driver(),
				dsn:    "file:" + path + "?_txlock=immediate&_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)",
				before: func(_ context.Context, query string) error {
					if failure == "commit" && query == "COMMIT" {
						injected = true
						return errors.New("injected commit failure")
					}
					return nil
				},
				after: func(_ context.Context, query string) error {
					if query == "PRAGMA incremental_vacuum(1)" {
						vacuumSteps++
					}
					return nil
				},
			})
			defer pool.Close()
			pool.SetMaxOpenConns(1)
			db := &store.DB{DB: pool}
			// The injection points are counted statements, not elapsed time. A
			// one-second cooperative slice expired first on a loaded single-core
			// runner (PausedReason=time_budget, no COMMIT reached), so give the
			// slice a budget it cannot exhaust and let only the injection fail it.
			result, err := db.Maintain(context.Background(), store.MaintenanceOptions{
				ReclaimPages: 3, Target: maintenanceUntimed,
				AvailableBytes: func(context.Context) (int64, error) {
					probes++
					if failure == "capacity_probe" && probes == 4 {
						injected = true
						return 0, errors.New("injected capacity probe failure")
					}
					return 1 << 40, nil
				},
			})
			if result.PausedReason == "time_budget" {
				t.Fatalf("slice expired before the injected failure: %+v", result)
			}
			if !injected || vacuumSteps < 2 || err == nil {
				t.Fatalf("failure missed in-flight reclaim: injected=%v vacuumSteps=%d result=%+v err=%v", injected, vacuumSteps, result, err)
			}
			if result.Before.PageCount != pages || result.Before.FreelistCount != free || result.ReclaimedPages != 0 {
				t.Fatalf("rolled-back reclaim reported progress: %+v", result)
			}
			for _, reader := range []*store.DB{independent, db} {
				if got := pragmaInt(t, reader, "page_count"); got != pages {
					t.Errorf("rollback page_count=%d want %d", got, pages)
				}
				if got := pragmaInt(t, reader, "freelist_count"); got != free {
					t.Errorf("rollback freelist_count=%d want %d", got, free)
				}
			}
			if result.After != (store.StorageStats{}) {
				t.Fatalf("rolled-back After must be unavailable: %+v", result.After)
			}
		})
	}
}

func TestCompactionCancellationAtBeginCompletionReleasesWriter(t *testing.T) {
	for _, ambiguous := range []bool{true, false} {
		name := "successful_begin"
		if ambiguous {
			name = "ambiguous_begin_error"
		}
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "hub.db")
			independent, err := store.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer independent.Close()
			freedPages(t, independent)

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			began := false
			pool := sql.OpenDB(maintenanceConnector{
				driver: independent.Driver(),
				dsn:    "file:" + path + "?_txlock=immediate&_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)",
				after: func(execCtx context.Context, query string) error {
					if query == "BEGIN IMMEDIATE" {
						began = true
						cancel()
						if ambiguous {
							return execCtx.Err()
						}
					}
					return nil
				},
			})
			defer pool.Close()
			pool.SetMaxOpenConns(1)
			db := &store.DB{DB: pool}
			// The injection is BEGIN itself; the slice timer must not win first.
			result, err := db.Maintain(ctx, store.MaintenanceOptions{ReclaimPages: 2, Target: maintenanceUntimed})
			if !began || ctx.Err() == nil || err == nil || result.ReclaimedPages != 0 {
				t.Fatalf("cancellation missed BEGIN completion: began=%v result=%+v err=%v", began, result, err)
			}

			// An independent connection detects an orphaned writer lock; the
			// original pool detects a connection returned with a transaction open.
			for _, writer := range []*store.DB{independent, db} {
				writeCtx, stop := context.WithTimeout(context.Background(), time.Second)
				err := writer.Tx(writeCtx, func(tx *sql.Tx) error {
					_, err := tx.ExecContext(writeCtx, "INSERT INTO reclaim_fixture(body) VALUES ('after-cancel')")
					return err
				})
				stop()
				if err != nil {
					t.Fatalf("write after canceled BEGIN: %v", err)
				}
			}
			var writes int
			if err := independent.QueryRow("SELECT count(*) FROM reclaim_fixture").Scan(&writes); err != nil || writes != 2 {
				t.Fatalf("committed writes=%d want 2: %v", writes, err)
			}
		})
	}
}
