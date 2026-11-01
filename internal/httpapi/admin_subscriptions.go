package httpapi

import (
	"errors"
	"net/http"
	"net/url"

	"github.com/XertroV/vecnet/internal/events"
	"github.com/XertroV/vecnet/internal/hub"
)

// subscriptionRow is one entry of GET /v1/admin/subscriptions. A callback
// path can carry a credential, so url is the host alone; no secret appears.
type subscriptionRow struct {
	SubID          string  `json:"sub_id"`
	Principal      string  `json:"principal"`
	PeerID         string  `json:"peer_id"`
	URL            string  `json:"url"`
	Name           string  `json:"name"`
	Cursor         string  `json:"cursor"`
	ExpiresAt      *string `json:"expires_at"`
	VerifiedAt     *string `json:"verified_at"`
	PausedReason   *string `json:"paused_reason"`
	LastDeliveryAt *string `json:"last_delivery_at"`
	LastStatus     *int    `json:"last_status"`
	Attempts       int     `json:"attempts"`
}

// adminListSubscriptions is GET /v1/admin/subscriptions: subscription health
// for the operator (PROTOCOL §13.4).
func (s *server) adminListSubscriptions(w http.ResponseWriter, r *http.Request) {
	list, err := events.NewStore(s.Hub).ListHealth(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	rows := make([]subscriptionRow, len(list))
	for i, h := range list {
		host := ""
		if u, err := url.Parse(h.URL); err == nil {
			host = u.Hostname()
		}
		rows[i] = subscriptionRow{
			SubID: h.ID, Principal: h.PrincipalKind + ":" + h.PrincipalID, PeerID: h.PeerID,
			URL: host, Name: h.Name, Cursor: h.Cursor.String(),
			ExpiresAt: h.ExpiresAt, VerifiedAt: h.VerifiedAt, PausedReason: h.PausedReason,
			LastDeliveryAt: h.LastDeliveryAt, LastStatus: h.LastStatus, Attempts: h.Attempts,
		}
	}
	writeJSON(w, http.StatusOK, rows)
}

// adminResumeSubscription is POST /v1/admin/subscriptions/{id}/resume: 204;
// paused_reason is cleared. An unknown id is 404 not_found.
func (s *server) adminResumeSubscription(w http.ResponseWriter, r *http.Request) {
	if err := readNoBody(r); err != nil {
		writeError(w, err)
		return
	}
	err := events.NewStore(s.Hub).Resume(r.Context(), r.PathValue("id"))
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
