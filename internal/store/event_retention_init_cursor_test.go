package store_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/XertroV/vecnet/internal/eventlog"
	"github.com/XertroV/vecnet/internal/store"
)

func TestRetentionInitializationPreservesDiscoverableOwnerCursorFloor(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "hub.db")
	db, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	// A deleted event raises this enrolled owner's floor above the captured
	// legacy floor. The startup checkpoint has not reached this owner yet.
	_, err = db.Exec(`INSERT INTO principals(prn_id,name,created_at) VALUES('prn_synthetic','synthetic','2026-01-01T00:00:00.000Z');
 INSERT INTO peers(peer_id,prn_id,name,kind,enrolled_at) VALUES('peer_synthetic','prn_synthetic','synthetic','session','2026-01-01T00:00:00.000Z');
 INSERT INTO events(seq,owner,type,at,data) VALUES(11,'peer_synthetic','presence','2026-01-01T00:00:00.000Z','{}');
 DELETE FROM events WHERE owner='peer_synthetic';
 UPDATE meta SET value='7' WHERE key='event_retention_legacy_floor';
 UPDATE meta SET value='' WHERE key='event_retention_init_owner';
 UPDATE meta SET value='0' WHERE key='event_retention_init_done';`)
	closeErr := db.Close()
	if err != nil || closeErr != nil {
		t.Fatal("prepare interrupted initialization", err, closeErr)
	}
	db, err = store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	log, err := eventlog.New(db)
	if err != nil {
		t.Fatal(err)
	}
	owners := []string{"peer_synthetic"}
	// Check the public read boundary first, so removing the max upsert guard
	// demonstrates the actual unsafe acceptance of a cursor below deleted 11.
	_, err = log.Read(ctx, owners, eventlog.Cursor{Epoch: 1, Seq: 10}, 1)
	var purged eventlog.ErrCursorPurged
	if !errors.As(err, &purged) || purged.Oldest != (eventlog.Cursor{Epoch: 1, Seq: 11}) {
		t.Fatalf("cursor below resumed floor: err=%v oldest=%v", err, purged.Oldest)
	}
	floor, err := log.OldestFor(ctx, owners)
	if err != nil || floor != (eventlog.Cursor{Epoch: 1, Seq: 11}) {
		t.Fatalf("resumed owner floor=%v err=%v", floor, err)
	}
	if _, err := log.Read(ctx, owners, floor, 1); err != nil {
		t.Fatalf("cursor at resumed floor rejected: %v", err)
	}
}
