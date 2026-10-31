package eventlog_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/eventlog"
	"github.com/XertroV/vecnet/internal/store"
)

const (
	peerA = "peer_01HZZZZZZZZZZZZZZZZZZZZZZA"
	peerB = "peer_01HZZZZZZZZZZZZZZZZZZZZZZB"
)

func openLog(t *testing.T) (*eventlog.Log, *store.DB) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "hub.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	l, err := eventlog.New(db)
	if err != nil {
		t.Fatalf("eventlog.New: %v", err)
	}
	return l, db
}

func appendOne(t *testing.T, l *eventlog.Log, db *store.DB, owner string, n int) eventlog.Cursor {
	t.Helper()
	ctx := context.Background()
	var c eventlog.Cursor
	at := time.Date(2026, 11, 1, 7, 0, n, 5_000_000, time.UTC)
	err := db.Tx(ctx, func(tx *sql.Tx) error {
		var err error
		c, err = l.Append(ctx, tx, owner, "delivery", at, map[string]any{"n": n, "owner": owner})
		return err
	})
	if err != nil {
		t.Fatalf("Append(%s, %d): %v", owner, n, err)
	}
	l.Notify(owner)
	return c
}

func dataN(t *testing.T, raw json.RawMessage) int {
	t.Helper()
	var d struct{ N int }
	if err := json.Unmarshal(raw, &d); err != nil {
		t.Fatalf("unmarshal data %s: %v", raw, err)
	}
	return d.N
}

func cursorOf(t *testing.T, s string) eventlog.Cursor {
	t.Helper()
	c, err := eventlog.ParseCursor(s)
	if err != nil {
		t.Fatalf("ParseCursor(%q): %v", s, err)
	}
	return c
}

func TestAppendReadOrdered(t *testing.T) {
	l, db := openLog(t)
	ctx := context.Background()

	// Interleave owners so a multi-owner read has to merge by the global seq.
	var cA []eventlog.Cursor
	cA = append(cA, appendOne(t, l, db, peerA, 1))
	appendOne(t, l, db, peerB, 2)
	cA = append(cA, appendOne(t, l, db, peerA, 3))
	appendOne(t, l, db, peerB, 4)
	cA = append(cA, appendOne(t, l, db, peerA, 5))

	zero := eventlog.Cursor{Epoch: 1, Seq: 0}

	evA, err := l.Read(ctx, []string{peerA}, zero, 100)
	if err != nil {
		t.Fatalf("Read([A]): %v", err)
	}
	if len(evA) != 3 {
		t.Fatalf("Read([A]) returned %d events, want 3", len(evA))
	}
	wantN := []int{1, 3, 5}
	var prev eventlog.Cursor
	for i, ev := range evA {
		if got := dataN(t, ev.Data); got != wantN[i] {
			t.Errorf("Read([A])[%d].data.n = %d, want %d", i, got, wantN[i])
		}
		c := cursorOf(t, ev.Cursor)
		if c != cA[i] {
			t.Errorf("Read([A])[%d].cursor = %v, want Append's %v", i, c, cA[i])
		}
		if c.Epoch != 1 {
			t.Errorf("cursor %q epoch = %d, want 1 (meta.epoch)", ev.Cursor, c.Epoch)
		}
		if i > 0 && c.Seq <= prev.Seq {
			t.Errorf("cursors not increasing: %v then %v", prev, c)
		}
		prev = c
		if ev.Type != "delivery" {
			t.Errorf("type = %q, want delivery", ev.Type)
		}
		if want := time.Date(2026, 11, 1, 7, 0, wantN[i], 5_000_000, time.UTC).Format("2006-01-02T15:04:05.000Z"); ev.At != want {
			t.Errorf("at = %q, want %q", ev.At, want)
		}
	}

	evAB, err := l.Read(ctx, []string{peerA, peerB}, zero, 100)
	if err != nil {
		t.Fatalf("Read([A,B]): %v", err)
	}
	if len(evAB) != 5 {
		t.Fatalf("Read([A,B]) returned %d events, want 5", len(evAB))
	}
	for i, ev := range evAB {
		if got := dataN(t, ev.Data); got != i+1 {
			t.Errorf("Read([A,B])[%d].data.n = %d, want %d (global seq order)", i, got, i+1)
		}
	}

	after, err := l.Read(ctx, []string{peerA}, cA[1], 100)
	if err != nil {
		t.Fatalf("Read([A], after=2nd): %v", err)
	}
	if len(after) != 1 || dataN(t, after[0].Data) != 5 {
		t.Fatalf("Read([A], after=2nd) = %+v, want exactly the 3rd A event", after)
	}

	limited, err := l.Read(ctx, []string{peerA, peerB}, zero, 2)
	if err != nil {
		t.Fatalf("Read limit 2: %v", err)
	}
	if len(limited) != 2 || dataN(t, limited[0].Data) != 1 || dataN(t, limited[1].Data) != 2 {
		t.Fatalf("Read limit 2 = %+v, want the first two events", limited)
	}

	cur, err := l.Current(ctx, []string{peerA})
	if err != nil {
		t.Fatalf("Current: %v", err)
	}
	if cur != cA[2] {
		t.Errorf("Current([A]) = %v, want %v", cur, cA[2])
	}
	head, err := l.Read(ctx, []string{peerA, peerB}, eventlog.Head, 100)
	if err != nil {
		t.Fatalf("Read(Head): %v", err)
	}
	if len(head) != 0 {
		t.Errorf("Read(Head) returned %d events, want 0", len(head))
	}
}

func TestCursorFormat(t *testing.T) {
	c, err := eventlog.ParseCursor("e1.10423")
	if err != nil {
		t.Fatalf("ParseCursor(e1.10423): %v", err)
	}
	if c != (eventlog.Cursor{Epoch: 1, Seq: 10423}) {
		t.Fatalf("ParseCursor(e1.10423) = %+v", c)
	}
	if s := c.String(); s != "e1.10423" {
		t.Fatalf("String() = %q, want e1.10423", s)
	}
	for _, bad := range []string{
		"x1.2", "e1", "e1.2.3", "", "e", "e.2", "e1.", "e-1.2", "e1.-2", "e+1.2",
		" e1.2", "e1.2 ", "E1.2", "e1,2", "e99999999999999999999.1", "e1.99999999999999999999", "head",
	} {
		if got, err := eventlog.ParseCursor(bad); err == nil {
			t.Errorf("ParseCursor(%q) = %+v, want error", bad, got)
		}
	}
}

func TestWrongEpochIsPurged(t *testing.T) {
	l, db := openLog(t)
	ctx := context.Background()

	// Empty log: the oldest valid cursor is the current one.
	_, err := l.Read(ctx, []string{peerA}, eventlog.Cursor{Epoch: 7}, 100)
	var purged eventlog.ErrCursorPurged
	if !errors.As(err, &purged) {
		t.Fatalf("Read(epoch 7) err = %v, want ErrCursorPurged", err)
	}
	cur, err := l.Current(ctx, []string{peerA})
	if err != nil {
		t.Fatalf("Current: %v", err)
	}
	if purged.Oldest != cur {
		t.Fatalf("Oldest = %v, want Current %v", purged.Oldest, cur)
	}

	// With events retained the error still names a cursor in the live epoch
	// (PROTOCOL §2.3: the oldest valid cursor), and reading from it succeeds.
	for i := 1; i <= 3; i++ {
		appendOne(t, l, db, peerA, i)
	}
	_, err = l.Read(ctx, []string{peerA}, eventlog.Cursor{Epoch: 7, Seq: 2}, 100)
	if !errors.As(err, &purged) {
		t.Fatalf("Read(epoch 7, seq 2) err = %v, want ErrCursorPurged", err)
	}
	if purged.Oldest != (eventlog.Cursor{Epoch: 1, Seq: 0}) {
		t.Fatalf("Oldest = %v, want e1.0", purged.Oldest)
	}
	evs, err := l.Read(ctx, []string{peerA}, purged.Oldest, 100)
	if err != nil || len(evs) != 3 {
		t.Fatalf("Read(Oldest) = %d events, %v; want 3, nil", len(evs), err)
	}

	// Below retention: drop seq 1..2 as a retention sweep would.
	if _, err := db.ExecContext(ctx, `DELETE FROM events WHERE seq <= 2`); err != nil {
		t.Fatalf("delete: %v", err)
	}
	_, err = l.Read(ctx, []string{peerA}, eventlog.Cursor{Epoch: 1, Seq: 0}, 100)
	if !errors.As(err, &purged) {
		t.Fatalf("Read(e1.0) after retention err = %v, want ErrCursorPurged", err)
	}
	if purged.Oldest != (eventlog.Cursor{Epoch: 1, Seq: 2}) {
		t.Fatalf("Oldest = %v, want e1.2", purged.Oldest)
	}
	evs, err = l.Read(ctx, []string{peerA}, eventlog.Cursor{Epoch: 1, Seq: 2}, 100)
	if err != nil || len(evs) != 1 {
		t.Fatalf("Read(e1.2) = %d events, %v; want 1, nil", len(evs), err)
	}

	// Everything purged: the cursor at the old high-water mark stays valid.
	if _, err := db.ExecContext(ctx, `DELETE FROM events`); err != nil {
		t.Fatalf("delete all: %v", err)
	}
	if _, err := l.Read(ctx, []string{peerA}, eventlog.Cursor{Epoch: 1, Seq: 3}, 100); err != nil {
		t.Fatalf("Read(e1.3) on emptied log: %v", err)
	}
	_, err = l.Read(ctx, []string{peerA}, eventlog.Cursor{Epoch: 1, Seq: 1}, 100)
	if !errors.As(err, &purged) || purged.Oldest != (eventlog.Cursor{Epoch: 1, Seq: 3}) {
		t.Fatalf("Read(e1.1) on emptied log err = %v, want ErrCursorPurged{e1.3}", err)
	}
}

func isClosed(ch <-chan struct{}) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}

func TestWaitWakesOnNotify(t *testing.T) {
	l, _ := openLog(t)
	ctx := context.Background()

	ch := l.Wait(ctx, []string{peerA})
	if isClosed(ch) {
		t.Fatal("Wait channel closed before any Notify")
	}
	l.Notify(peerB)
	if isClosed(ch) {
		t.Fatal("Wait([A]) closed by Notify(B)")
	}
	l.Notify(peerA)
	if !isClosed(ch) {
		t.Fatal("Wait([A]) not closed after Notify(A)")
	}
	// A second Notify on an already-woken waiter must not panic or block.
	l.Notify(peerA)

	multi := l.Wait(ctx, []string{peerA, peerB})
	l.Notify(peerB)
	if !isClosed(multi) {
		t.Fatal("Wait([A,B]) not closed after Notify(B)")
	}

	cctx, cancel := context.WithCancel(ctx)
	cc := l.Wait(cctx, []string{peerA})
	if isClosed(cc) {
		t.Fatal("Wait closed before ctx cancel")
	}
	cancel()
	select {
	case <-cc:
	case <-time.After(5 * time.Second):
		t.Fatal("Wait not closed after ctx cancel")
	}

	done, cancel2 := context.WithCancel(ctx)
	cancel2()
	select {
	case <-l.Wait(done, []string{peerA}):
	case <-time.After(5 * time.Second):
		t.Fatal("Wait on an already-done ctx never closed")
	}

	// Notify never blocks, even with many waiters nobody is reading.
	finished := make(chan struct{})
	go func() {
		for i := 0; i < 100; i++ {
			l.Wait(ctx, []string{peerA})
		}
		l.Notify(peerA, peerB)
		l.Notify("peer_nobody")
		l.Notify()
		close(finished)
	}()
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("Notify blocked")
	}
}

func TestAckMonotonic(t *testing.T) {
	l, db := openLog(t)
	ctx := context.Background()

	if _, ok, err := l.Acked(ctx, peerA, "*"); err != nil || ok {
		t.Fatalf("Acked before any ack = ok %v, err %v; want false, nil", ok, err)
	}
	ack := func(owner, scope string, c eventlog.Cursor) error {
		return db.Tx(ctx, func(tx *sql.Tx) error { return l.Ack(ctx, tx, owner, scope, c) })
	}
	if err := ack(peerA, "*", eventlog.Cursor{Epoch: 1, Seq: 5}); err != nil {
		t.Fatalf("Ack 5: %v", err)
	}
	if err := ack(peerA, "*", eventlog.Cursor{Epoch: 1, Seq: 3}); err != nil {
		t.Fatalf("Ack 3: %v", err)
	}
	c, ok, err := l.Acked(ctx, peerA, "*")
	if err != nil || !ok {
		t.Fatalf("Acked = ok %v, err %v", ok, err)
	}
	if c != (eventlog.Cursor{Epoch: 1, Seq: 5}) {
		t.Fatalf("Acked = %v, want e1.5", c)
	}

	if err := ack(peerA, "*", eventlog.Cursor{Epoch: 1, Seq: 9}); err != nil {
		t.Fatalf("Ack 9: %v", err)
	}
	if c, _, _ := l.Acked(ctx, peerA, "*"); c.Seq != 9 {
		t.Fatalf("Acked after forward ack = %v, want e1.9", c)
	}

	// Scopes are independent: a narrowed stream has its own cursor.
	if _, ok, _ := l.Acked(ctx, peerA, peerB); ok {
		t.Fatal("Acked(A, scope B) present before any ack in that scope")
	}

	// A cursor from another epoch is rejected and changes nothing.
	if err := ack(peerA, "*", eventlog.Cursor{Epoch: 7, Seq: 50}); err == nil {
		t.Fatal("Ack with wrong epoch = nil, want error")
	}
	if c, _, _ := l.Acked(ctx, peerA, "*"); c.Seq != 9 {
		t.Fatalf("Acked after wrong-epoch ack = %v, want e1.9", c)
	}
}
