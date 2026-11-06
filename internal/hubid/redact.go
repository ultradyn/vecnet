package hubid

import (
	"net/url"
	"regexp"
	"strings"
)

// Endpoint is scheme://host for raw. Userinfo, path, query and fragment are
// omitted. An absolute URL that does not yield both a scheme and a host
// returns "" so a caller can drop it instead of printing the raw string.
func Endpoint(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return ""
	}
	return u.Scheme + "://" + u.Host
}

// embeddedURL matches an absolute URL inside a diagnostic. It stops at
// whitespace and the quotes Go uses when it formats url.Error.
var embeddedURL = regexp.MustCompile(`(?i)[a-z][a-z0-9+.-]*://[^\s"'<>\\]+`)

// RedactURLs replaces every absolute URL in s with Endpoint. A match that
// has no usable scheme and host becomes "hub", so userinfo, path and query
// cannot survive in a printable error, log line or pane.
func RedactURLs(s string) string {
	return embeddedURL.ReplaceAllStringFunc(s, func(m string) string {
		cut := len(m)
		for cut > 0 && strings.ContainsRune(".,);]", rune(m[cut-1])) {
			cut--
		}
		body, suffix := m[:cut], m[cut:]
		if where := Endpoint(body); where != "" {
			return where + suffix
		}
		return "hub" + suffix
	})
}
