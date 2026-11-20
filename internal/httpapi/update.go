package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"math"
	"net/http"

	"github.com/XertroV/vecnet/internal/hub"
	"github.com/XertroV/vecnet/internal/selfupdate"
)

// Version check and one-click update (docs/OPERATOR.md "Hub updates"). Both
// routes are under /v1/admin/, so authenticate admits only the operator
// credential. The UI sends that credential as a bearer header, which a
// cross-site page cannot attach; the POST additionally refuses any
// cross-origin browser request (http.CrossOriginProtection: Sec-Fetch-Site,
// else Origin against Host) and takes no body, so no client can name a
// version.

// AuditUpdates returns the hub_update audit hook for a selfupdate.Service:
// the job calls it with the version it confirmed with the registry, which
// may be newer than the cached one the request saw.
func AuditUpdates(h *hub.Hub) func(from, to string) {
	return func(from, to string) {
		if err := h.AuditSelfUpdate(context.Background(), from, to); err != nil {
			slog.Warn("vecnet-hub: audit hub_update", "err", err.Error())
		}
	}
}

// updateCSRF is the cross-origin check for POST /v1/admin/update.
var updateCSRF = http.NewCrossOriginProtection()

// getUpdate is GET /v1/admin/update: the cached check result, whether
// one-click update is offered, and the last job. It never contacts npm.
func (s *server) getUpdate(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if s.Update == nil {
		writeJSON(w, http.StatusOK, selfupdate.Status{})
		return
	}
	writeJSON(w, http.StatusOK, s.Update.Status())
}

// postUpdate is POST /v1/admin/update: start installing the latest version
// the hub itself read from the registry. 202 {job}; 404 update_unavailable
// when one-click update is not offered; 409 no_update or update_running;
// 429 update_cooldown with Retry-After.
func (s *server) postUpdate(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if err := updateCSRF.Check(r); err != nil {
		writeError(w, hub.E(http.StatusForbidden, "forbidden", "cross-origin request refused"))
		return
	}
	if err := readNoBody(r); err != nil {
		writeError(w, err)
		return
	}
	if s.Update == nil {
		writeError(w, hub.E(http.StatusNotFound, "update_unavailable", selfupdate.ErrUnavailable.Error()))
		return
	}
	job, wait, err := s.Update.Start()
	switch {
	case err == nil:
	case errors.Is(err, selfupdate.ErrUnavailable):
		writeError(w, hub.E(http.StatusNotFound, "update_unavailable", err.Error()))
		return
	case errors.Is(err, selfupdate.ErrNoUpdate):
		writeError(w, hub.E(http.StatusConflict, "no_update", err.Error()))
		return
	case errors.Is(err, selfupdate.ErrBusy):
		writeError(w, hub.E(http.StatusConflict, "update_running", err.Error()))
		return
	case errors.Is(err, selfupdate.ErrCooldown):
		e := hub.E(http.StatusTooManyRequests, "update_cooldown", err.Error()+"; retry later")
		e.Details["retry_after_seconds"] = max(1, int(math.Ceil(wait.Seconds())))
		writeError(w, e)
		return
	default:
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"job": job})
}
