package httpapi

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/XertroV/vecnet/internal/auth"
	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/eventlog"
	"github.com/XertroV/vecnet/internal/hub"
)

// errorBody is the §2 error envelope [R01].
type errorBody struct {
	Error errorInner `json:"error"`
}

type errorInner struct {
	Code    string         `json:"code"`
	Message string         `json:"message"`
	Details map[string]any `json:"details"`
}

// writeJSON writes v as JSON with status.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeErr writes the error envelope; nil details become {}.
func writeErr(w http.ResponseWriter, status int, code, msg string, details map[string]any) {
	if details == nil {
		details = map[string]any{}
	}
	if status == http.StatusUnauthorized {
		w.Header().Set("WWW-Authenticate", `Bearer realm="vecnet"`)
	}
	writeJSON(w, status, errorBody{Error: errorInner{Code: code, Message: msg, Details: details}})
}

// writeError maps err onto the wire: *hub.APIError → its status and body,
// plus Retry-After when details carry retry_after_seconds;
// eventlog.ErrCursorPurged → 409 cursor_purged with details.oldest_cursor;
// *envelope.SchemaError → 400 invalid_body with details.path;
// auth.ErrUnauthenticated → 401 unauthenticated; *http.MaxBytesError → 413
// payload_too_large; anything else → 500 internal, logged, with a body that
// says only "internal".
func writeError(w http.ResponseWriter, err error) {
	var apiErr *hub.APIError
	var schemaErr *envelope.SchemaError
	var maxErr *http.MaxBytesError
	var purged eventlog.ErrCursorPurged
	switch {
	case errors.As(err, &apiErr):
		if secs, ok := apiErr.Details["retry_after_seconds"].(int); ok && secs > 0 {
			w.Header().Set("Retry-After", strconv.Itoa(secs)) // 429 rate_limited (PROTOCOL §16.2)
		}
		writeErr(w, apiErr.Status, apiErr.Code, apiErr.Message, apiErr.Details)
	case errors.As(err, &purged):
		writeErr(w, http.StatusConflict, "cursor_purged", "cursor is from another epoch or below retention",
			map[string]any{"oldest_cursor": purged.Oldest.String()})
	case errors.As(err, &schemaErr):
		writeErr(w, http.StatusBadRequest, "invalid_body", schemaErr.Message, map[string]any{"path": schemaErr.Path})
	case errors.Is(err, auth.ErrUnauthenticated):
		writeErr(w, http.StatusUnauthorized, "unauthenticated", "missing, unknown or revoked credential", nil)
	case errors.As(err, &maxErr):
		writeErr(w, http.StatusRequestEntityTooLarge, "payload_too_large", "request body exceeds 256 KiB",
			map[string]any{"limit": maxErr.Limit})
	default:
		// Errors from the hub carry ids and SQL, never secrets or bodies (§12).
		slog.Error("internal error", "err", err)
		writeErr(w, http.StatusInternalServerError, "internal", "internal", nil)
	}
}
