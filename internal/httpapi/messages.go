package httpapi

import (
	"io"
	"net/http"

	"github.com/XertroV/vecnet/internal/envelope"
)

// postMessages is POST /v1/messages (PROTOCOL §2.2): 202 with the
// SendResponse, or 200 with the original response on an idempotent replay.
// The operator's own UI marks its sends with X-Vecnet-Ingress: operator_ui;
// the header means nothing on any other credential. Likewise a bare
// `vecnet` run by a human names its terminal in X-Vecnet-Client, which the
// hub records as metadata["vecnet.client"] for the operator only (§2.2
// step 11); a malformed value is ignored rather than refused.
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
	var client *envelope.OperatorClient
	if c.Kind == "operator" {
		if oc, ok := envelope.ParseOperatorClient(r.Header.Get(envelope.ClientHeader)); ok {
			client = &oc
		}
	}
	resp, replayed, err := s.Hub.SendVia(r.Context(), c, raw, ingress, client)
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
