package httpapi

import (
	"net/http"

	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/hub"
)

// oauthRoutes mounts the OAuth endpoints (PROTOCOL §4.5) and the operator's
// link-code endpoint, which works with or without them.
func (s *server) oauthRoutes(mux *http.ServeMux) {
	if s.OAuth != nil {
		s.OAuth.Mount(mux)
	}
	mux.HandleFunc("POST /v1/admin/dot-links", s.adminDotLink)
}

// adminDotLink is `vecnet peer link <peer>`: a 12-character code, valid for
// DotLinkTTL, shown once. expires_at is taken from before the code is minted,
// so it never overstates the code's life.
func (s *server) adminDotLink(w http.ResponseWriter, r *http.Request) {
	top, err := readObject(r, []string{"peer_id"}, []string{"peer_id"})
	if err != nil {
		writeError(w, err)
		return
	}
	peer, err := stringField(top, "peer_id")
	if err != nil {
		writeError(w, err)
		return
	}
	expires := s.Hub.Now().Add(hub.DotLinkTTL)
	code, err := s.Hub.DotLink(r.Context(), peer)
	if err != nil {
		writeError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusCreated, map[string]string{"code": code, "expires_at": envelope.FormatTime(expires)})
}
