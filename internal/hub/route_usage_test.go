package hub_test

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/hub"
	"github.com/XertroV/vecnet/internal/testhub"
)

// Source-only A3 draft; uncompiled and unrun. Exercise the existing hub writer
// with real SQLite transactions/locks and telemetry-only injected faults.
// Attempted context deadlines do not substitute for the wall-time oracle.

type t1HubLog struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *t1HubLog) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}

func (b *t1HubLog) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.String()
}

type t1HubFault struct {
	mu                                  sync.Mutex
	stage, raw                          string
	begins, inserts, prunes, commits    int
	hits                                int
	deadline, detached, mixedSettlement bool
}

type t1HubConnector struct {
	driver driver.Driver
	dsn    string
	fault  *t1HubFault
}

func (c t1HubConnector) Driver() driver.Driver { return c.driver }
func (c t1HubConnector) Connect(context.Context) (driver.Conn, error) {
	conn, err := c.driver.Open(c.dsn)
	if err != nil {
		return nil, err
	}
	return &t1HubConn{Conn: conn, fault: c.fault}, nil
}

type t1HubConn struct {
	driver.Conn
	fault *t1HubFault
	inTx  bool
}

func (c *t1HubConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	c.fault.mu.Lock()
	c.fault.begins++
	d, ok := ctx.Deadline()
	c.fault.deadline = ok && time.Until(d) > 0 && time.Until(d) <= time.Second
	c.fault.detached = ctx.Err() == nil
	fail := c.fault.stage == "begin"
	if fail {
		c.fault.hits++
	}
	c.fault.mu.Unlock()
	if fail {
		return nil, errors.New(c.fault.raw)
	}
	tx, err := c.Conn.(driver.ConnBeginTx).BeginTx(ctx, opts)
	if err != nil {
		return nil, err
	}
	c.inTx = true
	return &t1HubTx{Tx: tx, conn: c}, nil
}

func (c *t1HubConn) inspect(q string) {
	if c.inTx && (strings.Contains(strings.ToLower(q), "deliveries") || strings.Contains(strings.ToLower(q), "event_outbox")) {
		c.fault.mu.Lock()
		c.fault.mixedSettlement = true
		c.fault.mu.Unlock()
	}
}

func (c *t1HubConn) QueryContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	c.inspect(q)
	return c.Conn.(driver.QueryerContext).QueryContext(ctx, q, args)
}

func (c *t1HubConn) ExecContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Result, error) {
	c.inspect(q)
	qUpper := strings.ToUpper(strings.TrimSpace(q))
	c.fault.mu.Lock()
	insert := strings.HasPrefix(qUpper, "INSERT") && strings.Contains(qUpper, "ROUTE_ATTEMPTS")
	prune := strings.HasPrefix(qUpper, "DELETE") && strings.Contains(qUpper, "ROUTE_ATTEMPTS")
	if insert {
		c.fault.inserts++
	}
	if prune {
		c.fault.prunes++
	}
	fail := (insert && c.fault.stage == "insert") || (prune && c.fault.stage == "prune")
	if fail {
		c.fault.hits++
	}
	c.fault.mu.Unlock()
	if fail {
		return nil, errors.New(c.fault.raw)
	}
	return c.Conn.(driver.ExecerContext).ExecContext(ctx, q, args)
}

type t1HubTx struct {
	driver.Tx
	conn *t1HubConn
}

func (tx *t1HubTx) Commit() error {
	defer func() { tx.conn.inTx = false }()
	tx.conn.fault.mu.Lock()
	tx.conn.fault.commits++
	fail := tx.conn.fault.stage == "commit"
	if fail {
		tx.conn.fault.hits++
	}
	tx.conn.fault.mu.Unlock()
	if fail {
		if err := tx.Tx.Rollback(); err != nil {
			return err
		}
		return errors.New(tx.conn.fault.raw)
	}
	return tx.Tx.Commit()
}

func (tx *t1HubTx) Rollback() error {
	tx.conn.inTx = false
	return tx.Tx.Rollback()
}

func t1HubInterpose(t *testing.T, env *testhub.Env, stage, raw string) *t1HubFault {
	t.Helper()
	original := env.Hub.DB.DB
	fault := &t1HubFault{stage: stage, raw: raw}
	dsn := "file:" + filepath.Join(env.Hub.Cfg.DataDir, "hub.db") + "?_txlock=immediate&_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(ON)"
	wrapped := sql.OpenDB(t1HubConnector{driver: original.Driver(), dsn: dsn, fault: fault})
	wrapped.SetMaxOpenConns(4)
	t.Cleanup(func() { wrapped.Close(); env.Hub.DB.DB = original })
	if err := wrapped.Ping(); err != nil {
		t.Fatal(err)
	}
	env.Hub.DB.DB = wrapped
	return fault
}

func t1HubSnapshot(t *testing.T, env *testhub.Env, route string) []byte {
	t.Helper()
	rows, err := env.Hub.DB.Query(`SELECT id,route_id,delivery_id,at,outcome,http_status,error_class,body_bytes,latency_ms FROM route_attempts WHERE route_id=? ORDER BY id`, route)
	if err != nil {
		t.Fatalf("query attempt storage: %v", err)
	}
	defer rows.Close()
	out := [][]any{}
	for rows.Next() {
		values, ptrs := make([]any, 9), make([]any, 9)
		for i := range values {
			ptrs[i] = &values[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			t.Fatal(err)
		}
		out = append(out, values)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func t1HubAccepted(t *testing.T) (*testhub.Env, string, string, *atomic.Int64) {
	t.Helper()
	env := testhub.Start(t)
	posts := &atomic.Int64{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		posts.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	peer, _ := env.NewPeer("session", "recipient")
	_, sender := env.NewPeer("session", "sender")
	route, _, err := env.Hub.AddRoute(context.Background(), "peer", peer, srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if err := env.Hub.MarkRouteVerified(context.Background(), route); err != nil {
		t.Fatal(err)
	}
	resp, raw := env.Do(sender, "POST", "/v1/messages", map[string]any{"idempotency_key": "deadline-fixture", "priority": "wake", "recipients": []string{peer}, "content": map[string]any{"text": "synthetic deadline body"}})
	var sent hub.SendResponse
	if err := json.Unmarshal(raw, &sent); err != nil || resp.StatusCode != http.StatusAccepted || len(sent.Deliveries) != 1 {
		t.Fatalf("send fixture: status %d, decode %v", resp.StatusCode, err)
	}
	if n, err := env.Worker.Step(context.Background()); err != nil || n != 1 || posts.Load() != 1 {
		t.Fatalf("real accepted payload fixture: %d, %v, posts %d", n, err, posts.Load())
	}
	return env, route, sent.Deliveries[0].DeliveryID, posts
}

func t1HubDiagnostics(t *testing.T, raw string, forbidden ...string) [2]string {
	t.Helper()
	for _, canary := range forbidden {
		if strings.Contains(raw, canary) {
			t.Fatalf("telemetry diagnostic leaked a canary (%d bytes)", len(canary))
		}
	}
	var found [2]string
	count := 0
	for _, line := range strings.Split(strings.TrimSpace(raw), "\n") {
		if line == "" {
			continue
		}
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatal("diagnostic is not JSON", err)
		}
		count++
		if record["msg"] != "route attempt telemetry failed" || record["level"] != "WARN" {
			t.Fatal("unexpected telemetry diagnostic")
		}
		for _, key := range []string{"err", "error", "sql", "url", "body", "headers"} {
			if _, exists := record[key]; exists {
				t.Fatalf("raw diagnostic field %s", key)
			}
		}
		for _, key := range []string{"stage", "failure_stage"} {
			if s, ok := record[key].(string); ok {
				found[0] = s
			}
		}
		for _, key := range []string{"code", "class", "error_class"} {
			if s, ok := record[key].(string); ok {
				found[1] = s
			}
		}
	}
	if count != 1 || found[0] == "" || found[1] != "write_failed" {
		t.Fatal("missing fixed diagnostic stage/code")
	}
	return found
}

func t1HubReleased(t *testing.T, env *testhub.Env) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	conn, err := env.Hub.DB.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	defer conn.ExecContext(context.Background(), "ROLLBACK")
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		t.Fatal("telemetry left the real writer locked", err)
	}
	if _, err := conn.ExecContext(ctx, "ROLLBACK"); err != nil {
		t.Fatal(err)
	}
}

func TestRouteAttemptTelemetryDeadlineAndNoRetry(t *testing.T) {
	for _, stage := range []string{"begin", "insert", "prune", "commit", "lock", "canceled_parent"} {
		t.Run(stage, func(t *testing.T) {
			var priorCode [2]string
			for _, canary := range []string{"secretcanarya", "secretcanaryb"} {
				t.Run(canary, func(t *testing.T) {
					env, route, dly, posts := t1HubAccepted(t)
					if _, err := env.Hub.DB.Exec(`INSERT INTO route_attempts(route_id,delivery_id,at,outcome,body_bytes,latency_ms) VALUES(?,'e1.1','2020-01-01T00:00:00.000Z','ok',0,0)`, route); err != nil {
						t.Fatal(err)
					}
					before := t1HubSnapshot(t, env, route)
					rawError := "https://user:key@fixture.invalid/private?token=" + canary + " SELECT private_sql_canary"
					fault := t1HubInterpose(t, env, stage, rawError)
					var logs t1HubLog
					oldLogger := slog.Default()
					slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
					t.Cleanup(func() { slog.SetDefault(oldLogger) })
					ctx, cancel := context.WithCancel(context.Background())
					defer cancel()
					if stage == "canceled_parent" || stage == "lock" {
						cancel() // completed observation must use a detached context
					}
					var locker *sql.Conn
					if stage == "lock" {
						var err error
						locker, err = env.Hub.DB.Conn(context.Background())
						if err != nil {
							t.Fatal(err)
						}
						t.Cleanup(func() {
							if locker != nil {
								_, _ = locker.ExecContext(context.Background(), "ROLLBACK")
								_ = locker.Close()
							}
						})
						// Open the telemetry connection before taking the writer lock;
						// the timing oracle must measure BeginTx, not connection setup.
						if err := env.Hub.DB.PingContext(context.Background()); err != nil {
							t.Fatal(err)
						}
						var mode string
						var busy int
						if err := locker.QueryRowContext(context.Background(), "PRAGMA journal_mode").Scan(&mode); err != nil {
							t.Fatal(err)
						}
						if err := locker.QueryRowContext(context.Background(), "PRAGMA busy_timeout").Scan(&busy); err != nil {
							t.Fatal(err)
						}
						if mode != "wal" || busy != 5000 {
							t.Fatalf("actual SQLite mode/busy = %s/%d", mode, busy)
						}
						if _, err := locker.ExecContext(context.Background(), "BEGIN IMMEDIATE"); err != nil {
							t.Fatal(err)
						}
					}
					observation := hub.RouteAttemptObservation{
						RouteID: route, DeliveryID: dly, Kind: hub.RouteKindHTTP,
						Attempted: true, HTTPStatus: 200, BodyBytes: 17,
						Elapsed: 1500 * time.Microsecond, CompletedAt: env.Now.Now(),
					}
					start := time.Now()
					env.Hub.NoteRouteAttempt(ctx, observation)
					elapsed := time.Since(start)
					t.Logf("driver telemetry timing: stage=%s elapsed=%v; source busy_timeout=5s, target deadline=1s", stage, elapsed)
					if locker != nil {
						if _, err := locker.ExecContext(context.Background(), "ROLLBACK"); err != nil {
							t.Fatal(err)
						}
						if err := locker.Close(); err != nil {
							t.Fatal(err)
						}
						locker = nil
					}
					// This is an actual timing oracle, not a claim that the driver's
					// five-second busy wait is already overridden. Allow 250ms only
					// for scheduling overhead; report a failure for controller ruling.
					if elapsed > 1250*time.Millisecond {
						t.Errorf("controller timing ruling required: telemetry took %v, target 1s", elapsed)
					}
					fault.mu.Lock()
					begins, inserts, prunes, commits, hits := fault.begins, fault.inserts, fault.prunes, fault.commits, fault.hits
					deadline, detached, mixed := fault.deadline, fault.detached, fault.mixedSettlement
					fault.mu.Unlock()
					if begins != 1 || !deadline || !detached || mixed {
						t.Fatalf("single detached telemetry transaction = begin %d/deadline %v/detached %v/mixed %v", begins, deadline, detached, mixed)
					}
					if stage != "lock" && stage != "canceled_parent" && hits != 1 {
						t.Fatalf("fault %s reached %d times", stage, hits)
					}
					want := map[string][3]int{"begin": {0, 0, 0}, "insert": {1, 0, 0}, "prune": {1, 1, 0}, "commit": {1, 1, 1}, "lock": {0, 0, 0}, "canceled_parent": {1, 1, 1}}[stage]
					if [3]int{inserts, prunes, commits} != want || ((stage == "lock" || stage == "canceled_parent") && hits != 0) {
						t.Fatal("telemetry statement/commit counts changed", inserts, prunes, commits, hits)
					}
					if stage == "canceled_parent" {
						var n int
						if err := env.Hub.DB.QueryRow(`SELECT count(*) FROM route_attempts WHERE route_id=? AND delivery_id=? AND body_bytes=17 AND latency_ms=2 AND outcome='ok' AND http_status=200 AND error_class IS NULL AND at=?`, route, dly, envelope.FormatTime(env.Now.Now())).Scan(&n); err != nil || n != 1 {
							t.Fatalf("detached write or nearest-ms rounding failed: %d, %v", n, err)
						}
						if logs.String() != "" {
							t.Fatal("successful detached write emitted a failure warning")
						}
					} else {
						if !bytes.Equal(before, t1HubSnapshot(t, env, route)) {
							t.Fatal("failed telemetry partially committed insert/prune")
						}
						code := t1HubDiagnostics(t, logs.String(), canary, "private_sql_canary", "fixture.invalid", "user:key")
						if code[0] != stage && !(stage == "lock" && code[0] == "begin") {
							t.Fatalf("diagnostic stage = %q, want %s", code[0], stage)
						}
						if priorCode != [2]string{} && code != priorCode {
							t.Fatal("diagnostic classification depends on raw error text")
						}
						priorCode = code
					}
					var state string
					if err := env.Hub.DB.QueryRow(`SELECT state FROM deliveries WHERE dly_id=?`, dly).Scan(&state); err != nil || state != "accepted" || posts.Load() != 1 {
						t.Fatalf("telemetry affected accepted outcome/request count: %s/%d, %v", state, posts.Load(), err)
					}
					if env.Hub.DB.Stats().InUse != 0 {
						t.Fatal("telemetry leaked a connection")
					}
					t1HubReleased(t, env)
					after := t1HubSnapshot(t, env, route)
					fault.mu.Lock()
					fault.stage = "" // a late retry can now succeed and change rows
					fault.mu.Unlock()
					// A late retry after releasing the lock/fault would be observable.
					time.Sleep(50 * time.Millisecond)
					fault.mu.Lock()
					counts := []int{fault.begins, fault.inserts, fault.prunes, fault.commits, fault.hits}
					fault.mu.Unlock()
					if !reflect.DeepEqual(counts, []int{begins, inserts, prunes, commits, hits}) || posts.Load() != 1 || !bytes.Equal(after, t1HubSnapshot(t, env, route)) || env.Hub.DB.Stats().InUse != 0 {
						t.Fatal("telemetry scheduled a background retry")
					}
				})
			}
		})
	}
}
