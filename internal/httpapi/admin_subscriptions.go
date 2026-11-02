package httpapi

import (
	"context"
	"errors"
	"net/http"

	"github.com/XertroV/vecnet/internal/events"
	"github.com/XertroV/vecnet/internal/hub"
)

// adminListSubscriptions is GET /v1/admin/subscriptions: subscription health
// for the operator (PROTOCOL §13.4).
func (s *server) adminListSubscriptions(w http.ResponseWriter, r *http.Request) {
	list, err := s.op.Subscriptions(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

// adminPauseSubscription is POST /v1/admin/subscriptions/{id}/pause: 204; the
// operator's stop for one subscription, which a re-subscribe does not lift. An
// unknown id is 404 not_found.
func (s *server) adminPauseSubscription(w http.ResponseWriter, r *http.Request) {
	s.subscriptionAction(w, r, events.NewStore(s.Hub).Pause)
}

// adminResumeSubscription is POST /v1/admin/subscriptions/{id}/resume: 204;
// paused_reason is cleared. An unknown id is 404 not_found.
func (s *server) adminResumeSubscription(w http.ResponseWriter, r *http.Request) {
	s.subscriptionAction(w, r, events.NewStore(s.Hub).Resume)
}

func (s *server) subscriptionAction(w http.ResponseWriter, r *http.Request, do func(ctx context.Context, id string) error) {
	if err := readNoBody(r); err != nil {
		writeError(w, err)
		return
	}
	err := do(r.Context(), r.PathValue("id"))
	if errors.Is(err, events.ErrNotFound) {
		writeError(w, hub.E(http.StatusNotFound, "not_found", "no such subscription"))
		return
	}
	if err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
