// Package oauth is the hub's minimal OAuth 2.1 authorization server and the
// resource server for /mcp, for dots (PROTOCOL §4.5): PKCE S256 only, public
// clients only, registered by CIMD or RFC 7591 DCR, and no machine-to-machine
// grant. A ChatGPT account becomes a vecnet principal bound to one dot peer by
// an operator-issued link code at first consent.
//
// Codes, tokens and link codes are stored only as SHA-256 hashes (§12); the
// plaintext is returned once.
package oauth

import (
	"encoding/json"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/XertroV/vecnet/internal/egress"
	"github.com/XertroV/vecnet/internal/hub"
	"github.com/XertroV/vecnet/internal/store"
)

// Token lifetimes [R15] and the other fixed durations of the flow.
const (
	DefaultAccessTTL  = time.Hour
	DefaultRefreshTTL = 30 * 24 * time.Hour

	codeTTL          = 10 * time.Minute    // an authorization code
	requestTTL       = 15 * time.Minute    // a consent form, so the operator has time to issue a code
	consentCookieTTL = 30 * 24 * time.Hour // the vecnet_oauth_sub cookie
	cimdCacheTTL     = time.Hour           // a fetched client metadata document
)

// Scope remains the base scope for vecnet tools. Event scopes are separate
// grants so a pre-Events token can authenticate and receive an explicit 403.
const (
	Scope               = "vecnet"
	ScopeEventsRead     = "events:read"
	ScopeEventsWrite    = "events:write"
	AuthorizationScopes = Scope + " " + ScopeEventsRead + " " + ScopeEventsWrite
)

func supportedScopes() []string { return []string{Scope, ScopeEventsRead, ScopeEventsWrite} }

// DefaultRedirects are the redirect URIs ChatGPT and Cursor use. An entry is
// matched exactly, except that a trailing "{callback_id}" matches one path
// segment and a trailing ":*" means any port and path on that scheme and host
// (redirectAllowed).
var DefaultRedirects = []string{
	"https://chatgpt.com/connector_platform_oauth_redirect",
	"https://chatgpt.com/connector/oauth/{callback_id}",
	// Cursor Web and Agents: https://cursor.com/docs/mcp#static-redirect-url
	"https://www.cursor.com/agents/mcp/oauth/callback",
}

// Server is the authorization server. Hub supplies the link codes, the
// principal binding and the sealing key.
type Server struct {
	Hub              *hub.Hub
	DB               *store.DB
	Issuer           string // public URL, no trailing slash
	Now              func() time.Time
	AccessTTL        time.Duration // 1 h
	RefreshTTL       time.Duration // 30 d
	AllowedRedirects []string      // see DefaultRedirects
	Egress           egress.Policy // CIMD fetches

	cimd cimdGate
}

// New returns a Server for h with the production defaults.
func New(h *hub.Hub) *Server {
	return &Server{
		Hub:              h,
		DB:               h.DB,
		Issuer:           strings.TrimRight(h.Cfg.PublicURL, "/"),
		Now:              h.Now,
		AccessTTL:        DefaultAccessTTL,
		RefreshTTL:       DefaultRefreshTTL,
		AllowedRedirects: slices.Clone(DefaultRedirects),
		Egress:           h.EgressPolicy(),
	}
}

func (s *Server) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// resource is the only resource tokens are issued for.
func (s *Server) resource() string { return s.Issuer + "/mcp" }

// Mount registers the OAuth endpoints. /oauth/* and /.well-known/oauth-* are
// public paths: a client reaches them before it has a token.
func (s *Server) Mount(mux *http.ServeMux) {
	mux.HandleFunc("GET /.well-known/oauth-authorization-server", s.diagnose("oauth.metadata", s.authServerMetadata))
	mux.HandleFunc("GET /.well-known/oauth-protected-resource", s.diagnose("oauth.metadata", s.resourceMetadata))
	mux.HandleFunc("GET /.well-known/oauth-protected-resource/mcp", s.diagnose("oauth.metadata", s.resourceMetadata)) // RFC 9728 §3.1
	mux.HandleFunc("POST /oauth/register", s.diagnose("oauth.register", s.register))
	mux.HandleFunc("GET /oauth/authorize", s.diagnose("oauth.authorize", s.getAuthorize))
	mux.HandleFunc("POST /oauth/authorize", s.diagnose("oauth.consent", s.postAuthorize))
	mux.HandleFunc("GET /oauth/consent.css", s.diagnose("oauth.metadata", s.consentCSS))
	mux.HandleFunc("POST /oauth/token", s.diagnose("oauth.token", s.token))
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeOAuthError writes the RFC 6749 §5.2 error body.
func writeOAuthError(w http.ResponseWriter, status int, code, description string) {
	oauthReason(w, code)
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, status, map[string]string{"error": code, "error_description": description})
}

// authServerMetadata is RFC 8414.
func (s *Server) authServerMetadata(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"issuer":                                         s.Issuer,
		"authorization_endpoint":                         s.Issuer + "/oauth/authorize",
		"token_endpoint":                                 s.Issuer + "/oauth/token",
		"registration_endpoint":                          s.Issuer + "/oauth/register",
		"response_types_supported":                       []string{"code"},
		"grant_types_supported":                          []string{"authorization_code", "refresh_token"},
		"code_challenge_methods_supported":               []string{"S256"},
		"token_endpoint_auth_methods_supported":          []string{"none"},
		"scopes_supported":                               supportedScopes(),
		"client_id_metadata_document_supported":          true,
		"authorization_response_iss_parameter_supported": true,
	})
}

// resourceMetadata is RFC 9728.
func (s *Server) resourceMetadata(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"resource":                 s.resource(),
		"authorization_servers":    []string{s.Issuer},
		"scopes_supported":         supportedScopes(),
		"bearer_methods_supported": []string{"header"},
	})
}

// repeated reports whether any parameter appears twice: RFC 6749 §3.1 and
// §3.2 forbid it, and the two spellings could be read differently.
func repeated(v url.Values) bool {
	for _, vals := range v {
		if len(vals) > 1 {
			return true
		}
	}
	return false
}

// scopeOK reports whether all requested scope names are supported.
func scopeOK(scope string) bool {
	for _, f := range strings.Fields(scope) {
		if !slices.Contains(supportedScopes(), f) {
			return false
		}
	}
	return true
}
