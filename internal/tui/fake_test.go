package tui

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/opapi"
	"github.com/XertroV/vecnet/internal/opclient"
)

// The real client must satisfy the interface the TUI programs against.
var _ API = (*opclient.Client)(nil)

// fakeAPI is the scripted hub every TUI test talks to. Set the value fields to
// choose responses, errs[method] to fail a method, and gate[method] to hold a
// method until the channel closes.
type fakeAPI struct {
	mu    sync.Mutex
	calls map[string]int
	errs  map[string]error
	gate  map[string]chan struct{}

	inbox      opapi.Inbox
	threads    opapi.ThreadPage
	detail     opapi.ThreadDetail
	history    opapi.History
	matrix     opapi.Matrix
	peers      []opapi.PeerRow
	subs       []opapi.Subscription
	cuts       opapi.LoopCuts
	health     opapi.DeliveryHealth
	audit      opapi.AuditPage
	counters   map[string]int
	sent       []envelope.SendRequest
	blob       []byte
	uploads    []fakeUpload // uploads whose bytes the fake read in full
	reacted    []envelope.ReactionRequest
	notices    []envelope.HubNotice
	prefs      opapi.Preferences
	prefsVerbs []string

	// tail replaces the default Tail, which reports live and blocks.
	tail func(ctx context.Context, handle func(opclient.Batch) error, status func(string)) error

	// byCursor, when set, answers Threads by query cursor. An empty cursor is
	// the first page. threadQueries records every query, including participant
	// filters, so a handoff can prove it asked for an unfiltered page.
	byCursor      map[string]opapi.ThreadPage
	threadQueries []opapi.ThreadQuery
}

// fakeUpload is one upload the fake took, with the bytes it read.
type fakeUpload struct {
	name, media string
	size        int64
	data        []byte
}

func newFake() *fakeAPI {
	return &fakeAPI{calls: map[string]int{}, errs: map[string]error{}, gate: map[string]chan struct{}{}, counters: map[string]int{}}
}

func (f *fakeAPI) hit(ctx context.Context, name string) error {
	f.mu.Lock()
	f.calls[name]++
	gate, err := f.gate[name], f.errs[name]
	f.mu.Unlock()
	if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return err
}

func (f *fakeAPI) count(name string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[name]
}

func (f *fakeAPI) reset() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = map[string]int{}
	f.prefsVerbs = nil
}

func (f *fakeAPI) counts() map[string]int {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make(map[string]int, len(f.calls))
	for name, n := range f.calls {
		out[name] = n
	}
	return out
}

func (f *fakeAPI) preferenceVerbs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string{}, f.prefsVerbs...)
}

func (f *fakeAPI) fail(name string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.errs[name] = err
}

func (f *fakeAPI) hold(name string) (release func()) {
	gate := make(chan struct{})
	f.mu.Lock()
	f.gate[name] = gate
	f.mu.Unlock()
	return func() {
		f.mu.Lock()
		delete(f.gate, name)
		f.mu.Unlock()
		close(gate)
	}
}

func (f *fakeAPI) Inbox(ctx context.Context, _ opapi.InboxQuery) (opapi.Inbox, error) {
	return f.inbox, f.hit(ctx, "Inbox")
}
func (f *fakeAPI) ReadThread(ctx context.Context, _ string) (opapi.ReadResult, error) {
	return opapi.ReadResult{}, f.hit(ctx, "ReadThread")
}
func (f *fakeAPI) Threads(ctx context.Context, q opapi.ThreadQuery) (opapi.ThreadPage, error) {
	if err := f.hit(ctx, "Threads"); err != nil {
		return opapi.ThreadPage{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.threadQueries = append(f.threadQueries, q)
	if f.byCursor != nil {
		if page, ok := f.byCursor[q.Cursor]; ok {
			return page, nil
		}
	}
	return f.threads, nil
}
func (f *fakeAPI) Thread(ctx context.Context, _ string) (opapi.ThreadDetail, error) {
	return f.detail, f.hit(ctx, "Thread")
}
func (f *fakeAPI) History(ctx context.Context, _, _ string, _ int) (opapi.History, error) {
	return f.history, f.hit(ctx, "History")
}
func (f *fakeAPI) Matrix(ctx context.Context, _, _ string, _ int) (opapi.Matrix, error) {
	return f.matrix, f.hit(ctx, "Matrix")
}
func (f *fakeAPI) Preferences(ctx context.Context) (opapi.Preferences, error) {
	f.mu.Lock()
	f.prefsVerbs = append(f.prefsVerbs, http.MethodGet)
	snap := f.prefs
	f.mu.Unlock()
	if err := f.hit(ctx, "Preferences"); err != nil {
		return opapi.Preferences{}, err
	}
	return snap, nil
}

func (f *fakeAPI) Notices(ctx context.Context) ([]envelope.HubNotice, error) {
	err := f.hit(ctx, "Notices")
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]envelope.HubNotice(nil), f.notices...), err
}
func (f *fakeAPI) setNotices(ns ...envelope.HubNotice) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.notices = ns
}
func (f *fakeAPI) Peers(ctx context.Context) ([]opapi.PeerRow, error) {
	return f.peers, f.hit(ctx, "Peers")
}
func (f *fakeAPI) LoopCuts(ctx context.Context, _ int) (opapi.LoopCuts, error) {
	return f.cuts, f.hit(ctx, "LoopCuts")
}
func (f *fakeAPI) DeliveryHealth(ctx context.Context) (opapi.DeliveryHealth, error) {
	return f.health, f.hit(ctx, "DeliveryHealth")
}
func (f *fakeAPI) Subscriptions(ctx context.Context) ([]opapi.Subscription, error) {
	return f.subs, f.hit(ctx, "Subscriptions")
}
func (f *fakeAPI) ResumeSubscription(ctx context.Context, _ string) error {
	return f.hit(ctx, "ResumeSubscription")
}
func (f *fakeAPI) Audit(ctx context.Context, _ int, _ string) (opapi.AuditPage, error) {
	return f.audit, f.hit(ctx, "Audit")
}
func (f *fakeAPI) Counters(ctx context.Context) (map[string]int, error) {
	return f.counters, f.hit(ctx, "Counters")
}
func (f *fakeAPI) RetryDeadLetter(ctx context.Context, _ string) error {
	return f.hit(ctx, "RetryDeadLetter")
}
func (f *fakeAPI) DiscardDeadLetter(ctx context.Context, _ string) error {
	return f.hit(ctx, "DiscardDeadLetter")
}
func (f *fakeAPI) Send(ctx context.Context, r envelope.SendRequest) (opapi.SendResult, error) {
	f.mu.Lock()
	f.sent = append(f.sent, r)
	f.mu.Unlock()
	return opapi.SendResult{}, f.hit(ctx, "Send")
}

// React records the request and answers as the hub would for the operator's
// own peer: an add leaves that peer as the emoji's only reactor, and a remove
// leaves no reactions.
func (f *fakeAPI) React(ctx context.Context, r envelope.ReactionRequest) (opapi.ReactionResult, error) {
	f.mu.Lock()
	f.reacted = append(f.reacted, r)
	me := f.inbox.Me.PeerID
	f.mu.Unlock()
	out := opapi.ReactionResult{MessageID: r.MessageID, Emoji: r.Emoji, Op: r.Op, Changed: true, Reactions: []envelope.ReactionGroup{}, Superseded: []string{}}
	if r.Op == "add" {
		out.Reactions = []envelope.ReactionGroup{{Emoji: r.Emoji, Peers: []string{me}}}
	}
	if err := f.hit(ctx, "React"); err != nil {
		return opapi.ReactionResult{}, err
	}
	return out, nil
}

func (f *fakeAPI) reactions() []envelope.ReactionRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]envelope.ReactionRequest(nil), f.reacted...)
}

func (f *fakeAPI) Answer(ctx context.Context, _ opapi.OpenApproval, _ int) error {
	return f.hit(ctx, "Answer")
}
func (f *fakeAPI) SetPresence(ctx context.Context, _ string, _ time.Time) error {
	return f.hit(ctx, "SetPresence")
}
func (f *fakeAPI) Blob(ctx context.Context, _ string, _ int64) ([]byte, string, error) {
	return f.blob, "application/octet-stream", f.hit(ctx, "Blob")
}

// Upload waits on gate["Upload"] before reading, so a held upload has read
// nothing, and answers with the sha256 of what it read.
func (f *fakeAPI) Upload(ctx context.Context, r io.Reader, size int64, mediaType, name string) (envelope.Attachment, error) {
	if err := f.hit(ctx, "Upload"); err != nil {
		return envelope.Attachment{}, err
	}
	// hit's select picks at random when the gate opens after a cancel: a
	// cancelled upload never reads, whichever case won.
	if err := ctx.Err(); err != nil {
		return envelope.Attachment{}, err
	}
	data, err := io.ReadAll(r)
	if err != nil {
		return envelope.Attachment{}, err
	}
	f.mu.Lock()
	f.uploads = append(f.uploads, fakeUpload{name: name, media: mediaType, size: size, data: data})
	f.mu.Unlock()
	sum := sha256.Sum256(data)
	return envelope.Attachment{SHA256: hex.EncodeToString(sum[:]), Size: int64(len(data)), MediaType: mediaType, Name: name}, nil
}

func (f *fakeAPI) uploaded() []fakeUpload {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]fakeUpload(nil), f.uploads...)
}

func (f *fakeAPI) TailWithAck(ctx context.Context, handle func(opclient.Batch) error, status func(string), accepted func(string)) error {
	f.mu.Lock()
	f.calls["Tail"]++
	tail := f.tail
	f.mu.Unlock()
	if tail != nil {
		return tail(ctx, handle, status)
	}
	status("live")
	<-ctx.Done()
	return ctx.Err()
}

// testClock is a client clock the test moves by hand.
type testClock struct {
	mu sync.Mutex
	t  time.Time
}

var fixedNow = time.Date(2026, 11, 2, 7, 41, 0, 0, time.UTC)

func newClock() *testClock { return &testClock{t: fixedNow} }
func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}
func (c *testClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// newTestModel returns a model on a fixed clock in UTC, so goldens do not
// depend on where the test runs.
func newTestModel(t *testing.T, api API, clock *testClock) *Model {
	t.Helper()
	m := newModel(context.Background(), Options{API: api, Profile: "home", Hub: "https://hub.example:8443", Now: clock.Now})
	m.loc = time.UTC
	t.Cleanup(m.cancel)
	return m
}

// pump is a small synchronous runtime: commands run on goroutines as they do
// under tea.Program, and their messages reach Update only from the test
// goroutine, so a test can wait on model state rather than on elapsed time.
type pump struct {
	t    *testing.T
	m    *Model
	msgs chan tea.Msg
	done chan struct{}
}

func newPump(t *testing.T, m *Model) *pump {
	t.Helper()
	p := &pump{t: t, m: m, msgs: make(chan tea.Msg, 256), done: make(chan struct{})}
	t.Cleanup(func() { close(p.done) })
	return p
}

func (p *pump) run(cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	go func() {
		msg := cmd()
		if msg == nil {
			return
		}
		if batch, ok := msg.(tea.BatchMsg); ok {
			for _, c := range batch {
				p.run(c)
			}
			return
		}
		// Time moves only when a test sends tickMsg, after moving its clock.
		if _, ok := msg.(tickMsg); ok {
			return
		}
		select {
		case p.msgs <- msg:
		case <-p.done:
		}
	}()
}

// send delivers msg to the model as the program would.
func (p *pump) send(msg tea.Msg) {
	p.t.Helper()
	_, cmd := p.m.Update(msg)
	p.run(cmd)
}

// press delivers one key by its name.
func (p *pump) press(keys ...string) {
	p.t.Helper()
	for _, key := range keys {
		p.send(keyPress(key))
	}
}

// until processes messages until cond holds.
func (p *pump) until(what string, cond func() bool) {
	p.t.Helper()
	deadline := time.After(30 * time.Second)
	recheck := time.NewTicker(time.Millisecond)
	defer recheck.Stop()
	for !cond() {
		select {
		case msg := <-p.msgs:
			p.send(msg)
		case <-recheck.C: // the fake's goroutines change state without a message
		case <-deadline:
			p.t.Fatalf("timed out waiting for %s", what)
		}
	}
}

// idle waits until no resource is loading and every load result already queued
// has been applied. A follow-up load is started from the message that finishes
// the one before it, so the queue has to be empty too.
func (p *pump) idle() {
	p.t.Helper()
	p.until("loads to finish", func() bool { return len(p.m.inflight) == 0 && len(p.msgs) == 0 })
}

// loaded waits until each method has been called and its load has landed.
func (p *pump) loaded(f *fakeAPI, names ...string) {
	p.t.Helper()
	p.until("requests", func() bool {
		for _, name := range names {
			if f.count(name) == 0 {
				return false
			}
		}
		return true
	})
	p.idle()
}

// keyPress builds a key message by the name String() reports.
func keyPress(key string) tea.KeyPressMsg {
	switch key {
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "ctrl+c":
		return tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}
	case "ctrl+l":
		return tea.KeyPressMsg{Code: 'l', Mod: tea.ModCtrl}
	case "ctrl+q":
		return tea.KeyPressMsg{Code: 'q', Mod: tea.ModCtrl}
	case "ctrl+u":
		return tea.KeyPressMsg{Code: 'u', Mod: tea.ModCtrl}
	case "ctrl+d":
		return tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl}
	case "ctrl+z":
		return tea.KeyPressMsg{Code: 'z', Mod: tea.ModCtrl}
	}
	r := []rune(key)[0]
	return tea.KeyPressMsg{Code: r, Text: key}
}

// staticScreen stands in for a tab, so shell goldens stay put as Tasks 18–21
// replace the real stubs.
type staticScreen struct {
	title, hints string
	keys         []string
}

func (s *staticScreen) Title() string { return s.title }
func (s *staticScreen) Hints() string { return s.hints }
func (s *staticScreen) Update(msg tea.Msg, _ *Model) tea.Cmd {
	if key, ok := msg.(tea.KeyPressMsg); ok {
		s.keys = append(s.keys, key.String())
	}
	return nil
}
func (s *staticScreen) View(m *Model, w, h int) string {
	return Pane(m.th, s.title, w, h, true, "body of "+s.title)
}

func staticScreens() []Screen {
	return []Screen{
		&staticScreen{title: "inbox", hints: "j/k move  a answer"},
		&staticScreen{title: "chat", hints: "i write"},
		&staticScreen{title: "dash", hints: "enter open"},
		&staticScreen{title: "subs", hints: "r resume"},
	}
}

func event(t *testing.T, cursor, kind string, data any) envelope.Event {
	t.Helper()
	raw, err := json.Marshal(data)
	if err != nil {
		t.Fatal(err)
	}
	return envelope.Event{Cursor: cursor, Type: kind, At: envelope.FormatTime(fixedNow), Data: raw}
}

func deliveryEvent(t *testing.T, cursor, thread string) envelope.Event {
	t.Helper()
	return event(t, cursor, "delivery", map[string]any{"message": map[string]any{"thread_id": thread}})
}

func presenceEvent(t *testing.T, cursor, peer string) envelope.Event {
	t.Helper()
	return event(t, cursor, "presence", map[string]any{"peer_id": peer, "state": "idle"})
}
