package httpapi

import (
	"encoding/json"
	"net/http"

	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/hub"
)

// postPeers provisions a peer with the authenticated principal's policy (§18.4).
func (s *server) postPeers(w http.ResponseWriter, r *http.Request) {
	top, err := readObject(r, []string{"kind", "name", "idempotency_key"}, []string{"kind", "name", "idempotency_key"})
	if err != nil {
		writeError(w, err)
		return
	}
	var req hub.CreatePeerRequest
	for _, field := range []struct {
		name string
		dest *string
	}{{"kind", &req.Kind}, {"name", &req.Name}, {"idempotency_key", &req.IdempotencyKey}} {
		if *field.dest, err = stringField(top, field.name); err != nil {
			writeError(w, err)
			return
		}
	}
	result, err := s.Hub.CreatePeer(r.Context(), callerFrom(r.Context()), req)
	if err != nil {
		writeError(w, err)
		return
	}
	status := http.StatusCreated
	if result.Replayed {
		status = http.StatusOK
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, status, result)
}

// patchPeer changes only a peer the caller created and still owns (§18.4).
func (s *server) patchPeer(w http.ResponseWriter, r *http.Request) {
	top, err := readObject(r, []string{"disabled"}, []string{"disabled"})
	if err != nil {
		writeError(w, err)
		return
	}
	var disabled bool
	if isNull(top["disabled"]) || json.Unmarshal(top["disabled"], &disabled) != nil {
		writeError(w, &envelope.SchemaError{Path: "/disabled", Message: "disabled must be a boolean"})
		return
	}
	peer, err := s.Hub.SetOwnPeerDisabled(r.Context(), callerFrom(r.Context()), r.PathValue("id"), disabled)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"peer": peer})
}
