package hub_test

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/eventlog"
	"github.com/XertroV/vecnet/internal/hub"
	"github.com/XertroV/vecnet/internal/prefs"
	"github.com/XertroV/vecnet/internal/testhub"
)

func TestRetentionFirstHardStartupRequiresApproval(t *testing.T) {
	h, _, clock := preferencesHubClock(t, hub.DefaultConfig())
	c := retentionEvent(t, h, "peer_upgrade", clock.Now().Add(-40*24*time.Hour))
	// Simulate upgrade from a binary without the consumer bootstrap checkpoint.
	if err := h.DB.SetMeta(context.Background(), "event_retention_initial_high_water", strconv.FormatInt(c.Seq, 10)); err != nil {
		t.Fatal(err)
	}
	if _, err := h.DB.Exec(`DELETE FROM meta WHERE key='event_retention_bootstrap_v1'`); err != nil {
		t.Fatal(err)
	}
	cfg := h.Cfg
	cfg.UnackedEventRetention = 30 * 24 * time.Hour
	restarted, err := hub.New(h.DB, cfg, clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	retentionRun(t, restarted)
	retentionFloor(t, restarted, "peer_upgrade", 0)
	// An ordinary source-only save carries no hard-class authorization.
	retentionConfirm(t, restarted, prefs.Patch{Set: map[string]prefs.Value{"retention.archive_mode": prefs.Bool(false)}})
	retentionRun(t, restarted)
	retentionFloor(t, restarted, "peer_upgrade", 0)
	retentionConfirm(t, restarted, prefs.Patch{Set: map[string]prefs.Value{"retention.archive_mode": prefs.Bool(false)}, ApplyExisting: true})
	retentionRun(t, restarted)
	retentionFloor(t, restarted, "peer_upgrade", 0)
	clock.Advance(24 * time.Hour)
	retentionRun(t, restarted)
	retentionFloor(t, restarted, "peer_upgrade", c.Seq)
}

func TestRetentionAckApprovalKeepsClassSpecificCutoff(t *testing.T) {
	cfg := hub.DefaultConfig()
	cfg.UnackedEventRetention = 30 * 24 * time.Hour
	h, _, clock := preferencesHubClock(t, cfg)
	retentionConfirm(t, h, prefs.Patch{Set: map[string]prefs.Value{"retention.ack_events": prefs.Duration(0)}})
	c := retentionEvent(t, h, "peer_ack_cutoff", clock.Now().Add(-60*24*time.Hour))
	retentionAck(t, h, "peer_ack_cutoff", "*", c)
	patch := prefs.Patch{Set: map[string]prefs.Value{"retention.ack_events": prefs.Duration(90 * 24 * 60 * 60)}, ApplyExisting: true}
	s := readPrefs(t, h)
	preview := previewPrefs(t, h.Preferences, prefsOperator, s, patch)
	// Hard expiry is still pending: only the acknowledged class is approved here.
	patch.Confirm = []string{preview.ConfirmToken}
	patchPrefs(t, h, s, patch)
	clock.Advance(31 * 24 * time.Hour)
	retentionRun(t, h)
	retentionFloor(t, h, "peer_ack_cutoff", 0)
	// A restart must retain the class-specific bound as well.
	restarted, err := hub.New(h.DB, h.Cfg, clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	retentionRun(t, restarted)
	retentionFloor(t, restarted, "peer_ack_cutoff", 0)
	// A fresh preview at age 91d can explicitly include the event.
	retentionConfirm(t, restarted, patchWithoutConfirmation(patch))
	clock.Advance(24 * time.Hour)
	retentionRun(t, restarted)
	retentionFloor(t, restarted, "peer_ack_cutoff", c.Seq)
}

func patchWithoutConfirmation(p prefs.Patch) prefs.Patch { p.Confirm = nil; return p }

// retentionProbe instruments the real SQLite driver only in these tests. It
// blocks actual SQL boundaries, without production hooks or alternate storage.
type retentionProbe struct {
	driver driver.Driver
	dsn    string
	begin  func(context.Context, driver.TxOptions) error
	row    func(context.Context, string) error
	query  func(context.Context, string, bool) error
}

func (p *retentionProbe) Driver() driver.Driver { return p.driver }
func (p *retentionProbe) Connect(ctx context.Context) (driver.Conn, error) {
	c, err := p.driver.Open(p.dsn)
	if err != nil {
		return nil, err
	}
	return &retentionProbeConn{Conn: c, p: p}, nil
}

type retentionProbeConn struct {
	driver.Conn
	p            *retentionProbe
	readSnapshot bool
}

func (c *retentionProbeConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	if c.p.begin != nil {
		if err := c.p.begin(ctx, opts); err != nil {
			return nil, err
		}
	}
	return c.Conn.(driver.ConnBeginTx).BeginTx(ctx, opts)
}
func (c *retentionProbeConn) ExecContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Result, error) {
	result, err := c.Conn.(driver.ExecerContext).ExecContext(ctx, q, args)
	if err == nil {
		if q == "BEGIN DEFERRED" {
			c.readSnapshot = true
		}
		if q == "ROLLBACK" {
			c.readSnapshot = false
		}
	}
	return result, err
}
func (c *retentionProbeConn) QueryContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	if c.p.query != nil {
		if err := c.p.query(ctx, q, c.readSnapshot); err != nil {
			return nil, err
		}
	}
	r, err := c.Conn.(driver.QueryerContext).QueryContext(ctx, q, args)
	if err != nil {
		return nil, err
	}
	return &retentionProbeRows{Rows: r, ctx: ctx, q: q, p: c.p}, nil
}

type retentionProbeRows struct {
	driver.Rows
	ctx   context.Context
	q     string
	p     *retentionProbe
	first bool
}

func (r *retentionProbeRows) Next(dest []driver.Value) error {
	err := r.Rows.Next(dest)
	if err == nil && !r.first {
		r.first = true
		if r.p.row != nil {
			return r.p.row(r.ctx, r.q)
		}
	}
	return err
}
func probeRetentionSQL(t *testing.T, h *hub.Hub, p *retentionProbe) {
	t.Helper()
	original := h.DB.DB
	p.driver = original.Driver()
	p.dsn = "file:" + filepath.Join(h.Cfg.DataDir, "hub.db") + "?_txlock=immediate&_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(ON)"
	h.DB.DB = sql.OpenDB(p)
	h.DB.SetMaxOpenConns(4)
	t.Cleanup(func() { h.DB.Close(); original.Close() })
}

type retentionProbeKey struct{}

func waitRetentionBarrier(t *testing.T, ctx context.Context, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
}

func setProtectiveRetention(t *testing.T, tx *sql.Tx, now time.Time) {
	t.Helper()
	if _, err := tx.Exec(`INSERT INTO hub_preferences VALUES('retention.ack_events','0',?,'synthetic')`, envelope.FormatTime(now)); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`UPDATE hub_preferences_state SET revision=revision+1`); err != nil {
		t.Fatal(err)
	}
}

func retentionReadPurgeInterleaving(t *testing.T, boundary string) {
	env := testhub.StartWith(t, func(cfg *hub.Config) { cfg.DeliveryReferences = true })
	h := env.Hub
	peer, secret := env.NewPeer("session", "reader")
	_, sender := env.NewPeer("session", "sender")
	for i := range 2 {
		mustSend(t, env, sender, req("snapshot-"+strconv.Itoa(i), peer))
	}
	c, err := h.Log.Current(context.Background(), []string{peer})
	if err != nil {
		t.Fatal(err)
	}
	caller, err := h.Auth.Resolve(context.Background(), secret)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Ack(context.Background(), caller, "", c); err != nil {
		t.Fatal(err)
	}
	env.Now.Advance(9 * 24 * time.Hour)
	start := eventlog.Cursor{Epoch: c.Epoch}
	want, err := h.Log.Read(context.Background(), []string{peer}, start, 100)
	if err != nil || len(want) < 2 {
		t.Fatalf("read fixture: events=%d err=%v", len(want), err)
	}
	var referenceCount int
	if err := h.DB.QueryRow(`SELECT count(*) FROM events WHERE owner=? AND payload_format='delivery_ref_v1'`, peer).Scan(&referenceCount); err != nil || referenceCount != 2 {
		t.Fatal("fixture needs two reference deliveries")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	readCtx := context.WithValue(ctx, retentionProbeKey{}, "read")
	ready, releaseCh, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var releaseOnce, readyOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseCh) }) }
	var hydrated atomic.Int32
	match := func(q string) bool {
		switch boundary {
		case "floor":
			return strings.Contains(q, "FROM event_retention WHERE")
		case "events":
			return strings.Contains(q, "FROM events e WHERE")
		default:
			return strings.Contains(q, "FROM messages WHERE")
		}
	}
	probeRetentionSQL(t, h, &retentionProbe{
		query: func(ctx context.Context, q string, inSnapshot bool) error {
			if ctx.Value(retentionProbeKey{}) != "read" {
				return nil
			}
			if !inSnapshot {
				return errors.New("read query escaped the deferred snapshot")
			}
			if strings.Contains(q, "FROM messages WHERE") {
				hydrated.Add(1)
			}
			return nil
		},
		row: func(ctx context.Context, q string) error {
			if ctx.Value(retentionProbeKey{}) != "read" || !match(q) {
				return nil
			}
			readyOnce.Do(func() { close(ready) })
			select {
			case <-releaseCh:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		},
	})
	type readResult struct {
		events []envelope.Event
		err    error
	}
	done := make(chan readResult, 1)
	go func() {
		defer close(finished)
		page, err := h.Log.Read(readCtx, []string{peer}, start, 100)
		done <- readResult{page, err}
	}()
	defer func() {
		release()
		cancel()
		select {
		case <-finished:
		case <-time.After(6 * time.Second):
			t.Error("reader failed to exit")
		}
	}()
	waitRetentionBarrier(t, ctx, ready)
	// The reader has consumed its floor/rows/body from the old snapshot.
	// Purge plus a concurrent idempotent pull ack commit before it can continue.
	ackDone := make(chan error, 1)
	go func() { ackDone <- h.Ack(ctx, caller, "", c) }()
	stat, err := h.PruneEventSlice(ctx)
	if err != nil || stat.Deleted != len(want) {
		t.Fatalf("purge deleted=%d want=%d err=%v", stat.Deleted, len(want), err)
	}
	select {
	case err := <-ackDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	release()
	select {
	case got := <-done:
		// This forced old-snapshot ordering requires the entire original page.
		if got.err != nil || !reflect.DeepEqual(got.events, want) {
			t.Fatalf("read lost a snapshot page: got=%d want=%d err=%v", len(got.events), len(want), got.err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if hydrated.Load() == 0 {
		t.Fatal("read did not hydrate immutable bodies")
	}
	_, err = h.Log.Read(ctx, []string{peer}, start, 100)
	var purged eventlog.ErrCursorPurged
	if !errors.As(err, &purged) || purged.Oldest != c {
		t.Fatalf("post-purge read floor=%v err=%v want=%v", purged.Oldest, err, c)
	}
	// A rolled-back delete cannot leak its floor.
	next := retentionEvent(t, h, peer, h.Now())
	err = h.DB.Tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM events WHERE seq=?`, next.Seq); err != nil {
			return err
		}
		return errors.New("rollback")
	})
	if err == nil {
		t.Fatal("expected rollback")
	}
	retentionFloor(t, h, peer, c.Seq)
}

func retentionPolicyWriterInterleaving(t *testing.T) {
	cfg := hub.DefaultConfig()
	cfg.EventRetentionLimits.Rows = 1
	h, _, clock := preferencesHubClock(t, cfg)
	peer := "peer_slice_policy"
	first := retentionEvent(t, h, peer, clock.Now().Add(-9*24*time.Hour))
	last := retentionEvent(t, h, peer, clock.Now().Add(-9*24*time.Hour))
	retentionAck(t, h, peer, "*", last)
	// Complete one bounded slice before changing policy between slices.
	initial, err := h.PruneEventSlice(context.Background())
	if err != nil || initial.Deleted != 1 || initial.Policy.Revision != 0 {
		t.Fatalf("first slice=%+v err=%v", initial, err)
	}
	retentionFloor(t, h, peer, first.Seq)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	attempted, policyRead, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var beginOnce, readOnce sync.Once
	probeRetentionSQL(t, h, &retentionProbe{
		begin: func(ctx context.Context, opts driver.TxOptions) error {
			if ctx.Value(retentionProbeKey{}) != "slice" {
				return nil
			}
			beginOnce.Do(func() { close(attempted) })
			// A standalone read before writer acquisition would observe old policy.
			if opts.ReadOnly {
				return errors.New("policy read before deletion writer acquisition")
			}
			return nil
		},
		row: func(ctx context.Context, q string) error {
			if ctx.Value(retentionProbeKey{}) == "slice" && strings.Contains(q, "FROM hub_preferences_state") {
				readOnce.Do(func() { close(policyRead) })
			}
			return nil
		},
	})
	tx, err := h.DB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	setProtectiveRetention(t, tx, clock.Now())
	done := make(chan error, 1)
	go func() {
		defer close(finished)
		_, err := h.PruneEventSlice(context.WithValue(ctx, retentionProbeKey{}, "slice"))
		done <- err
	}()
	defer func() {
		tx.Rollback()
		cancel()
		select {
		case <-finished:
		case <-time.After(6 * time.Second):
			t.Error("slice failed to exit")
		}
	}()
	waitRetentionBarrier(t, ctx, attempted)
	// The slice is at real driver BeginTx while the protective writer owns the
	// SQLite lock. Its policy query must remain blocked until this commit.
	select {
	case <-policyRead:
		t.Fatal("slice read policy outside the contended writer transaction")
	case err := <-done:
		t.Fatalf("slice escaped writer contention: %v", err)
	case <-time.After(100 * time.Millisecond):
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	waitRetentionBarrier(t, ctx, policyRead)
	retentionFloor(t, h, peer, first.Seq)
	if status := h.EventRetentionStatus(); status.Policy.Revision != 1 || status.Deleted != 0 {
		t.Fatalf("next slice retained old policy: revision=%d deleted=%d", status.Policy.Revision, status.Deleted)
	}
	var remaining int
	if err := h.DB.QueryRow(`SELECT count(*) FROM events WHERE owner=? AND seq=?`, peer, last.Seq).Scan(&remaining); err != nil || remaining != 1 {
		t.Fatalf("protective next slice lost remainder: count=%d err=%v", remaining, err)
	}
}
