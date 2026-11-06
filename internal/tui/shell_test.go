package tui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/opapi"
	"github.com/XertroV/vecnet/internal/opclient"
	"github.com/XertroV/vecnet/internal/tui/theme"
)

const tabBar = "vecnet | 1:inbox 2:chat 3:dash 4:subs"

func seeded() *fakeAPI {
	f := newFake()
	f.inbox = opapi.Inbox{
		Now:         envelope.FormatTime(fixedNow),
		Me:          opapi.Me{PeerID: "peer_op", Name: "Max", Presence: "working"},
		Approvals:   []opapi.OpenApproval{{ApprovalID: "apr_1"}, {ApprovalID: "apr_2"}},
		DeadLetters: []opapi.DeadLetter{{DeliveryID: "dly_1"}, {DeliveryID: "dly_2"}, {DeliveryID: "dly_3"}},
	}
	f.cuts = opapi.LoopCuts{Total: 7}
	return f
}

// loaded returns a model with static screens after its first load.
func loaded(t *testing.T, f *fakeAPI, clock *testClock) (*Model, *pump) {
	t.Helper()
	m := newTestModel(t, f, clock)
	m.screens = staticScreens()
	p := newPump(t, m)
	p.run(m.Init())
	p.until("first load", func() bool {
		return !m.firstLoading() && (m.Data.Inbox.Me.PeerID == "" || m.Data.MineKnown)
	})
	p.until("live", func() bool { return m.Conn == ConnLive })
	p.idle()
	return m, p
}

func assertFrame(t *testing.T, frame string, w, h int) {
	t.Helper()
	lines := strings.Split(frame, "\n")
	if len(lines) != h {
		t.Fatalf("frame has %d lines, want %d", len(lines), h)
	}
	for i, line := range lines {
		if got := lipgloss.Width(line); got != w {
			t.Fatalf("line %d is %d cells, want %d: %q", i, got, w, line)
		}
	}
}

func TestTabsResizeTooSmall(t *testing.T) {
	m, p := loaded(t, seeded(), newClock())
	for i, title := range []string{"inbox", "chat", "dash", "subs"} {
		p.press(fmt.Sprint(i + 1))
		if m.Tab != i {
			t.Fatalf("key %d: Tab = %d", i+1, m.Tab)
		}
		frame := plainAt(t, m, 140, 42)
		if !strings.Contains(frame, tabBar) {
			t.Fatalf("tab bar missing:\n%s", frame)
		}
		if !strings.Contains(frame, "body of "+title) {
			t.Fatalf("tab %d shows the wrong screen:\n%s", i+1, frame)
		}
		assertFrame(t, frame, 140, 42)
	}
	p.press("1")

	t.Run("140x42", func(t *testing.T) { assertFrame(t, renderAt(t, m, 140, 42), 140, 42) })
	t.Run("80x24", func(t *testing.T) { assertFrame(t, renderAt(t, m, 80, 24), 80, 24) })

	for _, size := range [][2]int{{79, 23}, {79, 24}, {80, 23}, {40, 10}, {79, 3}, {80, 3}, {79, 2}, {79, 4}} {
		frame := plainAt(t, m, size[0], size[1])
		if !strings.Contains(frame, "terminal too small: need 80x24") {
			t.Errorf("%dx%d: no too-small line:\n%s", size[0], size[1], frame)
		}
		if strings.Contains(frame, "body of inbox") {
			t.Errorf("%dx%d: body still drawn:\n%s", size[0], size[1], frame)
		}
		assertFrame(t, frame, size[0], size[1])
	}
	if frame := plainAt(t, m, 79, 2); !strings.Contains(frame, "terminal too small: need 80x24") {
		t.Errorf("79x2: no too-small line: %q", frame)
	}
	if frame := plainAt(t, m, 80, 24); strings.Contains(frame, "too small") {
		t.Errorf("80x24 is the minimum and must render:\n%s", frame)
	}
}

func TestTopBarAndStatusLine(t *testing.T) {
	clock := newClock()
	m, _ := loaded(t, seeded(), clock)
	frame := plainAt(t, m, 140, 42)
	lines := strings.Split(frame, "\n")
	for _, want := range []string{"hub hub.example:8443", "live", "apr 2", "dlq 3", "cuts/24h 7", "07:41:00"} {
		if !strings.Contains(lines[0], want) {
			t.Errorf("top bar lacks %q: %q", want, lines[0])
		}
	}
	for _, want := range []string{"NORMAL", "j/k move  a answer", "max@vecnet · working · prio wake"} {
		if !strings.Contains(lines[40], want) {
			t.Errorf("status line lacks %q: %q", want, lines[40])
		}
	}
	// Presence is shown as the hub reports it; unknown never reads as offline.
	m.Data.Inbox.Me.Presence = "unknown"
	if line := strings.Split(plainAt(t, m, 140, 42), "\n")[40]; !strings.Contains(line, "max@vecnet · unknown") || strings.Contains(line, "offline") {
		t.Errorf("unknown presence: %q", line)
	}
}

func TestModesAndKeys(t *testing.T) {
	t.Run("command mode", func(t *testing.T) {
		m, p := loaded(t, seeded(), newClock())
		p.press(":", "h", "i")
		if m.Mode != ModeCommand {
			t.Fatalf("Mode = %v", m.Mode)
		}
		frame := plainAt(t, m, 140, 42)
		if !strings.Contains(frame, "COMMAND") || !strings.Contains(frame, ":hi") {
			t.Fatalf("command line not shown:\n%s", frame)
		}
		p.press("1", "q")
		if m.Tab != 0 {
			t.Fatal("digits typed on the command line must not switch tabs")
		}
		p.press("esc")
		if m.Mode != ModeNormal {
			t.Fatalf("esc: Mode = %v", m.Mode)
		}
	})
	t.Run("help", func(t *testing.T) {
		m, p := loaded(t, seeded(), newClock())
		p.press("?")
		if frame := plainAt(t, m, 140, 42); !strings.Contains(frame, "ctrl+l") || strings.Contains(frame, "body of inbox") {
			t.Fatalf("help not shown:\n%s", frame)
		}
		p.press("esc")
		if frame := plainAt(t, m, 140, 42); !strings.Contains(frame, "body of inbox") {
			t.Fatalf("help did not close:\n%s", frame)
		}
	})
	t.Run("insert sends every printable key to the screen", func(t *testing.T) {
		m, p := loaded(t, seeded(), newClock())
		m.Mode = ModeInsert
		p.press("q", "1", ":", "?")
		screen := m.screens[0].(*staticScreen)
		if got := strings.Join(screen.keys, ""); got != "q1:?" {
			t.Fatalf("screen keys %q", got)
		}
		if m.Tab != 0 || m.Mode != ModeInsert || m.ctx.Err() != nil {
			t.Fatalf("Insert keys reached the shell: tab %d mode %v", m.Tab, m.Mode)
		}
	})
	t.Run("quit confirms in insert", func(t *testing.T) {
		for _, key := range []string{"ctrl+c", "ctrl+q"} {
			m, p := loaded(t, seeded(), newClock())
			m.Mode = ModeInsert
			p.press(key)
			if m.ctx.Err() != nil {
				t.Fatal("quit without confirmation")
			}
			if frame := plainAt(t, m, 140, 42); !strings.Contains(frame, "quit and lose this draft? y/n") {
				t.Fatalf("no confirm prompt:\n%s", frame)
			}
			p.press("n")
			if m.ctx.Err() != nil || m.Mode != ModeInsert {
				t.Fatal("n must return to Insert")
			}
			if len(m.screens[0].(*staticScreen).keys) != 0 {
				t.Fatal("the confirm answer leaked into the screen")
			}
			p.press(key, "y")
			if m.ctx.Err() == nil {
				t.Fatal("y must quit")
			}
		}
	})
	t.Run("q is immediate in normal", func(t *testing.T) {
		m, p := loaded(t, seeded(), newClock())
		p.press("q")
		if m.ctx.Err() == nil {
			t.Fatal("q did not quit")
		}
	})
	t.Run("ctrl+l reloads everything", func(t *testing.T) {
		f := seeded()
		_, p := loaded(t, f, newClock())
		f.reset()
		p.press("ctrl+l")
		names := []string{"Inbox", "Threads", "Peers", "Subscriptions", "LoopCuts", "DeliveryHealth", "Audit", "Counters"}
		p.loaded(f, names...)
		for _, name := range names {
			want := 1
			if name == "Threads" {
				// The unfiltered page, then Your Threads and Other Threads.
				want = 3
			}
			if f.count(name) != want {
				t.Errorf("%s loaded %d times, want %d", name, f.count(name), want)
			}
		}
	})
	t.Run("unclaimed keys reach the active screen", func(t *testing.T) {
		m, p := loaded(t, seeded(), newClock())
		p.press("2", "j")
		if got := m.screens[1].(*staticScreen).keys; len(got) != 1 || got[0] != "j" {
			t.Fatalf("screen keys %q", got)
		}
	})
}

func TestStatusConn(t *testing.T) {
	render := func(t *testing.T, m *Model, want string) {
		t.Helper()
		wide := strings.Split(plainAt(t, m, 140, 42), "\n")[0]
		narrow := strings.Split(plainAt(t, m, 80, 24), "\n")[0]
		if !strings.Contains(wide, want) || !strings.Contains(wide, profileHubLabel(m.Profile, m.Hub)) {
			t.Errorf("140x42 top bar lacks %q: %q", want, wide)
		}
		// 80 columns keep the profile, the host and the connection state.
		// A long state uses the short profile·host form.
		host := hubHost(m.Hub)
		if i := strings.IndexByte(host, ':'); i > 0 {
			host = host[:i]
		}
		if !strings.Contains(narrow, want) || !strings.Contains(narrow, m.Profile) || !strings.Contains(narrow, host) {
			t.Errorf("80x24 top bar lacks %q: %q", want, narrow)
		}
	}
	f := seeded()
	release := make(chan struct{})
	f.tail = func(ctx context.Context, _ func(opclient.Batch) error, status func(string)) error {
		status("connecting")
		<-release
		status("live")
		<-release
		status("offline")
		<-release
		status("live")
		<-release
		return opclient.ErrUnauthorized
	}
	m := newTestModel(t, f, newClock())
	m.screens = staticScreens()
	p := newPump(t, m)
	if m.Conn != ConnConnecting {
		t.Fatalf("initial Conn = %q", m.Conn)
	}
	p.run(m.Init())
	p.until("connecting", func() bool { return !m.firstLoading() })
	render(t, m, "connecting")

	release <- struct{}{}
	p.until("live", func() bool { return m.Conn == ConnLive })
	render(t, m, "live")

	release <- struct{}{}
	p.until("offline", func() bool { return m.Conn == ConnOffline })
	render(t, m, "reconnecting")
	if m.Data.Inbox.Me.Name != "Max" {
		t.Fatal("offline must keep the last data")
	}

	// Coming back refetches what the outage may have hidden.
	f.reset()
	release <- struct{}{}
	p.until("back live", func() bool { return m.Conn == ConnLive })
	p.loaded(f, "Inbox", "Threads", "Peers", "Subscriptions", "LoopCuts", "DeliveryHealth", "Audit", "Counters")

	release <- struct{}{}
	p.until("rejected", func() bool { return m.Conn == ConnRejected })
	render(t, m, "credential rejected")
	if m.Data.Inbox.Me.Name != "Max" {
		t.Fatal("a rejected credential must keep the last data")
	}
	if frame := plainAt(t, m, 140, 42); !strings.Contains(frame, "body of inbox") {
		t.Fatalf("last data hidden:\n%s", frame)
	}

	// No retry storm: a rejected credential stops every load, and a late
	// status cannot paper over it.
	f.reset()
	p.press("ctrl+l")
	p.send(tailStatusMsg{status: "live"})
	if m.Conn != ConnRejected {
		t.Fatalf("Conn = %q after a late status", m.Conn)
	}
	p.idle()
	if len(m.inflight) != 0 || f.count("Inbox") != 0 {
		t.Fatalf("reload after rejection: %d", f.count("Inbox"))
	}
}

// The hub disabling the operator stops the tail like a rejected credential:
// the top bar says so, the last data stays, and ctrl+l loads nothing.
func TestStatusConnDisabled(t *testing.T) {
	f := seeded()
	// The disable waits for the first load, so the last data is there to keep.
	disable := make(chan struct{})
	f.tail = func(ctx context.Context, _ func(opclient.Batch) error, status func(string)) error {
		status("live")
		select {
		case <-disable:
		case <-ctx.Done():
			return ctx.Err()
		}
		return opclient.ErrDisabled
	}
	m := newTestModel(t, f, newClock())
	m.screens = staticScreens()
	p := newPump(t, m)
	p.run(m.Init())
	p.until("first load", func() bool { return !m.firstLoading() })
	p.idle()
	close(disable)
	p.until("disabled", func() bool { return m.Conn == ConnDisabled })
	p.idle()
	for _, size := range [][2]int{{140, 42}, {80, 24}} {
		if line := strings.Split(plainAt(t, m, size[0], size[1]), "\n")[0]; !strings.Contains(line, "operator disabled") {
			t.Errorf("%dx%d top bar: %q", size[0], size[1], line)
		}
	}
	if frame := plainAt(t, m, 140, 42); !strings.Contains(frame, "body of inbox") {
		t.Fatalf("last data hidden:\n%s", frame)
	}
	f.reset()
	p.press("ctrl+l")
	p.idle()
	if len(m.inflight) != 0 || f.count("Inbox") != 0 {
		t.Fatalf("reload after disable: %d", f.count("Inbox"))
	}
	if !strings.Contains(m.toast.text, "restart with a valid profile") {
		t.Fatalf("toast %q", m.toast.text)
	}
}

// A disable that lands before the first reload runs suppresses every load; the
// first load must still settle, or the body would wait on it forever.
func TestStatusConnDisabledBeforeFirstLoad(t *testing.T) {
	for _, done := range []error{opclient.ErrDisabled, opclient.ErrUnauthorized} {
		t.Run(done.Error(), func(t *testing.T) {
			f := seeded()
			m := newTestModel(t, f, newClock())
			m.screens = staticScreens()
			p := newPump(t, m)
			// Init's commands are not run, so the order is fixed: the tail ends
			// before the first reload message is handled.
			_ = m.Init()
			p.send(tailDoneMsg{err: done})
			if !m.stopped() {
				t.Fatalf("Conn = %q", m.Conn)
			}
			p.send(reloadMsg{tags: []string{TagAll}})
			if m.firstLoading() {
				t.Fatalf("first load still open: %v", m.first)
			}
			if len(m.inflight) != 0 || f.count("Inbox") != 0 {
				t.Fatalf("loads after stop: %d", f.count("Inbox"))
			}
			frame := plainAt(t, m, 140, 42)
			if strings.Contains(frame, "connecting to") || strings.Contains(frame, "first load: waiting") {
				t.Fatalf("body still waits on the first load:\n%s", frame)
			}
		})
	}
}

func TestStatusConnLoad401StopsTail(t *testing.T) {
	f := seeded()
	stopped := make(chan struct{})
	f.tail = func(ctx context.Context, _ func(opclient.Batch) error, status func(string)) error {
		status("live")
		<-ctx.Done()
		close(stopped)
		return ctx.Err()
	}
	m, p := loaded(t, f, newClock())
	f.fail("Inbox", &opclient.Error{Status: 401, Code: "unauthorized", Message: "bad"})
	p.send(mustMsg(t, m.Reload("inbox")))
	p.until("rejected", func() bool { return m.Conn == ConnRejected })
	select {
	case <-stopped:
	case <-time.After(30 * time.Second):
		t.Fatal("a 401 on a read must stop the tail")
	}
	if strings.Contains(m.Conn, "offline") {
		t.Fatalf("Conn = %q", m.Conn)
	}
}

func mustMsg(t *testing.T, cmd tea.Cmd) tea.Msg {
	t.Helper()
	if cmd == nil {
		t.Fatal("nil command")
	}
	return cmd()
}

func TestTagReloadCoalesced(t *testing.T) {
	f := seeded()
	next := make(chan opclient.Batch)
	acked := make(chan error)
	f.tail = func(ctx context.Context, handle func(opclient.Batch) error, status func(string)) error {
		status("live")
		for {
			select {
			case b := <-next:
				acked <- handle(b)
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}
	m, p := loaded(t, f, newClock())
	feed := func(b opclient.Batch) error {
		t.Helper()
		next <- b
		var err error
		done := false
		p.until("handler to return", func() bool {
			select {
			case err = <-acked:
				done = true
			default:
			}
			return done
		})
		return err
	}

	f.reset()
	if err := feed(opclient.Batch{Cursor: "e0.3", Events: []envelope.Event{deliveryEvent(t, "e0.1", "thr_a"), deliveryEvent(t, "e0.2", "thr_a"), deliveryEvent(t, "e0.3", "thr_b")}}); err != nil {
		t.Fatal(err)
	}
	if f.count("Inbox") != 1 || f.count("Threads") != 3 {
		t.Fatalf("delivery batch: Inbox %d Threads %d, want 1 and 3", f.count("Inbox"), f.count("Threads"))
	}
	if f.count("Peers") != 0 {
		t.Fatalf("delivery batch loaded Peers %d times", f.count("Peers"))
	}
	if f.count("Thread") != 0 {
		t.Fatalf("a thread nobody opened was loaded %d times", f.count("Thread"))
	}

	f.reset()
	if err := feed(opclient.Batch{Cursor: "e0.4", Events: []envelope.Event{presenceEvent(t, "e0.4", "peer_a")}}); err != nil {
		t.Fatal(err)
	}
	if f.count("Peers") != 1 {
		t.Fatalf("presence loaded Peers %d times", f.count("Peers"))
	}
	for name, n := range f.calls {
		if name != "Peers" && n != 0 {
			t.Fatalf("presence also loaded %s", name)
		}
	}

	f.reset()
	if err := feed(opclient.Batch{Cursor: "e0.5", Events: []envelope.Event{event(t, "e0.5", "future_event", map[string]any{})}}); err != nil {
		t.Fatal(err)
	}
	if len(m.inflight) != 0 || f.count("Inbox")+f.count("Peers")+f.count("Threads") != 0 {
		t.Fatalf("unknown event loaded something: %v", f.calls)
	}

	t.Run("requests during a load fold into one more load", func(t *testing.T) {
		f.reset()
		release := f.hold("Inbox")
		for range 4 {
			p.send(mustMsg(t, m.Reload("inbox")))
		}
		p.until("one request", func() bool { return f.count("Inbox") == 1 })
		release()
		p.until("trailing load", func() bool { return f.count("Inbox") == 2 })
		p.idle()
		if f.count("Inbox") != 2 {
			t.Fatalf("Inbox loaded %d times", f.count("Inbox"))
		}
	})

	t.Run("open thread reloads on its tag", func(t *testing.T) {
		f.reset()
		p.run(m.OpenThread("thr_a"))
		p.until("thread", func() bool { st := m.Data.Thread["thr_a"]; return st != nil && !st.Loading })
		if m.Tab != tabChat || m.CurrentThread != "thr_a" {
			t.Fatalf("tab %d thread %q", m.Tab, m.CurrentThread)
		}
		if f.count("Thread") != 1 || f.count("History") != 1 || f.count("Matrix") != 1 {
			t.Fatalf("thread load: %v", f.calls)
		}
		f.reset()
		if err := feed(opclient.Batch{Cursor: "e0.6", Events: []envelope.Event{deliveryEvent(t, "e0.6", "thr_a"), deliveryEvent(t, "e0.7", "thr_b")}}); err != nil {
			t.Fatal(err)
		}
		if f.count("Thread") != 1 {
			t.Fatalf("Thread loaded %d times", f.count("Thread"))
		}
	})

	t.Run("cursor purge refetches everything", func(t *testing.T) {
		f.reset()
		if err := feed(opclient.Batch{Cursor: "e1.0", Reset: true}); err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{"Inbox", "Threads", "Peers", "Subscriptions", "LoopCuts", "DeliveryHealth", "Audit", "Counters", "Thread"} {
			want := 1
			if name == "Threads" {
				want = 3 // unfiltered page, then Your Threads and Other Threads
			}
			if f.count(name) != want {
				t.Errorf("%s loaded %d times, want %d", name, f.count(name), want)
			}
		}
	})
}

// The handler returns only once the batch is applied, so the tail never acks a
// cursor the views have not seen, and a failed reload replays.
func TestBatchAckFollowsReload(t *testing.T) {
	setup := func(t *testing.T) (*fakeAPI, *Model, *pump, chan opclient.Batch, chan error) {
		f := seeded()
		next, acked := make(chan opclient.Batch), make(chan error, 1)
		f.tail = func(ctx context.Context, handle func(opclient.Batch) error, status func(string)) error {
			status("live")
			for {
				select {
				case b := <-next:
					acked <- handle(b)
				case <-ctx.Done():
					return ctx.Err()
				}
			}
		}
		m, p := loaded(t, f, newClock())
		return f, m, p, next, acked
	}
	take := func(p *pump, acked chan error) error {
		var err error
		done := false
		p.until("ack", func() bool {
			select {
			case err = <-acked:
				done = true
			default:
			}
			return done
		})
		return err
	}
	batch := func(t *testing.T) opclient.Batch {
		return opclient.Batch{Cursor: "e0.1", Events: []envelope.Event{deliveryEvent(t, "e0.1", "thr_a")}}
	}

	t.Run("waits for the load", func(t *testing.T) {
		f, m, p, next, acked := setup(t)
		f.reset()
		release := f.hold("Inbox")
		next <- batch(t)
		p.until("load started", func() bool { return f.count("Inbox") == 1 })
		p.until("threads settled", func() bool { _, busy := m.inflight["threads"]; return !busy && len(m.inflight) == 1 })
		select {
		case err := <-acked:
			t.Fatalf("handler returned before the reload finished: %v", err)
		default:
		}
		release()
		if err := take(p, acked); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("network failure preserves the cursor", func(t *testing.T) {
		f, _, p, next, acked := setup(t)
		f.fail("Inbox", errors.New("connection reset"))
		next <- batch(t)
		if err := take(p, acked); err == nil {
			t.Fatal("a failed reload must not ack")
		}
	})
	t.Run("hub 500 preserves the cursor", func(t *testing.T) {
		f, _, p, next, acked := setup(t)
		f.fail("Threads", &opclient.Error{Status: 503, Code: "unavailable", Message: "x"})
		next <- batch(t)
		if err := take(p, acked); err == nil {
			t.Fatal("a failed reload must not ack")
		}
	})
	t.Run("a resource the hub refuses cannot wedge the tail", func(t *testing.T) {
		f, m, p, next, acked := setup(t)
		f.fail("Inbox", &opclient.Error{Status: 404, Code: "not_found", Message: "gone"})
		next <- batch(t)
		if err := take(p, acked); err != nil {
			t.Fatalf("permanent refusal must ack: %v", err)
		}
		if m.Data.Errs["inbox"] == nil {
			t.Fatal("error not recorded")
		}
	})
	t.Run("401 reaches the tail as terminal", func(t *testing.T) {
		f, _, p, next, acked := setup(t)
		f.fail("Inbox", &opclient.Error{Status: 401, Code: "unauthorized", Message: "x"})
		next <- batch(t)
		if err := take(p, acked); err == nil {
			t.Fatal("401 must stop the tail")
		}
	})
}

func TestEventsKeepNewest200(t *testing.T) {
	f := seeded()
	m, p := loaded(t, f, newClock())
	var evs []envelope.Event
	for i := range 250 {
		evs = append(evs, presenceEvent(t, fmt.Sprintf("e0.%d", i), "peer_a"))
	}
	p.send(tailBatchMsg{batch: opclient.Batch{Cursor: "e0.249", Events: evs}})
	p.idle()
	if len(m.Data.Events) != 200 || m.Data.Events[0].Cursor != "e0.50" || m.Data.Events[199].Cursor != "e0.249" {
		t.Fatalf("%d events, first %s", len(m.Data.Events), m.Data.Events[0].Cursor)
	}
	// A replay after a failed ack does not double the log.
	p.send(tailBatchMsg{batch: opclient.Batch{Cursor: "e0.249", Events: evs[240:]}})
	p.idle()
	if len(m.Data.Events) != 200 {
		t.Fatalf("replay grew the log to %d", len(m.Data.Events))
	}
}

func TestHubNowOffset(t *testing.T) {
	clock := newClock()
	clock.advance(5 * time.Minute) // the client is five minutes fast
	f := seeded()
	m := newTestModel(t, f, clock)
	if got := m.HubNow(); !got.Equal(clock.Now()) {
		t.Fatalf("before a snapshot HubNow = %v", got)
	}
	m.screens = staticScreens()
	p := newPump(t, m)
	p.run(m.Reload("inbox"))
	p.until("inbox", func() bool { return m.Data.Inbox.Now != "" })
	if got := m.HubNow(); !got.Equal(fixedNow) {
		t.Fatalf("HubNow = %v, want the snapshot's %v", got, fixedNow)
	}
	clock.advance(30 * time.Second)
	if got, want := m.HubNow(), fixedNow.Add(30*time.Second); !got.Equal(want) {
		t.Fatalf("HubNow = %v, want %v", got, want)
	}
	if line := strings.Split(plainAt(t, m, 140, 42), "\n")[0]; !strings.Contains(line, "07:41:30") {
		t.Fatalf("clock is not hub time: %q", line)
	}

	// The next snapshot moves it; the last snapshot wins, not the first.
	f.inbox.Now = envelope.FormatTime(fixedNow.Add(time.Hour))
	p.run(m.Reload("inbox"))
	p.until("second snapshot", func() bool { return m.Data.Inbox.Now == f.inbox.Now })
	if got, want := m.HubNow(), fixedNow.Add(time.Hour); !got.Equal(want) {
		t.Fatalf("HubNow = %v, want %v", got, want)
	}

	// Thread snapshots carry now as well.
	f.detail.Now = envelope.FormatTime(fixedNow.Add(2 * time.Hour))
	p.run(m.OpenThread("thr_a"))
	p.until("thread", func() bool { st := m.Data.Thread["thr_a"]; return st != nil && !st.Loading })
	if got, want := m.HubNow(), fixedNow.Add(2*time.Hour); !got.Equal(want) {
		t.Fatalf("HubNow = %v, want %v", got, want)
	}

	// A snapshot with an unreadable clock keeps the last good offset.
	f.inbox.Now = "not a time"
	p.run(m.Reload("inbox"))
	p.until("bad snapshot", func() bool { return m.Data.Inbox.Now == "not a time" })
	if got, want := m.HubNow(), fixedNow.Add(2*time.Hour); !got.Equal(want) {
		t.Fatalf("HubNow = %v, want %v", got, want)
	}
}

func TestForbiddenFirstLoad(t *testing.T) {
	cases := []struct {
		name string
		set  func(*fakeAPI)
	}{
		{"typed 403 on a read", func(f *fakeAPI) {
			f.fail("Inbox", &opclient.Error{Status: 403, Code: "forbidden", Message: "operator only"})
		}},
		{"sentinel on a read", func(f *fakeAPI) { f.fail("Peers", opclient.ErrForbidden) }},
		{"from the tail", func(f *fakeAPI) {
			f.hold("Inbox") // the first load is still open when the tail answers
			f.tail = func(context.Context, func(opclient.Batch) error, func(string)) error {
				return opclient.ErrForbidden
			}
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := seeded()
			c.set(f)
			in, w := io.Pipe()
			defer w.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			err := Run(ctx, Options{API: f, Profile: "home", Hub: "https://hub.example", In: in, Out: io.Discard})
			if err == nil || err.Error() != "profile home is not the operator credential" {
				t.Fatalf("Run = %v", err)
			}
			if !errors.Is(err, opclient.ErrForbidden) {
				t.Fatalf("Run error does not wrap ErrForbidden: %v", err)
			}
		})
	}
}

// A 403 after the first load is one resource failing, not a wrong profile.
func TestForbiddenLaterStaysUp(t *testing.T) {
	f := seeded()
	m, p := loaded(t, f, newClock())
	f.fail("Peers", &opclient.Error{Status: 403, Code: "forbidden", Message: "no"})
	p.run(m.Reload("peers"))
	p.until("error recorded", func() bool { return m.Data.Errs["peers"] != nil })
	if m.ctx.Err() != nil || m.fatal != nil {
		t.Fatal("a later 403 must not exit")
	}
}

func TestFirstLoadBody(t *testing.T) {
	f := seeded()
	release := f.hold("Inbox")
	defer release()
	clock := newClock()
	m := newTestModel(t, f, clock)
	m.screens = staticScreens()
	p := newPump(t, m)
	p.run(m.Init())
	frame := plainAt(t, m, 140, 42)
	if !strings.Contains(frame, "connecting to hub.example:8443") || !strings.Contains(frame, "connecting") {
		t.Fatalf("initial frame:\n%s", frame)
	}
	p.until("others loaded", func() bool { return f.count("Peers") == 1 && len(m.inflight) == 1 })
	clock.advance(3 * time.Second)
	frame = plainAt(t, m, 140, 42)
	if !strings.Contains(frame, "first load: waiting on inbox (3s)") {
		t.Fatalf("progress frame:\n%s", frame)
	}
	if strings.Contains(frame, "body of inbox") {
		t.Fatalf("screen drawn before the first load:\n%s", frame)
	}
}

func TestSlowRefresh(t *testing.T) {
	f := seeded()
	clock := newClock()
	m, p := loaded(t, f, clock)
	f.reset()
	p.send(tickMsg{})
	if len(m.inflight) != 0 || len(p.msgs) != 0 {
		t.Fatal("refreshed before the interval")
	}
	clock.advance(time.Minute)
	f.fail("Peers", errors.New("boom"))
	p.run(m.Reload("peers"))
	p.until("failure", func() bool { return m.Data.Errs["peers"] != nil })
	f.fail("Peers", nil)
	f.reset()
	p.send(tickMsg{})
	names := []string{"Inbox", "Subscriptions", "LoopCuts", "DeliveryHealth", "Audit", "Counters", "Peers"}
	p.loaded(f, names...)
	for _, name := range names {
		if f.count(name) != 1 {
			t.Errorf("%s loaded %d times", name, f.count(name))
		}
	}
	if m.Data.Errs["peers"] != nil {
		t.Error("a resource that recovered keeps its error")
	}
}

func TestToast(t *testing.T) {
	clock := newClock()
	m, p := loaded(t, seeded(), clock)
	m.Toast("saved", false)
	if frame := plainAt(t, m, 140, 42); !strings.Contains(frame, "saved") {
		t.Fatalf("toast missing:\n%s", frame)
	}
	clock.advance(time.Minute)
	p.send(tickMsg{})
	if frame := plainAt(t, m, 140, 42); strings.Contains(frame, "saved") {
		t.Fatalf("toast did not expire:\n%s", frame)
	}
}

// Review Focus 1 at the shell's own seams: nothing a peer controls reaches the
// frame with its controls intact.
func TestShellStringsAreClean(t *testing.T) {
	const payload = "\x1b]52;c;aGVsbG8=\x07\x1b[2J\x1b]0;pwned\x07\u202e"
	f := seeded()
	f.inbox.Me = opapi.Me{Name: "Max" + payload, Presence: "working" + payload}
	m := newTestModel(t, f, newClock())
	m.screens = staticScreens()
	m.Hub = "https://hub.example" + payload
	p := newPump(t, m)
	p.run(m.Reload("inbox"))
	p.until("inbox", func() bool { return m.Data.Inbox.Me.Name != "" })
	m.Toast("hub said"+payload, true)
	m.Update(tea.WindowSizeMsg{Width: 140, Height: 42})
	raw := m.View().Content
	for _, bad := range []string{"\x1b]", "\x1b[2J", "\u202e", "pwned", "aGVsbG8"} {
		if strings.Contains(raw, bad) {
			t.Errorf("frame contains %q:\n%q", bad, raw)
		}
	}
}

func TestViewIsAltScreen(t *testing.T) {
	m, _ := loaded(t, seeded(), newClock())
	m.Update(tea.WindowSizeMsg{Width: 140, Height: 42})
	if v := m.View(); !v.AltScreen {
		t.Fatal("View must use the alt screen")
	}
}

func TestPane(t *testing.T) {
	th := theme.New(theme.TrueColor, true)
	got := Pane(th, "title", 20, 5, true, "one\ntwo\nthree\nfour\nfive")
	assertFrame(t, got, 20, 5)
	if !strings.Contains(got, "title") || !strings.Contains(got, "three") || strings.Contains(got, "four") {
		t.Fatalf("pane:\n%s", got)
	}
	assertFrame(t, Pane(th, "a title much longer than its pane", 12, 3, false, strings.Repeat("wide 日本語 ", 10)), 12, 3)
	assertFrame(t, Pane(th, "t", 12, 4, false, "tab\there"), 12, 4)
	assertFrame(t, Pane(th, "t", 10, 2, false, "x"), 10, 2)
	assertFrame(t, Pane(th, "t", 10, 3, false, "x"), 10, 3)
	if got := Pane(th, "t", 1, 1, true, "x"); got != "" {
		t.Fatalf("degenerate pane: %q", got)
	}
}

func TestStubScreens(t *testing.T) {
	want := []string{"inbox", "chat", "dash", "subs"}
	for i, s := range []Screen{newInboxScreen(), newChatScreen(), newDashScreen(), newSubsScreen()} {
		if s.Title() != want[i] {
			t.Errorf("screen %d title %q", i, s.Title())
		}
	}
	m := newTestModel(t, seeded(), newClock())
	if len(m.screens) != 4 {
		t.Fatalf("%d screens", len(m.screens))
	}
	if viewHelp(m) == "" {
		t.Fatal("empty help")
	}
}

func TestRunEndsCleanly(t *testing.T) {
	t.Run("q", func(t *testing.T) {
		in, w := io.Pipe()
		defer w.Close()
		done := make(chan error, 1)
		go func() {
			done <- Run(context.Background(), Options{API: seeded(), Profile: "home", Hub: "https://hub.example", In: in, Out: io.Discard})
		}()
		if _, err := w.Write([]byte("q")); err != nil {
			t.Fatal(err)
		}
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("quitting is not an error: %v", err)
			}
		case <-time.After(30 * time.Second):
			t.Fatal("q did not end Run")
		}
	})
	t.Run("context", func(t *testing.T) {
		in, w := io.Pipe()
		defer w.Close()
		ctx, cancel := context.WithCancel(context.Background())
		f := seeded()
		started := make(chan struct{})
		f.tail = func(ctx context.Context, _ func(opclient.Batch) error, _ func(string)) error {
			close(started)
			<-ctx.Done()
			return ctx.Err()
		}
		done := make(chan error, 1)
		go func() {
			done <- Run(ctx, Options{API: f, Profile: "home", Hub: "https://hub.example", In: in, Out: io.Discard})
		}()
		<-started
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("an interrupted Run is not an error: %v", err)
			}
		case <-time.After(30 * time.Second):
			t.Fatal("cancelling did not end Run")
		}
	})
}

func TestProgramQuits(t *testing.T) {
	in, w := io.Pipe()
	defer w.Close()
	m := newTestModel(t, seeded(), newClock())
	program := tea.NewProgram(m, tea.WithInput(in), tea.WithOutput(io.Discard), tea.WithoutSignals(), tea.WithWindowSize(140, 42))
	done := make(chan error, 1)
	go func() {
		_, err := program.Run()
		done <- err
	}()
	if _, err := w.Write([]byte("q")); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("program ended with %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("q did not exit within 2 s")
	}
}

// A hint pair's key is its leading run of key words: `[ ]` and `( )` are one
// key, a narrow all-keys string is all key, and a pair of prose has no key
// (design §7.5).
func TestHintLineKeys(t *testing.T) {
	th := theme.New(theme.TrueColor, true)
	key, word := th.Fg(theme.Text).Render, th.Fg(theme.Muted).Render
	for _, tc := range []struct{ hints, want string }{
		{"i write  [ ] thread  ( ) who", key("i") + word(" write") + "  " + key("[ ]") + word(" thread") + "  " + key("( )") + word(" who")},
		{"i [ ] j/k R a e m s f o n ( ) :react", key("i [ ] j/k R a e m s f o n ( ) :react")},
		{"j/k pgup/pgdn esc", key("j/k pgup/pgdn esc")},
		{"digit answers  other keys cancel", key("digit") + word(" answers") + "  " + word("other keys cancel")},
		{"y discard  any other key cancels", key("y") + word(" discard") + "  " + word("any other key cancels")},
		{"j/k move  read-only", key("j/k") + word(" move") + "  " + word("read-only")},
		{"1-4 tabs  : command  ctrl+u/d log  enter open", key("1-4") + word(" tabs") + "  " + key(":") + word(" command") + "  " + key("ctrl+u/d") + word(" log") + "  " + key("enter") + word(" open")},
		{"f file  :react  … more", key("f") + word(" file") + "  " + key(":react") + "  " + word("… more")},
	} {
		if got := hintLine(th, tc.hints); got != tc.want {
			t.Errorf("%q:\n got %q\nwant %q", tc.hints, got, tc.want)
		}
	}
}
