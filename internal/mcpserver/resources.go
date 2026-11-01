package mcpserver

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/eventlog"
)

const (
	// hintCoalesce is the least time between two hints for one URI on one
	// subscription (PROTOCOL §4.2).
	hintCoalesce = 250 * time.Millisecond
	// inboxTailSize is how many of the newest events resources/read returns.
	inboxTailSize = 50
	// maxListenURIs bounds one subscriptions/listen filter.
	maxListenURIs = 256
	// maxListenStreams bounds one credential's open subscriptions/listen
	// streams; each holds a request and a watcher per URI.
	maxListenStreams = 16
	// tailRetries bounds resources/read's retries against a racing purge.
	tailRetries = 3

	methodSubscriptionsAck = "notifications/subscriptions/acknowledged"
	methodResourcesUpdated = "notifications/resources/updated"
	eventMessage           = "message"

	// metaSubscriptionID is the spec's _meta key for a listen stream's id,
	// which is the JSON-RPC id of its subscriptions/listen request (go-sdk
	// MetaKeySubscriptionID); metaSubscriptionIDPlain is the plain spelling.
	metaSubscriptionID      = "io.modelcontextprotocol/subscriptionId"
	metaSubscriptionIDPlain = "subscriptionId"

	prefixPeerURI      = "vecnet://peer/"
	prefixPrincipalURI = "vecnet://principal/"
	suffixInboxURI     = "/inbox"

	mimeJSON = "application/json"
)

// sessionResources is a 2025-11-25 session's resource subscriptions, guarded
// by Server.mu. Each subscribed URI has one watcher goroutine; cancel ends
// them all when the session goes away.
type sessionResources struct {
	ctx     context.Context
	cancel  context.CancelFunc
	subs    map[string]*sessionSub
	pending map[string]struct{} // hints with no open GET stream to go to
}

// sessionSub is one URI's watcher; its address identifies it.
type sessionSub struct {
	cancel context.CancelFunc
}

// dropResources stops the session's watchers. Server.mu held.
func (sess *Session) dropResources() {
	if sess.res != nil {
		sess.res.cancel()
		sess.res = nil
	}
}

// registerResources adds the inbox resource methods (PROTOCOL §4.2).
func (s *Server) registerResources() {
	s.Register("resources/list", s.resourcesList)
	s.Register("resources/read", s.resourcesRead)
	s.Register("resources/subscribe", s.resourcesSubscribe)
	s.Register("resources/unsubscribe", s.resourcesUnsubscribe)
	s.Register("subscriptions/listen", s.subscriptionsListen)
}

func uriError(reason, uri string) *RPCError {
	return &RPCError{Code: ErrInvalidParams, Message: reason + ": " + uri,
		Data: map[string]any{"reason": reason, "uri": uri}}
}

func peerInboxURI(peerID string) string { return prefixPeerURI + peerID + suffixInboxURI }

func principalInboxURI(prnID string) string { return prefixPrincipalURI + prnID + suffixInboxURI }

// uriID returns the id in a "<prefix><id>/inbox" URI.
func uriID(uri, prefix string) (string, bool) {
	rest, ok := strings.CutPrefix(uri, prefix)
	if !ok {
		return "", false
	}
	id, ok := strings.CutSuffix(rest, suffixInboxURI)
	return id, ok && id != "" && !strings.Contains(id, "/")
}

// inboxOwners resolves the event-log owners behind uri for p. It answers
// the -32602 not_found for anything that is no inbox URI and -32602
// forbidden for an inbox p may not read; every other error is internal.
func (s *Server) inboxOwners(ctx context.Context, p Principal, uri string) ([]string, error) {
	c := p.Caller
	forbidden := uriError("forbidden", uri)
	if id, ok := uriID(uri, prefixPeerURI); ok {
		// A peer's own inbox has no principal check: the Principal of an open
		// stream is the one it started with, and a peer can move under it.
		q, args := `SELECT disabled = 0 FROM peers WHERE peer_id = ? AND prn_id = ?`, []any{id, c.PrincipalID}
		switch {
		case c.Kind == "peer" && id == c.PeerID:
			q, args = `SELECT disabled = 0 FROM peers WHERE peer_id = ?`, []any{id}
		case c.Kind != "principal" && c.Kind != "operator":
			return nil, forbidden
		}
		var enabled bool
		err := s.Hub.DB.QueryRowContext(ctx, q, args...).Scan(&enabled)
		if errors.Is(err, sql.ErrNoRows) || (err == nil && !enabled) {
			return nil, forbidden
		}
		if err != nil {
			return nil, fmt.Errorf("mcpserver: inbox peer: %w", err)
		}
		return []string{id}, nil
	}
	if id, ok := uriID(uri, prefixPrincipalURI); ok {
		if (c.Kind != "principal" && c.Kind != "operator") || id != c.PrincipalID {
			return nil, forbidden
		}
		owners, _, _, err := s.Hub.StreamOwners(ctx, c, "")
		return owners, err
	}
	return nil, uriError("not_found", uri)
}

// subscribeOwners is inboxOwners for the subscribe paths, where every URI
// the caller may not read is -32602 not_subscribable.
func (s *Server) subscribeOwners(ctx context.Context, p Principal, uri string) ([]string, error) {
	owners, err := s.inboxOwners(ctx, p, uri)
	var re *RPCError
	if errors.As(err, &re) {
		return nil, uriError("not_subscribable", uri)
	}
	return owners, err
}

func inboxResource(uri, name, description string) map[string]any {
	return map[string]any{"uri": uri, "name": name, "description": description, "mimeType": mimeJSON}
}

func (s *Server) resourcesList(ctx context.Context, sess *Session, _ *Request) (any, error) {
	c := sess.Principal.Caller
	resources := []map[string]any{}
	addPeer := func(id string) {
		resources = append(resources, inboxResource(peerInboxURI(id), "inbox "+id,
			"Events for peer "+id+". Read it, or call vecnet_inbox, when notified of an update."))
	}
	switch c.Kind {
	case "peer":
		addPeer(c.PeerID)
	case "principal", "operator":
		rows, err := s.Hub.DB.QueryContext(ctx,
			`SELECT peer_id FROM peers WHERE prn_id = ? AND disabled = 0 ORDER BY peer_id`, c.PrincipalID)
		if err != nil {
			return nil, fmt.Errorf("mcpserver: list peers: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				return nil, fmt.Errorf("mcpserver: list peers: %w", err)
			}
			addPeer(id)
		}
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("mcpserver: list peers: %w", err)
		}
		resources = append(resources, inboxResource(principalInboxURI(c.PrincipalID), "inbox "+c.PrincipalID,
			"Events for every peer of principal "+c.PrincipalID+"."))
	}
	return map[string]any{"resources": resources}, nil
}

func uriParam(req *Request) (string, error) {
	var params struct {
		URI string `json:"uri"`
	}
	if req.Params != nil {
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return "", &RPCError{Code: ErrInvalidParams, Message: "invalid params"}
		}
	}
	if params.URI == "" {
		return "", &RPCError{Code: ErrInvalidParams, Message: "uri is required"}
	}
	return params.URI, nil
}

func (s *Server) resourcesRead(ctx context.Context, sess *Session, req *Request) (any, error) {
	uri, err := uriParam(req)
	if err != nil {
		return nil, err
	}
	owners, err := s.inboxOwners(ctx, sess.Principal, uri)
	if err != nil {
		return nil, err
	}
	evs, next, err := s.inboxTail(ctx, owners)
	if err != nil {
		return nil, err
	}
	text, err := json.Marshal(map[string]any{"events": evs, "next_cursor": next})
	if err != nil {
		return nil, err
	}
	return map[string]any{"contents": []map[string]any{
		{"uri": uri, "mimeType": mimeJSON, "text": string(text)},
	}}, nil
}

// inboxTail returns the newest inboxTailSize events of owners, oldest first,
// and the cursor to resume after them.
func (s *Server) inboxTail(ctx context.Context, owners []string) ([]envelope.Event, string, error) {
	var evs []envelope.Event
	var err error
	for range tailRetries {
		var after eventlog.Cursor
		if after, err = s.tailStart(ctx, owners); err != nil {
			return nil, "", err
		}
		evs, err = s.Hub.Log.Read(ctx, owners, after, inboxTailSize)
		var purged eventlog.ErrCursorPurged
		if !errors.As(err, &purged) {
			break
		}
	}
	if err != nil {
		return nil, "", err
	}
	if len(evs) > 0 {
		return evs, evs[len(evs)-1].Cursor, nil
	}
	cur, err := s.Hub.Log.Current(ctx, owners)
	if err != nil {
		return nil, "", err
	}
	return evs, cur.String(), nil
}

// tailStart returns the cursor just before owners' inboxTailSize-th newest
// event, clamped to the retention floor.
func (s *Server) tailStart(ctx context.Context, owners []string) (eventlog.Cursor, error) {
	after, err := s.Hub.Log.Oldest(ctx)
	if err != nil || len(owners) == 0 {
		return after, err
	}
	args := make([]any, 0, len(owners)+1)
	for _, o := range owners {
		args = append(args, o)
	}
	args = append(args, inboxTailSize-1)
	var seq int64
	err = s.Hub.DB.QueryRowContext(ctx, `SELECT seq FROM events WHERE owner IN (`+
		strings.TrimSuffix(strings.Repeat("?,", len(owners)), ",")+`) ORDER BY seq DESC LIMIT 1 OFFSET ?`,
		args...).Scan(&seq)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return after, nil
	case err != nil:
		return after, fmt.Errorf("mcpserver: inbox tail: %w", err)
	}
	after.Seq = max(after.Seq, seq-1)
	return after, nil
}

func methodNotInEra(method string) *RPCError {
	return &RPCError{Code: ErrMethodNotFound, Message: "method not found: " + method}
}

func (s *Server) resourcesSubscribe(ctx context.Context, sess *Session, req *Request) (any, error) {
	if sess.Proto != ProtoLegacy {
		return nil, methodNotInEra(req.Method) // 2026-07-28: subscriptions/listen
	}
	uri, err := uriParam(req)
	if err != nil {
		return nil, err
	}
	ended := &RPCError{Code: ErrInvalidRequest, Message: "session ended"}
	s.mu.Lock()
	if s.sessions[sess.ID] != sess {
		s.mu.Unlock()
		return nil, ended
	}
	if sess.res == nil {
		rctx, cancel := context.WithCancel(context.Background())
		sess.res = &sessionResources{ctx: rctx, cancel: cancel,
			subs: map[string]*sessionSub{}, pending: map[string]struct{}{}}
	}
	wctx, cancel := context.WithCancel(sess.res.ctx)
	s.mu.Unlock()

	// armWatch reads the store, so it runs outside s.mu.
	w, err := s.armWatch(wctx, sess.Principal, uri)
	if err != nil {
		cancel()
		return nil, err
	}
	s.mu.Lock()
	if s.sessions[sess.ID] != sess {
		s.mu.Unlock()
		cancel()
		return nil, ended
	}
	if _, dup := sess.res.subs[uri]; dup {
		s.mu.Unlock()
		cancel()
		return map[string]any{}, nil
	}
	sub := &sessionSub{cancel: cancel}
	sess.res.subs[uri] = sub
	s.mu.Unlock()
	go func() {
		defer func() {
			cancel()
			s.mu.Lock()
			if sess.res != nil && sess.res.subs[uri] == sub {
				delete(sess.res.subs, uri)
			}
			s.mu.Unlock()
		}()
		w.run(func() bool {
			s.hintSession(sess, uri)
			return true
		}, func(bool) {})
	}()
	return map[string]any{}, nil
}

func (s *Server) resourcesUnsubscribe(ctx context.Context, sess *Session, req *Request) (any, error) {
	if sess.Proto != ProtoLegacy {
		return nil, methodNotInEra(req.Method) // 2026-07-28: subscriptions/listen
	}
	uri, err := uriParam(req)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	var sub *sessionSub
	if sess.res != nil {
		sub = sess.res.subs[uri]
		delete(sess.res.subs, uri)
		delete(sess.res.pending, uri)
	}
	s.mu.Unlock()
	if sub != nil {
		sub.cancel()
		return map[string]any{}, nil
	}
	if _, err := s.subscribeOwners(ctx, sess.Principal, uri); err != nil {
		return nil, err
	}
	return map[string]any{}, nil
}

// hintSession sends uri's hint on the session's one GET stream (never
// broadcast), or holds it, coalesced by URI, until a stream opens. A send
// that fails because a newer GET replaced the stream moves to the new one.
func (s *Server) hintSession(sess *Session, uri string) {
	for {
		s.mu.Lock()
		conn := sess.stream
		if conn == nil {
			sess.holdHintLocked(uri)
			s.mu.Unlock()
			return
		}
		s.mu.Unlock()
		if !conn.live() { // the GET handler sees the close and clears the stream
			conn.st.Close()
			s.mu.Lock()
			sess.holdHintLocked(uri)
			s.mu.Unlock()
			return
		}
		if conn.st.Send("", eventMessage, updatedNotification(uri, nil)) == nil {
			return
		}
		s.mu.Lock()
		if sess.stream == conn { // dying; its GET handler has yet to clear it
			sess.holdHintLocked(uri)
			s.mu.Unlock()
			return
		}
		s.mu.Unlock()
	}
}

// holdHintLocked keeps a hint for the next stream unless uri has since been
// unsubscribed. Server.mu held.
func (sess *Session) holdHintLocked(uri string) {
	if sess.res == nil {
		return
	}
	if _, ok := sess.res.subs[uri]; ok {
		sess.res.pending[uri] = struct{}{}
	}
}

// flushHints delivers the hints held for sess; get calls it once the new
// stream is attached, so a hint held before that is always seen here. A URI
// the session may not read now keeps its hint for a stream that opens after
// access returns: a hint held before the loss says nothing to this one.
func (s *Server) flushHints(ctx context.Context, sess *Session) {
	s.mu.Lock()
	var uris []string
	if sess.res != nil {
		for uri := range sess.res.pending {
			uris = append(uris, uri)
		}
	}
	s.mu.Unlock()
	for _, uri := range uris {
		if _, err := s.subscribeOwners(ctx, sess.Principal, uri); err != nil {
			continue
		}
		s.mu.Lock()
		if sess.res != nil {
			delete(sess.res.pending, uri)
		}
		s.mu.Unlock()
		s.hintSession(sess, uri)
	}
}

func updatedNotification(uri string, meta map[string]any) map[string]any {
	params := map[string]any{"uri": uri}
	if meta != nil {
		params["_meta"] = meta
	}
	return map[string]any{"jsonrpc": "2.0", "method": methodResourcesUpdated, "params": params}
}

// listenFilter is the subscriptions/listen filter. It is closed to four
// fields [M3]; the hub's tool, prompt and resource lists never change, so
// resourceSubscriptions is the only one it honours, and the acknowledgement
// says so.
type listenFilter struct {
	ResourceSubscriptions []string `json:"resourceSubscriptions"`
}

// listenURIs reads the filter from where the go-sdk puts it
// (params.notifications) and where PROTOCOL §4.2 writes it (params),
// without duplicates.
func listenURIs(req *Request) ([]string, error) {
	var params struct {
		listenFilter
		Notifications listenFilter `json:"notifications"`
	}
	if req.Params != nil {
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return nil, &RPCError{Code: ErrInvalidParams, Message: "invalid subscriptions/listen params"}
		}
	}
	uris := slices.Concat(params.Notifications.ResourceSubscriptions, params.ResourceSubscriptions)
	slices.Sort(uris)
	uris = slices.Compact(uris)
	if len(uris) > maxListenURIs {
		return nil, &RPCError{Code: ErrInvalidParams, Message: "too many resourceSubscriptions",
			Data: map[string]any{"reason": "too_many", "limit": maxListenURIs}}
	}
	return uris, nil
}

// subscriptionsListen serves 2026-07-28 subscriptions/listen: a POST
// response stream that opens with the acknowledgement, then carries at most
// one notifications/resources/updated per URI per hintCoalesce whenever the
// log behind a URI grows. It stays open until the client goes, the stream
// fails, every URI's access is gone at once, the credential ends (expiry,
// revocation or rotation, checked on each wake and heartbeat, credentialLive),
// or the hub shuts down, and no database transaction is open while it writes.
// A URI whose access is gone
// is parked until it returns: ending the stream for it would not help, since
// a reconnect naming that URI is refused and takes the live URIs down too.
// Only an empty filter, with nothing to wait for, gets the final result: any
// other close is left unfinished so that the client treats it as a reason to
// reconnect [M3].
func (s *Server) subscriptionsListen(ctx context.Context, sess *Session, req *Request) (any, error) {
	if sess.Proto != ProtoCurrent {
		return nil, methodNotInEra(req.Method) // 2025-11-25: resources/subscribe
	}
	if req.IsNotification() {
		return nil, &RPCError{Code: ErrInvalidRequest, Message: "subscriptions/listen needs an id"}
	}
	uris, err := listenURIs(req)
	if err != nil {
		return nil, err
	}
	release, ok := s.acquireListen(sess.Principal)
	if !ok {
		return nil, &RPCError{Code: ErrInvalidParams, Message: "too many open listen streams",
			Data: map[string]any{"reason": "too_many", "limit": maxListenStreams}}
	}
	defer release()
	lctx, cancel := context.WithCancel(ctx)
	defer cancel()
	// Armed before the acknowledgement: any append the client could cause
	// after seeing it is seen here.
	watches := make([]*watch, len(uris))
	for i, uri := range uris {
		if watches[i], err = s.armWatch(lctx, sess.Principal, uri); err != nil {
			return nil, err
		}
	}
	w, r, ok := HTTPFromContext(ctx)
	if !ok {
		return nil, errors.New("mcpserver: subscriptions/listen outside a dispatched request")
	}
	live := func() bool {
		if s.credentialLive(r) {
			return true
		}
		cancel() // the watchers return, and with them this stream
		return false
	}
	for _, wt := range watches {
		wt.live = live
	}
	st, err := s.Stream(w, r)
	if err != nil {
		return nil, err
	}
	defer st.Close()
	var owners []string
	for _, wt := range watches {
		owners = append(owners, wt.owners...)
	}
	slices.Sort(owners)
	defer s.streamReach(slices.Compact(owners))()

	meta := map[string]any{metaSubscriptionID: req.ID, metaSubscriptionIDPlain: req.ID}
	honoured := map[string]any{}
	if len(uris) > 0 {
		honoured["resourceSubscriptions"] = uris
	}
	if err := st.Send("", eventMessage, map[string]any{"jsonrpc": "2.0", "method": methodSubscriptionsAck,
		"params": map[string]any{"_meta": meta, "notifications": honoured}}); err != nil {
		return nil, nil
	}
	if len(uris) == 0 {
		_ = st.Send("", eventMessage, resultResponse{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{"_meta": meta}})
		return nil, nil
	}
	var lost atomic.Int64
	var wg sync.WaitGroup
	for i, uri := range uris {
		wg.Go(func() {
			watches[i].run(func() bool {
				return st.Send("", eventMessage, updatedNotification(uri, meta)) == nil
			}, func(denied bool) {
				if !denied {
					lost.Add(-1)
				} else if lost.Add(1) == int64(len(uris)) {
					cancel() // the watchers return, and with them this stream
				}
			})
		})
	}
	watchersDone := make(chan struct{})
	go func() {
		wg.Wait()
		close(watchersDone)
	}()
	recheck := time.NewTicker(s.heartbeat())
	defer recheck.Stop()
wait:
	for {
		select {
		case <-ctx.Done():
			break wait
		case <-st.Done():
			break wait
		case <-s.Stop: // nil (never) unless shutting down
			break wait
		case <-watchersDone:
			break wait
		case <-recheck.C:
			live()
		}
	}
	cancel()
	<-watchersDone
	return nil, nil
}

// credentialKey names p's credential for the per-credential limits.
func credentialKey(p Principal) string {
	if p.OAuth != nil {
		return "oauth:" + p.OAuth.PrincipalID
	}
	return p.Caller.Kind + ":" + p.Caller.PrincipalID + ":" + p.Caller.PeerID
}

// acquireListen takes one of p's maxListenStreams; ok is false when none is
// free. Call release once the stream ends.
func (s *Server) acquireListen(p Principal) (release func(), ok bool) {
	key := credentialKey(p)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.listens[key] >= maxListenStreams {
		return nil, false
	}
	s.listens[key]++
	return func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.listens[key]--; s.listens[key] <= 0 {
			delete(s.listens, key)
		}
	}, true
}

// streamReach marks owners connected until the returned func runs (PROTOCOL
// §8), as httpapi does for /v1/events.
func (s *Server) streamReach(owners []string) func() {
	ctx := context.Background() // the closing write must outlive the request
	for _, o := range owners {
		s.Hub.StreamOpened(ctx, o)
	}
	return func() {
		for _, o := range owners {
			s.Hub.StreamClosed(ctx, o)
		}
	}
}

// watch tails the log behind one URI. armWatch fixes its starting point; run
// reports growth past it.
type watch struct {
	s      *Server
	ctx    context.Context
	p      Principal
	uri    string
	prn    []string // a principal URI's wake key, for changes to its peer set
	owners []string
	woke   <-chan struct{}
	last   int64 // the newest seq already hinted
	// live, when set, runs on every wake; false ends run. A legacy watcher has
	// none: the session outlives any one credential check, and its hints pass
	// through the GET stream's own.
	live func() bool
}

// armWatch resolves uri for p and registers the log waiter before reading
// the current cursor, so nothing appended after it returns is missed. The
// waiter ends with ctx.
func (s *Server) armWatch(ctx context.Context, p Principal, uri string) (*watch, error) {
	owners, err := s.subscribeOwners(ctx, p, uri)
	if err != nil {
		return nil, err
	}
	w := &watch{s: s, ctx: ctx, p: p, uri: uri, owners: owners}
	if id, ok := uriID(uri, prefixPrincipalURI); ok {
		w.prn = []string{id}
	}
	if err := w.rearm(); err != nil {
		return nil, err
	}
	cur, err := s.Hub.Log.Current(ctx, w.owners)
	if err != nil {
		return nil, err
	}
	w.last = cur.Seq
	return w, nil
}

// rearm registers the next waiter, then resolves the owners. The waiter
// comes first: the hub notifies a principal's id after its peer set changes,
// so a change the resolve misses fires this waiter. When the resolve finds
// new owners the waiter does not cover them, so it arms again, and the
// abandoned waiter lasts until the next notify of any of its keys or ctx. An
// error leaves the last known owners armed.
func (w *watch) rearm() error {
	for {
		woke := w.s.Hub.Log.Wait(w.ctx, slices.Concat(w.prn, w.owners))
		owners, err := w.s.subscribeOwners(w.ctx, w.p, w.uri)
		if err != nil || slices.Equal(owners, w.owners) {
			w.woke = woke
			return err
		}
		w.owners = owners
	}
}

// run calls emit each time the log grows, at most once per hintCoalesce,
// until ctx is done, the hub stops, live fails, or emit returns false. Each wake
// re-resolves the owners, so a principal's URI picks up a peer added after
// the subscription started, even from no peers at all, when no log exists to
// wake it. Appends during the coalescing pause close the waiter armed before
// it, so the pause delays a hint and never loses one.
//
// While p may not read the URI, run stays parked on the last known owners and
// emits nothing. Everything that restores access (SetPeerDisabled, MovePeer)
// notifies the peer, so the next wake resumes, and w.last, untouched by the
// loss, makes that wake hint for anything appended meanwhile. access reports
// each change, denied or not; run calls it from its own goroutine.
func (w *watch) run(emit func() bool, access func(denied bool)) {
	log := w.s.Hub.Log
	denied := false
	for {
		select {
		case <-w.woke:
		case <-w.ctx.Done():
			return
		case <-w.s.Stop:
			return
		}
		if w.ctx.Err() != nil || (w.live != nil && !w.live()) {
			return
		}
		err := w.rearm()
		var re *RPCError
		switch {
		case errors.As(err, &re):
			if !denied {
				denied = true
				access(true)
			}
			continue
		case err != nil:
			if w.ctx.Err() != nil {
				return
			}
			slog.Warn("mcp: inbox owners", "err", err) // keep the last known owners
			if denied {
				continue // not known to be back; the next wake asks again
			}
		case denied:
			denied = false
			access(false)
		}
		cur, err := log.Current(w.ctx, w.owners)
		if err != nil {
			if w.ctx.Err() != nil {
				return
			}
			slog.Warn("mcp: inbox hint", "err", err) // a spurious hint is harmless
		} else if cur.Seq <= w.last {
			continue // a Notify that appended nothing
		} else {
			w.last = cur.Seq
		}
		if !emit() {
			return
		}
		select {
		case <-time.After(hintCoalesce):
		case <-w.ctx.Done():
			return
		case <-w.s.Stop:
			return
		}
	}
}
