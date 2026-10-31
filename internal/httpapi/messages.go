package httpapi

import (
	"io"
	"net/http"
)

// postMessages is POST /v1/messages (PROTOCOL §2.2): 202 with the
// SendResponse, or 200 with the original response on an idempotent replay.
// The operator's own UI marks its sends with X-Vecnet-Ingress: operator_ui;
// the header means nothing on any other credential.
func (s *server) postMessages(w http.ResponseWriter, r *http.Request) {
	c := callerFrom(r.Context())
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, err)
		return
	}
	ingress := "http_send"
	if c.Kind == "operator" && r.Header.Get("X-Vecnet-Ingress") == "operator_ui" {
		ingress = "operator_ui"
	}
	resp, replayed, err := s.Hub.Send(r.Context(), c, raw, ingress)
	if err != nil {
		writeError(w, err)
		return
	}
	if replayed {
		writeJSON(w, http.StatusOK, resp)
		return
	}
	s.kick()
	writeJSON(w, http.StatusAccepted, resp)
}
