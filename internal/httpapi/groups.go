package httpapi

import (
	"encoding/json"
	"io"
	"net/http"

	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/hub"
)

// decodeMembers parses a {"members": [peer_id…]} body; anything else is
// 400 invalid_body.
func decodeMembers(r *http.Request) ([]string, error) {
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, err
	}
	top, err := decodeObject(raw, []string{"members"}, []string{"members"})
	if err != nil {
		return nil, err
	}
	var members []string
	if isNull(top["members"]) || json.Unmarshal(top["members"], &members) != nil {
		return nil, &envelope.SchemaError{Path: "/members", Message: "members must be an array of strings"}
	}
	return members, nil
}

// postGroups is POST /v1/groups {"members": […]} (PROTOCOL §2.9): 201
// {"group_id"}.
func (s *server) postGroups(w http.ResponseWriter, r *http.Request) {
	members, err := decodeMembers(r)
	if err != nil {
		writeError(w, err)
		return
	}
	g, err := s.Hub.CreateGroup(r.Context(), callerFrom(r.Context()), members)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"group_id": g})
}

// postGroupMembers is POST /v1/groups/{id}/members {"members": […]}
// (PROTOCOL §2.9): 204.
func (s *server) postGroupMembers(w http.ResponseWriter, r *http.Request) {
	members, err := decodeMembers(r)
	if err != nil {
		writeError(w, err)
		return
	}
	if err := s.Hub.AddGroupMembers(r.Context(), callerFrom(r.Context()), r.PathValue("id"), members); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// getGroups is GET /v1/groups (PROTOCOL §2.9): {"groups": [{group_id,
// members, created_by, archived}]}.
func (s *server) getGroups(w http.ResponseWriter, r *http.Request) {
	groups, err := s.Hub.ListGroups(r.Context(), callerFrom(r.Context()))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Groups []hub.GroupInfo `json:"groups"`
	}{groups})
}
