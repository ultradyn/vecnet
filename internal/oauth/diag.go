package oauth

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/XertroV/vecnet/internal/mcpdiag"
)

type oauthDiagKey struct{}

type oauthRequestDiag struct {
	started      time.Time
	status       int
	kind, reason string
	scopes       []string
}

// oauthResponse retains operational facts only. It never captures a response
// body, query, form value, Location header, cookie or credential. Each wrapper
// has its own header guard while nested wrappers annotate one shared record.
type oauthResponse struct {
	http.ResponseWriter
	diag        *oauthRequestDiag
	wroteHeader bool
}

func (w *oauthResponse) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w *oauthResponse) WriteHeader(status int) {
	if w.wroteHeader {
		return
	}
	w.wroteHeader = true
	w.diag.status = status
	w.ResponseWriter.WriteHeader(status)
}

func (w *oauthResponse) Write(body []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(body)
}

func oauthReason(w http.ResponseWriter, reason string) {
	if d, ok := w.(*oauthResponse); ok {
		d.diag.reason = reason
	}
}

func oauthKind(w http.ResponseWriter, kind string) {
	if d, ok := w.(*oauthResponse); ok {
		d.diag.kind = kind
	}
}

func oauthScopes(w http.ResponseWriter, scopes []string) {
	if d, ok := w.(*oauthResponse); ok {
		d.diag.scopes = scopes
	}
}

// ObserveHTTP belongs outside body limits, authentication and throttling so
// public OAuth requests and operator link creation have exactly one completed
// record, even when middleware rejects them before an endpoint is reached.
// It does not parse rejected request bodies or trust query/form credentials.
func ObserveHTTP(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		kind := oauthHTTPKind(r)
		if kind == "" {
			next.ServeHTTP(w, r)
			return
		}
		observeOAuth(w, r, kind, next.ServeHTTP)
	})
}

func oauthHTTPKind(r *http.Request) string {
	switch r.URL.Path {
	case "/.well-known/oauth-authorization-server", "/.well-known/oauth-protected-resource", "/.well-known/oauth-protected-resource/mcp", "/oauth/consent.css":
		return "oauth.metadata"
	case "/oauth/register":
		return "oauth.register"
	case "/oauth/token":
		return "oauth.token"
	case "/oauth/authorize":
		if r.Method == http.MethodPost {
			return "oauth.consent"
		}
		return "oauth.authorize"
	case "/oauth/consent":
		return "oauth.consent"
	case "/oauth/link", "/v1/admin/dot-links":
		return "oauth.link"
	}
	if strings.HasPrefix(r.URL.Path, "/oauth/") || strings.HasPrefix(r.URL.Path, "/.well-known/oauth-") {
		return "oauth.request"
	}
	return ""
}

func observeOAuth(w http.ResponseWriter, r *http.Request, kind string, next http.HandlerFunc) {
	d, _ := r.Context().Value(oauthDiagKey{}).(*oauthRequestDiag)
	if d != nil {
		d.kind = kind
		next(&oauthResponse{ResponseWriter: w, diag: d}, r)
		return
	}
	d = &oauthRequestDiag{started: time.Now(), kind: kind}
	r = r.WithContext(context.WithValue(r.Context(), oauthDiagKey{}, d))
	defer d.log(r)
	next(&oauthResponse{ResponseWriter: w, diag: d}, r)
}

func (d *oauthRequestDiag) log(r *http.Request) {
	if d.status == 0 {
		d.status = http.StatusOK
	}
	if d.reason == "" {
		switch d.status {
		case http.StatusUnauthorized:
			d.reason = "unauthorized"
		case http.StatusForbidden:
			d.reason = "forbidden"
		case http.StatusNotFound:
			d.reason = "not_found"
		case http.StatusMethodNotAllowed:
			d.reason = "method_not_allowed"
		case http.StatusRequestEntityTooLarge:
			d.reason = "payload_too_large"
		case http.StatusTooManyRequests:
			d.reason = "rate_limited"
		default:
			if d.status >= 500 {
				d.reason = "server_error"
			} else if d.status >= 400 {
				d.reason = "invalid_request"
			} else if d.kind == "oauth.link" && r.Method == http.MethodPost {
				d.reason = "linked"
			} else {
				d.reason = "ok"
			}
		}
	}
	outcome := "success"
	if d.status >= 500 || d.reason == "server_error" {
		outcome = "failed"
	} else if d.status >= 400 || d.reason == "access_denied" || d.reason == "invalid_scope" || d.reason == "invalid_target" || d.reason == "invalid_request" || d.reason == "unsupported_response_type" {
		outcome = "rejected"
	}
	mcpdiag.Emit(d.kind, map[string]any{"http_method": r.Method, "path": r.URL.Path, "http_status": d.status, "duration_ms": time.Since(d.started).Milliseconds(), "outcome": outcome, "reason": d.reason, "token_scopes": d.scopes})
}

func (s *Server) diagnose(kind string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) { observeOAuth(w, r, kind, next) }
}
