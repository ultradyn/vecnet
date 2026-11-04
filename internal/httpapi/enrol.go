package httpapi

import (
	"errors"
	"io"
	"net/http"

	"github.com/XertroV/vecnet/internal/auth"
	"github.com/XertroV/vecnet/internal/hub"
)

// postEnrol is POST /v1/enrol {"token", "name"?} (PROTOCOL §2.12, §12), with
// no bearer: it exchanges a one-time enrolment token for the peer's
// long-lived secret, returned once, as 200 {"peer_id", "secret", "name"}. An
// invite that left the name open takes it here, once (Hub.Enrol). An unknown or used
// token is 401 unauthenticated; an unused one past its expiry is 410
// enrol_token_expired (ask the operator for a new invite); a dot's is 422
// invalid_value, since a dot never holds a vecnet secret.
func (s *server) postEnrol(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, err)
		return
	}
	top, err := decodeObject(raw, []string{"token", "name"}, []string{"token"})
	if err != nil {
		writeError(w, err)
		return
	}
	tok, err := stringField(top, "token")
	if err != nil {
		writeError(w, err)
		return
	}
	name, err := stringField(top, "name")
	if err != nil {
		writeError(w, err)
		return
	}
	peer, secret, final, err := s.Hub.Enrol(r.Context(), tok, name)
	if errors.Is(err, auth.ErrDotBearer) {
		e := hub.E(http.StatusUnprocessableEntity, "invalid_value", "a dot holds no vecnet secret; run `vecnet peer link` for it")
		e.Details["field"] = "token"
		writeError(w, e)
		return
	}
	if errors.Is(err, auth.ErrEnrolTokenExpired) {
		writeError(w, hub.E(http.StatusGone, "enrol_token_expired", "the enrolment token has expired; ask the operator for a new invite"))
		return
	}
	if err != nil {
		writeError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]string{"peer_id": peer, "secret": secret, "name": final})
}
