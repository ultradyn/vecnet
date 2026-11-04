package httpapi

import (
	"net/http"

	"github.com/XertroV/vecnet/internal/hub"
	"github.com/XertroV/vecnet/internal/hubid"
)

// getHubIdentity is public; challenge proofs cost one §20.2 token.
func (s *server) getHubIdentity(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	query := r.URL.Query()
	challenge := query.Get("challenge")
	// Presence matters: ?challenge= requests a proof with an invalid challenge,
	// while no parameter requests metadata only.
	if query.Has("challenge") && !hubid.ValidChallenge(challenge) {
		e := hub.E(http.StatusUnprocessableEntity, "invalid_value", "challenge must be unpadded base64url encoding of 16–64 bytes")
		e.Details["field"] = "challenge"
		writeError(w, e)
		return
	}
	doc, err := s.Hub.IdentityDoc(challenge)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, doc)
}
