package mcpserver

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
)

// Diagnostics deliberately collect a fixed allowlist, never a body, credential
// or session id. The process installs internal/logging's redacting handler.
// Unwrap preserves ResponseController's streaming and deadline support.
type diagResponse struct {
	http.ResponseWriter
	status int
}

func (w *diagResponse) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w *diagResponse) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
	w.ResponseWriter.WriteHeader(status)
}
func (w *diagResponse) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(b)
}

type diagKey struct{}
type requestDiag struct {
	principal                                        Principal
	initialize                                       bool
	clientName, clientVersion, requested, negotiated string
}

func beginDiag(w http.ResponseWriter, r *http.Request) (*diagResponse, *http.Request, *requestDiag) {
	d := &requestDiag{requested: r.Header.Get(hdrProtocolVersion)}
	return &diagResponse{ResponseWriter: w}, r.WithContext(context.WithValue(r.Context(), diagKey{}, d)), d
}

// noteInitialize uses only explicitly allowed initialize metadata. Malformed
// params are left to the existing handler; diagnostics never affect its reply.
func noteInitialize(r *http.Request, req *Request) {
	if req.Method != "initialize" {
		return
	}
	d, _ := r.Context().Value(diagKey{}).(*requestDiag)
	if d == nil {
		return
	}
	d.initialize = true
	var params struct {
		ProtocolVersion string `json:"protocolVersion"`
		ClientInfo      struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		} `json:"clientInfo"`
	}
	if json.Unmarshal(req.Params, &params) == nil {
		d.requested = params.ProtocolVersion
		d.clientName = params.ClientInfo.Name
		d.clientVersion = params.ClientInfo.Version
	}
}

func noteDiagProtocol(r *http.Request, version string) {
	if d, _ := r.Context().Value(diagKey{}).(*requestDiag); d != nil {
		d.negotiated = version
	}
}

func (d *requestDiag) log(s *Server, w *diagResponse, r *http.Request) {
	refused := w.status == http.StatusForbidden || w.status == http.StatusBadRequest ||
		w.status == http.StatusNotFound || w.status == http.StatusNotAcceptable
	if !d.initialize && !refused {
		return
	}
	// A successful initialize mints a legacy session. Do not retain its id.
	if d.initialize && w.status == http.StatusOK && w.Header().Get(hdrSessionID) != "" {
		d.negotiated = ProtoLegacy
	}
	kind := "unauthenticated"
	if d.principal.OAuth != nil {
		kind = "oauth"
	} else {
		switch d.principal.Caller.Kind {
		case "peer":
			kind = "peer"
		case "principal", "operator":
			kind = "principal"
		}
	}
	fields := []any{
		"client_name", diagTruncate(d.clientName, 64),
		"client_version", diagTruncate(d.clientVersion, 64),
		"requested_protocol_version", d.requested,
		"negotiated_protocol_version", d.negotiated,
		"origin_present", len(r.Header.Values("Origin")) != 0,
		"accept", diagTruncate(r.Header.Get("Accept"), 100),
		"session_present", len(r.Header.Values(hdrSessionID)) != 0,
		"caller_kind", kind,
	}
	if origin := normOrigin(r.Header.Get("Origin")); origin != "" && origin != normOrigin(s.PublicOrigin) {
		fields = append(fields, "origin", diagTruncate(origin, 100))
	}
	if kind != "unauthenticated" && d.principal.Caller.PeerID != "" {
		fields = append(fields, "peer_id", d.principal.Caller.PeerID)
	}
	if refused {
		slog.Warn("mcp: request refused", fields...)
	} else {
		slog.Info("mcp: initialize", fields...)
	}
}

func diagTruncate(s string, limit int) string {
	runes := []rune(s)
	if len(runes) > limit {
		runes = runes[:limit]
	}
	return string(runes)
}
