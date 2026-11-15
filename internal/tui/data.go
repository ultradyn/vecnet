package tui

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/XertroV/vecnet/internal/blobwire"
	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/hubnotice"
	"github.com/XertroV/vecnet/internal/opapi"
	"github.com/XertroV/vecnet/internal/opclient"
)

// Data is the last snapshot of everything the hub showed this client. A failed
// load keeps the old value and records the error under its resource key.
type Data struct {
	Inbox   opapi.Inbox
	Threads opapi.ThreadPage
	// Mine is GET /v1/admin/threads?participant=<me> (Your Threads). Others is
	// the same list with exclude_participant=<me> (Other Threads). ThreadsKnown
	// is set only after a successful unfiltered page, and that page is a
	// fallback only once the operator's identity is known. The dashboard keeps
	// Threads.
	Mine, Others                         opapi.ThreadPage
	MineKnown, OthersKnown, ThreadsKnown bool
	Peers                                []opapi.PeerRow
	Subs                                 []opapi.Subscription
	Cuts                                 opapi.LoopCuts
	Health                               opapi.DeliveryHealth
	Audit                                opapi.AuditPage
	Counters                             map[string]int
	Notices                              []envelope.HubNotice // the hub's active notices, oldest first
	// Prefs is the last successful preferences snapshot. PrefsKnown is set
	// once a GET has been applied. PrefsNote explains a 404 or 403, which the
	// preferences screen shows as unavailable rather than as a failed load.
	Prefs      opapi.Preferences
	PrefsKnown bool
	PrefsNote  string
	Thread     map[string]*ThreadState
	Events     []envelope.Event // newest 200, oldest first, for the raw log
	Errs       map[string]error
}

// ThreadState is one opened thread.
type ThreadState struct {
	BlobStates map[string]blobwire.State
	Detail     opapi.ThreadDetail
	Messages   []envelope.Message
	Matrix     opapi.Matrix
	Loading    bool

	// rewind is one past the oldest held message whose reactions changed
	// since the last load (0: none), and rewinds counts the reaction events
	// that set it, so a load that raced a newer one leaves it set.
	rewind, rewinds int
}

// TagAll asks Reload for every resource and every opened thread. The other tags
// are resource keys: opapi.Tags' "inbox", "threads", "peers", "notices" and
// "thread:<id>", and the untagged "subs", "cuts", "health", "audit" and "counters".
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
	allKeys = []string{"inbox", "threads", "peers", "subs", "cuts", "health", "audit", "counters", "notices"}
	// The inbox is event-tagged, but its snapshot also carries the hub's
	// clock: a minute's reload keeps HubNow right across a quiet spell in
	// which this client's clock steps (Review Focus 3).
	slowKeys = []string{"inbox", "subs", "cuts", "health", "audit", "counters"}
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
	tailAckMsg  struct{}
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
			if m.Data.Inbox.Me.PeerID != "" {
				add("mine")
				add("other")
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
			// Your Threads and Other Threads ride with the unfiltered page once
			// the inbox has named the operator. Before that, the inbox load
			// starts them once.
			if tag == "threads" && m.Data.Inbox.Me.PeerID != "" {
				add("mine")
				add("other")
			}
		}
	}
	return keys
}

func (m *Model) startLoads(tags []string, ack chan<- error) tea.Cmd {
	keys := m.expand(tags)
	if m.stopped() {
		// A stop can land before the first reload runs; the suppressed keys
		// will never load, so the first load must not wait on them.
		for _, key := range keys {
			delete(m.first, key)
		}
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

// receivedHubError exposes checked recovery fields while keeping the original
// typed chain for classification. Callers must never unwrap for presentation.
type receivedHubError struct {
	checked *opclient.Error
	cause   error
}

func (e *receivedHubError) Error() string { return e.checked.Error() }
func (e *receivedHubError) Unwrap() error { return e.cause }
func (e *receivedHubError) As(target any) bool {
	p, ok := target.(**opclient.Error)
	if ok {
		*p = e.checked
	}
	return ok
}

// onLoad applies a finished load, runs the one load folded behind it if there
// is one, and otherwise settles whatever waits on this key.
func (m *Model) onLoad(msg loadMsg) tea.Cmd {
	var received *opclient.Error
	if errors.As(msg.err, &received) && received.Status == http.StatusGone && received.Code == "hub_moved" {
		checked := *received
		checked.Message, checked.Details = hubnotice.ProjectRelocation(received.Details, true)
		msg.err = &receivedHubError{checked: &checked, cause: msg.err}
	}
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
	case msg.err != nil && msg.key != "prefs" && m.Conn == ConnLive && !errors.Is(msg.err, context.Canceled):
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
	// The first inbox names the operator after the opening thread page was
	// already requested. One participant-page load follows it. Later thread
	// reloads include those pages themselves.
	var follow tea.Cmd
	if msg.key == "inbox" && msg.err == nil && m.Data.Inbox.Me.PeerID != "" && !m.Data.MineKnown && !m.inflight["mine"] && !m.stopped() {
		follow = m.Reload("mine", "other")
	}
	return tea.Batch(again, follow, m.broadcast(Loaded{Key: msg.key, Err: msg.err}))
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
		// Another toast during a long command must not lose its progress line.
		if m.progress != "" && m.inflight[retryKey] {
			m.toast = toast{text: m.progress, until: now.Add(retryShown)}
		}
	}
	cmds := []tea.Cmd{tick()}
	if now.Sub(m.lastSlow) >= m.slowEvery && m.Conn == ConnLive {
		m.lastSlow = now
		tags := slices.Clone(slowKeys)
		for id, st := range m.Data.Thread {
			if slices.ContainsFunc(st.Messages, func(msg envelope.Message) bool {
				return slices.ContainsFunc(msg.Attachments, func(att envelope.Attachment) bool { return att.BlobID != "" })
			}) {
				tags = append(tags, threadPrefix+id)
			}
		}
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
			return loadMsg{key: key, err: err, hubNow: v.Now, at: now(), apply: func(m *Model) {
				m.Data.Inbox = v
				for _, st := range m.Data.Thread {
					if st.BlobStates == nil {
						st.BlobStates = map[string]blobwire.State{}
					}
					for id, state := range v.BlobStates {
						st.BlobStates[id] = state
					}
				}
			}}
		}
	case "threads":
		return func() tea.Msg {
			v, err := api.Threads(ctx, opapi.ThreadQuery{Limit: pageLimit})
			return loadMsg{key: key, err: err, apply: func(m *Model) {
				m.Data.Threads = v
				m.Data.ThreadsKnown = true
			}}
		}
	case "mine":
		me := m.Data.Inbox.Me.PeerID
		if me == "" {
			return nil
		}
		return func() tea.Msg {
			v, err := api.Threads(ctx, opapi.ThreadQuery{Participant: me, Limit: pageLimit})
			return loadMsg{key: key, err: err, apply: func(m *Model) {
				m.Data.Mine = v
				m.Data.MineKnown = true
			}}
		}
	case "other":
		me := m.Data.Inbox.Me.PeerID
		if me == "" {
			return nil
		}
		limit := m.OtherLimit
		if limit <= 0 {
			limit = otherPage
		}
		return func() tea.Msg {
			v, err := api.Threads(ctx, opapi.ThreadQuery{ExcludeParticipant: me, Limit: limit})
			return loadMsg{key: key, err: err, apply: func(m *Model) {
				m.Data.Others = v
				m.Data.OthersKnown = true
			}}
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
	case "notices":
		return func() tea.Msg {
			v, err := api.Notices(ctx)
			// Non-HTTP APIs must meet the same URL contract as opclient.
			if err == nil {
				for _, n := range v {
					if err = hubnotice.ValidateNotice(n, true); err != nil {
						break
					}
				}
			}
			// A hub from before notices has none to show; that is not an error.
			if isStatus(err, 404) {
				v, err = nil, nil
			}
			return loadMsg{key: key, err: err, apply: func(m *Model) { m.Data.Notices = v }}
		}
	case "prefs":
		return func() tea.Msg {
			v, err := api.Preferences(ctx)
			// Forbidden and older hubs have no preferences view to edit. Say so
			// without a reload error, and do not PATCH.
			switch {
			case isStatus(err, 404):
				return loadMsg{key: key, apply: func(m *Model) {
					m.Data.Prefs = opapi.Preferences{}
					m.Data.PrefsKnown = true
					m.Data.PrefsNote = "This hub has no preferences API."
				}}
			case isStatus(err, 403):
				return loadMsg{key: key, apply: func(m *Model) {
					m.Data.Prefs = opapi.Preferences{}
					m.Data.PrefsKnown = true
					m.Data.PrefsNote = "This credential cannot read preferences."
				}}
			}
			return loadMsg{key: key, err: err, apply: func(m *Model) {
				m.Data.Prefs = v
				m.Data.PrefsKnown = true
				m.Data.PrefsNote = ""
			}}
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
	// A held message's reactions change in place (PROTOCOL §17.3), so a load
	// after a reaction event goes back to that message.
	from, rewinds := len(st.Messages), st.rewinds
	if st.rewind > 0 {
		from = st.rewind - 1
	}
	after := ""
	if from > 0 {
		after = st.Messages[from-1].MessageID
	}
	// Capture references before the command leaves the model goroutine. An
	// incremental history page cannot project state for older, read messages.
	refs := map[string]string{}
	for _, msg := range st.Messages {
		for _, att := range msg.Attachments {
			if att.BlobID != "" {
				refs[att.BlobID] = att.SHA256
			}
		}
	}
	api, ctx, now := m.API, m.ctx, m.Now
	return func() tea.Msg {
		detail, err := api.Thread(ctx, id)
		if err != nil {
			return loadMsg{key: key, err: err}
		}
		states := map[string]blobwire.State{}
		var messages []envelope.Message
		for cursor, page := after, 0; page < maxPages; page++ {
			h, err := api.History(ctx, id, cursor, pageLimit)
			if err != nil {
				return loadMsg{key: key, err: err}
			}
			for id, state := range h.BlobStates {
				states[id] = state
			}
			messages = append(messages, h.Messages...)
			if h.NextAfter == nil || *h.NextAfter == "" {
				break
			}
			cursor = *h.NextAfter
		}
		for id, sha := range refs {
			if _, fresh := states[id]; fresh {
				continue
			}
			state, err := api.BlobStatus(ctx, sha, id)
			if err != nil {
				// One status blip must not drop the history page or the
				// sibling refs. The row stays, with an unknown projection.
				states[id] = blobwire.State{State: "unknown"}
				continue
			}
			states[id] = state
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
			if st.BlobStates == nil {
				st.BlobStates = map[string]blobwire.State{}
			}
			for id, state := range states {
				st.BlobStates[id] = state
			}
			if st.rewinds == rewinds {
				st.rewind = 0
			}
			held := make(map[string]int, len(st.Messages))
			for i, msg := range st.Messages {
				held[msg.MessageID] = i
			}
			for _, msg := range messages {
				if i, ok := held[msg.MessageID]; ok {
					st.Messages[i] = msg
					continue
				}
				st.Messages = append(st.Messages, msg)
			}
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
			if err := hubnotice.ValidateBatch(b.Events, true); err != nil {
				return err
			}
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
		accepted := func(string) { send(tailAckMsg{}) }
		return tailDoneMsg{err: api.TailWithAck(ctx, handle, status, accepted)}
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
	case "restarting":
		m.Conn = ConnRestarting
	}
	// Coming back refetches what the outage or restart may have hidden.
	if (prev == ConnOffline || prev == ConnRestarting) && m.Conn == ConnLive {
		return m.Reload(TagAll)
	}
	return nil
}

func (m *Model) onTailBatch(msg tailBatchMsg) tea.Cmd {
	if err := hubnotice.ValidateBatch(msg.batch.Events, true); err != nil {
		if msg.ack != nil {
			msg.ack <- err
		}
		return nil
	}
	tags := []string{}
	for _, ev := range msg.batch.Events {
		eventTags := opapi.Tags(ev)
		tags = append(tags, eventTags...)
		if ev.Type == "delivery" {
			for _, tag := range eventTags {
				if strings.HasPrefix(tag, threadPrefix) {
					m.accepting[tag] = true
				}
			}
		}
		m.remember(ev)
		switch ev.Type {
		case "reaction":
			var r envelope.ReactionEvent
			if json.Unmarshal(ev.Data, &r) == nil {
				m.rewindTo(r.ThreadID, r.MessageID)
			}
		case "operator_view":
			// A non-participant operator learns of a reaction without the
			// message it changed (PROTOCOL §17.3): refetch every held one.
			var v struct {
				Reason   string `json:"reason"`
				ThreadID string `json:"thread_id"`
			}
			if json.Unmarshal(ev.Data, &v) == nil && v.Reason == "reaction" {
				m.rewindAll(v.ThreadID)
			}
		}
	}
	if msg.batch.Reset {
		// A purged cursor skipped events, reactions among them, which change
		// held messages in place: every held thread is fetched again whole.
		for id := range m.Data.Thread {
			m.rewindAll(id)
		}
		tags = append(tags, TagAll)
	}
	return m.startLoads(tags, msg.ack)
}

// onTailAck corrects snapshots loaded before the ack accepted our deliveries.
// The hub sends us no event for our own ack. Keep tags across refused acks:
// the next successful one covers all earlier deliveries too.
func (m *Model) onTailAck() tea.Cmd {
	tags := make([]string, 0, len(m.accepting))
	for tag := range m.accepting {
		tags = append(tags, tag)
	}
	clear(m.accepting)
	return m.startLoads(tags, nil)
}

// rewindTo marks a held message whose reactions changed, so the thread's next
// load fetches it again with its new reactions.
func (m *Model) rewindTo(threadID, messageID string) {
	st := m.Data.Thread[threadID]
	if st == nil {
		return
	}
	i := slices.IndexFunc(st.Messages, func(msg envelope.Message) bool { return msg.MessageID == messageID })
	if i < 0 {
		return
	}
	st.rewindFrom(i + 1)
}

// rewindAll marks every message held for a thread, for a change that does not
// say which message it touched.
func (m *Model) rewindAll(threadID string) {
	if st := m.Data.Thread[threadID]; st != nil && len(st.Messages) > 0 {
		st.rewindFrom(1)
	}
}

func (st *ThreadState) rewindFrom(i int) {
	if st.rewind == 0 || i < st.rewind {
		st.rewind = i
	}
	st.rewinds++
}

// remember keeps the newest events. A replay after a failed ack is not news.
func (m *Model) remember(ev envelope.Event) {
	if hubnotice.ValidateEvent(ev, true) != nil {
		return
	}
	for _, st := range m.Data.Thread {
		if st.BlobStates == nil {
			st.BlobStates = map[string]blobwire.State{}
		}
		for id, state := range ev.BlobStates {
			st.BlobStates[id] = state
		}
	}
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
