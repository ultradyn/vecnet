package tui

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/opapi"
	"github.com/XertroV/vecnet/internal/opclient"
)

// Data is the last snapshot of everything the hub showed this client. A failed
// load keeps the old value and records the error under its resource key.
type Data struct {
	Inbox    opapi.Inbox
	Threads  opapi.ThreadPage
	Peers    []opapi.PeerRow
	Subs     []opapi.Subscription
	Cuts     opapi.LoopCuts
	Health   opapi.DeliveryHealth
	Audit    opapi.AuditPage
	Counters map[string]int
	Thread   map[string]*ThreadState
	Events   []envelope.Event // newest 200, oldest first, for the raw log
	Errs     map[string]error
}

// ThreadState is one opened thread.
type ThreadState struct {
	Detail   opapi.ThreadDetail
	Messages []envelope.Message
	Matrix   opapi.Matrix
	Loading  bool
}

// TagAll asks Reload for every resource and every opened thread. The other tags
// are resource keys: opapi.Tags' "inbox", "threads", "peers" and "thread:<id>",
// and the untagged "subs", "cuts", "health", "audit" and "counters".
const TagAll = "all"

const (
	threadPrefix = "thread:"
	maxEvents    = 200
	pageLimit    = 200 // the hub's cap on limit
	auditLimit   = 50
	// maxPages bounds one thread load. A longer history finishes on the next
	// reload, because each load continues after the last message it holds.
	maxPages = 10
	// slowEvery refreshes what no event invalidates, and retries failed loads.
	slowEvery = time.Minute
	quitGrace = 100 * time.Millisecond
)

var (
	allKeys  = []string{"inbox", "threads", "peers", "subs", "cuts", "health", "audit", "counters"}
	slowKeys = []string{"subs", "cuts", "health", "audit", "counters"}
)

type (
	tickMsg   struct{}
	reloadMsg struct{ tags []string }
	// loadMsg is one finished load. apply runs on the program's goroutine. at
	// is the client's clock when the response arrived.
	loadMsg struct {
		key, hubNow string
		at          time.Time
		err         error
		apply       func(*Model)
	}
	tailStatusMsg struct{ status string }
	tailBatchMsg  struct {
		batch opclient.Batch
		ack   chan<- error
	}
	tailDoneMsg struct{ err error }
)

// waiter is one tail batch waiting for its loads, so its cursor is acked only
// after the views have seen what it announced.
type waiter struct {
	pending map[string]bool
	ack     chan<- error
	err     error
}

func tick() tea.Cmd {
	return tea.Tick(time.Second, func(time.Time) tea.Msg { return tickMsg{} })
}

// Reload loads the resources for tags, then updates Data. Tags for one resource
// fold together: a request made while its load runs becomes one more load after
// it, however many there were. An unopened thread's tag loads nothing.
func (m *Model) Reload(tags ...string) tea.Cmd {
	if len(tags) == 0 {
		return nil
	}
	return func() tea.Msg { return reloadMsg{tags: tags} }
}

// firstLoading reports whether the first load is still open.
func (m *Model) firstLoading() bool { return len(m.first) > 0 }

// stopped reports whether the hub has refused this credential for good.
func (m *Model) stopped() bool { return m.Conn == ConnRejected || m.Conn == ConnDisabled }

// expand turns tags into resource keys, without duplicates.
func (m *Model) expand(tags []string) []string {
	keys := []string{}
	add := func(key string) {
		if !slices.Contains(keys, key) {
			keys = append(keys, key)
		}
	}
	for _, tag := range tags {
		switch {
		case tag == TagAll:
			for _, key := range allKeys {
				add(key)
			}
			for id := range m.Data.Thread {
				add(threadPrefix + id)
			}
		case strings.HasPrefix(tag, threadPrefix):
			if m.Data.Thread[strings.TrimPrefix(tag, threadPrefix)] != nil {
				add(tag)
			}
		default:
			add(tag)
		}
	}
	return keys
}

func (m *Model) startLoads(tags []string, ack chan<- error) tea.Cmd {
	keys := m.expand(tags)
	if m.stopped() {
		keys = nil
	}
	var cmds []tea.Cmd
	pending := map[string]bool{}
	for _, key := range keys {
		cmd := m.fetch(key)
		if cmd == nil {
			continue
		}
		pending[key] = true
		if m.inflight[key] {
			m.dirty[key] = true
			continue
		}
		m.inflight[key] = true
		cmds = append(cmds, cmd)
	}
	if ack != nil {
		if len(pending) == 0 {
			ack <- nil
		} else {
			m.waiters = append(m.waiters, &waiter{pending: pending, ack: ack})
		}
	}
	return tea.Batch(cmds...)
}

// onLoad applies a finished load, runs the one load folded behind it if there
// is one, and otherwise settles whatever waits on this key.
func (m *Model) onLoad(msg loadMsg) tea.Cmd {
	delete(m.inflight, msg.key)
	if msg.err == nil {
		delete(m.Data.Errs, msg.key)
		msg.apply(m)
		m.noteHubNow(msg.hubNow, msg.at)
	} else {
		m.Data.Errs[msg.key] = msg.err
		if id, ok := strings.CutPrefix(msg.key, threadPrefix); ok && m.Data.Thread[id] != nil {
			m.Data.Thread[id].Loading = false
		}
	}
	first := m.first[msg.key]
	delete(m.first, msg.key)
	switch {
	case isStatus(msg.err, 401):
		m.reject(msg.err)
	case first && isStatus(msg.err, 403):
		return m.notOperator()
	case msg.err != nil && m.Conn == ConnLive && !errors.Is(msg.err, context.Canceled):
		m.Toast("reload "+msg.key+": "+msg.err.Error(), true)
	}
	var again tea.Cmd
	if m.dirty[msg.key] && !m.stopped() {
		m.inflight[msg.key] = true
		again = m.fetch(msg.key)
	} else {
		m.settle(msg.key, msg.err)
	}
	delete(m.dirty, msg.key)
	return tea.Batch(again, m.broadcast(Loaded{Key: msg.key, Err: msg.err}))
}

// Loaded reaches every screen after a resource's Data changed or failed.
type Loaded struct {
	Key string
	Err error
}

func (m *Model) settle(key string, err error) {
	kept := m.waiters[:0]
	for _, w := range m.waiters {
		if w.pending[key] {
			delete(w.pending, key)
			if w.err == nil && replayable(err) {
				w.err = err
			}
			if len(w.pending) == 0 {
				w.ack <- w.err
				continue
			}
		}
		kept = append(kept, w)
	}
	m.waiters = kept
}

// replayable reports whether a failed load should keep the tail's cursor: the
// next attempt may succeed. A refusal such as 404 never will, and replaying it
// would wedge the tail, so it stays in Data.Errs and the cursor moves on. 401
// and 403 return to the tail too, which stops on them.
func replayable(err error) bool {
	if err == nil {
		return false
	}
	var e *opclient.Error
	if errors.As(err, &e) {
		return e.Status >= 500 || e.Status == 429 || e.Status == 401 || e.Status == 403
	}
	return true
}

func isStatus(err error, status int) bool {
	var e *opclient.Error
	switch {
	case errors.As(err, &e):
		return e.Status == status
	case status == 401:
		return errors.Is(err, opclient.ErrUnauthorized)
	case status == 403:
		return errors.Is(err, opclient.ErrForbidden)
	}
	return false
}

// reject stops all traffic: a rejected credential gets no retries, and the last
// data stays on screen.
func (m *Model) reject(err error) {
	m.Conn = ConnRejected
	m.halt(err)
}

// halt ends the tail and releases its handler with err.
func (m *Model) halt(err error) {
	for _, w := range m.waiters {
		w.ack <- err
	}
	m.waiters = nil
	clear(m.dirty)
	m.stopTail()
}

// notOperator ends the program with the profile error. On a fast hub this can
// land inside the first frame, before Bubble Tea has flushed its terminal
// queries; exiting then would leave the replies on the operator's shell prompt,
// so it gives them quitGrace to come back.
func (m *Model) notOperator() tea.Cmd {
	m.fatal = notOperatorError{profile: m.Profile}
	m.cancel()
	return tea.Tick(quitGrace, func(time.Time) tea.Msg { return tea.Quit() })
}

// onTick expires the toast and refreshes what no event invalidates.
func (m *Model) onTick() tea.Cmd {
	now := m.Now()
	if m.toast.text != "" && !now.Before(m.toast.until) {
		m.toast = toast{}
	}
	cmds := []tea.Cmd{tick()}
	if now.Sub(m.lastSlow) >= slowEvery && m.Conn == ConnLive {
		m.lastSlow = now
		tags := slices.Clone(slowKeys)
		for key := range m.Data.Errs {
			tags = append(tags, key)
		}
		cmds = append(cmds, m.Reload(tags...))
	}
	return tea.Batch(cmds...)
}

// fetch is the command for one resource key, nil for a key that names nothing.
func (m *Model) fetch(key string) tea.Cmd {
	api, ctx, now := m.API, m.ctx, m.Now
	switch key {
	case "inbox":
		return func() tea.Msg {
			v, err := api.Inbox(ctx, opapi.InboxQuery{Limit: pageLimit})
			return loadMsg{key: key, err: err, hubNow: v.Now, at: now(), apply: func(m *Model) { m.Data.Inbox = v }}
		}
	case "threads":
		return func() tea.Msg {
			v, err := api.Threads(ctx, opapi.ThreadQuery{Limit: pageLimit})
			return loadMsg{key: key, err: err, apply: func(m *Model) { m.Data.Threads = v }}
		}
	case "peers":
		return func() tea.Msg {
			v, err := api.Peers(ctx)
			return loadMsg{key: key, err: err, apply: func(m *Model) { m.Data.Peers = v }}
		}
	case "subs":
		return func() tea.Msg {
			v, err := api.Subscriptions(ctx)
			return loadMsg{key: key, err: err, apply: func(m *Model) { m.Data.Subs = v }}
		}
	case "cuts":
		return func() tea.Msg {
			v, err := api.LoopCuts(ctx, 24)
			return loadMsg{key: key, err: err, apply: func(m *Model) { m.Data.Cuts = v }}
		}
	case "health":
		return func() tea.Msg {
			v, err := api.DeliveryHealth(ctx)
			return loadMsg{key: key, err: err, apply: func(m *Model) { m.Data.Health = v }}
		}
	case "audit":
		return func() tea.Msg {
			v, err := api.Audit(ctx, auditLimit, "")
			return loadMsg{key: key, err: err, apply: func(m *Model) { m.Data.Audit = v }}
		}
	case "counters":
		return func() tea.Msg {
			v, err := api.Counters(ctx)
			return loadMsg{key: key, err: err, apply: func(m *Model) { m.Data.Counters = v }}
		}
	}
	if id, ok := strings.CutPrefix(key, threadPrefix); ok {
		return m.fetchThread(key, id)
	}
	return nil
}

// fetchThread loads a thread in one step, so a screen never sees its detail,
// messages and matrix from different moments. Messages continue after the last
// one held; the matrix is refetched whole, because receipts change old rows.
func (m *Model) fetchThread(key, id string) tea.Cmd {
	st := m.Data.Thread[id]
	if st == nil {
		return nil
	}
	after := ""
	if n := len(st.Messages); n > 0 {
		after = st.Messages[n-1].MessageID
	}
	api, ctx, now := m.API, m.ctx, m.Now
	return func() tea.Msg {
		detail, err := api.Thread(ctx, id)
		if err != nil {
			return loadMsg{key: key, err: err}
		}
		var messages []envelope.Message
		for cursor, page := after, 0; page < maxPages; page++ {
			h, err := api.History(ctx, id, cursor, pageLimit)
			if err != nil {
				return loadMsg{key: key, err: err}
			}
			messages = append(messages, h.Messages...)
			if h.NextAfter == nil || *h.NextAfter == "" {
				break
			}
			cursor = *h.NextAfter
		}
		var matrix opapi.Matrix
		for cursor, page := "", 0; page < maxPages; page++ {
			mx, err := api.Matrix(ctx, id, cursor, pageLimit)
			if err != nil {
				return loadMsg{key: key, err: err}
			}
			matrix.Recipients = mx.Recipients
			matrix.Rows = append(matrix.Rows, mx.Rows...)
			matrix.NextAfter = mx.NextAfter
			if mx.NextAfter == nil || *mx.NextAfter == "" {
				break
			}
			cursor = *mx.NextAfter
		}
		return loadMsg{key: key, hubNow: detail.Now, at: now(), apply: func(m *Model) {
			st := m.Data.Thread[id]
			st.Detail, st.Matrix, st.Loading = detail, matrix, false
			st.Messages = append(st.Messages, messages...)
		}}
	}
}

// startTail runs the event tail for the life of the program. Its callbacks hand
// everything to the program through tailCh, in order.
func (m *Model) startTail() tea.Cmd {
	ctx, cancel := context.WithCancel(m.ctx)
	m.stopTail = cancel
	api, ch := m.API, m.tailCh
	send := func(msg tea.Msg) bool {
		select {
		case ch <- msg:
			return true
		case <-ctx.Done():
			return false
		}
	}
	return func() tea.Msg {
		// Each handler call waits for its reload, so the tail acks only what
		// the views already show, and a failed reload replays.
		handle := func(b opclient.Batch) error {
			if len(b.Events) == 0 && !b.Reset {
				return nil
			}
			ack := make(chan error, 1)
			if !send(tailBatchMsg{batch: b, ack: ack}) {
				return ctx.Err()
			}
			select {
			case err := <-ack:
				return err
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		status := func(s string) { send(tailStatusMsg{status: s}) }
		return tailDoneMsg{err: api.Tail(ctx, handle, status)}
	}
}

// listenTail waits for the tail's next message. Update re-arms it.
func (m *Model) listenTail() tea.Cmd {
	ctx, ch := m.ctx, m.tailCh
	return func() tea.Msg {
		select {
		case msg := <-ch:
			return msg
		case <-ctx.Done():
			return nil
		}
	}
}

func (m *Model) onTailStatus(status string) tea.Cmd {
	if m.stopped() {
		return nil
	}
	prev := m.Conn
	switch status {
	case "connecting":
		m.Conn = ConnConnecting
	case "live":
		m.Conn = ConnLive
	case "offline":
		m.Conn = ConnOffline
	}
	if prev == ConnOffline && m.Conn == ConnLive {
		return m.Reload(TagAll)
	}
	return nil
}

func (m *Model) onTailBatch(msg tailBatchMsg) tea.Cmd {
	tags := []string{}
	for _, ev := range msg.batch.Events {
		tags = append(tags, opapi.Tags(ev)...)
		m.remember(ev)
	}
	if msg.batch.Reset {
		tags = append(tags, TagAll)
	}
	return m.startLoads(tags, msg.ack)
}

// remember keeps the newest events. A replay after a failed ack is not news.
func (m *Model) remember(ev envelope.Event) {
	if slices.ContainsFunc(m.Data.Events, func(have envelope.Event) bool { return have.Cursor == ev.Cursor }) {
		return
	}
	m.Data.Events = append(m.Data.Events, ev)
	if extra := len(m.Data.Events) - maxEvents; extra > 0 {
		m.Data.Events = slices.Delete(m.Data.Events, 0, extra)
	}
}

func (m *Model) onTailDone(err error) tea.Cmd {
	switch {
	case errors.Is(err, opclient.ErrForbidden) && m.firstLoading():
		return m.notOperator()
	case errors.Is(err, opclient.ErrUnauthorized), errors.Is(err, opclient.ErrForbidden):
		m.reject(err)
	case errors.Is(err, opclient.ErrDisabled):
		m.Conn = ConnDisabled
		m.halt(err)
	}
	return nil
}
