package eventlog_test

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"testing"

	"github.com/XertroV/vecnet/internal/eventlog"
)

func TestOwnerRetentionInterleavedFloors(t *testing.T) {
	l, db := openLog(t)
	ctx := context.Background()
	a := appendOne(t, l, db, peerA, 1)
	appendOne(t, l, db, peerB, 2)
	c := appendOne(t, l, db, peerA, 3)
	appendOne(t, l, db, peerB, 4)
	before, err := l.Read(ctx, []string{peerB}, eventlog.Cursor{Epoch: 1}, 100)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`DELETE FROM events WHERE owner=?`, peerA); err != nil {
		t.Fatal(err)
	}
	for _, owners := range [][]string{{peerA}, {peerA, peerB}} {
		floor, err := l.OldestFor(ctx, owners)
		if err != nil || floor != c {
			t.Fatalf("floor=%v err=%v want %v", floor, err, c)
		}
		for _, cur := range []eventlog.Cursor{a, {Epoch: 9}} {
			_, err = l.Read(ctx, owners, cur, 100)
			var purged eventlog.ErrCursorPurged
			if !errors.As(err, &purged) || purged.Oldest != c {
				t.Fatalf("read after gap: %v", err)
			}
		}
		if _, err = l.Read(ctx, owners, c, 100); err != nil {
			t.Fatal(err)
		}
	}
	after, err := l.Read(ctx, []string{peerB}, eventlog.Cursor{Epoch: 1}, 100)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("narrowed replay changed")
	}
	for _, owners := range [][]string{nil, {"new-owner"}} {
		floor, err := l.OldestFor(ctx, owners)
		if err != nil || floor.Seq != 0 {
			t.Fatalf("empty floor %v %v", floor, err)
		}
	}
	if cur, err := l.Current(ctx, []string{peerA}); err != nil || cur != c {
		t.Fatalf("empty owner current %v %v", cur, err)
	}
	if evs, err := l.Read(ctx, []string{peerA}, eventlog.Head, 1); err != nil || len(evs) != 0 {
		t.Fatal("head must tail")
	}
	again, err := eventlog.New(db)
	if err != nil {
		t.Fatal(err)
	}
	floor, err := again.OldestFor(ctx, []string{peerA})
	if err != nil || floor != c {
		t.Fatal("restart lost floor")
	}
	next := appendOne(t, again, db, peerA, 5)
	if next.Seq <= c.Seq {
		t.Fatal("reused sequence")
	}
}

func TestRetentionReadSnapshotSurvivesConcurrentPurge(t *testing.T) {
	l, db := openLog(t)
	ctx := context.Background()
	c := appendOne(t, l, db, peerA, 1)
	purgedSnapshot := make(chan struct{})
	ready, done := make(chan struct{}), make(chan error, 1)
	go func() {
		done <- db.ReadSnapshot(ctx, func(q *sql.Conn) error {
			var n int
			if err := q.QueryRowContext(ctx, `SELECT count(*) FROM events`).Scan(&n); err != nil {
				return err
			}
			close(ready)
			// The other writer commits while this reader pins the WAL snapshot.
			<-purgedSnapshot
			var floor int64
			if err := q.QueryRowContext(ctx, `SELECT coalesce(max(purged_through_seq),0) FROM event_retention`).Scan(&floor); err != nil {
				return err
			}
			if floor != 0 {
				return errors.New("snapshot floor changed")
			}
			if err := q.QueryRowContext(ctx, `SELECT count(*) FROM events`).Scan(&n); err != nil {
				return err
			}
			if n != 1 {
				return errors.New("snapshot rows changed")
			}
			return nil
		})
	}()
	<-ready
	if _, err := db.Exec(`DELETE FROM events WHERE owner=?`, peerA); err != nil {
		t.Fatal(err)
	}
	close(purgedSnapshot)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if _, err := l.Read(ctx, []string{peerA}, eventlog.Cursor{Epoch: 1}, 1); err == nil {
		t.Fatal("post-purge read silently skipped")
	}
	floor, err := l.OldestFor(ctx, []string{peerA})
	if err != nil || floor != c {
		t.Fatal("missing committed floor")
	}
}
