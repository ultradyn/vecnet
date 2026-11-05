package hub_test

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/prefs"
	"github.com/XertroV/vecnet/internal/testhub"
)

// Keep the real SQLite transactions and writer locks. These barriers control
// only the gaps between SQL calls, including commit returning to Preferences
// before its immutable snapshot is published.
type runtimeSQLHooks struct {
	beforeQuery func(string, bool)
	afterExec   func(string)
	afterCommit func(bool)
}
type runtimeConnector struct {
	driver driver.Driver
	dsn    string
	hooks  runtimeSQLHooks
}

func (c runtimeConnector) Driver() driver.Driver { return c.driver }
func (c runtimeConnector) Connect(context.Context) (driver.Conn, error) {
	conn, err := c.driver.Open(c.dsn)
	if err != nil {
		return nil, err
	}
	return &runtimeConn{Conn: conn, hooks: c.hooks}, nil
}

type runtimeConn struct {
	driver.Conn
	hooks                 runtimeSQLHooks
	inTx, preferenceWrite bool
}

func (c *runtimeConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	tx, err := c.Conn.(driver.ConnBeginTx).BeginTx(ctx, opts)
	if err != nil {
		return nil, err
	}
	c.inTx, c.preferenceWrite = true, false
	return runtimeTx{Tx: tx, conn: c}, nil
}
func (c *runtimeConn) QueryContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	if c.hooks.beforeQuery != nil {
		c.hooks.beforeQuery(q, c.inTx)
	}
	return c.Conn.(driver.QueryerContext).QueryContext(ctx, q, args)
}
func (c *runtimeConn) ExecContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Result, error) {
	result, err := c.Conn.(driver.ExecerContext).ExecContext(ctx, q, args)
	if err == nil {
		if strings.Contains(q, "UPDATE hub_preferences_state") {
			c.preferenceWrite = true
		}
		if c.hooks.afterExec != nil {
			c.hooks.afterExec(q)
		}
	}
	return result, err
}

type runtimeTx struct {
	driver.Tx
	conn *runtimeConn
}

func (tx runtimeTx) Commit() error {
	err := tx.Tx.Commit()
	tx.conn.inTx = false
	if err == nil && tx.conn.hooks.afterCommit != nil {
		tx.conn.hooks.afterCommit(tx.conn.preferenceWrite)
	}
	return err
}
func (tx runtimeTx) Rollback() error {
	err := tx.Tx.Rollback()
	tx.conn.inTx = false
	return err
}
func runtimeInterposeSQL(t *testing.T, env *testhub.Env, hooks runtimeSQLHooks) {
	t.Helper()
	original := env.Hub.DB.DB
	// Match store.Open's transaction mode and pragmas on every connection.
	dsn := "file:" + filepath.Join(env.Hub.Cfg.DataDir, "hub.db") + "?_txlock=immediate&_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(ON)"
	wrapped := sql.OpenDB(runtimeConnector{driver: original.Driver(), dsn: dsn, hooks: hooks})
	wrapped.SetMaxOpenConns(4)
	// Retain the shared *store.DB: Preferences, Log and the hub use this pool.
	env.Hub.DB.DB = wrapped
	t.Cleanup(func() { wrapped.Close(); env.Hub.DB.DB = original })
}

// Return only once the patch is durable but Current is still the old revision.
// Publishing is explicitly released from inside the consuming operation.
func runtimePausePublication(t *testing.T, env *testhub.Env, values map[string]prefs.Value, publishAt string) func() {
	t.Helper()
	before := readPrefs(t, env.Hub)
	patch := prefs.Patch{Set: values}
	preview, err := env.Hub.Preferences.PreviewPreferences(context.Background(), prefsOperator, before.ETag, patch)
	if err != nil {
		t.Fatal(err)
	}
	if len(preview.Risks) > 0 {
		patch.Confirm = []string{preview.ConfirmToken}
	}
	committed, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var releaseOnce, publishOnce sync.Once
	var patchErr error
	releasePublication := func() { releaseOnce.Do(func() { close(release) }) }
	wait := func(ch <-chan struct{}, label string) {
		t.Helper()
		select {
		case <-ch:
		case <-time.After(5 * time.Second):
			t.Fatalf("timed out waiting for %s", label)
		}
	}
	runtimeInterposeSQL(t, env, runtimeSQLHooks{
		afterCommit: func(preferenceWrite bool) {
			if preferenceWrite {
				close(committed)
				// Cleanup always releases this barrier, including assertion failures.
				<-release
			}
		},
		afterExec: func(query string) {
			if strings.Contains(query, publishAt) {
				publishOnce.Do(func() { releasePublication(); wait(done, "preference publication") })
			}
		},
	})
	t.Cleanup(func() { releasePublication(); wait(done, "patch cleanup") })
	go func() {
		_, patchErr = env.Hub.Preferences.PatchPreferences(context.Background(), prefsOperator, before.ETag, patch)
		close(done)
	}()
	wait(committed, "preference commit")
	if env.Hub.Preferences.Current().Revision != before.Revision {
		t.Fatal("publication barrier did not retain old Current")
	}
	return func() {
		t.Helper()
		wait(done, "patch completion")
		if patchErr != nil {
			t.Fatal(patchErr)
		}
		if env.Hub.Preferences.Current().Revision != before.Revision+1 {
			t.Fatal("operation did not cross the publication barrier")
		}
	}
}

func TestPreferencesRuntimeSingleOperationSnapshot(t *testing.T) {
	env := testhub.Start(t)
	_, secret := env.NewPeer("session", "sender")
	one, _ := env.NewPeer("session", "one")
	two, _ := env.NewPeer("session", "two")
	three, _ := env.NewPeer("session", "three")
	at := envelope.FormatTime(env.Now.Now())
	if _, err := env.Hub.DB.Exec(`UPDATE peers SET presence_state='working', presence_at=?`, at); err != nil {
		t.Fatal(err)
	}
	env.Now.Advance(2 * time.Minute)
	runtimePatch(t, env.Hub, map[string]prefs.Value{
		"agents.default_max_depth": prefs.Int(7), "agents.default_ttl": prefs.Duration(420), "agents.thread_budget": prefs.Int(7), "presence.session_ttl": prefs.Duration(3600),
	})
	checkPublication := runtimePausePublication(t, env, map[string]prefs.Value{
		"agents.default_max_depth": prefs.Int(8), "agents.default_ttl": prefs.Duration(480), "agents.thread_budget": prefs.Int(8), "presence.session_ttl": prefs.Duration(60),
	}, "INSERT INTO deliveries")
	// The new revision is durable when send starts; publication occurs between
	// its first and second delivery inserts. All recipients must use that same
	// durable revision even while Current still names the previous revision.
	body := req("snapshot-deliveries", one, two, three)
	body["priority"] = "wake_idle"
	msg := mustSend(t, env, secret, body)
	checkPublication()
	loop := storedLoop(t, env, msg.MessageID)
	if loop.MaxDepth != 8 || loop.ExpiresAt != envelope.FormatTime(env.Now.Now().Add(8*time.Minute)) {
		t.Errorf("mixed message defaults: %+v", loop)
	}
	var budget int
	if err := env.Hub.DB.QueryRow(`SELECT budget_remaining FROM threads WHERE thr_id=?`, msg.ThreadID).Scan(&budget); err != nil || budget != 7 {
		t.Errorf("budget=%d err=%v; want 7", budget, err)
	}
	for _, peer := range []string{one, two, three} {
		var held int
		if err := env.Hub.DB.QueryRow(`SELECT held FROM deliveries WHERE msg_id=? AND recipient=?`, msg.MessageID, peer).Scan(&held); err != nil || held != 0 {
			t.Errorf("delivery %s held=%d err=%v; want 0 from captured short TTL", peer, held, err)
		}
	}
}

func TestPreferencesRuntimeReactionSnapshotAcrossPublication(t *testing.T) {
	env := testhub.Start(t)
	sender, secret := env.NewPeer("session", "sender")
	reactor, reactorSecret := env.NewPeer("session", "reactor")
	caller := provisionCaller(t, env, reactorSecret)
	target := mustSend(t, env, secret, req("reaction-parent", reactor))
	at := envelope.FormatTime(env.Now.Now())
	if _, err := env.Hub.DB.Exec(`UPDATE peers SET presence_state='working', presence_at=? WHERE peer_id=?`, at, sender); err != nil {
		t.Fatal(err)
	}
	env.Now.Advance(2 * time.Minute)
	runtimePatch(t, env.Hub, map[string]prefs.Value{"presence.session_ttl": prefs.Duration(3600)})
	checkPublication := runtimePausePublication(t, env, map[string]prefs.Value{"agents.default_ttl": prefs.Duration(600), "presence.session_ttl": prefs.Duration(60)}, "INSERT INTO reactions(")
	reaction, err := env.Hub.React(context.Background(), caller, envelope.ReactionRequest{MessageID: target.MessageID, Emoji: "✅", Op: "add", Priority: "wake_idle"}, "http_send")
	if err != nil {
		t.Fatal(err)
	}
	checkPublication()
	if reaction.Wake == nil {
		t.Fatal("reaction did not wake sender")
	}
	loop := storedLoop(t, env, reaction.Wake.MessageID)
	if loop.ExpiresAt != envelope.FormatTime(env.Now.Now().Add(10*time.Minute)) {
		t.Errorf("reaction mixed revisions: expires_at=%s; want committed short TTL", loop.ExpiresAt)
	}
	var held int
	if err := env.Hub.DB.QueryRow(`SELECT held FROM deliveries WHERE dly_id=?`, reaction.Wake.DeliveryID).Scan(&held); err != nil || held != 0 {
		t.Errorf("reaction held=%d err=%v; want 0 from same committed revision", held, err)
	}
}

func TestPreferencesRuntimeStaleHeldTTLExtension(t *testing.T) {
	env := testhub.Start(t)
	peer, _ := env.NewPeer("session", "working")
	runtimePatch(t, env.Hub, map[string]prefs.Value{"presence.session_ttl": prefs.Duration(60)})
	at := envelope.FormatTime(env.Now.Now())
	if _, err := env.Hub.DB.Exec(`UPDATE peers SET presence_state='working', presence_at=? WHERE peer_id=?`, at, peer); err != nil {
		t.Fatal(err)
	}
	body := req("old-held", peer)
	body["priority"] = "wake_idle"
	old := mustSend(t, env, env.OperatorSecret, body)
	env.Now.Advance(2 * time.Minute)
	var fresh string
	var deferred, interleaved bool
	change := func() {
		runtimePatch(t, env.Hub, map[string]prefs.Value{"presence.session_ttl": prefs.Duration(3600)})
		body := req("new-held", peer)
		body["priority"] = "wake_idle"
		fresh = mustSend(t, env, env.OperatorSecret, body).MessageID
	}
	runtimeInterposeSQL(t, env, runtimeSQLHooks{
		beforeQuery: func(query string, inTx bool) {
			if !interleaved && strings.Contains(query, "SELECT p.peer_id, p.kind, p.presence_state") {
				interleaved = true
				// A writer may insert the new delivery before an unprotected read. An
				// atomic sweep keeps the writer queued until its transaction commits.
				if inTx {
					deferred = true
				} else {
					change()
				}
			}
		},
		afterCommit: func(bool) {
			if deferred {
				deferred = false
				change()
			}
		},
	})
	env.Hub.Tick(context.Background(), false)
	if !interleaved || fresh == "" {
		t.Fatal("did not interleave extension/send with held sweep")
	}
	for _, msg := range []string{old.MessageID, fresh} {
		var held int
		if err := env.Hub.DB.QueryRow(`SELECT held FROM deliveries WHERE msg_id=? AND recipient=?`, msg, peer).Scan(&held); err != nil {
			t.Fatal(err)
		}
		if msg == fresh && held != 1 {
			t.Errorf("delivery created under longer TTL became pushable: held=%d", held)
		}
		if msg == old.MessageID && held != 0 {
			t.Error("eligible original delivery was not released")
		}
	}
	// No stale iterator or nested writer transaction may survive the sweep.
	if _, err := env.Hub.DB.Exec(`UPDATE peers SET presence_state='working' WHERE peer_id=?`, peer); err != nil {
		t.Fatal(fmt.Errorf("post-sweep writer: %w", err))
	}
}
