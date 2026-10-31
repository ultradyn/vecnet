// Package httpapi is the hub's HTTP surface (PROTOCOL §2): routing, the
// error envelope, body limits and bearer auth. Handlers translate requests
// into hub.Hub calls; all behaviour lives in internal/hub.
package httpapi

import (
	"net/http"

	"github.com/XertroV/vecnet/internal/hub"
)

// Deps are the handler's collaborators.
type Deps struct {
	Hub  *hub.Hub
	Kick func() // push worker wakeup after a commit; may be nil
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
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, hub.E(http.StatusNotFound, "not_found", "no such endpoint"))
	})
	return limitBody(authenticate(d.Hub.Auth, mux))
}

// routes registers the §2.1 endpoints implemented so far (none yet: later
// tasks add /v1/messages, /v1/events, /v1/peers, ... here).
func (s *server) routes(mux *http.ServeMux) {}
