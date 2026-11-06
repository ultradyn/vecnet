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

// optBool decodes an optional boolean field; absent is false.
func optBool(top map[string]json.RawMessage, name string) (bool, error) {
	raw, has := top[name]
	if !has {
		return false, nil
	}
	var v bool
	if json.Unmarshal(raw, &v) != nil {
		return false, &envelope.SchemaError{Path: "/" + name, Message: name + " must be a boolean"}
	}
	return v, nil
}

// writeInvite answers a peer create or a re-issue with everything an invite
// needs: the peer, its name and whether the agent picks it, its principal,
// the one-time token and expiry ("" for a dot), the hub's public URL and its
// operator's name.
func (s *server) writeInvite(w http.ResponseWriter, r *http.Request, peer, tok string, exp time.Time) {
	info, err := s.Hub.InviteInfo(r.Context(), peer)
	if err != nil {
		writeError(w, err)
		return
	}
	expires := ""
	if tok != "" {
		expires = envelope.FormatTime(exp)
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusCreated, map[string]any{"peer_id": peer, "name": info.Name, "name_open": info.NameOpen,
		"principal_id": info.PrincipalID, "enrol_token": tok, "enrol_expires_at": expires,
		"hub_url": strings.TrimRight(s.Hub.Cfg.PublicURL, "/"), "hub_owner": info.Owner})
}

// adminReissueEnrol is POST /v1/admin/peers/{id}/enrol-token
// {"enrol_ttl_seconds"?}: 201 {"peer_id", "enrol_token", "enrol_expires_at",
// "hub_url", "name", "name_open", "principal_id"}. It replaces the token of a peer that has not enrolled yet (an
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
	s.writeInvite(w, r, peer, tok, exp)
}

// adminInviteStatus is GET /v1/admin/peers/{id}/enrol-token: 200
// {"peer_id", "state", "expires_at", "revoked_at"}, the invite's state and
// never its token (hub.InviteStatus); 404 for an unknown peer.
func (s *server) adminInviteStatus(w http.ResponseWriter, r *http.Request) {
	st, err := s.Hub.InviteStatus(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, st)
}

// adminRevokeEnrol is DELETE /v1/admin/peers/{id}/enrol-token, no body: 200
// {"peer_id", "state", "expires_at", "revoked_at", "revoked"}. It spends the
// peer's live invite so its token no longer enrols; with none live it is a
// no-op answering "revoked": false. Redeemed credentials are untouched.
// Audited as "enrol_revoke" when it spent a token.
func (s *server) adminRevokeEnrol(w http.ResponseWriter, r *http.Request) {
	if err := readNoBody(r); err != nil {
		writeError(w, err)
		return
	}
	st, revoked, err := s.Hub.RevokeEnrolToken(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{"peer_id": st.PeerID, "state": st.State,
		"expires_at": st.ExpiresAt, "revoked_at": st.RevokedAt, "revoked": revoked})
}
