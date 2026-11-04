// Package httpapi is the hub's HTTP surface (PROTOCOL §2): routing, the
// error envelope, body limits and bearer auth. Handlers translate requests
// into hub.Hub calls; all behaviour lives in internal/hub.
package httpapi

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/XertroV/vecnet/internal/hub"
	"github.com/XertroV/vecnet/internal/oauth"
	"github.com/XertroV/vecnet/internal/opview"
	"github.com/XertroV/vecnet/internal/ui"
)

// Deps are the handler's collaborators.
type Deps struct {
	Hub  *hub.Hub
	Kick func() // push worker wakeup after a commit; may be nil
	// VerifyRoute sends a push route its verification challenge
	// (push.Worker.Verify, PROTOCOL §3.2); nil makes
	// POST /v1/admin/routes/{id}/verify a 500.
	VerifyRoute func(ctx context.Context, routeID string) error
	// OAuth, when non-nil, serves the OAuth endpoints and accepts its access
	// tokens on /mcp (PROTOCOL §4.5).
	OAuth *oauth.Server

	// GET /v1/events timing; zero means the package default. Tests shorten
	// them instead of sleeping.
	Heartbeat   time.Duration // SSE ": hb" interval (DefaultHeartbeat)
	DefaultWait time.Duration // long-poll hold without ?wait= (DefaultWait)
	MaxWait     time.Duration // long-poll hold cap (MaxWait)
	WaitUnit    time.Duration // length of one ?wait= second (time.Second)
	SSEStall    time.Duration // SSE queue-full wait before dropping a stuck client (DefaultSSEStall)

	// Stop, when non-nil, is closed when the server begins shutting down:
	// open SSE streams end and parked long-polls answer at once (empty), so
	// http.Server.Shutdown is not held for its whole grace period by
	// long-lived requests.
	Stop <-chan struct{}

	// UIDir, when set, serves the operator UI under /ui/ from this directory
	// on disk instead of the embedded copy (vecnet-hub --dev-ui-dir), so an
	// edit shows on refresh without a rebuild.
	UIDir string

	// Limiter throttles unauthenticated traffic per client (PROTOCOL
	// §20.2); nil limits nothing. Stack.Deps sets DefaultLimiter.
	Limiter *Limiter
	// TrustFlyClientIP keys Limiter on the Fly-Client-IP header, which only
	// Fly's proxy can set (vecnet-hub --trust-fly-client-ip). Off, a direct
	// client could name any address there, so RemoteAddr is used.
	TrustFlyClientIP bool
}

// Server timeouts (PROTOCOL §20.3). ReadTimeout bounds reading a whole
// request, WriteTimeout writing an ordinary response; streams (SSE, long
// polls, blob transfers) lift or extend both for themselves with an
// http.ResponseController, so they are not cut. ConfigureServer applies them.
const (
	ReadHeaderTimeout = 10 * time.Second
	ReadTimeout       = 30 * time.Second
	WriteTimeout      = 60 * time.Second
	IdleTimeout       = 120 * time.Second
	// blobTransferTimeout is a blob upload's read or download's write budget:
	// 25 MiB at about 350 kbit/s.
	blobTransferTimeout = 10 * time.Minute
)

// ConfigureServer sets srv's timeouts to the package values.
func ConfigureServer(srv *http.Server) {
	srv.ReadHeaderTimeout = ReadHeaderTimeout
	srv.ReadTimeout = ReadTimeout
	srv.WriteTimeout = WriteTimeout
	srv.IdleTimeout = IdleTimeout
}

// server carries Deps to the route handlers.
type server struct {
	Deps
	op *opview.Reader
}

// kick wakes the push worker when one is wired.
func (s *server) kick() {
	if s.Kick != nil {
		s.Kick()
	}
}

// New returns the hub's HTTP handler. The routes of §2.1 are registered task
// by task; the body limit and auth middleware wrap the whole mux, so they
// apply to routes that do not exist yet.
func New(d Deps) http.Handler {
	s := &server{Deps: d, op: opview.New(d.Hub)}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	})
	s.routes(mux)
	uiHandler := ui.Handler(d.UIDir) // operator inbox; public (isPublic)
	mux.Handle("GET /ui", uiHandler)
	mux.Handle("GET /ui/", uiHandler)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, hub.E(http.StatusNotFound, "not_found", "no such endpoint"))
	})
	h := throttle(d.Limiter, d.TrustFlyClientIP, limitBody(authenticate(d.Hub.Auth, d.Hub.TouchReachability, d.Limiter, d.TrustFlyClientIP, mux)))
	if strings.HasPrefix(d.Hub.Cfg.PublicURL, "https://") {
		h = hsts(h)
	}
	return h
}

// hsts adds Strict-Transport-Security to every response of a hub served over
// https (PROTOCOL §20.3), so a browser never again tries it in the clear.
func hsts(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Strict-Transport-Security", "max-age=31536000")
		next.ServeHTTP(w, r)
	})
}

// routes registers the §2.1 endpoints implemented so far; later tasks add
// /v1/events, /v1/peers, ... here.
func (s *server) routes(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/messages", s.postMessages)
	mux.HandleFunc("POST /v1/reactions", s.postReactions)
	mux.HandleFunc("PUT /v1/blobs", s.putBlob)
	mux.HandleFunc("GET /v1/blobs/{sha}", s.getBlob)
	mux.HandleFunc("GET /v1/events", s.getEvents)
	mux.HandleFunc("POST /v1/ack", s.postAck)
	mux.HandleFunc("POST /v1/receipts", s.postReceipts)
	mux.HandleFunc("POST /v1/presence", s.postPresence)
	mux.HandleFunc("GET /v1/peers", s.getPeers)
	mux.HandleFunc("GET /v1/peers/me", s.getPeersMe)
	mux.HandleFunc("POST /v1/peers", s.postPeers)
	mux.HandleFunc("PATCH /v1/peers/{id}", s.patchPeer)
	mux.HandleFunc("PATCH /v1/peers/me", s.patchPeersMe)
	mux.HandleFunc("GET /v1/threads", s.getThreads)
	mux.HandleFunc("GET /v1/threads/{id}/messages", s.getThreadMessages)
	mux.HandleFunc("POST /v1/groups", s.postGroups)
	mux.HandleFunc("POST /v1/groups/{id}/members", s.postGroupMembers)
	mux.HandleFunc("GET /v1/groups", s.getGroups)
	mux.HandleFunc("POST /v1/enrol", s.postEnrol)
	mux.HandleFunc("POST /v1/ingress/{src_id}", s.postIngress)
	mux.Handle("/mcp", newMCP(s.Deps)) // every method; mcpserver answers 405 itself
	s.oauthRoutes(mux)
	s.adminRoutes(mux)
}
