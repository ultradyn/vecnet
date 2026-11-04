package httpapi

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/XertroV/vecnet/internal/envelope"
)

// enrolTTL reads the optional enrol_ttl_seconds of an admin body; absent is
// 0, the hub's default. The hub checks the bounds (PROTOCOL §2.12).
func enrolTTL(top map[string]json.RawMessage) (time.Duration, error) {
	if _, has := top["enrol_ttl_seconds"]; !has {
		return 0, nil
	}
	n, err := intField(top, "enrol_ttl_seconds")
	if err != nil {
		return 0, err
	}
	if n <= 0 {
		n = -1 // 0 is not "default" when asked for explicitly; the hub refuses it
	}
	return time.Duration(n) * time.Second, nil
}

// inviteBody is the shared part of a peer-create and a re-issue answer: the
// peer, its one-time token and expiry ("" for a dot), and the hub's public
// URL, everything an invite needs.
func (s *server) inviteBody(peer, tok string, exp time.Time) map[string]string {
	expires := ""
	if tok != "" {
		expires = envelope.FormatTime(exp)
	}
	return map[string]string{"peer_id": peer, "enrol_token": tok, "enrol_expires_at": expires,
		"hub_url": strings.TrimRight(s.Hub.Cfg.PublicURL, "/")}
}

// adminReissueEnrol is POST /v1/admin/peers/{id}/enrol-token
// {"enrol_ttl_seconds"?}: 201 {"peer_id", "enrol_token", "enrol_expires_at",
// "hub_url"}. It replaces the token of a peer that has not enrolled yet (an
// invite that expired or leaked); an enrolled peer is 409 already_enrolled.
func (s *server) adminReissueEnrol(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, err)
		return
	}
	top := map[string]json.RawMessage{}
	if len(bytes.TrimSpace(raw)) > 0 {
		if top, err = decodeObject(raw, []string{"enrol_ttl_seconds"}, nil); err != nil {
			writeError(w, err)
			return
		}
	}
	ttl, err := enrolTTL(top)
	if err != nil {
		writeError(w, err)
		return
	}
	peer := r.PathValue("id")
	tok, exp, err := s.Hub.ReissueEnrolToken(r.Context(), peer, ttl)
	if err != nil {
		writeError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusCreated, s.inviteBody(peer, tok, exp))
}
