package oauth

import (
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"slices"
	"strings"
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

// logRedirectRefused deliberately drops query, fragment and userinfo. A URL
// parse error is never logged: net/url errors can contain the complete input.
func logRedirectRefused(uri, clientName, endpoint string) {
	uri, _, _ = strings.Cut(uri, "#")
	uri, _, _ = strings.Cut(uri, "?")
	var scheme, host, path string
	if u, err := url.Parse(uri); err == nil {
		scheme, host, path = u.Scheme, u.Host, u.EscapedPath()
	}
	name := []rune(clientName)
	if len(name) > 80 {
		name = name[:80]
	}
	slog.Info("oauth: redirect refused", "scheme", scheme, "host", host, "path", path,
		"client_name", string(name), "endpoint", endpoint)
}
