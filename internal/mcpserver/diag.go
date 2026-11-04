package mcpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"slices"
	"time"

	"github.com/XertroV/vecnet/internal/mcpdiag"
	"github.com/XertroV/vecnet/internal/webhook"
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
func (w *diagResponse) FlushError() error {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	return http.NewResponseController(w.ResponseWriter).Flush()
}
func (w *diagResponse) Flush() { _ = w.FlushError() }
func (w *diagResponse) WriteHeader(status int) {
	if w.status != 0 {
		return
	}
	w.status = status
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
	started                                          time.Time
	authOutcome, idType                              string
	initialize                                       bool
	clientName, clientVersion, requested, negotiated string
	method, toolName                                 string
	code                                             int
	toolError                                        bool
	subscribe                                        *subscribeDiag
}

type subscribeDiag struct {
	name, mode, host, verification, reason                     string
	argumentKeys                                               []string
	unknownArgumentKeys                                        int
	secretPresent, secretValidShape, cursorPresent, cursorNull bool
	ttlMs                                                      int64
}

// ObserveHTTP sits outside transport limits so even pre-dispatch /mcp
// refusals produce one completed record. ServeHTTP reuses this context.
func ObserveHTTP(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/mcp" {
			next.ServeHTTP(w, r)
			return
		}
		dw, r, d := beginDiag(w, r)
		defer d.log(nil, dw, r)
		next.ServeHTTP(dw, r)
	})
}

func beginDiag(w http.ResponseWriter, r *http.Request) (*diagResponse, *http.Request, *requestDiag) {
	d := &requestDiag{requested: r.Header.Get(hdrProtocolVersion), started: time.Now(), authOutcome: "none", idType: "absent"}
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
	d.idType = diagIDType(req.ID)
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
				Mode   string `json:"mode"`
				URL    string `json:"url"`
				Secret string `json:"secret"`
			} `json:"delivery"`
			TTLMS  int64           `json:"ttlMs"`
			Cursor json.RawMessage `json:"cursor"`
		}{}
		// Even partial/malformed params get a diagnostic without raw values.
		_ = json.Unmarshal(req.Params, &p)
		sub := &subscribeDiag{name: diagTruncate(p.Name, 128), mode: diagTruncate(p.Delivery.Mode, 32), verification: "not_attempted", argumentKeys: []string{}}
		if u, err := url.Parse(p.Delivery.URL); err == nil {
			sub.host = diagTruncate(u.Hostname(), 253)
		}
		sub.secretPresent = p.Delivery.Secret != ""
		_, secretErr := webhook.KeyBytes(p.Delivery.Secret)
		sub.secretValidShape = secretErr == nil
		sub.ttlMs = p.TTLMS
		sub.cursorPresent = len(p.Cursor) != 0
		sub.cursorNull = string(p.Cursor) == "null"
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

// noteDiagAuth records a fixed outcome only; callers must never pass an error.
func noteDiagAuth(r *http.Request, outcome string) {
	if d, _ := r.Context().Value(diagKey{}).(*requestDiag); d != nil {
		d.authOutcome = outcome
	}
}

func diagIDType(id json.RawMessage) string {
	if len(id) == 0 {
		return "absent"
	}
	if string(id) == "null" {
		return "null"
	}
	var v any
	if json.Unmarshal(id, &v) != nil {
		return "invalid"
	}
	switch v.(type) {
	case string:
		return "string"
	case float64:
		return "number"
	}
	return "invalid"
}

func (d *requestDiag) log(s *Server, w *diagResponse, r *http.Request) {
	status := w.status
	if status == 0 {
		status = http.StatusOK
	}
	if d.method == "" {
		d.method = "(unparsed)"
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
	outcome := d.authOutcome
	if kind != "unauthenticated" && outcome == "none" {
		outcome = "ok"
	}
	if status == http.StatusUnauthorized && outcome == "none" {
		if BearerToken(r) != "" {
			outcome = "invalid"
		}
	}
	result := "ok"
	if d.code != 0 || d.toolError || status >= 400 {
		result = "error"
	}
	if d.code == 0 && status >= 400 {
		d.code = status
	}
	fields := map[string]any{
		"http_method": r.Method, "path": "/mcp", "http_status": status, "duration_ms": time.Since(d.started).Milliseconds(),
		"protocol_version": r.Header.Get(hdrProtocolVersion), "mcp_method": r.Header.Get("Mcp-Method"), "mcp_name": r.Header.Get("Mcp-Name"),
		"method": d.method, "id_type": d.idType, "auth_outcome": outcome, "caller_kind": kind, "token_scopes": []string{},
		"origin": normOrigin(r.Header.Get("Origin")), "origin_present": len(r.Header.Values("Origin")) != 0,
		"user_agent": r.UserAgent(), "session_present": len(r.Header.Values(hdrSessionID)) != 0,
		"client_name": diagTruncate(d.clientName, 64), "client_version": diagTruncate(d.clientVersion, 64),
		"requested_protocol_version": d.requested, "negotiated_protocol_version": d.negotiated,
		"accept": diagTruncate(r.Header.Get("Accept"), 100), "result": result, "code": d.code,
	}
	if kind != "unauthenticated" {
		fields["peer_id"] = d.principal.Caller.PeerID
	}
	if d.principal.OAuth != nil {
		fields["token_scopes"] = d.principal.OAuth.Scopes
	}
	if d.method == "tools/call" {
		fields["tool_name"] = d.toolName
	}
	if sub := d.subscribe; sub != nil {
		fields["event_name"] = sub.name
		fields["argument_keys"] = sub.argumentKeys
		fields["unknown_argument_keys"] = sub.unknownArgumentKeys
		fields["delivery_mode"] = sub.mode
		fields["callback_host"] = sub.host
		fields["verification"] = sub.verification
		fields["reason"] = sub.reason
		fields["secret_present"] = sub.secretPresent
		fields["secret_valid_shape"] = sub.secretValidShape
		fields["ttl_ms"] = sub.ttlMs
		fields["cursor_present"] = sub.cursorPresent
		fields["cursor_null"] = sub.cursorNull
	}
	mcpdiag.Emit("mcp.request", fields)
}

func diagTruncate(s string, limit int) string {
	runes := []rune(s)
	if len(runes) > limit {
		runes = runes[:limit]
	}
	return string(runes)
}
