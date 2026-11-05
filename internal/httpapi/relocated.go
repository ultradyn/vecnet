package httpapi

import (
	"fmt"
	"html"
	"net/http"
	"strings"

	"github.com/XertroV/vecnet/internal/envelope"
)

// Relocated serves only health, identity, notices and an inert moved page.
// Route before authenticating: even missing or revoked credentials, oversized
// writes, MCP and OAuth requests must learn that this frozen hub has moved.
// The surviving API endpoints retain the normal middleware and auth policy.
func Relocated(d Deps, n envelope.HubNotice) http.Handler {
	s := &server{Deps: d}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	})
	mux.HandleFunc("GET /v1/hub/identity", s.getHubIdentity)
	mux.HandleFunc("GET /v1/hub/notices", s.getHubNotices)
	api := throttle(d.Limiter, d.TrustFlyClientIP, limitBody(authenticate(d.Hub.Auth, d.Hub.TouchReachability, d.Limiter, d.TrustFlyClientIP, mux)))
	var handler http.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet || r.Method == http.MethodHead {
			switch r.URL.Path {
			case "/healthz", "/v1/hub/identity", "/v1/hub/notices":
				api.ServeHTTP(w, r)
				return
			}
			if r.URL.Path == "/ui" || strings.HasPrefix(r.URL.Path, "/ui/") {
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				w.Header().Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
				w.Header().Set("X-Content-Type-Options", "nosniff")
				w.WriteHeader(http.StatusGone)
				target := html.EscapeString(n.NewURL)
				fmt.Fprintf(w, `<!doctype html><html lang="en"><head><meta charset="utf-8"><title>Hub moved</title></head><body><h1>This hub moved</h1><p>This hub moved to <a href="%s">%s</a>.</p></body></html>`, target, target)
				return
			}
		}
		writeErr(w, http.StatusGone, "hub_moved", "this hub moved to "+n.NewURL, map[string]any{"new_url": n.NewURL, "notice": n})
	})
	if strings.HasPrefix(d.Hub.Cfg.PublicURL, "https://") {
		handler = hsts(handler)
	}
	return handler
}
