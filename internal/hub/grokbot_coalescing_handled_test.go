package hub_test

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/auth"
	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/eventlog"
	"github.com/XertroV/vecnet/internal/hub"
)

func (f *t2Fixture) ack(c auth.Caller, filter string) error {
	var seq int64
	t2Must(f.t, f.env.Hub.DB.QueryRow(`SELECT coalesce(max(event_seq),0) FROM deliveries`).Scan(&seq))
	return f.env.Hub.Ack(f.ctx, c, filter, eventlog.Cursor{Epoch: f.env.Hub.Log.Epoch(), Seq: seq})
}
func (f *t2Fixture) processed(c auth.Caller, id string) bool {
	f.t.Helper()
	changed, e := f.env.Hub.ReceiptChanged(f.ctx, c, id, "processed", "")
	t2Must(f.t, e)
	return changed
}
func TestProcessedAndScopedAckSuppress(t *testing.T) {
	for _, scope := range []string{"processed-peer", "processed-principal", "peer/*", "principal/peer", "principal/*", "sibling", "former"} {
		t.Run(scope, func(t *testing.T) {
			f := t2Setup(t)
			id := f.send("wake")
			d := f.claim()
			var prn string
			t2Must(t, f.env.Hub.DB.QueryRow(`SELECT prn_id FROM peers WHERE peer_id=?`, f.b).Scan(&prn))
			peer := auth.Caller{Kind: "peer", PeerID: f.b}
			principal := auth.Caller{Kind: "principal", PrincipalID: prn}
			suppressed := true
			switch scope {
			case "processed-peer":
				if !f.processed(peer, id) {
					t.Fatal("processed unchanged")
				}
			case "processed-principal":
				f.processed(principal, id)
			case "peer/*":
				t2Must(t, f.ack(peer, ""))
			case "principal/peer":
				t2Must(t, f.ack(principal, f.b))
			case "principal/*":
				t2Must(t, f.ack(principal, ""))
			case "sibling":
				s, _ := f.env.NewPeer("session", "sibling")
				t2Must(t, f.env.Hub.MovePeer(f.ctx, s, prn))
				t2Must(t, f.ack(principal, s))
				suppressed = false
			case "former":
				p, _ := f.env.NewPrincipal("new")
				t2Must(t, f.env.Hub.MovePeer(f.ctx, f.b, p))
				if f.ack(principal, f.b) == nil {
					t.Fatal("former principal ack accepted")
				}
				if _, e := f.env.Hub.ReceiptChanged(f.ctx, principal, id, "processed", ""); e == nil {
					t.Fatal("former principal processed accepted")
				}
				t2Must(t, f.ack(principal, ""))
				suppressed = false
			}
			if suppressed {
				if f.covers(id) != 1 {
					t.Fatal("actual unarmed handled work lacks coverage")
				}
				f.w.Push(f.ctx, d)
				f.step()
				f.requests(0)
				if f.scalar(`SELECT count(*) FROM route_runs WHERE route_id=?`, f.route) != 0 {
					t.Fatal("handled work spent budget")
				}
			} else {
				if f.covers(id) != 0 {
					t.Fatal("nonrecipient scope fabricated coverage")
				}
				if scope == "former" {
					f.step()
				} else {
					f.w.Push(f.ctx, d)
				}
				f.requests(1)
			}
		})
	}
}
func TestReadAndAcceptedRunStartNotProcessing(t *testing.T) {
	f := t2Setup(t)
	id := f.send("wake")
	peer := auth.Caller{Kind: "peer", PeerID: f.b}
	t2Must(t, f.env.Hub.Receipt(f.ctx, peer, id, "read", ""))
	t2Must(t, f.env.Hub.Receipt(f.ctx, peer, id, "read", ""))
	if f.covers(id) != 0 {
		t.Fatal("read fabricated coverage")
	}
	f.step()
	f.requests(1)
	if f.covers(id) != 0 {
		t.Fatal("election/accepted run start covered representative")
	}
	if !f.processed(peer, id) {
		t.Fatal("accepted leader not processable")
	}
	if f.covers(id) != 0 {
		t.Fatal("known invoked accepted leader mislabeled no-POST")
	}
	if f.processed(peer, id) {
		t.Fatal("noop processed changed")
	}
	f.env.Now.Advance(hub.PushLease + time.Millisecond)
	f.step()
	f.requests(1)
}
func TestAckDuringPostKeepsExclusion(t *testing.T) {
	for _, kind := range []string{"ack", "processed"} {
		t.Run(kind, func(t *testing.T) {
			f := t2Setup(t)
			id := f.send("wake")
			d := f.claim()
			unblock := f.block(d)
			peer := auth.Caller{Kind: "peer", PeerID: f.b}
			if kind == "ack" {
				t2Must(t, f.ack(peer, ""))
			} else {
				f.processed(peer, id)
			}
			if f.covers(id) != 0 {
				t.Fatal("armed leader mislabeled no-POST while telemetry absent")
			}
			follower := f.send("wake")
			f.step()
			f.requests(1)
			for _, r := range receipts(t, f.env, f.a) {
				if r.DeliveryID == follower {
					t.Fatal("follower got fabricated receipt")
				}
			}
			unblock()
			f.step()
			f.requests(1)
			if f.scalar(`SELECT count(*) FROM grokbot_wakes WHERE route_id=? AND cover_until!=''`, f.route) != 1 {
				t.Fatal("group failed to settle independently of advanced leader")
			}
			f.env.Now.Advance(hub.PushLease + time.Millisecond)
			f.respond = nil
			f.step()
			f.requests(2)
		})
	}
}
func TestBulkCoverExcludesLeaderAndHandledBeforeBeginIsSeparate(t *testing.T) {
	f := t2Setup(t)
	leader := f.send("wake")
	follower := f.send("wake")
	d := f.claim()
	f.send("wake")
	f.step()
	if f.covers(leader) != 0 || f.covers(follower) != 1 {
		t.Fatal("bulk coverage included elected leader or lost follower")
	}
	peer := auth.Caller{Kind: "peer", PeerID: f.b}
	f.processed(peer, leader)
	f.processed(peer, leader)
	if f.covers(leader) != 1 {
		t.Fatal("genuinely handled unarmed leader must have one separate annotation")
	}
	f.w.Push(f.ctx, d)
	f.requests(0)
}
func TestReCoverReplacementKeepsFirstEvidence(t *testing.T) {
	f := t2Setup(t)
	f.send("wake")
	id := f.send("wake")
	f.step()
	f.requests(1)
	var first string
	t2Must(t, f.env.Hub.DB.QueryRow(`SELECT first_covered_at FROM grokbot_covers WHERE route_id=? AND delivery_id=?`, f.route, id).Scan(&first))
	f.env.Now.Advance(hub.PushLease + time.Millisecond)
	f.step()
	f.requests(2)
	f.processed(auth.Caller{Kind: "peer", PeerID: f.b}, id)
	var after string
	t2Must(t, f.env.Hub.DB.QueryRow(`SELECT first_covered_at FROM grokbot_covers WHERE route_id=? AND delivery_id=?`, f.route, id).Scan(&after))
	if first != after || f.covers(id) != 1 {
		t.Fatal("replacement rewrote first evidence")
	}
}
func TestRepairAndExplicitRetryBoundedRecovery(t *testing.T) {
	for _, repair := range []string{"url", "key", "retry", "discard"} {
		t.Run(repair, func(t *testing.T) {
			f := t2Setup(t)
			f.respond = func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(400)
				w.Write([]byte(`{"success":false,"error":"invalid payload"}`))
			}
			leader := f.send("wake")
			f.step()
			f.requests(1)
			if s, _ := deliveryState(t, f.env, leader); s != "dead_letter" {
				t.Fatalf("leader=%s", s)
			}
			follower := f.send("wake")
			f.step()
			f.requests(1)
			var deadline string
			t2Must(t, f.env.Hub.DB.QueryRow(`SELECT retry_at FROM grokbot_wakes WHERE route_id=?`, f.route).Scan(&deadline))
			switch repair {
			case "url":
				u := f.srv.URL + "/other"
				t2Must(t, f.env.Hub.UpdateRoute(f.ctx, f.route, hub.RouteUpdate{URL: &u}))
			case "key":
				t2Must(t, f.env.Hub.RotateGrokbotKey(f.ctx, f.route, t2Key))
			case "retry":
				t2Must(t, f.env.Hub.RetryDeadLetter(f.ctx, leader))
			case "discard":
				t2Must(t, f.env.Hub.DiscardDeadLetter(f.ctx, leader))
				f.step()
				f.requests(1)
				at, e := envelope.ParseTime(deadline)
				t2Must(t, e)
				f.env.Now.Advance(at.Sub(f.env.Now.Now()) + time.Millisecond)
			}
			f.respond = nil
			f.step()
			f.requests(2)
			if f.covers(follower) != 1 {
				t.Fatal("follower coverage lost")
			}
		})
	}
}
func TestSharedFiniteFailureCadenceAfterLeaderLoss(t *testing.T) {
	f := t2Setup(t)
	f.respond = func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		fmt.Fprint(w, `{"message":"Invalid API key"}`)
	}
	id := f.send("wake")
	f.step()
	f.requests(1)
	t2Must(t, f.env.Hub.DiscardDeadLetter(f.ctx, id))
	later := f.send("wake")
	f.step()
	f.requests(1)
	var until string
	t2Must(t, f.env.Hub.DB.QueryRow(`SELECT retry_at FROM grokbot_wakes WHERE route_id=?`, f.route).Scan(&until))
	at, e := envelope.ParseTime(until)
	t2Must(t, e)
	f.env.Now.Advance(at.Sub(f.env.Now.Now()) + time.Millisecond)
	f.step()
	f.requests(2)
	if s, _ := deliveryState(t, f.env, later); s != "dead_letter" {
		t.Fatalf("replacement=%s", s)
	}
	if f.scalar(`SELECT failure_ordinal FROM grokbot_wakes WHERE route_id=?`, f.route) != 2 {
		t.Fatal("replacement reset shared cadence")
	}
}

// Pause only the next BeginTx before SQLite takes its writer lock. The peer
// move commits on another connection while Ack is waiting; its scope must be
// resolved from the transaction that eventually wins the lock.
type t2AckConnector struct {
	driver           driver.Driver
	dsn              string
	pause            atomic.Bool
	entered, release chan struct{}
}

func (c *t2AckConnector) Driver() driver.Driver { return c.driver }
func (c *t2AckConnector) Connect(context.Context) (driver.Conn, error) {
	conn, e := c.driver.Open(c.dsn)
	if e != nil {
		return nil, e
	}
	return &t2AckConn{Conn: conn, gate: c}, nil
}

type t2AckConn struct {
	driver.Conn
	gate *t2AckConnector
}

func (c *t2AckConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	if c.gate.pause.CompareAndSwap(true, false) {
		close(c.gate.entered)
		select {
		case <-c.gate.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return c.Conn.(driver.ConnBeginTx).BeginTx(ctx, opts)
}
func (c *t2AckConn) QueryContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	return c.Conn.(driver.QueryerContext).QueryContext(ctx, q, args)
}
func (c *t2AckConn) ExecContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Result, error) {
	return c.Conn.(driver.ExecerContext).ExecContext(ctx, q, args)
}
func TestAckResolvesCurrentAuthorityInWriter(t *testing.T) {
	for _, narrow := range []bool{false, true} {
		t.Run(fmt.Sprint(narrow), func(t *testing.T) {
			f := t2Setup(t)
			id := f.send("wake")
			old := f.caller(f.b).PrincipalID
			next, _ := f.env.NewPrincipal("next")
			var seq int64
			t2Must(t, f.env.Hub.DB.QueryRow(`SELECT event_seq FROM deliveries WHERE dly_id=?`, id).Scan(&seq))
			original := f.env.Hub.DB.DB
			g := &t2AckConnector{driver: original.Driver(), dsn: "file:" + filepath.Join(f.env.Hub.Cfg.DataDir, "hub.db") + "?_txlock=immediate&_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(ON)", entered: make(chan struct{}), release: make(chan struct{})}
			wrapped := sql.OpenDB(g)
			wrapped.SetMaxOpenConns(2)
			f.env.Hub.DB.DB = wrapped
			t.Cleanup(func() { wrapped.Close(); f.env.Hub.DB.DB = original })
			g.pause.Store(true)
			done := make(chan error, 1)
			filter := ""
			if narrow {
				filter = f.b
			}
			go func() {
				done <- f.env.Hub.Ack(f.ctx, auth.Caller{Kind: "principal", PrincipalID: old}, filter, eventlog.Cursor{Epoch: f.env.Hub.Log.Epoch(), Seq: seq})
			}()
			var once sync.Once
			release := func() { once.Do(func() { close(g.release) }) }
			t.Cleanup(release)
			select {
			case <-g.entered:
			case <-time.After(5 * time.Second):
				t.Fatal("Ack did not reach writer boundary")
			}
			t2Must(t, f.env.Hub.MovePeer(f.ctx, f.b, next))
			release()
			select {
			case e := <-done:
				if narrow && e == nil {
					t.Fatal("former narrowed principal accepted after move")
				}
				if !narrow {
					t2Must(t, e)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("Ack did not finish")
			}
			if state, _ := deliveryState(t, f.env, id); state != "queued" {
				t.Fatalf("former principal advanced moved delivery: %s", state)
			}
			if f.covers(id) != 0 {
				t.Fatal("stale principal fabricated handled evidence")
			}
			f.step()
			f.requests(1)
		})
	}
}
func TestHandledUntouchedAndLegacyControls(t *testing.T) {
	for _, mode := range []string{hub.GrokbotTransportRecipientDrainV1, hub.GrokbotTransportDeliveryV1} {
		for _, operation := range []string{"processed", "ack"} {
			t.Run(mode+"/"+operation, func(t *testing.T) {
				f := t2Setup(t)
				t2Must(t, f.env.Hub.UpdateRoute(f.ctx, f.route, hub.RouteUpdate{GrokbotTransport: &mode}))
				f.presence("working")
				id := f.send("wake_idle")
				peer := f.caller(f.b)
				if operation == "ack" {
					t2Must(t, f.ack(peer, ""))
					t2Must(t, f.ack(peer, ""))
				} else {
					f.processed(peer, id)
					if f.processed(peer, id) {
						t.Fatal("noop receipt changed")
					}
				}
				want := 0
				if mode == hub.GrokbotTransportRecipientDrainV1 {
					want = 1
				}
				if f.covers(id) != want {
					t.Fatalf("coverage=%d want %d", f.covers(id), want)
				}
				f.step()
				f.requests(0)
				if rs := receipts(t, f.env, f.a); len(rs) != 1 || rs[0].DeliveryID != id {
					t.Fatalf("handling receipts=%+v", rs)
				}
			})
		}
	}
}

func TestExplicitRetryDuringPostRetainsLease(t *testing.T) {
	f := t2Setup(t)
	f.respond = func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
		fmt.Fprint(w, `{"success":false,"error":"invalid payload"}`)
	}
	old := f.send("wake")
	f.step()
	f.requests(1)
	var retry string
	t2Must(t, f.env.Hub.DB.QueryRow(`SELECT retry_at FROM grokbot_wakes WHERE route_id=?`, f.route).Scan(&retry))
	at, e := envelope.ParseTime(retry)
	t2Must(t, e)
	f.env.Now.Advance(at.Sub(f.env.Now.Now()) + time.Millisecond)
	f.send("wake")
	d := f.claim()
	deadline := f.env.Now.Now().Add(hub.PushLease)
	unblock := f.block(d)
	f.env.Now.Advance(time.Minute)
	t2Must(t, f.env.Hub.RetryDeadLetter(f.ctx, old))
	f.step()
	f.requests(2)
	unblock()
	f.respond = nil
	var lease string
	t2Must(t, f.env.Hub.DB.QueryRow(`SELECT lease_until FROM grokbot_wakes WHERE route_id=? AND token=? AND cover_until='' AND cover_valid=0`, f.route, d.GrokWake.Token).Scan(&lease))
	if lease != envelope.FormatTime(deadline) {
		t.Fatal("explicit retry changed original armed lease")
	}
	f.step()
	f.requests(2)
	f.env.Now.Advance(deadline.Sub(f.env.Now.Now()) + time.Millisecond)
	f.step()
	f.requests(3)
	if f.scalar(`SELECT count(*) FROM route_runs WHERE route_id=?`, f.route) != 2 {
		t.Fatal("retry transferred or duplicated reservation")
	}
}

type t2RoundTripFunc func(*http.Request) (*http.Response, error)

func (f t2RoundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestAmbiguousHandledLeaderIsNotNoPost(t *testing.T) {
	for _, operation := range []string{"processed", "ack"} {
		t.Run(operation, func(t *testing.T) {
			f := t2Setup(t)
			f.client.base = t2RoundTripFunc(func(r *http.Request) (*http.Response, error) {
				resp, e := http.DefaultTransport.RoundTrip(r)
				if e != nil {
					return nil, e
				}
				io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
				return nil, io.ErrUnexpectedEOF
			})
			id := f.send("wake")
			f.step()
			f.requests(1)
			if f.scalar(`SELECT count(*) FROM grokbot_wakes WHERE route_id=? AND phase='waiting' AND lease_until>?`, f.route, envelope.FormatTime(f.env.Now.Now())) != 1 {
				t.Fatal("ambiguity did not retain exclusion")
			}
			peer := f.caller(f.b)
			if operation == "ack" {
				t2Must(t, f.ack(peer, ""))
			} else {
				f.processed(peer, id)
			}
			if f.covers(id) != 0 {
				t.Fatal("ambiguous invoked leader mislabeled no-POST")
			}
			later := f.send("wake")
			f.step()
			f.requests(1)
			for _, r := range receipts(t, f.env, f.a) {
				if r.DeliveryID == later {
					t.Fatal("ambiguous group fabricated follower receipt")
				}
			}
			f.client.base = http.DefaultTransport
			var until string
			t2Must(t, f.env.Hub.DB.QueryRow(`SELECT max(lease_until,retry_at) FROM grokbot_wakes WHERE route_id=?`, f.route).Scan(&until))
			at, e := envelope.ParseTime(until)
			t2Must(t, e)
			f.env.Now.Advance(at.Sub(f.env.Now.Now()) + time.Millisecond)
			f.step()
			f.requests(2)
		})
	}
}
