package httpapi

import (
	"bytes"
	"io"
	"mime"
	"net/http"
	"strings"
	"time"

	"github.com/XertroV/vecnet/internal/avatar"
	"github.com/XertroV/vecnet/internal/hub"
)

// Peer avatars (PROTOCOL §8.1).
//
// PUT takes the image as the raw request body, as PUT /v1/blobs does, capped
// at avatar.MaxBytes (2 MiB) instead of the 256 KiB request cap; the hub
// sniffs the bytes and ignores the declared type, except that
// image/svg+xml is refused outright. A body of Content-Type
// application/json is instead {"url": "https://…"}, which the hub fetches.

func (s *server) avatarRoutes(mux *http.ServeMux) {
	mux.HandleFunc("PUT /v1/peers/me/avatar", s.putMyAvatar)
	mux.HandleFunc("DELETE /v1/peers/me/avatar", s.deleteMyAvatar)
	mux.HandleFunc("GET /v1/peers/{id}/avatar", s.getAvatar)
	mux.HandleFunc("PUT /v1/admin/peers/{id}/avatar", s.adminPutAvatar)
	mux.HandleFunc("DELETE /v1/admin/peers/{id}/avatar", s.adminDeleteAvatar)
}

// isAvatarUpload reports the avatar PUTs, whose bodies are capped at
// avatar.MaxBytes rather than MaxBodyBytes.
func isAvatarUpload(r *http.Request) bool {
	if r.Method != http.MethodPut || !strings.HasSuffix(r.URL.Path, "/avatar") {
		return false
	}
	p := r.URL.Path
	if p == "/v1/peers/me/avatar" {
		return true
	}
	id, ok := strings.CutPrefix(strings.TrimSuffix(p, "/avatar"), "/v1/admin/peers/")
	return ok && id != "" && !strings.Contains(id, "/")
}

// avatarBody is a decoded avatar PUT: image bytes, or a URL to fetch.
type avatarBody struct {
	data []byte
	url  string
}

func readAvatarBody(r *http.Request) (avatarBody, error) {
	mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if mt == "image/svg+xml" {
		e := hub.E(http.StatusUnsupportedMediaType, "unsupported_media_type", "an avatar must be a PNG, JPEG, GIF or WebP image")
		e.Details["accepted"] = []string{"image/png", "image/jpeg", "image/gif", "image/webp"}
		return avatarBody{}, e
	}
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		return avatarBody{}, err
	}
	if mt != "application/json" {
		return avatarBody{data: raw}, nil
	}
	top, err := decodeObject(raw, []string{"url"}, []string{"url"})
	if err != nil {
		return avatarBody{}, err
	}
	u, err := stringField(top, "url")
	if err != nil {
		return avatarBody{}, err
	}
	return avatarBody{url: u}, nil
}

// putMyAvatar is PUT /v1/peers/me/avatar[?as=<peer_id>]: 200 {"avatar": sha256}.
func (s *server) putMyAvatar(w http.ResponseWriter, r *http.Request) {
	b, err := readAvatarBody(r)
	if err != nil {
		writeError(w, err)
		return
	}
	c, as := callerFrom(r.Context()), r.URL.Query().Get("as")
	var sha string
	if b.url != "" {
		sha, err = s.Hub.SetAvatarURL(r.Context(), c, as, b.url)
	} else {
		sha, err = s.Hub.SetAvatar(r.Context(), c, as, b.data)
	}
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"avatar": sha})
}

// deleteMyAvatar is DELETE /v1/peers/me/avatar[?as=<peer_id>]: 204.
func (s *server) deleteMyAvatar(w http.ResponseWriter, r *http.Request) {
	if err := s.Hub.ClearAvatar(r.Context(), callerFrom(r.Context()), r.URL.Query().Get("as")); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// adminPutAvatar is PUT /v1/admin/peers/{id}/avatar: 200 {"avatar": sha256}.
func (s *server) adminPutAvatar(w http.ResponseWriter, r *http.Request) {
	b, err := readAvatarBody(r)
	if err != nil {
		writeError(w, err)
		return
	}
	var sha string
	if b.url != "" {
		sha, err = s.Hub.AdminSetAvatarURL(r.Context(), r.PathValue("id"), b.url)
	} else {
		sha, err = s.Hub.AdminSetAvatar(r.Context(), r.PathValue("id"), b.data)
	}
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"avatar": sha})
}

// adminDeleteAvatar is DELETE /v1/admin/peers/{id}/avatar: 204.
func (s *server) adminDeleteAvatar(w http.ResponseWriter, r *http.Request) {
	if err := s.Hub.AdminClearAvatar(r.Context(), r.PathValue("id")); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// getAvatar is GET /v1/peers/{id}/avatar: the PNG, with ETag "<sha256>" and
// Cache-Control private, no-cache, so a client revalidates with
// If-None-Match (304). Bearer only: an avatar never has a URL token.
func (s *server) getAvatar(w http.ResponseWriter, r *http.Request) {
	png, sha, err := s.Hub.Avatar(r.Context(), callerFrom(r.Context()), r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "image/png")
	h.Set("ETag", `"`+sha+`"`)
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", "sandbox; default-src 'none'")
	h.Set("Cache-Control", "private, no-cache")
	http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(png))
}

// avatarLimit is the body cap of an avatar PUT.
const avatarLimit = avatar.MaxBytes
