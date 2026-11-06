package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"

	"github.com/XertroV/vecnet/internal/hub"
	"github.com/XertroV/vecnet/internal/prefs"
)

// Preferences stay operator-only at both the HTTP and service boundaries.
// An operator using as-peer mode must return to their operator identity first.
func preferencesCaller(w http.ResponseWriter, r *http.Request) bool {
	if r.URL.Query().Has("as") {
		writeError(w, hub.E(http.StatusForbidden, "forbidden", "Operator credential required."))
		return false
	}
	return true
}
func (s *server) adminGetPreferences(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !preferencesCaller(w, r) {
		return
	}
	snapshot, err := s.Hub.Preferences.ReadPreferences(r.Context(), callerFrom(r.Context()))
	if err != nil {
		writePreferenceError(w, err)
		return
	}
	w.Header().Set("ETag", snapshot.ETag)
	for _, value := range r.Header.Values("If-None-Match") {
		for _, tag := range strings.Split(value, ",") {
			tag = strings.TrimSpace(tag)
			if tag == "*" || strings.TrimPrefix(tag, "W/") == snapshot.ETag {
				w.WriteHeader(http.StatusNotModified)
				return
			}
		}
	}
	s.writePreferencesSnapshot(w, r, snapshot)
}
func readPreferencePatch(w http.ResponseWriter, r *http.Request) (string, prefs.Patch, error) {
	var patch prefs.Patch
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		return "", patch, hub.E(http.StatusUnsupportedMediaType, "unsupported_media_type", "Content-Type must be application/json.")
	}
	tags := r.Header.Values("If-Match")
	if len(tags) == 0 {
		return "", patch, &prefs.Error{Code: prefs.CodePreconditionRequired}
	}
	tag := strings.TrimSpace(r.Header.Get("If-Match"))
	// Exactly one strong RFC entity tag. Its value need not be a current prefs
	// tag: a syntactically valid but unrecognized tag is a revision conflict.
	valid := len(tags) == 1 && len(tag) >= 2 && tag[0] == '"' && tag[len(tag)-1] == '"'
	if valid {
		for _, b := range []byte(tag[1 : len(tag)-1]) {
			if b == '"' || b < 0x21 || b == 0x7f {
				valid = false
				break
			}
		}
	}
	if !valid {
		return "", patch, hub.E(http.StatusBadRequest, "invalid_body", "If-Match must contain one strong entity tag.")
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, prefs.MaxPatchBytes))
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			return "", patch, err
		}
		return "", patch, &prefs.Error{Code: prefs.CodeInvalidBody}
	}
	if err := json.Unmarshal(raw, &patch); err != nil {
		var pe *prefs.Error
		if errors.As(err, &pe) {
			return "", patch, pe
		}
		return "", patch, &prefs.Error{Code: prefs.CodeInvalidBody}
	}
	return tag, patch, nil
}
func (s *server) adminPatchPreferences(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !preferencesCaller(w, r) {
		return
	}
	tag, patch, err := readPreferencePatch(w, r)
	if err != nil {
		writePreferenceError(w, err)
		return
	}
	snapshot, err := s.Hub.Preferences.PatchPreferences(r.Context(), callerFrom(r.Context()), tag, patch)
	if err != nil {
		writePreferenceError(w, err)
		return
	}
	w.Header().Set("ETag", snapshot.ETag)
	s.writePreferencesSnapshot(w, r, snapshot)
}
func (s *server) adminPreviewPreferences(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !preferencesCaller(w, r) {
		return
	}
	tag, patch, err := readPreferencePatch(w, r)
	if err != nil {
		writePreferenceError(w, err)
		return
	}
	preview, err := s.Hub.Preferences.PreviewPreferences(r.Context(), callerFrom(r.Context()), tag, patch)
	if err != nil {
		writePreferenceError(w, err)
		return
	}
	w.Header().Set("ETag", tag)
	writeJSON(w, http.StatusOK, preview)
}

// Translate the closed preference reason vocabulary into the shipped envelope.
// Messages are constants and details contain registry names/constraints only.
func writePreferenceError(w http.ResponseWriter, err error) {
	var pe *prefs.Error
	if !errors.As(err, &pe) {
		writeError(w, err)
		return
	}
	status := http.StatusConflict
	message := "Preference request could not be applied."
	switch pe.Code {
	case prefs.CodeInvalidBody:
		status = http.StatusBadRequest
		message = "Invalid preference request."
	case prefs.CodePayloadTooLarge:
		status = http.StatusRequestEntityTooLarge
		message = "Preference request exceeds 16 KiB."
	case prefs.CodeUnknownKey:
		status = http.StatusUnprocessableEntity
		message = "Unknown preference key."
	case prefs.CodeInvalidValue:
		status = http.StatusUnprocessableEntity
		message = "Invalid preference value."
	case prefs.CodeUnavailable:
		message = "Preference consumer is unavailable."
	case prefs.CodePolicyLocked:
		message = "Preference is locked by deployment policy."
	case prefs.CodeConfirmationRequired:
		message = "Preview and confirmation are required."
	case prefs.CodeConfirmationStale:
		message = "Preview is stale; preview again."
	case prefs.CodePreconditionRequired:
		status = http.StatusPreconditionRequired
		message = "If-Match is required; read preferences first."
	case prefs.CodeRevisionConflict:
		status = http.StatusPreconditionFailed
		message = "Preferences changed; reload and reconcile."
	case prefs.CodeStorageUnavailable:
		status = http.StatusServiceUnavailable
		message = "Preference storage is unavailable."
	default:
		status = http.StatusServiceUnavailable
		message = "Preference storage is unavailable."
	}
	api := hub.E(status, pe.Code, message)
	if pe.Details.Field != "" {
		api.Details["field"] = pe.Details.Field
	}
	if pe.Details.Constraint != "" {
		api.Details["constraint"] = pe.Details.Constraint
	}
	if len(pe.Details.Keys) > 0 {
		api.Details["keys"] = pe.Details.Keys
	}
	writeError(w, api)
}
