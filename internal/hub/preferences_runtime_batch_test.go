package hub_test

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/opview"
	"github.com/XertroV/vecnet/internal/prefs"
	"github.com/XertroV/vecnet/internal/store"
	"github.com/XertroV/vecnet/internal/testhub"
)

// Interpose only the peer-row query. The real WAL reader stays open while the
// original hub connection commits a preference change between its first and
// second rows. This tests the actual batch callers without timing or sleeps.
type presenceBatchConnector struct {
	driver driver.Driver
	dsn    string
	change func()
}

func (c presenceBatchConnector) Driver() driver.Driver { return c.driver }
func (c presenceBatchConnector) Connect(context.Context) (driver.Conn, error) {
	conn, err := c.driver.Open(c.dsn)
	if err != nil {
		return nil, err
	}
	return presenceBatchConn{Conn: conn, change: c.change}, nil
}

type presenceBatchConn struct {
	driver.Conn
	change func()
}

func (c presenceBatchConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	rows, err := c.Conn.(driver.QueryerContext).QueryContext(ctx, query, args)
	if err == nil && strings.Contains(query, "presence_state") && strings.Contains(query, "FROM peers") {
		return &presenceBatchRows{Rows: rows, change: c.change}, nil
	}
	return rows, err
}

type presenceBatchRows struct {
	driver.Rows
	change func()
	read   int
}

func (r *presenceBatchRows) Next(dest []driver.Value) error {
	if r.read == 1 {
		r.change()
	}
	err := r.Rows.Next(dest)
	if err == nil {
		r.read++
	}
	return err
}

func TestPreferencesRuntimePresenceBatchSnapshot(t *testing.T) {
	for _, operator := range []bool{false, true} {
		name := "directory"
		if operator {
			name = "operator-peers"
		}
		t.Run(name, func(t *testing.T) {
			env := testhub.Start(t)
			env.NewPeer("session", "one")
			env.NewPeer("session", "two")
			at := envelope.FormatTime(env.Now.Now())
			if _, err := env.Hub.DB.Exec(`UPDATE peers SET presence_state='working', presence_at=?`, at); err != nil {
				t.Fatal(err)
			}
			env.Now.Advance(2 * time.Minute)
			runtimePatch(t, env.Hub, map[string]prefs.Value{"presence.session_ttl": prefs.Duration(3600), "presence.default_ttl": prefs.Duration(3600)})
			changes := 0
			wrapped := sql.OpenDB(presenceBatchConnector{
				driver: env.Hub.DB.Driver(), dsn: filepath.Join(env.Hub.Cfg.DataDir, "hub.db"),
				change: func() {
					changes++
					if changes == 1 {
						runtimePatch(t, env.Hub, map[string]prefs.Value{"presence.session_ttl": prefs.Duration(60), "presence.default_ttl": prefs.Duration(60)})
					}
				},
			})
			t.Cleanup(func() { wrapped.Close() })
			// Preferences keeps the original pool for the interleaved writer.
			env.Hub.DB = &store.DB{DB: wrapped}
			read := func() []string {
				t.Helper()
				var states []string
				if operator {
					rows, err := opview.New(env.Hub).Peers(context.Background())
					if err != nil {
						t.Fatal(err)
					}
					for _, row := range rows {
						states = append(states, row.Peer.Presence)
					}
				} else {
					rows, err := env.Hub.Directory(context.Background())
					if err != nil {
						t.Fatal(err)
					}
					for _, row := range rows {
						states = append(states, row.Presence)
					}
				}
				return states
			}
			for i, want := range []string{"working", "unknown"} {
				states := read()
				if len(states) < 3 || changes != i+1 {
					t.Fatalf("did not interleave publication with peer batch: states=%v changes=%d", states, changes)
				}
				for _, state := range states {
					if state != want {
						t.Fatalf("batch %d mixed preference revisions: %v; want all %s", i+1, states, want)
					}
				}
			}
		})
	}
}
