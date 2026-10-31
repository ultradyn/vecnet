package httpapi

import (
	"io"
	"net/http"
)

// postEnrol is POST /v1/enrol {"token"} (PROTOCOL §2.12, §12), with no
// bearer: it exchanges a one-time enrolment token for the peer's long-lived
// secret, returned once, as 200 {"peer_id", "secret"}. An unknown, used or
// expired token is 401 unauthenticated.
func (s *server) postEnrol(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, err)
		return
	}
	top, err := decodeObject(raw, []string{"token"}, []string{"token"})
	if err != nil {
		writeError(w, err)
		return
	}
	tok, err := stringField(top, "token")
	if err != nil {
		writeError(w, err)
		return
	}
	peer, secret, err := s.Hub.Auth.Enrol(r.Context(), tok)
	if err != nil {
		writeError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]string{"peer_id": peer, "secret": secret})
}
