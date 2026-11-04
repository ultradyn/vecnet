package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/XertroV/vecnet/internal/auth"
	"github.com/XertroV/vecnet/internal/hub"
)

// MaxBodyBytes is the request body cap (PROTOCOL §2.2, plan Global Constraints).
const MaxBodyBytes = 256 << 10

type ctxKey struct{}

// callerFrom returns the authenticated Caller the auth middleware stored; the
// zero Caller on a public path.
func callerFrom(ctx context.Context) auth.Caller {
	c, _ := ctx.Value(ctxKey{}).(auth.Caller)
	return c
}

func withCaller(ctx context.Context, c auth.Caller) context.Context {
	return context.WithValue(ctx, ctxKey{}, c)
}

// limitBody rejects a declared Content-Length over MaxBodyBytes with 413
// payload_too_large before anything else runs, and caps every other body with
// http.MaxBytesReader (handlers surface the overflow through writeError).
// Blob uploads are exempt: the blob store enforces their own 25 MiB cap.
// Avatar uploads are capped at 2 MiB instead (isAvatarUpload).
func limitBody(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isBlobUpload(r) {
			next.ServeHTTP(w, r)
			return
		}
		limit := int64(MaxBodyBytes)
		if isAvatarUpload(r) {
			limit = avatarLimit // an avatar image may be up to 2 MiB (PROTOCOL §8.1)
		}
		if r.ContentLength > limit {
			writeError(w, &http.MaxBytesError{Limit: limit})
			return
		}
		if r.Body != nil {
			r.Body = http.MaxBytesReader(w, r.Body, limit)
		}
		next.ServeHTTP(w, r)
	})
}

// isPublic reports the paths that skip bearer auth: /healthz, /v1/enrol,
// /v1/ingress/*, /v1/blobs/{sha} carrying ?t=, /ui/*, and the OAuth endpoints
// (/oauth/*, /.well-known/oauth-*), which a client reaches before it has a
// token. /mcp also skips it because mcpserver authenticates every request
// itself (bearer or OAuth, with its own 401 WWW-Authenticate).
func isPublic(r *http.Request) bool {
	p := r.URL.Path
	switch {
	case p == "/healthz", p == "/v1/enrol", p == "/mcp":
		return true
	case strings.HasPrefix(p, "/v1/ingress/"):
		return true
	case strings.HasPrefix(p, "/oauth/"), strings.HasPrefix(p, "/.well-known/oauth-"):
		return true
	case p == "/ui" || strings.HasPrefix(p, "/ui/"):
		return true
	case strings.HasPrefix(p, "/v1/blobs/") && r.URL.Query().Has("t"):
		return true
	}
	return false
}

// isOperatorOnly reports the admin paths (PROTOCOL §2.12).
func isOperatorOnly(p string) bool {
	return p == "/v1/admin" || strings.HasPrefix(p, "/v1/admin/")
}

// bearer extracts the secret from "Authorization: Bearer <secret>".
func bearer(r *http.Request) string {
	h := r.Header.Get("Authorization")
	scheme, tok, ok := strings.Cut(h, " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return ""
	}
	return strings.TrimSpace(tok)
}

// authenticate resolves the bearer for every non-public path, stores the
// Caller in the request context and enforces operator-only admin paths
// (403 forbidden). A disabled peer's credential is 401 everywhere except
// GET /v1/events, which answers 204. Every authenticated call by a
// credential with its own peer is a reachability observation: touch(peer,
// "reachable") sets last_seen (PROTOCOL §8); touch may be nil. A principal
// acting for a peer with "as" is observed by the hub once "as" is
// authorised (Send, SetPresence).
func authenticate(res *auth.Resolver, touch func(ctx context.Context, peerID, value string), next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isPublic(r) {
			next.ServeHTTP(w, r)
			return
		}
		c, err := res.Resolve(r.Context(), bearer(r))
		if errors.Is(err, auth.ErrPeerDisabled) && r.Method == http.MethodGet && r.URL.Path == "/v1/events" {
			w.WriteHeader(http.StatusNoContent) // peer disabled: stop reconnecting (PROTOCOL §2.3)
			return
		}
		if err != nil {
			writeError(w, err)
			return
		}
		if isOperatorOnly(r.URL.Path) && c.Kind != "operator" {
			writeError(w, hub.E(http.StatusForbidden, "forbidden", "operator credential required"))
			return
		}
		if touch != nil && c.PeerID != "" {
			touch(context.WithoutCancel(r.Context()), c.PeerID, "reachable")
		}
		next.ServeHTTP(w, r.WithContext(withCaller(r.Context(), c)))
	})
}
