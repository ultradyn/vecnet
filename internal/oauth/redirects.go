package oauth

import (
	"fmt"
	"net"
	"net/url"
	"slices"
	"strings"

	"github.com/XertroV/vecnet/internal/mcpdiag"
)

// ParseAllowedRedirects appends comma-separated operator patterns to the
// defaults. Errors identify the entry, never its potentially sensitive URL.
func ParseAllowedRedirects(raw string) ([]string, error) {
	allowed := slices.Clone(DefaultRedirects)
	if strings.TrimSpace(raw) == "" {
		return allowed, nil
	}
	for i, entry := range strings.Split(raw, ",") {
		entry = strings.TrimSpace(entry)
		if !validRedirectPattern(entry) {
			return nil, fmt.Errorf("OAuth redirects: entry %d must be an HTTPS URL (HTTP only on loopback), with no userinfo, fragment or host wildcard; only trailing /{callback_id} or host:* patterns are supported", i+1)
		}
		allowed = append(allowed, entry)
	}
	return allowed, nil
}

func validRedirectPattern(entry string) bool {
	base, anyPort := strings.CutSuffix(entry, ":*")
	callback := strings.HasSuffix(base, "/{callback_id}")
	if callback {
		base = strings.TrimSuffix(base, "{callback_id}") + "callback-id"
	}
	if strings.ContainsAny(base, "*{}#") {
		return false
	}
	u, err := url.Parse(base)
	if err != nil || u.Opaque != "" || u.User != nil || u.Hostname() == "" {
		return false
	}
	loopback := strings.EqualFold(u.Hostname(), "localhost") || net.ParseIP(u.Hostname()).IsLoopback()
	if u.Scheme != "https" && !(u.Scheme == "http" && loopback) {
		return false
	}
	if callback && (u.RawQuery != "" || u.ForceQuery) {
		return false
	}
	if anyPort {
		return u.Port() == "" && !strings.HasSuffix(u.Host, ":") && u.Path == "" && u.RawQuery == "" && !u.ForceQuery
	}
	return !strings.HasSuffix(u.Host, ":") && (u.Port() == "" || validPort(u.Port()))
}

// logRedirectRefused shares the ring's sanitization boundary with INFO logs.
// The raw URI is only an input; the diagnostic retains safe components.
func logRedirectRefused(uri, clientName, endpoint string) {
	mcpdiag.Emit("oauth.redirect_refused", map[string]any{"redirect_uri": uri, "client_name": clientName, "endpoint": endpoint})
}
