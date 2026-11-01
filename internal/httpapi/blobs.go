package httpapi

import (
	"net/http"
	"strconv"
	"time"

	"github.com/XertroV/vecnet/internal/auth"
	"github.com/XertroV/vecnet/internal/hub"
)

// maxTTLSeconds bounds ?ttl= so the duration cannot overflow. A TTL only
// ever shortens an unreferenced blob's 24 h grace (PROTOCOL §11), so the
// bound changes nothing a client can observe.
const maxTTLSeconds = 1 << 31

// isBlobUpload reports PUT /v1/blobs, whose body is capped by the blob store
// (25 MiB, §2.7) instead of the 256 KiB request cap.
func isBlobUpload(r *http.Request) bool {
	return r.Method == http.MethodPut && r.URL.Path == "/v1/blobs"
}

// putBlob is PUT /v1/blobs?ttl=<seconds> (PROTOCOL §2.7): the body is
// streamed into the store; Content-Type is the media type. 201
// {"sha256","size"}.
func (s *server) putBlob(w http.ResponseWriter, r *http.Request) {
	var ttl time.Duration
	if r.URL.Query().Has("ttl") {
		secs, err := strconv.ParseInt(r.URL.Query().Get("ttl"), 10, 64)
		if err != nil || secs < 0 || secs > maxTTLSeconds {
			e := hub.E(http.StatusUnprocessableEntity, "invalid_value", "ttl must be a non-negative whole number of seconds")
			e.Details["field"] = "ttl"
			writeError(w, e)
			return
		}
		ttl = time.Duration(secs) * time.Second
	}
	if r.ContentLength > s.Hub.Blobs.MaxSize {
		e := hub.E(http.StatusRequestEntityTooLarge, "blob_too_large", "blob exceeds the per-blob cap")
		e.Details["limit"] = s.Hub.Blobs.MaxSize
		writeError(w, e)
		return
	}
	m, err := s.Hub.PutBlob(r.Context(), callerFrom(r.Context()), r.Body, r.Header.Get("Content-Type"), ttl)
	if err != nil {
		if r.Context().Err() != nil {
			return // the client went away; the store already removed the temp file
		}
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"sha256": m.SHA256, "size": m.Size})
}

// getBlob is GET /v1/blobs/{sha} (PROTOCOL §2.7, §11). The auth middleware
// skips paths carrying ?t=, so a bearer sent alongside a token is resolved
// here; a bad token then falls back to that bearer.
func (s *server) getBlob(w http.ResponseWriter, r *http.Request) {
	var caller *auth.Caller
	if isPublic(r) {
		if r.Header.Get("Authorization") != "" {
			c, err := s.Hub.Auth.Resolve(r.Context(), bearer(r))
			if err != nil {
				writeError(w, err)
				return
			}
			caller = &c
		}
	} else {
		c := callerFrom(r.Context())
		caller = &c
	}
	f, m, err := s.Hub.ReadBlob(r.Context(), caller, r.URL.Query().Get("t"), r.PathValue("sha"))
	if err != nil {
		writeError(w, err)
		return
	}
	defer f.Close()
	h := w.Header()
	h.Set("Content-Type", m.MediaType)
	h.Set("ETag", `"`+m.SHA256+`"`)
	// Uploaded bytes are untrusted (§14.2) and share the operator UI's
	// origin: never sniff them and never let them run as a document.
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", "sandbox; default-src 'none'")
	h.Set("Cache-Control", "private")
	http.ServeContent(w, r, "", time.Time{}, f)
}
