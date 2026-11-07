package eventlog_test

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

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
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c := appendOne(t, l, db, peerA, 1)
	purgedSnapshot := make(chan struct{})
	ready, done := make(chan struct{}), make(chan error, 1)
	finished := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(purgedSnapshot) }) }
	defer func() {
		release()
		cancel()
		<-finished
	}()
	go func() {
		defer close(finished)
		done <- db.ReadSnapshot(ctx, func(q *sql.Conn) error {
			var n int
			if err := q.QueryRowContext(ctx, `SELECT count(*) FROM events`).Scan(&n); err != nil {
				return err
			}
			close(ready)
			// The other writer commits while this reader pins the WAL snapshot.
			select {
			case <-purgedSnapshot:
			case <-ctx.Done():
				return ctx.Err()
			}
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
	select {
	case <-ready:
	case err := <-done:
		t.Fatalf("reader failed before readiness: %v", err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM events WHERE owner=?`, peerA); err != nil {
		t.Fatal(err)
	}
	release()
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
