package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/hub"
)

// postPresence is POST /v1/presence (PROTOCOL §2.6): body {state, at,
// capabilities?, quota?, location?, note?}; a principal names its peer with
// ?as=. 204.
func (s *server) postPresence(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, err)
		return
	}
	rep, err := decodePresence(raw)
	if err != nil {
		writeError(w, err)
		return
	}
	if err := s.Hub.SetPresence(r.Context(), callerFrom(r.Context()), r.URL.Query().Get("as"), rep); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// decodePresence parses the presence body; type errors, unknown fields and a
// malformed `at` are 400 invalid_body. Value ranges are checked by the hub
// against $defs/presence.
func decodePresence(raw []byte) (hub.PresenceReport, error) {
	var rep hub.PresenceReport
	top, err := decodeObject(raw, []string{"state", "at", "capabilities", "quota", "location", "note"}, []string{"state", "at"})
	if err != nil {
		return rep, err
	}
	if rep.State, err = stringField(top, "state"); err != nil {
		return rep, err
	}
	at, err := stringField(top, "at")
	if err != nil {
		return rep, err
	}
	if rep.At, err = envelope.ParseTime(at); err != nil {
		return rep, &envelope.SchemaError{Path: "/at", Message: "at must be YYYY-MM-DDTHH:MM:SS.sssZ"}
	}
	if rep.Location, err = stringField(top, "location"); err != nil {
		return rep, err
	}
	_, hasLoc := top["location"]
	rep.ClearLocation = hasLoc && rep.Location == ""
	if rep.Note, err = stringField(top, "note"); err != nil {
		return rep, err
	}
	if c, has := top["capabilities"]; has {
		if isNull(c) || json.Unmarshal(c, &rep.Capabilities) != nil {
			return rep, &envelope.SchemaError{Path: "/capabilities", Message: "capabilities must be an array of strings"}
		}
		if rep.Capabilities == nil {
			rep.Capabilities = []string{}
		}
	}
	if q, has := top["quota"]; has {
		dec := json.NewDecoder(bytes.NewReader(q))
		dec.UseNumber()
		if isNull(q) || dec.Decode(&rep.Quota) != nil {
			return rep, &envelope.SchemaError{Path: "/quota", Message: "quota must be an object"}
		}
		if rep.Quota == nil {
			rep.Quota = map[string]any{}
		}
	}
	return rep, nil
}

func isNull(raw json.RawMessage) bool {
	return bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}

// peersBody is the GET /v1/peers response.
type peersBody struct {
	Peers []hub.DirectoryEntry `json:"peers"`
}

// getPeers is GET /v1/peers (PROTOCOL §2.10): every peer's public directory
// fields, to any authenticated caller.
func (s *server) getPeers(w http.ResponseWriter, r *http.Request) {
	dir, err := s.Hub.Directory(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, peersBody{Peers: dir})
}

// getPeersMe is GET /v1/peers/me (PROTOCOL §2.10): the authenticated
// caller, so a client can bind local state to the identity its credential
// proves rather than to its config text. Only kind and the credential's own
// id: peer_id for a peer, principal_id for a principal, neither for an
// operator.
func (s *server) getPeersMe(w http.ResponseWriter, r *http.Request) {
	c := callerFrom(r.Context())
	me := map[string]string{"kind": c.Kind}
	switch c.Kind {
	case "peer":
		me["peer_id"] = c.PeerID
	case "principal":
		me["principal_id"] = c.PrincipalID
	}
	// Verify a single automatic-delivery recipient without reading its event
	// stream. The unfiltered identity response remains backwards compatible.
	if peer := r.URL.Query().Get("peer"); peer != "" {
		owners, _, _, err := s.Hub.StreamOwners(r.Context(), c, peer)
		if err != nil {
			if errors.Is(err, hub.ErrPeerDisabled) {
				err = hub.E(http.StatusServiceUnavailable, "peer_disabled", "recipient is disabled")
			}
			writeError(w, err)
			return
		}
		if len(owners) != 1 {
			writeError(w, hub.E(http.StatusForbidden, "not_your_peer", "a single owned recipient is required"))
			return
		}
		me["recipient_id"] = owners[0]
	}
	writeJSON(w, http.StatusOK, me)
}

// patchPeersMe is PATCH /v1/peers/me (PROTOCOL §2.10): body
// {"subscribe_presence": "all"|"teams"|"none"}; 204.
func (s *server) patchPeersMe(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, err)
		return
	}
	top, err := decodeObject(raw, []string{"subscribe_presence"}, []string{"subscribe_presence"})
	if err != nil {
		writeError(w, err)
		return
	}
	v, err := stringField(top, "subscribe_presence")
	if err != nil {
		writeError(w, err)
		return
	}
	if err := s.Hub.SetSubscribePresence(r.Context(), callerFrom(r.Context()), v); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// streamReach marks every owner of an SSE stream connected until the
// returned func runs (PROTOCOL §8).
func (s *server) streamReach(owners []string) func() {
	ctx := context.Background() // the closing write must outlive the request
	for _, o := range owners {
		s.Hub.StreamOpened(ctx, o)
	}
	return func() {
		for _, o := range owners {
			s.Hub.StreamClosed(ctx, o)
		}
	}
}
