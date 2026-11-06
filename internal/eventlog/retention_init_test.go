package eventlog

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/store"
)

func TestRetentionFloorConsumersRejectIncompleteInitialization(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	l, err := New(db)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := db.Exec(`UPDATE meta SET value='0' WHERE key='event_retention_init_done'`); err != nil {
		t.Fatal(err)
	}
	check := func(err error) {
		t.Helper()
		if err == nil || !strings.Contains(err.Error(), "retention_initialization_pending") {
			t.Fatalf("incomplete floors accepted: %v", err)
		}
	}
	_, err = l.OldestFor(ctx, []string{"peer_synthetic"})
	check(err)
	_, err = l.Current(ctx, []string{"peer_synthetic"})
	check(err)
	_, err = l.Read(ctx, []string{"peer_synthetic"}, Cursor{Epoch: l.epoch}, 1)
	check(err)
	err = db.Tx(ctx, func(tx *sql.Tx) error {
		_, err := l.PrunePrefix(ctx, tx, "peer_synthetic", time.Now(), func(int64, time.Time) (time.Duration, bool) { return time.Hour, true }, 0, nil, RetentionLimits{Rows: 1})
		return err
	})
	check(err)
}
