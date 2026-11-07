package store_test

import (
	"context"
	"database/sql"
	"database/sql/driver"
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
		after: c.after,
	}, nil
}

type maintenanceConn struct {
	driver.Conn
	driver.ExecerContext
	driver.QueryerContext
	driver.ConnBeginTx
	after func(context.Context, string) error
}

func (c *maintenanceConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	result, err := c.ExecerContext.ExecContext(ctx, query, args)
	if err == nil {
		err = c.after(ctx, query)
	}
	return result, err
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
			result, err := db.Maintain(ctx, store.MaintenanceOptions{ReclaimPages: 2, Target: time.Second})
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
