package mcpserver

import (
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

const defaultAllowedOrigin = "https://chatgpt.com"

// exactOrigin accepts only an HTTP(S) origin, never a URL with a path,
// userinfo, query or fragment. Matching uses normOrigin's case/default-port
// normalization, but never its permissive extraction of an origin from a URL.
func exactOrigin(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || u.Path != "" ||
		u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.ContainsAny(raw, "#* \t\r\n") ||
		(u.Scheme != "http" && u.Scheme != "https") {
		return ""
	}
	host := u.Hostname()
	if host == "" || strings.HasSuffix(u.Host, ":") {
		return ""
	}
	if port := u.Port(); port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return ""
		}
	}
	if net.ParseIP(host) == nil {
		if strings.ContainsAny(u.Host, "[]") || strings.Contains(host, ":") {
			return ""
		}
		for _, label := range strings.Split(strings.TrimSuffix(host, "."), ".") {
			if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
				return ""
			}
			for _, c := range label {
				if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-') {
					return ""
				}
			}
		}
	}
	return normOrigin(raw)
}

// originOK runs before authentication. A nil allowlist uses the default;
// a non-nil empty list permits only the hub's public origin.
func (s *Server) originOK(r *http.Request) bool {
	vals := r.Header.Values("Origin")
	if len(vals) == 0 {
		return true
	}
	if len(vals) != 1 {
		return false
	}
	origin := exactOrigin(vals[0])
	if origin == "" {
		return false
	}
	if origin == normOrigin(s.PublicOrigin) {
		return true
	}
	if s.AllowedOrigins == nil {
		return origin == defaultAllowedOrigin
	}
	for _, allowed := range s.AllowedOrigins {
		if origin == allowed {
			return true
		}
	}
	return false
}

// ParseAllowedOrigins validates the comma-separated extra-origin setting.
// Empty selects the default; "none" disables extras without dropping the
// hub's own public origin. A bad entry rejects the whole configuration.
func ParseAllowedOrigins(raw string) ([]string, error) {
	if raw == "" {
		return nil, nil
	}
	if raw == "none" {
		return []string{}, nil
	}
	var origins []string
	for _, entry := range strings.Split(raw, ",") {
		origin := exactOrigin(strings.TrimSpace(entry))
		if origin == "" {
			return nil, fmt.Errorf("MCP allowed origins: entries must be scheme://host[:port] HTTP(S) origins")
		}
		origins = append(origins, origin)
	}
	return origins, nil
}
