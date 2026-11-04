package cli

import (
	"fmt"
	"net/netip"
	"net/url"
	"strings"
)

// insecureEnv is the escape hatch for plain http:// hubs off the LAN.
const insecureEnv = "VECNET_ALLOW_INSECURE_HTTP"

// checkHubURL refuses a plain http:// hub unless its host is loopback or on
// the LAN, or allow is set: a bearer sent over http to a public host crosses
// the internet in the clear, even when the far end redirects to https
// (PROTOCOL §20.4). LAN means a private, loopback, link-local or CGNAT
// address, "localhost", a single-label name ("xsm"), or a name under .local,
// .lan, .home.arpa or .internal. Names are not resolved.
func checkHubURL(raw string, allow bool) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return usageErr(fmt.Sprintf("hub %q is not a URL like https://hub.example", raw))
	}
	switch strings.ToLower(u.Scheme) {
	case "https":
		return nil
	case "http":
	default:
		return usageErr(fmt.Sprintf("hub %q: the scheme must be https (or http on the LAN)", raw))
	}
	if allow || lanHost(u.Hostname()) {
		return nil
	}
	return usageErr(fmt.Sprintf("hub %s is plain http on a public host, so the credential would cross the internet in the clear; "+
		"use https://%s, or pass --allow-insecure-http to enrol (or set %s=1) if you mean it", raw, u.Host, insecureEnv))
}

// cgnat is 100.64.0.0/10 (Tailscale and other overlays live here).
var cgnat = netip.MustParsePrefix("100.64.0.0/10")

func lanHost(host string) bool {
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	if a, err := netip.ParseAddr(host); err == nil {
		a = a.Unmap()
		return a.IsLoopback() || a.IsPrivate() || a.IsLinkLocalUnicast() || cgnat.Contains(a)
	}
	if host == "localhost" || !strings.Contains(host, ".") {
		return true
	}
	for _, suf := range []string{".localhost", ".local", ".lan", ".home.arpa", ".internal"} {
		if strings.HasSuffix(host, suf) {
			return true
		}
	}
	return false
}
