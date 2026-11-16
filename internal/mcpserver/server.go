// Package mcpserver is the hub's hand-rolled MCP front door on /mcp
// (PROTOCOL §4). It speaks JSON-RPC 2.0 over Streamable HTTP in two eras on
// one endpoint: 2026-07-28 (POST-only, no sessions, server/discover) and
// 2025-11-25 (initialize, Mcp-Session-Id, the GET stream). It authenticates
// every request itself (bearer now, OAuth from Task 6) and dispatches to a
// method table that later tasks fill with tools, resources and MCP Events.
//
// The official go-sdk is not used as the server: its dispatch rejects
// unknown methods before any middleware and its ServerCapabilities has no
// top-level events key (docs/adr/0001-tech-stack.md, amendment).
package mcpserver

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/XertroV/vecnet/internal/auth"
	"github.com/XertroV/vecnet/internal/egress"
	"github.com/XertroV/vecnet/internal/events"
	"github.com/XertroV/vecnet/internal/hub"
)

// The protocol revisions /mcp serves [R12].
const (
	ProtoCurrent = "2026-07-28"
	ProtoLegacy  = "2025-11-25"
)

// SupportedVersions is every revision /mcp accepts, newest first.
var SupportedVersions = []string{ProtoCurrent, ProtoLegacy, "2025-06-18"}

// Defaults for Server fields left zero.
const (
	DefaultHeartbeat  = 15 * time.Second // ": hb" on idle streams (PROTOCOL §4.2)
	DefaultSessionTTL = time.Hour        // idle 2025-11-25 session lifetime
)

// maxSessionsPerCaller bounds one credential's live 2025 sessions; minting
// past it evicts that caller's least recently used session.
const maxSessionsPerCaller = 64

// Version is serverInfo.version.
var Version = "dev"

// Header names.
const (
	hdrProtocolVersion = "MCP-Protocol-Version"
	hdrSessionID       = "Mcp-Session-Id"
)

// metaVersionKeys are the params._meta keys that may carry a 2026 request's
// protocol version: the spec's namespaced key and the plain key.
var metaVersionKeys = []string{"io.modelcontextprotocol/protocolVersion", "protocolVersion"}

// Session is the context a handler runs in. A 2026-07-28 request gets a
// transient Session (ID "", Proto ProtoCurrent); a 2025-11-25 request gets
// the session its initialize minted.
type Session struct {
	ID                        string
	Proto                     string
	Principal                 Principal
	clientName, clientVersion string // bounded initialize metadata, never credentials

	// 2025 only:
	stream    *sseConn
	streamSeq uint64 // orders GETs before their initial flush; guarded by Server.mu
	created   time.Time
	lastUsed  time.Time
	res       *sessionResources // nil until the first resources/subscribe; guarded by Server.mu
}

// sseConn is a session's open GET stream. live re-checks the credential that
// opened it.
type sseConn struct {
	st   *Streamer
	live func() bool
}

// Handler serves one method. err may be an *RPCError; any other error is
// logged and answered with ErrInternal. A nil result is written as {}.
type Handler func(ctx context.Context, s *Session, req *Request) (result any, err error)

// Server is the /mcp handler.
type Server struct {
	Hub          *hub.Hub
	Auth         Authenticator
	PublicOrigin string // scheme://host[:port] of Cfg.PublicURL; always allowed
	// AllowedOrigins are extra normalized exact origins; nil uses https://chatgpt.com.
	AllowedOrigins []string
	// LegacySessions controls initialize/session transports: on (default),
	// off-for-oauth, or off. The current stateless protocol is always available.
	LegacySessions string
	Heartbeat      time.Duration // ": hb" interval; zero means DefaultHeartbeat
	SessionTTL     time.Duration // idle 2025 session lifetime; zero means DefaultSessionTTL
	Now            func() time.Time

	// Touch records a reachability observation for an authenticated peer
	// credential (PROTOCOL §8), as httpapi's bearer middleware does; nil
	// disables it.
	Touch func(ctx context.Context, peerID, value string)
	// Stop, when non-nil, is closed at shutdown and ends open GET streams.
	Stop <-chan struct{}
	// Kick, when non-nil, wakes the push worker after a tool send, as
	// POST /v1/messages does.
	Kick func()

	// Events is the subscription store, Egress the guard every callback URL
	// passes, and EventsClient (an Egress client, which never follows a
	// redirect) carries the verification challenge. New sets all three from
	// the hub.
	Events       *events.Store
	Egress       egress.Policy
	EventsClient *http.Client
	// ResolveDotPeer names the peer an OAuth principal is bound to, "" when
	// it has none (oauth.Server.ResolveDotPeer). While nil, every OAuth
	// principal is not_linked at events/subscribe.
	ResolveDotPeer func(ctx context.Context, principalID string) (peerID string, err error)

	methods      map[string]Handler
	capabilities func(proto string) map[string]any

	mu       sync.Mutex
	sessions map[string]*Session
	listens  map[string]int // open subscriptions/listen streams per credential
}

// New returns a Server for the hub h whose public URL is publicURL
// (Cfg.PublicURL). ping, the tools, the resources and MCP Events are
// registered.
func New(h *hub.Hub, a Authenticator, publicURL string) *Server {
	s := &Server{
		Hub:          h,
		Auth:         a,
		PublicOrigin: normOrigin(publicURL),
		Heartbeat:    DefaultHeartbeat,
		SessionTTL:   DefaultSessionTTL,
		Now:          time.Now,
		methods:      map[string]Handler{},
		capabilities: staticCapabilities,
		sessions:     map[string]*Session{},
		listens:      map[string]int{},
	}
	if h != nil {
		s.Now = h.Now
		s.Touch = h.TouchReachability
		s.Events = events.NewStore(h)
		s.Egress = h.EgressPolicy()
		s.EventsClient = s.Egress.Client(events.ClientTimeout)
	}
	s.Register("ping", func(context.Context, *Session, *Request) (any, error) {
		return map[string]any{}, nil
	})
	s.registerTools()
	s.registerPrompts()
	s.registerResources()
	s.registerEvents()
	return s
}

// Register adds a method handler. It panics on a duplicate, an empty name,
// or a name /mcp handles itself (initialize, server/discover).
func (s *Server) Register(method string, h Handler) {
	if method == "" || method == "initialize" || method == "server/discover" {
		panic("mcpserver: cannot register method " + method)
	}
	if _, dup := s.methods[method]; dup {
		panic("mcpserver: duplicate method " + method)
	}
	s.methods[method] = h
}

// Capabilities returns the server capabilities for proto. MCP Events is
// advertised under both capabilities.events and
// capabilities.extensions["io.modelcontextprotocol/events"] (PROTOCOL §4.4).
func (s *Server) Capabilities(proto string) map[string]any {
	return s.capabilities(proto)
}

func staticCapabilities(string) map[string]any {
	return map[string]any{
		"tools":      map[string]any{"listChanged": false},
		"prompts":    map[string]any{"listChanged": false},
		"resources":  map[string]any{"subscribe": true, "listChanged": false},
		"events":     map[string]any{},
		"extensions": map[string]any{"io.modelcontextprotocol/events": map[string]any{}},
	}
}

func serverInfo() map[string]any {
	return map[string]any{"name": "vecnet", "version": Version}
}

// completeResult adds the current era's envelope without mutating the
// handler's result or discarding other metadata.
func completeResult(res any) (map[string]any, error) {
	b, err := json.Marshal(res)
	if err != nil {
		return nil, err
	}
	var result map[string]any
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	if err := dec.Decode(&result); err != nil {
		return nil, err
	}
	if result == nil {
		result = map[string]any{}
	}
	meta, ok := result["_meta"].(map[string]any)
	if !ok {
		if result["_meta"] != nil {
			return nil, errors.New("mcp: result _meta must be an object")
		}
		meta = map[string]any{}
	}
	meta["io.modelcontextprotocol/serverInfo"] = serverInfo()
	result["_meta"] = meta
	result["resultType"] = "complete"
	return result, nil
}

func (s *Server) heartbeat() time.Duration {
	if s.Heartbeat > 0 {
		return s.Heartbeat
	}
	return DefaultHeartbeat
}

func (s *Server) sessionTTL() time.Duration {
	if s.SessionTTL > 0 {
		return s.SessionTTL
	}
	return DefaultSessionTTL
}

func (s *Server) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// ServeHTTP implements /mcp. Order: Origin (403), method (405),
// authentication (401), then the per-method handling. Last-Event-ID is
// never read.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	diag, _ := r.Context().Value(diagKey{}).(*requestDiag)
	if diag == nil {
		var dw *diagResponse
		dw, r, diag = beginDiag(w, r)
		w = dw
		defer diag.log(s, dw, r)
	} else {
		// The outer HTTP observer also sees middleware refusals. Keep a
		// local writer for RPC outcome capture without a second log.
		w = &diagResponse{ResponseWriter: w, diag: diag}
	}
	if !s.originOK(r) {
		httpErr(w, http.StatusForbidden, "forbidden", "Origin is not allowed")
		return
	}
	switch r.Method {
	case http.MethodPost, http.MethodGet, http.MethodDelete:
	default:
		w.Header().Set("Allow", "GET, POST, DELETE")
		httpErr(w, http.StatusMethodNotAllowed, "method_not_allowed", "use POST")
		return
	}
	p, err := s.Auth.Authenticate(r.Context(), r)
	diag.principal = p
	if err != nil {
		if errors.Is(err, auth.ErrUnauthenticated) {
			outcome := "invalid"
			if BearerToken(r) == "" {
				outcome = "none"
			}
			if errors.Is(err, ErrExpiredToken) {
				outcome = "expired"
			}
			noteDiagAuth(r, outcome)
			w.Header().Set("WWW-Authenticate",
				`Bearer resource_metadata="`+s.PublicOrigin+`/.well-known/oauth-protected-resource"`)
			httpErr(w, http.StatusUnauthorized, "unauthenticated", "missing, unknown or revoked credential")
			return
		}
		httpErr(w, http.StatusInternalServerError, "internal", "internal")
		return
	}
	noteDiagAuth(r, "ok")
	if s.Touch != nil && p.Caller.PeerID != "" {
		s.Touch(context.WithoutCancel(r.Context()), p.Caller.PeerID, "reachable")
	}
	switch r.Method {
	case http.MethodPost:
		s.post(w, r, p)
	case http.MethodGet:
		s.get(w, r, p)
	case http.MethodDelete:
		s.delete(w, r, p)
	}
}

func (s *Server) post(w http.ResponseWriter, r *http.Request, p Principal) {
	if mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mt != "application/json" {
		httpErr(w, http.StatusUnsupportedMediaType, "unsupported_media_type", "Content-Type must be application/json")
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			writeEnvelope(w, http.StatusRequestEntityTooLarge, "payload_too_large", "request body exceeds 256 KiB",
				map[string]any{"limit": maxErr.Limit})
			return
		}
		httpErr(w, http.StatusBadRequest, "bad_request", "could not read request body")
		return
	}
	// The authenticated, bounded body is already in memory. Capture only the
	// routing method and id type even for malformed envelopes/client replies.
	if d, _ := r.Context().Value(diagKey{}).(*requestDiag); d != nil {
		var meta struct {
			Method string          `json:"method"`
			ID     json.RawMessage `json:"id"`
		}
		if json.Unmarshal(body, &meta) == nil {
			d.method, d.idType = meta.Method, diagIDType(meta.ID)
		}
	}
	pr := parseBody(body)
	switch {
	case pr.err != nil:
		if d, _ := r.Context().Value(diagKey{}).(*requestDiag); d != nil {
			var meta struct {
				Method string `json:"method"`
			}
			_ = json.Unmarshal(body, &meta)
			d.method = meta.Method
			if d.method == "" {
				d.method = "(invalid)"
			}
		}
		writeRPCError(w, pr.errID, pr.err)
		return
	case pr.response:
		w.WriteHeader(http.StatusAccepted) // a reply to a server request; /mcp sends none
		return
	}
	req := pr.req
	noteRequest(r, req)
	hv := r.Header.Get(hdrProtocolVersion)
	if hv == ProtoCurrent && req.Method != "initialize" {
		noteDiagProtocol(r, ProtoCurrent)
	}

	switch req.Method {
	case "initialize":
		if !s.legacyAllowed(p) {
			writeRPCStatus(w, http.StatusNotFound, req.ID, &RPCError{Code: ErrMethodNotFound, Message: "method not found"})
			return
		}
		if !accepts(r, "application/json") {
			httpErr(w, http.StatusNotAcceptable, "not_acceptable", "Accept must list application/json")
			return
		}
		s.initialize(w, r, req, p)
		return
	case "server/discover":
		if hv == "" || hv != metaVersion(req) {
			httpErr(w, http.StatusBadRequest, "bad_request", "MCP-Protocol-Version header must equal the params._meta protocol version")
			return
		}
		if !accepts(r, "application/json", "text/event-stream") {
			httpErr(w, http.StatusNotAcceptable, "not_acceptable", "Accept must list application/json and text/event-stream")
			return
		}
		s.discover(w, req)
		return
	}

	var sess *Session
	switch sid := r.Header.Get(hdrSessionID); {
	case hv == ProtoCurrent:
		// 2026-07-28: no sessions; a stale Mcp-Session-Id is ignored.
		if mv := metaVersion(req); mv != "" && mv != hv {
			httpErr(w, http.StatusBadRequest, "bad_request", "MCP-Protocol-Version header must equal the params._meta protocol version")
			return
		}
		if !accepts(r, "application/json", "text/event-stream") {
			httpErr(w, http.StatusNotAcceptable, "not_acceptable", "Accept must list application/json and text/event-stream")
			return
		}
		sess = &Session{Proto: ProtoCurrent, Principal: p}
	case sid != "":
		var ok bool
		if sess, ok = s.lookup(sid, p); !ok {
			httpErr(w, http.StatusNotFound, "session_not_found", "session not found")
			return
		}
		noteDiagSession(r, sess)
		if hv != "" && hv != sess.Proto {
			httpErr(w, http.StatusBadRequest, "bad_request", "MCP-Protocol-Version does not match the session's protocol version")
			return
		}
		if !accepts(r, "application/json") {
			httpErr(w, http.StatusNotAcceptable, "not_acceptable", "Accept must list application/json")
			return
		}
	case hv != "":
		httpErr(w, http.StatusBadRequest, "bad_request", "unsupported MCP-Protocol-Version "+hv+" without a session")
		return
	default:
		httpErr(w, http.StatusBadRequest, "bad_request", "MCP-Protocol-Version header (2026-07-28) or Mcp-Session-Id (2025-11-25) required")
		return
	}
	s.dispatch(w, r, sess, p, req)
}

// reqState lets Stream tell ServeHTTP that a handler took over the response.
type reqState struct {
	w        http.ResponseWriter
	r        *http.Request
	streamed bool
}

type reqStateKey struct{}

// HTTPFromContext returns the response writer and request of the /mcp call a
// handler is serving, for handlers that answer with a stream
// (s.Stream(w, r)); ok is false outside a dispatched handler.
func HTTPFromContext(ctx context.Context) (w http.ResponseWriter, r *http.Request, ok bool) {
	st, ok := ctx.Value(reqStateKey{}).(*reqState)
	if !ok {
		return nil, nil, false
	}
	return st.w, st.r, true
}

func (s *Server) dispatch(w http.ResponseWriter, r *http.Request, sess *Session, p Principal, req *Request) {
	if (req.Method == "events/subscribe" || req.Method == "events/unsubscribe") && !s.RequireScope(w, r, p, ScopeEventsWrite, req.ID) {
		return
	}
	st := &reqState{w: w}
	ctx := context.WithValue(withPrincipal(r.Context(), p), reqStateKey{}, st)
	st.r = r.WithContext(ctx)

	h, ok := s.methods[req.Method]
	if req.IsNotification() {
		if ok {
			if _, err := h(ctx, sess, req); err != nil {
				// An RPCError message can quote the request body (a URI, an
				// argument): log the registered method and the numeric code only.
				var rpc *RPCError
				if errors.As(err, &rpc) {
					slog.Warn("mcp: notification handler", "method", req.Method, "code", rpc.Code)
				} else {
					slog.Warn("mcp: notification handler", "method", req.Method, "code", ErrInternal)
				}
				if d, _ := r.Context().Value(diagKey{}).(*requestDiag); d != nil {
					d.code = ErrInternal
					var re *RPCError
					if errors.As(err, &re) {
						d.rpcError(re)
					}
				}
			}
		}
		if !st.streamed {
			w.WriteHeader(http.StatusAccepted)
		}
		return
	}
	if !ok {
		status := http.StatusOK
		if sess.Proto == ProtoCurrent {
			status = http.StatusNotFound
		}
		writeRPCStatus(w, status, req.ID, &RPCError{Code: ErrMethodNotFound, Message: "method not found"})
		return
	}
	res, err := h(ctx, sess, req)
	if st.streamed {
		return
	}
	if err == nil && sess.Proto == ProtoCurrent {
		res, err = completeResult(res)
		if err == nil && req.Method == "tools/list" {
			result := res.(map[string]any)
			result["ttlMs"], result["cacheScope"] = 0, "private"
		}
	}
	if err != nil {
		var re *RPCError
		if !errors.As(err, &re) {
			re = &RPCError{Code: ErrInternal, Message: "internal error"}
		}
		writeRPCError(w, req.ID, re)
		return
	}
	writeResult(w, req.ID, res)
}

// unsupportedVersion is the -32602 answer to a revision /mcp does not serve
// [R12]: data.supportedVersions lists ours (data.supported and
// data.requested follow the go-sdk's UnsupportedProtocolVersionData).
func unsupportedVersion(requested string) *RPCError {
	return &RPCError{Code: ErrInvalidParams, Message: "unsupported protocol version",
		Data: map[string]any{
			"supportedVersions": SupportedVersions,
			"supported":         SupportedVersions,
			"requested":         requested,
		}}
}

// initialize echoes supported session revisions. Unknown revisions (and a
// current-era client using initialize rather than discover) retain the legacy
// fallback; the stateless current era is negotiated only by server/discover.
func (s *Server) initialize(w http.ResponseWriter, r *http.Request, req *Request, p Principal) {
	if req.IsNotification() {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	var params struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	if req.Params != nil {
		if err := json.Unmarshal(req.Params, &params); err != nil {
			writeRPCError(w, req.ID, &RPCError{Code: ErrInvalidParams, Message: "invalid initialize params"})
			return
		}
	}
	if params.ProtocolVersion == "" {
		writeRPCError(w, req.ID, unsupportedVersion(""))
		return
	}
	proto := ProtoLegacy
	if params.ProtocolVersion != ProtoCurrent && slices.Contains(SupportedVersions, params.ProtocolVersion) {
		proto = params.ProtocolVersion
	}
	sess := s.mint(p, proto)
	if d, _ := r.Context().Value(diagKey{}).(*requestDiag); d != nil {
		sess.clientName, sess.clientVersion = diagTruncate(d.clientName, 64), diagTruncate(d.clientVersion, 64)
	}
	noteDiagSession(r, sess)
	w.Header().Set(hdrSessionID, sess.ID)
	writeResult(w, req.ID, map[string]any{
		"protocolVersion": sess.Proto,
		"capabilities":    s.Capabilities(sess.Proto),
		"serverInfo":      serverInfo(),
		"instructions":    Instructions,
	})
}

// discover answers 2026-07-28 server/discover; no session is minted.
func (s *Server) discover(w http.ResponseWriter, req *Request) {
	if req.IsNotification() {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	if v := metaVersion(req); v != ProtoCurrent {
		writeRPCError(w, req.ID, unsupportedVersion(v))
		return
	}
	res, err := completeResult(map[string]any{
		"protocolVersion":   ProtoCurrent,
		"supportedVersions": []string{ProtoCurrent},
		"ttlMs":             0,
		"cacheScope":        "private",
		"capabilities":      s.Capabilities(ProtoCurrent),
		"serverInfo":        serverInfo(),
		"instructions":      Instructions,
	})
	if err != nil {
		slog.Error("mcp: discover result", "err", err)
		writeRPCError(w, req.ID, &RPCError{Code: ErrInternal, Message: "internal error"})
		return
	}
	writeResult(w, req.ID, res)
}

// get serves the 2025-11-25 session stream. One stream per session: a
// second GET closes the first.
func (s *Server) get(w http.ResponseWriter, r *http.Request, p Principal) {
	if !s.legacyAllowed(p) {
		w.Header().Set("Allow", "POST")
		httpErr(w, http.StatusMethodNotAllowed, "method_not_allowed", "use POST")
		return
	}
	sid := r.Header.Get(hdrSessionID)
	if r.Header.Get(hdrProtocolVersion) == ProtoCurrent || sid == "" {
		w.Header().Set("Allow", "POST")
		httpErr(w, http.StatusMethodNotAllowed, "method_not_allowed", "GET streams exist only for 2025-11-25 sessions")
		return
	}
	sess, ok := s.lookup(sid, p)
	if !ok {
		httpErr(w, http.StatusNotFound, "session_not_found", "session not found")
		return
	}
	noteDiagProtocol(r, sess.Proto)
	if !accepts(r, "text/event-stream") {
		httpErr(w, http.StatusNotAcceptable, "not_acceptable", "Accept must list text/event-stream")
		return
	}
	// The client can open its next GET as soon as Stream flushes headers,
	// before this handler attaches. Reserve the order now so a slow older
	// handler cannot later replace a newer stream.
	s.mu.Lock()
	sess.streamSeq++
	seq := sess.streamSeq
	s.mu.Unlock()
	st, err := s.Stream(w, r)
	if err != nil {
		return
	}
	owners, _, _, err := s.Hub.StreamOwners(r.Context(), p.Caller, "")
	if err != nil {
		slog.Warn("mcp: stream owners", "err", err) // the stream works without it
	}
	defer s.streamReach(owners)()
	conn := &sseConn{st: st, live: func() bool { return s.credentialLive(r) }}
	s.mu.Lock()
	if sess.streamSeq != seq || s.sessions[sid] != sess {
		s.mu.Unlock()
		st.Close()
		return
	}
	old := sess.stream
	sess.stream = conn
	s.mu.Unlock()
	if old != nil {
		old.st.Close()
	}
	s.flushHints(r.Context(), sess)
	recheck := time.NewTicker(s.heartbeat())
	defer recheck.Stop()
wait:
	for {
		select {
		case <-r.Context().Done():
			break wait
		case <-st.Done():
			break wait
		case <-s.Stop: // nil (never) unless shutting down
			break wait
		case <-recheck.C:
			if !conn.live() {
				break wait
			}
		}
	}
	st.Close()
	s.mu.Lock()
	if sess.stream == conn {
		sess.stream = nil
		sess.lastUsed = s.now()
	}
	s.mu.Unlock()
}

// credentialLive repeats the authentication of r, a stream's own request: a
// stream carries no call of its own for PROTOCOL §4.5's check on every call,
// so its wakes and heartbeats stand in for them. A token past its expiry or
// revoked, or a rotated bearer, ends the stream without a final result and
// the client reconnects with a live credential. A disabled peer is not an
// end: its stream parks, as subscriptionsListen says. A store failure says
// nothing about the credential, so the next check decides.
func (s *Server) credentialLive(r *http.Request) bool {
	_, err := s.Auth.Authenticate(r.Context(), r)
	switch {
	case err == nil, errors.Is(err, auth.ErrPeerDisabled):
		return true
	case errors.Is(err, auth.ErrUnauthenticated):
		return false
	}
	if r.Context().Err() == nil {
		slog.Warn("mcp: stream credential check", "err", err)
	}
	return true
}

// delete ends a 2025-11-25 session.
func (s *Server) delete(w http.ResponseWriter, r *http.Request, p Principal) {
	if !s.legacyAllowed(p) {
		w.Header().Set("Allow", "POST")
		httpErr(w, http.StatusMethodNotAllowed, "method_not_allowed", "use POST")
		return
	}
	sid := r.Header.Get(hdrSessionID)
	if r.Header.Get(hdrProtocolVersion) == ProtoCurrent || sid == "" {
		w.Header().Set("Allow", "POST")
		httpErr(w, http.StatusMethodNotAllowed, "method_not_allowed", "DELETE exists only for 2025-11-25 sessions")
		return
	}
	sess, ok := s.lookup(sid, p)
	if !ok {
		httpErr(w, http.StatusNotFound, "session_not_found", "session not found")
		return
	}
	s.mu.Lock()
	delete(s.sessions, sess.ID)
	sess.dropResources()
	conn := sess.stream
	sess.stream = nil
	s.mu.Unlock()
	if conn != nil {
		conn.st.Close()
	}
	w.WriteHeader(http.StatusNoContent)
}

// mint creates a 2025 session for p.
func (s *Server) mint(p Principal, proto string) *Session {
	now := s.now()
	sess := &Session{ID: newSessionID(), Proto: proto, Principal: p, created: now, lastUsed: now}
	var closing []*sseConn
	s.mu.Lock()
	s.sweepLocked(now)
	var mine []*Session
	for _, o := range s.sessions {
		if samePrincipal(o.Principal, p) {
			mine = append(mine, o)
		}
	}
	if len(mine) >= maxSessionsPerCaller {
		slices.SortFunc(mine, func(a, b *Session) int { return a.lastUsed.Compare(b.lastUsed) })
		for _, o := range mine[:len(mine)-maxSessionsPerCaller+1] {
			delete(s.sessions, o.ID)
			o.dropResources()
			if o.stream != nil {
				closing = append(closing, o.stream)
				o.stream = nil
			}
		}
	}
	s.sessions[sess.ID] = sess
	s.mu.Unlock()
	for _, c := range closing {
		c.st.Close()
	}
	return sess
}

// lookup finds a live session owned by p and marks it used. A session of
// another credential is reported as missing.
func (s *Server) lookup(id string, p Principal) (*Session, bool) {
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweepLocked(now)
	sess, ok := s.sessions[id]
	if !ok || !samePrincipal(sess.Principal, p) {
		return nil, false
	}
	sess.lastUsed = now
	return sess, true
}

// sweepLocked drops sessions idle past SessionTTL; a session with an open
// GET stream is in use and stays.
func (s *Server) sweepLocked(now time.Time) {
	ttl := s.sessionTTL()
	for id, o := range s.sessions {
		if o.stream == nil && now.Sub(o.lastUsed) > ttl {
			delete(s.sessions, id)
			o.dropResources()
		}
	}
}

func samePrincipal(a, b Principal) bool {
	if (a.OAuth == nil) != (b.OAuth == nil) {
		return false
	}
	ac, bc := a.Caller, b.Caller
	if a.OAuth != nil {
		// OAuth sessions belong to the principal/client and enrollment, so
		// refreshing an access token preserves ownership. Each request still
		// authorizes tool calls with its own current credential (toolsCall).
		ac.OAuth, bc.OAuth = auth.OAuthCredential{}, auth.OAuthCredential{}
	}
	return ac == bc && (a.OAuth == nil || (a.OAuth.PrincipalID == b.OAuth.PrincipalID && a.OAuth.ClientID == b.OAuth.ClientID))
}

// newSessionID is 26 Crockford base32 characters of crypto randomness (the
// ids.New alphabet and length, but unguessable).
func newSessionID() string {
	var b ulid.ULID
	if _, err := rand.Read(b[:]); err != nil {
		panic("mcpserver: crypto/rand failed: " + err.Error())
	}
	return b.String()
}

func metaVersion(req *Request) string {
	for _, k := range metaVersionKeys {
		if v, ok := req.Meta[k].(string); ok {
			return v
		}
	}
	return ""
}

// normOrigin reduces a URL to scheme://host[:port], lower-cased, default
// port dropped; "" when it has no scheme or host.
func normOrigin(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme == "" || u.Host == "" {
		return ""
	}
	scheme := strings.ToLower(u.Scheme)
	host := strings.ToLower(u.Hostname())
	port := u.Port()
	if (scheme == "http" && port == "80") || (scheme == "https" && port == "443") {
		port = ""
	}
	if port != "" {
		return scheme + "://" + net.JoinHostPort(host, port)
	}
	if strings.Contains(host, ":") {
		return scheme + "://[" + host + "]"
	}
	return scheme + "://" + host
}

// accepts reports whether the Accept header lists every one of types (an
// absent Accept accepts anything; */* and type/* wildcards match).
func accepts(r *http.Request, types ...string) bool {
	vals := r.Header.Values("Accept")
	if len(vals) == 0 {
		return true
	}
	var listed []string
	for _, v := range vals {
		for part := range strings.SplitSeq(v, ",") {
			mt, _, _ := strings.Cut(part, ";")
			listed = append(listed, strings.ToLower(strings.TrimSpace(mt)))
		}
	}
	for _, t := range types {
		major, _, _ := strings.Cut(t, "/")
		if !slices.ContainsFunc(listed, func(l string) bool { return l == t || l == "*/*" || l == major+"/*" }) {
			return false
		}
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	if dw, ok := w.(*diagResponse); ok {
		if e, ok := v.(errorResponse); ok {
			dw.diag.rpcError(e.Error)
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeResult(w http.ResponseWriter, id json.RawMessage, res any) {
	if res == nil {
		res = map[string]any{}
	}
	writeJSON(w, http.StatusOK, resultResponse{JSONRPC: "2.0", ID: orNull(id), Result: res})
}

// writeRPCError answers with a JSON-RPC error; the HTTP status is 200.
func writeRPCError(w http.ResponseWriter, id json.RawMessage, e *RPCError) {
	writeRPCStatus(w, http.StatusOK, id, e)
}

func writeRPCStatus(w http.ResponseWriter, status int, id json.RawMessage, e *RPCError) {
	writeJSON(w, status, errorResponse{JSONRPC: "2.0", ID: orNull(id), Error: e})
}

// httpErr writes the hub's §2 error envelope for transport-level failures.
func httpErr(w http.ResponseWriter, status int, code, msg string) {
	writeEnvelope(w, status, code, msg, nil)
}

func writeEnvelope(w http.ResponseWriter, status int, code, msg string, details map[string]any) {
	if details == nil {
		details = map[string]any{}
	}
	writeJSON(w, status, map[string]any{"error": map[string]any{"code": code, "message": msg, "details": details}})
}
