// Package httpapi is the hub's HTTP surface (PROTOCOL §2): routing, the
// error envelope, body limits and bearer auth. Handlers translate requests
// into hub.Hub calls; all behaviour lives in internal/hub.
package httpapi

import (
	"context"
	"net/http"
	"time"

	"github.com/XertroV/vecnet/internal/hub"
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
}

// server carries Deps to the route handlers.
type server struct {
	Deps
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
	s := &server{Deps: d}
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
	return limitBody(authenticate(d.Hub.Auth, d.Hub.TouchReachability, mux))
}

// routes registers the §2.1 endpoints implemented so far; later tasks add
// /v1/events, /v1/peers, ... here.
func (s *server) routes(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/messages", s.postMessages)
	mux.HandleFunc("PUT /v1/blobs", s.putBlob)
	mux.HandleFunc("GET /v1/blobs/{sha}", s.getBlob)
	mux.HandleFunc("GET /v1/events", s.getEvents)
	mux.HandleFunc("POST /v1/ack", s.postAck)
	mux.HandleFunc("POST /v1/receipts", s.postReceipts)
	mux.HandleFunc("POST /v1/presence", s.postPresence)
	mux.HandleFunc("GET /v1/peers", s.getPeers)
	mux.HandleFunc("PATCH /v1/peers/me", s.patchPeersMe)
	mux.HandleFunc("GET /v1/threads", s.getThreads)
	mux.HandleFunc("GET /v1/threads/{id}/messages", s.getThreadMessages)
	mux.HandleFunc("POST /v1/groups", s.postGroups)
	mux.HandleFunc("POST /v1/groups/{id}/members", s.postGroupMembers)
	mux.HandleFunc("GET /v1/groups", s.getGroups)
	mux.HandleFunc("POST /v1/enrol", s.postEnrol)
	mux.Handle("/mcp", newMCP(s.Deps)) // every method; mcpserver answers 405 itself
	s.adminRoutes(mux)
}
