package httpapi

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"

	"github.com/XertroV/vecnet/internal/envelope"
)

// decodeObject parses raw as a JSON object whose keys are all in allowed and
// include every name in required; anything else is a 400 invalid_body
// SchemaError naming the first violation.
func decodeObject(raw []byte, allowed, required []string) (map[string]json.RawMessage, error) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil || top == nil {
		return nil, &envelope.SchemaError{Path: "", Message: "body must be a JSON object"}
	}
	ok := map[string]bool{}
	for _, k := range allowed {
		ok[k] = true
	}
	for k := range top {
		if !ok[k] {
			return nil, &envelope.SchemaError{Path: "/" + k, Message: "unknown field"}
		}
	}
	for _, k := range required {
		if _, has := top[k]; !has {
			return nil, &envelope.SchemaError{Path: "/" + k, Message: k + " is required"}
		}
	}
	return top, nil
}

// stringField decodes top[name] as a JSON string ("" when absent); null or
// another type is a 400 invalid_body.
func stringField(top map[string]json.RawMessage, name string) (string, error) {
	raw, has := top[name]
	if !has {
		return "", nil
	}
	var s string
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) || json.Unmarshal(raw, &s) != nil {
		return "", &envelope.SchemaError{Path: "/" + name, Message: name + " must be a string"}
	}
	return s, nil
}

// postReceipts is POST /v1/receipts (PROTOCOL §2.5): body {delivery_id,
// state, note?}; 204.
func (s *server) postReceipts(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, err)
		return
	}
	top, err := decodeObject(raw, []string{"delivery_id", "state", "note"}, []string{"delivery_id", "state"})
	if err != nil {
		writeError(w, err)
		return
	}
	var f [3]string
	for i, name := range []string{"delivery_id", "state", "note"} {
		if f[i], err = stringField(top, name); err != nil {
			writeError(w, err)
			return
		}
	}
	if err := s.Hub.Receipt(r.Context(), callerFrom(r.Context()), f[0], f[1], f[2]); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
