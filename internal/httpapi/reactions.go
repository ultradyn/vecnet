package httpapi

import (
	"io"
	"net/http"
	"regexp"

	"github.com/XertroV/vecnet/internal/envelope"
)

// PROTOCOL §1.3: a wrong prefix or malformed ULID is a body-shape error.
// Keep this aligned with envelope.schema.json $defs.messageId.
var reactionMessageID = regexp.MustCompile(`^msg_[0-9A-HJKMNP-TV-Z]{26}$`)

// Keep this aligned with envelope.schema.json $defs.peerId.
var reactionPeerID = regexp.MustCompile(`^peer_[0-9A-HJKMNP-TV-Z]{26}$`)

func (s *server) postReactions(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, err)
		return
	}
	top, err := decodeObject(raw, []string{"message_id", "emoji", "op", "as", "priority"}, []string{"message_id", "emoji", "op"})
	if err != nil {
		writeError(w, err)
		return
	}
	var req envelope.ReactionRequest
	for _, field := range []struct {
		name  string
		value *string
	}{{"message_id", &req.MessageID}, {"emoji", &req.Emoji}, {"op", &req.Op}, {"as", &req.As}, {"priority", &req.Priority}} {
		*field.value, err = stringField(top, field.name)
		if err != nil {
			writeError(w, err)
			return
		}
	}
	// reactionRequest's maxLength also carries a UTF-8 byte cap.
	if len(req.Emoji) > 32 {
		writeError(w, &envelope.SchemaError{Path: "/emoji", Message: "emoji must be at most 32 UTF-8 bytes"})
		return
	}
	if !reactionMessageID.MatchString(req.MessageID) {
		writeError(w, &envelope.SchemaError{Path: "/message_id", Message: "message_id must match ^msg_[0-9A-HJKMNP-TV-Z]{26}$"})
		return
	}
	// An explicit as, even empty, must be a peer id; only omission means "the caller".
	if _, ok := top["as"]; ok && !reactionPeerID.MatchString(req.As) {
		writeError(w, &envelope.SchemaError{Path: "/as", Message: "as must match ^peer_[0-9A-HJKMNP-TV-Z]{26}$"})
		return
	}
	_, req.PrioritySupplied = top["priority"]
	c := callerFrom(r.Context())
	ingress := "http_send"
	if c.Kind == "operator" && r.Header.Get("X-Vecnet-Ingress") == "operator_ui" {
		ingress = "operator_ui"
	}
	resp, err := s.Hub.React(r.Context(), c, req, ingress)
	if err != nil {
		writeError(w, err)
		return
	}
	s.kick()
	writeJSON(w, http.StatusOK, resp)
}
