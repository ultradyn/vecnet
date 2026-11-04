package httpapi

import (
	"net/http"

	"github.com/XertroV/vecnet/internal/hub"
)

func (s *server) adminPostNotice(w http.ResponseWriter, r *http.Request) {
	top, err := readObject(r, []string{"kind", "message", "at", "expires_at", "new_url"}, []string{"kind", "message"})
	if err != nil {
		writeError(w, err)
		return
	}
	var fields [5]string
	for i, name := range []string{"kind", "message", "at", "expires_at", "new_url"} {
		fields[i], err = stringField(top, name)
		if err != nil {
			writeError(w, err)
			return
		}
	}
	n, err := s.Hub.PostNotice(r.Context(), callerFrom(r.Context()), hub.NoticeRequest{Kind: fields[0], Message: fields[1], At: fields[2], ExpiresAt: fields[3], NewURL: fields[4]})
	if err != nil {
		writeError(w, err)
		return
	}
	s.kick()
	writeJSON(w, http.StatusCreated, map[string]any{"notice": n})
}

func (s *server) adminClearNotice(w http.ResponseWriter, r *http.Request) {
	if err := readNoBody(r); err != nil {
		writeError(w, err)
		return
	}
	if err := s.Hub.ClearNotice(r.Context(), callerFrom(r.Context()), r.PathValue("id")); err != nil {
		writeError(w, err)
		return
	}
	s.kick()
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) getHubNotices(w http.ResponseWriter, r *http.Request) {
	notices, err := s.Hub.ActiveNotices(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"notices": notices})
}
