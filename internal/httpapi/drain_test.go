package httpapi

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/XertroV/vecnet/internal/eventlog"
	"github.com/XertroV/vecnet/internal/hub"
	"github.com/XertroV/vecnet/internal/store"
)

func TestDrainGenerationAndWait(t *testing.T) {
	d := NewDrain()
	initial := d.C()
	select {
	case <-initial:
		t.Fatal("new drain announced")
	default:
	}
	if d.RetryAfter() != DefaultRestartRetry {
		t.Fatalf("default retry = %v", d.RetryAfter())
	}
	done := d.Join()
	if d.Wait(time.Millisecond) {
		t.Fatal("wait ignored joined stream")
	}
	d.Announce(1500 * time.Millisecond)
	d.Announce(9 * time.Second)
	select {
	case <-initial:
	default:
		t.Fatal("announce did not close generation")
	}
	if d.RetryAfter() != 1500*time.Millisecond {
		t.Fatalf("repeat announce changed retry to %v", d.RetryAfter())
	}
	done()
	done()
	if !d.Wait(time.Second) {
		t.Fatal("completed stream did not leave drain")
	}
	d.Reset()
	if d.C() == initial {
		t.Fatal("reset reused generation")
	}
	select {
	case <-d.C():
		t.Fatal("reset generation already announced")
	default:
	}
	if d.RetryAfter() != DefaultRestartRetry {
		t.Fatalf("reset retry = %v", d.RetryAfter())
	}
	// A late completion of the old generation cannot complete a new stream.
	late := d.Join()
	d.Announce(0)
	if d.Wait(time.Millisecond) {
		t.Fatal("unfinished old generation completed")
	}
	d.Reset()
	next := d.Join()
	late()
	if d.Wait(time.Millisecond) {
		t.Fatal("old completion drained new generation")
	}
	next()
	if !d.Wait(time.Second) {
		t.Fatal("new generation did not finish")
	}
}

func TestDrainNilMethods(t *testing.T) {
	var d *Drain
	if d.C() != nil {
		t.Fatal("nil drain channel fires")
	}
	d.Announce(time.Second)
	d.Join()()
	if !d.Wait(0) {
		t.Fatal("nil drain wait did not finish")
	}
	d.Reset()
}

func TestDrainConcurrentJoinAnnounceWait(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		d := NewDrain()
		anchor := d.Join()
		waited := make(chan bool, 1)
		go func() { waited <- d.Wait(time.Minute) }()
		synctest.Wait() // Wait is parked before any additional stream joins.

		joined := make(chan struct{}, 32)
		release := make(chan struct{})
		var wg sync.WaitGroup
		for i := 0; i < cap(joined); i++ {
			wg.Go(func() {
				done := d.Join()
				joined <- struct{}{}
				<-release
				done()
			})
		}
		for i := 0; i < cap(joined); i++ {
			<-joined
		}
		final := d.Join()
		d.Announce(time.Second)
		anchor()
		close(release)
		wg.Wait()
		synctest.Wait() // All other completions overlap the original Wait.
		select {
		case result := <-waited:
			t.Fatalf("Wait returned %v before the final completion", result)
		default:
		}
		final()
		if !<-waited {
			t.Fatal("concurrent streams did not finish")
		}
	})
}

func TestDrainFinishFlushesQueuedFrames(t *testing.T) {
	w := httptest.NewRecorder()
	c := startSSE(w, time.Second)
	defer c.close()
	if !c.send(context.Background(), []byte("event: ready\ndata: {}\n\n")) {
		t.Fatal("preamble send failed")
	}
	c.finish(restartFrame(1500*time.Millisecond), time.Second)
	want := "event: ready\ndata: {}\n\nevent: hub_restarting\nretry: 1500\ndata: {\"retry_after_ms\":1500}\n\n"
	if w.Body.String() != want || !w.Flushed {
		t.Fatalf("flushed bytes = %q, flushed=%v", w.Body.String(), w.Flushed)
	}
	if c.send(context.Background(), []byte("data: after\n\n")) {
		t.Fatal("finish left writer open")
	}
}

func TestDrainFinishBoundsStuckWriter(t *testing.T) {
	w := newStuckWriter()
	c := startSSE(w, time.Hour)
	defer c.close()
	for i := 0; i <= sseBuffer; i++ {
		if !c.send(context.Background(), []byte("data: queued\n\n")) {
			t.Fatal("queue failed")
		}
	}
	finished := make(chan struct{})
	start := time.Now()
	go func() { c.finish(restartFrame(time.Second), 20*time.Millisecond); close(finished) }()
	select {
	case <-finished:
		if time.Since(start) > time.Second {
			t.Fatal("finish exceeded bound")
		}
	case <-time.After(time.Second):
		t.Fatal("finish blocked behind full queue")
	}
}

func TestDrainInterruptsBackpressuredSend(t *testing.T) {
	w := newStuckWriter()
	c := startSSE(w, time.Hour)
	defer c.close()
	drain := NewDrain()
	c.draining = drain.C()
	for i := 0; i <= sseBuffer; i++ {
		if !c.send(context.Background(), []byte("data: queued\n\n")) {
			t.Fatal("queue failed")
		}
	}
	result := make(chan bool, 1)
	go func() { result <- c.send(context.Background(), []byte("data: blocked\n\n")) }()
	drain.Announce(time.Second)
	select {
	case sent := <-result:
		if sent {
			t.Fatal("send succeeded after restart")
		}
	case <-time.After(time.Second):
		t.Fatal("restart did not interrupt backpressure")
	}
}

// restartBarrierWriter pauses setup after registration but before startSSE
// returns, where the handler used to capture the current drain channel.
type restartBarrierWriter struct {
	*httptest.ResponseRecorder
	entered chan struct{}
	release chan struct{}
}

func (w *restartBarrierWriter) WriteHeader(code int) {
	close(w.entered)
	<-w.release
	w.ResponseRecorder.WriteHeader(code)
}

func restartTestServer(t *testing.T) *server {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	log, err := eventlog.New(db)
	if err != nil {
		t.Fatal(err)
	}
	return &server{Deps: Deps{Hub: &hub.Hub{Log: log}, Drain: NewDrain()}}
}

func TestRestartKeepsGenerationAcrossReset(t *testing.T) {
	for _, mode := range []string{"sse_tail", "sse_setup", "activity_setup", "long_poll"} {
		t.Run(mode, func(t *testing.T) {
			s := restartTestServer(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			r := httptest.NewRequest("GET", "/", nil).WithContext(ctx)
			recorder := httptest.NewRecorder()
			entered, release := make(chan struct{}), make(chan struct{})
			var releaseOnce sync.Once
			unblock := func() { releaseOnce.Do(func() { close(release) }) }
			defer unblock()
			var w http.ResponseWriter = recorder
			if strings.HasSuffix(mode, "setup") {
				w = &restartBarrierWriter{recorder, entered, release}
			}
			// sseTail calls alive again on each later wake or tick; only the
			// first call is the barrier.
			var enterOnce sync.Once
			alive := func(context.Context) bool { enterOnce.Do(func() { close(entered) }); <-release; return true }
			finished := make(chan struct{})
			go func() {
				defer close(finished)
				switch mode {
				case "activity_setup":
					s.opActivity(w, r)
				case "long_poll":
					s.longPoll(w, r, "", nil, eventlog.Head, nil, 25*time.Second, alive)
				default:
					s.streamSSE(w, r, "", nil, eventlog.Head, nil, alive)
				}
			}()
			select {
			case <-entered:
			case <-time.After(5 * time.Second):
				t.Fatal("handler did not reach barrier")
			}
			s.Drain.Announce(1500 * time.Millisecond)
			if s.Drain.Wait(time.Millisecond) {
				t.Fatal("wait completed while handler was blocked")
			}
			s.Drain.Reset()
			// An independent announcement makes a setup generation mix-up
			// fail promptly with the wrong hint, rather than hang forever.
			if mode != "sse_tail" {
				s.Drain.Announce(2500 * time.Millisecond)
			}
			unblock()
			select {
			case <-finished:
			case <-time.After(5 * time.Second):
				t.Fatal("old handler missed its announcement after Reset")
			}
			want := "event: hub_restarting\nretry: 1500\ndata: {\"retry_after_ms\":1500}\n\n"
			if mode == "long_poll" {
				want = "\"hub_restarting\":{\"retry_after_ms\":1500}"
			}
			if got := recorder.Body.String(); !strings.Contains(got, want) || strings.Count(got, "hub_restarting") != 1 {
				t.Fatalf("restart response = %q; want exactly one %q", got, want)
			}
		})
	}
}

// The alive callback runs immediately after Log.Wait registers the poll. It
// provides a channel barrier without a wall-clock readiness polling loop.
func TestLongPollRestartField(t *testing.T) {
	s := restartTestServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	r := httptest.NewRequest("GET", "/v1/events?wait=25", nil).WithContext(ctx)
	w := httptest.NewRecorder()
	cur := eventlog.Cursor{Epoch: s.Hub.Log.Epoch()}
	registered, finished := make(chan struct{}), make(chan struct{})
	var once sync.Once
	alive := func(context.Context) bool {
		once.Do(func() { close(registered) })
		return true
	}
	go func() {
		defer close(finished)
		s.longPoll(w, r, "poll-owner", nil, cur, nil, 25*time.Second, alive)
	}()
	select {
	case <-registered:
	case <-ctx.Done():
		t.Fatal("long poll did not register")
	}
	if s.Hub.Log.Waiters("poll-owner") != 1 {
		t.Fatal("long poll is not registered")
	}
	s.Drain.Announce(1500 * time.Millisecond)
	select {
	case <-finished:
		want := `{"events":[],"next_cursor":"` + cur.String() + `","hub_restarting":{"retry_after_ms":1500}}` + "\n"
		if w.Code != 200 || w.Body.String() != want {
			t.Fatalf("restart poll = %d %s, want %s", w.Code, w.Body, want)
		}
	case <-time.After(time.Second):
		t.Fatal("parked poll ignored restart for 1s")
	}
	if !s.Drain.Wait(time.Second) {
		t.Fatal("poll did not leave drain")
	}
}

// A wake can park in credential checks while shutdown is announced and a
// worker commits a delivery. The subsequent nonempty reread must still answer
// with the held poll's original cursor and its pinned generation's retry hint.
func TestLongPollRestartOverridesRereadEvents(t *testing.T) {
	for _, reset := range []bool{false, true} {
		name := "announced"
		if reset {
			name = "reset"
		}
		t.Run(name, func(t *testing.T) {
			s := restartTestServer(t)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			const owner = "poll-owner"
			owners := []string{owner}
			cur, err := s.Hub.Log.Current(ctx, owners)
			if err != nil {
				t.Fatal(err)
			}
			first, err := s.Hub.Log.Read(ctx, owners, cur, eventsReadLimit)
			if err != nil || len(first) != 0 {
				t.Fatalf("initial read = %v, %v; want empty", first, err)
			}
			r := httptest.NewRequest("GET", "/v1/events?wait=25", nil).WithContext(ctx)
			w := httptest.NewRecorder()
			registered, blocked := make(chan struct{}), make(chan struct{})
			release, finished := make(chan struct{}), make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			defer unblock()
			calls := 0
			alive := func(context.Context) bool {
				calls++
				switch calls {
				case 1:
					close(registered)
				case 2:
					close(blocked)
					select {
					case <-release:
					case <-ctx.Done():
						return false
					}
				}
				return true
			}
			go func() {
				defer close(finished)
				s.longPoll(w, r, "poll-principal", owners, cur, first, 25*time.Second, alive)
			}()
			select {
			case <-registered:
			case <-ctx.Done():
				t.Fatal("long poll did not register")
			}
			s.Hub.Log.Notify(owner) // Wake the established hold without an event.
			select {
			case <-blocked:
			case <-ctx.Done():
				t.Fatal("long poll did not reach its second credential check")
			}
			if s.Hub.Log.Waiters(owner) != 1 {
				t.Fatal("long poll is not registered at the barrier")
			}
			s.Drain.Announce(1500 * time.Millisecond)
			if reset {
				if s.Drain.Wait(time.Millisecond) {
					t.Fatal("wait completed while poll was blocked")
				}
				s.Drain.Reset()
				s.Drain.Announce(2500 * time.Millisecond)
			}
			err = s.Hub.Log.DB.Tx(ctx, func(tx *sql.Tx) error {
				_, err := s.Hub.Log.Append(ctx, tx, owner, "delivery", time.Now(), map[string]string{"message": "during drain"})
				return err
			})
			if err != nil {
				t.Fatal(err)
			}
			s.Hub.Log.Notify(owner)
			if evs, err := s.Hub.Log.Read(ctx, owners, cur, eventsReadLimit); err != nil || len(evs) != 1 {
				t.Fatalf("committed read = %v, %v; want one event", evs, err)
			}
			unblock()
			select {
			case <-finished:
				want := `{"events":[],"next_cursor":"` + cur.String() + `","hub_restarting":{"retry_after_ms":1500}}` + "\n"
				if w.Code != http.StatusOK || w.Body.String() != want {
					t.Fatalf("restart poll = %d %s, want %s", w.Code, w.Body, want)
				}
			case <-time.After(time.Second):
				t.Fatal("held poll did not finish after the credential check")
			}
		})
	}
}
