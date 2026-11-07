package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/XertroV/vecnet/internal/auth"
	"github.com/XertroV/vecnet/internal/blobwire"
	"github.com/XertroV/vecnet/internal/envelope"
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
	if s.Hub.Cfg.BlobBackend == "r2" {
		writeError(w, hub.E(409, "direct_upload_required", "use POST /v1/blobs/uploads"))
		return
	}
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
	// A 25 MiB body may take longer than the server's ReadTimeout (§20.3).
	rc := http.NewResponseController(w)
	_ = rc.SetReadDeadline(time.Now().Add(blobTransferTimeout))
	_ = rc.SetWriteDeadline(time.Now().Add(blobTransferTimeout + WriteTimeout))
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
	id := r.URL.Query().Get("b")
	if id != "" {
		if !s.blobOriginAllowed(w, r) {
			return
		}
		grant, err := s.Hub.BlobDownload(r.Context(), caller, r.URL.Query().Get("t"), r.PathValue("sha"), id)
		if err != nil {
			writeError(w, err)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		// Set Location directly: http.Redirect writes the capability into HTML.
		w.Header().Set("Location", grant.URL)
		w.WriteHeader(http.StatusTemporaryRedirect)
		return
	}
	f, m, err := s.Hub.ReadBlob(r.Context(), caller, r.URL.Query().Get("t"), r.PathValue("sha"))
	if err != nil {
		writeError(w, s.Hub.BlobReadError(r.Context(), caller, err))
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
	// A 25 MiB download may take longer than the server's WriteTimeout (§20.3).
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(blobTransferTimeout))
	http.ServeContent(w, r, "", time.Time{}, f)
}

func (s *server) createBlobUpload(w http.ResponseWriter, r *http.Request) {
	if !s.blobOriginAllowed(w, r) {
		return
	}
	top, err := decodeBlobJSON(r, uploadRequestFields)
	if err != nil {
		writeError(w, err)
		return
	}
	// Shapes were checked exactly above; these decodes cannot fail.
	var in blobwire.UploadRequest
	_ = json.Unmarshal(top["size"], &in.Size)
	_ = json.Unmarshal(top["sha256"], &in.SHA256)
	_ = json.Unmarshal(top["media_type"], &in.MediaType)
	grant, err := s.Hub.CreateBlobUpload(r.Context(), callerFrom(r.Context()), r.Header.Get("Idempotency-Key"), in)
	if err != nil {
		writeError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 201, grant)
}

// blobField is one exact, required property of a blob JSON request body.
type blobField struct {
	name    string
	integer bool // otherwise a string
}

// uploadRequestFields is the exact POST /v1/blobs/uploads object.
var uploadRequestFields = []blobField{{"size", true}, {"sha256", false}, {"media_type", false}}

// decodeBlobJSON reads exactly one JSON object whose property names are
// exactly fields (case-sensitive), each present with its JSON type. A shape
// violation is 400 invalid_body with details.path (PROTOCOL §15): unknown
// properties first (with a fixed path token), then missing or mistyped fields
// in order.
// Semantic value checks (ranges, formats) stay with the hub (422).
func decodeBlobJSON(r *http.Request, fields []blobField) (map[string]json.RawMessage, error) {
	d := json.NewDecoder(r.Body)
	var raw json.RawMessage
	err := d.Decode(&raw)
	if err == nil {
		err = d.Decode(new(any))
		if err == io.EOF {
			err = nil
		} else if err == nil {
			err = errors.New("trailing data")
		}
	}
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			return nil, err
		}
		return nil, &envelope.SchemaError{Path: "", Message: "body must be one JSON object"}
	}
	var top map[string]json.RawMessage
	if json.Unmarshal(raw, &top) != nil || top == nil {
		return nil, &envelope.SchemaError{Path: "", Message: "body must be one JSON object"}
	}
	known := make(map[string]bool, len(fields))
	for _, f := range fields {
		known[f.name] = true
	}
	for k := range top {
		if !known[k] {
			return nil, &envelope.SchemaError{Path: "/" + envelope.UnknownProperty, Message: "unknown field"}
		}
	}
	for _, f := range fields {
		v, ok := top[f.name]
		path := "/" + f.name
		if !ok {
			return nil, &envelope.SchemaError{Path: path, Message: f.name + " is required"}
		}
		if f.integer {
			var n json.Number
			if len(v) == 0 || v[0] == '"' || json.Unmarshal(v, &n) != nil {
				return nil, &envelope.SchemaError{Path: path, Message: f.name + " must be an integer"}
			}
			if _, err := n.Int64(); err != nil {
				return nil, &envelope.SchemaError{Path: path, Message: f.name + " must be an integer"}
			}
			continue
		}
		var str string
		if len(v) == 0 || v[0] != '"' || json.Unmarshal(v, &str) != nil {
			return nil, &envelope.SchemaError{Path: path, Message: f.name + " must be a string"}
		}
	}
	return top, nil
}

func (s *server) completeBlobUpload(w http.ResponseWriter, r *http.Request) {
	if !s.blobOriginAllowed(w, r) {
		return
	}
	if _, err := decodeBlobJSON(r, nil); err != nil {
		writeError(w, err)
		return
	}
	ref, err := s.Hub.CompleteBlobUpload(r.Context(), callerFrom(r.Context()), r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, ref)
}
func (s *server) downloadBlob(w http.ResponseWriter, r *http.Request) {
	if !s.blobOriginAllowed(w, r) {
		return
	}
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
	grant, err := s.Hub.BlobDownload(r.Context(), caller, r.URL.Query().Get("t"), r.PathValue("sha"), r.URL.Query().Get("blob_id"))
	if err != nil {
		writeError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, grant)
}
func (s *server) blobStatus(w http.ResponseWriter, r *http.Request) {
	if !s.blobOriginAllowed(w, r) {
		return
	}
	status, err := s.Hub.BlobStatus(r.Context(), callerFrom(r.Context()), r.PathValue("sha"), r.URL.Query().Get("blob_id"))
	if err != nil {
		writeError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, status)
}

// Direct capabilities are readable only by the hub origin in browser contexts.
// Native clients omit Origin; R2 CORS remains an independent provider gate.
func (s *server) blobOriginAllowed(w http.ResponseWriter, r *http.Request) bool {
	origins := r.Header.Values("Origin")
	if len(origins) == 0 {
		return true
	}
	u, err := url.Parse(s.Hub.Cfg.PublicURL)
	if err != nil || len(origins) != 1 || origins[0] != u.Scheme+"://"+u.Host {
		writeError(w, hub.E(403, "forbidden", "origin refused"))
		return false
	}
	return true
}
