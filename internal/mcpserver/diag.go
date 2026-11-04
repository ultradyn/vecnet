package mcpserver

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
)

// Diagnostics deliberately collect a fixed allowlist, never a body, credential
// or session id. The process installs internal/logging's redacting handler.
// Unwrap preserves ResponseController's streaming and deadline support.
type diagResponse struct {
	http.ResponseWriter
	status int
	diag   *requestDiag
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
	method, toolName                                 string
	code                                             int
	toolError                                        bool
	subscribe                                        *subscribeDiag
}

type subscribeDiag struct {
	name, mode, host, verification, reason string
	argumentKeys                           []string
	unknownArgumentKeys                    int
}

func beginDiag(w http.ResponseWriter, r *http.Request) (*diagResponse, *http.Request, *requestDiag) {
	d := &requestDiag{requested: r.Header.Get(hdrProtocolVersion)}
	return &diagResponse{ResponseWriter: w, diag: d}, r.WithContext(context.WithValue(r.Context(), diagKey{}, d)), d
}

// noteRequest extracts metadata only. Argument values and callback paths never
// enter diagnostics. Unknown argument names are counted rather than printed:
// a malformed request could put message content or a credential in a key.
func noteRequest(r *http.Request, req *Request) {
	d, _ := r.Context().Value(diagKey{}).(*requestDiag)
	if d == nil {
		return
	}
	d.method = req.Method
	noteInitialize(r, req)
	if req.Method == "tools/call" {
		var p struct {
			Name string `json:"name"`
		}
		if json.Unmarshal(req.Params, &p) == nil {
			d.toolName = diagTruncate(p.Name, 128)
		}
	}
	if req.Method == "events/subscribe" {
		p := struct {
			Name      string                     `json:"name"`
			Arguments map[string]json.RawMessage `json:"arguments"`
			Delivery  struct {
				Mode string `json:"mode"`
				URL  string `json:"url"`
			} `json:"delivery"`
		}{}
		// Even partial/malformed params get a diagnostic without raw values.
		_ = json.Unmarshal(req.Params, &p)
		sub := &subscribeDiag{name: diagTruncate(p.Name, 128), mode: diagTruncate(p.Delivery.Mode, 32), verification: "not_attempted", argumentKeys: []string{}}
		if u, err := url.Parse(p.Delivery.URL); err == nil {
			sub.host = diagTruncate(u.Hostname(), 253)
		}
		for k := range p.Arguments {
			if k == "peer_id" || k == "thread_id" || k == "types" {
				sub.argumentKeys = append(sub.argumentKeys, k)
			} else {
				sub.unknownArgumentKeys++
			}
		}
		slices.Sort(sub.argumentKeys)
		d.subscribe = sub
	}
}

func noteEventVerification(ctx context.Context, result, reason string) {
	if d, _ := ctx.Value(diagKey{}).(*requestDiag); d != nil && d.subscribe != nil {
		d.subscribe.verification, d.subscribe.reason = result, reason
	}
}

func noteDiagSession(r *http.Request, sess *Session) {
	if d, _ := r.Context().Value(diagKey{}).(*requestDiag); d != nil {
		d.negotiated = sess.Proto
		d.clientName, d.clientVersion = sess.clientName, sess.clientVersion
	}
}

func (d *requestDiag) rpcError(e *RPCError) {
	d.code = e.Code
	if d.subscribe != nil && d.subscribe.reason == "" {
		if data, ok := e.Data.(map[string]any); ok {
			if reason, ok := data["reason"].(string); ok {
				d.subscribe.reason = reason
			}
		}
		if d.subscribe.reason == "" {
			d.subscribe.reason = "invalid_params"
			if e.Code == ErrInternal {
				d.subscribe.reason = "internal_error"
			}
		}
	}
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
	origin := normOrigin(r.Header.Get("Origin"))
	foreignAllowed := origin != "" && origin != normOrigin(s.PublicOrigin) && s.originOK(r)
	methodLog := d.method != "" && d.method != "ping"
	// Never read a rejected request's body solely for diagnostics: even a
	// bounded unauthenticated read can delay an authentication/Origin refusal.
	// Still expose every transport failure, with an explicit unknown method.
	if d.method == "" && w.status >= 400 {
		d.method, methodLog = "(unparsed)", true
	}
	if !methodLog && !d.initialize && !refused && !foreignAllowed {
		return
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
	if origin != "" && origin != normOrigin(s.PublicOrigin) {
		fields = append(fields, "origin", diagTruncate(origin, 100))
	}
	if kind != "unauthenticated" && (methodLog || d.principal.Caller.PeerID != "") {
		fields = append(fields, "peer_id", d.principal.Caller.PeerID)
	}
	if methodLog {
		result := "ok"
		if d.code != 0 || d.toolError || w.status >= 400 {
			result = "error"
		}
		if d.code == 0 && w.status >= 400 {
			d.code = w.status
		}
		fields = append(fields, "method", diagTruncate(d.method, 128), "result", result, "code", d.code)
		if d.method == "(unparsed)" {
			fields = append(fields, "http_method", r.Method)
		}
		if w.status >= 400 {
			fields = append(fields, "http_status", w.status)
		}
		if d.method == "tools/call" {
			fields = append(fields, "tool_name", d.toolName)
		}
		if sub := d.subscribe; sub != nil {
			fields = append(fields, "event_name", sub.name, "argument_keys", sub.argumentKeys, "unknown_argument_keys", sub.unknownArgumentKeys,
				"delivery_mode", sub.mode, "callback_host", sub.host, "verification", sub.verification, "reason", sub.reason)
		}
		slog.Info("mcp: request", fields...)
	} else if refused {
		slog.Warn("mcp: request refused", fields...)
	} else if !d.initialize && foreignAllowed {
		slog.Info("mcp: allowed foreign origin", fields...)
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
